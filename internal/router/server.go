package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

type ServerOpts struct {
	Config   *config.Config
	Registry *Registry
}

type Server struct {
	cfg      *config.Config
	registry *Registry
}

func NewServer(opts ServerOpts) *Server {
	return &Server{
		cfg:      opts.Config,
		registry: opts.Registry,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.handleProxyByModel)
	mux.HandleFunc("POST /v1/completions", s.handleProxyByModel)
	mux.HandleFunc("POST /v1/embeddings", s.handleProxyByModel)
	mux.HandleFunc("POST /v1/audio/speech", s.handleProxyByModel)
	mux.HandleFunc("POST /v1/audio/transcriptions", s.handleProxyByModel)
	mux.HandleFunc("GET /v1/models", s.handleListModels)
	mux.HandleFunc("GET /health", s.handleHealth)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(204)
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	// Derive the catalog live from the registry so hot-loaded backends appear
	// in /v1/models without a restart.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(NewCatalog(s.registry.Names()).OpenAIResponse())
}

func (s *Server) handleProxyByModel(w http.ResponseWriter, r *http.Request) {
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

	b, err := s.registry.Resolve(r.Context(), model)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	if err := b.EnsureLoaded(r.Context(), model); err != nil {
		http.Error(w, "ensure: "+err.Error(), 502)
		return
	}

	upstream := b.UpstreamModel()
	if upstream == "" {
		upstream = model // audio: multiplex on the per-request model
	}
	_ = ProxyJSON(w, r, b.BaseURL(), upstream)
}
