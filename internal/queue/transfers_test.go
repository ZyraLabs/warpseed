package queue

import (
	"fmt"
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
	n, err := s.ClearCompleted()
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
		id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: fmt.Sprintf("/p%d", i), Dst: fmt.Sprintf("/l/p%d", i)})
		pending = append(pending, id)
	}
	for i := 0; i < 10; i++ {
		id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: fmt.Sprintf("/d%d", i), Dst: fmt.Sprintf("/l/d%d", i)})
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
		id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: fmt.Sprintf("/f%d", i), Dst: fmt.Sprintf("/l/f%d", i)})
		if err := s.SetTransferState(id, "failed", &msg); err != nil {
			t.Fatal(err)
		}
		failed = append(failed, id)
	}
	var pending []int64
	for i := 0; i < 3; i++ {
		id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: fmt.Sprintf("/pp%d", i), Dst: fmt.Sprintf("/l/pp%d", i)})
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

// TestRetryAndClearFailedAreBulk covers the two buttons a batch failure
// needs: one drive unplug fails dozens of rows, and clearing them one at a
// time was the reported pain.
func TestRetryAndClearFailedAreBulk(t *testing.T) {
	// Arrange — two failed rows, one pending and one completed alongside
	// them, so a query that is too broad shows up as a wrong count.
	s := openTestStore(t)
	site := seedSite(t, s)
	ids := make([]int64, 0, 4)
	for i := 0; i < 4; i++ {
		id, err := s.EnqueueTransfer(Transfer{
			SiteID: site, Direction: "download",
			Src: fmt.Sprintf("/r/f%d", i), Dst: fmt.Sprintf("/l/f%d", i), Size: 10,
		})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		ids = append(ids, id)
	}
	boom := "disk gone"
	for _, id := range ids[:2] {
		if err := s.SetTransferState(id, "failed", &boom); err != nil {
			t.Fatalf("fail: %v", err)
		}
	}
	if err := s.SetTransferState(ids[3], "completed", nil); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// Progress that resume must keep.
	if err := s.UpdateTransferProgress(ids[0], 7); err != nil {
		t.Fatalf("progress: %v", err)
	}

	// Act — the caller needs every failed row to clean up placeholders.
	failed, err := s.FailedTransfers()
	if err != nil {
		t.Fatalf("failed transfers: %v", err)
	}

	// Assert
	if len(failed) != 2 {
		t.Fatalf("FailedTransfers returned %d rows, want 2", len(failed))
	}

	// Act
	n, err := s.RetryFailed()
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}

	// Assert — both requeued from a clean slate, byte progress intact so
	// they resume rather than re-download.
	if n != 2 {
		t.Fatalf("RetryFailed touched %d rows, want 2", n)
	}
	got, err := s.TransferByID(ids[0])
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.State != "pending" || got.Attempt != 0 || got.Error != nil || got.NextRetryAt != nil {
		t.Fatalf("retried row = %+v, want pending with a cleared ladder", got)
	}
	if got.BytesDone != 7 {
		t.Fatalf("retry lost byte progress: %d, want 7", got.BytesDone)
	}
	if done, _ := s.TransferByID(ids[3]); done.State != "completed" {
		t.Fatalf("RetryFailed disturbed a completed row: %s", done.State)
	}

	// Arrange — fail them again to clear.
	for _, id := range ids[:2] {
		if err := s.SetTransferState(id, "failed", &boom); err != nil {
			t.Fatalf("fail: %v", err)
		}
	}

	// Act — clear only the first of the two, by id.
	n, err = s.ClearFailedByID([]int64{ids[0]})
	if err != nil {
		t.Fatalf("clear failed: %v", err)
	}

	// Assert — the id not named survives, so a confirmation for one row can
	// never sweep up a row that failed while the dialog was open.
	if n != 1 {
		t.Fatalf("ClearFailedByID removed %d rows, want 1", n)
	}
	if _, err := s.TransferByID(ids[1]); err != nil {
		t.Fatalf("unnamed failed row was removed: %v", err)
	}

	// Act — a row that is no longer failed must survive being named.
	if err := s.SetTransferState(ids[1], "active", nil); err != nil {
		t.Fatalf("activate: %v", err)
	}
	n, err = s.ClearFailedByID([]int64{ids[1]})
	if err != nil {
		t.Fatalf("clear failed: %v", err)
	}

	// Assert
	if n != 0 {
		t.Fatalf("ClearFailedByID removed a non-failed row (%d)", n)
	}
	if err := s.SetTransferState(ids[1], "failed", &boom); err != nil {
		t.Fatalf("re-fail: %v", err)
	}
	if n, err := s.ClearFailedByID([]int64{ids[1]}); err != nil || n != 1 {
		t.Fatalf("ClearFailedByID = %d, %v; want 1, nil", n, err)
	}
	rest, err := s.Transfers(200)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rest) != 2 {
		t.Fatalf("%d rows left, want 2 (one pending, one completed)", len(rest))
	}
	for _, r := range rest {
		if r.State == "failed" {
			t.Fatalf("failed row %d survived the clear", r.ID)
		}
	}
}

// TestOtherLiveTransfersForDst guards the placeholder that a re-queued copy
// of the same file now owns: clearing the failed row must not delete it.
func TestOtherLiveTransfersForDst(t *testing.T) {
	// Arrange — the same destination queued twice, as re-dragging a folder
	// after an overnight run produces.
	s := openTestStore(t)
	site := seedSite(t, s)
	const dst = "/local/season/ep01.mkv"
	oldID, err := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/r/ep01.mkv", Dst: dst, Size: 99})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	boom := "drive gone"
	if err := s.SetTransferState(oldID, "failed", &boom); err != nil {
		t.Fatalf("fail: %v", err)
	}
	newID, err := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/r/ep01.mkv", Dst: dst, Size: 99})
	if err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}

	// Act & Assert — clearing only the failed row must see the pending one.
	n, err := s.OtherLiveTransfersForDst([]int64{oldID}, dst)
	if err != nil {
		t.Fatalf("owners: %v", err)
	}
	if n != 1 {
		t.Fatalf("owners = %d, want 1 (the re-queued copy)", n)
	}

	// Act & Assert — but when BOTH rows are in the batch being cleared,
	// neither is a live owner: something must delete the placeholder, or it
	// is stranded with no row pointing at it.
	if n, err := s.OtherLiveTransfersForDst([]int64{oldID, newID}, dst); err != nil || n != 0 {
		t.Fatalf("owners for the whole batch = %d, %v; want 0, nil", n, err)
	}

	// Arrange — once the duplicate has completed, its placeholder has been
	// renamed away and no longer needs protecting.
	if err := s.SetTransferState(newID, "completed", nil); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Act & Assert
	if n, err := s.OtherLiveTransfersForDst([]int64{oldID}, dst); err != nil || n != 0 {
		t.Fatalf("owners after completion = %d, %v; want 0, nil", n, err)
	}
	// A different destination is never confused for this one.
	if n, err := s.OtherLiveTransfersForDst([]int64{oldID}, "/local/season/ep02.mkv"); err != nil || n != 0 {
		t.Fatalf("owners for another dst = %d, %v; want 0, nil", n, err)
	}
}

// TestEnqueueIsIdempotentForAnUnfinishedRow — dragging the same folder
// across twice used to leave two rows writing one placeholder path.
func TestEnqueueIsIdempotentForAnUnfinishedRow(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	site := seedSite(t, s)
	tr := Transfer{SiteID: site, Direction: "download", Src: "/r/ep01.mkv", Dst: "/l/ep01.mkv", Size: 42}
	first, err := s.EnqueueTransfer(tr)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Act — the same drag again.
	again, err := s.EnqueueTransfer(tr)
	if err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}

	// Assert — the caller gets the row that already exists, not a second one.
	if again != first {
		t.Fatalf("second enqueue made row %d, want the existing %d", again, first)
	}

	// Act & Assert — a DIFFERENT source landing on the same destination is a
	// conflict, not a duplicate. Dropping it would be the queue lying about
	// what it accepted.
	other, err := s.EnqueueTransfer(Transfer{
		SiteID: site, Direction: "download", Src: "/r/other.mkv", Dst: "/l/ep01.mkv", Size: 42})
	if err != nil {
		t.Fatalf("conflicting enqueue: %v", err)
	}
	if other == first {
		t.Fatal("a different source was swallowed as a duplicate")
	}

	// Act & Assert — once the row is out of the running, re-queuing must work
	// again: that is how a user retries.
	if err := s.SetTransferState(first, "cancelled", nil); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	retried, err := s.EnqueueTransfer(tr)
	if err != nil {
		t.Fatalf("retry enqueue: %v", err)
	}
	if retried == first {
		t.Fatal("re-queuing after cancel returned the dead row instead of a fresh one")
	}
}

// TestClaimPendingRefusesARowTheUserStopped — the dispatcher claims from a
// list it read earlier, so a row can be cancelled in between. An
// unconditional write would resurrect it and start moving bytes for a
// transfer the user believes is stopped.
func TestClaimPendingRefusesARowTheUserStopped(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	site := seedSite(t, s)
	id, err := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/r/a", Dst: "/l/a"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Act & Assert — a pending row is claimable exactly once.
	won, err := s.ClaimPending(id)
	if err != nil || !won {
		t.Fatalf("ClaimPending = %v, %v; want true, nil", won, err)
	}
	if won, _ := s.ClaimPending(id); won {
		t.Fatal("an already-active row was claimed a second time")
	}

	// Arrange — the user cancels it.
	if err := s.SetTransferState(id, "cancelled", nil); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// Act & Assert — the claim must lose, and must not rewrite the state.
	if won, _ := s.ClaimPending(id); won {
		t.Fatal("a cancelled row was claimed")
	}
	got, err := s.TransferByID(id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.State != "cancelled" {
		t.Fatalf("state = %q, want cancelled: a lost claim must not resurrect the row", got.State)
	}
}

// TestClearCancelledOnlyRemovesNamedRows — clear-done must delete only the
// cancelled rows whose files were actually accounted for; the rest keep
// their row, which is the only record the file exists.
func TestClearCancelledOnlyRemovesNamedRows(t *testing.T) {
	// Arrange — two cancelled rows and one completed.
	s := openTestStore(t)
	site := seedSite(t, s)
	var ids []int64
	for i := 0; i < 3; i++ {
		id, err := s.EnqueueTransfer(Transfer{
			SiteID: site, Src: fmt.Sprintf("/r/%d", i), Dst: fmt.Sprintf("/l/%d", i)})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids[:2] {
		if err := s.SetTransferState(id, "cancelled", nil); err != nil {
			t.Fatalf("cancel: %v", err)
		}
	}
	if err := s.SetTransferState(ids[2], "completed", nil); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Act — only the first cancelled row's data could be removed.
	gone, err := s.ClearCancelledByID([]int64{ids[0]})
	if err != nil {
		t.Fatalf("clear cancelled: %v", err)
	}
	done, err := s.ClearCompleted()
	if err != nil {
		t.Fatalf("clear completed: %v", err)
	}

	// Assert
	if gone != 1 || done != 1 {
		t.Fatalf("removed %d cancelled and %d completed, want 1 and 1", gone, done)
	}
	if _, err := s.TransferByID(ids[1]); err != nil {
		t.Fatalf("the unnamed cancelled row was deleted, stranding its file: %v", err)
	}
}
