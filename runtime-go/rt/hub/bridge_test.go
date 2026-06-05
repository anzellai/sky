package hub

import (
	"encoding/json"
	"testing"
	"time"
)

// TestStoreReader_ServiceStatsJSON_MultipleServices verifies the
// v0.16.4 B5 aggregation:
//   - inserts mixed log + span data for two distinct services,
//   - asserts the JSON payload contains both services with the
//     correct shape (name + status + reqsPerSec + p95Ms + errorRate
//     + sparkRps + sparkP95).
//
// This is the regression artefact for the Hub_readServiceStats
// kernel — if the aggregator drops a service or shifts the wire
// shape, this test fails before the Sky-side multi-service Overview
// page ever renders blank.
func TestStoreReader_ServiceStatsJSON_MultipleServices(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	// 10 log rows for "alpha": 1 error → 10% error rate → "err" pill.
	for i := 0; i < 10; i++ {
		level := "info"
		if i == 0 {
			level = "error"
		}
		s.Insert([]pendingItem{{
			kind:        signalLog,
			ts:          now.Add(-time.Duration(i) * time.Second),
			serviceName: "alpha",
			level:       level,
			message:     "hello",
		}})
	}
	// 10 log rows for "beta": all info → 0% error rate → "ok" pill.
	for i := 0; i < 10; i++ {
		s.Insert([]pendingItem{{
			kind:        signalLog,
			ts:          now.Add(-time.Duration(i) * time.Second),
			serviceName: "beta",
			level:       "info",
			message:     "ping",
		}})
	}
	// Spans for "alpha" — drives p95 latency.
	for i := 0; i < 3; i++ {
		start := now.Add(-time.Duration(i+1) * time.Second)
		s.Insert([]pendingItem{{
			kind:        signalSpan,
			ts:          start,
			serviceName: "alpha",
			spanName:    "GET /healthz",
			traceID:     "trace-a",
			spanID:      "sp-a",
			startTime:   start,
			endTime:     start.Add(time.Duration(50+i*10) * time.Millisecond),
		}})
	}
	s.FlushSync(2 * time.Second)

	reader := s.AsReader()
	out, err := reader.ServiceStatsJSON()
	if err != nil {
		t.Fatalf("ServiceStatsJSON: %v", err)
	}

	type wireRow struct {
		Name       string    `json:"name"`
		Status     string    `json:"status"`
		ReqsPerSec float64   `json:"reqsPerSec"`
		P95Ms      float64   `json:"p95Ms"`
		ErrorRate  float64   `json:"errorRate"`
		SparkRps   []float64 `json:"sparkRps"`
		SparkP95   []float64 `json:"sparkP95"`
	}
	var rows []wireRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2; raw=%s", len(rows), out)
	}
	byName := make(map[string]wireRow, len(rows))
	for _, r := range rows {
		byName[r.Name] = r
	}
	alpha, ok := byName["alpha"]
	if !ok {
		t.Fatalf("alpha missing; raw=%s", out)
	}
	beta, ok := byName["beta"]
	if !ok {
		t.Fatalf("beta missing; raw=%s", out)
	}

	// Alpha: 1 err out of 10 = 10% → "err" pill (> 5% threshold).
	if alpha.Status != "err" {
		t.Errorf("alpha.Status=%q, want err; errRate=%v", alpha.Status, alpha.ErrorRate)
	}
	if alpha.ErrorRate < 0.099 || alpha.ErrorRate > 0.101 {
		t.Errorf("alpha.ErrorRate=%v, want ~0.10", alpha.ErrorRate)
	}
	if alpha.P95Ms <= 0 {
		t.Errorf("alpha.P95Ms=%v, want > 0 (from span durations)", alpha.P95Ms)
	}
	if alpha.ReqsPerSec <= 0 {
		t.Errorf("alpha.ReqsPerSec=%v, want > 0", alpha.ReqsPerSec)
	}
	if len(alpha.SparkRps) != statsBucketCount {
		t.Errorf("alpha.SparkRps len=%d, want %d", len(alpha.SparkRps), statsBucketCount)
	}
	if len(alpha.SparkP95) != statsBucketCount {
		t.Errorf("alpha.SparkP95 len=%d, want %d", len(alpha.SparkP95), statsBucketCount)
	}

	// Beta: 0 errors → "ok" pill.
	if beta.Status != "ok" {
		t.Errorf("beta.Status=%q, want ok; errRate=%v", beta.Status, beta.ErrorRate)
	}
	if beta.ErrorRate != 0 {
		t.Errorf("beta.ErrorRate=%v, want 0", beta.ErrorRate)
	}
	if beta.ReqsPerSec <= 0 {
		t.Errorf("beta.ReqsPerSec=%v, want > 0", beta.ReqsPerSec)
	}
}

// TestStoreReader_ServiceStatsJSON_EmptyStore returns an empty JSON
// array — the multi-service Overview UI tolerates this and shows the
// "Push telemetry to the hub" empty card.
func TestStoreReader_ServiceStatsJSON_EmptyStore(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	reader := s.AsReader()
	out, err := reader.ServiceStatsJSON()
	if err != nil {
		t.Fatalf("ServiceStatsJSON: %v", err)
	}
	if out != "[]" && out != "null" {
		t.Errorf("ServiceStatsJSON=%q, want [] or null", out)
	}
}

// TestStoreReader_QueryFilteredLogs verifies the v0.16.4 B6
// per-service filter narrows the log query to the named service.
// Pre-B6 every tab pulled the whole un-filtered store; the
// drill-down UI needs server-side scope so the wire payload stays
// bounded even on multi-tenant deployments.
//
// Setup: insert 5 log rows for "alpha" and 5 for "beta", then
// query with serviceName="alpha". The result MUST contain only
// alpha's rows.
func TestStoreReader_QueryFilteredLogs(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		s.Insert([]pendingItem{{
			kind:        signalLog,
			ts:          now.Add(-time.Duration(i) * time.Second),
			serviceName: "alpha",
			level:       "info",
			message:     "alpha-msg",
		}})
		s.Insert([]pendingItem{{
			kind:        signalLog,
			ts:          now.Add(-time.Duration(i) * time.Second),
			serviceName: "beta",
			level:       "info",
			message:     "beta-msg",
		}})
	}
	s.FlushSync(2 * time.Second)

	reader := s.AsReader()
	out, err := reader.QueryFilteredLogsJSON("alpha", "")
	if err != nil {
		t.Fatalf("QueryFilteredLogsJSON: %v", err)
	}
	var rows []hubLogRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, out)
	}
	if len(rows) == 0 {
		t.Fatalf("got 0 rows for alpha, want > 0; raw=%s", out)
	}
	for _, r := range rows {
		if r.Subapp != "alpha" {
			t.Errorf("row.Subapp=%q, want alpha; raw=%s", r.Subapp, out)
		}
		if r.Message != "alpha-msg" {
			t.Errorf("row.Message=%q, want alpha-msg", r.Message)
		}
	}

	// Empty service name = no filter → returns rows from both services.
	out, err = reader.QueryFilteredLogsJSON("", "")
	if err != nil {
		t.Fatalf("QueryFilteredLogsJSON(\"\"): %v", err)
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Subapp] = true
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Errorf("empty-service should return rows from both services; seen=%v", seen)
	}
}

// TestStoreReader_QueryFilteredMetrics — same pattern as Logs.
// Insert metrics for two services, assert the named filter
// narrows to one.
func TestStoreReader_QueryFilteredMetrics(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		s.Insert([]pendingItem{{
			kind:        signalMetric,
			ts:          now.Add(-time.Duration(i) * time.Second),
			serviceName: "alpha",
			metricName:  "alpha-counter",
			metricType:  "counter",
			value:       float64(i),
		}})
		s.Insert([]pendingItem{{
			kind:        signalMetric,
			ts:          now.Add(-time.Duration(i) * time.Second),
			serviceName: "beta",
			metricName:  "beta-counter",
			metricType:  "counter",
			value:       float64(i + 100),
		}})
	}
	s.FlushSync(2 * time.Second)

	reader := s.AsReader()
	out, err := reader.QueryFilteredMetricsJSON("alpha")
	if err != nil {
		t.Fatalf("QueryFilteredMetricsJSON: %v", err)
	}
	var rows []hubMetricRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, out)
	}
	if len(rows) == 0 {
		t.Fatalf("got 0 rows for alpha, want > 0; raw=%s", out)
	}
	for _, r := range rows {
		if r.Name != "alpha-counter" {
			t.Errorf("row.Name=%q, want alpha-counter; raw=%s", r.Name, out)
		}
	}
}

// TestStoreReader_QueryFilteredSpans — same pattern as Logs but
// over the span table.
func TestStoreReader_QueryFilteredSpans(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		start := now.Add(-time.Duration(i+1) * time.Second)
		s.Insert([]pendingItem{{
			kind:        signalSpan,
			ts:          start,
			serviceName: "alpha",
			spanName:    "alpha-op",
			traceID:     "tr-a",
			spanID:      "sp-a",
			startTime:   start,
			endTime:     start.Add(50 * time.Millisecond),
		}})
		s.Insert([]pendingItem{{
			kind:        signalSpan,
			ts:          start,
			serviceName: "beta",
			spanName:    "beta-op",
			traceID:     "tr-b",
			spanID:      "sp-b",
			startTime:   start,
			endTime:     start.Add(50 * time.Millisecond),
		}})
	}
	s.FlushSync(2 * time.Second)

	reader := s.AsReader()
	out, err := reader.QueryFilteredSpansJSON("alpha")
	if err != nil {
		t.Fatalf("QueryFilteredSpansJSON: %v", err)
	}
	var rows []hubTraceRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, out)
	}
	if len(rows) == 0 {
		t.Fatalf("got 0 rows for alpha, want > 0; raw=%s", out)
	}
	for _, r := range rows {
		if r.Name != "alpha-op" {
			t.Errorf("row.Name=%q, want alpha-op; raw=%s", r.Name, out)
		}
		// hubTraceRow.Kind carries the service name (legacy
		// field-mapping; see bridge.go::QuerySpansJSON).
		if r.Kind != "alpha" {
			t.Errorf("row.Kind=%q, want alpha; raw=%s", r.Kind, out)
		}
	}
}

// TestStoreReader_QueryFilteredErrors — error rollup MUST exclude
// rows from other services. Insert error logs for two services,
// assert the rollup count for the named service excludes the
// other.
func TestStoreReader_QueryFilteredErrors(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	for i := 0; i < 4; i++ {
		s.Insert([]pendingItem{{
			kind:        signalLog,
			ts:          now.Add(-time.Duration(i) * time.Second),
			serviceName: "alpha",
			level:       "error",
			message:     "alpha-error",
		}})
	}
	// 2 error rows for beta with a distinct message.
	for i := 0; i < 2; i++ {
		s.Insert([]pendingItem{{
			kind:        signalLog,
			ts:          now.Add(-time.Duration(i) * time.Second),
			serviceName: "beta",
			level:       "error",
			message:     "beta-error",
		}})
	}
	s.FlushSync(2 * time.Second)

	reader := s.AsReader()
	out, err := reader.QueryFilteredErrorsJSON("alpha")
	if err != nil {
		t.Fatalf("QueryFilteredErrorsJSON: %v", err)
	}
	var rows []hubErrorRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d distinct messages for alpha, want 1; raw=%s", len(rows), out)
	}
	if rows[0].Message != "alpha-error" {
		t.Errorf("rows[0].Message=%q, want alpha-error", rows[0].Message)
	}
	if rows[0].Count != 4 {
		t.Errorf("rows[0].Count=%d, want 4", rows[0].Count)
	}
}

// TestStoreReader_ServiceStatsJSON_WarnThreshold sits in the
// 1–5% error-rate band → "warn" pill. Locks in the threshold so
// future tuning doesn't silently change the UX.
func TestStoreReader_ServiceStatsJSON_WarnThreshold(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	// 50 logs total, 1 error → 2% error rate → "warn" pill.
	for i := 0; i < 50; i++ {
		level := "info"
		if i == 0 {
			level = "error"
		}
		s.Insert([]pendingItem{{
			kind:        signalLog,
			ts:          now.Add(-time.Duration(i) * time.Second),
			serviceName: "gamma",
			level:       level,
			message:     "msg",
		}})
	}
	s.FlushSync(2 * time.Second)

	reader := s.AsReader()
	out, err := reader.ServiceStatsJSON()
	if err != nil {
		t.Fatalf("ServiceStatsJSON: %v", err)
	}
	type wireRow struct {
		Name      string  `json:"name"`
		Status    string  `json:"status"`
		ErrorRate float64 `json:"errorRate"`
	}
	var rows []wireRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
	if rows[0].Status != "warn" {
		t.Errorf("Status=%q (errRate=%v), want warn", rows[0].Status, rows[0].ErrorRate)
	}
}
