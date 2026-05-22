# mlx-stack Phase 2 Implementation Plan — Tags Backend (always-on Managed)

> **Status (2026-05-22):** shipped. `Managed` was later renamed `Persistent` in phase 8, and the typed `[tags]` schema described here was replaced by a `mode = "persistent"` entry in the unified `[[backend]]` array. CLI is `mlxctl` (renamed in phase 5). Current schema: see phase 8 + README.

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement task-by-task.

**Goal:** Add a second backend class — always-on `Managed` — and wire it up so the `tags` profile (an always-loaded VLM for image tagging) is supervised, restarts on exit with exponential backoff, and routes alongside `chat`.

**Architecture:** Generalize the supervisor: factor `Managed` out as a sibling of `ChatSwap` that owns one worker for its lifetime and respawns it (with backoff) on unexpected exit. Promote the router from "knows only chat" to "knows a `BackendRegistry`" — a map of `alias → Backend` (chat profiles map to the single chatswap backend; tags maps to the managed backend). The router still calls `EnsureProfile` for chat aliases but `EnsureRunning` / `BaseURL` / `UpstreamModel` for tags.

**Tech stack:** Same as Phase 1.

**Out of scope:** Embed, audio. External backend variant for tags (always managed for now).

---

## Task 1: Config — add [tags] section

**Files:**
- Modify: `internal/config/schema.go`
- Modify: `internal/config/schema_test.go`
- Modify: `internal/config/loader_test.go` (small fixture update)

- [ ] **Step 1: Failing test in schema_test.go**

Append:

```go
func TestValidate_TagsOK(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat:      minChat(),
		Tags: Tags{
			Host:   "127.0.0.1",
			Port:   1235,
			Model:  "/m/qwen-tags",
			Engine: "vlm",
			Alias:  "qwen-tags",
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidate_TagsMissingModelOK(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat:      minChat(),
		// tags zero-valued: disabled, must pass
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("tags zero-value must validate: %v", err)
	}
}

func TestValidate_TagsBadEngine(t *testing.T) {
	c := &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1231},
		Chat:      minChat(),
		Tags: Tags{
			Host:   "127.0.0.1",
			Port:   1235,
			Model:  "/m",
			Engine: "audio",
			Alias:  "x",
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "tags.engine") {
		t.Fatalf("want tags.engine error: %v", err)
	}
}
```

- [ ] **Step 2: Add Tags struct and validation to schema.go**

```go
type Tags struct {
	Host     string   `toml:"host"`
	Port     int      `toml:"port"`
	Model    string   `toml:"model"`
	Engine   string   `toml:"engine"`
	Alias    string   `toml:"alias"`
	Cache    Cache    `toml:"cache"`
	Watchdog Watchdog `toml:"watchdog"`
	Memlog   Memlog   `toml:"memlog"`
}
```

Add `Tags Tags `toml:"tags"`` field on `Config`.

In `Validate()`, add at the end before `return nil`:

```go
if c.Tags.Model != "" || c.Tags.Port != 0 {
	if c.Tags.Port <= 0 || c.Tags.Port > 65535 {
		return fmt.Errorf("tags.port: must be 1..65535, got %d", c.Tags.Port)
	}
	if c.Tags.Model == "" {
		return fmt.Errorf("tags.model: required when tags configured")
	}
	if c.Tags.Engine != "lm" && c.Tags.Engine != "vlm" {
		return fmt.Errorf("tags.engine: must be 'lm' or 'vlm', got %q", c.Tags.Engine)
	}
	if c.Tags.Alias == "" {
		return fmt.Errorf("tags.alias: required when tags configured")
	}
}
```

- [ ] **Step 3: Add Tags ~-expansion in loader.go**

After existing expansion lines, before `Validate()`:

```go
c.Tags.Model = expandHome(c.Tags.Model)
```

- [ ] **Step 4: Run tests, commit.**

```
go test ./internal/config/... -v
git add internal/config/
git commit -m "feat(config): tags section"
```

---

## Task 2: supervisor.Managed type

**Files:**
- Create: `internal/supervisor/managed.go`
- Create: `internal/supervisor/managed_test.go`

`Managed` is the always-on counterpart to `ChatSwap`. One worker, no profiles, started by `Start(ctx)`, restarted on unexpected exit with exponential backoff (1s, 2s, 4s, capped at 30s; backoff resets after 5 minutes of uptime).

- [ ] **Step 1: Failing test**

```go
package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestManaged_StartProbesUntilReady(t *testing.T) {
	var started int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200); w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()

	port, _ := freePort()
	m := NewManaged(ManagedOpts{
		Name:          "tags",
		Host:          "127.0.0.1",
		Port:          port,
		Args:          []string{"--engine", "lm", "--model", "/m"},
		ProbeInterval: 20 * time.Millisecond,
		ProbeTimeout:  5 * time.Second,
		BackoffMin:    50 * time.Millisecond,
		BackoffMax:    200 * time.Millisecond,
		WorkerFactory: func(args []string) *Worker {
			atomic.AddInt32(&started, 1)
			return New(WorkerSpec{Name: "tags", Command: "/bin/sh", Args: []string{"-c", "sleep 2"}})
		},
	})
	m.upstreamURLOverride = upstream.URL

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(context.Background())
	if atomic.LoadInt32(&started) != 1 {
		t.Errorf("want 1 spawn, got %d", started)
	}
	if !m.Running() {
		t.Errorf("Running() should be true")
	}
}

func TestManaged_StopGraceful(t *testing.T) {
	var started int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200); w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	port, _ := freePort()
	m := NewManaged(ManagedOpts{
		Name: "tags", Host: "127.0.0.1", Port: port, Args: nil,
		ProbeInterval: 20 * time.Millisecond, ProbeTimeout: 5 * time.Second,
		BackoffMin: 50 * time.Millisecond, BackoffMax: 200 * time.Millisecond,
		WorkerFactory: func(args []string) *Worker {
			atomic.AddInt32(&started, 1)
			return New(WorkerSpec{Name: "tags", Command: "/bin/sh", Args: []string{"-c", "trap 'exit 0' TERM; sleep 5"}})
		},
	})
	m.upstreamURLOverride = upstream.URL

	m.Start(context.Background())
	time.Sleep(100 * time.Millisecond)
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if m.Running() {
		t.Errorf("Running() should be false after Stop")
	}
}

func TestManaged_RestartsOnUnexpectedExit(t *testing.T) {
	var started int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200); w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	port, _ := freePort()
	m := NewManaged(ManagedOpts{
		Name: "tags", Host: "127.0.0.1", Port: port, Args: nil,
		ProbeInterval: 20 * time.Millisecond, ProbeTimeout: 2 * time.Second,
		BackoffMin: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		WorkerFactory: func(args []string) *Worker {
			atomic.AddInt32(&started, 1)
			return New(WorkerSpec{Name: "tags", Command: "/bin/sh", Args: []string{"-c", "exit 1"}})
		},
	})
	m.upstreamURLOverride = upstream.URL

	go m.Start(context.Background())
	time.Sleep(500 * time.Millisecond)
	m.Stop(context.Background())
	if atomic.LoadInt32(&started) < 2 {
		t.Errorf("expected restart, got %d spawns", started)
	}
}
```

- [ ] **Step 2: Implement `internal/supervisor/managed.go`**

```go
package supervisor

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/backend"
)

type ManagedOpts struct {
	Name          string
	Host          string
	Port          int
	Args          []string
	Env           []string
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration
	BackoffMin    time.Duration
	BackoffMax    time.Duration
	WorkerFactory func(args []string) *Worker
}

type Managed struct {
	opts    ManagedOpts
	mu      sync.Mutex
	current *Worker
	running atomic.Bool
	stop    chan struct{}
	done    chan struct{}
	state   *backend.ChatState // reused: stores PID/URL

	upstreamURLOverride string
}

func NewManaged(opts ManagedOpts) *Managed {
	if opts.ProbeInterval == 0 {
		opts.ProbeInterval = 250 * time.Millisecond
	}
	if opts.ProbeTimeout == 0 {
		opts.ProbeTimeout = 90 * time.Second
	}
	if opts.BackoffMin == 0 {
		opts.BackoffMin = time.Second
	}
	if opts.BackoffMax == 0 {
		opts.BackoffMax = 30 * time.Second
	}
	return &Managed{
		opts:  opts,
		state: &backend.ChatState{},
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

func (m *Managed) URL() string {
	return fmt.Sprintf("http://%s:%d", m.opts.Host, m.opts.Port)
}

func (m *Managed) BaseURL() string { return m.URL() }

func (m *Managed) Running() bool { return m.running.Load() }

func (m *Managed) PID() int {
	m.mu.Lock(); defer m.mu.Unlock()
	if m.current != nil {
		return m.current.PID()
	}
	return 0
}

// Start launches the worker and enters a supervision loop in a goroutine.
// It returns once the worker is healthy (probe succeeds) or the ProbeTimeout
// expires.
func (m *Managed) Start(ctx context.Context) error {
	if !m.running.CompareAndSwap(false, true) {
		return nil // already running
	}

	// Probe first launch synchronously.
	if err := m.spawnAndProbe(ctx); err != nil {
		m.running.Store(false)
		return err
	}

	go m.supervise()
	return nil
}

func (m *Managed) spawnAndProbe(ctx context.Context) error {
	m.mu.Lock()
	w := m.opts.WorkerFactory(m.opts.Args)
	if err := w.Start(ctx); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("spawn %s: %w", m.opts.Name, err)
	}
	m.current = w
	m.state.WorkerPID = w.PID()
	m.state.WorkerURL = m.URL()
	m.mu.Unlock()

	return m.probeReady(ctx)
}

func (m *Managed) probeReady(ctx context.Context) error {
	deadline := time.Now().Add(m.opts.ProbeTimeout)
	probeURL := m.URL() + "/v1/models"
	if m.upstreamURLOverride != "" {
		probeURL = m.upstreamURLOverride + "/v1/models"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", probeURL, nil)
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(m.opts.ProbeInterval)
	}
	return fmt.Errorf("%s not ready within %s", m.opts.Name, m.opts.ProbeTimeout)
}

// supervise loops on worker exits with exponential backoff.
func (m *Managed) supervise() {
	defer close(m.done)
	backoff := m.opts.BackoffMin
	for {
		m.mu.Lock()
		w := m.current
		m.mu.Unlock()
		if w == nil {
			return
		}
		select {
		case <-m.stop:
			return
		case res := <-w.Done():
			_ = res
			select {
			case <-m.stop:
				return
			default:
			}
			// Unexpected exit; restart after backoff.
			time.Sleep(backoff)
			backoff = backoff * 2
			if backoff > m.opts.BackoffMax {
				backoff = m.opts.BackoffMax
			}
			if err := m.spawnAndProbe(context.Background()); err != nil {
				// Give up retrying for this round but keep trying.
				continue
			}
			backoff = m.opts.BackoffMin
		}
	}
}

func (m *Managed) Stop(ctx context.Context) error {
	if !m.running.CompareAndSwap(true, false) {
		return nil
	}
	close(m.stop)
	m.mu.Lock()
	w := m.current
	m.current = nil
	m.state.WorkerPID = 0
	m.state.WorkerURL = ""
	m.mu.Unlock()
	if w != nil {
		_ = w.Signal("TERM")
		select {
		case <-w.Done():
		case <-time.After(30 * time.Second):
			_ = w.Signal("KILL")
			<-w.Done()
		case <-ctx.Done():
			_ = w.Signal("KILL")
			return ctx.Err()
		}
	}
	select {
	case <-m.done:
	case <-time.After(2 * time.Second):
	}
	return nil
}
```

- [ ] **Step 3: Run + commit.**

```
go test ./internal/supervisor/... -v
git add internal/supervisor/managed.go internal/supervisor/managed_test.go
git commit -m "feat(supervisor): always-on Managed worker with backoff restart"
```

---

## Task 3: BackendRegistry — alias → Backend resolver

**Files:**
- Modify: `internal/backend/backend.go` (add tiny registry helper)
- Create: `internal/router/registry.go`
- Create: `internal/router/registry_test.go`

The router currently asks ChatSwap directly. Now it needs to dispatch based on the model alias:
- If alias is a chat profile name → use ChatSwap (call EnsureProfile + Proxy)
- If alias is a tags alias → use the Managed backend (no EnsureProfile; just proxy)

We add an interface `ManagedBackend` for the second case, and a `Registry` that the server consults.

- [ ] **Step 1: Failing test in `internal/router/registry_test.go`**

```go
package router

import (
	"context"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

type fakeManaged struct {
	url, upstream, alias string
}

func (f *fakeManaged) Alias() string  { return f.alias }
func (f *fakeManaged) BaseURL() string { return f.url }
func (f *fakeManaged) UpstreamModel() string { return f.upstream }
func (f *fakeManaged) Running() bool { return true }

func TestRegistry_ResolveChatProfile(t *testing.T) {
	cfg := &config.Config{Chat: config.Chat{Profiles: map[string]config.Profile{"valkyrie": {}}}}
	chat := &fakeSwap{}
	reg := NewRegistry(cfg, chat, nil)
	kind, _, err := reg.Resolve(context.Background(), "valkyrie")
	if err != nil || kind != ResolveChat {
		t.Errorf("got kind=%v err=%v", kind, err)
	}
}

func TestRegistry_ResolveTagsAlias(t *testing.T) {
	cfg := &config.Config{
		Chat: config.Chat{Profiles: map[string]config.Profile{"v": {}}},
		Tags: config.Tags{Alias: "qwen-tags"},
	}
	tags := &fakeManaged{alias: "qwen-tags", url: "http://x", upstream: "/m"}
	reg := NewRegistry(cfg, &fakeSwap{}, tags)
	kind, mb, err := reg.Resolve(context.Background(), "qwen-tags")
	if err != nil || kind != ResolveManaged {
		t.Fatalf("got kind=%v err=%v", kind, err)
	}
	if mb.BaseURL() != "http://x" {
		t.Errorf("base url: %s", mb.BaseURL())
	}
}

func TestRegistry_Unknown(t *testing.T) {
	cfg := &config.Config{Chat: config.Chat{Profiles: map[string]config.Profile{}}}
	reg := NewRegistry(cfg, &fakeSwap{}, nil)
	_, _, err := reg.Resolve(context.Background(), "ghost")
	if err == nil {
		t.Errorf("expected error")
	}
}
```

- [ ] **Step 2: Implement `internal/router/registry.go`**

```go
package router

import (
	"context"
	"fmt"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

// ManagedBackend is what tags/embed/audio/external backends implement.
type ManagedBackend interface {
	Alias() string
	BaseURL() string
	UpstreamModel() string
	Running() bool
}

type ResolveKind int

const (
	ResolveUnknown ResolveKind = iota
	ResolveChat
	ResolveManaged
)

type Registry struct {
	cfg     *config.Config
	chat    ChatSwapper
	managed map[string]ManagedBackend
}

func NewRegistry(cfg *config.Config, chat ChatSwapper, tags ManagedBackend) *Registry {
	m := map[string]ManagedBackend{}
	if tags != nil && tags.Alias() != "" {
		m[tags.Alias()] = tags
	}
	return &Registry{cfg: cfg, chat: chat, managed: m}
}

func (r *Registry) Resolve(_ context.Context, alias string) (ResolveKind, ManagedBackend, error) {
	if _, ok := r.cfg.Chat.Profiles[alias]; ok {
		return ResolveChat, nil, nil
	}
	if mb, ok := r.managed[alias]; ok {
		return ResolveManaged, mb, nil
	}
	return ResolveUnknown, nil, fmt.Errorf("unknown model %q", alias)
}

func (r *Registry) ManagedList() []ManagedBackend {
	out := make([]ManagedBackend, 0, len(r.managed))
	for _, m := range r.managed {
		out = append(out, m)
	}
	return out
}
```

- [ ] **Step 3: Run + commit.**

```
go test ./internal/router/... -v
git add internal/router/registry.go internal/router/registry_test.go
git commit -m "feat(router): backend registry with alias resolution"
```

---

## Task 4: ManagedAdapter on supervisor.Managed

**Files:**
- Modify: `internal/supervisor/managed.go` (add `Alias()` + `UpstreamModel()` so it satisfies `router.ManagedBackend`)
- Modify: `internal/supervisor/satisfies.go` (add compile-time check)

- [ ] **Step 1: Append to managed.go**

```go
// Alias returns the configured alias for this backend (set via ManagedOpts).
func (m *Managed) Alias() string { return m.opts.Alias }

// UpstreamModel is the absolute model path the upstream server expects.
func (m *Managed) UpstreamModel() string { return m.opts.UpstreamModel }
```

Add `Alias string` and `UpstreamModel string` fields to `ManagedOpts`. Wire them in tests via the existing factory.

- [ ] **Step 2: Update satisfies.go**

```go
var (
	_ router.ChatSwapper   = (*ChatSwap)(nil)
	_ admin.ChatController = (*ChatSwap)(nil)
	_ router.ManagedBackend = (*Managed)(nil)
)
```

- [ ] **Step 3: Run + commit.**

```
go test ./internal/supervisor/...
git add internal/supervisor/managed.go internal/supervisor/satisfies.go
git commit -m "feat(supervisor): Managed satisfies router.ManagedBackend"
```

---

## Task 5: Router uses Registry; tags get proxied

**Files:**
- Modify: `internal/router/server.go`
- Modify: `internal/router/server_test.go`

- [ ] **Step 1: Update ServerOpts and Server to accept a Registry**

Replace `Chat ChatSwapper` with `Registry *Registry` in `ServerOpts` and `Server`. Update `NewServer` accordingly. Keep `Catalog` as is.

```go
type ServerOpts struct {
	Config   *config.Config
	Chat     ChatSwapper
	Registry *Registry
}

type Server struct {
	cfg      *config.Config
	chat     ChatSwapper
	registry *Registry
	catalog  *Catalog
}

func NewServer(opts ServerOpts) *Server {
	reg := opts.Registry
	if reg == nil {
		reg = NewRegistry(opts.Config, opts.Chat, nil)
	}
	return &Server{cfg: opts.Config, chat: opts.Chat, registry: reg, catalog: NewCatalog(opts.Config)}
}
```

Update `handleChat` to consult registry:

```go
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil { http.Error(w, "read body: "+err.Error(), 400); return }
	r.Body = io.NopCloser(bytes.NewReader(body))

	model, err := ExtractModel(body)
	if err != nil { http.Error(w, err.Error(), 400); return }

	kind, mb, err := s.registry.Resolve(r.Context(), model)
	if err != nil { http.Error(w, err.Error(), 400); return }

	switch kind {
	case ResolveChat:
		if err := s.chat.EnsureProfile(r.Context(), model); err != nil {
			http.Error(w, "ensure profile: "+err.Error(), 502); return
		}
		_ = ProxyJSON(w, r, s.chat.BaseURL(), s.chat.UpstreamModel(model))
	case ResolveManaged:
		_ = ProxyJSON(w, r, mb.BaseURL(), mb.UpstreamModel())
	default:
		http.Error(w, "unknown model", 400)
	}
}
```

- [ ] **Step 2: Update Catalog to include managed aliases**

```go
func (c *Catalog) List() []Model {
	out := []Model{}
	for name := range c.cfg.Chat.Profiles {
		out = append(out, Model{ID: name})
	}
	if c.cfg.Tags.Alias != "" {
		out = append(out, Model{ID: c.cfg.Tags.Alias})
	}
	return out
}
```

- [ ] **Step 3: Add test that hits a tags-routed chat completion**

In `server_test.go`, append:

```go
func TestServer_TagsAliasRoutes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"/abs/qwen-tags"`) {
			t.Errorf("body: %s", body)
		}
		w.Write([]byte(`{"id":"x"}`))
	}))
	defer upstream.Close()

	cfg := &config.Config{
		Chat: config.Chat{Profiles: map[string]config.Profile{"v": {Model: "/m", Engine: "lm"}}},
		Tags: config.Tags{Alias: "qwen-tags", Model: "/abs/qwen-tags"},
	}
	tags := &fakeManaged{alias: "qwen-tags", url: upstream.URL, upstream: "/abs/qwen-tags"}
	srv := NewServer(ServerOpts{
		Config:   cfg,
		Chat:     &fakeSwap{},
		Registry: NewRegistry(cfg, &fakeSwap{}, tags),
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"qwen-tags"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
}
```

- [ ] **Step 4: Run + commit.**

```
go test ./internal/router/...
git add internal/router/server.go internal/router/server_test.go internal/router/catalog.go
git commit -m "feat(router): registry-driven dispatch (chat or managed)"
```

---

## Task 6: cmd/mlxd wires Tags when configured

**Files:**
- Modify: `cmd/mlxd/main.go`

After building `chatSwap`, add:

```go
var tagsMgr *supervisor.Managed
if cfg.Tags.Model != "" {
	tagsMgr = supervisor.NewManaged(supervisor.ManagedOpts{
		Name:          "tags",
		Host:          cfg.Tags.Host,
		Port:          cfg.Tags.Port,
		Alias:         cfg.Tags.Alias,
		UpstreamModel: cfg.Tags.Model,
		Args: []string{
			"-m", "mlx_stack.launcher_shim",
			"--engine", cfg.Tags.Engine,
			"--model", cfg.Tags.Model,
			"--host", cfg.Tags.Host,
			"--port", fmt.Sprintf("%d", cfg.Tags.Port),
		},
		Env: tagsEnv(cfg),
		WorkerFactory: func(args []string) *supervisor.Worker {
			return supervisor.New(supervisor.WorkerSpec{
				Name: "tags", Command: cfg.PythonBin, Args: args, Env: tagsEnv(cfg), Logger: logger,
			})
		},
	})
	if err := tagsMgr.Start(context.Background()); err != nil {
		logger.Error("tags start", "err", err)
	}
}
```

Wire into router via Registry:

```go
registry := router.NewRegistry(cfg, chatSwap, tagsMgr)
routerSrv := router.NewServer(router.ServerOpts{Config: cfg, Chat: chatSwap, Registry: registry})
```

On shutdown:

```go
if tagsMgr != nil {
	_ = tagsMgr.Stop(ctx)
}
```

Add `tagsEnv(cfg)` helper (same shape as `workerEnv` but reads from `cfg.Tags.Cache`, `cfg.Tags.Watchdog`, `cfg.Tags.Memlog`).

Commit:

```
git add cmd/mlxd/main.go
git commit -m "feat(mlxd): launch tags backend when configured"
```

---

## Task 7: admin /v1/status includes tags

**Files:**
- Modify: `internal/admin/handlers.go`
- Modify: `internal/admin/handlers_test.go`

Add `Tags TagsStatus` field to `StatusResponse`. `TagsStatus` has `{alias, pid, url, running}`. Plumb through a `TagsController` interface on `Handlers` (optional — nil means tags not configured).

```go
type TagsController interface {
	Alias() string
	PID() int
	BaseURL() string
	Running() bool
}

type TagsStatus struct {
	Alias   string `json:"alias"`
	PID     int    `json:"pid"`
	URL     string `json:"url"`
	Running bool   `json:"running"`
}

type StatusResponse struct {
	Chat ChatStatus  `json:"chat"`
	Tags *TagsStatus `json:"tags,omitempty"`
}
```

In `status` handler:

```go
if h.Tags != nil {
	resp.Tags = &TagsStatus{Alias: h.Tags.Alias(), PID: h.Tags.PID(), URL: h.Tags.BaseURL(), Running: h.Tags.Running()}
}
```

Test that when Tags is set, status includes the tags block. Then wire from cmd/mlxd: `admin.Handlers{..., Tags: tagsMgr}`.

```
go test ./internal/admin/...
git add internal/admin/ cmd/mlxd/main.go
git commit -m "feat(admin): /v1/status reports tags state"
```

---

## Task 8: e2e — tags backend serves a request

**Files:**
- Modify: `e2e/e2e_test.go` (add new test)

Spawn mlxd with `[tags]` configured to use the same fake-python → fakemlx shim. Hit `POST /v1/chat/completions {"model":"qwen-tags"}`. Verify 200.

```go
func TestE2E_TagsAlias(t *testing.T) {
	if testing.Short() { t.Skip("e2e") }
	root := repoRoot(t); buildAll(t, root)

	dir := t.TempDir()
	routerPort := freePort(t)
	chatPort := freePort(t)
	tagsPort := freePort(t)
	sockPath := filepath.Join(dir, "admin.sock")
	fakePython := filepath.Join(dir, "fake-python")
	os.WriteFile(fakePython, []byte(fmt.Sprintf(`#!/bin/sh
shift 4
exec "%s/bin/fakemlx" "$@"
`, root)), 0o755)

	cfg := fmt.Sprintf(`
log_dir = "%s"
models_root = "%s"
python_bin  = "%s"
[router]
host = "127.0.0.1"
port = %d
[chat]
default_profile  = "p1"
host = "127.0.0.1"
port = %d
swap_timeout_sec = 5
  [chat.profiles.p1]
  model = "/tmp/p1"
  engine = "lm"
[tags]
host = "127.0.0.1"
port = %d
model = "/tmp/qwen-tags"
engine = "vlm"
alias = "qwen-tags"
`, dir, dir, fakePython, routerPort, chatPort, tagsPort)
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte(cfg), 0o644)

	mlxd := exec.Command(filepath.Join(root, "bin", "mlxd"), "run", "--config", cfgPath, "--socket", sockPath)
	mlxd.Stdout = os.Stdout; mlxd.Stderr = os.Stderr
	mlxd.Start()
	defer func() { mlxd.Process.Signal(os.Interrupt); mlxd.Wait() }()
	waitPort(t, "127.0.0.1", routerPort, 8*time.Second)

	do(t, routerPort, `{"model":"qwen-tags"}`)
}
```

Run + commit:

```
go test ./e2e/... -v -timeout 60s
git add e2e/e2e_test.go
git commit -m "test(e2e): tags backend serves chat completion"
```

---

## Acceptance

- `go test ./... -count=1` green.
- `make test` green (Go + Python).
- e2e includes `TestE2E_TagsAlias` and passes.
