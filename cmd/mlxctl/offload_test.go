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
