package offload

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// fakeStore is an in-memory FileStore. Keys are full dir paths; value is size.
type fakeStore struct {
	dirs    map[string]int64
	mtimes  map[string]time.Time
	mounted map[string]bool
	copies  int
	removes int
}

func newFakeStore() *fakeStore {
	return &fakeStore{dirs: map[string]int64{}, mtimes: map[string]time.Time{}, mounted: map[string]bool{}}
}

func (f *fakeStore) add(dir string, size int64) { f.dirs[dir] = size }

func (f *fakeStore) Mounted(root string) bool {
	if v, ok := f.mounted[root]; ok {
		return v
	}
	return true
}
func (f *fakeStore) Exists(dir string) bool { _, ok := f.dirs[dir]; return ok }
func (f *fakeStore) Size(dir string) (int64, error) {
	if s, ok := f.dirs[dir]; ok {
		return s, nil
	}
	return 0, fmt.Errorf("no such dir %s", dir)
}
func (f *fakeStore) ModTime(dir string) (time.Time, error) {
	if t, ok := f.mtimes[dir]; ok {
		return t, nil
	}
	return time.Unix(0, 0), nil
}
func (f *fakeStore) List(root string) ([]string, error) {
	var names []string
	for d := range f.dirs {
		if filepath.Dir(d) == root {
			names = append(names, filepath.Base(d))
		}
	}
	sort.Strings(names)
	return names, nil
}
func (f *fakeStore) CopyDir(src, dst string) error {
	if _, ok := f.dirs[dst]; ok {
		return fmt.Errorf("dst exists")
	}
	s, ok := f.dirs[src]
	if !ok {
		return fmt.Errorf("src missing")
	}
	f.dirs[dst] = s
	f.copies++
	return nil
}
func (f *fakeStore) RemoveDir(dir string) error {
	if !strings.Contains(dir, "/") {
		return fmt.Errorf("bad dir")
	}
	delete(f.dirs, dir)
	f.removes++
	return nil
}
