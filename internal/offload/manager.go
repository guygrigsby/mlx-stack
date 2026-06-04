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
		return nil
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
		return nil
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
			continue
		}
		t := m.lastUsed[name]
		if best == "" || t.Before(bestT) {
			best, bestT = name, t
		}
	}
	return best
}

// Offload ensures a library copy exists, then removes the cache copy. A
// local-only model is copied to the library first so its only copy is never
// destroyed. No-op if the model is not in the cache. Errors if the drive is
// unmounted (the library copy cannot be guaranteed).
func (m *Manager) Offload(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.opt.FS.Exists(m.cachePath(name)) {
		return nil
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

// TierName is a string-typed accessor for callers that must not import the Tier type.
func (m *Manager) TierName(name string) string { return string(m.Tier(name)) }

// Budget returns the configured cache budget in bytes (0 = unbounded).
func (m *Manager) Budget() int64 { return m.opt.Budget }

// SetPinned replaces the function reporting model names that must not be evicted.
func (m *Manager) SetPinned(f func() map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f != nil {
		m.opt.Pinned = f
	}
}
