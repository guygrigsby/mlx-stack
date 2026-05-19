"""Periodic memory snapshot lines on stderr."""
from __future__ import annotations

import sys
import threading
from typing import Callable


def start(*, mx, interval_sec: float) -> Callable[[], None]:
    stop_event = threading.Event()

    def loop():
        while not stop_event.wait(interval_sec):
            try:
                a = mx.get_active_memory()
                c = mx.get_cache_memory()
                p = mx.get_peak_memory()
                print(f"[mlx-launch] mem: active={a} cache={c} peak={p}", file=sys.stderr, flush=True)
            except Exception:
                pass

    thread = threading.Thread(target=loop, daemon=True, name="mlx-memlog")
    thread.start()

    def stop():
        stop_event.set()
        thread.join(timeout=1.0)

    return stop
