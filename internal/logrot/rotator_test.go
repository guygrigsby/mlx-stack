package logrot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotator_WritesToTodayFile(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, "mlxd")
	defer r.Close()
	if _, err := r.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	day := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, "mlxd-"+day+".log")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "hello") {
		t.Errorf("content: %q", b)
	}
}

func TestRotator_RotatesAtDateBoundary(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, "mlxd")
	defer r.Close()

	clock := time.Date(2026, 1, 15, 23, 59, 59, 0, time.UTC)
	r.WithClock(func() time.Time { return clock })

	r.Write([]byte("late-jan-15\n"))

	clock = time.Date(2026, 1, 16, 0, 0, 1, 0, time.UTC)
	r.Write([]byte("early-jan-16\n"))

	b1, _ := os.ReadFile(filepath.Join(dir, "mlxd-2026-01-15.log"))
	b2, _ := os.ReadFile(filepath.Join(dir, "mlxd-2026-01-16.log"))
	if !strings.Contains(string(b1), "late-jan-15") {
		t.Errorf("jan-15 file: %q", b1)
	}
	if !strings.Contains(string(b2), "early-jan-16") {
		t.Errorf("jan-16 file: %q", b2)
	}
	if strings.Contains(string(b1), "jan-16") {
		t.Errorf("jan-15 file leaked jan-16 content")
	}
}

func TestRotator_AppendsToExistingFile(t *testing.T) {
	dir := t.TempDir()
	day := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, "mlxd-"+day+".log")
	os.WriteFile(path, []byte("prefix\n"), 0o644)

	r := New(dir, "mlxd")
	defer r.Close()
	r.Write([]byte("appended\n"))

	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "prefix") || !strings.Contains(string(b), "appended") {
		t.Errorf("content: %q", b)
	}
}
