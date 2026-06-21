package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_UnifiedSchema(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	contents := `
log_dir     = "~/logs"
models_root = "~/models"
python_bin  = "/usr/bin/python"

[router]
host        = "127.0.0.1"
port        = 1230
extra_ports = [8080]

[defaults.cache]
limit_bytes = 1000000

[[backend]]
name    = "valkyrie"
engine  = "lm"
mode    = "swap"
group   = "chat"
host    = "127.0.0.1"
port    = 1234
model   = "~/models/valkyrie"
default = true

[[backend]]
name   = "scout"
engine = "vlm"
mode   = "swap"
group  = "chat"
host   = "127.0.0.1"
port   = 1234
model  = "~/models/scout"

[[backend]]
name   = "qwen-tags"
engine = "vlm"
mode   = "persistent"
host   = "127.0.0.1"
port   = 1235
model  = "~/models/qwen-tags"
`
	if err := os.WriteFile(cfgPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(c.LogDir, home) {
		t.Errorf("LogDir not expanded: %q", c.LogDir)
	}
	if len(c.Backends) != 3 {
		t.Fatalf("want 3 backends, got %d", len(c.Backends))
	}
	if !strings.HasPrefix(c.Backends[0].Model, home) {
		t.Errorf("backend model not expanded: %q", c.Backends[0].Model)
	}
	if c.Defaults.Cache.LimitBytes != 1000000 {
		t.Errorf("defaults cache: %d", c.Defaults.Cache.LimitBytes)
	}
	groups := c.BackendsByGroup()
	if len(groups["chat"]) != 2 {
		t.Errorf("chat group: %v", groups["chat"])
	}
}

func TestLoad_RejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.toml")
	contents := `
python_bin = "/usr/bin/python"
nonexistent_top = "x"
[router]
host = "127.0.0.1"
port = 1230
[[backend]]
name   = "x"
engine = "lm"
mode   = "persistent"
host   = "127.0.0.1"
port   = 1
model  = "/m"
`
	os.WriteFile(cfgPath, []byte(contents), 0o644)
	_, err := Load(cfgPath)
	if err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Fatalf("want unknown-keys error: %v", err)
	}
}

func TestLoad_AppliesGroupDefault(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	contents := `
python_bin = "/usr/bin/python"
[router]
host = "x"
port = 1
[[backend]]
name    = "swappy"
engine  = "lm"
mode    = "swap"
host    = "x"
port    = 1234
model   = "/m"
`
	os.WriteFile(cfgPath, []byte(contents), 0o644)
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// Mode=swap with no group should default Group=Name.
	if c.Backends[0].Group != "swappy" {
		t.Errorf("group default: %q", c.Backends[0].Group)
	}
}

func TestLoad_ExternalUpstreamDefault(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	contents := `
python_bin = "/usr/bin/python"
[router]
host = "x"
port = 1
[[backend]]
name = "remote"
mode = "external"
url  = "http://x:1"
`
	os.WriteFile(cfgPath, []byte(contents), 0o644)
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.Backends[0].UpstreamModel != "remote" {
		t.Errorf("upstream default: %q", c.Backends[0].UpstreamModel)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent.toml")
	if err == nil {
		t.Error("expected error")
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct{ in, want string }{
		{"~/foo", filepath.Join(home, "foo")},
		{"~", home},
		{"/abs/path", "/abs/path"},
		{"", ""},
		{"relative", "relative"},
	}
	for _, c := range cases {
		if got := expandHome(c.in); got != c.want {
			t.Errorf("expandHome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoad_SlotVocabulary(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	contents := `
python_bin = "/usr/bin/python"
[router]
host = "127.0.0.1"
port = 1230

[[backend]]
name    = "valkyrie"
engine  = "lm"
slot    = "chat"
host    = "127.0.0.1"
port    = 1234
model   = "/m/valk"
default = true

[[backend]]
name   = "scout"
engine = "vlm"
slot   = "chat"
host   = "127.0.0.1"
port   = 1234
model  = "/m/scout"

[[backend]]
name   = "embed"
engine = "embed"
host   = "127.0.0.1"
port   = 1235
model  = "/m/embed"

[[backend]]
name   = "coder"
engine = "lm"
host   = "127.0.0.1"
port   = 1236
model  = "/m/coder"

[[backend]]
name   = "remote-gpt"
remote = true
url    = "http://other:8080"
`
	if err := os.WriteFile(cfgPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	by := map[string]BackendSpec{}
	for _, b := range c.Backends {
		by[b.Name] = b
	}
	// shared lazy slot -> internal swap
	if v := by["valkyrie"]; v.Mode != "swap" || v.Group != "chat" || v.Slot != "chat" {
		t.Errorf("valkyrie: mode=%q group=%q slot=%q", v.Mode, v.Group, v.Slot)
	}
	// embed engine is inherently a singleton -> warm/persistent
	if e := by["embed"]; !e.Warm || e.Mode != "persistent" {
		t.Errorf("embed: warm=%v mode=%q", e.Warm, e.Mode)
	}
	// bare lm with no slot -> lazy slot of one named after itself
	if co := by["coder"]; co.Mode != "swap" || co.Group != "coder" || co.Slot != "coder" {
		t.Errorf("coder: mode=%q group=%q slot=%q", co.Mode, co.Group, co.Slot)
	}
	// remote -> external, upstream defaults to name
	if r := by["remote-gpt"]; !r.Remote || r.Mode != "external" || r.UpstreamModel != "remote-gpt" {
		t.Errorf("remote-gpt: remote=%v mode=%q upstream=%q", r.Remote, r.Mode, r.UpstreamModel)
	}
	if len(c.BackendsByGroup()["chat"]) != 2 {
		t.Errorf("chat slot members: %v", c.BackendsByGroup()["chat"])
	}
}

func TestLoad_LegacyModeStillNormalizes(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	contents := `
python_bin = "/usr/bin/python"
[router]
host = "127.0.0.1"
port = 1230
[[backend]]
name   = "qwen-tags"
engine = "vlm"
mode   = "persistent"
host   = "127.0.0.1"
port   = 1235
model  = "/m/qwen"
`
	os.WriteFile(cfgPath, []byte(contents), 0o644)
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// legacy persistent should surface as warm in the new vocabulary
	if b := c.Backends[0]; !b.Warm || b.Slot != "qwen-tags" {
		t.Errorf("legacy persistent: warm=%v slot=%q", b.Warm, b.Slot)
	}
}
