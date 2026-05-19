import threading
import time
import types

import pytest


def test_janitor_clears_when_threshold_exceeded(monkeypatch):
    fake_mx = types.SimpleNamespace(
        get_cache_memory=lambda: 2_000_000_000,
        clear_cache=lambda: cleared.append(time.time()),
        set_cache_limit=lambda n: limits.append(n),
    )
    cleared, limits = [], []

    from mlx_stack.memory import janitor

    stop = janitor.start(
        mx=fake_mx,
        limit_bytes=2_147_483_648,
        clear_interval_sec=0.05,
        clear_threshold_bytes=1_073_741_824,
    )
    try:
        time.sleep(0.2)
    finally:
        stop()

    assert limits == [2_147_483_648]
    assert len(cleared) >= 2


def test_janitor_skips_clear_below_threshold(monkeypatch):
    fake_mx = types.SimpleNamespace(
        get_cache_memory=lambda: 100,
        clear_cache=lambda: cleared.append(1),
        set_cache_limit=lambda n: None,
    )
    cleared = []

    from mlx_stack.memory import janitor

    stop = janitor.start(
        mx=fake_mx,
        limit_bytes=2_147_483_648,
        clear_interval_sec=0.05,
        clear_threshold_bytes=1_000,
    )
    try:
        time.sleep(0.2)
    finally:
        stop()

    assert cleared == []


def test_janitor_stop_is_clean(monkeypatch):
    fake_mx = types.SimpleNamespace(
        get_cache_memory=lambda: 0,
        clear_cache=lambda: None,
        set_cache_limit=lambda n: None,
    )

    from mlx_stack.memory import janitor

    stop = janitor.start(
        mx=fake_mx,
        limit_bytes=1024,
        clear_interval_sec=0.05,
        clear_threshold_bytes=1024,
    )
    t0 = time.time()
    stop()
    assert time.time() - t0 < 0.5
