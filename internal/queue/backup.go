package queue

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BackupPrefix names snapshots so they are obvious in the folder and easy
// to distinguish from the live database.
const BackupPrefix = "warpseed-backup-"

// Backup writes a consistent snapshot of the database beside it and returns
// the path. VACUUM INTO copies a live database safely — no need to close the
// app or stop transfers — and compacts it on the way out.
func (s *Store) Backup(stamp time.Time) (string, error) {
	dbPath, err := s.Path()
	if err != nil {
		return "", err
	}
	if dbPath == "" {
		return "", fmt.Errorf("database has no file on disk")
	}

	name := BackupPrefix + stamp.Format("2006-01-02-150405") + ".db"
	dest := filepath.Join(filepath.Dir(dbPath), name)
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("a backup from this second already exists: %s", name)
	}

	// VACUUM INTO refuses to overwrite, so the Stat above is belt and braces.
	// The path is single-quoted SQL; escape quotes rather than interpolate raw.
	quoted := "'" + strings.ReplaceAll(dest, "'", "''") + "'"
	if _, err := s.db.Exec("VACUUM INTO " + quoted); err != nil {
		// A failed VACUUM INTO (disk full, killed mid-write) leaves its
		// partial output and a hot journal behind. Neither is a backup, and
		// leaving them is how a user ends up restoring a truncated file over
		// a working database.
		for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
			os.Remove(dest + suffix)
		}
		return "", fmt.Errorf("write backup: %w", err)
	}
	return dest, nil
}

// isDatabase reports whether a file really is a SQLite database. A crashed
// backup can leave a zero-length or header-less file, and PRAGMA quick_check
// answers "ok" for a zero-length file — the magic header is what settles it.
func isDatabase(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [16]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return false
	}
	return string(hdr[:]) == "SQLite format 3\x00"
}

// Path reports the database file backing this store. PRAGMA database_list
// gives the file this connection actually has open, so callers never work
// from a stale assumption about the location.
func (s *Store) Path() (string, error) {
	var p string
	if err := s.db.QueryRow(
		`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&p); err != nil {
		return "", fmt.Errorf("locate database: %w", err)
	}
	return p, nil
}

// Backups lists existing snapshots, newest first.
func (s *Store) Backups() ([]string, error) {
	p, err := s.Path()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		return nil, fmt.Errorf("read settings folder: %w", err)
	}
	dir := filepath.Dir(p)
	out := make([]string, 0, 4)
	for _, e := range entries {
		name := e.Name()
		// The suffix test keeps -journal/-wal/-shm sidecars from ever being
		// named as "newest"; the header test rejects a truncated .db.
		if e.IsDir() || !strings.HasPrefix(name, BackupPrefix) || !strings.HasSuffix(name, ".db") {
			continue
		}
		if !isDatabase(filepath.Join(dir, name)) {
			continue
		}
		out = append(out, name)
	}
	// Names are timestamped, so reverse lexical order is newest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
