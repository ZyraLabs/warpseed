package queue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupProducesAReadableCopy(t *testing.T) {
	// Arrange — a store with real content to prove the copy carries data
	s := openTestStore(t)
	site := seedSite(t, s)
	s.SetSetting("ui.theme", "nightshift")
	s.AddBookmark(site, "/downloads", "seedbox downloads")

	// Act
	dest, err := s.Backup(time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// Assert — the snapshot opens on its own and holds the same rows
	if !strings.Contains(filepath.Base(dest), "2026-08-03-093000") {
		t.Fatalf("backup name lacks its timestamp: %s", dest)
	}
	copyStore, err := Open(dest)
	if err != nil {
		t.Fatalf("reopen backup: %v", err)
	}
	defer copyStore.Close()

	if got := copyStore.Setting("ui.theme", ""); got != "nightshift" {
		t.Fatalf("settings lost in backup: %q", got)
	}
	marks, err := copyStore.Bookmarks(site)
	if err != nil || len(marks) != 1 || marks[0].Label != "seedbox downloads" {
		t.Fatalf("bookmarks lost in backup: %+v (%v)", marks, err)
	}
}

func TestBackupRefusesToOverwrite(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	stamp := time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC)
	if _, err := s.Backup(stamp); err != nil {
		t.Fatal(err)
	}

	// Act — same second, same name
	_, err := s.Backup(stamp)

	// Assert
	if err == nil {
		t.Fatal("a second backup silently overwrote the first")
	}
}

func TestBackupsIgnoresNonDatabases(t *testing.T) {
	// Arrange — the debris a failed or killed VACUUM INTO leaves: a
	// truncated .db, a zero-length .db, and journal/wal sidecars. Offering
	// any of these as "newest backup" invites restoring junk over the
	// live database.
	s := openTestStore(t)
	real, err := s.Backup(time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(real)
	junk := map[string][]byte{
		BackupPrefix + "2026-08-09-120000.db":         []byte("not a database"),
		BackupPrefix + "2026-08-09-120001.db":         {},
		BackupPrefix + "2026-08-09-120002.db-journal": []byte("hot journal"),
		BackupPrefix + "2026-08-09-120003.db-wal":     []byte("wal"),
	}
	for name, content := range junk {
		if werr := os.WriteFile(filepath.Join(dir, name), content, 0o644); werr != nil {
			t.Fatal(werr)
		}
	}

	// Act
	list, err := s.Backups()
	if err != nil {
		t.Fatal(err)
	}

	// Assert — only the genuine snapshot is offered
	if len(list) != 1 || list[0] != filepath.Base(real) {
		t.Fatalf("listed junk as backups: %v", list)
	}
}

func TestBackupLeavesNothingBehindWhenItFails(t *testing.T) {
	// Arrange — a destination that cannot be written, standing in for a
	// full disk mid-VACUUM.
	s := openTestStore(t)
	path, err := s.Path()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if cerr := os.Chmod(dir, 0o555); cerr != nil {
		t.Skipf("cannot make the folder read-only: %v", cerr)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	// Act
	_, err = s.Backup(time.Date(2026, 8, 5, 11, 0, 0, 0, time.UTC))

	// Assert — it failed, and it swept up after itself
	if err == nil {
		t.Skip("filesystem allowed the write despite read-only folder")
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), BackupPrefix) {
			t.Fatalf("failed backup left %q behind", e.Name())
		}
	}
}

func TestBackupsListsNewestFirst(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	s.Backup(time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC))
	s.Backup(time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC))

	// Act
	list, err := s.Backups()
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	if len(list) != 2 {
		t.Fatalf("found %d backups, want 2: %v", len(list), list)
	}
	if !strings.Contains(list[0], "2026-08-03") {
		t.Fatalf("newest backup is not first: %v", list)
	}
}
