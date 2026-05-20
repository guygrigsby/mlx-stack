# mlx-stack

Single-daemon replacement for the multi-script MLX inference setup. See `docs/2026-05-19-mlx-stack-design.md` for the design.

## Build

    make build

Produces `bin/mlxd` and `bin/mlxctl`.

## Test

    make test

Runs Go unit tests + Python tests.

## Install

    make install

Copies `bin/mlxd` and `bin/mlxctl` to `~/.local/bin` and installs the Python package editably into `~/venvs/mlx`.

## Autostart on login (macOS)

    make install-launchd
    launchctl load ~/Library/LaunchAgents/dev.grigsby.mlxd.plist

To unload:

    launchctl unload ~/Library/LaunchAgents/dev.grigsby.mlxd.plist
    make uninstall-launchd

mlxd writes its log to `~/.logs/mlx/mlxd-launchd.log` when run via launchd.

## Manual run

    mlxd run --config ~/.config/mlx/config.toml

## CLI

    mlxctl status           # show backend state
    mlxctl monitor          # live-refresh status
    mlxctl tail             # stream stderr events from all workers
    mlxctl swap <profile>   # swap chat profile
    mlxctl chat "..."       # send a chat request via the router
    mlxctl tags             # list available models
    mlxctl start chat       # start chat backend
    mlxctl stop chat        # stop chat backend
    mlxctl health           # daemon liveness check
