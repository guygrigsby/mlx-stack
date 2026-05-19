import io
import sys
import time
import types


def test_memlog_emits_snapshot(capsys):
    fake_mx = types.SimpleNamespace(
        get_active_memory=lambda: 1234,
        get_cache_memory=lambda: 5678,
        get_peak_memory=lambda: 9999,
    )

    from mlx_stack.memory import memlog

    stop = memlog.start(mx=fake_mx, interval_sec=0.05)
    try:
        time.sleep(0.18)
    finally:
        stop()

    err = capsys.readouterr().err
    assert "[mlx-launch] mem: active=1234 cache=5678 peak=9999" in err
    assert err.count("[mlx-launch] mem: ") >= 2


def test_memlog_stop_is_clean():
    fake_mx = types.SimpleNamespace(
        get_active_memory=lambda: 0,
        get_cache_memory=lambda: 0,
        get_peak_memory=lambda: 0,
    )

    from mlx_stack.memory import memlog

    stop = memlog.start(mx=fake_mx, interval_sec=0.05)
    t0 = time.time()
    stop()
    assert time.time() - t0 < 0.5
