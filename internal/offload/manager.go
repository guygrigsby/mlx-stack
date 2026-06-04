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

var _ = context.Background // context used by later methods in this package

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
