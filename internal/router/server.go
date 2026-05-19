package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

// ChatSwapper is the subset of supervisor.ChatSwap that the router uses.
type ChatSwapper interface {
	EnsureProfile(ctx context.Context, name string) error
	UpstreamModel(name string) string
	BaseURL() string
}

type ServerOpts struct {
	Config *config.Config
	Chat   ChatSwapper
}

type Server struct {
	cfg     *config.Config
	chat    ChatSwapper
	catalog *Catalog
}

func NewServer(opts ServerOpts) *Server {
	return &Server{
		cfg:     opts.Config,
		chat:    opts.Chat,
		catalog: NewCatalog(opts.Config),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("POST /v1/completions", s.handleChat)
	mux.HandleFunc("GET /v1/models", s.handleListModels)
	mux.HandleFunc("GET /health", s.handleHealth)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(204)
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.catalog.OpenAIResponse())
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	model, err := ExtractModel(body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if _, ok := s.cfg.Chat.Profiles[model]; !ok {
		http.Error(w, fmt.Sprintf("unknown model %q", model), 400)
		return
	}

	if err := s.chat.EnsureProfile(r.Context(), model); err != nil {
		http.Error(w, "ensure profile: "+err.Error(), 502)
		return
	}

	_ = ProxyJSON(w, r, s.chat.BaseURL(), s.chat.UpstreamModel(model))
}
