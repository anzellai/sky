// db_pool.go — connection-pool sizing and transaction isolation for
// Std.Db.
//
// # Why this file exists
//
// Until v0.20.3 `Db_connect` clamped SQLite to a single connection and
// let every other driver fall through with a comment asserting that
// "their connection pool defaults are already sane". That assertion was
// false. Go's `database/sql` zero values are:
//
//	MaxOpenConns    = 0   → UNLIMITED
//	MaxIdleConns    = 2
//	ConnMaxLifetime = 0   → connections never expire
//	ConnMaxIdleTime = 0   → idle connections are never reaped
//
// Against PostgreSQL that is two failure modes, not one:
//
//  1. Above the knee, `MaxOpenConns = 0` opens one backend per
//     concurrent query. PostgreSQL's own `max_connections` default is
//     100 and each backend is a PROCESS costing ~5–10 MB, so a burst
//     stops being slow and starts being `FATAL: sorry, too many clients
//     already` — a hard outage for every connection after the hundredth,
//     including the operator's psql.
//  2. Below the knee, `MaxIdleConns = 2` means a pool that briefly grew
//     to 20 closes 18 of them the moment the burst ends and re-dials
//     them on the next one. A PostgreSQL connect is a fork + auth +
//     (usually) a TLS handshake; paying it per request is the churn that
//     makes a healthy database look slow.
//
// Neither is theoretical and neither is visible in a test that runs one
// query at a time, which is why the false comment survived: nothing
// contradicted it until the load did.
//
// # Deployment-aware sizing
//
// The correct pool size is not a property of the app, it is a property
// of how many copies of the app there are. On a VM the app is one
// process and can afford a pool proportional to its CPUs. On request-
// billed serverless the platform runs many small instances and EACH
// holds a pool — the same per-instance number that is conservative on a
// VM is a connection storm across fifty Cloud Run instances.
//
// The runtime already knows which world it is in: `IsServerless()` in
// serverless.go reads the platform fingerprints (`K_SERVICE`,
// `AWS_LAMBDA_FUNCTION_NAME`, …) and `exporter.go` already varies its
// flush cadence on it. This file reuses that single detector rather than
// growing a second one.
//
// # SQLite is deliberately not tunable here
//
// The `MaxOpenConns(1)` clamp on SQLite is a CORRECTNESS constraint, not
// a tuning choice: SQLite has one global writer lock, and letting
// `database/sql` open a second connection reintroduces the SQLITE_BUSY
// class that `db_connect_defaults_test.go` exists to keep closed. The
// pool env vars are therefore ignored on SQLite, with a warning, rather
// than silently honoured into a regression.
package rt

import (
	"database/sql"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// ── Pool sizing ────────────────────────────────────────────────

// dbPoolConfig is the `database/sql` pool shape Std.Db applies at
// connect. A zero `ConnMaxLifetime` / `ConnMaxIdleTime` means "no
// limit", matching `database/sql`'s own convention.
type dbPoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// applyTo pushes the config into a live pool.
//
// Order matters: `database/sql` silently reduces `MaxIdleConns` to
// `MaxOpenConns` when the former is larger, so open is set first and the
// resolver keeps them equal anyway.
func (c dbPoolConfig) applyTo(conn *sql.DB) {
	conn.SetMaxOpenConns(c.MaxOpenConns)
	conn.SetMaxIdleConns(c.MaxIdleConns)
	conn.SetConnMaxLifetime(c.ConnMaxLifetime)
	conn.SetConnMaxIdleTime(c.ConnMaxIdleTime)
}

// Pool defaults. Chosen, not inherited — see the file header for what
// the inherited ones cost.
const (
	// dbPoolLifetime bounds how long any one backend is reused. This is
	// not about leaks; it is about topology changes. A failover, a
	// pgbouncer restart or a DNS repoint leaves the pool holding
	// connections to a machine that is no longer the primary, and
	// without an expiry those connections are held until the process
	// restarts. 30 minutes is long enough to be invisible to
	// per-request latency and short enough that a failover heals itself
	// well inside a maintenance window.
	dbPoolLifetime = 30 * time.Minute

	// dbPoolIdleTimeVM reaps a connection that has sat unused. Five
	// minutes keeps a normal traffic trough warm while returning
	// backends from an app that has genuinely gone quiet.
	dbPoolIdleTimeVM = 5 * time.Minute

	// dbPoolIdleTimeServerless is deliberately much shorter. A frozen
	// serverless instance keeps its TCP connections — and therefore the
	// PostgreSQL backend processes behind them — alive while doing no
	// work at all, and the platform may keep it frozen for many minutes
	// before evicting it. Sixty seconds means a quiet instance gives
	// its backends back rather than hoarding them; the cost is a
	// re-dial on the next request after a minute of silence, which is
	// the right trade when the alternative is `too many clients`.
	dbPoolIdleTimeServerless = 60 * time.Second
)

// defaultPostgresPoolConfig returns the deployment-aware default sizing.
//
// VM / long-lived: 4 connections per CPU, floored at 4 and capped at 32.
// The multiplier reflects that a request spends most of its time waiting
// on the database rather than burning CPU, so more connections than
// cores is right; the cap keeps a big host from pointing 128 backends at
// a server whose default `max_connections` is 100.
//
// Serverless: 2 per CPU, floored at 2 and capped at 8, because the
// multiplier that matters is the instance count, which the app does not
// control and cannot see. Operators running high per-instance request
// concurrency (Cloud Run defaults to 80) may need to raise this — that
// is what the env override is for, and raising it is a decision about
// the server's `max_connections` budget, so it should be explicit.
//
// `MaxIdleConns` is set EQUAL to `MaxOpenConns` in both modes. Leaving
// idle below open is what produces connect churn: the pool grows under
// load and then immediately throws the connections away.
func defaultPostgresPoolConfig() dbPoolConfig {
	cpus := runtime.GOMAXPROCS(0)
	if IsServerless() {
		n := clampInt(cpus*2, 2, 8)
		return dbPoolConfig{
			MaxOpenConns:    n,
			MaxIdleConns:    n,
			ConnMaxLifetime: dbPoolLifetime,
			ConnMaxIdleTime: dbPoolIdleTimeServerless,
		}
	}
	n := clampInt(cpus*4, 4, 32)
	return dbPoolConfig{
		MaxOpenConns:    n,
		MaxIdleConns:    n,
		ConnMaxLifetime: dbPoolLifetime,
		ConnMaxIdleTime: dbPoolIdleTimeVM,
	}
}

// sqlitePoolConfig is the single-connection clamp. See the file header
// for why it is not tunable.
func sqlitePoolConfig() dbPoolConfig {
	return dbPoolConfig{MaxOpenConns: 1, MaxIdleConns: 1}
}

// dbAuxPoolConfig sizes the runtime's OWN PostgreSQL pools — the
// Sky.Live session store, the Std.Analytics store, the telemetry
// persistence store — as opposed to the app's `Db.connect` pool.
//
// It is a QUARTER of the app pool, floored at 2 and capped at 8,
// because a Sky.Live app on PostgreSQL opens SEVERAL pools in one
// process and they share one server's `max_connections` budget. Sizing
// each of them like the app's own pool is how a single 8-core instance
// quietly asks for 96 backends: the sum is what the server sees, not any
// one pool.
//
// A quarter is enough because these pools do small point work — a
// session read and write per request, a batched analytics insert — with
// sub-millisecond service times, so a handful of connections sustains
// far more throughput than the app's query pool needs to. Raising
// `<PREFIX>_DB_MAX_OPEN_CONNS` raises these proportionally, so one knob
// still controls the process's whole footprint.
func dbAuxPoolConfig() dbPoolConfig {
	c := resolveDbPoolConfig("pgx")
	if c.MaxOpenConns == 0 {
		// The app pool was explicitly set to unlimited. The runtime's own
		// pools do not follow it there — an unbounded session-store pool
		// is a connection storm with no upside.
		c.MaxOpenConns = defaultPostgresPoolConfig().MaxOpenConns
	}
	n := clampInt(c.MaxOpenConns/4, 2, 8)
	c.MaxOpenConns = n
	c.MaxIdleConns = n
	return c
}

// dbPoolEnvSuffixes are the pool knobs, in the Sky-prefixed namespace
// (`SKY_DB_MAX_OPEN_CONNS` by default — see env_prefix.go). Listed once
// so the SQLite ignore-warning can name exactly what it ignored.
var dbPoolEnvSuffixes = []string{
	"DB_MAX_OPEN_CONNS",
	"DB_MAX_IDLE_CONNS",
	"DB_CONN_MAX_LIFETIME",
	"DB_CONN_MAX_IDLE_TIME",
}

// resolveDbPoolConfig returns the pool config for a driver: the
// deployment-aware default, then any explicit env override on top.
func resolveDbPoolConfig(driver string) dbPoolConfig {
	if driver != "pgx" {
		// SQLite. Warn rather than silently ignore — a knob that looks
		// set and does nothing is the failure mode sky.toml's
		// unknown-key warning exists to prevent.
		for _, suffix := range dbPoolEnvSuffixes {
			if skyGetenv(suffix) != "" {
				Log_warn("db.connect: " + skyEnvName(suffix) +
					" is ignored on SQLite — SQLite has a single global writer lock, " +
					"so the pool is pinned to one connection (raising it reintroduces SQLITE_BUSY)")
			}
		}
		return sqlitePoolConfig()
	}
	c := defaultPostgresPoolConfig()
	c.MaxOpenConns = dbEnvInt("DB_MAX_OPEN_CONNS", c.MaxOpenConns)
	c.MaxIdleConns = dbEnvInt("DB_MAX_IDLE_CONNS", c.MaxIdleConns)
	c.ConnMaxLifetime = dbEnvDuration("DB_CONN_MAX_LIFETIME", c.ConnMaxLifetime)
	c.ConnMaxIdleTime = dbEnvDuration("DB_CONN_MAX_IDLE_TIME", c.ConnMaxIdleTime)
	if c.MaxOpenConns < 0 {
		c.MaxOpenConns = 0
	}
	// `MaxOpenConns == 0` is `database/sql` for "unlimited"; honour an
	// explicit request for it but say what it means, because it is the
	// default this file exists to replace.
	if c.MaxOpenConns == 0 {
		Log_warn("db.connect: " + skyEnvName("DB_MAX_OPEN_CONNS") +
			"=0 means UNLIMITED connections — a burst can exhaust the server's max_connections")
	} else if c.MaxIdleConns > c.MaxOpenConns {
		c.MaxIdleConns = c.MaxOpenConns
	}
	if c.MaxIdleConns < 0 {
		c.MaxIdleConns = 0
	}
	return c
}

// ── Transaction isolation ──────────────────────────────────────

// dbTxConfig is what `Db_withTransaction` begins with.
//
// `Opts == nil` reproduces the historical `d.conn.Begin()` exactly: the
// driver default, which is READ COMMITTED on PostgreSQL. That is the
// DEFAULT and it is unchanged deliberately. Raising the default to
// SERIALIZABLE would start surfacing `40001 serialization_failure` to
// apps that have never seen it and have no retry — a breaking change
// wearing a bug fix's clothes.
type dbTxConfig struct {
	Opts    *sql.TxOptions
	Retries int
}

// resolveDbTxConfig reads the opt-in isolation + retry settings.
//
//	<PREFIX>_DB_ISOLATION  unset (default) | read uncommitted |
//	                       read committed | repeatable read | serializable
//	<PREFIX>_DB_TX_RETRY   0 (default) — retry budget for a transaction
//	                       that fails with 40001 / 40P01
//
// # The replayability requirement — read before enabling DB_TX_RETRY
//
// Retrying a serialization failure means RUNNING THE TRANSACTION BODY
// AGAIN. A Sky `Task` body is not guaranteed to be replayable: it may
// have sent an email, charged a card, called a third-party API or
// published to a topic before the conflicting write was detected, and
// none of those are undone by `ROLLBACK`. The database's half of the
// work is atomic; the outside world's half is not.
//
// So `DB_TX_RETRY` is opt-in, defaults to 0, and is only safe when every
// effect inside the body is either a database write on the same
// transaction or genuinely idempotent. Sky does not yet have a way to
// express "this Task body is replayable" in the type system, and until
// it does the runtime cannot check this for you — enabling the knob is
// an assertion the operator makes.
func resolveDbTxConfig(driver string) dbTxConfig {
	cfg := dbTxConfig{}
	raw := strings.TrimSpace(skyGetenv("DB_ISOLATION"))
	if raw != "" {
		if driver != "pgx" {
			// SQLite serialises every transaction on the single pooled
			// connection under its global write lock, so there is no
			// weaker level to ask for and no stronger one to grant.
			Log_warn("db.connect: " + skyEnvName("DB_ISOLATION") +
				" is ignored on SQLite — its transactions already serialise on the " +
				"single pooled connection")
		} else if level, ok := parseIsolationLevel(raw); !ok {
			Log_warn("db.connect: " + skyEnvName("DB_ISOLATION") + "=" + raw +
				" is not a recognised isolation level (read uncommitted / read committed / " +
				"repeatable read / serializable) — using the driver default")
		} else {
			cfg.Opts = &sql.TxOptions{Isolation: level}
		}
	}
	if n := dbEnvInt("DB_TX_RETRY", 0); n > 0 {
		if driver != "pgx" {
			Log_warn("db.connect: " + skyEnvName("DB_TX_RETRY") +
				" is ignored on SQLite — SQLite does not raise 40001/40P01")
		} else {
			cfg.Retries = clampInt(n, 0, 10)
		}
	}
	return cfg
}

// parseIsolationLevel maps the documented spellings onto
// `sql.IsolationLevel`. Case, spaces, hyphens and underscores are all
// accepted so `REPEATABLE_READ`, `repeatable-read` and
// `"Repeatable Read"` all work.
//
// `sql.LevelSnapshot` and `sql.LevelLinearizable` are deliberately NOT
// accepted: PostgreSQL implements neither, and accepting a name the
// driver will reject at BEGIN turns a config typo into a runtime error
// on the first transaction instead of a warning at connect.
func parseIsolationLevel(s string) (sql.IsolationLevel, bool) {
	norm := strings.ToLower(strings.TrimSpace(s))
	norm = strings.NewReplacer("-", " ", "_", " ").Replace(norm)
	norm = strings.Join(strings.Fields(norm), " ")
	switch norm {
	case "default", "driver default":
		return sql.LevelDefault, true
	case "read uncommitted":
		return sql.LevelReadUncommitted, true
	case "read committed":
		return sql.LevelReadCommitted, true
	case "repeatable read":
		return sql.LevelRepeatableRead, true
	case "serializable":
		return sql.LevelSerializable, true
	}
	return sql.LevelDefault, false
}

// dbIsRetryableTxError reports whether a driver error is a PostgreSQL
// transaction conflict that a REPLAYABLE body may safely retry.
//
// Classified by SQLSTATE, never by message text — the message is
// localised and version-dependent, the code is neither:
//
//	40001 serialization_failure — SERIALIZABLE / REPEATABLE READ
//	                              detected a conflicting concurrent write
//	40P01 deadlock_detected     — PostgreSQL broke a lock cycle by
//	                              cancelling this transaction
func dbIsRetryableTxError(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}

// dbTxRetryBackoff spaces out retries. Exponential from 5 ms, capped at
// 200 ms: a serialization conflict resolves as soon as the OTHER
// transaction commits, so the useful wait is short, and a long one just
// holds the request open.
func dbTxRetryBackoff(attempt int) time.Duration {
	d := 5 * time.Millisecond << attempt
	if d > 200*time.Millisecond {
		d = 200 * time.Millisecond
	}
	return d
}

// txExecutor wraps a `*sql.Tx` so `Db_withTransaction` can see the RAW
// driver error before the data kernels convert it into a Sky `Error`.
//
// This exists because a `40001` is usually raised by a statement inside
// the transaction, not by COMMIT, and by the time it reaches
// `dbWithTransactionBody` it is a Sky `Err` whose SQLSTATE has been
// flattened into a string. Recovering the code by grepping that string
// would be exactly the message-text classification `dbIsRetryableTxError`
// refuses to do, so the flag is set at the point the typed error is
// still in hand.
//
// Only installed when a retry budget is configured; otherwise
// `executor()` hands back the bare `*sql.Tx` and this file costs
// nothing.
//
// KNOWN GAP: `QueryRow` defers its error to `Scan`, so a conflict raised
// by the one `QueryRow` call site (the `INSERT … RETURNING id` path in
// `dbInsertRowBody`) is not seen here. It is still caught at COMMIT,
// which is where PostgreSQL reports a conflict the statement did not.
type txExecutor struct {
	tx        *sql.Tx
	retryable *atomic.Bool
}

func (e txExecutor) note(err error) {
	if dbIsRetryableTxError(err) {
		e.retryable.Store(true)
	}
}

func (e txExecutor) Exec(query string, args ...any) (sql.Result, error) {
	res, err := e.tx.Exec(query, args...)
	e.note(err)
	return res, err
}

func (e txExecutor) Query(query string, args ...any) (*sql.Rows, error) {
	rows, err := e.tx.Query(query, args...)
	e.note(err)
	return rows, err
}

func (e txExecutor) QueryRow(query string, args ...any) *sql.Row {
	return e.tx.QueryRow(query, args...)
}

// ── env helpers ────────────────────────────────────────────────

// dbEnvInt reads a Sky-prefixed integer env var, falling back to `def`
// when unset or unparseable (with a warning in the latter case — a
// typo'd number must not silently become a default).
func dbEnvInt(suffix string, def int) int {
	raw := strings.TrimSpace(skyGetenv(suffix))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		Log_warn("db.connect: " + skyEnvName(suffix) + "=" + raw +
			" is not an integer — using " + strconv.Itoa(def))
		return def
	}
	return n
}

// dbEnvDuration reads a Sky-prefixed duration env var. Accepts Go
// duration syntax ("30m", "90s", "1h30m") and a bare integer read as
// SECONDS, because "300" is what an operator coming from a JDBC or
// pgbouncer config will write. "0" disables the limit.
func dbEnvDuration(suffix string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(skyGetenv(suffix))
	if raw == "" {
		return def
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second
	}
	Log_warn("db.connect: " + skyEnvName(suffix) + "=" + raw +
		" is not a duration (e.g. \"30m\", \"90s\", or a bare integer of seconds) — using " +
		def.String())
	return def
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
