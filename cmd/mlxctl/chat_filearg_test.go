package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandFileArgs(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.md")
	if err := os.WriteFile(f, []byte("This is a test."), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := expandFileArgs([]string{"summarize", "@" + f})
	if err != nil {
		t.Fatal(err)
	}
	if got != "summarize This is a test." {
		t.Fatalf("got %q", got)
	}
	// non-@ args pass through; missing file errors
	if _, err := expandFileArgs([]string{"@/no/such/file"}); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestPickSlot(t *testing.T) {
	known := func(s string) bool { return s == "scout" || s == "chat" }
	auto := func() string { return "loaded" }

	// known slot + prompt -> route to slot
	if m, p := pickSlot([]string{"scout", "what is this"}, known, auto); m != "scout" || len(p) != 1 || p[0] != "what is this" {
		t.Errorf("slot route: m=%q p=%v", m, p)
	}
	// single arg -> auto-pick, whole thing is prompt
	if m, p := pickSlot([]string{"hello there"}, known, auto); m != "loaded" || len(p) != 1 {
		t.Errorf("single arg: m=%q p=%v", m, p)
	}
	// unknown first word with more args -> prompt, not a slot
	if m, p := pickSlot([]string{"hello", "world"}, known, auto); m != "loaded" || len(p) != 2 {
		t.Errorf("unknown words: m=%q p=%v", m, p)
	}
}
