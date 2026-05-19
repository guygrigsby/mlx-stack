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
