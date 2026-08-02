package localfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteRemovesFilesAndTrees(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	file := filepath.Join(dir, "f.bin")
	os.WriteFile(file, []byte("x"), 0o644)
	tree := filepath.Join(dir, "tree")
	os.MkdirAll(filepath.Join(tree, "nested"), 0o755)
	os.WriteFile(filepath.Join(tree, "nested", "deep.bin"), []byte("y"), 0o644)

	// Act
	n, err := Delete([]string{file, tree})

	// Assert
	if err != nil || n != 2 {
		t.Fatalf("Delete = %d, %v; want 2, nil", n, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("file survived")
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatal("tree survived")
	}
}

func TestDeleteReportsMissingPathsInsteadOfClaimingSuccess(t *testing.T) {
	// Arrange — os.RemoveAll returns nil for a path that was never there,
	// which would let the UI announce deletions that never happened.
	dir := t.TempDir()

	// Act
	n, err := Delete([]string{filepath.Join(dir, "never-existed")})

	// Assert
	if err == nil {
		t.Fatal("deleting a missing path reported success")
	}
	if n != 0 {
		t.Fatalf("counted %d removals for a missing path", n)
	}
}

func TestDeleteRemovesSymlinkNotItsTarget(t *testing.T) {
	// Arrange — a link pointing at a directory of real files
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	os.Mkdir(target, 0o755)
	os.WriteFile(filepath.Join(target, "keep.bin"), []byte("precious"), 0o644)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Act
	n, err := Delete([]string{link})

	// Assert — the link goes, the target and its contents stay
	if err != nil || n != 1 {
		t.Fatalf("Delete = %d, %v; want 1, nil", n, err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("symlink survived")
	}
	if _, err := os.Stat(filepath.Join(target, "keep.bin")); err != nil {
		t.Fatal("link target's contents were destroyed")
	}
}

func TestRenameRejectsPathEscapes(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	file := filepath.Join(dir, "f.bin")
	os.WriteFile(file, []byte("x"), 0o644)

	// Act & Assert — a name is a name, never a path
	for _, bad := range []string{"../escaped", "sub/child", `back\slash`, "..", ""} {
		if err := Rename(file, bad); err == nil {
			t.Fatalf("rename to %q was allowed", bad)
		}
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("original was disturbed by rejected renames")
	}
}

func TestRenameSucceedsAndRefusesCollision(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	os.WriteFile(a, []byte("x"), 0o644)
	os.WriteFile(b, []byte("y"), 0o644)

	// Act & Assert
	if err := Rename(a, "renamed.bin"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "renamed.bin")); err != nil {
		t.Fatal("renamed file missing")
	}
	if err := Rename(b, "renamed.bin"); err == nil {
		t.Fatal("overwriting an existing name was allowed")
	}
}

func TestMkdirValidatesName(t *testing.T) {
	// Arrange
	dir := t.TempDir()

	// Act & Assert
	if err := Mkdir(dir, "new folder"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if st, err := os.Stat(filepath.Join(dir, "new folder")); err != nil || !st.IsDir() {
		t.Fatal("folder not created")
	}
	if err := Mkdir(dir, "../escape"); err == nil {
		t.Fatal("escaping name was allowed")
	}
}

func TestMoveRelocatesAndRefusesCollision(t *testing.T) {
	// Arrange
	src, dst := t.TempDir(), t.TempDir()
	file := filepath.Join(src, "f.bin")
	os.WriteFile(file, []byte("x"), 0o644)

	// Act
	n, err := Move([]string{file}, dst)

	// Assert
	if err != nil || n != 1 {
		t.Fatalf("Move = %d, %v; want 1, nil", n, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "f.bin")); err != nil {
		t.Fatal("file not at destination")
	}
	os.WriteFile(file, []byte("x"), 0o644)
	if _, err := Move([]string{file}, dst); err == nil {
		t.Fatal("overwriting at destination was allowed")
	}
}
