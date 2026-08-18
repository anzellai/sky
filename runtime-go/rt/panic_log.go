// panic_log.go — the ONE place the Go runtime decides what a recovered
// panic is allowed to say out loud.
//
// `logPanicFrame` (Sky.Http.Server) was hardened: compact
// `method path (kind)` on stderr in production, full frame appended to
// `.skylog/panic.log`. Nothing else was. `live.go` contained zero
// occurrences of the production predicate and dumped a full goroutine
// stack from its top-level handler. Same panic, `ENV=production`:
//
//	Sky.Http.Server  "[sky.http] panic GET /checkout (*errors.errorString)"
//	Sky.Live         "[sky.live] panic handling GET /checkout: boom
//	                  goroutine 26 [running]: ..."
//
// 53 bytes against 1252, and Sky.Live is the PINNED DEFAULT app shape —
// so the leak was the common case. A Go stack names internal paths,
// package layout and frame addresses in whatever aggregator the deploy
// ships its stderr to.
//
// The rule this file exists to make checkable: `debug.Stack()` is called
// from HERE and nowhere else in the runtime
// (`rust/crates/xtask/tests/panic_stacks_are_production_gated.rs` fails
// the build on a second caller). Every recovery site calls
// `LogRecoveredPanic`; every structured-log site calls
// `panicStackForLog`.
//
// Production behaviour is not "drop the stack" — it is "move the stack":
// the full frame still lands in `.skylog/panic.log` (0600, host-rotated),
// so an operator with shell access loses nothing while the aggregated
// stream carries only the class.

package rt

import (
	"fmt"
	"os"
	"runtime/debug"
	"time"
)

// panicStackHint replaces the stack in a production log line. It names
// where the real frame went, so nobody concludes the runtime threw it
// away.
const panicStackHint = "(stack suppressed in production; full frame in .skylog/panic.log)"

// panicStackHintNoFile is used when .skylog/panic.log could not be
// written — the frame is genuinely gone, and saying so is better than
// implying a file that does not exist.
const panicStackHintNoFile = "(stack suppressed in production; .skylog/panic.log not writable)"

// capturePanicStack is the only `debug.Stack()` call in the runtime.
// Callers pass the result to LogRecoveredPanic / panicStackForLog, which
// apply the production policy.
func capturePanicStack() []byte { return debug.Stack() }

// debugStack is the legacy alias kept for the handful of call sites that
// want the raw text. Same single capture point.
func debugStack() string { return string(capturePanicStack()) }

// writePanicFrameFile appends a full panic frame to .skylog/panic.log.
// Reports whether the write succeeded, so the caller can say which of
// the two hints applies. Never fatal: losing the file must not turn a
// recovered panic into a crash.
func writePanicFrameFile(tag, context string, rec any, stack []byte) bool {
	full := fmt.Sprintf("[%s] %s %s (%T): %v\n%s\n",
		time.Now().UTC().Format(time.RFC3339), tag, context, rec, rec, stack)
	if err := os.MkdirAll(".skylog", 0o750); err != nil {
		return false
	}
	f, err := os.OpenFile(".skylog/panic.log",
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	if _, err := f.WriteString(full); err != nil {
		_ = f.Close()
		return false
	}
	return f.Close() == nil
}

// panicStackForLog returns the value to put in a structured log's stack
// field. Dev: the compressed application frames, which is what makes a
// panic debuggable from the console. Production: a hint, with the full
// frame persisted to .skylog/panic.log instead.
func panicStackForLog(tag, context string, rec any, stack []byte, maxFrames int) string {
	if !productionFromEnv() {
		return compressStack(stack, maxFrames)
	}
	if writePanicFrameFile(tag, context, rec, stack) {
		return panicStackHint
	}
	return panicStackHintNoFile
}

// LogRecoveredPanic is THE path for "a recover() caught something and it
// needs to reach stderr". Dev prints the panic and its full stack;
// production prints the class only and persists the frame.
//
// `tag` is the subsystem (`sky.live`, `sky.http`, `sky.websocket`, …);
// `context` is what was being done (`GET /checkout`, `topic="orders"`).
//
// Exported because `rt/hub` is a separate package that imports `rt` and
// must obey the same policy — a second copy of the policy is exactly the
// shape of the defect this file closes.
func LogRecoveredPanic(tag, context string, rec any) {
	logRecoveredPanicStack(tag, context, rec, capturePanicStack())
}

// logRecoveredPanicStack is the testable seam: same policy, caller-
// supplied stack.
func logRecoveredPanicStack(tag, context string, rec any, stack []byte) {
	if productionFromEnv() {
		writtenTo := ""
		if !writePanicFrameFile(tag, context, rec, stack) {
			writtenTo = " (frame not persisted: .skylog/panic.log not writable)"
		}
		fmt.Fprintf(os.Stderr, "[%s] panic %s (%T)%s\n", tag, context, rec, writtenTo)
		return
	}
	fmt.Fprintf(os.Stderr, "[%s] panic %s: %v\n%s\n", tag, context, rec, stack)
}
