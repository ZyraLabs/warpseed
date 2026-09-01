package sftpfast

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
)

// ErrChunkPreallocUnsupported means the server will not size the remote part
// file up front, so the chunked resume guard cannot be made sound. The caller
// must fall back to the single-stream Upload.
var ErrChunkPreallocUnsupported = errors.New("server cannot preallocate the remote part file")

// resumeVerifyBytes is how much of each already-claimed range is read back
// from the server and compared against the local source before its bytes are
// trusted. Head and tail are both sampled: the tail catches a torn write, the
// head catches a part file belonging to some other file entirely.
const resumeVerifyBytes = 256 << 10

// truncateRemote is a seam so tests can force the FSETSTAT path to fail and
// exercise the sparse-write fallback.
var truncateRemote = func(rf *sftp.File, size int64) error { return rf.Truncate(size) }

// isNotExistRemote reports whether a server error means "that file is not
// there". pkg/sftp normalises SSH_FX_NO_SUCH_FILE to fs.ErrNotExist, but the
// code is unexported and not every server answers with it, so the message is
// a second opinion rather than the only one.
func isNotExistRemote(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "file does not exist") || strings.Contains(msg, "no such file")
}

// RemoveRemote deletes one remote file, ignoring absence. Deliberately NOT
// Client.Remove, which recurses into directories.
func (c *Client) RemoveRemote(remotePath string) error {
	if err := c.sftp.Remove(remotePath); err != nil && !isNotExistRemote(err) {
		return fmt.Errorf("remove remote %q: %w", remotePath, err)
	}
	return nil
}

// StatRemoteSize reports a remote file's size, distinguishing absent from
// error so a resume guard can tell them apart.
func (c *Client) StatRemoteSize(remotePath string) (size int64, exists bool, err error) {
	st, err := c.sftp.Stat(remotePath)
	if err != nil {
		if isNotExistRemote(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("stat remote %q: %w", remotePath, err)
	}
	return st.Size(), true, nil
}

// UploadChunks sends ranges of one local file in parallel — one worker per
// client, each on its own connection — writing into a single remote .wschunk
// at absolute offsets. The mirror of DownloadChunks: it is what lifts a large
// upload past a server's per-connection speed cap.
//
// progress reports byte deltas per chunk index; checkpoint persists a chunk's
// cumulative byte count so an interrupted transfer resumes each range where it
// stopped rather than restarting the file.
func UploadChunks(
	ctx context.Context,
	clients []*Client,
	localPath, remotePath string,
	size int64,
	ranges []ChunkRange,
	progress func(idx int, delta int64),
	checkpoint func(idx int, done int64),
) error {
	if len(clients) == 0 {
		return errors.New("no connections for chunked upload")
	}
	if progress == nil {
		progress = func(int, int64) {}
	}
	if checkpoint == nil {
		checkpoint = func(int, int64) {}
	}

	// Validate the source against the plan before anything is created on the
	// server: a source that no longer matches the plan must be refused, never
	// silently re-planned around.
	lf0, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local %q: %w", localPath, err)
	}
	defer lf0.Close()
	lstat, err := lf0.Stat()
	if err != nil {
		return fmt.Errorf("stat local %q: %w", localPath, err)
	}
	if lstat.Size() != size {
		return fmt.Errorf("local source is %d bytes, plan expects %d", lstat.Size(), size)
	}
	mtime := lstat.ModTime()

	if dir := path.Dir(remotePath); dir != "." && dir != "/" {
		if err := clients[0].sftp.MkdirAll(dir); err != nil {
			return fmt.Errorf("create remote dir %q: %w", dir, err)
		}
	}
	part := remotePath + ChunkPartSuffix

	var resumed int64
	for _, r := range ranges {
		resumed += r.Done
	}
	if resumed > 0 {
		// The part is already the right size and already holds bytes we are
		// about to skip — prove they are OUR bytes before trusting them.
		// Nothing here may truncate or re-create: that would destroy the very
		// state the checkpoints describe.
		if err := verifyResumableRemotePart(ctx, clients[0], lf0, part, size, ranges); err != nil {
			return err
		}
	} else {
		// A leftover from a different file would otherwise survive under the
		// ranges we never write.
		if err := clients[0].RemoveRemote(part); err != nil {
			return err
		}
		if err := prepareRemotePart(clients[0], part, size); err != nil {
			return err
		}
	}

	// Probe fsync once, not per range: pkg/sftp's Sync fails outright unless
	// the server advertised fsync@openssh.com. Real OpenSSH does, so the
	// seedbox gets full durability ordering; where it is absent the SFTP WRITE
	// status reply is the acknowledgement, and the read-back guard above is
	// what covers the residual window on the next attempt.
	fsyncOK := false
	if probe, perr := clients[0].sftp.OpenFile(part, os.O_WRONLY|os.O_CREATE); perr == nil {
		fsyncOK = probe.Sync() == nil
		probe.Close()
	}

	// Byte-accounting is the real integrity gate: every range must report its
	// full length as sent before the file may be published. The remote size
	// check in finalization is vacuous here — preallocation guarantees it.
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
			cancel() // one failed range stops the rest; the .wschunk survives for resume
		}
		errMu.Unlock()
	}

	for _, client := range clients {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			// Each worker owns its own local read handle: Windows Pread takes
			// a per-handle mutex and reuses one overlapped operation struct,
			// so a shared os.File would serialize every lane.
			lf, err := os.Open(localPath)
			if err != nil {
				fail(fmt.Errorf("open local source: %w", err))
				return
			}
			defer lf.Close()

			// O_WRONLY|O_CREATE and nothing else. O_TRUNC would wipe the other
			// workers' ranges; O_APPEND becomes SSH_FXF_APPEND, which OpenSSH
			// honours as O_APPEND on the fd, so every write would land at EOF
			// regardless of its offset — a correctly-sized, scrambled file
			// that no size check can catch. (pkg/sftp's test server ignores
			// the flag, so that failure would be invisible in unit tests.)
			rf, err := c.sftp.OpenFile(part, os.O_WRONLY|os.O_CREATE)
			if err != nil {
				fail(fmt.Errorf("open remote part: %w", err))
				return
			}
			// CLOSE carries the server's status for close(2), which is where
			// delayed-allocation storage (NFS, quotas) reports ENOSPC/EDQUOT
			// for bytes its WRITE replies already acknowledged. Preallocation
			// makes the finalize size check blind to that, so discarding this
			// would publish a file whose tail is holes. A cancelled run is not
			// a close failure, so it never masks the real reason we stopped.
			defer func() {
				if cerr := rf.Close(); cerr != nil && wctx.Err() == nil {
					fail(fmt.Errorf("close remote part: %w", cerr))
				}
			}()

			buf := make([]byte, chunkBuffer)
			for r := range work {
				if wctx.Err() != nil {
					return
				}
				record := func(idx int, done int64) {
					account(int64(idx), done)
					checkpoint(idx, done)
				}
				if err := sendRange(wctx, lf, rf, r, buf, fsyncOK, progress, record); err != nil {
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
		return fmt.Errorf("chunked upload cancelled: %w", err)
	}

	accMu.Lock()
	var written int64
	for _, v := range accounted {
		written += v
	}
	accMu.Unlock()
	if written != size {
		return fmt.Errorf("sent %d of %d bytes — refusing to publish an incomplete file", written, size)
	}
	return finalizeRemoteChunked(clients[0], part, remotePath, size, mtime)
}

// prepareRemotePart sizes the remote part so that "part size == final size"
// becomes a meaningful resume invariant. Writing past EOF already works on
// every server that implements WRITE as lseek+write (OpenSSH) or WriteAt
// (pkg/sftp's server), so this is not needed for the happy path — without it
// the part's size is merely whatever the highest completed write reached, and
// no size-based guard is possible at all.
func prepareRemotePart(c *Client, part string, size int64) error {
	if size <= 0 {
		return ErrChunkPreallocUnsupported
	}
	rf, err := c.sftp.OpenFile(part, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return fmt.Errorf("create remote part: %w", err)
	}
	defer rf.Close()

	// Stage 1: FSETSTAT SIZE. Works on OpenSSH (ftruncate), but the SFTP spec
	// leaves growth undefined, so a failure here is a capability probe result,
	// not a bug.
	if err := truncateRemote(rf, size); err == nil {
		if st, serr := c.sftp.Stat(part); serr == nil && st.Size() == size {
			return nil
		}
	}
	// Stage 2: sparse-write probe. That single byte lives inside the LAST
	// range, which the completeness gate always requires a worker to overwrite
	// before publishing, and it exercises exactly the mechanism the workers
	// rely on — so if it works, chunked upload works.
	if _, err := rf.WriteAt([]byte{0}, size-1); err == nil {
		if st, serr := c.sftp.Stat(part); serr == nil && st.Size() == size {
			return nil
		}
	}
	return ErrChunkPreallocUnsupported
}

// verifyResumableRemotePart proves the remote part still holds the bytes the
// checkpoints claim, and returns ErrChunkStateLost on any doubt. It is
// strictly stronger than the download's size-only guard, and it can be: for an
// upload the authoritative bytes are on local disk and can be compared.
//
// Three failure modes a size check alone cannot see, all ending in a published
// file of the right length and the wrong content: the directory was restored
// from a snapshot or rewritten by another tool between attempts; two queue
// rows target the same destination and so compute the same part path; or the
// source was edited in place with no change to its size or mtime (routine for
// build outputs and for torrent clients writing into preallocated files).
//
// The linear Upload's mtime heuristic is no help here — that part is a strict
// prefix written by one stream, this one is preallocated and touched by
// several connections, so its mtime says nothing about which ranges are real.
func verifyResumableRemotePart(ctx context.Context, c *Client, lf *os.File, part string, size int64, ranges []ChunkRange) error {
	// ErrChunkStateLost is destructive — the dispatcher answers it by deleting
	// the remote part and restarting from byte zero. Only facts that actually
	// prove the state is wrong may return it; a stat that failed because the
	// server was busy proves nothing, and would throw away tens of GB.
	sz, exists, err := c.StatRemoteSize(part)
	if err != nil {
		return fmt.Errorf("verify remote part: %w", err)
	}
	if !exists || sz != size {
		return ErrChunkStateLost
	}

	rf, err := c.sftp.OpenFile(part, os.O_RDONLY)
	if err != nil {
		if isNotExistRemote(err) {
			return ErrChunkStateLost
		}
		return fmt.Errorf("open remote part to verify: %w", err)
	}
	defer rf.Close()

	remote := make([]byte, resumeVerifyBytes)
	local := make([]byte, resumeVerifyBytes)
	sameAt := func(off, n int64) (bool, error) {
		if _, err := rf.ReadAt(remote[:n], off); err != nil {
			// A short read of a range we were told is complete is itself proof
			// the state is gone; any other failure is the transport, and must
			// not be allowed to masquerade as proof.
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return false, nil
			}
			return false, fmt.Errorf("read remote part at %d: %w", off, err)
		}
		if _, err := lf.ReadAt(local[:n], off); err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, fmt.Errorf("read local source at %d: %w", off, err)
		}
		return bytes.Equal(remote[:n], local[:n]), nil
	}

	for _, r := range ranges {
		// Cancellation must not destroy resumable state, so it never reports
		// as ErrChunkStateLost.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("chunked upload cancelled: %w", err)
		}
		if r.Done <= 0 {
			continue
		}
		n := int64(resumeVerifyBytes)
		if r.Done < n {
			n = r.Done
		}
		// Tail catches a torn write; head catches a part belonging to another
		// file entirely (only worth a second read once the windows are
		// disjoint).
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

// sendRange copies one range to the remote part, resuming at its recorded
// offset. The mirror of fetchRange with the directions swapped.
func sendRange(
	ctx context.Context,
	lf *os.File,
	rf *sftp.File,
	r ChunkRange,
	buf []byte,
	fsyncOK bool,
	progress func(idx int, delta int64),
	checkpoint func(idx int, done int64),
) error {
	pos := r.Offset + r.Done
	end := r.Offset + r.Length
	done := r.Done
	sinceCheckpoint := int64(0)
	lastCheckpoint := time.Now()

	// Same durability ordering as the download side: bytes reach the server,
	// THEN the checkpoint records them. Where the server has no fsync the
	// WRITE status reply is the acknowledgement — the analogue of a successful
	// write(2) — and the resume read-back covers the rest.
	commit := func() error {
		if fsyncOK {
			if err := rf.Sync(); err != nil {
				return fmt.Errorf("sync chunk %d: %w", r.Idx, err)
			}
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
		n, rerr := lf.ReadAt(buf[:want], pos)
		if n > 0 {
			w, werr := rf.WriteAt(buf[:n], pos)
			// Advance by w, never by n: pkg/sftp reports the count up to the
			// EARLIEST failing offset, so bytes before it are guaranteed
			// present and re-sending from pos+w is safe. Advancing by n would
			// over-credit the checkpoint and leave a hole.
			pos += int64(w)
			done += int64(w)
			sinceCheckpoint += int64(w)
			if w > 0 {
				progress(r.Idx, int64(w))
			}
			if werr != nil {
				if cerr := commit(); cerr != nil {
					return errors.Join(werr, cerr)
				}
				return fmt.Errorf("write chunk %d at %d: %w", r.Idx, pos, werr)
			}
			// Checkpoint mid-range so an ungraceful kill costs at most this
			// much rework. The time bound matters on a throttled or slow link,
			// where the byte bound alone could be minutes away.
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

// finalizeRemoteChunked publishes the assembled remote file. Ranges are written
// directly at their offsets, so there is no merge step to corrupt; byte
// accounting in the caller is what proves completeness, and the size check here
// only guards against an external truncation between the last write and the
// rename. There is no remote analogue of the download's directory fsync —
// SFTP cannot express one.
func finalizeRemoteChunked(c *Client, part, remotePath string, size int64, mtime time.Time) error {
	st, err := c.sftp.Stat(part)
	if err != nil {
		return fmt.Errorf("stat remote part: %w", err)
	}
	if st.Size() != size {
		return fmt.Errorf("remote part is %d bytes, expected %d", st.Size(), size)
	}
	// Never overwrite a directory: pkg/sftp's Remove falls back to
	// RemoveDirectory, so a name collision would silently delete it.
	if st, err := c.sftp.Stat(remotePath); err == nil && st.IsDir() {
		return fmt.Errorf("remote destination %q is a directory", remotePath)
	}
	// Prefer atomic replace; fall back to remove+rename on servers without the
	// posix-rename extension.
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
	if err := c.sftp.Chtimes(remotePath, mtime, mtime); err != nil {
		// Best-effort: some servers deny setstat on owned files; the upload
		// itself is complete, so surface nothing.
		_ = err
	}
	return nil
}
