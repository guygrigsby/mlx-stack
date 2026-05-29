package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

func newTestGroup(t *testing.T) (*Group, *int32) {
	t.Helper()
	var started int32
	members := map[string]config.BackendSpec{
		"p1": {Name: "p1", Mode: "swap", Group: "chat", Engine: "lm", Model: "/tmp/p1", Host: "127.0.0.1", Port: 0},
		"p2": {Name: "p2", Mode: "swap", Group: "chat", Engine: "lm", Model: "/tmp/p2", Host: "127.0.0.1", Port: 0},
	}
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	g := NewGroup(GroupOpts{
		Name:           "chat",
		Host:           "127.0.0.1",
		Port:           port,
		Members:        members,
		DefaultMember:  "p1",
		SwapTimeoutSec: 5,
		ProbeInterval:  20 * time.Millisecond,
		WorkerFactory: func(spec config.BackendSpec) *Worker {
			atomic.AddInt32(&started, 1)
			return New(WorkerSpec{Name: "chat[" + spec.Name + "]", Command: "/bin/sh", Args: []string{"-c", "sleep 2"}})
		},
	})
	return g, &started
}

func TestGroup_NoSwapWhenAlreadyCurrent(t *testing.T) {
	g, started := newTestGroup(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	g.upstreamURLOverride = upstream.URL

	if err := g.EnsureLoaded(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if err := g.EnsureLoaded(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(started) != 1 {
		t.Errorf("want 1 spawn, got %d", *started)
	}
}

func TestGroup_SwapKillsOldSpawnsNew(t *testing.T) {
	g, started := newTestGroup(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	g.upstreamURLOverride = upstream.URL

	if err := g.EnsureLoaded(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if err := g.EnsureLoaded(context.Background(), "p2"); err != nil {
		t.Fatal(err)
	}
	if g.Current() != "p2" {
		t.Errorf("current: %q", g.Current())
	}
	if atomic.LoadInt32(started) != 2 {
		t.Errorf("want 2 spawns, got %d", *started)
	}
}

func TestGroup_EnsureLoadedGroupNameLoadsDefault(t *testing.T) {
	// `mlxctl start chat` and chat requests to model "chat" both pass the
	// group name (not a member name) to EnsureLoaded. It should resolve to
	// the group's default member.
	g, _ := newTestGroup(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	g.upstreamURLOverride = upstream.URL

	if err := g.EnsureLoaded(context.Background(), "chat"); err != nil {
		t.Fatal(err)
	}
	if g.Current() != "p1" {
		t.Errorf("default member not loaded for group name: %q", g.Current())
	}
}

func TestGroup_UnknownMember(t *testing.T) {
	g, _ := newTestGroup(t)
	err := g.EnsureLoaded(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "no member") {
		t.Fatalf("want no-member error: %v", err)
	}
}

func TestGroup_ConcurrentEnsureLoadedSwapOnce(t *testing.T) {
	g, started := newTestGroup(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	g.upstreamURLOverride = upstream.URL

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.EnsureLoaded(context.Background(), "p1")
		}()
	}
	wg.Wait()
	if atomic.LoadInt32(started) != 1 {
		t.Errorf("want 1 spawn, got %d", *started)
	}
}

func TestGroup_Start(t *testing.T) {
	g, _ := newTestGroup(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	g.upstreamURLOverride = upstream.URL

	if err := g.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.Current() != "p1" {
		t.Errorf("default member not loaded: %q", g.Current())
	}
}

func TestGroup_WorkerSurvivesCallerCtxCancel(t *testing.T) {
	// Regression: previously, exec.CommandContext bound the worker's
	// lifetime to the EnsureLoaded ctx (typically an HTTP request ctx),
	// so the moment the triggering request finished, the model server was
	// SIGKILLed and the next request hit a refused port.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	members := map[string]config.BackendSpec{
		"p1": {Name: "p1", Mode: "swap", Group: "chat", Engine: "lm", Model: "/tmp/p1", Host: "127.0.0.1", Port: 0},
	}
	g := NewGroup(GroupOpts{
		Name:           "chat",
		Host:           "127.0.0.1",
		Port:           port,
		Members:        members,
		SwapTimeoutSec: 5,
		ProbeInterval:  20 * time.Millisecond,
		WorkerFactory: func(spec config.BackendSpec) *Worker {
			return New(WorkerSpec{Name: "chat[" + spec.Name + "]", Command: "/bin/sh", Args: []string{"-c", "sleep 2"}})
		},
	})
	g.upstreamURLOverride = upstream.URL

	ctx, cancel := context.WithCancel(context.Background())
	if err := g.EnsureLoaded(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	cancel() // simulate the HTTP request that triggered the load ending

	// Give the runtime a moment to potentially kill the process.
	time.Sleep(150 * time.Millisecond)

	if g.Current() != "p1" {
		t.Fatalf("worker died after caller ctx cancel: current=%q", g.Current())
	}
	if !g.Running() {
		t.Fatalf("worker died after caller ctx cancel: Running()=false")
	}
}

func TestGroup_WorkerExitClearsState(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	var started int32
	members := map[string]config.BackendSpec{
		"p1": {Name: "p1", Mode: "swap", Group: "chat", Engine: "lm", Model: "/tmp/p1", Host: "127.0.0.1", Port: 0},
	}
	g := NewGroup(GroupOpts{
		Name:           "chat",
		Host:           "127.0.0.1",
		Port:           port,
		Members:        members,
		DefaultMember:  "p1",
		SwapTimeoutSec: 5,
		ProbeInterval:  20 * time.Millisecond,
		WorkerFactory: func(spec config.BackendSpec) *Worker {
			atomic.AddInt32(&started, 1)
			// Sleeps briefly then exits, simulating a worker that dies.
			return New(WorkerSpec{Name: "chat[" + spec.Name + "]", Command: "/bin/sh", Args: []string{"-c", "sleep 0.2"}})
		},
	})
	g.upstreamURLOverride = upstream.URL

	if err := g.EnsureLoaded(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if g.Current() != "p1" {
		t.Fatalf("current after first load: %q", g.Current())
	}

	// Wait for the worker to exit; reaper goroutine should clear state.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g.Current() == "" && !g.Running() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if g.Current() != "" {
		t.Fatalf("state not cleared after worker exit: current=%q", g.Current())
	}

	if err := g.EnsureLoaded(context.Background(), "p1"); err != nil {
		t.Fatalf("second load after death: %v", err)
	}
	if atomic.LoadInt32(&started) != 2 {
		t.Errorf("want 2 spawns after worker death, got %d", started)
	}
}

func TestGroup_Stop(t *testing.T) {
	g, _ := newTestGroup(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	g.upstreamURLOverride = upstream.URL

	g.EnsureLoaded(context.Background(), "p1")
	g.Stop(context.Background())
	if g.Current() != "" {
		t.Errorf("current after Stop: %q", g.Current())
	}
	if g.Running() {
		t.Errorf("Running after Stop")
	}
}
