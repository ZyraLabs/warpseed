//go:build windows

package sftpfast

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// markSparse flags f as a sparse file so that extending it and writing at
// far offsets costs nothing. Without this NTFS zero-fills every byte between
// the valid-data length and the write offset, synchronously, inside the
// WriteFile call — for a 4-lane 50 GB download that is ~37 GB of zeros
// before lane four's first write returns, in a call neither context
// cancellation nor Task Manager can interrupt.
func markSparse(f *os.File) error { return setSparse(f, true) }

// unmarkSparse clears the flag once every byte has been written, so the
// published file carries no sparse attribute. NTFS may refuse while any
// range is still unallocated; callers treat that as cosmetic.
func unmarkSparse(f *os.File) error { return setSparse(f, false) }

func setSparse(f *os.File, on bool) error {
	// FILE_SET_SPARSE_BUFFER is a single BOOLEAN.
	var flag byte
	if on {
		flag = 1
	}
	var returned uint32
	return windows.DeviceIoControl(
		windows.Handle(f.Fd()),
		windows.FSCTL_SET_SPARSE,
		(*byte)(unsafe.Pointer(&flag)), 1,
		nil, 0,
		&returned, nil,
	)
}
