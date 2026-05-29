package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPersistent_StartProbesUntilReady(t *testing.T) {
	var started int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()

	port, _ := freePort()
	p := NewPersistent(PersistentOpts{
		Name:          "tags",
		Engine:        "vlm",
		Host:          "127.0.0.1",
		Port:          port,
		ProbeInterval: 20 * time.Millisecond,
		ProbeTimeout:  5 * time.Second,
		BackoffMin:    50 * time.Millisecond,
		BackoffMax:    200 * time.Millisecond,
		WorkerFactory: func(args []string) *Worker {
			atomic.AddInt32(&started, 1)
			return New(WorkerSpec{Name: "tags", Command: "/bin/sh", Args: []string{"-c", "sleep 2"}})
		},
	})
	p.upstreamURLOverride = upstream.URL

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Stop(context.Background())
	if atomic.LoadInt32(&started) != 1 {
		t.Errorf("want 1 spawn, got %d", started)
	}
	if !p.Running() {
		t.Errorf("Running should be true")
	}
}

func TestPersistent_StopGraceful(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	port, _ := freePort()
	p := NewPersistent(PersistentOpts{
		Name: "tags", Host: "127.0.0.1", Port: port,
		ProbeInterval: 20 * time.Millisecond, ProbeTimeout: 5 * time.Second,
		BackoffMin: 50 * time.Millisecond, BackoffMax: 200 * time.Millisecond,
		WorkerFactory: func(args []string) *Worker {
			return New(WorkerSpec{Name: "tags", Command: "/bin/sh", Args: []string{"-c", "trap 'exit 0' TERM; sleep 5"}})
		},
	})
	p.upstreamURLOverride = upstream.URL

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p.Running() {
		t.Errorf("Running should be false after Stop")
	}
}

func TestPersistent_RestartsOnUnexpectedExit(t *testing.T) {
	var started int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	port, _ := freePort()
	p := NewPersistent(PersistentOpts{
		Name: "tags", Host: "127.0.0.1", Port: port,
		ProbeInterval: 20 * time.Millisecond, ProbeTimeout: 2 * time.Second,
		BackoffMin: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		WorkerFactory: func(args []string) *Worker {
			atomic.AddInt32(&started, 1)
			return New(WorkerSpec{Name: "tags", Command: "/bin/sh", Args: []string{"-c", "exit 1"}})
		},
	})
	p.upstreamURLOverride = upstream.URL

	go p.Start(context.Background())
	time.Sleep(500 * time.Millisecond)
	p.Stop(context.Background())
	if atomic.LoadInt32(&started) < 2 {
		t.Errorf("expected restart, got %d spawns", started)
	}
}

// A worker that never becomes ready (probe keeps failing) must not be spawned
// twice: a second Start while the first is still loading waits, it does not
// kick off a duplicate worker (which would be a duplicate model download).
func TestPersistent_NoDoubleSpawnWhileLoading(t *testing.T) {
	var started int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503) // never ready
	}))
	defer upstream.Close()

	port, _ := freePort()
	p := NewPersistent(PersistentOpts{
		Name: "tags", Host: "127.0.0.1", Port: port,
		ProbeInterval: 20 * time.Millisecond,
		ProbeTimeout:  150 * time.Millisecond,
		BackoffMin:    50 * time.Millisecond, BackoffMax: 200 * time.Millisecond,
		WorkerFactory: func(args []string) *Worker {
			atomic.AddInt32(&started, 1)
			return New(WorkerSpec{Name: "tags", Command: "/bin/sh", Args: []string{"-c", "sleep 3"}})
		},
	})
	p.upstreamURLOverride = upstream.URL

	if err := p.Start(context.Background()); err == nil {
		t.Fatal("expected a timeout error while still loading")
	}
	if got := p.Phase(); got != "loading" {
		t.Fatalf("phase = %q, want loading", got)
	}
	if p.Running() {
		t.Error("Running should be false while loading")
	}
	// Second attempt while loading: must reuse the in-flight load, not respawn.
	_ = p.Start(context.Background())
	if n := atomic.LoadInt32(&started); n != 1 {
		t.Fatalf("want exactly 1 spawn while loading, got %d", n)
	}
	p.Stop(context.Background())
}

func TestPersistent_EnsureLoadedRejectsWrongName(t *testing.T) {
	p := NewPersistent(PersistentOpts{
		Name: "tags", Host: "127.0.0.1", Port: 1,
		WorkerFactory: func(args []string) *Worker { return nil },
	})
	err := p.EnsureLoaded(context.Background(), "other")
	if err == nil {
		t.Error("expected error for wrong name")
	}
}
