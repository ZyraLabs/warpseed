package sftpfast

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"
)

// writeRemotePart builds a preallocated part file the way a previous chunked
// attempt would have left it: full final size, sparse everywhere the caller
// does not fill.
func writeRemotePart(t *testing.T, part string, size int64, fill func(f *os.File)) {
	t.Helper()
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if fill != nil {
		fill(f)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUploadChunksAssemblesExactFile(t *testing.T) {
	// Arrange — 4 connections over a file with an uneven tail
	c := testClients(t, 4)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	data := writeRandomFile(t, src, 1<<20+777)
	dst := filepath.Join(dstDir, "big.bin")

	var mu sync.Mutex
	perChunk := map[int]int64{}

	// Act
	err := UploadChunks(context.Background(), c, src, dst, int64(len(data)),
		plan(int64(len(data)), 4),
		func(idx int, delta int64) {
			mu.Lock()
			perChunk[idx] += delta
			mu.Unlock()
		}, nil)

	// Assert — byte-identical, every chunk contributed, no part left behind
	if err != nil {
		t.Fatalf("UploadChunks: %v", err)
	}
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("uploaded content mismatch")
	}
	if len(perChunk) != 4 {
		t.Fatalf("only %d chunks reported progress, want 4", len(perChunk))
	}
	var total int64
	for _, v := range perChunk {
		total += v
	}
	if total != int64(len(data)) {
		t.Fatalf("progress totalled %d, want %d", total, len(data))
	}
	if _, err := os.Stat(dst + ChunkPartSuffix); !os.IsNotExist(err) {
		t.Fatal("chunk part left behind")
	}
	if _, err := os.Stat(dst + PartSuffix); !os.IsNotExist(err) {
		t.Fatal("linear part left behind")
	}
}

func TestUploadChunksResumesPerChunk(t *testing.T) {
	// Arrange — a remote part where chunk 0 is half done and chunk 2 complete,
	// both holding the real source bytes
	c := testClients(t, 3)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	const size = 300 << 10
	data := writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "big.bin")

	ranges := plan(size, 3)
	half := ranges[0].Length / 2
	c2 := ranges[2]
	writeRemotePart(t, dst+ChunkPartSuffix, size, func(f *os.File) {
		if _, err := f.WriteAt(data[:half], 0); err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt(data[c2.Offset:c2.Offset+c2.Length], c2.Offset); err != nil {
			t.Fatal(err)
		}
	})
	ranges[0].Done = half
	ranges[2].Done = c2.Length

	var mu sync.Mutex
	var sent int64

	// Act
	err := UploadChunks(context.Background(), c, src, dst, size, ranges,
		func(_ int, delta int64) {
			mu.Lock()
			sent += delta
			mu.Unlock()
		}, nil)

	// Assert — only the missing bytes moved and the file is intact
	if err != nil {
		t.Fatalf("UploadChunks: %v", err)
	}
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("resumed upload mismatch")
	}
	want := int64(size) - half - c2.Length
	if sent != want {
		t.Fatalf("sent %d bytes, want %d (per-chunk resume)", sent, want)
	}
}

func TestUploadChunksRefusesResumeWhenRemotePartVanished(t *testing.T) {
	// Arrange — checkpoints claim a range is already on the server, but the
	// part file is gone. Trusting them would skip those bytes and publish a
	// full-size file with holes in it.
	c := testClients(t, 2)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	const size = 200 << 10
	writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "big.bin")

	ranges := plan(size, 2)
	ranges[0].Done = ranges[0].Length

	// Act
	err := UploadChunks(context.Background(), c, src, dst, size, ranges, nil, nil)

	// Assert
	if !errors.Is(err, ErrChunkStateLost) {
		t.Fatalf("err = %v, want ErrChunkStateLost", err)
	}
	if _, serr := os.Stat(dst); !os.IsNotExist(serr) {
		t.Fatal("a file with holes was published")
	}
}

func TestUploadChunksRefusesResumeWhenRemotePartWrongSize(t *testing.T) {
	// Arrange — the part exists but its offsets no longer line up with the
	// recorded checkpoints
	c := testClients(t, 2)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	const size = 200 << 10
	writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "big.bin")
	writeRemotePart(t, dst+ChunkPartSuffix, size/4, nil)

	ranges := plan(size, 2)
	ranges[1].Done = ranges[1].Length

	// Act
	err := UploadChunks(context.Background(), c, src, dst, size, ranges, nil, nil)

	// Assert
	if !errors.Is(err, ErrChunkStateLost) {
		t.Fatalf("err = %v, want ErrChunkStateLost", err)
	}
	if _, serr := os.Stat(dst); !os.IsNotExist(serr) {
		t.Fatal("a file with holes was published")
	}
}

func TestUploadChunksRefusesResumeWhenRemotePartContentDiffers(t *testing.T) {
	// Arrange — the part is exactly the right size and the checkpoints look
	// perfect, but the claimed range holds someone else's bytes. Size alone
	// can never see this; the read-back can.
	c := testClients(t, 2)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	const size = 300 << 10
	writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "big.bin")

	ranges := plan(size, 3)
	junk := bytes.Repeat([]byte{0xAA}, int(ranges[1].Length))
	writeRemotePart(t, dst+ChunkPartSuffix, size, func(f *os.File) {
		if _, err := f.WriteAt(junk, ranges[1].Offset); err != nil {
			t.Fatal(err)
		}
	})
	ranges[1].Done = ranges[1].Length

	// Act
	err := UploadChunks(context.Background(), c, src, dst, size, ranges, nil, nil)

	// Assert — refused, and nothing of the final size was published
	if !errors.Is(err, ErrChunkStateLost) {
		t.Fatalf("err = %v, want ErrChunkStateLost", err)
	}
	st, serr := os.Stat(dst)
	if serr == nil {
		t.Fatalf("destination published (%d bytes) over unverified bytes", st.Size())
	}
	if !os.IsNotExist(serr) {
		t.Fatalf("stat destination: %v", serr)
	}
}

func TestUploadChunksCheckpointsOnCancel(t *testing.T) {
	// Arrange
	c := testClients(t, 2)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	const size = 4 << 20
	writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "big.bin")

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	checkpoints := map[int]int64{}

	// Act — cancel once bytes start flowing
	err := UploadChunks(ctx, c, src, dst, size, plan(size, 2),
		func(int, int64) { cancel() },
		func(idx int, done int64) {
			mu.Lock()
			checkpoints[idx] = done
			mu.Unlock()
		})

	// Assert — errored, destination not published, offsets recorded and the
	// remote part kept so the next attempt can resume
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if _, serr := os.Stat(dst); !os.IsNotExist(serr) {
		t.Fatal("destination published despite cancellation")
	}
	if len(checkpoints) == 0 {
		t.Fatal("no chunk checkpoints recorded — resume would restart from zero")
	}
	if _, serr := os.Stat(dst + ChunkPartSuffix); serr != nil {
		t.Fatal("remote part discarded — resume impossible")
	}
}

func TestUploadChunksRejectsShortSource(t *testing.T) {
	// Arrange — claim a larger size than the source actually has
	c := testClients(t, 2)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "f.bin")
	data := writeRandomFile(t, src, 8<<10)
	dst := filepath.Join(dstDir, "f.bin")
	claimed := int64(len(data)) * 2

	// Act
	err := UploadChunks(context.Background(), c, src, dst, claimed, plan(claimed, 2), nil, nil)

	// Assert — refused before anything is created on the server
	if err == nil {
		t.Fatal("expected failure when the source is shorter than planned")
	}
	if _, serr := os.Stat(dst); !os.IsNotExist(serr) {
		t.Fatal("short file was published")
	}
	if _, serr := os.Stat(dst + ChunkPartSuffix); !os.IsNotExist(serr) {
		t.Fatal("remote part created for a source that failed validation")
	}
}

func TestUploadChunksRefusesDirectoryDestination(t *testing.T) {
	// Arrange — destination name is an existing directory
	c := testClients(t, 2)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "f.bin")
	const size = 64 << 10
	writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "collide")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	// Act
	err := UploadChunks(context.Background(), c, src, dst, size, plan(size, 2), nil, nil)

	// Assert — refused, and the directory survives
	if err == nil {
		t.Fatal("expected refusal when destination is a directory")
	}
	if st, serr := os.Stat(dst); serr != nil || !st.IsDir() {
		t.Fatal("destination directory was destroyed")
	}
}

func TestLinearUploadIgnoresChunkedRemotePart(t *testing.T) {
	// Arrange — a chunked attempt left a preallocated, mostly-empty part on
	// the server. The single-connection path must not mistake its full size
	// for a finished upload and publish it.
	c := newTestClient(t)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	const size = 128 << 10
	data := writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "big.bin")
	writeRemotePart(t, dst+ChunkPartSuffix, size, nil)

	// Act — the linear engine uses its own suffix and starts clean
	var sent int64
	if err := c.Upload(context.Background(), src, dst, nil, func(d int64) { sent += d }); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// Assert — real content, not the sparse chunk part
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("linear upload published the chunked part file's zeros")
	}
	if sent != size {
		t.Fatalf("sent %d bytes, want full %d (no resume from a chunk part)", sent, size)
	}
}

func TestUploadChunksFallsBackWhenPreallocUnsupported(t *testing.T) {
	// Arrange — a server that refuses FSETSTAT SIZE, so only the sparse-write
	// probe can size the part
	prev := truncateRemote
	t.Cleanup(func() { truncateRemote = prev })
	truncateRemote = func(*sftp.File, int64) error { return errors.New("fsetstat size refused") }

	c := testClients(t, 2)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	const size = 200 << 10
	data := writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "big.bin")

	// Act
	err := UploadChunks(context.Background(), c, src, dst, size, plan(size, 2), nil, nil)

	// Assert — the sparse write carried it
	if err != nil {
		t.Fatalf("UploadChunks: %v", err)
	}
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("uploaded content mismatch after the FSETSTAT fallback")
	}
}

func TestPrepareRemotePartReportsUnsupported(t *testing.T) {
	// Arrange — FSETSTAT refused and the part cannot be made the right size by
	// writing either (here: it is already larger, so the sparse write leaves
	// the wrong size behind). The engine must say so rather than let workers
	// write into a part no resume guard can trust.
	prev := truncateRemote
	t.Cleanup(func() { truncateRemote = prev })
	truncateRemote = func(*sftp.File, int64) error { return errors.New("fsetstat size refused") }

	c := newTestClient(t)
	dir := t.TempDir()
	part := filepath.Join(dir, "big.bin"+ChunkPartSuffix)
	const size = 64 << 10
	writeRemotePart(t, part, size*2, nil)

	// Act
	err := prepareRemotePart(c, part, size)

	// Assert
	if !errors.Is(err, ErrChunkPreallocUnsupported) {
		t.Fatalf("err = %v, want ErrChunkPreallocUnsupported", err)
	}
}

func TestUploadChunksSurvivesMissingRemoteFsync(t *testing.T) {
	// Arrange — the in-process server advertises no fsync@openssh.com, which
	// is exactly the case a copied Sync-then-checkpoint commit would break on.
	c := testClients(t, 2)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	const size = 300 << 10
	data := writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "big.bin")

	probe, err := c[0].sftp.OpenFile(filepath.Join(dstDir, "probe.bin"), os.O_WRONLY|os.O_CREATE)
	if err != nil {
		t.Fatal(err)
	}
	syncErr := probe.Sync()
	probe.Close()
	if syncErr == nil {
		t.Skip("test server now supports fsync — the capability probe is untested here")
	}

	// Act
	err = UploadChunks(context.Background(), c, src, dst, size, plan(size, 3), nil,
		func(int, int64) {})

	// Assert — a full, correct upload without one fsync
	if err != nil {
		t.Fatalf("UploadChunks: %v", err)
	}
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("uploaded content mismatch")
	}
	if strings.Contains(strings.ToLower(syncErr.Error()), "fsync") == false {
		t.Fatalf("probe error %v does not look like a missing-fsync refusal", syncErr)
	}
}
