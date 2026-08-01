package queue

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store is the queue-of-record. All transfer/site state lives here, never in
// engine memory (rclone jobids reset per process; WinSCP's non-persistent
// queue is a known complaint we deliberately fix).
type Store struct {
	db *sql.DB
}

// DefaultPath returns the per-user database location:
// %APPDATA%\warpseed\warpseed.db on Windows, ~/.config/warpseed/... elsewhere.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(dir, "warpseed", "warpseed.db"), nil
}

// Open opens (creating if needed) the SQLite store at path, enables WAL mode,
// and applies pending migrations transactionally.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// One writer connection avoids SQLITE_BUSY storms; readers are cheap.
	db.SetMaxOpenConns(1)

	pragmas := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for package-internal repositories
// (sites, transfers) added in later phases.
func (s *Store) DB() *sql.DB { return s.db }

// SchemaVersion reports the number of applied migrations.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&v)
	return v, err
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := s.SchemaVersion()
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	for i := applied; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}

// RecoverInterrupted demotes rows left in transient states by a crash or kill
// back to pending so the dispatcher re-runs them (approved plan: crash
// recovery on startup; resume semantics arrive with the engines).
func (s *Store) RecoverInterrupted() (int64, error) {
	res, err := s.db.Exec(`UPDATE transfers
		SET state='pending', updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE state IN ('dispatched','active')`)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted transfers: %w", err)
	}
	return res.RowsAffected()
}
