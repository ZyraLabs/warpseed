// Package hostkeys implements per-site host-key pinning with TOFU
// (approved plan: pinned keys, TOFU dialog, loud changed-key alarm).
package hostkeys

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrRejected is returned when the user (or prompt timeout) declines a new key.
var ErrRejected = errors.New("host key rejected by user")

// KeyChangedError is the alarm case: a pinned key no longer matches. Never
// auto-retried (core.ClassHostKey); the UI shows the loud dialog.
type KeyChangedError struct {
	Algo      string
	Pinned    string
	Presented string
}

func (e *KeyChangedError) Error() string {
	return fmt.Sprintf("HOST KEY CHANGED (%s): pinned %s, server presented %s",
		e.Algo, e.Pinned, e.Presented)
}

// Store persists pins in the queue database's hostkeys table.
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

// Callback builds an ssh.HostKeyCallback for one site. prompt is consulted
// only for unknown keys (TOFU); returning true pins the key. A nil prompt
// denies unknown keys outright.
func (s *Store) Callback(siteID int64, prompt func(algo, fingerprint string) bool) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		algo := key.Type()
		fp := ssh.FingerprintSHA256(key)

		var pinned string
		err := s.db.QueryRow(
			`SELECT sha256_fp FROM hostkeys WHERE site_id=? AND algo=?`, siteID, algo,
		).Scan(&pinned)

		switch {
		case errors.Is(err, sql.ErrNoRows):
			if prompt != nil && prompt(algo, fp) {
				if _, ierr := s.db.Exec(
					`INSERT INTO hostkeys(site_id, algo, sha256_fp, pinned_at) VALUES (?,?,?,?)`,
					siteID, algo, fp, time.Now().UTC().Format(time.RFC3339),
				); ierr != nil {
					return fmt.Errorf("pin host key: %w", ierr)
				}
				return nil
			}
			return ErrRejected
		case err != nil:
			return fmt.Errorf("host key lookup: %w", err)
		case pinned == fp:
			return nil
		default:
			return &KeyChangedError{Algo: algo, Pinned: pinned, Presented: fp}
		}
	}
}
