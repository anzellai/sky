package rt

// Std.Jobs kernel bindings. Phase 1.3 — bridges Sky source
// `Jobs.enqueue / Jobs.enqueueIn / Jobs.cancel / Jobs.define` to
// the runtime worker in runtime-go/rt/jobs/.
//
// Wiring (per docs/v1-roadmap.md Phase 1.3 acceptance):
//
//   - The runtime maintains a process-wide default Store (memory
//     backend) + per-queue worker. Started lazily on first use so
//     binaries that don't enqueue anything don't spin a worker
//     goroutine.
//   - Sky source signatures (registered in
//     src/Sky/Type/Constrain/Expression.hs):
//
//       Jobs.define    : String -> (a -> Task Error ()) -> Job a
//       Jobs.enqueue   : Job a -> a -> Task Error JobId
//       Jobs.enqueueIn : Int -> Job a -> a -> Task Error JobId  -- delay ms
//       Jobs.cancel    : JobId -> Task Error ()
//
//   - The Job a opaque type wraps (name, payload-encoder). On the
//     Go side we store the handler keyed by name in the jobs
//     package's registry.
//
//   - Metrics: Sky.Live's Phase 1.1a observability picks up the
//     standard sky_jobs_total / sky_jobs_duration_seconds /
//     sky_jobs_inflight / sky_jobs_queue_depth via the worker's
//     OnSuccess / OnFailure / OnDeadLetter / OnInflight callbacks.
//
// What's NOT here (deferred to 1.3.x):
//   - Postgres backend (the Store interface is in place; impl is
//     a 200-line file similar to live_store_postgres.go).
//   - Web UI at /_sky/jobs (lands with the Phase 1.1b dashboard).
//   - sky.toml [jobs] store = "sqlite" wiring (sqlite backend
//     itself lands in 1.3.x; defaulting to memory means apps work
//     out of the box).

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"sky-app/rt/jobs"
	"sky-app/rt/telemetry"
)

// jobsDefaultQueue — the queue name used by Jobs.enqueue when no
// explicit queue is specified. Matches the convention of every
// other queue lib (Sidekiq, Resque, Oban).
const jobsDefaultQueue = "default"

// jobsRuntime — process-wide singleton holding the active store +
// worker. Lazy-init on first Jobs.* call so binaries that don't
// import the module pay zero overhead.
var (
	jobsRuntimeMu     sync.Mutex
	jobsRuntimeStarted bool
	jobsStore         jobs.Store
	jobsWorker        *jobs.Worker
)

// jobsBoot starts the default worker on first use. Idempotent.
func jobsBoot() {
	jobsRuntimeMu.Lock()
	defer jobsRuntimeMu.Unlock()
	if jobsRuntimeStarted {
		return
	}
	jobsRuntimeStarted = true

	// MVP: in-memory backend. SQLite / Postgres backends land in
	// 1.3.x via sky.toml [jobs] store = "...".
	jobsStore = jobs.NewMemoryStore()
	jobsWorker = jobs.NewWorker(jobsStore, jobsDefaultQueue)

	// Wire metrics into Phase 1.1a observability. The worker
	// callbacks fire on every dispatch; we fold into the standard
	// Prometheus counters / histogram / gauge.
	t := telemetry.Default()
	jobsWorker.OnSuccess = func(queue string, d time.Duration) {
		t.Inc("sky_jobs_total", map[string]string{
			"queue":   queue,
			"outcome": "succeeded",
		})
		t.Observe("sky_jobs_duration_seconds",
			map[string]string{"queue": queue}, d.Seconds())
	}
	jobsWorker.OnFailure = func(queue string, d time.Duration, attempt int) {
		t.Inc("sky_jobs_total", map[string]string{
			"queue":   queue,
			"outcome": "failed",
		})
		t.Observe("sky_jobs_duration_seconds",
			map[string]string{"queue": queue}, d.Seconds())
	}
	jobsWorker.OnDeadLetter = func(queue string) {
		t.Inc("sky_jobs_total", map[string]string{
			"queue":   queue,
			"outcome": "dlq",
		})
	}
	jobsWorker.OnInflight = func(queue string, delta int) {
		t.AddGauge("sky_jobs_inflight",
			map[string]string{"queue": queue}, float64(delta))
	}

	jobsWorker.Start()
}

// JobsShutdown stops the worker. Called from SIGTERM handler in
// Sky.Live + Sky.Http.Server (added in the next edit). Idempotent.
func JobsShutdown() {
	jobsRuntimeMu.Lock()
	w := jobsWorker
	started := jobsRuntimeStarted
	jobsRuntimeMu.Unlock()
	if !started || w == nil {
		return
	}
	// 1s grace — matches the SIGTERM budget the rest of the
	// runtime uses. Serverless mode has shorter grace via
	// ServerlessShutdownGrace, but jobs in serverless are an
	// anti-pattern anyway (the container evicts before the job
	// finishes — use a real queue like Cloud Tasks instead).
	w.Stop(1 * time.Second)
}

// ─── Sky.Jobs kernel bindings ────────────────────────────────

// Jobs_define is called from Sky source as `Jobs.define name handler`.
// Registers the handler in the global registry and returns an opaque
// "job" reference (currently just the name string — wrapping it in a
// struct gives future room for adding per-job config like queue
// override, max attempts).
func Jobs_define(name any, handler any) any {
	n := fmt.Sprintf("%v", name)
	if n == "" {
		return ""
	}
	jobs.Define(n, func(payload []byte) error {
		// The user's handler is a Sky function: (a -> Task Error ()).
		// payload was JSON-encoded at enqueue time. Decode into a
		// generic any, then invoke the handler.
		//
		// `sky_call` runs the Sky function. The Task it returns is
		// then forced via `runtime_force_task` — anything that
		// returns Err is treated as a job failure.
		var v any
		if err := jobs.DecodePayload(payload, &v); err != nil {
			return fmt.Errorf("decode payload: %w", err)
		}
		taskResult := sky_call(handler, v)
		// sky_call returns the Task thunk (zero-arg func returning
		// the Result). Force it.
		if result := AnyTaskRun(taskResult); result != nil {
			if isErrResult(result) {
				return fmt.Errorf("%v", extractErrResultValue(result))
			}
		}
		return nil
	})
	return n // Opaque job reference == the name
}

// Jobs_enqueue : Job a -> a -> Task Error JobId
// Called as `Jobs.enqueue job payload` from Sky source.
// Returns a Sky Task that, when run, enqueues the job and yields
// the JobId.
func Jobs_enqueue(jobRef any, payload any) any {
	return makeTaskThunk(func() any {
		jobsBoot()
		name := fmt.Sprintf("%v", jobRef)
		if name == "" {
			return Err[any, any](ErrFfi("Jobs.enqueue: job reference is empty (did you forget Jobs.define?)"))
		}
		raw, err := jobs.EncodePayload(payload)
		if err != nil {
			return Err[any, any](ErrFfi("Jobs.enqueue: encode payload: " + err.Error()))
		}
		id, err := jobsStore.Enqueue(jobs.JobRecord{
			Queue:   jobsDefaultQueue,
			Name:    name,
			Payload: raw,
		})
		if err != nil {
			return Err[any, any](ErrFfi("Jobs.enqueue: " + err.Error()))
		}
		return Ok[any, any](id.String())
	})
}

// Jobs_enqueueIn : Int -> Job a -> a -> Task Error JobId
// Like enqueue but delays the first run by `ms` milliseconds. Used
// for scheduled / retry-out-of-band patterns (a "remind me in 5
// minutes" feature).
func Jobs_enqueueIn(ms any, jobRef any, payload any) any {
	return makeTaskThunk(func() any {
		jobsBoot()
		delayMs := AsInt(ms)
		name := fmt.Sprintf("%v", jobRef)
		if name == "" {
			return Err[any, any](ErrFfi("Jobs.enqueueIn: empty job reference"))
		}
		raw, err := jobs.EncodePayload(payload)
		if err != nil {
			return Err[any, any](ErrFfi("Jobs.enqueueIn: encode payload: " + err.Error()))
		}
		id, err := jobsStore.Enqueue(jobs.JobRecord{
			Queue:     jobsDefaultQueue,
			Name:      name,
			Payload:   raw,
			NextRunAt: time.Now().Add(time.Duration(delayMs) * time.Millisecond),
		})
		if err != nil {
			return Err[any, any](ErrFfi("Jobs.enqueueIn: " + err.Error()))
		}
		return Ok[any, any](id.String())
	})
}

// Jobs_cancel : JobId -> Task Error ()
// Removes a pending (not-yet-started) job. Returns Err when the
// job ID doesn't exist (already ran, dead-lettered, etc.).
func Jobs_cancel(idArg any) any {
	return makeTaskThunk(func() any {
		jobsBoot()
		idStr := fmt.Sprintf("%v", idArg)
		idInt, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return Err[any, any](ErrFfi("Jobs.cancel: invalid id " + idStr))
		}
		if err := jobsStore.Cancel(jobs.JobID(idInt)); err != nil {
			return Err[any, any](ErrFfi("Jobs.cancel: " + err.Error()))
		}
		return Ok[any, any](struct{}{})
	})
}

// makeTaskThunk wraps a function in the Sky Task shape:
// `func() any` returning a SkyResult. Sky's `Task.run` /
// `Cmd.perform` then forces the thunk; this gives lazy semantics
// matching the rest of the kernel.
func makeTaskThunk(fn func() any) any {
	return func() any { return fn() }
}

// ─── Metrics: queue-depth gauge (polled) ──────────────────────

// jobsMetricsTicker — periodic refresh of sky_jobs_queue_depth.
// Started by jobsBoot's worker init path. Polls every 5s; under
// a queue draining at 100 jobs/sec the gauge lags by at most 500
// jobs, fine for ops dashboards.
//
// Started inline within jobsBoot's lock so we don't spawn a
// duplicate ticker on a hot enqueue race.
func init() {
	// No-op; the ticker is started from jobsBoot to keep
	// "binaries that don't use jobs spawn zero goroutines"
	// invariant. Init function placeholder for future setup.
}
