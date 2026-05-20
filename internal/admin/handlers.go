package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/guygrigsby/mlx-stack/internal/logobs"
	"github.com/guygrigsby/mlx-stack/internal/obsstate"
)

type ChatController interface {
	State() *backend.ChatState
	EnsureProfile(ctx context.Context, name string) error
	Stop(ctx context.Context) error
}

type TagsController interface {
	Alias() string
	PID() int
	BaseURL() string
	Running() bool
}

type Handlers struct {
	Config   *config.Config
	Chat     ChatController
	Tags     TagsController
	Broker   *logobs.Broker
	ObsStore *obsstate.Store
}

type ChatStatus struct {
	CurrentProfile string   `json:"current_profile"`
	PID            int      `json:"pid"`
	URL            string   `json:"url"`
	Profiles       []string `json:"profiles"`
}

type TagsStatus struct {
	Alias   string `json:"alias"`
	PID     int    `json:"pid"`
	URL     string `json:"url"`
	Running bool   `json:"running"`
}

type StatusResponse struct {
	Chat    ChatStatus                   `json:"chat"`
	Tags    *TagsStatus                  `json:"tags,omitempty"`
	Workers map[string]obsstate.WorkerObs `json:"workers,omitempty"`
}

type swapReq struct {
	Profile string `json:"profile"`
}

type backendReq struct {
	Backend string `json:"backend"`
}

func (h *Handlers) Mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", h.health)
	mux.HandleFunc("GET /v1/status", h.status)
	mux.HandleFunc("POST /v1/swap", h.swap)
	mux.HandleFunc("POST /v1/start", h.start)
	mux.HandleFunc("POST /v1/stop", h.stop)
	mux.HandleFunc("POST /v1/restart", h.restart)
	mux.HandleFunc("GET /v1/logs/tail", h.tail)
	return mux
}

func (h *Handlers) health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
	w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handlers) status(w http.ResponseWriter, r *http.Request) {
	st := h.Chat.State()
	profiles := make([]string, 0, len(h.Config.Chat.Profiles))
	for name := range h.Config.Chat.Profiles {
		profiles = append(profiles, name)
	}
	sort.Strings(profiles)
	resp := StatusResponse{
		Chat: ChatStatus{
			CurrentProfile: st.CurrentProfile,
			PID:            st.WorkerPID,
			URL:            st.WorkerURL,
			Profiles:       profiles,
		},
	}
	if h.Tags != nil {
		resp.Tags = &TagsStatus{
			Alias:   h.Tags.Alias(),
			PID:     h.Tags.PID(),
			URL:     h.Tags.BaseURL(),
			Running: h.Tags.Running(),
		}
	}
	if h.ObsStore != nil {
		resp.Workers = h.ObsStore.Snapshot()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handlers) swap(w http.ResponseWriter, r *http.Request) {
	var req swapReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if _, ok := h.Config.Chat.Profiles[req.Profile]; !ok {
		http.Error(w, fmt.Sprintf("unknown profile %q", req.Profile), 400)
		return
	}
	if err := h.Chat.EnsureProfile(r.Context(), req.Profile); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func (h *Handlers) start(w http.ResponseWriter, r *http.Request) {
	var req backendReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Backend != "chat" {
		http.Error(w, "only 'chat' supported in phase 1", 400)
		return
	}
	if err := h.Chat.EnsureProfile(r.Context(), h.Config.Chat.DefaultProfile); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func (h *Handlers) stop(w http.ResponseWriter, r *http.Request) {
	var req backendReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Backend != "chat" {
		http.Error(w, "only 'chat' supported in phase 1", 400)
		return
	}
	if err := h.Chat.Stop(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

func (h *Handlers) restart(w http.ResponseWriter, r *http.Request) {
	var req backendReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Backend != "chat" {
		http.Error(w, "only 'chat' supported in phase 1", 400)
		return
	}
	if err := h.Chat.Stop(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := h.Chat.EnsureProfile(r.Context(), h.Config.Chat.DefaultProfile); err != nil {
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
	events := h.Broker.Subscribe(r.Context())
	for ev := range events {
		_, err := fmt.Fprintf(w, "data: %s\n\n", ev.Raw)
		if err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
