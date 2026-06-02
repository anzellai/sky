package rt

// Phase 1.1b — /_sky/console dashboard.
//
// A pre-built monitoring UI mounted on every Sky binary by default.
// Reads from the Hot-tier telemetry store + the runtime's other
// observable state (sessions, jobs, OTel spans). Implemented as a
// plain Go HTTP handler instead of a Sky.Live app because:
//
//   - Every binary must serve this regardless of whether the user
//     imported anything UI-related; bundling a Sky-source app would
//     bloat the codegen path.
//   - The UI is small + read-only — TEA / reactivity gives nothing
//     here, just plain fetch-poll-every-1s.
//   - Removing the Sky-source dependency means the dashboard is
//     identical across every Sky version (no surprise UI changes
//     when the user upgrades).
//
// Tabs (MVP — five of the RFC's ten):
//
//   /_sky/console                       — index HTML shell
//   /_sky/console/api/overview          — req/sec, error rate, sessions
//   /_sky/console/api/metrics-summary   — Prometheus snapshot, parsed
//   /_sky/console/api/logs              — recent log ring entries
//   /_sky/console/api/traces            — recent trace spans
//   /_sky/console/api/errors            — ranked distinct error logs
//
// Tabs deferred to v1.x: Live Sessions, Msg Flow, Routes, DB, FFI,
// Jobs (Jobs depends on Phase 1.3 metrics integration which now
// exists — could land at any point).
//
// Auth (same gate as /_sky/metrics):
//   - Production mode (env=production OR binding to non-loopback)
//     → require SKY_METRICS_TOKEN bearer (the existing admin-auth
//     hook from observability.go). Returns 401 + WWW-Authenticate.
//   - Dev mode → open.
//
// Serverless mode → returns 503 + "dashboard requires always-on CPU"
// hint. The 1Hz polling loop the dashboard runs would burn the
// per-request billing window with zero user value (container evicts
// before the user can interact).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"sky-app/rt/telemetry"
)

// MountConsoleEndpoints wires the console routes onto a ServeMux.
// Called from MountObservabilityEndpoints when the dashboard is
// enabled (default ON; opt out via SKY_OBSERVABILITY_DISABLED=1 or
// SKY_CONSOLE_DISABLED=1 for "metrics yes, dashboard no" deploys).
//
// v0.16.0: the legacy hand-written HTML shell is no longer the
// canonical mount — `MountEmbeddedConsole` (in this same file) is
// the new entry point and is called BEFORE this one from the
// Sky.Live / Sky.Http.Server boot path. When the inline console
// successfully mounts at `/_sky/console`, calling `safeMount` here
// for the same pattern would panic (Go's ServeMux rejects duplicate
// registrations); `safeMount`'s internal guard handles that — the
// HTML shell registration silently no-ops when the pattern is
// already claimed. The JSON API endpoints below are MORE specific
// patterns (Go ServeMux longest-prefix-match), so they coexist with
// the inline mount and serve fresh telemetry to its polling loop.
func MountConsoleEndpoints(mux *http.ServeMux) {
	if skyGetenv("CONSOLE_DISABLED") == "1" {
		return
	}
	// Always attempt to register the legacy HTML shell as a fallback.
	// `safeMount`'s sync.Map guard turns a duplicate registration
	// (when MountEmbeddedConsole already claimed the path) into a
	// no-op. Inline-console-unavailable binaries (i.e. apps that
	// somehow didn't link console_app) still get a working dashboard.
	safeMount(mux, "/_sky/console", HandleConsole)
	safeMount(mux, "/_sky/console/api/overview", HandleConsoleOverview)
	safeMount(mux, "/_sky/console/api/metrics-summary", HandleConsoleMetricsSummary)
	safeMount(mux, "/_sky/console/api/logs", HandleConsoleLogs)
	safeMount(mux, "/_sky/console/api/traces", HandleConsoleTraces)
	safeMount(mux, "/_sky/console/api/errors", HandleConsoleErrors)
}

// MountEmbeddedConsole wires the inline Std.Ui-rendered Sky Console
// onto `mux` at `/_sky/console`. Replaces the v0.15.x subprocess +
// reverse-proxy path entirely.
//
// v0.16.0 contract:
//   - Dev-mode (`productionFromEnv()` returns false): always mount.
//     Dev banner remains visible; same-origin link works.
//   - Production mode: mount ONLY when an admin token is configured
//     via `SKY_ADMIN_TOKEN` / `SKY_METRICS_TOKEN` /
//     `SKY_CONSOLE_TOKEN_SECRET`. Without one, the console stays
//     offline so we don't expose telemetry to anonymous reads.
//   - Skip entirely when running AS a sub-app (legacy mode —
//     `SKY_LIVE_BASE_PATH` set); a sub-app shouldn't host its own
//     console.
//
// Auth: PR 2 keeps the existing `consoleAccessAllowed` gate (lives
// in the JSON API handlers below). PR 3 replaces it with the new
// `SKY_CONSOLE_AUTH=token|app|off` env-driven gate. The inline
// path is intentionally permissive in PR 2 — it serves the static
// HTML shell to dev users without auth, identical to the v0.15.x
// dev-mode subprocess.
//
// The function logs (best-effort) to stderr on outcome:
//   - "inline console mounted at /_sky/console"
//   - "inline console skipped (production + no auth secret)"
//   - "inline console unavailable: <ErrInlineConsoleUnavailable>"
//     when the host binary failed to link `sky-app/rt/console_app`
//     (the compiler emits the blank import; missing means a build
//     that's been hand-edited away from the canonical codegen).
func MountEmbeddedConsole(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	// Sub-app mode (legacy SKY_LIVE_BASE_PATH carries a non-empty
	// prefix): never auto-mount a console inside ourselves. This
	// duplicates the v0.15.x maybeAutoMountConsole guard so apps
	// transitioning from the old runtime don't gain an unexpected
	// sub-mount.
	if base := os.Getenv("SKY_LIVE_BASE_PATH"); base != "" {
		return
	}
	if v := os.Getenv("SKY_CONSOLE_EMBED"); v == "off" || v == "0" || v == "false" {
		return
	}
	// Production gate: require an admin token, otherwise stay
	// silent — exposing logs/traces/metrics to anonymous reads in
	// production is the bug the legacy subprocess auth gate was
	// added to close. PR 3 generalises this via SKY_CONSOLE_AUTH.
	if productionFromEnv() && adminTokenSecret() == "" {
		fmt.Fprintln(os.Stderr, "[sky.console] inline console skipped (production mode requires SKY_ADMIN_TOKEN; see docs/v0.16.x-console/EMBEDDED.md)")
		return
	}
	// Initialise the ingest token even though the inline mount
	// doesn't use it directly — keeps observability federation
	// (push exporter from foreign sub-apps, if any) functional.
	IngestTokenInit()
	if err := MountInlineConsole(mux, ""); err != nil {
		// ErrInlineConsoleUnavailable means the user's app didn't
		// link sky-app/rt/console_app — fall back to the legacy
		// HTML shell (mounted by MountConsoleEndpoints below).
		// Log loudly because this typically means a manually-edited
		// main.go has lost the blank import the compiler emits.
		fmt.Fprintf(os.Stderr, "[sky.console] inline console unavailable: %v; falling back to legacy HTML shell\n", err)
		return
	}
	fmt.Fprintln(os.Stderr, "[sky.console] inline console mounted at /_sky/console")
}

// HandleConsole serves the dashboard's HTML shell — a static
// single-page app that polls the JSON API endpoints below every
// 1s. Bundled as a raw string constant; no template engine
// because there's nothing to template.
func HandleConsole(w http.ResponseWriter, r *http.Request) {
	if !consoleAccessAllowed(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store") // always fresh
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(consoleHTML))
}

// consoleAccessAllowed implements the auth gate. Returns true when
// the request may proceed; false when a response (401 / 503) has
// been written.
func consoleAccessAllowed(w http.ResponseWriter, r *http.Request) bool {
	if IsServerless() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"unavailable","hint":"dashboard requires always-on CPU; use OTLP push to your observability vendor instead (configure OTEL_EXPORTER_OTLP_ENDPOINT)"}`))
		return false
	}
	if isProductionMode() && !hasAdminAuth(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="sky-console"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"status":"unauthorized","hint":"set SKY_METRICS_TOKEN and pass via Authorization: Bearer <token>"}`))
		return false
	}
	return true
}

// ─── API handlers (JSON) ──────────────────────────────────────

// OverviewResponse is the shape served by /_sky/console/api/overview.
// Fields chosen to power the dashboard's at-a-glance pane: traffic
// rate, error rate, latency, active sessions, build info.
type OverviewResponse struct {
	BuiltAt        string  `json:"builtAt"`
	Commit         string  `json:"commit"`
	SkyVersion     string  `json:"skyVersion"`
	UptimeSeconds  float64 `json:"uptimeSeconds"`
	RequestsTotal  float64 `json:"requestsTotal"`
	ErrorRate5xx   float64 `json:"errorRate5xx"`     // fraction in [0,1]
	BufferLogUsed  uint64  `json:"bufferLogUsed"`
	BufferTraceUsed uint64 `json:"bufferTraceUsed"`
	ServerlessMode bool    `json:"serverlessMode"`
	ProductionMode bool    `json:"productionMode"`
}

// HandleConsoleOverview returns the at-a-glance JSON payload.
func HandleConsoleOverview(w http.ResponseWriter, r *http.Request) {
	if !consoleAccessAllowed(w, r) {
		return
	}
	store := telemetry.Default()
	snap := store.Snapshot()

	var requestsTotal, requests5xx float64
	for _, s := range snap {
		if s.Name != "sky_live_requests_total" {
			continue
		}
		requestsTotal += s.Value
		if status, ok := s.Labels["status"]; ok && len(status) > 0 && status[0] == '5' {
			requests5xx += s.Value
		}
	}
	errorRate := 0.0
	if requestsTotal > 0 {
		errorRate = requests5xx / requestsTotal
	}

	bi := currentBuildInfo()
	resp := OverviewResponse{
		BuiltAt:         bi.BuiltAt,
		Commit:          bi.Commit,
		SkyVersion:      bi.SkyVersion,
		UptimeSeconds:   time.Since(store.StartedAt()).Seconds(),
		RequestsTotal:   requestsTotal,
		ErrorRate5xx:    errorRate,
		BufferLogUsed:   countLogs(store),
		BufferTraceUsed: countTraces(store),
		ServerlessMode:  IsServerless(),
		ProductionMode:  isProductionMode(),
	}
	writeJSON(w, resp)
}

// countLogs / countTraces — return the in-memory ring occupancy.
//
// Pre-2026-05-18 these read from `Snapshot()` for a gauge named
// `sky_telemetry_buffer_used` — but that gauge is computed at
// `/_sky/metrics` scrape time and NEVER stored in the metric
// registry, so Snapshot always returned 0. Result: the console
// Overview's "Log buffer" / "Trace buffer" KPI cards always
// showed 0 even when the rings were full of entries. The
// per-tab Logs / Traces views worked because they call
// RecentLogs / RecentTraces directly.
//
// Cost: O(n) ring walk per scrape. The ring caps at 10K logs /
// 1K traces by default, well within budget for a 1Hz dashboard
// tick.
func countLogs(store *telemetry.Store) uint64 {
	return uint64(len(store.RecentLogs(0)))
}

func countTraces(store *telemetry.Store) uint64 {
	return uint64(len(store.RecentTraces(0)))
}

// HandleConsoleMetricsSummary returns the metrics snapshot grouped
// by family, structured for the dashboard table. Same data as
// /_sky/metrics but pre-parsed so the dashboard doesn't have to
// re-implement the Prometheus exposition parser in JS.
func HandleConsoleMetricsSummary(w http.ResponseWriter, r *http.Request) {
	if !consoleAccessAllowed(w, r) {
		return
	}
	// Labels are flattened to a "k=v, k=v" string here — the
	// console's MetricRow.labels field is a String ("rendered
	// server-side"). Sending the raw map made the Sky-side decode
	// yield an empty string, so distinct label-series (e.g.
	// sky_live_msg_seconds{name=Tick} vs {name=PagesLoaded})
	// rendered as indistinguishable "duplicate" rows.
	type metricRow struct {
		Name   string  `json:"name"`
		Type   string  `json:"type"`
		Labels string  `json:"labels,omitempty"`
		Value  float64 `json:"value"`
		Sum    float64 `json:"sum,omitempty"`
		Count  uint64  `json:"count,omitempty"`
	}
	snap := telemetry.Default().Snapshot()
	out := make([]metricRow, 0, len(snap))
	for _, s := range snap {
		out = append(out, metricRow{
			Name:   s.Name,
			Type:   s.Type,
			Labels: flattenMetricLabels(s.Labels),
			Value:  s.Value,
			Sum:    s.Sum,
			Count:  s.Count,
		})
	}
	writeJSON(w, out)
}

// flattenMetricLabels renders a label map as a stable "k=v, k=v"
// string (keys sorted so the same label set always serialises
// identically — important for the console diffing rows frame to
// frame).
func flattenMetricLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ", ")
}

// HandleConsoleLogs returns the most-recent ring entries. Filter
// via query params:
//
//   ?level=warn,error    — comma-separated set; default: all levels
//   ?req=<id>           — exact match on req_id field
//   ?limit=200          — cap on entries returned (default 200, max 1000)
func HandleConsoleLogs(w http.ResponseWriter, r *http.Request) {
	if !consoleAccessAllowed(w, r) {
		return
	}
	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}
	levelFilter := parseSetParam(r.URL.Query().Get("level"))
	reqFilter := r.URL.Query().Get("req")

	logs := telemetry.Default().RecentLogs(0)
	out := make([]telemetry.LogEntry, 0, limit)
	for _, l := range logs {
		if len(levelFilter) > 0 && !levelFilter[l.Level] {
			continue
		}
		if reqFilter != "" && l.ReqID != reqFilter {
			continue
		}
		out = append(out, l)
		if len(out) >= limit {
			break
		}
	}
	writeJSON(w, out)
}

// HandleConsoleTraces returns recent OTel-shaped trace spans.
// Newest first; capped at 100 (caller passes ?limit=N for less).
func HandleConsoleTraces(w http.ResponseWriter, r *http.Request) {
	if !consoleAccessAllowed(w, r) {
		return
	}
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}
	traces := telemetry.Default().RecentTraces(limit)
	// Project a serialisable shape (avoid leaking the trace.Span
	// SDK type — JSON-marshals as opaque).
	type traceRow struct {
		TraceID    string            `json:"traceId"`
		SpanID     string            `json:"spanId"`
		ParentID   string            `json:"parentId,omitempty"`
		Name       string            `json:"name"`
		Kind       string            `json:"kind,omitempty"`
		StartTime  string            `json:"startTime"`
		DurationMS float64           `json:"durationMs"`
		Status     string            `json:"status,omitempty"`
		StatusMsg  string            `json:"statusMessage,omitempty"`
		Attributes map[string]string `json:"attributes,omitempty"`
	}
	out := make([]traceRow, 0, len(traces))
	for _, t := range traces {
		out = append(out, traceRow{
			TraceID:    t.TraceID,
			SpanID:     t.SpanID,
			ParentID:   t.ParentID,
			Name:       t.Name,
			Kind:       t.Kind,
			StartTime:  t.StartTime.UTC().Format(time.RFC3339Nano),
			DurationMS: float64(t.Duration().Microseconds()) / 1000.0,
			Status:     t.StatusCode,
			StatusMsg:  t.StatusMessage,
			Attributes: t.Attributes,
		})
	}
	writeJSON(w, out)
}

// HandleConsoleErrors returns a ranked summary of distinct error
// messages from the log ring buffer. Bucket key is (level, error
// substring) so transient differences (timestamps, request IDs)
// don't fragment the summary. Most-recent occurrence + count surfaces.
func HandleConsoleErrors(w http.ResponseWriter, r *http.Request) {
	if !consoleAccessAllowed(w, r) {
		return
	}
	logs := telemetry.Default().RecentLogs(0)
	type errSummary struct {
		Level       string `json:"level"`
		Message     string `json:"message"`
		Count       int    `json:"count"`
		LastSeen    string `json:"lastSeen"`
		LastReqID   string `json:"lastReqId,omitempty"`
		LastError   string `json:"lastError,omitempty"`
	}
	buckets := make(map[string]*errSummary)
	for _, l := range logs {
		if l.Level != "warn" && l.Level != "error" {
			continue
		}
		// Bucket by message + truncated error string — keeps the
		// view low-cardinality when the same handler errors with
		// different timestamps.
		key := l.Level + "|" + l.Message
		if l.ErrorStr != "" {
			// First 80 chars of the error — long enough to
			// differentiate, short enough to coalesce.
			if len(l.ErrorStr) > 80 {
				key += "|" + l.ErrorStr[:80]
			} else {
				key += "|" + l.ErrorStr
			}
		}
		b, ok := buckets[key]
		if !ok {
			b = &errSummary{Level: l.Level, Message: l.Message}
			buckets[key] = b
		}
		b.Count++
		// logs come newest-first → first occurrence is the
		// most-recent. Keep.
		if b.LastSeen == "" {
			b.LastSeen = l.TS.UTC().Format(time.RFC3339Nano)
			b.LastReqID = l.ReqID
			b.LastError = l.ErrorStr
		}
	}
	out := make([]*errSummary, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, b)
	}
	// Sort by count desc, then by lastSeen desc.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].LastSeen > out[j].LastSeen
	})
	writeJSON(w, out)
}

// ─── helpers ──────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	// We use Encode (not Marshal+Write) to stream; for the small
	// payloads here the difference is rounding error, but it lets
	// us skip allocating the full byte slice up front.
	_ = json.NewEncoder(w).Encode(v)
}

// parseSetParam splits "warn,error" → {"warn": true, "error": true}.
// Empty input → empty map (filters disabled).
func parseSetParam(s string) map[string]bool {
	if s == "" {
		return nil
	}
	out := map[string]bool{}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out[s[start:i]] = true
			}
			start = i + 1
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
