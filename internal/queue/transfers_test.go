package queue

import (
	"strings"
	"testing"
)

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

// Regression: with more rows queued than the finished-row window, the
// active transfers (the oldest ids, claimed first) vanished from the UI list
// and every view showed nothing in flight.
func TestTransfersKeepsLiveRowsBeyondWindow(t *testing.T) {
	// Arrange — two old rows in flight, one paused, then a burst of newer
	// pending and finished rows far larger than the window.
	s := openTestStore(t)
	site := seedSite(t, s)
	active, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/a", Dst: "/l/a"})
	paused, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/b", Dst: "/l/b"})
	if err := s.SetTransferState(active, "active", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTransferState(paused, "paused", nil); err != nil {
		t.Fatal(err)
	}
	var pending, finished []int64
	for i := 0; i < 30; i++ {
		id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/p", Dst: "/l/p"})
		pending = append(pending, id)
	}
	for i := 0; i < 10; i++ {
		id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/d", Dst: "/l/d"})
		if err := s.SetTransferState(id, "completed", nil); err != nil {
			t.Fatal(err)
		}
		finished = append(finished, id)
	}

	// Act
	got, err := s.Transfers(3)
	if err != nil {
		t.Fatal(err)
	}

	// Assert — every unfinished row present, only the 3 newest finished,
	// whole list newest first.
	byID := map[int64]Transfer{}
	for _, tr := range got {
		byID[tr.ID] = tr
	}
	if _, ok := byID[active]; !ok {
		t.Fatalf("active row %d missing from %d rows", active, len(got))
	}
	if _, ok := byID[paused]; !ok {
		t.Fatalf("paused row %d missing", paused)
	}
	for _, id := range pending {
		if _, ok := byID[id]; !ok {
			t.Fatalf("pending row %d missing", id)
		}
	}
	wantFinished := finished[len(finished)-3:]
	for _, id := range finished[:len(finished)-3] {
		if _, ok := byID[id]; ok {
			t.Fatalf("finished row %d should be outside the window", id)
		}
	}
	for _, id := range wantFinished {
		if _, ok := byID[id]; !ok {
			t.Fatalf("newest finished row %d missing", id)
		}
	}
	if len(got) != 2+len(pending)+3 {
		t.Fatalf("got %d rows, want %d", len(got), 2+len(pending)+3)
	}
	for i := 1; i < len(got); i++ {
		if got[i].ID >= got[i-1].ID {
			t.Fatalf("not newest first at %d: %d then %d", i, got[i-1].ID, got[i].ID)
		}
	}
}

// The pending cap must drop far-off pending rows, never live ones, and the
// pending rows it keeps are the ones next in claim order.
func TestTransfersPendingCapKeepsLiveRowsFirst(t *testing.T) {
	// Arrange — pending rows enqueued BEFORE the active one, so "newest" or
	// "oldest" alone would both get this wrong.
	s := openTestStore(t)
	site := seedSite(t, s)
	var pending []int64
	for i := 0; i < 5; i++ {
		id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/p", Dst: "/l/p"})
		pending = append(pending, id)
	}
	urgent, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/u", Dst: "/l/u", Priority: 9})
	active, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/a", Dst: "/l/a"})
	if err := s.SetTransferState(active, "active", nil); err != nil {
		t.Fatal(err)
	}

	// Act — the live row is never capped; room for two pending
	got, err := s.transfersWindow(2, 500, 200)
	if err != nil {
		t.Fatal(err)
	}

	// Assert — claim order is priority DESC then id ASC: the urgent row,
	// then the oldest plain pending row.
	ids := map[int64]bool{}
	for _, tr := range got {
		ids[tr.ID] = true
	}
	if len(got) != 3 || !ids[active] || !ids[urgent] || !ids[pending[0]] {
		t.Fatalf("window wrong: %+v", got)
	}
}

// A night of mass failures must not push the pending backlog out of view:
// failed rows have their own window.
func TestTransfersFailedRowsDoNotEvictPending(t *testing.T) {
	// Arrange — more failed rows than the queued cap, plus a few pending.
	s := openTestStore(t)
	site := seedSite(t, s)
	msg := "boom"
	var failed []int64
	for i := 0; i < 6; i++ {
		id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/f", Dst: "/l/f"})
		if err := s.SetTransferState(id, "failed", &msg); err != nil {
			t.Fatal(err)
		}
		failed = append(failed, id)
	}
	var pending []int64
	for i := 0; i < 3; i++ {
		id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/p", Dst: "/l/p"})
		pending = append(pending, id)
	}

	// Act — pending cap 4 (> pending), failed cap 2
	got, err := s.transfersWindow(4, 2, 200)
	if err != nil {
		t.Fatal(err)
	}

	// Assert — every pending row, only the 2 newest failed
	ids := map[int64]bool{}
	for _, tr := range got {
		ids[tr.ID] = true
	}
	for _, id := range pending {
		if !ids[id] {
			t.Fatalf("pending %d evicted by failed rows: %+v", id, got)
		}
	}
	if len(got) != 5 || !ids[failed[5]] || !ids[failed[4]] || ids[failed[0]] {
		t.Fatalf("failed window wrong: %+v", got)
	}
}

// The production list query must not sort anything but its capped
// windows: a user who never presses Clear done accumulates tens of
// thousands of completed rows, and this read runs on every queue:changed
// on the connection the dispatcher's checkpoints share. Every per-state arm
// must walk an index; the temp sorts SQLite adds to merge the arms in the
// outer newest-first order may only sit over an arm's (capped) output,
// never over the table itself.
func TestTransfersWindowUsesIndexes(t *testing.T) {
	s := openTestStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+transfersWindowSQL, 1, 1, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type node struct {
		id, parent int
		detail     string
	}
	var plan []node
	for rows.Next() {
		var n node
		var notused int
		if err := rows.Scan(&n.id, &n.parent, &notused, &n.detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, n)
	}
	siblings := func(parent int) []string {
		var out []string
		for _, n := range plan {
			if n.parent == parent {
				out = append(out, n.detail)
			}
		}
		return out
	}
	indexWalks := 0
	for _, n := range plan {
		if strings.Contains(n.detail, "SCAN transfers") {
			t.Fatalf("full table scan in list query: %+v", plan)
		}
		if strings.Contains(n.detail, "SEARCH transfers USING INDEX idx_transfers_state") {
			indexWalks++
		}
		if strings.Contains(n.detail, "TEMP B-TREE") {
			for _, sib := range siblings(n.parent) {
				if strings.Contains(sib, " transfers ") {
					t.Fatalf("temp sort over the table itself (%q): %+v", sib, plan)
				}
			}
		}
	}
	if indexWalks != 7 {
		t.Fatalf("want 7 index walks (one per state arm), got %d: %+v", indexWalks, plan)
	}
}
