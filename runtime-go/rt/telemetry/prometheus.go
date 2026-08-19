package telemetry

import (
	"fmt"
	"io"
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
func (s *Store) WriteProm(w io.Writer) {
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

// helpTexts — short HELP descriptions for each kernel metric. Loose
// match: helpTexts[name] looked up, else a default. Keep in sync
// with the RFC's metric list.
var helpTexts = map[string]string{
	"sky_live_requests_total":        "HTTP requests handled, partitioned by method/route/status",
	"sky_live_msg_total":             "Sky.Live Msg dispatches, partitioned by name/outcome/noop",
	"sky_live_sse_connections_total": "Sky.Live SSE connection lifecycle events",
	"sky_live_sessions_active":       "Active Sky.Live sessions (open SSE channels)",
	"sky_live_request_seconds":       "HTTP request latency histogram (seconds)",
	"sky_live_msg_seconds":           "Msg dispatch + update + diff latency histogram (seconds)",
	"sky_db_query_total":             "Std.Db queries executed, partitioned by table/outcome",
	"sky_db_query_seconds":           "Std.Db query latency histogram (seconds)",
	"sky_db_pool_in_use":             "Std.Db connections currently leased",
	"sky_db_pool_idle":               "Std.Db connections currently idle",
	"sky_jobs_total":                 "Std.Jobs runs, partitioned by queue/outcome",
	"sky_jobs_duration_seconds":      "Std.Jobs job duration histogram (seconds)",
	"sky_jobs_inflight":              "Std.Jobs currently-running jobs",
	"sky_jobs_queue_depth":           "Std.Jobs pending jobs per queue",
	"sky_ffi_calls_total":            "Go FFI invocations, partitioned by pkg/outcome",
	"sky_telemetry_buffer_used":      "Hot-tier ring buffer occupancy (entries)",
	"process_start_time_seconds":     "Process start time (seconds since epoch)",
}

func writeHeader(w io.Writer, name, mtype string) {
	help := helpTexts[name]
	if help == "" {
		help = "Sky metric"
	}
	io.WriteString(w, "# HELP ")
	io.WriteString(w, name)
	io.WriteString(w, " ")
	io.WriteString(w, help)
	io.WriteString(w, "\n# TYPE ")
	io.WriteString(w, name)
	io.WriteString(w, " ")
	io.WriteString(w, mtype)
	io.WriteString(w, "\n")
}

func writeLine(w io.Writer, name string, labels map[string]string, v float64) {
	io.WriteString(w, name)
	writeLabels(w, labels, "", "")
	io.WriteString(w, " ")
	io.WriteString(w, formatFloat(v))
	io.WriteString(w, "\n")
}

func writeHistogram(w io.Writer, name string, sm MetricSample) {
	// Stream each row straight to the writer — no intermediate slice, so a
	// /_sky/metrics scrape allocates nothing here. The row SHAPE is defined
	// once in emitHistogramSeries and shared with the persist exploder.
	emitHistogramSeries(name, sm, func(rowName, leValue string, value float64, isCount bool) {
		io.WriteString(w, rowName)
		if leValue != "" {
			writeLabels(w, sm.Labels, "le", leValue)
		} else {
			writeLabels(w, sm.Labels, "", "")
		}
		io.WriteString(w, " ")
		if isCount {
			io.WriteString(w, strconv.FormatUint(uint64(value), 10))
		} else {
			io.WriteString(w, formatFloat(value))
		}
		io.WriteString(w, "\n")
	})
}

// emitHistogramSeries renders a histogram MetricSample as OpenMetrics rows —
// `<name>_bucket{le=…}` (cumulative counts), `<name>_bucket{le="+Inf"}`,
// `<name>_sum`, `<name>_count` — invoking `emit` once per row. It is the SINGLE
// definition of the histogram wire shape, shared by the Prometheus text writer
// (above) and the persist exploder (persist.go), so the two can never drift.
//
// `emit(rowName, leValue, value, isCount)`: leValue is "" for _sum/_count and
// the raw name for a bucket ("+Inf" for the inf bucket); isCount marks integer
// rows so a text sink formats them without a decimal point.
//
// CLAMP (load-bearing for the persist path): Snapshot reads each bucket / sum /
// count as a SEPARATE atomic load (store.go), so a concurrent Observe can skew
// them — a finite bucket momentarily above a higher `le`, or above count —
// yielding a NON-monotonic cumulative vector. On a live scrape that self-heals
// next tick; PERSISTED and later diffed as window deltas it is a permanent
// malformed-histogram artifact (a bucket share > 1). Emitting a running maximum
// guarantees a monotonic non-decreasing vector. Absent skew the running max
// equals each value, so this is a no-op — the text output is byte-identical to
// the pre-refactor writer for any consistent snapshot.
func emitHistogramSeries(name string, sm MetricSample, emit func(rowName, leValue string, value float64, isCount bool)) {
	keys := make([]float64, 0, len(sm.Buckets))
	for k := range sm.Buckets {
		keys = append(keys, k)
	}
	sort.Float64s(keys)
	var running uint64
	for _, b := range keys {
		if c := sm.Buckets[b]; c > running {
			running = c
		}
		emit(name+"_bucket", formatFloat(b), float64(running), true)
	}
	// +Inf == count, clamped up to the running finite maximum so the whole
	// cumulative vector is monotonic non-decreasing.
	inf := sm.Count
	if running > inf {
		inf = running
	}
	emit(name+"_bucket", "+Inf", float64(inf), true)
	emit(name+"_sum", "", sm.Sum, false)
	emit(name+"_count", "", float64(inf), true)
}

// writeLabels emits the Prometheus label block — `{k1="v1",k2="v2"}`.
// When `extraK` is non-empty, it's appended as an additional label
// (used for histogram `le=...`).
//
// Label values are escaped per the Prometheus text spec: backslash,
// double-quote, newline. We do NOT escape commas/equals signs in
// values — those aren't required by the spec and the histogram +Inf
// path emits a raw "+Inf" deliberately.
func writeLabels(w io.Writer, labels map[string]string, extraK, extraV string) {
	if len(labels) == 0 && extraK == "" {
		return
	}
	io.WriteString(w, "{")
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	first := true
	for _, k := range keys {
		if !first {
			io.WriteString(w, ",")
		}
		io.WriteString(w, k)
		io.WriteString(w, "=\"")
		io.WriteString(w, escapePromLabelValue(labels[k]))
		io.WriteString(w, "\"")
		first = false
	}
	if extraK != "" {
		if !first {
			io.WriteString(w, ",")
		}
		io.WriteString(w, extraK)
		io.WriteString(w, "=\"")
		io.WriteString(w, escapePromLabelValue(extraV))
		io.WriteString(w, "\"")
	}
	io.WriteString(w, "}")
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
