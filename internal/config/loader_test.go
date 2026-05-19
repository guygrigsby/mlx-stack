package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	contents := `
log_dir     = "~/logs"
models_root = "~/models"
python_bin  = "/usr/bin/python"

[router]
host        = "127.0.0.1"
port        = 1231
extra_ports = [8080]

[chat]
default_profile  = "p1"
host             = "127.0.0.1"
port             = 1234
swap_timeout_sec = 30

  [chat.profiles.p1]
  model  = "~/models/p1"
  engine = "lm"
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
		t.Errorf("LogDir not ~-expanded: %q", c.LogDir)
	}
	if !strings.HasPrefix(c.ModelsRoot, home) {
		t.Errorf("ModelsRoot not ~-expanded: %q", c.ModelsRoot)
	}
	if !strings.HasPrefix(c.Chat.Profiles["p1"].Model, home) {
		t.Errorf("profile model not ~-expanded: %q", c.Chat.Profiles["p1"].Model)
	}
	if c.Router.Port != 1231 {
		t.Errorf("Router.Port: want 1231, got %d", c.Router.Port)
	}
}

func TestLoad_ValidatesAfterParse(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.toml")
	contents := `
python_bin = "/usr/bin/python"
[router]
host = "127.0.0.1"
port = 1231
[chat]
default_profile = "missing"
host = "127.0.0.1"
port = 1234
  [chat.profiles.p1]
  model = "/tmp"
  engine = "lm"
`
	os.WriteFile(cfgPath, []byte(contents), 0o644)

	_, err := Load(cfgPath)
	if err == nil || !strings.Contains(err.Error(), "default_profile") {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		in, want string
	}{
		{"~/foo", filepath.Join(home, "foo")},
		{"~", home},
		{"/abs/path", "/abs/path"},
		{"", ""},
		{"relative", "relative"},
	}
	for _, c := range cases {
		got := expandHome(c.in)
		if got != c.want {
			t.Errorf("expandHome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
