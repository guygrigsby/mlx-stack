"""Per-request timing wrap for mlx_lm.server.ResponseGenerator.generate."""
from __future__ import annotations

import sys
import time
import functools

_WRAPPED_MARKER = "__timing_wrapped__"


def _fmt(ms: float, tokens: int) -> str:
    tps = (tokens / (ms / 1000.0)) if ms > 0 else 0.0
    return f"{ms:.1f}ms@{tps:.1f}tps"


def apply() -> None:
    from mlx_lm import server as _server

    if hasattr(_server.ResponseGenerator.generate, _WRAPPED_MARKER):
        return

    original = _server.ResponseGenerator.generate

    @functools.wraps(original)
    def wrapped(self, prompt_tokens, completion_tokens, request_id="", *args, **kwargs):
        t0 = time.perf_counter()
        first_token_at = None
        produced = 0

        for token in original(self, prompt_tokens, completion_tokens, request_id=request_id, *args, **kwargs):
            if first_token_at is None:
                first_token_at = time.perf_counter()
            produced += 1
            yield token

        t_end = time.perf_counter()
        prefill_ms = ((first_token_at or t_end) - t0) * 1000.0
        decode_ms  = (t_end - (first_token_at or t_end)) * 1000.0

        print(
            f"[mlx-launch] req={request_id} prompt={prompt_tokens}t "
            f"prefill={_fmt(prefill_ms, prompt_tokens)} "
            f"decode={_fmt(decode_ms, produced)}",
            file=sys.stderr,
            flush=True,
        )

    setattr(wrapped, _WRAPPED_MARKER, True)
    _server.ResponseGenerator.generate = wrapped
