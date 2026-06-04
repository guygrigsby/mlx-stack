# Offload: two-tier model storage

Status: design, approved for planning
Date: 2026-06-03

## Problem

Model weights are large (8 GB to 120 GB+) and the internal SSD is finite (1.8 TB,
~630 GB free). A new external drive (`/Volumes/weights-data`, 5.5 TB) will hold the
bulk of the library. We want most models to live on the external drive while the
SSD keeps only a fast-loading working set, without the user hand-managing paths or
hitting the "status says loaded but it froze" class of failure when a path is stale.

Today backends point at absolute `models_root/<name>` paths. A model moved to the
external drive (already done for `anubis`/`anubis-draft`) leaves a dangling path and
fails to load. Offload turns that into normal, managed behavior.

## Goals / non-goals

Goals:
- External drive is the durable home of every model; SSD is a budgeted cache of copies.
- Loading a model that is only on the external drive transparently pulls it to the SSD.
- The SSD cache stays under a configured size via LRU eviction.
- Manual `offload` / `pull` overrides, plus a one-shot sweep for the initial migration.
- Degrade gracefully when the external drive is unmounted.
- Opt-in: with no `[offload]` config, behavior is exactly as today (single tier).

Non-goals:
- No automatic tiering across more than two locations.
- No dedup/compression of weights.
- No change to how the supervisor spawns or probes workers.

## Ubiquitous language (bounded context: `internal/offload`)

- **Library**: `external_root`. The durable home. Every offloaded or backed-up model lives here.
- **Cache**: internal `models_root`. Budgeted copies of models for fast loading.
- **Tier** (derived from the filesystem, never stored as truth):
  - `offloaded` — in library only.
  - `hot` — in cache, and the library also has a copy (safe to evict by deleting the cache copy).
  - `local-only` — in cache only, not yet in the library (e.g. a freshly `add`ed model, or today's valkyrie). Not backed up until first offloaded/evicted.
- **pull**: copy library to cache (evicting to fit the budget).
- **offload**: ensure a library copy exists, then delete the cache copy.
- **evict**: an automatic offload of the least-recently-used `hot` model to make room.

The offload manager is the aggregate that owns these transitions and their invariants.

## Configuration

Opt-in. Absent `[offload]`, the manager is disabled and load behaves as today.

```toml
[offload]
external_root      = "/Volumes/weights-data/mlx-models"
local_budget_bytes = 400_000_000_000   # cap on total cache size
```

`models_root` (existing) is the cache root. `external_root` is the library root.

## Architecture

New package `internal/offload`, a self-contained bounded context.

- `Manager` (aggregate): owns tier resolution, `EnsurePulled`, `Offload`, `Pull`,
  eviction, reconcile, and the LRU metadata. Pure domain logic.
- `FileStore` (port / anti-corruption boundary): the only thing that touches disk.
  Interface with `Exists`, `Size`, `CopyDir` (temp + atomic rename), `RemoveDir`,
  `MountedAt`. Production impl wraps `os`; tests use an in-memory/temp-dir fake. The
  manager never imports `os` directly, so its logic is testable without a real drive.
- mlxd wiring: when configured, mlxd constructs a `Manager` and the supervisor calls
  `Manager.EnsurePulled(ctx, modelDir)` (and the backend's draft model dir, if any)
  immediately before spawning a worker. No `[offload]` config means no manager and
  the call is skipped.
- admin endpoints: `POST /v1/offload`, `POST /v1/pull` (and `--inactive` handled by
  mlxctl enumerating cache dirs not referenced by the config and calling offload per dir).
- mlxctl: `offload`, `pull` commands; tier + cache-usage columns in `status` / `list`.

State that must persist (LRU access times) lives in
`~/.local/state/mlxd/offload.json` as `{modelName: lastUsedUnix}`. Sizes are read
from disk on demand. On startup the manager reconciles: scan both roots, classify
each model's tier, drop metadata for models that no longer exist, default a missing
`lastUsed` to the directory mtime.

## Load flow

`EnsurePulled(ctx, name)`:
1. If `name` is in the cache: touch `lastUsed`, return (hot or local-only, already fast).
2. Else if `name` is in the library:
   - If the drive is not mounted: return error `model %q is offloaded but the external drive is not mounted`.
   - Compute `need = size(library/name)`. While `cacheUsed + need > budget`: pick the
     LRU evictable model (tier `hot`, has a library copy, not currently loaded, not
     `name`); offload it (delete its cache copy). If none is evictable and it still
     does not fit, return error `cannot fit %q in cache budget (all cached models are pinned)`.
   - Copy `library/name` to `cache/name` (temp dir + atomic rename). Touch `lastUsed`.
3. Else: return error `unknown model %q` (not in cache or library).

The supervisor then spawns the worker against `models_root/<name>` exactly as today.
`EnsurePulled` is called for the backend's `model` and, if set, its `draft_model`.

"Currently loaded" is known to mlxd from the supervisor (running workers and the
current swap member); those names are pinned and never evicted.

## Eviction policy

LRU by `lastUsed`. A model is evictable only if: tier is `hot` (a library copy
exists, so eviction is a cheap delete with no write-back), it is not currently
loaded, and it is not the model being pulled. `local-only` models are not evicted
implicitly; they are only moved to the library by an explicit `offload` (which copies
first). If the working set is pinned and a pull cannot fit, the pull/load fails with a
clear error rather than thrashing.

## Commands

- `mlxctl offload <model>`: if the model is `local-only`, copy cache to library first;
  then delete the cache copy. Frees SSD. Errors if the drive is unmounted.
- `mlxctl offload --inactive`: offload every cache model not referenced by any active
  backend (`model` or `draft_model`) in the config. The initial-migration sweep.
- `mlxctl pull <model>`: copy library to cache (evicting to fit). Pre-warms a model.
- `mlxctl status` / `list`: per-model tier (`hot` / `offloaded` / `local-only`) and
  total cache used vs budget.

## Drive absent (degraded mode)

The manager checks `external_root` is a mounted, present directory before any library
operation. When absent: `hot` and `local-only` models load normally; loading an
`offloaded` model errors clearly; `offload` / `pull` / eviction refuse with "external
drive not mounted"; `status` shows offloaded models as `unavailable`. Nothing crashes.

## Edge cases and safety

- **Copy integrity**: copies write to a sibling temp dir and atomic-rename into place;
  a copy is considered valid only if `config.json` (and at least one weights file)
  is present. Partial copies are removed on failure. A pull that fails leaves the
  cache unchanged.
- **Draft models**: a backend's `draft_model` is a model dir too and is pulled and
  evicted alongside its parent; while the parent is loaded, the draft is pinned.
- **local-only first offload**: copies to the library before deleting the cache copy,
  so the only durable copy is never destroyed.
- **Duplicated dirs already present** (e.g. `Austral-Qwen3-235B`, `gpt-oss-120b`):
  reconciled as `hot`; no action needed.
- **Already-offloaded** (`anubis`, `anubis-draft`): reconciled as `offloaded`; the next
  load auto-pulls them, fixing the currently-dangling backend path.
- **Concurrency**: `EnsurePulled` / `offload` / `pull` for the same model serialize in
  the manager so a load and an offload cannot race on the same directory.

## Testing

- `internal/offload` unit tests against the `FileStore` fake: tier classification,
  `EnsurePulled` for each tier, eviction order and budget math, pin protection,
  drive-absent paths, local-only-first-offload, reconcile from a dirty state. No real disk.
- A small `FileStore` integration test against a temp dir for copy/atomic-rename/size.
- mlxd test: a load of an offloaded model triggers `EnsurePulled` and pins running models.
- mlxctl tests for `offload` / `pull` / `--inactive` argument handling and status rendering.

## Migration

1. Ship with `[offload]` configured for `/Volumes/weights-data/mlx-models`.
2. `mlxctl offload --inactive` sweeps cache models not in the active config to the library.
3. Active models stay hot and gain a library backup lazily the first time they are evicted,
   or immediately via `mlxctl offload <model>` if the user wants them backed up now.
