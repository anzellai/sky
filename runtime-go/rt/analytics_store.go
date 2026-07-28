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
	"sync"
)

// analyticsDefaultStorePath is where analytics persists when neither
// SKY_ANALYTICS_DB_PATH nor SKY_CONSOLE_DB_PATH is set — so enabling analytics
// works with no extra config. Project-local + hidden; the console reads it too.
const analyticsDefaultStorePath = ".sky/analytics.db"

var (
	analyticsStoreOnce       sync.Once
	analyticsStoreDB         *sql.DB
	analyticsNoStoreWarnOnce sync.Once
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
	if p := os.Getenv("SKY_CONSOLE_DB_PATH"); p != "" {
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
		// Ensure the parent dir exists (the default `.sky/` won't exist yet).
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0o755)
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

// ── query / aggregate helpers (read the store) ───────────────────────────
// These read whatever store this process resolves (the console sub-process
// inherits the same SKY_*_DB_PATH env, so it reads the app's events). Each is a
// no-op-safe read: no store → an empty/zero result, never an error.

// Analytics_totalEvents : Task Error Int
func Analytics_totalEvents(_ any) any {
	return func() any {
		db := analyticsStore()
		if db == nil {
			return Ok[any, any](int64(0))
		}
		var n int64
		_ = db.QueryRow(`SELECT count(*) FROM analytics_events`).Scan(&n)
		return Ok[any, any](n)
	}
}

// Analytics_uniqueUsers : Task Error Int — distinct identified users.
func Analytics_uniqueUsers(_ any) any {
	return func() any {
		db := analyticsStore()
		if db == nil {
			return Ok[any, any](int64(0))
		}
		var n int64
		_ = db.QueryRow(`SELECT count(DISTINCT user_id) FROM analytics_events WHERE user_id IS NOT NULL`).Scan(&n)
		return Ok[any, any](n)
	}
}

// Analytics_eventCounts : Task Error (List (String, Int)) — count per event
// name, most frequent first.
func Analytics_eventCounts(_ any) any {
	return func() any {
		db := analyticsStore()
		if db == nil {
			return Ok[any, any]([]any{})
		}
		rows, err := db.Query(`SELECT event, count(*) AS c FROM analytics_events GROUP BY event ORDER BY c DESC`)
		if err != nil {
			return Err[any, any](ErrUnexpected("analytics.eventCounts: " + err.Error()))
		}
		defer rows.Close()
		out := []any{}
		for rows.Next() {
			var ev string
			var c int64
			if err := rows.Scan(&ev, &c); err == nil {
				out = append(out, SkyTuple2{V0: ev, V1: c})
			}
		}
		return Ok[any, any](out)
	}
}

// Analytics_recentEvents : Int -> Task Error (List String) — the last N events,
// each a JSON object (ts, event, anonymous_id?, user_id?, props?, context?).
func Analytics_recentEvents(nArg any) any {
	return func() any {
		db := analyticsStore()
		if db == nil {
			return Ok[any, any]([]any{})
		}
		n := AsInt(nArg)
		if n <= 0 || n > 1000 {
			n = 50
		}
		rows, err := db.Query(
			`SELECT ts, event, anonymous_id, user_id, props, context FROM analytics_events ORDER BY id DESC LIMIT ?`, n)
		if err != nil {
			return Err[any, any](ErrUnexpected("analytics.recentEvents: " + err.Error()))
		}
		defer rows.Close()
		out := []any{}
		for rows.Next() {
			var ts int64
			var ev string
			var anon, uid, props, ctx sql.NullString
			if err := rows.Scan(&ts, &ev, &anon, &uid, &props, &ctx); err != nil {
				continue
			}
			obj := map[string]any{"ts": ts, "event": ev}
			if anon.Valid {
				obj["anonymous_id"] = anon.String
			}
			if uid.Valid {
				obj["user_id"] = uid.String
			}
			if props.Valid {
				obj["props"] = json.RawMessage(props.String)
			}
			if ctx.Valid {
				obj["context"] = json.RawMessage(ctx.String)
			}
			if b, err := json.Marshal(obj); err == nil {
				out = append(out, string(b))
			}
		}
		return Ok[any, any](out)
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
