package sftpfast

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// PartSuffix marks in-flight downloads. Renamed away only after a clean
// finish — the destination name never holds a torn file (unlike rclone's
// --inplace hazard).
const PartSuffix = ".wspart"

// Download fetches remote → local with byte-level resume. progress receives
// byte deltas as they land (caller coalesces for the UI). Cancelling ctx
// aborts promptly; the .wspart file stays for the next resume.
func (c *Client) Download(ctx context.Context, remotePath, localPath string, progress func(delta int64)) error {
	if progress == nil {
		progress = func(int64) {}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("download cancelled: %w", err)
	}

	rf, err := c.sftp.Open(remotePath)
	if err != nil {
		return fmt.Errorf("open remote %q: %w", remotePath, err)
	}
	defer rf.Close()

	rstat, err := rf.Stat()
	if err != nil {
		return fmt.Errorf("stat remote %q: %w", remotePath, err)
	}
	remoteSize := rstat.Size()

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("create local dir: %w", err)
	}
	part := localPath + PartSuffix
	lf, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open part file: %w", err)
	}
	defer lf.Close()

	// Resume validation: a part file larger than the remote means the remote
	// changed — restart from zero rather than produce a corrupt file.
	offset := int64(0)
	if st, err := lf.Stat(); err == nil {
		offset = st.Size()
	}
	if offset > remoteSize {
		offset = 0
		if err := lf.Truncate(0); err != nil {
			return fmt.Errorf("truncate stale part: %w", err)
		}
	}

	if offset < remoteSize {
		if _, err := rf.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("seek remote to %d: %w", offset, err)
		}
		if _, err := lf.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("seek part to %d: %w", offset, err)
		}

		// ctx watchdog: closing the remote handle aborts WriteTo promptly.
		watchdogDone := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				rf.Close()
			case <-watchdogDone:
			}
		}()

		// File.WriteTo drives pkg/sftp's concurrent read pipeline (the
		// 64-256 outstanding requests that make this engine fast). The
		// writer is ctx-aware: closing the remote handle alone is not a
		// reliable abort in pkg/sftp, but a writer error always stops WriteTo.
		_, copyErr := rf.WriteTo(&progressWriter{ctx: ctx, w: lf, onWrite: progress})
		close(watchdogDone)
		if copyErr != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("download cancelled: %w", ctx.Err())
			}
			return fmt.Errorf("transfer %q: %w", remotePath, copyErr)
		}
	}

	if err := lf.Sync(); err != nil {
		return fmt.Errorf("sync part: %w", err)
	}
	if err := lf.Close(); err != nil {
		return fmt.Errorf("close part: %w", err)
	}
	if err := os.Rename(part, localPath); err != nil {
		return fmt.Errorf("finalize %q: %w", localPath, err)
	}
	// Preserve remote mtime so size+mtime sync comparisons work later.
	if err := os.Chtimes(localPath, rstat.ModTime(), rstat.ModTime()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("set mtime: %w", err)
	}
	return nil
}

type progressWriter struct {
	ctx     context.Context
	w       io.Writer
	onWrite func(delta int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.w.Write(b)
	if n > 0 {
		p.onWrite(int64(n))
	}
	return n, err
}
