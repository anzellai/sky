// analytics_store.go — Std.Analytics SQLite persistence.
//
// Events are persisted to a SQLite store so the console can render history +
// aggregates. DB path resolution (env > sky.toml > reuse):
//
//	SKY_ANALYTICS_DB_PATH  (env, or sky.toml `[analytics] dbPath`)  — override
//	SKY_CONSOLE_DB_PATH    (reuse the console DB — the default)
//	(unset)                — no store; the configured Sinks are the only output
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
	"sync"
)

var (
	analyticsStoreOnce sync.Once
	analyticsStoreDB   *sql.DB
)

const analyticsSchema = `
CREATE TABLE IF NOT EXISTS analytics_events (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	ts           INTEGER NOT NULL,
	anonymous_id TEXT,
	user_id      TEXT,
	event        TEXT NOT NULL,
	props        TEXT,
	context      TEXT
);
CREATE INDEX IF NOT EXISTS idx_analytics_ts ON analytics_events(ts);
CREATE INDEX IF NOT EXISTS idx_analytics_event ON analytics_events(event);
`

// analyticsStorePath resolves the configured store path (override > console DB
// reuse > none).
func analyticsStorePath() string {
	if p := skyGetenv("ANALYTICS_DB_PATH"); p != "" {
		return p
	}
	return os.Getenv("SKY_CONSOLE_DB_PATH")
}

// analyticsStore lazily opens (once) the analytics store, or nil when no path is
// configured / the open fails.
func analyticsStore() *sql.DB {
	analyticsStoreOnce.Do(func() {
		path := analyticsStorePath()
		if path == "" {
			return
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			return
		}
		db.SetMaxOpenConns(1)
		for _, p := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=5000`} {
			if _, err := db.Exec(p); err != nil {
				db.Close()
				return
			}
		}
		if _, err := db.Exec(analyticsSchema); err != nil {
			db.Close()
			return
		}
		analyticsStoreDB = db
	})
	return analyticsStoreDB
}

// analyticsStoreInsert persists one already-gated event row. No-op when no
// store is configured. Called from analyticsEmit (main app — sole writer).
func analyticsStoreInsert(payload map[string]any) {
	db := analyticsStore()
	if db == nil {
		return
	}
	ts, _ := payload["ts"].(int64)
	event, _ := payload["event"].(string)
	anonID, _ := payload["anonymous_id"].(string)
	userID, _ := payload["user_id"].(string)
	_, _ = db.Exec(
		`INSERT INTO analytics_events (ts, anonymous_id, user_id, event, props, context) VALUES (?, ?, ?, ?, ?, ?)`,
		ts, nullableStr(anonID), nullableStr(userID), event,
		analyticsJSONText(payload["props"]), analyticsJSONText(payload["context"]),
	)
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
		id := fmt.Sprintf("%v", unwrapAny(idArg))
		res, err := db.Exec(`DELETE FROM analytics_events WHERE anonymous_id = ? OR user_id = ?`, id, id)
		if err != nil {
			return Err[any, any](ErrUnexpected("analytics.erase: " + err.Error()))
		}
		n, _ := res.RowsAffected()
		return Ok[any, any](n)
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
