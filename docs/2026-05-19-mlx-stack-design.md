# mlx-stack: design

**Date:** 2026-05-19
**Status:** historical snapshot. The runtime is now what shipped through phase 8. CLI is `mlxctl`, not `mlx`. The TOML schema is the unified `[[backend]]` array described in `2026-05-19-mlx-stack-phase-8-unified-backends.md`, not the typed `[chat]`/`[tags]`/... shown below. Recent runtime-stability fixes and per-backend samplers are in `2026-05-22-runtime-stability-and-samplers.md`. Use the README for the current quickstart.
**Author:** Guy
**Replaces:** `~/scripts/mlx` (zsh), `~/scripts/mlx-router.py`, `~/scripts/mlx-server-launch.py`, `~/scripts/mlx-embed-server.py`, `~/.config/mlx.conf`

## Background

Today's local MLX inference setup is a working but sprawling collection of scripts:

- `mlx` — 1979-line zsh CLI doing process management, status, monitoring, log tailing, config sourcing, profile switching, chat/tag helpers, router orchestration.
- `mlx-router.py` — 519-line aiohttp proxy with hot-swap-by-`model`-field, static backends, and `lsof`-based discovery of what each backend is actually serving.
- `mlx-server-launch.py` — 348-line Python shim around `mlx_lm.server`/`mlx_vlm.server` adding cache controls, KV-cache watchdog, and monkey-patches for upstream bugs (xtc tokenizer crash, deterministic-sampling bug, per-request timing).
- `mlx-embed-server.py` — 73-line FastAPI server for embeddings because `mlx_embeddings` does not ship one.
- `mlx_audio.server` — upstream multi-model TTS server, spawned twice (TTS + Kokoro on hardcoded port 8880).
- `~/.config/mlx.conf` — shell-sourced config: profile maps, paths, cache/watchdog knobs.

This works. It has months of carefully-earned bug-workaround scar tissue. But the design has reached its limits:

- State is implicit: pidfiles + `lsof` + parsing `ps` cmdlines is the only way the router knows what the chat backend is currently serving. The router even **shells out to `mlx restart chat --chat-model NAME`** to swap profiles, then re-lsofs the port to confirm the swap landed.
- Three languages (zsh, Python, sourced shell .conf) and four entry points stay in sync only by repeated manual attention.
- The zsh script has long since passed maintainability for accreting new features.
- mlx_lm bug workarounds live in a launcher shim. Fine in shape, but they should sit in tested modules, not a 348-line script.

## Goals

- Replace the four-script + sourced-config arrangement with a single application: one daemon, one CLI, one config format, two languages by necessity.
- Eliminate `lsof`/pidfile/cmdline-parsing state. The supervisor knows what it spawned.
- Keep the OpenAI-compatible surface (`/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/audio/*`, `/v1/models`) on the same ports clients already use (1230 + 8080).
- Keep hot-swap-on-demand for chat profiles. Make it an in-process method call, not a shell-out.
- Preserve every mlx_lm bug workaround verbatim: xtc flatten, request-timing wrap, KV-headroom execv-restart watchdog, cache janitor, memlog, `MLX_DISABLE_COMPILE=1` for deterministic sampling.
- No venv activation required to invoke any CLI command.
- Support both **managed** backends (mlxd spawns and supervises) and **external** backends (mlxd just knows the URL). Same router code path either way.
- Run a CLI command in ~5ms cold, not ~250ms — `mlx status` should be free.

## Non-goals

- No multi-user / multi-tenant story. Single Mac, single user.
- No auth on the router (loopback only, like today).
- No model management (downloading, converting). `mlx-models/` stays user-curated.
- No UI beyond CLI + structured logs. Web dashboard is a possible future, not in scope.
- No multi-machine routing in v1. The architecture supports pointing at external URLs, but mlxd itself runs on one Mac.

## Architecture

### Process topology

```
   ┌──────────────────────────────────────────────────────────────────┐
   │  mlxd  (Go, single static binary, long-running daemon)           │
   │  ┌────────────────────────────────────────────────────────────┐  │
   │  │ router    http.Server on 127.0.0.1:1230 (+extra ports)     │  │
   │  │           /v1/chat/completions, /v1/completions             │  │
   │  │           /v1/embeddings                                    │  │
   │  │           /v1/audio/*                                       │  │
   │  │           /v1/models   (aggregated from in-memory state)    │  │
   │  └────────────────────────────────────────────────────────────┘  │
   │  ┌────────────────────────────────────────────────────────────┐  │
   │  │ supervisor   per-backend: spawn, wait, restart, signal     │  │
   │  │              tracks PID, model, mem snapshot, exit history │  │
   │  └────────────────────────────────────────────────────────────┘  │
   │  ┌────────────────────────────────────────────────────────────┐  │
   │  │ admin API  unix socket ~/.local/state/mlxd/admin.sock      │  │
   │  │            /v1/swap /start /stop /restart /status /tail    │  │
   │  └────────────────────────────────────────────────────────────┘  │
   └─────────┬──────────────┬────────────────┬───────────────────────┘
             │ spawns       │ spawns         │ proxies (external)
             ▼              ▼                ▼
   ┌──────────────────┐ ┌──────────────────┐ ┌──────────────────────┐
   │ chat worker      │ │ tts/embed/...    │ │ external URL         │
   │ python -m        │ │ python -m        │ │ (no supervision,     │
   │ mlx_stack        │ │ mlx_stack        │ │  periodic health     │
   │ .launcher_shim   │ │ .launcher_shim   │ │  probe only)         │
   │  --engine lm/vlm │ │  --engine audio  │ └──────────────────────┘
   │  → mlx_lm.server │ │  --engine embed  │
   │  (full patches + │ │  (cache controls │
   │   KV watchdog)   │ │   only)          │
   └──────────────────┘ └──────────────────┘

   ┌────────────────────────────┐
   │  mlx  (Go, CLI binary)     │ ──── unix socket ────▶  mlxd
   │  ~5ms cold start            │
   └────────────────────────────┘
```

### Language split

| Layer | Language | Reason |
|---|---|---|
| Daemon (router, supervisor, admin) | Go | Stdlib `net/http`, `os/exec`, `os/signal`, `context` cover this exactly. Goroutines map naturally to "supervise N workers". Single static binary. No venv. |
| CLI | Go | ~5ms cold start vs ~250ms for Python. Run dozens of times a day. |
| Launcher shim | Python | Imports `mlx.core`, `mlx_lm.server`, `mlx_vlm.server`, `mlx_audio.server`, `mlx_embeddings`. Owns the monkey-patches. Cannot be anything but Python. |
| Embed server | Python | Same reason. Temporary — see "Future work: upstream PR". |

The boundary is narrow: Go spawns `~/venvs/mlx/bin/python -m mlx_stack.launcher_shim …`, reads its stderr for structured `[mlx-launch]` lines, watches its exit code.

### Backend abstraction

```go
type Backend interface {
    Alias() string                                       // /v1/models id
    URL() string                                          // base URL for proxying
    UpstreamModel(ctx context.Context) (string, error)    // what to put in "model" upstream
    Health(ctx context.Context) error
}

type External struct {
    alias, url, upstreamModel string
}

type Managed struct {
    alias, url string
    worker     *Worker          // PID + lifecycle
    // for audio: multi-model preload; for embed: single model
}

type ChatSwap struct {
    port     int
    profiles map[string]Profile
    worker   *Worker
    current  string             // currently-loaded profile
    lock     sync.Mutex          // serializes swaps
}
```

The router does not distinguish — it calls `Backend.UpstreamModel()` and proxies. The supervisor only touches `Managed` and `ChatSwap`.

### Three classes of managed worker

| Class | Backends today | Worker entry point | Patches | Watchdog | Cache controls | Swap |
|---|---|---|---|---|---|---|
| `MlxLm` | chat, tags | `launcher_shim --engine lm/vlm` | xtc, timing | KV-headroom execv-restart | yes | chat: on-demand |
| `MlxAudio` | tts, kokoro | `launcher_shim --engine audio` | none | none (no KV growth) | optional cache limit/janitor/memlog | no (multi-model built into mlx_audio.server) |
| `MlxEmbed` | embed | `launcher_shim --engine embed` | none | none | optional cache limit/memlog | no |

All three share `Backend` and the supervisor's spawn/wait/restart loop. What differs is the **policy bundle** per class: env vars, entry point, whether `mlx swap` is meaningful, restart policy.

### Why one shim, engine-dispatched

The current `mlx-server-launch.py` already dispatches on `MLX_ENGINE=lm|vlm`. We extend that to `lm | vlm | audio | embed`. The shim becomes the **universal MLX worker runtime** — one Python entry point for any process that imports `mlx.core`.

Pros:
- One spawn path in mlxd (`exec.Command(pythonBin, "-m", "mlx_stack.launcher_shim", "--engine", e, ...)`)
- Cache controls available to any engine via env vars; watchdog opt-in via env vars; patches applied conditionally
- One place to add new cross-cutting concerns (allocator stats, log line tagging, signal handling)

Cons:
- The shim grows ~20 lines for the audio + embed dispatch
- Embed's FastAPI is one of several `server_main()` callables; mixes server styles inside one file

Tradeoff lands on yes. The cons are cosmetic; the pro of single-spawn-path is real every time a new engine appears.

### Hot-swap flow (replaces today's shell-out)

```
client → POST /v1/chat/completions {"model":"valkyrie"}
router → resolveBackend("valkyrie") → ChatSwap
       → supervisor.EnsureChat(ctx, "valkyrie")
              acquire lock
              if current == "valkyrie": release, return
              SIGTERM old worker (PID known); wait 10s; SIGKILL if still alive
              spawn new worker:
                  python -m mlx_stack.launcher_shim \
                      --engine lm \
                      --model ~/mlx-models/valkyrie \
                      --port 1234 \
                      [--draft-model ...]
                with env:
                  MLX_CACHE_LIMIT_BYTES=2147483648
                  MLX_KV_HEADROOM_BYTES=8000000000
                  MLX_CACHE_CLEAR_INTERVAL_SEC=60
                  ...
              stream worker stderr → log router (tagged backend=chat profile=valkyrie pid=N)
              probe http://127.0.0.1:1234/v1/models until 200, or swap_timeout
              current = "valkyrie"
              release lock
       → proxy request to 127.0.0.1:1234, stream chunks back to client
```

No `mlx restart chat …` shell-out. No `lsof`. No re-discovery. The supervisor knows what's loaded because it spawned it.

If the launcher_shim's KV watchdog fires mid-stream and `execv`-restarts the worker, the PID stays the same — the supervisor doesn't notice. It will see a brief stderr gap and resume parsing once the new process logs its `WATCHDOG: armed` line. **Behavior identical to today.**

### `/v1/models` aggregation

Pure function over supervisor state + config:

```go
func (r *Router) listModels() []Model {
    models := []Model{}
    for _, profile := range config.Chat.Profiles {
        models = append(models, Model{ID: profile.Name})
    }
    for _, b := range r.backends.External() {
        models = append(models, Model{ID: b.Alias()})
    }
    for _, b := range r.backends.Managed() {
        models = append(models, Model{ID: b.Alias()})
    }
    return models
}
```

Zero probing, zero `lsof`. Returns instantly. Today's "lsof the port and parse `--model` from cmdline" disappears entirely.

### CLI ↔ daemon protocol

CLI is a thin client over the unix socket at `~/.local/state/mlxd/admin.sock`. Wire format: HTTP/1.1 over the unix socket, JSON bodies. Reuses Go's `net/http`; nothing custom.

```
GET  /v1/status                 → snapshot: all backends, PIDs, models, mem, exits
GET  /v1/status?backend=chat    → just one backend
GET  /v1/profiles               → chat profiles + current
GET  /v1/mem                    → per-worker active/cache/peak (parsed from launcher_shim logs)
POST /v1/start    {backend}      → start a backend
POST /v1/stop     {backend}      → stop a backend (graceful: SIGTERM, 30s, SIGKILL)
POST /v1/restart  {backend}      → stop + start
POST /v1/swap     {profile}      → chat-specific swap; equivalent to today's restart chat --chat-model
GET  /v1/logs/tail?backend=…    → SSE stream of structured log events
GET  /v1/health                  → daemon liveness
```

CLI mapping:

| Today (zsh) | New (Go CLI calls admin API) |
|---|---|
| `mlx start chat` | `mlx start chat` |
| `mlx start` | `mlx start all` (start everything configured as `managed`) |
| `mlx stop chat` / `mlx stop all` | same |
| `mlx restart chat --chat-model X` | `mlx swap chat X` (or `mlx restart chat -p X`) |
| `mlx status` | renders table from `/v1/status` |
| `mlx monitor` | SSE on `/v1/status`, redraws TTY |
| `mlx tail chat` | SSE on `/v1/logs/tail?backend=chat` |
| `mlx chat "..."` | direct HTTP to `127.0.0.1:1230/v1/chat/completions` (no admin socket needed) |
| `mlx tag image.jpg` | direct HTTP to router |
| `mlx router start` / `mlx router stop` | gone — the daemon **is** the router |
| `mlx config show` | reads + dumps current TOML |
| `mlx config migrate` | new: convert `~/.config/mlx.conf` to TOML |

If mlxd is not running and the CLI command requires it: print one line "mlxd is not running. Start with: `mlxd run` or `launchctl load …`" and exit 2. The CLI never silently starts the daemon.

### Configuration

TOML at `~/.config/mlx/config.toml`. Validated on load; mlxd fails fast on schema errors.

```toml
log_dir     = "~/.logs/mlx"
models_root = "~/mlx-models"
python_bin  = "~/venvs/mlx/bin/python"   # how mlxd spawns workers

[router]
host        = "127.0.0.1"
port        = 1230
extra_ports = [8080]
# CORS: empty = use upstream default. Set to lock down (e.g. "http://localhost:8000").
allowed_origins = []

# ============================================================================
# Chat: swap-on-demand managed backend
# ============================================================================
[chat]
default_profile  = "valkyrie"
host             = "127.0.0.1"
port             = 1234
swap_timeout_sec = 90

  [chat.cache]
  limit_bytes           = 2_147_483_648    # 2 GB pool cap
  clear_interval_sec    = 60
  clear_threshold_bytes = 1_073_741_824    # 1 GB

  [chat.watchdog]
  kv_headroom_bytes  = 8_000_000_000       # 8 GB above model weights
  check_interval_sec = 30
  grace_sec          = 90

  [chat.memlog]
  interval_sec = 300                        # 5 min snapshots

  [chat.profiles.valkyrie]
  model  = "~/mlx-models/valkyrie"
  draft  = ""                               # spec-dec off
  engine = "lm"

  [chat.profiles.scout]
  model  = "~/mlx-models/scout"
  draft  = ""
  engine = "vlm"

  [chat.profiles.anubis]
  model  = "~/mlx-models/anubis"
  draft  = "~/mlx-models/anubis-draft"
  engine = "lm"

  # ... more profiles, mirroring today's CHAT_MODEL_PROFILES

# ============================================================================
# Tags: always-on managed backend (single model, no swap)
# ============================================================================
[tags]
managed = true
host    = "127.0.0.1"
port    = 1235
model   = "~/mlx-models/qwen-tags"
engine  = "vlm"
alias   = "qwen-tags"                       # /v1/models id

  [tags.cache]    limit_bytes = 1_073_741_824; clear_interval_sec = 60; clear_threshold_bytes = 536_870_912
  [tags.watchdog] kv_headroom_bytes = 4_000_000_000; check_interval_sec = 60; grace_sec = 90
  [tags.memlog]   interval_sec = 300

# ============================================================================
# Embed: managed local OR external URL
# ============================================================================
[embed]
managed = true
host    = "127.0.0.1"
port    = 1236
model   = "nomic-ai/nomic-embed-text-v1.5"
alias   = "embed"

# External variant (uncomment, comment out the above):
# [embed]
# managed = false
# url     = "http://other-mac.lan:1236"
# alias   = "embed"

# ============================================================================
# Audio: two instances (TTS + Kokoro) on different ports, both managed
# ============================================================================
[tts]
managed = true
host    = "127.0.0.1"
port    = 1237
engine  = "audio"
models  = ["~/mlx-models/omnivoice"]        # multi-model; clients pick via "model" field
alias   = "tts"
  [tts.cache] limit_bytes = 1_073_741_824; clear_interval_sec = 60

[kokoro]
managed = true
host    = "127.0.0.1"
port    = 8880                              # hardcoded by Open WebUI etc.
engine  = "audio"
models  = ["~/mlx-models/kokoro"]
alias   = "kokoro"
  [kokoro.cache] limit_bytes = 1_073_741_824; clear_interval_sec = 60
```

#### Migration from `~/.config/mlx.conf`

`mlx config migrate` reads the existing shell config, sources it via `zsh -c`, dumps the environment, and synthesizes the TOML equivalent. Output to stdout for review; user redirects to the new path when satisfied:

```sh
mlx config migrate ~/.config/mlx.conf > ~/.config/mlx/config.toml.draft
diff ~/.config/mlx/config.toml.draft <(mlx config show --resolved)  # sanity check
mv ~/.config/mlx/config.toml.draft ~/.config/mlx/config.toml
```

### Logging and observability

mlxd consolidates everything into one stream:

- mlxd's own structured JSON to `~/.logs/mlx/mlxd-YYYY-MM-DD.log`.
- Each worker's stderr is captured, parsed (the launcher_shim emits `[mlx-launch] …` prefixed lines that we already know how to parse), tagged with `backend=chat profile=valkyrie pid=12345`, forwarded to the same file.
- `mlx tail` reads the file (or SSEs the structured stream from mlxd in real time).

Today's `~/.logs/mlx/{chat,tags,embed,tts,kokoro}.log` split goes away. One stream, filter by backend in the CLI or query the structured events.

Memory snapshots:

- The launcher shim already emits `mem: active=X cache=Y peak=Z` periodically.
- mlxd parses these into `mem.snapshot` events, stores latest-per-worker, serves via `/v1/mem` and renders in `mlx status`.
- Same source of truth as today's `_mlx_active_bytes` zsh helper, just observed centrally instead of re-shelled-out each invocation.

Per-request timing:

- The launcher_shim's `_log_request_timing` wrapper continues to emit `req=… prompt=…t prefill=…ms decode=…ms`.
- mlxd parses + structures these. `/v1/status` includes recent-request stats; `mlx monitor` shows live decode rate.

### Process supervision details

- Spawn with `cmd.SysProcAttr.Setpgid = true` so we can signal the whole group on shutdown.
- `Cmd.Stdout` / `Cmd.Stderr` piped through a goroutine that:
  - Tags each line with backend metadata
  - Forwards to the log file
  - Parses the `[mlx-launch]` prefix for structured events (mem snapshots, watchdog triggers, request timings)
- `Cmd.Wait()` runs in a watcher goroutine; on exit:
  - Records exit code + last 50 log lines
  - For chat: stays down until next request (or explicit `mlx start chat`)
  - For managed embed/tags/tts/audio: restart with exponential backoff (1s, 2s, 4s, max 30s; reset after 5 min of uptime)
- Graceful daemon shutdown: SIGTERM all workers in parallel, wait up to 30s, SIGKILL stragglers, exit.

### KV-cache watchdog: behavior preserved exactly

The launcher_shim continues to:
1. Wait `MLX_ACTIVE_MEMORY_GRACE_SEC` after startup for the model to settle
2. Sample `mx.get_active_memory()` as the baseline
3. Every `MLX_ACTIVE_MEMORY_CHECK_INTERVAL_SEC`, compare current active to `baseline + MLX_KV_HEADROOM_BYTES`
4. If exceeded: log the event, `os.execv()` itself with the same argv

The execv is in-process — same PID, same env, same socket FD (closed via `FD_CLOEXEC` and rebound by the new process). mlxd sees:
- A stderr line: `WATCHDOG: active=X > trigger=Y — execv-restarting`
- A stderr gap of a few seconds during the new process's model load
- A new `WATCHDOG: armed. baseline=…` line once the new process settles

The `Cmd.Wait()` watcher does **not** fire (the process didn't exit; it was replaced). The supervisor's state stays correct. This is the same dance as today.

### Preserved monkey-patches

Moved from `mlx-server-launch.py` to `mlx_stack/patches/`:

- **`patches/xtc.py`** — wraps `mlx_lm.server.make_sampler` to flatten `xtc_special_tokens` from list-of-lists to list-of-ints. Fixes the Qwen2.5 crash where `tokenizer.encode("\n")` returns `[198]` instead of `198` and `apply_xtc` chokes on the nested list.
- **`patches/timing.py`** — wraps `mlx_lm.server.ResponseGenerator.generate` to log per-request prompt/completion token counts, prefill ms+tps, decode ms+tps.

Applied conditionally in the shim based on `--engine lm`. Both have unit tests against stub `mlx_lm.server`-shaped objects (no model load required).

The `MLX_DISABLE_COMPILE=1` env var setup also moves into the shim, applied unconditionally before importing `mlx.core` (regardless of engine — the bug is in `mlx_lm.sample_utils` but the env var is harmless for other engines).

## Repo layout

```
mlx-stack/
├── go.mod
├── go.sum
├── README.md
├── Makefile                          # build / test / install / clean
├── cmd/
│   ├── mlxd/main.go                  # daemon entry
│   └── mlx/main.go                   # CLI entry
├── internal/
│   ├── config/
│   │   ├── schema.go                 # TOML structs + validation
│   │   ├── loader.go                 # parse + env override
│   │   └── migrate.go                # zsh conf → TOML
│   ├── router/
│   │   ├── server.go                 # http.Server setup, route registration
│   │   ├── proxy.go                  # streaming chunk-by-chunk proxy
│   │   ├── catalog.go                # /v1/models aggregation
│   │   └── rewrite.go                # model field rewriting
│   ├── supervisor/
│   │   ├── worker.go                 # generic Worker (PID, lifecycle, stderr piping)
│   │   ├── chatswap.go               # ChatSwap.EnsureChat with lock + probe loop
│   │   ├── managed.go                # always-on Managed worker with backoff restart
│   │   └── exits.go                  # exit watcher + restart policy
│   ├── backend/
│   │   └── backend.go                # Backend interface, External/Managed/ChatSwap structs
│   ├── admin/
│   │   ├── server.go                 # unix-socket http.Server
│   │   └── handlers.go               # /v1/status etc.
│   ├── ipc/
│   │   └── client.go                 # CLI-side client for admin
│   ├── status/
│   │   └── snapshot.go               # status model + rendering
│   ├── logobs/
│   │   └── parser.go                 # parse [mlx-launch] mem/timing/watchdog lines
│   └── ttyrender/
│       └── monitor.go                # mlx monitor's live re-render
├── python/
│   ├── pyproject.toml                # installs into existing ~/venvs/mlx
│   ├── README.md
│   ├── mlx_stack/
│   │   ├── __init__.py
│   │   ├── launcher_shim.py          # entry: python -m mlx_stack.launcher_shim
│   │   ├── patches/
│   │   │   ├── __init__.py
│   │   │   ├── xtc.py                # xtc_special_tokens flatten
│   │   │   └── timing.py             # ResponseGenerator.generate wrap
│   │   ├── memory/
│   │   │   ├── __init__.py
│   │   │   ├── janitor.py            # periodic mx.clear_cache
│   │   │   ├── memlog.py             # periodic mem snapshot lines
│   │   │   └── watchdog.py           # KV-headroom execv-restart
│   │   └── embed_server/
│   │       ├── __init__.py
│   │       └── app.py                # FastAPI /v1/embeddings (temporary; see Future work)
│   └── tests/
│       └── test_patches.py
├── testdata/
│   ├── fixtures/
│   │   ├── mlx.conf.legacy           # input for migration tests
│   │   └── expected.toml             # expected output
│   └── fakemlx/
│       └── main.go                   # fake mlx_lm.server for integration tests
└── legacy/
    ├── README.md                     # "archived; see ../README.md"
    ├── mlx
    ├── mlx-router.py
    ├── mlx-server-launch.py
    ├── mlx-embed-server.py
    └── mlx.conf
```

## Install / run model

```sh
# One-time
mkdir -p ~/projects
cd ~/projects
git init mlx-stack            # or clone from remote once it has one
cd mlx-stack
make install
# → builds ./bin/{mlxd,mlx}
# → installs them to ~/.local/bin (or ~/scripts if user prefers; configurable)
# → ~/venvs/mlx/bin/pip install -e ./python (uses existing venv with mlx_lm etc.)
# → no new venv created

# First-time config
mlx config migrate ~/.config/mlx.conf > ~/.config/mlx/config.toml.draft
# review and adjust
mv ~/.config/mlx/config.toml.draft ~/.config/mlx/config.toml

# Run daemon — three options:
mlxd run                                    # foreground; logs to stderr
nohup mlxd run >> ~/.logs/mlx/mlxd.log &    # background; matches today's workflow
launchctl load ~/Library/LaunchAgents/dev.grigsby.mlxd.plist  # auto-start on login
```

A launchd plist template ships in the repo. Daemon runs as the user (no root), keeps mlxd alive on crash, restarts on log rotation, logs to `~/.logs/mlx/mlxd-launchd.log`. Manual `nohup` is documented as the fallback for users who don't want launchd.

No venv activation, ever. The Go binaries are static. The Python shim is invoked via the absolute `python_bin` from config.

## Testing strategy

**Go side, no GPU or real models needed:**

- Unit tests: config parsing/validation/migration, model resolution (alias → backend), log line parsing, request body rewriting, catalog aggregation.
- Integration: `testdata/fakemlx/main.go` is a tiny Go binary that pretends to be `mlx_lm.server` — binds a port, returns `/v1/models`, accepts `POST /v1/chat/completions` (returns a canned SSE stream), prints `[mlx-launch] mem: …` to stderr, honors SIGTERM. Drives full supervisor + swap + restart-on-exit + watchdog-restart-in-place + catalog flows. Fast, deterministic, no GPU.
- Streaming proxy tested with a fake upstream that emits chunks at controlled rates; verifies no buffering, no out-of-order delivery, EOF semantics.

**Python side:**

- Unit tests for the patches: xtc flatten correctness with various input shapes, timing math, env var parsing.
- Tests stub out `mlx_lm.server`-shaped objects; no model loading.

**End-to-end smoke test (manual, not CI):**

- Bring mlxd up, run a small chat completion, verify response.
- Swap profiles, verify the new model is loaded and serves correctly.
- Hit `/v1/embeddings`, verify vector shape.
- Hit `/v1/audio/speech`, verify audio bytes.
- Kill the chat worker externally; verify supervisor restart.
- Watch a long-running stream; verify KV watchdog fires and execv-restart happens transparently.

## Migration plan

Each phase leaves you with a working system. Old stack stays untouched until phase 6.

**Phase 1 — Skeleton + chat backend.**
mlxd on port 1231 (next to old router on 1230). Go module set up. Chat backend works end-to-end: spawn launcher_shim → mlx_lm.server, hot-swap profiles, `/v1/chat/completions`, `/v1/models`. Old zsh stack untouched. Test against real models.

**Phase 2 — Tags backend.**
Add `[tags]` to config. Verify same supervisor pattern works for an always-on `Managed`.

**Phase 3 — Embed backend.**
Lift `mlx-embed-server.py` content into `mlx_stack/embed_server/app.py`. Add `[embed]` support. Verify both `managed=true` and `managed=false` paths.

**Phase 4 — Audio (TTS + Kokoro).**
Add `--engine audio` to the launcher_shim. Two `[tts]` + `[kokoro]` instances. Verify multi-model in-process switching works (it's a feature of mlx_audio.server, not us).

**Phase 5 — CLI parity.**
All `mlx <subcommand>` flows wired to admin API. Old zsh script renamed to `mlx-legacy` for fallback. Add launchd plist.

**Phase 6 — TOML migration + port swap.**
`mlx config migrate` works. Old `~/.config/mlx.conf` archived. mlxd takes :1230 + :8080. Old stack shut down. Zsh script moved to `legacy/`.

**Phase 7 — Observability polish.**
`mlx status` table render, `mlx monitor` SSE TTY refresh, structured log routing finalized.

Cutover risk concentrated in phase 6 (port swap). All prior phases are additive.

## Future work (out of scope for v1)

### Upstream the embed server

Our `mlx_stack.embed_server` is a stopgap. Structure it as a near-drop-in for a future `mlx_embeddings.server`:

- Same argparse conventions as `mlx_lm.server` (`--model`, `--host`, `--port`, `--log-level`, `--trust-remote-code`)
- Support `dimensions` param (matryoshka), `encoding_format=base64`, batch size flag
- `/v1/models` returns the actual loaded model id
- Tests + docstrings to upstream quality

Work happens in a separate clone of the upstream repo, not in `mlx-stack`:

```sh
cd ~/projects
git clone https://github.com/Blaizzy/mlx-embeddings.git mlx-embed
cd mlx-embed
# port mlx_stack/embed_server/app.py here, polish, add tests
# open PR upstream
```

When merged, the change in `mlx-stack` is a one-line edit in the launcher_shim dispatch — from `mlx_stack.embed_server.app` to `mlx_embeddings.server` — and we delete our copy.

### Other possible follow-ups (not yet scoped)

- launchd integration polish (per-backend launchd-managed children for stronger crash recovery)
- A small Web UI for status/monitoring (only if CLI ever feels insufficient)
- Multi-machine routing (mlxd-A routes some requests to mlxd-B; needs auth)
- Metrics export (Prometheus textfile or OpenTelemetry; only if there's a consumer)
- Request queueing / backpressure (today every request goes immediately to the backend; under load a queue with priority might help)

## Open questions

1. **CLI binary install path.** `~/.local/bin/` (clean, PATH-canonical) or `~/scripts/` (matches today). Defaulting to `~/.local/bin/`; user can symlink into `~/scripts/` if they prefer.
2. **launchd vs manual nohup.** Both supported; recommendation is launchd for auto-restart on login + crash. User picks at install time.
3. **Multi-port binding.** Today the router binds both 1230 and 8080. Confirmed both are needed (SillyTavern uses 1230, some client uses 8080). New config has `port = 1230, extra_ports = [8080]`.
4. **Log retention.** Today logs accumulate in `~/.logs/mlx/`. mlxd should rotate (daily?) or rely on launchd's rotation. Defaulting to daily files (`mlxd-YYYY-MM-DD.log`); rotation handled by mlxd, no logrotate dependency.

## Decided

- **Repo location:** `~/projects/mlx-stack` (this repo).
- **Upstream PR workspace:** `~/projects/mlx-embed` (separate clone of `Blaizzy/mlx-embeddings` for the embed-server upstreaming work; not part of `mlx-stack`).
- **Daemon/CLI language:** Go.
- **Worker shim language:** Python (unchanged).
- **Embed model handling:** separate process (managed or external), never loaded inside mlxd.
- **Audio:** two managed worker instances (TTS + Kokoro) via the `--engine audio` dispatch in the unified launcher_shim.

## Appendix: what's preserved verbatim from today

| Capability | Today | New location | Notes |
|---|---|---|---|
| OpenAI surface on :1230 + :8080 | `mlx-router.py` | Go `internal/router` | Identical wire behavior |
| Hot-swap chat profile by `model` field | `mlx-router.py` shells `mlx restart chat …` | Go `internal/supervisor/chatswap.go` in-process | No more shell-out |
| `/v1/models` aggregation | `mlx-router.py` + `lsof` | Go `internal/router/catalog.go` from state | No more probing |
| Static backends (tags, embed, tts) | `ROUTER_STATICS=…` | TOML `managed=true/false` | Same router code path |
| Per-backend cache limit, janitor, memlog | env vars consumed in `mlx-server-launch.py` | Same env vars from launcher_shim | Behavior identical |
| KV-headroom active-memory watchdog | `mlx-server-launch.py:_active_watchdog` | `mlx_stack/memory/watchdog.py` | Behavior identical |
| `MLX_DISABLE_COMPILE=1` (deterministic sampling fix) | `mlx-server-launch.py` top-level | `mlx_stack/launcher_shim.py` top-level | Behavior identical |
| `xtc_special_tokens` flatten patch | `mlx-server-launch.py:_patch_xtc_special_tokens` | `mlx_stack/patches/xtc.py` | Behavior identical |
| Per-request timing wrap | `mlx-server-launch.py:_patch_request_timing` | `mlx_stack/patches/timing.py` | Behavior identical |
| `MLX_ENGINE=lm\|vlm` selector | `mlx-server-launch.py:main` | `mlx_stack/launcher_shim.py` | Extended to `audio`, `embed` |
| Chat profiles map (name → model path + draft + engine) | `CHAT_MODEL_PROFILES` etc. shell arrays | `[chat.profiles.<name>]` TOML tables | Migration tool generates |
| Cache memory knobs per server | `CHAT_*`, `TAGS_*` env vars | `[<backend>.cache]`, `[<backend>.watchdog]`, `[<backend>.memlog]` TOML | Migration tool generates |
| Two mlx_audio.server instances (TTS + Kokoro) | `launch_tts` + `launch_kokoro` zsh | Two `[tts]` + `[kokoro]` TOML sections | Same hardcoded :8880 default |
| `mlx chat`, `mlx tag`, `mlx tags` helpers | zsh functions | Go CLI subcommands | Direct HTTP to router |
| `mlx status` / `mlx monitor` / `mlx tail` | zsh + lsof + tail | Go CLI ↔ admin API | Same UX |
| Log directory | `~/.logs/mlx/<server>.log` | `~/.logs/mlx/mlxd-YYYY-MM-DD.log` | Consolidated; filter by backend in CLI |
