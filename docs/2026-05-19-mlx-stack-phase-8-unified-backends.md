# mlx-stack Phase 8 Implementation Plan — Unified Backend Schema + CLI

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans`.

**Goal:** Collapse the typed config (`[chat]` / `[tags]` / `[embed]` / `[tts]` / `[kokoro]`) into a single `[[backend]]` array-of-tables, where each entry is a named model with `engine` and `mode`. CLI becomes uniform: every backend is addressed by name through the same `start|stop|restart|swap|status|monitor|tail` commands.

**Architecture:** Replace `ChatSwap` and `Managed` with three concrete `Backend` implementations sharing a common interface — `Group` (multi-member swap-on-shared-port), `Persistent` (always-on), `External` (URL-only proxy). The router Registry maps `name → Backend`; the request handler calls `Backend.EnsureLoaded(ctx, name)` before proxying. The CLI's existing subcommands accept any backend name.

**Tech stack:** No new deps. Same Go + Python as before.

**Out of scope:** UI / dashboard. New backend kinds beyond the existing four engines.

**Compat:** Hard cutover. Old typed config no longer accepted (validation errors point to migrate). `mlxctl config migrate` generates the new shape from either today's typed TOML or the legacy zsh `.conf`.

---

## New TOML schema

```toml
log_dir     = "~/.logs/mlx"
models_root = "~/mlx-models"
python_bin  = "~/venvs/mlx/bin/python"

[router]
host        = "127.0.0.1"
port        = 1230
extra_ports = [8080]

# ---- Backend defaults (applied unless overridden per-backend) ----
[defaults.cache]
limit_bytes           = 2_147_483_648
clear_interval_sec    = 60
clear_threshold_bytes = 1_073_741_824

[defaults.watchdog]
kv_headroom_bytes  = 8_000_000_000
check_interval_sec = 30
grace_sec          = 90

[defaults.memlog]
interval_sec = 300

# ---- Backends ----

# Swap group "chat": multiple profiles, one loaded at a time on port 1234.
[[backend]]
name        = "valkyrie"
engine      = "lm"
mode        = "swap"
group       = "chat"
host        = "127.0.0.1"
port        = 1234
model       = "~/mlx-models/valkyrie"
default     = true              # the auto-started member of group "chat"

[[backend]]
name        = "scout"
engine      = "vlm"
mode        = "swap"
group       = "chat"
host        = "127.0.0.1"
port        = 1234
model       = "~/mlx-models/scout"

[[backend]]
name        = "anubis"
engine      = "lm"
mode        = "swap"
group       = "chat"
host        = "127.0.0.1"
port        = 1234
model       = "~/mlx-models/anubis"
draft_model = "~/mlx-models/anubis-draft"

# Persistent: always-on, single worker.
[[backend]]
name   = "qwen-tags"
engine = "vlm"
mode   = "persistent"
host   = "127.0.0.1"
port   = 1235
model  = "~/mlx-models/qwen-tags"
  [backend.watchdog]            # override for this one
  kv_headroom_bytes = 4_000_000_000

[[backend]]
name   = "embed"
engine = "embed"
mode   = "persistent"
host   = "127.0.0.1"
port   = 1236
model  = "nomic-ai/nomic-embed-text-v1.5"

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

# External: just a URL.
[[backend]]
name           = "remote-embed"
mode           = "external"
url            = "http://other-mac.lan:1236"
upstream_model = "nomic-ai/nomic-embed-text-v1.5"
```

**Schema rules:**

- Each `name` unique.
- `mode = "swap"`: requires `group`, `host`, `port`, `model`, `engine ∈ {lm,vlm}`. All swap members of a group must have identical `host`+`port`. One member per group should have `default = true` (or the first listed is implicitly default).
- `mode = "persistent"`: requires `host`, `port`, `engine`. `model` required unless `engine = "audio"` (audio multiplexes per-request).
- `mode = "external"`: requires `url`. `upstream_model` defaults to `name`.
- `engine ∈ {lm, vlm, audio, embed}`.
- `[defaults.*]` applies to every backend; per-backend `[backend.cache]`/`[backend.watchdog]`/`[backend.memlog]` tables override individual fields.

---

## File structure

Phase 8 doesn't add many new files — it consolidates supervisor types and rewires the daemon entry. New / modified:

```
internal/config/
  schema.go               # rewritten — [[backend]] array, defaults, BackendSpec type
  schema_test.go          # rewritten
  loader.go               # apply defaults to each backend; ~-expand model paths
  loader_test.go          # rewritten

internal/backend/
  backend.go              # rewritten — Backend interface + Spec type + helpers

internal/supervisor/
  group.go                # NEW — Group manages swap members on shared port (replaces chatswap.go)
  group_test.go           # NEW
  persistent.go           # NEW — refactor of Managed (renamed)
  persistent_test.go      # NEW
  external.go             # renamed: keeps ExternalAdapter behavior, dropped wrapper of backend.External
  chatswap.go             # DELETED
  chatswap_test.go        # DELETED
  managed.go              # DELETED
  managed_test.go         # DELETED
  satisfies.go            # updated

internal/router/
  registry.go             # NEW shape — map[string]Backend
  registry_test.go        # rewritten
  server.go               # handleProxyByModel calls Backend.EnsureLoaded
  catalog.go              # lists all backend names

internal/admin/
  handlers.go             # uniform /v1/start /v1/stop /v1/restart /v1/swap {name}
  handlers_test.go        # rewritten

cmd/mlxd/main.go          # build backends from cfg, register, start persistents
cmd/mlxctl/
  main.go                 # subcommands take <name>, drop "chat" special-case
  monitor.go              # accept optional <name>
  tail.go                 # accept optional <name>
  list.go                 # NEW — `mlxctl list`
  render.go               # update for new status shape
  config.go               # migrate emits [[backend]] arrays

testdata/fakemlx/main.go  # unchanged
e2e/e2e_test.go           # rewritten configs throughout
```

---

## Task 1: New config schema + Backend spec type

**Files:**
- Rewrite: `internal/config/schema.go`
- Rewrite: `internal/config/schema_test.go`

### Step 1: Replace `internal/config/schema.go` with:

```go
package config

import (
	"fmt"
	"strings"
)

type Config struct {
	LogDir     string        `toml:"log_dir"`
	ModelsRoot string        `toml:"models_root"`
	PythonBin  string        `toml:"python_bin"`
	Router     Router        `toml:"router"`
	Defaults   Defaults      `toml:"defaults"`
	Backends   []BackendSpec `toml:"backend"`
}

type Router struct {
	Host           string   `toml:"host"`
	Port           int      `toml:"port"`
	ExtraPorts     []int    `toml:"extra_ports"`
	AllowedOrigins []string `toml:"allowed_origins"`
}

type Defaults struct {
	Cache    Cache    `toml:"cache"`
	Watchdog Watchdog `toml:"watchdog"`
	Memlog   Memlog   `toml:"memlog"`
}

// BackendSpec is one entry in the [[backend]] array.
type BackendSpec struct {
	Name          string   `toml:"name"`
	Engine        string   `toml:"engine"`
	Mode          string   `toml:"mode"`          // "swap" | "persistent" | "external"
	Group         string   `toml:"group"`         // required when mode=swap; defaults to Name otherwise
	Default       bool     `toml:"default"`       // for swap members: auto-load on start
	Host          string   `toml:"host"`
	Port          int      `toml:"port"`
	Model         string   `toml:"model"`
	DraftModel    string   `toml:"draft_model"`
	URL           string   `toml:"url"`           // mode=external
	UpstreamModel string   `toml:"upstream_model"`// mode=external
	Cache         *Cache   `toml:"cache"`         // optional override (nil = use defaults)
	Watchdog      *Watchdog`toml:"watchdog"`
	Memlog        *Memlog  `toml:"memlog"`
}

type Cache struct {
	LimitBytes          int64 `toml:"limit_bytes"`
	ClearIntervalSec    int   `toml:"clear_interval_sec"`
	ClearThresholdBytes int64 `toml:"clear_threshold_bytes"`
}

type Watchdog struct {
	KVHeadroomBytes  int64 `toml:"kv_headroom_bytes"`
	CheckIntervalSec int   `toml:"check_interval_sec"`
	GraceSec         int   `toml:"grace_sec"`
}

type Memlog struct {
	IntervalSec int `toml:"interval_sec"`
}

// EffectiveCache returns the merged Cache (per-backend override on top of defaults).
func (b BackendSpec) EffectiveCache(d Defaults) Cache {
	if b.Cache == nil { return d.Cache }
	return *b.Cache
}
func (b BackendSpec) EffectiveWatchdog(d Defaults) Watchdog {
	if b.Watchdog == nil { return d.Watchdog }
	return *b.Watchdog
}
func (b BackendSpec) EffectiveMemlog(d Defaults) Memlog {
	if b.Memlog == nil { return d.Memlog }
	return *b.Memlog
}

func (c *Config) Validate() error {
	if c.PythonBin == "" {
		return fmt.Errorf("python_bin: required")
	}
	if c.Router.Port <= 0 || c.Router.Port > 65535 {
		return fmt.Errorf("router.port: must be 1..65535, got %d", c.Router.Port)
	}
	for _, p := range c.Router.ExtraPorts {
		if p <= 0 || p > 65535 {
			return fmt.Errorf("router.extra_ports: %d out of range", p)
		}
	}
	if len(c.Backends) == 0 {
		return fmt.Errorf("at least one [[backend]] entry required")
	}
	seen := map[string]bool{}
	groupPorts := map[string]int{}
	groupDefaults := map[string]int{}
	for i, b := range c.Backends {
		idx := fmt.Sprintf("backend[%d:%s]", i, b.Name)
		if b.Name == "" {
			return fmt.Errorf("%s: name required", idx)
		}
		if seen[b.Name] {
			return fmt.Errorf("%s: duplicate name", idx)
		}
		seen[b.Name] = true
		switch b.Mode {
		case "swap":
			if b.Engine != "lm" && b.Engine != "vlm" {
				return fmt.Errorf("%s.engine: must be 'lm' or 'vlm' for swap mode, got %q", idx, b.Engine)
			}
			if b.Model == "" { return fmt.Errorf("%s.model: required", idx) }
			if b.Host == "" || b.Port <= 0 { return fmt.Errorf("%s: host+port required", idx) }
			group := b.Group
			if group == "" { return fmt.Errorf("%s.group: required for swap mode", idx) }
			if p, ok := groupPorts[group]; ok && p != b.Port {
				return fmt.Errorf("%s.port: swap members of group %q must share a port (got %d vs %d)", idx, group, b.Port, p)
			}
			groupPorts[group] = b.Port
			if b.Default { groupDefaults[group]++ }
		case "persistent":
			if !strings.Contains("lm vlm audio embed", b.Engine) {
				return fmt.Errorf("%s.engine: must be lm|vlm|audio|embed, got %q", idx, b.Engine)
			}
			if b.Host == "" || b.Port <= 0 { return fmt.Errorf("%s: host+port required", idx) }
			if b.Engine != "audio" && b.Model == "" {
				return fmt.Errorf("%s.model: required for engine=%s", idx, b.Engine)
			}
		case "external":
			if b.URL == "" { return fmt.Errorf("%s.url: required for external mode", idx) }
		default:
			return fmt.Errorf("%s.mode: must be 'swap', 'persistent', or 'external', got %q", idx, b.Mode)
		}
	}
	// At most one default per group; if zero, the first member is implicitly default.
	for g, n := range groupDefaults {
		if n > 1 {
			return fmt.Errorf("group %q: only one backend may set default=true", g)
		}
	}
	return nil
}

// BackendsByGroup returns swap-mode backends grouped by Group name, preserving
// declaration order within each group.
func (c *Config) BackendsByGroup() map[string][]BackendSpec {
	out := map[string][]BackendSpec{}
	for _, b := range c.Backends {
		if b.Mode == "swap" {
			out[b.Group] = append(out[b.Group], b)
		}
	}
	return out
}

// Persistents returns backends with mode=persistent in declaration order.
func (c *Config) Persistents() []BackendSpec {
	out := []BackendSpec{}
	for _, b := range c.Backends {
		if b.Mode == "persistent" { out = append(out, b) }
	}
	return out
}

// Externals returns backends with mode=external in declaration order.
func (c *Config) Externals() []BackendSpec {
	out := []BackendSpec{}
	for _, b := range c.Backends {
		if b.Mode == "external" { out = append(out, b) }
	}
	return out
}
```

### Step 2: Rewrite `internal/config/schema_test.go`

Cover: minimal valid config (one persistent backend), swap group with shared port, swap group with mismatched ports (error), duplicate name (error), invalid mode, invalid engine, missing url for external, model required for non-audio persistent, multiple defaults per group (error), defaults merge.

```go
package config

import (
	"strings"
	"testing"
)

func minCfg() *Config {
	return &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1230},
		Backends: []BackendSpec{
			{Name: "embed", Engine: "embed", Mode: "persistent", Host: "127.0.0.1", Port: 1236, Model: "/m"},
		},
	}
}

func TestValidate_MinimalOK(t *testing.T) {
	if err := minCfg().Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidate_SwapGroupSharedPort(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends,
		BackendSpec{Name: "valkyrie", Engine: "lm", Mode: "swap", Group: "chat", Host: "127.0.0.1", Port: 1234, Model: "/m/v", Default: true},
		BackendSpec{Name: "scout",    Engine: "vlm", Mode: "swap", Group: "chat", Host: "127.0.0.1", Port: 1234, Model: "/m/s"},
	)
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidate_SwapGroupMismatchedPort(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends,
		BackendSpec{Name: "a", Engine: "lm", Mode: "swap", Group: "chat", Host: "127.0.0.1", Port: 1234, Model: "/m"},
		BackendSpec{Name: "b", Engine: "lm", Mode: "swap", Group: "chat", Host: "127.0.0.1", Port: 1235, Model: "/m"},
	)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "must share a port") {
		t.Fatalf("want share-a-port error: %v", err)
	}
}

func TestValidate_DuplicateName(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends, BackendSpec{Name: "embed", Engine: "lm", Mode: "swap", Group: "g", Host: "x", Port: 1, Model: "/m"})
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error: %v", err)
	}
}

func TestValidate_InvalidMode(t *testing.T) {
	c := minCfg()
	c.Backends[0].Mode = "bogus"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("want mode error: %v", err)
	}
}

func TestValidate_ExternalRequiresURL(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends, BackendSpec{Name: "remote", Mode: "external"})
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("want url error: %v", err)
	}
}

func TestValidate_AudioPersistentNoModelOK(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends, BackendSpec{Name: "tts", Engine: "audio", Mode: "persistent", Host: "127.0.0.1", Port: 1237})
	if err := c.Validate(); err != nil { t.Fatalf("unexpected: %v", err) }
}

func TestValidate_TwoSwapDefaultsInGroup(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends,
		BackendSpec{Name: "a", Engine: "lm", Mode: "swap", Group: "g", Host: "127.0.0.1", Port: 1, Model: "/m", Default: true},
		BackendSpec{Name: "b", Engine: "lm", Mode: "swap", Group: "g", Host: "127.0.0.1", Port: 1, Model: "/m", Default: true},
	)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "default=true") {
		t.Fatalf("want default error: %v", err)
	}
}

func TestEffectiveOverrides(t *testing.T) {
	defaults := Defaults{
		Cache:    Cache{LimitBytes: 1000},
		Watchdog: Watchdog{KVHeadroomBytes: 2000},
	}
	b := BackendSpec{Watchdog: &Watchdog{KVHeadroomBytes: 99}}
	if b.EffectiveCache(defaults).LimitBytes != 1000 {
		t.Errorf("default cache lost")
	}
	if b.EffectiveWatchdog(defaults).KVHeadroomBytes != 99 {
		t.Errorf("override lost")
	}
}
```

### Step 3: Run, commit

```
go test ./internal/config/... -v
git add internal/config/schema.go internal/config/schema_test.go
git commit -m "feat(config): unified [[backend]] schema + validation"
```

---

## Task 2: Loader expand-home + defaults application

**Files:**
- Rewrite: `internal/config/loader.go`
- Rewrite: `internal/config/loader_test.go`

Loader now:
1. Decodes TOML.
2. Rejects unknown keys.
3. Expands `~` in `LogDir`, `ModelsRoot`, `PythonBin`, and every backend's `Model`+`DraftModel`+`URL` (URL doesn't have `~` but skip-it logic is in expandHome).
4. Fills in default Mode (`swap` members without Mode-stated default? no — Mode is required).
5. Validates.

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

func Load(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("config load %s: %w", path, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		keys := make([]string, 0, len(u))
		for _, k := range u { keys = append(keys, k.String()) }
		return nil, fmt.Errorf("config %s: unknown keys: %s", path, strings.Join(keys, ", "))
	}

	c.LogDir = expandHome(c.LogDir)
	c.ModelsRoot = expandHome(c.ModelsRoot)
	c.PythonBin = expandHome(c.PythonBin)
	for i := range c.Backends {
		c.Backends[i].Model = expandHome(c.Backends[i].Model)
		c.Backends[i].DraftModel = expandHome(c.Backends[i].DraftModel)
		// Default mode-specific fields.
		if c.Backends[i].Mode == "swap" && c.Backends[i].Group == "" {
			c.Backends[i].Group = c.Backends[i].Name
		}
		if c.Backends[i].Mode == "external" && c.Backends[i].UpstreamModel == "" {
			c.Backends[i].UpstreamModel = c.Backends[i].Name
		}
	}

	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &c, nil
}

func expandHome(p string) string {
	if p == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil { return p }
	if p == "~" { return home }
	if strings.HasPrefix(p, "~/") { return filepath.Join(home, p[2:]) }
	return p
}
```

Tests: load a TOML file with several backends, verify expansion, verify defaults merging, verify unknown-key rejection.

Run + commit:

```
go test ./internal/config/...
git add internal/config/loader.go internal/config/loader_test.go
git commit -m "feat(config): loader for unified backend schema"
```

---

## Task 3: Backend interface + types

**Files:**
- Rewrite: `internal/backend/backend.go`

```go
package backend

import (
	"context"
	"sync"
)

// Backend is the unified lifecycle interface for all named backends.
type Backend interface {
	Name() string
	Group() string
	Mode() string           // "swap" | "persistent" | "external"
	Engine() string         // "lm" | "vlm" | "audio" | "embed" | ""
	BaseURL() string
	UpstreamModel() string  // what to put in the "model" field upstream
	Running() bool
	PID() int

	// EnsureLoaded prepares the backend to serve a request for `name` (which
	// may be the backend's own Name or, for swap groups, any group member).
	// For persistent: ensures the worker is up. For swap: loads the named
	// member onto the shared port. For external: no-op.
	EnsureLoaded(ctx context.Context, name string) error

	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// State holds per-backend live state (PID, URL, currently-loaded swap member).
type State struct {
	Mu             sync.Mutex
	Current        string  // for swap groups: name of currently-loaded member
	WorkerPID      int
	WorkerURL      string
}
```

Commit:

```
git add internal/backend/backend.go
git commit -m "feat(backend): unified Backend interface"
```

---

## Task 4: supervisor.Persistent (rename Managed)

**Files:**
- Create: `internal/supervisor/persistent.go` (copy of managed.go with rename)
- Create: `internal/supervisor/persistent_test.go`
- Delete: `internal/supervisor/managed.go`, `managed_test.go`

Conceptually: rename `Managed` → `Persistent`. Add `Name() string`, `Group() string` (returns Name for persistent — every persistent is its own group), `Mode() string` (returns "persistent"), `Engine() string`. Implement `EnsureLoaded` as a no-op when already running, or `Start` when not.

```go
type Persistent struct { ... }

func (p *Persistent) Name() string  { return p.opts.Name }
func (p *Persistent) Group() string { return p.opts.Name }
func (p *Persistent) Mode() string  { return "persistent" }
func (p *Persistent) Engine() string { return p.opts.Engine }
// existing BaseURL/UpstreamModel/Running/PID/Start/Stop carry over.

func (p *Persistent) EnsureLoaded(ctx context.Context, name string) error {
	if name != p.opts.Name {
		return fmt.Errorf("persistent backend %q can't serve %q", p.opts.Name, name)
	}
	if p.Running() {
		return nil
	}
	return p.Start(ctx)
}
```

`ManagedOpts` becomes `PersistentOpts` and gains an `Engine` field.

Move all `Managed_*` tests to `persistent_test.go` with renames.

```
git add internal/supervisor/persistent.go internal/supervisor/persistent_test.go
git rm internal/supervisor/managed.go internal/supervisor/managed_test.go
git commit -m "refactor(supervisor): rename Managed to Persistent + EnsureLoaded"
```

---

## Task 5: supervisor.Group (replaces ChatSwap)

**Files:**
- Create: `internal/supervisor/group.go`
- Create: `internal/supervisor/group_test.go`
- Delete: `internal/supervisor/chatswap.go`, `chatswap_test.go`

`Group` is a renamed/generalized `ChatSwap`. Holds a list of `BackendSpec` swap members. `EnsureLoaded(ctx, name)` locks, kills the current worker (if any) when name differs, spawns the new one, probes. `Name()` returns the group's name. `BaseURL()` returns the shared port URL. `UpstreamModel()` is dynamic: returns the current member's Model.

```go
type GroupOpts struct {
	Name          string
	Host          string
	Port          int
	Members       map[string]config.BackendSpec  // name → spec
	DefaultMember string
	SwapTimeoutSec int
	ProbeInterval time.Duration
	WorkerFactory func(spec config.BackendSpec) *Worker
}

type Group struct {
	opts GroupOpts
	mu sync.Mutex
	current string
	worker  *Worker
	state   *backend.State
}

func (g *Group) Name() string  { return g.opts.Name }
func (g *Group) Group() string { return g.opts.Name }
func (g *Group) Mode() string  { return "swap" }
func (g *Group) Engine() string {
	// best-effort: report current member's engine, or first member's if none loaded
	if g.current != "" { return g.opts.Members[g.current].Engine }
	for _, m := range g.opts.Members { return m.Engine }
	return ""
}
func (g *Group) BaseURL() string { return fmt.Sprintf("http://%s:%d", g.opts.Host, g.opts.Port) }
func (g *Group) UpstreamModel() string {
	if g.current == "" { return "" }
	return g.opts.Members[g.current].Model
}
func (g *Group) Running() bool { ... }
func (g *Group) PID() int      { ... }
func (g *Group) EnsureLoaded(ctx context.Context, name string) error {
	// existing ChatSwap.EnsureProfile body, generalized.
}
func (g *Group) Start(ctx context.Context) error {
	return g.EnsureLoaded(ctx, g.opts.DefaultMember)
}
func (g *Group) Stop(ctx context.Context) error { ... }
```

Tests cover: swap from A to B kills A's worker, concurrent EnsureLoaded for same name spawns once, unknown name errors, Stop clears state.

```
git add internal/supervisor/group.go internal/supervisor/group_test.go
git rm internal/supervisor/chatswap.go internal/supervisor/chatswap_test.go
git commit -m "refactor(supervisor): Group replaces ChatSwap for any swap-mode collection"
```

---

## Task 6: supervisor.External

**Files:**
- Rewrite: `internal/supervisor/external.go`

Drop the wrapper-of-`backend.External` pattern. `External` is a plain struct that implements `Backend`:

```go
type External struct {
	NameValue          string
	URLValue           string
	UpstreamModelValue string
}

func (e *External) Name() string         { return e.NameValue }
func (e *External) Group() string        { return e.NameValue }
func (e *External) Mode() string         { return "external" }
func (e *External) Engine() string       { return "" }
func (e *External) BaseURL() string      { return e.URLValue }
func (e *External) UpstreamModel() string { return e.UpstreamModelValue }
func (e *External) Running() bool        { return true }
func (e *External) PID() int             { return 0 }
func (e *External) EnsureLoaded(_ context.Context, _ string) error { return nil }
func (e *External) Start(_ context.Context) error { return nil }
func (e *External) Stop(_ context.Context) error  { return nil }
```

Tests verify all methods.

```
git add internal/supervisor/external.go internal/supervisor/external_test.go
git commit -m "refactor(supervisor): External is a first-class Backend"
```

---

## Task 7: Router Registry — name → Backend

**Files:**
- Rewrite: `internal/router/registry.go`
- Rewrite: `internal/router/registry_test.go`

```go
package router

import (
	"context"
	"fmt"

	"github.com/guygrigsby/mlx-stack/internal/backend"
)

type Registry struct {
	byName map[string]backend.Backend // direct name → backend
	// For swap groups, every member name also points at the group's Backend.
}

func NewRegistry(backends ...backend.Backend) *Registry {
	r := &Registry{byName: map[string]backend.Backend{}}
	for _, b := range backends {
		r.byName[b.Name()] = b
	}
	return r
}

// RegisterAlias adds an additional name pointing at a backend. Used for swap
// group members so they can be addressed by member name.
func (r *Registry) RegisterAlias(alias string, b backend.Backend) {
	r.byName[alias] = b
}

func (r *Registry) Resolve(ctx context.Context, name string) (backend.Backend, error) {
	b, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("unknown backend %q", name)
	}
	return b, nil
}

func (r *Registry) All() []backend.Backend {
	seen := map[backend.Backend]bool{}
	out := []backend.Backend{}
	for _, b := range r.byName {
		if !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	return out
}

func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	return out
}
```

Tests: register a Group, register its members as aliases, Resolve any returns the Group. Register external. Unknown errors.

```
git add internal/router/registry.go internal/router/registry_test.go
git commit -m "feat(router): name-based registry"
```

---

## Task 8: Router — uniform handleProxyByModel

**Files:**
- Modify: `internal/router/server.go`
- Modify: `internal/router/server_test.go`
- Modify: `internal/router/catalog.go`
- Modify: `internal/router/catalog_test.go`

`handleProxyByModel` becomes:

```go
func (s *Server) handleProxyByModel(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil { http.Error(w, err.Error(), 400); return }
	r.Body = io.NopCloser(bytes.NewReader(body))

	model, err := ExtractModel(body)
	if err != nil { http.Error(w, err.Error(), 400); return }

	b, err := s.registry.Resolve(r.Context(), model)
	if err != nil { http.Error(w, err.Error(), 400); return }

	if err := b.EnsureLoaded(r.Context(), model); err != nil {
		http.Error(w, "ensure: "+err.Error(), 502); return
	}

	upstreamModel := b.UpstreamModel()
	if upstreamModel == "" {
		upstreamModel = model  // audio: pass through
	}
	_ = ProxyJSON(w, r, b.BaseURL(), upstreamModel)
}
```

`Server` drops `chat ChatSwapper` field; only keeps `registry`. `ServerOpts` likewise.

Catalog now iterates `registry.Names()` (or accepts a list from cmd/mlxd).

```go
func (c *Catalog) List() []Model {
	out := make([]Model, 0, len(c.names))
	for _, n := range c.names { out = append(out, Model{ID: n}) }
	return out
}
```

`Catalog` now takes `names []string` at construction.

Update tests with a `fakeBackend` matching the new interface. Drop old `fakeSwap`/`fakeManaged` distinction.

```
git commit -m "feat(router): uniform name-based dispatch via Backend interface"
```

---

## Task 9: cmd/mlxd — build all backends from cfg

**Files:**
- Modify: `cmd/mlxd/main.go`

```go
// Build backends.
var backends []backend.Backend
broker := logobs.NewBroker()

// Group construction
groups := cfg.BackendsByGroup()
for groupName, members := range groups {
	first := members[0]
	defaultMember := first.Name
	memberMap := map[string]config.BackendSpec{}
	for _, m := range members {
		memberMap[m.Name] = m
		if m.Default { defaultMember = m.Name }
	}
	g := supervisor.NewGroup(supervisor.GroupOpts{
		Name:           groupName,
		Host:           first.Host,
		Port:           first.Port,
		Members:        memberMap,
		DefaultMember:  defaultMember,
		SwapTimeoutSec: 90,
		ProbeInterval:  250 * time.Millisecond,
		WorkerFactory: func(spec config.BackendSpec) *supervisor.Worker {
			return supervisor.New(supervisor.WorkerSpec{
				Name:    fmt.Sprintf("%s[%s]", groupName, spec.Name),
				Command: cfg.PythonBin,
				Args:    launcherArgs(spec),
				Env:     backendEnv(spec, cfg.Defaults),
				Broker:  broker,
				Logger:  logger,
			})
		},
	})
	backends = append(backends, g)
}

// Persistent backends
for _, p := range cfg.Persistents() {
	spec := p
	pb := supervisor.NewPersistent(supervisor.PersistentOpts{
		Name:    spec.Name,
		Engine:  spec.Engine,
		Host:    spec.Host,
		Port:    spec.Port,
		UpstreamModel: spec.Model,
		Args:    launcherArgs(spec),
		WorkerFactory: func(args []string) *supervisor.Worker {
			return supervisor.New(supervisor.WorkerSpec{
				Name:    spec.Name,
				Command: cfg.PythonBin,
				Args:    args,
				Env:     backendEnv(spec, cfg.Defaults),
				Broker:  broker,
				Logger:  logger,
			})
		},
	})
	if err := pb.Start(context.Background()); err != nil {
		logger.Error("persistent start failed", "name", spec.Name, "err", err)
	} else {
		backends = append(backends, pb)
	}
}

// External backends
for _, e := range cfg.Externals() {
	backends = append(backends, &supervisor.External{
		NameValue: e.Name, URLValue: e.URL, UpstreamModelValue: e.UpstreamModel,
	})
}

// Build registry with name + member-alias registration
registry := router.NewRegistry(backends...)
for _, b := range backends {
	if b.Mode() == "swap" {
		// Add each member name as an alias for the group
		group := groups[b.Group()]
		for _, m := range group {
			registry.RegisterAlias(m.Name, b)
		}
	}
}
```

Add helpers:

```go
func launcherArgs(spec config.BackendSpec) []string {
	args := []string{"-m", "mlx_stack.launcher_shim", "--engine", spec.Engine,
		"--host", spec.Host, "--port", fmt.Sprintf("%d", spec.Port)}
	if spec.Engine != "audio" {
		args = append(args, "--model", spec.Model)
	}
	if spec.DraftModel != "" {
		args = append(args, "--draft-model", spec.DraftModel)
	}
	return args
}

func backendEnv(spec config.BackendSpec, d config.Defaults) []string {
	cache := spec.EffectiveCache(d)
	wd := spec.EffectiveWatchdog(d)
	ml := spec.EffectiveMemlog(d)
	env := []string{}
	if cache.LimitBytes > 0 { env = append(env, fmt.Sprintf("MLX_CACHE_LIMIT_BYTES=%d", cache.LimitBytes)) }
	if cache.ClearIntervalSec > 0 { env = append(env, fmt.Sprintf("MLX_CACHE_CLEAR_INTERVAL_SEC=%d", cache.ClearIntervalSec)) }
	if cache.ClearThresholdBytes > 0 { env = append(env, fmt.Sprintf("MLX_CACHE_CLEAR_THRESHOLD_BYTES=%d", cache.ClearThresholdBytes)) }
	if wd.KVHeadroomBytes > 0 { env = append(env, fmt.Sprintf("MLX_KV_HEADROOM_BYTES=%d", wd.KVHeadroomBytes)) }
	if wd.CheckIntervalSec > 0 { env = append(env, fmt.Sprintf("MLX_ACTIVE_MEMORY_CHECK_INTERVAL_SEC=%d", wd.CheckIntervalSec)) }
	if wd.GraceSec > 0 { env = append(env, fmt.Sprintf("MLX_ACTIVE_MEMORY_GRACE_SEC=%d", wd.GraceSec)) }
	if ml.IntervalSec > 0 { env = append(env, fmt.Sprintf("MLX_MEMLOG_INTERVAL_SEC=%d", ml.IntervalSec)) }
	return env
}
```

Build the catalog from the union of all backend names + alias names. Build admin Handlers with a unified `Backends []backend.Backend` field.

```
git commit -m "feat(mlxd): construct backends from unified config"
```

---

## Task 10: Admin handlers — uniform name-based actions

**Files:**
- Rewrite: `internal/admin/handlers.go`
- Rewrite: `internal/admin/handlers_test.go`

```go
type BackendController interface {
	Backends() []backend.Backend
	ByName(name string) (backend.Backend, bool)
}

type Handlers struct {
	Config    *config.Config
	Backends  BackendController
	Broker    *logobs.Broker
	ObsStore  *obsstate.Store
}

type BackendStatus struct {
	Name        string  `json:"name"`
	Group       string  `json:"group"`
	Mode        string  `json:"mode"`
	Engine      string  `json:"engine"`
	URL         string  `json:"url"`
	Running     bool    `json:"running"`
	PID         int     `json:"pid"`
	CurrentName string  `json:"current_name,omitempty"`  // for swap groups
}

type StatusResponse struct {
	Backends []BackendStatus           `json:"backends"`
	Workers  map[string]obsstate.WorkerObs `json:"workers,omitempty"`
}
```

Endpoints (all take `{"name": "..."}`):

```
POST /v1/start    {name}   → call EnsureLoaded(name) on the resolved backend
POST /v1/stop     {name}   → call Stop on the backend
POST /v1/restart  {name}   → Stop then Start
POST /v1/swap     {name}   → alias for /v1/start (kept for muscle memory)
GET  /v1/status            → BackendStatus list
GET  /v1/status?name=X     → single backend's status
GET  /v1/health
GET  /v1/logs/tail
```

Resolution: the controller's `ByName` looks up by backend name OR group-member name; if member name, it returns the Group.

Tests cover each endpoint with a `fakeBackendController`.

```
git commit -m "feat(admin): uniform name-based actions across all backends"
```

---

## Task 11: mlxctl — uniform subcommands + `list`

**Files:**
- Modify: `cmd/mlxctl/main.go`
- Create: `cmd/mlxctl/list.go`
- Modify: `cmd/mlxctl/render.go`

Subcommands accept any backend name:

```
mlxctl list                          # all backends, with name | group | mode | engine | url | running
mlxctl status [name]                 # all backends or one
mlxctl start <name>
mlxctl stop  <name>
mlxctl restart <name>
mlxctl swap <name>                   # alias for start (just for muscle memory)
mlxctl monitor [name]                # optional filter
mlxctl tail [name]                   # optional filter (worker-name match in event.Worker)
mlxctl chat "..."                    # unchanged
mlxctl tags                          # unchanged (just lists /v1/models)
mlxctl health
mlxctl config migrate|show
```

`cmd/mlxctl/list.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
)

func cmdList(args []string) {
	c := newClient()
	b, err := c.Get(context.Background(), "/v1/status")
	if err != nil { notRunning() }
	var resp struct {
		Backends []struct {
			Name        string `json:"name"`
			Group       string `json:"group"`
			Mode        string `json:"mode"`
			Engine      string `json:"engine"`
			URL         string `json:"url"`
			Running     bool   `json:"running"`
			PID         int    `json:"pid"`
			CurrentName string `json:"current_name"`
		} `json:"backends"`
	}
	json.Unmarshal(b, &resp)
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tGROUP\tMODE\tENGINE\tURL\tRUNNING\tPID\tCURRENT")
	for _, bk := range resp.Backends {
		current := bk.CurrentName
		if current == "" { current = "-" }
		running := "no"
		if bk.Running { running = "yes" }
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			bk.Name, bk.Group, bk.Mode, bk.Engine, bk.URL, running, bk.PID, current)
	}
	tw.Flush()
}
```

Update `cmdSwap`/`cmdStart`/`cmdStop`/`cmdRestart` to accept `<name>` (not `<backend>` keyword):

```go
case "start":
    cmdStart(os.Args[2:])  // arg is the backend name now
```

```go
func cmdStart(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mlxctl start <name>")
		os.Exit(2)
	}
	body, _ := json.Marshal(map[string]string{"name": args[0]})
	resp, err := newClient().PostJSON(context.Background(), "/v1/start", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start failed: %v\n%s\n", err, resp)
		os.Exit(1)
	}
	fmt.Println(string(resp))
}
```

`mlxctl tail [name]` filters: SSE lines have `worker=…` tagging; client greps. Use a simple `strings.Contains(line, "name=<name>")` or parse the JSON-ish prefix.

Update `render.go` for the new status shape (Backends array, Workers map).

```
git commit -m "feat(mlxctl): uniform name-based subcommands + list"
```

---

## Task 12: config migrate emits [[backend]] format

**Files:**
- Modify: `cmd/mlxctl/config.go`

Rewrite `buildConfig`: walk the legacy env+profiles, emit:
- One `[[backend]]` per chat profile (mode=swap, group=chat, port=CHAT_PORT, default for CHAT_PROFILE_DEFAULT).
- One `[[backend]]` for tags (mode=persistent).
- One `[[backend]]` for embed (mode=persistent or external based on legacy env).
- One `[[backend]]` for tts.
- One `[[backend]]` for kokoro.
- `[defaults.*]` from CHAT_* cache/watchdog/memlog vars (since chat is the historical default home for these knobs).

Verify by loading the migrated TOML in `mlxd`.

```
./bin/mlxctl config migrate ~/.config/mlx.conf > /tmp/m.toml
go run ./cmd/mlxd run --config /tmp/m.toml --socket /tmp/test.sock 2>&1 | head -2
```

```
git commit -m "feat(mlxctl): config migrate emits unified [[backend]] format"
```

---

## Task 13: e2e — adapt fixtures to new schema

**Files:**
- Rewrite: `e2e/e2e_test.go`

Each test now writes TOML in the new format:

```toml
log_dir     = "..."
python_bin  = "..."
[router]
host = "127.0.0.1"
port = $ROUTER_PORT

[[backend]]
name = "p1"
mode = "swap"
group = "chat"
engine = "lm"
host = "127.0.0.1"
port = $CHAT_PORT
model = "/tmp/p1"
default = true

[[backend]]
name = "p2"
mode = "swap"
group = "chat"
engine = "lm"
host = "127.0.0.1"
port = $CHAT_PORT
model = "/tmp/p2"
```

Add a new test `TestE2E_BackendList` that hits `/v1/status` and verifies the new shape. Existing tests update to use the new TOML.

```
go test ./e2e/... -v -timeout 120s
```

Commit.

---

## Task 14: README + smoke against real model

- Update `README.md` to document the new schema with an example config.
- Update `mlxctl --help` text.
- Rerun the manual smoke test from Phase 1 against `eva-6bit` using the new schema.

```
git commit -m "docs: README for unified backend schema"
```

---

## Acceptance

- `mlxctl list` shows every configured backend in one table.
- `mlxctl start qwen-tags` / `mlxctl start valkyrie` / `mlxctl start embed` all work uniformly.
- `mlxctl status valkyrie` shows just that backend's row plus its worker telemetry.
- Old config formats are rejected with an error pointing to `mlxctl config migrate`.
- Full e2e + Python suite green.

## What we still don't have (carry forward)

- Per-backend health-probe for `mode=external`.
- Per-backend resource limits beyond cache/watchdog/memlog (CPU pinning, memory limit).
- Web dashboard.
- Multi-host orchestration (running mlxd on two Macs and load-balancing).
