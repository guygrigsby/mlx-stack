package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

// servingBackend records EnsureLoaded calls and proxies to a captive upstream.
type servingBackend struct {
	*fakeBackend
	ensureCalls []string
	ensureErr   error
}

func (s *servingBackend) EnsureLoaded(_ context.Context, name string) error {
	s.ensureCalls = append(s.ensureCalls, name)
	return s.ensureErr
}

func TestServer_ChatCompletionFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"model":"/abs/valkyrie"`)) {
			t.Errorf("upstream got body: %s", body)
		}
		w.Write([]byte(`{"id":"x"}`))
	}))
	defer upstream.Close()

	bk := &servingBackend{fakeBackend: &fakeBackend{
		name: "chat", mode: "swap", url: upstream.URL, upstream: "/abs/valkyrie",
	}}
	reg := NewRegistry(bk)
	reg.RegisterAlias("valkyrie", bk)

	cfg := &config.Config{}
	srv := NewServer(ServerOpts{Config: cfg, Registry: reg, Names: []string{"chat", "valkyrie"}})

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"valkyrie"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if len(bk.ensureCalls) != 1 || bk.ensureCalls[0] != "valkyrie" {
		t.Errorf("ensure calls: %v", bk.ensureCalls)
	}
}

func TestServer_EmbeddingsRoute(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer upstream.Close()

	bk := &servingBackend{fakeBackend: &fakeBackend{name: "embed", mode: "persistent", url: upstream.URL, upstream: "/abs/embed"}}
	srv := NewServer(ServerOpts{Config: &config.Config{}, Registry: NewRegistry(bk)})

	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{"model":"embed","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
}

func TestServer_AudioSpeechRoute(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// Audio backends pass through the original model field (UpstreamModel="").
		if !bytes.Contains(body, []byte(`"model":"tts"`)) {
			t.Errorf("body: %s", body)
		}
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	bk := &servingBackend{fakeBackend: &fakeBackend{name: "tts", mode: "persistent", url: upstream.URL, upstream: ""}}
	srv := NewServer(ServerOpts{Config: &config.Config{}, Registry: NewRegistry(bk)})

	req := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(`{"model":"tts","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
}

func TestServer_ListModels(t *testing.T) {
	srv := NewServer(ServerOpts{Config: &config.Config{}, Registry: NewRegistry(), Names: []string{"a", "b", "embed"}})
	req := httptest.NewRequest("GET", "/v1/models", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	var resp OpenAIList
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Object != "list" || len(resp.Data) != 3 {
		t.Errorf("resp: %+v", resp)
	}
}

func TestServer_UnknownModelReturns400(t *testing.T) {
	srv := NewServer(ServerOpts{Config: &config.Config{}, Registry: NewRegistry()})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"ghost"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: %d", rr.Code)
	}
}
