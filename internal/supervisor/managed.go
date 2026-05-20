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

type ManagedOpts struct {
	Name          string
	Host          string
	Port          int
	Alias         string
	UpstreamModel string
	Args          []string
	Env           []string
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration
	BackoffMin    time.Duration
	BackoffMax    time.Duration
	WorkerFactory func(args []string) *Worker
}

type Managed struct {
	opts    ManagedOpts
	mu      sync.Mutex
	current *Worker
	running atomic.Bool
	stopCh  chan struct{}
	doneCh  chan struct{}
	state   *backend.ChatState

	upstreamURLOverride string
}

func NewManaged(opts ManagedOpts) *Managed {
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
	return &Managed{
		opts:   opts,
		state:  &backend.ChatState{},
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

func (m *Managed) Name() string { return m.opts.Name }
func (m *Managed) URL() string {
	return fmt.Sprintf("http://%s:%d", m.opts.Host, m.opts.Port)
}
func (m *Managed) Running() bool { return m.running.Load() }
func (m *Managed) PID() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		return m.current.PID()
	}
	return 0
}

// Start launches the worker, waits for the probe, then enters a supervision
// loop in a background goroutine.
func (m *Managed) Start(ctx context.Context) error {
	if !m.running.CompareAndSwap(false, true) {
		return nil
	}
	if err := m.spawnAndProbe(ctx); err != nil {
		m.running.Store(false)
		return err
	}
	go m.supervise()
	return nil
}

func (m *Managed) spawnAndProbe(ctx context.Context) error {
	m.mu.Lock()
	w := m.opts.WorkerFactory(m.opts.Args)
	if err := w.Start(ctx); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("spawn %s: %w", m.opts.Name, err)
	}
	m.current = w
	m.state.WorkerPID = w.PID()
	m.state.WorkerURL = m.URL()
	m.mu.Unlock()
	return m.probeReady(ctx)
}

func (m *Managed) probeReady(ctx context.Context) error {
	deadline := time.Now().Add(m.opts.ProbeTimeout)
	probeURL := m.URL() + "/v1/models"
	if m.upstreamURLOverride != "" {
		probeURL = m.upstreamURLOverride + "/v1/models"
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
		time.Sleep(m.opts.ProbeInterval)
	}
	return fmt.Errorf("%s not ready within %s", m.opts.Name, m.opts.ProbeTimeout)
}

func (m *Managed) supervise() {
	defer close(m.doneCh)
	backoff := m.opts.BackoffMin
	for {
		m.mu.Lock()
		w := m.current
		m.mu.Unlock()
		if w == nil {
			return
		}
		select {
		case <-m.stopCh:
			return
		case <-w.Done():
			select {
			case <-m.stopCh:
				return
			default:
			}
			time.Sleep(backoff)
			backoff *= 2
			if backoff > m.opts.BackoffMax {
				backoff = m.opts.BackoffMax
			}
			if err := m.spawnAndProbe(context.Background()); err != nil {
				continue
			}
			backoff = m.opts.BackoffMin
		}
	}
}

func (m *Managed) Stop(ctx context.Context) error {
	if !m.running.CompareAndSwap(true, false) {
		return nil
	}
	close(m.stopCh)
	m.mu.Lock()
	w := m.current
	m.current = nil
	m.state.WorkerPID = 0
	m.state.WorkerURL = ""
	m.mu.Unlock()
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
	case <-m.doneCh:
	case <-time.After(2 * time.Second):
	}
	return nil
}
