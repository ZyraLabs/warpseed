package sftpfast

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// plan splits size into n contiguous ranges, mirroring queue.PlanChunks
// without importing it (engine stays independent of the queue package).
func plan(size int64, n int) []ChunkRange {
	each := size / int64(n)
	out := make([]ChunkRange, 0, n)
	for i := 0; i < n; i++ {
		off := int64(i) * each
		length := each
		if i == n-1 {
			length = size - off
		}
		out = append(out, ChunkRange{Idx: i, Offset: off, Length: length})
	}
	return out
}

func testClients(t *testing.T, n int) []*Client {
	t.Helper()
	cs := make([]*Client, 0, n)
	for i := 0; i < n; i++ {
		cs = append(cs, newTestClient(t))
	}
	return cs
}

func TestDownloadChunksAssemblesExactFile(t *testing.T) {
	// Arrange — 4 connections over a file with an uneven tail
	c := testClients(t, 4)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	data := writeRandomFile(t, src, 1<<20+777)
	dst := filepath.Join(dstDir, "big.bin")

	var mu sync.Mutex
	perChunk := map[int]int64{}

	// Act
	err := DownloadChunks(context.Background(), c, src, dst, int64(len(data)),
		plan(int64(len(data)), 4),
		func(idx int, delta int64) {
			mu.Lock()
			perChunk[idx] += delta
			mu.Unlock()
		}, nil)

	// Assert — byte-identical, every chunk contributed, no part left behind
	if err != nil {
		t.Fatalf("DownloadChunks: %v", err)
	}
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("assembled content mismatch")
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
		t.Fatal("part file left behind")
	}
}

func TestDownloadChunksResumesPerChunk(t *testing.T) {
	// Arrange — a part file where chunk 0 is half done and chunk 2 complete
	c := testClients(t, 3)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	const size = 300 << 10
	data := writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "big.bin")

	ranges := plan(size, 3)
	part := dst + ChunkPartSuffix
	pf, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := pf.Truncate(size); err != nil {
		t.Fatal(err)
	}
	half := ranges[0].Length / 2
	pf.WriteAt(data[:half], 0)
	ranges[0].Done = half
	c2 := ranges[2]
	pf.WriteAt(data[c2.Offset:c2.Offset+c2.Length], c2.Offset)
	ranges[2].Done = c2.Length
	pf.Close()

	var mu sync.Mutex
	var fetched int64

	// Act
	err = DownloadChunks(context.Background(), c, src, dst, size, ranges,
		func(_ int, delta int64) {
			mu.Lock()
			fetched += delta
			mu.Unlock()
		}, nil)

	// Assert — only the missing bytes moved and the file is intact
	if err != nil {
		t.Fatalf("DownloadChunks: %v", err)
	}
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("resumed assembly mismatch")
	}
	want := int64(size) - half - c2.Length
	if fetched != want {
		t.Fatalf("fetched %d bytes, want %d (per-chunk resume)", fetched, want)
	}
}

func TestDownloadChunksCheckpointsOnCancel(t *testing.T) {
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
	err := DownloadChunks(ctx, c, src, dst, size, plan(size, 2),
		func(int, int64) { cancel() },
		func(idx int, done int64) {
			mu.Lock()
			checkpoints[idx] = done
			mu.Unlock()
		})

	// Assert — errored, destination not published, offsets recorded for resume
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
		t.Fatal("part file discarded — resume impossible")
	}
}

func TestDownloadChunksRefusesResumeWhenPartFileVanished(t *testing.T) {
	// Arrange — checkpoints claim 60% is already on disk, but the partial
	// file was deleted (a user reclaiming space). Trusting the checkpoints
	// would skip those ranges and publish a file full of zeros at full size.
	c := testClients(t, 2)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	const size = 200 << 10
	writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "big.bin")

	ranges := plan(size, 2)
	ranges[0].Done = ranges[0].Length // "complete" — but nothing on disk

	// Act
	err := DownloadChunks(context.Background(), c, src, dst, size, ranges, nil, nil)

	// Assert — refuses rather than assembling holes
	if !errors.Is(err, ErrChunkStateLost) {
		t.Fatalf("err = %v, want ErrChunkStateLost", err)
	}
	if _, serr := os.Stat(dst); !os.IsNotExist(serr) {
		t.Fatal("a file with holes was published")
	}
}

func TestDownloadChunksRefusesResumeWhenPartFileTruncated(t *testing.T) {
	// Arrange — the part file exists but is the wrong size, so its offsets
	// no longer line up with the recorded checkpoints.
	c := testClients(t, 2)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	const size = 200 << 10
	writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "big.bin")
	if err := os.WriteFile(dst+ChunkPartSuffix, make([]byte, size/4), 0o644); err != nil {
		t.Fatal(err)
	}

	ranges := plan(size, 2)
	ranges[1].Done = ranges[1].Length

	// Act
	err := DownloadChunks(context.Background(), c, src, dst, size, ranges, nil, nil)

	// Assert
	if !errors.Is(err, ErrChunkStateLost) {
		t.Fatalf("err = %v, want ErrChunkStateLost", err)
	}
}

func TestLinearDownloadIgnoresChunkedPartFile(t *testing.T) {
	// Arrange — a chunked attempt left a preallocated, mostly-empty part
	// file. The single-connection path must not mistake its full size for a
	// finished download and publish it.
	c := newTestClient(t)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	const size = 128 << 10
	data := writeRandomFile(t, src, size)
	dst := filepath.Join(dstDir, "big.bin")

	sparse, err := os.OpenFile(dst+ChunkPartSuffix, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	sparse.Truncate(size) // full size, no content
	sparse.Close()

	// Act — the linear engine uses its own suffix and starts clean
	if err := c.Download(context.Background(), src, dst, nil, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}

	// Assert — real content, not the zero-filled chunk part
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("linear download published the chunked part file's zeros")
	}
}

func TestDownloadChunksRejectsShortAssembly(t *testing.T) {
	// Arrange — claim a larger size than the source actually has
	c := testClients(t, 2)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "f.bin")
	data := writeRandomFile(t, src, 8<<10)
	dst := filepath.Join(dstDir, "f.bin")
	claimed := int64(len(data)) * 2

	// Act
	err := DownloadChunks(context.Background(), c, src, dst, claimed, plan(claimed, 2), nil, nil)

	// Assert — a truncated read must not be published as a complete file
	if err == nil {
		t.Fatal("expected failure when the remote is shorter than planned")
	}
	if _, serr := os.Stat(dst); !os.IsNotExist(serr) {
		t.Fatal("short file was published")
	}
}
