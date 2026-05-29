package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

func TestSamplerFromFlags(t *testing.T) {
	if s := samplerFromFlags(0, 0, 0, 0, 0, 0); s != nil {
		t.Fatalf("all-zero flags: want nil sampler, got %+v", s)
	}
	s := samplerFromFlags(0.7, 0.8, 20, 0, 1.05, 0)
	if s == nil {
		t.Fatal("want non-nil sampler when a flag is set")
	}
	if s.Temperature != 0.7 || s.TopP != 0.8 || s.TopK != 20 || s.RepetitionPenalty != 1.05 {
		t.Fatalf("unexpected sampler fields: %+v", s)
	}
}

func TestAppendBackendSampler(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("python_bin = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := config.BackendSpec{
		Name: "qwen3-coder", Engine: "lm", Mode: "swap", Group: "chat",
		Host: "127.0.0.1", Port: 1234, Model: "mlx-community/Qwen3-Coder-Next-6bit",
		Sampler: samplerFromFlags(0.7, 0.8, 20, 0, 1.05, 0),
	}
	if err := appendBackend(path, b); err != nil {
		t.Fatal(err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		"[backend.sampler]",
		"temperature        = 0.7",
		"top_p              = 0.8",
		"top_k              = 20",
		"repetition_penalty = 1.05",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}
	// Unset fields must not be written.
	for _, absent := range []string{"min_p", "max_tokens"} {
		if strings.Contains(got, absent) {
			t.Errorf("output should not contain unset field %q\n---\n%s", absent, got)
		}
	}
}

const overwriteFixture = `python_bin = "x"

[router]
port = 8080

# Chat group.
[[backend]]
name   = "valkyrie"
engine = "lm"
mode   = "swap"
group  = "chat"
host   = "127.0.0.1"
port   = 1234
model  = "/m/valkyrie"
  [backend.sampler]
  temperature = 1.15

[[backend]]
name   = "qwen3-coder"
engine = "lm"
mode   = "swap"
group  = "chat"
host   = "127.0.0.1"
port   = 1234
model  = "old-model"

# Audio.
[[backend]]
name   = "kokoro"
engine = "audio"
mode   = "persistent"
host   = "127.0.0.1"
port   = 8880
`

func TestBackendBlockSpan(t *testing.T) {
	lines := strings.Split(strings.TrimSuffix(overwriteFixture, "\n"), "\n")
	start, end, ok := backendBlockSpan(lines, "qwen3-coder")
	if !ok {
		t.Fatal("qwen3-coder block not found")
	}
	if got := lines[start]; strings.TrimSpace(got) != "[[backend]]" {
		t.Errorf("start should be header, got %q", got)
	}
	// end is exclusive and must skip the trailing blank line so the "# Audio."
	// comment + kokoro block survive untouched.
	if got := lines[end]; strings.TrimSpace(got) != "" && !strings.HasPrefix(strings.TrimSpace(got), "#") {
		// end should land on the blank line separating qwen3-coder from "# Audio."
		t.Logf("end line = %q", got)
	}
	for _, l := range lines[start:end] {
		if strings.Contains(l, "kokoro") || strings.Contains(l, "valkyrie") {
			t.Errorf("span leaked into another backend: %q", l)
		}
	}
}

func TestReplaceBackendInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(overwriteFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	b := config.BackendSpec{
		Name: "qwen3-coder", Engine: "lm", Mode: "swap", Group: "chat",
		Host: "127.0.0.1", Port: 1234, Model: "mlx-community/Qwen3-Coder-Next-6bit",
		Sampler: samplerFromFlags(0.7, 0.8, 20, 0, 1.05, 0),
	}
	if err := replaceBackend(path, "qwen3-coder", b); err != nil {
		t.Fatal(err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	if strings.Contains(got, "old-model") {
		t.Error("old model line should be gone")
	}
	if strings.Count(got, `name   = "qwen3-coder"`) != 1 {
		t.Errorf("expected exactly one qwen3-coder block, got:\n%s", got)
	}
	for _, want := range []string{
		"mlx-community/Qwen3-Coder-Next-6bit",
		"top_k              = 20",
		`name   = "valkyrie"`, // other backends untouched
		`name   = "kokoro"`,
		"# Chat group.", // comments preserved
		"# Audio.",
		"temperature = 1.15", // valkyrie's existing sampler intact
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}

	// The rewritten file must still load cleanly.
	if _, err := config.Load(path); err != nil {
		t.Fatalf("rewritten config does not load: %v", err)
	}
}

func TestReplaceBackendNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(overwriteFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	err := replaceBackend(path, "does-not-exist", config.BackendSpec{Name: "does-not-exist"})
	if err == nil {
		t.Fatal("expected error for missing backend")
	}
}

func TestHFCLIPrefersVenvLocal(t *testing.T) {
	dir := t.TempDir()
	pythonBin := filepath.Join(dir, "python")
	hfPath := filepath.Join(dir, "hf")
	if err := os.WriteFile(hfPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := hfCLI(pythonBin)
	if err != nil {
		t.Fatal(err)
	}
	if got != hfPath {
		t.Errorf("want venv-local %q, got %q", hfPath, got)
	}
}

func TestHFCLINotFound(t *testing.T) {
	dir := t.TempDir()
	// Empty PATH so the fallback LookPath cannot find a system hf.
	t.Setenv("PATH", "")
	_, err := hfCLI(filepath.Join(dir, "python"))
	if err == nil {
		t.Fatal("expected error when hf is absent both locally and on PATH")
	}
	if !strings.Contains(err.Error(), "huggingface_hub") {
		t.Errorf("error should suggest the install command, got: %v", err)
	}
}

func TestAppendBackendNoSampler(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("python_bin = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := config.BackendSpec{Name: "n", Engine: "lm", Mode: "swap", Group: "chat", Host: "h", Port: 1, Model: "m"}
	if err := appendBackend(path, b); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	if strings.Contains(string(out), "[backend.sampler]") {
		t.Errorf("nil sampler should not emit a sampler block\n---\n%s", out)
	}
}
