package sftpfast

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveDeletesSymlinkNotItsTarget(t *testing.T) {
	// Arrange — the seedbox layout that makes this dangerous: a link like
	// ~/torrents/completed pointing at a media library elsewhere.
	c := newTestClient(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "media")
	os.Mkdir(target, 0o755)
	os.WriteFile(filepath.Join(target, "library.bin"), []byte("irreplaceable"), 0o644)
	link := filepath.Join(dir, "completed")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Act
	err := c.Remove(context.Background(), link)

	// Assert — only the link is gone
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, lerr := os.Lstat(link); !os.IsNotExist(lerr) {
		t.Fatal("symlink survived")
	}
	if _, serr := os.Stat(filepath.Join(target, "library.bin")); serr != nil {
		t.Fatal("link target's contents were destroyed — the link was followed")
	}
}

func TestRemoveTreeSkipsSymlinkedChildren(t *testing.T) {
	// Arrange — a real directory containing a link out to protected data
	c := newTestClient(t)
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	os.Mkdir(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "keep.bin"), []byte("keep"), 0o644)

	victim := filepath.Join(dir, "victim")
	os.Mkdir(victim, 0o755)
	os.WriteFile(filepath.Join(victim, "gone.bin"), []byte("x"), 0o644)
	if err := os.Symlink(outside, filepath.Join(victim, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Act
	err := c.Remove(context.Background(), victim)

	// Assert — the tree goes, what it linked to does not
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, serr := os.Stat(victim); !os.IsNotExist(serr) {
		t.Fatal("directory survived")
	}
	if _, serr := os.Stat(filepath.Join(outside, "keep.bin")); serr != nil {
		t.Fatal("recursive delete followed a child symlink out of the tree")
	}
}

func TestRemoveRefusesRoot(t *testing.T) {
	// Arrange
	c := newTestClient(t)

	// Act & Assert
	for _, p := range []string{"/", ".", ""} {
		if err := c.Remove(context.Background(), p); err == nil {
			t.Fatalf("deleting %q was allowed", p)
		}
	}
}

func TestRenameEntryRejectsPathEscapes(t *testing.T) {
	// Arrange
	c := newTestClient(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "f.bin")
	os.WriteFile(file, []byte("x"), 0o644)

	// Act & Assert
	for _, bad := range []string{"../escaped", "sub/child", "..", ""} {
		if err := c.RenameEntry(file, bad); err == nil {
			t.Fatalf("rename to %q was allowed", bad)
		}
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("original was disturbed by rejected renames")
	}
}
