//go:build !windows

package sftpfast

import "os"

// markSparse is a no-op off Windows: ext4, XFS, btrfs and APFS all make a
// truncated-out file sparse by default, so writes at any offset are cheap.
func markSparse(*os.File) error { return nil }

// unmarkSparse is likewise a no-op: a fully written file has no holes and
// Unix filesystems carry no separate sparse attribute to clear.
func unmarkSparse(*os.File) error { return nil }
