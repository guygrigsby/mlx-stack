package offload

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOSStore_CopySizeRemoveList(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "lib", "m")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "config.json"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := OSStore{}

	dst := filepath.Join(root, "cache", "m")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fs.CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}
	if !fs.Exists(dst) {
		t.Fatal("dst should exist after copy")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "config.json")); string(b) != "12345" {
		t.Fatalf("copied content wrong: %q", b)
	}
	if n, err := fs.Size(dst); err != nil || n != 5 {
		t.Fatalf("Size = %d, %v; want 5", n, err)
	}
	names, err := fs.List(filepath.Join(root, "cache"))
	if err != nil || len(names) != 1 || names[0] != "m" {
		t.Fatalf("List = %v, %v", names, err)
	}
	if !fs.Mounted(filepath.Join(root, "cache")) {
		t.Fatal("Mounted should be true for an existing dir")
	}
	if fs.Mounted(filepath.Join(root, "nope")) {
		t.Fatal("Mounted should be false for a missing dir")
	}
	if err := fs.RemoveDir(dst); err != nil || fs.Exists(dst) {
		t.Fatalf("RemoveDir failed: %v", err)
	}
}

func TestOSStore_CopyDirRefusesExistingDst(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "s")
	dst := filepath.Join(root, "d")
	os.MkdirAll(src, 0o755)
	os.MkdirAll(dst, 0o755)
	if err := (OSStore{}).CopyDir(src, dst); err == nil {
		t.Fatal("CopyDir into existing dst should error")
	}
}
