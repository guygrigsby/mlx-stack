package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

func TestChatSwap_NoSwapWhenAlreadyCurrent(t *testing.T) {
	cs, started := newTestSwap(t)
	st := cs.State()
	st.CurrentProfile = "p1"
	st.WorkerURL = "http://127.0.0.1:1"
	// Plant a dummy worker so EnsureProfile short-circuits. Tests are in
	// the same package so we can write the unexported field directly.
	cs.current = &Worker{spec: WorkerSpec{Name: "dummy"}}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := cs.EnsureProfile(ctx, "p1"); err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	if atomic.LoadInt32(started) != 0 {
		t.Errorf("expected zero spawns, got %d", *started)
	}
}

func TestChatSwap_FirstStartProbesUntilReady(t *testing.T) {
	cs, started := newTestSwap(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(200)
			w.Write([]byte(`{"data":[{"id":"x"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	cs.upstreamURLOverride = upstream.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cs.EnsureProfile(ctx, "p1"); err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	if atomic.LoadInt32(started) != 1 {
		t.Errorf("expected 1 spawn, got %d", *started)
	}
	if cs.State().CurrentProfile != "p1" {
		t.Errorf("CurrentProfile: %q", cs.State().CurrentProfile)
	}
}

func TestChatSwap_SwapKillsOldSpawnsNew(t *testing.T) {
	cs, started := newTestSwap(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()
	cs.upstreamURLOverride = upstream.URL

	ctx := context.Background()
	if err := cs.EnsureProfile(ctx, "p1"); err != nil {
		t.Fatalf("first EnsureProfile: %v", err)
	}
	firstPID := cs.State().WorkerPID

	if err := cs.EnsureProfile(ctx, "p2"); err != nil {
		t.Fatalf("second EnsureProfile: %v", err)
	}
	if cs.State().CurrentProfile != "p2" {
		t.Errorf("CurrentProfile after swap: %q", cs.State().CurrentProfile)
	}
	if cs.State().WorkerPID == firstPID {
		t.Errorf("expected different PID after swap")
	}
	if n := atomic.LoadInt32(started); n != 2 {
		t.Errorf("expected 2 spawns, got %d", n)
	}
}

func TestChatSwap_UnknownProfile(t *testing.T) {
	cs, _ := newTestSwap(t)
	ctx := context.Background()
	err := cs.EnsureProfile(ctx, "ghost")
	if err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("expected unknown profile error, got: %v", err)
	}
}

func TestChatSwap_ConcurrentRequestsSwapOnce(t *testing.T) {
	cs, started := newTestSwap(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()
	cs.upstreamURLOverride = upstream.URL

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cs.EnsureProfile(context.Background(), "p1")
		}()
	}
	wg.Wait()
	if n := atomic.LoadInt32(started); n != 1 {
		t.Errorf("expected 1 spawn under concurrency, got %d", n)
	}
}

func newTestSwap(t *testing.T) (*ChatSwap, *int32) {
	t.Helper()
	var started int32
	profiles := map[string]config.Profile{
		"p1": {Model: "/tmp/p1", Engine: "lm"},
		"p2": {Model: "/tmp/p2", Engine: "lm"},
	}
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	cs := NewChatSwap(ChatSwapOpts{
		Host:           "127.0.0.1",
		Port:           port,
		Profiles:       profiles,
		DefaultProfile: "p1",
		SwapTimeoutSec: 5,
		ProbeInterval:  20 * time.Millisecond,
		WorkerFactory: func(name string, args []string) *Worker {
			atomic.AddInt32(&started, 1)
			return New(WorkerSpec{
				Name:    name,
				Command: "/bin/sh",
				Args:    []string{"-c", "sleep 2"},
			})
		},
	})
	return cs, &started
}

func freePort() (int, error) {
	l, err := newListener()
	if err != nil {
		return 0, err
	}
	defer l.Close()
	_, ps, _ := strings.Cut(l.Addr().String(), ":")
	p, _ := strconv.Atoi(ps)
	return p, nil
}

func TestChatSwap_UpstreamModelAndBaseURL(t *testing.T) {
	cs, _ := newTestSwap(t)
	if got := cs.UpstreamModel("p1"); got != "/tmp/p1" {
		t.Errorf("UpstreamModel: %q", got)
	}
	if got := cs.UpstreamModel("ghost"); got != "" {
		t.Errorf("UpstreamModel ghost: %q", got)
	}
	if !strings.Contains(cs.BaseURL(), "127.0.0.1:") {
		t.Errorf("BaseURL: %q", cs.BaseURL())
	}
}

func TestChatSwap_Stop(t *testing.T) {
	cs, _ := newTestSwap(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()
	cs.upstreamURLOverride = upstream.URL

	if err := cs.EnsureProfile(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if err := cs.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	st := cs.State()
	if st.CurrentProfile != "" || st.WorkerPID != 0 {
		t.Errorf("state not cleared: %+v", st)
	}
}
