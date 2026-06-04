package offload

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// FileStore is the only thing in this package that touches disk. The Manager
// depends on this interface so its logic is unit-tested with an in-memory fake.
type FileStore interface {
	Mounted(root string) bool
	Exists(dir string) bool
	Size(dir string) (int64, error)
	ModTime(dir string) (time.Time, error)
	List(root string) ([]string, error)
	CopyDir(src, dst string) error
	RemoveDir(dir string) error
}

// OSStore is the production FileStore backed by the os package.
type OSStore struct{}

func (OSStore) Mounted(root string) bool {
	fi, err := os.Stat(root)
	return err == nil && fi.IsDir()
}

func (OSStore) Exists(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

func (OSStore) Size(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		total += fi.Size()
		return nil
	})
	return total, err
}

func (OSStore) ModTime(dir string) (time.Time, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

func (OSStore) List(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// CopyDir copies src to a temp sibling of dst, then atomically renames it into
// place. dst must not already exist. A failed copy leaves no partial dst.
func (OSStore) CopyDir(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("copy dst already exists: %s", dst)
	}
	tmp := dst + ".partial"
	_ = os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	return nil
}

func (OSStore) RemoveDir(dir string) error { return os.RemoveAll(dir) }

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
