package queue

import (
	"fmt"
)

// Chunk is one byte range of a multi-connection transfer. Rows are the
// resume checkpoints: a paused or crashed chunked transfer restarts each
// range at its own recorded offset, never from zero.
type Chunk struct {
	TransferID int64  `json:"transferId"`
	Idx        int    `json:"idx"`
	Offset     int64  `json:"offset"`
	Length     int64  `json:"length"`
	BytesDone  int64  `json:"bytesDone"`
	State      string `json:"state"`
}

// PlanChunks splits size into n contiguous ranges (the last absorbs the
// remainder). Returns nil when chunking does not apply.
func PlanChunks(transferID, size int64, n int) []Chunk {
	if n < 2 || size <= 0 {
		return nil
	}
	each := size / int64(n)
	if each == 0 {
		return nil
	}
	chunks := make([]Chunk, 0, n)
	for i := 0; i < n; i++ {
		off := int64(i) * each
		length := each
		if i == n-1 {
			length = size - off
		}
		chunks = append(chunks, Chunk{
			TransferID: transferID, Idx: i, Offset: off, Length: length, State: "pending",
		})
	}
	return chunks
}

// SaveChunks replaces the chunk plan for a transfer in one transaction.
func (s *Store) SaveChunks(transferID int64, chunks []Chunk) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin chunk plan: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM chunks WHERE transfer_id=?`, transferID); err != nil {
		return fmt.Errorf("clear chunks: %w", err)
	}
	for _, c := range chunks {
		if _, err := tx.Exec(
			`INSERT INTO chunks(transfer_id,idx,offset,length,bytes_done,state)
			 VALUES (?,?,?,?,?,?)`,
			transferID, c.Idx, c.Offset, c.Length, c.BytesDone, c.State); err != nil {
			return fmt.Errorf("insert chunk %d: %w", c.Idx, err)
		}
	}
	return tx.Commit()
}

// Chunks returns a transfer's chunk plan in index order.
func (s *Store) Chunks(transferID int64) ([]Chunk, error) {
	rows, err := s.db.Query(
		`SELECT transfer_id,idx,offset,length,bytes_done,state
		 FROM chunks WHERE transfer_id=? ORDER BY idx`, transferID)
	if err != nil {
		return nil, fmt.Errorf("list chunks: %w", err)
	}
	defer rows.Close()

	out := make([]Chunk, 0, 8)
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.TransferID, &c.Idx, &c.Offset, &c.Length,
			&c.BytesDone, &c.State); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateChunkProgress checkpoints one chunk's byte count.
func (s *Store) UpdateChunkProgress(transferID int64, idx int, bytesDone int64, state string) error {
	_, err := s.db.Exec(
		`UPDATE chunks SET bytes_done=?, state=? WHERE transfer_id=? AND idx=?`,
		bytesDone, state, transferID, idx)
	if err != nil {
		return fmt.Errorf("update chunk %d: %w", idx, err)
	}
	return nil
}

// DeleteChunks drops a transfer's plan (completed or restarted from zero).
func (s *Store) DeleteChunks(transferID int64) error {
	_, err := s.db.Exec(`DELETE FROM chunks WHERE transfer_id=?`, transferID)
	if err != nil {
		return fmt.Errorf("delete chunks: %w", err)
	}
	return nil
}
