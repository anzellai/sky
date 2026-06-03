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
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"sky-app/rt/telemetry"
)

// Locally-aliased net helpers — used by isLoopbackRemoteAddr below.
// Pulled out to keep the test seam (a fake RemoteAddr can be plugged
// in without touching the std-net package).
var (
	netSplitHostPort = net.SplitHostPort
	netParseIP       = net.ParseIP
)

// v0.16.1 PR 2 — boot-time mount-precedence invariant.
//
// Two atomic flags record whether each console-mount path actually
// claimed `/_sky/console`:
//
//   - inlineConsoleHealthy: MountEmbeddedConsole successfully wired
//     the inline Sky.Live-rendered console (PR 2 of v0.16.0).
//   - legacyConsoleHealthy: MountConsoleEndpoints successfully wired
//     the hand-written HTML shell (HandleConsole, pre-v0.16.0
//     canonical path).
//
// Pre-v0.16.1, both paths attempted to register `/_sky/console`
// whenever they ran. `safeMount`'s sync.Map-backed dedup turned the
// duplicate Go ServeMux registration into a no-op so the runtime
// didn't panic, but the surviving handler was registration-order-
// dependent: first writer won.
//
// The fix:
//
//   1. When `MountEmbeddedConsole` mounts inline successfully, it
//      sets `inlineConsoleHealthy` BEFORE `MountObservabilityEndpoints`
//      runs (which is where `MountConsoleEndpoints` lives).
//   2. `MountConsoleEndpoints` checks the flag and SKIPS the
//      `/_sky/console` HTML-shell registration when inline owns it.
//      The JSON API endpoints under `/_sky/console/api/*` stay —
//      they're MORE specific patterns under Go ServeMux longest-
//      prefix-match, so they coexist with the inline mount and are
//      what the inline console (and the legacy shell, when it
//      serves) call back into for fresh telemetry.
//   3. A boot-time invariant check (called from the Sky.Live +
//      Sky.Http.Server entry points) verifies that, when
//      `SKY_CONSOLE_AUTH` is explicitly set to a non-`off` value
//      AND we're not in sub-app context AND `SKY_CONSOLE_EMBED`
//      isn't off, AT LEAST ONE of {inline, legacy} ended up
//      claiming the path. If neither did, we exit 1 with a clear
//      stderr line — the user explicitly asked for a console but
//      none ever materialised.
var (
	inlineConsoleHealthy atomic.Bool
	legacyConsoleHealthy atomic.Bool
)

// InlineConsoleHealthy reports whether MountEmbeddedConsole
// successfully mounted the inline console for this binary. Public so
// the boot-time invariant + downstream observability surfaces can
// inspect the post-mount state.
func InlineConsoleHealthy() bool {
	return inlineConsoleHealthy.Load()
}

// LegacyConsoleHealthy reports whether MountConsoleEndpoints
// successfully mounted the legacy HTML shell for this binary.
func LegacyConsoleHealthy() bool {
	return legacyConsoleHealthy.Load()
}

// ResetConsoleHealthFlagsForTesting clears the atomic mount-health
// flags so individual tests can re-run the mount paths from a clean
// slate. Test-only; not part of the public API.
func ResetConsoleHealthFlagsForTesting() {
	inlineConsoleHealthy.Store(false)
	legacyConsoleHealthy.Store(false)
}

// shouldHaveConsole reports whether the user EXPLICITLY asked for a
// console to be served at `/_sky/console` for this binary. Returns
// true only when `SKY_CONSOLE_AUTH` is set to `token` / `app`
// (NOT `off`, NOT unset), AND `SKY_CONSOLE_EMBED` is not off, AND
// we're not running as a sub-app (parent owns the console).
//
// This is the gate for the boot-time invariant: if `shouldHaveConsole`
// is true but neither flag is set after both mount paths ran, exit
// loudly rather than silently leave the user with a missing surface.
func shouldHaveConsole() bool {
	if base := os.Getenv("SKY_LIVE_BASE_PATH"); base != "" {
		return false
	}
	if v := os.Getenv("SKY_CONSOLE_EMBED"); v == "off" || v == "0" || v == "false" {
		return false
	}
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("SKY_CONSOLE_AUTH")))
	return raw == "token" || raw == "app"
}

// AssertConsoleInvariantOrExit verifies the mount-precedence
// invariant. Called from Sky.Live + Sky.Http.Server boot AFTER both
// `MountEmbeddedConsole` and `MountObservabilityEndpoints` have run.
// When `shouldHaveConsole` is true but BOTH mount-health flags are
// false, prints a stderr line and `os.Exit(1)`. Otherwise no-op.
//
// Idempotent: re-running with the same env + flag state is safe.
// The exit path is OS-level so tests must drive it via subprocess
// (os/exec).
func AssertConsoleInvariantOrExit() {
	if !shouldHaveConsole() {
		return
	}
	if inlineConsoleHealthy.Load() || legacyConsoleHealthy.Load() {
		return
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("SKY_CONSOLE_AUTH")))
	fmt.Fprintf(os.Stderr,
		"[sky.console] FATAL: SKY_CONSOLE_AUTH=%s is set but neither inline nor legacy console mounted /_sky/console. "+
			"Either link the console_app blank import (the compiler emits this for every Sky.Live + Sky.Http.Server "+
			"app — a hand-edited main.go may have dropped it) OR set SKY_CONSOLE_AUTH=off to declare the surface "+
			"intentionally absent.\n", mode)
	os.Exit(1)
}

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
	// v0.16.1 PR14: SKY_CONSOLE_AUTH=off means "no console at all".
	// Pre-PR14 the legacy HTML shell still mounted in this case,
	// surfacing console UI on a deployment that explicitly declined
	// it. Honour the off mode universally — inline already declines
	// via MountEmbeddedConsole's consoleAuthModeOff branch; this
	// closes the matching legacy path.
	if v := os.Getenv("SKY_CONSOLE_AUTH"); v == "off" {
		return
	}
	// PR 2 (v0.16.1): skip the legacy HTML shell registration when
	// the inline console (MountEmbeddedConsole, called first from
	// the boot path) already claimed `/_sky/console`. Previously
	// both paths called `safeMount` for the same pattern and
	// `safeMount`'s sync.Map dedup made one a no-op based on
	// registration order — fragile + order-dependent. Now intent is
	// explicit: inline wins by design, legacy serves only when
	// inline declined (no console_app blank import, or it failed at
	// mount time).
	//
	// The JSON API endpoints below are NOT gated — they're more-
	// specific patterns under Go ServeMux longest-prefix-match
	// (`/_sky/console/api/...` ≠ `/_sky/console`), so they coexist
	// with whichever path serves the root and feed both UIs.
	if !inlineConsoleHealthy.Load() {
		safeMount(mux, "/_sky/console", HandleConsole)
		// Only flip the legacy-healthy flag when WE registered the
		// path. The `safeMount` panic-recover hides duplicate
		// registrations, but in this branch the inline path didn't
		// claim it so our registration must have succeeded (modulo
		// user-mounted `/_sky/console`, which is an explicit opt-out
		// the user already declared by mounting their own handler).
		legacyConsoleHealthy.Store(true)
	}
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
// v0.16.0 contract (PR 3):
//   - Sub-app context (`SKY_LIVE_BASE_PATH` set) → skip entirely;
//     the parent owns its own console.
//   - `SKY_CONSOLE_EMBED=off|0|false` → skip (legacy opt-out, kept).
//   - `SKY_CONSOLE_AUTH=off` → skip and log the explicit decline.
//   - Production mode (`ENV` ∈ unset-but-non-dev-marker) AND
//     `SKY_CONSOLE_AUTH` unset → skip + warn `console.disabled
//     reason=auth-unset`. No silent open-to-the-world.
//   - Otherwise mount, with per-request auth gating handled by
//     evaluateConsoleAuth (token cookie / app callback / dev open).
//
// The function logs (best-effort) to stderr on outcome:
//   - "inline console mounted at /_sky/console mode=<m>"
//   - "inline console skipped reason=<r>"
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
	st := loadConsoleAuthState()
	switch st.mode {
	case consoleAuthModeOff:
		fmt.Fprintln(os.Stderr, "[sky.console] inline console skipped reason=auth-off (SKY_CONSOLE_AUTH=off)")
		logStructured("info", "console.disabled", "reason", "auth-off")
		return
	case consoleAuthModeUnsetProd:
		fmt.Fprintln(os.Stderr, "[sky.console] inline console skipped reason=auth-unset (production mode requires SKY_CONSOLE_AUTH; see docs/v0.16.x-console/EMBEDDED.md)")
		logStructured("warn", "console.disabled", "reason", "auth-unset")
		return
	}
	// Initialise the ingest token even though the inline mount
	// doesn't use it directly — keeps observability federation
	// (push exporter from foreign sub-apps, if any) functional.
	IngestTokenInit()
	// Login POST handler (always-on companion when the gate is
	// active). Mount BEFORE the inline catch-all so the more-specific
	// pattern wins in Go's ServeMux longest-prefix-match.
	mountConsoleAuthRoutes(mux)

	// v0.16.1 PR10-F — mount the inline console via the canonical
	// Sky.Live sub-app primitive. The bundled console's Sky-source
	// init / update / view / subscriptions cycle drives the SSE +
	// event loop via the SAME machinery that powers every user
	// Sky.Live app. No bespoke console_loop, no parallel SSE
	// channel, no separate session map.
	//
	// Fallback: when console_app is NOT linked into this binary
	// (a hand-edited main.go dropped the blank import the compiler
	// emits), InlineConsoleCfg returns nil — we log loudly and
	// return without claiming /_sky/console. MountConsoleEndpoints
	// (called later from MountObservabilityEndpoints) will then
	// mount the legacy HTML shell at /_sky/console as the safe
	// degraded surface.
	cfg := InlineConsoleCfg()
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "[sky.console] inline console unavailable: console_app cfg-provider not registered "+
			"(host binary missing `import _ \"sky-app/rt/console_app\"`); falling back to legacy HTML shell")
		return
	}

	// Wrap the sub-app's routes with the auth gate. ConsoleGate
	// evaluates the __Host-sky_console cookie / app callback / dev
	// mode contract and writes the appropriate response on failure.
	app := MountLiveSubAppInProcessWithGate(mux, "/_sky/console", cfg, ConsoleGate)
	_ = app

	// PR 2 (v0.16.1): mark the inline mount healthy so
	// MountConsoleEndpoints (called later from
	// MountObservabilityEndpoints) skips its own /_sky/console
	// registration. The legacy HTML shell's JSON API endpoints
	// (/_sky/console/api/*) remain registered — the inline UI's
	// Cmd.perform Http.get calls hit those endpoints for fresh
	// telemetry, same as the legacy UI did.
	inlineConsoleHealthy.Store(true)

	fmt.Fprintf(os.Stderr, "[sky.console] inline console mounted as Sky.Live sub-app at /_sky/console mode=%s\n", describeConsoleAuthMode(st.mode))
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

// consoleAccessAllowed implements the auth gate.
//
// v0.16.0 PR 3: delegates to evaluateConsoleAuth which dispatches
// on SKY_CONSOLE_AUTH = token | app | off (+ the production +
// dev-open defaults). The v0.15.x admin-token / serverless branches
// migrated INTO that function, so this is now a thin alias.
//
// v0.16.1 PR10-F: same-process loopback callers (the inline
// console's Sky-side Cmd.perform → Http.get on /_sky/console/api/*)
// bypass the auth check. Loopback originates from RemoteAddr =
// 127.0.0.1 OR ::1 — never reachable from a browser, so it cannot
// be abused to bypass the gate on a deployed binary. This keeps the
// canonical Sky.Live console update loop working in token-mode
// without requiring the loopback Http.get to mint + carry the
// console auth cookie.
//
// Returns true when the request may proceed; false when a response
// (401 / 403 / 503) has been written.
func consoleAccessAllowed(w http.ResponseWriter, r *http.Request) bool {
	if isLoopbackRemoteAddr(r) {
		return true
	}
	return evaluateConsoleAuth(w, r)
}

// isLoopbackRemoteAddr reports whether r.RemoteAddr resolves to a
// loopback IP. Loopback in this context means the request originated
// from a SAME-PROCESS or SAME-HOST source (most commonly the inline
// console's own Cmd.perform Http.get cycle). Browsers connecting
// from a real network NEVER hit this branch because Go's net/http
// records the actual peer IP in RemoteAddr.
//
// Implementation: parse the address as host:port, then look up
// `net.ParseIP(host).IsLoopback()`. Bare-host fallback (no colon)
// applies the same check to the whole string. Empty / unparseable
// addresses return false (fail-closed).
func isLoopbackRemoteAddr(r *http.Request) bool {
	if r == nil || r.RemoteAddr == "" {
		return false
	}
	host, _, err := netSplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "" {
		return false
	}
	ip := netParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
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
//   ?limit=50           — cap on entries returned (default 50, max 1000)
//
// Default cap lowered from 200 → 50 in v0.16.1 PR11 — the polling
// console under Sub.every 3000 was returning 67 KB JSON per tick
// at 2.5k buffer occupancy, pegging 1-CPU VMs at >180% CPU. 50
// entries renders fully in <50 KB; UI pagination follow-up adds
// ?offset for back-pages.
func HandleConsoleLogs(w http.ResponseWriter, r *http.Request) {
	if !consoleAccessAllowed(w, r) {
		return
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}
	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}
	levelFilter := parseSetParam(r.URL.Query().Get("level"))
	reqFilter := r.URL.Query().Get("req")

	logs := telemetry.Default().RecentLogs(0)
	matched := make([]telemetry.LogEntry, 0, limit+offset)
	for _, l := range logs {
		if len(levelFilter) > 0 && !levelFilter[l.Level] {
			continue
		}
		if reqFilter != "" && l.ReqID != reqFilter {
			continue
		}
		matched = append(matched, l)
		if len(matched) >= limit+offset {
			break
		}
	}
	out := matched
	if offset < len(matched) {
		out = matched[offset:]
	} else {
		out = matched[:0]
	}
	writeJSON(w, out)
}

// HandleConsoleTraces returns recent OTel-shaped trace spans.
// Newest first; default 25 (was 100 pre-PR11). Use ?limit=N&offset=M
// for pagination.
func HandleConsoleTraces(w http.ResponseWriter, r *http.Request) {
	if !consoleAccessAllowed(w, r) {
		return
	}
	limit := 25
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}
	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}
	_ = offset
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
