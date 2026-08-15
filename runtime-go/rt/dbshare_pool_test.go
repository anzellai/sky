package rt

// Gates for pool sharing (the dbshare package) and for the bulkhead it must
// not cost.
//
// The requirement these are written against: "for the shared pool, assert THE
// SERVER SEES ONE CONNECTION SET (e.g. via pg_stat_activity), not merely that
// the code path was taken." So the headline gate counts backends from inside
// PostgreSQL, and it counts them BOTH ways in one test — with sharing and
// without — so a miscounted query cannot make sharing look good.

import (
	"context"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sky-app/rt/dbshare"
	"sky-app/rt/telemetry"
)

// backendsFor counts the server-side backends belonging to a given
// application_name, from PostgreSQL's own live view of its processes.
//
// `pg_stat_activity` is a live view of the postmaster's process array, not the
// statistics collector's lagged counters — so unlike `pg_stat_database` it
// answers immediately and does not need settling. `application_name` is what
// makes the count attributable: it is carried in the DSN, so each phase of the
// gate can be told apart on the server.
func backendsFor(t *testing.T, obs *sql.DB, appName string) int {
	t.Helper()
	var n int
	if err := obs.QueryRow(
		`SELECT count(*) FROM pg_stat_activity
		  WHERE application_name = $1 AND pid <> pg_backend_pid()`, appName).Scan(&n); err != nil {
		t.Fatalf("pg_stat_activity: %v", err)
	}
	return n
}

// holdConnections forces `n` concurrent statements through a pool, so the
// backends actually exist while the observer looks. A pool that is merely
// configured for N connections has opened none of them — `sql.Open` does not
// dial — so counting without holding would count zero and prove nothing.
func holdConnections(t *testing.T, db *sql.DB, n int, hold time.Duration) {
	t.Helper()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var slept string
			// pg_sleep pins the connection for the duration, which is what
			// makes the backends concurrent rather than serially reused.
			_ = db.QueryRow(`SELECT pg_sleep($1)::text`, hold.Seconds()).Scan(&slept)
		}()
	}
	wg.Wait()
}

// TestLiveSameDsnConsumersShareOneConnectionSet is the Phase D gate.
//
// Three consumers on the same DSN must cost the SERVER one pool's worth of
// backends, not three. The control half — the same three consumers opening
// their own handles, as the code did before — is measured with the same query
// in the same test, so the comparison cannot pass on a broken counter.
func TestLiveSameDsnConsumersShareOneConnectionSet(t *testing.T) {
	base := liveAnalyticsCluster(t, "dbshare-pools")
	dbshare.ResetForTesting()
	t.Cleanup(dbshare.ResetForTesting)

	obs, err := sql.Open("pgx", base+"&application_name=observer")
	if err != nil {
		t.Fatalf("observer: %v", err)
	}
	defer obs.Close()

	const perConsumer = 4
	const consumers = 3
	cfg := dbshare.Config{MaxOpenConns: perConsumer, MaxIdleConns: perConsumer}

	// ── CONTROL: separate handles, as the runtime did before ─────────────
	unsharedDSN := base + "&application_name=unshared"
	var unshared []*sql.DB
	for i := 0; i < consumers; i++ {
		db, err := sql.Open("pgx", unsharedDSN)
		if err != nil {
			t.Fatalf("unshared handle %d: %v", i, err)
		}
		db.SetMaxOpenConns(perConsumer)
		db.SetMaxIdleConns(perConsumer)
		unshared = append(unshared, db)
	}
	var wg sync.WaitGroup
	for _, db := range unshared {
		wg.Add(1)
		go func(d *sql.DB) { defer wg.Done(); holdConnections(t, d, perConsumer, 900*time.Millisecond) }(db)
	}
	time.Sleep(400 * time.Millisecond)
	unsharedBackends := backendsFor(t, obs, "unshared")
	wg.Wait()
	for _, db := range unshared {
		db.Close()
	}

	if unsharedBackends < consumers*perConsumer {
		t.Fatalf("the control opened %d backends for %d separate pools of %d — expected at "+
			"least %d. The measurement is not seeing the connections, so nothing else "+
			"this gate reports means anything",
			unsharedBackends, consumers, perConsumer, consumers*perConsumer)
	}

	// ── SHARED: the same three consumers through the registry ────────────
	sharedDSN := base + "&application_name=shared"
	var handles []*dbshare.Handle
	for i := 0; i < consumers; i++ {
		h, err := dbshare.Acquire("gate-shared", "pgx", sharedDSN, cfg, 0)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		handles = append(handles, h)
	}
	if got := dbshare.PoolCount(); got != 1 {
		t.Fatalf("%d consumers on one DSN produced %d pools, want 1", consumers, got)
	}
	if !handles[1].Shared() {
		t.Error("the second consumer on the same DSN does not report a shared pool")
	}

	wg = sync.WaitGroup{}
	for _, h := range handles {
		wg.Add(1)
		go func(d *sql.DB) { defer wg.Done(); holdConnections(t, d, perConsumer, 900*time.Millisecond) }(h.DB())
	}
	time.Sleep(400 * time.Millisecond)
	sharedBackends := backendsFor(t, obs, "shared")
	wg.Wait()

	if sharedBackends > perConsumer {
		t.Fatalf("%d consumers sharing one pool of %d opened %d backends — the server is "+
			"seeing more than one connection set, so they are not sharing",
			consumers, perConsumer, sharedBackends)
	}
	if sharedBackends == 0 {
		t.Fatal("the shared pool opened no backends at all — the gate measured nothing")
	}
	t.Logf("SERVER-OBSERVED backends for %d consumers of one DSN, each sized %d:\n"+
		"  separate pools: %d backends\n"+
		"  shared pool:    %d backends",
		consumers, perConsumer, unsharedBackends, sharedBackends)

	for _, h := range handles {
		_ = h.Close()
	}
}

// TestADifferentDsnStillGetsItsOwnPool — sharing is keyed on the RESOLVED
// string, so a consumer pointed somewhere else must not be quietly folded in.
//
// This is the property that keeps the app's own `Db.connect` pool separate:
// it registers a pgx config with the simple query protocol and therefore has
// its own opaque DSN. A registry that matched loosely would put the app and
// the runtime's pools on one connection, and one of them would get the wrong
// query exec mode.
func TestADifferentDsnStillGetsItsOwnPool(t *testing.T) {
	dbshare.ResetForTesting()
	t.Cleanup(dbshare.ResetForTesting)

	cfg := dbshare.Config{MaxOpenConns: 2, MaxIdleConns: 2}
	a, err := dbshare.Acquire("gate-a", "pgx", "postgres://u:p@127.0.0.1:1/one?sslmode=disable", cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := dbshare.Acquire("gate-b", "pgx", "postgres://u:p@127.0.0.1:1/two?sslmode=disable", cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := dbshare.PoolCount(); got != 2 {
		t.Fatalf("two different DSNs produced %d pools, want 2", got)
	}
	if a.DB() == b.DB() {
		t.Fatal("two different DSNs were handed the same pool")
	}
	// The driver is part of the key too: the same string on a different
	// driver is a different pool.
	c, err := dbshare.Acquire("gate-c", "sqlite", "postgres://u:p@127.0.0.1:1/one?sslmode=disable", cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := dbshare.PoolCount(); got != 3 {
		t.Fatalf("the same DSN on a different driver produced %d pools, want 3", got)
	}
	_ = a.Close()
	_ = b.Close()
	_ = c.Close()
}

// TestClosingOneConsumerLeavesTheOthersServing is the refcount gate.
//
// Without refcounting, one subsystem shutting down closes a pool another is
// still serving requests through, and the symptom — `sql: database is closed`
// from a component nobody asked to stop — points at the victim rather than at
// the cause.
func TestClosingOneConsumerLeavesTheOthersServing(t *testing.T) {
	dsn := liveAnalyticsCluster(t, "dbshare-refcount")
	dbshare.ResetForTesting()
	t.Cleanup(dbshare.ResetForTesting)

	cfg := dbshare.Config{MaxOpenConns: 2, MaxIdleConns: 2}
	first, err := dbshare.Acquire("gate-refcount", "pgx", dsn, cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := dbshare.Acquire("gate-refcount", "pgx", dsn, cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.DB() != second.DB() {
		t.Fatal("two consumers of one DSN did not get the same pool")
	}

	if err := first.Close(); err != nil {
		t.Fatalf("closing the first consumer: %v", err)
	}
	var one int
	if err := second.DB().QueryRow(`SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("the surviving consumer cannot query after another closed: %v — "+
			"one subsystem shutting down took the pool out from under the rest", err)
	}
	if got := dbshare.PoolCount(); got != 1 {
		t.Fatalf("the pool was dropped from the registry while a consumer still held it (count %d)", got)
	}

	// Last one out closes it.
	if err := second.Close(); err != nil {
		t.Fatalf("closing the last consumer: %v", err)
	}
	if got := dbshare.PoolCount(); got != 0 {
		t.Fatalf("the pool survived its last consumer (count %d) — it is leaked", got)
	}
}

// TestAConsumerCapBoundsItsShareOfAPool is the bulkhead gate.
//
// Sharing removes the isolation four separate pools gave. The cap is what
// puts it back, and this asserts it holds under real concurrency rather than
// by inspecting the constant.
func TestAConsumerCapBoundsItsShareOfAPool(t *testing.T) {
	dsn := liveAnalyticsCluster(t, "dbshare-cap")
	dbshare.ResetForTesting()
	t.Cleanup(dbshare.ResetForTesting)

	const poolSize = 6
	const cap = 2
	capped, err := dbshare.Acquire("gate-cap", "pgx", dsn,
		dbshare.Config{MaxOpenConns: poolSize, MaxIdleConns: poolSize}, cap)
	if err != nil {
		t.Fatal(err)
	}
	defer capped.Close()

	var peak int64
	var wg sync.WaitGroup
	for i := 0; i < poolSize*3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = capped.ExecContext(context.Background(), `SELECT pg_sleep(0.05)`)
		}()
	}
	// Sample the semaphore's occupancy while the burst runs.
	done := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		for {
			select {
			case <-done:
				return
			default:
				if n := int64(capped.InFlight()); n > atomicLoad(&peak) {
					atomicStore(&peak, n)
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()
	wg.Wait()
	close(done)
	sampler.Wait()

	if peak > cap {
		t.Fatalf("a consumer capped at %d held %d slots at once — the bulkhead does not bound it",
			cap, peak)
	}
	if peak == 0 {
		t.Fatal("the cap was never occupied — the gate measured nothing")
	}
	t.Logf("a consumer capped at %d peaked at %d concurrent statements against a pool of %d",
		cap, peak, poolSize)
}

// atomicLoad / atomicStore keep the sampler goroutine and the assertion off
// each other's toes under `-race`.
func atomicLoad(p *int64) int64     { return atomic.LoadInt64(p) }
func atomicStore(p *int64, v int64) { atomic.StoreInt64(p, v) }

// TestTheSharedPoolReservesTelemetrysActualCap replaces a gate that compared
// `telemetry.telemetryShare` with a second constant `dbTelemetryShare` and
// asserted they were equal.
//
// That gate proved the copy, not the property. There is now ONE definition —
// `telemetry.Share`, in the package that hands the number to
// `dbshare.Acquire` — and the shared-pool sizing reads it. What is left to
// assert is the property the arithmetic exists for: the pool the acquire sites
// ask for is large enough that both background caps can be fully occupied and
// the session store still has what it would have had with a pool of its own.
// (The cap telemetry actually PASSES to Acquire is observed at the call site
// by TestTheConnectionDemandMatchesWhatTheConsumersAcquire, on a live cluster.)
func TestTheSharedPoolReservesTelemetrysActualCap(t *testing.T) {
	withServerlessEnv(t, nil)
	pool := dbSharedAuxPoolConfig().MaxOpenConns
	owned := dbAuxPoolConfig().MaxOpenConns
	if got := dbGuaranteedSessionShare(pool); got < owned {
		t.Fatalf("with analytics capped at %d and telemetry at %d, a shared pool of %d "+
			"guarantees the session store only %d connections — it had %d when it owned a "+
			"pool outright, so sharing is costing the request path",
			dbAnalyticsShare, telemetry.Share, pool, got, owned)
	}
	_ = fmt.Sprint()
}

// TestAPoolGrowsForALaterConsumerThatNeedsMore is the acquisition-order gate.
//
// A shared pool has ONE size, and the consumers do not arrive in a fixed order.
// `telemetry.EnablePersistenceFromEnv()` runs at observability init — BEFORE
// the session store opens — and acquires with `MaxOpenConns = 4`. If the pool
// were frozen at whatever the first consumer asked for, the session store would
// then run on 4 connections having asked for 12, silently, on the request path.
//
// Asserted through `Stats().MaxOpenConnections`, which is what the pool will
// actually honour, not through the config the registry recorded.
func TestAPoolGrowsForALaterConsumerThatNeedsMore(t *testing.T) {
	dbshare.ResetForTesting()
	t.Cleanup(dbshare.ResetForTesting)

	const dsn = "postgres://u:p@127.0.0.1:1/grow?sslmode=disable"
	small, err := dbshare.Acquire("early-small", "pgx", dsn,
		dbshare.Config{MaxOpenConns: 4, MaxIdleConns: 4}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	if got := small.DB().Stats().MaxOpenConnections; got != 4 {
		t.Fatalf("the first consumer asked for 4 and got %d", got)
	}

	large, err := dbshare.Acquire("late-large", "pgx", dsn,
		dbshare.Config{MaxOpenConns: 12, MaxIdleConns: 12}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer large.Close()
	if got := large.DB().Stats().MaxOpenConnections; got != 12 {
		t.Fatalf("a consumer that asked for 12 connections is running on a pool of %d — "+
			"the pool did not grow for it, so whichever subsystem happens to initialise "+
			"first decides the size for every later one (telemetry initialises before the "+
			"session store)", got)
	}
	// …and the pool is never SHRUNK by a later, smaller consumer: an existing
	// consumer sized its expectations against what it was given.
	tiny, err := dbshare.Acquire("later-tiny", "pgx", dsn,
		dbshare.Config{MaxOpenConns: 2, MaxIdleConns: 2}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer tiny.Close()
	if got := large.DB().Stats().MaxOpenConnections; got != 12 {
		t.Fatalf("a later consumer asking for 2 shrank the shared pool to %d, taking "+
			"connections away from a consumer already serving requests on it", got)
	}
}

// TestATransactionHoldsItsConsumerSlotUntilItEnds is the cap gate for the path
// production actually writes through.
//
// `TestAConsumerCapBoundsItsShareOfAPool` drives `ExecContext`. The analytics
// writer's default path is not Exec — it is `Begin` / `SET LOCAL` / `INSERT` /
// `Commit`, because `synchronous_commit = off` is a per-transaction setting. A
// transaction PINS its connection for its lifetime, so a cap released at BEGIN
// bounds nothing at all for the one consumer whose every flush is a
// transaction. The mutation is one line (`release()` before returning the Tx)
// and the Exec-driven gate stays green through it.
func TestATransactionHoldsItsConsumerSlotUntilItEnds(t *testing.T) {
	dsn := liveAnalyticsCluster(t, "dbshare-txcap")
	dbshare.ResetForTesting()
	t.Cleanup(dbshare.ResetForTesting)

	const poolSize = 6
	const capSlots = 2
	h, err := dbshare.Acquire("gate-txcap", "pgx", dsn,
		dbshare.Config{MaxOpenConns: poolSize, MaxIdleConns: poolSize}, capSlots)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	var open, peak int64
	var wg sync.WaitGroup
	for i := 0; i < poolSize; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := h.Begin()
			if err != nil {
				return
			}
			n := atomic.AddInt64(&open, 1)
			for {
				p := atomicLoad(&peak)
				if n <= p || atomic.CompareAndSwapInt64(&peak, p, n) {
					break
				}
			}
			time.Sleep(200 * time.Millisecond)
			atomic.AddInt64(&open, -1)
			_ = tx.Commit()
		}()
	}
	wg.Wait()

	if peak == 0 {
		t.Fatal("no transaction ever opened — the gate measured nothing")
	}
	if peak > capSlots {
		t.Fatalf("a consumer capped at %d held %d transactions open at once against a pool of "+
			"%d — a transaction pins its connection for its lifetime, so the cap bounds "+
			"nothing for a consumer that writes in transactions (which the analytics "+
			"writer does on every flush)", capSlots, peak, poolSize)
	}
	t.Logf("cap %d, pool %d: peak concurrent transactions = %d", capSlots, poolSize, peak)
}

// TestTheLastConsumerReleasesTheBackendsOnTheServer is the leak gate.
//
// `TestClosingOneConsumerLeavesTheOthersServing` asserts the registry no longer
// HOLDS the pool after the last consumer closes. That is bookkeeping, not
// release: a `Close` that deletes the map entry and forgets `db.Close()`
// satisfies it while the backends stay open on the server for the life of the
// process — every open/close cycle leaking a pool's worth of PostgreSQL
// processes. So this counts the backends from inside PostgreSQL.
func TestTheLastConsumerReleasesTheBackendsOnTheServer(t *testing.T) {
	base := liveAnalyticsCluster(t, "dbshare-leak")
	dbshare.ResetForTesting()
	t.Cleanup(dbshare.ResetForTesting)

	obs, err := sql.Open("pgx", base+"&application_name=leak-observer")
	if err != nil {
		t.Fatalf("observer: %v", err)
	}
	defer obs.Close()

	h, err := dbshare.Acquire("gate-leak", "pgx", base+"&application_name=leak-probe",
		dbshare.Config{MaxOpenConns: 3, MaxIdleConns: 3}, 0)
	if err != nil {
		t.Fatal(err)
	}
	holdConnections(t, h.DB(), 3, 200*time.Millisecond)
	if n := backendsFor(t, obs, "leak-probe"); n == 0 {
		t.Fatal("the gate opened no backends — it would prove nothing")
	}

	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The postmaster reaps an exited backend asynchronously; give it a moment.
	var left int
	for i := 0; i < 50; i++ {
		if left = backendsFor(t, obs, "leak-probe"); left == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%d backends are still open on the server after the last consumer closed its "+
		"handle — the pool was dropped from the registry but never closed, so every "+
		"open/close cycle leaks a pool's worth of PostgreSQL processes", left)
}

// TestTheAnalyticsWriterActuallyGoesThroughItsCap closes the gap between "a
// cap is configured" and "a cap is used".
//
// The first version of the shared-pool wiring acquired a capped handle and
// then wrote through `Handle.DB()`. Every existing gate stayed green: the pool
// was shared, the backends collapsed, the semaphore existed and had the right
// size — and the bulkhead did nothing at all, because no write ever took a
// slot. It was found by reading the code, which is not a mechanism. This is.
//
// Run under BOTH values of SKY_ANALYTICS_SYNCHRONOUS_COMMIT, because the writer
// has two write paths and they take the semaphore by different means: `off`
// (the default) goes through `execSyncCommitOff` → `pool.Begin()`, and `on`
// goes through `pool.Exec`. The durable branch previously had no coverage at
// all — a version of it that wrote through `w.db` instead of `w.pool` would
// have gone around the semaphore with every gate still green.
func TestTheAnalyticsWriterActuallyGoesThroughItsCap(t *testing.T) {
	for _, syncCommit := range []string{"off", "on"} {
		t.Run("SKY_ANALYTICS_SYNCHRONOUS_COMMIT="+syncCommit, func(t *testing.T) {
			dsn := liveAnalyticsCluster(t, "analytics-cap-used-"+syncCommit)
			dbshare.ResetForTesting()
			t.Cleanup(dbshare.ResetForTesting)

			t.Cleanup(resetAnalyticsStore)
			resetAnalyticsStore()
			t.Setenv("SKY_ANALYTICS_SYNCHRONOUS_COMMIT", syncCommit)
			t.Setenv("SKY_ANALYTICS_DB_PATH", dsn)
			if db := analyticsStore(); db == nil {
				t.Fatal("analytics store did not open against the live cluster")
			}
			if analyticsPool == nil {
				t.Fatal("the analytics store on PostgreSQL did not take a shared-pool handle")
			}
			if got := analyticsPool.Cap(); got != dbAnalyticsShare {
				t.Fatalf("the analytics handle is capped at %d, want %d", got, dbAnalyticsShare)
			}
			if got := analyticsWriterInst.syncCommitOff; got != (syncCommit == "off") {
				t.Fatalf("the writer's syncCommitOff is %v under %q — this subtest is not "+
					"exercising the path it names", got, syncCommit)
			}

			before := analyticsPool.Acquisitions()
			for i := 0; i < 300; i++ {
				analyticsStoreInsert(map[string]any{
					"ts": int64(i), "event": "capped", "anonymous_id": "anon",
				})
			}
			analyticsFlushPending()

			if f := analyticsWriterInst.failures.Load(); f > 0 {
				t.Fatalf("the writer failed %d batches; last error: %v",
					f, analyticsWriterInst.lastErr.Load())
			}
			if got := analyticsPool.Acquisitions() - before; got == 0 {
				t.Fatal("the analytics writer wrote 300 events without ever taking a slot from " +
					"its own cap — it is going around the semaphore, so the bulkhead is decorative")
			}
		})
	}
}

// TestTheConnectionDemandMatchesWhatTheConsumersAcquire ties the sizing
// arithmetic to the acquire sites.
//
// `dbProcessConnectionDemand` decides `max_connections` for every cluster Sky
// generates. It used to compute that from one "aux pool size" multiplied by the
// number of consumers, while the consumers acquired with something else
// entirely — the shared config (a quarter-share plus BOTH background caps) for
// two of them and a fixed 4 for telemetry. The sum was short by ten backends at
// 1 core and four at 8, so the restart-overlap claim printed into the generated
// conf was false at every core count, and no gate saw it because the gate
// re-derived the same wrong number.
//
// So this opens the real stores against a live cluster and compares the demand
// with what dbshare was actually ASKED for.
func TestTheConnectionDemandMatchesWhatTheConsumersAcquire(t *testing.T) {
	dsn := liveAnalyticsCluster(t, "demand-vs-acquire")
	withServerlessEnv(t, nil)
	dbshare.ResetForTesting()
	t.Cleanup(dbshare.ResetForTesting)
	t.Cleanup(resetAnalyticsStore)
	resetAnalyticsStore()

	// Every runtime consumer, opened the way production opens it.
	t.Setenv("SKY_ANALYTICS_DB_PATH", dsn)
	if db := analyticsStore(); db == nil {
		t.Fatal("analytics store did not open")
	}
	sess, err := openPostgresSessionPool(dsn)
	if err != nil {
		t.Fatalf("session pool: %v", err)
	}
	defer sess.Close()
	tel := telemetry.NewStore()
	t.Setenv("SKY_CONSOLE_DB_PATH", dsn)
	if err := tel.EnablePersistenceFromEnv(); err != nil {
		t.Fatalf("telemetry persistence: %v", err)
	}
	defer tel.ClosePersistence()

	asked := map[string]int{}
	for _, r := range dbshare.Requests() {
		if n, seen := asked[r.Consumer]; seen && n != r.Config.MaxOpenConns {
			t.Fatalf("%s acquired twice with different sizes (%d, %d)",
				r.Consumer, n, r.Config.MaxOpenConns)
		}
		asked[r.Consumer] = r.Config.MaxOpenConns
	}

	cpus := runtime.GOMAXPROCS(0)
	total := defaultPostgresPoolConfigFor(cpus, false).MaxOpenConns
	for _, c := range dbAuxPoolConsumers {
		got, ok := asked[c.name]
		if !ok {
			t.Fatalf("%q is counted in the connection demand but never acquired a pool "+
				"(acquired: %v) — the demand is counting a pool that does not exist",
				c.name, asked)
		}
		if want := c.maxOpen(cpus, false); got != want {
			t.Errorf("%q acquired a pool of %d but the demand arithmetic attributes %d to it "+
				"— every cluster sized from that arithmetic is wrong by %d for this consumer",
				c.name, got, want, got-want)
		}
		total += got
		delete(asked, c.name)
	}
	if len(asked) > 0 {
		t.Errorf("pools were acquired that the connection demand does not count: %v — "+
			"add them to dbAuxPoolConsumers or every cluster Sky sizes is short by their "+
			"connections", asked)
	}
	if got := dbProcessConnectionDemand(cpus, false); got != total {
		t.Errorf("the process acquired pools totalling %d connections but "+
			"dbProcessConnectionDemand reports %d — the cluster sizing is short by %d",
			total, got, total-got)
	}
}

// TestEveryDbsharePoolIsAccountedFor is the other half of the accounting: the
// live gate above can only see pools that something OPENED, so a new subsystem
// that acquires a pool no test exercises would still slip past it.
//
// `dbAuxPoolConsumers` catches a name that is REMOVED — the demand drops and
// the property gates notice. It could not catch a pool that is ADDED, because
// nothing tied the list to the call sites. This parses the runtime's own source
// and requires every `dbshare.Acquire` in non-test code to name a consumer the
// demand arithmetic counts.
//
// Parsed rather than grepped so that a call spread over several lines, or one
// whose name is spelled with a constant, is handled honestly: a non-literal
// first argument fails the gate with an explanation instead of being skipped.
func TestEveryDbsharePoolIsAccountedFor(t *testing.T) {
	counted := map[string]bool{}
	for _, name := range dbAuxPoolConsumerNames() {
		counted[name] = true
	}

	type site struct{ name, where string }
	var sites []site
	fset := token.NewFileSet()
	root := ".." // runtime-go/rt → runtime-go
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Acquire" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "dbshare" || len(call.Args) == 0 {
				return true
			}
			where := fset.Position(call.Pos()).String()
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				sites = append(sites, site{"", where + " (consumer name is not a string literal)"})
				return true
			}
			name, _ := strconv.Unquote(lit.Value)
			sites = append(sites, site{name, where})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the runtime source: %v", err)
	}
	if len(sites) == 0 {
		t.Fatal("no dbshare.Acquire call sites found in the runtime source — the gate is " +
			"looking in the wrong place and would pass whatever the code did")
	}

	seen := map[string]bool{}
	for _, s := range sites {
		seen[s.name] = true
		if !counted[s.name] {
			t.Errorf("%s acquires a shared pool as %q, which dbAuxPoolConsumers does not "+
				"count. Every cluster Sky sizes (embedded, shared, dev) is therefore short "+
				"by that pool's connections. Add it to dbAuxPoolConsumers with the sizing "+
				"function this call site passes.", s.where, s.name)
		}
	}
	var missing []string
	for name := range counted {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("dbAuxPoolConsumers counts %v, but no dbshare.Acquire call site names them — "+
			"the cluster sizing is paying for pools that are not opened", missing)
	}
	t.Logf("dbshare.Acquire call sites in the runtime: %d, all accounted for by %v",
		len(sites), dbAuxPoolConsumerNames())
}
