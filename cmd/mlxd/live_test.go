package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/admin"
	bk "github.com/guygrigsby/mlx-stack/internal/backend"
	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/guygrigsby/mlx-stack/internal/router"
	"github.com/guygrigsby/mlx-stack/internal/supervisor"
)

func TestDiffNewBackends(t *testing.T) {
	known := map[string]bool{"chat": true, "valkyrie": true, "embed": true}
	cfg := &config.Config{Backends: []config.BackendSpec{
		{Name: "valkyrie", Mode: "swap", Group: "chat"}, // known -> skip
		{Name: "scout", Mode: "swap", Group: "chat"},    // new member of existing group
		{Name: "newp", Mode: "persistent"},              // new persistent
		{Name: "embed", Mode: "persistent"},             // known -> skip
	}}
	add, skip := diffNewBackends(known, cfg)
	if len(add) != 2 || add[0].Name != "scout" || add[1].Name != "newp" {
		t.Errorf("add: %+v", add)
	}
	if len(skip) != 2 {
		t.Errorf("skip: %+v", skip)
	}
}

// buildLiveForReloadTest seeds a liveState mirroring boot: an existing "chat"
// swap group (default member "valkyrie"), registered in the registry and admin.
func buildLiveForReloadTest(t *testing.T, cfgPath string) *liveState {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	builder := &backendBuilder{pythonBin: "/bin/true", logger: logger}

	chat := builder.newGroup("chat", []config.BackendSpec{
		{Name: "valkyrie", Mode: "swap", Group: "chat", Host: "127.0.0.1", Port: 1234, Default: true},
	})
	registry := router.NewRegistry(chat)
	registry.RegisterAlias("valkyrie", chat)

	backends := []bk.Backend{chat}
	aliases := map[string]string{"valkyrie": "chat"}

	h := &admin.Handlers{}
	h.SetState(backends, aliases)

	return &liveState{
		builder:     builder,
		registry:    registry,
		admin:       h,
		cfgPath:     cfgPath,
		logger:      logger,
		groups:      map[string]*supervisor.Group{"chat": chat},
		persistents: nil,
		backends:    backends,
		aliases:     aliases,
	}
}

func TestLiveReloadAdditive(t *testing.T) {
	cfg := `python_bin = "/bin/true"

[router]
port = 8080

[[backend]]
name   = "valkyrie"
engine = "lm"
mode   = "swap"
group  = "chat"
host   = "127.0.0.1"
port   = 1234
model  = "/m/valkyrie"
default = true

[[backend]]
name   = "scout"
engine = "lm"
mode   = "swap"
group  = "chat"
host   = "127.0.0.1"
port   = 1234
model  = "/m/scout"

[[backend]]
name   = "newp"
engine = "embed"
mode   = "persistent"
host   = "127.0.0.1"
port   = 9001
model  = "/m/newp"
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	live := buildLiveForReloadTest(t, path)
	res, err := live.reload(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(res.Added, "scout") || !slices.Contains(res.Added, "newp") {
		t.Errorf("added missing entries: %+v", res.Added)
	}
	if !slices.Contains(res.Skipped, "valkyrie") {
		t.Errorf("valkyrie should be skipped (already known): %+v", res.Skipped)
	}
	for _, n := range []string{"scout", "newp"} {
		if !live.registry.Has(n) {
			t.Errorf("%q not registered after reload", n)
		}
	}
	// scout joined the existing chat group rather than creating a new one.
	if len(live.groups) != 1 {
		t.Errorf("want 1 group (scout joins chat), got %d", len(live.groups))
	}
	// The new persistent shows in the admin status view.
	if !slices.Contains(adminNames(live.admin), "newp") {
		t.Error("newp missing from admin status after reload")
	}

	// A second reload with no changes adds nothing.
	res2, err := live.reload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Added) != 0 {
		t.Errorf("second reload added: %+v", res2.Added)
	}
}

func TestLiveReloadBadConfigNoMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("garbage = = ["), 0o644); err != nil {
		t.Fatal(err)
	}
	live := buildLiveForReloadTest(t, path)
	before := len(live.registry.Names())
	if _, err := live.reload(context.Background()); err == nil {
		t.Fatal("expected error on bad config")
	}
	if len(live.registry.Names()) != before {
		t.Error("registry mutated on bad config")
	}
}

// adminNames pulls backend names out of the admin /v1/status view.
func adminNames(h *admin.Handlers) []string {
	req := httptest.NewRequest("GET", "/v1/status", nil)
	rr := httptest.NewRecorder()
	h.Mux().ServeHTTP(rr, req)
	var resp struct {
		Backends []struct {
			Name string `json:"name"`
		} `json:"backends"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	out := make([]string, 0, len(resp.Backends))
	for _, b := range resp.Backends {
		out = append(out, b.Name)
	}
	return out
}
