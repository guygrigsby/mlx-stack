package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	bk "github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/guygrigsby/mlx-stack/internal/logobs"
	"github.com/guygrigsby/mlx-stack/internal/obsstate"
)

type Handlers struct {
	Config   *config.Config
	Backends []bk.Backend
	// Aliases maps member-name -> primary backend name (used for swap groups).
	Aliases  map[string]string
	Broker   *logobs.Broker
	ObsStore *obsstate.Store
}

type BackendStatus struct {
	Name        string `json:"name"`
	Group       string `json:"group"`
	Mode        string `json:"mode"`
	Engine      string `json:"engine"`
	URL         string `json:"url"`
	Running     bool   `json:"running"`
	PID         int    `json:"pid"`
	CurrentName string `json:"current_name,omitempty"`
}

type StatusResponse struct {
	Backends []BackendStatus               `json:"backends"`
	Workers  map[string]obsstate.WorkerObs `json:"workers,omitempty"`
}

type nameReq struct {
	Name string `json:"name"`
}

// currentNameAccessor lets us pull Current() from a Group without importing
// the supervisor package (which would create a cycle).
type currentNameAccessor interface {
	Current() string
}

func (h *Handlers) Mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", h.health)
	mux.HandleFunc("GET /v1/status", h.status)
	mux.HandleFunc("POST /v1/start", h.start)
	mux.HandleFunc("POST /v1/stop", h.stop)
	mux.HandleFunc("POST /v1/restart", h.restart)
	mux.HandleFunc("POST /v1/swap", h.start) // alias
	mux.HandleFunc("GET /v1/logs/tail", h.tail)
	return mux
}

func (h *Handlers) byName(name string) (bk.Backend, error) {
	// First try direct match.
	for _, b := range h.Backends {
		if b.Name() == name {
			return b, nil
		}
	}
	// Then try aliases (swap group members).
	if primary, ok := h.Aliases[name]; ok {
		for _, b := range h.Backends {
			if b.Name() == primary {
				return b, nil
			}
		}
	}
	return nil, fmt.Errorf("unknown backend %q", name)
}

func (h *Handlers) health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
	w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handlers) status(w http.ResponseWriter, r *http.Request) {
	statuses := make([]BackendStatus, 0, len(h.Backends))
	for _, b := range h.Backends {
		s := BackendStatus{
			Name:    b.Name(),
			Group:   b.Group(),
			Mode:    b.Mode(),
			Engine:  b.Engine(),
			URL:     b.BaseURL(),
			Running: b.Running(),
			PID:     b.PID(),
		}
		if cn, ok := b.(currentNameAccessor); ok {
			s.CurrentName = cn.Current()
		}
		statuses = append(statuses, s)
	}
	resp := StatusResponse{Backends: statuses}
	if h.ObsStore != nil {
		resp.Workers = h.ObsStore.Snapshot()
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

// to keep the context import live (it's used by request handlers via r.Context()).
var _ = context.Background
