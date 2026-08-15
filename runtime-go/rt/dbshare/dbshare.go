// Package dbshare is a process-wide registry of `*sql.DB` pools, keyed by
// resolved DSN + driver, with a per-consumer concurrency cap on top.
//
// # The problem
//
// One Sky app process opened FOUR PostgreSQL-facing pools: the app's own
// `Db.connect`, the Std.Analytics store, the Sky.Live session store, and the
// telemetry persistence store. Each called `sql.Open` independently, and each
// got its own pool of backends EVEN WHEN THE DSNs RESOLVED TO THE SAME
// STRING — which is the normal case, because "one database for everything" is
// the shape `DATABASE_URL` and `sky db provision --embed` both produce.
//
// On an 8-core host that is 32 + 8 + 8 + 8 = 56 backends for one process.
// PostgreSQL's own `max_connections` default is 100, and each backend is an
// operating-system PROCESS costing several megabytes, so the redundancy is
// paid twice: once in memory on the server, and once in the connection budget
// that decides how many app instances the database can serve at all.
//
// `database/sql` is explicitly safe for concurrent use by multiple
// goroutines, so there was never a correctness reason for the separate
// handles. They were separate because each subsystem opened its own.
//
// # The property that must NOT be lost
//
// Separate pools did buy something real: a BULKHEAD. A burst of telemetry
// writes could exhaust the telemetry pool and nothing else, because the app's
// queries drew on a different one. Collapsing four pools into one collapses
// that isolation too — and "observability took the app down" is a worse
// failure than "observability was slow".
//
// So the registry hands back a Handle, not a bare `*sql.DB`: one pool that
// the SERVER sees as one set of connections, plus a per-consumer semaphore
// bounding how much of it any one consumer may hold at once. That is the
// bulkhead pattern applied in-process, and it keeps both properties — fewer
// connections AND isolation between the consumers sharing them.
//
// # What deliberately does NOT share
//
// The app's own `Db.connect` pool on PostgreSQL registers a pgx config with
// `QueryExecModeSimpleProtocol` (see db_auth.go) so that SQLite-era apps
// binding stringified integers keep working. That registration produces its
// OWN opaque DSN, so the app pool's registry key never matches the raw
// `postgres://…` key the runtime's pools use, and it gets its own pool
// without anything special being written to arrange it. That is the correct
// outcome rather than an accident to be worked around: the app wants the
// simple protocol and the runtime's pools want the extended protocol with
// prepared statements, and a shared pool can only have one query exec mode.
//
// A consumer whose DSN differs for any other reason likewise gets its own
// pool, which is the whole point of keying on the RESOLVED string.
package dbshare

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Config is the pool shape a consumer asks for.
type Config struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// entry is one shared pool and its refcount.
type entry struct {
	db   *sql.DB
	cfg  Config
	refs int
	// consumers counts how many Handles have been issued over this pool's
	// lifetime, so a gate can tell "shared" from "happened to be alone".
	consumers int
}

var (
	mu       sync.Mutex
	registry = map[string]*entry{}
)

// Handle is one consumer's view of a pool that may be shared with others.
//
// It is NOT an `*sql.DB` replacement — `DB()` hands back the real thing for
// code that needs it. It adds two things: a concurrency cap that bounds this
// consumer's share, and a `Close` that only closes the underlying pool when
// the last consumer lets go.
type Handle struct {
	key    string
	db     *sql.DB
	sem    chan struct{}
	closed atomic.Bool
	// shared records whether any other consumer held this pool when this
	// handle was issued, or took it afterwards. Gate surface.
	shared *atomic.Bool
	// acquisitions counts how many times this consumer's slot has been taken.
	//
	// It exists because "the cap is configured" and "the cap is USED" are
	// different claims, and only the second one is a bulkhead. Acquiring a
	// capped handle and then writing through `Handle.DB()` builds the
	// semaphore and never touches it — the cap reads as enforced in review
	// and enforces nothing. This counter is what lets a gate tell the two
	// apart.
	acquisitions atomic.Int64
}

// ErrClosed is returned by a Handle used after Close.
var ErrClosed = errors.New("dbshare: handle is closed")

// Acquire returns a Handle over the pool for (driver, dsn), opening it if this
// is the first consumer.
//
// `cap` bounds this consumer's concurrent in-flight statements through the
// semaphore-wrapped methods. A cap of 0 means "no cap" — appropriate for the
// one consumer on the request hot path, whose share should not be throttled;
// the OTHER consumers being capped is what guarantees it cannot be starved.
//
// # Pool sizing when consumers disagree
//
// A shared pool has one size, so when a second consumer asks for a LARGER
// pool the pool grows to the larger of the two. It is never shrunk: an
// existing consumer sized its expectations against the pool it was given, and
// silently taking connections away from it to satisfy a newcomer converts a
// configuration difference into a stall under load. Growing is safe in the
// direction that matters — the server's budget is checked when the cluster is
// sized (see the pool-demand arithmetic in rt/db_pool.go), not here.
func Acquire(driver, dsn string, cfg Config, cap int) (*Handle, error) {
	key := driver + "\x00" + dsn

	mu.Lock()
	defer mu.Unlock()

	e, ok := registry[key]
	if !ok {
		db, err := sql.Open(driver, dsn)
		if err != nil {
			return nil, err
		}
		apply(db, cfg)
		e = &entry{db: db, cfg: cfg}
		registry[key] = e
	} else if cfg.MaxOpenConns > e.cfg.MaxOpenConns && e.cfg.MaxOpenConns != 0 {
		e.cfg.MaxOpenConns = cfg.MaxOpenConns
		if cfg.MaxIdleConns > e.cfg.MaxIdleConns {
			e.cfg.MaxIdleConns = cfg.MaxIdleConns
		}
		apply(e.db, e.cfg)
	}
	e.refs++
	e.consumers++

	h := &Handle{key: key, db: e.db, shared: &atomic.Bool{}}
	if cap > 0 {
		h.sem = make(chan struct{}, cap)
	}
	if e.consumers > 1 {
		h.shared.Store(true)
	}
	return h, nil
}

func apply(db *sql.DB, c Config) {
	// Open first: `database/sql` silently reduces MaxIdleConns to
	// MaxOpenConns when the former is larger.
	db.SetMaxOpenConns(c.MaxOpenConns)
	db.SetMaxIdleConns(c.MaxIdleConns)
	db.SetConnMaxLifetime(c.ConnMaxLifetime)
	db.SetConnMaxIdleTime(c.ConnMaxIdleTime)
}

// DB hands back the underlying pool, bypassing this consumer's cap.
//
// For the consumer that is allowed the whole pool (cap 0) this is the normal
// access path and costs nothing. For a capped consumer it is an escape hatch
// that should be rare and deliberate — the cap is the bulkhead.
func (h *Handle) DB() *sql.DB { return h.db }

// Shared reports whether more than one consumer has taken this pool.
func (h *Handle) Shared() bool { return h.shared.Load() }

// acquire takes this consumer's slot, or returns immediately when uncapped.
func (h *Handle) acquire(ctx context.Context) (func(), error) {
	if h.closed.Load() {
		return nil, ErrClosed
	}
	if h.sem == nil {
		return func() {}, nil
	}
	select {
	case h.sem <- struct{}{}:
		h.acquisitions.Add(1)
		return func() { <-h.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ExecContext runs a statement inside this consumer's share.
func (h *Handle) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	release, err := h.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return h.db.ExecContext(ctx, query, args...)
}

// Exec is ExecContext with a background context.
func (h *Handle) Exec(query string, args ...any) (sql.Result, error) {
	return h.ExecContext(context.Background(), query, args...)
}

// BeginTx starts a transaction inside this consumer's share.
//
// The slot is held for the WHOLE transaction and released by Commit or
// Rollback, because a transaction pins its connection for its lifetime — a
// cap that released at BeginTx would bound nothing.
func (h *Handle) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	release, err := h.acquire(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := h.db.BeginTx(ctx, opts)
	if err != nil {
		release()
		return nil, err
	}
	return &Tx{Tx: tx, release: release}, nil
}

// Begin is BeginTx with a background context and driver-default options.
func (h *Handle) Begin() (*Tx, error) { return h.BeginTx(context.Background(), nil) }

// Tx is a transaction that releases its consumer slot when it ends.
type Tx struct {
	*sql.Tx
	release func()
	once    sync.Once
}

func (t *Tx) Commit() error {
	defer t.once.Do(t.release)
	return t.Tx.Commit()
}

func (t *Tx) Rollback() error {
	defer t.once.Do(t.release)
	return t.Tx.Rollback()
}

// Close releases this consumer's reference. The underlying pool is closed
// only when the LAST consumer closes.
//
// Refcounting is the point: without it, one subsystem shutting down would
// close a pool another is still serving requests through, and the symptom —
// `sql: database is closed` from a component that was never asked to
// stop — would point at the victim rather than at the cause.
func (h *Handle) Close() error {
	if h.closed.Swap(true) {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	e, ok := registry[h.key]
	if !ok {
		return nil
	}
	e.refs--
	if e.refs > 0 {
		return nil
	}
	delete(registry, h.key)
	return e.db.Close()
}

// ── test/gate surface ──────────────────────────────────────────────

// InFlight reports how many of this consumer's slots are currently held. 0
// for an uncapped handle. Gate surface for the bulkhead: it lets a test
// observe the cap under real concurrency rather than assert the constant back
// to itself.
func (h *Handle) InFlight() int {
	if h.sem == nil {
		return 0
	}
	return len(h.sem)
}

// Acquisitions reports how many times this consumer's slot has been taken —
// i.e. how much work actually went THROUGH the cap rather than around it.
func (h *Handle) Acquisitions() int64 { return h.acquisitions.Load() }

// Cap reports this consumer's configured ceiling (0 = uncapped).
func (h *Handle) Cap() int {
	if h.sem == nil {
		return 0
	}
	return cap(h.sem)
}

// PoolCount reports how many distinct pools the registry holds. A gate uses
// it to assert that same-DSN consumers collapsed onto one.
func PoolCount() int {
	mu.Lock()
	defer mu.Unlock()
	return len(registry)
}

// ResetForTesting drops every registered pool. Test-only.
func ResetForTesting() {
	mu.Lock()
	defer mu.Unlock()
	for k, e := range registry {
		_ = e.db.Close()
		delete(registry, k)
	}
}
