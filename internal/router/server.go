package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

type ServerOpts struct {
	Config   *config.Config
	Registry *Registry
}

type Server struct {
	cfg      *config.Config
	registry *Registry
	mu       sync.Mutex
	active   map[string]*atomic.Int32 // backend name → in-flight count
}

func NewServer(opts ServerOpts) *Server {
	return &Server{
		cfg:      opts.Config,
		registry: opts.Registry,
		active:   map[string]*atomic.Int32{},
	}
}

func (s *Server) counter(name string) *atomic.Int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.active[name]
	if !ok {
		c = new(atomic.Int32)
		s.active[name] = c
	}
	return c
}

// Active returns a snapshot of backends with at least one in-flight request.
func (s *Server) Active() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for name, c := range s.active {
		if n := int(c.Load()); n > 0 {
			out[name] = n
		}
	}
	return out
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

	c := s.counter(b.Name())
	c.Add(1)
	defer c.Add(-1)
	_ = ProxyJSON(w, r, b.BaseURL(), upstream)
}
