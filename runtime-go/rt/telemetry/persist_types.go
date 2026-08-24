package telemetry

import "time"

// persistEntry — a single record queued for the flusher goroutine.
// The `kind` field discriminates the variant; only the matching
// payload field is populated per entry.
type persistEntry struct {
	kind   string // "log" | "metric" | "span"
	log    LogEntry
	metric persistMetric
	span   TraceEntry
}

type persistMetric struct {
	name   string
	labels map[string]string
	value  float64
	// mtype is the metric family — "counter" | "gauge" | "histogram". It is
	// NOT persisted (the telemetry_metric schema has no type column); it only
	// tells the flusher which rows are safe to window-coalesce. Counters
	// persist a cumulative value, so all but the last row per (name,labels)
	// window are redundant — losslessly droppable. Gauges (spiky) and
	// histograms (per-observation, rebuilt by the out-of-repo SkyDeploy
	// reader) are never coalesced.
	mtype string
	// hist — for a histogram observation, the live in-RAM series (source of
	// truth for the cumulative bucket vector). Carried so the flusher can
	// snapshot it at a window boundary when histogram coalescing is on; nil
	// for non-histogram metrics and unused when the histogram window is off
	// (then the raw per-observation row is persisted, as before). The series
	// is never deleted, so the pointer is stable for the process lifetime.
	hist       *histogramSeries
	observedAt time.Time
}
