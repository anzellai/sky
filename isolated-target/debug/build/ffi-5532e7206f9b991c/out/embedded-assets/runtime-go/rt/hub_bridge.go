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
	"strings"
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

	// ServiceStatsJSON returns a JSON-encoded []ServiceStat payload
	// (camelCase keys matching `State.ServiceStat` in
	// `sky-bundled/console/src/State.sky`):
	//
	//   [ { name, status, reqsPerSec, p95Ms, errorRate,
	//       sparkRps : [Float...], sparkP95 : [Float...] }, ... ]
	//
	// Aggregation is the hub-side responsibility — the bridge in
	// `runtime-go/rt/hub/bridge.go` derives req/s + p95 + error
	// rate over the last 60 s window per service, and emits 30
	// 2-second buckets for each sparkline.
	ServiceStatsJSON() (string, error)

	// QueryFilteredLogsJSON / QueryFilteredMetricsJSON /
	// QueryFilteredSpansJSON / QueryFilteredErrorsJSON (v0.16.4 B6)
	// — per-service drill-down variants of the readers above.
	// `serviceName == ""` means "all services" (no filter — the
	// store-side WHERE clause is omitted).
	//
	// Wire shape matches the single-service variants exactly so the
	// Sky-side typed record narrowing (rt.Coerce →
	// narrowMapToStruct) is reused.
	QueryFilteredLogsJSON(serviceName, filterJSON string) (string, error)
	QueryFilteredMetricsJSON(serviceName string) (string, error)
	QueryFilteredSpansJSON(serviceName string) (string, error)
	QueryFilteredErrorsJSON(serviceName string) (string, error)
}

// HubStoreReaderWithTenant is the v0.16.6 #493 part 2c-defense
// extension of HubStoreReader.  When the live session carries a
// tenant claim, the Hub_readFiltered* kernels prefer these variants
// — the SQL layer applies `AND service_name LIKE prefix || '%'` so
// the SQLite engine (not the caller) enforces the row scope.
//
// Optional by design: legacy readers + test fakes that only satisfy
// HubStoreReader continue to work; the kernel layer type-asserts
// for the optional interface and falls through to the un-scoped
// path when absent (no tenant claim) or when the fake doesn't
// implement it (still rejected at the kernel layer if a tenant
// claim is present).
type HubStoreReaderWithTenant interface {
	QueryFilteredLogsJSONWithTenant(serviceName, tenantPrefix, filterJSON string) (string, error)
	QueryFilteredMetricsJSONWithTenant(serviceName, tenantPrefix string) (string, error)
	QueryFilteredSpansJSONWithTenant(serviceName, tenantPrefix string) (string, error)
	QueryFilteredErrorsJSONWithTenant(serviceName, tenantPrefix string) (string, error)
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

// Hub_currentIdentity implements:
//
//	Hub.currentIdentity : String -> Task Error Identity
//
// v0.16.5 #493. Returns the authenticated `Std.Live.Console.Identity`
// stashed on the current liveSession at mint time by dispatchRoot
// (from IdentityFromContext(r.Context()) written by the auth gate).
//
// `_dbPathArg` is reserved for the multi-store future — same shape
// as other Hub_* kernels for consistency.
//
// Failure modes:
//   - No live session in scope (CLI / unit test) → Err with explicit
//     "no live session" message. Caller decides whether to treat as
//     anonymous or fatal.
//   - Live session exists but identityValid is false (gate didn't
//     write, auth=off path) → Err with "no identity in session".
//     The bundled console treats this as "anonymous read all".
//   - Identity present → Ok with the typed record matching
//     `Std.Live.Console.Identity`'s field shape.
func Hub_currentIdentity(_dbPathArg any) any {
	return func() any {
		sess := currentLiveSession()
		if sess == nil {
			return Err[any, any](ErrFfi("hub.currentIdentity: no live session in scope"))
		}
		id, ok := SessionIdentity(sess)
		if !ok {
			return Err[any, any](ErrFfi("hub.currentIdentity: no identity in session (auth=off or gate didn't run)"))
		}
		// Match Std.Live.Console.Identity's record shape exactly so
		// rt.Coerce[Std_Live_Console_Identity_R] narrows cleanly on
		// the Sky side. Claims is a Dict String String — the runtime
		// already round-trips map[string]string via the existing Dict
		// kernel infrastructure.
		claims := id.Claims
		if claims == nil {
			claims = map[string]string{}
		}
		out := map[string]any{
			"subject": id.Subject,
			"email":   id.Email,
			"claims":  dictFromStringMap(claims),
		}
		return Ok[any, any](out)
	}
}

// dictFromStringMap converts a Go map[string]string to the rt
// representation of `Dict String String` — built on top of the
// existing Dict_empty / Dict_insert primitives. Local to hub_bridge.go
// because no other kernel currently constructs a Dict; promote to a
// shared helper if a second caller appears.
func dictFromStringMap(m map[string]string) any {
	d := Dict_empty()
	for k, v := range m {
		d = Dict_insert(k, v, d)
	}
	return d
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

// Hub_readServiceStats implements:
//
//	HubStore.hubReadServiceStats : String -> Task Error (List ServiceStat)
//
// Delegates to the reader's ServiceStatsJSON which aggregates the
// last 60 s of telemetry per service into req/s + p95 + error-rate
// + sparkline buckets.  The bridge returns one JSON row per
// distinct service_name; an empty store → empty list (no error).
func Hub_readServiceStats(_dbPathArg any) any {
	return func() any {
		r := getHubStore()
		if r == nil {
			return Ok[any, any]([]any{})
		}
		out, err := r.ServiceStatsJSON()
		if err != nil {
			return Err[any, any](ErrFfi("hub.readServiceStats: " + err.Error()))
		}
		rows, err := decodeServiceStatRows(out)
		if err != nil {
			return Err[any, any](ErrFfi("hub.readServiceStats: decode: " + err.Error()))
		}
		return Ok[any, any](rows)
	}
}

// decodeServiceStatRows parses the JSON array emitted by
// ServiceStatsJSON into a []any whose entries are map[string]any
// shapes that rt.Coerce narrows to State_ServiceStat_R via
// narrowMapToStruct.  The `sparkRps` / `sparkP95` arrays are
// preserved as `[]any` of float64 — rt.AsListT[float64] handles
// the per-element coerce at the typed slot.
func decodeServiceStatRows(raw string) ([]any, error) {
	if raw == "" || raw == "null" {
		return []any{}, nil
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil, err
	}
	out := make([]any, len(arr))
	for i, row := range arr {
		// JSON unmarshal hands us []interface{} for the spark
		// fields. narrowMapToStruct + rt.AsListT[float64] expects
		// []any, which is the same underlying type — no conversion
		// needed.  Pre-emptively flatten nested int → float to
		// match the Float-typed slot on the Sky side.
		if s, ok := row["sparkRps"].([]any); ok {
			row["sparkRps"] = coerceFloatList(s)
		}
		if s, ok := row["sparkP95"].([]any); ok {
			row["sparkP95"] = coerceFloatList(s)
		}
		out[i] = row
	}
	return out, nil
}

// coerceFloatList walks a heterogeneous slice and forces every
// element to float64.  JSON-unmarshalling gives us float64 for
// every numeric, but an upstream encoder change (e.g. integer
// sample counts) could surface as json.Number; this keeps the
// downstream rt.AsListT[float64] happy without panicking.
func coerceFloatList(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		switch x := v.(type) {
		case float64:
			out[i] = x
		case float32:
			out[i] = float64(x)
		case int:
			out[i] = float64(x)
		case int64:
			out[i] = float64(x)
		default:
			out[i] = 0.0
		}
	}
	return out
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

// hubStringArg coerces a Sky-side argument (typically a `String`
// passed through the typed kernel dispatch) into a Go `string`.
// Returns "" for `nil` or non-string values so callers can treat
// an empty service-name as "no filter".
func hubStringArg(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// tenantPrefixForSession returns the tenant prefix derived from the
// current goroutine's live-session identity, or "" when none is in
// scope.  Used by every Hub_readFiltered* kernel as the
// defense-in-depth gate: even if the bundled console mis-threads
// the prefix, the kernel always applies the session's claim.
//
// v0.16.6 #493 part 2c-defense.  Convention matches Std.Auth /
// SkyDeploy: the tenant identifier lives on `claims["tenant"]`.
func tenantPrefixForSession() string {
	sess := currentLiveSession()
	if sess == nil {
		return ""
	}
	id, ok := SessionIdentity(sess)
	if !ok || id.Claims == nil {
		return ""
	}
	return id.Claims["tenant"]
}

// rejectCrossTenantSvc enforces that an explicit service-name
// argument is scoped within the caller's tenant.  Returns ("", true)
// when the caller didn't pick a specific service (svc == "") so
// the kernel can rely on the tenant prefix alone; returns (svc,
// true) when svc starts with the tenant prefix (in-scope), and ("",
// false) otherwise — the caller treats false as "refuse the query
// with an Err that says cross-tenant".
//
// When the session has NO tenant claim, every svc is in-scope; the
// caller passes through unchanged.
func rejectCrossTenantSvc(svc, tenantPrefix string) (string, bool) {
	if tenantPrefix == "" {
		return svc, true
	}
	if svc == "" {
		return "", true
	}
	if strings.HasPrefix(svc, tenantPrefix) {
		return svc, true
	}
	return "", false
}

// Hub_readFilteredLogs implements:
//
//	HubStore.hubReadFilteredLogs : String -> String -> LogFilter -> Task Error (List LogEntry)
//
// The first arg is the unused dbPath (multi-store future), the
// second is the service name to filter by, the third is the
// LogFilter record. An empty service name means "no filter".
func Hub_readFilteredLogs(_dbPathArg, serviceArg, filterArg any) any {
	return func() any {
		r := getHubStore()
		if r == nil {
			return Ok[any, any]([]any{})
		}
		svc := hubStringArg(serviceArg)
		tenant := tenantPrefixForSession()
		effectiveSvc, ok := rejectCrossTenantSvc(svc, tenant)
		if !ok {
			return Err[any, any](ErrFfi("hub.readFilteredLogs: service outside tenant scope"))
		}
		filterJSON := encodeFilterJSON(filterArg)
		out, err := readFilteredLogsRouted(r, effectiveSvc, tenant, filterJSON)
		if err != nil {
			return Err[any, any](ErrFfi("hub.readFilteredLogs: " + err.Error()))
		}
		rows, err := decodeRowsJSON(out)
		if err != nil {
			return Err[any, any](ErrFfi("hub.readFilteredLogs: decode: " + err.Error()))
		}
		return Ok[any, any](rows)
	}
}

// readFilteredLogsRouted prefers the tenant-aware
// HubStoreReaderWithTenant API; falls through to the legacy reader
// when the runtime hasn't been upgraded (typically test fakes that
// only satisfy the v0.16.4 interface).
func readFilteredLogsRouted(r HubStoreReader, svc, tenant, filterJSON string) (string, error) {
	if tenant != "" {
		if t, ok := r.(HubStoreReaderWithTenant); ok {
			return t.QueryFilteredLogsJSONWithTenant(svc, tenant, filterJSON)
		}
	}
	return r.QueryFilteredLogsJSON(svc, filterJSON)
}

// Hub_readFilteredMetrics implements:
//
//	HubStore.hubReadFilteredMetrics : String -> String -> Task Error (List MetricRow)
func Hub_readFilteredMetrics(_dbPathArg, serviceArg any) any {
	return func() any {
		r := getHubStore()
		if r == nil {
			return Ok[any, any]([]any{})
		}
		svc := hubStringArg(serviceArg)
		tenant := tenantPrefixForSession()
		effectiveSvc, ok := rejectCrossTenantSvc(svc, tenant)
		if !ok {
			return Err[any, any](ErrFfi("hub.readFilteredMetrics: service outside tenant scope"))
		}
		var (
			out string
			err error
		)
		if tenant != "" {
			if t, tok := r.(HubStoreReaderWithTenant); tok {
				out, err = t.QueryFilteredMetricsJSONWithTenant(effectiveSvc, tenant)
			} else {
				out, err = r.QueryFilteredMetricsJSON(effectiveSvc)
			}
		} else {
			out, err = r.QueryFilteredMetricsJSON(effectiveSvc)
		}
		if err != nil {
			return Err[any, any](ErrFfi("hub.readFilteredMetrics: " + err.Error()))
		}
		rows, err := decodeRowsJSON(out)
		if err != nil {
			return Err[any, any](ErrFfi("hub.readFilteredMetrics: decode: " + err.Error()))
		}
		return Ok[any, any](rows)
	}
}

// Hub_readFilteredTraces implements:
//
//	HubStore.hubReadFilteredTraces : String -> String -> Task Error (List TraceRow)
func Hub_readFilteredTraces(_dbPathArg, serviceArg any) any {
	return func() any {
		r := getHubStore()
		if r == nil {
			return Ok[any, any]([]any{})
		}
		svc := hubStringArg(serviceArg)
		tenant := tenantPrefixForSession()
		effectiveSvc, ok := rejectCrossTenantSvc(svc, tenant)
		if !ok {
			return Err[any, any](ErrFfi("hub.readFilteredTraces: service outside tenant scope"))
		}
		var (
			out string
			err error
		)
		if tenant != "" {
			if t, tok := r.(HubStoreReaderWithTenant); tok {
				out, err = t.QueryFilteredSpansJSONWithTenant(effectiveSvc, tenant)
			} else {
				out, err = r.QueryFilteredSpansJSON(effectiveSvc)
			}
		} else {
			out, err = r.QueryFilteredSpansJSON(effectiveSvc)
		}
		if err != nil {
			return Err[any, any](ErrFfi("hub.readFilteredTraces: " + err.Error()))
		}
		rows, err := decodeRowsJSON(out)
		if err != nil {
			return Err[any, any](ErrFfi("hub.readFilteredTraces: decode: " + err.Error()))
		}
		return Ok[any, any](rows)
	}
}

// Hub_readFilteredErrors implements:
//
//	HubStore.hubReadFilteredErrors : String -> String -> Task Error (List ErrorRow)
func Hub_readFilteredErrors(_dbPathArg, serviceArg any) any {
	return func() any {
		r := getHubStore()
		if r == nil {
			return Ok[any, any]([]any{})
		}
		svc := hubStringArg(serviceArg)
		tenant := tenantPrefixForSession()
		effectiveSvc, ok := rejectCrossTenantSvc(svc, tenant)
		if !ok {
			return Err[any, any](ErrFfi("hub.readFilteredErrors: service outside tenant scope"))
		}
		var (
			out string
			err error
		)
		if tenant != "" {
			if t, tok := r.(HubStoreReaderWithTenant); tok {
				out, err = t.QueryFilteredErrorsJSONWithTenant(effectiveSvc, tenant)
			} else {
				out, err = r.QueryFilteredErrorsJSON(effectiveSvc)
			}
		} else {
			out, err = r.QueryFilteredErrorsJSON(effectiveSvc)
		}
		if err != nil {
			return Err[any, any](ErrFfi("hub.readFilteredErrors: " + err.Error()))
		}
		rows, err := decodeRowsJSON(out)
		if err != nil {
			return Err[any, any](ErrFfi("hub.readFilteredErrors: decode: " + err.Error()))
		}
		return Ok[any, any](rows)
	}
}
