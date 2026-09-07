package queue

import "testing"

const gb = int64(1) << 30

// TestClassifyConflict pins the one judgement call in the whole policy:
// which combination is confident enough to replace a file without asking.
func TestClassifyConflict(t *testing.T) {
	// Arrange — 1 March and 12 March 2026, as Unix seconds.
	const early, late = int64(1772323200), int64(1773273600)

	cases := []struct {
		name               string
		incoming, existing FileFacts
		want               string
	}{
		{
			"same size and time is identical",
			FileFacts{Size: 2 * gb, Mtime: late}, FileFacts{Size: 2 * gb, Mtime: late},
			KindIdentical,
		},
		{
			"newer and bigger is the only confident upgrade",
			FileFacts{Size: 2 * gb, Mtime: late}, FileFacts{Size: 1 * gb, Mtime: early},
			KindNewerLarger,
		},
		{
			"smaller is a possible downgrade even when newer",
			FileFacts{Size: 400 << 20, Mtime: late}, FileFacts{Size: 2 * gb, Mtime: early},
			KindSmaller,
		},
		{
			"older but bigger is not an upgrade we may assume",
			FileFacts{Size: 3 * gb, Mtime: early}, FileFacts{Size: 2 * gb, Mtime: late},
			KindOlder,
		},
		{
			"same size, different time is nobody's clear win",
			FileFacts{Size: 2 * gb, Mtime: late}, FileFacts{Size: 2 * gb, Mtime: early},
			KindOther,
		},
		{
			// A server that reports no mtime must not let a same-size file
			// be called identical — that would skip a re-download the user
			// deliberately asked for.
			"unknown timestamps never claim identical",
			FileFacts{Size: 2 * gb}, FileFacts{Size: 2 * gb},
			KindOther,
		},
		{
			"unknown timestamps still compare sizes",
			FileFacts{Size: 1 * gb}, FileFacts{Size: 2 * gb},
			KindSmaller,
		},
		{
			// Bigger with no timestamp is not proven newer, so it must not
			// take the silent-overwrite path.
			"bigger with unknown time is not the confident case",
			FileFacts{Size: 3 * gb}, FileFacts{Size: 2 * gb},
			KindOther,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			got := ClassifyConflict(tc.incoming, tc.existing)

			// Assert
			if got != tc.want {
				t.Fatalf("ClassifyConflict = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestConflictActionDefaultsAndOverrides — the defaults are the shipped
// policy, and a stored value nobody recognises must never be treated as
// permission to delete.
func TestConflictActionDefaultsAndOverrides(t *testing.T) {
	// Arrange
	s := openTestStore(t)

	// Act & Assert — out of the box.
	if got := s.ConflictAction(KindNewerLarger); got != ActionOverwrite {
		t.Fatalf("newer+larger defaults to %q, want %q", got, ActionOverwrite)
	}
	for _, kind := range []string{KindSmaller, KindOlder, KindOther} {
		if got := s.ConflictAction(kind); got != ActionAsk {
			t.Fatalf("%s defaults to %q, want %q", kind, got, ActionAsk)
		}
	}
	if got := s.ConflictAction(KindIdentical); got != ActionSkip {
		t.Fatalf("identical defaults to %q, want %q", got, ActionSkip)
	}

	// Arrange — the user changes one rule.
	if err := s.SetSetting("transfers.conflict_smaller", ActionSkip); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Act & Assert
	if got := s.ConflictAction(KindSmaller); got != ActionSkip {
		t.Fatalf("override gave %q, want %q", got, ActionSkip)
	}

	// Arrange — a value from a newer build, or a corrupted row.
	if err := s.SetSetting("transfers.conflict_older", "obliterate"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Act & Assert — falls back to asking, never to acting.
	if got := s.ConflictAction(KindOlder); got != ActionAsk {
		t.Fatalf("unknown action resolved to %q, want %q", got, ActionAsk)
	}
	if got := s.ConflictAction("not-a-kind"); got != ActionAsk {
		t.Fatalf("unknown kind resolved to %q, want %q", got, ActionAsk)
	}
}

// TestHeldRowsNeverDispatch is the guarantee the whole feature rests on: a
// transfer waiting for a decision must not move a single byte.
func TestHeldRowsNeverDispatch(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	site := seedSite(t, s)
	id, err := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/r/a.mkv", Dst: "/l/a.mkv", Size: gb})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := s.SetConflict(id, Conflict{
		Kind:     KindSmaller,
		Incoming: FileFacts{Size: 400 << 20, Mtime: 1772323200},
		Existing: FileFacts{Size: gb, Mtime: 1773273600},
	}); err != nil {
		t.Fatalf("set conflict: %v", err)
	}

	// Act & Assert — invisible to the dispatcher, and unclaimable even if
	// something reached past it.
	pending, err := s.PendingTransfers("2030-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("a held row was offered for dispatch: %+v", pending)
	}
	if won, _ := s.ClaimPending(id); won {
		t.Fatal("a held row was claimed")
	}

	// Assert — the row still carries the facts the queue needs to explain.
	held, err := s.ConflictTransfers()
	if err != nil {
		t.Fatalf("conflicts: %v", err)
	}
	if len(held) != 1 || held[0].Conflict == nil {
		t.Fatalf("held rows = %+v, want one carrying its conflict", held)
	}

	// Act — the user chooses rename.
	ok, err := s.ResolveConflict(id, "/l/a (1).mkv")
	if err != nil || !ok {
		t.Fatalf("ResolveConflict = %v, %v; want true, nil", ok, err)
	}

	// Assert — released, re-pointed, and now dispatchable.
	got, err := s.TransferByID(id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Conflict != nil {
		t.Fatal("resolving left the hold in place")
	}
	if got.Dst != "/l/a (1).mkv" {
		t.Fatalf("dst = %q, want the renamed destination", got.Dst)
	}
	pending, _ = s.PendingTransfers("2030-01-01T00:00:00Z")
	if len(pending) != 1 {
		t.Fatalf("released row is still not dispatchable: %+v", pending)
	}

	// Act & Assert — resolving twice must not re-point a transfer that has
	// since been released and may already be running.
	if ok, _ := s.ResolveConflict(id, "/l/somewhere-else.mkv"); ok {
		t.Fatal("a row that was not held was resolved anyway")
	}
}

// TestDstIsClaimedStopsKeepBothCollisions — "Keep both" asks the filesystem
// for a free name, but a name nothing has created yet can still be spoken
// for by another queued row. Two clashes resolved in one click would
// otherwise both take "ep01 (1).mkv" and the second would rename over the
// first's finished file.
func TestDstIsClaimedStopsKeepBothCollisions(t *testing.T) {
	// Arrange — one row already headed for the renamed destination.
	s := openTestStore(t)
	site := seedSite(t, s)
	const taken = "/l/ep01 (1).mkv"
	if _, err := s.EnqueueTransfer(Transfer{
		SiteID: site, Direction: "download", Src: "/a/ep01.mkv", Dst: taken}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Act & Assert
	claimed, err := s.DstIsClaimed(taken, "download", site)
	if err != nil || !claimed {
		t.Fatalf("DstIsClaimed = %v, %v; want true, nil", claimed, err)
	}
	free, err := s.DstIsClaimed("/l/ep01 (2).mkv", "download", site)
	if err != nil || free {
		t.Fatalf("an unused name reported claimed: %v, %v", free, err)
	}

	// Arrange — a finished row no longer speaks for its destination; the
	// file itself does, and the filesystem check covers that.
	rows, _ := s.Transfers(10)
	if err := s.SetTransferState(rows[0].ID, "completed", nil); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Act & Assert
	if claimed, _ := s.DstIsClaimed(taken, "download", site); claimed {
		t.Fatal("a completed row still claimed its destination")
	}
}

// TestDeletePendingOnlyRemovesUntouchedRows — used to undo an enqueue whose
// hold could not be written. It must never remove a transfer that is running
// or that already holds bytes.
func TestDeletePendingOnlyRemovesUntouchedRows(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	site := seedSite(t, s)
	fresh, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/r/a", Dst: "/l/a"})
	started, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/r/b", Dst: "/l/b"})
	running, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/r/c", Dst: "/l/c"})
	if err := s.UpdateTransferProgress(started, 1<<20); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := s.SetTransferState(running, "active", nil); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// Act & Assert
	if n, err := s.DeletePending(fresh); err != nil || n != 1 {
		t.Fatalf("untouched row: %d, %v; want 1, nil", n, err)
	}
	if n, _ := s.DeletePending(started); n != 0 {
		t.Fatal("a row holding bytes was deleted")
	}
	if n, _ := s.DeletePending(running); n != 0 {
		t.Fatal("a running row was deleted")
	}
}
