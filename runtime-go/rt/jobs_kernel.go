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
// What's NOT here:
//   - Web UI at /_sky/jobs (lands with the Phase 1.1b dashboard).
//   (`sky.toml [jobs]` parsing landed in v0.19.14 — see jobsBoot.)
//
// (The Postgres and SQLite backends listed here as "deferred" both exist —
// rt/jobs/postgres_store.go and rt/jobs/sqlite_store.go.)

import (
	"errors"
	"fmt"
	"log"
	"os"
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
//
// Backend selection is read via skyGetenv (honouring the `[env] prefix`):
//   SKY_JOBS_STORE       — "memory" (default) | "sqlite" | "postgres"
//   SKY_JOBS_STORE_PATH  — file path (sqlite, default "./_sky/jobs.db")
//                          or URL (postgres; falls back to DATABASE_URL)
//
// `sky.toml [jobs] store` / `storePath` seed those two, exactly as `[live]`
// seeds the session store (`rust/crates/project/src/build.rs`). Shell env still
// wins, so a deployment overrides the file without a rebuild.
//
// Until v0.19.14 that was NOT true: comments here described `[jobs]` as the
// configuration surface, and the degrade message below told operators to set
// `[jobs] store_path` — while the compiler parsed no such section and dropped
// the block on the floor. In production the app then refused to start, naming
// the key the operator had just set. Both halves are fixed: the section is
// parsed, and `docs/sky-toml.md` documents it.
//
// All three backends implement the same Store interface — the worker code
// doesn't care. Choose based on deploy shape (single-host file-backed →
// sqlite, multi-host → postgres).
func jobsBoot() {
	jobsRuntimeMu.Lock()
	defer jobsRuntimeMu.Unlock()
	if jobsRuntimeStarted {
		return
	}
	jobsRuntimeStarted = true

	jobsStore = chooseJobsStore()
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

// JobsShutdown stops the worker. Idempotent.
//
// Called from ONE place: Sky.Live's SIGTERM handler (live.go). This comment
// used to claim "Sky.Live + Sky.Http.Server (added in the next edit)"; that
// second call site was never added, so a `Sky.Http.Server` app running jobs
// does NOT drain its queue on SIGTERM — in-flight jobs are lost rather than
// completed, and their leases expire before another worker reclaims them.
// Recorded rather than silently fixed: wiring the HTTP server's shutdown path
// changes that server's termination behaviour and belongs in its own change.
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
				// `extractErrMsg`, NOT `fmt.Errorf("%v", extractErrResultValue(…))`.
				// The latter renders the Sky Error ADT's Go struct, so the only
				// durable record of a failure — `_sky_jobs.last_error` and
				// `_sky_jobs_dead.final_error` — read
				// `{0 Error [7 {the real message {1 <nil>}}]}` instead of the
				// message. `extractErrMsg` is the package's existing unwrapper
				// (stdlib_extra.go) and is what every other Err call site uses.
				return errors.New(extractErrMsg(result))
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

// jobsStoreFatalf is the fail-loud action for a jobs-store misconfiguration;
// overridable in tests so the contract can be asserted without killing the test
// binary. Mirrors `storeFatalf` in live_store.go — the session store's
// equivalent, whose contract this one had never been given.
var jobsStoreFatalf = log.Fatalf

// chooseJobsStore picks the backend implementation from SKY_JOBS_STORE +
// SKY_JOBS_STORE_PATH, which `sky.toml [jobs]` seeds (see jobsBoot).
//
// A DURABLE store that was explicitly asked for and cannot be provided is a
// hard failure in production, and a warning + memory fallback in dev.
//
// It used to degrade to an in-process memory queue on every failure path —
// unknown kind, unopenable SQLite, missing Postgres URL, unreachable Postgres —
// with nothing but a line on stderr. That converts a durability guarantee into
// RAM without the operator asking: enqueued jobs are lost on every restart and
// never shared across replicas, while the app reports healthy. It is the same
// silent-degrade class the session store closed in v0.19.4/#8
// (live_store.go:1578); the jobs store never got the treatment because nothing
// in the repo imported Std.Jobs, so this switch had no coverage at all.
//
// `memory` (and an unset kind) stay a memory store in production on purpose —
// that is the deliberate opt-in to a volatile queue, not a degradation.
func chooseJobsStore() jobs.Store {
	kind := skyGetenv("JOBS_STORE")
	if kind == "" {
		kind = "memory"
	}
	switch kind {
	case "memory":
		return jobs.NewMemoryStore()
	case "sqlite":
		path := skyGetenv("JOBS_STORE_PATH")
		if path == "" {
			path = "./_sky/jobs.db"
		}
		s, err := jobs.NewSQLiteStore(path)
		if err != nil {
			return jobsStoreDegrade("sqlite",
				fmt.Sprintf("cannot open %q: %v", path, err))
		}
		return s
	case "postgres":
		url := skyGetenv("JOBS_STORE_PATH")
		if url == "" {
			url = os.Getenv("DATABASE_URL")
		}
		if url == "" {
			// Asked for a durable shared queue and named no server: a config
			// error, not a connect failure.
			return jobsStoreDegrade("postgres",
				"no connection string (set sky.toml [jobs] storePath, SKY_JOBS_STORE_PATH, or DATABASE_URL)")
		}
		s, err := jobs.NewPostgresStore(url)
		if err != nil {
			return jobsStoreDegrade("postgres", fmt.Sprintf("connect failed: %v", err))
		}
		return s
	default:
		// A kind we do not recognise: a typo ("postgress" / "psql"), or a
		// backend named somewhere in the docs that has no branch here.
		return jobsStoreDegrade(kind, "unknown store kind — valid kinds are memory, sqlite, postgres")
	}
}

// jobsStoreDegrade is the ONE place a requested durable jobs store turns into a
// memory queue. Fatal in production, loud warning + fallback in dev.
func jobsStoreDegrade(kind, reason string) jobs.Store {
	if productionFromEnv() {
		jobsStoreFatalf("[sky.jobs] FATAL: jobs store %q unavailable — %s. Refusing to "+
			"start with a silent in-memory fallback in production (every enqueued job "+
			"would be lost on restart and never shared across replicas). Fix [jobs] "+
			"store / SKY_JOBS_STORE, or set it to \"memory\" to opt in to the "+
			"in-memory queue deliberately.", kind, reason)
		// jobsStoreFatalf is log.Fatalf in prod (never returns); a test override
		// may return, so fall through to a valid value.
	}
	log.Printf("┌─ [sky.jobs] WARNING ────────────────────────────────────────")
	log.Printf("│ jobs store %q unavailable — %s", kind, reason)
	log.Printf("│ DEV fallback → in-memory queue: jobs lost on restart, single-instance only.")
	log.Printf("│ In PRODUCTION (ENV set) this is a HARD failure — the app refuses to start.")
	log.Printf("└─────────────────────────────────────────────────────────────")
	return jobs.NewMemoryStore()
}
