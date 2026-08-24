//go:build !js

package jobs

// Postgres-backed job store. Phase 1.3.x — gives multi-host Sky
// deployments at-least-once durability with correct concurrent-
// worker semantics.
//
// Why a separate backend from sqlite (vs "just use Postgres
// everywhere"): the dev / hobbyist / single-VM case shouldn't
// require a database server. The MVP goes:
//
//   * memory   — default; lost on restart; dev only
//   * sqlite   — opt-in via sky.toml [jobs] store = "sqlite"; file-
//                backed; single-host prod
//   * postgres — opt-in via sky.toml [jobs] store = "postgres";
//                multi-host prod
//
// All three implement the same Store interface so the worker code
// in jobs.go doesn't care.
//
// Concurrency model:
//   * SELECT ... FOR UPDATE SKIP LOCKED is the canonical Postgres
//     pattern for safe concurrent claim — every modern Postgres
//     job lib (Oban, GoodJob, faktory, river) uses it. Workers
//     across hosts hit the same row at the same time; SKIP LOCKED
//     means N-1 of them get the NEXT row instead of blocking.
//   * UPDATE sets claimed_at to the lease deadline so a crashed
//     worker's in-flight job gets re-claimed by a peer after the
//     lease expires (30 min default).
//
// Schema mirrors the SQLite backend's so dashboards / monitoring
// queries written against one work on both.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// postgresStore implements Store against a Postgres pool.
type postgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore connects to Postgres via the given URL
// (postgres://user:pass@host/db). Ensures schema. Returns the
// Store ready for use.
//
// Conn pool tuning is left to the URL (?pool_max_conns=10). v1.0
// uses pgx's defaults which work fine for job-queue workloads
// (~10 connections); high-volume workloads override via the URL.
func NewPostgresStore(url string) (Store, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	if err := initPostgresSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres init schema: %w", err)
	}
	return &postgresStore{pool: pool}, nil
}

func initPostgresSchema(ctx context.Context, pool *pgxpool.Pool) error {
	// BIGSERIAL for compatibility with old Postgres; modern Postgres
	// could use IDENTITY columns but BIGSERIAL works everywhere
	// from 9.x onwards.
	const ddl = `
CREATE TABLE IF NOT EXISTS _sky_jobs (
    id           BIGSERIAL PRIMARY KEY,
    queue        TEXT NOT NULL,
    name         TEXT NOT NULL,
    payload      BYTEA NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 0,
    next_run_at  TIMESTAMPTZ NOT NULL,
    enqueued_at  TIMESTAMPTZ NOT NULL,
    last_error   TEXT NOT NULL DEFAULT '',
    claimed_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS _sky_jobs_ready ON _sky_jobs (queue, next_run_at) WHERE claimed_at IS NULL;

CREATE TABLE IF NOT EXISTS _sky_jobs_dead (
    id           BIGINT PRIMARY KEY,
    queue        TEXT NOT NULL,
    name         TEXT NOT NULL,
    payload      BYTEA NOT NULL,
    attempts     INTEGER NOT NULL,
    enqueued_at  TIMESTAMPTZ NOT NULL,
    died_at      TIMESTAMPTZ NOT NULL,
    final_error  TEXT NOT NULL
);
`
	_, err := pool.Exec(ctx, ddl)
	return err
}

func (s *postgresStore) Enqueue(rec JobRecord) (JobID, error) {
	if rec.EnqueuedAt.IsZero() {
		rec.EnqueuedAt = time.Now()
	}
	if rec.NextRunAt.IsZero() {
		rec.NextRunAt = time.Now()
	}
	// BYTEA NOT NULL — same fix as the SQLite path: nil → empty.
	if rec.Payload == nil {
		rec.Payload = []byte{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO _sky_jobs (queue, name, payload, attempts, next_run_at, enqueued_at, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`,
		rec.Queue,
		rec.Name,
		rec.Payload,
		rec.Attempts,
		rec.NextRunAt,
		rec.EnqueuedAt,
		rec.LastError,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("postgres enqueue: %w", err)
	}
	return JobID(id), nil
}

// Claim uses SELECT ... FOR UPDATE SKIP LOCKED — the canonical
// safe-concurrent-worker pattern. SKIP LOCKED means concurrent
// workers don't block each other on a single hot row; each picks
// the NEXT available row.
//
// The lease mechanism (claimed_at < now - 30min recoverable) lets
// a crashed worker's job get re-dispatched by a peer after the
// lease expires.
func (s *postgresStore) Claim(queue string, now time.Time) (JobRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	leaseExpiry := now.Add(-30 * time.Minute)
	var rec JobRecord
	err := s.pool.QueryRow(ctx, `
		WITH picked AS (
			SELECT id FROM _sky_jobs
			WHERE  queue = $1
			AND    next_run_at <= $2
			AND    (claimed_at IS NULL OR claimed_at < $3)
			ORDER  BY next_run_at ASC
			FOR    UPDATE SKIP LOCKED
			LIMIT  1
		)
		UPDATE _sky_jobs
		SET    claimed_at = $2
		FROM   picked
		WHERE  _sky_jobs.id = picked.id
		RETURNING _sky_jobs.id, _sky_jobs.queue, _sky_jobs.name,
		          _sky_jobs.payload, _sky_jobs.attempts,
		          _sky_jobs.next_run_at, _sky_jobs.enqueued_at,
		          _sky_jobs.last_error
	`, queue, now, leaseExpiry).Scan(
		&rec.ID, &rec.Queue, &rec.Name, &rec.Payload,
		&rec.Attempts, &rec.NextRunAt, &rec.EnqueuedAt, &rec.LastError,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return JobRecord{}, ErrNoJob
	}
	if err != nil {
		return JobRecord{}, fmt.Errorf("postgres claim: %w", err)
	}
	return rec, nil
}

func (s *postgresStore) Complete(id JobID) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.pool.Exec(ctx, `DELETE FROM _sky_jobs WHERE id = $1`, int64(id))
	if err != nil {
		return fmt.Errorf("postgres complete: %w", err)
	}
	return nil
}

func (s *postgresStore) Reschedule(rec JobRecord) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		UPDATE _sky_jobs
		SET    attempts    = $1,
		       next_run_at = $2,
		       last_error  = $3,
		       claimed_at  = NULL
		WHERE  id = $4
	`,
		rec.Attempts,
		rec.NextRunAt,
		rec.LastError,
		int64(rec.ID),
	)
	if err != nil {
		return fmt.Errorf("postgres reschedule: %w", err)
	}
	return nil
}

func (s *postgresStore) DeadLetter(id JobID, finalError string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres dlq begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var queue, name, lastError string
	var payload []byte
	var attempts int
	var enqAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT queue, name, payload, attempts, enqueued_at, last_error
		FROM   _sky_jobs WHERE id = $1
	`, int64(id)).Scan(&queue, &name, &payload, &attempts, &enqAt, &lastError)
	if errors.Is(err, pgx.ErrNoRows) {
		// Idempotent — already gone.
		return nil
	}
	if err != nil {
		return fmt.Errorf("postgres dlq read: %w", err)
	}

	combinedError := finalError
	if lastError != "" {
		combinedError = finalError + " (last attempt: " + lastError + ")"
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO _sky_jobs_dead
		    (id, queue, name, payload, attempts, enqueued_at, died_at, final_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO NOTHING
	`, int64(id), queue, name, payload, attempts, enqAt, time.Now(), combinedError)
	if err != nil {
		return fmt.Errorf("postgres dlq insert: %w", err)
	}

	_, err = tx.Exec(ctx, `DELETE FROM _sky_jobs WHERE id = $1`, int64(id))
	if err != nil {
		return fmt.Errorf("postgres dlq delete: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *postgresStore) Cancel(id JobID) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM _sky_jobs WHERE id = $1 AND claimed_at IS NULL
	`, int64(id))
	if err != nil {
		return fmt.Errorf("postgres cancel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrJobNotFound
	}
	return nil
}

func (s *postgresStore) QueueDepth(queue string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM _sky_jobs WHERE queue = $1
	`, queue).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("postgres queue_depth: %w", err)
	}
	return n, nil
}

// Close releases the pool. Called from JobsShutdown when the
// Postgres backend is active.
func (s *postgresStore) Close() {
	s.pool.Close()
}
