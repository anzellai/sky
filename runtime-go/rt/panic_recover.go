// panic_recover.go — top-level Sky main() panic→Err recovery.
//
// Cycle 6 PC (v0.15.43) closes the synchronous-panic class. Today
// only the FFI boundary (runWithRecover in rt.go:3249) recovers
// Go panics into typed Err. Sky's main = … emits Go's func main()
// calling the user's task directly, so a panic in the synchronous
// path (`1 // 0`, `rt.AsInt` on bad value, comparison type-mismatch
// from a heterogeneous slice etc.) crashes the process with a
// Go stack dump.
//
// Sky.Http.Server handlers already recover per-request; Cmd.perform
// goroutines have rt.SafeGo. The remaining un-recovered surface is:
// Sky.Cli, Sky.Tui (synchronous main loop), batch jobs, scheduled
// tasks — every `main = Task.run …` shape that's not server-y.
//
// Mechanism: codegen injects `defer rt.LogPanicAndExit()` as the
// FIRST statement of Go's `func main()`. The deferred call's
// recover() catches whatever escaped the synchronous path; structured
// log + exit(1). Errors that are already Sky `Err` propagate normally
// (no panic, no recover invocation, normal exit).
//
// Production gate: panic-class messages explain the most likely Sky-
// side cause (div-by-zero / type mismatch / nil deref / oob) and
// include a 4-char errId matching CLAUDE.md's two-level error
// pattern. JSON-format output if SKY_LOG_FORMAT=json is set, matching
// Std.Log shape. Go stack tail compressed to top 8 frames.

package rt

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

// LogPanicAndExit is intended for `defer rt.LogPanicAndExit()` at
// the start of Sky's emitted func main(). When the deferred call
// fires, recover() catches whatever escaped the synchronous path,
// produces a structured Error log line with an errId, and exits
// with code 1.
//
// Nothing escaped → no-op, normal main exit path runs (return code 0).
func LogPanicAndExit() {
	r := recover()
	if r == nil {
		return
	}
	emitPanicLog(r, debug.Stack())
	os.Exit(1)
}

// emitPanicLog is the testable seam: takes the recovered value and
// a (mockable) stack trace, writes the structured log line.
// Honours SKY_LOG_FORMAT=json when set.
func emitPanicLog(r any, stack []byte) {
	errId := newErrId()
	rawMsg := fmt.Sprintf("%v", r)
	kind, hint := classifyPanic(rawMsg)
	stackTail := compressStack(stack, 8)

	ctx := map[string]any{
		"errId":      errId,
		"panicKind":  kind,
		"panicMsg":   rawMsg,
		"hint":       hint,
		"stackFrame": stackTail,
	}
	logEmit(logLevelError, "error",
		"Sky panic: "+kind+" (ref "+errId+") — "+hint,
		ctx)
}

// classifyPanic inspects the panic message to bucket it into a
// known class (so users get a useful hint, not just the raw Go
// runtime message). Defaults to "unexpected" for anything we don't
// recognise — still produces a structured-log line with the raw
// message, so users can grep.
func classifyPanic(msg string) (kind, hint string) {
	switch {
	case strings.Contains(msg, "rt.IntDiv: integer division by zero"),
		strings.Contains(msg, "rt.Rem: modulo by zero"),
		strings.Contains(msg, "rt.Div: division by zero"):
		return "DivisionByZero",
			"Integer or float division by zero. Guard the divisor with `if d == 0 then Err … else Ok (n // d)` before the operation."
	case strings.Contains(msg, "rt.AsInt: expected numeric"),
		strings.Contains(msg, "rt.AsFloat: expected numeric"),
		strings.Contains(msg, "rt.AsBool: expected bool"),
		strings.Contains(msg, "rt.skyCallDirect: argument"):
		return "TypeMismatch",
			"A value flowed into a numeric/boolean position with the wrong runtime type — usually from a heterogeneous list or untyped FFI return. Check the type at the source."
	case strings.Contains(msg, "rt.Coerce: expected"),
		strings.Contains(msg, "rt.coerceInner: type mismatch"):
		return "CoerceFailure",
			"Typed-codegen routing tried to narrow a value to the wrong shape. If reproducible from valid Sky code, this is a compiler bug — please report with the offending source."
	case strings.Contains(msg, "rt.cmp: type mismatch"):
		return "ComparisonMismatch",
			"Comparison operator (`<`, `>`, `<=`, `>=`) applied across incompatible types. Sky requires both sides to be the same primitive (Int, Float, String, …)."
	case strings.Contains(msg, "runtime error: index out of range"):
		return "IndexOutOfRange",
			"List/array index out of bounds. Prefer `List.head`/`List.get` which return Maybe, or bounds-check before indexing."
	case strings.Contains(msg, "runtime error: invalid memory address"),
		strings.Contains(msg, "runtime error: nil"):
		return "NilDereference",
			"Tried to use a nil FFI value as if it were initialised. Most Go FFI bindings return `*T` — check for nil before using."
	case strings.Contains(msg, "sky.Unreachable"),
		strings.Contains(msg, "Ffi.kernel"):
		return "CompilerBug",
			"Unreachable code path or unrewritten Ffi.kernel sentinel reached at runtime. This is a compiler bug — please file an issue."
	default:
		return "Unexpected",
			"An unrecognised runtime error occurred. The raw message is preserved in panicMsg; check the stackFrame for the failing call site."
	}
}

// compressStack returns a short tail of the Go stack — top N frames
// after stripping the runtime/debug.Stack and panic_recover frames
// (which are noise for the user). Keeps the log line readable.
func compressStack(stack []byte, maxFrames int) string {
	lines := strings.Split(string(stack), "\n")
	var frames []string
	// Skip the goroutine header + the rt.LogPanicAndExit /
	// runtime.gopanic / debug.Stack frames at the top.
	skip := true
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if skip {
			if strings.Contains(ln, "panic(") || strings.Contains(ln, "rt.LogPanicAndExit") ||
				strings.Contains(ln, "rt.emitPanicLog") || strings.Contains(ln, "rt.compressStack") ||
				strings.Contains(ln, "runtime.gopanic") || strings.Contains(ln, "runtime/debug") ||
				strings.HasPrefix(ln, "goroutine ") {
				continue
			}
			skip = false
		}
		frames = append(frames, ln)
		if len(frames) >= maxFrames*2 { // 2 lines per frame (func + file:line)
			break
		}
	}
	if len(frames) == 0 {
		return "(no application frames)"
	}
	return strings.Join(frames, " | ")
}

// newErrId returns a 4-byte hex correlation ID (8 chars). Matches
// the shape of CLAUDE.md's two-level error pattern. Uses crypto/rand
// for entropy so a flood of panics doesn't collide.
func newErrId() string {
	b := make([]byte, 4)
	if _, err := cryptorand.Read(b); err != nil {
		// Fallback: timestamp-derived. The pattern is "ref XXXXXXXX"
		// — even a non-unique value is useful for grep correlation
		// of one log line against another. We do NOT want to fail
		// the panic-recover path because entropy reads failed.
		return "00000000"
	}
	return hex.EncodeToString(b)
}
