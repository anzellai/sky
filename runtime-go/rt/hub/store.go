package hub

// SQLite-backed hot store for the hub. Schema mirrors the embedded
// console schema at runtime-go/rt/telemetry/persist.go but adds a
// `service_name` column on every table (required for the hub's
// multi-service queries — see HUB.md §"Service identity"). Hour-
// level indexes are sized for the v0.16.4 single-service queries
// in Chunk 4 + the multi-service queries in Chunk 5/6.
//
// Write path:
//
//	receiver.Insert([]pendingItem)
//	    └─> channel send (best-effort; drops at saturation)
//	        └─> batcher goroutine
//	            └─> drain channel into a slice
//	                └─> flush every 200 ms OR 128 entries
//	                    └─> single tx commit
//
// Hourly prune deletes rows older than RetentionHours (default 24).
// Setting RetentionHours=0 prunes anything older than `now` —
// useful for tests that want to assert prune behaviour.
//
// Defaults: WAL mode, busy_timeout=2000, foreign_keys=ON. modernc.
// org/sqlite is the canonical SQLite driver across the runtime
// (already a direct dep) — no cgo needed.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// hubSchema is the embedded SQL the store materialises on first
// open. service_name + (service_name, time) index every table —
// the v0.16.4 service-filtered queries scan these in O(log N).
//
// Time columns are stored as ISO-8601 strings with millisecond
// precision (same convention as telemetry/persist.go) so sqlite3
// CLI introspection stays human-readable.
const hubSchema = `
CREATE TABLE IF NOT EXISTS telemetry_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    service_name TEXT NOT NULL DEFAULT 'unknown',
    time         TEXT NOT NULL,
    level        TEXT NOT NULL DEFAULT 'info',
    message      TEXT NOT NULL DEFAULT '',
    trace_id     TEXT NOT NULL DEFAULT '',
    span_id      TEXT NOT NULL DEFAULT '',
    attrs        TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS telemetry_metric (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    service_name TEXT NOT NULL DEFAULT 'unknown',
    time         TEXT NOT NULL,
    name         TEXT NOT NULL,
    type         TEXT NOT NULL DEFAULT 'gauge',
    value        REAL NOT NULL,
    attrs        TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS telemetry_span (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    service_name TEXT NOT NULL DEFAULT 'unknown',
    time         TEXT NOT NULL,
    name         TEXT NOT NULL,
    trace_id     TEXT NOT NULL DEFAULT '',
    span_id      TEXT NOT NULL DEFAULT '',
    parent_id    TEXT NOT NULL DEFAULT '',
    start_time   TEXT NOT NULL,
    end_time     TEXT NOT NULL,
    attrs        TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_log_service_time
    ON telemetry_log (service_name, time DESC);
CREATE INDEX IF NOT EXISTS idx_metric_service_time
    ON telemetry_metric (service_name, time DESC);
CREATE INDEX IF NOT EXISTS idx_span_service_time
    ON telemetry_span (service_name, time DESC);

CREATE INDEX IF NOT EXISTS idx_log_time
    ON telemetry_log (time DESC);
CREATE INDEX IF NOT EXISTS idx_metric_time
    ON telemetry_metric (time DESC);
CREATE INDEX IF NOT EXISTS idx_span_time
    ON telemetry_span (time DESC);
`

const timeFormat = "2006-01-02 15:04:05.000"

// flushInterval governs how often the batcher commits.
const flushInterval = 200 * time.Millisecond

// flushBatchSize triggers an early commit when the in-RAM batch
// fills up before flushInterval elapses.
const flushBatchSize = 128

// storeOptions toggles retention behaviour. Defaulted by Run; tests
// override directly via newStore.
type storeOptions struct {
	retentionHours int
	pruneInterval  time.Duration
}

// Store wraps the SQLite handle plus the batcher + pruner
// goroutines. One Store per hub process.
type Store struct {
	db   *sql.DB
	path string
	opts storeOptions

	queue chan pendingItem
	stop  chan struct{}
	wg    sync.WaitGroup
	ready atomic.Bool

	insertedTotal atomic.Uint64
	droppedTotal  atomic.Uint64
}

// newStore opens / creates the hot DB under dataDir, runs the
// schema migration, and starts the batcher + pruner goroutines.
// Caller MUST call Close() to drain on shutdown.
func newStore(dataDir string, opts storeOptions) (*Store, error) {
	if dataDir == "" {
		return nil, errors.New("hub: store: data-dir is empty")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("hub: mkdir %s: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, "console-hot.db")
	// `_pragma=journal_mode(WAL)` style pragmas are supported via the
	// connection-string syntax in modernc.org/sqlite. We issue them
	// explicitly after Open so the connection-string parsing is
	// driver-version-independent.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("hub: open sqlite %s: %w", path, err)
	}
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=2000`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA synchronous=NORMAL`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("hub: pragma %q: %w", pragma, err)
		}
	}
	if _, err := db.Exec(hubSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("hub: schema migrate: %w", err)
	}

	if opts.pruneInterval <= 0 {
		opts.pruneInterval = DefaultPruneInterval
	}

	s := &Store{
		db:    db,
		path:  path,
		opts:  opts,
		queue: make(chan pendingItem, HubBufferCap),
		stop:  make(chan struct{}),
	}
	s.ready.Store(true)
	s.wg.Add(2)
	go s.batcher()
	go s.pruner()
	return s, nil
}

// Ready reports whether the store's DB handle is open + the
// batcher goroutine is alive. Used by /_hub/readyz.
func (s *Store) Ready() bool {
	return s.ready.Load()
}

// Path returns the on-disk DB file path. Test helper.
func (s *Store) Path() string {
	return s.path
}

// Insert is the receiver-facing entry point. Non-blocking: enqueues
// each item, dropping at the channel boundary when the writer is
// saturated. A burst that fills the channel surfaces as a single
// warn log line per epoch so the operator notices without a flood.
func (s *Store) Insert(items []pendingItem) {
	for i := range items {
		select {
		case s.queue <- items[i]:
		default:
			s.droppedTotal.Add(1)
		}
	}
}

// Close drains the queue and shuts down the batcher + pruner. Idempotent.
func (s *Store) Close() error {
	if !s.ready.CompareAndSwap(true, false) {
		return nil
	}
	close(s.stop)
	s.wg.Wait()
	return s.db.Close()
}

// batcher drains queue, flushes every flushInterval (or every
// flushBatchSize items, whichever first). On stop, fully drains
// the channel before exiting so a Close() right after Insert
// doesn't lose entries.
func (s *Store) batcher() {
	defer s.wg.Done()
	tick := time.NewTicker(flushInterval)
	defer tick.Stop()
	batch := make([]pendingItem, 0, flushBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.writeBatch(batch); err != nil {
			log.Printf("[sky.hub] writeBatch: %v", err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-s.stop:
			// Drain pending items so an in-flight burst at the
			// moment of shutdown reaches disk before Close.
			for {
				select {
				case item := <-s.queue:
					batch = append(batch, item)
					if len(batch) >= flushBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case item := <-s.queue:
			batch = append(batch, item)
			if len(batch) >= flushBatchSize {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

// writeBatch commits a slice inside one tx. Splits by kind so each
// table gets a prepared statement reused across its slice.
func (s *Store) writeBatch(batch []pendingItem) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		// Rollback is a no-op after Commit succeeds; harmless on
		// the error path.
		_ = tx.Rollback()
	}()

	var (
		logStmt    *sql.Stmt
		metricStmt *sql.Stmt
		spanStmt   *sql.Stmt
	)
	defer func() {
		if logStmt != nil {
			_ = logStmt.Close()
		}
		if metricStmt != nil {
			_ = metricStmt.Close()
		}
		if spanStmt != nil {
			_ = spanStmt.Close()
		}
	}()

	for i := range batch {
		item := &batch[i]
		svc := item.serviceName
		if svc == "" {
			svc = unknownService
		}
		switch item.kind {
		case signalLog:
			if logStmt == nil {
				stmt, err := tx.Prepare(`
					INSERT INTO telemetry_log
						(service_name, time, level, message, trace_id, span_id, attrs)
					VALUES (?, ?, ?, ?, ?, ?, ?)`)
				if err != nil {
					return err
				}
				logStmt = stmt
			}
			if _, err := logStmt.Exec(
				svc,
				formatTime(item.ts),
				strDefault(item.level, "info"),
				item.message,
				item.traceID,
				item.spanID,
				encodeAttrs(item.attrs),
			); err != nil {
				return err
			}
		case signalMetric:
			if metricStmt == nil {
				stmt, err := tx.Prepare(`
					INSERT INTO telemetry_metric
						(service_name, time, name, type, value, attrs)
					VALUES (?, ?, ?, ?, ?, ?)`)
				if err != nil {
					return err
				}
				metricStmt = stmt
			}
			if _, err := metricStmt.Exec(
				svc,
				formatTime(item.ts),
				item.metricName,
				strDefault(item.metricType, "gauge"),
				item.value,
				encodeAttrs(item.attrs),
			); err != nil {
				return err
			}
		case signalSpan:
			if spanStmt == nil {
				stmt, err := tx.Prepare(`
					INSERT INTO telemetry_span
						(service_name, time, name, trace_id, span_id, parent_id, start_time, end_time, attrs)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
				if err != nil {
					return err
				}
				spanStmt = stmt
			}
			startTime := item.startTime
			if startTime.IsZero() {
				startTime = item.ts
			}
			endTime := item.endTime
			if endTime.IsZero() {
				endTime = startTime
			}
			if _, err := spanStmt.Exec(
				svc,
				formatTime(item.ts),
				item.spanName,
				item.traceID,
				item.spanID,
				item.parentID,
				formatTime(startTime),
				formatTime(endTime),
				encodeAttrs(item.attrs),
			); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.insertedTotal.Add(uint64(len(batch)))
	return nil
}

// pruner runs every PruneInterval and deletes rows older than
// RetentionHours.
func (s *Store) pruner() {
	defer s.wg.Done()
	// Stagger the first sweep so a busy boot doesn't immediately
	// hammer the DB with a giant DELETE. 60 s for normal config;
	// tests that set very-low intervals get the first tick promptly.
	first := 60 * time.Second
	if s.opts.pruneInterval < first {
		first = s.opts.pruneInterval
	}
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-timer.C:
			if err := s.runPrune(); err != nil {
				log.Printf("[sky.hub] prune: %v", err)
			}
			timer.Reset(s.opts.pruneInterval)
		}
	}
}

func (s *Store) runPrune() error {
	now := time.Now().UTC()
	cutoff := now.Add(-time.Duration(s.opts.retentionHours) * time.Hour)
	cutoffStr := formatTime(cutoff)
	for _, q := range []string{
		`DELETE FROM telemetry_log    WHERE time < ?`,
		`DELETE FROM telemetry_metric WHERE time < ?`,
		`DELETE FROM telemetry_span   WHERE time < ?`,
	} {
		if _, err := s.db.Exec(q, cutoffStr); err != nil {
			return err
		}
	}
	return nil
}

// ─── read path ───────────────────────────────────────────────────
//
// Chunk 3 ships the basic filters Chunk 4's UI needs: service +
// time-range + level. Chunk 5/6 will layer richer queries on top.

// LogFilter narrows the log read.
type LogFilter struct {
	ServiceName string    // "" → no filter
	Level       string    // "" → no filter
	Since       time.Time // zero → no lower bound
	Until       time.Time // zero → no upper bound
	Limit       int       // 0 → 100
}

// LogRow mirrors a telemetry_log SELECT row.
type LogRow struct {
	ID          int64
	ServiceName string
	Time        time.Time
	Level       string
	Message     string
	TraceID     string
	SpanID      string
	Attrs       map[string]string
}

// QueryLogs returns at most Limit rows matching the filter, newest
// first.
func (s *Store) QueryLogs(filter LogFilter) ([]LogRow, error) {
	q, args := buildLogQuery(filter)
	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]LogRow, 0, max(filter.Limit, 32))
	for rows.Next() {
		var (
			r       LogRow
			timeStr string
			attrStr string
		)
		if err := rows.Scan(&r.ID, &r.ServiceName, &timeStr, &r.Level, &r.Message, &r.TraceID, &r.SpanID, &attrStr); err != nil {
			return nil, err
		}
		r.Time = parseTime(timeStr)
		r.Attrs = decodeAttrs(attrStr)
		out = append(out, r)
	}
	return out, rows.Err()
}

func buildLogQuery(f LogFilter) (string, []any) {
	q := `SELECT id, service_name, time, level, message, trace_id, span_id, attrs
	      FROM telemetry_log WHERE 1=1`
	args := make([]any, 0, 4)
	if f.ServiceName != "" {
		q += ` AND service_name = ?`
		args = append(args, f.ServiceName)
	}
	if f.Level != "" {
		q += ` AND level = ?`
		args = append(args, f.Level)
	}
	if !f.Since.IsZero() {
		q += ` AND time >= ?`
		args = append(args, formatTime(f.Since))
	}
	if !f.Until.IsZero() {
		q += ` AND time <= ?`
		args = append(args, formatTime(f.Until))
	}
	q += ` ORDER BY time DESC, id DESC`
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q += ` LIMIT ?`
	args = append(args, limit)
	return q, args
}

// MetricFilter narrows the metric read.
type MetricFilter struct {
	ServiceName string
	Name        string
	Since       time.Time
	Until       time.Time
	Limit       int
}

// MetricRow mirrors a telemetry_metric SELECT row.
type MetricRow struct {
	ID          int64
	ServiceName string
	Time        time.Time
	Name        string
	Type        string
	Value       float64
	Attrs       map[string]string
}

// QueryMetrics returns rows newest first.
func (s *Store) QueryMetrics(filter MetricFilter) ([]MetricRow, error) {
	q := `SELECT id, service_name, time, name, type, value, attrs
	      FROM telemetry_metric WHERE 1=1`
	args := make([]any, 0, 4)
	if filter.ServiceName != "" {
		q += ` AND service_name = ?`
		args = append(args, filter.ServiceName)
	}
	if filter.Name != "" {
		q += ` AND name = ?`
		args = append(args, filter.Name)
	}
	if !filter.Since.IsZero() {
		q += ` AND time >= ?`
		args = append(args, formatTime(filter.Since))
	}
	if !filter.Until.IsZero() {
		q += ` AND time <= ?`
		args = append(args, formatTime(filter.Until))
	}
	q += ` ORDER BY time DESC, id DESC`
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	q += ` LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MetricRow, 0, max(filter.Limit, 32))
	for rows.Next() {
		var (
			r       MetricRow
			timeStr string
			attrStr string
		)
		if err := rows.Scan(&r.ID, &r.ServiceName, &timeStr, &r.Name, &r.Type, &r.Value, &attrStr); err != nil {
			return nil, err
		}
		r.Time = parseTime(timeStr)
		r.Attrs = decodeAttrs(attrStr)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SpanFilter narrows the span read.
type SpanFilter struct {
	ServiceName string
	TraceID     string
	Since       time.Time
	Until       time.Time
	Limit       int
}

// SpanRow mirrors a telemetry_span SELECT row.
type SpanRow struct {
	ID          int64
	ServiceName string
	Time        time.Time
	Name        string
	TraceID     string
	SpanID      string
	ParentID    string
	StartTime   time.Time
	EndTime     time.Time
	Attrs       map[string]string
}

// QuerySpans returns rows newest first.
func (s *Store) QuerySpans(filter SpanFilter) ([]SpanRow, error) {
	q := `SELECT id, service_name, time, name, trace_id, span_id, parent_id, start_time, end_time, attrs
	      FROM telemetry_span WHERE 1=1`
	args := make([]any, 0, 4)
	if filter.ServiceName != "" {
		q += ` AND service_name = ?`
		args = append(args, filter.ServiceName)
	}
	if filter.TraceID != "" {
		q += ` AND trace_id = ?`
		args = append(args, filter.TraceID)
	}
	if !filter.Since.IsZero() {
		q += ` AND time >= ?`
		args = append(args, formatTime(filter.Since))
	}
	if !filter.Until.IsZero() {
		q += ` AND time <= ?`
		args = append(args, formatTime(filter.Until))
	}
	q += ` ORDER BY time DESC, id DESC`
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	q += ` LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SpanRow, 0, max(filter.Limit, 32))
	for rows.Next() {
		var (
			r        SpanRow
			timeStr  string
			startStr string
			endStr   string
			attrStr  string
		)
		if err := rows.Scan(&r.ID, &r.ServiceName, &timeStr, &r.Name, &r.TraceID, &r.SpanID, &r.ParentID, &startStr, &endStr, &attrStr); err != nil {
			return nil, err
		}
		r.Time = parseTime(timeStr)
		r.StartTime = parseTime(startStr)
		r.EndTime = parseTime(endStr)
		r.Attrs = decodeAttrs(attrStr)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Services returns the distinct service_name values currently in the
// store across all three tables. Useful for the multi-service
// selector in Chunk 5.
func (s *Store) Services() ([]string, error) {
	q := `
		SELECT service_name FROM telemetry_log
		UNION SELECT service_name FROM telemetry_metric
		UNION SELECT service_name FROM telemetry_span
		ORDER BY service_name`
	rows, err := s.db.QueryContext(context.Background(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Counts returns the row count per table (test introspection helper).
func (s *Store) Counts() (logs, metrics, spans int, err error) {
	scan := func(q string, dst *int) error {
		row := s.db.QueryRow(q)
		return row.Scan(dst)
	}
	if err = scan(`SELECT COUNT(*) FROM telemetry_log`, &logs); err != nil {
		return
	}
	if err = scan(`SELECT COUNT(*) FROM telemetry_metric`, &metrics); err != nil {
		return
	}
	err = scan(`SELECT COUNT(*) FROM telemetry_span`, &spans)
	return
}

// Stats returns counters useful for /_sky/metrics or test probes.
func (s *Store) Stats() (inserted, dropped uint64) {
	return s.insertedTotal.Load(), s.droppedTotal.Load()
}

// FlushSync waits for any in-flight queue entries to commit. Tests
// only — production code lets the 200 ms tick handle latency.
func (s *Store) FlushSync(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(s.queue) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// One extra tick for the in-flight batch.
	time.Sleep(flushInterval + 50*time.Millisecond)
}

// RunPruneNow triggers the prune sweep synchronously. Tests use
// this in conjunction with RetentionHours=0 to assert prune
// behaviour without waiting for the timer.
func (s *Store) RunPruneNow() error {
	return s.runPrune()
}

// ─── helpers ─────────────────────────────────────────────────────

func formatTime(t time.Time) string {
	if t.IsZero() {
		return time.Now().UTC().Format(timeFormat)
	}
	return t.UTC().Format(timeFormat)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(timeFormat, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

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

func decodeAttrs(s string) map[string]string {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

func strDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
