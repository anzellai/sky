package hub

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestStore_InsertLog_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	s.Insert([]pendingItem{{
		kind:        signalLog,
		ts:          now,
		serviceName: "alpha",
		level:       "warn",
		message:     "stop the press",
		traceID:     "tr-1",
		spanID:      "sp-1",
		attrs:       map[string]string{"foo": "bar"},
	}})
	s.FlushSync(2 * time.Second)

	rows, err := s.QueryLogs(LogFilter{ServiceName: "alpha"})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
	got := rows[0]
	if got.ServiceName != "alpha" || got.Level != "warn" || got.Message != "stop the press" {
		t.Errorf("got %+v", got)
	}
	if got.TraceID != "tr-1" || got.SpanID != "sp-1" {
		t.Errorf("ids: %+v", got)
	}
	if got.Attrs["foo"] != "bar" {
		t.Errorf("attrs: %+v", got.Attrs)
	}
}

func TestStore_InsertMetric_QueryByName(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	s.Insert([]pendingItem{
		{kind: signalMetric, ts: now, serviceName: "svc", metricName: "reqs", metricType: "sum", value: 5.0},
		{kind: signalMetric, ts: now, serviceName: "svc", metricName: "latency", metricType: "gauge", value: 12.5},
	})
	s.FlushSync(2 * time.Second)

	rows, err := s.QueryMetrics(MetricFilter{Name: "reqs"})
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
	if rows[0].Value != 5.0 || rows[0].Type != "sum" {
		t.Errorf("got %+v", rows[0])
	}
}

func TestStore_InsertSpan_QueryByTraceID(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	s.Insert([]pendingItem{{
		kind:        signalSpan,
		ts:          now,
		serviceName: "svc",
		spanName:    "GET /api/foo",
		traceID:     "trace-xyz",
		spanID:      "span-1",
		parentID:    "",
		startTime:   now,
		endTime:     now.Add(50 * time.Millisecond),
	}})
	s.FlushSync(2 * time.Second)

	rows, err := s.QuerySpans(SpanFilter{TraceID: "trace-xyz"})
	if err != nil {
		t.Fatalf("QuerySpans: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
	got := rows[0]
	if got.Name != "GET /api/foo" {
		t.Errorf("name = %q", got.Name)
	}
	if got.EndTime.Sub(got.StartTime) < 40*time.Millisecond {
		t.Errorf("duration: end-start = %v", got.EndTime.Sub(got.StartTime))
	}
}

func TestStore_Services(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	s.Insert([]pendingItem{
		{kind: signalLog, ts: now, serviceName: "alpha", level: "info", message: "x"},
		{kind: signalMetric, ts: now, serviceName: "beta", metricName: "m", metricType: "gauge", value: 1},
		{kind: signalSpan, ts: now, serviceName: "alpha", spanName: "s", startTime: now, endTime: now},
	})
	s.FlushSync(2 * time.Second)

	svcs, err := s.Services()
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if len(svcs) != 2 || svcs[0] != "alpha" || svcs[1] != "beta" {
		t.Errorf("services = %v, want [alpha beta]", svcs)
	}
}

func TestStore_ServiceMissing_FallsBackToUnknown(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	s.Insert([]pendingItem{{kind: signalLog, ts: time.Now(), message: "no svc"}})
	s.FlushSync(2 * time.Second)
	rows, err := s.QueryLogs(LogFilter{})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(rows) != 1 || rows[0].ServiceName != "unknown" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestStore_LevelFilter(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now()
	s.Insert([]pendingItem{
		{kind: signalLog, ts: now, serviceName: "x", level: "info", message: "1"},
		{kind: signalLog, ts: now, serviceName: "x", level: "error", message: "boom"},
		{kind: signalLog, ts: now, serviceName: "x", level: "info", message: "2"},
	})
	s.FlushSync(2 * time.Second)

	rows, err := s.QueryLogs(LogFilter{Level: "error"})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(rows) != 1 || rows[0].Message != "boom" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestStore_Prune_RemovesOldRows(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 1, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	old := time.Now().Add(-2 * time.Hour)
	now := time.Now()
	s.Insert([]pendingItem{
		{kind: signalLog, ts: old, serviceName: "svc", level: "info", message: "old"},
		{kind: signalLog, ts: now, serviceName: "svc", level: "info", message: "new"},
	})
	s.FlushSync(2 * time.Second)
	if err := s.RunPruneNow(); err != nil {
		t.Fatalf("RunPruneNow: %v", err)
	}
	rows, err := s.QueryLogs(LogFilter{})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(rows) != 1 || rows[0].Message != "new" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestStore_Counts(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now()
	s.Insert([]pendingItem{
		{kind: signalLog, ts: now, serviceName: "a"},
		{kind: signalLog, ts: now, serviceName: "b"},
		{kind: signalMetric, ts: now, metricName: "m", metricType: "gauge", value: 1},
		{kind: signalSpan, ts: now, spanName: "s", startTime: now, endTime: now},
	})
	s.FlushSync(2 * time.Second)
	logs, metrics, spans, err := s.Counts()
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if logs != 2 || metrics != 1 || spans != 1 {
		t.Errorf("counts: logs=%d metrics=%d spans=%d", logs, metrics, spans)
	}
}

func TestStore_BatchMany_Persists(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	batch := make([]pendingItem, 0, 500)
	for i := 0; i < 500; i++ {
		batch = append(batch, pendingItem{
			kind:        signalLog,
			ts:          now,
			serviceName: "svc",
			level:       "info",
			message:     "msg",
		})
	}
	s.Insert(batch)
	s.FlushSync(3 * time.Second)
	logs, _, _, err := s.Counts()
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if logs != 500 {
		t.Fatalf("logs=%d, want 500", logs)
	}
}

func TestStore_DBFileExists(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()
	want := filepath.Join(dir, "console-hot.db")
	if s.Path() != want {
		t.Errorf("Path = %q, want %q", s.Path(), want)
	}
	// External readers (the sqlite3 CLI test in the acceptance plan)
	// must be able to open the file concurrently. Verify by spinning
	// up a second handle and reading the schema.
	db2, err := sql.Open("sqlite", want)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer db2.Close()
	var n int
	row := db2.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'telemetry_%'`)
	if err := row.Scan(&n); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n != 3 {
		t.Errorf("found %d telemetry_* tables, want 3", n)
	}
}
