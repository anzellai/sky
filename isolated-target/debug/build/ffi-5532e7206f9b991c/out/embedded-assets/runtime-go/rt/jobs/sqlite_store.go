package jobs

// SQLite-backed job store. Phase 1.3.x — gives single-host Sky
// deployments at-least-once durability across restarts.
//
// Schema:
//
//   CREATE TABLE _sky_jobs (
//       id           INTEGER PRIMARY KEY AUTOINCREMENT,
//       queue        TEXT NOT NULL,
//       name         TEXT NOT NULL,
//       payload      BLOB NOT NULL,
//       attempts     INTEGER NOT NULL DEFAULT 0,
//       next_run_at  INTEGER NOT NULL,   -- unix nanos
//       enqueued_at  INTEGER NOT NULL,
//       last_error   TEXT NOT NULL DEFAULT '',
//       claimed_at   INTEGER NOT NULL DEFAULT 0  -- non-zero when in-flight
//   );
//   CREATE INDEX _sky_jobs_ready ON _sky_jobs (queue, next_run_at, claimed_at);
//
//   CREATE TABLE _sky_jobs_dead (
//       id           INTEGER PRIMARY KEY,
//       queue        TEXT NOT NULL,
//       name         TEXT NOT NULL,
//       payload      BLOB NOT NULL,
//       attempts     INTEGER NOT NULL,
//       enqueued_at  INTEGER NOT NULL,
//       died_at      INTEGER NOT NULL,
//       final_error  TEXT NOT NULL
//   );
//
// Why SEPARATE _sky_jobs file from the user's data DB:
//   * Telemetry write workload (every retry = WRITE; queue depth
//     scans = READ) competes with user's `users` / `orders` writes.
//   * Backup bloat — telemetry rows mean a 7-day backup retention
//     carries weeks of dead job history.
//   * Schema migrations on the user's DB must NOT be entangled
//     with our internal schema.
//
// Configured via sky.toml [jobs] store_path = "./_sky/jobs.db"
// (default when [jobs] store = "sqlite" but no path given).
//
// Concurrency: SQLite WAL mode + per-connection serialisation in
// Go's database/sql. Claim uses BEGIN IMMEDIATE + UPDATE...
// RETURNING (SQLite 3.35+) to atomically lock + read the job. The
// modernc.org/sqlite driver bundled with Sky uses recent SQLite so
// RETURNING is available; we don't need a fallback.

import (
	"database/sql"
	"fmt"
	"time"

	// SQLite driver (pure-Go, already a Sky runtime dep — same one
	// Std.Db uses; no extra binary growth).
	_ "modernc.org/sqlite"
)

// sqliteStore implements Store against a SQLite database.
type sqliteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) the SQLite database at `path`
// and ensures the schema is present. The file is auto-created;
// parent directory must exist (caller's responsibility — typically
// sky.toml [jobs] store_path is e.g. "./_sky/jobs.db" and Sky
// init created the dir).
//
// Tuning: WAL mode for concurrent reader/writer, synchronous=NORMAL
// for the durability/perf trade-off most apps want (fsync on
// commit boundary, not per-page). Closing busy_timeout to 5000ms so
// concurrent workers don't immediately fail on "database is locked"
// — they retry transparently within the timeout window.
func NewSQLiteStore(path string) (Store, error) {
	// _txlock=immediate so BEGIN IMMEDIATE is the default for
	// every transaction — matches our Claim path's lock semantics.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	// Single connection serialises writes — SQLite is fastest
	// when there's no write contention; ~5k inserts/sec on
	// commodity hardware. Plenty for job-queue workloads.
	db.SetMaxOpenConns(1)
	if err := initSQLiteSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite init schema: %w", err)
	}
	return &sqliteStore{db: db}, nil
}

func initSQLiteSchema(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS _sky_jobs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    queue        TEXT NOT NULL,
    name         TEXT NOT NULL,
    payload      BLOB NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 0,
    next_run_at  INTEGER NOT NULL,
    enqueued_at  INTEGER NOT NULL,
    last_error   TEXT NOT NULL DEFAULT '',
    claimed_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS _sky_jobs_ready ON _sky_jobs (queue, claimed_at, next_run_at);

CREATE TABLE IF NOT EXISTS _sky_jobs_dead (
    id           INTEGER PRIMARY KEY,
    queue        TEXT NOT NULL,
    name         TEXT NOT NULL,
    payload      BLOB NOT NULL,
    attempts     INTEGER NOT NULL,
    enqueued_at  INTEGER NOT NULL,
    died_at      INTEGER NOT NULL,
    final_error  TEXT NOT NULL
);
`
	_, err := db.Exec(ddl)
	return err
}

func (s *sqliteStore) Enqueue(rec JobRecord) (JobID, error) {
	if rec.EnqueuedAt.IsZero() {
		rec.EnqueuedAt = time.Now()
	}
	if rec.NextRunAt.IsZero() {
		rec.NextRunAt = time.Now()
	}
	// payload BLOB NOT NULL — nil from the Go side ends up as NULL
	// and the INSERT silently rejects. Normalise to empty bytes so
	// no-payload jobs (e.g. timer ticks with no carrier data) work
	// uniformly across backends.
	if rec.Payload == nil {
		rec.Payload = []byte{}
	}
	res, err := s.db.Exec(`
		INSERT INTO _sky_jobs (queue, name, payload, attempts, next_run_at, enqueued_at, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		rec.Queue,
		rec.Name,
		rec.Payload,
		rec.Attempts,
		rec.NextRunAt.UnixNano(),
		rec.EnqueuedAt.UnixNano(),
		rec.LastError,
	)
	if err != nil {
		return 0, fmt.Errorf("sqlite enqueue: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqlite enqueue lastinsertid: %w", err)
	}
	return JobID(id), nil
}

// Claim atomically picks the oldest-ready job in `queue`, marks
// it in-flight (claimed_at > 0) so concurrent workers don't double-
// dispatch, and returns the full record. SQLite 3.35+'s
// `UPDATE ... RETURNING` does both in one round trip.
//
// "Oldest ready" = lowest next_run_at where claimed_at = 0 AND
// next_run_at <= now. Index `_sky_jobs_ready` on (queue, claimed_at,
// next_run_at) makes this an O(log n) lookup.
//
// Hung-worker recovery: claimed_at acts as a lease. After 30 min
// of inflight without a Complete/Reschedule/DeadLetter call (a
// crashed worker), we re-claim. Implemented via the `OR claimed_at
// < now - lease` branch in the WHERE.
func (s *sqliteStore) Claim(queue string, now time.Time) (JobRecord, error) {
	nowNs := now.UnixNano()
	const leaseNs = int64(30 * 60 * 1e9) // 30 minutes
	row := s.db.QueryRow(`
		UPDATE _sky_jobs
		SET    claimed_at = ?
		WHERE  id = (
			SELECT id FROM _sky_jobs
			WHERE  queue = ?
			AND    next_run_at <= ?
			AND    (claimed_at = 0 OR claimed_at < ?)
			ORDER  BY next_run_at ASC
			LIMIT  1
		)
		RETURNING id, queue, name, payload, attempts, next_run_at, enqueued_at, last_error
	`, nowNs, queue, nowNs, nowNs-leaseNs)

	var rec JobRecord
	var nextNs, enqNs int64
	err := row.Scan(&rec.ID, &rec.Queue, &rec.Name, &rec.Payload,
		&rec.Attempts, &nextNs, &enqNs, &rec.LastError)
	if err == sql.ErrNoRows {
		return JobRecord{}, ErrNoJob
	}
	if err != nil {
		return JobRecord{}, fmt.Errorf("sqlite claim: %w", err)
	}
	rec.NextRunAt = time.Unix(0, nextNs)
	rec.EnqueuedAt = time.Unix(0, enqNs)
	return rec, nil
}

func (s *sqliteStore) Complete(id JobID) error {
	_, err := s.db.Exec(`DELETE FROM _sky_jobs WHERE id = ?`, int64(id))
	if err != nil {
		return fmt.Errorf("sqlite complete: %w", err)
	}
	return nil
}

func (s *sqliteStore) Reschedule(rec JobRecord) error {
	// UPDATE in place: bump attempts, set next_run_at + last_error,
	// clear the claim so the worker picks it up again at backoff time.
	_, err := s.db.Exec(`
		UPDATE _sky_jobs
		SET    attempts    = ?,
		       next_run_at = ?,
		       last_error  = ?,
		       claimed_at  = 0
		WHERE  id = ?
	`,
		rec.Attempts,
		rec.NextRunAt.UnixNano(),
		rec.LastError,
		int64(rec.ID),
	)
	if err != nil {
		return fmt.Errorf("sqlite reschedule: %w", err)
	}
	return nil
}

func (s *sqliteStore) DeadLetter(id JobID, finalError string) error {
	// Move the row from _sky_jobs to _sky_jobs_dead atomically.
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("sqlite dlq begin: %w", err)
	}
	defer tx.Rollback()

	// Read the source row.
	var queue, name, lastError string
	var payload []byte
	var attempts int
	var enqNs int64
	err = tx.QueryRow(`
		SELECT queue, name, payload, attempts, enqueued_at, last_error
		FROM   _sky_jobs WHERE id = ?
	`, int64(id)).Scan(&queue, &name, &payload, &attempts, &enqNs, &lastError)
	if err == sql.ErrNoRows {
		// Already gone — DLQ is idempotent, treat as success.
		return nil
	}
	if err != nil {
		return fmt.Errorf("sqlite dlq read: %w", err)
	}

	// Combine the last attempt's error with the supplied final error
	// for the post-mortem chain.
	combinedError := finalError
	if lastError != "" {
		combinedError = finalError + " (last attempt: " + lastError + ")"
	}

	_, err = tx.Exec(`
		INSERT INTO _sky_jobs_dead
		    (id, queue, name, payload, attempts, enqueued_at, died_at, final_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, int64(id), queue, name, payload, attempts, enqNs, time.Now().UnixNano(), combinedError)
	if err != nil {
		return fmt.Errorf("sqlite dlq insert: %w", err)
	}

	_, err = tx.Exec(`DELETE FROM _sky_jobs WHERE id = ?`, int64(id))
	if err != nil {
		return fmt.Errorf("sqlite dlq delete: %w", err)
	}

	return tx.Commit()
}

func (s *sqliteStore) Cancel(id JobID) error {
	res, err := s.db.Exec(`
		DELETE FROM _sky_jobs WHERE id = ? AND claimed_at = 0
	`, int64(id))
	if err != nil {
		return fmt.Errorf("sqlite cancel: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Either the id doesn't exist or the job is already in-
		// flight (claimed). Match memoryStore's contract: return
		// ErrJobNotFound — caller can decide whether to retry.
		return ErrJobNotFound
	}
	return nil
}

func (s *sqliteStore) QueueDepth(queue string) (int, error) {
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM _sky_jobs WHERE queue = ?
	`, queue).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sqlite queue_depth: %w", err)
	}
	return n, nil
}

// Close releases the database connection. Tests call this; the
// production runtime calls it from JobsShutdown when the SQLite
// backend is active.
func (s *sqliteStore) Close() error {
	return s.db.Close()
}
