#!/usr/bin/env python3
"""mlx-router — OpenAI-compatible proxy that fronts a swap backend plus static backends.

Listens on one or more ports (--port plus --extra-port). Routes incoming
OpenAI requests to the right backend by inspecting the `model` field:

  - Static backends (--static name=url): always-on, fixed model. Forwarded
    directly, no swap. Their model ids appear in the router's aggregated
    GET /v1/models response.
  - Swap backend (--backend, default 127.0.0.1:1234): single mlx_lm.server
    process that hot-swaps between --profiles via `mlx chat restart`.

GET /v1/models is intercepted and returns the union of static-advertised
ids and configured profile names so clients (SillyTavern, LM Studio, etc.)
see the full catalog.

SSE streams pass through chunk-by-chunk without buffering.

Configuration is via env vars or CLI flags:
  ROUTER_HOST              default 127.0.0.1
  ROUTER_PORT              default 1230
  ROUTER_EXTRA_PORTS       comma-separated extra ports (e.g. "8080")
  BACKEND_URL              default http://127.0.0.1:1234 (the swap backend)
  ROUTER_PROFILES          comma-separated profile names (e.g. "anubis,skyfall,valkyrie")
  ROUTER_STATICS           comma-separated name=url pairs for always-on backends
                           (e.g. "qwen-tags=http://127.0.0.1:1235,nomic=http://127.0.0.1:1236")
  ROUTER_SWITCH_TIMEOUT    seconds to wait for backend after a swap (default 90)
  MLX_SERVERS_CMD          path to mlx script (default ~/scripts/mlx)
"""
from __future__ import annotations

import argparse
import asyncio
import json
import logging
import os
import shlex
import signal
import sys
import time
from urllib.parse import urlparse

from aiohttp import ClientSession, ClientTimeout, web

LOG = logging.getLogger("mlx-router")

STATIC_PROBE_TTL = 30.0  # seconds between background re-probes of statics


async def detect_listening_model(url: str) -> str | None:
    """For a local backend, lsof the port and parse --model from the
    listening process's cmdline. Returns None for remote URLs or if no
    match is found. This is the only reliable way to know what an
    mlx_lm.server / mlx-embed-server is actually serving — /v1/models
    on those returns the HF cache, not the loaded model."""
    parsed = urlparse(url)
    if parsed.hostname not in ("127.0.0.1", "localhost", "::1"):
        return None
    port = parsed.port
    if port is None:
        return None
    try:
        proc = await asyncio.create_subprocess_exec(
            "lsof", f"-iTCP:{port}", "-sTCP:LISTEN", "-Fp", "-P", "-n",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.DEVNULL,
        )
        out, _ = await proc.communicate()
        pids = [int(line[1:]) for line in out.decode().splitlines()
                if line.startswith("p")]
        if not pids:
            return None
        proc = await asyncio.create_subprocess_exec(
            "ps", "-p", str(pids[0]), "-o", "command=",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.DEVNULL,
        )
        out, _ = await proc.communicate()
        cmdline = out.decode().strip()
        try:
            toks = shlex.split(cmdline)
        except ValueError:
            toks = cmdline.split()
        for i, t in enumerate(toks):
            if t == "--model" and i + 1 < len(toks):
                return toks[i + 1]
            if t.startswith("--model="):
                return t.split("=", 1)[1]
        return None
    except Exception as e:
        LOG.debug("listening-model detect for %s failed: %s", url, e)
        return None


class Static:
    """A pinned backend that always serves one model.

    `name` is the user-supplied alias clients can use in `model` (e.g.
    "qwen-tags"). The router advertises it in /v1/models.

    `upstream_model` is what we send to the backend in `model`. For local
    mlx_lm.server / mlx-embed-server processes we discover it by lsof'ing
    the port and parsing --model from the cmdline. Can also be set
    explicitly via the optional third field in --static.
    """

    def __init__(self, name: str, url: str, upstream_model: str | None = None):
        self.name = name
        self.url = url.rstrip("/")
        self.upstream_model: str | None = upstream_model
        self.last_probe: float = 0.0
        self.healthy: bool = False

    async def probe(self, session: ClientSession) -> None:
        # If the user pinned an upstream model explicitly, just confirm
        # the port is up.
        if self.upstream_model:
            self.healthy = await self._port_up(session)
            self.last_probe = time.monotonic()
            return

        detected = await detect_listening_model(self.url)
        if detected:
            self.upstream_model = detected
            self.healthy = True
            self.last_probe = time.monotonic()
            return

        # Last resort: ask the backend for its /v1/models and take the
        # first id. Works for mlx_lm.server fronting an HF-id model, but
        # not when the loaded model is a local path it doesn't list.
        try:
            async with session.get(f"{self.url}/v1/models",
                                   timeout=ClientTimeout(total=2)) as r:
                if r.status == 200:
                    data = await r.json()
                    ids = [m.get("id") for m in (data.get("data") or [])
                           if m.get("id")]
                    matched = [m for m in ids
                               if self.name.lower() in m.lower()]
                    chosen = matched[0] if matched else (ids[0] if ids else None)
                    if chosen:
                        self.upstream_model = chosen
                        self.healthy = True
                        self.last_probe = time.monotonic()
                        return
        except Exception as e:
            LOG.debug("static %s /v1/models probe failed: %s", self.name, e)

        self.healthy = False
        self.last_probe = time.monotonic()

    async def _port_up(self, session: ClientSession) -> bool:
        """Cheap reachability check: any HTTP response (even 404) means up."""
        for path in ("/v1/models", "/"):
            try:
                async with session.get(f"{self.url}{path}",
                                       timeout=ClientTimeout(total=1.5)) as r:
                    if r.status < 500:
                        return True
            except Exception:
                continue
        return False

    def matches(self, requested: str) -> str | None:
        """Return the canonical upstream model id, or None."""
        if not requested or not self.upstream_model:
            return None
        if requested == self.name:
            return self.upstream_model
        u = self.upstream_model
        if requested == u or requested in u or u in requested:
            return u
        return None


class Router:
    def __init__(self, backend: str, profiles: set[str], statics: list[Static],
                 mlx_servers_cmd: str, switch_timeout: float):
        self.backend = backend.rstrip("/")
        self.profiles = profiles
        self.statics = statics
        self.mlx_servers_cmd = mlx_servers_cmd
        self.switch_timeout = switch_timeout
        self.lock = asyncio.Lock()
        self.current_model: str | None = None
        self.session: ClientSession | None = None

    async def on_startup(self, _app):
        self.session = ClientSession(timeout=ClientTimeout(total=None))
        await self._refresh_current_model()
        for s in self.statics:
            await s.probe(self.session)
        LOG.info("router up; backend=%s loaded=%s profiles=%s statics=%s",
                 self.backend, self.current_model, sorted(self.profiles),
                 [(s.name, s.url, s.healthy, s.upstream_model)
                  for s in self.statics])

    async def on_cleanup(self, _app):
        if self.session is not None:
            await self.session.close()

    async def _refresh_current_model(self) -> None:
        # Prefer process-cmdline inspection. /v1/models reports the HF
        # cache, not the loaded model, so items[0] can be wildly wrong.
        detected = await detect_listening_model(self.backend)
        if detected:
            self.current_model = detected
            return
        try:
            async with self.session.get(f"{self.backend}/v1/models",
                                        timeout=ClientTimeout(total=5)) as r:
                if r.status != 200:
                    return
                data = await r.json()
                items = data.get("data") or []
                if items:
                    self.current_model = items[0].get("id")
        except Exception as e:
            LOG.debug("backend not reachable for model refresh: %s", e)

    async def _refresh_stale_statics(self) -> None:
        now = time.monotonic()
        for s in self.statics:
            if now - s.last_probe > STATIC_PROBE_TTL:
                await s.probe(self.session)

    def _profile_matches_loaded(self, requested: str) -> bool:
        if not self.current_model:
            return False
        r = requested.lower()
        c = self.current_model.lower()
        return r == c or r in c or c in r

    def _find_static(self, requested: str) -> tuple[Static, str] | None:
        for s in self.statics:
            mid = s.matches(requested)
            if mid is not None:
                return s, mid
        return None

    async def ensure_model(self, name: str) -> None:
        if name not in self.profiles:
            return
        async with self.lock:
            if self._profile_matches_loaded(name):
                return
            t0 = time.monotonic()
            LOG.info("swap requested: %s -> %s", self.current_model, name)
            proc = await asyncio.create_subprocess_exec(
                self.mlx_servers_cmd, "restart", "chat", "--chat-model", name,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            _, stderr_b = await proc.communicate()
            if proc.returncode != 0:
                LOG.error("mlx restart failed (rc=%d): %s",
                          proc.returncode, stderr_b.decode(errors="replace"))
                raise web.HTTPServiceUnavailable(
                    reason=f"backend swap to {name!r} failed",
                )

            deadline = time.monotonic() + self.switch_timeout
            while time.monotonic() < deadline:
                detected = await detect_listening_model(self.backend)
                if detected and name.lower() in detected.lower():
                    self.current_model = detected
                    # Confirm the backend is actually serving requests, not
                    # just listening — mlx_lm.server can be mid-load.
                    try:
                        async with self.session.get(
                                f"{self.backend}/v1/models",
                                timeout=ClientTimeout(total=2)) as r:
                            if r.status == 200:
                                LOG.info("swap done: %s ready in %.1fs",
                                         name, time.monotonic() - t0)
                                return
                    except Exception:
                        pass
                await asyncio.sleep(0.5)
            raise web.HTTPGatewayTimeout(
                reason=f"backend did not return with {name!r} in {self.switch_timeout:.0f}s",
            )

    async def list_models(self, request: web.Request) -> web.Response:
        t0 = time.monotonic()
        await self._refresh_stale_statics()
        now = int(time.time())
        data: list[dict] = []
        seen: set[str] = set()

        for s in self.statics:
            if not s.healthy:
                continue
            if s.name not in seen:
                seen.add(s.name)
                data.append({"id": s.name, "object": "model", "created": now})

        for p in sorted(self.profiles):
            if p in seen:
                continue
            seen.add(p)
            data.append({"id": p, "object": "model", "created": now})

        if self.current_model:
            cm = self.current_model.lower()
            already_listed = any(s.lower() in cm or cm in s.lower()
                                 for s in seen)
            if not already_listed:
                data.append({"id": self.current_model, "object": "model",
                             "created": now})

        LOG.info("%s %s -> router [aggregate] 200 %d models in %.2fs",
                 request.method, request.path, len(data),
                 time.monotonic() - t0)
        return web.json_response({"object": "list", "data": data})

    async def proxy(self, request: web.Request) -> web.StreamResponse:
        t0 = time.monotonic()
        body = b""
        if request.method in ("POST", "PUT", "PATCH") and request.body_exists:
            body = await request.read()

        target_base = self.backend
        route_reason = "swap-backend"

        if request.method == "POST" and request.path.startswith("/v1/") and body:
            try:
                payload = json.loads(body)
                if LOG.isEnabledFor(logging.DEBUG):
                    relevant = {k: payload.get(k) for k in
                                ("model", "temperature", "top_p", "top_k",
                                 "min_p", "repetition_penalty",
                                 "repetition_context_size", "max_tokens",
                                 "frequency_penalty", "presence_penalty",
                                 "stream")}
                    LOG.debug("request fields: %s", json.dumps(relevant))
                requested = payload.get("model")
                if isinstance(requested, str) and requested:
                    static_hit = self._find_static(requested)
                    if static_hit is not None:
                        s, mid = static_hit
                        target_base = s.url
                        route_reason = f"static:{s.name}"
                        if requested != mid:
                            payload["model"] = mid
                            body = json.dumps(payload).encode()
                    else:
                        await self.ensure_model(requested)
                        if (requested in self.profiles
                                and self.current_model
                                and self.current_model != requested):
                            payload["model"] = self.current_model
                            body = json.dumps(payload).encode()
            except web.HTTPException:
                raise
            except json.JSONDecodeError:
                pass
            except Exception:
                LOG.exception("model peek failed; passing request through")

        target = f"{target_base}{request.rel_url}"
        skip = {"host", "content-length", "connection",
                "transfer-encoding", "te", "upgrade",
                "proxy-authorization", "proxy-authenticate"}
        out_headers = {k: v for k, v in request.headers.items()
                       if k.lower() not in skip}

        try:
            upstream = await self.session.request(
                request.method, target,
                data=body if body else None,
                headers=out_headers,
                timeout=ClientTimeout(total=None),
                allow_redirects=False,
            )
        except Exception as e:
            LOG.error("backend %s request failed: %s", target_base, e)
            raise web.HTTPBadGateway(reason=str(e))

        try:
            response_headers = {k: v for k, v in upstream.headers.items()
                                if k.lower() not in skip}
            response = web.StreamResponse(
                status=upstream.status,
                reason=upstream.reason,
                headers=response_headers,
            )
            await response.prepare(request)

            async for chunk in upstream.content.iter_any():
                await response.write(chunk)
            await response.write_eof()
            LOG.info("%s %s -> %s [%s] %d in %.2fs",
                     request.method, request.path, target_base,
                     route_reason, upstream.status, time.monotonic() - t0)
            return response
        finally:
            upstream.release()

    async def health(self, _request: web.Request) -> web.Response:
        await self._refresh_current_model()
        await self._refresh_stale_statics()
        return web.json_response({
            "ok": True,
            "backend": self.backend,
            "loaded_model": self.current_model,
            "profiles": sorted(self.profiles),
            "statics": [
                {"name": s.name, "url": s.url, "healthy": s.healthy,
                 "upstream_model": s.upstream_model}
                for s in self.statics
            ],
        })


async def _serve(app: web.Application, host: str, ports: list[int]) -> None:
    runner = web.AppRunner(app, access_log=None)
    await runner.setup()
    for port in ports:
        site = web.TCPSite(runner, host, port)
        await site.start()
        LOG.info("listening on %s:%d", host, port)

    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            loop.add_signal_handler(sig, stop.set)
        except NotImplementedError:
            pass
    try:
        await stop.wait()
    finally:
        await runner.cleanup()


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--host", default=os.environ.get("ROUTER_HOST", "127.0.0.1"))
    p.add_argument("--port", type=int,
                   default=int(os.environ.get("ROUTER_PORT", "1230")))
    p.add_argument("--extra-port", action="append", type=int, default=[],
                   help="additional port to bind (repeatable)")
    p.add_argument("--backend",
                   default=os.environ.get("BACKEND_URL", "http://127.0.0.1:1234"))
    p.add_argument("--profiles",
                   default=os.environ.get("ROUTER_PROFILES", ""),
                   help="comma-separated profile names (e.g. anubis,skyfall,valkyrie). "
                        "Only requests for these trigger a swap on --backend.")
    p.add_argument("--static", action="append", default=[],
                   help="static backend, repeatable. Form: name=url[|upstream_id]. "
                        "If upstream_id is omitted, the router lsofs the port and "
                        "parses --model from the process cmdline. "
                        "(e.g. qwen-tags=http://127.0.0.1:1235)")
    p.add_argument("--switch-timeout", type=float,
                   default=float(os.environ.get("ROUTER_SWITCH_TIMEOUT", "90")))
    p.add_argument("--mlx-cmd",
                   default=os.environ.get(
                       "MLX_SERVERS_CMD",
                       os.path.expanduser("~/scripts/mlx")))
    p.add_argument("--log-level",
                   default=os.environ.get("ROUTER_LOG_LEVEL", "INFO"))
    args = p.parse_args()

    logging.basicConfig(
        level=getattr(logging, args.log_level.upper(), logging.INFO),
        format="%(asctime)s [%(name)s] %(levelname)s %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    )

    profiles = {s.strip() for s in args.profiles.split(",") if s.strip()}
    if not profiles:
        LOG.warning("no profiles configured (--profiles or $ROUTER_PROFILES); "
                    "router will pass through to the swap backend without swapping")

    static_specs: list[str] = list(args.static)
    env_statics = os.environ.get("ROUTER_STATICS", "")
    if env_statics:
        static_specs.extend(s for s in env_statics.split(",") if s.strip())
    statics: list[Static] = []
    for spec in static_specs:
        if "=" not in spec:
            raise SystemExit(f"--static expects name=url[|upstream_id], got: {spec!r}")
        name, rest = spec.split("=", 1)
        if "|" in rest:
            url, upstream = rest.split("|", 1)
            statics.append(Static(name.strip(), url.strip(), upstream.strip()))
        else:
            statics.append(Static(name.strip(), rest.strip()))

    ports = [args.port] + list(args.extra_port)
    env_extra = os.environ.get("ROUTER_EXTRA_PORTS", "")
    if env_extra:
        ports.extend(int(s.strip()) for s in env_extra.split(",") if s.strip())
    seen: set[int] = set()
    ports = [p for p in ports if not (p in seen or seen.add(p))]

    router = Router(
        backend=args.backend,
        profiles=profiles,
        statics=statics,
        mlx_servers_cmd=args.mlx_cmd,
        switch_timeout=args.switch_timeout,
    )

    app = web.Application(client_max_size=64 * 1024 * 1024)
    app.on_startup.append(router.on_startup)
    app.on_cleanup.append(router.on_cleanup)
    app.router.add_get("/router/health", router.health)
    app.router.add_get("/v1/models", router.list_models)
    app.router.add_route("*", "/{tail:.*}", router.proxy)

    asyncio.run(_serve(app, args.host, ports))


if __name__ == "__main__":
    sys.exit(main())
