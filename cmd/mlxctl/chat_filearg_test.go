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
