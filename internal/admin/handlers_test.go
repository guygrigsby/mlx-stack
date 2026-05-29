package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bk "github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/guygrigsby/mlx-stack/internal/logobs"
	"github.com/guygrigsby/mlx-stack/internal/obsstate"
)

type fakeBackend struct {
	name, group, mode, engine, url, upstream string
	current                                  string
	running                                  bool
	pid                                      int

	ensureCalls []string
	stopCalls   int
	ensureErr   error
	stopErr     error
}

func (f *fakeBackend) Name() string          { return f.name }
func (f *fakeBackend) Group() string         { return f.group }
func (f *fakeBackend) Mode() string          { return f.mode }
func (f *fakeBackend) Engine() string        { return f.engine }
func (f *fakeBackend) BaseURL() string       { return f.url }
func (f *fakeBackend) UpstreamModel() string { return f.upstream }
func (f *fakeBackend) Running() bool         { return f.running }
func (f *fakeBackend) PID() int              { return f.pid }
func (f *fakeBackend) Current() string       { return f.current }
func (f *fakeBackend) EnsureLoaded(_ context.Context, n string) error {
	f.ensureCalls = append(f.ensureCalls, n)
	return f.ensureErr
}
func (f *fakeBackend) Start(_ context.Context) error {
	return f.EnsureLoaded(context.Background(), f.name)
}
func (f *fakeBackend) Stop(_ context.Context) error {
	f.stopCalls++
	return f.stopErr
}

func newTestHandlers() (*Handlers, *fakeBackend, *fakeBackend) {
	chat := &fakeBackend{name: "chat", group: "chat", mode: "swap", engine: "lm", url: "http://x:1234", running: true, pid: 100, current: "valkyrie"}
	embed := &fakeBackend{name: "embed", group: "embed", mode: "persistent", engine: "embed", url: "http://x:1236", running: true, pid: 200}
	h := &Handlers{Config: &config.Config{}}
	h.SetState([]bk.Backend{chat, embed}, map[string]string{"valkyrie": "chat", "scout": "chat"})
	return h, chat, embed
}

func TestHandler_Health(t *testing.T) {
	h, _, _ := newTestHandlers()
	req := httptest.NewRequest("GET", "/v1/health", nil)
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: %d", rr.Code)
	}
}

func TestHandler_Status(t *testing.T) {
	h, _, _ := newTestHandlers()
	req := httptest.NewRequest("GET", "/v1/status", nil)
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	var resp StatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Backends) != 2 {
		t.Fatalf("want 2 backends, got %d", len(resp.Backends))
	}
	chat := resp.Backends[0]
	if chat.Name != "chat" || chat.Mode != "swap" || chat.CurrentName != "valkyrie" {
		t.Errorf("chat: %+v", chat)
	}
}

func TestHandler_StartByPrimaryName(t *testing.T) {
	h, chat, _ := newTestHandlers()
	req := httptest.NewRequest("POST", "/v1/start", strings.NewReader(`{"name":"chat"}`))
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if len(chat.ensureCalls) != 1 || chat.ensureCalls[0] != "chat" {
		t.Errorf("ensureCalls: %v", chat.ensureCalls)
	}
}

func TestHandler_StartByAlias(t *testing.T) {
	h, chat, _ := newTestHandlers()
	req := httptest.NewRequest("POST", "/v1/start", strings.NewReader(`{"name":"valkyrie"}`))
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if len(chat.ensureCalls) != 1 || chat.ensureCalls[0] != "valkyrie" {
		t.Errorf("ensureCalls: %v", chat.ensureCalls)
	}
}

func TestHandler_StartUnknownReturns400(t *testing.T) {
	h, _, _ := newTestHandlers()
	req := httptest.NewRequest("POST", "/v1/start", strings.NewReader(`{"name":"ghost"}`))
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: %d", rr.Code)
	}
}

func TestHandler_Swap(t *testing.T) {
	h, chat, _ := newTestHandlers()
	req := httptest.NewRequest("POST", "/v1/swap", strings.NewReader(`{"name":"scout"}`))
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	if len(chat.ensureCalls) != 1 || chat.ensureCalls[0] != "scout" {
		t.Errorf("ensureCalls: %v", chat.ensureCalls)
	}
}

func TestHandler_Stop(t *testing.T) {
	h, chat, _ := newTestHandlers()
	req := httptest.NewRequest("POST", "/v1/stop", strings.NewReader(`{"name":"chat"}`))
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	if chat.stopCalls != 1 {
		t.Errorf("stopCalls: %d", chat.stopCalls)
	}
}

func TestHandler_Restart(t *testing.T) {
	h, _, embed := newTestHandlers()
	req := httptest.NewRequest("POST", "/v1/restart", strings.NewReader(`{"name":"embed"}`))
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	if embed.stopCalls != 1 {
		t.Errorf("stopCalls: %d", embed.stopCalls)
	}
	if len(embed.ensureCalls) != 1 {
		t.Errorf("ensureCalls: %v", embed.ensureCalls)
	}
}

func TestHandler_LogsTail(t *testing.T) {
	broker := logobs.NewBroker()
	h, _, _ := newTestHandlers()
	h.Broker = broker

	ts := httptest.NewServer(h.Mux())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/logs/tail", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	time.Sleep(50 * time.Millisecond)
	broker.Publish(logobs.Event{Raw: "[mlx-launch] test-line"})

	scanner := bufio.NewScanner(resp.Body)
	gotLine := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "test-line") {
			gotLine = true
			break
		}
	}
	if !gotLine {
		t.Errorf("did not receive published event")
	}
}

func TestHandler_StatusIncludesWorkers(t *testing.T) {
	store := obsstate.New()
	store.Apply(logobs.Event{Worker: "chat", Kind: logobs.KindMem, Mem: logobs.MemSnapshot{Active: 1234}})
	h, _, _ := newTestHandlers()
	h.ObsStore = store

	req := httptest.NewRequest("GET", "/v1/status", nil)
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	var resp StatusResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Workers == nil {
		t.Fatal("workers missing")
	}
	if w, ok := resp.Workers["chat"]; !ok || w.LatestMem == nil || w.LatestMem.Active != 1234 {
		t.Errorf("worker mem: %+v", w)
	}
}

func TestHandler_ReloadReturnsAddedNames(t *testing.T) {
	h, _, _ := newTestHandlers()
	h.Reload = func(_ context.Context) (ReloadResult, error) {
		return ReloadResult{Added: []string{"newmodel"}, Skipped: []string{"chat"}}, nil
	}
	req := httptest.NewRequest("POST", "/v1/reload", nil)
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	var res ReloadResult
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 || res.Added[0] != "newmodel" {
		t.Errorf("added: %v", res.Added)
	}
}

func TestHandler_ReloadErrorIs500(t *testing.T) {
	h, _, _ := newTestHandlers()
	h.Reload = func(_ context.Context) (ReloadResult, error) {
		return ReloadResult{}, errFake
	}
	req := httptest.NewRequest("POST", "/v1/reload", nil)
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: %d", rr.Code)
	}
}

func TestHandler_ReloadUnsupportedIs501(t *testing.T) {
	h, _, _ := newTestHandlers()
	h.Reload = nil
	req := httptest.NewRequest("POST", "/v1/reload", nil)
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	if rr.Code != 501 {
		t.Errorf("status: %d", rr.Code)
	}
}

var errFake = fmt.Errorf("boom")
