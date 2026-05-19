import sys
import time
import types

import pytest


@pytest.fixture
def fake_response_generator(monkeypatch):
    """Mimic mlx_lm.server's real (request, generation_args, progress_callback) shape."""
    fake_module = types.ModuleType("mlx_lm.server")

    class FakeTokenizer:
        def encode(self, s):
            return [0] * len(s or "")  # token-per-char approximation

    class FakeRequest:
        def __init__(self, prompt="hello"):
            self.prompt = prompt

    class ResponseGenerator:
        def __init__(self):
            self.tokenizer = FakeTokenizer()
            self.calls = 0

        def generate(self, request, generation_args, progress_callback=None):
            self.calls += 1
            time.sleep(0.002)
            n = getattr(generation_args, "completion_tokens", 3)

            def gen():
                for i in range(n):
                    yield {"token": i}
                    time.sleep(0.0005)

            ctx = types.SimpleNamespace(model="fake")
            return ctx, gen()

    fake_module.ResponseGenerator = ResponseGenerator
    fake_module.CompletionRequest = FakeRequest
    monkeypatch.setitem(sys.modules, "mlx_lm", types.ModuleType("mlx_lm"))
    monkeypatch.setitem(sys.modules, "mlx_lm.server", fake_module)
    return fake_module


def test_timing_emits_one_line_per_request(fake_response_generator, capsys):
    from mlx_stack.patches import timing

    timing.apply()

    rg = fake_response_generator.ResponseGenerator()
    req = fake_response_generator.CompletionRequest(prompt="hello there")
    args = types.SimpleNamespace(completion_tokens=5)
    ctx, gen = rg.generate(req, args)
    list(gen)

    captured = capsys.readouterr()
    assert "[mlx-launch] req=" in captured.err
    assert "prompt=11t" in captured.err  # len("hello there") via fake tokenizer
    assert "prefill=" in captured.err
    assert "decode=" in captured.err


def test_timing_is_idempotent(fake_response_generator, capsys):
    from mlx_stack.patches import timing

    timing.apply()
    timing.apply()  # must not stack wrappers

    rg = fake_response_generator.ResponseGenerator()
    req = fake_response_generator.CompletionRequest(prompt="x")
    args = types.SimpleNamespace(completion_tokens=1)
    _, gen = rg.generate(req, args)
    list(gen)

    captured = capsys.readouterr()
    assert captured.err.count("[mlx-launch] req=") == 1


def test_timing_handles_zero_completion(fake_response_generator, capsys):
    from mlx_stack.patches import timing

    timing.apply()

    rg = fake_response_generator.ResponseGenerator()
    req = fake_response_generator.CompletionRequest(prompt="x")
    args = types.SimpleNamespace(completion_tokens=0)
    _, gen = rg.generate(req, args)
    list(gen)

    captured = capsys.readouterr()
    assert "decode=0" in captured.err or "decode=0.0" in captured.err


def test_timing_returns_tuple_passthrough(fake_response_generator):
    """If original returns non-tuple, patch passes it through silently."""
    from mlx_stack.patches import timing

    timing.apply()

    rg = fake_response_generator.ResponseGenerator()

    def odd_generate(self, request, generation_args, progress_callback=None):
        return "not-a-tuple"

    # Temporarily monkey-patch to return non-tuple. Use bound replacement.
    import mlx_lm.server as srv
    orig = srv.ResponseGenerator.generate
    try:
        srv.ResponseGenerator.generate = odd_generate
        result = rg.generate(None, None)
        assert result == "not-a-tuple"
    finally:
        srv.ResponseGenerator.generate = orig
