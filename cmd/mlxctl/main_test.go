package main

import (
	"bytes"
	"strings"
	"testing"
)

// Errors must reach the user exactly once: main() prints the error returned by
// Execute(), so cobra itself must stay silent (SilenceErrors). Before the fix
// every error printed twice.
func TestRootCmd_ErrorsPrintOnce(t *testing.T) {
	root := newRootCmd()
	var errBuf bytes.Buffer
	root.SetErr(&errBuf)
	root.SetOut(&errBuf)
	root.SetArgs([]string{"start"}) // missing required arg -> error

	err := root.Execute()
	if err == nil {
		t.Fatal("start with no args should error")
	}
	// cobra must not have printed the error itself; main() is the one printer.
	if s := errBuf.String(); strings.Contains(s, err.Error()) {
		t.Errorf("cobra printed the error (would appear twice with main's print):\n%s", s)
	}
}

// One config-resolution path for every command:
// --config flag > MLXD_CONFIG > MLX_CONFIG (legacy) > default.
func TestConfigPathPrecedence(t *testing.T) {
	t.Setenv("MLXD_CONFIG", "")
	t.Setenv("MLX_CONFIG", "")

	orig := configFlag
	defer func() { configFlag = orig }()

	configFlag = ""
	if got := configPath(); !strings.HasSuffix(got, ".config/mlx/config.toml") {
		t.Errorf("default: got %q", got)
	}

	t.Setenv("MLX_CONFIG", "/legacy.toml")
	if got := configPath(); got != "/legacy.toml" {
		t.Errorf("MLX_CONFIG fallback: got %q", got)
	}

	t.Setenv("MLXD_CONFIG", "/primary.toml")
	if got := configPath(); got != "/primary.toml" {
		t.Errorf("MLXD_CONFIG beats MLX_CONFIG: got %q", got)
	}

	configFlag = "/flag.toml"
	if got := configPath(); got != "/flag.toml" {
		t.Errorf("--config beats env: got %q", got)
	}
}

// --config must be a root persistent flag so every subcommand accepts it.
func TestConfigFlagIsPersistent(t *testing.T) {
	root := newRootCmd()
	if root.PersistentFlags().Lookup("config") == nil {
		t.Fatal("--config should be a root persistent flag")
	}
}

// Old names keep working as cobra aliases of the canonical commands; the
// duplicate command implementations are gone.
func TestCommandAliases(t *testing.T) {
	root := newRootCmd()
	for alias, want := range map[string]string{
		"tags":   "models",
		"swap":   "start",
		"list":   "list",
		"models": "models",
		"start":  "start",
		"status": "status",
	} {
		cmd, _, err := root.Find([]string{alias})
		if err != nil {
			t.Errorf("%s: %v", alias, err)
			continue
		}
		if cmd.Name() != want {
			t.Errorf("%s resolved to %q, want %q", alias, cmd.Name(), want)
		}
	}
}

// The global --output validator must still run and reject bad values.
func TestRootCmd_RejectsBadOutputFormat(t *testing.T) {
	root := newRootCmd()
	root.SetErr(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"health", "-o", "yaml"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "invalid --output") {
		t.Fatalf("want invalid --output error, got %v", err)
	}
}
