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

	"sky-app/rt/periodic"

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

// defaultFlushInterval governs how often the batcher commits on time —
// the "or interval" half of "size or interval, whichever comes first".
// It bounds how long an item can sit unwritten on a hub that is not busy
// enough to fill a batch.
const defaultFlushInterval = 200 * time.Millisecond

// flushInterval reads the batcher's tick through an override, and is a
// function over a var rather than a const for one reason: it is the knob that
// proves FlushSync is a real synchronisation and not a race that happens to be
// won.
//
// Until this was fixed, FlushSync polled for an empty queue and then slept
// `flushInterval + 50ms`. Raising this interval out of reach — a tuning change
// nobody would look at twice — turned EIGHTEEN tests in this package red,
// including `logs=384, want 500`: the exact count CI reported. Seventeen of
// them reported zero rows, because an empty queue means the batcher has TAKEN
// the items, not that it has committed them; only the 500-row test had enough
// entries for the size trigger to commit three batches (3 x 128 = 384) before
// the read. Every one of those tests was passing on the ticker rather than on
// the flush they called.
//
// `TestMain` pins this to an hour for the whole package, so any regression to
// an interval-dependent flush fails immediately and locally rather than
// intermittently on a loaded runner.
func flushInterval() time.Duration {
	if d := flushIntervalOverride.Load(); d > 0 {
		return time.Duration(d)
	}
	return defaultFlushInterval
}

var flushIntervalOverride atomic.Int64

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
	// flushReq carries a caller's request for a synchronous drain. The batcher
	// closes the handed-in channel once every item queued before the request
	// has been COMMITTED, which is the happens-before edge the old
	// poll-and-sleep FlushSync never established. Unbuffered on purpose: the
	// rendezvous is what guarantees the batcher has seen the request.
	flushReq chan chan struct{}
	stop     chan struct{}
	wg       sync.WaitGroup
	ready    atomic.Bool

	// closeOnce + closed make Close a real barrier for EVERY caller, not only
	// the one that wins the race to start the drain. See Close.
	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error

	insertedTotal atomic.Uint64
	droppedTotal  atomic.Uint64

	// Saturation-warning epoch state. See warnSaturated.
	//
	// dropWarnWindowNanos is an override rather than a constructor
	// parameter so a Store built as a literal behaves exactly like one
	// built by newStore — there is no wiring for a caller to forget. Same
	// shape as flushIntervalOverride.
	dropWarnMu          sync.Mutex
	dropWarnAt          time.Time
	dropWarnReported    uint64
	dropWarnWindowNanos atomic.Int64
}

// defaultDropWarnWindow bounds saturation warnings to one line per window.
// A saturated hub drops thousands of items a second; a line each would bury
// the one line an operator needs to see.
const defaultDropWarnWindow = time.Minute

// dropWarnWindow reads the epoch through its override, supplying the default
// itself so a zero-valued Store cannot silently disable the rate limit.
func (s *Store) dropWarnWindow() time.Duration {
	if d := s.dropWarnWindowNanos.Load(); d > 0 {
		return time.Duration(d)
	}
	return defaultDropWarnWindow
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
		db:       db,
		path:     path,
		opts:     opts,
		queue:    make(chan pendingItem, HubBufferCap),
		flushReq: make(chan chan struct{}),
		stop:     make(chan struct{}),
		closed:   make(chan struct{}),
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
// warn log line per epoch so the operator notices without a flood —
// see warnSaturated, and TestHubStoreSaturationWarnsOncePerEpoch for
// the gate that holds this sentence to it.
func (s *Store) Insert(items []pendingItem) {
	var dropped uint64
	for i := range items {
		select {
		case s.queue <- items[i]:
		default:
			dropped++
		}
	}
	if dropped == 0 {
		return
	}
	s.warnSaturated(s.droppedTotal.Add(dropped))
}

// warnSaturated emits at most one line per epoch, reporting every drop since
// the previous line.
//
// The counter alone was not enough, and saying so is the point of this
// function. `droppedTotal` is readable only through `Stats()`, which on the
// hub nothing scrapes — so telemetry the hub was asked to keep disappeared
// with nothing anywhere saying it had. Dropping is a legitimate response to
// saturation; dropping in silence is not.
//
// The count is "since the last line", not "in this burst": the drops the rate
// limit suppressed inside the epoch are carried into the next line, so the
// rate limit cannot become a second way to lose data quietly.
func (s *Store) warnSaturated(total uint64) {
	s.dropWarnMu.Lock()
	now := time.Now()
	if !s.dropWarnAt.IsZero() && now.Sub(s.dropWarnAt) < s.dropWarnWindow() {
		s.dropWarnMu.Unlock()
		return
	}
	since := total - s.dropWarnReported
	s.dropWarnAt = now
	s.dropWarnReported = total
	s.dropWarnMu.Unlock()

	log.Printf("[sky.hub] store queue saturated (cap=%d): dropped %d telemetry "+
		"item(s) since the last warning, %d in this process; the batcher is not "+
		"keeping up with the receiver", cap(s.queue), since, total)
}

// Close drains the queue and shuts down the batcher + pruner. Idempotent, and
// idempotent in the sense that matters: EVERY caller blocks until the drain has
// committed, not only the first one.
//
// The previous guard was `ready.CompareAndSwap(true, false)`, which returned
// nil immediately to a second caller while the first was still draining. That
// made "Close returned, so the data is on disk" true for one goroutine and
// false for the other — the same defect as the old FlushSync, one layer up,
// and the version of it that costs real data rather than a red test.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.ready.Store(false)
		close(s.stop)
		// The batcher's stop branch commits everything queued before it
		// returns; waiting on the WaitGroup is what turns that into a promise
		// the caller can rely on.
		s.wg.Wait()
		s.closeErr = s.db.Close()
		close(s.closed)
	})
	<-s.closed
	return s.closeErr
}

// batcher drains queue, flushes every flushInterval (or every
// flushBatchSize items, whichever first). On stop, fully drains
// the channel before exiting so a Close() right after Insert
// doesn't lose entries.
//
// Three things end a batch: the size cap, the interval, and an explicit
// synchronous flush request. The last is what gives callers read-your-writes
// against an asynchronous writer — see FlushSync. This is the same shape as
// `analyticsWriter.run` and `telemetry.persistence.flusher`, reused rather
// than reinvented.
func (s *Store) batcher() {
	defer s.wg.Done()
	tick := time.NewTicker(flushInterval())
	defer tick.Stop()
	batch := make([]pendingItem, 0, flushBatchSize)
	// coalesce pulls everything already queued into the current batch, up to
	// the size cap — a burst of N items becomes ceil(N/128) transactions
	// rather than N.
	coalesce := func() {
		for len(batch) < flushBatchSize {
			select {
			case item := <-s.queue:
				batch = append(batch, item)
			default:
				return
			}
		}
	}
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.writeBatch(batch); err != nil {
			log.Printf("[sky.hub] writeBatch: %v", err)
		}
		batch = batch[:0]
	}
	// drainAll commits everything currently queued, not merely one batch.
	drainAll := func() {
		for {
			coalesce()
			flush()
			if len(s.queue) == 0 {
				return
			}
		}
	}
	for {
		select {
		case <-s.stop:
			// Final drain. Everything that reached the queue before the stop
			// is committed before this goroutine returns, and Close waits on
			// the WaitGroup — so "the hub store is flushed on shutdown" is a
			// guarantee the caller can rely on rather than a signal and a hope.
			drainAll()
			return

		case done := <-s.flushReq:
			// A caller is waiting. Drain to empty before closing `done`: every
			// item enqueued before the request must be committed by the time
			// the caller observes the close.
			drainAll()
			close(done)

		case item := <-s.queue:
			batch = append(batch, item)
			coalesce()
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

// periodicReport routes this package's background-loop failures into the hub's
// log. periodic deliberately does no logging of its own — see that package's
// header — because rt/hub cannot import rt and so cannot reach rt's structured
// logger. `log.Printf` with the `[sky.hub]` tag is what this package's
// operators already read.
func periodicReport(r periodic.Report) {
	switch {
	case r.Panic != nil:
		log.Printf("[sky.hub] %s: cycle panicked: %v — this cycle is lost, the next tick will retry\n%s",
			r.Loop, r.Panic, r.Stack)
	case r.Err != nil:
		log.Printf("[sky.hub] %s: %v", r.Loop, r.Err)
	}
}

// pruner runs every PruneInterval and deletes rows older than
// RetentionHours.
//
// The recover is per cycle (periodic.Every → periodic.Guard). This loop had
// none — the exact mirror of the analytics retention pruner, which had a
// recover at its goroutine's top level and discarded its error while this one
// checked its error and had no recover at all. Each was missing precisely what
// the other had, and both end the same way: retention stops for the process
// lifetime and the telemetry tables grow without bound.
//
// The panic is not hypothetical. runPrune drives a database/sql driver, and
// modernc's SQLite panics on a closed or nil underlying handle; a driver that
// panics once panics every interval, which is why the recover is scoped to the
// cycle rather than to the loop.
func (s *Store) pruner() { s.runPruner(s.db) }

// runPruner is the prune loop, taking its execer as a parameter so the
// regression gate can drive the REAL loop with a database that panics. See
// hubPruneExecer.
func (s *Store) runPruner(db hubPruneExecer) {
	defer s.wg.Done()
	// Stagger the first sweep so a busy boot doesn't immediately
	// hammer the DB with a giant DELETE. 60 s for normal config;
	// tests that set very-low intervals get the first tick promptly.
	first := 60 * time.Second
	if s.opts.pruneInterval < first {
		first = s.opts.pruneInterval
	}
	select {
	case <-s.stop:
		return
	case <-time.After(first):
	}
	prune := func() error { return s.runPruneOn(db) }
	periodic.Guard("hub.pruner", periodicReport, prune)
	periodic.Every(periodic.Config{
		Name:     "hub.pruner",
		Interval: s.opts.pruneInterval,
		Stop:     s.stop,
		Report:   periodicReport,
		Work:     func(time.Time) error { return prune() },
	})
}

// hubPruneExecer is the one method the pruner needs from the store. A narrow
// interface rather than *sql.DB so the regression gate can inject an Exec that
// panics — the behaviour the loop has to survive, and one a real driver
// produces only when its handle is already closed.
type hubPruneExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func (s *Store) runPrune() error { return s.runPruneOn(s.db) }

func (s *Store) runPruneOn(db hubPruneExecer) error {
	now := time.Now().UTC()
	cutoff := now.Add(-time.Duration(s.opts.retentionHours) * time.Hour)
	cutoffStr := formatTime(cutoff)
	for _, q := range []string{
		`DELETE FROM telemetry_log    WHERE time < ?`,
		`DELETE FROM telemetry_metric WHERE time < ?`,
		`DELETE FROM telemetry_span   WHERE time < ?`,
	} {
		if _, err := db.Exec(q, cutoffStr); err != nil {
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
	ServiceName  string    // "" → no exact-match filter
	TenantPrefix string    // "" → no tenant scoping; non-empty → AND service_name LIKE prefix || '%'
	Level        string    // "" → no filter
	Since        time.Time // zero → no lower bound
	Until        time.Time // zero → no upper bound
	Limit        int       // 0 → 100
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
	args := make([]any, 0, 5)
	if f.ServiceName != "" {
		q += ` AND service_name = ?`
		args = append(args, f.ServiceName)
	}
	if f.TenantPrefix != "" {
		q += ` AND service_name LIKE ?`
		args = append(args, escapeLikePrefix(f.TenantPrefix)+"%")
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
	ServiceName  string
	TenantPrefix string // "" → no tenant scoping
	Name         string
	Since        time.Time
	Until        time.Time
	Limit        int
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
	args := make([]any, 0, 5)
	if filter.ServiceName != "" {
		q += ` AND service_name = ?`
		args = append(args, filter.ServiceName)
	}
	if filter.TenantPrefix != "" {
		q += ` AND service_name LIKE ?`
		args = append(args, escapeLikePrefix(filter.TenantPrefix)+"%")
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
	ServiceName  string
	TenantPrefix string // "" → no tenant scoping
	TraceID      string
	Since        time.Time
	Until        time.Time
	Limit        int
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
	args := make([]any, 0, 5)
	if filter.ServiceName != "" {
		q += ` AND service_name = ?`
		args = append(args, filter.ServiceName)
	}
	if filter.TenantPrefix != "" {
		q += ` AND service_name LIKE ?`
		args = append(args, escapeLikePrefix(filter.TenantPrefix)+"%")
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

// FlushSync asks the batcher to drain synchronously and waits for it. When it
// returns, every item enqueued by the calling goroutine before the call has
// been committed and is visible to any reader of the database.
//
// # Why this is a rendezvous and not a sleep
//
// It used to poll until `len(s.queue) == 0` and then sleep one flush interval
// plus 50 ms. Both halves were wrong, and together they produced a package
// that was green on a quiet laptop and red on a loaded CI runner:
//
//   - AN EMPTY QUEUE IS NOT A COMMITTED WRITE. The batcher moves items out of
//     the channel into a local slice and commits them later, so the queue
//     reaches zero at the moment the data is LEAST durable — in one
//     goroutine's stack, in no transaction.
//
//   - The trailing sleep was covering that gap by out-waiting the batcher's
//     own ticker. That is not synchronisation, it is a race with a handicap:
//     it holds only while the runner schedules the batcher promptly and the
//     three SQLite transactions it still owes finish inside 250 ms. Under
//     `-race` they did not, and CI reported `logs=384, want 500` — three
//     size-triggered batches committed, the 116-item remainder still in the
//     batcher's hands.
//
// The batcher now answers an explicit request and closes the caller's channel
// only after the drain has COMMITTED, which is a happens-before edge rather
// than a probability. `timeout` bounds the wait so a wedged batcher degrades
// the caller instead of hanging it.
func (s *Store) FlushSync(timeout time.Duration) {
	done := make(chan struct{})
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case s.flushReq <- done:
	case <-s.stop:
		// Already shutting down; the stop drain commits the queue and Close is
		// what waits for it.
		return
	case <-timer.C:
		return
	}
	select {
	case <-done:
	case <-timer.C:
	}
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

// escapeLikePrefix strips SQLite LIKE wildcards (`%` and `_`) from
// the tenant prefix so a malicious / unsanitised claim can't widen
// the prefix scope.  Legitimate tenant claims (slug / UUID / numeric
// ID) never contain LIKE metachars; if any do appear we drop them
// rather than enable an ESCAPE clause (which would propagate
// through every WHERE-clause builder in this file).  Caller appends
// `%` AFTER calling this helper.
func escapeLikePrefix(p string) string {
	if p == "" {
		return ""
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '%' || p[i] == '_' {
			out := make([]byte, 0, len(p))
			out = append(out, p[:i]...)
			for j := i; j < len(p); j++ {
				if p[j] != '%' && p[j] != '_' {
					out = append(out, p[j])
				}
			}
			return string(out)
		}
	}
	return p
}
