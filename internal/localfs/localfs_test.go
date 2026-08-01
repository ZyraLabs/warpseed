package localfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListSortsDirsFirstThenCaseInsensitive(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	for _, d := range []string{"zeta", "Alpha"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"beta.txt", "ALPHA.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Act
	l, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Assert
	got := make([]string, len(l.Entries))
	for i, e := range l.Entries {
		got[i] = e.Name
	}
	want := []string{"Alpha", "zeta", "ALPHA.txt", "beta.txt"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order mismatch at %d: got %v, want %v", i, got, want)
		}
	}
}

func TestListDirectorySizeIsMinusOne(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)

	// Act
	l, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	if len(l.Entries) != 1 || !l.Entries[0].IsDir || l.Entries[0].Size != -1 {
		t.Fatalf("directory entry not normalized: %+v", l.Entries)
	}
}

func TestListMissingDirectoryErrors(t *testing.T) {
	// Act
	_, err := List(filepath.Join(t.TempDir(), "nope"))

	// Assert
	if err == nil {
		t.Fatal("expected error for missing directory")
	}
}

func TestListParentAtRootIsEmpty(t *testing.T) {
	// Act
	roots := Roots()
	if len(roots) == 0 {
		t.Fatal("no roots enumerated")
	}
	l, err := List(roots[0].Path)
	if err != nil {
		t.Skipf("root unreadable in test env: %v", err)
	}

	// Assert
	if l.Parent != "" {
		t.Fatalf("parent at root = %q, want empty", l.Parent)
	}
}
