package queue

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAppliesAllMigrations(t *testing.T) {
	// Arrange & Act
	s := openTestStore(t)

	// Assert
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != len(migrations) {
		t.Fatalf("schema version = %d, want %d", v, len(migrations))
	}
	for _, table := range []string{"sites", "transfers", "chunks", "hostkeys", "settings"} {
		var n int
		err := s.DB().QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&n)
		if err != nil || n != 1 {
			t.Errorf("table %q missing (n=%d, err=%v)", table, n, err)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	// Arrange
	path := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	// Act — reopening must not re-apply migrations
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	// Assert
	v, _ := s2.SchemaVersion()
	if v != len(migrations) {
		t.Fatalf("schema version after reopen = %d, want %d", v, len(migrations))
	}
}

func TestRecoverInterruptedDemotesTransientStates(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	now := "2026-08-01T00:00:00.000Z"
	if _, err := s.DB().Exec(
		`INSERT INTO sites(id,name,protocol,host,created_at,updated_at) VALUES (1,'t','sftp','example.test',?,?)`,
		now, now); err != nil {
		t.Fatalf("insert site: %v", err)
	}
	states := []string{"pending", "dispatched", "active", "completed", "paused"}
	for i, st := range states {
		if _, err := s.DB().Exec(
			`INSERT INTO transfers(id,site_id,engine,direction,src,dst,state,created_at,updated_at)
			 VALUES (?,1,'sftpfast','download','/r/a','/l/a',?,?,?)`,
			i+1, st, now, now); err != nil {
			t.Fatalf("insert transfer %s: %v", st, err)
		}
	}

	// Act
	n, err := s.RecoverInterrupted()
	if err != nil {
		t.Fatalf("RecoverInterrupted: %v", err)
	}

	// Assert — only dispatched + active are demoted
	if n != 2 {
		t.Fatalf("recovered %d rows, want 2", n)
	}
	var pending int
	s.DB().QueryRow(`SELECT COUNT(*) FROM transfers WHERE state='pending'`).Scan(&pending)
	if pending != 3 { // original pending + 2 demoted
		t.Fatalf("pending rows = %d, want 3", pending)
	}
	var completed int
	s.DB().QueryRow(`SELECT COUNT(*) FROM transfers WHERE state IN ('completed','paused')`).Scan(&completed)
	if completed != 2 {
		t.Fatalf("terminal/paused rows disturbed: %d, want 2", completed)
	}
}

// TestRecoverInterruptedResetsTheRetryLadder — quitting the app is not a
// transfer failure, so a row must not come back one hiccup away from giving
// up, wearing an error from a run that is over.
func TestRecoverInterruptedResetsTheRetryLadder(t *testing.T) {
	// Arrange — an active row that had already burned two of its three
	// attempts, with byte progress that recovery must preserve.
	s := openTestStore(t)
	site := seedSite(t, s)
	id, err := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/r/big.mkv", Dst: "/l/big.mkv", Size: 5 << 30})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := s.UpdateTransferProgress(id, 3<<30); err != nil {
		t.Fatalf("progress: %v", err)
	}
	boom := "connection reset"
	spendAttempt(t, s, id, "2026-08-01T00:00:05Z", &boom)
	spendAttempt(t, s, id, "2026-08-01T00:00:10Z", &boom)
	if err := s.SetTransferState(id, "active", nil); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// Act — the app is killed and relaunches.
	if _, err := s.RecoverInterrupted(); err != nil {
		t.Fatalf("RecoverInterrupted: %v", err)
	}

	// Assert
	got, err := s.TransferByID(id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.State != "pending" {
		t.Fatalf("state = %q, want pending", got.State)
	}
	if got.Attempt != 0 {
		t.Fatalf("attempt = %d, want 0: a relaunch must not spend the retry budget", got.Attempt)
	}
	if got.Error != nil {
		t.Fatalf("error = %q, want nil: the failure belonged to the previous run", *got.Error)
	}
	if got.NextRetryAt != nil {
		t.Fatalf("nextRetryAt = %q, want nil: recovery should not sit behind an old backoff", *got.NextRetryAt)
	}
	if got.BytesDone != 3<<30 {
		t.Fatalf("bytesDone = %d, want 3 GiB kept so it resumes rather than restarts", got.BytesDone)
	}
}

func TestInvalidStateRejected(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	now := "2026-08-01T00:00:00.000Z"
	s.DB().Exec(`INSERT INTO sites(id,name,protocol,host,created_at,updated_at) VALUES (1,'t','sftp','example.test',?,?)`, now, now)

	// Act
	_, err := s.DB().Exec(
		`INSERT INTO transfers(site_id,engine,direction,src,dst,state,created_at,updated_at)
		 VALUES (1,'sftpfast','download','/r/a','/l/a','teleporting',?,?)`, now, now)

	// Assert
	if err == nil {
		t.Fatal("CHECK constraint should reject unknown state")
	}
}
