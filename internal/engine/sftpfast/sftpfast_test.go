package sftpfast

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// pipeRWC glues one read end and one write end into the ReadWriteCloser the
// sftp server wants. os.Pipe (kernel-buffered) avoids net.Pipe lockstep.
type pipeRWC struct {
	io.Reader
	io.WriteCloser
}

func (p pipeRWC) Close() error { return p.WriteCloser.Close() }

// newTestClient runs a real pkg/sftp server over pipes — the full SFTP
// protocol without SSH, serving the OS filesystem (tests use TempDir paths).
func newTestClient(t *testing.T) *Client {
	t.Helper()
	c2sR, c2sW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s2cR, s2cW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := sftp.NewServer(pipeRWC{Reader: c2sR, WriteCloser: s2cW})
	if err != nil {
		t.Fatalf("sftp server: %v", err)
	}
	go srv.Serve()

	sc, err := sftp.NewClientPipe(s2cR, c2sW,
		sftp.UseConcurrentReads(true),
		sftp.MaxConcurrentRequestsPerFile(16),
	)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	t.Cleanup(func() {
		// Close the raw pipe ends rather than sc.Close(): over bare pipes the
		// sftp client's Close waits for a transport EOF that only closing the
		// descriptors delivers.
		c2sW.Close() // server's read loop gets EOF → Serve returns
		srv.Close()
		s2cW.Close() // client's recv loop gets EOF
		c2sR.Close()
		s2cR.Close()
	})
	return newFromSFTP(sc)
}

func writeRandomFile(t *testing.T, path string, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestListSortsAndNormalizes(t *testing.T) {
	// Arrange
	c := newTestClient(t)
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "zdir"), 0o755)
	writeRandomFile(t, filepath.Join(dir, "afile.bin"), 64)

	// Act
	l, err := c.List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Assert
	if len(l.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(l.Entries))
	}
	if !l.Entries[0].IsDir || l.Entries[0].Name != "zdir" || l.Entries[0].Size != -1 {
		t.Fatalf("dir-first ordering broken: %+v", l.Entries)
	}
	if l.Entries[1].Name != "afile.bin" || l.Entries[1].Size != 64 {
		t.Fatalf("file entry wrong: %+v", l.Entries[1])
	}
}

func TestDownloadFull(t *testing.T) {
	// Arrange
	c := newTestClient(t)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "payload.bin")
	data := writeRandomFile(t, src, 1<<20) // 1 MiB across many packets
	dst := filepath.Join(dstDir, "payload.bin")

	var got int64
	// Act
	if err := c.Download(context.Background(), src, dst, nil, func(d int64) { got += d }); err != nil {
		t.Fatalf("Download: %v", err)
	}

	// Assert
	out, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(out) != sha256.Sum256(data) {
		t.Fatal("content mismatch")
	}
	if got != int64(len(data)) {
		t.Fatalf("progress reported %d bytes, want %d", got, len(data))
	}
	if _, err := os.Stat(dst + PartSuffix); !os.IsNotExist(err) {
		t.Fatal("part file left behind after finalize")
	}
}

func TestDownloadResumesFromPartFile(t *testing.T) {
	// Arrange — a partial .wspart already on disk
	c := newTestClient(t)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	data := writeRandomFile(t, src, 512<<10)
	dst := filepath.Join(dstDir, "big.bin")
	const have = 100 << 10
	if err := os.WriteFile(dst+PartSuffix, data[:have], 0o644); err != nil {
		t.Fatal(err)
	}

	var transferred int64
	// Act
	if err := c.Download(context.Background(), src, dst, nil, func(d int64) { transferred += d }); err != nil {
		t.Fatalf("Download: %v", err)
	}

	// Assert — only the missing tail moved, and the file is byte-identical
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("resumed content mismatch")
	}
	want := int64(len(data) - have)
	if transferred != want {
		t.Fatalf("transferred %d bytes, want %d (resume from offset)", transferred, want)
	}
}

func TestDownloadRestartsWhenPartLargerThanRemote(t *testing.T) {
	// Arrange — stale part bigger than the (changed) remote file
	c := newTestClient(t)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "f.bin")
	data := writeRandomFile(t, src, 10<<10)
	dst := filepath.Join(dstDir, "f.bin")
	if err := os.WriteFile(dst+PartSuffix, make([]byte, 20<<10), 0o644); err != nil {
		t.Fatal(err)
	}

	// Act
	if err := c.Download(context.Background(), src, dst, nil, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}

	// Assert
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("restart-from-zero content mismatch")
	}
}

func TestDownloadCancellation(t *testing.T) {
	// Arrange
	c := newTestClient(t)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "slow.bin")
	writeRandomFile(t, src, 4<<20)
	dst := filepath.Join(dstDir, "slow.bin")

	ctx, cancel := context.WithCancel(context.Background())
	// Act — cancel on the first bytes; the ctx-aware writer must refuse the
	// next chunk regardless of how much the read pipeline prefetched.
	err := c.Download(ctx, src, dst, nil, func(int64) { cancel() })

	// Assert
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatal("destination must not exist after cancelled transfer")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUploadFull(t *testing.T) {
	// Arrange
	c := newTestClient(t)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "up.bin")
	data := writeRandomFile(t, src, 768<<10)
	dst := filepath.Join(dstDir, "nested", "up.bin") // exercises MkdirAll

	var got int64
	// Act
	if err := c.Upload(context.Background(), src, dst, nil, func(d int64) { got += d }); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// Assert
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("uploaded content mismatch")
	}
	if got != int64(len(data)) {
		t.Fatalf("progress reported %d bytes, want %d", got, len(data))
	}
	if _, err := os.Stat(dst + PartSuffix); !os.IsNotExist(err) {
		t.Fatal("remote part file left behind after finalize")
	}
}

func TestUploadResumesFromRemotePart(t *testing.T) {
	// Arrange — remote .wspart already holds the first chunk
	c := newTestClient(t)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	data := writeRandomFile(t, src, 400<<10)
	dst := filepath.Join(dstDir, "big.bin")
	const have = 150 << 10
	if err := os.WriteFile(dst+PartSuffix, data[:have], 0o644); err != nil {
		t.Fatal(err)
	}

	var sent int64
	// Act
	if err := c.Upload(context.Background(), src, dst, nil, func(d int64) { sent += d }); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// Assert — only the tail moved; final file byte-identical
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("resumed upload content mismatch")
	}
	if want := int64(len(data) - have); sent != want {
		t.Fatalf("sent %d bytes, want %d (resume from remote offset)", sent, want)
	}
}

func TestUploadOverwritesExistingDestination(t *testing.T) {
	// Arrange
	c := newTestClient(t)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "f.bin")
	data := writeRandomFile(t, src, 8<<10)
	dst := filepath.Join(dstDir, "f.bin")
	if err := os.WriteFile(dst, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Act
	if err := c.Upload(context.Background(), src, dst, nil, nil); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// Assert
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("destination not replaced")
	}
}

func TestUploadIgnoresUnprovenRemotePart(t *testing.T) {
	// Arrange — a remote part older than the source: it cannot be ours, so
	// its bytes must never be renamed into place as a completed upload.
	c := newTestClient(t)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "f.bin")
	dst := filepath.Join(dstDir, "f.bin")
	planted := bytes.Repeat([]byte{0xAA}, 32<<10)
	if err := os.WriteFile(dst+PartSuffix, planted, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(dst+PartSuffix, old, old); err != nil {
		t.Fatal(err)
	}
	data := writeRandomFile(t, src, 64<<10) // written now → newer than part

	var sent int64
	// Act
	if err := c.Upload(context.Background(), src, dst, nil, func(d int64) { sent += d }); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// Assert — full re-send, and the planted bytes are gone
	if !bytes.Equal(mustRead(t, dst), data) {
		t.Fatal("unproven part was trusted — uploaded content mismatch")
	}
	if sent != int64(len(data)) {
		t.Fatalf("sent %d bytes, want full %d (no resume from unproven part)", sent, len(data))
	}
}

func TestUploadRefusesDirectoryDestination(t *testing.T) {
	// Arrange — destination name is an existing directory
	c := newTestClient(t)
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "f.bin")
	writeRandomFile(t, src, 1024)
	dst := filepath.Join(dstDir, "collide")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	// Act
	err := c.Upload(context.Background(), src, dst, nil, nil)

	// Assert — refused, and the directory survives
	if err == nil {
		t.Fatal("expected refusal when destination is a directory")
	}
	if st, serr := os.Stat(dst); serr != nil || !st.IsDir() {
		t.Fatal("destination directory was destroyed")
	}
}

func TestWalkFilesEnumeratesTree(t *testing.T) {
	// Arrange — nested tree with 3 files across 2 levels
	c := newTestClient(t)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	writeRandomFile(t, filepath.Join(root, "top.bin"), 10)
	writeRandomFile(t, filepath.Join(root, "a", "mid.bin"), 20)
	writeRandomFile(t, filepath.Join(root, "a", "b", "deep.bin"), 30)

	// Act
	var paths []string
	var total int64
	err := c.WalkFiles(context.Background(), root, func(p string, size int64) error {
		paths = append(paths, p)
		total += size
		return nil
	})

	// Assert
	if err != nil {
		t.Fatalf("WalkFiles: %v", err)
	}
	if len(paths) != 3 || total != 60 {
		t.Fatalf("walk found %d files totalling %d, want 3 / 60: %v", len(paths), total, paths)
	}
}
