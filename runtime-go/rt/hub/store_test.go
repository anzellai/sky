package hub

import (
	"database/sql"
	"path/filepath"
	"sync"
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

// FlushSync must be a synchronisation, not a race with a handicap.
//
// This is the regression gate for the CI failure that motivated the flushReq
// rendezvous. The old helper polled for an empty queue and then slept one
// flush interval plus 50 ms, which out-waited the batcher on an idle machine
// and did not under `-race` — 384 of 500 rows, three committed batches and a
// 116-item remainder still in the batcher's local slice.
//
// TestMain pins the interval to an hour, so the timer is out of the picture
// entirely and every row must still be present because FlushSync commits them
// itself. Against the poll-and-sleep helper this fails 100% of the time rather
// than intermittently, which is the whole point: the bug becomes reproducible
// on the developer's machine instead of only on CI's.
func TestStore_FlushSyncIsSynchronisedNotTimed(t *testing.T) {
	requirePinnedFlushInterval(t)

	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer s.Close()

	// Deliberately FEWER than flushBatchSize, so nothing is committed by the
	// size trigger and the assertion rests entirely on FlushSync. A count
	// above the batch size would pass even with a broken flush, on the
	// batches the size cap happened to commit — which is exactly how the
	// 500-row test reported 384 instead of 0 and looked like a near miss.
	const n = 10
	if n >= flushBatchSize {
		t.Fatalf("test is vacuous: %d items reaches the %d size trigger, "+
			"which would commit without FlushSync", n, flushBatchSize)
	}
	now := time.Now().UTC()
	batch := make([]pendingItem, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, pendingItem{
			kind: signalLog, ts: now, serviceName: "svc",
			level: "info", message: "sync",
		})
	}
	s.Insert(batch)
	s.FlushSync(3 * time.Second)

	logs, _, _, err := s.Counts()
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if logs != n {
		t.Fatalf("expected %d log rows committed by FlushSync, got %d "+
			"(the flush returned before the batcher committed)", n, logs)
	}
}

// The deploy-safety gate: Close alone must commit the queue.
//
// No FlushSync here on purpose, and TestMain has pinned the interval to an
// hour so the ticker cannot do the work either. If Close merely signalled the
// batcher and returned, this would report a fraction of the rows — and "the
// hub store is drained on shutdown" would be false on every hub restart,
// losing exactly the telemetry window an operator reads afterwards.
func TestStore_CloseCommitsTheQueue(t *testing.T) {
	requirePinnedFlushInterval(t)

	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}

	// Spans the size trigger, so a partial batch is guaranteed to be pending
	// at the moment of Close.
	const n = 300
	now := time.Now().UTC()
	batch := make([]pendingItem, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, pendingItem{
			kind: signalLog, ts: now, serviceName: "svc",
			level: "info", message: "bye",
		})
	}
	s.Insert(batch)

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Read through a FRESH handle: the store's own handle is closed, and a
	// reader that shared it could see uncommitted state.
	db, err := sql.Open("sqlite", s.Path())
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM telemetry_log`).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != n {
		t.Fatalf("expected Close to commit all %d queued rows, got %d", n, got)
	}
}

// Close is a barrier for every caller, not only the one that starts the drain.
//
// The old guard was a CompareAndSwap that returned nil immediately to the
// loser, so a second goroutine could observe "Close returned" while the queue
// was still being written. This asserts the concurrent caller waits too.
func TestStore_CloseBlocksEveryCaller(t *testing.T) {
	requirePinnedFlushInterval(t)

	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}

	const n = 300
	now := time.Now().UTC()
	batch := make([]pendingItem, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, pendingItem{
			kind: signalLog, ts: now, serviceName: "svc",
			level: "info", message: "concurrent",
		})
	}
	s.Insert(batch)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Close()
		}()
	}
	wg.Wait()

	db, err := sql.Open("sqlite", s.Path())
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM telemetry_log`).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != n {
		t.Fatalf("every Close caller must observe the completed drain: "+
			"got %d of %d rows", got, n)
	}
}

// FlushSync must not hang when the batcher is already stopped.
func TestStore_FlushSyncAfterCloseIsSafe(t *testing.T) {
	requirePinnedFlushInterval(t)

	dir := t.TempDir()
	s, err := newStore(dir, storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	s.Insert([]pendingItem{{
		kind: signalLog, ts: time.Now().UTC(),
		serviceName: "svc", level: "info", message: "x",
	}})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.FlushSync(3 * time.Second)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("FlushSync hung after Close")
	}
}

// requirePinnedFlushInterval fails loudly if TestMain's pin ever goes away.
//
// Without it, the three gates above would silently degrade into tests that
// pass because the ticker fired — which is precisely the failure mode they
// exist to catch, and it would be invisible.
func requirePinnedFlushInterval(t *testing.T) {
	t.Helper()
	if got := flushInterval(); got < time.Minute {
		t.Fatalf("flushInterval is %v: TestMain's pin is not in effect, so "+
			"this test could pass on the batcher's timer instead of on the "+
			"flush it is asserting", got)
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
