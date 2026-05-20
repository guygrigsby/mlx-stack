# Archived legacy scripts

Pre-mlx-stack inference setup. Kept here for reference. Replaced by `mlxd` + `mlxctl` in this repo.

| File | Replaced by |
|---|---|
| `mlx` (zsh CLI) | `mlxctl` (Go) |
| `mlx-router.py` (aiohttp proxy) | `internal/router/` |
| `mlx-server-launch.py` (mlx_lm/vlm launcher shim) | `python/mlx_stack/launcher_shim.py` |
| `mlx-embed-server.py` (FastAPI embed server) | `python/mlx_stack/embed_server/app.py` |
| `mlx.conf` (shell-sourced config) | `~/.config/mlx/config.toml` (run `mlxctl config migrate` to convert) |

**DO NOT run these.** They conflict with mlxd on ports 1230 + 8080. Kept for archival and to ease debugging of edge cases the new stack hasn't seen yet.

## Migration

    mlxctl config migrate ~/.config/mlx.conf > ~/.config/mlx/config.toml

Review the output, then start mlxd:

    mlxd run --config ~/.config/mlx/config.toml
