package sftpfast

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"time"
)

// Upload sends local → remote with byte-level resume, mirroring Download:
// data lands in a remote .wspart, is validated by size, and only a clean
// finish renames it into place. onStart reports the resume offset once it is
// final; progress receives byte deltas.
func (c *Client) Upload(ctx context.Context, localPath, remotePath string, onStart func(offset int64), progress func(delta int64)) error {
	if progress == nil {
		progress = func(int64) {}
	}
	if onStart == nil {
		onStart = func(int64) {}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("upload cancelled: %w", err)
	}

	lf, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local %q: %w", localPath, err)
	}
	defer lf.Close()
	lstat, err := lf.Stat()
	if err != nil {
		return fmt.Errorf("stat local %q: %w", localPath, err)
	}
	localSize := lstat.Size()

	if dir := path.Dir(remotePath); dir != "." && dir != "/" {
		if err := c.sftp.MkdirAll(dir); err != nil {
			return fmt.Errorf("create remote dir %q: %w", dir, err)
		}
	}

	part := remotePath + PartSuffix
	// Resume only from a part we plausibly wrote: it must be no larger than
	// the source and no older than the source's last modification. Anything
	// else (stale leftovers, content planted by someone else) restarts at
	// zero rather than being renamed into place as "our" upload.
	offset := int64(0)
	if st, err := c.sftp.Stat(part); err == nil {
		// SFTP timestamps are second-granular while local ones are
		// nanosecond, so a legitimately newer part can read as marginally
		// older; allow that slack without accepting genuinely stale files.
		const clockSlack = 2 * time.Second
		notStale := st.ModTime().Add(clockSlack).After(lstat.ModTime())
		if st.Size() <= localSize && notStale {
			offset = st.Size()
		}
	}
	onStart(offset)

	rf, err := c.sftp.OpenFile(part, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return fmt.Errorf("open remote part: %w", err)
	}
	defer rf.Close()
	if offset == 0 {
		if err := rf.Truncate(0); err != nil {
			return fmt.Errorf("truncate remote part: %w", err)
		}
	}

	if offset < localSize {
		if _, err := lf.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("seek local to %d: %w", offset, err)
		}
		if _, err := rf.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("seek remote to %d: %w", offset, err)
		}
		// ReadFrom drives pkg/sftp's concurrent write pipeline; the reader is
		// ctx-aware so cancellation stops the copy deterministically.
		_, copyErr := rf.ReadFrom(&progressReader{ctx: ctx, r: lf, onRead: progress})
		if copyErr != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("upload cancelled: %w", ctx.Err())
			}
			return fmt.Errorf("transfer %q: %w", localPath, copyErr)
		}
	}

	if err := rf.Close(); err != nil {
		return fmt.Errorf("close remote part: %w", err)
	}
	// Never overwrite a directory: pkg/sftp's Remove falls back to
	// RemoveDirectory, so a name collision would silently delete it.
	if st, err := c.sftp.Stat(remotePath); err == nil && st.IsDir() {
		return fmt.Errorf("remote destination %q is a directory", remotePath)
	}
	// Prefer atomic replace; fall back to remove+rename on servers without
	// the posix-rename extension.
	if err := c.sftp.PosixRename(part, remotePath); err != nil {
		if _, serr := c.sftp.Stat(remotePath); serr == nil {
			if rerr := c.sftp.Remove(remotePath); rerr != nil {
				return fmt.Errorf("replace remote %q: %w", remotePath, rerr)
			}
		}
		if rerr := c.sftp.Rename(part, remotePath); rerr != nil {
			return fmt.Errorf("finalize remote %q: %w", remotePath, rerr)
		}
	}
	if err := c.sftp.Chtimes(remotePath, lstat.ModTime(), lstat.ModTime()); err != nil {
		// Best-effort: some servers deny setstat on owned files; the upload
		// itself is complete, so surface nothing.
		_ = err
	}
	return nil
}

// WalkFiles walks a remote directory tree depth-first, calling fn for every
// regular file with its full path and size. Used to expand folder downloads
// into queue rows. Depth and count are bounded to keep runaway trees sane.
func (c *Client) WalkFiles(ctx context.Context, root string, fn func(path string, size int64) error) error {
	const maxEntries = 50000
	seen := 0
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 32 {
			return fmt.Errorf("directory tree deeper than 32 levels at %q", dir)
		}
		listing, err := c.List(dir)
		if err != nil {
			return err
		}
		for _, e := range listing.Entries {
			full := path.Join(dir, e.Name)
			if e.IsDir {
				if err := walk(full, depth+1); err != nil {
					return err
				}
				continue
			}
			seen++
			if seen > maxEntries {
				return fmt.Errorf("more than %d files under %q — refusing runaway walk", maxEntries, root)
			}
			if err := fn(full, e.Size); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(path.Clean(root), 0)
}

type progressReader struct {
	ctx    context.Context
	r      io.Reader
	onRead func(delta int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(b)
	if n > 0 {
		p.onRead(int64(n))
	}
	return n, err
}
