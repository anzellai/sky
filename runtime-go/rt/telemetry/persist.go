//go:build !js

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
	"math"
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
		// The hourly prune deletes on a bare observed_at range, which
		// the composite (name, observed_at) index above CANNOT serve —
		// it leads on name. Without this single-column index the prune
		// was a full table scan of up to ~300M rows every hour, on the
		// pool the session store shares. IF NOT EXISTS + schema-on-open
		// means existing databases pick it up at next boot.
		`CREATE INDEX IF NOT EXISTS idx_metric_observed_at ON telemetry_metric (observed_at)`,
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

// metricAggregationWindowOverride lets a test force a window without touching
// the process environment (positive = that duration; 0 = read the env). Tests
// never need to force-0 because 0 is already the default.
var metricAggregationWindowOverride atomic.Int64

// metricAggregationWindow is the counter-coalescing window: within it, all but
// the last persisted row per (name,labels) counter series is redundant (the
// value is cumulative), so the flusher keeps only the survivor. 0 DISABLES
// coalescing — every row is written, exactly as before this landed. That is
// the DEFAULT: a non-zero window reduces the persisted-row time-resolution of
// the out-of-repo SkyDeploy console's counter graphs (lossless for rate/delta,
// only sub-window points are lost), which is a change to an external contract
// the runtime cannot see — so it is opt-in via SKY_TELEMETRY_AGGREGATION_WINDOW
// (a Go duration, e.g. "10s"). Gauges and histograms are never coalesced.
func metricAggregationWindow() time.Duration {
	if d := metricAggregationWindowOverride.Load(); d > 0 {
		return time.Duration(d)
	}
	v := strings.TrimSpace(os.Getenv("SKY_TELEMETRY_AGGREGATION_WINDOW"))
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0 // unparseable or non-positive → disabled, never a surprise window
	}
	return d
}

// parseHumanBytes parses an operator capacity string ("100GB", "1.5TiB",
// "512mb", "100 GB", "0", or a bare byte count) into bytes. ok=false on any
// non-empty-but-malformed input (bad number, negative, unknown unit, int64
// overflow) — the CALLER turns that into a one-shot warning, never a silent
// zero, so a typo cannot quietly drop the capacity danger flag. Decimal units
// are powers of 1000 (GB=1e9, the marketing/cloud convention); binary units
// (GiB=2^30) are powers of 1024; a bare number is bytes. "0" is a valid
// explicit disable.
func parseHumanBytes(v string) (int64, bool) {
	v = strings.ReplaceAll(strings.TrimSpace(v), " ", "")
	if v == "" {
		return 0, false
	}
	upper := strings.ToUpper(v)
	var mult float64 = 1
	numPart := v
	switch {
	case strings.HasSuffix(upper, "TIB"):
		mult, numPart = 1<<40, v[:len(v)-3]
	case strings.HasSuffix(upper, "GIB"):
		mult, numPart = 1<<30, v[:len(v)-3]
	case strings.HasSuffix(upper, "MIB"):
		mult, numPart = 1<<20, v[:len(v)-3]
	case strings.HasSuffix(upper, "KIB"):
		mult, numPart = 1<<10, v[:len(v)-3]
	case strings.HasSuffix(upper, "TB"):
		mult, numPart = 1e12, v[:len(v)-2]
	case strings.HasSuffix(upper, "GB"):
		mult, numPart = 1e9, v[:len(v)-2]
	case strings.HasSuffix(upper, "MB"):
		mult, numPart = 1e6, v[:len(v)-2]
	case strings.HasSuffix(upper, "KB"):
		mult, numPart = 1e3, v[:len(v)-2]
	case strings.HasSuffix(upper, "B"):
		mult, numPart = 1, v[:len(v)-1]
	}
	f, err := strconv.ParseFloat(numPart, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	b := f * mult
	if b > math.MaxInt64 { // overflow — never wrap to a negative threshold
		return 0, false
	}
	return int64(b), true
}

// capacityBytes resolves SKY_TELEMETRY_DB_CAPACITY to a byte quota (0 =
// disabled). Unset → 0, silently (the operator did not ask). Set-but-malformed
// → 0 AND a one-shot WARN (a typo like "100 gigs" must not silently drop the
// danger flag the operator believes protects them).
func (p *persistence) capacityBytes(s *Store) int64 {
	raw := os.Getenv("SKY_TELEMETRY_DB_CAPACITY")
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	b, ok := parseHumanBytes(raw)
	if !ok {
		p.capacityWarnOnce.Do(func() {
			s.logs.append(LogEntry{
				TS:      time.Now(),
				Level:   "warn",
				Message: "SKY_TELEMETRY_DB_CAPACITY is set but unparseable; the DB-capacity danger flag is disabled",
				Fields:  map[string]string{"value": raw},
			})
		})
		return 0
	}
	return b
}

// metricHistogramWindowOverride — test hook, parallel to the counter one.
var metricHistogramWindowOverride atomic.Int64

// histogramAggregationWindow is the histogram-coalescing window, gated by
// SKY_TELEMETRY_HISTOGRAM_AGGREGATION_WINDOW (a Go duration; default 0 = off).
// It is DELIBERATELY separate from the counter window: coalescing a histogram
// is a lossy, BUCKET-RESOLUTION aggregation — today's rows carry the raw
// full-precision observation, so a reader can compute exact quantiles / max,
// which bucket rows cannot — and it is a BREAKING representation change for the
// out-of-repo SkyDeploy console (which reconstructs from per-observation rows).
// So it ships default-off and enabling it requires a bucket-aware reader; the
// counter window (lossless for rate/delta) must not silently drag it along.
func histogramAggregationWindow() time.Duration {
	if d := metricHistogramWindowOverride.Load(); d > 0 {
		return time.Duration(d)
	}
	v := strings.TrimSpace(os.Getenv("SKY_TELEMETRY_HISTOGRAM_AGGREGATION_WINDOW"))
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0
	}
	return d
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
	// dsn is the resolved backend target (a SQLite file path, or a
	// postgres:// URL). Retained so the size report can statfs the SQLite
	// file's directory for free space.
	dsn string
	// localDataDir is the filesystem path of a Postgres data directory THIS
	// process owns — set only for the EMBEDDED cluster (plumbed from the
	// supervisor's cfg.dataDir via EnablePersistenceFromEnvWithLocalDir). Empty
	// for SQLite (its own path drives statfs) and for any external/remote pg
	// (whose disk this process cannot see). It is the "same server, I can
	// statfs the DB's disk" signal for Postgres. Set at construction, before
	// the flusher/pruner goroutines spawn, so it is single-writer like the
	// growth fields below. (A non-default PGDATA tablespace on a different mount
	// would desync this from the DB's real disk; the embedded provisioner never
	// creates one.)
	localDataDir string
	// Size-report growth tracking. Written and read ONLY from the pruner
	// goroutine (reportSizes runs inside pruneCycle / at startup), so no lock is
	// needed — documenting the single-writer contract rather than guarding it.
	lastSizeBytes map[string]int64
	lastSizeAt    time.Time
	// capacityWarnOnce fires a single WARN when SKY_TELEMETRY_DB_CAPACITY is set
	// but unparseable, so a typo disables the quota danger flag loudly (once),
	// never silently.
	capacityWarnOnce sync.Once
	// dbstatChecked / dbstatOK memoise the one-time probe for the SQLite
	// dbstat vtable (absent in the default modernc build), so the size
	// report degrades to whole-DB bytes without re-probing every hour.
	dbstatChecked bool
	dbstatOK      bool
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
	return s.EnablePersistenceWithLocalDir(path, "")
}

// EnablePersistenceWithLocalDir is EnablePersistence plus the filesystem path of
// a Postgres data directory THIS process owns (the embedded cluster). Pass ""
// for SQLite or a remote/external Postgres. Setting it lets the size report
// statfs the embedded cluster's disk for a real free-space + danger signal.
func (s *Store) EnablePersistenceWithLocalDir(path, localDataDir string) error {
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
	// localDataDir applies only to a Postgres cluster this process owns; a
	// stray value on SQLite would mis-drive statfs, so ignore it there.
	ownedDir := ""
	if driver == "pgx" {
		ownedDir = localDataDir
	}
	p := &persistence{
		db:            db,
		pool:          handle,
		driver:        driver,
		dsn:           dsn,
		localDataDir:  ownedDir,
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
	return s.EnablePersistenceFromEnvWithLocalDir("")
}

// EnablePersistenceFromEnvWithLocalDir is EnablePersistenceFromEnv plus the
// embedded cluster's data-dir path. The embedded-Postgres boot path calls this
// (with the supervisor's cfg.dataDir) after it has exported DATABASE_URL, so the
// size report can statfs the cluster's disk. The localDataDir is used only if
// the resolved backend is that Postgres (EnablePersistenceWithLocalDir ignores
// it on SQLite).
func (s *Store) EnablePersistenceFromEnvWithLocalDir(localDataDir string) error {
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
	return s.EnablePersistenceWithLocalDir(path, localDataDir)
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

	// Counter-coalescing state. `window` is read ONCE at flusher start (it is a
	// deployment setting, not a per-request one). When window > 0, counter
	// entries are held in `coalesced` — keyed (name,labels), overwritten by
	// each later sample so only the last cumulative value per key survives —
	// and emitted every `window` instead of per-row. Memory is O(counter
	// cardinality) (already capped by checkCardinality), NOT O(rows). When
	// window == 0 the map stays empty and the windowTick never fires, so the
	// path below is byte-for-byte the old behaviour.
	window := metricAggregationWindow()
	coalesced := map[string]persistEntry{}
	var windowTickC <-chan time.Time
	if window > 0 {
		wt := time.NewTicker(window)
		defer wt.Stop()
		windowTickC = wt.C
	}

	// Histogram-coalescing state — SEPARATE knob + window from counters (a
	// histogram is a lossy bucket-resolution change with a distinct reader
	// contract; see histogramAggregationWindow). `dirtyHists` is a dirty-SET,
	// not a value cache: the in-RAM *histogramSeries is the source of truth for
	// the cumulative vector, so the map only records WHICH series were touched
	// this window (keyed by the stable series pointer, 1:1 with identity) and
	// its name (histogramSeries doesn't store its own name). At the window tick
	// each is snapshotted + exploded into cumulative bucket rows. O(dirty
	// series), independent of observation rate.
	histWindow := histogramAggregationWindow()
	dirtyHists := map[*histogramSeries]string{}
	var histTickC <-chan time.Time
	if histWindow > 0 {
		ht := time.NewTicker(histWindow)
		defer ht.Stop()
		histTickC = ht.C
	}

	// ingest routes ONE dequeued entry: a coalescable counter (counter window
	// on) overwrites its survivor in `coalesced`; a coalescable histogram
	// (histogram window on) marks its series dirty; everything else — logs,
	// spans, gauges, raw counters/histograms when their window is off — appends
	// to the batch exactly as before.
	ingest := func(e persistEntry) {
		if window > 0 && e.kind == "metric" && e.metric.mtype == "counter" {
			coalesced[e.metric.name+"\x00"+encodeAttrs(e.metric.labels)] = e
			return
		}
		if histWindow > 0 && e.kind == "metric" && e.metric.mtype == "histogram" && e.metric.hist != nil {
			dirtyHists[e.metric.hist] = e.metric.name
			return
		}
		batch = append(batch, e)
	}
	// drainQueue pulls everything already queued through ingest, until the
	// batch reaches the size cap or the queue empties.
	drainQueue := func() {
		for len(batch) < persistBatchSize {
			select {
			case e := <-p.queue:
				ingest(e)
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
	// flushCoalesced moves every counter survivor into the batch and clears the
	// map. Caller flushes the batch. This is the LOAD-BEARING step for
	// read-your-writes + shutdown-drain: a survivor still sitting in `coalesced`
	// has not been committed, so drainAll (flushReq + stop) MUST call this or a
	// FlushPersistence would return before an enqueued counter is visible and a
	// shutdown would lose it.
	flushCoalesced := func() {
		for k, e := range coalesced {
			batch = append(batch, e)
			delete(coalesced, k)
		}
	}
	// flushDirtyHists snapshots each dirty histogram series and explodes it into
	// cumulative bucket rows (`_bucket{le}` / `_sum` / `_count`) appended to the
	// batch, then clears the set. Same LOAD-BEARING role as flushCoalesced for
	// read-your-writes + shutdown-drain. The rows are appended DIRECTLY to the
	// batch (not re-enqueued), so they never re-enter ingest. emitHistogramSeries
	// clamps the vector monotonic (the per-field atomic-read skew is durable once
	// on disk). ~len(boundaries)+3 rows per dirty series per window.
	flushDirtyHists := func() {
		if len(dirtyHists) == 0 {
			return
		}
		ts := time.Now()
		for ser, name := range dirtyHists {
			sm := s.snapshotHistogram(name, ser)
			emitHistogramSeries(name, sm, func(rowName, leValue string, value float64, _ bool) {
				labels := sm.Labels
				if leValue != "" {
					labels = withLabel(sm.Labels, "le", leValue)
				}
				batch = append(batch, persistEntry{
					kind: "metric",
					metric: persistMetric{
						name:       rowName,
						labels:     labels,
						value:      value,
						mtype:      "histogram",
						observedAt: ts,
					},
				})
			})
			delete(dirtyHists, ser)
		}
	}
	// drainAll writes everything currently queued AND every held counter
	// survivor AND every dirty histogram, not merely one batch.
	drainAll := func() {
		for {
			drainQueue()
			flushCoalesced()
			flushDirtyHists()
			flush()
			if len(p.queue) == 0 && len(coalesced) == 0 && len(dirtyHists) == 0 {
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
			// the caller observes the close — including held counter survivors.
			drainAll()
			close(done)

		case e := <-p.queue:
			ingest(e)
			drainQueue()
			if len(batch) >= persistBatchSize {
				flush()
			}

		case <-tick.C:
			// 200ms cadence: commit the non-coalesced batch (logs / spans /
			// gauges / histograms / all counters when window==0).
			flush()

		case <-windowTickC:
			// Window boundary: emit the coalesced counter survivors. Never
			// fires when window==0 (windowTickC is nil).
			flushCoalesced()
			flush()

		case <-histTickC:
			// Histogram window boundary: explode each dirty series into
			// cumulative bucket rows. Never fires when the histogram window is
			// off (histTickC is nil).
			flushDirtyHists()
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
	// STARTUP size report — an immediate baseline the moment persistence is up,
	// so an already-large / already-near-full DB is visible at boot rather than
	// a minute later, and growth tracking starts from t=0. Under its own recover
	// (it is outside pruneCycle's). It does NOT prune — the prune timer below is
	// unchanged, so the first retention DELETE still waits the deliberate ~1 min.
	p.reportSizesRecovered(s)
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
	// P1 — measure the runtime-owned tables' on-disk footprint on the same
	// hourly cadence, POST-prune (so the number reflects steady state). Runs
	// under this cycle's recover, so a driver that panics on the size query
	// costs one report, not the retention loop.
	p.reportSizes(s, time.Now())
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

// telemetryOwnedTables are the runtime-owned tables the size report measures.
// Fixed allowlist — never user input — so interpolating a name into a
// COUNT/SUM query below is not an injection surface.
var telemetryOwnedTables = []string{"telemetry_log", "telemetry_metric", "telemetry_span"}

// reportSizes measures the on-disk footprint of the runtime-owned telemetry
// tables and emits ONE structured telemetry_log event per prune cycle. It is
// the only measurement of database size in the runtime — every figure in the
// perf docs before this was arithmetic.
//
// It deliberately emits a LOG event, never a telemetry_metric row: measuring
// the table you are trying to keep small by writing into it every hour is the
// trap this avoids (grill P1 attack 6). The event lands in the in-RAM ring
// (console-visible) and, via AppendLog, in telemetry_log (24h retention).
//
// Per-table BYTES are available on Postgres (pg_total_relation_size, a
// non-locking catalog function) and on SQLite only when the dbstat vtable is
// compiled in (it is NOT in the default modernc build) — so SQLite degrades to
// whole-database bytes (page_count*page_size, an O(1) header read). It never
// falls back to per-table COUNT(*): that is a full scan of the very
// telemetry_metric table whose growth is the concern, self-defeating on the
// shared pool.
//
// Free space is knowable only where the runtime owns the filesystem path — the
// SQLite file's directory. For a remote Postgres the data dir is unknown from
// here, so the report states absolute size + growth rate and leaves free space
// unreported. The low-space warning is a RATIO (free < total telemetry bytes),
// not a byte constant, so it scales with the disk.
// Danger thresholds (documented, overridable later if needed):
//   - owned-path: warn when free disk drops below this fraction of total disk
//     (the standard "disk near full" signal — captures ALL disk use, incl. WAL
//     and other databases, not just the figure this app can name).
//   - capacity: warn when the whole database exceeds this fraction of the
//     operator-declared SKY_TELEMETRY_DB_CAPACITY.
const (
	dangerFreeRatio     = 0.10
	dangerCapacityRatio = 0.90
)

func (p *persistence) reportSizes(s *Store, now time.Time) {
	// Bound every query so a wedged connection at startup can't stall the
	// pruner goroutine (and thus the first retention prune) indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fields := map[string]string{}
	sizes := map[string]int64{}
	var telemetryBytes int64 // the runtime-owned telemetry tables only
	var dbTotalBytes int64   // the WHOLE database (app + sessions + telemetry)
	perTableBytes := false

	switch p.driver {
	case "pgx":
		perTableBytes = true
		for _, t := range telemetryOwnedTables {
			var n int64
			// to_regclass tolerates a not-yet-created table (NULL → 0).
			row := p.db.QueryRowContext(ctx,
				`SELECT COALESCE(pg_total_relation_size(to_regclass($1)), 0)`, t)
			if err := row.Scan(&n); err == nil {
				sizes[t] = n
				telemetryBytes += n
			}
		}
		// Whole-DB size — cheap, non-locking, works remotely. Measures only
		// THIS database (excludes WAL / other DBs), so it under-counts true disk
		// use — which is why the owned-path danger flag keys on statfs free, not
		// on this figure.
		_ = p.db.QueryRowContext(ctx,
			`SELECT pg_database_size(current_database())`).Scan(&dbTotalBytes)
	default: // sqlite
		if p.sqliteHasDbstat() {
			perTableBytes = true
			for _, t := range telemetryOwnedTables {
				var n sql.NullInt64
				row := p.db.QueryRowContext(ctx, `SELECT SUM(pgsize) FROM dbstat WHERE name = ?`, t)
				if err := row.Scan(&n); err == nil && n.Valid {
					sizes[t] = n.Int64
					telemetryBytes += n.Int64
				}
			}
		} else {
			fields["breakdown"] = "whole-db (dbstat vtable absent)"
		}
		// Whole-DB size = page_count*page_size (an O(1) header read, never a
		// COUNT(*) scan). Deliberately the STABLE logical size, not
		// stat(main)+stat(-wal): the WAL balloons then checkpoints to ~0, so a
		// file-sum would make this figure oscillate and flap any threshold keyed
		// on it. WAL disk use still shows up correctly in statfs free space.
		var pageCount, pageSize int64
		if err := p.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err == nil {
			if err := p.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err == nil {
				dbTotalBytes = pageCount * pageSize
			}
		}
		if !perTableBytes {
			telemetryBytes = dbTotalBytes // best available without dbstat
		}
	}

	if perTableBytes {
		for _, t := range telemetryOwnedTables {
			fields[t+"_bytes"] = strconv.FormatInt(sizes[t], 10)
		}
	}
	// Distinct names: telemetry-tables-only vs the whole database, so neither is
	// misread as the other.
	fields["telemetry_total_bytes"] = strconv.FormatInt(telemetryBytes, 10)
	fields["db_total_bytes"] = strconv.FormatInt(dbTotalBytes, 10)
	fields["driver"] = p.driver

	// Growth rate vs the previous report (bytes/day projection), single-writer
	// state so no lock. First cycle has no prior — report rate as unknown.
	if !p.lastSizeAt.IsZero() {
		if dt := now.Sub(p.lastSizeAt).Seconds(); dt > 0 {
			deltaBytes := telemetryBytes - p.lastSizeBytes["telemetry_total_bytes"]
			perDay := int64(float64(deltaBytes) / dt * 86400)
			fields["growth_bytes_per_day"] = strconv.FormatInt(perDay, 10)
		}
	}
	if p.lastSizeBytes == nil {
		p.lastSizeBytes = map[string]int64{}
	}
	p.lastSizeBytes["telemetry_total_bytes"] = telemetryBytes
	p.lastSizeAt = now

	level := "info"
	warn := func(msg string) {
		level = "warn"
		fields["warning"] = msg
	}

	// Owned path = this process can statfs the DB's own disk: SQLite (its file)
	// or the EMBEDDED cluster (its data dir). External/remote pg cannot.
	statfsPath := ""
	switch {
	case p.driver == "sqlite":
		statfsPath = p.sqlitePath()
	case p.localDataDir != "":
		statfsPath = p.localDataDir
	}

	capacity := p.capacityBytes(s)
	if capacity > 0 {
		fields["db_capacity_bytes"] = strconv.FormatInt(capacity, 10)
	}

	if statfsPath != "" {
		if free, total, ok := freeBytesForPath(statfsPath); ok {
			fields["fs_free_bytes"] = strconv.FormatInt(free, 10)
			fields["fs_total_bytes"] = strconv.FormatInt(total, 10)
			// Primary owned-path danger: disk near full (free < 10% of total).
			if total > 0 && float64(free) < dangerFreeRatio*float64(total) {
				warn("disk nearly full: free space below 10% of the volume")
			}
		} else {
			fields["fs_free_bytes"] = "unknown"
		}
	} else {
		fields["fs_free_bytes"] = "unknown (remote backend)"
	}

	// Capacity danger applies on EVERY tier when the operator declared a quota —
	// it is the only signal available for a remote DB, and a useful extra one on
	// an owned path.
	if capacity > 0 && dbTotalBytes > 0 && float64(dbTotalBytes) > dangerCapacityRatio*float64(capacity) {
		warn("database size is above 90% of SKY_TELEMETRY_DB_CAPACITY")
	}

	s.AppendLog(LogEntry{
		TS:      now,
		Level:   level,
		Message: "telemetry.storage_size",
		Fields:  fields,
	})
}

// reportSizesRecovered runs reportSizes under its own recover, for the STARTUP
// call that sits outside pruneCycle's recover. Without this a driver panic on a
// cold handle at boot (modernc/pgx panic on a nil/closed handle) would kill the
// pruner goroutine and end retention for the process lifetime — the exact
// permanent-silence trap pruneCycle is structured to avoid.
func (p *persistence) reportSizesRecovered(s *Store) {
	defer func() {
		if r := recover(); r != nil {
			s.logs.append(LogEntry{
				TS:      time.Now(),
				Level:   "warn",
				Message: "telemetry startup size report panicked",
				Fields:  map[string]string{"panic": fmt.Sprintf("%v", r)},
			})
		}
	}()
	p.reportSizes(s, time.Now())
}

// sqlitePath strips any DSN query suffix so the bare file path can be handed
// to statfs. SQLite DSNs are usually a plain path but may carry `?_pragma=…`.
func (p *persistence) sqlitePath() string {
	if i := strings.IndexByte(p.dsn, '?'); i >= 0 {
		return p.dsn[:i]
	}
	return p.dsn
}

// sqliteHasDbstat probes once for the dbstat virtual table. The default
// modernc SQLite build omits it, so the probe (not a version guess) decides
// whether per-table bytes are available. Memoised — the answer can't change
// for the life of the handle.
func (p *persistence) sqliteHasDbstat() bool {
	if p.dbstatChecked {
		return p.dbstatOK
	}
	p.dbstatChecked = true
	// count(*) always returns exactly one row when the vtable exists (0 on an
	// empty DB) and errors "no such table" when it does not — so a nil Scan
	// error is a reliable presence signal, where `SELECT 1 … LIMIT 1` would
	// false-negative (ErrNoRows) on an empty dbstat.
	p.dbstatOK = p.db.QueryRow(`SELECT count(*) FROM dbstat`).Scan(new(int)) == nil
	return p.dbstatOK
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
