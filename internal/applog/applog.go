// Package applog gives warpseed a log file.
//
// Every diagnostic in this codebase goes through the standard library's log
// package, which writes to stderr — and a Wails GUI build on Windows has no
// console attached, so all of it was being discarded. That made every field
// bug report a guess: a user could say "uploads are slow" and there was
// nothing to look at.
package applog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// maxBytes is when the live file is rotated. One previous generation is kept,
// so the on-disk cost is bounded at twice this. Sized so a long transfer
// session survives in full while staying small enough to attach to an email.
const maxBytes = 2 << 20

// Writer is an io.Writer that rotates at maxBytes, keeping one old file.
// Safe for concurrent use: the dispatcher logs from several goroutines.
type Writer struct {
	mu   sync.Mutex
	dir  string
	name string
	f    *os.File
	n    int64
}

// Open prepares dir/name for appending and returns a rotating writer. The
// caller closes it. A failure here is never fatal — the caller keeps logging
// to stderr instead.
func Open(dir, name string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	w := &Writer{dir: dir, name: name}
	if err := w.reopen(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) reopen() error {
	f, err := os.OpenFile(w.path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	size := int64(0)
	if st, serr := f.Stat(); serr == nil {
		size = st.Size()
	}
	w.f, w.n = f, size
	return nil
}

func (w *Writer) path() string { return filepath.Join(w.dir, w.name) }

// prevPath is the single retained generation. Deliberately one, not a dated
// series: nobody reads warpseed.log.7, and an unbounded series is how a
// logger quietly fills the disk it was meant to help diagnose.
func (w *Writer) prevPath() string { return filepath.Join(w.dir, w.name+".1") }

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	// Rotation failing must not lose the line: on error keep writing to the
	// file we already have, oversized, rather than dropping the message.
	if w.n+int64(len(p)) > maxBytes {
		_ = w.rotate()
	}
	if w.f == nil {
		return 0, os.ErrClosed
	}
	n, err := w.f.Write(p)
	w.n += int64(n)
	return n, err
}

// rotate must be called with the lock held. On any failure it leaves w.f
// usable, because losing the logger is worse than an oversized file.
func (w *Writer) rotate() error {
	if err := w.f.Close(); err != nil {
		w.f = nil
		if rerr := w.reopen(); rerr != nil {
			return rerr
		}
		return err
	}
	// Windows will not rename onto an existing name, so clear the previous
	// generation first.
	_ = os.Remove(w.prevPath())
	if err := os.Rename(w.path(), w.prevPath()); err != nil {
		if rerr := w.reopen(); rerr != nil {
			return rerr
		}
		return err
	}
	return w.reopen()
}

// Close flushes and releases the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Dir reports the directory the log lives in, for a "show me" button.
func (w *Writer) Dir() string { return w.dir }

var _ io.Writer = (*Writer)(nil)
