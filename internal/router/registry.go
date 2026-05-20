package router

import (
	"context"
	"fmt"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

// ManagedBackend is what tags/embed/audio/external backends implement so the
// router can proxy to them.
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

// Registry maps a request's model alias to either the chat backend
// (resolved by EnsureProfile + ChatSwapper) or a ManagedBackend (proxy
// straight through).
type Registry struct {
	cfg     *config.Config
	chat    ChatSwapper
	managed map[string]ManagedBackend
}

// NewRegistry takes the chat swapper and any number of managed backends
// (nil values are ignored). Aliases registered by managed backends shadow
// matching chat-profile names — but in practice that should never happen
// because Config.Validate would have rejected the collision (TODO).
func NewRegistry(cfg *config.Config, chat ChatSwapper, managedBackends ...ManagedBackend) *Registry {
	m := map[string]ManagedBackend{}
	for _, mb := range managedBackends {
		if mb == nil || mb.Alias() == "" {
			continue
		}
		m[mb.Alias()] = mb
	}
	return &Registry{cfg: cfg, chat: chat, managed: m}
}

// Resolve returns ResolveChat if alias names a chat profile, ResolveManaged
// (with the backend) if it names a managed alias, or an error otherwise.
func (r *Registry) Resolve(_ context.Context, alias string) (ResolveKind, ManagedBackend, error) {
	if _, ok := r.cfg.Chat.Profiles[alias]; ok {
		return ResolveChat, nil, nil
	}
	if mb, ok := r.managed[alias]; ok {
		return ResolveManaged, mb, nil
	}
	return ResolveUnknown, nil, fmt.Errorf("unknown model %q", alias)
}

// ManagedList returns all registered managed backends (any order).
func (r *Registry) ManagedList() []ManagedBackend {
	out := make([]ManagedBackend, 0, len(r.managed))
	for _, m := range r.managed {
		out = append(out, m)
	}
	return out
}
