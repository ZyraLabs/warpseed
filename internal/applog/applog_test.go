package applog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriteAppendsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, "test.log")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A second run must not truncate the previous run's log — that is the
	// whole point of a file you can send after the fact.
	w2, err := Open(dir, "test.log")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := w2.Write([]byte("second\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	w2.Close()

	b, err := os.ReadFile(filepath.Join(dir, "test.log"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(b); got != "first\nsecond\n" {
		t.Fatalf("log = %q, want %q", got, "first\nsecond\n")
	}
}

func TestRotatesAndKeepsOneGeneration(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, "test.log")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	line := strings.Repeat("x", 64<<10) + "\n"
	// Comfortably past maxBytes so at least one rotation must have happened.
	for i := 0; i < (maxBytes/len(line))+4; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	live, err := os.Stat(filepath.Join(dir, "test.log"))
	if err != nil {
		t.Fatalf("stat live: %v", err)
	}
	if live.Size() > maxBytes {
		t.Errorf("live log %d bytes, want <= %d — rotation did not bound it", live.Size(), maxBytes)
	}
	if _, err := os.Stat(filepath.Join(dir, "test.log.1")); err != nil {
		t.Errorf("previous generation missing: %v", err)
	}
	// Exactly two files: the live log and one generation, never a series.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(ents) != 2 {
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("log dir holds %v, want exactly the live log and one generation", names)
	}
}

func TestConcurrentWritesDoNotRace(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, "test.log")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	// The dispatcher logs from one goroutine per lane, so this is the real
	// access pattern; run with -race to make it meaningful.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if _, err := w.Write([]byte("dispatch: chunk checkpoint\n")); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestWriteAfterCloseReportsClosed(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, "test.log")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.Close()
	if _, err := w.Write([]byte("late\n")); err == nil {
		t.Fatal("Write after Close returned nil error, want os.ErrClosed")
	}
	// Close is idempotent: shutdown may run it more than once.
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
