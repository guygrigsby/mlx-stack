# Two-Tier Model Offload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the external drive the durable model library and the internal SSD a budgeted cache, so loading auto-pulls from the library, LRU-evicts to stay under budget, and offloaded models degrade gracefully when the drive is unmounted.

**Architecture:** A new bounded context `internal/offload` owns tier resolution, pull/offload/evict, and LRU metadata behind a `FileStore` port (anti-corruption boundary, so the manager is unit-tested with no real disk). mlxd constructs a `Manager` when `[offload]` is configured and the supervisor calls a `BeforeLoad` hook before spawning each worker. mlxctl gains `offload`/`pull` commands and tier columns.

**Tech Stack:** Go, cobra (mlxctl), net/http admin IPC, TOML config, `testing` + `httptest`/temp dirs.

**Spec:** `docs/specs/2026-06-03-offload-two-tier-storage-design.md`

---

## File Structure

- Create `internal/offload/store.go` — `FileStore` port + `OSStore` (os-backed) impl.
- Create `internal/offload/store_test.go` — `OSStore` temp-dir integration tests.
- Create `internal/offload/tier.go` — `Tier` type + `Manager.Tier`.
- Create `internal/offload/manager.go` — `Manager` aggregate: `New`, `EnsurePulled`, `Offload`, `Pull`, `Reconcile`, `CacheUsed`, eviction, LRU state.
- Create `internal/offload/manager_test.go` — manager unit tests against a fake `FileStore`.
- Create `internal/offload/fakestore_test.go` — in-memory `FileStore` test double.
- Modify `internal/config/schema.go` — add `Offload` struct + field + validation.
- Modify `internal/config/loader.go` — expand `~` in `external_root`.
- Modify `internal/config/loader_test.go` / `schema_test.go` — cover the new field.
- Modify `internal/supervisor/group.go` + `internal/supervisor/persistent.go` — add a `BeforeLoad` hook invoked before spawning a worker.
- Modify `internal/supervisor/group_test.go` / `persistent_test.go` — cover the hook.
- Modify `cmd/mlxd/live.go` + `cmd/mlxd/main.go` — build the `Manager`, wire `BeforeLoad` and `Pinned`.
- Modify `internal/admin/handlers.go` + `handlers_test.go` — `POST /v1/offload`, `POST /v1/pull`, tier in status.
- Create `cmd/mlxctl/offload.go` — `offload` (+ `--inactive`) and `pull` commands.
- Create `cmd/mlxctl/offload_test.go` — `--inactive` selection logic.
- Modify `cmd/mlxctl/render.go` + `render_test.go` — tier column + cache-usage line.
- Modify `cmd/mlxctl/main.go` — register the new commands.

Canonical types used across tasks (defined in Task 2/3/4, referenced later):

```go
// internal/offload/store.go
type FileStore interface {
	Mounted(root string) bool                 // root exists and is a directory
	Exists(dir string) bool                   // model dir exists at full path
	Size(dir string) (int64, error)           // total bytes under dir
	ModTime(dir string) (time.Time, error)    // dir mtime (reconcile default)
	List(root string) ([]string, error)       // immediate subdir names of root
	CopyDir(src, dst string) error            // atomic: temp sibling + rename; dst must not exist
	RemoveDir(dir string) error
}

// internal/offload/tier.go
type Tier string
const (
	TierUnknown   Tier = "unknown"
	TierOffloaded Tier = "offloaded"    // library only
	TierHot       Tier = "hot"          // cache + library
	TierLocalOnly Tier = "local-only"   // cache only, not backed up
)

// internal/offload/manager.go
type Options struct {
	CacheRoot   string
	LibraryRoot string
	Budget      int64
	StatePath   string
	FS          FileStore
	Pinned      func() map[string]bool // model names that must not be evicted
}
type Manager struct {
	opt      Options
	mu       sync.Mutex
	lastUsed map[string]time.Time
}
```

---

## Task 1: Config `[offload]` section

**Files:**
- Modify: `internal/config/schema.go`
- Modify: `internal/config/loader.go`
- Test: `internal/config/schema_test.go`, `internal/config/loader_test.go`

- [ ] **Step 1: Write the failing test (validation + parse)**

Add to `internal/config/schema_test.go`:

```go
func TestOffloadValidation(t *testing.T) {
	// Present but missing external_root is an error.
	c := &Config{
		PythonBin: "/x", Router: Router{Port: 8080},
		Backends:  []BackendSpec{{Name: "a", Mode: "external", URL: "http://x"}},
		Offload:   &Offload{LocalBudgetBytes: 1},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("offload without external_root should error")
	}
	// Fully specified is fine.
	c.Offload.ExternalRoot = "/Volumes/weights-data/mlx-models"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid offload rejected: %v", err)
	}
	// Absent offload is fine (opt-in).
	c.Offload = nil
	if err := c.Validate(); err != nil {
		t.Fatalf("nil offload rejected: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestOffloadValidation`
Expected: build failure — `Offload` undefined.

- [ ] **Step 3: Add the struct, field, and validation**

In `internal/config/schema.go`, add the field to `Config`:

```go
type Config struct {
	LogDir     string        `toml:"log_dir"`
	ModelsRoot string        `toml:"models_root"`
	PythonBin  string        `toml:"python_bin"`
	Router     Router        `toml:"router"`
	Defaults   Defaults      `toml:"defaults"`
	Offload    *Offload      `toml:"offload"`
	Backends   []BackendSpec `toml:"backend"`
}
```

Add the type near `Defaults`:

```go
// Offload configures two-tier model storage. When nil, models are single-tier
// (today's behavior). ExternalRoot is the durable library; ModelsRoot is the
// budgeted cache.
type Offload struct {
	ExternalRoot     string `toml:"external_root"`
	LocalBudgetBytes int64  `toml:"local_budget_bytes"`
}
```

In `Validate()`, before `return nil`:

```go
	if c.Offload != nil {
		if c.Offload.ExternalRoot == "" {
			return fmt.Errorf("offload.external_root: required when [offload] is set")
		}
		if c.ModelsRoot == "" {
			return fmt.Errorf("offload: models_root required (it is the cache root)")
		}
	}
```

- [ ] **Step 4: Expand `~` in external_root**

In `internal/config/loader.go`, where other paths are expanded (next to `c.ModelsRoot = expandHome(c.ModelsRoot)`):

```go
	if c.Offload != nil {
		c.Offload.ExternalRoot = expandHome(c.Offload.ExternalRoot)
	}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/config/ -run TestOffloadValidation`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/config/schema.go internal/config/loader.go internal/config/schema_test.go
git commit -m "feat(config): add opt-in [offload] section (external_root + local_budget_bytes)"
```

---

## Task 2: `FileStore` port + `OSStore` impl

**Files:**
- Create: `internal/offload/store.go`
- Test: `internal/offload/store_test.go`

- [ ] **Step 1: Write the failing test (temp-dir round trip)**

Create `internal/offload/store_test.go`:

```go
package offload

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOSStore_CopySizeRemoveList(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "lib", "m")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "config.json"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := OSStore{}

	dst := filepath.Join(root, "cache", "m")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fs.CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}
	if !fs.Exists(dst) {
		t.Fatal("dst should exist after copy")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "config.json")); string(b) != "12345" {
		t.Fatalf("copied content wrong: %q", b)
	}
	if n, err := fs.Size(dst); err != nil || n != 5 {
		t.Fatalf("Size = %d, %v; want 5", n, err)
	}
	names, err := fs.List(filepath.Join(root, "cache"))
	if err != nil || len(names) != 1 || names[0] != "m" {
		t.Fatalf("List = %v, %v", names, err)
	}
	if !fs.Mounted(filepath.Join(root, "cache")) {
		t.Fatal("Mounted should be true for an existing dir")
	}
	if fs.Mounted(filepath.Join(root, "nope")) {
		t.Fatal("Mounted should be false for a missing dir")
	}
	if err := fs.RemoveDir(dst); err != nil || fs.Exists(dst) {
		t.Fatalf("RemoveDir failed: %v", err)
	}
}

func TestOSStore_CopyDirRefusesExistingDst(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "s")
	dst := filepath.Join(root, "d")
	os.MkdirAll(src, 0o755)
	os.MkdirAll(dst, 0o755)
	if err := (OSStore{}).CopyDir(src, dst); err == nil {
		t.Fatal("CopyDir into existing dst should error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/offload/ -run TestOSStore`
Expected: build failure — `OSStore` undefined.

- [ ] **Step 3: Implement `store.go`**

Create `internal/offload/store.go`:

```go
package offload

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// FileStore is the only thing in this package that touches disk. The Manager
// depends on this interface so its logic is unit-tested with an in-memory fake.
type FileStore interface {
	Mounted(root string) bool
	Exists(dir string) bool
	Size(dir string) (int64, error)
	ModTime(dir string) (time.Time, error)
	List(root string) ([]string, error)
	CopyDir(src, dst string) error
	RemoveDir(dir string) error
}

// OSStore is the production FileStore backed by the os package.
type OSStore struct{}

func (OSStore) Mounted(root string) bool {
	fi, err := os.Stat(root)
	return err == nil && fi.IsDir()
}

func (OSStore) Exists(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

func (OSStore) Size(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		total += fi.Size()
		return nil
	})
	return total, err
}

func (OSStore) ModTime(dir string) (time.Time, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

func (OSStore) List(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// CopyDir copies src to a temp sibling of dst, then atomically renames it into
// place. dst must not already exist. A failed copy leaves no partial dst.
func (OSStore) CopyDir(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("copy dst already exists: %s", dst)
	}
	tmp := dst + ".partial"
	_ = os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	return nil
}

func (OSStore) RemoveDir(dir string) error { return os.RemoveAll(dir) }

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/offload/ -run TestOSStore`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/offload/store.go internal/offload/store_test.go
git commit -m "feat(offload): FileStore port + os-backed OSStore (atomic CopyDir)"
```

---

## Task 3: Fake `FileStore` + tier classification

**Files:**
- Create: `internal/offload/fakestore_test.go`
- Create: `internal/offload/tier.go`
- Test: `internal/offload/manager_test.go` (started here)

- [ ] **Step 1: Write the in-memory fake (test double, no production dep)**

Create `internal/offload/fakestore_test.go`:

```go
package offload

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// fakeStore is an in-memory FileStore. Keys are full dir paths; value is size.
type fakeStore struct {
	dirs    map[string]int64     // dir path -> size in bytes
	mtimes  map[string]time.Time // dir path -> mtime
	mounted map[string]bool      // root path -> mounted
	copies  int
	removes int
}

func newFakeStore() *fakeStore {
	return &fakeStore{dirs: map[string]int64{}, mtimes: map[string]time.Time{}, mounted: map[string]bool{}}
}

func (f *fakeStore) add(dir string, size int64) { f.dirs[dir] = size }

func (f *fakeStore) Mounted(root string) bool {
	if v, ok := f.mounted[root]; ok {
		return v
	}
	return true // default mounted unless explicitly set false
}
func (f *fakeStore) Exists(dir string) bool { _, ok := f.dirs[dir]; return ok }
func (f *fakeStore) Size(dir string) (int64, error) {
	if s, ok := f.dirs[dir]; ok {
		return s, nil
	}
	return 0, fmt.Errorf("no such dir %s", dir)
}
func (f *fakeStore) ModTime(dir string) (time.Time, error) {
	if t, ok := f.mtimes[dir]; ok {
		return t, nil
	}
	return time.Unix(0, 0), nil
}
func (f *fakeStore) List(root string) ([]string, error) {
	var names []string
	for d := range f.dirs {
		if filepath.Dir(d) == root {
			names = append(names, filepath.Base(d))
		}
	}
	sort.Strings(names)
	return names, nil
}
func (f *fakeStore) CopyDir(src, dst string) error {
	if _, ok := f.dirs[dst]; ok {
		return fmt.Errorf("dst exists")
	}
	s, ok := f.dirs[src]
	if !ok {
		return fmt.Errorf("src missing")
	}
	f.dirs[dst] = s
	f.copies++
	return nil
}
func (f *fakeStore) RemoveDir(dir string) error {
	if !strings.Contains(dir, "/") {
		return fmt.Errorf("bad dir")
	}
	delete(f.dirs, dir)
	f.removes++
	return nil
}
```

- [ ] **Step 2: Write the failing tier test**

Create `internal/offload/manager_test.go`:

```go
package offload

import (
	"path/filepath"
	"testing"
)

func newTestManager(t *testing.T, fs *fakeStore, budget int64) *Manager {
	t.Helper()
	m, err := New(Options{
		CacheRoot:   "/cache",
		LibraryRoot: "/lib",
		Budget:      budget,
		StatePath:   filepath.Join(t.TempDir(), "offload.json"),
		FS:          fs,
		Pinned:      func() map[string]bool { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManager_Tier(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/hot", 10)
	fs.add("/lib/hot", 10)
	fs.add("/lib/cold", 10)
	fs.add("/cache/fresh", 10)
	m := newTestManager(t, fs, 1000)

	cases := map[string]Tier{
		"hot":     TierHot,
		"cold":    TierOffloaded,
		"fresh":   TierLocalOnly,
		"missing": TierUnknown,
	}
	for name, want := range cases {
		if got := m.Tier(name); got != want {
			t.Errorf("Tier(%q) = %q, want %q", name, got, want)
		}
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/offload/ -run TestManager_Tier`
Expected: build failure — `New`, `Manager`, `Tier`, `Options` undefined.

- [ ] **Step 4: Implement `tier.go`**

Create `internal/offload/tier.go`:

```go
package offload

import "path/filepath"

type Tier string

const (
	TierUnknown   Tier = "unknown"
	TierOffloaded Tier = "offloaded"
	TierHot       Tier = "hot"
	TierLocalOnly Tier = "local-only"
)

func (m *Manager) cachePath(name string) string { return filepath.Join(m.opt.CacheRoot, name) }
func (m *Manager) libPath(name string) string   { return filepath.Join(m.opt.LibraryRoot, name) }

// Tier classifies a model by name from the filesystem. Caller need not hold mu.
func (m *Manager) Tier(name string) Tier {
	inCache := m.opt.FS.Exists(m.cachePath(name))
	inLib := m.opt.FS.Exists(m.libPath(name))
	switch {
	case inCache && inLib:
		return TierHot
	case inCache:
		return TierLocalOnly
	case inLib:
		return TierOffloaded
	default:
		return TierUnknown
	}
}
```

- [ ] **Step 5: Implement minimal `manager.go` (constructor only)**

Create `internal/offload/manager.go`:

```go
package offload

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type Options struct {
	CacheRoot   string
	LibraryRoot string
	Budget      int64
	StatePath   string
	FS          FileStore
	Pinned      func() map[string]bool
}

type Manager struct {
	opt      Options
	mu       sync.Mutex
	lastUsed map[string]time.Time
}

func New(opt Options) (*Manager, error) {
	if opt.FS == nil {
		return nil, fmt.Errorf("offload: FS required")
	}
	if opt.Pinned == nil {
		opt.Pinned = func() map[string]bool { return nil }
	}
	m := &Manager{opt: opt, lastUsed: map[string]time.Time{}}
	if err := m.load(); err != nil {
		return nil, err
	}
	if err := m.Reconcile(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) load() error {
	b, err := os.ReadFile(m.opt.StatePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	raw := map[string]int64{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil // corrupt state is non-fatal; reconcile rebuilds
	}
	for k, v := range raw {
		m.lastUsed[k] = time.Unix(v, 0)
	}
	return nil
}

func (m *Manager) save() {
	raw := map[string]int64{}
	for k, v := range m.lastUsed {
		raw[k] = v.Unix()
	}
	b, _ := json.MarshalIndent(raw, "", "  ")
	_ = os.WriteFile(m.opt.StatePath, b, 0o644)
}
```

- [ ] **Step 6: Add a no-op `Reconcile` so `New` compiles**

Append to `internal/offload/manager.go`:

```go
// Reconcile syncs LRU metadata with what is actually on disk: seed missing
// access times from dir mtime, drop entries for models no longer cached.
func (m *Manager) Reconcile() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cached, err := m.opt.FS.List(m.opt.CacheRoot)
	if err != nil {
		return err
	}
	inCache := map[string]bool{}
	for _, name := range cached {
		inCache[name] = true
		if _, ok := m.lastUsed[name]; !ok {
			if t, err := m.opt.FS.ModTime(m.cachePath(name)); err == nil {
				m.lastUsed[name] = t
			} else {
				m.lastUsed[name] = time.Unix(0, 0)
			}
		}
	}
	for name := range m.lastUsed {
		if !inCache[name] {
			delete(m.lastUsed, name)
		}
	}
	m.save()
	return nil
}
```

- [ ] **Step 7: Run tests**

Run: `go test ./internal/offload/ -run TestManager_Tier`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/offload/tier.go internal/offload/manager.go internal/offload/fakestore_test.go internal/offload/manager_test.go
git commit -m "feat(offload): Manager skeleton, tier classification, LRU state + reconcile"
```

---

## Task 4: `CacheUsed` + `Reconcile` behavior test

**Files:**
- Modify: `internal/offload/manager_test.go`
- Modify: `internal/offload/manager.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/offload/manager_test.go`:

```go
func TestManager_CacheUsed(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/a", 100)
	fs.add("/cache/b", 250)
	fs.add("/lib/a", 100)
	m := newTestManager(t, fs, 1000)
	used, err := m.CacheUsed()
	if err != nil || used != 350 {
		t.Fatalf("CacheUsed = %d, %v; want 350", used, err)
	}
}

func TestManager_ReconcileDropsAndSeeds(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/keep", 10)
	fs.add("/lib/keep", 10)
	m := newTestManager(t, fs, 1000)
	// "ghost" was never on disk; reconcile (run in New) must not have kept it.
	if _, ok := m.lastUsed["ghost"]; ok {
		t.Fatal("ghost should not be tracked")
	}
	if _, ok := m.lastUsed["keep"]; !ok {
		t.Fatal("keep should be seeded from disk")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/offload/ -run TestManager_CacheUsed`
Expected: build failure — `CacheUsed` undefined.

- [ ] **Step 3: Implement `CacheUsed`**

Append to `internal/offload/manager.go`:

```go
// CacheUsed returns the total bytes of all model dirs in the cache root.
func (m *Manager) CacheUsed() (int64, error) {
	names, err := m.opt.FS.List(m.opt.CacheRoot)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, name := range names {
		s, err := m.opt.FS.Size(m.cachePath(name))
		if err != nil {
			return 0, err
		}
		total += s
	}
	return total, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/offload/ -run 'TestManager_CacheUsed|TestManager_Reconcile'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/offload/manager.go internal/offload/manager_test.go
git commit -m "feat(offload): CacheUsed accounting + reconcile coverage"
```

---

## Task 5: `EnsurePulled` + eviction (the core)

**Files:**
- Modify: `internal/offload/manager_test.go`
- Modify: `internal/offload/manager.go`

- [ ] **Step 1: Write the failing tests (all load paths)**

Add to `internal/offload/manager_test.go`:

```go
import "context"

func TestEnsurePulled_HotTouchesNoCopy(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/m", 10)
	fs.add("/lib/m", 10)
	m := newTestManager(t, fs, 1000)
	if err := m.EnsurePulled(context.Background(), "m"); err != nil {
		t.Fatal(err)
	}
	if fs.copies != 0 {
		t.Fatalf("hot model should not copy, got %d copies", fs.copies)
	}
}

func TestEnsurePulled_OffloadedPullsWhenRoom(t *testing.T) {
	fs := newFakeStore()
	fs.add("/lib/m", 100)
	m := newTestManager(t, fs, 1000)
	if err := m.EnsurePulled(context.Background(), "m"); err != nil {
		t.Fatal(err)
	}
	if !fs.Exists("/cache/m") || fs.copies != 1 {
		t.Fatalf("expected pull copy; exists=%v copies=%d", fs.Exists("/cache/m"), fs.copies)
	}
}

func TestEnsurePulled_EvictsLRUToFit(t *testing.T) {
	fs := newFakeStore()
	// Two hot models fill the cache; budget only fits one more.
	fs.add("/cache/old", 400)
	fs.add("/lib/old", 400)
	fs.add("/cache/recent", 400)
	fs.add("/lib/recent", 400)
	fs.add("/lib/new", 400)
	m := newTestManager(t, fs, 1000) // 400+400 used; +400 = 1200 > 1000
	// Make "old" the LRU, "recent" newer.
	m.lastUsed["old"] = time.Unix(100, 0)
	m.lastUsed["recent"] = time.Unix(200, 0)

	if err := m.EnsurePulled(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	if fs.Exists("/cache/old") {
		t.Error("LRU 'old' should have been evicted")
	}
	if !fs.Exists("/cache/recent") {
		t.Error("'recent' should remain")
	}
	if !fs.Exists("/cache/new") {
		t.Error("'new' should be pulled")
	}
}

func TestEnsurePulled_PinnedNotEvicted(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/pinnedm", 800)
	fs.add("/lib/pinnedm", 800)
	fs.add("/lib/new", 400)
	m := newTestManager(t, fs, 1000)
	m.lastUsed["pinnedm"] = time.Unix(1, 0) // would be LRU
	m.opt.Pinned = func() map[string]bool { return map[string]bool{"pinnedm": true} }

	err := m.EnsurePulled(context.Background(), "new")
	if err == nil {
		t.Fatal("expected 'cannot fit' error when the only victim is pinned")
	}
	if fs.Exists("/cache/new") {
		t.Fatal("new should not have been pulled when it cannot fit")
	}
}

func TestEnsurePulled_UnknownErrors(t *testing.T) {
	m := newTestManager(t, newFakeStore(), 1000)
	if err := m.EnsurePulled(context.Background(), "ghost"); err == nil {
		t.Fatal("unknown model should error")
	}
}

func TestEnsurePulled_DriveAbsentErrors(t *testing.T) {
	fs := newFakeStore()
	fs.add("/lib/m", 10)
	fs.mounted["/lib"] = false
	m := newTestManager(t, fs, 1000)
	if err := m.EnsurePulled(context.Background(), "m"); err == nil {
		t.Fatal("offloaded + unmounted drive should error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/offload/ -run TestEnsurePulled`
Expected: build failure — `EnsurePulled` undefined.

- [ ] **Step 3: Implement `EnsurePulled` + eviction**

Append to `internal/offload/manager.go`:

```go
// EnsurePulled makes model `name` present in the cache so the supervisor can
// load it from CacheRoot/name. Hot/local-only: touch LRU and return. Offloaded:
// evict LRU hot models to fit the budget, then copy from the library. Errors if
// the model is unknown, or offloaded while the drive is unmounted, or the cache
// budget cannot fit it because every other cached model is pinned.
func (m *Manager) EnsurePulled(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.opt.FS.Exists(m.cachePath(name)) {
		m.lastUsed[name] = time.Now()
		m.save()
		return nil
	}
	if !m.opt.FS.Exists(m.libPath(name)) {
		return fmt.Errorf("unknown model %q (not in cache or library)", name)
	}
	if !m.opt.FS.Mounted(m.opt.LibraryRoot) {
		return fmt.Errorf("model %q is offloaded but the external drive is not mounted", name)
	}
	need, err := m.opt.FS.Size(m.libPath(name))
	if err != nil {
		return err
	}
	if err := m.ensureRoom(need, name); err != nil {
		return err
	}
	if err := m.opt.FS.CopyDir(m.libPath(name), m.cachePath(name)); err != nil {
		return fmt.Errorf("pull %q: %w", name, err)
	}
	m.lastUsed[name] = time.Now()
	m.save()
	return nil
}

// ensureRoom evicts least-recently-used evictable models until `need` bytes fit
// under the budget. keep is never evicted. Caller holds mu.
func (m *Manager) ensureRoom(need int64, keep string) error {
	if m.opt.Budget <= 0 {
		return nil // no budget configured: never evict
	}
	used, err := m.CacheUsed()
	if err != nil {
		return err
	}
	pinned := m.opt.Pinned()
	for used+need > m.opt.Budget {
		victim := m.lruEvictable(keep, pinned)
		if victim == "" {
			return fmt.Errorf("cannot fit %q in cache budget (all cached models are pinned)", keep)
		}
		sz, err := m.opt.FS.Size(m.cachePath(victim))
		if err != nil {
			return err
		}
		if err := m.opt.FS.RemoveDir(m.cachePath(victim)); err != nil {
			return err
		}
		used -= sz
	}
	return nil
}

// lruEvictable returns the least-recently-used cached model that is safe to
// evict: tier hot (a library copy exists), not pinned, not keep. "" if none.
func (m *Manager) lruEvictable(keep string, pinned map[string]bool) string {
	var best string
	var bestT time.Time
	names, _ := m.opt.FS.List(m.opt.CacheRoot)
	for _, name := range names {
		if name == keep || pinned[name] {
			continue
		}
		if !m.opt.FS.Exists(m.libPath(name)) {
			continue // local-only: evicting would lose the only copy
		}
		t := m.lastUsed[name]
		if best == "" || t.Before(bestT) {
			best, bestT = name, t
		}
	}
	return best
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/offload/ -run TestEnsurePulled`
Expected: PASS (all 6)

- [ ] **Step 5: Commit**

```bash
git add internal/offload/manager.go internal/offload/manager_test.go
git commit -m "feat(offload): EnsurePulled with LRU eviction, pin protection, drive-absent guard"
```

---

## Task 6: `Offload` + `Pull` operations

**Files:**
- Modify: `internal/offload/manager_test.go`
- Modify: `internal/offload/manager.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/offload/manager_test.go`:

```go
func TestOffload_HotDeletesCacheKeepsLibrary(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/m", 10)
	fs.add("/lib/m", 10)
	m := newTestManager(t, fs, 1000)
	if err := m.Offload("m"); err != nil {
		t.Fatal(err)
	}
	if fs.Exists("/cache/m") || !fs.Exists("/lib/m") {
		t.Fatalf("offload should drop cache, keep library")
	}
}

func TestOffload_LocalOnlyBacksUpFirst(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/m", 10) // no library copy yet
	m := newTestManager(t, fs, 1000)
	if err := m.Offload("m"); err != nil {
		t.Fatal(err)
	}
	if !fs.Exists("/lib/m") {
		t.Fatal("local-only offload must copy to library before deleting cache")
	}
	if fs.Exists("/cache/m") {
		t.Fatal("cache copy should be removed after backup")
	}
}

func TestOffload_DriveAbsentErrors(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/m", 10)
	fs.mounted["/lib"] = false
	m := newTestManager(t, fs, 1000)
	if err := m.Offload("m"); err == nil {
		t.Fatal("offload with unmounted drive should error")
	}
	if !fs.Exists("/cache/m") {
		t.Fatal("cache must be untouched when offload cannot proceed")
	}
}

func TestPull_IsEnsurePulled(t *testing.T) {
	fs := newFakeStore()
	fs.add("/lib/m", 100)
	m := newTestManager(t, fs, 1000)
	if err := m.Pull(context.Background(), "m"); err != nil {
		t.Fatal(err)
	}
	if !fs.Exists("/cache/m") {
		t.Fatal("pull should materialize the cache copy")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/offload/ -run 'TestOffload|TestPull'`
Expected: build failure — `Offload`, `Pull` undefined.

- [ ] **Step 3: Implement `Offload` and `Pull`**

Append to `internal/offload/manager.go`:

```go
// Offload ensures a library copy exists, then removes the cache copy. A
// local-only model is copied to the library first so its only copy is never
// destroyed. No-op if the model is not in the cache. Errors if the drive is
// unmounted (the library copy cannot be guaranteed).
func (m *Manager) Offload(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.opt.FS.Exists(m.cachePath(name)) {
		return nil // already offloaded or unknown; nothing to do
	}
	if !m.opt.FS.Mounted(m.opt.LibraryRoot) {
		return fmt.Errorf("cannot offload %q: external drive not mounted", name)
	}
	if !m.opt.FS.Exists(m.libPath(name)) {
		if err := m.opt.FS.CopyDir(m.cachePath(name), m.libPath(name)); err != nil {
			return fmt.Errorf("back up %q to library: %w", name, err)
		}
	}
	if err := m.opt.FS.RemoveDir(m.cachePath(name)); err != nil {
		return err
	}
	delete(m.lastUsed, name)
	m.save()
	return nil
}

// Pull pre-warms a model into the cache (same flow as a load).
func (m *Manager) Pull(ctx context.Context, name string) error {
	return m.EnsurePulled(ctx, name)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/offload/ -count=1`
Expected: PASS (whole package)

- [ ] **Step 5: Commit**

```bash
git add internal/offload/manager.go internal/offload/manager_test.go
git commit -m "feat(offload): Offload (backup-then-drop) and Pull operations"
```

---

## Task 7: Supervisor `BeforeLoad` hook

**Files:**
- Modify: `internal/supervisor/group.go`
- Modify: `internal/supervisor/persistent.go`
- Test: `internal/supervisor/group_test.go`, `internal/supervisor/persistent_test.go`

The supervisor must not import `offload` (keep the boundary). It exposes a hook that mlxd wires.

- [ ] **Step 1: Write the failing test (Group)**

Add to `internal/supervisor/group_test.go`:

```go
func TestGroup_BeforeLoadRunsBeforeSpawn(t *testing.T) {
	g, started := newTestGroup(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	g.upstreamURLOverride = upstream.URL

	var calledWith string
	g.opts.BeforeLoad = func(ctx context.Context, spec config.BackendSpec) error {
		calledWith = spec.Name
		if atomic.LoadInt32(started) != 0 {
			t.Error("BeforeLoad must run before the worker is spawned")
		}
		return nil
	}
	if err := g.EnsureLoaded(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	defer g.Stop(context.Background())
	if calledWith != "p1" {
		t.Errorf("BeforeLoad got %q, want p1", calledWith)
	}
}

func TestGroup_BeforeLoadErrorAbortsLoad(t *testing.T) {
	g, started := newTestGroup(t)
	g.opts.BeforeLoad = func(ctx context.Context, spec config.BackendSpec) error {
		return fmt.Errorf("pull failed")
	}
	if err := g.EnsureLoaded(context.Background(), "p1"); err == nil {
		t.Fatal("BeforeLoad error should abort the load")
	}
	if atomic.LoadInt32(started) != 0 {
		t.Error("no worker should spawn when BeforeLoad fails")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/supervisor/ -run TestGroup_BeforeLoad`
Expected: build failure — `BeforeLoad` field undefined on `GroupOpts`.

- [ ] **Step 3: Add the hook to `GroupOpts` and call it in `EnsureLoaded`**

In `internal/supervisor/group.go`, add to `GroupOpts`:

```go
	// BeforeLoad, if set, runs just before a member's worker is spawned (after
	// the lock is held and the member is resolved). A non-nil error aborts the
	// load. mlxd uses it to pull the model into the SSD cache (offload).
	BeforeLoad func(ctx context.Context, spec config.BackendSpec) error
```

In `EnsureLoaded`, after the member `spec` is resolved and the "already current" short-circuit, but before killing the old worker / spawning the new one, insert:

```go
	if g.opts.BeforeLoad != nil {
		if err := g.opts.BeforeLoad(ctx, spec); err != nil {
			return fmt.Errorf("before-load group[%s] member[%s]: %w", g.opts.Name, name, err)
		}
	}
```

(Place it immediately after the `if g.current == name && g.worker != nil { return nil }` block, so a no-op reload does not re-pull.)

- [ ] **Step 4: Write the failing test (Persistent)**

Add to `internal/supervisor/persistent_test.go`:

```go
func TestPersistent_BeforeLoadRunsBeforeSpawn(t *testing.T) {
	var started int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()
	port, _ := freePort()
	var called bool
	p := NewPersistent(PersistentOpts{
		Name: "tags", Engine: "vlm", Host: "127.0.0.1", Port: port,
		ProbeInterval: 20 * time.Millisecond, ProbeTimeout: 5 * time.Second,
		BackoffMin: 50 * time.Millisecond, BackoffMax: 200 * time.Millisecond,
		WorkerFactory: func(args []string) *Worker {
			atomic.AddInt32(&started, 1)
			return New(WorkerSpec{Name: "tags", Command: "/bin/sh", Args: []string{"-c", "sleep 2"}})
		},
		BeforeLoad: func(ctx context.Context, spec config.BackendSpec) error {
			called = true
			if atomic.LoadInt32(&started) != 0 {
				t.Error("BeforeLoad must run before spawn")
			}
			return nil
		},
	})
	p.upstreamURLOverride = upstream.URL
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	if !called {
		t.Error("BeforeLoad was not called")
	}
}
```

- [ ] **Step 5: Add the hook to `PersistentOpts` and call it before spawn**

In `internal/supervisor/persistent.go`, add to `PersistentOpts`:

```go
	BeforeLoad func(ctx context.Context, spec config.BackendSpec) error
```

Find where Persistent spawns its worker (the load/start path that calls `WorkerFactory`). Immediately before the worker is created/started, add:

```go
	if p.opts.BeforeLoad != nil {
		spec := config.BackendSpec{Name: p.opts.Name, Engine: p.opts.Engine, Model: p.opts.Model, DraftModel: p.opts.DraftModel}
		if err := p.opts.BeforeLoad(ctx, spec); err != nil {
			return fmt.Errorf("before-load persistent[%s]: %w", p.opts.Name, err)
		}
	}
```

Note: `PersistentOpts` currently has no `Model`/`DraftModel` fields. Add them:

```go
	Model      string
	DraftModel string
```

and ensure mlxd populates them when constructing the Persistent (Task 8). If the spawn path is in a goroutine without a returnable error, set the worker phase to a failed/stopped state and record the error the same way a failed probe does; mirror the existing error handling in that function.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/supervisor/ -run 'BeforeLoad' -count=1`
Expected: PASS

- [ ] **Step 7: Run the whole supervisor package under race**

Run: `go test ./internal/supervisor/ -race -count=1`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/supervisor/group.go internal/supervisor/persistent.go internal/supervisor/group_test.go internal/supervisor/persistent_test.go
git commit -m "feat(supervisor): BeforeLoad hook (pre-spawn) on Group and Persistent"
```

---

## Task 8: Build the Manager in mlxd and wire the hook

**Files:**
- Modify: `cmd/mlxd/main.go`
- Modify: `cmd/mlxd/live.go`
- Test: `cmd/mlxd/live_test.go`

- [ ] **Step 1: Write the failing test (manager built only when configured)**

Add to `cmd/mlxd/live_test.go`:

```go
func TestBuildOffloadManager_NilWhenUnconfigured(t *testing.T) {
	cfg := &config.Config{ModelsRoot: "/cache"} // no Offload
	m := buildOffloadManager(cfg, t.TempDir())
	if m != nil {
		t.Fatal("manager should be nil when [offload] is absent")
	}
}

func TestBuildOffloadManager_BuiltWhenConfigured(t *testing.T) {
	cfg := &config.Config{
		ModelsRoot: t.TempDir(),
		Offload:    &config.Offload{ExternalRoot: t.TempDir(), LocalBudgetBytes: 1 << 30},
	}
	m := buildOffloadManager(cfg, t.TempDir())
	if m == nil {
		t.Fatal("manager should be built when [offload] is set")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/mlxd/ -run TestBuildOffloadManager`
Expected: build failure — `buildOffloadManager` undefined.

- [ ] **Step 3: Implement `buildOffloadManager` and wire it**

In `cmd/mlxd/live.go` (or `main.go` near setup), add:

```go
// buildOffloadManager returns a configured offload.Manager, or nil when
// [offload] is absent (single-tier behavior). stateDir is mlxd's state dir
// (e.g. ~/.local/state/mlxd).
func buildOffloadManager(cfg *config.Config, stateDir string) *offload.Manager {
	if cfg.Offload == nil {
		return nil
	}
	m, err := offload.New(offload.Options{
		CacheRoot:   cfg.ModelsRoot,
		LibraryRoot: cfg.Offload.ExternalRoot,
		Budget:      cfg.Offload.LocalBudgetBytes,
		StatePath:   filepath.Join(stateDir, "offload.json"),
		FS:          offload.OSStore{},
		Pinned:      func() map[string]bool { return nil }, // replaced below
	})
	if err != nil {
		slog.Error("offload: disabled", "err", err)
		return nil
	}
	return m
}
```

Wire the hook where backends are constructed (the `backendBuilder` in `live.go`). The hook converts the spec's model paths to names and pulls them. Add a helper:

```go
// offloadBeforeLoad returns a BeforeLoad hook that pulls a backend's model (and
// draft model) into the SSD cache before the worker spawns. nil manager => nil hook.
func offloadBeforeLoad(m *offload.Manager) func(context.Context, config.BackendSpec) error {
	if m == nil {
		return nil
	}
	return func(ctx context.Context, spec config.BackendSpec) error {
		for _, p := range []string{spec.Model, spec.DraftModel} {
			if p == "" {
				continue
			}
			if err := m.EnsurePulled(ctx, filepath.Base(p)); err != nil {
				return err
			}
		}
		return nil
	}
}
```

Set `BeforeLoad: offloadBeforeLoad(mgr)` in both the `GroupOpts` and `PersistentOpts` the builder constructs, and populate `PersistentOpts.Model`/`DraftModel` from the spec.

For `Pinned`, after the supervisor registry is built, replace the manager's pinned func with one that reports currently-loaded model names. Add an accessor the registry already can provide (running backends' `UpstreamModel`/current member dir base). If a direct accessor does not exist, add a small `LoadedModelNames() map[string]bool` to the registry that walks backends and collects `filepath.Base` of the current model for any backend whose `Running()` is true, and assign it:

```go
mgr.SetPinned(func() map[string]bool { return registry.LoadedModelNames() })
```

Add `SetPinned` to the manager:

```go
// in internal/offload/manager.go
func (m *Manager) SetPinned(f func() map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f != nil {
		m.opt.Pinned = f
	}
}
```

(Write a one-line test `TestManager_SetPinned` asserting a pinned model is not evicted after `SetPinned`, mirroring `TestEnsurePulled_PinnedNotEvicted`.)

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/mlxd/ ./internal/offload/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/mlxd/live.go cmd/mlxd/main.go cmd/mlxd/live_test.go internal/offload/manager.go internal/offload/manager_test.go
git commit -m "feat(mlxd): build offload.Manager when configured, wire BeforeLoad + Pinned"
```

---

## Task 9: Admin endpoints `POST /v1/offload` and `/v1/pull`

**Files:**
- Modify: `internal/admin/handlers.go`
- Test: `internal/admin/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/admin/handlers_test.go`:

```go
type fakeOffloader struct {
	offloaded []string
	pulled    []string
	err       error
}

func (f *fakeOffloader) Offload(name string) error {
	f.offloaded = append(f.offloaded, name)
	return f.err
}
func (f *fakeOffloader) Pull(ctx context.Context, name string) error {
	f.pulled = append(f.pulled, name)
	return f.err
}

func TestHandler_OffloadAndPull(t *testing.T) {
	h, _, _ := newTestHandlers()
	off := &fakeOffloader{}
	h.Offloader = off

	for _, tc := range []struct {
		path, field string
		want        *[]string
	}{
		{"/v1/offload", "valkyrie", &off.offloaded},
		{"/v1/pull", "valkyrie", &off.pulled},
	} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(`{"name":"valkyrie"}`))
		rr := httptest.NewRecorder()
		h.Mux().ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("%s: code %d body %s", tc.path, rr.Code, rr.Body.String())
		}
		if len(*tc.want) != 1 || (*tc.want)[0] != "valkyrie" {
			t.Errorf("%s: recorded %v", tc.path, *tc.want)
		}
	}
}

func TestHandler_OffloadUnconfiguredIs501(t *testing.T) {
	h, _, _ := newTestHandlers()
	h.Offloader = nil
	req := httptest.NewRequest("POST", "/v1/offload", strings.NewReader(`{"name":"x"}`))
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	if rr.Code != 501 {
		t.Errorf("code %d, want 501", rr.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/admin/ -run TestHandler_Offload`
Expected: build failure — `Handlers.Offloader` undefined.

- [ ] **Step 3: Add the interface, field, routes, handlers**

In `internal/admin/handlers.go`, add near the top:

```go
// Offloader is the subset of offload.Manager the admin layer uses. Defined here
// (not imported) to keep admin decoupled from the offload package.
type Offloader interface {
	Offload(name string) error
	Pull(ctx context.Context, name string) error
}
```

Add the field to `Handlers`:

```go
	// Offloader, when set, enables POST /v1/offload and /v1/pull.
	Offloader Offloader
```

Register routes in `Mux()`:

```go
	mux.HandleFunc("POST /v1/offload", h.offload)
	mux.HandleFunc("POST /v1/pull", h.pull)
```

Add the handlers:

```go
func (h *Handlers) offload(w http.ResponseWriter, r *http.Request) {
	if h.Offloader == nil {
		http.Error(w, "offload not configured", 501)
		return
	}
	var req nameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := h.Offloader.Offload(req.Name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func (h *Handlers) pull(w http.ResponseWriter, r *http.Request) {
	if h.Offloader == nil {
		http.Error(w, "offload not configured", 501)
		return
	}
	var req nameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := h.Offloader.Pull(r.Context(), req.Name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}
```

Then in `cmd/mlxd` where `Handlers` is constructed, set `Offloader: mgr` (nil when unconfigured, which the 501 path handles).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/admin/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/admin/handlers.go internal/admin/handlers_test.go cmd/mlxd/
git commit -m "feat(admin): POST /v1/offload and /v1/pull endpoints"
```

---

## Task 10: mlxctl `offload` / `pull` commands (+ `--inactive`)

**Files:**
- Create: `cmd/mlxctl/offload.go`
- Create: `cmd/mlxctl/offload_test.go`
- Modify: `cmd/mlxctl/main.go`

- [ ] **Step 1: Write the failing test for `--inactive` selection**

Create `cmd/mlxctl/offload_test.go`:

```go
package main

import (
	"sort"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

func TestInactiveModels_ExcludesConfigReferenced(t *testing.T) {
	cfg := &config.Config{Backends: []config.BackendSpec{
		{Name: "chat", Model: "/Users/g/mlx-models/valkyrie"},
		{Name: "anubis", Model: "/Users/g/mlx-models/anubis", DraftModel: "/Users/g/mlx-models/anubis-draft"},
	}}
	cacheDirs := []string{"valkyrie", "anubis", "anubis-draft", "Austral-Qwen3-235B", "old-merge"}
	got := inactiveModels(cfg, cacheDirs)
	sort.Strings(got)
	want := []string{"Austral-Qwen3-235B", "old-merge"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("inactiveModels = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/mlxctl/ -run TestInactiveModels`
Expected: build failure — `inactiveModels` undefined.

- [ ] **Step 3: Implement `offload.go`**

Create `cmd/mlxctl/offload.go`:

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/spf13/cobra"
)

// inactiveModels returns the cache dir names not referenced (as model or
// draft_model) by any backend in cfg.
func inactiveModels(cfg *config.Config, cacheDirs []string) []string {
	active := map[string]bool{}
	for _, b := range cfg.Backends {
		if b.Model != "" {
			active[filepath.Base(b.Model)] = true
		}
		if b.DraftModel != "" {
			active[filepath.Base(b.DraftModel)] = true
		}
	}
	var out []string
	for _, d := range cacheDirs {
		if !active[d] {
			out = append(out, d)
		}
	}
	return out
}

func cacheDirNames(cfg *config.Config) ([]string, error) {
	if cfg == nil || cfg.ModelsRoot == "" {
		return nil, fmt.Errorf("models_root not set in config")
	}
	entries, err := os.ReadDir(cfg.ModelsRoot)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

func postName(path, name string) error {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	body := fmt.Sprintf(`{"name":%q}`, name)
	_, err := c.Post(cx, path, []byte(body))
	return err
}

func newOffloadCmd() *cobra.Command {
	var inactive bool
	cmd := &cobra.Command{
		Use:   "offload [model]",
		Short: "Move a model to the external library, freeing SSD cache",
		RunE: func(cmd *cobra.Command, args []string) error {
			if inactive {
				cfg := loadCfg()
				dirs, err := cacheDirNames(cfg)
				if err != nil {
					return err
				}
				targets := inactiveModels(cfg, dirs)
				for _, name := range targets {
					if err := postName("/v1/offload", name); err != nil {
						return fmt.Errorf("offload %s: %w", name, err)
					}
					fmt.Println("offloaded", name)
				}
				if len(targets) == 0 {
					fmt.Println("nothing to offload (no inactive cached models)")
				}
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("usage: mlxctl offload <model> | --inactive")
			}
			if err := postName("/v1/offload", args[0]); err != nil {
				return err
			}
			return printStatus()
		},
	}
	cmd.Flags().BoolVar(&inactive, "inactive", false, "offload every cached model not referenced by the active config")
	return cmd
}

func newPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull <model>",
		Short: "Pre-warm a model from the external library into the SSD cache",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := postName("/v1/pull", args[0]); err != nil {
				return err
			}
			return printStatus()
		},
	}
}
```

- [ ] **Step 4: Register the commands**

In `cmd/mlxctl/main.go`, add to the lifecycle group (next to start/stop/restart):

```go
		newOffloadCmd(),
		newPullCmd(),
```

- [ ] **Step 5: Verify `ipc.Client` has a `Post` returning `([]byte, error)`**

Run: `grep -n "func (c \*Client) Post" internal/ipc/client.go`
Expected: a `Post(ctx, path, body)` method exists (used by start/stop). If its signature differs, match it in `postName` (Step 3).

- [ ] **Step 6: Run tests + build**

Run: `go test ./cmd/mlxctl/ -run TestInactiveModels -count=1 && go build ./...`
Expected: PASS, build OK

- [ ] **Step 7: Commit**

```bash
git add cmd/mlxctl/offload.go cmd/mlxctl/offload_test.go cmd/mlxctl/main.go
git commit -m "feat(mlxctl): offload (+ --inactive) and pull commands"
```

---

## Task 11: Tier + cache usage in status

**Files:**
- Modify: `internal/admin/handlers.go` (status: add per-backend tier)
- Modify: `internal/admin/handlers_test.go`
- Modify: `cmd/mlxctl/render.go`
- Modify: `cmd/mlxctl/render_test.go`

- [ ] **Step 1: Write the failing render test**

Add to `cmd/mlxctl/render_test.go`:

```go
func TestRenderStatus_ShowsTier(t *testing.T) {
	body := []byte(`{
		"backends": [
			{"name":"chat","group":"chat","mode":"swap","engine":"lm","url":"http://x:1234","running":true,"state":"ready","pid":100,"current_name":"valkyrie","tier":"offloaded"}
		]
	}`)
	var b bytes.Buffer
	renderStatus(&b, body)
	if !strings.Contains(b.String(), "offloaded") {
		t.Errorf("tier not rendered: %s", b.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/mlxctl/ -run TestRenderStatus_ShowsTier`
Expected: FAIL — "offloaded" not in output.

- [ ] **Step 3: Add `Tier` to the status payload and renderer**

In `internal/admin/handlers.go`, add to `BackendStatus`:

```go
	Tier string `json:"tier,omitempty"`
```

In the `status` handler loop, after computing `s`, set the tier when a tier source is available. Add a `Tierer` interface and field mirroring `Offloader`. The method is named `TierName` so the manager (whose `Tier` returns the `offload.Tier` type) satisfies it via its string shim, and admin never imports the `offload.Tier` type:

```go
// near Offloader
type Tierer interface {
	TierName(name string) string
	CacheUsed() (int64, error)
	Budget() int64
}
```

Add `Tierer Tierer` to `Handlers`, and in the loop:

```go
	if h.Tierer != nil && b.UpstreamModel() != "" {
		s.Tier = h.Tierer.TierName(filepath.Base(b.UpstreamModel()))
	}
```

Import `path/filepath` in `handlers.go`. After the loop, attach a cache-usage summary to the response so status can show "cache used / budget":

```go
	if h.Tierer != nil {
		if used, err := h.Tierer.CacheUsed(); err == nil {
			resp.CacheUsedBytes = used
			resp.CacheBudgetBytes = h.Tierer.Budget()
		}
	}
```

Add `CacheUsedBytes` and `CacheBudgetBytes int64 (json:"cache_used_bytes,omitempty" / "cache_budget_bytes,omitempty")` to `StatusResponse`. Wire `Tierer: mgr` in `cmd/mlxd`. Add to the manager: `func (m *Manager) TierName(name string) string { return string(m.Tier(name)) }` and `func (m *Manager) Budget() int64 { return m.opt.Budget }`.

In `cmd/mlxctl/render.go`: add `Tier string \`json:"tier"\`` to `backendStatusJSON`; add `CacheUsedBytes int64 \`json:"cache_used_bytes"\`` and `CacheBudgetBytes int64 \`json:"cache_budget_bytes"\`` to `statusJSON`; add a `TIER` column to the header and a `tier` cell (default `-` when empty) in the row `Fprintf`. After the table flush, when `CacheBudgetBytes > 0`, print a summary line: `fmt.Fprintf(w, "\nCACHE  %s / %s\n", humanBytes(s.CacheUsedBytes), humanBytes(s.CacheBudgetBytes))`.

- [ ] **Step 4: Add the manager shims + a test**

In `internal/offload/manager.go`:

```go
// TierName is a string-typed accessor for callers that must not import the Tier type.
func (m *Manager) TierName(name string) string { return string(m.Tier(name)) }

// Budget returns the configured cache budget in bytes (0 = unbounded).
func (m *Manager) Budget() int64 { return m.opt.Budget }
```

Add to `internal/offload/manager_test.go`:

```go
func TestManager_TierName(t *testing.T) {
	fs := newFakeStore()
	fs.add("/lib/m", 10)
	m := newTestManager(t, fs, 1000)
	if m.TierName("m") != "offloaded" {
		t.Errorf("TierName = %q", m.TierName("m"))
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/offload/ ./internal/admin/ ./cmd/mlxctl/ -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/offload/manager.go internal/offload/manager_test.go internal/admin/handlers.go internal/admin/handlers_test.go cmd/mlxctl/render.go cmd/mlxctl/render_test.go cmd/mlxd/
git commit -m "feat(offload): surface model tier in mlxctl status"
```

---

## Task 12: Full-suite gate + sample config

**Files:**
- Modify: `docs/` sample config snippet (or README) showing `[offload]`.

- [ ] **Step 1: Run the whole gate**

Run: `go build ./... && go vet ./... && go test ./... -race -count=1`
Expected: all PASS.

- [ ] **Step 2: Document the config block**

Add an `[offload]` example to the project's sample config / README near the existing `models_root` docs:

```toml
[offload]
external_root      = "/Volumes/weights-data/mlx-models"
local_budget_bytes = 400_000_000_000
```

- [ ] **Step 3: Commit**

```bash
git add docs/ README.md
git commit -m "docs: document [offload] config block"
```

---

## Self-Review Notes (for the implementer)

- The supervisor must never import `internal/offload`; it only knows the `BeforeLoad` hook (Task 7). mlxd is the only wiring point (Task 8).
- `EnsurePulled` is the single entry the load path uses; `Pull` delegates to it. Eviction only removes `hot` models (library copy exists), never `local-only` (Task 5/6).
- Pinning depends on mlxd reporting currently-loaded model dir base names (Task 8). If the registry lacks that accessor, add `LoadedModelNames()` there; do not let the manager reach into the supervisor. `LoadedModelNames` must include each running backend's `draft_model` base name too, so a loaded model's draft is never evicted out from under it.
- Drive-absent behavior is enforced in `EnsurePulled` and `Offload` via `FS.Mounted(LibraryRoot)` (Task 5/6); `hot`/`local-only` loads never touch the library and so keep working unmounted.
- If `local_budget_bytes` is 0 or unset, `ensureRoom` never evicts (Task 5) — treat that as "unbounded cache" and note it in the config docs.
