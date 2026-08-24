//go:build js

package rt

import (
	"context"

	"sky-app/rt/telemetry"
)

// --- tracing / span helpers (real impls in tracing.go, //go:build !js) ---
// Under wasm there is no OTel exporter and no server request context; spans
// degrade to a direct call of the wrapped closure, and context helpers return
// a background context. This keeps the File_* / Http_* kernels in rt.go
// compilable under js (they wrap their bodies in these span helpers).

func CurrentTraceContext() context.Context { return context.Background() }

func WithFileSpan(op, path string, fn func() any) any { return fn() }

func WithHTTPClientSpan(method, url string, fn func() any) any { return fn() }

// InjectTraceHeaders is server-only (tracing.go, //go:build !js): a Sky.Spa
// client issues its HTTP via the browser `fetch` (http_wasm.go), which never
// touches a *http.Request, so no js-build code calls it and no stub is needed.

// --- pending stream / websocket handoff (real impls in server_stream.go /
// server_websocket.go, //go:build !js) ---
// A Sky.Spa client issues no server upgrades, so no handler is ever pending.

func extractPendingStreamToken(body string) (string, bool) { return "", false }

func extractPendingWebSocketToken(body string) (string, bool) { return "", false }

func takePendingStreamHandler(token string) (any, bool) { return nil, false }

func takePendingWebSocketCfg(token string) (webSocketUpgradeCfg, bool) {
	return webSocketUpgradeCfg{}, false
}

// js/wasm stubs for server-only symbols that portable core kernels (logEmit,
// Unreachable, logPanicFrame, RegisterGobType in rt.go) reference. The real
// implementations live in //go:build !js files (observability_push.go,
// panic_log.go, live_store.go) which pull net/http + database/sql and cannot
// compile under GOOS=js. Under wasm these paths are inert: a Sky.Spa client
// has no parent telemetry store, no gob session serialization, and its panic
// recovery is handled by the wasm driver.

// pushLogEntryToParent — no parent telemetry store exists under wasm.
func pushLogEntryToParent(entry telemetry.LogEntry) {}

// LogRecoveredPanic — the wasm driver owns panic recovery; log to the ring
// via the portable logEmit at error level so the message is not lost.
func LogRecoveredPanic(tag, context string, rec any) {
	logEmit(logLevelError, "error", tag+": "+context, map[string]any{"panic": rec})
}

// gobRegisterAll — gob session serialization is a server concern; no-op here.
func gobRegisterAll(v any) {}
