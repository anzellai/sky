// Package jobs implements Sky's Std.Jobs background-task module.
// Phase 1.3 of the v1 production-readiness push.
//
// Design goals (full RFC: docs/v1-rfc/1-observability.md, jobs
// section in v1-roadmap.md Phase 1.3):
//
//   - Default ON for any binary that imports Std.Jobs. AI-deployed
//     apps get retry / dead-letter without configuration.
//
//   - Three backends, opt-in via sky.toml [jobs] store = "...":
//     "memory" (default — dev / single-process), "sqlite" (file-
//     backed; survives restart on single host), "postgres" (multi-
//     host; deferred to 1.3.x — interface in place).
//
//   - Exponential backoff: 1s, 2s, 4s, 8s, 16s, 32s, 60s, 120s,
//     240s, 480s, 960s (16min), cap 1h. Max 10 attempts → dead-
//     letter.
//
//   - Metrics + traces auto-fold into Phase 1.1a observability:
//     sky_jobs_total{queue,outcome=succeeded|failed|dlq},
//     sky_jobs_duration_seconds{queue},
//     sky_jobs_inflight{queue}, sky_jobs_queue_depth{queue}.
//
//   - Handler registration via Define(name, handler) at boot.
//     Worker looks up handler by name when dispatching. Payload
//     serialised as JSON on the wire so SQLite-backed jobs
//     survive restart.
package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"sky-app/rt/periodic"
)

// JobID identifies a queued job. Opaque to callers; backend-specific
// shape (memory: monotonic int; sqlite: rowid). Marshalled as a
// decimal string on the Sky-side.
type JobID int64

func (id JobID) String() string {
	return strconv.FormatInt(int64(id), 10)
}

// HandlerFunc is the user's job code — receives the JSON-decoded
// payload, returns error on failure (triggers retry / dead-letter
// per backoff schedule).
//
// Returning nil = success. Returning non-nil = retry; after max
// attempts, the job + error chain is moved to the dead-letter
// table.
//
// Sky-side bindings call user code; this Go-level type is the
// shape the runtime stores.
type HandlerFunc func(payload []byte) error

// JobRecord is the on-the-wire representation of a queued job.
// Identical across backends — JSON-encoded payload + minimal
// metadata. Backends serialise this struct into their storage
// (memory: in-process map; sqlite: row in `_sky_jobs` table).
type JobRecord struct {
	ID          JobID
	Queue       string
	Name        string    // handler name (registered via Define)
	Payload     []byte    // JSON-encoded user data
	Attempts    int       // 0 on first enqueue; bumps each retry
	NextRunAt   time.Time // when the worker should pick this up
	EnqueuedAt  time.Time
	LastError   string // last attempt's error message (nil on first attempt)
}

// Store is the persistence interface. Implementations:
//
//	memoryStore — sync.Mutex + slice; lost on restart
//	sqliteStore — _sky_jobs + _sky_jobs_dead tables
//	postgresStore — deferred to 1.3.x
type Store interface {
	Enqueue(rec JobRecord) (JobID, error)
	// Claim returns the next job whose NextRunAt is in the past,
	// marking it as in-flight (so concurrent workers don't double-
	// run). Returns (zero, ErrNoJob) when nothing is ready.
	Claim(queue string, now time.Time) (JobRecord, error)
	// Complete removes the job from the queue (successful run).
	Complete(id JobID) error
	// Reschedule re-queues a job for retry. Caller passes the
	// full record so backends can update in-place (SQLite UPDATE
	// by ID) OR re-insert (memory backend pops on Claim).
	// Attempts / NextRunAt / LastError on rec carry the updated
	// metadata.
	Reschedule(rec JobRecord) error
	// DeadLetter moves the job to the dead-letter table with the
	// final error.
	DeadLetter(id JobID, finalError string) error
	// Cancel removes a not-yet-started job.
	Cancel(id JobID) error
	// QueueDepth returns the count of jobs in `queue` not yet
	// completed (for metrics).
	QueueDepth(queue string) (int, error)
}

// ErrNoJob signals there's nothing to dispatch right now. Worker
// loop sleeps + retries.
var ErrNoJob = errors.New("no job ready")

// ─── Handler registry ─────────────────────────────────────────

var handlerMu sync.RWMutex
var handlers = map[string]HandlerFunc{}

// Define registers a handler under the given name. Worker looks up
// by name when dispatching a JobRecord. Called from user code at
// boot via Sky-side `Jobs.define`. Idempotent — re-registering the
// same name overwrites silently (useful for tests).
func Define(name string, h HandlerFunc) {
	if name == "" {
		return
	}
	handlerMu.Lock()
	defer handlerMu.Unlock()
	handlers[name] = h
}

// LookupHandler — used by the worker to dispatch a job. Returns
// nil + false when no handler is registered for the name; the
// worker treats this as a permanent error (no point retrying — the
// code that would handle this job doesn't exist).
func LookupHandler(name string) (HandlerFunc, bool) {
	handlerMu.RLock()
	defer handlerMu.RUnlock()
	h, ok := handlers[name]
	return h, ok
}

// ResetHandlersForTest clears the global registry. Test-only.
func ResetHandlersForTest() {
	handlerMu.Lock()
	defer handlerMu.Unlock()
	handlers = map[string]HandlerFunc{}
}

// ─── Backoff schedule ─────────────────────────────────────────

// MaxAttempts — after this many failures, move to dead-letter.
// Default 10; configurable via sky.toml [jobs] max_attempts in a
// future release.
const MaxAttempts = 10

// BackoffFor returns the duration to wait before retry attempt N
// (1-indexed: N=1 is the first retry, after the initial attempt
// failed). Exponential 2^N seconds, capped at 1h.
//
//	N=1  → 1s
//	N=2  → 2s
//	N=3  → 4s
//	...
//	N=10 → 512s
//	N=11 → 1024s
//	N=12 → 2048s (≈34min)
//	N=13+ → 3600s (cap)
//
// Plus 0-25% jitter (full-jitter rebalance pattern) so a thundering
// herd of retries from a transient DB outage spreads out instead of
// hitting the DB simultaneously on every backoff boundary.
//
// The math.rand source is deliberately the non-crypto one — jitter
// doesn't need security-grade randomness, and crypto/rand would
// add ~1μs per call which adds up under load.
func BackoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	const cap = 1 * time.Hour
	// Cap the shift count so 1<<N can't overflow int64. 2^31s is
	// ~68 years — well past our 1h cap, so any attempt past ~32
	// is permanently clamped.
	shift := uint(attempt - 1)
	if shift > 31 {
		shift = 31
	}
	exp := time.Duration(int64(1)<<shift) * time.Second
	if exp > cap || exp <= 0 { // overflow / cap guard
		exp = cap
	}
	jitterBound := int64(exp / 4)
	if jitterBound <= 0 {
		return exp
	}
	return exp + time.Duration(rand.Int63n(jitterBound))
}

// ─── Memory backend ───────────────────────────────────────────

// memoryStore is the default backend. In-process map + mutex.
// Lost on restart — fine for dev / single-process apps where
// at-least-once durability isn't required.
type memoryStore struct {
	mu     sync.Mutex
	nextID atomic.Int64
	queue  []JobRecord // sorted by NextRunAt; binary-search-free linear scan is fine at our volumes
}

// NewMemoryStore — default in-process backend.
func NewMemoryStore() Store {
	return &memoryStore{}
}

func (m *memoryStore) Enqueue(rec JobRecord) (JobID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := JobID(m.nextID.Add(1))
	rec.ID = id
	if rec.EnqueuedAt.IsZero() {
		rec.EnqueuedAt = time.Now()
	}
	if rec.NextRunAt.IsZero() {
		rec.NextRunAt = time.Now()
	}
	m.queue = append(m.queue, rec)
	return id, nil
}

func (m *memoryStore) Claim(queue string, now time.Time) (JobRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.queue {
		if r.Queue == queue && !r.NextRunAt.After(now) {
			// Remove from queue (in-flight = claimed = held by
			// worker until Complete/Reschedule/DeadLetter).
			out := r
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			return out, nil
		}
	}
	return JobRecord{}, ErrNoJob
}

func (m *memoryStore) Complete(id JobID) error {
	// Already removed at Claim. No-op for memory backend.
	return nil
}

func (m *memoryStore) Reschedule(rec JobRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Claim removed it; re-append with the worker's updated
	// Attempts / NextRunAt / LastError. ID, Queue, Name, Payload
	// preserved from the original.
	m.queue = append(m.queue, rec)
	return nil
}

func (m *memoryStore) DeadLetter(id JobID, finalError string) error {
	// Memory backend: drop the job (already removed at Claim).
	// Future enhancement: keep a bounded dead-letter ring buffer
	// for the dashboard to display.
	return nil
}

func (m *memoryStore) Cancel(id JobID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.queue {
		if r.ID == id {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			return nil
		}
	}
	return ErrJobNotFound
}

func (m *memoryStore) QueueDepth(queue string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, r := range m.queue {
		if r.Queue == queue {
			count++
		}
	}
	return count, nil
}

var ErrJobNotFound = errors.New("job not found")

// ─── Worker loop ──────────────────────────────────────────────

// Worker pulls from the store + dispatches handlers. Started once
// at runtime boot. Polls every PollInterval; sleeps that long when
// the queue is empty (could be replaced with a notification
// channel in v1.x for lower latency).
type Worker struct {
	store        Store
	queue        string
	pollInterval time.Duration
	stop         chan struct{}
	stopped      atomic.Bool
	// done is closed by run on its way out. It is the happens-before edge Stop
	// waits on; `stopped` remains only as a cheap non-blocking status probe.
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
	started   atomic.Bool

	// Metrics callbacks — injected so the worker doesn't depend
	// on the telemetry package directly (avoids import cycle:
	// jobs ← telemetry ← jobs). The runtime startup wires these
	// to actual Prometheus increments.
	OnSuccess    func(queue string, duration time.Duration)
	OnFailure    func(queue string, duration time.Duration, attempt int)
	OnDeadLetter func(queue string)
	OnInflight   func(queue string, delta int)
}

// NewWorker constructs a worker for the given queue. Caller should
// Start() it.
func NewWorker(store Store, queue string) *Worker {
	return &Worker{
		store:        store,
		queue:        queue,
		pollInterval: 100 * time.Millisecond,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
}

// Start spawns the worker goroutine. Returns immediately. Idempotent — a
// second Start would otherwise run a second loop against the same stop
// channel, and both would claim from the same queue.
func (w *Worker) Start() {
	w.startOnce.Do(func() {
		w.started.Store(true)
		go w.run()
	})
}

// Stop signals the worker to exit + waits for the in-flight dispatch to
// finish. Bounded so we don't hang past an orchestrator grace window.
//
// # What was wrong with the previous shape
//
//   - IT COULD PANIC. The guard was `if w.stopped.Load() { return }` followed
//     by `close(w.stop)`. Two goroutines that both observed `stopped == false`
//     both reached the close, and the second one panicked on a closed channel.
//
//   - IT POLLED A PROXY. `stopped` is set by a defer in `run`, so the loop
//     below was sampling a flag every 10 ms rather than waiting on an edge.
//
//   - IT GAVE UP IN SILENCE. Past the deadline the loop simply fell out, with
//     no return value and no log line, so a job abandoned mid-dispatch left no
//     trace — the fact an operator most needs after a restart.
//
// Reports whether the worker actually stopped inside the budget.
func (w *Worker) Stop(timeout time.Duration) bool {
	w.stopOnce.Do(func() { close(w.stop) })
	if !w.started.Load() {
		// Nothing was ever spawned, so `done` will never close.
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
		return true
	case <-timer.C:
		log.Printf("[sky.jobs] worker %q did not stop within %s; "+
			"an in-flight dispatch was abandoned", w.queue, timeout)
		return false
	}
}

// periodicReport routes this package's background-loop failures into the jobs
// log. periodic deliberately does no logging of its own — see that package's
// header — because rt/jobs cannot import rt and so cannot reach rt's
// structured logger. `log.Printf` with the `[sky.jobs]` tag is what this
// package's operators already read.
// No stack is logged, deliberately. Capturing one is production-gated policy
// that lives in exactly one place (rt/panic_log.go's LogRecoveredPanic), and
// rt/jobs cannot import rt — rt imports jobs. Keeping a second copy of the
// policy here is the shape of defect that file exists to close, and an
// ungated capture would put internal frames in a production log, so the panic
// value is logged without one. rt/hub, which imports rt the other way round,
// does route through LogRecoveredPanic.
func periodicReport(r periodic.Report) {
	switch {
	case r.Recovered != nil:
		log.Printf("[sky.jobs] %s: cycle panicked: %v — this job is lost, the worker continues",
			r.Loop, r.Recovered)
	case r.Err != nil:
		log.Printf("[sky.jobs] %s: %v", r.Loop, r.Err)
	}
}

// run is the claim-and-dispatch poll loop.
//
// # Why the recover is per iteration
//
// `safeHandle` already recovers the USER's handler, so the worker survived a
// panicking job. Nothing recovered the surrounding machinery: a panic in
// `Claim`, in `LookupHandler`, in an `OnInflight`/`OnSuccess` callback, or in
// the store's `Complete`/`Reschedule`/`DeadLetter` unwound straight out of
// `run`, past the loop, and killed the worker for the process lifetime. Every
// job on that queue then sat unclaimed forever with no log line — the queue
// simply stopped, and `Stop` reported a clean exit because `done` closes on
// the way out either way.
//
// periodic.Guard scopes the recover to ONE claim-and-dispatch. The job that
// panicked is lost — it stays claimed until its lease expires and is then
// redelivered, which is at-least-once behaving as designed — and the worker
// takes the next one.
func (w *Worker) run() {
	defer close(w.done)
	defer w.stopped.Store(true)
	for {
		select {
		case <-w.stop:
			return
		default:
		}
		periodic.Guard("jobs.worker."+w.queue, periodicReport, w.claimAndDispatch)
	}
}

// claimAndDispatch is ONE iteration of the worker loop: claim a job, dispatch
// it, or sleep. It returns the storage error rather than swallowing it — a
// store that has been failing every claim for an hour is a fact an operator
// needs, and `continue` after a bare backoff is how it stayed invisible.
func (w *Worker) claimAndDispatch() error {
	rec, err := w.store.Claim(w.queue, time.Now())
	if err == ErrNoJob {
		time.Sleep(w.pollInterval)
		return nil
	}
	if err != nil {
		// Storage error — backoff briefly + retry. Reported, not discarded.
		time.Sleep(w.pollInterval * 5)
		return fmt.Errorf("claiming from queue %q: %w", w.queue, err)
	}
	return w.dispatch(rec)
}

// dispatch runs one claimed job and records its outcome.
//
// # Why every store call's error is now returned
//
// These three were `_ = w.store.Complete(...)`, `_ = w.store.DeadLetter(...)`
// and `_ = w.store.Reschedule(...)`. The discarded `Complete` is the worst of
// the three by a distance: a job whose handler SUCCEEDED but whose completion
// write failed stays claimed, its lease expires, it is redelivered, it
// succeeds again, and its completion fails again. At-least-once delivery
// becomes an INFINITE redelivery loop, running the handler's side effects
// forever, and the only evidence is that the queue never drains. With the
// error discarded there was nothing to correlate that with.
//
// The callbacks still fire on a failed write, deliberately: OnSuccess records
// that the handler succeeded, which it did. What failed is the bookkeeping,
// and that is what the returned error says.
func (w *Worker) dispatch(rec JobRecord) error {
	if w.OnInflight != nil {
		w.OnInflight(rec.Queue, +1)
		defer w.OnInflight(rec.Queue, -1)
	}
	handler, ok := LookupHandler(rec.Name)
	if !ok {
		// No registered handler — permanent failure. Goes
		// straight to dead-letter (retrying won't help; the
		// missing code isn't going to appear).
		err := w.store.DeadLetter(rec.ID, "no handler registered for "+rec.Name)
		if w.OnDeadLetter != nil {
			w.OnDeadLetter(rec.Queue)
		}
		if err != nil {
			return fmt.Errorf("dead-lettering job %s (%s, no handler): %w — "+
				"it stays claimed and will be redelivered", rec.ID, rec.Name, err)
		}
		return nil
	}

	start := time.Now()
	err := safeHandle(handler, rec.Payload)
	elapsed := time.Since(start)

	if err == nil {
		completeErr := w.store.Complete(rec.ID)
		if w.OnSuccess != nil {
			w.OnSuccess(rec.Queue, elapsed)
		}
		if completeErr != nil {
			return fmt.Errorf("completing job %s (%s): %w — the handler SUCCEEDED but the "+
				"job is still claimed; it will be redelivered when its lease expires and "+
				"the handler's side effects will run again", rec.ID, rec.Name, completeErr)
		}
		return nil
	}

	attempts := rec.Attempts + 1
	if w.OnFailure != nil {
		w.OnFailure(rec.Queue, elapsed, attempts)
	}
	if attempts >= MaxAttempts {
		// Move to dead-letter with the final error chain.
		dlErr := w.store.DeadLetter(rec.ID,
			fmt.Sprintf("max attempts (%d) reached: %v",
				MaxAttempts, err))
		if w.OnDeadLetter != nil {
			w.OnDeadLetter(rec.Queue)
		}
		if dlErr != nil {
			return fmt.Errorf("dead-lettering job %s (%s) after %d attempts: %w — "+
				"it stays claimed and will be redelivered past MaxAttempts",
				rec.ID, rec.Name, attempts, dlErr)
		}
		return nil
	}
	// Retry with backoff.
	rec.Attempts = attempts
	rec.NextRunAt = time.Now().Add(BackoffFor(attempts))
	rec.LastError = err.Error()
	if rescheduleErr := w.store.Reschedule(rec); rescheduleErr != nil {
		return fmt.Errorf("rescheduling job %s (%s) for attempt %d: %w — its backoff "+
			"was not recorded", rec.ID, rec.Name, attempts, rescheduleErr)
	}
	return nil
}

// safeHandle wraps the user's handler in panic recovery — a
// panicking job mustn't kill the worker goroutine (it'd take down
// every queued job).
func safeHandle(h HandlerFunc, payload []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panic: %v", r)
		}
	}()
	return h(payload)
}

// ─── Payload helpers ──────────────────────────────────────────

// EncodePayload — JSON-marshal helper used by Sky-side enqueue.
// Centralised so the wire format stays consistent across backends.
func EncodePayload(v any) ([]byte, error) {
	return json.Marshal(v)
}

// DecodePayload — JSON-unmarshal into the target. Handlers call
// this on the payload bytes they receive.
func DecodePayload(b []byte, v any) error {
	return json.Unmarshal(b, v)
}
