// Package hub — bridge from hub.Store → rt.HubStoreReader.
//
// v0.16.4 Option B B4. `rt/` declares the interface (see
// `runtime-go/rt/hub_bridge.go`); this file implements it as a
// thin wrapper around `*Store`. The boundary uses JSON strings
// for filter/row payloads so `rt/` doesn't need to import `hub/`
// (would form a cycle — `hub/` already imports `rt/` for
// logging + telemetry).
//
// JSON marshalling is the cost; per-row processing is dominated by
// the SQLite query, so the marshal overhead is invisible (a few
// hundred bytes per response).

package hub

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	rt "sky-app/rt"
)

// AsReader returns a `rt.HubStoreReader` view of this store. The
// hub daemon's Run() calls `rt.SetHubStore(store.AsReader())` once
// the store is open; subsequent Sky-side Hub_* kernel calls route
// here.
func (s *Store) AsReader() rt.HubStoreReader {
	return &storeReader{s: s}
}

// storeReader is the concrete `rt.HubStoreReader`. One per Store.
type storeReader struct {
	s *Store
}

// Counts delegates straight to Store.Counts.
func (r *storeReader) Counts() (logs, metrics, spans int, err error) {
	return r.s.Counts()
}

// hubLogFilter mirrors the Sky-side LogFilter shape (camelCase
// fields). Sent in via `rt.encodeFilterJSON` per call.
type hubLogFilter struct {
	Query     string `json:"query"`
	Session   string `json:"session"`
	ShowDebug bool   `json:"showDebug"`
	ShowInfo  bool   `json:"showInfo"`
	ShowWarn  bool   `json:"showWarn"`
	ShowError bool   `json:"showError"`
}

// hubLogRow is the wire row shape the console UI's LogEntry record
// decodes against (matches `sky-bundled/console/src/State.sky`'s
// `type alias LogEntry` field set). Field tags use lowerCamel so
// `narrowMapToStruct` resolves them off the runtime's lower-first
// probe path.
type hubLogRow struct {
	Time      string  `json:"time"`
	Level     string  `json:"level"`
	Message   string  `json:"message"`
	Subapp    string  `json:"subapp"`
	ReqID     string  `json:"reqId"`
	SessionID string  `json:"sessionId"`
	UserLabel string  `json:"userLabel"`
	Route     string  `json:"route"`
	Status    float64 `json:"status"`
	LatencyMS float64 `json:"latencyMs"`
}

// hubMetricRow mirrors State.MetricRow.
type hubMetricRow struct {
	Name   string  `json:"name"`
	Typ    string  `json:"typ"`
	Labels string  `json:"labels"`
	Value  float64 `json:"value"`
	Sum    float64 `json:"sum"`
	Count  float64 `json:"count"`
}

// hubTraceRow mirrors State.TraceRow.
type hubTraceRow struct {
	TraceID    string  `json:"traceId"`
	SpanID     string  `json:"spanId"`
	ParentID   string  `json:"parentId"`
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	StartTime  string  `json:"startTime"`
	DurationMs float64 `json:"durationMs"`
	Status     string  `json:"status"`
}

// hubErrorRow mirrors State.ErrorRow (aggregated bad-status logs).
type hubErrorRow struct {
	Count   int    `json:"count"`
	Message string `json:"message"`
}

// QueryLogsJSON parses the Sky-side filter JSON, translates to a
// hub.LogFilter, runs QueryLogs, and emits a JSON array of
// hubLogRow values.
//
// `showDebug/Info/Warn/Error` map to the store's `Level` filter the
// same way the embedded console's HTTP endpoint does (server-side
// when exactly one level is selected; client-side / no-filter
// otherwise — the store-side filter only accepts ONE level at a
// time, so this matches behaviour).
func (r *storeReader) QueryLogsJSON(filterJSON string) (string, error) {
	var f hubLogFilter
	if filterJSON != "" {
		if err := json.Unmarshal([]byte(filterJSON), &f); err != nil {
			return "", fmt.Errorf("filter unmarshal: %w", err)
		}
	}
	storeFilter := LogFilter{
		Limit: 200,
		Level: pickSingleLevel(f),
	}
	rows, err := r.s.QueryLogs(storeFilter)
	if err != nil {
		return "", err
	}
	// Free-text + session filters are applied client-side because
	// the store's where-clause doesn't have a `LIKE` arm yet —
	// match the embedded console's UI behaviour.
	out := make([]hubLogRow, 0, len(rows))
	for _, row := range rows {
		if f.Query != "" && !logMatchesQuery(row, f.Query) {
			continue
		}
		if f.Session != "" && row.Attrs["session_id"] != f.Session {
			continue
		}
		out = append(out, toHubLogRow(row))
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// pickSingleLevel returns "" (no filter) when zero or two-plus
// levels are toggled — the store applies an `=` filter so we can
// only express "exactly one level" at a time. Mirror of the
// existing HTTP endpoint behaviour.
func pickSingleLevel(f hubLogFilter) string {
	count := 0
	chosen := ""
	if f.ShowDebug {
		count++
		chosen = "debug"
	}
	if f.ShowInfo {
		count++
		chosen = "info"
	}
	if f.ShowWarn {
		count++
		chosen = "warn"
	}
	if f.ShowError {
		count++
		chosen = "error"
	}
	if count == 1 {
		return chosen
	}
	return ""
}

func logMatchesQuery(row LogRow, q string) bool {
	ql := strings.ToLower(q)
	if strings.Contains(strings.ToLower(row.Message), ql) {
		return true
	}
	if strings.Contains(strings.ToLower(row.ServiceName), ql) {
		return true
	}
	return false
}

func toHubLogRow(row LogRow) hubLogRow {
	out := hubLogRow{
		Time:    row.Time.UTC().Format(time.RFC3339),
		Level:   row.Level,
		Message: row.Message,
		Subapp:  row.ServiceName,
	}
	if row.Attrs != nil {
		out.ReqID = row.Attrs["req_id"]
		out.SessionID = row.Attrs["session_id"]
		out.UserLabel = row.Attrs["user_label"]
		out.Route = row.Attrs["route"]
		// status / latencyMs are stored as attrs in the existing
		// telemetry encoder; ignore for now (display path tolerates 0).
	}
	return out
}

// QueryMetricsJSON returns the most recent metric rows as a JSON
// array matching State.MetricRow.
func (r *storeReader) QueryMetricsJSON() (string, error) {
	rows, err := r.s.QueryMetrics(MetricFilter{Limit: 200})
	if err != nil {
		return "", err
	}
	out := make([]hubMetricRow, 0, len(rows))
	for _, m := range rows {
		labels := ""
		if len(m.Attrs) > 0 {
			parts := make([]string, 0, len(m.Attrs))
			for k, v := range m.Attrs {
				parts = append(parts, k+"="+v)
			}
			labels = strings.Join(parts, ", ")
		}
		out = append(out, hubMetricRow{
			Name:   m.Name,
			Typ:    m.Type,
			Labels: labels,
			Value:  m.Value,
			Sum:    0,
			Count:  0,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// QuerySpansJSON returns spans as TraceRow JSON.
func (r *storeReader) QuerySpansJSON() (string, error) {
	rows, err := r.s.QuerySpans(SpanFilter{Limit: 100})
	if err != nil {
		return "", err
	}
	out := make([]hubTraceRow, 0, len(rows))
	for _, sp := range rows {
		durMs := 0.0
		if !sp.StartTime.IsZero() && !sp.EndTime.IsZero() {
			durMs = float64(sp.EndTime.Sub(sp.StartTime)) / float64(time.Millisecond)
		}
		status := ""
		if sp.Attrs != nil {
			status = sp.Attrs["status"]
		}
		out = append(out, hubTraceRow{
			TraceID:    sp.TraceID,
			SpanID:     sp.SpanID,
			ParentID:   sp.ParentID,
			Name:       sp.Name,
			Kind:       sp.ServiceName,
			StartTime:  sp.StartTime.UTC().Format(time.RFC3339),
			DurationMs: durMs,
			Status:     status,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// QueryErrorsJSON aggregates error-level logs into ErrorRow shape.
// v0.16.4 ships the simplest possible grouping (count by message).
// Future cycles can layer in span error-rates + http-status
// classification (B5/B6 territory).
func (r *storeReader) QueryErrorsJSON() (string, error) {
	rows, err := r.s.QueryLogs(LogFilter{Level: "error", Limit: 500})
	if err != nil {
		return "", err
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.Message]++
	}
	out := make([]hubErrorRow, 0, len(counts))
	for msg, c := range counts {
		out = append(out, hubErrorRow{Count: c, Message: msg})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Services delegates to Store.Services.
func (r *storeReader) Services() ([]string, error) {
	return r.s.Services()
}

// hubServiceStatRow mirrors `sky-bundled/console/src/State.sky`'s
// `ServiceStat` typed record. Field tags use lowerCamel so
// `narrowMapToStruct` (rt.Coerce's struct narrower) resolves them
// off its lower-first probe path.
type hubServiceStatRow struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	ReqsPerSec float64   `json:"reqsPerSec"`
	P95Ms      float64   `json:"p95Ms"`
	ErrorRate  float64   `json:"errorRate"`
	SparkRps   []float64 `json:"sparkRps"`
	SparkP95   []float64 `json:"sparkP95"`
}

// statsWindow is the size of the per-service aggregation window.
// 60 s matches the typical Cloud-Run-style "recent activity" look-
// back and gives the sparkline 30 2-second buckets at most.
const statsWindow = 60 * time.Second

// statsBucketCount controls the sparkline resolution. 30 ×
// 2 s = 60 s window. Plenty for a UI thumbnail; cheaper than
// 1 s buckets which would double the wire payload.
const statsBucketCount = 30

// ServiceStatsJSON aggregates the last `statsWindow` seconds of
// telemetry per service into the wire shape consumed by the Sky
// console's multi-service Overview (v0.16.4 B5).
//
// Aggregation strategy:
//   - req/s        : count of telemetry_log rows in the window /
//                    window-seconds. Logs are the cheapest signal
//                    that's always populated; metrics/spans not
//                    every service emits.
//   - p95          : sorted "latency_ms" / "duration_ms" attrs
//                    from spans + logs, take the 95th percentile.
//                    Zero when no latency observations recorded.
//   - error rate   : ratio of error-level log rows over total log
//                    rows in the window. Same denominator as req/s
//                    so the rate is comparable.
//   - sparkRps/P95 : bucketed series, oldest → newest.
//
// One round-trip per call — a handful of indexed queries over the
// (service_name, time DESC) indexes; well-bounded.
func (r *storeReader) ServiceStatsJSON() (string, error) {
	services, err := r.s.Services()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	since := now.Add(-statsWindow)
	rows := make([]hubServiceStatRow, 0, len(services))
	for _, svc := range services {
		if svc == "" {
			continue
		}
		row, err := r.s.aggregateServiceStat(svc, since, now)
		if err != nil {
			return "", fmt.Errorf("aggregate %s: %w", svc, err)
		}
		rows = append(rows, row)
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// aggregateServiceStat computes a single ServiceStat row by
// running three small SELECTs over the (service_name, time) index.
// All three queries are O(log N + window-rows); the per-call cost
// is dominated by the log count, not the latency parsing.
func (s *Store) aggregateServiceStat(svc string, since, until time.Time) (hubServiceStatRow, error) {
	rows := hubServiceStatRow{
		Name:     svc,
		Status:   "ok",
		SparkRps: make([]float64, statsBucketCount),
		SparkP95: make([]float64, statsBucketCount),
	}

	// 1. Pull every log row in the window — drives req/s, error
	// rate, and the latency observations.
	logRows, err := s.QueryLogs(LogFilter{
		ServiceName: svc,
		Since:       since,
		Until:       until,
		Limit:       10000,
	})
	if err != nil {
		return rows, err
	}

	// 2. Pull every span row in the window — drives the p95
	// latency calc when spans carry duration_ms.
	spanRows, err := s.QuerySpans(SpanFilter{
		ServiceName: svc,
		Since:       since,
		Until:       until,
		Limit:       10000,
	})
	if err != nil {
		return rows, err
	}

	windowSec := until.Sub(since).Seconds()
	if windowSec <= 0 {
		windowSec = 1
	}

	// req/s = total log rows in the window / window-seconds.
	rows.ReqsPerSec = float64(len(logRows)) / windowSec

	// error rate = errors / total. Both numerator + denominator
	// are log-row counts so the ratio is bounded by [0, 1].
	errorCount := 0
	latencies := make([]float64, 0, len(logRows)+len(spanRows))
	for _, l := range logRows {
		if l.Level == "error" {
			errorCount++
		}
		if l.Attrs != nil {
			if v, ok := parseFloatAttr(l.Attrs["latency_ms"]); ok {
				latencies = append(latencies, v)
			}
		}
	}
	if len(logRows) > 0 {
		rows.ErrorRate = float64(errorCount) / float64(len(logRows))
	}

	for _, sp := range spanRows {
		if !sp.StartTime.IsZero() && !sp.EndTime.IsZero() {
			ms := float64(sp.EndTime.Sub(sp.StartTime)) / float64(time.Millisecond)
			if ms > 0 {
				latencies = append(latencies, ms)
			}
		}
	}

	rows.P95Ms = percentile(latencies, 0.95)

	// Bucket logs + span latencies into the sparkline series.
	bucketSize := until.Sub(since) / statsBucketCount
	if bucketSize <= 0 {
		bucketSize = time.Second
	}
	// req/s buckets: count log rows per bucket / bucket-seconds.
	reqCounts := make([]int, statsBucketCount)
	for _, l := range logRows {
		idx := bucketIndex(l.Time, since, bucketSize)
		if idx >= 0 && idx < statsBucketCount {
			reqCounts[idx]++
		}
	}
	bucketSec := bucketSize.Seconds()
	if bucketSec <= 0 {
		bucketSec = 1
	}
	for i, c := range reqCounts {
		rows.SparkRps[i] = float64(c) / bucketSec
	}

	// p95 latency buckets: group every observation by bucket then
	// run percentile per bucket. Skipped buckets stay at zero —
	// the sparkline tolerates a flat tail (matches the "no
	// traffic" reading).
	latBuckets := make([][]float64, statsBucketCount)
	for _, sp := range spanRows {
		if sp.StartTime.IsZero() || sp.EndTime.IsZero() {
			continue
		}
		ms := float64(sp.EndTime.Sub(sp.StartTime)) / float64(time.Millisecond)
		idx := bucketIndex(sp.StartTime, since, bucketSize)
		if idx >= 0 && idx < statsBucketCount && ms > 0 {
			latBuckets[idx] = append(latBuckets[idx], ms)
		}
	}
	for _, l := range logRows {
		if l.Attrs == nil {
			continue
		}
		v, ok := parseFloatAttr(l.Attrs["latency_ms"])
		if !ok {
			continue
		}
		idx := bucketIndex(l.Time, since, bucketSize)
		if idx >= 0 && idx < statsBucketCount {
			latBuckets[idx] = append(latBuckets[idx], v)
		}
	}
	for i, bucket := range latBuckets {
		rows.SparkP95[i] = percentile(bucket, 0.95)
	}

	rows.Status = classifyStatus(rows.ErrorRate)
	return rows, nil
}

// bucketIndex maps an observation timestamp to its sparkline
// bucket index. Returns -1 when the timestamp is outside the
// window (defensive — shouldn't happen given the SQL WHERE clause,
// but a clock skew between writer + reader could produce one).
func bucketIndex(ts time.Time, since time.Time, bucketSize time.Duration) int {
	if bucketSize <= 0 {
		return -1
	}
	off := ts.Sub(since)
	if off < 0 {
		return -1
	}
	return int(off / bucketSize)
}

// parseFloatAttr accepts the attr-bag value strings the runtime
// stores ("3.14" / "42") and returns the parsed float. The bool
// return signals whether the parse succeeded — callers skip the
// observation rather than counting a zero.
func parseFloatAttr(raw string) (float64, bool) {
	if raw == "" {
		return 0, false
	}
	var f float64
	_, err := fmt.Sscanf(raw, "%f", &f)
	if err != nil {
		return 0, false
	}
	return f, true
}

// percentile returns the p-th percentile of vals. Empty input
// returns 0 — the caller treats that as "no observations"
// rather than "latency is zero".
//
// Uses the classic "nearest-rank" definition: index = ceil(p * N).
// Cheaper than linear interpolation; accuracy is sufficient for
// the UI's 1-decimal-place display.
func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// classifyStatus maps the recent error rate into the 3-state pill
// (ok / warn / err) the Overview UI renders. Thresholds match the
// embedded console's existing convention (1% warn / 5% err).
func classifyStatus(errorRate float64) string {
	switch {
	case errorRate > 0.05:
		return "err"
	case errorRate >= 0.01:
		return "warn"
	default:
		return "ok"
	}
}

// ─── v0.16.4 B6 — per-service drill-down readers ──────────────────
//
// These are thin layers over the un-filtered variants above:
// they apply a `WHERE service_name = ?` clause via the existing
// LogFilter / MetricFilter / SpanFilter `ServiceName` field, then
// reuse the same row → wire-shape converter. An empty service
// name short-circuits to the un-filtered reader so callers don't
// need to special-case "all services" — the SQL WHERE clause
// just doesn't apply that arm.

// QueryFilteredLogsJSON narrows the log read to a single service.
// `serviceName == ""` falls through to the un-filtered query.
func (r *storeReader) QueryFilteredLogsJSON(serviceName, filterJSON string) (string, error) {
	return r.QueryFilteredLogsJSONWithTenant(serviceName, "", filterJSON)
}

// QueryFilteredLogsJSONWithTenant is the tenant-scoped variant
// shipped by v0.16.6 #493 part 2c-defense.  `tenantPrefix == ""`
// keeps the v0.16.4 behaviour byte-identical; non-empty applies
// `AND service_name LIKE prefix || '%'` at the SQL layer so the
// SQLite engine — not the caller — enforces the row scope.
func (r *storeReader) QueryFilteredLogsJSONWithTenant(serviceName, tenantPrefix, filterJSON string) (string, error) {
	var f hubLogFilter
	if filterJSON != "" {
		if err := json.Unmarshal([]byte(filterJSON), &f); err != nil {
			return "", fmt.Errorf("filter unmarshal: %w", err)
		}
	}
	storeFilter := LogFilter{
		ServiceName:  serviceName,
		TenantPrefix: tenantPrefix,
		Limit:        200,
		Level:        pickSingleLevel(f),
	}
	rows, err := r.s.QueryLogs(storeFilter)
	if err != nil {
		return "", err
	}
	out := make([]hubLogRow, 0, len(rows))
	for _, row := range rows {
		if f.Query != "" && !logMatchesQuery(row, f.Query) {
			continue
		}
		if f.Session != "" && row.Attrs["session_id"] != f.Session {
			continue
		}
		out = append(out, toHubLogRow(row))
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// QueryFilteredMetricsJSON narrows the metric read to a single
// service. `serviceName == ""` falls through to the un-filtered
// query.
func (r *storeReader) QueryFilteredMetricsJSON(serviceName string) (string, error) {
	return r.QueryFilteredMetricsJSONWithTenant(serviceName, "")
}

// QueryFilteredMetricsJSONWithTenant is the tenant-scoped variant
// (v0.16.6 #493 part 2c-defense).
func (r *storeReader) QueryFilteredMetricsJSONWithTenant(serviceName, tenantPrefix string) (string, error) {
	rows, err := r.s.QueryMetrics(MetricFilter{
		ServiceName:  serviceName,
		TenantPrefix: tenantPrefix,
		Limit:        200,
	})
	if err != nil {
		return "", err
	}
	out := make([]hubMetricRow, 0, len(rows))
	for _, m := range rows {
		labels := ""
		if len(m.Attrs) > 0 {
			parts := make([]string, 0, len(m.Attrs))
			for k, v := range m.Attrs {
				parts = append(parts, k+"="+v)
			}
			labels = strings.Join(parts, ", ")
		}
		out = append(out, hubMetricRow{
			Name:   m.Name,
			Typ:    m.Type,
			Labels: labels,
			Value:  m.Value,
			Sum:    0,
			Count:  0,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// QueryFilteredSpansJSON narrows the span read to a single
// service. `serviceName == ""` falls through to the un-filtered
// query.
func (r *storeReader) QueryFilteredSpansJSON(serviceName string) (string, error) {
	return r.QueryFilteredSpansJSONWithTenant(serviceName, "")
}

// QueryFilteredSpansJSONWithTenant is the tenant-scoped variant
// (v0.16.6 #493 part 2c-defense).
func (r *storeReader) QueryFilteredSpansJSONWithTenant(serviceName, tenantPrefix string) (string, error) {
	rows, err := r.s.QuerySpans(SpanFilter{
		ServiceName:  serviceName,
		TenantPrefix: tenantPrefix,
		Limit:        100,
	})
	if err != nil {
		return "", err
	}
	out := make([]hubTraceRow, 0, len(rows))
	for _, sp := range rows {
		durMs := 0.0
		if !sp.StartTime.IsZero() && !sp.EndTime.IsZero() {
			durMs = float64(sp.EndTime.Sub(sp.StartTime)) / float64(time.Millisecond)
		}
		status := ""
		if sp.Attrs != nil {
			status = sp.Attrs["status"]
		}
		out = append(out, hubTraceRow{
			TraceID:    sp.TraceID,
			SpanID:     sp.SpanID,
			ParentID:   sp.ParentID,
			Name:       sp.Name,
			Kind:       sp.ServiceName,
			StartTime:  sp.StartTime.UTC().Format(time.RFC3339),
			DurationMs: durMs,
			Status:     status,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// QueryFilteredErrorsJSON narrows the error rollup to a single
// service. `serviceName == ""` falls through to the un-filtered
// query. Aggregation strategy mirrors QueryErrorsJSON — count by
// message over the most recent error-level log rows.
func (r *storeReader) QueryFilteredErrorsJSON(serviceName string) (string, error) {
	return r.QueryFilteredErrorsJSONWithTenant(serviceName, "")
}

// QueryFilteredErrorsJSONWithTenant is the tenant-scoped variant
// (v0.16.6 #493 part 2c-defense).
func (r *storeReader) QueryFilteredErrorsJSONWithTenant(serviceName, tenantPrefix string) (string, error) {
	rows, err := r.s.QueryLogs(LogFilter{
		ServiceName:  serviceName,
		TenantPrefix: tenantPrefix,
		Level:        "error",
		Limit:        500,
	})
	if err != nil {
		return "", err
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.Message]++
	}
	out := make([]hubErrorRow, 0, len(counts))
	for msg, c := range counts {
		out = append(out, hubErrorRow{Count: c, Message: msg})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
