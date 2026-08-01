package sftpfast

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// chunkBuffer is the read size per request batch. Large enough that
// pkg/sftp splits it across its concurrent request pipeline, small enough
// that progress stays responsive.
const chunkBuffer = 4 << 20

// ChunkPartSuffix marks a chunked assembly in progress. It is deliberately
// NOT the linear PartSuffix: a chunked part is preallocated to the final
// size and sparse, so the single-connection resume path (which infers its
// offset from file size) would mistake it for a finished download and
// publish a file full of holes.
const ChunkPartSuffix = ".wschunk"

// checkpointEvery bounds how much work an ungraceful kill can discard.
const checkpointEvery = 8 << 20

// ErrChunkStateLost means the recorded per-chunk offsets no longer describe
// the file on disk (it was deleted, truncated, or replaced). The caller must
// discard the checkpoints and restart the transfer rather than assemble a
// file with holes where the "already done" ranges should be.
var ErrChunkStateLost = errors.New("chunk state no longer matches the partial file on disk")

// ChunkRange is one byte range of a chunked download. Done is the number of
// bytes already present at Offset from an earlier attempt.
type ChunkRange struct {
	Idx    int
	Offset int64
	Length int64
	Done   int64
}

// Remaining reports the bytes still to fetch for this range.
func (c ChunkRange) Remaining() int64 { return c.Length - c.Done }

// DownloadChunks fetches ranges of one remote file in parallel — one worker
// per client, each on its own connection — writing into a single local
// .wspart at absolute offsets. This is what lifts a single large file past
// a server's per-connection speed cap.
//
// progress reports byte deltas per chunk index; checkpoint persists a
// chunk's cumulative byte count so an interrupted transfer resumes each
// range where it stopped rather than restarting the file.
func DownloadChunks(
	ctx context.Context,
	clients []*Client,
	remotePath, localPath string,
	size int64,
	ranges []ChunkRange,
	progress func(idx int, delta int64),
	checkpoint func(idx int, done int64),
) error {
	if len(clients) == 0 {
		return errors.New("no connections for chunked download")
	}
	if progress == nil {
		progress = func(int, int64) {}
	}
	if checkpoint == nil {
		checkpoint = func(int, int64) {}
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("create local dir: %w", err)
	}
	part := localPath + ChunkPartSuffix

	// Resume safety: a recorded offset is only trustworthy if the file those
	// bytes were written to is still here, still the right size. Otherwise
	// the "already done" ranges would silently become zero-filled holes —
	// and the size check could never catch it, because we preallocate.
	var resumed int64
	for _, r := range ranges {
		resumed += r.Done
	}
	if resumed > 0 {
		st, serr := os.Stat(part)
		if serr != nil || st.Size() != size {
			return ErrChunkStateLost
		}
	}

	// Size the destination once so workers can write at any offset. Truncate
	// creates a sparse file on both NTFS and ext4 — no zero-fill stall even
	// for a 50 GB target.
	sizer, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create part file: %w", err)
	}
	if err := sizer.Truncate(size); err != nil {
		sizer.Close()
		return fmt.Errorf("preallocate part file: %w", err)
	}
	if err := sizer.Close(); err != nil {
		return fmt.Errorf("close part sizer: %w", err)
	}

	// Byte-accounting is the real integrity gate: every range must report
	// its full length as written before the file may be published. The size
	// check alone is vacuous here — Truncate guarantees it.
	var (
		accMu     sync.Mutex
		accounted = make(map[int]int64, len(ranges))
	)
	account := func(idx, done int64) {
		accMu.Lock()
		accounted[int(idx)] = done
		accMu.Unlock()
	}

	// Work queue: chunks outnumber connections when a plan is re-run with
	// fewer clients, so workers pull rather than owning a fixed range.
	work := make(chan ChunkRange, len(ranges))
	for _, r := range ranges {
		account(int64(r.Idx), r.Done)
		if r.Remaining() > 0 {
			work <- r
		}
	}
	close(work)

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg      sync.WaitGroup
		errMu   sync.Mutex
		firstEr error
	)
	fail := func(err error) {
		errMu.Lock()
		if firstEr == nil {
			firstEr = err
			cancel() // one failed range stops the rest; .wspart survives for resume
		}
		errMu.Unlock()
	}

	for _, client := range clients {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			// Each worker owns its own local handle: concurrent WriteAt on
			// separate handles at disjoint offsets is safe on Windows and
			// POSIX alike, with no shared-offset state to coordinate.
			lf, err := os.OpenFile(part, os.O_WRONLY, 0o644)
			if err != nil {
				fail(fmt.Errorf("open part file: %w", err))
				return
			}
			defer lf.Close()

			rf, err := c.sftp.Open(remotePath)
			if err != nil {
				fail(fmt.Errorf("open remote %q: %w", remotePath, err))
				return
			}
			defer rf.Close()

			buf := make([]byte, chunkBuffer)
			for r := range work {
				if wctx.Err() != nil {
					return
				}
				record := func(idx int, done int64) {
					account(int64(idx), done)
					checkpoint(idx, done)
				}
				if err := fetchRange(wctx, rf, lf, r, buf, progress, record); err != nil {
					fail(err)
					return
				}
				// Durability: each completed range is flushed before it counts
				// as done, so a crash can never leave a checkpoint claiming
				// bytes the OS had not yet written.
				if serr := lf.Sync(); serr != nil {
					fail(fmt.Errorf("sync chunk %d: %w", r.Idx, serr))
					return
				}
			}
		}(client)
	}

	wg.Wait()
	if firstEr != nil {
		return firstEr
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("chunked download cancelled: %w", err)
	}

	accMu.Lock()
	var written int64
	for _, v := range accounted {
		written += v
	}
	accMu.Unlock()
	if written != size {
		return fmt.Errorf("assembled %d of %d bytes — refusing to publish an incomplete file", written, size)
	}
	return finalizeChunked(part, localPath, size)
}

// fetchRange copies one range, resuming at its recorded offset.
func fetchRange(
	ctx context.Context,
	rf io.ReaderAt,
	lf interface {
		io.WriterAt
		Sync() error
	},
	r ChunkRange,
	buf []byte,
	progress func(idx int, delta int64),
	checkpoint func(idx int, done int64),
) error {
	pos := r.Offset + r.Done
	end := r.Offset + r.Length
	done := r.Done
	sinceCheckpoint := int64(0)

	for pos < end {
		if err := ctx.Err(); err != nil {
			checkpoint(r.Idx, done)
			return fmt.Errorf("chunk %d cancelled: %w", r.Idx, err)
		}
		want := int64(len(buf))
		if remaining := end - pos; remaining < want {
			want = remaining
		}
		// ReadAt with a large buffer engages pkg/sftp's concurrent request
		// pipeline inside this range, on top of the cross-range parallelism.
		n, rerr := rf.ReadAt(buf[:want], pos)
		if n > 0 {
			if _, werr := lf.WriteAt(buf[:n], pos); werr != nil {
				checkpoint(r.Idx, done)
				return fmt.Errorf("write chunk %d at %d: %w", r.Idx, pos, werr)
			}
			pos += int64(n)
			done += int64(n)
			sinceCheckpoint += int64(n)
			progress(r.Idx, int64(n))
			// Checkpoint mid-range so an ungraceful kill costs at most this
			// much rework, not the whole range.
			if sinceCheckpoint >= checkpointEvery {
				sinceCheckpoint = 0
				if syncer, ok := lf.(interface{ Sync() error }); ok {
					if serr := syncer.Sync(); serr != nil {
						return fmt.Errorf("sync chunk %d: %w", r.Idx, serr)
					}
				}
				checkpoint(r.Idx, done)
			}
		}
		if rerr != nil {
			// EOF with a full range read is the normal end of the last chunk.
			if errors.Is(rerr, io.EOF) && pos >= end {
				break
			}
			checkpoint(r.Idx, done)
			return fmt.Errorf("read chunk %d at %d: %w", r.Idx, pos, rerr)
		}
	}
	checkpoint(r.Idx, done)
	return nil
}

// finalizeChunked flushes and publishes the assembled file. Ranges are
// written directly at their offsets, so there is no merge step to corrupt;
// byte accounting in the caller is what proves completeness, and the size
// check here only guards against an external truncation between the last
// write and the rename.
func finalizeChunked(part, localPath string, size int64) error {
	f, err := os.OpenFile(part, os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open assembled file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync assembled file: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat assembled file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close assembled file: %w", err)
	}
	if st.Size() != size {
		return fmt.Errorf("assembled file is %d bytes, expected %d", st.Size(), size)
	}
	if err := os.Rename(part, localPath); err != nil {
		return fmt.Errorf("finalize %q: %w", localPath, err)
	}
	return nil
}

// StatRemote reports a remote file's size and modification time, used to
// plan chunks and to detect a source that changed under a resume.
func (c *Client) StatRemote(remotePath string) (size int64, modTimeUnix int64, err error) {
	st, err := c.sftp.Stat(remotePath)
	if err != nil {
		return 0, 0, fmt.Errorf("stat remote %q: %w", remotePath, err)
	}
	return st.Size(), st.ModTime().Unix(), nil
}
