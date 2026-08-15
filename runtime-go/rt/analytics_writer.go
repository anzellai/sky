// analytics_writer.go — the buffered, single-writer, batching path for
// Std.Analytics events.
//
// # What this replaces, and why it was the biggest lever
//
// Until this file existed, `analyticsStoreInsert` ran one
// `INSERT INTO analytics_events (…) VALUES (…)` per event, synchronously, on
// the goroutine that emitted it. Every event was therefore its own
// transaction, and every transaction is an fsync: on PostgreSQL that is a
// WAL flush to durable storage before the INSERT returns, and on SQLite it is
// a WAL commit. That ceiling is a property of the disk, not of the code —
// order 5–10k events/s — while a multi-row INSERT reaches order 100k–500k/s
// on the same hardware, because the fsync is amortised across the whole
// batch rather than paid per row.
//
// The row-at-a-time shape also put the disk on the REQUEST path. A page view
// is tracked while rendering the page, so a slow or stalled analytics disk
// showed up as a slow page — the observability surface degrading the thing it
// observes, which is the failure mode `telemetry/persist.go` had already been
// given a buffered flusher to avoid. This file gives analytics the same shape:
//
//	hot path  → marshal + enqueue (bounded, non-blocking) → return
//	writer    → one goroutine, one connection, batched multi-row INSERT
//
// # The four properties that make it safe rather than merely fast
//
//  1. BOUNDED QUEUE. An unbounded channel converts a database stall into an
//     OOM — the queue grows for as long as the stall lasts, and the process
//     dies of the outage rather than riding it out. The queue is a fixed
//     `analyticsQueueCap`.
//
//  2. A DELIBERATE, COUNTED OVERFLOW POLICY. See `analyticsStoreInsert`.
//
//  3. FLUSH ON SHUTDOWN. A buffered writer that loses its queue on SIGTERM
//     loses events on every single deploy, which is worse than the problem it
//     solved: it is a silent, recurring, correlated loss rather than a random
//     one. The writer registers a shutdown hook, and under `sky db provision
//     --embed` that hook runs in the supervisor's DRAIN phase — strictly
//     before PostgreSQL is stopped (`pgSupervisor.shutdown`: stop-accepting →
//     RunShutdownHooks → awaitShutdownHooks → stopPostgres). A flush that ran
//     after the database stopped would be a flush into a closed socket.
//
//  4. SINGLE WRITER. Exactly one goroutine executes the INSERTs, so the
//     process holds ONE analytics connection busy no matter how many
//     goroutines emit events. That is the connection-count half of the win,
//     and it is why the batch is written by the flusher rather than by
//     whichever caller happens to fill it.
//
// # Read-your-writes
//
// Buffering makes writes asynchronous, which would otherwise mean a query
// issued straight after `track` could miss the event it just recorded — for
// the console's Analytics tab, for `Analytics.erase` (a right-to-erasure
// request that missed a queued event would be a COMPLIANCE bug, not a
// staleness one), and for `Analytics.openStore`. Every read path therefore
// calls `analyticsFlushPending` first, which asks the writer for a synchronous
// drain and waits for it. The single-writer property is preserved: the reader
// does not write, it asks the writer to.
package rt

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sky-app/rt/telemetry"
)

const (
	// analyticsQueueCap bounds the pending-event queue. ~4 k events is
	// several seconds of a busy app's analytics at the batching path's
	// throughput, which is the useful shape for a bounded queue: long enough
	// to ride out a checkpoint, a failover or a brief disk stall without
	// dropping anything, short enough that a SUSTAINED outage is reported as
	// back-pressure within seconds instead of being absorbed into RAM until
	// the process dies.
	//
	// At six columns of mostly-short strings this is single-digit MB of
	// worst-case retention, which is the budget an app can afford to lose to
	// its own telemetry.
	analyticsQueueCap = 4096

	// analyticsBatchSize is the row count that triggers a flush on size.
	//
	// 256 rows × 6 columns = 1536 bind parameters, comfortably inside
	// PostgreSQL's 65535-parameter ceiling for a single statement (the reason
	// a size cap is a correctness constraint here and not only a tuning
	// choice — an unbounded batch would eventually produce a statement the
	// server rejects, under exactly the load that produced the big batch).
	analyticsBatchSize = 256

	// analyticsFlushInterval is the flush-on-time half of "size or interval,
	// whichever comes first". It bounds how long an event can sit unwritten
	// on an app that is NOT busy enough to fill a batch — which is most apps
	// most of the time, and the case where a size-only trigger would leave
	// the first event of the day invisible until the second arrived.
	//
	// 250 ms also bounds the loss window on an unclean kill (a crash, a
	// SIGKILL, an OOM — the paths that do not run shutdown hooks) to a
	// quarter-second of events.
	analyticsFlushInterval = 250 * time.Millisecond

	// analyticsFlushWait caps how long a READ waits for the writer to drain
	// before proceeding anyway. A reader that blocked indefinitely on a
	// wedged writer would turn a degraded analytics store into a hung
	// console page.
	analyticsFlushWait = 5 * time.Second
)

// analyticsRow is one event, already flattened and JSON-marshalled, ready to
// bind. Marshalling happens on the EMITTING goroutine rather than in the
// writer, for two reasons: it keeps the single writer spending its time on
// the database instead of on `encoding/json`, and it means the caller's
// `payload` map is not retained past the call — a map handed to the queue and
// mutated afterwards by the caller would be a data race the race detector
// would find only under load.
type analyticsRow struct {
	ts     int64
	anonID any // string, or nil for SQL NULL
	userID any // string, or nil for SQL NULL
	event  string
	props  any // JSON TEXT, or nil
	ctx    any // JSON TEXT, or nil
}

// analyticsWriter owns the queue, the flusher goroutine and the counters.
type analyticsWriter struct {
	db     *sql.DB
	driver string // "sqlite" | "pgx"

	queue    chan analyticsRow
	flushReq chan chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// syncCommitOff records whether this writer asks PostgreSQL to skip the
	// WAL fsync at commit. See analyticsSynchronousCommitOff.
	syncCommitOff bool

	// Counters. These are the gate surface as much as the operator surface:
	// a batching gate that only checks that rows arrive passes whether or not
	// anything was batched, so `statements` — incremented once per INSERT
	// actually executed — is what makes the claim falsifiable.
	dropped    atomic.Int64
	rows       atomic.Int64
	statements atomic.Int64
	failures   atomic.Int64
	// lastErr keeps the most recent write failure. The operator-facing
	// warning is once-per-process by design (a writer failing every 250 ms
	// would otherwise fill the log), which means a SECOND failure — including
	// the first one a test sees, when an earlier test consumed the Once — is
	// otherwise invisible. A gate that cannot say WHY a write failed reports
	// "0 rows" and leaves the reader to guess.
	lastErr atomic.Value // error

	dropWarnOnce sync.Once

	// Last values handed to the metrics store. Touched ONLY by the writer
	// goroutine (from publishMetrics, called from writeBatch), so they need
	// no synchronisation — and deliberately are not atomics, so that a future
	// reader from another goroutine trips the race detector rather than
	// silently double-counting.
	pubRows       int64
	pubDropped    int64
	pubStatements int64
}

// analyticsSynchronousCommitOff reports whether the analytics writer should
// run its flush transactions with `synchronous_commit = off`.
//
// # Why this is the right default for analytics, and only for analytics
//
// `synchronous_commit = off` tells PostgreSQL to acknowledge a COMMIT once
// the WAL record is in memory, without waiting for it to reach durable
// storage. It does NOT risk corruption and it does NOT relax atomicity or
// isolation — a crash cannot leave a torn row or a half-applied batch. What
// it risks is precisely one thing: a crash of the SERVER (not of the app) can
// lose the last few hundred milliseconds of committed transactions.
//
// For app data that is unacceptable — an order the customer was told was
// placed must survive. For analytics it is the obviously correct trade: the
// events in that window were a sample of behaviour, the app already drops
// them under queue overflow by design, and paying an fsync per batch to
// protect a sample is spending the app's write throughput on the wrong thing.
//
// It is a PER-TRANSACTION setting, applied with `SET LOCAL` inside the flush
// transaction, so it is scoped to this writer's commits and cannot leak to
// the app's pool — including when they share a pool. Setting it globally in
// `postgresql.conf` would silently weaken the durability of every write in
// the cluster, which is why the generated conf refuses to set it (see
// `tuning_block` in rust/crates/sky/src/db_shared.rs: resource knobs only,
// nothing that changes what a query means).
//
// An operator who genuinely wants durable analytics says so:
//
//	SKY_ANALYTICS_SYNCHRONOUS_COMMIT=on
func analyticsSynchronousCommitOff() bool {
	return skyEnvSynchronousCommitOff("ANALYTICS_SYNCHRONOUS_COMMIT")
}

// skyEnvSynchronousCommitOff parses a `<PREFIX>_<name>` knob spelled with
// PostgreSQL's own vocabulary (`on` / `off`), defaulting to `off`. Shared by
// the analytics and telemetry sinks so the two knobs cannot drift in meaning.
func skyEnvSynchronousCommitOff(suffix string) bool {
	switch strings.ToLower(strings.TrimSpace(skyGetenv(suffix))) {
	case "", "off", "false", "0", "local":
		return true
	case "on", "true", "1", "remote_write", "remote_apply":
		return false
	default:
		Log_warn("analytics: " + skyEnvName(suffix) +
			" must be `on` or `off` — using `off` (the default: analytics trades a " +
			"few hundred ms of crash-loss for throughput)")
		return true
	}
}

// newAnalyticsWriter starts the flusher for an open store and registers its
// shutdown flush.
func newAnalyticsWriter(db *sql.DB, driver string) *analyticsWriter {
	w := &analyticsWriter{
		db:            db,
		driver:        driver,
		queue:         make(chan analyticsRow, analyticsQueueCap),
		flushReq:      make(chan chan struct{}),
		stop:          make(chan struct{}),
		syncCommitOff: driver == "pgx" && analyticsSynchronousCommitOff(),
	}
	w.wg.Add(1)
	go w.run()
	// Registered at writer START rather than at process init, mirroring the
	// hub exporter (exporter.go): a hook for a subsystem that never opened
	// would be a hook that has nothing to flush.
	RegisterShutdownHook("analytics-writer", func(ctx context.Context) {
		w.shutdown(ctx)
	})
	return w
}

// enqueue is the hot path's non-blocking send. Reports whether the row was
// accepted; a false return has already been counted as a drop.
func (w *analyticsWriter) enqueue(r analyticsRow) bool {
	select {
	case w.queue <- r:
		return true
	default:
		n := w.dropped.Add(1)
		// One warning per process, carrying the running total, so a
		// sustained overflow is DISCOVERABLE without becoming a log flood
		// that costs more than the events it is reporting on. The counter is
		// also republished as a metric on every flush (see writeBatch), so
		// the ongoing rate is visible even after the one-shot warning.
		w.dropWarnOnce.Do(func() {
			logStructured("warn", "analytics.queue_full",
				"detail", "the analytics write queue is full — events are being dropped",
				"policy", "drop-newest",
				"queue_cap", itoa64(int64(analyticsQueueCap)),
				"dropped", itoa64(n),
				"fix", "the analytics store cannot keep up or is stalled; check its disk/server")
		})
		return false
	}
}

// run is the single writer goroutine: flush on size or interval, whichever
// comes first, plus an explicit synchronous flush for readers and a final
// drain on shutdown.
func (w *analyticsWriter) run() {
	defer w.wg.Done()
	tick := time.NewTicker(analyticsFlushInterval)
	defer tick.Stop()

	batch := make([]analyticsRow, 0, analyticsBatchSize)

	// coalesce pulls everything already queued into the current batch, up to
	// the size cap. This is what turns a burst of N events into ceil(N/256)
	// statements instead of N: without it the loop would take one row per
	// select pass and only the ticker would ever group them.
	coalesce := func() {
		for len(batch) < analyticsBatchSize {
			select {
			case r := <-w.queue:
				batch = append(batch, r)
			default:
				return
			}
		}
	}
	flush := func() {
		if len(batch) > 0 {
			w.writeBatch(batch)
			batch = batch[:0]
		}
	}

	for {
		select {
		case <-w.stop:
			// Final drain. Everything that reached the queue before the stop
			// is written, in batches, before the goroutine returns — this is
			// the deploy-safety property.
			for {
				select {
				case r := <-w.queue:
					batch = append(batch, r)
					coalesce()
					if len(batch) >= analyticsBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}

		case done := <-w.flushReq:
			// A reader is waiting. Drain to empty, not merely one batch:
			// read-your-writes means every event enqueued before the request
			// must be on disk when we close `done`.
			for {
				coalesce()
				flush()
				if len(w.queue) == 0 {
					break
				}
			}
			close(done)

		case r := <-w.queue:
			batch = append(batch, r)
			coalesce()
			if len(batch) >= analyticsBatchSize {
				flush()
			}

		case <-tick.C:
			flush()
		}
	}
}

// writeBatch writes the whole batch as ONE multi-row INSERT.
//
// One statement, one transaction, one fsync — for up to `analyticsBatchSize`
// events. The per-row shape this replaces paid all three per event.
func (w *analyticsWriter) writeBatch(batch []analyticsRow) {
	if len(batch) == 0 {
		return
	}
	stmt, args := analyticsInsertStatement(w.driver, batch)
	var err error
	if w.syncCommitOff {
		err = w.execSyncCommitOff(stmt, args)
	} else {
		_, err = w.db.Exec(stmt, args...)
	}
	w.statements.Add(1)
	if err != nil {
		w.failures.Add(1)
		w.lastErr.Store(err)
		// Warn once per process — a broken store (disk full, permissions,
		// server down) must be diagnosable, but a failing writer retrying
		// every 250 ms would otherwise fill the log with the same line.
		analyticsWriteErrWarnOnce.Do(func() {
			logStructured("warn", "analytics.write_failed",
				"detail", "an analytics batch failed to persist",
				"rows", itoa64(int64(len(batch))),
				"error", err.Error())
		})
		return
	}
	w.rows.Add(int64(len(batch)))
	w.publishMetrics()
}

// execSyncCommitOff runs the batch in a transaction that has asked PostgreSQL
// not to wait for the WAL fsync at commit.
//
// `SET LOCAL` is used rather than `SET`, and that distinction is the whole
// safety argument: `SET LOCAL` reverts at the end of the transaction, so the
// setting cannot outlive this flush on a POOLED connection and reach the next
// borrower of it. A bare `SET` on a pooled connection would silently make
// somebody else's writes non-durable — including the app's, once Phase D lets
// consumers share a pool.
func (w *analyticsWriter) execSyncCommitOff(stmt string, args []any) error {
	tx, err := w.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck — the Commit below supersedes it
	if _, err := tx.Exec(`SET LOCAL synchronous_commit = off`); err != nil {
		return err
	}
	if _, err := tx.Exec(stmt, args...); err != nil {
		return err
	}
	return tx.Commit()
}

// publishMetrics republishes the writer's counters so an operator sees them
// at /_sky/console and on the Prometheus endpoint, rather than only in the
// one-shot warning — "silently dropping is not correct" is the requirement,
// and a warning that fires once per process is not an ongoing rate.
//
// Published as DELTAS through the counter API (`Add`), not as absolute values
// through a gauge, so the series is a real monotonic counter that
// `rate()` means something on. Called once per flush rather than once per
// event, so reporting an overload does not add load proportional to it.
func (w *analyticsWriter) publishMetrics() {
	s := telemetry.Default()
	if s == nil {
		return
	}
	pub := func(name string, now int64, last *int64) {
		if d := now - *last; d > 0 {
			s.Add(name, nil, float64(d))
			*last = now
		}
	}
	pub("sky_analytics_events_written_total", w.rows.Load(), &w.pubRows)
	pub("sky_analytics_events_dropped_total", w.dropped.Load(), &w.pubDropped)
	pub("sky_analytics_write_batches_total", w.statements.Load(), &w.pubStatements)
}

// flushNow asks the writer to drain synchronously and waits for it.
func (w *analyticsWriter) flushNow() {
	done := make(chan struct{})
	timer := time.NewTimer(analyticsFlushWait)
	defer timer.Stop()
	select {
	case w.flushReq <- done:
	case <-w.stop:
		return
	case <-timer.C:
		return
	}
	select {
	case <-done:
	case <-timer.C:
	}
}

// shutdown drains the queue and stops the writer, inside whatever budget the
// shutdown chain has left.
func (w *analyticsWriter) shutdown(ctx context.Context) {
	w.stopOnce.Do(func() { close(w.stop) })
	drained := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-ctx.Done():
		// Budget exhausted. The events still queued are lost; say so rather
		// than exiting quietly, because "we lost events on this deploy" is
		// exactly the fact an operator needs and cannot otherwise recover.
		if n := len(w.queue); n > 0 {
			logStructured("warn", "analytics.shutdown_incomplete",
				"detail", "the shutdown budget expired before the analytics queue drained",
				"unwritten", itoa64(int64(n)))
		}
	}
}

// analyticsInsertStatement builds the multi-row INSERT and its bind args.
//
// Authored with `?` and passed through `analyticsQFor` exactly once, so there
// is a single SQL string for both dialects rather than a per-dialect fork —
// the rewrite renumbers every `?` to `$1, $2, …` for pgx, which is precisely
// what a multi-row VALUES list needs.
//
// The driver is a PARAMETER, not read from `analyticsDriverName`. The writer
// owns a handle and therefore owns the dialect that goes with it; taking the
// dialect from a process-wide variable instead is how the first version of
// this sent `?` to PostgreSQL.
func analyticsInsertStatement(driver string, batch []analyticsRow) (string, []any) {
	const cols = 6
	var b strings.Builder
	b.Grow(96 + len(batch)*14)
	b.WriteString(`INSERT INTO analytics_events (ts, anonymous_id, user_id, event, props, context) VALUES `)
	args := make([]any, 0, len(batch)*cols)
	for i, r := range batch {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`(?,?,?,?,?,?)`)
		args = append(args, r.ts, r.anonID, r.userID, r.event, r.props, r.ctx)
	}
	return analyticsQFor(driver, b.String()), args
}

// itoa64 — strconv.FormatInt without the import churn at each call site.
func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}
