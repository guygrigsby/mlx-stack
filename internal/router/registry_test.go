package router

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

type fakeBackend struct {
	name, group, mode, engine, url, upstream string
	running                                  bool
}

func (f *fakeBackend) Name() string                                   { return f.name }
func (f *fakeBackend) Group() string                                  { return f.group }
func (f *fakeBackend) Mode() string                                   { return f.mode }
func (f *fakeBackend) Engine() string                                 { return f.engine }
func (f *fakeBackend) BaseURL() string                                { return f.url }
func (f *fakeBackend) UpstreamModel() string                          { return f.upstream }
func (f *fakeBackend) Running() bool                                  { return f.running }
func (f *fakeBackend) PID() int                                       { return 0 }
func (f *fakeBackend) EnsureLoaded(_ context.Context, _ string) error { return nil }
func (f *fakeBackend) Start(_ context.Context) error                  { return nil }
func (f *fakeBackend) Stop(_ context.Context) error                   { return nil }

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

// Backends register a lowercased name (HF repo ids are mixed-case), so a
// model id sent in its original case must still resolve, not 404.
func TestRegistry_ResolveCaseInsensitive(t *testing.T) {
	b := &fakeBackend{name: "gpt-oss-120b-mxfp4-q4", mode: "persistent"}
	r := NewRegistry(b)
	for _, name := range []string{
		"gpt-oss-120b-mxfp4-q4", // exact
		"gpt-oss-120b-MXFP4-Q4", // mixed (what an HF-id client sends)
		"GPT-OSS-120B-MXFP4-Q4", // upper
	} {
		got, err := r.Resolve(context.Background(), name)
		if err != nil || got != b {
			t.Errorf("Resolve(%q): got=%v err=%v", name, got, err)
		}
		if !r.Has(name) {
			t.Errorf("Has(%q) = false", name)
		}
	}
	if _, err := r.Resolve(context.Background(), "gpt-oss-7b"); err == nil {
		t.Error("a genuinely different name must still be unknown")
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

func TestRegistry_RegisterAndHas(t *testing.T) {
	r := NewRegistry()
	if r.Has("late") {
		t.Fatal("Has should be false before Register")
	}
	b := &fakeBackend{name: "late", mode: "persistent"}
	r.Register(b)
	r.Register(nil) // no-op, must not panic
	if !r.Has("late") {
		t.Fatal("Has should be true after Register")
	}
	got, err := r.Resolve(context.Background(), "late")
	if err != nil || got != b {
		t.Errorf("resolve after register: got=%v err=%v", got, err)
	}
}

// TestRegistry_ConcurrentRegisterResolve exercises the lock under -race: a
// hot reload registers from one goroutine while the router resolves and lists
// from others.
func TestRegistry_ConcurrentRegisterResolve(t *testing.T) {
	seed := &fakeBackend{name: "seed"}
	r := NewRegistry(seed)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Register(&fakeBackend{name: fmt.Sprintf("b%d", i)})
			r.RegisterAlias(fmt.Sprintf("a%d", i), seed)
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Resolve(context.Background(), "seed")
			_ = r.Names()
			_ = r.All()
			_ = r.Has("seed")
		}()
	}
	wg.Wait()
	if !r.Has("seed") {
		t.Error("seed lost")
	}
}
