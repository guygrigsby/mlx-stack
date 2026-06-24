package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	bk "github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/guygrigsby/mlx-stack/internal/logobs"
	"github.com/guygrigsby/mlx-stack/internal/obsstate"
)

type Handlers struct {
	Config   *config.Config
	Broker   *logobs.Broker
	ObsStore *obsstate.Store

	// Reload, when set, re-reads the config and registers any newly added
	// backends without a restart (additive). POST /v1/reload invokes it.
	Reload func(context.Context) (ReloadResult, error)

	// ActiveFunc, when set, returns a map of backend name → in-flight request count.
	ActiveFunc func() map[string]int

	// Offloader, when set, enables POST /v1/offload and /v1/pull.
	Offloader Offloader

	// Tierer, when set, adds model tier + cache usage to status.
	Tierer Tierer

	// backends and aliases are the live view of registered backends. A hot
	// reload grows them via SetState while request handlers read them, so they
	// are guarded by mu.
	mu       sync.RWMutex
	backends []bk.Backend
	aliases  map[string]string // member-name -> primary backend name (swap groups)
}

// ReloadResult reports what a hot reload changed.
type ReloadResult struct {
	Added   []string `json:"added"`
	Skipped []string `json:"skipped"`
}

// SetState replaces the live backend/alias view. Called once at startup and
// after each successful hot reload.
func (h *Handlers) SetState(backends []bk.Backend, aliases map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.backends = backends
	h.aliases = aliases
}

// snapshot returns the current backend list and alias map under a read lock.
func (h *Handlers) snapshot() ([]bk.Backend, map[string]string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.backends, h.aliases
}

type BackendStatus struct {
	Name        string `json:"name"`
	Group       string `json:"group"`
	Mode        string `json:"mode"`
	Engine      string `json:"engine"`
	URL         string `json:"url"`
	Running     bool   `json:"running"`
	State       string `json:"state"` // "stopped" | "loading" | "ready"
	Model       string `json:"model,omitempty"`
	PID         int    `json:"pid"`
	CurrentName string `json:"current_name,omitempty"`
	Tier        string `json:"tier,omitempty"`
}

type StatusResponse struct {
	Router           RouterInfo                    `json:"router"`
	Backends         []BackendStatus               `json:"backends"`
	Workers          map[string]obsstate.WorkerObs `json:"workers,omitempty"`
	Active           map[string]int                `json:"active,omitempty"`
	CacheUsedBytes   int64                         `json:"cache_used_bytes,omitempty"`
	CacheBudgetBytes int64                         `json:"cache_budget_bytes,omitempty"`
}

type RouterInfo struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	ExtraPorts []int  `json:"extra_ports,omitempty"`
}

type nameReq struct {
	Name string `json:"name"`
}

// currentNameAccessor lets us pull Current() from a Group without importing
// the supervisor package (which would create a cycle).
type currentNameAccessor interface {
	Current() string
}

// readinessChecker lets status confirm a backend the supervisor believes is
// loaded can actually serve right now. A backend that wedges after loading
// still reports Running()==true forever (the load-time probe never re-runs),
// so status probes the upstream live and reports "unhealthy" when it can't
// answer. Backends that can probe their upstream implement it.
type readinessChecker interface {
	Ready(ctx context.Context) bool
}

// Offloader is the subset of offload.Manager the admin layer uses. Defined here
// (not imported) to keep admin decoupled from the offload package.
type Offloader interface {
	Offload(name string) error
	Pull(ctx context.Context, name string) error
}

// Tierer exposes model tier + cache accounting for status. *offload.Manager
// satisfies it via TierName/CacheUsed/Budget.
type Tierer interface {
	TierName(name string) string
	CacheUsed() (int64, error)
	Budget() int64
}

// readinessProbeTimeout bounds each live probe so a wedged backend can't make
// the status call itself hang.
const readinessProbeTimeout = 2 * time.Second

func (h *Handlers) Mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", h.health)
	mux.HandleFunc("GET /v1/status", h.status)
	mux.HandleFunc("POST /v1/start", h.start)
	mux.HandleFunc("POST /v1/stop", h.stop)
	mux.HandleFunc("POST /v1/restart", h.restart)
	mux.HandleFunc("POST /v1/swap", h.start) // alias
	mux.HandleFunc("POST /v1/reload", h.reload)
	mux.HandleFunc("POST /v1/offload", h.offload)
	mux.HandleFunc("POST /v1/pull", h.pull)
	mux.HandleFunc("GET /v1/logs/tail", h.tail)
	return mux
}

func (h *Handlers) byName(name string) (bk.Backend, error) {
	backends, aliases := h.snapshot()
	// First try direct match.
	for _, b := range backends {
		if b.Name() == name {
			return b, nil
		}
	}
	// Then try aliases (swap group members).
	if primary, ok := aliases[name]; ok {
		for _, b := range backends {
			if b.Name() == primary {
				return b, nil
			}
		}
	}
	// Case-insensitive fallback. Backends register lowercased names but HF
	// repo ids are mixed-case, so `mlx start Model-MXFP4-Q4` should resolve.
	lower := strings.ToLower(name)
	for _, b := range backends {
		if strings.ToLower(b.Name()) == lower {
			return b, nil
		}
	}
	for alias, primary := range aliases {
		if strings.ToLower(alias) == lower {
			for _, b := range backends {
				if b.Name() == primary {
					return b, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("unknown backend %q", name)
}

func (h *Handlers) reload(w http.ResponseWriter, r *http.Request) {
	if h.Reload == nil {
		http.Error(w, "reload not supported", 501)
		return
	}
	res, err := h.Reload(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func (h *Handlers) health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
	w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handlers) status(w http.ResponseWriter, r *http.Request) {
	backends, _ := h.snapshot()
	statuses := make([]BackendStatus, 0, len(backends))
	for _, b := range backends {
		s := BackendStatus{
			Name:    b.Name(),
			Group:   b.Group(),
			Mode:    b.Mode(),
			Engine:  b.Engine(),
			URL:     b.BaseURL(),
			Running: b.Running(),
			Model:   b.UpstreamModel(),
			PID:     b.PID(),
		}
		if b.Running() {
			s.State = "ready"
		} else {
			s.State = "stopped"
		}
		// A backend the supervisor reports as ready might have wedged after
		// loading. Probe it live; if it can't answer, report the truth so
		// status stops claiming a frozen backend is loaded.
		if s.State == "ready" {
			if rc, ok := b.(readinessChecker); ok {
				pctx, cancel := context.WithTimeout(r.Context(), readinessProbeTimeout)
				ready := rc.Ready(pctx)
				cancel()
				if !ready {
					s.State = "unhealthy"
					s.Running = false
				}
			}
		}
		if cn, ok := b.(currentNameAccessor); ok {
			s.CurrentName = cn.Current()
		}
		if h.Tierer != nil && b.UpstreamModel() != "" {
			s.Tier = h.Tierer.TierName(filepath.Base(b.UpstreamModel()))
		}
		statuses = append(statuses, s)
	}
	resp := StatusResponse{Backends: statuses}
	if h.Tierer != nil {
		if used, err := h.Tierer.CacheUsed(); err == nil {
			resp.CacheUsedBytes = used
			resp.CacheBudgetBytes = h.Tierer.Budget()
		}
	}
	if h.Config != nil {
		resp.Router = RouterInfo{
			Host:       h.Config.Router.Host,
			Port:       h.Config.Router.Port,
			ExtraPorts: h.Config.Router.ExtraPorts,
		}
	}
	if h.ObsStore != nil {
		resp.Workers = h.ObsStore.Snapshot()
	}
	if h.ActiveFunc != nil {
		resp.Active = h.ActiveFunc()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handlers) start(w http.ResponseWriter, r *http.Request) {
	var req nameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	b, err := h.byName(req.Name)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := b.EnsureLoaded(r.Context(), req.Name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func (h *Handlers) stop(w http.ResponseWriter, r *http.Request) {
	var req nameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	b, err := h.byName(req.Name)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := b.Stop(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func (h *Handlers) restart(w http.ResponseWriter, r *http.Request) {
	var req nameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	b, err := h.byName(req.Name)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := b.Stop(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := b.EnsureLoaded(r.Context(), req.Name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func (h *Handlers) offload(w http.ResponseWriter, r *http.Request) {
	if h.Offloader == nil {
		http.Error(w, "offload not configured", 501)
		return
	}
	var req nameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := h.Offloader.Offload(req.Name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func (h *Handlers) pull(w http.ResponseWriter, r *http.Request) {
	if h.Offloader == nil {
		http.Error(w, "offload not configured", 501)
		return
	}
	var req nameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := h.Offloader.Pull(r.Context(), req.Name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func (h *Handlers) tail(w http.ResponseWriter, r *http.Request) {
	if h.Broker == nil {
		http.Error(w, "broker not configured", 503)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	filter := r.URL.Query().Get("worker") // optional ?worker=name filter
	events := h.Broker.Subscribe(r.Context())
	for ev := range events {
		if filter != "" && ev.Worker != filter {
			continue
		}
		_, err := fmt.Fprintf(w, "data: %s\n\n", ev.Raw)
		if err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
