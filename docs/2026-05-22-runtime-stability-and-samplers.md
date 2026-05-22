# mlx-stack: runtime stability + per-backend samplers

**Date:** 2026-05-22
**Status:** shipped (commits `033c116`..`e17e61c`)
**Type:** bugfix + small feature

Notes on a cluster of fixes done after the cobra port. Captures root causes since the bugs were non-obvious and the commit messages alone don't carry enough context. Treat this as the source of truth for current runtime behavior; the design doc (`2026-05-19-mlx-stack-design.md`) is a snapshot from project start and has drifted in places.

## What changed

| Area | File | Behavior |
|---|---|---|
| `mlxctl chat` | `cmd/mlxctl/chat.go` | Reads `/v1/status` in the new `backends[]` shape, picks router URL from `~/.config/mlx/config.toml`, streams SSE deltas, applies per-backend sampler defaults. `--no-stream` for the old buffered output. |
| Backend config | `internal/config/schema.go` | New optional fields: `trust_remote_code: bool` and `[backend.sampler]` (`temperature`, `top_p`, `top_k`, `min_p`, `repetition_penalty`, `max_tokens`). Both off by default. |
| Worker process | `internal/supervisor/worker.go` | `exec.Command` instead of `exec.CommandContext(ctx, …)`. |
| Swap group | `internal/supervisor/group.go` | Added a reaper that clears `g.current` / `g.worker` when the underlying process exits. |
| KV watchdog | `python/mlx_stack/memory/watchdog.py` + `launcher_shim.py` | `watchdog.start` accepts an explicit `restart_argv`; launcher_shim snapshots its own argv up front. |

## Bugs

### `mlxctl chat` printed "could not determine default chat profile"

`cmd/mlxctl/chat.go` was reading `status.chat.current_profile`, but Phase 8's `/v1/status` returns `{backends: [{name, group, mode, engine, running, current_name, ...}]}`. Field never populated → empty string → error. Separately, default router URL was hardcoded `http://127.0.0.1:1231`, but `router.port` had moved to `8080` in real configs.

Fix: parse the actual status shape, look up the running swap+lm backend's `current_name`, fall back to the configured chat-group default. Router URL is read from config (env `MLXD_ROUTER` overrides).

### Valkyrie crashed at load time with `AttributeError: 'PreTrainedConfig' object has no attribute 'max_position_embeddings'`

Valkyrie is DeciLM; its `config.json` has `auto_map → configuration_decilm.DeciLMConfig`, which transformers will only honor when `trust_remote_code=True`. Without it, transformers fell back to the base `PreTrainedConfig`, and the newer rope-standardization path then read a field that lives on the custom class. (The `signal only works in main thread` earlier in the stack trace was noise: transformers' interactive trust-remote-code prompt was being called off the main thread on Python 3.14.)

Fix: opt-in `trust_remote_code = true` per backend → propagated through `launcherArgs` → `launcher_shim` → `--trust-remote-code` on `mlx_lm.server` / `mlx_vlm.server`. Only set on backends that ship custom config/modeling .py files (grep `auto_map` in `config.json`).

### After one chat, all subsequent chats returned 502 "upstream: dial tcp 127.0.0.1:1234: connect: connection refused"

`Worker.Start` used `exec.CommandContext(ctx, ...)` where `ctx` came from `EnsureLoaded` — and the router was calling `b.EnsureLoaded(r.Context(), model)`, threading the HTTP **request** context through. Go's `exec` package SIGKILLs the child the moment that context is done, so the worker was killed the instant the request that started it completed (or the stream closed).

Persistent backends had been masking the same bug for free via their `supervise()` respawn loop. Swap groups had no reaper, so they wedged with stale `g.current` and the router kept routing to a dead port.

Fix: drop the request-bound exec context — `exec.Command` instead. Worker lifetime is managed explicitly via `Signal` / `Stop`. Then added a Group reaper as belt-and-braces so any future cause-of-death recovers on the next request.

Regression tests:
- `TestGroup_WorkerSurvivesCallerCtxCancel` — cancel the `EnsureLoaded` ctx, assert the worker stays up.
- `TestGroup_WorkerExitClearsState` — let a worker exit, assert `Current()` clears and the next load respawns.

### KV-headroom watchdog crashed the worker on trigger

Hadn't fired in production yet, but it would have. `watchdog.start` did `os.execv(sys.executable, [sys.executable, *sys.argv])`. `launcher_shim` rewrites `sys.argv` to `["mlx_lm.server", "--model", ...]` before handing off, so the watchdog would execv `python mlx_lm.server …`, which the interpreter tries to run as a script path. It doesn't exist. Process exits, port unbinds.

Fix: `watchdog.start` now accepts `restart_argv`; `launcher_shim` snapshots `[python, "-m", "mlx_stack.launcher_shim", *original_args]` before the engine handoff and passes it in. Default still falls back to the old behavior so test fixtures and other callers stay green.

### Repetitive chat output

`mlxctl chat` was sending only `model` + `messages` + `max_tokens`, so `mlx_lm.server` used its CLI defaults (no `min_p`, no `repetition_penalty`). The user's pluma config (`~/.config/pluma/samplers.json`) had per-model profiles tuned for these checkpoints.

Fix: optional `[backend.sampler]` block on each backend. `mlxctl chat` looks up the resolved model name and merges non-zero sampler fields into the OpenAI-compatible request body. `mlx_lm.server` accepts `min_p` / `top_k` / `repetition_penalty` as request-body overrides (see `mlx_lm/server.py:1178-1180`).

Ported pluma's profiles into `~/.config/mlx/config.toml`:

| backend | temperature | top_p | top_k | min_p | rep_penalty | max_tokens |
|---|---|---|---|---|---|---|
| valkyrie | 1.15 | – | – | 0.05 | 1.05 | 400 |
| anubis / shakudo / skyfall | 1.1 | – | – | 0.075 | 1.05 | – |
| scout | 0.7 | 0.9 | 40 | 0.05 | – | – |
| qwen-tags | 0.2 | 0.9 | – | – | – | 256 |

`context_size` is intentionally not in the sampler block — it's a load-time setting, not a per-request field.

## Behavior preserved

- The unified `[[backend]]` schema from phase 8.
- Per-backend `[backend.cache]` / `[backend.watchdog]` / `[backend.memlog]` overrides on top of `[defaults.*]`.
- Persistent's `supervise()` exponential-backoff respawn.
- The launcher_shim's xtc + timing patches, `MLX_DISABLE_COMPILE=1`, all memory env vars.
- mlxctl subcommand surface: `start|stop|restart|swap <member>` still takes a member name; group names are not member-substitutable on the admin socket (the router and `chat` accept either).

## What's still loose

- `Group.EnsureLoaded` returns "already loaded" without verifying the worker is reachable. The reaper covers the case where the process has exited; a hung-but-alive worker would still get traffic and 502 on dial timeout. Worth adding a probe-on-reuse if it becomes a problem.
- The watchdog's execv path is now correctly aimed but has never been exercised end-to-end on a real model. The unit test verifies the argv shape; first real trigger may surface model-reload edge cases (no model loaded in the new process until the next request hits it).
- Sampler resolution happens only in `mlxctl chat`; other clients (third-party OpenAI-compatible apps) hit the router directly and don't see the per-backend defaults. The right long-term home for these is `mlx_lm.server` CLI flags at spawn time, not request-body merge in one client. Out of scope for this batch.
