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
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type consoleEventCount struct {
	Event string `json:"event"`
	Count int64  `json:"count"`
}

type consoleAnalyticsEvent struct {
	TS     int64  `json:"ts"`
	Event  string `json:"event"`
	UserID string `json:"userId"`
	// Path is the `props.path` value (present on `page_view` and any event the
	// app tags with a path) — surfaced so the recent stream shows WHICH page was
	// viewed, not a bare "page_view". Empty for events without a path prop.
	Path string `json:"path,omitempty"`
}

// consoleCurrencyTotal is one currency's summed revenue. Money props are
// stored losslessly as "ISO_CODE AMOUNT" (e.g. "USD 19.99"), so revenue can
// only be summed WITHIN a currency — never across — which is exactly what
// grouping by currency gives.
type consoleCurrencyTotal struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
	Count    int64  `json:"count"`
}

type consoleAnalyticsResponse struct {
	Total       int64                   `json:"total"`
	UniqueUsers int64                   `json:"uniqueUsers"`
	Counts      []consoleEventCount     `json:"counts"`
	Recent      []consoleAnalyticsEvent `json:"recent"`
	Revenue     []consoleCurrencyTotal  `json:"revenue"`
	// WindowDays is how far back EVERY figure above was computed over, and it
	// is on the wire so the console can SAY so. See consoleAnalyticsWindow:
	// these numbers used to be all-time, and a windowed number rendered under
	// an all-time label is a wrong number, not a fast one.
	WindowDays int `json:"windowDays"`
	// RowCapHit reports that the revenue rollup stopped at its row cap, so its
	// total is a floor over the window rather than the whole of it. The
	// console renders "≥" when this is set.
	RowCapHit bool `json:"rowCapHit"`
	// RowCap is the cap itself, so the console can name it without a second
	// copy of the constant.
	RowCap int `json:"rowCap"`
}

// The console's Analytics tab is an OBSERVABILITY surface running on a
// connection pool it SHARES with the session store (analytics_store.go's
// dbshare.Acquire), and the read paths are deliberately outside the write
// semaphore (analytics_store.go:161-165). An unbounded scan here therefore
// competes with session reads on the request path — the observability surface
// degrading the thing it observes. Every statement below is bounded twice: by
// a time window that an index can seek to, and by a row cap.
const (
	// consoleAnalyticsWindow is how far back the tab looks. 30 days is chosen
	// to match the shortest retention window an operator is likely to set
	// (`[analytics] retention`), so the tab does not routinely claim to
	// summarise a period the store no longer holds.
	consoleAnalyticsWindow = 30 * 24 * time.Hour
	// consoleAnalyticsWindowDays is the same figure, for the wire + the label.
	consoleAnalyticsWindowDays = 30
	// consoleAnalyticsRowCap bounds the rows the aggregates walk. The revenue
	// rollup json.Unmarshals EVERY row it reads, in Go, on the request
	// goroutine — that per-row cost is what makes an uncapped scan expensive,
	// not the SQL. 20k rows is ~2 orders of magnitude more than a dev console
	// needs to show a trend and still parses in tens of milliseconds.
	consoleAnalyticsRowCap = 20000
)

// The statements the Analytics tab runs. Named constants rather than literals
// at the call sites, because `console-analytics-queries-are-bounded` EXPLAINs
// these exact strings: a gate holding its own copy of the SQL would keep
// passing after the handler's copy regressed.
//
// Every one seeks `idx_analytics_ts` for its window and stops at a LIMIT. The
// two aggregates read their window through a capped sub-select rather than
// aggregating the range directly, so the work is bounded by the cap even when
// the window holds ten million rows.
const (
	qConsoleTotal = `SELECT count(*) FROM (
		SELECT 1 FROM analytics_events WHERE ts >= ? ORDER BY ts DESC LIMIT ?
	) AS w`

	qConsoleUniqueUsers = `SELECT count(DISTINCT user_id) FROM (
		SELECT user_id FROM analytics_events
		WHERE user_id IS NOT NULL AND ts >= ? ORDER BY ts DESC LIMIT ?
	) AS w`

	qConsoleEventCounts = `SELECT event, count(*) AS c FROM (
		SELECT event FROM analytics_events WHERE ts >= ? ORDER BY ts DESC LIMIT ?
	) AS w GROUP BY event ORDER BY c DESC LIMIT 50`

	qConsoleRecent = `SELECT ts, event, user_id, props FROM analytics_events
		WHERE ts >= ? ORDER BY ts DESC LIMIT 50`

	qConsoleRevenue = `SELECT props FROM analytics_events
		WHERE props IS NOT NULL AND ts >= ? ORDER BY ts DESC LIMIT ?`
)

// consoleAnalyticsStatements is the enumeration the bounded-plan gate walks.
// It is the SAME constants the handler executes; `nArgs` is how many bind
// parameters each takes, so the gate can EXPLAIN them without a second copy
// of the argument list.
var consoleAnalyticsStatements = []struct {
	Name  string
	SQL   string
	NArgs int
}{
	{"total", qConsoleTotal, 2},
	{"uniqueUsers", qConsoleUniqueUsers, 2},
	{"eventCounts", qConsoleEventCounts, 2},
	{"recent", qConsoleRecent, 1},
	{"revenue", qConsoleRevenue, 2},
}

// consoleAnalyticsCutoff is the window's lower bound in the store's `ts`
// units (Unix milliseconds — see analyticsStoreInsert).
func consoleAnalyticsCutoff() int64 {
	return time.Now().Add(-consoleAnalyticsWindow).UnixMilli()
}

// moneyPropRe matches a stored Money prop value: a 3-5 letter uppercase ISO
// (or crypto) code, a space, then a signed decimal amount — the exact shape
// sqlMoneyToString emits. A plain string prop won't match unless it happens to
// be a bare "CODE 12.34", which is an acceptable (and vanishingly rare)
// heuristic for a dev-console revenue rollup.
var moneyPropRe = regexp.MustCompile(`^[A-Z]{3,5} -?[0-9]+(\.[0-9]+)?$`)

// HandleConsoleAnalytics implements GET /_sky/console/api/analytics.
func HandleConsoleAnalytics(w http.ResponseWriter, r *http.Request) {
	if !consoleAccessAllowed(w, r) {
		return
	}
	// Always emit non-nil slices so the JSON is `[]` not `null`
	// (keeps the Sky decoder's List path happy either way).
	out := consoleAnalyticsResponse{
		Counts:     []consoleEventCount{},
		Recent:     []consoleAnalyticsEvent{},
		Revenue:    []consoleCurrencyTotal{},
		WindowDays: consoleAnalyticsWindowDays,
		RowCap:     consoleAnalyticsRowCap,
	}
	db := analyticsStore()
	if db == nil {
		writeJSON(w, out)
		return
	}
	// Drain the buffered writer first, so the console shows the events the
	// app has just emitted rather than the ones from a quarter-second ago.
	analyticsFlushPending()

	cutoff := consoleAnalyticsCutoff()
	_ = db.QueryRow(analyticsQ(qConsoleTotal), cutoff, consoleAnalyticsRowCap).Scan(&out.Total)
	_ = db.QueryRow(analyticsQ(qConsoleUniqueUsers), cutoff, consoleAnalyticsRowCap).Scan(&out.UniqueUsers)

	if rows, err := db.Query(analyticsQ(qConsoleEventCounts), cutoff, consoleAnalyticsRowCap); err == nil {
		for rows.Next() {
			var c consoleEventCount
			if err := rows.Scan(&c.Event, &c.Count); err == nil {
				out.Counts = append(out.Counts, c)
			}
		}
		rows.Close()
	}

	out.Recent = analyticsRecentEvents(db, cutoff)
	out.Revenue, out.RowCapHit = analyticsRevenueByCurrency(db, cutoff)

	writeJSON(w, out)
}

// analyticsRecentEvents returns the most recent events (newest first, capped),
// with the `path` prop lifted out of the props JSON so the console's recent
// stream shows WHICH page a `page_view` hit (a bare "page_view" isn't
// actionable). The props JSON is parsed in Go rather than via json_extract /
// `->>` so it's identical on SQLite and Postgres.
// Ordered by `ts` rather than `id` so the window predicate and the ordering
// are served by the SAME index (idx_analytics_ts) — `ORDER BY id` forced a
// sort of the whole window on top of the seek.
func analyticsRecentEvents(db *sql.DB, cutoff int64) []consoleAnalyticsEvent {
	out := []consoleAnalyticsEvent{}
	rows, err := db.Query(analyticsQ(qConsoleRecent), cutoff)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var e consoleAnalyticsEvent
		var uid, props sql.NullString
		if err := rows.Scan(&e.TS, &e.Event, &uid, &props); err != nil {
			continue
		}
		if uid.Valid {
			e.UserID = uid.String
		}
		if props.Valid && props.String != "" {
			var m map[string]any
			if json.Unmarshal([]byte(props.String), &m) == nil {
				if p, ok := m["path"].(string); ok {
					e.Path = p
				}
			}
		}
		out = append(out, e)
	}
	return out
}

// analyticsRevenueByCurrency scans the WINDOW's events for Money-shaped prop
// values and returns the exact per-currency sum (+ occurrence count), most
// valuable currency-count first. Sums use shopspring/decimal so cents never
// drift. Currencies are summed independently — "USD 10 + EUR 5" is two rows,
// never one — because adding across currencies is meaningless.
//
// It is a rolling total for a dev console, not an audit, so it is bounded: the
// most recent `consoleAnalyticsRowCap` prop-bearing events inside
// `consoleAnalyticsWindow`. It used to be `SELECT props FROM analytics_events
// WHERE props IS NOT NULL` — no window, no cap, no usable index — plus a
// json.Unmarshal per row in Go, on every load of the tab, on a pool shared
// with the session store.
//
// The second return reports whether the cap was reached, so the caller can
// render the figure as a floor. A capped total presented as a complete one
// would be the same error as a windowed total under an all-time label.
func analyticsRevenueByCurrency(db *sql.DB, cutoff int64) ([]consoleCurrencyTotal, bool) {
	rows, err := db.Query(analyticsQ(qConsoleRevenue), cutoff, consoleAnalyticsRowCap)
	if err != nil {
		return []consoleCurrencyTotal{}, false
	}
	defer rows.Close()

	sums := map[string]decimal.Decimal{}
	counts := map[string]int64{}
	scanned := 0
	for rows.Next() {
		var props string
		if err := rows.Scan(&props); err != nil {
			continue
		}
		scanned++
		var m map[string]any
		if json.Unmarshal([]byte(props), &m) != nil {
			continue
		}
		for _, v := range m {
			s, ok := v.(string)
			if !ok || !moneyPropRe.MatchString(s) {
				continue
			}
			sp := strings.SplitN(s, " ", 2)
			if len(sp) != 2 {
				continue
			}
			amt, err := decimal.NewFromString(sp[1])
			if err != nil {
				continue
			}
			cur := sp[0]
			if prev, seen := sums[cur]; seen {
				sums[cur] = prev.Add(amt)
			} else {
				sums[cur] = amt
			}
			counts[cur]++
		}
	}

	out := make([]consoleCurrencyTotal, 0, len(sums))
	for cur, sum := range sums {
		// Format to the currency's minor units with banker's rounding —
		// the SAME currency table (lookupCurrency: USD→2, JPY→0, BTC→8, …)
		// and rounding mode Std.Money uses, so the console's totals read
		// identically to Money.format elsewhere (USD "10.00", not "10").
		minor := int32(lookupCurrency(cur).Minor)
		out = append(out, consoleCurrencyTotal{
			Currency: cur,
			Amount:   sum.StringFixedBank(minor),
			Count:    counts[cur],
		})
	}
	// Most transactions first, then currency code for a stable order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Currency < out[j].Currency
	})
	return out, scanned >= consoleAnalyticsRowCap
}
