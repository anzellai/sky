package rt

// A `database/sql` driver that counts the statements handed to the server.
//
// # Why a wrapper driver rather than a server-side counter
//
// The obvious instrument for "how many round-trips did this cost" is
// PostgreSQL's own `pg_stat_database.xact_commit`. It was tried first and it
// is unusable for this, for two independent reasons discovered by measuring
// rather than by reading:
//
//  1. Reading `pg_stat_database` is ITSELF a transaction, so any gate that
//     polls the counter increments what it is sampling — the value cannot
//     settle, by construction.
//  2. The statistics collector reports asynchronously and throttled, and a
//     burst of single-statement autocommit transactions is not reflected in
//     the counter the way an equal number of explicit BEGIN/COMMIT
//     transactions is. A probe measured 500 autocommit INSERTs as a delta of
//     8, and 500 explicit transactions as 517. An instrument that answers
//     "8" and "517" for the same amount of work cannot support a claim about
//     round-trips.
//
// Counting in a driver shim sits BELOW the code under test — analytics_writer.go
// cannot see it, cannot report to it, and cannot be written to satisfy it —
// while being exact and immediate. One `ExecContext` is one statement sent to
// the server, which is the quantity the batching claim is about.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/stdlib"
)

// countingStmts is the process-wide tally, keyed by the tag embedded in the
// DSN so concurrent gates cannot read each other's numbers.
var (
	countingMu sync.Mutex
	counting   = map[string]*atomic.Int64{}
)

func countingCounter(tag string) *atomic.Int64 {
	countingMu.Lock()
	defer countingMu.Unlock()
	c, ok := counting[tag]
	if !ok {
		c = &atomic.Int64{}
		counting[tag] = c
	}
	return c
}

type countingDriver struct{}

// Open splits "<tag>\x00<dsn>" so one registered driver serves every gate.
func (countingDriver) Open(name string) (driver.Conn, error) {
	tag, dsn := splitCountingDSN(name)
	c, err := stdlib.GetDefaultDriver().Open(dsn)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: c, n: countingCounter(tag)}, nil
}

func splitCountingDSN(name string) (tag, dsn string) {
	for i := 0; i < len(name); i++ {
		if name[i] == 0 {
			return name[:i], name[i+1:]
		}
	}
	return "", name
}

type countingConn struct {
	driver.Conn
	n *atomic.Int64
}

// ExecContext is the counted path: every statement the pool sends lands here.
func (c *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	c.n.Add(1)
	return e.ExecContext(ctx, query, args)
}

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return q.QueryContext(ctx, query, args)
}

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	b, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return c.Conn.Begin() //nolint:staticcheck — fallback for a driver without BeginTx
	}
	return b.BeginTx(ctx, opts)
}

func (c *countingConn) Ping(ctx context.Context) error {
	p, ok := c.Conn.(driver.Pinger)
	if !ok {
		return nil
	}
	return p.Ping(ctx)
}

func init() { sql.Register("pgx-counting", countingDriver{}) }

// countingDSN builds the DSN a gate hands to `sql.Open("pgx-counting", …)`,
// and returns the counter it will feed.
func countingDSN(tag, dsn string) (string, *atomic.Int64) {
	return tag + "\x00" + dsn, countingCounter(tag)
}
