//go:build !js

package rt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"sky-app/rt/telemetry"
)

// ─────────────────────────────────────────────────────────────────
// Universal observability push exporter (sub-app side)
// ─────────────────────────────────────────────────────────────────
//
// When a Sky.Live or Sky.Http.Server process detects it's running
// as a sub-app (SKY_PARENT_URL + SKY_LIVE_NAMESPACE both set,
// typically via MountSubApp's spawn-time env), it starts a
// background goroutine that batches every Log / Counter / Span
// write and POSTs the batch to the parent's
// /_sky/observability/ingest endpoint every
// `SKY_OBSERVABILITY_PUSH_INTERVAL_MS` (default 2000).
//
// Buffer overflow:
//   * Capped at `SKY_OBSERVABILITY_BUFFER` entries per category
//     (default 1024 each). Once full, ADDITIONAL writes are
//     dropped with a single warning log line per minute (logged
//     LOCALLY only — pushing the drop-warning over the wire could
//     spiral).
//   * Parent-unreachable doesn't backpressure the sub-app — pushes
//     fail silently (after one warning per minute), the buffer
//     continues to fill, drops kick in at cap.
//
// Failure modes covered:
//   * Parent down at sub-app start → first push fails; buffer
//     accumulates up to cap; pushes retry on every tick. Sub-app
//     functionality unaffected.
//   * Parent restarts mid-session → pushes resume on next tick
//     after the parent's listener comes back.
//   * Sub-app crashes → in-flight buffer lost (acceptable for
//     observability, not a correctness path).

// PushExporter is the per-process exporter. Only one is active per
// sub-app; subsequent StartPushExporter calls are no-ops.
type PushExporter struct {
	parentURL string
	namespace string
	token     string
	interval  time.Duration
	bufCap    int
	httpC     *http.Client

	mu       sync.Mutex
	logs     []telemetry.LogEntry
	metrics  []pushMetric
	spans    []telemetry.TraceEntry
	dropped  uint64 // any-category drop counter
	lastWarn time.Time

	stopOnce sync.Once
	stopCh   chan struct{}
	// wg is what makes "flushed on shutdown" a fact rather than a hope. The
	// final flush runs on the exporter's OWN goroutine and this WaitGroup is
	// what a stopping caller waits on, so a POST that is still in flight when
	// the signal arrives is completed rather than abandoned mid-request.
	wg sync.WaitGroup
}

// pushMetric — internal buffered representation. We mirror
// telemetry's `MetricSample` shape but with a `delta` field so
// counter pushes don't double-count if the buffer flushes mid-way.
type pushMetric struct {
	Name   string
	Type   string // counter | gauge | histogram
	Delta  float64
	Value  float64
	Labels map[string]string
}

// activeExporter — the singleton; nil when no exporter has been
// started (the common case — only sub-apps spawn one).
var (
	activeExporter atomic.Pointer[PushExporter]
)

// StartPushExporter spins up the background goroutine if
// SKY_PARENT_URL + SKY_LIVE_NAMESPACE are both set in env. Returns
// the exporter (or nil if not in sub-app mode / already started).
//
// Idempotent. Called once from each server-runtime startup
// (Sky.Live `liveAppRun` + Sky.Http.Server `Server_listen`) so
// every Sky binary auto-wires itself when spawned as a sub-app.
func StartPushExporter() *PushExporter {
	if existing := activeExporter.Load(); existing != nil {
		return existing
	}
	parent := os.Getenv("SKY_PARENT_URL")
	ns := skyGetenv("LIVE_NAMESPACE")
	if parent == "" || ns == "" {
		return nil // standalone — no parent to push to
	}
	// The Sky Console is the observability VIEWER, not a viewed
	// app. Its own TEA loop (poll → GotLogs / GotOverview / Tick →
	// re-render) would otherwise federate a `msg_dispatch` log + a
	// `msg` span into the parent's ring every poll interval —
	// observability observing itself, drowning the real app's
	// activity in console-poll noise. The console keeps its
	// telemetry local; it never pushes.
	if ns == "console" {
		return nil
	}
	intervalMs := 2000
	if v := os.Getenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS"); v != "" {
		if n, ok := parsePositiveInt(v); ok {
			intervalMs = n
		}
	}
	bufCap := 1024
	if v := os.Getenv("SKY_OBSERVABILITY_BUFFER"); v != "" {
		if n, ok := parsePositiveInt(v); ok {
			bufCap = n
		}
	}
	exp := &PushExporter{
		parentURL: parent,
		namespace: ns,
		token:     os.Getenv("SKY_INGEST_TOKEN"),
		interval:  time.Duration(intervalMs) * time.Millisecond,
		bufCap:    bufCap,
		httpC: &http.Client{
			// Short timeout — observability pushes are fire-and-
			// forget. We never want a slow parent to slow down
			// the sub-app's own work.
			Timeout: 5 * time.Second,
		},
		stopCh: make(chan struct{}),
	}
	if !activeExporter.CompareAndSwap(nil, exp) {
		// Lost a CAS race — another goroutine got there first.
		// Discard our exporter; the active one stays.
		return activeExporter.Load()
	}
	exp.wg.Add(1)
	go exp.run()
	// Registered at exporter START rather than at process init, mirroring the
	// analytics writer and the hub exporter: a hook for a subsystem that never
	// started would be a hook with nothing to flush.
	//
	// Until this existed, StopPushExporter had exactly ONE caller in the whole
	// tree and it was a test. The doc comment said "called from the runtime's
	// signal handler", and nothing called it — so every sub-app dropped up to
	// a full push interval of logs, metrics and spans on every single deploy,
	// silently, while claiming in its own documentation not to. A flush that
	// nothing calls is not a flush.
	RegisterShutdownHook("observability-push", func(ctx context.Context) {
		StopPushExporterContext(ctx)
	})
	return exp
}

// StopPushExporter halts the exporter cleanly + flushes any pending buffer.
// Idempotent. Blocks until the final push has been attempted.
func StopPushExporter() {
	StopPushExporterContext(context.Background())
}

// StopPushExporterContext is StopPushExporter bounded by a shutdown budget.
//
// # Why the flush moved off this goroutine
//
// It used to `close(stopCh)` and then call `exp.flush()` on the CALLER's
// goroutine, described as a "best-effort final flush". Both halves leaked:
//
//   - Closing stopCh does not wait for `run` to return. If a tick had just
//     fired, `run` was inside flush → send → an outstanding http.Post, and
//     this function returned while that request was still in flight — the
//     process then exited out from under it.
//
//   - Because `run` and the caller could be in `flush` simultaneously, the
//     buffer swap raced: each took a share of the pending entries and the
//     caller's share was whatever happened to arrive after the other swap.
//
// The final flush now runs on the exporter's own goroutine, preserving the
// single-writer property, and this function waits for that goroutine to
// finish. A drain that outruns the budget is REPORTED rather than dropped in
// silence, because "we lost this deploy's tail" is exactly the fact an
// operator needs and cannot recover afterwards.
func StopPushExporterContext(ctx context.Context) {
	exp := activeExporter.Load()
	if exp == nil {
		return
	}
	exp.stopOnce.Do(func() {
		close(exp.stopCh)
	})
	drained := make(chan struct{})
	go func() {
		exp.wg.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-ctx.Done():
		exp.mu.Lock()
		n := len(exp.logs) + len(exp.metrics) + len(exp.spans)
		exp.mu.Unlock()
		if n > 0 {
			logStructured("warn", "observability.push_shutdown_incomplete",
				"detail", "the shutdown budget expired before the push buffer drained",
				"unwritten", itoa64(int64(n)),
				"parent", exp.parentURL)
		}
	}
}

// Active returns the running exporter for the runtime hooks to
// dual-write to. Returns nil when no exporter is active (standalone
// mode), and the runtime helpers fall through to local-only.
func ActivePushExporter() *PushExporter {
	return activeExporter.Load()
}

// run — background batcher. Drains the buffer every `interval`, and once more
// on the way out.
func (e *PushExporter) run() {
	defer e.wg.Done()
	tick := time.NewTicker(e.interval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			e.flush()
		case <-e.stopCh:
			// The final push, on the single writer goroutine. Everything
			// buffered before the stop is sent before this returns, and
			// StopPushExporterContext waits on the WaitGroup — which is what
			// makes the shutdown flush a guarantee instead of a race between
			// the caller and this loop.
			e.flush()
			return
		}
	}
}

// flush sends whatever's buffered RIGHT NOW. Swaps the buffer
// atomically so the producer goroutine isn't blocked while we
// marshal + POST.
func (e *PushExporter) flush() {
	e.mu.Lock()
	if len(e.logs) == 0 && len(e.metrics) == 0 && len(e.spans) == 0 {
		e.mu.Unlock()
		return
	}
	logs := e.logs
	mets := e.metrics
	spans := e.spans
	e.logs = nil
	e.metrics = nil
	e.spans = nil
	e.mu.Unlock()
	e.send(logs, mets, spans)
}

// send marshals + POSTs. On any error logs a single warning (rate-
// limited to 1/min so a downed parent doesn't spam stderr); on
// success returns silently.
func (e *PushExporter) send(logs []telemetry.LogEntry, mets []pushMetric, spans []telemetry.TraceEntry) {
	payload := IngestPayload{Namespace: e.namespace}
	for _, l := range logs {
		payload.Logs = append(payload.Logs, logEntryToWire(l))
	}
	for _, m := range mets {
		payload.Metrics = append(payload.Metrics, IngestMetric{
			Name:   m.Name,
			Type:   m.Type,
			Delta:  m.Delta,
			Value:  m.Value,
			Labels: m.Labels,
		})
	}
	for _, s := range spans {
		payload.Spans = append(payload.Spans, traceToWire(s))
	}
	body, err := json.Marshal(payload)
	if err != nil {
		e.warnRate("[sky.observability] marshal failed: " + err.Error())
		return
	}
	url := e.parentURL + "/_sky/observability/ingest"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		e.warnRate("[sky.observability] request build failed: " + err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if e.token != "" {
		req.Header.Set("X-Sky-Ingest-Token", e.token)
	}
	resp, err := e.httpC.Do(req)
	if err != nil {
		e.warnRate("[sky.observability] push failed: " + err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		e.warnRate(fmt.Sprintf("[sky.observability] push got status %d", resp.StatusCode))
	}
}

// PushLog buffers a log entry for the next flush. Called from the
// runtime's Log_* helpers when an exporter is active. Drops with a
// rate-limited warning when the buffer is full.
func (e *PushExporter) PushLog(entry telemetry.LogEntry) {
	if e == nil {
		return
	}
	// Byte-bound before buffering: the buffer is count-capped, but a
	// slot holding a 1 MiB request path verbatim would let 3 × 1024
	// buffered entries pin gigabytes on a sub-app whose parent is
	// down (telemetry/limits.go).
	entry = telemetry.BoundLogEntry(entry)
	e.mu.Lock()
	if len(e.logs) >= e.bufCap {
		e.dropped++
		e.mu.Unlock()
		e.warnRate(fmt.Sprintf("[sky.observability] log buffer full (cap=%d); dropping entries", e.bufCap))
		return
	}
	e.logs = append(e.logs, entry)
	e.mu.Unlock()
}

// PushMetric buffers a metric point. Counters use `delta`, gauges
// use `value`, histograms use `value` (treated as a single
// observation).
func (e *PushExporter) PushMetric(name, mtype string, delta, value float64, labels map[string]string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	if len(e.metrics) >= e.bufCap {
		e.dropped++
		e.mu.Unlock()
		e.warnRate(fmt.Sprintf("[sky.observability] metric buffer full (cap=%d); dropping entries", e.bufCap))
		return
	}
	e.metrics = append(e.metrics, pushMetric{
		Name: name, Type: mtype, Delta: delta, Value: value,
		Labels: copyMap(telemetry.BoundLabels(labels)),
	})
	e.mu.Unlock()
}

// PushSpan buffers a trace span.
func (e *PushExporter) PushSpan(span telemetry.TraceEntry) {
	if e == nil {
		return
	}
	span = telemetry.BoundTraceEntry(span)
	e.mu.Lock()
	if len(e.spans) >= e.bufCap {
		e.dropped++
		e.mu.Unlock()
		e.warnRate(fmt.Sprintf("[sky.observability] span buffer full (cap=%d); dropping entries", e.bufCap))
		return
	}
	e.spans = append(e.spans, span)
	e.mu.Unlock()
}

// warnRate logs to stderr at most once per minute. Sub-app
// telemetry failures shouldn't spam the user's terminal.
func (e *PushExporter) warnRate(msg string) {
	e.mu.Lock()
	now := time.Now()
	if now.Sub(e.lastWarn) < time.Minute {
		e.mu.Unlock()
		return
	}
	e.lastWarn = now
	e.mu.Unlock()
	fmt.Fprintln(os.Stderr, msg)
}

// ─── helpers ────────────────────────────────────────────────────

func logEntryToWire(l telemetry.LogEntry) IngestLog {
	out := IngestLog{
		Level:     l.Level,
		Message:   l.Message,
		ReqID:     l.ReqID,
		TraceID:   l.TraceID,
		SpanID:    l.SpanID,
		Route:     l.Route,
		Status:    l.Status,
		LatencyMS: l.LatencyMS,
		ErrorStr:  l.ErrorStr,
		Fields:    l.Fields,
	}
	if !l.TS.IsZero() {
		out.TS = l.TS.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func traceToWire(s telemetry.TraceEntry) IngestSpan {
	startMs := s.StartTime.UnixNano() / int64(time.Millisecond)
	durMs := int64(0)
	if !s.EndTime.IsZero() && !s.StartTime.IsZero() {
		durMs = s.EndTime.Sub(s.StartTime).Milliseconds()
	}
	return IngestSpan{
		TraceID:       s.TraceID,
		SpanID:        s.SpanID,
		ParentID:      s.ParentID,
		Name:          s.Name,
		Kind:          s.Kind,
		StartMS:       startMs,
		DurationMS:    durMs,
		Attributes:    s.Attributes,
		StatusCode:    s.StatusCode,
		StatusMessage: s.StatusMessage,
	}
}

func copyMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// parsePositiveInt lives in live.go; we share the implementation.

// ─────────────────────────────────────────────────────────────────
// Dual-write helpers — record locally + push to parent when active
// ─────────────────────────────────────────────────────────────────
//
// Use these from every runtime call-site that updates telemetry. The
// local store always sees the write (so `/_sky/metrics` on the
// sub-app itself stays accurate when accessed directly through the
// proxy); the push exporter — if any — also queues a wire payload so
// the parent's store aggregates with `subapp=<namespace>` labelling.
//
// Pre-existing direct calls to `telemetry.Default().Add` /
// `.SetGauge` etc. should be migrated to these wrappers over time;
// migration site by site is fine — sites still calling telemetry
// directly just won't surface on the parent.

// RecordCounter inc/add — counters take a `delta`.
func RecordCounter(name string, labels map[string]string, delta float64) {
	telemetry.Default().Add(name, labels, delta)
	if exp := ActivePushExporter(); exp != nil {
		exp.PushMetric(name, "counter", delta, 0, labels)
	}
}

// RecordGauge — gauges take an absolute `value`.
func RecordGauge(name string, labels map[string]string, value float64) {
	telemetry.Default().SetGauge(name, labels, value)
	if exp := ActivePushExporter(); exp != nil {
		exp.PushMetric(name, "gauge", 0, value, labels)
	}
}

// RecordHistogram — histograms take a single observation.
func RecordHistogram(name string, labels map[string]string, value float64) {
	telemetry.Default().Observe(name, labels, value)
	if exp := ActivePushExporter(); exp != nil {
		exp.PushMetric(name, "histogram", 0, value, labels)
	}
}

// RecordTrace — append a span to the local trace ring + push.
func RecordTrace(span telemetry.TraceEntry) {
	telemetry.Default().AppendTrace(span)
	if exp := ActivePushExporter(); exp != nil {
		exp.PushSpan(span)
	}
}

// RecordLog — append a structured log entry to the local ring +
// push. Sky-side `Log.*` already flows through `logEmit` which
// dual-writes; this is for Go-side runtime call sites (HTTP access
// log, msg dispatch log, etc.) that build LogEntry directly.
func RecordLog(entry telemetry.LogEntry) {
	telemetry.Default().AppendLog(entry)
	if exp := ActivePushExporter(); exp != nil {
		exp.PushLog(entry)
	}
}
