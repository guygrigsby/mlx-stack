package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

type fakeSwap struct {
	ensureErr   error
	ensureCalls []string
	upstream    string
}

func (f *fakeSwap) EnsureProfile(ctx context.Context, name string) error {
	f.ensureCalls = append(f.ensureCalls, name)
	return f.ensureErr
}
func (f *fakeSwap) UpstreamModel(name string) string { return "/abs/" + name }
func (f *fakeSwap) BaseURL() string                  { return f.upstream }

func TestServer_ChatCompletionsFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"/abs/valkyrie"`) {
			t.Errorf("upstream got body: %s", body)
		}
		w.Write([]byte(`{"id":"x"}`))
	}))
	defer upstream.Close()

	swap := &fakeSwap{upstream: upstream.URL}
	cfg := &config.Config{Chat: config.Chat{Profiles: map[string]config.Profile{"valkyrie": {Model: "/m", Engine: "lm"}}}}
	srv := NewServer(ServerOpts{Config: cfg, Chat: swap})

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"valkyrie"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if len(swap.ensureCalls) != 1 || swap.ensureCalls[0] != "valkyrie" {
		t.Errorf("ensure calls: %v", swap.ensureCalls)
	}
}

func TestServer_ListModels(t *testing.T) {
	cfg := &config.Config{Chat: config.Chat{Profiles: map[string]config.Profile{"a": {Model: "/m", Engine: "lm"}, "b": {Model: "/m", Engine: "lm"}}}}
	srv := NewServer(ServerOpts{Config: cfg, Chat: &fakeSwap{}})

	req := httptest.NewRequest("GET", "/v1/models", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	var resp OpenAIList
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Object != "list" || len(resp.Data) != 2 {
		t.Errorf("resp: %+v", resp)
	}
}

func TestServer_UnknownModelReturns400(t *testing.T) {
	cfg := &config.Config{Chat: config.Chat{Profiles: map[string]config.Profile{"valkyrie": {Model: "/m", Engine: "lm"}}}}
	swap := &fakeSwap{}
	srv := NewServer(ServerOpts{Config: cfg, Chat: swap})

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"ghost"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: %d body: %s", rr.Code, rr.Body.String())
	}
}

func TestServer_EmbeddingsRoute(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"/abs/embed"`) {
			t.Errorf("body: %s", body)
		}
		w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer upstream.Close()

	cfg := &config.Config{
		Chat:  config.Chat{Profiles: map[string]config.Profile{"v": {Model: "/m", Engine: "lm"}}},
		Embed: config.Embed{Alias: "embed", URL: upstream.URL},
	}
	embed := &fakeManaged{alias: "embed", url: upstream.URL, upstream: "/abs/embed", running: true}
	srv := NewServer(ServerOpts{
		Config:   cfg,
		Chat:     &fakeSwap{},
		Registry: NewRegistry(cfg, &fakeSwap{}, embed),
	})

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
		if r.URL.Path != "/v1/audio/speech" {
			t.Errorf("path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"omnivoice"`) {
			t.Errorf("body: %s", body)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("fake-audio"))
	}))
	defer upstream.Close()

	cfg := &config.Config{
		Chat: config.Chat{Profiles: map[string]config.Profile{"v": {Model: "/m", Engine: "lm"}}},
		TTS:  config.AudioInstance{Engine: "audio", Alias: "tts", Port: 1237},
	}
	tts := &fakeManaged{alias: "tts", url: upstream.URL, upstream: "omnivoice", running: true}
	srv := NewServer(ServerOpts{
		Config:   cfg,
		Chat:     &fakeSwap{},
		Registry: NewRegistry(cfg, &fakeSwap{}, tts),
	})

	req := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(`{"model":"tts","input":"hi","voice":"af_sky"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "fake-audio") {
		t.Errorf("missing audio body: %s", rr.Body.String())
	}
}

func TestServer_ManagedAliasRoutes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"/abs/qwen-tags"`) {
			t.Errorf("body: %s", body)
		}
		w.Write([]byte(`{"id":"x"}`))
	}))
	defer upstream.Close()

	cfg := &config.Config{
		Chat: config.Chat{Profiles: map[string]config.Profile{"v": {Model: "/m", Engine: "lm"}}},
		Tags: config.Tags{Alias: "qwen-tags", Model: "/abs/qwen-tags"},
	}
	tags := &fakeManaged{alias: "qwen-tags", url: upstream.URL, upstream: "/abs/qwen-tags", running: true}
	srv := NewServer(ServerOpts{
		Config:   cfg,
		Chat:     &fakeSwap{},
		Registry: NewRegistry(cfg, &fakeSwap{}, tags),
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"qwen-tags"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
}
