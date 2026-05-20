package logrot

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Rotator is an io.Writer that writes to a dated log file and rolls over
// when the local date changes.
type Rotator struct {
	dir    string
	prefix string
	now    func() time.Time
	mu     sync.Mutex
	cur    *os.File
	curDay string
}

// New returns a Rotator that writes files named prefix-YYYY-MM-DD.log in dir.
func New(dir, prefix string) *Rotator {
	return &Rotator{dir: dir, prefix: prefix, now: time.Now}
}

// WithClock lets tests inject a deterministic clock.
func (r *Rotator) WithClock(now func() time.Time) *Rotator {
	r.now = now
	return r
}

// Write implements io.Writer. It opens (or rotates) the log file as needed.
func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	day := r.now().Format("2006-01-02")
	if r.cur == nil || day != r.curDay {
		if err := r.openLocked(day); err != nil {
			return 0, err
		}
	}
	return r.cur.Write(p)
}

func (r *Rotator) openLocked(day string) error {
	if r.cur != nil {
		_ = r.cur.Close()
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(r.dir, fmt.Sprintf("%s-%s.log", r.prefix, day))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	r.cur = f
	r.curDay = day
	return nil
}

// Close closes the currently open log file.
func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur != nil {
		err := r.cur.Close()
		r.cur = nil
		return err
	}
	return nil
}
