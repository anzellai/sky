// Package periodic is the one shape every background loop in the runtime uses
// to survive a panic and to refuse to discard an error.
//
// # The defect class this package exists to close
//
// A periodic background goroutine written the obvious way carries one or both
// of two faults, and both fail SILENTLY and PERMANENTLY:
//
//	go func() {
//	    defer func() { _ = recover() }()   // (1) recover at the TOP LEVEL
//	    t := time.NewTicker(6 * time.Hour)
//	    for range t.C {
//	        _, _ = db.Exec(`DELETE ...`)    // (2) the error is DISCARDED
//	    }
//	}()
//
//  1. The recover is scoped to the GOROUTINE, not to the cycle. A panic
//     anywhere inside the work unwinds PAST the ticker loop, the deferred
//     recover swallows it, and the goroutine returns. The loop is then dead
//     for the whole process lifetime — with no log line, no metric, and no
//     symptom until whatever the loop was maintaining has grown without bound
//     for a day. It looks defensive and is the exact opposite: without the
//     recover the process would at least have crashed loudly.
//
//  2. The error is discarded, so a permissions failure, a lock timeout and a
//     dropped table are indistinguishable from a successful zero-row delete.
//     A loop that has never once done its job looks identical to a healthy
//     one.
//
// Both were live in this runtime. `analytics_store.go` had (1) and (2);
// `telemetry/persist.go` had (2)'s mirror image — it checked its error and had
// no recover at all. Each was missing exactly what the other had, which is why
// they are fixed through a shared mechanism rather than one patch each: seven
// independent patches leave the eighth site to the next audit.
//
// # The rule
//
// The recover is scoped to the unit of work you are willing to lose. For a
// periodic loop that unit is ONE CYCLE: the cycle is lost, it is reported, and
// the next tick tries again. It is never the goroutine.
//
// # Layering — why this is its own package
//
// `rt/telemetry`, `rt/hub` and `rt/jobs` cannot import `rt` (rt imports them;
// Go forbids the cycle), and all four packages own loops in this class. This
// package therefore depends on NOTHING but the standard library, so every one
// of them can reach it. It deliberately does no logging of its own: each
// caller supplies a Reporter that routes into whatever that package's
// operators actually read — `logStructured` in rt, the telemetry log ring in
// rt/telemetry, `log.Printf` in rt/hub and rt/jobs.
package periodic

import (
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"time"
)

// Report is one thing that went wrong in one cycle of one loop.
//
// Exactly one of Panic and Err is set. Panic carries the recovered value with
// the stack that produced it; Err carries what Work returned.
type Report struct {
	// Loop names the loop, for the log line. Free-form, but it should be
	// greppable back to the call site — "live.time-every", "hub.pruner".
	Loop string
	// Panic is the value recover() returned, non-nil when the cycle panicked.
	Panic any
	// Stack is the stack at the point of recovery. Set with Panic.
	Stack []byte
	// Err is what Work returned, non-nil when the cycle failed without
	// panicking.
	Err error
}

// String renders a Report as one operator-readable line. Reporters that route
// into a structured sink usually want the fields instead.
func (r Report) String() string {
	switch {
	case r.Panic != nil:
		return fmt.Sprintf("%s: cycle panicked: %v — this cycle is lost, the loop continues", r.Loop, r.Panic)
	case r.Err != nil:
		return fmt.Sprintf("%s: cycle failed: %v", r.Loop, r.Err)
	default:
		return r.Loop + ": cycle reported nothing"
	}
}

// Reporter receives every panic and every error the loop produces.
//
// A nil Reporter does NOT mean "discard" — that is the defect. It falls back
// to stderr via the standard logger, which is loud, ugly, and impossible to
// mistake for working. Pass a real one.
type Reporter func(Report)

func (r Reporter) emit(rep Report) {
	if r == nil {
		log.Printf("[sky.periodic] %s (no Reporter was supplied to this loop)", rep)
		return
	}
	r(rep)
}

// Guard runs ONE cycle of work with a recover scoped to that cycle, and
// reports whatever the cycle produced — a panic or a returned error.
//
// This is the primitive. Use it directly in loops whose shape is not a plain
// ticker: the telemetry drainer's four-case select, the jobs worker's claim
// poll. Use Every when the loop really is "do this every N".
//
// Guard never panics: a panic from `work` is recovered and reported, and a
// panic from `report` itself is recovered and dumped to stderr, because a
// reporting bug must not be able to kill the loop it was added to protect.
func Guard(loop string, report Reporter, work func() error) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			// The reporter runs inside its own recover. A Reporter that
			// panics — a nil map write in a log adapter, say — would
			// otherwise re-panic out of THIS deferred function, past the
			// caller's loop, and reproduce the exact defect this package
			// exists to close.
			func() {
				defer func() {
					if rr := recover(); rr != nil {
						log.Printf("[sky.periodic] %s: Reporter panicked (%v) while reporting a cycle panic (%v)\n%s",
							loop, rr, r, stack)
					}
				}()
				report.emit(Report{Loop: loop, Panic: r, Stack: stack})
			}()
		}
	}()
	if work == nil {
		report.emit(Report{Loop: loop, Err: errors.New("periodic.Guard called with nil work")})
		return
	}
	if err := work(); err != nil {
		report.emit(Report{Loop: loop, Err: err})
	}
}

// Config describes one periodic loop.
type Config struct {
	// Name identifies the loop in every Report it produces. Required.
	Name string
	// Interval is the tick period. Must be > 0.
	Interval time.Duration
	// Stop ends the loop when it closes. A nil channel blocks forever, which
	// is the correct spelling of "this loop runs for the process lifetime".
	Stop <-chan struct{}
	// AlsoStop is a second exit channel, for the loops that genuinely have
	// two — Time.every exits on both the per-dispatch `cancelSub` and the
	// session-wide `done`. Also nil-safe.
	//
	// Two named fields rather than a slice: a slice needs reflect.Select,
	// which allocates on every tick, and Time.every ticks once per interval
	// per live session. No site in the runtime needs a third.
	AlsoStop <-chan struct{}
	// Report receives panics and errors. See Reporter — nil is loud, not
	// silent.
	Report Reporter
	// Work is one cycle. Returning an error is how a cycle says it failed;
	// the error is reported, never discarded. `now` is the tick time.
	Work func(now time.Time) error
}

// Every runs cfg.Work on a fixed-period ticker until one of the stop channels
// closes, recovering and reporting PER CYCLE.
//
// It is a fixed-period ticker (time.Ticker), not a work-then-sleep timer, so
// the period does not drift with the cost of the work — Time.every's
// user-visible timing depends on that.
//
// Every returns only when a stop channel closes or the configuration is
// unusable. It does not return because a cycle failed; that is the whole
// point.
func Every(cfg Config) {
	// A misconfigured loop must be LOUD. Returning quietly here would produce
	// a goroutine that never runs and never says so — the same silence, at
	// startup instead of at the first panic.
	if cfg.Interval <= 0 {
		cfg.Report.emit(Report{Loop: cfg.Name, Err: fmt.Errorf(
			"periodic.Every: interval %v is not positive — this loop will never run", cfg.Interval)})
		return
	}
	if cfg.Work == nil {
		cfg.Report.emit(Report{Loop: cfg.Name, Err: errors.New(
			"periodic.Every: nil Work — this loop will never do anything")})
		return
	}
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-cfg.Stop:
			return
		case <-cfg.AlsoStop:
			return
		case now := <-t.C:
			Guard(cfg.Name, cfg.Report, func() error { return cfg.Work(now) })
		}
	}
}
