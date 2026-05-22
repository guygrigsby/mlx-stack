# Archived legacy scripts

Pre-mlx-stack inference setup. Kept here for reference. Replaced by `mlxd` + `mlxctl` in this repo.

| File | Replaced by |
|---|---|
| `mlx` (zsh CLI) | `mlxctl` (Go) |
| `mlx-router.py` (aiohttp proxy) | `internal/router/` |
| `mlx-server-launch.py` (mlx_lm/vlm launcher shim) | `python/mlx_stack/launcher_shim.py` |
| `mlx-embed-server.py` (FastAPI embed server) | `python/mlx_stack/embed_server/app.py` |
| `mlx.conf` (shell-sourced config) | `~/.config/mlx/config.toml` (hand-written; see README) |

**DO NOT run these.** They conflict with mlxd on ports 1230 + 8080. Kept for archival and to ease debugging of edge cases the new stack hasn't seen yet.

The `mlxctl config migrate` command that once auto-converted `mlx.conf` to TOML was removed (commit `ab482fc`). The current unified `[[backend]]` schema (README + `docs/2026-05-19-mlx-stack-phase-8-unified-backends.md`) doesn't map cleanly from the legacy env-var format; write the new config by hand instead.

    mlxd run --config ~/.config/mlx/config.toml
