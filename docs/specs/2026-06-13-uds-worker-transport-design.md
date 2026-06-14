# UDS worker transport (hybrid)

Status: proposed
Date: 2026-06-13

## Problem

Each internal MLX worker (`mlx_lm.server`, `mlx_vlm.server`, `mlx_audio.server`,
embed) binds a stable TCP port on `127.0.0.1`. That port is auto-allocated,
**persisted in config TOML**, and surfaced in `mlxctl add` output, `mlxctl
list`/status, `/v1/status` JSON, and the `[mlx-launch] ... port=N` log line.

The router is the only client of these workers and it routes to them itself, so
the per-worker port is pure surface area: something to allocate, avoid colliding,
store, print, and reason about. The goal is to remove the port as a managed,
visible concept for internal backends.

Loopback binding already makes workers unreachable off-box; this work is about
the management/visibility surface and, where cheap, the port itself.

## Decision summary

Internal backends stop having a stable, surfaced port. Transport is selected
purely by engine — **no user-facing knob**:

| Engine                 | Transport            | Mechanism                                            |
| ---------------------- | -------------------- | --------------------------------------------------- |
| `vlm`, `audio`, `embed`| Unix domain socket   | shim passes `uds=<path>` to `uvicorn.run`           |
| `lm`                   | ephemeral loopback   | daemon picks `127.0.0.1:0` at spawn, port in memory |
| `external`             | unchanged (TCP `url`)| untouched                                           |

The router keeps its own TCP listener — that is the public surface and is
unaffected. Only the router↔worker hop changes.

### Why hybrid, not uniform UDS

`mlx_lm.server` builds its server in a private function `_run_http_server`,
which calls `socket.getaddrinfo(host, port)` and overrides `address_family`
from the result. There is no public host→socket seam, so true UDS for mlx_lm
would require replacing that private function — a runtime monkeypatch of vendored
internals that a `pip install --upgrade mlx-lm` could silently break.

The three uvicorn engines expose `uds=` on `uvicorn.run`, which is stable,
documented, public uvicorn API.

Chosen tradeoff: true UDS where the seam is public and stable (uvicorn engines);
ephemeral-hidden loopback for mlx_lm. This **eliminates all engine-internals
patching**. mlx_lm gets a daemon-allocated ephemeral port via the existing
`allocatePort` trick (`net.Listen("tcp", "127.0.0.1:0")` → read assigned port →
close → pass via `--port`), held only in daemon memory. The port still exists
but is random per boot, loopback-only, and never persisted, printed, or fixed.

Net runtime-patch fragility: one monkeypatch of `uvicorn.run`, asserting only
that `uds=` is accepted. No real-venv shape-assertion gate test needed.

## Components changed

### Python shim (`python/mlx_stack/launcher_shim.py`)
- Monkeypatch `uvicorn.run` so that when `MLX_UDS=<path>` is set in the
  environment, the call is rewritten to pass `uds=path` and drop `host`/`port`.
  Covers `vlm` and `audio` (whose vendored `main()` calls `uvicorn.run` with
  host/port and we do not control that call).
- embed (our code) takes a `uds` parameter directly — no reliance on the patch.
- mlx_lm path unchanged: still `--host 127.0.0.1 --port N`.

### Embed server (`python/mlx_stack/embed_server/app.py`)
- `main(host, port, model_path)` gains `uds: str | None = None`; when set, call
  `uvicorn.run(app, uds=uds, ...)` instead of host/port.

### Daemon (`cmd/mlxd/live.go`, `cmd/mlxd/main.go`)
- `transportFor(engine)`:
  - `lm` → allocate ephemeral loopback at spawn, pass `--host 127.0.0.1 --port N`.
  - `vlm`/`audio`/`embed` → derive socket path
    `~/.local/state/mlxd/<name>.sock`, pass `--uds <path>` (and `MLX_UDS` env
    for the uvicorn patch), no `--port`.
- Ephemeral port and socket path live on the in-memory backend, never in config.

### Socket lifecycle
- Daemon ensures `~/.local/state/mlxd/` exists at mode **0700** — owner-only on
  the directory gates every worker socket regardless of individual socket perms.
- Unlink stale `<name>.sock` before spawn and on worker exit.
- Mirrors the existing pattern in `internal/admin/server.go` (which manages
  `admin.sock` with mkdir / unlink-stale / cleanup-on-exit).

### Backend interface (`internal/supervisor`)
- Add `RoundTripper() http.RoundTripper` (or an equivalent `*http.Client`) to
  the Backend interface.
  - UDS backends return a `unix`-dialing transport mirroring
    `internal/ipc/client.go`; `BaseURL()` returns `http://unix`.
  - Loopback backends return the default transport and `http://127.0.0.1:N`.
- `External` returns the default transport and its configured `url`.

### Router (`internal/router/proxy.go`, health probes in `group.go`/`persistent.go`)
- Stop using `http.DefaultTransport.RoundTrip`. Use the resolved backend's
  transport for both proxying and the `/v1/models` health probes.

### Config schema (`internal/config/schema.go`)
- Internal backends no longer require or use `Port`. Relax persistent-mode
  validation that currently requires `Port > 0`.
- `url` / `upstream_model` stay for `external`.
- Migration: existing `port=` lines on internal backends become ignored no-ops.
  Document in release notes; no rewrite of user config required.

### Exposure (`cmd/mlxctl/render.go`, `internal/admin/handlers.go`, `cmd/mlxctl/add.go`)
- Internal backends no longer print an upstream URL/port. Show name + state +
  transport kind (`uds` / `local`).
- `mlxctl add` drops the `port=N` note for internal backends.
- Router's own port/URL still shown — public surface.

### Fake worker (`testdata/fakemlx`)
- Gains `--uds <path>`: bind a `unix` listener instead of `host:port` (the Go
  equivalent of the uvicorn `uds=` path). `--port` and `--uds` are mutually
  exclusive. This is what lets the `e2e` suite drive a real worker over a real
  unix socket through the real router stack without an MLX model. The fake
  worker is the regression harness for the transport, so it must mirror both
  transports the daemon can select.

## Data flow (shape unchanged)

client → router TCP listener → `Resolve(model)` → `EnsureLoaded` → `ProxyJSON`
over the backend's transport (unix socket or loopback) → worker.

## Error handling

- Socket dir creation failure or stale-socket unlink failure → worker fails to
  start with a specific error; daemon surfaces it like any spawn failure.
- uvicorn patch: if `MLX_UDS` is set but `uvicorn.run` does not accept `uds`
  (engine no longer uses uvicorn), fail loud at apply with a specific message
  rather than silently falling back to a TCP port.
- AF_UNIX path length: `~/.local/state/mlxd/<name>.sock` is well under the
  macOS `sun_path` limit (~104) for realistic backend names; if a name would
  exceed it, fail at config/spawn validation with a clear message.

## Testing (TDD, failing-first)

This is a big, cross-cutting change to the router↔worker path. The regression
guard is layered: unit tests pin each seam, **integration tests exercise the
real transport end-to-end through a real `mlxd`**, and a gated suite proves it
against real models. The integration layer is the one that catches regressions,
so it is the priority and runs in the default gate.

### Unit (fast, `go test ./...` / `pytest`)
- Go: `transportFor(engine)` mapping table test (lm → loopback, uvicorn engines
  → uds).
- Go: `ProxyJSON` against a real `net.Listen("unix")` upstream served by
  `http.Server.Serve` — proves the router's per-backend transport actually
  dials a unix socket and proxies/rewrites correctly.
- Go: ephemeral loopback port is never written back to config (config
  round-trip assertion).
- Go: `render` / `handlers` omit internal upstream ports/URLs; still show the
  router port.
- Python: spy on `uvicorn.run` (monkeypatched) — when `MLX_UDS` set, assert
  `uds=` passed and `host`/`port` dropped; when unset, host/port preserved.

### Integration — real mlxd + fake worker (default gate, `e2e/`)
Built on the existing `e2e` harness (real `mlxd` + `fakemlx`, skipped only under
`-short`, already part of `go test ./...`). Each test boots a real daemon from a
real config and drives requests through the real router. New cases:
- **UDS path, per uvicorn engine** (`vlm`, `audio`, `embed`): config declares the
  backend with no port; daemon derives `~/.local/state/mlxd/<name>.sock`
  (redirected to the test temp dir), spawns `fakemlx --uds <path>`; a
  `/v1/chat/completions` (and `/v1/embeddings`, `/v1/audio/speech`) request
  round-trips 200. Assert the socket file exists and is the dial target.
- **Loopback-ephemeral path** (`lm`): config declares the backend with **no
  `port`**; request round-trips; assert no stable port was persisted or
  surfaced.
- **Hot-swap over UDS**: extend the existing `TestE2E_HotSwap` shape to a swap
  group on the UDS transport, proving load/unload/reuse works over a unix
  socket.
- **Regression invariant** — `/v1/status` JSON and `mlxctl list` output contain
  **no upstream port/URL for internal backends** (only the router port). This is
  the direct guard on the "obscure the ports" goal: any future change that
  reintroduces port surfacing fails here.

### Python integration — real uvicorn over UDS (default gate, `python/tests`)
- Launch the embed shim (`--engine embed --uds <tmp.sock>`) against a trivial
  app, connect a real HTTP client over the unix socket, assert a 200. Proves the
  one remaining runtime patch (`uvicorn.run(uds=...)`) binds a real socket
  against the **installed** uvicorn — not a mock. Doubles as the guard on the
  "installed uvicorn accepts `uds`" assumption.

### Gated — real models, real transport (manual / `gputex`, not default gate)
- `//go:build integration` (or `MLX_E2E_REAL=1`) suite: one small real model per
  engine, started by a real `mlxd`, round-tripped over the actual UDS / loopback
  transport, asserting 200 and no surfaced port. Metal-bound and mlxd owns the
  GPU, so it is excluded from `make test` and documented to run via
  `gputex run "uds-e2e" -- go test -tags integration ./e2e/...`. This is the
  "real path" confirmation required before claiming the change works.

## Out of scope

- External backends (stay TCP via `url`).
- Router's own listener (stays TCP — public surface).
- Any change to the wire protocol or request/response handling.
- Configurable socket paths or a TCP escape hatch for internal backends (the
  whole point is to remove the knob).
