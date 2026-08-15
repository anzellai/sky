package rt

// Regression gates for the two v0.20.3 Std.Db defects:
//
//  1. the PostgreSQL connection pool was never configured — `Db_connect`
//     clamped SQLite and let pgx fall through on Go's database/sql
//     defaults (MaxOpenConns=0 → unlimited, MaxIdleConns=2, no lifetime)
//     under a comment claiming those defaults were "already sane";
//  2. `Db_withTransaction` called a bare `conn.Begin()`, so no isolation
//     level could be requested at all.
//
// The falsifying mutations these gates are built against:
//
//   - delete the `resolveDbPoolConfig(driver).applyTo(conn)` line in
//     Db_connect → TestDbPoolPostgresIsConfiguredAtConnect reports
//     MaxOpenConnections 0.
//   - make defaultPostgresPoolConfig ignore IsServerless() →
//     TestDbPoolServerlessSizingIsSmallerThanVM fails on both the helper
//     and the through-connect assertion.
//   - restore `d.conn.Begin()` in dbTransactionAttempt →
//     TestDbWithTransactionAppliesRequestedIsolation fails.
//   - drop the `attempt == attempts-1` guard or default Retries to
//     non-zero → TestDbWithTransactionRetriesOnlyWhenOptedIn fails.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// An unreachable DSN is deliberate: Db_connect's Ping failure is a
// warning, not an error (a transient boot race must not freeze the
// memoised handle), so the pool is fully constructed and configured
// without a live server. Port 1 refuses instantly.
const testPgDSN = "postgres://sky:sky@127.0.0.1:1/skypooltest?sslmode=disable"

// connectPgForPoolTest opens the pgx path and unregisters it afterwards
// so the memoising dbRegistry does not leak the handle into other tests.
func connectPgForPoolTest(t *testing.T, dsn string) *SkyDb {
	t.Helper()
	db := unwrapDbConnect(t, dsn)
	t.Cleanup(func() {
		if db.conn != nil {
			_ = db.conn.Close()
		}
		dbRegistryMu.Lock()
		delete(dbRegistry, dsn)
		dbRegistryMu.Unlock()
	})
	return db
}

// ── Defect 2: the pool is configured ───────────────────────────

// TestDbPoolPostgresIsConfiguredAtConnect is the headline regression.
// Pre-fix this reported MaxOpenConnections 0 — database/sql for
// "unlimited", which is how a burst becomes `FATAL: sorry, too many
// clients already` against a server whose max_connections default is
// 100.
func TestDbPoolPostgresIsConfiguredAtConnect(t *testing.T) {
	withServerlessEnv(t, nil) // VM mode

	db := connectPgForPoolTest(t, testPgDSN)
	if db.driver != "pgx" {
		t.Fatalf("driver: want pgx, got %q (DSN routing changed)", db.driver)
	}

	want := clampInt(runtime.GOMAXPROCS(0)*4, 4, 32)
	got := db.conn.Stats().MaxOpenConnections
	if got == 0 {
		t.Fatalf("MaxOpenConnections is 0 — the pool is UNLIMITED, which is the defect")
	}
	if got != want {
		t.Errorf("MaxOpenConnections: want %d (4 per CPU, clamped 4..32), got %d", want, got)
	}

	// database/sql exposes no getter for MaxIdleConns / ConnMaxLifetime /
	// ConnMaxIdleTime, so those are asserted on the resolved config. The
	// Stats assertion above is what proves applyTo actually ran on this
	// pool rather than the config merely being computed.
	cfg := resolveDbPoolConfig("pgx")
	if cfg.MaxIdleConns != cfg.MaxOpenConns {
		t.Errorf("MaxIdleConns %d != MaxOpenConns %d — idle below open is what causes connect churn",
			cfg.MaxIdleConns, cfg.MaxOpenConns)
	}
	if cfg.ConnMaxLifetime != dbPoolLifetime {
		t.Errorf("ConnMaxLifetime: want %v, got %v", dbPoolLifetime, cfg.ConnMaxLifetime)
	}
	if cfg.ConnMaxIdleTime != dbPoolIdleTimeVM {
		t.Errorf("ConnMaxIdleTime: want %v, got %v", dbPoolIdleTimeVM, cfg.ConnMaxIdleTime)
	}
}

// TestDbPoolServerlessSizingIsSmallerThanVM proves the sizing reads the
// EXISTING serverless detector (serverless.go IsServerless, the same
// signal exporter.go varies its flush cadence on) and not a second one,
// and that it reaches the real connect path.
func TestDbPoolServerlessSizingIsSmallerThanVM(t *testing.T) {
	withServerlessEnv(t, nil)
	vm := defaultPostgresPoolConfig()

	withServerlessEnv(t, map[string]string{"K_SERVICE": "sky-pool-test"})
	if !IsServerless() {
		t.Fatal("K_SERVICE set but IsServerless() is false — detector not reached")
	}
	fn := defaultPostgresPoolConfig()

	if fn.MaxOpenConns >= vm.MaxOpenConns {
		t.Errorf("serverless MaxOpenConns %d must be smaller than VM %d — many instances "+
			"each holding a pool is how a connection storm happens",
			fn.MaxOpenConns, vm.MaxOpenConns)
	}
	if fn.ConnMaxIdleTime >= vm.ConnMaxIdleTime {
		t.Errorf("serverless ConnMaxIdleTime %v must be shorter than VM %v — a frozen "+
			"instance must give its backends back", fn.ConnMaxIdleTime, vm.ConnMaxIdleTime)
	}
	if fn.MaxIdleConns != fn.MaxOpenConns {
		t.Errorf("serverless MaxIdleConns %d != MaxOpenConns %d", fn.MaxIdleConns, fn.MaxOpenConns)
	}

	// …and the same number reaches the live pool, not just the helper.
	db := connectPgForPoolTest(t, testPgDSN)
	if got := db.conn.Stats().MaxOpenConnections; got != fn.MaxOpenConns {
		t.Errorf("serverless connect: MaxOpenConnections want %d, got %d", fn.MaxOpenConns, got)
	}
}

func TestDbPoolEnvOverridesPostgres(t *testing.T) {
	withServerlessEnv(t, nil)
	t.Setenv("SKY_DB_MAX_OPEN_CONNS", "7")
	t.Setenv("SKY_DB_MAX_IDLE_CONNS", "3")
	t.Setenv("SKY_DB_CONN_MAX_LIFETIME", "12m")
	t.Setenv("SKY_DB_CONN_MAX_IDLE_TIME", "90") // bare integer → seconds

	cfg := resolveDbPoolConfig("pgx")
	if cfg.MaxOpenConns != 7 {
		t.Errorf("MaxOpenConns: want 7, got %d", cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns != 3 {
		t.Errorf("MaxIdleConns: want 3, got %d", cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime != 12*time.Minute {
		t.Errorf("ConnMaxLifetime: want 12m, got %v", cfg.ConnMaxLifetime)
	}
	if cfg.ConnMaxIdleTime != 90*time.Second {
		t.Errorf("ConnMaxIdleTime: want 90s from a bare integer, got %v", cfg.ConnMaxIdleTime)
	}

	db := connectPgForPoolTest(t, testPgDSN)
	if got := db.conn.Stats().MaxOpenConnections; got != 7 {
		t.Errorf("override did not reach the pool: MaxOpenConnections %d, want 7", got)
	}
}

// MaxIdleConns above MaxOpenConns is silently reduced by database/sql;
// the resolver clamps it up front so the effective value is the reported
// one.
func TestDbPoolIdleClampedToOpen(t *testing.T) {
	withServerlessEnv(t, nil)
	t.Setenv("SKY_DB_MAX_OPEN_CONNS", "4")
	t.Setenv("SKY_DB_MAX_IDLE_CONNS", "40")
	if cfg := resolveDbPoolConfig("pgx"); cfg.MaxIdleConns != 4 {
		t.Errorf("MaxIdleConns: want clamped to 4, got %d", cfg.MaxIdleConns)
	}
}

func TestDbPoolUnparseableEnvFallsBackToDefault(t *testing.T) {
	withServerlessEnv(t, nil)
	t.Setenv("SKY_DB_MAX_OPEN_CONNS", "lots")
	t.Setenv("SKY_DB_CONN_MAX_LIFETIME", "half an hour")
	def := defaultPostgresPoolConfig()
	cfg := resolveDbPoolConfig("pgx")
	if cfg.MaxOpenConns != def.MaxOpenConns {
		t.Errorf("MaxOpenConns: want default %d, got %d", def.MaxOpenConns, cfg.MaxOpenConns)
	}
	if cfg.ConnMaxLifetime != def.ConnMaxLifetime {
		t.Errorf("ConnMaxLifetime: want default %v, got %v", def.ConnMaxLifetime, cfg.ConnMaxLifetime)
	}
}

// SQLite's single-connection pool is a CORRECTNESS constraint (one
// global writer lock), so the pool env vars must not be able to raise it
// and reopen the SQLITE_BUSY class db_connect_defaults_test.go closed.
func TestDbPoolSqliteIgnoresPoolEnv(t *testing.T) {
	withServerlessEnv(t, nil)
	t.Setenv("SKY_DB_MAX_OPEN_CONNS", "16")
	t.Setenv("SKY_DB_MAX_IDLE_CONNS", "16")
	t.Setenv("SKY_DB_CONN_MAX_LIFETIME", "1m")

	if cfg := resolveDbPoolConfig("sqlite"); cfg.MaxOpenConns != 1 || cfg.MaxIdleConns != 1 {
		t.Fatalf("sqlite pool: want 1/1, got %d/%d", cfg.MaxOpenConns, cfg.MaxIdleConns)
	}

	dbPath := filepath.Join(t.TempDir(), "ignored-env.db")
	db := unwrapDbConnect(t, dbPath)
	t.Cleanup(func() {
		_ = db.conn.Close()
		dbRegistryMu.Lock()
		delete(dbRegistry, dbPath)
		dbRegistryMu.Unlock()
	})
	if got := db.conn.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("sqlite MaxOpenConnections: want 1 regardless of env, got %d", got)
	}
}

// A Sky.Live app on PostgreSQL opens SEVERAL pools in one process — the
// app's own `Db.connect`, the session store, the analytics store — and
// they share ONE server's max_connections budget. Sizing each like the
// app pool is how one 8-core instance quietly asks for 96 backends.
func TestDbAuxPoolIsASmallShareOfTheAppPool(t *testing.T) {
	withServerlessEnv(t, nil)
	app := resolveDbPoolConfig("pgx")
	aux := dbAuxPoolConfig()

	if aux.MaxOpenConns >= app.MaxOpenConns {
		t.Errorf("aux pool %d must be smaller than the app pool %d — they share one "+
			"server's max_connections", aux.MaxOpenConns, app.MaxOpenConns)
	}
	if aux.MaxOpenConns < 2 {
		t.Errorf("aux pool %d is below the floor of 2 — a single connection can "+
			"self-deadlock a store that reads while rows are open", aux.MaxOpenConns)
	}
	if aux.MaxIdleConns != aux.MaxOpenConns {
		t.Errorf("aux MaxIdleConns %d != MaxOpenConns %d", aux.MaxIdleConns, aux.MaxOpenConns)
	}

	// The app-pool override flows through, so one knob still bounds the
	// process's whole footprint.
	t.Setenv("SKY_DB_MAX_OPEN_CONNS", "24")
	if got := dbAuxPoolConfig().MaxOpenConns; got != 6 {
		t.Errorf("aux under an app override of 24: want 6 (a quarter), got %d", got)
	}

	// "Unlimited" is not inherited: an unbounded session-store pool is a
	// connection storm with no upside.
	t.Setenv("SKY_DB_MAX_OPEN_CONNS", "0")
	if got := dbAuxPoolConfig().MaxOpenConns; got <= 0 || got > 8 {
		t.Errorf("aux under an unlimited app pool: want a bounded 2..8, got %d", got)
	}
}

// ── Defect 1: isolation is requestable ─────────────────────────

func TestParseIsolationLevel(t *testing.T) {
	ok := map[string]sql.IsolationLevel{
		"serializable":       sql.LevelSerializable,
		"SERIALIZABLE":       sql.LevelSerializable,
		"repeatable read":    sql.LevelRepeatableRead,
		"repeatable-read":    sql.LevelRepeatableRead,
		"REPEATABLE_READ":    sql.LevelRepeatableRead,
		"  Read  Committed ": sql.LevelReadCommitted,
		"read uncommitted":   sql.LevelReadUncommitted,
		"default":            sql.LevelDefault,
	}
	for in, want := range ok {
		got, valid := parseIsolationLevel(in)
		if !valid || got != want {
			t.Errorf("parseIsolationLevel(%q) = (%v, %v); want (%v, true)", in, got, valid, want)
		}
	}
	// Rejected on purpose: PostgreSQL implements neither, so accepting
	// the name would turn a config typo into a runtime BEGIN failure.
	for _, bad := range []string{"snapshot", "linearizable", "serialisable", "strict"} {
		if _, valid := parseIsolationLevel(bad); valid {
			t.Errorf("parseIsolationLevel(%q) accepted; want rejected", bad)
		}
	}
}

// The default MUST stay the driver default. Raising it to SERIALIZABLE
// silently would start surfacing 40001 to apps with no retry path.
func TestResolveDbTxConfigDefaultIsUnchanged(t *testing.T) {
	withServerlessEnv(t, nil)
	cfg := resolveDbTxConfig("pgx")
	if cfg.Opts != nil {
		t.Errorf("default TxOptions: want nil (driver default), got %+v", cfg.Opts)
	}
	if cfg.Retries != 0 {
		t.Errorf("default Retries: want 0 (retry requires a replayable body), got %d", cfg.Retries)
	}
}

func TestResolveDbTxConfigHonoursIsolationEnv(t *testing.T) {
	withServerlessEnv(t, nil)
	t.Setenv("SKY_DB_ISOLATION", "serializable")
	t.Setenv("SKY_DB_TX_RETRY", "3")
	cfg := resolveDbTxConfig("pgx")
	if cfg.Opts == nil || cfg.Opts.Isolation != sql.LevelSerializable {
		t.Fatalf("TxOptions: want SERIALIZABLE, got %+v", cfg.Opts)
	}
	if cfg.Retries != 3 {
		t.Errorf("Retries: want 3, got %d", cfg.Retries)
	}

	// An unrecognised level warns and keeps the driver default rather
	// than failing every transaction at BEGIN.
	t.Setenv("SKY_DB_ISOLATION", "snapshot")
	if got := resolveDbTxConfig("pgx"); got.Opts != nil {
		t.Errorf("unrecognised level: want nil TxOptions, got %+v", got.Opts)
	}
}

// SQLite serialises on the single pooled connection, so neither knob has
// anything to do there — they are ignored, not honoured into surprise.
func TestResolveDbTxConfigIgnoresBothOnSqlite(t *testing.T) {
	withServerlessEnv(t, nil)
	t.Setenv("SKY_DB_ISOLATION", "serializable")
	t.Setenv("SKY_DB_TX_RETRY", "5")
	cfg := resolveDbTxConfig("sqlite")
	if cfg.Opts != nil {
		t.Errorf("sqlite TxOptions: want nil, got %+v", cfg.Opts)
	}
	if cfg.Retries != 0 {
		t.Errorf("sqlite Retries: want 0, got %d", cfg.Retries)
	}
}

// TestDbWithTransactionAppliesRequestedIsolation proves the TxOptions
// actually reach BeginTx, by recording what the driver was asked for.
//
// A behavioural probe against SQLite is NOT sufficient here: modernc's
// driver accepts every isolation level it is handed and provides its own
// regardless, so a bare `conn.Begin()` and a `BeginTx(…, SERIALIZABLE)`
// are indistinguishable from the outside. That is exactly the shape of
// vacuous gate this repository has been bitten by, so the assertion is
// made where it cannot be vacuous: at the driver boundary.
func TestDbWithTransactionAppliesRequestedIsolation(t *testing.T) {
	drv := &recordingTxDriver{}
	conn := sql.OpenDB(drv)
	defer conn.Close()

	body := func(any) any { return func() any { return Ok[any, any](int64(1)) } }

	// Baseline: nil options → LevelDefault, i.e. exactly what the
	// historical `d.conn.Begin()` produced. The default is unchanged.
	plain := &SkyDb{conn: conn, name: "iso", driver: "pgx"}
	if res := AnyTaskRun(Db_withTransaction(plain, body)); !isOkResult(res) {
		t.Fatalf("nil TxOptions path must commit; got %v", res)
	}
	got := drv.recorded()
	if len(got) != 1 {
		t.Fatalf("want 1 BeginTx, got %d", len(got))
	}
	if got[0].Isolation != driver.IsolationLevel(sql.LevelDefault) {
		t.Errorf("default isolation: want LevelDefault, got %v — the DEFAULT MUST NOT CHANGE",
			sql.IsolationLevel(got[0].Isolation))
	}

	// Opted in: the level requested is the level the driver is asked for.
	strict := &SkyDb{
		conn: conn, name: "iso", driver: "pgx",
		txCfg: dbTxConfig{Opts: &sql.TxOptions{Isolation: sql.LevelSerializable}},
	}
	if res := AnyTaskRun(Db_withTransaction(strict, body)); !isOkResult(res) {
		t.Fatalf("serializable path must commit; got %v", res)
	}
	got = drv.recorded()
	if len(got) != 2 {
		t.Fatalf("want 2 BeginTx calls, got %d", len(got))
	}
	if got[1].Isolation != driver.IsolationLevel(sql.LevelSerializable) {
		t.Errorf("requested isolation: want SERIALIZABLE, got %v — TxOptions are not "+
			"reaching BeginTx", sql.IsolationLevel(got[1].Isolation))
	}
}

// ── Retry classification + the opt-in loop ─────────────────────

func TestDbIsRetryableTxError(t *testing.T) {
	if dbIsRetryableTxError(nil) {
		t.Error("nil is not retryable")
	}
	if dbIsRetryableTxError(sql.ErrNoRows) {
		t.Error("a non-pg error must not be classified as retryable")
	}
	for _, code := range []string{"40001", "40P01"} {
		if !dbIsRetryableTxError(newPgError(code)) {
			t.Errorf("SQLSTATE %s must be retryable", code)
		}
	}
	// Classification is by SQLSTATE only — never by message text, which
	// is localised and version-dependent.
	for _, code := range []string{"23505", "42P01", "57014"} {
		if dbIsRetryableTxError(newPgError(code)) {
			t.Errorf("SQLSTATE %s must NOT be retryable", code)
		}
	}

	// …and the message text must not be consulted even when it CONTAINS the
	// codes, which is the failure a `strings.Contains(err.Error(), "40001")`
	// fallback produces. `sql.ErrNoRows` above cannot catch it: its message
	// mentions neither code, so a text-matching implementation passes that
	// case and every other negative here.
	//
	// The consequence is not a spurious retry. With SKY_DB_TX_RETRY set, the
	// retry loop REPLAYS the whole Task body — which, per this function's own
	// docstring, may already have sent the mail and charged the card. An order
	// reference, an invoice number or an account id that happens to read
	// `40001` is enough, and a unique-constraint violation on it is a
	// permanent error that would then be replayed ten times.
	for _, msg := range []string{
		`pq: duplicate key value violates unique constraint "orders_ref_key" ` +
			`DETAIL: Key (ref)=(40001) already exists.`,
		"app: refusing to import row 40001",
		"http 500: upstream reported deadlock detected in the billing queue",
		"serialization_failure while rendering the report",
	} {
		if dbIsRetryableTxError(errors.New(msg)) {
			t.Errorf("classified by MESSAGE TEXT, not SQLSTATE — a replayable-body "+
				"retry would re-run a body that has already charged a card:\n  %s", msg)
		}
	}
	if dbIsRetryableTxError(errWrap{newPgError("40001")}) == false {
		t.Error("a wrapped PgError must still be classified (errors.As, not a type assert)")
	}
}

// The retry loop must be inert unless a budget was opted into, because a
// Sky Task body is not guaranteed replayable — it may already have sent
// mail or charged a card before the conflict was detected.
func TestDbWithTransactionRetriesOnlyWhenOptedIn(t *testing.T) {
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "retry.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	// A body that reports a retryable conflict on every attempt, counting
	// how many times it ran. It flags the tx-scoped handle exactly as the
	// wrapped executor does when a statement returns 40001.
	var calls atomic.Int32
	conflicting := func(txdb any) any {
		return func() any {
			calls.Add(1)
			if h, ok := txdb.(*SkyDb); ok && h.txRetryFlag != nil {
				h.txRetryFlag.Store(true)
			}
			return Err[any, any](ErrFfi("serialization failure"))
		}
	}

	// Retries = 0 (the default): one attempt, no replay.
	calls.Store(0)
	noRetry := &SkyDb{conn: conn, name: "retry", driver: "sqlite"}
	if res := AnyTaskRun(Db_withTransaction(noRetry, conflicting)); isOkResult(res) {
		t.Fatal("conflicting body must return Err")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("default budget: body ran %d times, want exactly 1", n)
	}

	// Retries = 2: three attempts total, then the Err is returned.
	calls.Store(0)
	withRetry := &SkyDb{conn: conn, name: "retry", driver: "sqlite",
		txCfg: dbTxConfig{Retries: 2}}
	if res := AnyTaskRun(Db_withTransaction(withRetry, conflicting)); isOkResult(res) {
		t.Fatal("exhausted retries must surface the Err, not a fake Ok")
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("budget 2: body ran %d times, want 3 (1 + 2 retries)", n)
	}

	// A body that conflicts once then succeeds must commit on attempt 2.
	calls.Store(0)
	flaky := func(txdb any) any {
		return func() any {
			if calls.Add(1) == 1 {
				if h, ok := txdb.(*SkyDb); ok && h.txRetryFlag != nil {
					h.txRetryFlag.Store(true)
				}
				return Err[any, any](ErrFfi("serialization failure"))
			}
			return Ok[any, any](int64(1))
		}
	}
	if res := AnyTaskRun(Db_withTransaction(withRetry, flaky)); !isOkResult(res) {
		t.Fatalf("retry must reach the succeeding attempt; got %v", res)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("flaky body ran %d times, want 2", n)
	}
}

// A panic is a defect in the body, not a conflict; replaying it just
// panics again, so it must never be retried even under a budget.
func TestDbWithTransactionNeverRetriesAPanic(t *testing.T) {
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "panic.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()

	var calls atomic.Int32
	boom := func(txdb any) any {
		return func() any {
			calls.Add(1)
			if h, ok := txdb.(*SkyDb); ok && h.txRetryFlag != nil {
				h.txRetryFlag.Store(true)
			}
			panic("body defect")
		}
	}
	db := &SkyDb{conn: conn, name: "panic", driver: "sqlite", txCfg: dbTxConfig{Retries: 3}}
	if res := AnyTaskRun(Db_withTransaction(db, boom)); isOkResult(res) {
		t.Fatal("a panicking body must return Err")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("panicking body ran %d times, want exactly 1", n)
	}
}

func TestDbTxRetryBackoffIsBoundedAndIncreasing(t *testing.T) {
	prev := time.Duration(0)
	for i := 0; i < 10; i++ {
		d := dbTxRetryBackoff(i)
		if d < prev {
			t.Errorf("attempt %d backoff %v decreased from %v", i, d, prev)
		}
		if d > 200*time.Millisecond {
			t.Errorf("attempt %d backoff %v exceeds the 200ms cap", i, d)
		}
		prev = d
	}
}

// ── helpers ────────────────────────────────────────────────────

func isOkResult(v any) bool {
	sr, ok := v.(SkyResult[any, any])
	return ok && sr.Tag == 0
}

// recordingTxDriver is a no-op database/sql driver whose only job is to
// record the driver.TxOptions each BeginTx was called with. It supports
// transactions and nothing else — the bodies under test run no
// statements — so a Prepare would be a bug in the test, not a silent
// pass.
type recordingTxDriver struct {
	mu   sync.Mutex
	opts []driver.TxOptions
}

func (d *recordingTxDriver) recorded() []driver.TxOptions {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]driver.TxOptions(nil), d.opts...)
}

func (d *recordingTxDriver) Connect(context.Context) (driver.Conn, error) {
	return &recordingConn{d: d}, nil
}
func (d *recordingTxDriver) Driver() driver.Driver                          { return d }
func (d *recordingTxDriver) Open(string) (driver.Conn, error)               { return &recordingConn{d: d}, nil }
func (d *recordingTxDriver) OpenConnector(string) (driver.Connector, error) { return d, nil }

type recordingConn struct{ d *recordingTxDriver }

func (c *recordingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recordingTxDriver: statements are not supported")
}
func (c *recordingConn) Close() error              { return nil }
func (c *recordingConn) Begin() (driver.Tx, error) { return recordingTx{}, nil }
func (c *recordingConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.d.mu.Lock()
	c.d.opts = append(c.d.opts, opts)
	c.d.mu.Unlock()
	return recordingTx{}, nil
}

type recordingTx struct{}

func (recordingTx) Commit() error   { return nil }
func (recordingTx) Rollback() error { return nil }

func newPgError(code string) error {
	return &pgconn.PgError{Code: code, Message: "synthetic " + code, Severity: "ERROR"}
}

type errWrap struct{ inner error }

func (e errWrap) Error() string { return "wrapped: " + e.inner.Error() }
func (e errWrap) Unwrap() error { return e.inner }
