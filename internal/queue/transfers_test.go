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

// spendAttempt drives a row through one real retry: active, then the
// engine scheduling a retry. The only way a row gets a spent attempt and a
// deadline, now that the unguarded write is gone — and closer to what the
// dispatcher actually does than a direct write was.
func spendAttempt(t *testing.T, s *Store, id int64, at string, msg *string) {
	t.Helper()
	if err := s.SetTransferState(id, "active", nil); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if ok, err := s.ScheduleRetryIfActive(id, at, msg); err != nil || !ok {
		t.Fatalf("schedule retry: ok=%v err=%v", ok, err)
	}
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
	spendAttempt(t, s, id, "2026-08-01T12:00:00Z", nil)

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

func TestCancelByIDOnlyTouchesUnfinishedRows(t *testing.T) {
	// Arrange — one row per state the dock can show.
	s := openTestStore(t)
	site := seedSite(t, s)
	pending, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/p", Dst: "/l/p"})
	paused, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/q", Dst: "/l/q"})
	active, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/a", Dst: "/l/a"})
	done, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/d", Dst: "/l/d"})
	other, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/o", Dst: "/l/o"})
	for id, st := range map[int64]string{paused: "paused", active: "active", done: "completed"} {
		if err := s.SetTransferState(id, st, nil); err != nil {
			t.Fatal(err)
		}
	}

	failed, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/x", Dst: "/l/x"})
	boom := "boom"
	_ = s.SetTransferState(failed, "failed", &boom)
	if err := s.SetConflict(pending, Conflict{Kind: "other"}); err != nil {
		t.Fatal(err)
	}

	// Act — the finished row is named on purpose; it must be ignored. The
	// failed one is cancellable one at a time, so it must be here too.
	marked, err := s.CancelByID([]int64{pending, paused, active, done, failed})
	if err != nil {
		t.Fatal(err)
	}

	// Assert — the ids it reports are the ids it changed.
	if len(marked) != 4 {
		t.Fatalf("cancelled %d rows, want 4 (the finished one is not cancellable): %v", len(marked), marked)
	}
	for _, id := range marked {
		if id == done || id == other {
			t.Errorf("CancelByID reported id %d, which it must not have touched", id)
		}
	}
	if got, _ := s.TransferByID(pending); got.Conflict != nil {
		t.Errorf("cancelled row still holds its conflict: %v", *got.Conflict)
	}
	for _, id := range []int64{pending, paused, active, failed} {
		if got, _ := s.TransferByID(id); got.State != "cancelled" {
			t.Errorf("row %d is %q, want cancelled", id, got.State)
		}
	}
	if got, _ := s.TransferByID(done); got.State != "completed" {
		t.Errorf("completed row became %q", got.State)
	}
	if got, _ := s.TransferByID(other); got.State != "pending" {
		t.Errorf("row that was not named became %q", got.State)
	}
}

func TestCancelByIDHandlesMoreIdsThanOneBatch(t *testing.T) {
	// Arrange — past the per-statement batch, so the loop takes two trips.
	s := openTestStore(t)
	site := seedSite(t, s)
	ids := make([]int64, 0, idBatch+7)
	for i := 0; i < idBatch+7; i++ {
		id, err := s.EnqueueTransfer(Transfer{SiteID: site, Src: fmt.Sprintf("/f%d", i), Dst: fmt.Sprintf("/l/f%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	// Act
	rows, err := s.TransfersByID(ids)
	if err != nil {
		t.Fatal(err)
	}
	marked, err := s.CancelByID(ids)
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	if len(rows) != len(ids) || len(marked) != len(ids) {
		t.Fatalf("read %d rows and cancelled %d, want %d of each", len(rows), len(marked), len(ids))
	}
	if gone, _ := s.CancelledTransfers(); len(gone) != len(ids) {
		t.Fatalf("%d rows cancelled on disk, want %d", len(gone), len(ids))
	}
}

func TestCancelQueuedLeavesRunningRowsAlone(t *testing.T) {
	// Arrange — a held row is pending underneath, so it counts as queued.
	s := openTestStore(t)
	site := seedSite(t, s)
	pending, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/p", Dst: "/l/p"})
	paused, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/q", Dst: "/l/q"})
	held, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/h", Dst: "/l/h"})
	active, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/a", Dst: "/l/a"})
	failed, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/f", Dst: "/l/f"})
	_ = s.SetTransferState(paused, "paused", nil)
	_ = s.SetTransferState(active, "active", nil)
	msg := "boom"
	_ = s.SetTransferState(failed, "failed", &msg)
	if err := s.SetConflict(held, Conflict{Kind: "other"}); err != nil {
		t.Fatal(err)
	}

	// Act
	marked, err := s.CancelQueued()
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	if len(marked) != 3 {
		t.Fatalf("cancelled %d rows, want 3 (pending, paused, held)", len(marked))
	}
	if got, _ := s.TransferByID(held); got.Conflict != nil {
		t.Errorf("cancelled held row still asks for a decision: %v", *got.Conflict)
	}
	for id, want := range map[int64]string{pending: "cancelled", paused: "cancelled", held: "cancelled", active: "active", failed: "failed"} {
		if got, _ := s.TransferByID(id); got.State != want {
			t.Errorf("row %d is %q, want %q", id, got.State, want)
		}
	}
}

func TestRequeueResetsOnlyAnActiveRow(t *testing.T) {
	// Arrange — an active row two attempts in, with an error and a deadline
	// left over from its previous run.
	s := openTestStore(t)
	site := seedSite(t, s)
	id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/a", Dst: "/l/a", Size: 100})
	msg := "flaky"
	spendAttempt(t, s, id, "2026-08-01T12:00:00Z", &msg)
	spendAttempt(t, s, id, "2026-08-01T12:00:00Z", &msg)
	_ = s.SetTransferState(id, "active", &msg)
	_ = s.UpdateTransferProgress(id, 42)
	userPaused, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/b", Dst: "/l/b"})
	_ = s.SetTransferState(userPaused, "paused", nil)

	// Act
	ok, err := s.Requeue(id)
	if err != nil {
		t.Fatal(err)
	}
	notActive, err := s.Requeue(userPaused)
	if err != nil {
		t.Fatal(err)
	}

	// Assert — clean slate, bytes kept; the user's own pause is respected.
	if !ok || notActive {
		t.Fatalf("Requeue reported active=%v paused=%v, want true and false", ok, notActive)
	}
	got, _ := s.TransferByID(id)
	if got.State != "pending" || got.Attempt != 0 || got.NextRetryAt != nil || got.Error != nil {
		t.Errorf("requeued row = state %q attempt %d retryAt %v err %v; want pending/0/nil/nil", got.State, got.Attempt, got.NextRetryAt, got.Error)
	}
	if got.BytesDone != 42 {
		t.Errorf("requeue lost progress: %d bytes, want 42", got.BytesDone)
	}
	if p, _ := s.TransferByID(userPaused); p.State != "paused" {
		t.Errorf("user-paused row became %q", p.State)
	}
}

func TestOtherLiveTransfersForDstIgnoresCancelledRows(t *testing.T) {
	// Arrange — three rows on one destination: one cancelled, one failed,
	// one completed. Only the failed one still has a claim on the bytes.
	s := openTestStore(t)
	site := seedSite(t, s)
	cancelled, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/a", Dst: "/l/x"})
	failed, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/b", Dst: "/l/x"})
	done, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/c", Dst: "/l/x"})
	_ = s.SetTransferState(cancelled, "cancelled", nil)
	boom := "boom"
	_ = s.SetTransferState(failed, "failed", &boom)
	_ = s.SetTransferState(done, "completed", nil)

	// Act
	n, err := s.OtherLiveTransfersForDst(nil, "/l/x")
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	if n != 1 {
		t.Fatalf("%d live owners, want 1 (only the failed row)", n)
	}
}

func TestGuardedWritesRefuseARowTheUserMoved(t *testing.T) {
	// Arrange — a running row the user cancels while the engine unwinds.
	s := openTestStore(t)
	site := seedSite(t, s)
	id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/a", Dst: "/l/a"})
	_ = s.SetTransferState(id, "active", nil)
	if _, err := s.CancelByID([]int64{id}); err != nil {
		t.Fatal(err)
	}
	boom := "boom"

	// Act
	finished, err := s.FinishActive(id, "completed", nil)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := s.ScheduleRetryIfActive(id, "2026-08-01T00:00:00Z", &boom)
	if err != nil {
		t.Fatal(err)
	}

	// Assert — neither write landed; the cancel stands.
	if finished || retried {
		t.Fatalf("finished=%v retried=%v, want both refused", finished, retried)
	}
	if got, _ := s.TransferByID(id); got.State != "cancelled" || got.Attempt != 0 {
		t.Errorf("row = %q attempt %d, want cancelled/0", got.State, got.Attempt)
	}

	// Arrange — and for a row that IS still active, both land as before.
	live, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/b", Dst: "/l/b"})
	_ = s.SetTransferState(live, "active", nil)
	if ok, _ := s.ScheduleRetryIfActive(live, "2026-08-01T00:00:00Z", &boom); !ok {
		t.Fatal("retry refused for an active row")
	}
	_ = s.SetTransferState(live, "active", nil)
	if ok, _ := s.FinishActive(live, "completed", nil); !ok {
		t.Fatal("completion refused for an active row")
	}
}

func TestRequeueExceptHealsOrphansOnly(t *testing.T) {
	// Arrange — two active rows: one still has a goroutine, one does not.
	s := openTestStore(t)
	site := seedSite(t, s)
	orphan, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/a", Dst: "/l/a"})
	running, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/b", Dst: "/l/b"})
	_ = s.SetTransferState(orphan, "active", nil)
	_ = s.SetTransferState(running, "active", nil)

	// Act
	n, err := s.RequeueExcept([]int64{running})
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	if n != 1 {
		t.Fatalf("requeued %d rows, want 1", n)
	}
	if got, _ := s.TransferByID(orphan); got.State != "pending" {
		t.Errorf("orphan is %q, want pending", got.State)
	}
	if got, _ := s.TransferByID(running); got.State != "active" {
		t.Errorf("running row became %q", got.State)
	}
}
