// Package rt — hub-store bridge for the bundled console.
//
// v0.16.4 Option B B4. When the bundled console (sky-bundled/console)
// runs INSIDE the hub daemon process (sky console-serve), it reads
// telemetry directly from the hub's SQLite hot store instead of
// fetching `/_sky/console/api/*` JSON over loopback. This file
// exposes the Sky-callable `Hub_*` kernels that route to a
// pre-registered `HubStoreReader`; the registration happens in
// `runtime-go/rt/hub/hub.go` Run() right after the store opens.
//
// Why a Reader interface instead of importing `rt/hub` here?
// `rt/hub` already imports `rt/` (for Log_*, telemetry, etc.). A
// reverse import would form a cycle. The interface lives here so
// the only direction is `hub → rt`; the implementation in
// `rt/hub/bridge.go` wraps `*hub.Store` to satisfy it.
//
// Return shapes match the Sky-side typed records declared in
// `sky-bundled/console/src/State.sky`:
//
//   - Hub_readOverview   → State_Overview_R   (single record)
//   - Hub_readLogs       → []State_LogEntry_R
//   - Hub_readMetrics    → []State_MetricRow_R
//   - Hub_readTraces     → []State_TraceRow_R
//   - Hub_readErrors     → []State_ErrorRow_R
//   - Hub_listServices   → []string
//
// Each kernel returns a `func() any` Task closure that yields
// `Ok[any, any](result)` on success or `Err[any, any](err)` on
// failure. Sky's typed lowering then narrows the inner `any` to
// the declared record/list type via `rt.Coerce` →
// `narrowMapToStruct` at the call site (no extra cast needed
// here).
//
// When no reader is registered (embedded console / hub still
// initialising / unit tests), each kernel resolves with empty
// payloads instead of an error — matches the embedded-mode
// fallback in `Main.sky`'s `httpStore` and keeps the UI alive
// during the first second of hub boot.
package rt

import (
	"encoding/json"
	"sync"
)

// HubStoreReader is the bridge interface between the bundled
// console's Sky code and the hub's SQLite store. All methods take
// and return JSON strings to keep this package agnostic of the
// concrete `hub.Store` types (which import `rt`).
//
// JSON-at-the-boundary trades a marshal/unmarshal cycle per call
// for a clean dependency direction. The cost is negligible (the
// queries themselves dominate); the win is no package cycle.
type HubStoreReader interface {
	// Counts returns (logs, metrics, spans) row counts across the
	// whole store. Used to populate the Overview tab's "buffer
	// used" counters.
	Counts() (logs, metrics, spans int, err error)

	// QueryLogsJSON takes a JSON-encoded LogFilter and returns a
	// JSON-encoded []LogEntry payload mirroring the wire shape the
	// embedded-mode `/_sky/console/api/logs` endpoint produces.
	QueryLogsJSON(filterJSON string) (string, error)

	// QueryMetricsJSON returns the JSON-encoded []MetricRow
	// (camelCase fields matching State.MetricRow).
	QueryMetricsJSON() (string, error)

	// QuerySpansJSON returns the JSON-encoded []TraceRow.
	QuerySpansJSON() (string, error)

	// QueryErrorsJSON returns the JSON-encoded []ErrorRow
	// (aggregated bad-status logs/metrics; v0.16.4 implementation
	// derives this from QueryLogsJSON filtered by level=error).
	QueryErrorsJSON() (string, error)

	// Services returns the distinct service_name values currently
	// in the store.
	Services() ([]string, error)
}

var (
	hubStoreMu     sync.RWMutex
	hubStoreReader HubStoreReader
)

// SetHubStore registers the global hub-store reader. Called once
// by the hub at startup (see `runtime-go/rt/hub/hub.go` Run).
// Idempotent: a second call replaces the previous reader (useful
// for tests that swap fixtures).
func SetHubStore(r HubStoreReader) {
	hubStoreMu.Lock()
	hubStoreReader = r
	hubStoreMu.Unlock()
}

// getHubStore returns the registered reader or nil. Read under a
// brief RLock so concurrent SetHubStore calls don't tear.
func getHubStore() HubStoreReader {
	hubStoreMu.RLock()
	defer hubStoreMu.RUnlock()
	return hubStoreReader
}

// Hub_readOverview implements:
//
//	HubStore.hubReadOverview : String -> Task Error Overview
//
// `_dbPathArg` is reserved for the multi-store future (one hub
// process serving multiple databases — not in v0.16.4). The
// current reader is process-global.
func Hub_readOverview(_dbPathArg any) any {
	return func() any {
		r := getHubStore()
		if r == nil {
			return Ok[any, any](emptyHubOverview())
		}
		logs, metrics, spans, err := r.Counts()
		if err != nil {
			return Err[any, any](ErrFfi("hub.readOverview: " + err.Error()))
		}
		ov := emptyHubOverview()
		ov["bufferLogUsed"] = logs
		ov["bufferTraceUsed"] = spans
		ov["requestsTotal"] = logs + metrics + spans
		return Ok[any, any](ov)
	}
}

// emptyHubOverview returns a default Overview record (lowerCamel
// keys matching `sky-bundled/console/src/State.sky`'s
// `type alias Overview`). `narrowMapToStruct` accepts lower-first
// keys at the rt.Coerce boundary, so the caller's
// `rt.Coerce[State_Overview_R]` will narrow cleanly.
func emptyHubOverview() map[string]any {
	return map[string]any{
		"skyVersion":      "hub",
		"commit":          "",
		"builtAt":         "",
		"uptimeSeconds":   0,
		"requestsTotal":   0,
		"errorRate5xx":    0.0,
		"bufferLogUsed":   0,
		"bufferTraceUsed": 0,
		"productionMode":  false,
	}
}

// Hub_readLogs implements:
//
//	HubStore.hubReadLogs : String -> LogFilter -> Task Error (List LogEntry)
func Hub_readLogs(_dbPathArg, filterArg any) any {
	return func() any {
		r := getHubStore()
		if r == nil {
			return Ok[any, any]([]any{})
		}
		// Forward the filter as JSON so the hub-side bridge can
		// translate to its `hub.LogFilter` shape without dragging
		// `hub.LogFilter` into rt's interface.
		filterJSON := encodeFilterJSON(filterArg)
		out, err := r.QueryLogsJSON(filterJSON)
		if err != nil {
			return Err[any, any](ErrFfi("hub.readLogs: " + err.Error()))
		}
		rows, err := decodeRowsJSON(out)
		if err != nil {
			return Err[any, any](ErrFfi("hub.readLogs: decode: " + err.Error()))
		}
		return Ok[any, any](rows)
	}
}

// Hub_readMetrics implements:
//
//	HubStore.hubReadMetrics : String -> Task Error (List MetricRow)
func Hub_readMetrics(_dbPathArg any) any {
	return func() any {
		r := getHubStore()
		if r == nil {
			return Ok[any, any]([]any{})
		}
		out, err := r.QueryMetricsJSON()
		if err != nil {
			return Err[any, any](ErrFfi("hub.readMetrics: " + err.Error()))
		}
		rows, err := decodeRowsJSON(out)
		if err != nil {
			return Err[any, any](ErrFfi("hub.readMetrics: decode: " + err.Error()))
		}
		return Ok[any, any](rows)
	}
}

// Hub_readTraces implements:
//
//	HubStore.hubReadTraces : String -> Task Error (List TraceRow)
func Hub_readTraces(_dbPathArg any) any {
	return func() any {
		r := getHubStore()
		if r == nil {
			return Ok[any, any]([]any{})
		}
		out, err := r.QuerySpansJSON()
		if err != nil {
			return Err[any, any](ErrFfi("hub.readTraces: " + err.Error()))
		}
		rows, err := decodeRowsJSON(out)
		if err != nil {
			return Err[any, any](ErrFfi("hub.readTraces: decode: " + err.Error()))
		}
		return Ok[any, any](rows)
	}
}

// Hub_readErrors implements:
//
//	HubStore.hubReadErrors : String -> Task Error (List ErrorRow)
func Hub_readErrors(_dbPathArg any) any {
	return func() any {
		r := getHubStore()
		if r == nil {
			return Ok[any, any]([]any{})
		}
		out, err := r.QueryErrorsJSON()
		if err != nil {
			return Err[any, any](ErrFfi("hub.readErrors: " + err.Error()))
		}
		rows, err := decodeRowsJSON(out)
		if err != nil {
			return Err[any, any](ErrFfi("hub.readErrors: decode: " + err.Error()))
		}
		return Ok[any, any](rows)
	}
}

// Hub_listServices implements:
//
//	HubStore.hubListServices : String -> Task Error (List String)
func Hub_listServices(_dbPathArg any) any {
	return func() any {
		r := getHubStore()
		if r == nil {
			return Ok[any, any]([]any{})
		}
		svcs, err := r.Services()
		if err != nil {
			return Err[any, any](ErrFfi("hub.listServices: " + err.Error()))
		}
		out := make([]any, len(svcs))
		for i, s := range svcs {
			out[i] = s
		}
		return Ok[any, any](out)
	}
}

// encodeFilterJSON converts the Sky-side LogFilter record (which
// arrives as a Go struct value via typed lowering OR as a
// map[string]any from the dynamic path) to a JSON string. Failures
// degrade to an empty filter — better than blocking the UI.
func encodeFilterJSON(filterArg any) string {
	if filterArg == nil {
		return "{}"
	}
	// Pull fields via the same accessor path the rest of the
	// runtime uses (recordField handles both struct and map shapes).
	out := map[string]any{
		"query":     hubStringField(filterArg, "Query", "query"),
		"session":   hubStringField(filterArg, "Session", "session"),
		"showDebug": hubBoolField(filterArg, "ShowDebug", "showDebug"),
		"showInfo":  hubBoolField(filterArg, "ShowInfo", "showInfo"),
		"showWarn":  hubBoolField(filterArg, "ShowWarn", "showWarn"),
		"showError": hubBoolField(filterArg, "ShowError", "showError"),
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// hubStringField pulls a string-typed field from a Sky record value.
// Tries the runtime's recordField under both Pascal + camel keys —
// matches the narrowMapToStruct probe order so typed structs +
// map-shape values both work.
func hubStringField(v any, pascal, camel string) string {
	raw := recordField(v, pascal, camel)
	if raw == nil {
		return ""
	}
	if s, ok := raw.(string); ok {
		return s
	}
	return ""
}

func hubBoolField(v any, pascal, camel string) bool {
	raw := recordField(v, pascal, camel)
	if raw == nil {
		return false
	}
	if b, ok := raw.(bool); ok {
		return b
	}
	return false
}

// decodeRowsJSON parses a JSON array of objects into []any. Each
// element is `map[string]any` which Sky's rt.Coerce narrows to the
// per-row typed struct via narrowMapToStruct.
func decodeRowsJSON(raw string) ([]any, error) {
	if raw == "" || raw == "null" {
		return []any{}, nil
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil, err
	}
	out := make([]any, len(arr))
	for i, row := range arr {
		out[i] = row
	}
	return out, nil
}
