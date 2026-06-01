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
	"database/sql"
	"encoding/json"
	"os"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// consoleDBSchema is the embedded canonical schema for /data/console.db.
// Kept in sync with `control-plane/static/console.db.schema.sql` in
// SkyDeploy.  When the file changes there, mirror it here AND bump
// the EmbeddedRuntime TH marker.
const consoleDBSchema = `
CREATE TABLE IF NOT EXISTS telemetry_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace  TEXT NOT NULL DEFAULT '',
    level      TEXT NOT NULL,
    message    TEXT NOT NULL,
    attrs      TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS telemetry_metric (
    name        TEXT NOT NULL,
    labels      TEXT NOT NULL DEFAULT '{}',
    value       REAL NOT NULL,
    observed_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS telemetry_span (
    id         TEXT NOT NULL,
    trace_id   TEXT NOT NULL,
    parent_id  TEXT NOT NULL DEFAULT '',
    name       TEXT NOT NULL,
    started_at TEXT NOT NULL,
    ended_at   TEXT NOT NULL,
    attrs      TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_log_created
    ON telemetry_log (created_at DESC);

CREATE INDEX IF NOT EXISTS idx_metric_observed
    ON telemetry_metric (name, observed_at DESC);

CREATE INDEX IF NOT EXISTS idx_span_started
    ON telemetry_span (started_at DESC);
`

// persistEnvVar is the env var SkyDeploy injects on Pro+ tenants.
// When set + non-empty, the store dual-writes to the SQLite file.
const persistEnvVar = "SKY_CONSOLE_DB_PATH"

// persistQueueCap bounds the buffered channel of pending writes.
// Sized to ~1 s of telemetry at expected peak (~1 k events/s for a
// busy app); a sustained overflow surfaces as a warn-level entry in
// the in-RAM log ring (no panic, no block).
const persistQueueCap = 1024

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
	db    *sql.DB
	queue chan persistEntry
	stop  chan struct{}
	wg    sync.WaitGroup
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
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	// WAL mode → console mini-app can read concurrently with our writes.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return err
	}
	// busy_timeout: tolerate brief contention with the console
	// mini-app's reader without surfacing SQLITE_BUSY.
	if _, err := db.Exec(`PRAGMA busy_timeout=2000`); err != nil {
		db.Close()
		return err
	}
	if _, err := db.Exec(consoleDBSchema); err != nil {
		db.Close()
		return err
	}
	p := &persistence{
		db:    db,
		queue: make(chan persistEntry, persistQueueCap),
		stop:  make(chan struct{}),
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
	if path == "" {
		return nil
	}
	return s.EnablePersistence(path)
}

// ClosePersistence stops the flusher + pruner and closes the DB
// handle.  Test-only; production code lets the goroutines run for
// the process lifetime.
func (s *Store) ClosePersistence() {
	s.persistMu.Lock()
	p := s.persist
	s.persist = nil
	s.persistMu.Unlock()
	if p == nil {
		return
	}
	p.onceClose.Do(func() {
		close(p.stop)
	})
	p.wg.Wait()
	if p.db != nil {
		_ = p.db.Close()
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
					"queue_cap": "1024",
				},
			})
		}
	}
}

// flusher drains the queue, batching writes inside a single
// transaction every 200 ms (or when 128 entries accumulate).  This
// keeps SQLite write amplification low without delaying log
// visibility for the console operator beyond a fraction of a second.
func (p *persistence) flusher(s *Store) {
	defer p.wg.Done()
	const batchSize = 128
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	batch := make([]persistEntry, 0, batchSize)
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
	for {
		select {
		case <-p.stop:
			// Drain remaining queued entries on shutdown so tests
			// observing the file see every write that landed in the
			// queue before Close.
			for {
				select {
				case e := <-p.queue:
					batch = append(batch, e)
					if len(batch) >= batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case e := <-p.queue:
			batch = append(batch, e)
			if len(batch) >= batchSize {
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
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck — Commit below supersedes

	var (
		insLog    *sql.Stmt
		insMetric *sql.Stmt
		insSpan   *sql.Stmt
	)
	for _, e := range batch {
		switch e.kind {
		case "log":
			if insLog == nil {
				insLog, err = tx.Prepare(`INSERT INTO telemetry_log
                    (namespace, level, message, attrs, created_at)
                    VALUES (?, ?, ?, ?, ?)`)
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
				insMetric, err = tx.Prepare(`INSERT INTO telemetry_metric
                    (name, labels, value, observed_at)
                    VALUES (?, ?, ?, ?)`)
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
				insSpan, err = tx.Prepare(`INSERT INTO telemetry_span
                    (id, trace_id, parent_id, name, started_at, ended_at, attrs)
                    VALUES (?, ?, ?, ?, ?, ?, ?)`)
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
			if err := p.runPrune(); err != nil {
				s.logs.append(LogEntry{
					TS:      time.Now(),
					Level:   "warn",
					Message: "telemetry persistence prune failed",
					Fields:  map[string]string{"error": err.Error()},
				})
			}
			timer.Reset(1 * time.Hour)
		}
	}
}

func (p *persistence) runPrune() error {
	now := time.Now().UTC()
	logCutoff := now.Add(-24 * time.Hour).Format("2006-01-02 15:04:05.000")
	metricCutoff := now.Add(-7 * 24 * time.Hour).Format("2006-01-02 15:04:05.000")
	spanCutoff := now.Add(-24 * time.Hour).Format("2006-01-02 15:04:05.000")
	if _, err := p.db.Exec(`DELETE FROM telemetry_log WHERE created_at < ?`, logCutoff); err != nil {
		return err
	}
	if _, err := p.db.Exec(`DELETE FROM telemetry_metric WHERE observed_at < ?`, metricCutoff); err != nil {
		return err
	}
	if _, err := p.db.Exec(`DELETE FROM telemetry_span WHERE started_at < ?`, spanCutoff); err != nil {
		return err
	}
	return nil
}

// FlushPersistence is a test-only helper that waits for the flusher
// to drain the queue and commit.  Production code never calls this —
// the 200 ms tick is fast enough for the console UI.
func (s *Store) FlushPersistence() {
	s.persistMu.RLock()
	p := s.persist
	s.persistMu.RUnlock()
	if p == nil {
		return
	}
	// Poll-drain.  Cheap because the channel has a known capacity
	// and we just wait for the count to hit zero plus one tick.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.queue) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// One extra tick to let an in-flight batch commit.
	time.Sleep(250 * time.Millisecond)
}
