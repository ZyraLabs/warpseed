package localfs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is one row in a file pane. Field names are the wire format the
// frontend consumes via Wails bindings — change only with a frontend change.
type Entry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`    // -1 for directories
	ModTime string `json:"modTime"` // RFC3339 UTC, e.g. 2026-08-01T12:00:00Z
	Mode    string `json:"mode"`    // e.g. "-rw-r--r--"
}

// Listing is the result of reading one directory.
type Listing struct {
	Path    string  `json:"path"`    // cleaned absolute path actually listed
	Parent  string  `json:"parent"`  // "" at a filesystem root
	Entries []Entry `json:"entries"` // dirs first, then files, case-insensitive name order
}

// Root is a top-level navigation target (a drive on Windows, "/" elsewhere).
type Root struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

// List reads a directory. Unreadable children are skipped, not fatal —
// a pane must render what it can (mirrors every mature commander).
func List(path string) (Listing, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Listing{}, fmt.Errorf("resolve %q: %w", path, err)
	}
	dirents, err := os.ReadDir(abs)
	if err != nil {
		return Listing{}, fmt.Errorf("read dir %q: %w", abs, err)
	}

	entries := make([]Entry, 0, len(dirents))
	for _, de := range dirents {
		info, err := de.Info()
		if err != nil {
			continue // vanished or unreadable between ReadDir and Stat
		}
		e := Entry{
			Name:    de.Name(),
			IsDir:   de.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
			Mode:    info.Mode().String(),
		}
		if e.IsDir {
			e.Size = -1
		}
		entries = append(entries, e)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	parent := filepath.Dir(abs)
	if parent == abs {
		parent = ""
	}
	return Listing{Path: abs, Parent: parent, Entries: entries}, nil
}

// Home returns the user's home directory (initial pane location).
func Home() (string, error) {
	return os.UserHomeDir()
}
