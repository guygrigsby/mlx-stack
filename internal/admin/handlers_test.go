package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
)

type fakeChat struct {
	state     *backend.ChatState
	ensureErr error
}

func (f *fakeChat) State() *backend.ChatState                         { return f.state }
func (f *fakeChat) EnsureProfile(ctx context.Context, n string) error { return f.ensureErr }
func (f *fakeChat) Stop(ctx context.Context) error                    { return nil }

type fakeTags struct {
	alias, url string
	pid        int
	running    bool
}

func (f *fakeTags) Alias() string    { return f.alias }
func (f *fakeTags) PID() int         { return f.pid }
func (f *fakeTags) BaseURL() string  { return f.url }
func (f *fakeTags) Running() bool    { return f.running }

func newTestHandlers() *Handlers {
	return &Handlers{
		Config: &config.Config{
			Chat: config.Chat{
				DefaultProfile: "p1",
				Profiles:       map[string]config.Profile{"p1": {Model: "/m", Engine: "lm"}, "p2": {Model: "/m", Engine: "lm"}},
			},
		},
		Chat: &fakeChat{state: &backend.ChatState{CurrentProfile: "p1", WorkerPID: 12345}},
	}
}

func TestHandler_Health(t *testing.T) {
	h := newTestHandlers().Mux()
	req := httptest.NewRequest("GET", "/v1/health", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: %d", rr.Code)
	}
}

func TestHandler_Status(t *testing.T) {
	h := newTestHandlers().Mux()
	req := httptest.NewRequest("GET", "/v1/status", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	var resp StatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Chat.CurrentProfile != "p1" || resp.Chat.PID != 12345 {
		t.Errorf("chat status: %+v", resp.Chat)
	}
	if len(resp.Chat.Profiles) != 2 {
		t.Errorf("profile count: %d", len(resp.Chat.Profiles))
	}
}

func TestHandler_Swap(t *testing.T) {
	h := newTestHandlers().Mux()
	req := httptest.NewRequest("POST", "/v1/swap", strings.NewReader(`{"profile":"p2"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: %d body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandler_SwapUnknownProfile(t *testing.T) {
	h := newTestHandlers().Mux()
	req := httptest.NewRequest("POST", "/v1/swap", strings.NewReader(`{"profile":"ghost"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: %d", rr.Code)
	}
}

func TestHandler_Stop(t *testing.T) {
	h := newTestHandlers().Mux()
	req := httptest.NewRequest("POST", "/v1/stop", strings.NewReader(`{"backend":"chat"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: %d", rr.Code)
	}
}

func TestHandler_StatusWithTags(t *testing.T) {
	h := newTestHandlers()
	h.Tags = &fakeTags{alias: "qwen-tags", url: "http://127.0.0.1:1235", pid: 12999, running: true}
	mux := h.Mux()
	req := httptest.NewRequest("GET", "/v1/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	var resp StatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Tags == nil {
		t.Fatal("Tags missing")
	}
	if resp.Tags.Alias != "qwen-tags" || resp.Tags.PID != 12999 || resp.Tags.Running != true {
		t.Errorf("tags status: %+v", *resp.Tags)
	}
}
