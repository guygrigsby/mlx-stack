package offload

import (
	"path/filepath"
	"testing"
)

func newTestManager(t *testing.T, fs *fakeStore, budget int64) *Manager {
	t.Helper()
	m, err := New(Options{
		CacheRoot:   "/cache",
		LibraryRoot: "/lib",
		Budget:      budget,
		StatePath:   filepath.Join(t.TempDir(), "offload.json"),
		FS:          fs,
		Pinned:      func() map[string]bool { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManager_CacheUsed(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/a", 100)
	fs.add("/cache/b", 250)
	fs.add("/lib/a", 100)
	m := newTestManager(t, fs, 1000)
	used, err := m.CacheUsed()
	if err != nil || used != 350 {
		t.Fatalf("CacheUsed = %d, %v; want 350", used, err)
	}
}

func TestManager_ReconcileDropsAndSeeds(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/keep", 10)
	fs.add("/lib/keep", 10)
	m := newTestManager(t, fs, 1000)
	if _, ok := m.lastUsed["ghost"]; ok {
		t.Fatal("ghost should not be tracked")
	}
	if _, ok := m.lastUsed["keep"]; !ok {
		t.Fatal("keep should be seeded from disk")
	}
}

func TestManager_Tier(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/hot", 10)
	fs.add("/lib/hot", 10)
	fs.add("/lib/cold", 10)
	fs.add("/cache/fresh", 10)
	m := newTestManager(t, fs, 1000)

	cases := map[string]Tier{
		"hot":     TierHot,
		"cold":    TierOffloaded,
		"fresh":   TierLocalOnly,
		"missing": TierUnknown,
	}
	for name, want := range cases {
		if got := m.Tier(name); got != want {
			t.Errorf("Tier(%q) = %q, want %q", name, got, want)
		}
	}
}
