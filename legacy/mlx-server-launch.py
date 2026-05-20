#!/usr/bin/env python3
"""Launcher shim around mlx_lm.server / mlx_vlm.server.

Adds in-process controls the upstream servers don't expose:
  - mx.set_cache_limit()    via MLX_CACHE_LIMIT_BYTES
  - mx.set_memory_limit()   via MLX_MEMORY_LIMIT_BYTES
  - periodic mx.clear_cache() above a threshold, via
    MLX_CACHE_CLEAR_INTERVAL_SEC and MLX_CACHE_CLEAR_THRESHOLD_BYTES
  - periodic memory log line via MLX_MEMORY_LOG_INTERVAL_SEC
  - active-memory watchdog that os.execv-restarts the process when KV
    cache growth crosses MLX_ACTIVE_MEMORY_LIMIT_BYTES (defends against
    mlx_lm.server's broken --prompt-cache-bytes — verified 2026-05-05
    when KV grew unbounded to 39 GB and Metal aborted with GPU Timeout).
    Polled every MLX_ACTIVE_MEMORY_CHECK_INTERVAL_SEC; first
    MLX_ACTIVE_MEMORY_GRACE_SEC seconds after start are exempt to allow
    the initial model load to settle.

Engine selection: MLX_ENGINE=lm (default) | vlm. lm imports
mlx_lm.server.main; vlm imports mlx_vlm.server.main (multimodal).
The cache/memory controls above operate on mx.core directly and are
engine-agnostic.

Each env var is optional. Unset/blank/0 disables that control.
All other CLI args pass straight through to the chosen server.
"""
import os
import sys
import threading
import time

# Disable mx.compile globally before importing mlx.core. mlx_lm 0.31.3's
# sampler functions in sample_utils.py are decorated with
# @mx.compile(inputs=mx.random.state, outputs=mx.random.state). The compile
# cache appears to capture the random state at first call and reuse it for
# subsequent invocations, which makes sampling fully deterministic across
# requests regardless of the seed parameter or temperature. Empirically:
# same prompt + temp=2.0 + 3 different seeds → byte-identical output every
# time (verified 2026-05-12 with direct curl probes). Setting this env var
# (or calling mx.disable_compile()) before sample_utils.py is imported
# routes around the bug. Cost: ~5-15% slower decode (mx.compile is normally
# a real win), accepted because deterministic-RP is unusable.
os.environ.setdefault("MLX_DISABLE_COMPILE", "1")

import mlx.core as mx


# Capture the launcher path and original argv at import time, BEFORE main()
# mutates sys.argv[0] for cosmetic process-listing reasons. The watchdog's
# os.execv needs these to re-launch the same launcher with the same args;
# without this snapshot it would inherit the corrupted "mlx_lm.server"
# argv[0] and Python would treat that as a script-path-to-open, failing.
LAUNCHER_PATH = os.path.abspath(__file__)
LAUNCHER_ARGV = list(sys.argv)
LAUNCHER_PYTHON = sys.executable


def _int_env(name, default=0):
    v = os.environ.get(name, "").strip()
    if not v:
        return default
    try:
        return int(v)
    except ValueError:
        _log(f"WARN: {name}={v!r} not an int, ignoring")
        return default


def _float_env(name, default=0.0):
    v = os.environ.get(name, "").strip()
    if not v:
        return default
    try:
        return float(v)
    except ValueError:
        _log(f"WARN: {name}={v!r} not a float, ignoring")
        return default


def _fmt_gb(b):
    return f"{b / 1024**3:.2f} GB"


def _log(msg):
    sys.stderr.write(f"[mlx-launch] {msg}\n")
    sys.stderr.flush()


def _janitor(interval, threshold):
    while True:
        time.sleep(interval)
        try:
            cache = mx.get_cache_memory()
            if cache > threshold:
                mx.clear_cache()
                _log(
                    f"clear_cache: was={_fmt_gb(cache)} "
                    f"now={_fmt_gb(mx.get_cache_memory())} "
                    f"active={_fmt_gb(mx.get_active_memory())}"
                )
        except Exception as e:
            _log(f"janitor error: {e!r}")


def _memlog(interval):
    while True:
        time.sleep(interval)
        try:
            _log(
                f"mem: active={_fmt_gb(mx.get_active_memory())} "
                f"cache={_fmt_gb(mx.get_cache_memory())} "
                f"peak={_fmt_gb(mx.get_peak_memory())}"
            )
        except Exception as e:
            _log(f"memlog error: {e!r}")


def _active_watchdog(interval, headroom, grace):
    """Restart the process via execv when active MLX memory grows past
    (model-loaded baseline + headroom). The headroom is configured by the
    user as "how much KV cache and per-request activation memory am I
    willing to tolerate above the model weights"; the baseline is captured
    automatically at the end of the grace period so the user doesn't have
    to know the model's exact footprint.

    Active memory tracks live allocations including mlx_lm's prompt cache —
    the right signal for KV-cache runaway. mx.clear_cache() does NOT free
    these (they're held by the prompt cache structure), so a restart is the
    only reliable recovery. execv replaces the process image in place,
    keeping the same PID, so external pid files / supervisors don't need
    awareness of the restart.
    """
    # Grace: sleep until model load is done. mx.get_active_memory() during
    # load is volatile (weights stream in, allocator pool churns), so
    # baseline is taken at the END of grace.
    started_at = time.time()
    while time.time() - started_at < grace:
        time.sleep(min(interval, max(1.0, grace - (time.time() - started_at))))

    try:
        baseline = mx.get_active_memory()
    except Exception as e:
        _log(f"WATCHDOG: failed to read baseline ({e!r}); disabling")
        return
    threshold = baseline + headroom
    _log(
        f"WATCHDOG: armed. baseline={_fmt_gb(baseline)} "
        f"headroom={_fmt_gb(headroom)} "
        f"trigger_at={_fmt_gb(threshold)}"
    )

    while True:
        time.sleep(interval)
        try:
            active = mx.get_active_memory()
            if active > threshold:
                _log(
                    f"WATCHDOG: active={_fmt_gb(active)} > "
                    f"trigger={_fmt_gb(threshold)} "
                    f"(baseline {_fmt_gb(baseline)} + headroom "
                    f"{_fmt_gb(headroom)}) — execv-restarting"
                )
                sys.stderr.flush()
                sys.stdout.flush()
                # Replace process image with a fresh interpreter. PID stays
                # the same. Env vars persist. The server's listening socket
                # has FD_CLOEXEC by default so it closes on exec and the new
                # process rebinds.
                #
                # Use captured LAUNCHER_PATH/ARGV (snapshotted at import
                # time) instead of live sys.argv — main() rewrites argv[0]
                # to "mlx_lm.server" for cosmetic process-listing, which
                # would otherwise feed Python a non-existent script path.
                argv = [LAUNCHER_PYTHON, LAUNCHER_PATH] + LAUNCHER_ARGV[1:]
                os.execv(LAUNCHER_PYTHON, argv)
        except Exception as e:
            _log(f"watchdog error: {e!r}")


def main():
    cache_limit = _int_env("MLX_CACHE_LIMIT_BYTES")
    memory_limit = _int_env("MLX_MEMORY_LIMIT_BYTES")
    clear_interval = _float_env("MLX_CACHE_CLEAR_INTERVAL_SEC")
    clear_threshold = _int_env("MLX_CACHE_CLEAR_THRESHOLD_BYTES")
    memlog_interval = _float_env("MLX_MEMORY_LOG_INTERVAL_SEC")
    # KV/cache headroom above the loaded-model baseline. Watchdog samples
    # active memory at end of grace, locks that as the baseline, and fires
    # when active memory grows past baseline + headroom.
    kv_headroom = _int_env("MLX_KV_HEADROOM_BYTES")
    active_check_interval = _float_env("MLX_ACTIVE_MEMORY_CHECK_INTERVAL_SEC", 30.0)
    active_grace_sec = _float_env("MLX_ACTIVE_MEMORY_GRACE_SEC", 90.0)

    if cache_limit > 0:
        mx.set_cache_limit(cache_limit)
        _log(f"set_cache_limit({_fmt_gb(cache_limit)})")
    if memory_limit > 0:
        mx.set_memory_limit(memory_limit)
        _log(f"set_memory_limit({_fmt_gb(memory_limit)})")

    if clear_interval > 0:
        threading.Thread(
            target=_janitor,
            args=(clear_interval, clear_threshold),
            daemon=True,
            name="mlx-cache-janitor",
        ).start()
        _log(
            f"janitor: every {clear_interval:g}s, "
            f"clear when cache > {_fmt_gb(clear_threshold)}"
        )

    if memlog_interval > 0:
        threading.Thread(
            target=_memlog,
            args=(memlog_interval,),
            daemon=True,
            name="mlx-memlog",
        ).start()
        _log(f"memlog: every {memlog_interval:g}s")

    if kv_headroom > 0:
        threading.Thread(
            target=_active_watchdog,
            args=(active_check_interval, kv_headroom, active_grace_sec),
            daemon=True,
            name="mlx-active-watchdog",
        ).start()
        _log(
            f"active-watchdog: every {active_check_interval:g}s, "
            f"execv-restart when active > baseline + {_fmt_gb(kv_headroom)} "
            f"(grace {active_grace_sec:g}s; baseline captured at end of grace)"
        )

    engine = os.environ.get("MLX_ENGINE", "lm").strip().lower() or "lm"
    if engine == "vlm":
        sys.argv[0] = "mlx_vlm.server"
        from mlx_vlm.server import main as server_main
    elif engine == "lm":
        sys.argv[0] = "mlx_lm.server"
        import mlx_lm.server as _mlxlm_server
        _patch_xtc_special_tokens(_mlxlm_server)
        _patch_request_timing(_mlxlm_server)
        from mlx_lm.server import main as server_main
    else:
        _log(f"ERROR: unknown MLX_ENGINE={engine!r} (expected 'lm' or 'vlm')")
        sys.exit(2)
    _log(f"engine: {engine}")
    sys.exit(server_main())


def _patch_xtc_special_tokens(mlxlm_server):
    """Wrap mlx_lm.server.make_sampler so xtc_special_tokens is always a flat
    list of ints. Upstream bug: server._make_sampler builds the list with
    [tokenizer.eos_token_id, tokenizer.encode("\\n")] — for tokenizers where
    encode("\\n") returns a multi-element list (Qwen2.5 returns [198]), the
    nested list crashes apply_xtc with "Initialization encountered extra
    dimension". Flattening fixes it without forking mlx_lm.
    """
    orig = mlxlm_server.make_sampler

    def patched(*args, **kwargs):
        toks = kwargs.get("xtc_special_tokens")
        if toks is not None:
            flat = []
            for t in toks:
                if isinstance(t, (list, tuple)):
                    flat.extend(int(x) for x in t if x is not None)
                elif t is not None:
                    flat.append(int(t))
            kwargs["xtc_special_tokens"] = flat
        return orig(*args, **kwargs)

    mlxlm_server.make_sampler = patched
    _log("patched mlx_lm.server.make_sampler (xtc_special_tokens flatten)")


def _patch_request_timing(mlxlm_server):
    """Wrap mlx_lm.server.ResponseGenerator.generate so each completion logs
    a one-line summary with prompt/completion token counts, prefill ms+tps,
    and decode ms+tps. Useful for diagnosing "why is profile A slower than
    profile B" — separates prefill cost (cache miss → cold prompt eval)
    from decode cost (sampler / GPU bandwidth).

    Lives in the launcher rather than as a fork because mlx_lm.server's
    handle_completion is large and version-volatile; the public method
    `ResponseGenerator.generate(request, args, progress_callback)` returns
    `(ctx, iterator)` and is a stable wrap point.
    """
    orig = mlxlm_server.ResponseGenerator.generate

    def timed(self, request, generation_args, progress_callback=None):
        # t_start captures wall-clock at the moment we enqueue the request;
        # by the time the wrapped iterator gets its first item, prefill is
        # already done (mlx_lm's worker thread is blasting ahead even before
        # the consumer iterates). Tokenize cost on the server side is ~ms.
        t_start = time.monotonic()
        ctx, inner = orig(self, request, generation_args, progress_callback)

        def wrapped():
            t_first = None
            count = 0
            try:
                for r in inner:
                    if t_first is None:
                        t_first = time.monotonic()
                    count += 1
                    yield r
            finally:
                t_end = time.monotonic()
                _log_request_timing(request, ctx, t_start, t_first, t_end, count)

        return ctx, wrapped()

    mlxlm_server.ResponseGenerator.generate = timed
    _log("patched mlx_lm.server.ResponseGenerator.generate (per-request timing)")


def _log_request_timing(request, ctx, t_start, t_first, t_end, completion_tokens):
    try:
        prompt_tokens = len(ctx.prompt) if getattr(ctx, "prompt", None) is not None else 0
        cached = getattr(ctx, "prompt_cache_count", 0) or 0
        if cached < 0:
            cached = 0
        new_prompt = max(prompt_tokens - cached, 0)

        prefill_s = (t_first - t_start) if t_first is not None else (t_end - t_start)
        # Decode time excludes the first token (it's in prefill_s); decode_tps
        # is computed over (count - 1) tokens to match.
        decode_s = (t_end - t_first) if (t_first is not None and completion_tokens > 1) else 0.0

        prefill_tps = (new_prompt / prefill_s) if (prefill_s > 0 and new_prompt > 0) else 0.0
        decode_tps = ((completion_tokens - 1) / decode_s) if decode_s > 0 else 0.0

        rid = getattr(request, "request_id", None) or "?"
        rid = str(rid)[:24]

        _log(
            f"req={rid} "
            f"prompt={prompt_tokens}t (cached {cached}, new {new_prompt}) "
            f"completion={completion_tokens}t "
            f"prefill={prefill_s * 1000:.0f}ms ({prefill_tps:.1f} t/s) "
            f"decode={decode_s * 1000:.0f}ms ({decode_tps:.1f} t/s)"
        )
    except Exception as e:
        _log(f"timing log error: {e!r}")


if __name__ == "__main__":
    main()
