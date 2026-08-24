//go:build !js

package rt

// Garbage-collector defaults derived from the machine the app is running on.
//
// # Why this is a default and not a documented knob
//
// `docs/perf/runs/gogc-postgres-20260816/` measured 34 arms on the PostgreSQL
// session store with `fsync=on`. `GOGC=400` under a `GOMEMLIMIT` of 750MiB was
// worth **+19% throughput at 759 MB peak RSS** at 500 concurrent sessions
// (`combo-n500-gogc400-750MiB`: 3,344.6 int/s / 758,512 kB, against
// `n500-gogc100-b1`/`b2` at 2,793–2,838 int/s / ~411,000 kB).
//
// A 19% lever a user only gets by reading the performance corpus is not an
// optimisation the product is choosing to leave on the table; it is a default
// set wrongly. The same run also shows why the lever cannot simply be handed
// over as advice: `GOGC=800` alone peaked at **1,827 MB**, ~96% of an
// e2-small's app budget, and two identical n=300 arms read 1,148 MB and
// 1,926 MB — a 68% spread against ≤6% at `GOGC ≤ 400`. An operator cannot
// provision against that, and the failure it produces is an OOM kill, which
// cannot be debugged from outside the box.
//
// So: the multiplier and the bound ship together, both derived, neither
// configured.
//
// # Why the bound is what makes the multiplier shippable
//
// `GOGC` is a multiplier on the live heap, so it does not raise a baseline and
// leave the per-session slope alone — it scales the slope with it, by **4.4×**
// across `GOGC` 100 → 800 in that run. (The ratio rather than the two absolute
// figures: the run's own kB/session numbers are computed with a factor of 1000
// where 1024 belongs, which cancels in a ratio and does not in a level.) A bare
// `GOGC` default therefore has no ceiling at any session count. `GOMEMLIMIT` supplies
// the ceiling, and the run measured it as free: adding a 750MiB limit to
// `GOGC=400` moved throughput 3,314 → 3,345 int/s (inside noise) while cutting
// peak RSS 31%, from 1,093 MB to 759 MB.
//
// # Why `GOGC` stays a multiplier rather than `off`
//
// `GOGC=off` plus a limit is the wrong shape: with no multiplier the collector
// only runs as the limit approaches, so the process spends its entire budget
// regardless of load — `gml-n100-gogcoff-750MiB` used **776 MB at n=100** where
// the combined arm used 390 MB for more throughput at n=500. Keeping the
// multiplier means the app takes what it needs and stops.
//
// # The soft-limit failure mode, which is the point of the floor below
//
// `GOMEMLIMIT` is SOFT. If the live heap genuinely exceeds it, Go exceeds the
// limit rather than killing the process, and the runtime's GC CPU limiter caps
// collector CPU at 50% so the outcome is a slower process rather than a death
// spiral. That is the behaviour we want when the bound is wrong — but it also
// means a limit set below the app's real working set buys nothing and costs
// throughput permanently. Hence `gcMinMemoryLimitBytes`, and hence the rule
// that a machine too small to afford the bound does not get the multiplier
// either.

import (
	"os"
	"runtime/debug"
	"strconv"
)

const (
	// gcHeapPercent is the multiplier the run measured. 400, not 800: at
	// n=500 `GOGC=800` bought a further 5 points of throughput for 734 MB
	// more peak RSS and an unprovisionable 68% run-to-run spread.
	gcHeapPercent = 400

	// gcOSReserveBytes is the operating system's share of a VM, from the
	// sizing table in AGENTS.md ("Minimal Linux ~250 MB"). Rounded to 256MiB.
	//
	// It is a FIXED subtraction rather than a percentage on purpose: the OS
	// does not get smaller when the instance does, so a flat fraction of RAM
	// systematically over-claims on exactly the small machines where the
	// over-claim is fatal.
	gcOSReserveBytes = 256 * mb

	// gcPostgresWorkingSetBytes is what an embedded cluster costs BEYOND its
	// `shared_buffers` — the postmaster, its auxiliaries and the per-backend
	// memory of the handful of connections the app actually holds. AGENTS.md
	// measures the base at ~36 MB and backends at 5–10 MB each, ~40–70 MB at
	// 6–10 active; `gogc-postgres-20260816/results.tsv` reads `pg_peak_kb`
	// 82,256–82,400 in every one of its 40 arms. 96MiB covers the measured
	// figure with room.
	gcPostgresWorkingSetBytes = 96 * mb

	// gcAppShareNumerator / gcAppShareDenominator is the app's share of what
	// is left after the OS and the database have been paid.
	//
	// Three quarters, and the remaining quarter is not spare — it is the
	// allowance for the three things a `GOMEMLIMIT` does not cover: the
	// soft limit's own overshoot, memory in the process that is not the Go
	// heap (the binary's text, cgo, TLS buffers), and RSS sitting above the
	// limit because pages have not been returned to the OS yet.
	gcAppShareNumerator   = 3
	gcAppShareDenominator = 4

	// gcMinMemoryLimitBytes is the point below which the derivation declines
	// to act at all.
	//
	// Calibrated against measurement, not taste: at n=100 on the postgres
	// store the STOCK collector already peaks at 138–145 MB
	// (`gogc-postgres-20260816/results.tsv`, `n100-gogc100-b1`/`b2`). A limit
	// under that is not a bound, it is a treadmill — the collector running
	// continuously against a target it cannot reach. 256MiB is ~1.8× the
	// stock peak, so a limit we set can never bind below the footprint the
	// app has without us.
	gcMinMemoryLimitBytes = 256 * mb
)

// gcEnvironment is everything outside the machine's size that changes the
// answer. Passed in rather than read, for the reason `tuningFor` takes a
// `machine`: a function that reads the ambient process can only be tested on
// the process the test happens to run in.
type gcEnvironment struct {
	// gogc / gomemlimit are the operator's own settings, verbatim from the
	// environment. Non-empty means "the operator has decided", and an
	// operator who has sized this knows more about the deployment than any
	// heuristic can.
	gogc       string
	gomemlimit string

	// embeddedPostgres is true when this process also supervises its own
	// PostgreSQL, which is claiming a share of the same RAM.
	embeddedPostgres bool

	// serverless is true on a request-billed platform.
	serverless bool
}

// gcTuning is the decision. Both halves are optional and independent, because
// an operator who sets one has not thereby declined the other.
type gcTuning struct {
	setMemoryLimit   bool
	memoryLimitBytes int64
	setGCPercent     bool
	gcPercent        int
	// reason is one line an operator can read when diagnosing an OOM or a
	// throughput surprise. It is populated in EVERY branch, including the
	// branches that decide to do nothing — "we declined, and here is why" is
	// the harder thing to find out from outside the process.
	reason string
}

// gcTuningFor is the whole decision, pure.
func gcTuningFor(m machine, env gcEnvironment) gcTuning {
	wantLimit := env.gomemlimit == ""
	wantPercent := env.gogc == ""

	switch {
	case !wantLimit && !wantPercent:
		return gcTuning{reason: "GOMEMLIMIT " + env.gomemlimit + ", GOGC " + env.gogc +
			" — both set by you, sky derived nothing"}
	case m.ramBytes == 0:
		// 0 is a real answer from detectRAMBytes, and it is handled the same
		// way pg_embed_conf handles it: do not guess large. Guessing large on
		// a machine whose size is unknown is how a limited container gets an
		// OOM kill.
		return gcTuning{reason: "machine memory could not be detected; left on the Go defaults"}
	}

	limit, ok := gcMemoryLimitFor(m.ramBytes, env)
	if !ok {
		// Below the floor nothing is derived — not the bound, and not the
		// multiplier either, even when the operator has supplied a bound of
		// their own. A machine with no room for four times the live heap does
		// not acquire it by someone else naming the ceiling.
		under := " (" + humanRAM(m.ramBytes) + " is under the " +
			humanRAM(gcMinMemoryLimitBytes) + " floor)"
		if !wantLimit {
			return gcTuning{reason: "GOMEMLIMIT " + env.gomemlimit +
				" set by you, GOGC at the Go default" + under}
		}
		return gcTuning{reason: "Go defaults" + under}
	}

	out := gcTuning{}

	if wantLimit {
		out.setMemoryLimit = true
		out.memoryLimitBytes = int64(limit)
		out.reason = "GOMEMLIMIT " + humanRAM(limit)
	} else {
		out.reason = "GOMEMLIMIT " + env.gomemlimit + " set by you"
	}

	// The multiplier is withheld on serverless. The +19% is a property of a
	// long-lived, session-holding process; a request-billed container has a
	// HARD, platform-enforced ceiling where the soft limit's overshoot is a
	// killed instance rather than a slower one, and asking for four times the
	// live heap against that is the wrong trade. The bound is still taken,
	// because at the stock multiplier it measured free
	// (`gml-n500-gogc100-750MiB`: 2,839.8 int/s against 2,838.5 unbounded).
	switch {
	case env.serverless:
		out.reason += ", GOGC left at the Go default on a request-billed platform"
	case wantPercent:
		out.setGCPercent = true
		out.gcPercent = gcHeapPercent
		out.reason += ", GOGC " + strconv.Itoa(gcHeapPercent)
	default:
		out.reason += ", GOGC " + env.gogc + " set by you"
	}

	out.reason += " — from " + humanRAM(m.ramBytes) + " detected" + gcReserveNote(env)
	return out
}

// gcMemoryLimitFor is the arithmetic: pay the OS, pay the database, take three
// quarters of what is left. Reports false when the result is not worth setting.
func gcMemoryLimitFor(ram uint64, env gcEnvironment) (uint64, bool) {
	budget := ram

	// A request-billed container's memory limit IS the app's allocation — the
	// platform's operating system lives outside what the container is charged
	// for — so subtracting a host OS reserve there would under-size the app
	// for nothing.
	if !env.serverless {
		if budget <= gcOSReserveBytes {
			return 0, false
		}
		budget -= gcOSReserveBytes
	}

	if reserve := gcPostgresReserveFor(ram, env); reserve > 0 {
		if budget <= reserve {
			return 0, false
		}
		budget -= reserve
	}

	limit := budget / gcAppShareDenominator * gcAppShareNumerator
	if limit < gcMinMemoryLimitBytes {
		return 0, false
	}
	return limit, true
}

// gcPostgresReserveFor is what an embedded cluster is going to take out of this
// machine, so the app does not also claim it.
//
// The `shared_buffers` term calls `pgSharedBuffersFor` — the SAME function
// `tuningFor` renders into `postgresql.conf` — rather than restating 15%. That
// is the coupling that keeps the two provisioners from each sizing themselves
// as though they own the box, and it is what
// `TestTheAppAndPostgresDoNotEachClaimTheWholeMachine` checks by parsing the
// number back out of the rendered conf.
func gcPostgresReserveFor(ram uint64, env gcEnvironment) uint64 {
	if !env.embeddedPostgres {
		return 0
	}
	return pgSharedBuffersFor(ram) + gcPostgresWorkingSetBytes
}

func gcReserveNote(env gcEnvironment) string {
	switch {
	case env.serverless && env.embeddedPostgres:
		return ", less embedded PostgreSQL"
	case env.serverless:
		return ""
	case env.embeddedPostgres:
		return ", less the OS and embedded PostgreSQL"
	default:
		return ", less the OS"
	}
}

// gcEnvironmentFromAmbient is the impure half: the one place the real process
// is read.
func gcEnvironmentFromAmbient() gcEnvironment {
	return gcEnvironment{
		// GOGC and GOMEMLIMIT are Go's OWN variables, not sky-prefixed ones.
		// That is deliberate and it is the argument against a `sky.toml` knob:
		// the escape hatch already exists, every Go operator already knows it,
		// and it is the one that also works when the process is launched by
		// something that never reads sky.toml.
		gogc:             os.Getenv("GOGC"),
		gomemlimit:       os.Getenv("GOMEMLIMIT"),
		embeddedPostgres: embedRequested(os.Args, osEnv),
		serverless:       IsServerless(),
	}
}

// applyGCTuning performs the decision. Idempotent.
func applyGCTuning(t gcTuning) {
	if t.setMemoryLimit {
		debug.SetMemoryLimit(t.memoryLimitBytes)
	}
	if t.setGCPercent {
		debug.SetGCPercent(t.gcPercent)
	}
}

// gcStartupDecision is what init derived, kept so the startup report can state
// it. It is recorded rather than printed here on purpose: printing at package
// init would put the line above every other startup fact and, on a one-shot
// CLI, on stderr that somebody else is reading. `startup_report.go` prints it
// as one line of the block a server emits when it comes up, which is where an
// operator diagnosing an OOM is already looking.
var gcStartupDecision gcTuning

// init derives and applies the GC defaults, once, before main.
//
// Package initialisation is the right slot for the same reason `profile.go`
// uses it: it runs in every app shape — Sky.Live, Sky.Http.Server, Sky.Cli,
// Sky.Tui, Sky.Webview — and it runs before any allocation-heavy work,
// including before `MaybeStartEmbeddedPostgres` forks the postmaster.
//
// It runs after `dotenv.go`'s init (the go command presents files in filename
// order, and `dotenv` sorts before `gc_tuning`), so a `GOGC` written into a
// project's `.env` is honoured here. That is a bonus rather than a
// requirement: Go reads its own `GOGC`/`GOMEMLIMIT` from the real environment
// before any Go code runs at all, so a `.env` value never reaches the runtime
// by itself — this is the only path by which it has any effect.
func init() {
	gcStartupDecision = gcTuningFor(detectMachine(), gcEnvironmentFromAmbient())
	applyGCTuning(gcStartupDecision)
}
