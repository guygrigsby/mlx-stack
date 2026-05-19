"""Universal MLX worker entry point.

Sets MLX_DISABLE_COMPILE=1 before any mlx import, applies engine-specific
patches, starts memory threads, dispatches to the engine's server.main().

Usage:
    python -m mlx_stack.launcher_shim --engine lm --model PATH --port PORT [--draft-model PATH] [--host HOST]
"""
from __future__ import annotations

import argparse
import os
import sys

# CRITICAL: must run before any module imports mlx.core.
os.environ.setdefault("MLX_DISABLE_COMPILE", "1")


def parse_args(argv=None) -> argparse.Namespace:
    p = argparse.ArgumentParser(prog="mlx_stack.launcher_shim")
    p.add_argument("--engine", required=True, choices=["lm", "vlm"])
    p.add_argument("--model", required=True)
    p.add_argument("--draft-model", default="", dest="draft_model")
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--port", required=True, type=int)
    return p.parse_args(argv)


def build_server_argv(args) -> list[str]:
    argv = [
        "mlx_lm.server" if args.engine == "lm" else "mlx_vlm.server",
        "--model", args.model,
        "--host", args.host,
        "--port", str(args.port),
    ]
    if args.draft_model:
        argv += ["--draft-model", args.draft_model]
    return argv


def _env_int(name: str, default: int = 0) -> int:
    try:
        return int(os.environ.get(name, default))
    except ValueError:
        return default


def _env_float(name: str, default: float = 0.0) -> float:
    try:
        return float(os.environ.get(name, default))
    except ValueError:
        return default


def main(argv=None) -> int:
    args = parse_args(argv)

    if args.engine == "lm":
        from mlx_stack.patches import xtc, timing
        xtc.apply()
        timing.apply()

    import mlx.core as mx

    stops = []

    cache_limit = _env_int("MLX_CACHE_LIMIT_BYTES")
    if cache_limit:
        from mlx_stack.memory import janitor
        stops.append(janitor.start(
            mx=mx,
            limit_bytes=cache_limit,
            clear_interval_sec=_env_float("MLX_CACHE_CLEAR_INTERVAL_SEC", 60.0),
            clear_threshold_bytes=_env_int("MLX_CACHE_CLEAR_THRESHOLD_BYTES", cache_limit // 2),
        ))

    memlog_interval = _env_float("MLX_MEMLOG_INTERVAL_SEC", 0.0)
    if memlog_interval > 0:
        from mlx_stack.memory import memlog
        stops.append(memlog.start(mx=mx, interval_sec=memlog_interval))

    kv_headroom = _env_int("MLX_KV_HEADROOM_BYTES")
    if kv_headroom > 0:
        from mlx_stack.memory import watchdog
        stops.append(watchdog.start(
            mx=mx,
            kv_headroom_bytes=kv_headroom,
            check_interval_sec=_env_float("MLX_ACTIVE_MEMORY_CHECK_INTERVAL_SEC", 30.0),
            grace_sec=_env_float("MLX_ACTIVE_MEMORY_GRACE_SEC", 90.0),
        ))

    print(f"[mlx-launch] starting engine={args.engine} model={args.model} port={args.port}", file=sys.stderr, flush=True)

    if args.engine == "lm":
        from mlx_lm.server import main as server_main
    else:
        from mlx_vlm.server import main as server_main

    sys.argv = build_server_argv(args)
    try:
        server_main()
    finally:
        for stop in stops:
            try:
                stop()
            except Exception:
                pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
