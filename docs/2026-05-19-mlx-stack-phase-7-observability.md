# mlx-stack Phase 7 Implementation Plan — Observability Polish

> **Status (2026-05-22):** shipped. Status table, monitor, and tail are wired to `obsstate` + `logobs`. CLI subsequently moved to spf13/cobra; the subcommand wiring shown below no longer matches the code, but the behavior is preserved.

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans`.

**Goal:** Make `mlxctl status` and `mlxctl monitor` actually useful: pretty table, latest mem snapshot per worker, latest request timing, exit history. Add daily log rotation in mlxd.

---

## Task 1: Per-worker state aggregator

**Files:**
- Create: `internal/obsstate/state.go`
- Create: `internal/obsstate/state_test.go`

Background goroutine subscribes to the `logobs.Broker` and keeps the last N events of each kind per worker. Exposes a snapshot read.

```go
type WorkerObs struct {
	Name         string
	LatestMem    *logobs.MemSnapshot
	LatestTiming *logobs.Timing
	LastWatchdog *logobs.WatchdogEvent
	RecentTiming []logobs.Timing  // ring buffer, size 10
	Updated      time.Time
}

type Store struct {
	mu      sync.Mutex
	workers map[string]*WorkerObs
}

func (s *Store) Apply(name string, ev logobs.Event) { ... }
func (s *Store) Snapshot() map[string]WorkerObs { ... }
```

Each worker spec includes a `Name` already; the broker should publish events tagged with the worker name. **Change `logobs.Event` to carry an optional `Worker` field**, or change the broker to carry `(name, event)` tuples.

Choose: extend `Worker.consumeStderr` to publish `obsstate.Event{Worker: spec.Name, Ev: parsedEvent}` to a typed obsstate broker. Or: add `Worker` to the `logobs.Event` struct.

**Recommended:** add `Worker string` field to `logobs.Event` (set by the Worker before publishing).

Tests cover: Apply mem → snapshot has LatestMem; Apply timing → ring buffer fills; Apply watchdog → LastWatchdog set.

Commit:

```
git add internal/obsstate/
git commit -m "feat(obsstate): per-worker event aggregator"
```

---

## Task 2: mlxd wires obsstate.Store

**Files:**
- Modify: `cmd/mlxd/main.go`
- Modify: `internal/admin/handlers.go`

Construct `store := obsstate.New()`. Pass to admin Handlers. Have a goroutine subscribe to the broker and call `store.Apply`. Admin /v1/status reads from the store.

`StatusResponse` gains a `Workers map[string]WorkerObs` field.

Commit.

---

## Task 3: mlxctl status — pretty table

**Files:**
- Create: `cmd/mlxctl/render.go`
- Modify: `cmd/mlxctl/main.go`

Replace the JSON dump with a column-aligned table:

```
WORKER       PROFILE    PID     URL                       MEM(active/cache/peak)   LAST TIMING
chat         valkyrie   12345   http://127.0.0.1:1234     8.2G / 0.5G / 9.0G       req=abc prefill=120ms@10tps decode=850ms@45tps
tags         qwen-tags  12346   http://127.0.0.1:1235     2.1G / 0.0G / 2.2G       (none)
embed        embed      12347   http://127.0.0.1:1236     0.3G / 0.0G / 0.3G       (none)
```

Use `text/tabwriter` from stdlib. Test by feeding canned JSON to the renderer and snapshot-comparing the output.

Commit.

---

## Task 4: mlxctl monitor — refresh

**Files:**
- Modify: `cmd/mlxctl/monitor.go`

Use the same renderer. Clear screen + redraw every 500ms. Add a footer hint "press Ctrl-C to exit".

Commit.

---

## Task 5: Daily log rotation

**Files:**
- Modify: `cmd/mlxd/main.go`
- Create: `internal/logrot/rotator.go`

mlxd should write to `~/.logs/mlx/mlxd-YYYY-MM-DD.log`. At midnight (or on next write after midnight), close the current file and open a new one. Implement a rotating slog handler:

```go
type Rotator struct {
	dir    string
	prefix string
	mu     sync.Mutex
	cur    *os.File
	day    string
}

func New(dir, prefix string) *Rotator { ... }
func (r *Rotator) Write(p []byte) (int, error) {
	// check date; if changed, swap files; write to current.
}
```

Wrap into `slog.NewTextHandler(rotator, ...)`. Tests verify rotation by injecting a clock.

Commit.

---

## Acceptance

- `mlxctl status` produces a readable table.
- `mlxctl monitor` refreshes the table in place every 500ms.
- mlxd writes `~/.logs/mlx/mlxd-2026-05-19.log` and rolls at midnight.

---

# Final acceptance for Phases 1-7

After Phase 7 lands, the project should:

- Run with `mlxd run` and serve real models on ports 1230 + 8080.
- Expose `mlxctl status / monitor / tail / swap / start / stop / restart / chat / tag / tags / health / config migrate / config show`.
- Self-restart audio/tags/embed workers on unexpected exit.
- Hot-swap chat profiles in-process.
- Survive KV-cache growth via execv watchdog.
- Be installable via `make install` + `make install-launchd`.
- Have no dependency on the legacy zsh/Python scripts.
