"""Per-request timing wrap for mlx_lm.server.ResponseGenerator.generate.

Real upstream signature (mlx_lm 0.31.3):
    generate(self, request, generation_args, progress_callback=None)
returns: (ctx, generator)

The returned generator yields response objects. We wrap it to time prefill
(call -> first yield) and decode (first yield -> end of iteration), emitting
a live progress line ~every second during decode:

    [mlx-launch] req=<id> progress tokens=N tps=X.X max=M

and a completion line at the end:

    [mlx-launch] req=<id> prompt=<N>t prefill=<ms>ms@<tps>tps decode=<ms>ms@<tps>tps

max is generation_args.max_tokens (always set — model default when client omits it).
"""
from __future__ import annotations

import sys
import time
import uuid

_WRAPPED_MARKER = "__timing_wrapped__"
_PROGRESS_INTERVAL = 1.0  # seconds between live progress lines


def _fmt(ms: float, tokens: int) -> str:
    tps = (tokens / (ms / 1000.0)) if ms > 0 else 0.0
    return f"{ms:.1f}ms@{tps:.1f}tps"


def _prompt_token_count(self, request) -> int:
    try:
        return len(self.tokenizer.encode(request.prompt))
    except Exception:
        try:
            return len(request.prompt or "")
        except Exception:
            return 0


def apply() -> None:
    from mlx_lm import server as _server

    if hasattr(_server.ResponseGenerator.generate, _WRAPPED_MARKER):
        return

    original = _server.ResponseGenerator.generate

    def wrapped(self, request, generation_args, progress_callback=None):
        t0 = time.perf_counter()
        req_id = uuid.uuid4().hex[:8]
        max_tokens = getattr(generation_args, "max_tokens", 0) or 0
        result = original(self, request, generation_args, progress_callback)

        try:
            ctx, gen = result
        except (TypeError, ValueError):
            return result

        prompt_tokens = _prompt_token_count(self, request)
        first_at = [None]
        produced = [0]
        last_emit = [0.0]

        def timed_gen():
            for item in gen:
                now = time.perf_counter()
                if first_at[0] is None:
                    first_at[0] = now
                produced[0] += 1
                # Live decode progress ~every second
                if first_at[0] is not None and (now - last_emit[0]) >= _PROGRESS_INTERVAL:
                    elapsed = now - first_at[0]
                    tps = produced[0] / elapsed if elapsed > 0 else 0.0
                    print(
                        f"[mlx-launch] req={req_id} progress"
                        f" tokens={produced[0]} tps={tps:.1f} max={max_tokens}",
                        file=sys.stderr, flush=True,
                    )
                    last_emit[0] = now
                yield item
            t_end = time.perf_counter()
            prefill_ms = ((first_at[0] or t_end) - t0) * 1000.0
            decode_ms  = (t_end - (first_at[0] or t_end)) * 1000.0
            print(
                f"[mlx-launch] req={req_id} prompt={prompt_tokens}t "
                f"prefill={_fmt(prefill_ms, prompt_tokens)} "
                f"decode={_fmt(decode_ms, produced[0])}",
                file=sys.stderr,
                flush=True,
            )

        return ctx, timed_gen()

    setattr(wrapped, _WRAPPED_MARKER, True)
    _server.ResponseGenerator.generate = wrapped
