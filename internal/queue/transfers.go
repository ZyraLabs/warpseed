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
}

var ErrTransferNotFound = errors.New("transfer not found")

const transferCols = `id,site_id,engine,direction,src,dst,size,state,priority,
	bytes_done,attempt,next_retry_at,error,src_mtime,created_at,updated_at`

func scanTransfer(row interface{ Scan(...any) error }) (Transfer, error) {
	var t Transfer
	err := row.Scan(&t.ID, &t.SiteID, &t.Engine, &t.Direction, &t.Src, &t.Dst,
		&t.Size, &t.State, &t.Priority, &t.BytesDone, &t.Attempt,
		&t.NextRetryAt, &t.Error, &t.SrcMtime, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// SetTransferSrcMtime records the source mtime a chunk plan was built for.
func (s *Store) SetTransferSrcMtime(id, mtime int64) error {
	_, err := s.db.Exec(`UPDATE transfers SET src_mtime=? WHERE id=?`, mtime, id)
	if err != nil {
		return fmt.Errorf("set src mtime: %w", err)
	}
	return nil
}

// EnqueueTransfer inserts a pending row and returns its id.
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

// Transfers returns the newest rows for the queue UI.
func (s *Store) Transfers(limit int) ([]Transfer, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(
		`SELECT `+transferCols+` FROM transfers ORDER BY id DESC LIMIT ?`, limit)
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

// ClearFinished removes completed and cancelled rows.
func (s *Store) ClearFinished() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM transfers WHERE state IN ('completed','cancelled')`)
	if err != nil {
		return 0, fmt.Errorf("clear finished: %w", err)
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
