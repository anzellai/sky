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

	"sky-app/rt/telemetry"
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
	return defaultPostgresPoolConfigFor(runtime.GOMAXPROCS(0), IsServerless())
}

// defaultPostgresPoolConfigFor is defaultPostgresPoolConfig with the machine
// passed in.
//
// The seam exists so the SERVER's `max_connections` can be sized from the
// same arithmetic that sizes the pools, for any core count, without a second
// copy of the numbers. A comment asserting that "4×CPU+20 keeps the app's
// ceiling below the server's" was how the two came to disagree: it reasoned
// about the app's pool while the process opened four. See
// dbProcessConnectionDemand.
func defaultPostgresPoolConfigFor(cpus int, serverless bool) dbPoolConfig {
	if cpus < 1 {
		cpus = 1
	}
	if serverless {
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

// dbAuxPoolConsumer is one PostgreSQL pool the RUNTIME opens for its own
// purposes, as distinct from the app's `Db.connect`.
//
// `maxOpen` is not a restatement of how big that pool is — it is the SAME
// function the consumer's `dbshare.Acquire` call site passes. That matters more
// than it looks. The demand arithmetic used to multiply one per-pool size by
// the number of consumers, while the two large consumers actually acquired with
// `dbSharedAuxPoolConfig()` (a quarter-share PLUS both background caps) and
// telemetry acquired with its own fixed four. The sum was short by up to ten
// backends at every core count, and the shortfall was invisible because the
// gate compared the arithmetic with a second copy of the arithmetic.
type dbAuxPoolConsumer struct {
	// name matches the string passed to dbshare.Acquire at the call site.
	// TestEveryDbsharePoolIsAccountedFor ties the two together by parsing the
	// runtime's source, so a pool added without a line here fails the build's
	// gates rather than silently under-sizing every cluster.
	name string
	// maxOpen is the MaxOpenConns this consumer hands to dbshare.Acquire when
	// the APP's pool ceiling is `app`.
	//
	// The parameter is the app pool, not `cpus`, and that is the second frame
	// error this type has been fixed for. Every runtime pool is a share of the
	// app's, and the app's is `defaults + env` — so a per-consumer size stated
	// as `f(cpus)` is only correct on a process whose operator set no pool
	// knob. Taking the app pool makes the whole arithmetic a function of the
	// one input that actually determines it, and leaves `cpus` entering in
	// exactly one place: the default.
	maxOpen func(app int) int
}

// dbAuxPoolConsumers names every PostgreSQL pool the RUNTIME opens for its
// own purposes.
//
// It is a list rather than a constant `3` so that adding a fifth pool is a
// change to this line, and the server sizing that reads it moves with it. The
// failure this guards against has already happened once: the embedded
// cluster's `max_connections` was derived from the app pool alone and left the
// process short by three pools' worth of backends at 6–9 cores.
var dbAuxPoolConsumers = []dbAuxPoolConsumer{
	{"analytics", dbSharedAuxPoolSizeFrom},                         // analytics_store.go
	{"live-sessions", dbSharedAuxPoolSizeFrom},                     // live_store.go (pgx path)
	{"telemetry", func(int) int { return telemetry.PoolMaxConns }}, // telemetry/persist.go
}

// dbAuxPoolConsumerMaxOpen returns the pool size the connection-demand
// arithmetic attributes to a named consumer on THIS machine.
//
// It is what a pool-ceiling gate should compare a live pool against. Asking
// `dbSharedAuxPoolConfig()` — the expression the acquire site itself
// evaluates — makes the gate an identity: it checks that the pool was built
// from the config, which is true by construction at the call site, and says
// nothing about whether the SERVER was sized for that pool. Routed through the
// demand table, the expected value is the same number the cluster's
// `max_connections` is derived from, and that number is pinned by a fixture a
// second implementation reproduces.
func dbAuxPoolConsumerMaxOpen(name string) (int, bool) {
	app, _ := dbAppPoolMaxOpenFor(runtime.GOMAXPROCS(0), IsServerless())
	for _, c := range dbAuxPoolConsumers {
		if c.name == name {
			return c.maxOpen(app), true
		}
	}
	return 0, false
}

// dbAuxPoolConsumerNames is the list as bare names, for the gates and the
// diagnostics that report which pools were counted.
func dbAuxPoolConsumerNames() []string {
	out := make([]string, 0, len(dbAuxPoolConsumers))
	for _, c := range dbAuxPoolConsumers {
		out = append(out, c.name)
	}
	return out
}

// dbProcessConnectionDemand returns the maximum number of PostgreSQL backends
// ONE Sky app process can hold open at once, on a machine with `cpus` cores.
//
// This is the WORST case, deliberately: it assumes every runtime pool
// resolves to a different DSN and therefore does NOT share (see the dbshare
// package). When they do share — the normal case, since "one database for
// everything" is what `DATABASE_URL` and `sky db provision --embed` both
// produce — real demand is `app + one shared pool`, well inside this. Sizing a
// server for the worst case its client can produce is the right direction to be
// wrong in; the alternative is a cluster that works until someone points
// telemetry at a second database.
//
// The sum is taken over what each consumer ACTUALLY asks dbshare for. Deriving
// it from a single "aux pool size" instead is the frame error this function has
// already been fixed for once, one level down: it under-reported by ten
// backends at 1 core and by four at 8, so the restart-overlap claim printed
// into every generated conf was false at every core count.
//
// # The app term follows the documented knob
//
// The app pool is read from `dbAppPoolMaxOpenFor`, which resolves it exactly as
// `Db_connect` does — deployment-aware default THEN the
// `<PREFIX>_DB_MAX_OPEN_CONNS` / `sky.toml [database] maxOpenConns` override.
// This function used to read `defaultPostgresPoolConfigFor` instead, i.e. the
// defaults with the operator's knob discarded, while the three aux terms
// already routed through the resolver. At `maxOpenConns = 64` on one core the
// process opened 92 backends and this reported 32. That the knob is documented
// (`docs/sky-toml.md`) and first-class made it worse, not better: setting it is
// how an operator tells Sky how big the process will be, and it was the one
// input the sizing ignored.
func dbProcessConnectionDemand(cpus int, serverless bool) int {
	app, _ := dbAppPoolMaxOpenFor(cpus, serverless)
	return dbProcessConnectionDemandFrom(app)
}

// dbDerivedProcessConnectionDemand is the demand sky DERIVES from the machine
// alone — the operator's pool knob deliberately ignored.
//
// It exists so the cluster sizings can clamp what they derive without clamping
// what the operator explicitly asked for. See `embeddedMaxConnections`.
func dbDerivedProcessConnectionDemand(cpus int, serverless bool) int {
	return dbProcessConnectionDemandFrom(defaultPostgresPoolConfigFor(cpus, serverless).MaxOpenConns)
}

// dbProcessConnectionDemandFrom is the arithmetic itself: the whole process's
// demand as a function of the APP pool's ceiling.
//
// Stating it this way is what keeps the frame honest. Every runtime pool is a
// share of the app's, so the app pool is the only independent input; `cpus`
// reaches the sum solely through the default the resolver starts from. A
// function of `cpus` alone can only be right for a process whose operator set
// no knob, and every gate that swept `cpus` was blind to precisely that.
func dbProcessConnectionDemandFrom(app int) int {
	n := app
	for _, c := range dbAuxPoolConsumers {
		n += c.maxOpen(app)
	}
	return n
}

// dbAppPoolMaxOpenFor is the app's own `Db.connect` pool ceiling on a machine
// with `cpus` cores, resolved the way PRODUCTION resolves it —
// `resolveDbPoolConfigFor`, the function `Db_connect` calls (db_auth.go:344).
//
// `unlimited` reports that the operator asked for an unbounded pool
// (`<PREFIX>_DB_MAX_OPEN_CONNS=0`, or a negative value, which the resolver
// folds to the same thing). NO finite `max_connections` covers an unbounded
// pool, so the sizing substitutes the deployment-aware default and the callers
// SAY SO in the conf they generate rather than printing a coverage claim that
// cannot be true. The operator has already been warned at connect time that a
// burst can exhaust the server; what must not happen is a generated file
// asserting otherwise.
func dbAppPoolMaxOpenFor(cpus int, serverless bool) (n int, unlimited bool) {
	c := resolveDbPoolConfigFor("pgx", cpus, serverless)
	if c.MaxOpenConns == 0 {
		return defaultPostgresPoolConfigFor(cpus, serverless).MaxOpenConns, true
	}
	return c.MaxOpenConns, false
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
	return dbAuxPoolConfigFor(runtime.GOMAXPROCS(0), IsServerless())
}

// dbAuxPoolConfigFor is dbAuxPoolConfig with the machine passed in.
//
// The seam exists so a gate — and the server-sizing arithmetic — can ask for
// the config at any core count WITHOUT restating how it is computed. Every
// per-core "size" function below is a projection of this one; there is no
// second derivation of the quarter-share to disagree with it.
func dbAuxPoolConfigFor(cpus int, serverless bool) dbPoolConfig {
	c := resolveDbPoolConfigFor("pgx", cpus, serverless)
	// The app pool may be unlimited; the runtime's own pools do not follow it
	// there — an unbounded session-store pool is a connection storm with no
	// upside — and `dbAppPoolMaxOpenFor` is the single place that substitution
	// is made, so the sizing and the pools cannot disagree about it.
	app, _ := dbAppPoolMaxOpenFor(cpus, serverless)
	n := dbAuxPoolSizeFrom(app)
	c.MaxOpenConns = n
	c.MaxIdleConns = n
	return c
}

// dbAuxPoolSizeFrom is the quarter-share itself, as a function of the app pool.
func dbAuxPoolSizeFrom(app int) int { return clampInt(app/4, 2, 8) }

// dbAuxPoolMaxOpenFor is the per-aux-pool ceiling on a given machine — the
// pool size a consumer would get if it did NOT share.
func dbAuxPoolMaxOpenFor(cpus int, serverless bool) int {
	return dbAuxPoolConfigFor(cpus, serverless).MaxOpenConns
}

// ── how much of a shared pool each consumer may hold ───────────────
//
// When the runtime's pools resolve to the same DSN they share one `*sql.DB`
// (see the dbshare package), which is what takes an 8-core process from 56
// backends to 40. Sharing on its own would lose the bulkhead the separate
// pools provided, so each consumer carries a cap.
//
// The caps are asymmetric on purpose. The session store is on the REQUEST
// path — every request reads and writes its session — so capping it below the
// pool would be capping the app itself for no benefit. The two BACKGROUND
// writers are capped instead, and capping them is what guarantees the session
// store cannot be starved: analytics and telemetry together can hold at most
// `dbAnalyticsShare + telemetry.Share` of the pool, so the session store
// always has the rest.
//
// Both background writers are single-goroutine batchers after the buffered
// writer landed, so a cap of 2 costs them nothing: one slot for the flusher,
// one for a concurrent read (the console tab, an erase, a prune).
// dbAnalyticsShare is the analytics writer's cap. dbSessionShare is 0, meaning
// uncapped — the hot path draws on the whole pool, and the background caps are
// what keep it from being consumed.
//
// Telemetry's cap is NOT restated here. It lives in `telemetry.Share`, because
// telemetry is the package that passes it to dbshare.Acquire, and rt reads it
// from there. It used to be declared in both places under a gate asserting the
// two agreed — which is a gate proving a copy. One definition needs no gate.
const (
	dbAnalyticsShare = 2
	dbSessionShare   = 0
)

// dbSharedAuxPoolConfig sizes the ONE pool the runtime's consumers share when
// their DSNs resolve alike. It is the config the acquire sites pass, so it is
// also the config every gate about that pool must ask.
func dbSharedAuxPoolConfig() dbPoolConfig {
	return dbSharedAuxPoolConfigFor(runtime.GOMAXPROCS(0), IsServerless())
}

// dbSharedAuxPoolConfigFor is dbSharedAuxPoolConfig with the machine passed in.
//
// The size is the session store's own former pool size PLUS the two background
// caps, and that addition is the whole bulkhead argument. Sizing the shared
// pool at merely `aux` and capping the background writers inside it would take
// connections AWAY from the session store to give them to telemetry: on a small
// machine `aux` is 2, two caps of 2 consume the entire pool, and the request
// path is guaranteed nothing. A gate caught exactly that
// (TestTheBackgroundWritersCannotStarveTheSessionStore) before this shipped —
// though only after that gate was pointed at THIS function instead of at a
// parallel one that the acquire sites never call.
//
// With the addition, the session store can always obtain `aux` connections —
// precisely what it had when it owned a pool outright — so sharing costs it
// nothing, while the process opens `aux + 4` backends instead of `3 × aux`.
// That is a strict improvement at every core count, since `aux + 4 ≤ 3 × aux`
// for all `aux ≥ 2`, and `aux` is floored at 2.
func dbSharedAuxPoolConfigFor(cpus int, serverless bool) dbPoolConfig {
	c := dbAuxPoolConfigFor(cpus, serverless)
	n := c.MaxOpenConns + dbAnalyticsShare + telemetry.Share
	c.MaxOpenConns = n
	c.MaxIdleConns = n
	return c
}

// dbSharedAuxPoolSizeFrom is that size as a function of the app pool — the
// form the demand table consumes, so the acquire sites and the server sizing
// read one derivation rather than two.
func dbSharedAuxPoolSizeFrom(app int) int {
	return dbAuxPoolSizeFrom(app) + dbAnalyticsShare + telemetry.Share
}

// dbSharedAuxPoolMaxOpenFor is the shared pool's size on a given machine — a
// projection of the config above, never a second derivation of it.
func dbSharedAuxPoolMaxOpenFor(cpus int, serverless bool) int {
	return dbSharedAuxPoolConfigFor(cpus, serverless).MaxOpenConns
}

// dbGuaranteedSessionShare returns the number of connections the session
// store is guaranteed to be able to obtain from a shared pool of `poolSize`,
// however hard the background writers are working.
//
// Stated as a function so a gate can assert the bulkhead as a PROPERTY rather
// than by re-deriving the arithmetic and agreeing with itself.
func dbGuaranteedSessionShare(poolSize int) int {
	n := poolSize - dbAnalyticsShare - telemetry.Share
	if n < 0 {
		return 0
	}
	return n
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
	return resolveDbPoolConfigFor(driver, runtime.GOMAXPROCS(0), IsServerless())
}

// resolveDbPoolConfigFor is resolveDbPoolConfig with the machine passed in, so
// the server-sizing arithmetic can ask what the pools would be on any host
// without a second copy of the resolution rules (defaults, env overrides,
// clamps and all).
func resolveDbPoolConfigFor(driver string, cpus int, serverless bool) dbPoolConfig {
	if driver != "pgx" {
		// SQLite. Warn rather than silently ignore — a knob that looks
		// set and does nothing is the failure mode sky.toml's
		// unknown-key warning exists to prevent.
		for _, suffix := range dbPoolEnvSuffixes {
			if skyGetenv(suffix) != "" {
				rtWarn("db.connect: " + skyEnvName(suffix) +
					" is ignored on SQLite — SQLite has a single global writer lock, " +
					"so the pool is pinned to one connection (raising it reintroduces SQLITE_BUSY)")
			}
		}
		return sqlitePoolConfig()
	}
	c := defaultPostgresPoolConfigFor(cpus, serverless)
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
		rtWarn("db.connect: " + skyEnvName("DB_MAX_OPEN_CONNS") +
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
			rtWarn("db.connect: " + skyEnvName("DB_ISOLATION") +
				" is ignored on SQLite — its transactions already serialise on the " +
				"single pooled connection")
		} else if level, ok := parseIsolationLevel(raw); !ok {
			rtWarn("db.connect: " + skyEnvName("DB_ISOLATION") + "=" + raw +
				" is not a recognised isolation level (read uncommitted / read committed / " +
				"repeatable read / serializable) — using the driver default")
		} else {
			cfg.Opts = &sql.TxOptions{Isolation: level}
		}
	}
	if n := dbEnvInt("DB_TX_RETRY", 0); n > 0 {
		if driver != "pgx" {
			rtWarn("db.connect: " + skyEnvName("DB_TX_RETRY") +
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
		rtWarn("db.connect: " + skyEnvName(suffix) + "=" + raw +
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
	rtWarn("db.connect: " + skyEnvName(suffix) + "=" + raw +
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
