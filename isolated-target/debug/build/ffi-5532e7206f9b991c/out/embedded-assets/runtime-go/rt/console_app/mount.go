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
// v0.16.1 PR10-G status:
//   - The bespoke MountInlineConsole one-shot HTML render path is
//     DELETED. The canonical mount path is rt.MountEmbeddedConsole,
//     which now uses rt.MountLiveSubAppInProcessWithGate against the
//     Sky-source cfg returned by InlineConsoleCfg() (registered via
//     console_app's init in register_v3.go).
//   - main.go's generated `init_` / `update` / `viewWrapped` /
//     `subscriptions` are still the Sky-source TEA loop — they're just
//     consumed via the canonical Sky.Live machinery now instead of
//     console_app's own handleConsoleRoot.
//   - register.go (PR 1's MountInlineConsole shim) is removed; the
//     hook surface lives in rt.RegisterInlineConsoleHook as a no-op
//     for back-compat.
//   - hydrateInitialModel + computeOverview / computeLogs / etc. are
//     KEPT — they're the in-process telemetry → State_*_R bridges the
//     bundled console's Cmd-shaped Http.get loopback replaces. v0.16.2
//     may inline those bridges directly into the Sky source so the
//     loopback Http.get becomes unnecessary.

package console_app

import (
	"sort"
	"strings"
	"time"

	rt "sky-app/rt"
	"sky-app/rt/telemetry"
)

// hydrateInitialModel (v0.16.1 PR7-B; kept after PR10-G) replaces the
// data-driven fields of `m` with values pulled directly from
// `telemetry.Default()` plus rt-side build info / production-mode
// flags. The bundled console's `init_` returns a sparsely-populated
// model; this overlay fills the data slots so the initial render
// shows real numbers before the first Cmd-spawned HTTP loopback fires.
//
// Post-PR10-F the canonical Sky.Live handleInitial still calls
// `init_` (via Field(cfg, "Init") and sky_call), and the bundled
// console's first render still needs the overlay to surface real
// telemetry. The function stays as an in-process bridge against
// rt.telemetry.Default(); v0.16.2 may move the bridge into the Sky
// source via dedicated Cmd-shaped helpers.
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
