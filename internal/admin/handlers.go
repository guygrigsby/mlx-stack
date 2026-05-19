package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
)

type ChatController interface {
	State() *backend.ChatState
	EnsureProfile(ctx context.Context, name string) error
	Stop(ctx context.Context) error
}

type Handlers struct {
	Config *config.Config
	Chat   ChatController
}

type ChatStatus struct {
	CurrentProfile string   `json:"current_profile"`
	PID            int      `json:"pid"`
	URL            string   `json:"url"`
	Profiles       []string `json:"profiles"`
}

type StatusResponse struct {
	Chat ChatStatus `json:"chat"`
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
