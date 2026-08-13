// Package telemetry implements Sky's Hot-tier observability storage:
// in-memory ring buffers for logs + traces and lock-free counters /
// gauges / histograms for metrics. Backs the /_sky/metrics Prometheus
// endpoint, the /_sky/console dashboard (Phase 1.1b), and the
// structured-log access trail.
//
// Design (full reasoning in docs/v1-rfc/1-observability.md):
//
//   - DEFAULT-ON, zero configuration. AI-generated Sky apps get
//     observability for free.
//
//   - In-memory only. ~7 MB RAM steady-state. Never writes to the
//     user's SQLite database. The Warm tier (separate
//     `_sky/telemetry.db`) and Cold tier (OTLP export) are opt-in
//     on top of this.
//
//   - Lock-free reads via atomic snapshots. The /_sky/console
//     dashboard polls every second; the hot path (Sky.Live dispatch,
//     HTTP middleware) must not block on read traffic.
//
//   - High-cardinality safe: each metric's label combinations are
//     capped at 10,000 entries (Prometheus convention). Beyond the
//     cap we drop new combinations with a one-shot warning, instead
//     of unbounded growth.
//
//   - Tick noise: counters and histograms always bump; log lines
//     emit only when the dispatcher decides (state-change-based —
//     see dispatcher integration in Step 5).
package telemetry

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Store is the per-process telemetry buffer. One singleton per Sky
// binary; the `Default` global is initialised in `init()` and is what
// the HTTP middleware / Live dispatcher write into.
//
// Field grouping mirrors the three Hot-tier sub-buffers:
//
//   - counters / gauges / histograms — Prometheus-shaped metrics
//   - logs — structured log ring buffer
//   - traces — span ring buffer (OTel-shaped)
//
// All operations are safe for concurrent use. Writes take the
// per-buffer mutex; reads use a copy-on-read snapshot.
type Store struct {
	// metric map mutex. Only protects MAP SHAPE — values inside are
	// atomics so updates don't contend on this lock.
	metricsMu sync.RWMutex
	counters  map[seriesKey]*counterSeries
	gauges    map[seriesKey]*gaugeSeries
	hists     map[seriesKey]*histogramSeries

	// Label-combination cap per metric name. Beyond this we drop new
	// label sets to prevent unbounded growth from
	// high-cardinality bugs.
	cardinalityCap   int
	cardinalityWarns sync.Map // map[string]bool — one warning per overflowed metric name

	// Ring buffers.
	logs   *logRing
	traces *traceRing

	// Start time, for `process_start_time_seconds`.
	startedAt time.Time

	// Optional write-through persistence to SQLite (console.db).
	// Enabled when SKY_CONSOLE_DB_PATH is set — SkyDeploy injects it
	// on Pro+ tenants.  Nil = in-RAM only.  See persist.go.
	persistMu           sync.RWMutex
	persist             *persistence
	persistOverflowOnce sync.Map
}

// seriesKey identifies a metric series by name + sorted label string.
// Sorted-label is canonical so {a=1,b=2} and {b=2,a=1} collapse to
// one key.
type seriesKey struct {
	name   string
	labels string // canonical "k1=v1,k2=v2" sorted by key
}

// NewStore allocates a fresh Hot-tier store. Sizes are tuned per the
// RFC volume math (10k log lines, 1k trace spans, ~50 KB metrics —
// total ~7 MB steady-state).
func NewStore() *Store {
	return &Store{
		counters:       make(map[seriesKey]*counterSeries),
		gauges:         make(map[seriesKey]*gaugeSeries),
		hists:          make(map[seriesKey]*histogramSeries),
		cardinalityCap: 10000,
		logs:           newLogRing(10000),
		traces:         newTraceRing(1000),
		startedAt:      time.Now(),
	}
}

// Default is the process-wide telemetry store. Sky.Live's HTTP
// middleware + dispatcher write here; /_sky/metrics + the dashboard
// read here.
//
// Lazy-init via sync.Once so test packages can swap in a custom
// store before the runtime touches the default.
var (
	defaultStore     *Store
	defaultStoreOnce sync.Once
)

// Default returns the process-wide telemetry store. Constructs on
// first call. Tests that want isolation use `NewStore()` directly
// and inject it via the relevant middleware constructor.
func Default() *Store {
	defaultStoreOnce.Do(func() {
		defaultStore = NewStore()
	})
	return defaultStore
}

// ResetDefault is a test-only helper to drop the global store and
// force a fresh one on next Default(). Calling from production code
// is a bug — it loses every counter / log / trace.
func ResetDefault() {
	if defaultStore != nil {
		defaultStore.ClosePersistence()
	}
	defaultStoreOnce = sync.Once{}
	defaultStore = nil
}

// canonicaliseLabels turns a map[string]string into a sorted
// "k1=v1,k2=v2" string. This is the canonical form used as part of
// the seriesKey, so {a=1,b=2} and {b=2,a=1} hash to the same
// series.
//
// Values are NOT escaped — the caller must not put commas or equals
// signs in label values. Sky's emitted metrics use enum-shaped
// values (method name, status code, msg name), so escaping isn't
// load-bearing today. If we ever expose user-controlled label
// values, this gets a real escape pass.
func canonicaliseLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
	}
	return b.String()
}

// ──────────────────────────────────────────────────────────────────
// COUNTERS
// ──────────────────────────────────────────────────────────────────

// counterSeries is a monotonically-increasing float (Prometheus
// counter). Stored as an atomic uint64 holding the bit pattern of the
// float — same trick Prometheus client_golang uses.
type counterSeries struct {
	bits   atomic.Uint64 // float64 bit pattern
	labels map[string]string
}

// Inc adds 1 to the counter for the given metric name + labels.
// Creates the series on first use (within cardinality cap).
func (s *Store) Inc(name string, labels map[string]string) {
	s.Add(name, labels, 1)
}

// Add increments a counter by `delta`. Negative deltas are a usage
// error (counters are monotonic) — silently ignored to avoid
// poisoning the series.
func (s *Store) Add(name string, labels map[string]string, delta float64) {
	if delta < 0 {
		return
	}
	ser := s.counterSeries(name, labels)
	if ser == nil {
		return // cardinality cap hit
	}
	for {
		old := ser.bits.Load()
		nv := float64FromBits(old) + delta
		if ser.bits.CompareAndSwap(old, bitsFromFloat64(nv)) {
			s.enqueuePersist(persistEntry{
				kind: "metric",
				metric: persistMetric{
					name:       name,
					labels:     labels,
					value:      nv,
					observedAt: time.Now(),
				},
			})
			return
		}
	}
}

func (s *Store) counterSeries(name string, labels map[string]string) *counterSeries {
	key := seriesKey{name: name, labels: canonicaliseLabels(labels)}
	s.metricsMu.RLock()
	ser, ok := s.counters[key]
	s.metricsMu.RUnlock()
	if ok {
		return ser
	}
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	// Re-check under write lock.
	if ser, ok := s.counters[key]; ok {
		return ser
	}
	if !s.checkCardinality(name, len(s.counters)) {
		return nil
	}
	ser = &counterSeries{labels: copyLabels(labels)}
	s.counters[key] = ser
	return ser
}

// ──────────────────────────────────────────────────────────────────
// GAUGES
// ──────────────────────────────────────────────────────────────────

// gaugeSeries is a Prometheus gauge — can go up or down. Atomic
// float bit-pattern like counters.
type gaugeSeries struct {
	bits   atomic.Uint64
	labels map[string]string
}

// SetGauge overwrites a gauge value.
func (s *Store) SetGauge(name string, labels map[string]string, v float64) {
	ser := s.gaugeSeries(name, labels)
	if ser == nil {
		return
	}
	ser.bits.Store(bitsFromFloat64(v))
	s.enqueuePersist(persistEntry{
		kind: "metric",
		metric: persistMetric{
			name:       name,
			labels:     labels,
			value:      v,
			observedAt: time.Now(),
		},
	})
}

// AddGauge increments / decrements a gauge.
func (s *Store) AddGauge(name string, labels map[string]string, delta float64) {
	ser := s.gaugeSeries(name, labels)
	if ser == nil {
		return
	}
	for {
		old := ser.bits.Load()
		nv := float64FromBits(old) + delta
		if ser.bits.CompareAndSwap(old, bitsFromFloat64(nv)) {
			s.enqueuePersist(persistEntry{
				kind: "metric",
				metric: persistMetric{
					name:       name,
					labels:     labels,
					value:      nv,
					observedAt: time.Now(),
				},
			})
			return
		}
	}
}

func (s *Store) gaugeSeries(name string, labels map[string]string) *gaugeSeries {
	key := seriesKey{name: name, labels: canonicaliseLabels(labels)}
	s.metricsMu.RLock()
	ser, ok := s.gauges[key]
	s.metricsMu.RUnlock()
	if ok {
		return ser
	}
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	if ser, ok := s.gauges[key]; ok {
		return ser
	}
	if !s.checkCardinality(name, len(s.gauges)) {
		return nil
	}
	ser = &gaugeSeries{labels: copyLabels(labels)}
	s.gauges[key] = ser
	return ser
}

// ──────────────────────────────────────────────────────────────────
// HISTOGRAMS
// ──────────────────────────────────────────────────────────────────

// histogramSeries is a fixed-bucket histogram (Prometheus
// "histogram", not "summary"). Each bucket is an atomic counter; sum
// + count tracked separately for the `_sum` and `_count` exposition
// lines.
//
// The `boundaries` slice is the BucketProfile this series was
// constructed against (from `bucketsFor(name)`). Captured per-series
// so the snapshot + exposition can reflect the right `le` labels
// regardless of which profile a given metric uses.
type histogramSeries struct {
	boundaries BucketProfile
	buckets    []atomic.Uint64 // len(boundaries) + 1 (last is +Inf)
	sumBits    atomic.Uint64   // float64 bits of cumulative sum
	count      atomic.Uint64
	labels     map[string]string
}

// Observe records a single measurement into the histogram. Bumps
// every bucket whose `le` boundary is >= v (Prometheus cumulative
// convention).
func (s *Store) Observe(name string, labels map[string]string, v float64) {
	ser := s.histogramSeries(name, labels)
	if ser == nil {
		return
	}
	for i, b := range ser.boundaries {
		if v <= b {
			ser.buckets[i].Add(1)
		}
	}
	ser.buckets[len(ser.boundaries)].Add(1) // +Inf bucket always bumped
	ser.count.Add(1)
	for {
		old := ser.sumBits.Load()
		ns := float64FromBits(old) + v
		if ser.sumBits.CompareAndSwap(old, bitsFromFloat64(ns)) {
			// Persist the raw observation, NOT the rolling sum — the
			// console UI's histogram rendering rebuilds from
			// per-observation rows.
			s.enqueuePersist(persistEntry{
				kind: "metric",
				metric: persistMetric{
					name:       name,
					labels:     labels,
					value:      v,
					observedAt: time.Now(),
				},
			})
			return
		}
	}
}

func (s *Store) histogramSeries(name string, labels map[string]string) *histogramSeries {
	key := seriesKey{name: name, labels: canonicaliseLabels(labels)}
	s.metricsMu.RLock()
	ser, ok := s.hists[key]
	s.metricsMu.RUnlock()
	if ok {
		return ser
	}
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	if ser, ok := s.hists[key]; ok {
		return ser
	}
	if !s.checkCardinality(name, len(s.hists)) {
		return nil
	}
	bounds := bucketsFor(name)
	ser = &histogramSeries{
		boundaries: bounds,
		buckets:    make([]atomic.Uint64, len(bounds)+1),
		labels:     copyLabels(labels),
	}
	s.hists[key] = ser
	return ser
}

// ──────────────────────────────────────────────────────────────────
// CARDINALITY GUARD
// ──────────────────────────────────────────────────────────────────

// checkCardinality returns false when the metric family has exceeded
// its label-combination cap. Logs ONE warning per offending metric
// name (subsequent overflows are silent).
//
// Bug pattern this guards against: a label that's accidentally
// derived from a user-controlled value (e.g. URL with a user ID
// embedded as a path segment when the route should have used a
// placeholder). Without the cap, each unique URL spawns a fresh
// series and the metrics map balloons until OOM. Prometheus's
// own client libs ship with the same default.
func (s *Store) checkCardinality(name string, current int) bool {
	if current < s.cardinalityCap {
		return true
	}
	if _, loaded := s.cardinalityWarns.LoadOrStore(name, true); !loaded {
		// First overflow on this metric — emit a single warning
		// log line (avoid recursive log → telemetry by writing
		// directly to the ring buffer, not to a logger).
		s.logs.append(LogEntry{
			TS:      time.Now(),
			Level:   "warn",
			Message: "telemetry cardinality cap exceeded; dropping new label combinations",
			Fields: map[string]string{
				"metric": name,
				"cap":    "10000",
			},
		})
	}
	return false
}

// ──────────────────────────────────────────────────────────────────
// SNAPSHOT (for dashboard / exposition)
// ──────────────────────────────────────────────────────────────────

// MetricSample is a single point read out of a counter / gauge /
// histogram for the Prometheus exposition or the dashboard. Mirror
// of OpenMetrics shape.
type MetricSample struct {
	Name    string
	Labels  map[string]string
	Type    string // "counter" | "gauge" | "histogram"
	Value   float64
	Buckets map[float64]uint64 // populated when Type == "histogram"
	Sum     float64            // populated when Type == "histogram"
	Count   uint64             // populated when Type == "histogram"
}

// Snapshot returns a point-in-time view of every metric series.
// O(N) in total series count; called only by /_sky/metrics scrapes
// and the dashboard tick (1Hz default), so the cost is bounded.
//
// Returns sorted by (name, labels) for deterministic exposition —
// makes Prometheus diffing on the scraper side stable.
func (s *Store) Snapshot() []MetricSample {
	s.metricsMu.RLock()
	defer s.metricsMu.RUnlock()
	out := make([]MetricSample, 0, len(s.counters)+len(s.gauges)+len(s.hists))
	for k, ser := range s.counters {
		out = append(out, MetricSample{
			Name:   k.name,
			Labels: copyLabels(ser.labels),
			Type:   "counter",
			Value:  float64FromBits(ser.bits.Load()),
		})
	}
	for k, ser := range s.gauges {
		out = append(out, MetricSample{
			Name:   k.name,
			Labels: copyLabels(ser.labels),
			Type:   "gauge",
			Value:  float64FromBits(ser.bits.Load()),
		})
	}
	for k, ser := range s.hists {
		bs := make(map[float64]uint64, len(ser.boundaries)+1)
		for i, b := range ser.boundaries {
			bs[b] = ser.buckets[i].Load()
		}
		// +Inf bucket
		out = append(out, MetricSample{
			Name:    k.name,
			Labels:  copyLabels(ser.labels),
			Type:    "histogram",
			Buckets: bs,
			Sum:     float64FromBits(ser.sumBits.Load()),
			Count:   ser.count.Load(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return canonicaliseLabels(out[i].Labels) <
			canonicaliseLabels(out[j].Labels)
	})
	return out
}

// StartedAt returns the time the store was created (== process
// start time, approximately). Used for the `process_start_time_seconds`
// Prometheus convention metric.
func (s *Store) StartedAt() time.Time {
	return s.startedAt
}

// ──────────────────────────────────────────────────────────────────
// LOG / TRACE accessors (full impl in log.go / trace.go)
// ──────────────────────────────────────────────────────────────────

// AppendLog stores a single structured log entry in the ring buffer.
// O(1). Safe for concurrent writers. Oldest entry is overwritten
// when the buffer fills.
func (s *Store) AppendLog(e LogEntry) {
	s.logs.append(e)
	s.enqueuePersist(persistEntry{kind: "log", log: e})
}

// RecentLogs returns up to `limit` most-recent log entries, newest
// first. Caller-owned copy — safe to mutate.
func (s *Store) RecentLogs(limit int) []LogEntry {
	return s.logs.recent(limit)
}

// AppendTrace stores a span in the trace ring buffer.
func (s *Store) AppendTrace(e TraceEntry) {
	s.traces.append(e)
	s.enqueuePersist(persistEntry{kind: "span", span: e})
}

// RecentTraces returns up to `limit` most-recent traces, newest
// first.
func (s *Store) RecentTraces(limit int) []TraceEntry {
	return s.traces.recent(limit)
}

// ──────────────────────────────────────────────────────────────────
// HELPERS
// ──────────────────────────────────────────────────────────────────

func copyLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
