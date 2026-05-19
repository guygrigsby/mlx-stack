"""Periodic mx.clear_cache when cache memory exceeds a threshold."""
from __future__ import annotations

import threading
from typing import Callable


def start(*, mx, limit_bytes: int, clear_interval_sec: float, clear_threshold_bytes: int) -> Callable[[], None]:
    """Configure cache limit and start the janitor thread. Returns a stop function."""
    if limit_bytes:
        mx.set_cache_limit(limit_bytes)

    stop_event = threading.Event()

    def loop():
        while not stop_event.wait(clear_interval_sec):
            try:
                if mx.get_cache_memory() > clear_threshold_bytes:
                    mx.clear_cache()
            except Exception:
                pass

    thread = threading.Thread(target=loop, daemon=True, name="mlx-cache-janitor")
    thread.start()

    def stop():
        stop_event.set()
        thread.join(timeout=1.0)

    return stop
