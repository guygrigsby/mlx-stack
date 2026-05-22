"""KV-headroom watchdog: execv self when active memory grows beyond baseline + headroom."""
from __future__ import annotations

import os
import sys
import threading
import time
from typing import Callable, Sequence


def start(
    *,
    mx,
    kv_headroom_bytes: int,
    check_interval_sec: float,
    grace_sec: float,
    restart_argv: Sequence[str] | None = None,
) -> Callable[[], None]:
    # Capture argv now, before the host overwrites sys.argv (launcher_shim
    # rewrites it to mlx_lm.server's CLI before dispatching). Without this,
    # execv would re-launch the wrong program and the process would die.
    argv = list(restart_argv) if restart_argv is not None else [sys.executable, *sys.argv]

    stop_event = threading.Event()

    def loop():
        t_start = time.monotonic()
        baseline = None
        while not stop_event.is_set():
            now = time.monotonic()
            if baseline is None:
                if now - t_start >= grace_sec:
                    baseline = mx.get_active_memory()
                    trigger = baseline + kv_headroom_bytes
                    print(
                        f"[mlx-launch] WATCHDOG: armed. baseline={baseline} trigger={trigger}",
                        file=sys.stderr,
                        flush=True,
                    )
            else:
                active = mx.get_active_memory()
                trigger = baseline + kv_headroom_bytes
                if active > trigger:
                    print(
                        f"[mlx-launch] WATCHDOG: active={active} > trigger={trigger} — execv-restarting argv={argv}",
                        file=sys.stderr,
                        flush=True,
                    )
                    os.execv(argv[0], argv)
                    return
            if stop_event.wait(check_interval_sec):
                return

    thread = threading.Thread(target=loop, daemon=True, name="mlx-watchdog")
    thread.start()

    def stop():
        stop_event.set()
        thread.join(timeout=1.0)

    return stop
