package supervisor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
)

type GroupOpts struct {
	Name           string
	Host           string
	Port           int
	Members        map[string]config.BackendSpec // member name → spec
	DefaultMember  string
	SwapTimeoutSec int
	ProbeInterval  time.Duration
	WorkerFactory  func(spec config.BackendSpec) *Worker

	// BeforeLoad, if set, runs just before a member's worker is spawned (after
	// the lock is held and the member is resolved). A non-nil error aborts the
	// load. mlxd uses it to pull the model into the SSD cache (offload).
	BeforeLoad func(ctx context.Context, spec config.BackendSpec) error
}

type Group struct {
	opts    GroupOpts
	mu      sync.Mutex
	state   *backend.State
	current string
	worker  *Worker

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

// Ready reports whether the loaded member's upstream answers a live probe
// right now. Unlike Running (set once at load and never re-checked), Ready
// re-probes every call, so a member that wedges after loading is reported as
// not ready instead of silently "loaded".
func (g *Group) Ready(ctx context.Context) bool {
	g.mu.Lock()
	loaded := g.worker != nil && g.current != ""
	url := g.BaseURL() + "/v1/models"
	if g.upstreamURLOverride != "" {
		url = g.upstreamURLOverride + "/v1/models"
	}
	g.mu.Unlock()
	if !loaded {
		return false
	}
	return probeOnce(ctx, url)
}

// probeOnce reports whether a single GET to url returns 200 within a short
// timeout. Used for live readiness checks where a wedged upstream must not
// block the caller.
func probeOnce(ctx context.Context, url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
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
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]config.BackendSpec, 0, len(g.opts.Members))
	for _, m := range g.opts.Members {
		out = append(out, m)
	}
	return out
}

// AddMember registers a new swap member at runtime (hot reload). The member
// joins on the group's existing port; its own spec.Port is ignored, and a
// mismatch is reported to the caller so it can warn. Returns false if a member
// of that name already exists (additive reload skips it). The new member is not
// spawned; it loads lazily on first request like any swap member.
func (g *Group) AddMember(spec config.BackendSpec) (added bool, portMismatch bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.opts.Members[spec.Name]; exists {
		return false, false
	}
	mismatch := spec.Port != 0 && spec.Port != g.opts.Port
	spec.Port = g.opts.Port
	spec.Host = g.opts.Host
	g.opts.Members[spec.Name] = spec
	return true, mismatch
}

func (g *Group) EnsureLoaded(ctx context.Context, name string) error {
	// Passing the group name itself (e.g. `mlxctl start chat` or a chat
	// request to model "chat") means "load the default member". Name and
	// DefaultMember are fixed at construction, so reading them before the lock
	// is safe; the Members map is mutable (AddMember), so it's read under mu.
	if name == g.opts.Name {
		if g.opts.DefaultMember == "" {
			return fmt.Errorf("group %q has no default member", g.opts.Name)
		}
		name = g.opts.DefaultMember
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	spec, ok := g.opts.Members[name]
	if !ok {
		return fmt.Errorf("group %q has no member %q", g.opts.Name, name)
	}

	if g.current == name && g.worker != nil {
		return nil
	}

	if g.opts.BeforeLoad != nil {
		if err := g.opts.BeforeLoad(ctx, spec); err != nil {
			return fmt.Errorf("before-load group[%s] member[%s]: %w", g.opts.Name, name, err)
		}
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

	if err := g.probeReady(ctx, w); err != nil {
		_ = g.worker.Signal("KILL")
		g.worker = nil
		g.state.WorkerPID = 0
		g.state.WorkerURL = ""
		return fmt.Errorf("probe group[%s] member[%s]: %w", g.opts.Name, name, err)
	}

	g.current = name
	g.state.Current = name
	g.watchExit(w)
	return nil
}

// watchExit clears the group's loaded state when the worker process exits,
// so the next EnsureLoaded call respawns instead of returning a stale
// "already loaded" and proxying to a dead port.
func (g *Group) watchExit(w *Worker) {
	go func() {
		<-w.Done()
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.worker == w {
			g.worker = nil
			g.current = ""
			g.state.Current = ""
			g.state.WorkerPID = 0
			g.state.WorkerURL = ""
		}
	}()
}

func (g *Group) probeReady(ctx context.Context, w *Worker) error {
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
		// A worker that exits during startup will never answer the probe. Bail
		// out immediately with its own error instead of polling a dead port
		// until the swap timeout and reporting a generic "not ready".
		select {
		case res := <-w.Done():
			return workerExitErr(res, w)
		default:
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
		select {
		case res := <-w.Done():
			return workerExitErr(res, w)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(g.opts.ProbeInterval):
		}
	}
	return fmt.Errorf("not ready within %s", swapTimeout)
}

// workerExitErr builds a readiness error from a worker that died during load,
// carrying the exit code and a tail of its stderr so the real cause (e.g. an
// unsupported model_type) reaches the caller instead of a generic timeout.
func workerExitErr(res WorkerResult, w *Worker) error {
	lines := w.StderrLines()
	const n = 8
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	tail := strings.TrimSpace(strings.Join(lines, "; "))
	if tail == "" {
		tail = "(no stderr)"
	}
	return fmt.Errorf("worker exited during load (code %d): %s", res.ExitCode, tail)
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
