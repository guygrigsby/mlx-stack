# Hot-load new backends (no mlxd restart)

Status: approved design, pre-implementation.
Date: 2026-05-29.

## Problem

`mlxctl add` writes a new `[[backend]]` to `config.toml`, but mlxd reads config
once at startup and builds an immutable set of backends. A new model is not
routable until the daemon restarts. Goal: make a freshly added backend usable
without a restart.

## Decisions

- **Trigger:** `mlxctl add` writes config as today, then calls a new
  `POST /v1/reload` over the existing admin unix socket. mlxd re-reads
  `config.toml` and reconciles. Also exposed as a standalone `mlxctl reload`.
- **Scope: additive only.** Reload registers `[[backend]]` entries mlxd does not
  already know by name. Existing and running backends stay untouched. Removed or
  edited entries are ignored until a restart (surfaced in reload output).
- **Load timing: lazy.** A reloaded backend becomes routable; its worker spawns
  on the first request or on explicit `mlxctl start <name>`, matching how
  persistent and swap backends already behave. No eager spawn.
- **`mlxctl add` reloads by default**, with `--no-reload` to opt out. The reload
  call is best-effort: a down daemon never fails the config write.

## Current state (what is immutable today)

- `config.Load` runs once in `cmd/mlxd/main.go` (`newRunCmd`); no watch/reload.
- Backends built once at lines ~80-154: Groups (swap), Persistents, Externals,
  collected into `backends []bk.Backend`.
- `router.Registry.byName` is a plain map, read concurrently per request, never
  written after construction.
- `/v1/models` catalog is a frozen snapshot built from `cfg.AllNames()`.
- `admin.Handlers` holds its own `Backends []bk.Backend` + `Aliases` snapshot.
- Each `supervisor.Group` holds a fixed `opts.Members` map.

Reads happen on router goroutines; reload writes come from the admin goroutine.
So shared state must become mutable and concurrency-safe, and one code path must
build a backend from a spec for both boot and reload.

## Components

### 1. Backend builder (extract from main.go)

Pull the per-backend construction (lines ~80-154) into a `backendBuilder` that
captures the shared deps: `PythonBin`, `shimDir`, `broker`, `logger`,
`cfg.Defaults`. Methods:

- `buildPersistent(spec) *supervisor.Persistent`
- `buildExternal(spec) *supervisor.External`
- `newGroup(name string, members map[string]config.BackendSpec) *supervisor.Group`

Boot and reload both call it. This is the single source of truth for
"spec to running backend" and the anti-corruption boundary around worker
construction.

### 2. router.Registry becomes concurrency-safe

Add a `sync.RWMutex`:

- `Resolve` / `All` / `Names` take `RLock`.
- New `Register(b backend.Backend)` and existing `RegisterAlias` take `Lock`.

The router already holds the registry by pointer, so a newly registered backend
is routable with no further wiring.

### 3. /v1/models reads live

Change `router.Server.handleListModels` to derive names from `registry.Names()`
at request time instead of the frozen catalog snapshot, so new backends appear.
Removes the duplicate `Names` snapshot threaded through `ServerOpts`.

### 4. supervisor.Group.AddMember(spec)

Common case: `mlxctl add <model>` defaults to `group=chat, mode=swap`, so the new
model joins the existing chat Group rather than creating a new one. Add
`AddMember(spec config.BackendSpec)` guarded by `g.mu`. Also fix the existing
unlocked read in `Members()` to take `g.mu`. The member joins on the group's
existing port; if the spec port disagrees, log a warning and use the group's
port.

### 5. admin.Handlers: live state + reload hook

- Guard `Backends` and `Aliases` with an `RWMutex`; `byName` and `status`
  read-lock; add a setter that swaps in the grown set under write-lock.
- Add a `Reload func(ctx context.Context) (ReloadResult, error)` field and a
  `POST /v1/reload` handler that invokes it and returns the result as JSON.
- `ReloadResult { Added []string; Skipped []string }`.

### 6. Reload closure (main.go)

Lives where all deps are in scope (no new package, no import cycle). Steps:

1. `config.Load(cfgPath)`. On error, return it; mutate nothing.
2. Compute additions with a pure helper `diffNewBackends(known map[string]bool, cfg)`
   returning specs whose name is not already registered.
3. For each addition:
   - external -> `buildExternal` -> `registry.Register`.
   - persistent -> `buildPersistent` -> `registry.Register` (lazy).
   - swap -> if group exists, `group.AddMember(spec)` + `registry.RegisterAlias`;
     else `newGroup(...)` with this as sole + default member -> `registry.Register`
     + member aliases.
4. Push the grown backend list + aliases into `admin.Handlers` via its setter.
5. Return `{Added, Skipped}`.

A small `liveState` struct (mutex + `groups map[string]*Group` + `persistents` +
`backends`) holds what both reload and shutdown touch, so the two goroutines do
not race.

## mlxctl side

- `add`: after a successful config write, if not `--no-reload`, call
  `POST /v1/reload` best-effort.
  - Success: print `reloaded mlxd (added: <names>)`.
  - Socket missing / connection refused: print
    `mlxd not running; takes effect on next start` and exit 0.
- `--no-reload` flag on `add`.
- New `mlxctl reload` command for the standalone case, printing added/skipped.

## Limitations (stated, not hidden)

- Additive only. Removing or editing a backend still needs a restart. Reload
  output and `add` help text say so.
- Reloaded backends load lazily; no eager worker spawn.

## Testing

- `router.Registry`: concurrent `Register` + `Resolve` under the race detector.
- `Group.AddMember`: add a member, `EnsureLoaded` routes to it; `Members()` is
  race-free.
- `diffNewBackends`: pure unit over old-set + new-config covering new persistent,
  new external, new swap group, new member into existing group, and dup skip.
- `admin` reload handler: returns added names on success; no mutation on bad
  config.
- `mlxctl add`: best-effort reload degrades cleanly when the socket is absent.

## Out of scope

- Full reconcile (remove/update). Config file watch (fsnotify). Eager spawn.
  Direct in-memory registration that bypasses the config file.
