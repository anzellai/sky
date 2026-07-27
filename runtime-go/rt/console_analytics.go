// console_analytics.go — the Std.Analytics console API endpoint.
//
// Serves GET /_sky/console/api/analytics for the console's Analytics
// tab (sky-bundled/console/src/AnalyticsTab.sky). Because the embedded
// console runs IN-PROCESS with the host app (MountLiveSubAppInProcess),
// this handler reads the SAME analyticsStore() singleton the app writes
// to — no second DB, no env plumbing, no cross-process fetch. When no
// analytics store is configured the handler returns a zero/empty
// payload (HTTP 200), matching the no-op-safe contract of the
// Analytics_* query helpers.
//
// Wire shape mirrors the console's `analyticsDecoder` (Main.sky):
//
//	{ "total": N,
//	  "uniqueUsers": N,
//	  "counts": [ { "event": "page_view", "count": 128 }, ... ],
//	  "recent": [ { "ts": 1719500000000, "event": "signup",
//	               "userId": "u1" }, ... ] }
package rt

import (
	"database/sql"
	"net/http"
)

type consoleEventCount struct {
	Event string `json:"event"`
	Count int64  `json:"count"`
}

type consoleAnalyticsEvent struct {
	TS     int64  `json:"ts"`
	Event  string `json:"event"`
	UserID string `json:"userId"`
}

type consoleAnalyticsResponse struct {
	Total       int64                   `json:"total"`
	UniqueUsers int64                   `json:"uniqueUsers"`
	Counts      []consoleEventCount     `json:"counts"`
	Recent      []consoleAnalyticsEvent `json:"recent"`
}

// HandleConsoleAnalytics implements GET /_sky/console/api/analytics.
func HandleConsoleAnalytics(w http.ResponseWriter, r *http.Request) {
	if !consoleAccessAllowed(w, r) {
		return
	}
	// Always emit non-nil slices so the JSON is `[]` not `null`
	// (keeps the Sky decoder's List path happy either way).
	out := consoleAnalyticsResponse{
		Counts: []consoleEventCount{},
		Recent: []consoleAnalyticsEvent{},
	}
	db := analyticsStore()
	if db == nil {
		writeJSON(w, out)
		return
	}

	_ = db.QueryRow(`SELECT count(*) FROM analytics_events`).Scan(&out.Total)
	_ = db.QueryRow(
		`SELECT count(DISTINCT user_id) FROM analytics_events WHERE user_id IS NOT NULL`,
	).Scan(&out.UniqueUsers)

	if rows, err := db.Query(
		`SELECT event, count(*) AS c FROM analytics_events GROUP BY event ORDER BY c DESC LIMIT 50`,
	); err == nil {
		for rows.Next() {
			var c consoleEventCount
			if err := rows.Scan(&c.Event, &c.Count); err == nil {
				out.Counts = append(out.Counts, c)
			}
		}
		rows.Close()
	}

	if rows, err := db.Query(
		`SELECT ts, event, user_id FROM analytics_events ORDER BY id DESC LIMIT 50`,
	); err == nil {
		for rows.Next() {
			var e consoleAnalyticsEvent
			var uid sql.NullString
			if err := rows.Scan(&e.TS, &e.Event, &uid); err == nil {
				if uid.Valid {
					e.UserID = uid.String
				}
				out.Recent = append(out.Recent, e)
			}
		}
		rows.Close()
	}

	writeJSON(w, out)
}
