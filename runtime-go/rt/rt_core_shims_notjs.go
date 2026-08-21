//go:build !js

package rt

import "sky-app/rt/telemetry"

// pushLogEntryToParent forwards a log entry to the parent process's telemetry
// store via the sub-app push exporter, when one is active. Server-only: the
// push exporter (observability_push.go) pulls net/http and is //go:build !js.
// The js/wasm counterpart in rt_core_shims_js.go is a no-op.
func pushLogEntryToParent(entry telemetry.LogEntry) {
	if exp := ActivePushExporter(); exp != nil {
		exp.PushLog(entry)
	}
}
