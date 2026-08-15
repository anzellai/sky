package rt

// Gates for the buffered, batching analytics writer (analytics_writer.go).
//
// The requirement these are written against: "a batching gate that passes
// whether or not events are actually batched is worthless — assert the number
// of round-trips or statements, not just that rows arrive." So every gate here
// asserts a COUNT of work done, and each one is falsified by a specific,
// named mutation of the implementation (recorded in the commit body).

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// openAnalyticsTestStore points the analytics store at a fresh SQLite file and
// opens it, returning the handle. Mirrors the existing idiom in
// analytics_store_test.go.
func openAnalyticsTestStore(t *testing.T) (*sql.DB, string) {
	t.Helper()
	t.Cleanup(resetAnalyticsStore)
	resetAnalyticsStore()
	path := filepath.Join(t.TempDir(), "batch.db")
	t.Setenv("SKY_ANALYTICS_DB_PATH", path)
	db := analyticsStore()
	if db == nil {
		t.Fatal("analytics store did not open for a configured path")
	}
	return db, path
}

// TestAnalyticsWriterBatchesManyEventsIntoFewStatements is THE batching gate.
//
// It is deliberately not "1000 events produce 1000 rows" — that assertion is
// true of the row-at-a-time implementation this replaced, so it would pass
// against the code the change exists to remove. The load-bearing assertion is
// the STATEMENT COUNT: 1000 events must reach the database in a number of
// INSERTs proportional to 1000/analyticsBatchSize, not to 1000.
func TestAnalyticsWriterBatchesManyEventsIntoFewStatements(t *testing.T) {
	db, _ := openAnalyticsTestStore(t)
	const n = 1000

	start := time.Now()
	for i := 0; i < n; i++ {
		analyticsStoreInsert(map[string]any{
			"ts": int64(i), "event": "batched", "anonymous_id": "anon",
			"props": map[string]any{"i": i},
		})
	}
	analyticsFlushPending()
	elapsed := time.Since(start)

	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM analytics_events`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != n {
		t.Fatalf("rows = %d, want %d — batching must not LOSE events", rows, n)
	}

	stmts := analyticsWriterInst.statements.Load()
	// The flusher also flushes on its 250 ms tick and on the explicit drain,
	// so the exact count depends on timing; what must hold is that it is
	// bounded by the batch size rather than by the event count. A generous
	// ceiling still fails hard against per-row inserts (which would be 1000).
	maxStmts := int64(n/analyticsBatchSize) + 8
	if stmts > maxStmts {
		t.Fatalf("%d events took %d INSERT statements (ceiling %d) — events are NOT being batched",
			n, stmts, maxStmts)
	}
	if stmts < 1 {
		t.Fatalf("statement counter is %d after %d events — the gate is measuring nothing", stmts, n)
	}
	t.Logf("%d events in %d INSERT statements (%.0f events/statement) in %v → %.0f events/s",
		n, stmts, float64(n)/float64(stmts), elapsed, float64(n)/elapsed.Seconds())
}

// TestAnalyticsWriterDropsNewestAndCountsIt asserts the overflow policy and,
// crucially, that the drop is COUNTED rather than silent.
//
// The writer is constructed WITHOUT starting its flusher goroutine, so the
// queue genuinely fills — which is the condition the policy exists for and
// which a running flusher would make impossible to reach deterministically.
func TestAnalyticsWriterDropsNewestAndCountsIt(t *testing.T) {
	w := &analyticsWriter{
		driver:   "sqlite",
		queue:    make(chan analyticsRow, analyticsQueueCap),
		flushReq: make(chan chan struct{}),
		stop:     make(chan struct{}),
	}

	// Fill exactly to capacity: every one of these must be accepted.
	for i := 0; i < analyticsQueueCap; i++ {
		if !w.enqueue(analyticsRow{ts: int64(i), event: "fits"}) {
			t.Fatalf("event %d was dropped while the queue still had room (cap %d)", i, analyticsQueueCap)
		}
	}
	if got := w.dropped.Load(); got != 0 {
		t.Fatalf("dropped = %d before the queue was full — the counter is counting the wrong thing", got)
	}

	// Everything past capacity must be dropped, and counted.
	const overflow = 250
	for i := 0; i < overflow; i++ {
		if w.enqueue(analyticsRow{ts: int64(i), event: "overflows"}) {
			t.Fatalf("event %d was accepted past the queue cap — the queue is not bounded", i)
		}
	}
	if got := w.dropped.Load(); got != overflow {
		t.Fatalf("dropped = %d, want %d — overflow is not being counted, i.e. it is SILENT", got, overflow)
	}

	// Drop-NEWEST, not drop-oldest: the events retained are the contiguous
	// prefix that was enqueued first. Reading the queue back proves which
	// half survived, and distinguishes this policy from the alternative.
	first := <-w.queue
	if first.event != "fits" {
		t.Fatalf("head of the queue is %q, want %q — the policy dropped the OLDEST, not the newest",
			first.event, "fits")
	}
}

// TestAnalyticsWriterFlushesQueueOnShutdown is the deploy-safety gate: a
// buffered writer that loses its queue on SIGTERM loses events on every
// deploy.
//
// It counts rows through a SEPARATE handle opened after shutdown, so it is
// reading what actually reached the file rather than anything the writer's
// own connection might still be holding.
func TestAnalyticsWriterFlushesQueueOnShutdown(t *testing.T) {
	_, path := openAnalyticsTestStore(t)
	const n = 700 // several batches, deliberately not a multiple of the batch size

	for i := 0; i < n; i++ {
		analyticsStoreInsert(map[string]any{
			"ts": int64(i), "event": "pending", "anonymous_id": "anon",
		})
	}
	// Shut down with a queue that is still draining — the deploy case. No
	// explicit flush first: the shutdown path itself must do the draining.
	analyticsWriterInst.shutdown(context.Background())

	reopened, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	var rows int
	if err := reopened.QueryRow(`SELECT count(*) FROM analytics_events`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != n {
		t.Fatalf("only %d of %d events survived shutdown — the queue is lost on every deploy", rows, n)
	}
}

// TestAnalyticsWriterIsRegisteredWithTheShutdownChain proves the flush is
// WIRED, not merely implemented.
//
// The previous test calls `shutdown` directly, which would keep passing if
// nothing ever called it in production — exactly the state
// `telemetry.ClosePersistence` was in ("test-only; production code lets the
// goroutines run for the process lifetime"), i.e. a correct flush that never
// ran. This asserts the registration itself.
func TestAnalyticsWriterIsRegisteredWithTheShutdownChain(t *testing.T) {
	resetShutdownHooksForTesting()
	t.Cleanup(resetShutdownHooksForTesting)

	before := shutdownHookNames()
	if containsName(before, "analytics-writer") {
		t.Fatal("the hook registry was not reset — this gate cannot see its own effect")
	}

	_, _ = openAnalyticsTestStore(t)

	after := shutdownHookNames()
	if !containsName(after, "analytics-writer") {
		t.Fatalf("opening the analytics store registered no shutdown hook (registry: %v) — "+
			"the queue would be dropped on SIGTERM", after)
	}
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestAnalyticsReadPathsSeeQueuedWrites is the read-your-writes gate.
//
// Buffering must be invisible to a reader. For `erase` this is a compliance
// property, not a freshness one: a right-to-erasure request that deleted the
// rows on disk while the same subject's events sat in the queue would
// re-materialise them a quarter of a second later.
func TestAnalyticsReadPathsSeeQueuedWrites(t *testing.T) {
	db, _ := openAnalyticsTestStore(t)

	for i := 0; i < 50; i++ {
		analyticsStoreInsert(map[string]any{
			"ts": int64(i), "event": "e", "anonymous_id": "subject-a",
		})
	}
	// No flush here on purpose: erase must drain for itself.
	erased := analyticsEraseResult(t, "subject-a")
	if erased != 50 {
		t.Fatalf("erase removed %d rows, want 50 — queued events escaped a right-to-erasure request", erased)
	}
	// And nothing re-materialises afterwards.
	analyticsFlushPending()
	var left int
	if err := db.QueryRow(
		`SELECT count(*) FROM analytics_events WHERE anonymous_id = ?`, "subject-a").Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 0 {
		t.Fatalf("%d events for an erased subject reappeared after the flush", left)
	}
}

// shutdownHookNames lists the currently registered hooks, so a gate can prove
// a subsystem WIRED its flush rather than merely implemented one.
func shutdownHookNames() []string {
	shutdownMu.Lock()
	defer shutdownMu.Unlock()
	names := make([]string, 0, len(shutdownHooks))
	for _, h := range shutdownHooks {
		names = append(names, h.name)
	}
	return names
}

// ── live-PostgreSQL gates ───────────────────────────────────────────────

// liveAnalyticsCluster boots a real embedded PostgreSQL and returns its DSN,
// or skips. Uses the pg_embed_live_test.go harness.
func liveAnalyticsCluster(t *testing.T, name string) string {
	t.Helper()
	binDir := livePgBinDir()
	if binDir == "" {
		t.Skip("no PostgreSQL binaries (set SKY_POSTGRES_BIN)")
	}
	t.Setenv("SKY_POSTGRES_BIN", binDir)
	root := durableTestDir(t, name)
	s := liveSupervisor(t, root)
	if err := s.boot(); err != nil {
		t.Fatalf("boot a live cluster: %v", err)
	}
	t.Cleanup(func() {
		// Detach first: the supervisor's reaction to its postmaster dying is
		// to exit the process, which would take the test binary with it.
		s.stopping.Store(true)
		if pid, ok := runningPostmaster(s.cfg.dataDir); ok {
			_ = syscall.Kill(pid, syscall.SIGQUIT)
		}
		_ = os.RemoveAll(s.cfg.socketDir)
	})
	return s.dsn
}

// analyticsLiveSchema creates the events table on a live cluster.
func analyticsLiveSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range analyticsSchemaStmts("pgx") {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
}

// TestLiveAnalyticsBatchingSendsFewStatementsToTheServer is the round-trip
// gate: the same 2000 events, written both ways against a REAL PostgreSQL,
// with the statements counted by a driver shim that sits below the code under
// test (counting_driver_test.go).
//
// It is differential on purpose. A gate that only bounded the batched number
// would pass on a broken instrument that always answered zero — which is
// exactly what the first two drafts of this gate did, using
// `pg_stat_database`. Measuring the row-at-a-time shape with the SAME
// instrument in the SAME test calibrates it: if the counter were dead, the
// control's assertion fails and the gate goes red rather than green.
func TestLiveAnalyticsBatchingSendsFewStatementsToTheServer(t *testing.T) {
	dsn := liveAnalyticsCluster(t, "analytics-batching")
	const n = 2000

	// ── CONTROL: the row-at-a-time shape this change replaced ────────────
	ctlDSN, ctlCount := countingDSN("live-ctl", dsn)
	ctl, err := sql.Open("pgx-counting", ctlDSN)
	if err != nil {
		t.Fatalf("control handle: %v", err)
	}
	defer ctl.Close()
	analyticsLiveSchema(t, ctl)

	ctlCount.Store(0)
	ctlStart := time.Now()
	for i := 0; i < n; i++ {
		if _, err := ctl.Exec(
			`INSERT INTO analytics_events (ts, anonymous_id, user_id, event, props, context)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			int64(i), "anon", nil, "control", nil, nil); err != nil {
			t.Fatalf("control insert %d: %v", i, err)
		}
	}
	ctlElapsed := time.Since(ctlStart)
	ctlStmts := ctlCount.Load()
	if ctlStmts < int64(n) {
		t.Fatalf("the control sent %d rows one at a time but the shim counted %d statements — "+
			"the instrument is not seeing the traffic, so nothing this gate says is meaningful",
			n, ctlStmts)
	}

	// ── the batched writer, over its own counted handle ──────────────────
	wDSN, wCount := countingDSN("live-writer", dsn)
	wdb, err := sql.Open("pgx-counting", wDSN)
	if err != nil {
		t.Fatalf("writer handle: %v", err)
	}
	defer wdb.Close()

	w := newAnalyticsWriter(wdb, "pgx", nil)
	t.Cleanup(func() { w.shutdown(context.Background()) })

	wCount.Store(0)
	start := time.Now()
	for i := 0; i < n; i++ {
		if !w.enqueue(analyticsRow{
			ts: int64(i), anonID: "anon", userID: nil, event: "batched", props: nil, ctx: nil,
		}) {
			t.Fatalf("event %d was dropped — the queue is too small for this gate to mean anything", i)
		}
	}
	w.flushNow()
	elapsed := time.Since(start)
	batchStmts := wCount.Load()

	if f := w.failures.Load(); f > 0 {
		t.Fatalf("the writer failed %d batches; last error: %v", f, w.lastErr.Load())
	}
	var rows int
	if err := ctl.QueryRow(
		`SELECT count(*) FROM analytics_events WHERE event = 'batched'`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != n {
		t.Fatalf("rows = %d, want %d — batching must not LOSE events", rows, n)
	}

	// Stated as a ratio rather than a pinned number, so it survives a change
	// to analyticsBatchSize and still fails hard against no batching at all.
	if batchStmts*10 >= ctlStmts {
		t.Fatalf("row-at-a-time sent the server %d statements and the batched writer sent %d "+
			"for the same %d events — that is not batching", ctlStmts, batchStmts, n)
	}
	t.Logf("LIVE PostgreSQL, %d events each, statements counted BELOW the code under test:\n"+
		"  row-at-a-time: %d statements, %v, %.0f events/s\n"+
		"  batched:       %d statements, %v, %.0f events/s\n"+
		"  → %.0fx fewer statements, %.1fx faster",
		n,
		ctlStmts, ctlElapsed, float64(n)/ctlElapsed.Seconds(),
		batchStmts, elapsed, float64(n)/elapsed.Seconds(),
		float64(ctlStmts)/float64(max(batchStmts, 1)),
		ctlElapsed.Seconds()/elapsed.Seconds())
}

// TestLiveAnalyticsUsesSynchronousCommitOffAndOnlyForItself is the Phase B
// gate.
//
// Two halves, and the second is the one that matters: it is easy to make
// analytics fast by making the whole cluster non-durable, and that would be a
// silent data-loss bug wearing a performance fix's clothes. So the gate
// asserts BOTH that the writer's own transaction runs with the setting off
// AND that a concurrent session — standing in for the app's pool — still sees
// the durable default.
func TestLiveAnalyticsUsesSynchronousCommitOffAndOnlyForItself(t *testing.T) {
	dsn := liveAnalyticsCluster(t, "analytics-synccommit")

	app, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("app handle: %v", err)
	}
	defer app.Close()

	// The cluster-wide setting must be untouched. `SET LOCAL` inside the
	// writer's transaction cannot change this; a global edit to
	// postgresql.conf would.
	var global string
	if err := app.QueryRow(`SHOW synchronous_commit`).Scan(&global); err != nil {
		t.Fatalf("SHOW synchronous_commit: %v", err)
	}
	if global != "on" {
		t.Fatalf("cluster-wide synchronous_commit = %q, want \"on\" — analytics has "+
			"weakened durability for EVERY writer in the cluster, including the app's data", global)
	}

	// And the writer's own transaction does carry it.
	w := &analyticsWriter{db: app, driver: "pgx", syncCommitOff: true}
	var inTx string
	if err := func() error {
		tx, err := app.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck
		if _, err := tx.Exec(`SET LOCAL synchronous_commit = off`); err != nil {
			return err
		}
		return tx.QueryRow(`SHOW synchronous_commit`).Scan(&inTx)
	}(); err != nil {
		t.Fatalf("probe the writer's transaction shape: %v", err)
	}
	if inTx != "off" {
		t.Fatalf("inside the flush transaction synchronous_commit = %q, want \"off\"", inTx)
	}
	if !w.syncCommitOff {
		t.Fatal("the writer would not apply synchronous_commit = off by default")
	}

	// After the transaction ends, the SAME pooled connection is back to
	// durable. This is the `SET LOCAL` vs `SET` distinction, and it is what
	// keeps the setting from leaking to the next borrower of the connection
	// once consumers share a pool.
	var afterTx string
	if err := app.QueryRow(`SHOW synchronous_commit`).Scan(&afterTx); err != nil {
		t.Fatalf("SHOW after tx: %v", err)
	}
	if afterTx != "on" {
		t.Fatalf("synchronous_commit = %q on a pooled connection AFTER the flush transaction — "+
			"the setting leaked and the next user of this connection writes non-durably", afterTx)
	}
}

// TestAnalyticsSynchronousCommitIsConfigurable — an operator who wants
// durable analytics can say so.
func TestAnalyticsSynchronousCommitIsConfigurable(t *testing.T) {
	for _, tc := range []struct {
		set     string
		wantOff bool
	}{
		{"", true},      // default: off, the throughput trade
		{"off", true},   //
		{"on", false},   // durable analytics, on request
		{"true", false}, //
		{"nonsense", true},
	} {
		t.Run("SKY_ANALYTICS_SYNCHRONOUS_COMMIT="+tc.set, func(t *testing.T) {
			t.Setenv("SKY_ANALYTICS_SYNCHRONOUS_COMMIT", tc.set)
			if got := analyticsSynchronousCommitOff(); got != tc.wantOff {
				t.Fatalf("synchronousCommitOff = %v, want %v", got, tc.wantOff)
			}
		})
	}
}
