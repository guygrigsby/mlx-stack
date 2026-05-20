package supervisor

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
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

type Persistent struct {
	opts    PersistentOpts
	mu      sync.Mutex
	current *Worker
	running atomic.Bool
	stopCh  chan struct{}
	doneCh  chan struct{}
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
		opts:   opts,
		state:  &backend.State{},
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

func (p *Persistent) Name() string          { return p.opts.Name }
func (p *Persistent) Group() string         { return p.opts.Name }
func (p *Persistent) Mode() string          { return "persistent" }
func (p *Persistent) Engine() string        { return p.opts.Engine }
func (p *Persistent) BaseURL() string       { return fmt.Sprintf("http://%s:%d", p.opts.Host, p.opts.Port) }
func (p *Persistent) UpstreamModel() string { return p.opts.UpstreamModel }
func (p *Persistent) Running() bool         { return p.running.Load() }
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
	if p.Running() {
		return nil
	}
	return p.Start(ctx)
}

func (p *Persistent) Start(ctx context.Context) error {
	if !p.running.CompareAndSwap(false, true) {
		return nil
	}
	if err := p.spawnAndProbe(ctx); err != nil {
		p.running.Store(false)
		return err
	}
	go p.supervise()
	return nil
}

func (p *Persistent) spawnAndProbe(ctx context.Context) error {
	p.mu.Lock()
	w := p.opts.WorkerFactory(p.opts.Args)
	if err := w.Start(ctx); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("spawn %s: %w", p.opts.Name, err)
	}
	p.current = w
	p.state.WorkerPID = w.PID()
	p.state.WorkerURL = p.BaseURL()
	p.mu.Unlock()
	return p.probeReady(ctx)
}

func (p *Persistent) probeReady(ctx context.Context) error {
	deadline := time.Now().Add(p.opts.ProbeTimeout)
	probeURL := p.BaseURL() + "/v1/models"
	if p.upstreamURLOverride != "" {
		probeURL = p.upstreamURLOverride + "/v1/models"
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
		time.Sleep(p.opts.ProbeInterval)
	}
	return fmt.Errorf("%s not ready within %s", p.opts.Name, p.opts.ProbeTimeout)
}

func (p *Persistent) supervise() {
	defer close(p.doneCh)
	backoffDur := p.opts.BackoffMin
	for {
		p.mu.Lock()
		w := p.current
		p.mu.Unlock()
		if w == nil {
			return
		}
		select {
		case <-p.stopCh:
			return
		case <-w.Done():
			select {
			case <-p.stopCh:
				return
			default:
			}
			time.Sleep(backoffDur)
			backoffDur *= 2
			if backoffDur > p.opts.BackoffMax {
				backoffDur = p.opts.BackoffMax
			}
			if err := p.spawnAndProbe(context.Background()); err != nil {
				continue
			}
			backoffDur = p.opts.BackoffMin
		}
	}
}

func (p *Persistent) Stop(ctx context.Context) error {
	if !p.running.CompareAndSwap(true, false) {
		return nil
	}
	close(p.stopCh)
	p.mu.Lock()
	w := p.current
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
	select {
	case <-p.doneCh:
	case <-time.After(2 * time.Second):
	}
	return nil
}
