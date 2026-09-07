package sftpfast

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
	"warpseed/internal/applog"
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
//
// The upload side is the mirror image, and worse because the holes end up on
// the server: if the linear Upload ever adopted a .wschunk as its resume
// part, offset would equal localSize, the offset < localSize guard would be
// false, no bytes would be sent, and the rename would publish a full-size
// file of holes.
const ChunkPartSuffix = ".wschunk"

// checkpointEvery and checkpointInterval bound how much work an ungraceful
// kill can discard — whichever comes first, so a slow link still records
// progress regularly.
const (
	checkpointEvery    = 8 << 20
	checkpointInterval = 30 * time.Second
)

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

// verifyResumableLocalPart proves the local part still holds the bytes the
// checkpoints claim, and returns ErrChunkStateLost on any doubt.
//
// The size check above cannot do this on its own and never could: the part is
// preallocated to the full size before the first byte arrives, so Truncate
// guarantees the number it compares. Anything that replaced the file's
// contents without changing its length passes — a folder restored from a
// snapshot or from Windows' "Previous Versions", a second tool writing the
// same path, or a part left by an interrupted attempt at a different file of
// the same size. The ranges marked done are then never fetched again, and
// the published file has the right length and the wrong bytes.
//
// For a download the authoritative bytes are the remote ones, so this reads
// back a window of each claimed range from the server and compares. That is
// two small reads per range against a transfer measured in gigabytes — the
// mirror of what verifyResumableRemotePart does for uploads, which had this
// protection from the start.
func verifyResumableLocalPart(ctx context.Context, c *Client, remotePath, part string, ranges []ChunkRange) error {
	lf, err := os.Open(part)
	if err != nil {
		// ErrChunkStateLost is destructive — the dispatcher answers it by
		// deleting the part and restarting from byte zero — so only "the
		// file is gone" may return it. The caller has already stat'd the
		// part, and os.Stat needs no handle, so an open that fails on a file
		// that exists means something is holding it (a backup agent or
		// scanner on Windows takes a share-violation here). That is a reason
		// to try again later, not to throw away tens of gigabytes.
		if os.IsNotExist(err) {
			return ErrChunkStateLost
		}
		return fmt.Errorf("open part to verify: %w", err)
	}
	defer lf.Close()

	rf, err := c.sftp.Open(remotePath)
	if err != nil {
		if isNotExistRemote(err) {
			return ErrChunkStateLost
		}
		// ErrChunkStateLost is destructive — the dispatcher answers it by
		// deleting the part and restarting from byte zero. A server that was
		// merely busy proves nothing and must never trigger that.
		return fmt.Errorf("open remote source to verify: %w", err)
	}
	defer rf.Close()

	remote := make([]byte, resumeVerifyBytes)
	local := make([]byte, resumeVerifyBytes)
	sameAt := func(off, n int64) (bool, error) {
		if _, err := rf.ReadAt(remote[:n], off); err != nil {
			// A short read where the remote file is supposed to have bytes is
			// itself proof the state is wrong; anything else is the transport
			// and must not masquerade as proof.
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return false, nil
			}
			return false, fmt.Errorf("read remote source at %d: %w", off, err)
		}
		if _, err := lf.ReadAt(local[:n], off); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return false, nil
			}
			return false, fmt.Errorf("read local part at %d: %w", off, err)
		}
		return bytes.Equal(remote[:n], local[:n]), nil
	}

	for _, r := range ranges {
		// Cancellation must never destroy resumable state, so it is reported
		// as itself and never as ErrChunkStateLost.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("chunked download cancelled: %w", err)
		}
		if r.Done <= 0 {
			continue
		}
		n := int64(resumeVerifyBytes)
		if r.Done < n {
			n = r.Done
		}
		// Tail catches a torn write; head catches a part belonging to a
		// different file entirely (worth the second read only once the two
		// windows are disjoint).
		ok, err := sameAt(r.Offset+r.Done-n, n)
		if err != nil {
			return err
		}
		if !ok {
			return ErrChunkStateLost
		}
		if r.Done >= 2*resumeVerifyBytes {
			ok, err := sameAt(r.Offset, resumeVerifyBytes)
			if err != nil {
				return err
			}
			if !ok {
				return ErrChunkStateLost
			}
		}
	}
	return nil
}

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
		if err := verifyResumableLocalPart(ctx, clients[0], remotePath, part, ranges); err != nil {
			return err
		}
	}

	// Size the destination once so workers can write at any offset. On ext4
	// a bare Truncate already yields a sparse file; on NTFS it does not —
	// the file must be flagged sparse first, or the first write past the
	// valid-data length makes the kernel zero-fill everything below it
	// inside an uncancellable WriteFile (see markSparse).
	//
	// This block runs on EVERY attempt, resume included, on purpose: a part
	// left by a build that did not flag it (1.1.0) is only cheap to resume
	// because the flag is applied again here. Do not skip it when the part
	// already has the right size.
	name := filepath.Base(localPath)
	sizer, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create part file: %w", err)
	}
	if err := markSparse(sizer); err != nil {
		// Not fatal: a volume that refuses (FAT32, some network shares)
		// still works, only with the zero-fill cost. Said per attempt.
		log.Printf("sftpfast: sparse flag on %s refused (%v); first writes may stall while the volume zero-fills", name, err)
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
		applog.Debugf("sftpfast: chunk %d of %s: offset %d length %d done %d", r.Idx, name, r.Offset, r.Length, r.Done)
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
				if err := fetchRange(wctx, name, rf, lf, r, buf, progress, record); err != nil {
					fail(err)
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
	name string,
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
	lastCheckpoint := time.Now()
	first := true

	// Durability ordering, non-negotiable: bytes hit the disk, THEN the
	// checkpoint records them. A checkpoint that runs first can claim bytes
	// a crash never flushed, and the resume would skip them — leaving a hole
	// no size check can see.
	commit := func() error {
		if err := lf.Sync(); err != nil {
			return fmt.Errorf("sync chunk %d: %w", r.Idx, err)
		}
		checkpoint(r.Idx, done)
		sinceCheckpoint = 0
		lastCheckpoint = time.Now()
		return nil
	}

	for pos < end {
		if err := ctx.Err(); err != nil {
			if cerr := commit(); cerr != nil {
				return cerr
			}
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
			wstart := time.Now()
			_, werr := lf.WriteAt(buf[:n], pos)
			if first {
				// The first write of a range is where a non-sparse volume
				// pays its zero-fill; a long number here is that stall.
				applog.Debugf("sftpfast: chunk %d of %s: first write at %d took %s", r.Idx, name, pos, time.Since(wstart).Round(time.Millisecond))
				first = false
			}
			if werr != nil {
				if cerr := commit(); cerr != nil {
					return errors.Join(werr, cerr)
				}
				return fmt.Errorf("write chunk %d at %d: %w", r.Idx, pos, werr)
			}
			pos += int64(n)
			done += int64(n)
			sinceCheckpoint += int64(n)
			progress(r.Idx, int64(n))
			// Checkpoint mid-range so an ungraceful kill costs at most this
			// much rework. The time bound matters on a throttled or slow
			// link, where the byte bound alone could be minutes away.
			if sinceCheckpoint >= checkpointEvery || time.Since(lastCheckpoint) >= checkpointInterval {
				if cerr := commit(); cerr != nil {
					return cerr
				}
			}
		}
		if rerr != nil {
			// EOF with a full range read is the normal end of the last chunk.
			if errors.Is(rerr, io.EOF) && pos >= end {
				break
			}
			if cerr := commit(); cerr != nil {
				return cerr
			}
			return fmt.Errorf("read chunk %d at %d: %w", r.Idx, pos, rerr)
		}
	}
	return commit()
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
	if st.Size() != size {
		f.Close()
		return fmt.Errorf("assembled file is %d bytes, expected %d", st.Size(), size)
	}
	// Every byte is accounted for, so the sparse flag has done its job;
	// clear it so the published file is an ordinary one to Explorer and
	// backup tools. Best effort — a refusal changes nothing about the data.
	if err := unmarkSparse(f); err != nil {
		applog.Debugf("sftpfast: %s: sparse flag not cleared (%v)", filepath.Base(localPath), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close assembled file: %w", err)
	}
	if err := os.Rename(part, localPath); err != nil {
		return fmt.Errorf("finalize %q: %w", localPath, err)
	}
	// Persist the rename itself: without a directory fsync, a crash can
	// leave the file under neither name. (No-op on Windows, where opening a
	// directory for sync isn't supported — the rename is already durable.)
	if dir, derr := os.Open(filepath.Dir(localPath)); derr == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}

// StatRemote reports a remote file's size and modification time, used to
// plan chunks and to detect a source that changed under a resume.
func (c *Client) StatRemote(remotePath string) (size int64, modTimeUnix int64, err error) {
	size, modTimeUnix, _, err = c.StatRemoteEntry(remotePath)
	return size, modTimeUnix, err
}

// StatRemoteEntry is StatRemote plus whether the path is a directory. The
// overwrite policy needs that: a directory's stat size is a filesystem
// bookkeeping number, and comparing a real file against it would call a
// 3 GB upload "newer and larger" and send the lot before the server refused
// to be overwritten.
func (c *Client) StatRemoteEntry(remotePath string) (size, modTimeUnix int64, isDir bool, err error) {
	st, err := c.sftp.Stat(remotePath)
	if err != nil {
		return 0, 0, false, fmt.Errorf("stat remote %q: %w", remotePath, err)
	}
	return st.Size(), st.ModTime().Unix(), st.IsDir(), nil
}
