package router

import (
	"context"
	"testing"
)

type fakeBackend struct {
	name, group, mode, engine, url, upstream string
	running                                  bool
}

func (f *fakeBackend) Name() string                                        { return f.name }
func (f *fakeBackend) Group() string                                       { return f.group }
func (f *fakeBackend) Mode() string                                        { return f.mode }
func (f *fakeBackend) Engine() string                                      { return f.engine }
func (f *fakeBackend) BaseURL() string                                     { return f.url }
func (f *fakeBackend) UpstreamModel() string                               { return f.upstream }
func (f *fakeBackend) Running() bool                                       { return f.running }
func (f *fakeBackend) PID() int                                            { return 0 }
func (f *fakeBackend) EnsureLoaded(_ context.Context, _ string) error     { return nil }
func (f *fakeBackend) Start(_ context.Context) error                      { return nil }
func (f *fakeBackend) Stop(_ context.Context) error                       { return nil }

func TestRegistry_ResolveByName(t *testing.T) {
	a := &fakeBackend{name: "embed", mode: "persistent"}
	r := NewRegistry(a)
	got, err := r.Resolve(context.Background(), "embed")
	if err != nil || got != a {
		t.Errorf("got=%v err=%v", got, err)
	}
}

func TestRegistry_ResolveAlias(t *testing.T) {
	g := &fakeBackend{name: "chat", group: "chat", mode: "swap"}
	r := NewRegistry(g)
	r.RegisterAlias("valkyrie", g)
	r.RegisterAlias("scout", g)
	for _, alias := range []string{"chat", "valkyrie", "scout"} {
		got, err := r.Resolve(context.Background(), alias)
		if err != nil || got != g {
			t.Errorf("alias %q: got=%v err=%v", alias, got, err)
		}
	}
}

func TestRegistry_Unknown(t *testing.T) {
	r := NewRegistry()
	_, err := r.Resolve(context.Background(), "ghost")
	if err == nil {
		t.Error("expected unknown error")
	}
}

func TestRegistry_AllDedupes(t *testing.T) {
	g := &fakeBackend{name: "chat", mode: "swap"}
	r := NewRegistry(g)
	r.RegisterAlias("valkyrie", g)
	r.RegisterAlias("scout", g)
	if len(r.All()) != 1 {
		t.Errorf("want 1 distinct backend, got %d", len(r.All()))
	}
}

func TestRegistry_NilSafe(t *testing.T) {
	r := NewRegistry(nil)
	r.RegisterAlias("", nil)
	r.RegisterAlias("x", nil)
	if len(r.All()) != 0 {
		t.Errorf("nil should be skipped")
	}
}
