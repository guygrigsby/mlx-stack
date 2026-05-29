package router

import (
	"context"
	"fmt"
	"sync"

	"github.com/guygrigsby/mlx-stack/internal/backend"
)

// Registry maps backend names (and aliases) to backend.Backend instances.
// It is safe for concurrent use: the router resolves on request goroutines
// while a hot reload registers new backends from the admin goroutine.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]backend.Backend
}

func NewRegistry(backends ...backend.Backend) *Registry {
	r := &Registry{byName: map[string]backend.Backend{}}
	for _, b := range backends {
		if b == nil {
			continue
		}
		r.byName[b.Name()] = b
	}
	return r
}

// Register adds b under its primary name. Used at startup and by hot reload.
func (r *Registry) Register(b backend.Backend) {
	if b == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[b.Name()] = b
}

// RegisterAlias adds another name that resolves to b. Used for swap group
// members so each member name routes to the owning Group.
func (r *Registry) RegisterAlias(alias string, b backend.Backend) {
	if b == nil || alias == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[alias] = b
}

// Has reports whether a name (primary or alias) is already registered.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byName[name]
	return ok
}

func (r *Registry) Resolve(_ context.Context, name string) (backend.Backend, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("unknown backend %q", name)
	}
	return b, nil
}

// All returns the distinct set of registered backends (an alias and a
// backend's primary name may both resolve to the same Backend; All
// deduplicates).
func (r *Registry) All() []backend.Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
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

// Names returns every registered name (including aliases), unsorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	return out
}
