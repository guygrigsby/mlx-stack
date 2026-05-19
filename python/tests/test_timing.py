import io
import sys
import time
import types

import pytest


@pytest.fixture
def fake_response_generator(monkeypatch):
    fake_module = types.ModuleType("mlx_lm.server")

    class ResponseGenerator:
        def __init__(self):
            self.calls = 0

        def generate(self, prompt_tokens, completion_tokens, request_id="r"):
            self.calls += 1
            time.sleep(0.002)
            for i in range(completion_tokens):
                yield {"token": i}
                time.sleep(0.0005)

    fake_module.ResponseGenerator = ResponseGenerator
    monkeypatch.setitem(sys.modules, "mlx_lm", types.ModuleType("mlx_lm"))
    monkeypatch.setitem(sys.modules, "mlx_lm.server", fake_module)
    return fake_module


def test_timing_emits_one_line_per_request(fake_response_generator, capsys):
    from mlx_stack.patches import timing

    timing.apply()

    rg = fake_response_generator.ResponseGenerator()
    list(rg.generate(prompt_tokens=10, completion_tokens=5, request_id="req-1"))

    captured = capsys.readouterr()
    assert "[mlx-launch] req=req-1" in captured.err
    assert "prompt=10t" in captured.err
    assert "prefill=" in captured.err
    assert "decode=" in captured.err


def test_timing_is_idempotent(fake_response_generator, capsys):
    from mlx_stack.patches import timing

    timing.apply()
    timing.apply()  # second apply must not stack wrappers

    rg = fake_response_generator.ResponseGenerator()
    list(rg.generate(prompt_tokens=1, completion_tokens=1, request_id="r"))

    captured = capsys.readouterr()
    assert captured.err.count("[mlx-launch] req=") == 1


def test_timing_handles_zero_completion(fake_response_generator, capsys):
    from mlx_stack.patches import timing

    timing.apply()

    rg = fake_response_generator.ResponseGenerator()
    list(rg.generate(prompt_tokens=5, completion_tokens=0, request_id="zero"))

    captured = capsys.readouterr()
    assert "decode=0" in captured.err or "decode=0.0" in captured.err
