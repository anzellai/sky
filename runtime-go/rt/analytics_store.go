// analytics_store.go — Std.Analytics SQLite persistence.
//
// Events are persisted to a SQLite store so the console can render history +
// aggregates. DB path resolution (override > reuse console > sensible default):
//
//	SKY_ANALYTICS_DB_PATH  (env, or sky.toml `[analytics] dbPath`)  — override
//	SKY_CONSOLE_DB_PATH    (reuse the console DB when set)
//	(unset)                — DEFAULT to a project-local `.sky/analytics.db`, so
//	                         enabling analytics WORKS OUT-OF-THE-BOX (no config)
//	                         and the console Analytics tab reads the same file.
//	                         The file is only created when analytics is actually
//	                         used. gitignore `.sky/`.
//
// Concurrency (the single-vs-multi-writer question): the MAIN app process is
// the sole analytics WRITER (track / trackEvent / page-view all run here); the
// console sub-process only READS. We open our OWN *sql.DB with the SAME
// `journal_mode=WAL` + `busy_timeout=5000` the telemetry persistence uses — its
// comment is already tuned for "a second writer holding the write lock", so the
// analytics write-handle, the telemetry write-handle, and the console read
// coexist safely on one LOCAL file. A network filesystem is unsafe (WAL is
// undefined there) — the same single-host constraint the console DB already
// carries. `MaxOpenConns(1)` serialises this process's own analytics writes.
package rt

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// analyticsDefaultStorePath is where analytics persists when neither
// SKY_ANALYTICS_DB_PATH nor SKY_CONSOLE_DB_PATH is set — so enabling analytics
// works with no extra config. Project-local + hidden; the console reads it too.
const analyticsDefaultStorePath = ".sky/analytics.db"

var (
	analyticsStoreOnce        sync.Once
	analyticsStoreDB          *sql.DB
	analyticsNoStoreWarnOnce  sync.Once
	analyticsWriteErrWarnOnce sync.Once
	// analyticsWriterInst is the single buffered writer for this process,
	// created alongside the store inside analyticsStoreOnce. See
	// analytics_writer.go.
	analyticsWriterInst *analyticsWriter
)

// analyticsDriverName is "sqlite" or "pgx", set when the store opens. It drives
// the dialect differences: `?` vs `$1` placeholders + the autoincrement column.
var analyticsDriverName = "sqlite"

// analyticsBackend classifies the configured store into a (driver, dsn) pair.
// A `postgres://` / `postgresql://` value → the shared Postgres (same pgx driver
// the session store uses); anything else is a local SQLite file path.
func analyticsBackend(path string) (driver, dsn string) {
	if strings.HasPrefix(path, "postgres://") || strings.HasPrefix(path, "postgresql://") {
		return "pgx", path
	}
	return "sqlite", path
}

// analyticsQ rewrites `?` placeholders to `$1,$2,…` for pgx; SQLite keeps `?`.
// Queries are AUTHORED with `?` and passed through this once so there is a single
// SQL string per query, not a per-dialect fork.
//
// This overload reads the process-wide `analyticsDriverName`, which is set when
// the store opens. That is correct for the query helpers below, which only ever
// run against the one store this process opened — but it is a trap for anything
// holding its OWN handle, so such code calls analyticsQFor with the driver it
// actually has. The buffered writer does; a version of it that did not silently
// sent `?` placeholders to PostgreSQL and every batch failed with
// `syntax error at or near ","`.
func analyticsQ(sql string) string {
	return analyticsQFor(analyticsDriverName, sql)
}

// analyticsQFor is analyticsQ with the driver passed explicitly, mirroring
// telemetryQ in the telemetry package.
func analyticsQFor(driver, sql string) string {
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

// analyticsSchemaStmts returns the CREATE statements for the driver, run
// individually (pgx's simple protocol doesn't batch `;`-separated statements).
// Only the id column differs; `BIGINT` ts + `TEXT` cols are portable.
func analyticsSchemaStmts(driver string) []string {
	idCol := "id INTEGER PRIMARY KEY AUTOINCREMENT"
	if driver == "pgx" {
		idCol = "id BIGSERIAL PRIMARY KEY"
	}
	return []string{
		`CREATE TABLE IF NOT EXISTS analytics_events (
			` + idCol + `,
			ts           BIGINT NOT NULL,
			anonymous_id TEXT,
			user_id      TEXT,
			event        TEXT NOT NULL,
			props        TEXT,
			context      TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_analytics_ts ON analytics_events(ts)`,
		`CREATE INDEX IF NOT EXISTS idx_analytics_event ON analytics_events(event)`,
	}
}

// analyticsStorePath resolves the configured store path (override > console DB
// reuse > none).
func analyticsStorePath() string {
	if p := skyGetenv("ANALYTICS_DB_PATH"); p != "" {
		return p
	}
	if p := os.Getenv("SKY_CONSOLE_DB_PATH"); p != "" {
		return p
	}
	// One-DB-for-everything: if the app is on a Postgres DATABASE_URL, analytics
	// lands in the SAME database (one connection string for app data, sessions,
	// and analytics). Falls through to the local SQLite default otherwise.
	if p := os.Getenv("DATABASE_URL"); strings.HasPrefix(p, "postgres") {
		return p
	}
	return analyticsDefaultStorePath
}

// analyticsStore lazily opens (once) the analytics store, or nil when no path is
// configured / the open fails.
func analyticsStore() *sql.DB {
	analyticsStoreOnce.Do(func() {
		path := analyticsStorePath()
		if path == "" {
			return
		}
		driver, dsn := analyticsBackend(path)
		analyticsDriverName = driver
		if driver == "sqlite" {
			// Ensure the parent dir exists (the default `.sky/` won't exist yet).
			if dir := filepath.Dir(dsn); dir != "" && dir != "." {
				_ = os.MkdirAll(dir, 0o755)
			}
		}
		db, err := sql.Open(driver, dsn)
		if err != nil {
			return
		}
		// Pool sizing for both drivers. SQLite is pinned to one connection;
		// Postgres gets the auxiliary sizing (a quarter of the app pool) —
		// see db_pool.go.
		//
		// The comment this replaces said "Postgres handles concurrency
		// natively — no single-conn cap", which conflated two things.
		// Postgres does handle concurrency natively; that is not a reason to
		// leave `MaxOpenConns` at Go's zero value, which means UNLIMITED and
		// makes a burst of analytics writes able to exhaust the same
		// `max_connections` budget the app's own queries need.
		if driver == "sqlite" {
			sqlitePoolConfig().applyTo(db)
		} else {
			dbAuxPoolConfig().applyTo(db)
		}
		if driver == "sqlite" {
			// SQLite is single-file: WAL so the console reader coexists with
			// this process's writes.
			for _, p := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=5000`} {
				if _, err := db.Exec(p); err != nil {
					db.Close()
					return
				}
			}
		}
		for _, stmt := range analyticsSchemaStmts(driver) {
			if _, err := db.Exec(stmt); err != nil {
				db.Close()
				return
			}
		}
		analyticsStoreDB = db
		analyticsWriterInst = newAnalyticsWriter(db, driver)
		analyticsStartRetention(db)
	})
	return analyticsStoreDB
}

// analyticsFlushPending drains the buffered writer synchronously.
//
// Called by every path that READS the analytics store, so that buffering is
// invisible to a reader: the console's Analytics tab, `Analytics.erase` and
// `Analytics.openStore` all see the events emitted before they were called.
// For `erase` this is a correctness requirement rather than a nicety — a
// right-to-erasure request that deleted the rows on disk while leaving the
// same subject's events sitting in the queue would re-materialise them a
// quarter of a second later.
//
// No-op when no writer exists (unconfigured store, or a test that never
// opened one).
func analyticsFlushPending() {
	w := analyticsWriterInst
	if w == nil {
		return
	}
	w.flushNow()
}

// analyticsStartRetention launches a periodic pruner when
// `[analytics] retention` (SKY_ANALYTICS_RETENTION, e.g. "90d" / "720h") is set,
// deleting events older than the window so the table stays bounded in
// long-running production. Unset = keep everything (no prune). One goroutine,
// one indexed DELETE — cheap + simple.
func analyticsStartRetention(db *sql.DB) {
	window := analyticsRetentionWindow()
	if window <= 0 {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		prune := func() {
			cutoff := time.Now().Add(-window).UnixMilli()
			_, _ = db.Exec(analyticsQ(`DELETE FROM analytics_events WHERE ts < ?`), cutoff)
		}
		prune() // once at startup
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for range t.C {
			prune()
		}
	}()
}

// analyticsRetentionWindow parses SKY_ANALYTICS_RETENTION — a Go duration
// ("720h") or an "<n>d" day form ("90d"). 0 when unset/invalid (keep all).
func analyticsRetentionWindow() time.Duration {
	v := skyGetenv("ANALYTICS_RETENTION")
	if v == "" {
		return 0
	}
	if strings.HasSuffix(v, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour
		}
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	return 0
}

// analyticsStoreInsert queues one already-gated event row for the buffered
// writer. No-op when no store is configured. Called from analyticsEmit (main
// app — sole writer).
//
// # This is an ENQUEUE, not an INSERT
//
// It used to execute the INSERT inline, which put an fsync on whatever
// goroutine emitted the event — usually a request handler rendering a page.
// It now marshals the row and hands it to the single writer goroutine, which
// batches it (see analytics_writer.go). The function keeps its name because
// its CONTRACT to the caller is unchanged: "record this event, best effort,
// do not fail my request over it".
//
// # Overflow policy: DROP-NEWEST, counted
//
// When the queue is full the incoming event is dropped and the drop is
// counted — the send is non-blocking.
//
// BLOCKING was rejected: it reinstates exactly the coupling this change
// removes, and worse, because a full queue means the store is already
// struggling, so blocking would apply the back-pressure of a stalled disk
// directly to every request handler in the app. Analytics must never be able
// to take the app down.
//
// DROP-OLDEST was rejected as the wrong trade for THIS data. It costs a
// second synchronisation point on the hot path (a channel cannot evict its
// own head, so it needs a ring plus a lock) to buy a property analytics does
// not want: under sustained overload drop-oldest keeps the most recent events
// and discards the beginning of the incident, whereas the events adjacent to
// the START of a burst are the ones that explain it. Drop-newest also leaves
// the retained events a contiguous prefix in time rather than a window with a
// hole in it, which is what makes a funnel or a session reconstruction from
// the survivors honest rather than subtly wrong.
//
// Dropping is correct for analytics; dropping SILENTLY is not. Drops are
// counted in `analyticsWriter.dropped`, warned about once per process with a
// running total, and republished every flush as the
// `sky_analytics_events_dropped_total` counter.
func analyticsStoreInsert(payload map[string]any) {
	db := analyticsStore()
	if db == nil {
		// With the default `.sky/analytics.db` fallback a nil store means the
		// store FAILED to open (unwritable dir, bad path) — warn ONCE so the
		// dropped events are discoverable rather than silent.
		analyticsNoStoreWarnOnce.Do(func() {
			logStructured("warn", "analytics.store_unavailable",
				"detail", "analytics store could not be opened — events are being dropped",
				"path", analyticsStorePath(),
				"fix", "check the path is writable, or set [analytics] dbPath in sky.toml")
		})
		return
	}
	w := analyticsWriterInst
	if w == nil {
		return
	}
	ts, _ := payload["ts"].(int64)
	event, _ := payload["event"].(string)
	anonID, _ := payload["anonymous_id"].(string)
	userID, _ := payload["user_id"].(string)
	// Marshalled HERE, on the emitting goroutine, so the caller's map is not
	// retained past this call. Handing the map itself to the queue would make
	// any later mutation by the caller a data race with the writer.
	w.enqueue(analyticsRow{
		ts:     ts,
		anonID: nullableStr(anonID),
		userID: nullableStr(userID),
		event:  event,
		props:  analyticsJSONText(payload["props"]),
		ctx:    analyticsJSONText(payload["context"]),
	})
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Analytics_erase implements:
//
//	Std.Analytics.erase : String -> Task Error Int
//
// Right-to-erasure: delete every stored event for an anonymous OR user id from
// the LOCAL store, returning the row count. Events already exported to a
// provider must be erased there (out of the stdlib's reach). No-op (Ok 0) when
// no store is configured.
func Analytics_erase(idArg any) any {
	return func() any {
		db := analyticsStore()
		if db == nil {
			return Ok[any, any](int64(0))
		}
		// Erasure must see the queue. See analyticsFlushPending: a subject's
		// events still buffered when the DELETE ran would land on disk
		// moments after the app reported them erased.
		analyticsFlushPending()
		id := fmt.Sprintf("%v", unwrapAny(idArg))
		res, err := db.Exec(analyticsQ(`DELETE FROM analytics_events WHERE anonymous_id = ? OR user_id = ?`), id, id)
		if err != nil {
			return Err[any, any](ErrUnexpected("analytics.erase: " + err.Error()))
		}
		n, _ := res.RowsAffected()
		return Ok[any, any](n)
	}
}

// ── query / aggregate helpers (read the store) ───────────────────────────
// These read whatever store this process resolves (the console sub-process
// inherits the same SKY_*_DB_PATH env, so it reads the app's events). Each is a
// no-op-safe read: no store → an empty/zero result, never an error.

// Analytics_openStore : () -> Task Error Db
// Hands back a Std.Db handle over the analytics events store (the console DB or
// the [analytics] dbPath), so apps/admins can query, aggregate (Store.selectRaw)
// and patch analytics events with Std.Db.Store — the same typed, "if it compiles
// it works" path as any other table. The consent-gated WRITE (track) stays in the
// runtime; this is the read / query / update / aggregate side.
func Analytics_openStore(_ any) any {
	return func() any {
		db := analyticsStore()
		if db == nil {
			return Err[any, any](ErrIo("analytics store is not configured — set [analytics] dbPath (or SKY_CONSOLE_DB_PATH), or run under the dev console"))
		}
		// Hand back a store whose queued writes are already on disk, so the
		// first query through this handle is not missing the events the app
		// emitted a moment ago.
		analyticsFlushPending()
		driver, _ := analyticsBackend(analyticsStorePath())
		return Ok[any, any](&SkyDb{conn: db, driver: driver, name: "analytics"})
	}
}

// analyticsJSONText marshals props/context to a JSON TEXT column (nil → NULL).
func analyticsJSONText(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(b)
}
