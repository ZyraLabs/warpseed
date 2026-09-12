package queue

import (
	"database/sql"
	"errors"
	"fmt"
)

// Transfer is one queue row — the durable record of a transfer through its
// whole lifecycle (approved plan: SQLite is the queue-of-record).
type Transfer struct {
	ID          int64   `json:"id"`
	SiteID      int64   `json:"siteId"`
	Engine      string  `json:"engine"`    // sftpfast | rclone
	Direction   string  `json:"direction"` // download | upload
	Src         string  `json:"src"`
	Dst         string  `json:"dst"`
	Size        int64   `json:"size"`
	State       string  `json:"state"`
	Priority    int     `json:"priority"`
	BytesDone   int64   `json:"bytesDone"`
	Attempt     int     `json:"attempt"`
	NextRetryAt *string `json:"nextRetryAt"`
	Error       *string `json:"error"`
	// SrcMtime is the source modification time a chunk plan was built
	// against; a mismatch on resume means the file changed under us.
	SrcMtime  int64  `json:"srcMtime"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	// StartedAt/StartBytes describe the CURRENT run, not the row's whole
	// life: they are re-stamped every time the transfer is claimed, so a
	// resumed transfer reports the speed of the run you actually watched.
	StartedAt  *string `json:"startedAt"`
	StartBytes int64   `json:"startBytes"`
	// Conflict is set when the destination already exists and the policy
	// said to ask. The row stays pending but is held out of dispatch until
	// the user resolves it, so the decision happens before any bytes move.
	// JSON; see conflict.go.
	Conflict *string `json:"conflict"`
}

var ErrTransferNotFound = errors.New("transfer not found")

const transferCols = `id,site_id,engine,direction,src,dst,size,state,priority,
	bytes_done,attempt,next_retry_at,error,src_mtime,created_at,updated_at,
	started_at,start_bytes,conflict`

func scanTransfer(row interface{ Scan(...any) error }) (Transfer, error) {
	var t Transfer
	err := row.Scan(&t.ID, &t.SiteID, &t.Engine, &t.Direction, &t.Src, &t.Dst,
		&t.Size, &t.State, &t.Priority, &t.BytesDone, &t.Attempt,
		&t.NextRetryAt, &t.Error, &t.SrcMtime, &t.CreatedAt, &t.UpdatedAt,
		&t.StartedAt, &t.StartBytes, &t.Conflict)
	return t, err
}

// MarkTransferStarted stamps the beginning of a run. Called once per claim,
// so a transfer that resumes three times reports the last run's speed rather
// than an average smeared across the pauses between them.
func (s *Store) MarkTransferStarted(id int64, at string, startBytes int64) error {
	_, err := s.db.Exec(
		`UPDATE transfers SET started_at=?, start_bytes=? WHERE id=?`, at, startBytes, id)
	if err != nil {
		return fmt.Errorf("mark transfer started: %w", err)
	}
	return nil
}

// SetTransferSrcMtime records the source mtime a chunk plan was built for.
func (s *Store) SetTransferSrcMtime(id, mtime int64) error {
	_, err := s.db.Exec(`UPDATE transfers SET src_mtime=? WHERE id=?`, mtime, id)
	if err != nil {
		return fmt.Errorf("set src mtime: %w", err)
	}
	return nil
}

// unfinishedStates are the rows that still intend to write their
// destination. Failed, cancelled and completed rows are excluded on
// purpose: re-queuing one of those is how a user retries, and it must keep
// working.
const unfinishedStates = `'pending','dispatched','active','paused'`

// EnqueueTransfer inserts a pending row and returns its id.
//
// Re-queuing a transfer an unfinished row already describes — same source,
// same destination, same direction, same site — returns that row's id
// instead of adding a second one. Dragging the same folder across twice is
// easy to do and used to produce two rows writing one placeholder path.
//
// The match is deliberately on the source too. Two DIFFERENT files landing
// on one destination is a conflict, not a duplicate, and silently dropping
// the second would be the queue lying about what it accepted; the
// dispatcher's per-destination lock keeps them from running together until
// there is an overwrite policy to resolve it properly (roadmap 1.1).
func (s *Store) EnqueueTransfer(t Transfer) (int64, error) {
	if t.SiteID == 0 || t.Src == "" || t.Dst == "" {
		return 0, errors.New("transfer requires site, src and dst")
	}
	if t.Engine == "" {
		t.Engine = "sftpfast"
	}
	if t.Direction == "" {
		t.Direction = "download"
	}
	var existing int64
	err := s.db.QueryRow(
		`SELECT id FROM transfers
		 WHERE dst=? AND src=? AND direction=? AND site_id=? AND state IN (`+unfinishedStates+`)
		 ORDER BY id ASC LIMIT 1`, t.Dst, t.Src, t.Direction, t.SiteID).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("check duplicate transfer: %w", err)
	}
	now := nowUTC()
	res, err := s.db.Exec(
		`INSERT INTO transfers(site_id,engine,direction,src,dst,size,state,priority,created_at,updated_at)
		 VALUES (?,?,?,?,?,?,'pending',?,?,?)`,
		t.SiteID, t.Engine, t.Direction, t.Src, t.Dst, t.Size, t.Priority, now, now)
	if err != nil {
		return 0, fmt.Errorf("enqueue transfer: %w", err)
	}
	return res.LastInsertId()
}

// PendingTransfers returns dispatchable rows: pending, with any retry
// deadline due, highest priority first, oldest first within a priority.
func (s *Store) PendingTransfers(now string) ([]Transfer, error) {
	rows, err := s.db.Query(
		`SELECT `+transferCols+` FROM transfers
		 WHERE state='pending' AND conflict IS NULL
		   AND (next_retry_at IS NULL OR next_retry_at <= ?)
		 ORDER BY priority DESC, id ASC`, now)
	if err != nil {
		return nil, fmt.Errorf("pending transfers: %w", err)
	}
	defer rows.Close()
	return collectTransfers(rows)
}

// Caps on how many rows of each kind one Transfers call returns. The UI
// refetches the whole list on every queue:changed, which fires for each
// completion, so an unbounded list would turn a long run of small files
// into a JSON storm over the bridge. Rows in flight (active, dispatched,
// paused) are never capped — their number is bounded by the concurrency
// settings and the user's own pauses. Pending rows are capped in claim
// order so the ones shown are the ones next in line; failed and finished
// rows are capped newest first. Each kind has its own window so a night of
// mass failures cannot evict the pending backlog from view, and vice versa.
const (
	maxPendingRows  = 2000
	maxFailedRows   = 500
	defaultFinished = 200 // completed + cancelled together
)

// transfersWindowSQL is one subquery per state so every arm walks an index
// in its output order and stops at its LIMIT: in-flight states and pending
// use idx_transfers_state (state, priority DESC, id) in claim order, the
// newest-first arms use idx_transfers_state_id. Nothing here sorts more
// than the capped rows, however many the table has accumulated. The test
// TestTransfersWindowUsesIndexes EXPLAINs this exact string.
//
// Parameters: pending cap, failed cap, finished cap (three times).
const transfersWindowSQL = `SELECT ` + transferCols + ` FROM (
   SELECT ` + transferCols + ` FROM transfers WHERE state='active'
    ORDER BY priority DESC, id ASC)
 UNION ALL
 SELECT ` + transferCols + ` FROM (
   SELECT ` + transferCols + ` FROM transfers WHERE state='dispatched'
    ORDER BY priority DESC, id ASC)
 UNION ALL
 SELECT ` + transferCols + ` FROM (
   SELECT ` + transferCols + ` FROM transfers WHERE state='paused'
    ORDER BY priority DESC, id ASC)
 UNION ALL
 SELECT ` + transferCols + ` FROM (
   SELECT ` + transferCols + ` FROM transfers WHERE state='pending'
    ORDER BY priority DESC, id ASC LIMIT ?)
 UNION ALL
 SELECT ` + transferCols + ` FROM (
   SELECT ` + transferCols + ` FROM transfers WHERE state='failed'
    ORDER BY id DESC LIMIT ?)
 UNION ALL
 SELECT ` + transferCols + ` FROM (
   SELECT * FROM (
     SELECT ` + transferCols + ` FROM transfers WHERE state='completed'
      ORDER BY id DESC LIMIT ?)
   UNION ALL
   SELECT * FROM (
     SELECT ` + transferCols + ` FROM transfers WHERE state='cancelled'
      ORDER BY id DESC LIMIT ?)
   ORDER BY id DESC LIMIT ?)
 ORDER BY id DESC`

// Transfers returns the rows the queue UI shows: every row in flight, the
// next `maxPendingRows` pending rows in claim order, the newest
// maxFailedRows failed rows, and the newest `limit` finished rows — all
// ordered newest first.
//
// It used to be a plain "newest 200 rows". The dispatcher claims oldest
// first, so once more than 200 rows were queued the active transfers fell
// outside the window and every view derived from it (dock, flight,
// activity, mini pill) showed nothing in flight while the bytes kept
// moving.
func (s *Store) Transfers(limit int) ([]Transfer, error) {
	if limit <= 0 {
		limit = defaultFinished
	}
	return s.transfersWindow(maxPendingRows, maxFailedRows, limit)
}

// transfersWindow is Transfers with the caps exposed for tests.
func (s *Store) transfersWindow(pendingCap, failedCap, finishedCap int) ([]Transfer, error) {
	rows, err := s.db.Query(transfersWindowSQL,
		pendingCap, failedCap, finishedCap, finishedCap, finishedCap)
	if err != nil {
		return nil, fmt.Errorf("list transfers: %w", err)
	}
	defer rows.Close()
	return collectTransfers(rows)
}

// TransferByID fetches one row.
func (s *Store) TransferByID(id int64) (Transfer, error) {
	t, err := scanTransfer(s.db.QueryRow(
		`SELECT `+transferCols+` FROM transfers WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, ErrTransferNotFound
	}
	if err != nil {
		return Transfer{}, fmt.Errorf("get transfer: %w", err)
	}
	return t, nil
}

// SetConflict holds a row for a decision, storing the facts of the clash.
func (s *Store) SetConflict(id int64, c Conflict) error {
	enc, err := c.Encode()
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE transfers SET conflict=?, updated_at=? WHERE id=?`, enc, nowUTC(), id)
	if err != nil {
		return fmt.Errorf("set conflict: %w", err)
	}
	return nil
}

// ResolveConflict releases a held row, optionally onto a new destination
// (the rename action). It only touches rows that are actually held, so a
// stale click from a list the user has been staring at for ten minutes
// cannot re-point a transfer that has since started.
func (s *Store) ResolveConflict(id int64, newDst string) (bool, error) {
	q := `UPDATE transfers SET conflict=NULL, updated_at=? WHERE id=? AND conflict IS NOT NULL`
	args := []any{nowUTC(), id}
	if newDst != "" {
		q = `UPDATE transfers SET conflict=NULL, dst=?, updated_at=? WHERE id=? AND conflict IS NOT NULL`
		args = []any{newDst, nowUTC(), id}
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return false, fmt.Errorf("resolve conflict: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ConflictTransfers returns every held row, uncapped: the caller resolves
// them as a batch and a row missing from this list would be a transfer
// nothing ever releases.
func (s *Store) ConflictTransfers() ([]Transfer, error) {
	rows, err := s.db.Query(
		`SELECT ` + transferCols + ` FROM transfers
		 WHERE conflict IS NOT NULL ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("conflict transfers: %w", err)
	}
	defer rows.Close()
	return collectTransfers(rows)
}

// ClaimPending moves a row to active only while it is still pending, and
// reports whether it won. The dispatcher works from a list it read earlier,
// so by the time it claims a row the user may have paused or cancelled it —
// an unconditional write would silently resurrect a cancelled transfer and
// hand a running goroutine to a row the user believes is stopped.
func (s *Store) ClaimPending(id int64) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE transfers SET state='active', error=NULL, updated_at=?
		 WHERE id=? AND state='pending' AND conflict IS NULL`, nowUTC(), id)
	if err != nil {
		return false, fmt.Errorf("claim transfer: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetTransferState transitions a row; errMsg is stored for 'failed'.
func (s *Store) SetTransferState(id int64, state string, errMsg *string) error {
	res, err := s.db.Exec(
		`UPDATE transfers SET state=?, error=?, updated_at=? WHERE id=?`,
		state, errMsg, nowUTC(), id)
	if err != nil {
		return fmt.Errorf("set transfer state: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTransferNotFound
	}
	return nil
}

// UpdateTransferProgress persists byte progress (throttled by the caller —
// events carry live progress, the DB carries crash-recovery checkpoints).
func (s *Store) UpdateTransferProgress(id, bytesDone int64) error {
	_, err := s.db.Exec(
		`UPDATE transfers SET bytes_done=?, updated_at=? WHERE id=?`,
		bytesDone, nowUTC(), id)
	if err != nil {
		return fmt.Errorf("update progress: %w", err)
	}
	return nil
}

// FailedTransfers returns every failed row, uncapped: the caller deletes
// their leftover placeholders, and a row missing from this list is a
// .wspart nothing will ever clean up. The UI list is capped
// (maxFailedRows); this is deliberately not.
func (s *Store) FailedTransfers() ([]Transfer, error) {
	return s.transfersInState("failed")
}

// CancelledTransfers returns every cancelled row, uncapped, for the same
// reason FailedTransfers is uncapped: clearing them deletes their
// placeholders, and a row missing from this list is a file nothing will
// ever clean up.
func (s *Store) CancelledTransfers() ([]Transfer, error) {
	return s.transfersInState("cancelled")
}

func (s *Store) transfersInState(state string) ([]Transfer, error) {
	rows, err := s.db.Query(
		`SELECT `+transferCols+` FROM transfers WHERE state=? ORDER BY id ASC`, state)
	if err != nil {
		return nil, fmt.Errorf("%s transfers: %w", state, err)
	}
	defer rows.Close()
	return collectTransfers(rows)
}

// RetryFailed requeues every failed row from a clean slate: a drive that
// came back or a server that stopped refusing connections is a new attempt,
// not a continuation of the ladder that gave up. The recorded byte progress
// stays, so each one resumes from its .wspart rather than restarting.
func (s *Store) RetryFailed() (int64, error) {
	res, err := s.db.Exec(`UPDATE transfers `+requeueSet+` WHERE state='failed'`, nowUTC())
	if err != nil {
		return 0, fmt.Errorf("retry failed: %w", err)
	}
	return res.RowsAffected()
}

// ClearFailedByID removes the named rows, and only while they are still
// failed. Taking explicit ids is what keeps the confirmation honest: the
// user approved the rows they were shown, and a transfer that failed — or
// was retried back into flight — between the dialog opening and the click
// must not be swept up by it. Chunk rows go with them via the schema's
// ON DELETE CASCADE; the placeholder files are the caller's job and are
// gone before this is called.
func (s *Store) ClearFailedByID(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	// Batched like the cancelled sweep, and for the same reason: one
	// statement naming every id would trip SQLite's bound-parameter ceiling
	// on a big enough batch and clear none of them.
	var total int64
	for start := 0; start < len(ids); start += idBatch {
		end := min(start+idBatch, len(ids))
		ph, args := placeholders(ids[start:end])
		res, err := s.db.Exec(
			`DELETE FROM transfers WHERE state='failed' AND id IN (`+ph+`)`, args...)
		if err != nil {
			return total, fmt.Errorf("clear failed: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// DstIsClaimed reports whether an unfinished row already writes this exact
// destination. "Keep both" asks the filesystem for a free name, but a name
// nothing has created yet can still be spoken for by another queued row —
// two clashes resolved in the same click would otherwise both be handed
// "ep01 (1).mkv" and the second would rename over the first.
func (s *Store) DstIsClaimed(dst string, direction string, siteID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM transfers
		 WHERE dst=? AND direction=? AND site_id=? AND state IN (`+unfinishedStates+`)`,
		dst, direction, siteID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("dst claimed: %w", err)
	}
	return n > 0, nil
}

// DeletePending removes a row that has not started, used to undo an enqueue
// whose follow-up write failed. Guarded on state and on zero progress so it
// can never remove a transfer that is running or that holds bytes.
func (s *Store) DeletePending(id int64) (int64, error) {
	res, err := s.db.Exec(
		`DELETE FROM transfers WHERE id=? AND state='pending' AND bytes_done=0`, id)
	if err != nil {
		return 0, fmt.Errorf("delete pending: %w", err)
	}
	return res.RowsAffected()
}

// OtherLiveTransfersForDst counts rows that still own the placeholder files
// at dst, ignoring every id in `going` — the rows the caller is about to
// remove. A destination can legitimately be queued twice (re-dragging a
// folder after an overnight run is the ordinary way it happens) and the
// placeholder path is derived from dst alone, so deleting a failed row's
// .wspart would silently reset a live duplicate to byte zero, or unlink a
// file an in-flight transfer is writing.
//
// `going` must be the WHOLE batch, not just the row being examined. When
// two rows of one batch share a destination they would otherwise each see
// the other as a live owner, both decline to delete, and both rows would
// then be removed — stranding the placeholder with nothing pointing at it,
// which is the exact outcome this guard exists to prevent.
//
// Completed rows are excluded: their placeholder was renamed away on
// success. Cancelled rows are excluded too: nothing is coming back for
// their bytes, so they can never be the reason to keep a placeholder — a
// cancelled sibling that counted as an owner blocked every later discard
// of the shared file, and a running row cancelled as part of a batch kept
// its full-size .wschunk on the seedbox because of the idle row cancelled
// beside it. Failed rows still count: their data is what a retry resumes.
func (s *Store) OtherLiveTransfersForDst(going []int64, dst string) (int, error) {
	ph, ids := placeholders(going)
	args := append([]any{dst}, ids...)
	q := `SELECT COUNT(*) FROM transfers WHERE dst=? AND state IN (` + placeholderOwnerStates + `)`
	if len(going) > 0 {
		q += ` AND id NOT IN (` + ph + `)`
	}
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("transfers for dst: %w", err)
	}
	return n, nil
}

// ClearCompleted removes completed rows. A completed transfer renamed its
// placeholder away on success, so there is nothing on disk to account for
// and no reason to name them individually.
func (s *Store) ClearCompleted() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM transfers WHERE state='completed'`)
	if err != nil {
		return 0, fmt.Errorf("clear completed: %w", err)
	}
	return res.RowsAffected()
}

// ClearCancelledByID removes the named cancelled rows. Cancelled rows can
// still have a placeholder on disk or on a server, so the caller names only
// the ones whose data it has actually accounted for — deleting the rest
// would delete the only record that those files exist.
func (s *Store) ClearCancelledByID(ids []int64) (int64, error) {
	// Batched like CancelByID: "Cancel all queued" can leave tens of
	// thousands of cancelled rows, and one statement naming them all would
	// exceed SQLite's bound-parameter ceiling and clear none of them.
	var total int64
	for start := 0; start < len(ids); start += idBatch {
		end := min(start+idBatch, len(ids))
		ph, args := placeholders(ids[start:end])
		res, err := s.db.Exec(
			`DELETE FROM transfers WHERE state='cancelled' AND id IN (`+ph+`)`, args...)
		if err != nil {
			return total, fmt.Errorf("clear cancelled: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

func collectTransfers(rows *sql.Rows) ([]Transfer, error) {
	out := make([]Transfer, 0, 16)
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan transfer: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// queuedStates are the rows waiting on the dispatcher: not running, not
// finished. "Cancel all queued" acts on exactly these — a running transfer
// is the user's to stop individually, and a finished one has nothing left
// to cancel.
const queuedStates = `'pending','dispatched','paused'`

// cancellableStates are the rows the dock's Cancel button is offered on:
// everything unfinished plus failed. A failed row is cancellable one at a
// time, so a bulk cancel must reach it too, or "Cancel 40 transfers" would
// quietly cancel none of a failed batch.
const cancellableStates = unfinishedStates + `,'failed'`

// placeholderOwnerStates are the rows that still have a claim on the
// .wspart/.wschunk files at a destination: everything unfinished, plus
// failed rows, whose data is exactly what a retry resumes from. Deliberately
// its own constant rather than a reference to cancellableStates, which the
// two currently coincide with: that one means "states the Cancel button is
// offered on", and quietly widening this one along with it would delete
// bytes a live row is still writing.
const placeholderOwnerStates = unfinishedStates + `,'failed'`

// requeueSet is the clean-slate reset every "put this row back in the
// queue" path applies: queued again, retry ladder wound back, stale error
// dropped. Byte progress is deliberately absent, so a requeued transfer
// resumes from its placeholder instead of starting over.
//
// One definition because the four callers — crash recovery, Retry failed,
// a queue pause, and the orphan heal — differ only in which rows they
// pick, and a reset that drifted between them would mean the same row came
// back with a different number of attempts depending on how it stopped.
// The caller binds updated_at; it was SQL-side in one of the four, which
// wrote that row's timestamp at a different precision from the rest.
const requeueSet = `SET state='pending', attempt=0, next_retry_at=NULL,
	error=NULL, updated_at=?`

// idBatch is how many ids one statement names. SQLite's bound-parameter
// ceiling is far higher, but a folder of five thousand files is an ordinary
// queue here and a single statement that size is no faster than ten.
const idBatch = 500

// TransfersByID returns the named rows, whatever their state, in claim
// order. The cancel sweep reads back the rows it has just marked with it.
func (s *Store) TransfersByID(ids []int64) ([]Transfer, error) {
	out := make([]Transfer, 0, len(ids))
	for start := 0; start < len(ids); start += idBatch {
		end := min(start+idBatch, len(ids))
		ph, args := placeholders(ids[start:end])
		rows, err := s.db.Query(
			`SELECT `+transferCols+` FROM transfers WHERE id IN (`+ph+`) ORDER BY id ASC`, args...)
		if err != nil {
			return nil, fmt.Errorf("transfers by id: %w", err)
		}
		got, err := collectTransfers(rows)
		rows.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

// CancelByID marks the named rows cancelled, and only while they are still
// cancellable. Explicit ids keep the confirmation honest, as ClearFailedByID
// does: the user approved the rows they were shown, and one that finished
// while the dialog sat open is not part of that consent. A running row is
// included on purpose — the dispatcher stops its goroutine afterwards, and
// the state must already say cancelled when the engine lets go, or
// finishWithError would treat the stop as a failure and retry it.
//
// The conflict column is cleared with the state: a cancelled row is no
// longer waiting for an answer, and every "held" check in the app keys on
// the column alone, so leaving it would keep the row in the decision bar
// and let "Overwrite all" act on a transfer the user just cancelled.
//
// Returns the ids the statement actually changed, not a count: the
// caller's follow-through (stop the goroutine, discard placeholders) must
// act on exactly those rows. A snapshot taken before the write is wrong in
// both directions — it names a row the pump claimed in between, whose
// placeholder is then unlinked under live lanes, and it misses a row that
// arrived in between, whose placeholder is then never discarded.
func (s *Store) CancelByID(ids []int64) ([]int64, error) {
	marked := make([]int64, 0, len(ids))
	for start := 0; start < len(ids); start += idBatch {
		end := min(start+idBatch, len(ids))
		ph, args := placeholders(ids[start:end])
		got, err := s.updateReturningIDs(
			`UPDATE transfers SET state='cancelled', conflict=NULL, updated_at=?
			 WHERE state IN (`+cancellableStates+`) AND id IN (`+ph+`) RETURNING id`,
			append([]any{nowUTC()}, args...)...)
		if err != nil {
			return marked, fmt.Errorf("cancel transfers: %w", err)
		}
		marked = append(marked, got...)
	}
	return marked, nil
}

// CancelQueued marks every waiting row cancelled in one statement, so a
// row enqueued between the caller's read and this write is cancelled too
// rather than left as the lone survivor of a "cancel everything". Running
// rows are untouched. Returns the ids it changed; see CancelByID.
func (s *Store) CancelQueued() ([]int64, error) {
	ids, err := s.updateReturningIDs(
		`UPDATE transfers SET state='cancelled', conflict=NULL, updated_at=?
		 WHERE state IN (`+queuedStates+`) RETURNING id`, nowUTC())
	if err != nil {
		return nil, fmt.Errorf("cancel queued: %w", err)
	}
	return ids, nil
}

// updateReturningIDs runs an UPDATE ... RETURNING id and collects the ids.
func (s *Store) updateReturningIDs(q string, args ...any) ([]int64, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Requeue returns a running row to pending from a clean slate — the same
// reset RecoverInterrupted applies on launch, for the same reason: pausing
// the queue or closing the app is not a transfer failure, so the row must
// not come back with attempts spent and a stale error attached. Byte
// progress is untouched. Reports whether the row was active; a row the
// user paused or cancelled in the meantime is left as they set it.
func (s *Store) Requeue(id int64) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE transfers `+requeueSet+` WHERE id=? AND state='active'`, nowUTC(), id)
	if err != nil {
		return false, fmt.Errorf("requeue transfer: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// placeholders builds the "?,?,?" list and argument slice for an IN clause.
func placeholders(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	ph := make([]byte, 0, len(ids)*2)
	for i, id := range ids {
		args[i] = id
		if i > 0 {
			ph = append(ph, ',')
		}
		ph = append(ph, '?')
	}
	return string(ph), args
}

// FinishActive writes a terminal state for a running row, and only while
// it is still running. The engine's goroutine is the caller: between its
// last read and this write the user may have cancelled or paused the row,
// and an unguarded write would overwrite that choice — a cancelled
// download flipping to completed, or a paused one to failed. Reports
// whether the write landed; the caller re-reads and settles otherwise.
func (s *Store) FinishActive(id int64, state string, errMsg *string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE transfers SET state=?, error=?, updated_at=? WHERE id=? AND state='active'`,
		state, errMsg, nowUTC(), id)
	if err != nil {
		return false, fmt.Errorf("finish transfer: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ScheduleRetryIfActive bumps attempt, sets the retry deadline and
// requeues — for a row that is still the engine's to schedule, and only
// then. Guarded like FinishActive, and for the same reason: the user can
// cancel or pause a transfer while its engine unwinds, and an unguarded
// write here would put the row they just cancelled back on the ladder.
// There is deliberately no unguarded variant to reach for.
func (s *Store) ScheduleRetryIfActive(id int64, nextRetryAt string, errMsg *string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE transfers SET state='pending', attempt=attempt+1, next_retry_at=?, error=?, updated_at=?
		 WHERE id=? AND state='active'`, nextRetryAt, errMsg, nowUTC(), id)
	if err != nil {
		return false, fmt.Errorf("schedule retry: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RequeueExcept returns every active row NOT in `running` to a clean
// pending. An active row with no goroutine behind it is an orphan: a
// requeue that failed to write while the queue paused, for instance. The
// pump lists only pending rows, so nothing in a running app would ever
// pick it up again; resuming the queue is the natural moment to heal it.
func (s *Store) RequeueExcept(running []int64) (int64, error) {
	ph, args := placeholders(running)
	q := `UPDATE transfers ` + requeueSet + ` WHERE state='active'`
	if len(running) > 0 {
		q += ` AND id NOT IN (` + ph + `)`
	}
	res, err := s.db.Exec(q, append([]any{nowUTC()}, args...)...)
	if err != nil {
		return 0, fmt.Errorf("requeue orphans: %w", err)
	}
	return res.RowsAffected()
}
