package router

import (
	"context"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

type fakeManaged struct {
	alias, url, upstream string
	running              bool
}

func (f *fakeManaged) Alias() string         { return f.alias }
func (f *fakeManaged) BaseURL() string       { return f.url }
func (f *fakeManaged) UpstreamModel() string { return f.upstream }
func (f *fakeManaged) Running() bool         { return f.running }

func TestRegistry_ResolveChatProfile(t *testing.T) {
	cfg := &config.Config{Chat: config.Chat{Profiles: map[string]config.Profile{"valkyrie": {Model: "/m", Engine: "lm"}}}}
	reg := NewRegistry(cfg, &fakeSwap{}, nil)
	kind, _, err := reg.Resolve(context.Background(), "valkyrie")
	if err != nil || kind != ResolveChat {
		t.Errorf("got kind=%v err=%v", kind, err)
	}
}

func TestRegistry_ResolveManagedAlias(t *testing.T) {
	cfg := &config.Config{
		Chat: config.Chat{Profiles: map[string]config.Profile{"v": {Model: "/m", Engine: "lm"}}},
		Tags: config.Tags{Alias: "qwen-tags"},
	}
	tags := &fakeManaged{alias: "qwen-tags", url: "http://x", upstream: "/m", running: true}
	reg := NewRegistry(cfg, &fakeSwap{}, tags)
	kind, mb, err := reg.Resolve(context.Background(), "qwen-tags")
	if err != nil || kind != ResolveManaged {
		t.Fatalf("got kind=%v err=%v", kind, err)
	}
	if mb.BaseURL() != "http://x" || mb.UpstreamModel() != "/m" {
		t.Errorf("backend: %+v", mb)
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

func TestRegistry_NilManagedIgnored(t *testing.T) {
	cfg := &config.Config{Chat: config.Chat{Profiles: map[string]config.Profile{"v": {Model: "/m", Engine: "lm"}}}}
	reg := NewRegistry(cfg, &fakeSwap{}, nil, nil)
	// Should not panic; should not have any managed entries.
	if got := reg.ManagedList(); len(got) != 0 {
		t.Errorf("expected empty managed list, got %v", got)
	}
}
