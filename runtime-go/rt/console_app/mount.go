// Package console_app holds the Std.Ui Sky.Live console UI, translated
// to Go ONCE at compiler-release time by scripts/regenerate-console.sh
// and committed alongside the rest of the runtime.
//
// Why a subpackage of `sky-app/rt` rather than a peer?
//   - `runtime-go/` is embedded recursively into the Sky compiler
//     binary via TH (`Sky.Build.EmbeddedRuntime`), then re-materialised
//     into every user app's `sky-out/rt/`. Putting console_app inside
//     `rt/` is the only way to get it materialised alongside the rest
//     of the runtime without changing the embedding mechanism.
//   - The directory layout mirrors what user apps see at build time:
//       sky-out/main.go              package main         imports sky-app/rt
//       sky-out/rt/*.go              package rt
//       sky-out/rt/console_app/*.go  package console_app  imports sky-app/rt
//
// PR 1 status (v0.16.0):
//   - main.go contains the generated Go translation of
//     sky-bundled/console/src/*.sky.
//   - This file (mount.go) exposes MountInlineConsole — the public
//     entry point a host app calls to register the console handlers
//     onto an existing *http.ServeMux. The implementation is
//     intentionally minimal for PR 1: a single server-rendered HTML
//     view from the bundled Sky source. SSE / event dispatch / full
//     Sky.Live wiring lands in PR 2/3.
//   - rt (the parent package) cannot import console_app — that would
//     be an import cycle (console_app imports rt). Instead, the rt
//     side exposes rt.MountInlineConsole as a registration shim
//     (see runtime-go/rt/console_inline.go); console_app's init()
//     in register.go pushes its implementation into that shim.
//     Until something in the user's binary imports console_app
//     (blank import is sufficient), the shim returns
//     ErrInlineConsoleUnavailable. PR 2 adds the wiring in user
//     codegen + flips SKY_CONSOLE_MODE's default to inline.

package console_app

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	rt "sky-app/rt"
	"sky-app/rt/telemetry"
)

// MountInlineConsole registers the inline Sky Console handlers on
// `mux`. basePath is the same kind of prefix Sky.Live's MountSubApp
// would use ("" for "no prefix"; "/admin" for "this app sits under
// /admin/_sky/console/…"). The function is safe to call exactly
// once per mux; subsequent calls on the same mux + path will panic
// inside net/http when registering a duplicate pattern.
//
// What it serves (PR 1):
//   - GET <basePath>/_sky/console      — server-rendered HTML shell
//     produced by calling the bundled Sky.Live console's init/view
//     functions on a fresh Model. No JS bundling, no SSE patch
//     channel — the UI is static-rendered at request time. PR 2/3
//     wires up the full Sky.Live event + SSE plumbing.
//
// The JSON API endpoints (/_sky/console/api/*) are NOT touched here.
// They are mounted by rt.MountConsoleEndpoints (the existing
// console.go path) — keeping the two surfaces separate makes the PR
// 1 deletion-window (PR 2) cleaner: PR 2 removes the legacy HTML
// shell, but the JSON API stays exactly where it is.
func MountInlineConsole(mux *http.ServeMux, basePath string) error {
	if mux == nil {
		return fmt.Errorf("console_app: MountInlineConsole called with nil *http.ServeMux")
	}
	prefix := normaliseBasePath(basePath)
	path := prefix + "/_sky/console"
	// PR 3 (v0.16.0): wrap the bundled handler with the rt-side auth
	// gate. rt.ConsoleGate is a thin shim around evaluateConsoleAuth
	// that returns true when the request may proceed; false (with the
	// response already written) otherwise.
	gated := func(w http.ResponseWriter, r *http.Request) {
		if !rt.ConsoleGate(w, r) {
			return
		}
		handleConsoleRoot(w, r)
	}
	// Two-arg signature: handle both /_sky/console and /_sky/console/
	// (Go's ServeMux treats trailing-slash as different patterns).
	mux.HandleFunc(path, gated)
	if !strings.HasSuffix(path, "/") {
		mux.HandleFunc(path+"/", gated)
	}
	return nil
}

// handleConsoleRoot renders the initial HTML view of the bundled
// console. The view is derived by calling the generated `init_` to
// produce a starter Model, then `viewWrapped` to produce a Sky.Html
// value, then rt.HtmlRender to flatten it to HTML.
//
// Authentication is intentionally NOT enforced here for PR 1 — the
// host app's mux will route via rt.consoleAccessAllowed in PR 2/3
// when this becomes the canonical mount path. For now the inline
// path is opt-in (SKY_CONSOLE_MODE=inline) so it never auto-mounts
// on a user app's listener.
func handleConsoleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	defer func() {
		// Defensive: if the generated Sky code panics for any reason
		// (e.g. a stdlib version mismatch between the regen-time
		// runtime-go and the host's), serve a 500 with a diagnostic
		// hint rather than letting the panic propagate.
		if rec := recover(); rec != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w,
				"<!DOCTYPE html><html><body style=\"font-family:system-ui;padding:24px;\">"+
					"<h1>Sky Console — inline mount panic</h1>"+
					"<p>The bundled console UI panicked while rendering. This is a Sky compiler / "+
					"stdlib mismatch — regenerate via <code>scripts/regenerate-console.sh</code> "+
					"against the current runtime.</p>"+
					"<pre style=\"background:#f0f0f0;padding:12px;overflow:auto;\">%v</pre>"+
					"</body></html>",
					rec)
		}
	}()

	// Build the initial Model via the generated init_ function. The
	// init takes a request shape — pass an empty Dict-shaped value
	// because the bundled console doesn't read req fields (it reads
	// SKY_PARENT_URL from env instead).
	req := map[string]any{"path": "/", "query": ""}
	tuple := init_[any](req)
	model, ok := tuple.V0.(State_Model_R)
	if !ok {
		// Should never happen — init_ is statically typed to return
		// SkyTuple2{V0: State_Model_R}. Treat as compiler bug.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "console_app: init_ returned unexpected V0 type %T", tuple.V0)
		return
	}
	// v0.16.1 PR7-B — pre-populate data-driven fields directly from
	// telemetry.Default() so the first render shows real numbers.
	// Without this, init_'s emitted Cmd would fetch the data
	// asynchronously via Http.get loopbacks — but the inline mount
	// renders synchronously to the response body before any update
	// loop turns the wheel, so first paint would always show empty
	// Overview / mock Logs. PR8's SSE-driven update loop refreshes
	// these fields on subsequent ticks via the same Sub.every 3000
	// pump that init_ already wired.
	model = hydrateInitialModel(model)
	htmlNode := viewWrapped(model)
	body := rt.HtmlRender(htmlNode)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Sky-Console-Mode", "inline")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	// Wrap the body fragment in a minimal HTML5 document. The
	// generated view returns a layout-rooted Std.Ui tree, which when
	// rendered produces a self-contained `<div>` tree with inline
	// styles. We add the doctype + html/head/body shell here for
	// browser compatibility.
	//
	// v0.16.1 PR 8 — bundle the inline client-side script that:
	//   1. Opens an EventSource on /_sky/console/_sse and applies
	//      the SSE-delivered `event: patches` / `event: patch`
	//      frames to the rendered DOM.
	//   2. Captures gestures on the [data-sky-console] subtree and
	//      POSTs {msg, args} envelopes to /_sky/console/_event so
	//      console_loop.go (the rt-side update loop) can fold them
	//      through hooks.Update + diff + broadcast.
	//
	// Why the wrapping `<div data-sky-console>`: a host app may run
	// its own Sky.Live JS on the same page. Scoping our gesture
	// listeners to elements inside `[data-sky-console]` keeps the
	// two click handlers cleanly separated.
	fmt.Fprintf(w,
		"<!DOCTYPE html>\n"+
			"<html lang=\"en\">\n"+
			"<head>\n"+
			"  <meta charset=\"utf-8\">\n"+
			"  <meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n"+
			"  <meta name=\"sky-console-mode\" content=\"inline\">\n"+
			"  <title>Sky Console</title>\n"+
			"  <style>html,body{margin:0;padding:0;background:#0f1115;color:#e4e6eb;font-family:ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;}</style>\n"+
			"</head>\n"+
			"<body><div id=\"sky-console-root\" data-sky-console=\"1\">%s</div>\n"+
			"<script>%s</script>\n"+
			"</body>\n"+
			"</html>\n",
		body, consoleClientJS())
}

// normaliseBasePath mirrors rt.normaliseBasePath so callers don't need
// to know the prefix-cleaning rules. (Trimmed copy to avoid an
// exported-helper churn in rt itself, which PR 2 will revisit when
// the rt-side mounting code consolidates.)
//
// Rules:
//   - "" or "/" → ""        (no prefix; routes mount at /_sky/console)
//   - "/admin"  → "/admin"
//   - "admin"   → "/admin"  (leading slash inserted)
//   - "/admin/" → "/admin"  (trailing slash stripped)
func normaliseBasePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if strings.HasSuffix(p, "/") {
		p = strings.TrimRight(p, "/")
	}
	return p
}

// hydrateInitialModel (v0.16.1 PR7-B) replaces the data-driven
// fields of `m` with values pulled directly from `telemetry.Default()`
// plus rt-side build info / production-mode flags. Mirror of what
// HandleConsoleOverview / HandleConsoleLogs / HandleConsoleMetricsSummary
// / HandleConsoleTraces / HandleConsoleErrors compute — minus the
// JSON-encode hop, since we're staying in-process.
//
// Why "overlay" rather than "rebuild from scratch": init_ also sets
// LogFilter defaults, Tab=Overview, TraceQuery="", LastError="",
// ParentUrl — preserving those means future tweaks to the Sky-source
// init_ stay live without touching this Go path.
//
// Resilience: the underlying telemetry.Default() returns empty slices
// when nothing has been recorded (cold start, immediately after
// process boot). Empty slices in State_*_R are fine — they render as
// "no entries" cards rather than the misleading mock-mode banner.
//
// Performance: each call walks the in-RAM ring buffers (capped at
// 10K logs / 1K spans by default). Per-request cost in the µs range;
// negligible against the HTML render that follows.
func hydrateInitialModel(m State_Model_R) State_Model_R {
	store := telemetry.Default()

	m.Overview = computeOverview(store)
	m.Logs = computeLogs(store, m.LogFilter, 200)
	m.Metrics = computeMetrics(store)
	m.Traces = computeTraces(store, 100)
	m.Errors = computeErrors(store)
	return m
}

// computeOverview mirrors HandleConsoleOverview's shape but constructs
// the typed State_Overview_R directly (no JSON detour). Keep field
// initialisation in struct-literal sorted order so re-generations of
// the Sky source surface field renames as Go compile errors.
func computeOverview(store *telemetry.Store) State_Overview_R {
	bi := rt.ConsoleCurrentBuildInfo()
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

	return State_Overview_R{
		BufferLogUsed:   len(store.RecentLogs(0)),
		BufferTraceUsed: len(store.RecentTraces(0)),
		BuiltAt:         bi.BuiltAt,
		Commit:          bi.Commit,
		ErrorRate5xx:    errorRate,
		ProductionMode:  rt.ConsoleIsProductionMode(),
		RequestsTotal:   int(requestsTotal),
		SkyVersion:      bi.SkyVersion,
		UptimeSeconds:   int(time.Since(store.StartedAt()).Seconds()),
	}
}

// computeLogs mirrors HandleConsoleLogs (level filter, limit). The
// `filter` here is the Sky-side LogFilter (sourced from the freshly
// init_'d model). On first render this is the empty filter
// (`State_emptyLogFilter`: showDebug=false, showInfo/Warn/Error=true)
// so debug-level entries are excluded by default — matches the JSON
// handler's `?level=info,warn,error` default behaviour.
func computeLogs(store *telemetry.Store, filter State_LogFilter_R, limit int) []State_LogEntry_R {
	logs := store.RecentLogs(0)
	out := make([]State_LogEntry_R, 0, limit)
	for _, l := range logs {
		if !logPassesFilter(l.Level, filter) {
			continue
		}
		out = append(out, State_LogEntry_R{
			LatencyMs: l.LatencyMS,
			Level:     l.Level,
			Message:   l.Message,
			ReqId:     l.ReqID,
			Route:     l.Route,
			SessionId: "",
			Status:    float64(l.Status),
			Subapp:    l.Subapp,
			Time:      l.TS.UTC().Format(time.RFC3339Nano),
			UserLabel: "",
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// logPassesFilter mirrors the State_LogFilter_R semantics — Show* are
// per-level boolean opts. Default-construction filter (post init_)
// has ShowDebug=false + the other three true.
func logPassesFilter(level string, f State_LogFilter_R) bool {
	switch level {
	case "debug":
		return f.ShowDebug
	case "info":
		return f.ShowInfo
	case "warn":
		return f.ShowWarn
	case "error":
		return f.ShowError
	}
	// Unknown level — let it through so it surfaces in the console
	// rather than disappearing silently.
	return true
}

// computeMetrics mirrors HandleConsoleMetricsSummary — flatten the
// labels map to a stable "k=v, k=v" string so distinct label-series
// don't render as duplicates.
func computeMetrics(store *telemetry.Store) []State_MetricRow_R {
	snap := store.Snapshot()
	out := make([]State_MetricRow_R, 0, len(snap))
	for _, s := range snap {
		out = append(out, State_MetricRow_R{
			Name:   s.Name,
			Typ:    s.Type,
			Labels: flattenLabels(s.Labels),
			Value:  s.Value,
			Sum:    s.Sum,
			Count:  float64(s.Count),
		})
	}
	return out
}

// flattenLabels duplicates rt/console.go's flattenMetricLabels —
// kept local to avoid widening rt's exported surface for a 12-line
// helper that's purely internal to the inline-mount data path.
func flattenLabels(labels map[string]string) string {
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

// computeTraces mirrors HandleConsoleTraces. Status maps "ok"/"error"
// onto the Sky-side string field; an unfinished span (EndTime zero)
// reports duration 0 — consistent with the JSON handler.
func computeTraces(store *telemetry.Store, limit int) []State_TraceRow_R {
	traces := store.RecentTraces(limit)
	out := make([]State_TraceRow_R, 0, len(traces))
	for _, t := range traces {
		out = append(out, State_TraceRow_R{
			DurationMs: float64(t.Duration().Microseconds()) / 1000.0,
			Kind:       t.Kind,
			Name:       t.Name,
			ParentId:   t.ParentID,
			SpanId:     t.SpanID,
			StartTime:  t.StartTime.UTC().Format(time.RFC3339Nano),
			Status:     t.StatusCode,
			TraceId:    t.TraceID,
		})
	}
	return out
}

// computeErrors mirrors HandleConsoleErrors. Bucket by (level, message,
// truncated-errstr) so transient differences (timestamps / request IDs)
// don't fragment the summary. Sort by count desc.
func computeErrors(store *telemetry.Store) []State_ErrorRow_R {
	logs := store.RecentLogs(0)
	type bucket struct {
		level   string
		message string
		count   int
	}
	buckets := make(map[string]*bucket)
	for _, l := range logs {
		if l.Level != "warn" && l.Level != "error" {
			continue
		}
		key := l.Level + "|" + l.Message
		if l.ErrorStr != "" {
			if len(l.ErrorStr) > 80 {
				key += "|" + l.ErrorStr[:80]
			} else {
				key += "|" + l.ErrorStr
			}
		}
		b, ok := buckets[key]
		if !ok {
			b = &bucket{level: l.Level, message: l.Message}
			buckets[key] = b
		}
		b.count++
	}
	out := make([]State_ErrorRow_R, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, State_ErrorRow_R{
			Count:   b.count,
			Message: b.message,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Count > out[j].Count
	})
	return out
}
