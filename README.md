# mlx-stack

Single-daemon replacement for a multi-script MLX inference setup. One Go binary supervises any number of named MLX models (chat, vision, embedding, audio), exposes them as OpenAI-compatible HTTP endpoints, and hot-swaps members of a shared port. See `docs/2026-05-19-mlx-stack-design.md` and `docs/2026-05-19-mlx-stack-phase-8-unified-backends.md` for the design.

Built on Apple's [MLX](https://github.com/ml-explore/mlx) framework (via `mlx_lm`, `mlx_vlm`, `mlx_embeddings`, `mlx_audio`).

## Install via Homebrew

    brew install --HEAD guygrigsby/mlx-stack/mlx-stack

(Requires tapping `guygrigsby/mlx-stack` first, or `brew install --HEAD ./Formula/mlx-stack.rb` from a local clone.)

mlxd auto-detects the bundled Python shim — no pip install step. You still need a Python with `mlx_lm` installed; if you don't have one, run:

    mlxctl bootstrap --path ~/venvs/mlx

That creates a venv and installs `mlx`, `mlx_lm`, `mlx_vlm`, `mlx_embeddings`, `mlx_audio`. Point `python_bin` in `~/.config/mlx/config.toml` at the resulting `.../bin/python`.

## Build from source

    make build

Produces `bin/mlxd` (daemon) and `bin/mlxctl` (CLI).

## Test

    make test

Go unit + integration tests, Python patch tests, and e2e against a fake `mlx_lm.server`.

## Install

    make install
    make install-launchd      # optional: autostart on login (macOS)
    launchctl load ~/Library/LaunchAgents/dev.grigsby.mlxd.plist

## Config

Every backend is one `[[backend]]` entry. `mode` is the only real distinction:

- `mode = "swap"` — multiple members share a port; only one is loaded at a time. Use for chat-style models where the request's `model` field picks the active member.
- `mode = "persistent"` — always-on, single worker.
- `mode = "external"` — URL-only; mlxd just proxies to it.

Example `~/.config/mlx/config.toml`:

```toml
log_dir     = "~/.logs/mlx"
models_root = "~/mlx-models"
python_bin  = "~/venvs/mlx/bin/python"

[router]
host = "127.0.0.1"
port = 1230
extra_ports = [8080]

[defaults.cache]
limit_bytes        = 2_147_483_648
clear_interval_sec = 60

[defaults.watchdog]
kv_headroom_bytes  = 8_000_000_000
check_interval_sec = 30
grace_sec          = 90

[defaults.memlog]
interval_sec = 300

# Chat group: 1 port, 3 swappable models.
[[backend]]
name    = "valkyrie"
engine  = "lm"
mode    = "swap"
group   = "chat"
host    = "127.0.0.1"
port    = 1234
model   = "~/mlx-models/valkyrie"
default = true

[[backend]]
name   = "scout"
engine = "vlm"
mode   = "swap"
group  = "chat"
host   = "127.0.0.1"
port   = 1234
model  = "~/mlx-models/scout"

[[backend]]
name              = "anubis"
engine            = "lm"
mode              = "swap"
group             = "chat"
host              = "127.0.0.1"
port              = 1234
model             = "~/mlx-models/anubis"
draft_model       = "~/mlx-models/anubis-draft"
trust_remote_code = true   # required when the model ships custom config/modeling .py files (auto_map in config.json)

# Always-on tags model (VLM).
[[backend]]
name   = "qwen-tags"
engine = "vlm"
mode   = "persistent"
host   = "127.0.0.1"
port   = 1235
model  = "~/mlx-models/qwen-tags"
  [backend.watchdog]
  kv_headroom_bytes = 4_000_000_000

# Always-on embedding server.
[[backend]]
name   = "embed"
engine = "embed"
mode   = "persistent"
host   = "127.0.0.1"
port   = 1236
model  = "nomic-ai/nomic-embed-text-v1.5"

# Audio backends. mlx_audio.server multiplexes models per request.
[[backend]]
name   = "tts"
engine = "audio"
mode   = "persistent"
host   = "127.0.0.1"
port   = 1237

[[backend]]
name   = "kokoro"
engine = "audio"
mode   = "persistent"
host   = "127.0.0.1"
port   = 8880

# External backend (mlxd just proxies):
[[backend]]
name           = "remote-embed"
mode           = "external"
url            = "http://other-mac.lan:1236"
upstream_model = "nomic-ai/nomic-embed-text-v1.5"
```

## Run

    mlxd run --config ~/.config/mlx/config.toml --log-dir ~/.logs/mlx

## CLI

`start` / `stop` / `restart` / `swap` take a **member** name (e.g. `valkyrie`, `scout`, `anubis`) — not a group name. `mlxctl chat` and the router can be addressed by either, and `chat` auto-picks the currently-loaded LM swap member (or the configured `default=true` chat member if nothing is loaded yet).

    mlxctl status                 # table of every backend's state
    mlxctl list                   # alias for status
    mlxctl monitor                # status, refreshed every 500ms
    mlxctl tail                   # stream structured stderr events from all workers
    mlxctl tail --worker qwen-tags  # filter to one worker
    mlxctl start <member>         # load a backend (for swap: switch to it)
    mlxctl stop <member>          # stop a backend
    mlxctl restart <member>       # stop then start
    mlxctl swap <member>          # alias for start
    mlxctl chat "hello"           # send a chat request via the router
    mlxctl tags                   # list available models (calls /v1/models)
    mlxctl health                 # daemon liveness
    mlxctl config show            # print current TOML config
    mlxctl add <path-or-hf-repo>  # register a backend in config.toml
    mlxctl scan <dir>             # bulk-register backends from a directory
    mlxctl bootstrap [--path P]   # create a venv with mlx_lm + friends (fresh machines)

`MLXD_ROUTER` overrides the router URL `chat` and `tags` use; otherwise it's read from `~/.config/mlx/config.toml` (`router.host:port`).

## HTTP surface

OpenAI-compatible, all on the router port (default 1230):

    POST /v1/chat/completions      # body's "model" field picks the backend
    POST /v1/completions
    POST /v1/embeddings
    POST /v1/audio/speech
    POST /v1/audio/transcriptions
    GET  /v1/models                # aggregated catalog
    GET  /health

The admin socket at `~/.local/state/mlxd/admin.sock` exposes per-backend actions and a `/v1/logs/tail` SSE stream.
