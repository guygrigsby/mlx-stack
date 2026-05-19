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

// ChatSwapOpts configures a ChatSwap instance.
type ChatSwapOpts struct {
	Host           string
	Port           int
	Profiles       map[string]config.Profile
	DefaultProfile string
	SwapTimeoutSec int
	ProbeInterval  time.Duration
	WorkerFactory  func(name string, args []string) *Worker
	WorkerEnv      []string
}

// ChatSwap is the in-process supervisor for the chat backend. It holds a mutex
// that serializes all EnsureProfile calls so concurrent requests for the same
// profile result in exactly one spawn.
type ChatSwap struct {
	host           string
	port           int
	profiles       map[string]config.Profile
	defaultProfile string
	swapTimeout    time.Duration
	probeInterval  time.Duration
	factory        func(name string, args []string) *Worker
	env            []string

	mu      sync.Mutex
	state   *backend.ChatState
	current *Worker

	// upstreamURLOverride is used in tests to redirect probe requests to a
	// local httptest.Server instead of the real worker URL.
	upstreamURLOverride string
}

// NewChatSwap creates a ChatSwap from opts, applying defaults for zero values.
func NewChatSwap(opts ChatSwapOpts) *ChatSwap {
	pi := opts.ProbeInterval
	if pi == 0 {
		pi = 250 * time.Millisecond
	}
	st := opts.SwapTimeoutSec
	if st == 0 {
		st = 90
	}
	return &ChatSwap{
		host:           opts.Host,
		port:           opts.Port,
		profiles:       opts.Profiles,
		defaultProfile: opts.DefaultProfile,
		swapTimeout:    time.Duration(st) * time.Second,
		probeInterval:  pi,
		factory:        opts.WorkerFactory,
		env:            opts.WorkerEnv,
		state:          &backend.ChatState{},
	}
}

// State returns the live ChatState. Callers must not rely on it being
// consistent while a swap is in progress (the lock is held during swaps).
func (c *ChatSwap) State() *backend.ChatState { return c.state }

// URL returns the base URL the worker listens on.
func (c *ChatSwap) URL() string {
	return fmt.Sprintf("http://%s:%d", c.host, c.port)
}

// Profiles returns the configured profile map.
func (c *ChatSwap) Profiles() map[string]config.Profile { return c.profiles }

func (c *ChatSwap) probeURL() string {
	if c.upstreamURLOverride != "" {
		return c.upstreamURLOverride + "/v1/models"
	}
	return c.URL() + "/v1/models"
}

// EnsureProfile guarantees the worker running the named profile is up and
// accepting requests before returning. The mutex is held for the entire
// operation so concurrent callers for the same profile block until the first
// one completes, then return immediately on the already-current check.
func (c *ChatSwap) EnsureProfile(ctx context.Context, name string) error {
	prof, ok := c.profiles[name]
	if !ok {
		return fmt.Errorf("unknown profile %q", name)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Already running the requested profile — nothing to do.
	if c.state.CurrentProfile == name && c.current != nil {
		return nil
	}

	// Kill old worker asynchronously so we don't block on its exit.
	if c.current != nil {
		_ = c.current.Signal("TERM")
		old := c.current
		go func() {
			select {
			case <-old.Done():
			case <-time.After(10 * time.Second):
				_ = old.Signal("KILL")
				<-old.Done()
			}
		}()
		c.current = nil
		c.state.CurrentProfile = ""
		c.state.WorkerPID = 0
		c.state.WorkerURL = ""
	}

	args := buildLauncherArgs(prof, c.host, c.port)
	w := c.factory(fmt.Sprintf("chat[%s]", name), args)
	if err := w.Start(ctx); err != nil {
		return fmt.Errorf("spawn chat worker: %w", err)
	}
	c.current = w
	c.state.WorkerPID = w.PID()
	c.state.WorkerURL = c.URL()

	if err := c.probeReady(ctx); err != nil {
		_ = c.current.Signal("KILL")
		c.current = nil
		c.state.WorkerPID = 0
		c.state.WorkerURL = ""
		return fmt.Errorf("probe chat[%s]: %w", name, err)
	}

	c.state.CurrentProfile = name
	return nil
}

// probeReady polls GET <probeURL> every probeInterval until 200 OK or timeout.
// Must be called with c.mu held (the lock is held for the full swap duration).
func (c *ChatSwap) probeReady(ctx context.Context) error {
	deadline := time.Now().Add(c.swapTimeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, "GET", c.probeURL(), nil)
		if err != nil {
			return fmt.Errorf("build probe request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(c.probeInterval)
	}
	return fmt.Errorf("not ready within %s", c.swapTimeout)
}

// buildLauncherArgs produces the argument list for the Python launcher shim.
func buildLauncherArgs(p config.Profile, host string, port int) []string {
	args := []string{
		"-m", "mlx_stack.launcher_shim",
		"--engine", p.Engine,
		"--model", p.Model,
		"--host", host,
		"--port", fmt.Sprintf("%d", port),
	}
	if p.Draft != "" {
		args = append(args, "--draft-model", p.Draft)
	}
	return args
}

// UpstreamModel returns the absolute model path the upstream server expects
// in the "model" field. Empty string if name not in profiles.
func (c *ChatSwap) UpstreamModel(name string) string {
	p, ok := c.profiles[name]
	if !ok {
		return ""
	}
	return p.Model
}

// BaseURL is what the router proxies to.
func (c *ChatSwap) BaseURL() string { return c.URL() }
