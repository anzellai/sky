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
