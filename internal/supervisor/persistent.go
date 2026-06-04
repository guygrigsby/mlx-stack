package supervisor

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/guygrigsby/mlx-stack/internal/backend"
)

type PersistentOpts struct {
	Name          string
	Engine        string
	Host          string
	Port          int
	UpstreamModel string
	Args          []string
	Env           []string
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration
	BackoffMin    time.Duration
	BackoffMax    time.Duration
	WorkerFactory func(args []string) *Worker
}

// phase is the load lifecycle of a persistent backend.
type phase int

const (
	phaseStopped phase = iota
	phaseLoading       // worker spawned, not yet serving (starting up, possibly downloading the model)
	phaseReady         // probe succeeded; serving requests
)

type Persistent struct {
	opts PersistentOpts
	mu   sync.Mutex

	current *Worker
	ph      phase
	readyCh chan struct{} // closed when ph -> ready; recreated each load cycle
	stopCh  chan struct{}
	doneCh  chan struct{} // closed when the manager goroutine exits
	state   *backend.State

	upstreamURLOverride string // tests only
}

func NewPersistent(opts PersistentOpts) *Persistent {
	if opts.ProbeInterval == 0 {
		opts.ProbeInterval = 250 * time.Millisecond
	}
	if opts.ProbeTimeout == 0 {
		opts.ProbeTimeout = 90 * time.Second
	}
	if opts.BackoffMin == 0 {
		opts.BackoffMin = time.Second
	}
	if opts.BackoffMax == 0 {
		opts.BackoffMax = 30 * time.Second
	}
	return &Persistent{
		opts:  opts,
		state: &backend.State{},
	}
}

func (p *Persistent) Name() string          { return p.opts.Name }
func (p *Persistent) Group() string         { return p.opts.Name }
func (p *Persistent) Mode() string          { return "persistent" }
func (p *Persistent) Engine() string        { return p.opts.Engine }
func (p *Persistent) BaseURL() string       { return fmt.Sprintf("http://%s:%d", p.opts.Host, p.opts.Port) }
func (p *Persistent) UpstreamModel() string { return p.opts.UpstreamModel }

func (p *Persistent) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ph == phaseReady
}

// Ready reports whether the backend's upstream answers a live probe right now.
// Running/Phase report phaseReady from the load-time probe and never re-check,
// so Ready re-probes to catch a worker that wedged after becoming ready.
func (p *Persistent) Ready(ctx context.Context) bool {
	p.mu.Lock()
	ready := p.ph == phaseReady
	url := p.BaseURL() + "/v1/models"
	if p.upstreamURLOverride != "" {
		url = p.upstreamURLOverride + "/v1/models"
	}
	p.mu.Unlock()
	if !ready {
		return false
	}
	return probeOnce(ctx, url)
}

// Phase reports the load lifecycle: "stopped", "loading", or "ready". "loading"
// covers a worker that's started but not yet serving, which on a first run
// includes downloading the model from Hugging Face.
func (p *Persistent) Phase() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.ph {
	case phaseReady:
		return "ready"
	case phaseLoading:
		return "loading"
	default:
		return "stopped"
	}
}

func (p *Persistent) PID() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != nil {
		return p.current.PID()
	}
	return 0
}

func (p *Persistent) EnsureLoaded(ctx context.Context, name string) error {
	if name != p.opts.Name {
		return fmt.Errorf("persistent backend %q cannot serve %q", p.opts.Name, name)
	}
	return p.Start(ctx)
}

// Start brings the backend up and blocks until it's serving (or ctx/probe
// timeout). It spawns the worker at most once per load cycle: if a load is
// already in flight, concurrent callers wait on the same cycle rather than
// spawning a second worker (which would mean a second model download).
func (p *Persistent) Start(ctx context.Context) error {
	p.mu.Lock()
	switch p.ph {
	case phaseReady:
		p.mu.Unlock()
		return nil
	case phaseLoading:
		ch := p.readyCh
		p.mu.Unlock()
		return p.waitReady(ctx, ch)
	}

	// phaseStopped: this caller owns the spawn.
	p.ph = phaseLoading
	p.readyCh = make(chan struct{})
	p.stopCh = make(chan struct{})
	p.doneCh = make(chan struct{})
	ch := p.readyCh
	w := p.opts.WorkerFactory(p.opts.Args)
	p.mu.Unlock()

	if err := w.Start(ctx); err != nil {
		p.mu.Lock()
		p.ph = phaseStopped
		p.mu.Unlock()
		return fmt.Errorf("spawn %s: %w", p.opts.Name, err)
	}
	p.mu.Lock()
	p.current = w
	p.state.WorkerPID = w.PID()
	p.state.WorkerURL = p.BaseURL()
	p.mu.Unlock()

	go p.manage(w)
	return p.waitReady(ctx, ch)
}

// waitReady blocks until the load cycle for ch reaches ready, ctx is cancelled,
// or ProbeTimeout elapses. A timeout does NOT abort the load: the worker keeps
// running (and may still be downloading), so the next caller just waits again
// instead of spawning a duplicate.
func (p *Persistent) waitReady(ctx context.Context, ch chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(p.opts.ProbeTimeout):
		select {
		case <-ch:
			return nil
		default:
		}
		return fmt.Errorf("%s still loading after %s (model may be downloading)", p.opts.Name, p.opts.ProbeTimeout)
	}
}

// manage owns a worker for its lifetime: it probes for readiness and, on
// unexpected exit, respawns with backoff until Stop is called.
func (p *Persistent) manage(w *Worker) {
	defer func() {
		p.mu.Lock()
		done := p.doneCh
		p.mu.Unlock()
		close(done)
	}()

	backoff := p.opts.BackoffMin
	for {
		go p.probeUntilReady(w)

		select {
		case <-p.stopCh:
			return
		case <-w.Done():
			select {
			case <-p.stopCh:
				return
			default:
			}
			// Lost the worker before/after readiness. Drop back to loading
			// and respawn; readiness is re-established by the next probe.
			p.mu.Lock()
			p.ph = phaseLoading
			p.readyCh = make(chan struct{})
			p.mu.Unlock()

			time.Sleep(backoff)
			backoff *= 2
			if backoff > p.opts.BackoffMax {
				backoff = p.opts.BackoffMax
			}
			nw := p.opts.WorkerFactory(p.opts.Args)
			if err := nw.Start(context.Background()); err != nil {
				continue // try again next loop
			}
			p.mu.Lock()
			p.current = nw
			p.state.WorkerPID = nw.PID()
			p.state.WorkerURL = p.BaseURL()
			p.mu.Unlock()
			w = nw
			backoff = p.opts.BackoffMin
		}
	}
}

// probeUntilReady polls the upstream until it answers, then flips the current
// cycle to ready. It exits if the worker dies or the backend is stopped, with
// no fixed deadline so slow first-run downloads still come up eventually.
func (p *Persistent) probeUntilReady(w *Worker) {
	probeURL := p.BaseURL() + "/v1/models"
	if p.upstreamURLOverride != "" {
		probeURL = p.upstreamURLOverride + "/v1/models"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-p.stopCh:
			return
		case <-w.Done():
			return
		default:
		}
		req, _ := http.NewRequest("GET", probeURL, nil)
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			p.mu.Lock()
			if p.ph == phaseLoading {
				p.ph = phaseReady
				close(p.readyCh)
			}
			p.mu.Unlock()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(p.opts.ProbeInterval)
	}
}

func (p *Persistent) Stop(ctx context.Context) error {
	p.mu.Lock()
	if p.ph == phaseStopped {
		p.mu.Unlock()
		return nil
	}
	p.ph = phaseStopped
	close(p.stopCh)
	w := p.current
	done := p.doneCh
	p.current = nil
	p.state.WorkerPID = 0
	p.state.WorkerURL = ""
	p.mu.Unlock()

	if w != nil {
		_ = w.Signal("TERM")
		select {
		case <-w.Done():
		case <-time.After(30 * time.Second):
			_ = w.Signal("KILL")
			<-w.Done()
		case <-ctx.Done():
			_ = w.Signal("KILL")
			return ctx.Err()
		}
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}
