package main

import (
	"sort"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

func TestInactiveModels_ExcludesConfigReferenced(t *testing.T) {
	cfg := &config.Config{Backends: []config.BackendSpec{
		{Name: "chat", Model: "/Users/g/mlx-models/valkyrie"},
		{Name: "anubis", Model: "/Users/g/mlx-models/anubis", DraftModel: "/Users/g/mlx-models/anubis-draft"},
	}}
	cacheDirs := []string{"valkyrie", "anubis", "anubis-draft", "Austral-Qwen3-235B", "old-merge"}
	got := inactiveModels(cfg, cacheDirs)
	sort.Strings(got)
	want := []string{"Austral-Qwen3-235B", "old-merge"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("inactiveModels = %v, want %v", got, want)
	}
}

func TestResolveModelName(t *testing.T) {
	cfg := &config.Config{Backends: []config.BackendSpec{
		{Name: "zerofata_glm-4-5-iceblink-106b-a12b-mlx-mxfp4", Model: "/Users/g/mlx-models/zerofata_GLM-4.5-Iceblink-106B-A12B-MLX-MXFP4"},
	}}
	// backend name (as shown in status) resolves to the model dir basename
	if got := resolveModelName(cfg, "zerofata_glm-4-5-iceblink-106b-a12b-mlx-mxfp4"); got != "zerofata_GLM-4.5-Iceblink-106B-A12B-MLX-MXFP4" {
		t.Errorf("backend name: got %q", got)
	}
	// an exact dir name passes through unchanged
	if got := resolveModelName(cfg, "zerofata_GLM-4.5-Iceblink-106B-A12B-MLX-MXFP4"); got != "zerofata_GLM-4.5-Iceblink-106B-A12B-MLX-MXFP4" {
		t.Errorf("passthrough: got %q", got)
	}
	// an unknown name passes through so the server can error on it
	if got := resolveModelName(cfg, "ghost"); got != "ghost" {
		t.Errorf("unknown: got %q", got)
	}
}
