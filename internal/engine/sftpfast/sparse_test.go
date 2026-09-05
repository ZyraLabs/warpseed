//go:build !windows

package sftpfast

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Pins the contract the engine relies on: flag, then size, and no data
// block is allocated for the hole. The Windows path has its own test in
// sparse_windows_test.go; this one guards the ordering on the CI runner.
func TestMarkSparseThenTruncateAllocatesNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.wschunk")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := markSparse(f); err != nil {
		t.Fatalf("markSparse: %v", err)
	}
	const size = 64 << 20
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != size {
		t.Fatalf("size = %d, want %d", st.Size(), size)
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no Stat_t on this platform")
	}
	if sys.Blocks != 0 {
		t.Fatalf("truncate allocated %d blocks; the part file is not sparse", sys.Blocks)
	}
	if err := unmarkSparse(f); err != nil {
		t.Fatalf("unmarkSparse: %v", err)
	}
}
