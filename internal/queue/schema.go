package queue

// Migrations are applied in order at startup. Never edit an entry after it
// has shipped in a release — append a new one instead.
var migrations = []string{
	// 001 — initial schema (approved plan, Part 2: queue-of-record)
	`
	CREATE TABLE sites (
		id           INTEGER PRIMARY KEY,
		name         TEXT NOT NULL UNIQUE,
		protocol     TEXT NOT NULL,             -- sftp | ftp | ftps | s3 | webdav
		host         TEXT NOT NULL,
		port         INTEGER NOT NULL DEFAULT 22,
		username     TEXT NOT NULL DEFAULT '',
		cred_ref     TEXT NOT NULL DEFAULT '',  -- reference into Windows Credential Manager, never a secret
		options_json TEXT NOT NULL DEFAULT '{}',-- per-site tuning: concurrency, chunk_size, max_connections…
		created_at   TEXT NOT NULL,
		updated_at   TEXT NOT NULL
	);

	CREATE TABLE transfers (
		id            INTEGER PRIMARY KEY,
		site_id       INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
		engine        TEXT NOT NULL,            -- sftpfast | rclone
		direction     TEXT NOT NULL,            -- download | upload
		src           TEXT NOT NULL,
		dst           TEXT NOT NULL,
		size          INTEGER NOT NULL DEFAULT -1,
		state         TEXT NOT NULL DEFAULT 'pending',
		priority      INTEGER NOT NULL DEFAULT 0,
		bytes_done    INTEGER NOT NULL DEFAULT 0,
		attempt       INTEGER NOT NULL DEFAULT 0,
		next_retry_at TEXT,
		error         TEXT,
		rclone_jobid  INTEGER,                  -- never trusted across restarts
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL,
		CHECK (state IN ('pending','dispatched','active','completed','paused','failed','cancelled'))
	);
	CREATE INDEX idx_transfers_state    ON transfers(state, priority DESC, id);
	CREATE INDEX idx_transfers_site     ON transfers(site_id, state);

	CREATE TABLE chunks (
		transfer_id INTEGER NOT NULL REFERENCES transfers(id) ON DELETE CASCADE,
		idx         INTEGER NOT NULL,
		offset      INTEGER NOT NULL,
		length      INTEGER NOT NULL,
		bytes_done  INTEGER NOT NULL DEFAULT 0,
		state       TEXT NOT NULL DEFAULT 'pending',
		PRIMARY KEY (transfer_id, idx)
	);

	CREATE TABLE hostkeys (
		site_id    INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
		algo       TEXT NOT NULL,
		sha256_fp  TEXT NOT NULL,               -- SHA256:base64 fingerprint as shown to the user
		pinned_at  TEXT NOT NULL,
		PRIMARY KEY (site_id, algo)
	);

	CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	`,
}
