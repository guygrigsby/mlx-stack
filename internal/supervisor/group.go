package supervisor

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
)

type GroupOpts struct {
	Name          string
	Host          string
	Port          int
	Members       map[string]config.BackendSpec // member name → spec
	DefaultMember string
	SwapTimeoutSec int
	ProbeInterval time.Duration
	WorkerFactory func(spec config.BackendSpec) *Worker
}

type Group struct {
	opts   GroupOpts
	mu     sync.Mutex
	state  *backend.State
	current string
	worker *Worker

	upstreamURLOverride string
}

func NewGroup(opts GroupOpts) *Group {
	if opts.ProbeInterval == 0 {
		opts.ProbeInterval = 250 * time.Millisecond
	}
	if opts.SwapTimeoutSec == 0 {
		opts.SwapTimeoutSec = 90
	}
	return &Group{
		opts:  opts,
		state: &backend.State{},
	}
}

func (g *Group) Name() string  { return g.opts.Name }
func (g *Group) Group() string { return g.opts.Name }
func (g *Group) Mode() string  { return "swap" }

func (g *Group) Engine() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.current != "" {
		if m, ok := g.opts.Members[g.current]; ok {
			return m.Engine
		}
	}
	for _, m := range g.opts.Members {
		return m.Engine
	}
	return ""
}

func (g *Group) BaseURL() string {
	return fmt.Sprintf("http://%s:%d", g.opts.Host, g.opts.Port)
}

func (g *Group) UpstreamModel() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.current == "" {
		return ""
	}
	return g.opts.Members[g.current].Model
}

func (g *Group) Running() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.worker != nil && g.current != ""
}

func (g *Group) PID() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.worker != nil {
		return g.worker.PID()
	}
	return 0
}

func (g *Group) Current() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.current
}

// Members returns the spec slice in arbitrary order.
func (g *Group) Members() []config.BackendSpec {
	out := make([]config.BackendSpec, 0, len(g.opts.Members))
	for _, m := range g.opts.Members {
		out = append(out, m)
	}
	return out
}

func (g *Group) EnsureLoaded(ctx context.Context, name string) error {
	spec, ok := g.opts.Members[name]
	if !ok {
		return fmt.Errorf("group %q has no member %q", g.opts.Name, name)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.current == name && g.worker != nil {
		return nil
	}

	// Kill old worker
	if g.worker != nil {
		_ = g.worker.Signal("TERM")
		old := g.worker
		go func() {
			select {
			case <-old.Done():
			case <-time.After(10 * time.Second):
				_ = old.Signal("KILL")
				<-old.Done()
			}
		}()
		g.worker = nil
		g.current = ""
		g.state.WorkerPID = 0
		g.state.WorkerURL = ""
	}

	w := g.opts.WorkerFactory(spec)
	if err := w.Start(ctx); err != nil {
		return fmt.Errorf("spawn group[%s] member[%s]: %w", g.opts.Name, name, err)
	}
	g.worker = w
	g.state.WorkerPID = w.PID()
	g.state.WorkerURL = g.BaseURL()

	if err := g.probeReady(ctx); err != nil {
		_ = g.worker.Signal("KILL")
		g.worker = nil
		g.state.WorkerPID = 0
		g.state.WorkerURL = ""
		return fmt.Errorf("probe group[%s] member[%s]: %w", g.opts.Name, name, err)
	}

	g.current = name
	g.state.Current = name
	return nil
}

func (g *Group) probeReady(ctx context.Context) error {
	swapTimeout := time.Duration(g.opts.SwapTimeoutSec) * time.Second
	deadline := time.Now().Add(swapTimeout)
	probeURL := g.BaseURL() + "/v1/models"
	if g.upstreamURLOverride != "" {
		probeURL = g.upstreamURLOverride + "/v1/models"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", probeURL, nil)
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(g.opts.ProbeInterval)
	}
	return fmt.Errorf("not ready within %s", swapTimeout)
}

func (g *Group) Start(ctx context.Context) error {
	if g.opts.DefaultMember == "" {
		return fmt.Errorf("group %q has no default member", g.opts.Name)
	}
	return g.EnsureLoaded(ctx, g.opts.DefaultMember)
}

func (g *Group) Stop(ctx context.Context) error {
	g.mu.Lock()
	if g.worker == nil {
		g.mu.Unlock()
		return nil
	}
	old := g.worker
	g.worker = nil
	g.current = ""
	g.state.Current = ""
	g.state.WorkerPID = 0
	g.state.WorkerURL = ""
	g.mu.Unlock()

	_ = old.Signal("TERM")
	select {
	case <-old.Done():
	case <-time.After(30 * time.Second):
		_ = old.Signal("KILL")
		<-old.Done()
	case <-ctx.Done():
		_ = old.Signal("KILL")
		return ctx.Err()
	}
	return nil
}
