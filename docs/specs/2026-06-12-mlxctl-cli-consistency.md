# mlxctl CLI consistency plan

Status: proposed
Date: 2026-06-12

Audit of every mlxctl command found one user-visible bug, two structural
inconsistencies, and a handful of drift. Four phases, each independently
shippable, ordered so later phases build on earlier refactors. TDD throughout:
failing test, fix, green.

## Findings being addressed

1. Every error prints twice: root sets `SilenceUsage` but not `SilenceErrors`,
   and `main()` prints the error again after `Execute()`.
2. `-o/--output` is a global flag honored only by `printStatus`. `health`
   always emits raw JSON (ignores `-o text`); `tags`, `scan`, `reload`,
   `stop`, `offload --inactive`, `config show` ignore the flag entirely.
3. Config file resolution differs per command: `add`/`scan` take `--config`,
   `chat`/`run`/`offload` read `MLX_CONFIG` (undocumented), `config show`
   takes a positional path with its own hardcoded default. Env prefix is
   inconsistent (`MLX_CONFIG` vs `MLXD_SOCK`/`MLXD_ROUTER`).
4. `list`/`status` and `swap`/`start` are duplicate command implementations
   instead of cobra aliases; their help text has already drifted.
5. `start`/`restart`/`swap` use `context.Background()` with no timeout while
   `reload`/`stop`/`health`/`offload` use the 120s `ctx()`.
6. `offload` hand-rolls arg validation inside `RunE` instead of cobra `Args`.
7. `tags` collides with a backend name (`tags` is a configured vlm backend),
   and is the only network call with no HTTP timeout. The OpenAI-conventional
   name for `GET /v1/models` is `models`.
8. Post-action output mixes status tables (`start`/`restart`/`swap`/`pull`)
   with prose (`stop`, `reload`, `offload --inactive`).

Not changing: kebab-case flag names, `--no-*` negative flags, stdout/stderr
split, help groups, `run`/`chat` arg shapes, monitor refresh interval.

## Phase 1: pure fixes, no surface change

- Extract root command construction from `main()` into `newRootCmd()` so
  error handling and flag wiring become testable.
- Set `SilenceErrors: true`; `main()` remains the single printer. Test:
  execute a failing command via `newRootCmd()`, assert the error appears
  once on stderr.
- `start`/`restart`/`swap`/`monitor` requests use `ctx()` (120s covers the
  90s swap timeout) instead of bare `context.Background()`.
- `offload`: `Args: cobra.MaximumNArgs(1)` plus the existing `--inactive`
  exclusivity check; drop the hand-rolled usage string.
- `tags`: use a client with a timeout (same 120s helper).

## Phase 2: one way to find the config

- New `configPath()` helper, precedence: `--config` flag, then `MLXD_CONFIG`,
  then `MLX_CONFIG` (back-compat), then `~/.config/mlx/config.toml`.
- `--config` becomes a root persistent flag; `add`/`scan` drop their local
  copies. `config show [path]` keeps its positional override (wins over the
  flag) for back-compat.
- `loadCfg()` and `config show` route through `configPath()`.
- Root long help documents all env vars: `MLXD_SOCK`, `MLXD_ROUTER`,
  `MLXD_CONFIG` (primary), `MLX_CONFIG` (legacy).

## Phase 3: -o json everywhere it makes sense

Pattern: text mode prints the current human output; json mode prints a stable
machine shape. Streaming chat output is exempt (`chat`/`run` stay text; the
flag is documented as not applying to them).

- `health`: text prints `ok` (or the not-running error); json prints the raw
  daemon body. This fixes the current backwards behavior.
- `tags` (renamed to `models` in Phase 4): text prints IDs one per line
  (unchanged); json prints the raw `/v1/models` body.
- `reload`: json prints the `reloadResult`.
- `stop`: json prints `{"stopped": [...], "failed": [...]}`.
- `scan`: json prints the candidates array (name, engine, path, in_config).
- `offload --inactive`: json prints `{"offloaded": [...]}`.

## Phase 4: names and aliases

- `tags` renamed to `models`; `Aliases: ["tags"]` keeps the old name working.
- `newSwapCmd`/`newListCmd` deleted; `start` gets `Aliases: ["swap"]`,
  `status` gets `Aliases: ["list"]`. Help shows the alias on the command
  page; the top-level list shrinks by two entries.
- `stop` prose normalized to match the `stopped <name>` style already used;
  `reload` keeps prose (multi-target results read better as lines than a
  table). No other output changes.

## Risks

- Scripts calling `mlxctl list`/`mlxctl swap`/`mlxctl tags` keep working via
  aliases; only `--help` output changes.
- `MLX_CONFIG` remains honored, so existing environments are unaffected.
- Phases are chained (P1 -> P2 -> P3 -> P4) to avoid merge conflicts in the
  same files, not because of hard dependencies.
