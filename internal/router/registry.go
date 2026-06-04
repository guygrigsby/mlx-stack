package router

import (
	"context"
	"fmt"
	"slices"
	"strings"
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

// canon canonicalizes a backend name to its registry key: lowercase, trimmed.
// HF repo ids are mixed-case (e.g. mlx-community/gpt-oss-120b-MXFP4-Q4), so
// normalizing at the boundary lets registration and resolution agree on one
// form and makes lookup a single map hit instead of a case-insensitive scan.
func canon(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func NewRegistry(backends ...backend.Backend) *Registry {
	r := &Registry{byName: map[string]backend.Backend{}}
	for _, b := range backends {
		if b == nil {
			continue
		}
		r.byName[canon(b.Name())] = b
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
	r.byName[canon(b.Name())] = b
}

// RegisterAlias adds another name that resolves to b. Used for swap group
// members so each member name routes to the owning Group.
func (r *Registry) RegisterAlias(alias string, b backend.Backend) {
	if b == nil || canon(alias) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[canon(alias)] = b
}

// resolveLocked looks a name up by its canonical (lowercase) key. Keys are
// canonicalized at registration, so an OpenAI client sending the original-case
// model id resolves with a single map hit. Caller holds at least the read lock.
func (r *Registry) resolveLocked(name string) (backend.Backend, bool) {
	b, ok := r.byName[canon(name)]
	return b, ok
}

// Has reports whether a name (primary or alias) is already registered.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.resolveLocked(name)
	return ok
}

func (r *Registry) Resolve(_ context.Context, name string) (backend.Backend, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if b, ok := r.resolveLocked(name); ok {
		return b, nil
	}
	return nil, fmt.Errorf("unknown backend %q", name)
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

// Names returns every registered name (including aliases), sorted. The order
// must be stable: /v1/models is derived from it, and an OpenAI client that
// tracks its model picker by list position selects the wrong model if the order
// reshuffles between polls (Go map iteration is randomized).
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}
