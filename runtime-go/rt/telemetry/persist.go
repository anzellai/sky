// Package telemetry — write-through persistence to a SQLite file.
//
// When `SKY_CONSOLE_DB_PATH` is set (SkyDeploy injects this on Pro+
// tenants — see docs/phase-3d-console-persistence.md), the Store
// dual-writes every RecordLog / RecordMetric / RecordTrace call to a
// `/data/console.db` SQLite file in addition to the in-RAM ring
// buffers.  The console mini-app (running in-process or under a
// reverse proxy) reads from that file to serve the Logs / Metrics /
// Traces tabs.  When the env var is UNSET (dev mode, OSS) the store
// behaves exactly as before — pure in-RAM.
//
// Design notes:
//
//   - Lazy open.  The DB handle is opened on first telemetry write
//     after `EnsurePersistence` is called from the runtime entry
//     point; tests that never write telemetry don't touch the file.
//
//   - Buffered + async writer.  A 1024-deep channel feeds a single
//     flusher goroutine.  Errors are logged warn-level to the in-RAM
//     ring (visible at /_sky/console even when the DB write fails)
//     and DO NOT block the in-RAM hot path — per design, the
//     observability surface must never poison the request path.
//
//   - WAL mode.  Enables concurrent readers (the console mini-app)
//     while we write.  Matches the convention used by `live_store.go`.
//
//   - TTL pruning.  An hourly goroutine deletes telemetry_log /
//     telemetry_span rows older than 24 h and telemetry_metric rows
//     older than 7 d.  Matches the retention contract documented in
//     `control-plane/static/console.db.schema.sql`.
//
//   - Schema is embedded as a Go string literal so the runtime carries
//     its own copy.  The SkyDeploy schema file is the human reference
//     but is NOT read at runtime.

package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // Postgres driver "pgx" (one-DB-for-everything)
	_ "modernc.org/sqlite"

	"sky-app/rt/dbshare"
)

// Connection-pool bounds for the telemetry persistence handle. Small and
// fixed: a single buffered flusher goroutine does all the writing, so the
// pool exists to survive a burst and to bound the damage, not to scale.
// See EnsurePersistence for why the deployment-aware sizing in
// `rt/db_pool.go` cannot be reached from this package.
//
// `PoolMaxConns` is EXPORTED because it is a term in the process's
// connection-demand arithmetic (`dbProcessConnectionDemand`), which sizes every
// cluster Sky generates. That arithmetic reads this constant rather than
// restating it: telemetry is the package that hands the number to
// `dbshare.Acquire`, so this is where the number lives.
const (
	PoolMaxConns          = 4
	telemetryPoolLifetime = 30 * time.Minute
	telemetryPoolIdleTime = 60 * time.Second
)

// The console telemetry store (logs / metrics / spans) — the embedded schema
// mirrors `control-plane/static/console.db.schema.sql` in SkyDeploy. v0.19 makes
// it dialect-aware so it can share the app's Postgres (one DB for everything);
// SQLite stays the default. Only the write path lives here — the console
// mini-app / SkyDeploy own the reads.
//
// telemetryBackend classifies a configured path into a (driver, dsn) pair —
// a postgres:// URL uses the shared pgx driver (one DB for everything); anything
// else is a local SQLite file.
func telemetryBackend(path string) (driver, dsn string) {
	if strings.HasPrefix(path, "postgres://") || strings.HasPrefix(path, "postgresql://") {
		return "pgx", path
	}
	return "sqlite", path
}

// telemetryQ rewrites `?` placeholders to `$1,$2,…` for pgx; SQLite keeps `?`.
func telemetryQ(driver, sql string) string {
	if driver != "pgx" {
		return sql
	}
	var b strings.Builder
	n := 0
	for _, r := range sql {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// consoleDBSchemaStmts returns the CREATE statements for the driver, run
// individually (pgx doesn't batch `;`-separated statements). Only the id
// column, the timestamp DEFAULT, and the float type differ; the inserts always
// supply created_at/observed_at, so dropping the default on Postgres is safe.
func consoleDBSchemaStmts(driver string) []string {
	logID := "id INTEGER PRIMARY KEY AUTOINCREMENT"
	tsDefault := " DEFAULT (datetime('now'))"
	valueType := "REAL"
	if driver == "pgx" {
		logID = "id BIGSERIAL PRIMARY KEY"
		tsDefault = ""
		valueType = "DOUBLE PRECISION"
	}
	return []string{
		`CREATE TABLE IF NOT EXISTS telemetry_log (
			` + logID + `,
			namespace  TEXT NOT NULL DEFAULT '',
			level      TEXT NOT NULL,
			message    TEXT NOT NULL,
			attrs      TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL` + tsDefault + `
		)`,
		`CREATE TABLE IF NOT EXISTS telemetry_metric (
			name        TEXT NOT NULL,
			labels      TEXT NOT NULL DEFAULT '{}',
			value       ` + valueType + ` NOT NULL,
			observed_at TEXT NOT NULL` + tsDefault + `
		)`,
		`CREATE TABLE IF NOT EXISTS telemetry_span (
			id         TEXT NOT NULL,
			trace_id   TEXT NOT NULL,
			parent_id  TEXT NOT NULL DEFAULT '',
			name       TEXT NOT NULL,
			started_at TEXT NOT NULL,
			ended_at   TEXT NOT NULL,
			attrs      TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_log_created ON telemetry_log (created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_metric_observed ON telemetry_metric (name, observed_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_span_started ON telemetry_span (started_at DESC)`,
	}
}

// openTelemetryPool opens the persistence handle.
//
// On PostgreSQL it goes through the shared registry (dbshare), so when the
// telemetry DSN resolves to the same string as the app's analytics or session
// store — the normal case, since this store's own fallback is the shared
// `DATABASE_URL` — the consumers draw on ONE set of backends instead of
// three. Before this, the telemetry writer opened its own pool against the
// same server and competed with the app's queries for the same
// `max_connections` budget: observability taking down the thing it observes.
//
// The cap (`Share`) is what preserves the bulkhead that separate
// pools used to provide: a telemetry burst can hold at most that many of the
// shared pool, so it cannot starve the session store on the request path.
//
// On SQLite it keeps its own handle with the small plural pool it has always
// had. That is deliberate and it is NOT the single-connection clamp used
// elsewhere: this store is read by the console mini-app while being written
// here, and a one-connection pool turns any read issued while another query's
// rows are still open into a self-deadlock. Bounded-but-plural was the fix;
// unbounded was the defect. It also means the SQLite handle cannot be shared
// with analytics, which wants the single-connection clamp — one handle cannot
// be both.
func openTelemetryPool(driver, dsn string) (*sql.DB, *dbshare.Handle, error) {
	if driver != "pgx" {
		db, err := sql.Open(driver, dsn)
		if err != nil {
			return nil, nil, err
		}
		db.SetMaxOpenConns(PoolMaxConns)
		db.SetMaxIdleConns(PoolMaxConns)
		db.SetConnMaxLifetime(telemetryPoolLifetime)
		db.SetConnMaxIdleTime(telemetryPoolIdleTime)
		return db, nil, nil
	}
	// The deployment-aware sizing lives in `rt` (db_pool.go), which imports
	// THIS package, so it cannot be called from here without an import cycle.
	// The registry is a leaf package precisely so both sides can reach it;
	// `rt` passes its sizing in when IT acquires, and whichever consumer gets
	// there first sizes the shared pool. The fixed numbers below are the
	// floor this package can justify alone.
	return acquireShared(driver, dsn)
}

func acquireShared(driver, dsn string) (*sql.DB, *dbshare.Handle, error) {
	h, err := dbshare.Acquire("telemetry", driver, dsn, dbshare.Config{
		MaxOpenConns:    PoolMaxConns,
		MaxIdleConns:    PoolMaxConns,
		ConnMaxLifetime: telemetryPoolLifetime,
		ConnMaxIdleTime: telemetryPoolIdleTime,
	}, Share)
	if err != nil {
		return nil, nil, err
	}
	return h.DB(), h, nil
}

// persistTx is the subset of a transaction writeBatch uses, so the capped and
// uncapped paths are interchangeable.
type persistTx interface {
	Prepare(string) (*sql.Stmt, error)
	Exec(string, ...any) (sql.Result, error)
	Commit() error
	Rollback() error
}

func (p *persistence) begin() (persistTx, error) {
	if p.pool != nil {
		return p.pool.Begin()
	}
	return p.db.Begin()
}

// closeTelemetryPool releases this consumer's claim on the pool.
//
// On PostgreSQL that is a refcount decrement, NOT a close: another consumer
// may be serving requests through the same pool, and closing it under them
// would surface as `sql: database is closed` in a subsystem nobody asked to
// stop. On SQLite the handle is this store's alone, so it really does close.
func closeTelemetryPool(db *sql.DB, h *dbshare.Handle) {
	if h != nil {
		_ = h.Close()
		return
	}
	if db != nil {
		_ = db.Close()
	}
}

// Share bounds how much of a SHARED pool this writer may hold at once, and it
// is the ONLY definition of that number.
//
// It used to be declared twice — here and as `dbTelemetryShare` in
// rt/db_pool.go, where the bulkhead arithmetic lives — with a gate asserting
// the two agreed. A gate that compares two copies proves the copies, not the
// property: the shared pool's size is `aux + analyticsShare + telemetryShare`,
// and rt now reads this constant to compute it, so there is nothing left to
// drift. The import direction permits it: rt imports telemetry.
//
// Two is enough because the writer is a single batching goroutine: one slot
// for the flush, one for the hourly prune.
const Share = 2

// persistEnvVar is the env var SkyDeploy injects on Pro+ tenants.
// When set + non-empty, the store dual-writes to the SQLite file.
const persistEnvVar = "SKY_CONSOLE_DB_PATH"

// persistQueueCap bounds the buffered channel of pending writes.
// Sized to ~1 s of telemetry at expected peak (~1 k events/s for a
// busy app); a sustained overflow surfaces as a warn-level entry in
// the in-RAM log ring (no panic, no block).
const persistQueueCap = 1024

// persistBatchSize is the entry count that triggers a flush on size.
const persistBatchSize = 128

// persistFlushWait caps how long a caller of FlushPersistence waits for the
// writer to drain before proceeding anyway. A caller that blocked indefinitely
// on a wedged writer would turn a degraded telemetry store into a hung test or
// a hung console page.
const persistFlushWait = 5 * time.Second

// persistFlushInterval is the flush-on-time half of "size or interval,
// whichever comes first". It bounds how long an entry can sit unwritten on an
// app that is not busy enough to fill a batch.
//
// It is a function over a var, rather than a const, for one reason: it is the
// knob that proves FlushPersistence is a real synchronisation and not a race
// that happens to be won. Until this commit the helper polled for an empty
// queue and then slept 250 ms, which was correct only while this interval
// stayed BELOW that sleep — two unrelated constants in different functions,
// neither documenting its dependence on the other. Raising this to 300 ms
// turned three tests red with exactly the counts CI reported (86/85/85, and 0
// of 1), because an empty queue means the flusher has taken the entries, not
// that it has committed them. `TestPersistence_FlushIsSynchronisedNotTimed`
// now pins the interval to an hour and still expects every row, so a
// regression to any interval-dependent flush fails immediately and locally
// rather than intermittently on a loaded runner.
var persistFlushIntervalOverride atomic.Int64

func persistFlushInterval() time.Duration {
	if d := persistFlushIntervalOverride.Load(); d > 0 {
		return time.Duration(d)
	}
	return 200 * time.Millisecond
}

// persistEntry — a single record queued for the flusher goroutine.
// The `kind` field discriminates the variant; only the matching
// payload field is populated per entry.
type persistEntry struct {
	kind   string // "log" | "metric" | "span"
	log    LogEntry
	metric persistMetric
	span   TraceEntry
}

type persistMetric struct {
	name       string
	labels     map[string]string
	value      float64
	observedAt time.Time
}

// persistence wraps the DB handle + writer goroutine.  One per Store.
// nil when SKY_CONSOLE_DB_PATH is unset (in-RAM-only).
type persistence struct {
	db *sql.DB
	// pool is this consumer's reference into the shared registry (nil on
	// SQLite, which keeps its own handle). Closing it releases the reference;
	// the pool closes only when the last consumer lets go.
	pool   *dbshare.Handle
	driver string // "sqlite" or "pgx" — drives placeholder style
	// syncCommitOff asks PostgreSQL not to wait for the WAL fsync when this
	// writer's batches commit. See writeBatch. Always false on SQLite, which
	// has no such setting.
	syncCommitOff bool
	queue         chan persistEntry
	// flushReq carries a caller's request for a synchronous drain. The flusher
	// closes the handed-in channel once every entry that was queued before the
	// request has been COMMITTED, which is the happens-before edge the old
	// poll-and-sleep helper never established. Unbuffered on purpose: the
	// rendezvous is what guarantees the writer has seen the request.
	flushReq chan chan struct{}
	stop     chan struct{}
	wg       sync.WaitGroup
	// onceClose protects Close() against double-close from test
	// teardown + the eventual process-exit hook.
	onceClose sync.Once
}

// EnablePersistence opens (or creates) the console.db at `path` and
// wires the flusher goroutine.  Idempotent — calling twice with the
// same store is a no-op (the existing persistence stays).  Returns
// an error if the SQLite file can't be opened OR the schema migration
// fails; in either case the store keeps its in-RAM behaviour.
//
// Typical call site: runtime/rt's `init()` (or the dual-write
// helpers) checks `os.Getenv("SKY_CONSOLE_DB_PATH")` and forwards
// to `Default().EnablePersistence(path)`.
func (s *Store) EnablePersistence(path string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if s.persist != nil {
		return nil // already enabled
	}
	if path == "" {
		return nil
	}
	driver, dsn := telemetryBackend(path)
	db, handle, err := openTelemetryPool(driver, dsn)
	if err != nil {
		return err
	}
	if driver == "sqlite" {
		// WAL mode → console mini-app can read concurrently with our writes.
		if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
			closeTelemetryPool(db, handle)
			return err
		}
	}
	if driver == "sqlite" {
		// busy_timeout: tolerate contention with the console mini-app's reader
		// (and WAL checkpoints) without surfacing SQLITE_BUSY. 5s absorbs a WAL
		// checkpoint / a second writer briefly holding the write lock. Postgres
		// handles concurrency natively — no PRAGMA.
		if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
			closeTelemetryPool(db, handle)
			return err
		}
	}
	for _, stmt := range consoleDBSchemaStmts(driver) {
		if _, err := db.Exec(stmt); err != nil {
			closeTelemetryPool(db, handle)
			return err
		}
	}
	p := &persistence{
		db:            db,
		pool:          handle,
		driver:        driver,
		syncCommitOff: driver == "pgx" && SynchronousCommitOff(),
		queue:         make(chan persistEntry, persistQueueCap),
		flushReq:      make(chan chan struct{}),
		stop:          make(chan struct{}),
	}
	s.persist = p
	p.wg.Add(2)
	go p.flusher(s)
	go p.pruner(s)
	return nil
}

// EnablePersistenceFromEnv consults SKY_CONSOLE_DB_PATH and forwards
// to EnablePersistence when set.  Convenience used by the runtime's
// dual-write boot path so callers don't have to repeat the env check.
func (s *Store) EnablePersistenceFromEnv() error {
	path := os.Getenv(persistEnvVar)
	// One-DB-for-everything: fall back to a Postgres DATABASE_URL (the same var
	// the app DB, sessions, and analytics use) so console telemetry lands in the
	// shared database too. SKY_CONSOLE_DB_PATH still wins when set.
	if path == "" {
		if p := os.Getenv("DATABASE_URL"); strings.HasPrefix(p, "postgres") {
			path = p
		}
	}
	if path == "" {
		return nil
	}
	return s.EnablePersistence(path)
}

// ClosePersistence stops the flusher + pruner, waits for the queue to be
// committed, and closes the DB handle. It blocks until the drain is complete.
func (s *Store) ClosePersistence() {
	s.closePersistence(context.Background())
}

// ClosePersistenceContext is ClosePersistence bounded by a shutdown budget.
//
// The drain itself is unchanged and is genuinely synchronous — the flusher's
// stop branch commits everything still queued, and the WaitGroup below is what
// makes "flushed on shutdown" a fact rather than a hope. What the context adds
// is the ability to REPORT a drain that did not finish. Without it, a shutdown
// whose budget expired mid-drain lost the tail of the queue silently, which is
// the one fact an operator needs after a deploy and cannot recover afterwards.
// This mirrors `analyticsWriter.shutdown`.
func (s *Store) ClosePersistenceContext(ctx context.Context) {
	s.closePersistence(ctx)
}

func (s *Store) closePersistence(ctx context.Context) {
	s.persistMu.Lock()
	p := s.persist
	s.persist = nil
	s.persistMu.Unlock()
	if p == nil {
		return
	}
	// Cleared from the store BEFORE the stop is signalled, so a concurrent
	// record call sees a nil persistence and no-ops rather than sending to a
	// queue nobody will drain again.
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.onceClose.Do(func() {
			close(p.stop)
		})
		p.wg.Wait()
		if p.db != nil {
			closeTelemetryPool(p.db, p.pool)
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
		if n := len(p.queue); n > 0 {
			s.logs.append(LogEntry{
				TS:      time.Now(),
				Level:   "warn",
				Message: "telemetry persistence shutdown incomplete: the budget expired before the queue drained",
				Fields:  map[string]string{"unwritten": strconv.Itoa(n)},
			})
		}
	}
}

// enqueue best-effort sends an entry to the flusher.  When the queue
// is full (sustained overflow), drops the entry + logs a one-shot
// warning into the in-RAM ring so the operator sees the back-pressure
// at /_sky/console.  Never blocks.
func (s *Store) enqueuePersist(e persistEntry) {
	s.persistMu.RLock()
	p := s.persist
	s.persistMu.RUnlock()
	if p == nil {
		return
	}
	select {
	case p.queue <- e:
	default:
		// Queue full — record one warning per overflow burst so the
		// caller sees back-pressure without log-flood.
		if _, loaded := s.persistOverflowOnce.LoadOrStore("warned", true); !loaded {
			s.logs.append(LogEntry{
				TS:      time.Now(),
				Level:   "warn",
				Message: "telemetry persistence queue full; dropping write-through",
				Fields: map[string]string{
					// Derived, not a literal: a hand-copied "1024" here would
					// report the wrong capacity the first time the const moved.
					"queue_cap": strconv.Itoa(persistQueueCap),
				},
			})
		}
	}
}

// flusher drains the queue, batching writes inside a single
// transaction every 200 ms (or when 128 entries accumulate).  This
// keeps SQLite write amplification low without delaying log
// visibility for the console operator beyond a fraction of a second.
//
// Three things end a batch: the size cap, the interval, and an explicit
// synchronous flush request. The last is what gives callers read-your-writes
// against an asynchronous writer — see FlushPersistence.
func (p *persistence) flusher(s *Store) {
	defer p.wg.Done()
	tick := time.NewTicker(persistFlushInterval())
	defer tick.Stop()
	batch := make([]persistEntry, 0, persistBatchSize)
	// coalesce pulls everything already queued into the current batch, up to
	// the size cap — a burst of N entries becomes ceil(N/128) transactions
	// rather than N.
	coalesce := func() {
		for len(batch) < persistBatchSize {
			select {
			case e := <-p.queue:
				batch = append(batch, e)
			default:
				return
			}
		}
	}
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := p.writeBatch(batch); err != nil {
			s.logs.append(LogEntry{
				TS:      time.Now(),
				Level:   "warn",
				Message: "telemetry persistence write failed",
				Fields:  map[string]string{"error": err.Error()},
			})
		}
		batch = batch[:0]
	}
	// drainAll writes everything currently queued, not merely one batch.
	drainAll := func() {
		for {
			coalesce()
			flush()
			if len(p.queue) == 0 {
				return
			}
		}
	}
	for {
		select {
		case <-p.stop:
			// Final drain. Everything that reached the queue before the stop is
			// committed before this goroutine returns, and ClosePersistence
			// waits on the WaitGroup — so "telemetry is flushed on shutdown" is
			// a guarantee the caller can rely on rather than a signal and a
			// hope.
			drainAll()
			return

		case done := <-p.flushReq:
			// A caller is waiting. Drain to empty before closing `done`: every
			// entry enqueued before the request must be committed by the time
			// the caller observes the close.
			drainAll()
			close(done)

		case e := <-p.queue:
			batch = append(batch, e)
			coalesce()
			if len(batch) >= persistBatchSize {
				flush()
			}

		case <-tick.C:
			flush()
		}
	}
}

// writeBatch commits a slice of entries inside a single transaction.
// Splits by kind so each table sees one prepared statement reused
// across its share of the batch.
func (p *persistence) writeBatch(batch []persistEntry) error {
	// Begun through the CAPPED handle when there is one, so this consumer's
	// slot is held for the whole transaction. A transaction pins its
	// connection for its lifetime, so a cap released at BEGIN would bound
	// nothing — and a cap that is created but never taken is a bulkhead that
	// exists only in the comment above it.
	tx, err := p.begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck — Commit below supersedes

	// Telemetry takes the same durability trade as analytics: acknowledge the
	// commit without waiting for the WAL fsync, at the cost of losing a few
	// hundred milliseconds of it if the SERVER crashes. Logs, metrics and
	// spans are already a sampled, lossy, best-effort surface — the queue in
	// front of this transaction drops entries under overflow by design — so
	// paying an fsync per batch to protect them is spending the app's write
	// throughput on the wrong thing.
	//
	// `SET LOCAL`, so it reverts at the end of THIS transaction and cannot
	// reach the next borrower of the connection. That matters more here than
	// it looks: this pool is now SHARED with the session store and with
	// analytics, and a bare `SET` would quietly make somebody else's writes
	// non-durable.
	if p.syncCommitOff {
		if _, err := tx.Exec(`SET LOCAL synchronous_commit = off`); err != nil {
			return err
		}
	}

	var (
		insLog    *sql.Stmt
		insMetric *sql.Stmt
		insSpan   *sql.Stmt
	)
	for _, e := range batch {
		switch e.kind {
		case "log":
			if insLog == nil {
				insLog, err = tx.Prepare(telemetryQ(p.driver, `INSERT INTO telemetry_log
                    (namespace, level, message, attrs, created_at)
                    VALUES (?, ?, ?, ?, ?)`))
				if err != nil {
					return err
				}
			}
			attrs := encodeAttrs(e.log.Fields)
			ts := e.log.TS
			if ts.IsZero() {
				ts = time.Now()
			}
			if _, err := insLog.Exec(
				e.log.Subapp,
				e.log.Level,
				e.log.Message,
				attrs,
				ts.UTC().Format("2006-01-02 15:04:05.000"),
			); err != nil {
				return err
			}
		case "metric":
			if insMetric == nil {
				insMetric, err = tx.Prepare(telemetryQ(p.driver, `INSERT INTO telemetry_metric
                    (name, labels, value, observed_at)
                    VALUES (?, ?, ?, ?)`))
				if err != nil {
					return err
				}
			}
			ts := e.metric.observedAt
			if ts.IsZero() {
				ts = time.Now()
			}
			if _, err := insMetric.Exec(
				e.metric.name,
				encodeAttrs(e.metric.labels),
				e.metric.value,
				ts.UTC().Format("2006-01-02 15:04:05.000"),
			); err != nil {
				return err
			}
		case "span":
			if insSpan == nil {
				insSpan, err = tx.Prepare(telemetryQ(p.driver, `INSERT INTO telemetry_span
                    (id, trace_id, parent_id, name, started_at, ended_at, attrs)
                    VALUES (?, ?, ?, ?, ?, ?, ?)`))
				if err != nil {
					return err
				}
			}
			start := e.span.StartTime
			if start.IsZero() {
				start = time.Now()
			}
			end := e.span.EndTime
			if end.IsZero() {
				end = start
			}
			if _, err := insSpan.Exec(
				e.span.SpanID,
				e.span.TraceID,
				e.span.ParentID,
				e.span.Name,
				start.UTC().Format("2006-01-02 15:04:05.000"),
				end.UTC().Format("2006-01-02 15:04:05.000"),
				encodeAttrs(e.span.Attributes),
			); err != nil {
				return err
			}
		}
	}
	if insLog != nil {
		_ = insLog.Close()
	}
	if insMetric != nil {
		_ = insMetric.Close()
	}
	if insSpan != nil {
		_ = insSpan.Close()
	}
	return tx.Commit()
}

// encodeAttrs serialises a map[string]string as JSON for the `attrs`
// / `labels` columns.  Empty / nil maps become `"{}"` to keep the
// schema's NOT NULL DEFAULT '{}' contract.
func encodeAttrs(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// pruner runs hourly and deletes rows past their retention window.
// Retention windows match the schema header:
//
//	telemetry_log    24 h
//	telemetry_metric  7 d
//	telemetry_span   24 h
//
// VACUUM is intentionally NOT run here — autovacuum or a separate
// maintenance task can reclaim space; an hourly VACUUM would lock the
// file for too long under load.
func (p *persistence) pruner(s *Store) {
	defer p.wg.Done()
	// First sweep ~1 minute after open so a busy reboot doesn't
	// stall on startup with a giant first-pass delete.
	timer := time.NewTimer(1 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-timer.C:
			p.pruneCycle(s)
			timer.Reset(1 * time.Hour)
		}
	}
}

// pruneCycle is ONE retention sweep, with its own recover.
//
// # Why the recover is here and not around the loop
//
// This pruner checked and logged `runPrune`'s error from the day it was
// written, and had no recover at all — the mirror image of the analytics
// pruner, which recovered at the goroutine's top level and discarded the
// error. Each was missing exactly what the other had, and both failure modes
// end the same way: telemetry retention stops for the process lifetime and
// the rings' backing tables grow without bound.
//
// A panic in here is not hypothetical. `runPrune` drives a database/sql
// driver: modernc's SQLite and pgx both panic on a closed/nil underlying
// handle, and a driver that panics once will panic every hour — which is why
// the recover is scoped to the CYCLE. Wrapping the loop instead would turn
// the first panic into permanent silence.
func (p *persistence) pruneCycle(s *Store) {
	defer func() {
		if r := recover(); r != nil {
			s.logs.append(LogEntry{
				TS:      time.Now(),
				Level:   "warn",
				Message: "telemetry persistence prune panicked",
				Fields: map[string]string{
					"panic":  fmt.Sprintf("%v", r),
					"detail": "this cycle is lost; the next hourly tick will retry",
				},
			})
		}
	}()
	if err := p.runPrune(); err != nil {
		s.logs.append(LogEntry{
			TS:      time.Now(),
			Level:   "warn",
			Message: "telemetry persistence prune failed",
			Fields:  map[string]string{"error": err.Error()},
		})
	}
}

func (p *persistence) runPrune() error {
	now := time.Now().UTC()
	logCutoff := now.Add(-24 * time.Hour).Format("2006-01-02 15:04:05.000")
	metricCutoff := now.Add(-7 * 24 * time.Hour).Format("2006-01-02 15:04:05.000")
	spanCutoff := now.Add(-24 * time.Hour).Format("2006-01-02 15:04:05.000")
	if _, err := p.db.Exec(telemetryQ(p.driver, `DELETE FROM telemetry_log WHERE created_at < ?`), logCutoff); err != nil {
		return err
	}
	if _, err := p.db.Exec(telemetryQ(p.driver, `DELETE FROM telemetry_metric WHERE observed_at < ?`), metricCutoff); err != nil {
		return err
	}
	if _, err := p.db.Exec(telemetryQ(p.driver, `DELETE FROM telemetry_span WHERE started_at < ?`), spanCutoff); err != nil {
		return err
	}
	return nil
}

// FlushPersistence asks the writer to drain synchronously and waits for it.
// When it returns, every telemetry entry enqueued by the calling goroutine
// before the call has been committed and is visible to any other reader of the
// database — including a freshly opened handle.
//
// # Why this is a rendezvous and not a sleep
//
// It used to poll until `len(p.queue) == 0` and then sleep 250 ms. Both halves
// were wrong, and together they produced a test suite that was green on a quiet
// laptop and red on a loaded CI runner:
//
//   - An EMPTY QUEUE IS NOT A COMMITTED WRITE. The flusher moves entries out of
//     the channel into a local batch and commits them later, so the queue
//     reaches zero at the moment the data is least durable — in one goroutine's
//     stack, in no transaction.
//
//   - The 250 ms sleep was covering that gap by out-waiting the flusher's
//     200 ms tick. That is not synchronisation, it is a race with a handicap:
//     it holds only while those two unrelated constants keep their accidental
//     ordering, and only while the runner schedules the flusher promptly. CI
//     lost it and reported 86 of 100 rows — two full batches committed, the
//     44-entry remainder still in the flusher's hands.
//
// The flusher now answers an explicit request and closes the caller's channel
// only after the drain has COMMITTED, which is a happens-before edge rather
// than a probability. `persistFlushWait` bounds the wait so a wedged writer
// degrades the caller instead of hanging it.
func (s *Store) FlushPersistence() {
	s.persistMu.RLock()
	p := s.persist
	s.persistMu.RUnlock()
	if p == nil {
		return
	}
	done := make(chan struct{})
	timer := time.NewTimer(persistFlushWait)
	defer timer.Stop()
	select {
	case p.flushReq <- done:
	case <-p.stop:
		// Already shutting down; the stop drain commits the queue and
		// ClosePersistence is what waits for it.
		return
	case <-timer.C:
		return
	}
	select {
	case <-done:
	case <-timer.C:
	}
}

// SynchronousCommitOff reports whether telemetry's flush transactions should
// skip the WAL fsync at commit. Default ON (i.e. the setting is `off`); an
// operator who wants durable telemetry sets:
//
//	SKY_TELEMETRY_SYNCHRONOUS_COMMIT=on
//
// Parsed here rather than in `rt` because this package cannot import `rt`; the
// spelling deliberately matches PostgreSQL's own vocabulary and the analytics
// knob, so the two cannot mean different things.
func SynchronousCommitOff() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SKY_TELEMETRY_SYNCHRONOUS_COMMIT"))) {
	case "on", "true", "1", "remote_write", "remote_apply":
		return false
	default:
		return true
	}
}
