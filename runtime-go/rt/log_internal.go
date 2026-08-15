// log_internal.go — how the RUNTIME logs about itself.
//
// `Log_warn` and friends are Sky KERNELS. They return a `Task` — in the emitted
// Go, a `func() any` the runtime forces when the Sky program runs it. That is
// correct for Sky code and wrong for Go code inside `rt`: written as a bare
// statement, `Log_warn("…")` constructs the thunk and drops it. It compiles, it
// reads exactly like a log line, and it emits nothing.
//
// Every internal warning in the runtime was written that way. The operator-
// facing diagnostics for an ignored SQLite pool knob, an unparseable duration,
// an UNLIMITED connection pool, an unrecognised isolation level and a
// non-replayable retry budget were all dead — and each of those sites FALLS
// BACK to a default, so the warning was the only thing that said the operator's
// configuration had not been used. A knob that looks set and does nothing,
// silently, is the failure mode `sky.toml`'s unknown-key warning exists to
// prevent.
//
// So rt logs about itself through these helpers, which call `logEmit` directly.
// `Sky.Build`'s `TestNoRuntimeSourceDropsALogKernelThunk` fails the build on a
// `Log_*` kernel call in statement position, so the shape cannot come back.
package rt

// rtWarn emits a warning FROM THE RUNTIME, immediately.
//
// Not `Log_warn`, which returns a Task the caller would have to force. Same
// level, same destinations (the telemetry ring, the console's Logs tab, the
// configured stdout/stderr writer), no thunk.
func rtWarn(msg string) {
	logEmit(logLevelWarn, "warn", msg, nil)
}

// rtInfo is rtWarn at info level, for the same reason.
func rtInfo(msg string) {
	logEmit(logLevelInfo, "info", msg, nil)
}
