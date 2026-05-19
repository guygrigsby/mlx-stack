import sys
import time
import types

import pytest


def test_watchdog_logs_baseline_after_grace(monkeypatch, capsys):
    fake_mx = types.SimpleNamespace(get_active_memory=lambda: 1_000_000_000)
    execv_calls = []
    monkeypatch.setattr("os.execv", lambda *a: execv_calls.append(a))

    from mlx_stack.memory import watchdog

    stop = watchdog.start(
        mx=fake_mx,
        kv_headroom_bytes=8_000_000_000,
        check_interval_sec=0.05,
        grace_sec=0.1,
    )
    try:
        time.sleep(0.25)
    finally:
        stop()

    err = capsys.readouterr().err
    assert "WATCHDOG: armed" in err
    assert "baseline=1000000000" in err
    assert execv_calls == []


def test_watchdog_triggers_execv_when_above_headroom(monkeypatch, capsys):
    samples = iter([1_000_000_000, 1_000_000_000, 9_500_000_000, 9_500_000_000])
    fake_mx = types.SimpleNamespace(get_active_memory=lambda: next(samples))

    execv_calls = []
    monkeypatch.setattr("os.execv", lambda exe, argv: execv_calls.append((exe, list(argv))))

    from mlx_stack.memory import watchdog

    stop = watchdog.start(
        mx=fake_mx,
        kv_headroom_bytes=8_000_000_000,
        check_interval_sec=0.05,
        grace_sec=0.05,
    )
    try:
        time.sleep(0.5)
    finally:
        stop()

    err = capsys.readouterr().err
    assert "WATCHDOG: active=" in err
    assert "execv-restarting" in err
    assert len(execv_calls) == 1
    assert execv_calls[0][0] == sys.executable


def test_watchdog_stop_is_clean(monkeypatch):
    fake_mx = types.SimpleNamespace(get_active_memory=lambda: 0)
    monkeypatch.setattr("os.execv", lambda *a: None)

    from mlx_stack.memory import watchdog

    stop = watchdog.start(
        mx=fake_mx,
        kv_headroom_bytes=1,
        check_interval_sec=0.05,
        grace_sec=10.0,
    )
    t0 = time.time()
    stop()
    assert time.time() - t0 < 0.5
