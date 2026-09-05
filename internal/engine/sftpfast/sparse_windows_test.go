//go:build windows

package sftpfast

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// Runs only in the user's native `go test` on Windows: proves the FSCTL
// actually takes on the volume, and that a fully written file can drop the
// attribute again.
func TestSparseFlagRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.wschunk")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := markSparse(f); err != nil {
		t.Fatalf("markSparse: %v", err)
	}
	const size = 4 << 20
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if !hasSparseAttr(t, p) {
		t.Fatal("FILE_ATTRIBUTE_SPARSE_FILE not set after markSparse")
	}
	// Fill every byte, as a completed download would have.
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(i)
	}
	if _, err := f.WriteAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if err := unmarkSparse(f); err != nil {
		t.Fatalf("unmarkSparse: %v", err)
	}
	if hasSparseAttr(t, p) {
		t.Fatal("FILE_ATTRIBUTE_SPARSE_FILE still set after unmarkSparse")
	}
}

func hasSparseAttr(t *testing.T, p string) bool {
	t.Helper()
	w, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := windows.GetFileAttributes(w)
	if err != nil {
		t.Fatal(err)
	}
	return attrs&windows.FILE_ATTRIBUTE_SPARSE_FILE != 0
}
