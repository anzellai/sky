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
		h, err := dbshare.Acquire("pgx", sharedDSN, cfg, 0)
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
	a, err := dbshare.Acquire("pgx", "postgres://u:p@127.0.0.1:1/one?sslmode=disable", cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := dbshare.Acquire("pgx", "postgres://u:p@127.0.0.1:1/two?sslmode=disable", cfg, 0)
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
	c, err := dbshare.Acquire("sqlite", "postgres://u:p@127.0.0.1:1/one?sslmode=disable", cfg, 0)
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
	first, err := dbshare.Acquire("pgx", dsn, cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := dbshare.Acquire("pgx", dsn, cfg, 0)
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
	capped, err := dbshare.Acquire("pgx", dsn,
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

// TestTelemetryShareMatchesThePoolArithmetic — `telemetry.telemetryShare` is
// a second copy of `dbTelemetryShare`, forced by the import direction (rt
// imports telemetry, so telemetry cannot import rt). This is the gate that
// makes the duplication safe: if the two drift, the bulkhead arithmetic in
// db_pool.go reserves a different number of connections than telemetry
// actually takes, and the guarantee it states about the session store stops
// being true.
func TestTelemetryShareMatchesThePoolArithmetic(t *testing.T) {
	if got := telemetry.ShareForTesting(); got != dbTelemetryShare {
		t.Fatalf("telemetry's own share is %d but rt.dbTelemetryShare is %d — "+
			"the shared-pool sizing in db_pool.go reserves a different number of "+
			"connections than telemetry actually takes", got, dbTelemetryShare)
	}
	_ = fmt.Sprint()
}
