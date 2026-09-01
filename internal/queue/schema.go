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

	// 002 — site editor fields (initial remote path, per-site transfer cap)
	// and transfer/bandwidth/theme setting defaults (feedback batch 2026-08-02)
	`
	ALTER TABLE sites ADD COLUMN remote_path TEXT NOT NULL DEFAULT '';
	ALTER TABLE sites ADD COLUMN max_transfers INTEGER NOT NULL DEFAULT 0;
	INSERT OR IGNORE INTO settings(key,value) VALUES
		('transfers.global_max','6'),
		('transfers.site_max','3'),
		('bw.mode','off'),
		('bw.limit_bytes','0'),
		('bw.percent','80'),
		('bw.observed_max','0'),
		('ui.theme','dark');
	`,

	// 003 — multi-connection chunked downloads: a single large file spans
	// several connections to beat a server's per-connection speed cap.
	`
	INSERT OR IGNORE INTO settings(key,value) VALUES
		('transfers.chunk_min_mb','256'),
		('transfers.chunk_streams','4');
	`,

	// 004 — record the source mtime a chunk plan was built against so a
	// resume can detect a file that changed on the server, and raise the
	// per-site default so the shipped chunk stream count is achievable.
	`
	ALTER TABLE transfers ADD COLUMN src_mtime INTEGER NOT NULL DEFAULT 0;
	UPDATE settings SET value='6' WHERE key='transfers.site_max' AND value='3';
	`,

	// 005 — saved folder bookmarks. site_id 0 means the local filesystem, so
	// one table serves both panes without a nullable foreign key.
	`
	CREATE TABLE bookmarks (
		id         INTEGER PRIMARY KEY,
		site_id    INTEGER NOT NULL DEFAULT 0,
		path       TEXT NOT NULL,
		label      TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		UNIQUE (site_id, path)
	);
	INSERT OR IGNORE INTO settings(key,value) VALUES ('ui.local_default','');
	`,

	// 006 — named themes replace the plain dark/light pair.
	`
	UPDATE settings SET value='flightdeck' WHERE key='ui.theme' AND value='dark';
	UPDATE settings SET value='drafting'   WHERE key='ui.theme' AND value='light';
	`,

	// 007 — multi-connection chunked uploads. Separate lane settings from
	// downloads: the uplink is ~12 MiB/s against a ~5 MiB/s per-connection
	// cap, so ~3 lanes saturate it and more is pure connection overhead,
	// while the downlink (~110 MiB/s against the same cap) wants many more.
	`
	INSERT OR IGNORE INTO settings(key,value) VALUES
		('transfers.upload_chunk_min_mb','128'),
		('transfers.upload_chunk_streams','3');
	`,
}
