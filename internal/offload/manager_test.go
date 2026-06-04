package offload

import (
	"context"
	"path/filepath"
	"testing"
	"time"
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

func TestManager_TierName(t *testing.T) {
	fs := newFakeStore()
	fs.add("/lib/m", 10)
	m := newTestManager(t, fs, 1000)
	if m.TierName("m") != "offloaded" {
		t.Errorf("TierName = %q", m.TierName("m"))
	}
}

func TestEnsurePulled_HotTouchesNoCopy(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/m", 10)
	fs.add("/lib/m", 10)
	m := newTestManager(t, fs, 1000)
	if err := m.EnsurePulled(context.Background(), "m"); err != nil {
		t.Fatal(err)
	}
	if fs.copies != 0 {
		t.Fatalf("hot model should not copy, got %d copies", fs.copies)
	}
}

func TestEnsurePulled_OffloadedPullsWhenRoom(t *testing.T) {
	fs := newFakeStore()
	fs.add("/lib/m", 100)
	m := newTestManager(t, fs, 1000)
	if err := m.EnsurePulled(context.Background(), "m"); err != nil {
		t.Fatal(err)
	}
	if !fs.Exists("/cache/m") || fs.copies != 1 {
		t.Fatalf("expected pull copy; exists=%v copies=%d", fs.Exists("/cache/m"), fs.copies)
	}
}

func TestEnsurePulled_EvictsLRUToFit(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/old", 400)
	fs.add("/lib/old", 400)
	fs.add("/cache/recent", 400)
	fs.add("/lib/recent", 400)
	fs.add("/lib/new", 400)
	m := newTestManager(t, fs, 1000)
	m.lastUsed["old"] = time.Unix(100, 0)
	m.lastUsed["recent"] = time.Unix(200, 0)

	if err := m.EnsurePulled(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	if fs.Exists("/cache/old") {
		t.Error("LRU 'old' should have been evicted")
	}
	if !fs.Exists("/cache/recent") {
		t.Error("'recent' should remain")
	}
	if !fs.Exists("/cache/new") {
		t.Error("'new' should be pulled")
	}
}

func TestEnsurePulled_PinnedNotEvicted(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/pinnedm", 800)
	fs.add("/lib/pinnedm", 800)
	fs.add("/lib/new", 400)
	m := newTestManager(t, fs, 1000)
	m.lastUsed["pinnedm"] = time.Unix(1, 0)
	m.opt.Pinned = func() map[string]bool { return map[string]bool{"pinnedm": true} }

	err := m.EnsurePulled(context.Background(), "new")
	if err == nil {
		t.Fatal("expected 'cannot fit' error when the only victim is pinned")
	}
	if fs.Exists("/cache/new") {
		t.Fatal("new should not have been pulled when it cannot fit")
	}
}

func TestManager_SetPinned(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/p", 800)
	fs.add("/lib/p", 800)
	fs.add("/lib/new", 400)
	m := newTestManager(t, fs, 1000)
	m.lastUsed["p"] = time.Unix(1, 0)
	m.SetPinned(func() map[string]bool { return map[string]bool{"p": true} })
	if err := m.EnsurePulled(context.Background(), "new"); err == nil {
		t.Fatal("pinned model (via SetPinned) must not be evicted")
	}
}

func TestEnsurePulled_UnknownErrors(t *testing.T) {
	m := newTestManager(t, newFakeStore(), 1000)
	if err := m.EnsurePulled(context.Background(), "ghost"); err == nil {
		t.Fatal("unknown model should error")
	}
}

func TestEnsurePulled_DriveAbsentErrors(t *testing.T) {
	fs := newFakeStore()
	fs.add("/lib/m", 10)
	fs.mounted["/lib"] = false
	m := newTestManager(t, fs, 1000)
	if err := m.EnsurePulled(context.Background(), "m"); err == nil {
		t.Fatal("offloaded + unmounted drive should error")
	}
}

func TestOffload_HotDeletesCacheKeepsLibrary(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/m", 10)
	fs.add("/lib/m", 10)
	m := newTestManager(t, fs, 1000)
	if err := m.Offload("m"); err != nil {
		t.Fatal(err)
	}
	if fs.Exists("/cache/m") || !fs.Exists("/lib/m") {
		t.Fatalf("offload should drop cache, keep library")
	}
}

func TestOffload_LocalOnlyBacksUpFirst(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/m", 10)
	m := newTestManager(t, fs, 1000)
	if err := m.Offload("m"); err != nil {
		t.Fatal(err)
	}
	if !fs.Exists("/lib/m") {
		t.Fatal("local-only offload must copy to library before deleting cache")
	}
	if fs.Exists("/cache/m") {
		t.Fatal("cache copy should be removed after backup")
	}
}

func TestOffload_DriveAbsentErrors(t *testing.T) {
	fs := newFakeStore()
	fs.add("/cache/m", 10)
	fs.mounted["/lib"] = false
	m := newTestManager(t, fs, 1000)
	if err := m.Offload("m"); err == nil {
		t.Fatal("offload with unmounted drive should error")
	}
	if !fs.Exists("/cache/m") {
		t.Fatal("cache must be untouched when offload cannot proceed")
	}
}

func TestOffload_UnknownErrors(t *testing.T) {
	m := newTestManager(t, newFakeStore(), 1000)
	if err := m.Offload("ghost"); err == nil {
		t.Fatal("offload of a name in neither cache nor library should error, not silently succeed")
	}
}

func TestOffload_AlreadyOffloadedIsNoop(t *testing.T) {
	fs := newFakeStore()
	fs.add("/lib/m", 10) // library only, already offloaded
	m := newTestManager(t, fs, 1000)
	if err := m.Offload("m"); err != nil {
		t.Fatalf("offload of an already-offloaded model should be a no-op, got %v", err)
	}
}

func TestPull_IsEnsurePulled(t *testing.T) {
	fs := newFakeStore()
	fs.add("/lib/m", 100)
	m := newTestManager(t, fs, 1000)
	if err := m.Pull(context.Background(), "m"); err != nil {
		t.Fatal(err)
	}
	if !fs.Exists("/cache/m") {
		t.Fatal("pull should materialize the cache copy")
	}
}
