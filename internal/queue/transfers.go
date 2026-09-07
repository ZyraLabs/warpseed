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
}

var ErrTransferNotFound = errors.New("transfer not found")

const transferCols = `id,site_id,engine,direction,src,dst,size,state,priority,
	bytes_done,attempt,next_retry_at,error,src_mtime,created_at,updated_at,
	started_at,start_bytes`

func scanTransfer(row interface{ Scan(...any) error }) (Transfer, error) {
	var t Transfer
	err := row.Scan(&t.ID, &t.SiteID, &t.Engine, &t.Direction, &t.Src, &t.Dst,
		&t.Size, &t.State, &t.Priority, &t.BytesDone, &t.Attempt,
		&t.NextRetryAt, &t.Error, &t.SrcMtime, &t.CreatedAt, &t.UpdatedAt,
		&t.StartedAt, &t.StartBytes)
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
		 WHERE state='pending' AND (next_retry_at IS NULL OR next_retry_at <= ?)
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

// ClaimPending moves a row to active only while it is still pending, and
// reports whether it won. The dispatcher works from a list it read earlier,
// so by the time it claims a row the user may have paused or cancelled it —
// an unconditional write would silently resurrect a cancelled transfer and
// hand a running goroutine to a row the user believes is stopped.
func (s *Store) ClaimPending(id int64) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE transfers SET state='active', error=NULL, updated_at=?
		 WHERE id=? AND state='pending'`, nowUTC(), id)
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

// ScheduleRetry bumps attempt, sets the retry deadline, and requeues.
func (s *Store) ScheduleRetry(id int64, nextRetryAt string, errMsg *string) error {
	_, err := s.db.Exec(
		`UPDATE transfers SET state='pending', attempt=attempt+1, next_retry_at=?, error=?, updated_at=?
		 WHERE id=?`, nextRetryAt, errMsg, nowUTC(), id)
	if err != nil {
		return fmt.Errorf("schedule retry: %w", err)
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
	res, err := s.db.Exec(
		`UPDATE transfers SET state='pending', attempt=0, next_retry_at=NULL,
		 error=NULL, updated_at=? WHERE state='failed'`, nowUTC())
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
	args := make([]any, len(ids))
	ph := make([]byte, 0, len(ids)*2)
	for i, id := range ids {
		args[i] = id
		if i > 0 {
			ph = append(ph, ',')
		}
		ph = append(ph, '?')
	}
	res, err := s.db.Exec(
		`DELETE FROM transfers WHERE state='failed' AND id IN (`+string(ph)+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("clear failed: %w", err)
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
// success.
func (s *Store) OtherLiveTransfersForDst(going []int64, dst string) (int, error) {
	args := make([]any, 0, len(going)+1)
	args = append(args, dst)
	ph := make([]byte, 0, len(going)*2)
	for i, id := range going {
		if i > 0 {
			ph = append(ph, ',')
		}
		ph = append(ph, '?')
		args = append(args, id)
	}
	q := `SELECT COUNT(*) FROM transfers WHERE dst=? AND state<>'completed'`
	if len(going) > 0 {
		q += ` AND id NOT IN (` + string(ph) + `)`
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
	if len(ids) == 0 {
		return 0, nil
	}
	args := make([]any, len(ids))
	ph := make([]byte, 0, len(ids)*2)
	for i, id := range ids {
		args[i] = id
		if i > 0 {
			ph = append(ph, ',')
		}
		ph = append(ph, '?')
	}
	res, err := s.db.Exec(
		`DELETE FROM transfers WHERE state='cancelled' AND id IN (`+string(ph)+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("clear cancelled: %w", err)
	}
	return res.RowsAffected()
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
