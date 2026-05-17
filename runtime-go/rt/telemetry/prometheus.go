package telemetry

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WriteProm renders the store's metric snapshot in Prometheus text
// exposition format 0.0.4 — the format `prometheus.io/scrape` Go
// scrapers expect at the /_sky/metrics endpoint.
//
// Output shape per metric family:
//
//	# HELP <name> <description>
//	# TYPE <name> counter|gauge|histogram
//	<name>{labels...} <value> [<timestamp_ms>]
//
// For histograms, three additional lines:
//
//	<name>_bucket{labels...,le="<bound>"} <count>
//	<name>_sum{labels...} <sum>
//	<name>_count{labels...} <count>
//
// Caller passes a buffer (typically `bytes.Buffer` from a HTTP
// handler); we write directly to avoid intermediate allocation per
// metric.
//
// Help text comes from the helpTexts map below; metrics without an
// entry get a generic "Sky metric" line so the output stays
// well-formed for scrapers that require # HELP.
func (s *Store) WriteProm(w stringWriter) {
	samples := s.Snapshot()
	// Group by metric name so we emit one HELP+TYPE pair per family.
	byName := make(map[string][]MetricSample)
	for _, sm := range samples {
		byName[sm.Name] = append(byName[sm.Name], sm)
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		series := byName[name]
		t := series[0].Type
		writeHeader(w, name, t)
		switch t {
		case "counter", "gauge":
			for _, sm := range series {
				writeLine(w, name, sm.Labels, sm.Value)
			}
		case "histogram":
			for _, sm := range series {
				writeHistogram(w, name, sm)
			}
		}
	}

	// Process metrics — Prometheus convention. Lets dashboards show
	// uptime, restart events, age.
	startedAtSecs := float64(s.StartedAt().Unix())
	writeHeader(w, "process_start_time_seconds", "gauge")
	writeLine(w, "process_start_time_seconds", nil, startedAtSecs)

	// `sky_telemetry_buffer_used` exposes the ring-buffer occupancy
	// so the dashboard can warn when log/trace volume is outpacing
	// retention (a signal that the Hot tier needs Cold-tier export).
	writeHeader(w, "sky_telemetry_buffer_used", "gauge")
	writeLine(w, "sky_telemetry_buffer_used",
		map[string]string{"kind": "log"}, float64(s.logs.snapshotCount()))
	writeLine(w, "sky_telemetry_buffer_used",
		map[string]string{"kind": "trace"}, float64(s.traces.snapshotCount()))
}

// stringWriter — minimal interface for callers that want to pass
// `bytes.Buffer` or `http.ResponseWriter` without an io.Writer
// allocation. Both types satisfy this naturally.
type stringWriter interface {
	WriteString(string) (int, error)
}

// helpTexts — short HELP descriptions for each kernel metric. Loose
// match: helpTexts[name] looked up, else a default. Keep in sync
// with the RFC's metric list.
var helpTexts = map[string]string{
	"sky_live_requests_total":      "HTTP requests handled, partitioned by method/route/status",
	"sky_live_msg_total":           "Sky.Live Msg dispatches, partitioned by name/outcome/noop",
	"sky_live_sse_connections_total": "Sky.Live SSE connection lifecycle events",
	"sky_live_sessions_active":     "Active Sky.Live sessions (open SSE channels)",
	"sky_live_request_seconds":     "HTTP request latency histogram (seconds)",
	"sky_live_msg_seconds":         "Msg dispatch + update + diff latency histogram (seconds)",
	"sky_db_query_total":           "Std.Db queries executed, partitioned by table/outcome",
	"sky_db_query_seconds":         "Std.Db query latency histogram (seconds)",
	"sky_db_pool_in_use":           "Std.Db connections currently leased",
	"sky_db_pool_idle":             "Std.Db connections currently idle",
	"sky_jobs_total":               "Std.Jobs runs, partitioned by queue/outcome",
	"sky_jobs_duration_seconds":    "Std.Jobs job duration histogram (seconds)",
	"sky_jobs_inflight":            "Std.Jobs currently-running jobs",
	"sky_jobs_queue_depth":         "Std.Jobs pending jobs per queue",
	"sky_ffi_calls_total":          "Go FFI invocations, partitioned by pkg/outcome",
	"sky_telemetry_buffer_used":    "Hot-tier ring buffer occupancy (entries)",
	"process_start_time_seconds":   "Process start time (seconds since epoch)",
}

func writeHeader(w stringWriter, name, mtype string) {
	help := helpTexts[name]
	if help == "" {
		help = "Sky metric"
	}
	w.WriteString("# HELP ")
	w.WriteString(name)
	w.WriteString(" ")
	w.WriteString(help)
	w.WriteString("\n# TYPE ")
	w.WriteString(name)
	w.WriteString(" ")
	w.WriteString(mtype)
	w.WriteString("\n")
}

func writeLine(w stringWriter, name string, labels map[string]string, v float64) {
	w.WriteString(name)
	writeLabels(w, labels, "", "")
	w.WriteString(" ")
	w.WriteString(formatFloat(v))
	w.WriteString("\n")
}

func writeHistogram(w stringWriter, name string, sm MetricSample) {
	// Emit one _bucket line per boundary, in sorted order. The
	// snapshot already holds cumulative counts (Observe bumps every
	// bucket whose `le` >= v), so emit them as-is — no second
	// accumulation pass.
	keys := make([]float64, 0, len(sm.Buckets))
	for k := range sm.Buckets {
		keys = append(keys, k)
	}
	sort.Float64s(keys)
	for _, b := range keys {
		w.WriteString(name)
		w.WriteString("_bucket")
		writeLabels(w, sm.Labels, "le", formatFloat(b))
		w.WriteString(" ")
		w.WriteString(strconv.FormatUint(sm.Buckets[b], 10))
		w.WriteString("\n")
	}
	// +Inf bucket
	w.WriteString(name)
	w.WriteString("_bucket")
	writeLabels(w, sm.Labels, "le", "+Inf")
	w.WriteString(" ")
	w.WriteString(strconv.FormatUint(sm.Count, 10))
	w.WriteString("\n")
	// _sum + _count
	w.WriteString(name)
	w.WriteString("_sum")
	writeLabels(w, sm.Labels, "", "")
	w.WriteString(" ")
	w.WriteString(formatFloat(sm.Sum))
	w.WriteString("\n")
	w.WriteString(name)
	w.WriteString("_count")
	writeLabels(w, sm.Labels, "", "")
	w.WriteString(" ")
	w.WriteString(strconv.FormatUint(sm.Count, 10))
	w.WriteString("\n")
}

// writeLabels emits the Prometheus label block — `{k1="v1",k2="v2"}`.
// When `extraK` is non-empty, it's appended as an additional label
// (used for histogram `le=...`).
//
// Label values are escaped per the Prometheus text spec: backslash,
// double-quote, newline. We do NOT escape commas/equals signs in
// values — those aren't required by the spec and the histogram +Inf
// path emits a raw "+Inf" deliberately.
func writeLabels(w stringWriter, labels map[string]string, extraK, extraV string) {
	if len(labels) == 0 && extraK == "" {
		return
	}
	w.WriteString("{")
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	first := true
	for _, k := range keys {
		if !first {
			w.WriteString(",")
		}
		w.WriteString(k)
		w.WriteString("=\"")
		w.WriteString(escapePromLabelValue(labels[k]))
		w.WriteString("\"")
		first = false
	}
	if extraK != "" {
		if !first {
			w.WriteString(",")
		}
		w.WriteString(extraK)
		w.WriteString("=\"")
		w.WriteString(escapePromLabelValue(extraV))
		w.WriteString("\"")
	}
	w.WriteString("}")
}

func escapePromLabelValue(v string) string {
	if !strings.ContainsAny(v, "\\\"\n") {
		return v
	}
	var b strings.Builder
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString("\\\\")
		case '"':
			b.WriteString("\\\"")
		case '\n':
			b.WriteString("\\n")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// formatFloat — Prometheus text format wants `NaN`, `+Inf`, `-Inf`,
// or decimal. strconv's `'g'` format is the recommended encoding.
func formatFloat(v float64) string {
	switch {
	case v != v: // NaN
		return "NaN"
	case v > 1e308:
		return "+Inf"
	case v < -1e308:
		return "-Inf"
	default:
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
}

// ContentType is the Prometheus text exposition format's content
// type. HTTP handlers should set this header so scrapers parse the
// body correctly.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// formatTimestampMS — Prometheus optional per-sample timestamp.
// Unused today (every sample carries the scrape time implicitly)
// but kept so future per-sample timestamp support is a one-line
// change. Eliminates a "future-me will need this" tax.
func formatTimestampMS(t time.Time) string {
	return strconv.FormatInt(t.UnixMilli(), 10)
}

var _ = formatTimestampMS // referenced from internal future use
var _ = fmt.Sprintf       // reserved for future debug paths
