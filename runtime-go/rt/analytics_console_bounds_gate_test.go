// analytics_console_bounds_gate_test.go — the two gates behind the console's
// unbounded-read defect found by adversarial review on 2026-08-17.
//
//	console-analytics-queries-are-bounded  TestConsoleAnalyticsQueriesAreBounded
//	erasure-path-uses-an-index             TestErasurePathUsesAnIndex
//
// (The retention pruner is the other half of the same review; its gates are in
// analytics_retention_gate_test.go, which also declares `reportAssertions`.)
//
// Fixture isolation: every store goes in `t.TempDir()`, which is per-process
// and per-test, so several agents' worktrees can run these at once.
package rt

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ── plan helpers (SQLite) ──────────────────────────────────────────────────

// sqliteFullScan matches a plan step that reads the whole table with no index
// at all. `SEARCH … USING INDEX` and `SCAN … USING [COVERING] INDEX` both
// carry an index and do not match; a bare `SCAN analytics_events` does.
var sqliteFullScan = regexp.MustCompile(`SCAN\s+(TABLE\s+)?analytics_events(?:\s+AS\s+\w+)?\s*$`)

// explainPlan returns SQLite's query plan for `q` as one string per step.
func explainPlan(t *testing.T, db *sql.DB, q string, args ...any) []string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN failed for %s: %v", q, err)
	}
	defer rows.Close()
	var steps []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		steps = append(steps, strings.TrimSpace(detail))
	}
	if len(steps) == 0 {
		t.Fatalf("EXPLAIN QUERY PLAN returned no rows for %s — the plan assertion would be vacuous", q)
	}
	return steps
}

// placeholderArgs builds `n` bind values for a windowed statement: the cutoff
// first, then the row cap. Matches the order every consoleAnalyticsStatements
// entry declares.
func placeholderArgs(n int, cutoff int64) []any {
	switch n {
	case 1:
		return []any{cutoff}
	case 2:
		return []any{cutoff, consoleAnalyticsRowCap}
	}
	return nil
}

// seedAnalytics bulk-inserts `n` rows spread over the last 45 days — half
// inside the console's 30-day window, half outside it, so a query that ignores
// the window is measurably doing more work.
func seedAnalytics(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	st, err := tx.Prepare(`INSERT INTO analytics_events (ts, anonymous_id, user_id, event, props, context)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	now := time.Now().UnixMilli()
	span := int64(45 * 24 * time.Hour / time.Millisecond)
	for i := 0; i < n; i++ {
		ts := now - (int64(i)*span)/int64(n)
		_, err := st.Exec(
			ts,
			fmt.Sprintf("anon_%d", i),
			fmt.Sprintf("user_%d", i%1000),
			[]string{"page_view", "signup", "purchase"}[i%3],
			fmt.Sprintf(`{"path":"/p/%d","total":"USD %d.%02d"}`, i%50, i%100, i%100),
			`{"ip":"1.2.3.0"}`,
		)
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	_ = st.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// The planner picks a full scan over an index on a table it believes is
	// tiny, so the fixture is only a fixture once the stats are real.
	if _, err := db.Exec(`ANALYZE`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}
}

// openSeededStore opens a SQLite analytics store with the production schema
// and `rows` seeded events.
func openSeededStore(t *testing.T, rows int) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), fmt.Sprintf("bounded-%d.db", os.Getpid()))
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, p := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=5000`} {
		if _, err := db.Exec(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	for _, stmt := range analyticsSchemaStmts("sqlite") {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	seedAnalytics(t, db, rows)
	return db
}

// consoleAnalyticsSeedRows is the fixture size for the bounded-plan gate —
// 200k, the size the design suggested. Measured on the dev host (modernc
// pure-Go SQLite): the whole test runs in ~2 s, of which the seed is the bulk,
// so there was no reason to trade the design's number down. 200k is 10x the
// row cap, which is what the assertions need: every bounded statement must
// stop at consoleAnalyticsRowCap while the table holds far more than that.
const consoleAnalyticsSeedRows = 200000

// consoleAnalyticsBudget is the wall-clock ceiling for one render of the tab
// against consoleAnalyticsSeedRows. Measured on the dev host at ~54 ms bounded
// over 120k rows; 3 s is a ceiling for a loaded CI runner, not a target. The
// PLAN assertions are what pin the shape — this bound catches a regression
// that keeps an index but walks the whole table through it.
const consoleAnalyticsBudget = 3 * time.Second

// TestConsoleAnalyticsQueriesAreBounded — every statement the console's
// Analytics tab runs is bounded by a window AND a row cap, plans without a
// full table scan, and the whole tab renders inside its budget.
//
// The defect: `SELECT props FROM analytics_events WHERE props IS NOT NULL` —
// no LIMIT, no time filter, no usable index — plus a json.Unmarshal per row in
// Go, on EVERY load of the tab. `count(*)` and `count(DISTINCT user_id)` were
// unbounded scans too. All of it ran on a connection from the pool SHARED with
// the session store, and analytics_store.go:161-165 deliberately leaves the
// read paths outside the write semaphore — so the scan competed directly with
// session reads on the request path. The observability surface degraded the
// thing it observes.
func TestConsoleAnalyticsQueriesAreBounded(t *testing.T) {
	db := openSeededStore(t, consoleAnalyticsSeedRows)
	cutoff := consoleAnalyticsCutoff()
	n := 0

	// Anti-vacuity: the enumeration the gate walks must actually cover the
	// handler's call sites. A statement added to console_analytics.go without
	// being added here would otherwise be unmeasured, forever.
	n++
	assertStatementListCoversTheHandler(t)

	for _, s := range consoleAnalyticsStatements {
		args := placeholderArgs(s.NArgs, cutoff)
		if args == nil {
			t.Fatalf("statement %q declares %d args — placeholderArgs has no shape for it", s.Name, s.NArgs)
		}

		// (a) the SQL text carries both bounds.
		n++
		if !strings.Contains(s.SQL, "LIMIT") {
			t.Errorf("statement %q has no LIMIT: it walks however many rows the window holds.\nSQL: %s", s.Name, s.SQL)
		}
		n++
		if !strings.Contains(s.SQL, "ts >= ?") {
			t.Errorf("statement %q has no time window: it reads the whole of history on every "+
				"load of the tab.\nSQL: %s", s.Name, s.SQL)
		}

		// (b) the planner agrees — a LIMIT on a full scan is still a full scan.
		n++
		steps := explainPlan(t, db, s.SQL, args...)
		for _, step := range steps {
			if sqliteFullScan.MatchString(step) {
				t.Errorf("statement %q plans a FULL TABLE SCAN of analytics_events:\n  %s\n"+
					"On a shared pool this competes with session reads on the request path.\nSQL: %s",
					s.Name, strings.Join(steps, "\n  "), s.SQL)
				break
			}
		}
	}

	// (c) the row cap actually binds: the revenue rollup must stop at the cap
	// even though the table holds ~6x that.
	n++
	rev, capped := analyticsRevenueByCurrency(db, cutoff)
	if !capped {
		t.Errorf("the revenue rollup did not report hitting its %d-row cap against a %d-row "+
			"table — the cap is not binding, so a large store is walked in full",
			consoleAnalyticsRowCap, consoleAnalyticsSeedRows)
	}
	n++
	if len(rev) == 0 {
		t.Error("the revenue rollup returned nothing from a table seeded with Money props — " +
			"the bound has removed the data it was supposed to bound")
	}

	// (d) the aggregates are capped, not merely windowed.
	n++
	var total int64
	if err := db.QueryRow(qConsoleTotal, cutoff, consoleAnalyticsRowCap).Scan(&total); err != nil {
		t.Fatalf("total: %v", err)
	}
	if total > int64(consoleAnalyticsRowCap) {
		t.Errorf("the total counted %d rows against a cap of %d", total, consoleAnalyticsRowCap)
	}

	// (e) the whole tab renders inside its budget.
	n++
	start := time.Now()
	renderConsoleAnalytics(t, db, cutoff)
	elapsed := time.Since(start)
	t.Logf("console analytics render over %d rows: %v (budget %v)",
		consoleAnalyticsSeedRows, elapsed, consoleAnalyticsBudget)
	if elapsed > consoleAnalyticsBudget {
		t.Errorf("the Analytics tab took %v over %d rows, budget %v — it is doing work "+
			"proportional to the whole store on a pool shared with the session store",
			elapsed, consoleAnalyticsSeedRows, consoleAnalyticsBudget)
	}

	reportAssertions(t, n)
}

// renderConsoleAnalytics runs every statement the handler runs, in the order
// the handler runs them, against `db`. It does NOT go through
// HandleConsoleAnalytics because that reads the process-wide store singleton;
// the point here is to time the queries against a controlled fixture.
func renderConsoleAnalytics(t *testing.T, db *sql.DB, cutoff int64) {
	t.Helper()
	var total, unique int64
	_ = db.QueryRow(qConsoleTotal, cutoff, consoleAnalyticsRowCap).Scan(&total)
	_ = db.QueryRow(qConsoleUniqueUsers, cutoff, consoleAnalyticsRowCap).Scan(&unique)
	if rows, err := db.Query(qConsoleEventCounts, cutoff, consoleAnalyticsRowCap); err == nil {
		for rows.Next() {
			var ev string
			var c int64
			_ = rows.Scan(&ev, &c)
		}
		rows.Close()
	}
	_ = analyticsRecentEvents(db, cutoff)
	_, _ = analyticsRevenueByCurrency(db, cutoff)
}

// consoleQueryCallSite matches a `db.Query(` / `db.QueryRow(` whose first
// argument is one of the named statement constants.
var consoleQueryCallSite = regexp.MustCompile(`db\.(?:Query|QueryRow)\(\s*(?:analyticsQ\(\s*)?(\w+)`)

// assertStatementListCoversTheHandler proves consoleAnalyticsStatements is the
// handler's real query set, by reading console_analytics.go: every SQL call
// site must name a constant that appears in the list, and every list entry
// must be called. Without this the gate could pass while the handler ran an
// unbounded sixth query.
func assertStatementListCoversTheHandler(t *testing.T) {
	t.Helper()
	src, err := os.ReadFile("console_analytics.go")
	if err != nil {
		t.Fatalf("cannot read console_analytics.go: %v", err)
	}
	declared := map[string]bool{}
	for _, s := range consoleAnalyticsStatements {
		declared[s.Name] = true
	}
	// Map constant name -> statement name via the SQL text, so the two
	// spellings cannot drift apart silently.
	constOf := map[string]string{
		"qConsoleTotal":       "total",
		"qConsoleUniqueUsers": "uniqueUsers",
		"qConsoleEventCounts": "eventCounts",
		"qConsoleRecent":      "recent",
		"qConsoleRevenue":     "revenue",
	}
	called := map[string]bool{}
	for _, m := range consoleQueryCallSite.FindAllStringSubmatch(string(src), -1) {
		name, known := constOf[m[1]]
		if !known {
			t.Errorf("console_analytics.go runs `db.Query…(%s)` — a statement outside "+
				"consoleAnalyticsStatements, so no gate has ever checked that it is bounded. "+
				"Add it to the list (and to constOf here) in the same commit.", m[1])
			continue
		}
		called[name] = true
	}
	for name := range declared {
		if !called[name] {
			t.Errorf("consoleAnalyticsStatements declares %q but console_analytics.go never "+
				"runs it — the list has drifted from the handler", name)
		}
	}
	if len(called) == 0 {
		t.Fatal("found no console analytics call sites at all — the matcher is broken and " +
			"every plan assertion below it would be vacuous")
	}
}

// ── gate 4: erasure-path-uses-an-index ─────────────────────────────────────

// TestErasurePathUsesAnIndex — `Analytics.erase` resolves through indexes on
// both subject columns, not a full table scan.
//
// This one matters beyond performance. `Analytics.erase` is the
// right-to-erasure path: a GDPR/CCPA deletion request. Against a store with
// real history it was `DELETE … WHERE anonymous_id = ? OR user_id = ?` with an
// index on NEITHER column — a full scan holding the write lock. On a busy
// store that is slow enough to time out, and a timed-out erasure is an erasure
// that did not happen.
func TestErasurePathUsesAnIndex(t *testing.T) {
	db := openSeededStore(t, 20000)
	n := 0

	n++
	steps := explainPlan(t, db, qAnalyticsErase, "anon_7", "user_7")
	plan := strings.Join(steps, "\n  ")
	for _, step := range steps {
		if sqliteFullScan.MatchString(step) {
			t.Errorf("the right-to-erasure DELETE plans a FULL TABLE SCAN:\n  %s\n"+
				"Both `anonymous_id` and `user_id` must be indexed (analyticsSchemaStmts).", plan)
			break
		}
	}

	n++
	if !strings.Contains(plan, "idx_analytics_anonymous_id") {
		t.Errorf("the erasure plan never touches idx_analytics_anonymous_id:\n  %s", plan)
	}
	n++
	if !strings.Contains(plan, "idx_analytics_user_id") {
		t.Errorf("the erasure plan never touches idx_analytics_user_id:\n  %s", plan)
	}

	// The indexes must exist in the SHIPPED schema, not just in this fixture.
	n++
	schema := strings.Join(analyticsSchemaStmts("sqlite"), "\n")
	for _, idx := range []string{"idx_analytics_anonymous_id", "idx_analytics_user_id"} {
		if !strings.Contains(schema, idx) {
			t.Errorf("analyticsSchemaStmts does not create %s — an existing store would never "+
				"gain it, because that function is the only migration analytics has", idx)
		}
	}

	// And the erasure must still erase.
	n++
	res, err := db.Exec(qAnalyticsErase, "anon_7", "user_7")
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		t.Error("the indexed erasure deleted nothing — an index that changes the RESULT is a bug, not an optimisation")
	}

	reportAssertions(t, n)
}
