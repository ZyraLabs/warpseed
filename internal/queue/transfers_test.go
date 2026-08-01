package queue

import "testing"

func seedSite(t *testing.T, s *Store) int64 {
	t.Helper()
	id, err := s.SaveSite(Site{Name: "t", Protocol: "sftp", Host: "example.test", Username: "u"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEnqueueAndPendingOrder(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	site := seedSite(t, s)
	low, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/a", Dst: "/l/a"})
	high, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/b", Dst: "/l/b", Priority: 5})

	// Act
	pending, err := s.PendingTransfers("2026-08-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}

	// Assert — higher priority first
	if len(pending) != 2 || pending[0].ID != high || pending[1].ID != low {
		t.Fatalf("order wrong: %+v", pending)
	}
}

func TestPendingRespectsRetryDeadline(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	site := seedSite(t, s)
	id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/a", Dst: "/l/a"})
	if err := s.ScheduleRetry(id, "2026-08-01T12:00:00Z", nil); err != nil {
		t.Fatal(err)
	}

	// Act & Assert — before deadline: hidden; after: visible with attempt=1
	before, _ := s.PendingTransfers("2026-08-01T11:00:00Z")
	if len(before) != 0 {
		t.Fatalf("retry surfaced early: %+v", before)
	}
	after, _ := s.PendingTransfers("2026-08-01T13:00:00Z")
	if len(after) != 1 || after[0].Attempt != 1 {
		t.Fatalf("retry not surfaced: %+v", after)
	}
}

func TestStateTransitionsAndClear(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	site := seedSite(t, s)
	a, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/a", Dst: "/l/a"})
	b, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/b", Dst: "/l/b"})

	// Act
	if err := s.SetTransferState(a, "completed", nil); err != nil {
		t.Fatal(err)
	}
	msg := "boom"
	if err := s.SetTransferState(b, "failed", &msg); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClearFinished()
	if err != nil {
		t.Fatal(err)
	}

	// Assert — completed cleared, failed retained with its error
	if n != 1 {
		t.Fatalf("cleared %d, want 1", n)
	}
	rest, _ := s.Transfers(10)
	if len(rest) != 1 || rest[0].ID != b || rest[0].Error == nil || *rest[0].Error != "boom" {
		t.Fatalf("failed row wrong: %+v", rest)
	}
}

func TestProgressPersists(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	site := seedSite(t, s)
	id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/a", Dst: "/l/a", Size: 100})

	// Act
	if err := s.UpdateTransferProgress(id, 42); err != nil {
		t.Fatal(err)
	}

	// Assert
	got, _ := s.TransferByID(id)
	if got.BytesDone != 42 {
		t.Fatalf("bytesDone = %d, want 42", got.BytesDone)
	}
}
