package supervisor

import (
	"context"
	"fmt"
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

func TestGroup_ReadyReflectsLiveUpstream(t *testing.T) {
	g, _ := newTestGroup(t)

	// Not loaded yet -> not ready.
	if g.Ready(context.Background()) {
		t.Fatal("Ready() = true before any load, want false")
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	g.upstreamURLOverride = upstream.URL
	if err := g.EnsureLoaded(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	defer g.Stop(context.Background())

	// Loaded and upstream answers -> ready.
	if !g.Ready(context.Background()) {
		t.Error("Ready() = false with a live upstream, want true")
	}

	// Upstream wedges (here: stops answering) -> not ready, though still loaded.
	upstream.Close()
	if g.Ready(context.Background()) {
		t.Error("Ready() = true after upstream stopped answering, want false")
	}
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

func TestGroup_AddMemberHotLoadRoutes(t *testing.T) {
	g, _ := newTestGroup(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	g.upstreamURLOverride = upstream.URL

	added, mismatch := g.AddMember(config.BackendSpec{
		Name: "p3", Mode: "swap", Group: "chat", Engine: "lm", Model: "/tmp/p3", Host: "127.0.0.1", Port: 0,
	})
	if !added {
		t.Fatal("AddMember should report added for a new member")
	}
	if mismatch {
		t.Errorf("no port mismatch expected when member port is 0")
	}

	// New member is in the member set and routable.
	names := map[string]bool{}
	for _, m := range g.Members() {
		names[m.Name] = true
	}
	if !names["p3"] {
		t.Fatalf("p3 not in members: %v", names)
	}
	if err := g.EnsureLoaded(context.Background(), "p3"); err != nil {
		t.Fatalf("EnsureLoaded p3: %v", err)
	}
	if g.Current() != "p3" {
		t.Errorf("current after loading hot-added member: %q", g.Current())
	}
}

func TestGroup_ConcurrentAddMemberAndEnsureLoaded(t *testing.T) {
	// Regression for the race between a hot reload's AddMember (mutating the
	// member map) and a request's EnsureLoaded (reading it). Run under -race.
	g, _ := newTestGroup(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	g.upstreamURLOverride = upstream.URL

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g.AddMember(config.BackendSpec{Name: fmt.Sprintf("m%d", i), Group: "chat"})
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = g.EnsureLoaded(context.Background(), "p1")
			_ = g.Members()
		}()
	}
	wg.Wait()
}

func TestGroup_AddMemberDuplicateAndPortMismatch(t *testing.T) {
	g, _ := newTestGroup(t)

	// p1 already exists.
	if added, _ := g.AddMember(config.BackendSpec{Name: "p1", Group: "chat"}); added {
		t.Error("AddMember should report not-added for an existing member name")
	}

	// A port differing from the group's is reported, and the member still
	// joins on the group's port.
	_, mismatch := g.AddMember(config.BackendSpec{Name: "p9", Group: "chat", Port: 65000})
	if !mismatch {
		t.Error("expected port mismatch to be reported")
	}
	for _, m := range g.Members() {
		if m.Name == "p9" && m.Port != g.opts.Port {
			t.Errorf("hot-added member should inherit group port %d, got %d", g.opts.Port, m.Port)
		}
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
