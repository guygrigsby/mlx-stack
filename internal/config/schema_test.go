package config

import (
	"strings"
	"testing"
)

func minCfg() *Config {
	return &Config{
		PythonBin: "/usr/bin/python",
		Router:    Router{Host: "127.0.0.1", Port: 1230},
		Backends: []BackendSpec{
			{Name: "embed", Engine: "embed", Mode: "persistent", Host: "127.0.0.1", Port: 1236, Model: "/m"},
		},
	}
}

func TestValidate_MinimalOK(t *testing.T) {
	if err := minCfg().Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidate_NoBackends(t *testing.T) {
	c := &Config{PythonBin: "/x", Router: Router{Host: "x", Port: 1}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "[[backend]]") {
		t.Fatalf("want backends-required error: %v", err)
	}
}

func TestValidate_SwapGroupSharedPort(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends,
		BackendSpec{Name: "valkyrie", Engine: "lm", Mode: "swap", Group: "chat", Host: "127.0.0.1", Port: 1234, Model: "/m/v", Default: true},
		BackendSpec{Name: "scout", Engine: "vlm", Mode: "swap", Group: "chat", Host: "127.0.0.1", Port: 1234, Model: "/m/s"},
	)
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidate_SwapGroupMismatchedPort(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends,
		BackendSpec{Name: "a", Engine: "lm", Mode: "swap", Group: "chat", Host: "127.0.0.1", Port: 1234, Model: "/m"},
		BackendSpec{Name: "b", Engine: "lm", Mode: "swap", Group: "chat", Host: "127.0.0.1", Port: 1235, Model: "/m"},
	)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "must share a port") {
		t.Fatalf("want share-a-port error: %v", err)
	}
}

func TestValidate_SwapMissingGroup(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends, BackendSpec{Name: "v", Engine: "lm", Mode: "swap", Host: "x", Port: 1, Model: "/m"})
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "group") {
		t.Fatalf("want group error: %v", err)
	}
}

func TestValidate_DuplicateName(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends, BackendSpec{Name: "embed", Engine: "lm", Mode: "swap", Group: "g", Host: "x", Port: 1, Model: "/m"})
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error: %v", err)
	}
}

func TestValidate_InvalidMode(t *testing.T) {
	c := minCfg()
	c.Backends[0].Mode = "bogus"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("want mode error: %v", err)
	}
}

func TestValidate_InvalidEngine(t *testing.T) {
	c := minCfg()
	c.Backends[0].Engine = "foobar"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "engine") {
		t.Fatalf("want engine error: %v", err)
	}
}

func TestValidate_ExternalRequiresURL(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends, BackendSpec{Name: "remote", Mode: "external"})
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("want url error: %v", err)
	}
}

func TestValidate_ExternalOK(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends, BackendSpec{Name: "remote", Mode: "external", URL: "http://x:1"})
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidate_AudioPersistentNoModelOK(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends, BackendSpec{Name: "tts", Engine: "audio", Mode: "persistent", Host: "127.0.0.1", Port: 1237})
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidate_PersistentLmRequiresModel(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends, BackendSpec{Name: "x", Engine: "lm", Mode: "persistent", Host: "127.0.0.1", Port: 1238})
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("want model error: %v", err)
	}
}

func TestValidate_TwoSwapDefaultsInGroup(t *testing.T) {
	c := minCfg()
	c.Backends = append(c.Backends,
		BackendSpec{Name: "a", Engine: "lm", Mode: "swap", Group: "g", Host: "127.0.0.1", Port: 1, Model: "/m", Default: true},
		BackendSpec{Name: "b", Engine: "lm", Mode: "swap", Group: "g", Host: "127.0.0.1", Port: 1, Model: "/m", Default: true},
	)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "default=true") {
		t.Fatalf("want default error: %v", err)
	}
}

func TestEffectiveOverrides(t *testing.T) {
	defaults := Defaults{
		Cache:    Cache{LimitBytes: 1000},
		Watchdog: Watchdog{KVHeadroomBytes: 2000},
	}
	b := BackendSpec{Watchdog: &Watchdog{KVHeadroomBytes: 99}}
	if b.EffectiveCache(defaults).LimitBytes != 1000 {
		t.Errorf("default cache lost")
	}
	if b.EffectiveWatchdog(defaults).KVHeadroomBytes != 99 {
		t.Errorf("override lost")
	}
}

func TestBackendsByGroup(t *testing.T) {
	c := &Config{
		Backends: []BackendSpec{
			{Name: "a", Mode: "swap", Group: "chat"},
			{Name: "b", Mode: "swap", Group: "chat"},
			{Name: "c", Mode: "persistent"},
		},
	}
	groups := c.BackendsByGroup()
	if len(groups["chat"]) != 2 {
		t.Errorf("chat group: %d", len(groups["chat"]))
	}
	if _, ok := groups["c"]; ok {
		t.Errorf("persistent backend should not appear in groups")
	}
}
