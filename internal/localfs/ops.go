package localfs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validName rejects anything that is not a single, safe path element — the
// UI passes user-typed names straight through, so "../etc" must never
// escape the directory being operated on.
func validName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid name %q", name)
	}
	if strings.ContainsAny(name, `/\`) || !filepath.IsLocal(name) {
		return fmt.Errorf("name %q may not contain path separators", name)
	}
	return nil
}

// Delete removes files and directories (recursively for directories).
// It reports how many entries were removed and the first failure, so the UI
// can refresh even on a partial success.
func Delete(paths []string) (int, error) {
	removed := 0
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return removed, fmt.Errorf("resolve %q: %w", p, err)
		}
		// Refuse to delete a filesystem root outright.
		if parent := filepath.Dir(abs); parent == abs {
			return removed, fmt.Errorf("refusing to delete the root %q", abs)
		}
		// RemoveAll reports success for a path that was never there, which
		// would let the UI claim it deleted files it never touched. Verify
		// the target exists so a wrong path surfaces as an error.
		if _, err := os.Lstat(abs); err != nil {
			return removed, fmt.Errorf("delete %q: %w", filepath.Base(abs), err)
		}
		if err := os.RemoveAll(abs); err != nil {
			return removed, fmt.Errorf("delete %q: %w", filepath.Base(abs), err)
		}
		removed++
	}
	return removed, nil
}

// Rename renames one entry within its directory.
func Rename(path, newName string) error {
	if err := validName(newName); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", path, err)
	}
	target := filepath.Join(filepath.Dir(abs), newName)
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("%q already exists", newName)
	}
	if err := os.Rename(abs, target); err != nil {
		return fmt.Errorf("rename to %q: %w", newName, err)
	}
	return nil
}

// Move relocates entries into destDir, keeping their base names.
func Move(paths []string, destDir string) (int, error) {
	moved := 0
	for _, p := range paths {
		src, err := filepath.Abs(p)
		if err != nil {
			return moved, fmt.Errorf("resolve %q: %w", p, err)
		}
		dst := filepath.Join(destDir, filepath.Base(src))
		if src == dst {
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			return moved, fmt.Errorf("%q already exists in the destination", filepath.Base(src))
		}
		if err := os.Rename(src, dst); err != nil {
			return moved, fmt.Errorf("move %q: %w", filepath.Base(src), err)
		}
		moved++
	}
	return moved, nil
}

// Mkdir creates a directory inside parent.
func Mkdir(parent, name string) error {
	if err := validName(name); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(parent, name), 0o755); err != nil {
		return fmt.Errorf("create folder %q: %w", name, err)
	}
	return nil
}
