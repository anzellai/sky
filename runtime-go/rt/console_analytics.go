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
		Counts:  []consoleEventCount{},
		Recent:  []consoleAnalyticsEvent{},
		Revenue: []consoleCurrencyTotal{},
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

	out.Revenue = analyticsRevenueByCurrency(db)

	writeJSON(w, out)
}

// analyticsRevenueByCurrency scans every event's props for Money-shaped
// values and returns the exact per-currency sum (+ occurrence count), most
// valuable currency-count first. Sums use shopspring/decimal so cents never
// drift. Currencies are summed independently — "USD 10 + EUR 5" is two rows,
// never one — because adding across currencies is meaningless.
func analyticsRevenueByCurrency(db *sql.DB) []consoleCurrencyTotal {
	rows, err := db.Query(`SELECT props FROM analytics_events WHERE props IS NOT NULL`)
	if err != nil {
		return []consoleCurrencyTotal{}
	}
	defer rows.Close()

	sums := map[string]decimal.Decimal{}
	counts := map[string]int64{}
	for rows.Next() {
		var props string
		if err := rows.Scan(&props); err != nil {
			continue
		}
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
	return out
}
