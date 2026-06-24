package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/guygrigsby/mlx-stack/internal/config"
)

func TestRenderList(t *testing.T) {
	specs := []config.BackendSpec{
		{Name: "valkyrie", Engine: "lm", Slot: "chat"},
		{Name: "scout", Engine: "vlm", Slot: "chat"},
		{Name: "embed", Engine: "embed", Slot: "embed"},
		{Name: "remote-gpt", Slot: "remote-gpt", Remote: true},
	}
	s := statusJSON{Backends: []backendStatusJSON{
		{Name: "chat", Mode: "swap", CurrentName: "valkyrie", State: "ready"},
		{Name: "embed", Mode: "swap", State: "ready"},
		{Name: "remote-gpt", Mode: "external", Running: true},
	}}
	var buf bytes.Buffer
	renderList(&buf, specs, s)
	out := buf.String()

	// multi-model slot shows header + indented members with the hot one loaded
	if !strings.Contains(out, "chat") || !strings.Contains(out, "1 of 2 loaded") {
		t.Errorf("missing chat slot header:\n%s", out)
	}
	if !strings.Contains(out, "valkyrie") || !strings.Contains(out, "● loaded") {
		t.Errorf("valkyrie should be loaded:\n%s", out)
	}
	if !strings.Contains(out, "scout") || !strings.Contains(out, "○ idle") {
		t.Errorf("scout should be idle:\n%s", out)
	}
	// engine words are humanized
	if !strings.Contains(out, "vision") || !strings.Contains(out, "embeddings") {
		t.Errorf("engine words not humanized:\n%s", out)
	}
	// singleton + remote single-model slots
	if !strings.Contains(out, "embed") || !strings.Contains(out, "remote") {
		t.Errorf("singleton/remote states missing:\n%s", out)
	}
}
