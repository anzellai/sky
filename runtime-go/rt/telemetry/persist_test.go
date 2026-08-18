package telemetry

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Spawn a fresh Store with persistence enabled at a tmp file,
// drive 100 RecordLog / RecordMetric / RecordTrace calls, then
// read back the rows.  Pre-fix the store was pure in-RAM; this
// is the load-bearing test that the dual-write actually lands
// in console.db.
func TestPersistence_DualWriteRoundTrip(t *testing.T) {
	// The flusher's timer is pinned out of reach: these rows must be committed
	// by FlushPersistence, not by a tick the test raced and usually won.
	pinFlushInterval(t, time.Hour)
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "console.db")

	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()

	for i := 0; i < 100; i++ {
		s.AppendLog(LogEntry{
			Level:   "info",
			Message: "test log line",
			Fields:  map[string]string{"i": "x"},
		})
		s.Inc("test_counter", map[string]string{"k": "v"})
		s.AppendTrace(TraceEntry{
			TraceID:   "trace-1",
			SpanID:    "span-1",
			Name:      "test span",
			StartTime: time.Now(),
			EndTime:   time.Now().Add(5 * time.Millisecond),
		})
	}

	s.FlushPersistence()

	// Open a fresh handle to read back — confirms the writer
	// actually flushed to disk, not just queued in memory.
	rdb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("re-open DB: %v", err)
	}
	defer rdb.Close()

	var logCount, metricCount, spanCount int
	if err := rdb.QueryRow(`SELECT COUNT(*) FROM telemetry_log`).Scan(&logCount); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if err := rdb.QueryRow(`SELECT COUNT(*) FROM telemetry_metric`).Scan(&metricCount); err != nil {
		t.Fatalf("count metrics: %v", err)
	}
	if err := rdb.QueryRow(`SELECT COUNT(*) FROM telemetry_span`).Scan(&spanCount); err != nil {
		t.Fatalf("count spans: %v", err)
	}
	if logCount != 100 {
		t.Errorf("expected 100 log rows, got %d", logCount)
	}
	if metricCount != 100 {
		t.Errorf("expected 100 metric rows, got %d", metricCount)
	}
	if spanCount != 100 {
		t.Errorf("expected 100 span rows, got %d", spanCount)
	}

	// Read one log row and assert shape.
	var level, message, attrs string
	if err := rdb.QueryRow(`SELECT level, message, attrs FROM telemetry_log LIMIT 1`).
		Scan(&level, &message, &attrs); err != nil {
		t.Fatalf("read log row: %v", err)
	}
	if level != "info" {
		t.Errorf("expected level=info, got %q", level)
	}
	if message != "test log line" {
		t.Errorf("expected message=test log line, got %q", message)
	}
	if !strings.Contains(attrs, `"i"`) {
		t.Errorf("expected attrs JSON to contain field i, got %q", attrs)
	}
}

// pinFlushInterval makes the flusher's timer irrelevant for the duration of a
// test. Any test that passes with the interval pinned to an hour is asserting
// synchronisation; a test that needs the timer to fire is asserting that it
// won a race, which is the defect this pin exists to catch.
func pinFlushInterval(t *testing.T, d time.Duration) {
	t.Helper()
	persistFlushIntervalOverride.Store(int64(d))
	t.Cleanup(func() { persistFlushIntervalOverride.Store(0) })
}

// FlushPersistence must be a synchronisation, not a race with a handicap.
//
// This is the regression gate for the CI failure that motivated the flushReq
// rendezvous. The old helper polled for an empty queue and then slept 250 ms,
// which happened to out-wait the flusher's 200 ms tick on an idle machine and
// did not on a loaded runner — 86 of 100 rows, two committed batches and a
// 44-entry remainder still in the flusher's local slice.
//
// Pinning the interval to an hour removes the timer from the picture entirely.
// Every row must still be present, because FlushPersistence commits them
// itself. Against the poll-and-sleep helper this test fails 100% of the time
// rather than intermittently, which is the whole point: the bug becomes
// reproducible on the developer's machine instead of only on CI's.
func TestPersistence_FlushIsSynchronisedNotTimed(t *testing.T) {
	pinFlushInterval(t, time.Hour)

	dbPath := filepath.Join(t.TempDir(), "sync.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()

	// 10 entries — deliberately fewer than persistBatchSize, so NOTHING is
	// flushed by the size trigger and the assertion rests entirely on
	// FlushPersistence. A count above the batch size would pass even with a
	// broken flush, on the batches the size cap happened to commit.
	const n = 10
	for i := 0; i < n; i++ {
		s.AppendLog(LogEntry{Level: "info", Message: "sync"})
	}
	if n >= persistBatchSize {
		t.Fatalf("test is vacuous: %d entries reaches the %d size trigger, "+
			"which would flush without FlushPersistence", n, persistBatchSize)
	}
	s.FlushPersistence()

	rdb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("re-open DB: %v", err)
	}
	defer rdb.Close()
	var got int
	if err := rdb.QueryRow(`SELECT COUNT(*) FROM telemetry_log`).Scan(&got); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if got != n {
		t.Errorf("expected %d log rows committed by FlushPersistence, got %d "+
			"(the flush returned before the writer committed)", n, got)
	}
}

// The deploy-safety gate: shutdown alone must commit the queue.
//
// No FlushPersistence here on purpose, and the interval is pinned to an hour so
// the ticker cannot do the work. If ClosePersistence merely signalled the
// flusher and returned, this would report a fraction of the rows — and
// "telemetry is flushed on shutdown" would be false on every deploy, losing
// exactly the window an operator reads when a deploy goes wrong.
func TestPersistence_ShutdownCommitsTheQueue(t *testing.T) {
	pinFlushInterval(t, time.Hour)

	dbPath := filepath.Join(t.TempDir(), "shutdown.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}

	const n = 200 // spans the size trigger, so a partial batch is left pending
	for i := 0; i < n; i++ {
		s.AppendLog(LogEntry{Level: "info", Message: "bye"})
	}

	s.ClosePersistence()

	rdb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("re-open DB: %v", err)
	}
	defer rdb.Close()
	var got int
	if err := rdb.QueryRow(`SELECT COUNT(*) FROM telemetry_log`).Scan(&got); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if got != n {
		t.Errorf("expected shutdown to commit all %d queued log rows, got %d", n, got)
	}
}

// FlushPersistence must not hang when the writer is already stopped, and must
// stay safe to call on a store that never enabled persistence.
func TestPersistence_FlushAfterCloseIsSafe(t *testing.T) {
	pinFlushInterval(t, time.Hour)

	s := NewStore()
	s.FlushPersistence() // never enabled — a no-op, not a nil deref

	dbPath := filepath.Join(t.TempDir(), "after-close.db")
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	s.AppendLog(LogEntry{Level: "info", Message: "x"})
	s.ClosePersistence()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.FlushPersistence()
	}()
	select {
	case <-done:
	case <-time.After(persistFlushWait + 2*time.Second):
		t.Fatal("FlushPersistence hung after ClosePersistence")
	}
}

// When persistence is NOT enabled, the in-RAM behaviour is unchanged.
// Mostly belt-and-braces — the regression we want to catch is the
// dual-write hook accidentally panicking on a nil persist field.
func TestPersistence_DisabledIsInRamOnly(t *testing.T) {
	s := NewStore()
	for i := 0; i < 10; i++ {
		s.AppendLog(LogEntry{Level: "info", Message: "x"})
		s.Inc("c", nil)
		s.AppendTrace(TraceEntry{Name: "n", StartTime: time.Now(), EndTime: time.Now()})
	}
	if got := s.RecentLogs(0); len(got) != 10 {
		t.Errorf("expected 10 in-RAM logs, got %d", len(got))
	}
	if got := s.RecentTraces(0); len(got) != 10 {
		t.Errorf("expected 10 in-RAM traces, got %d", len(got))
	}
}

// EnablePersistenceFromEnv reads SKY_CONSOLE_DB_PATH and enables
// the writer when set.  Tests that the env-var indirection works.
func TestPersistence_EnableFromEnv(t *testing.T) {
	pinFlushInterval(t, time.Hour)
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "from-env.db")
	t.Setenv(persistEnvVar, dbPath)

	s := NewStore()
	if err := s.EnablePersistenceFromEnv(); err != nil {
		t.Fatalf("EnablePersistenceFromEnv: %v", err)
	}
	defer s.ClosePersistence()

	s.AppendLog(LogEntry{Level: "info", Message: "env-driven"})
	s.FlushPersistence()

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected DB file at %s, stat failed: %v", dbPath, err)
	}

	rdb, _ := sql.Open("sqlite", dbPath)
	defer rdb.Close()
	var count int
	_ = rdb.QueryRow(`SELECT COUNT(*) FROM telemetry_log`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 log row, got %d", count)
	}
}

// EnableFromEnv with the env var unset is a no-op (no error, no
// file created, no goroutines spawned).
func TestPersistence_EnableFromEnvUnsetIsNoOp(t *testing.T) {
	t.Setenv(persistEnvVar, "")
	s := NewStore()
	if err := s.EnablePersistenceFromEnv(); err != nil {
		t.Fatalf("EnablePersistenceFromEnv (unset): %v", err)
	}
	if s.persist != nil {
		t.Errorf("expected nil persistence when env unset, got %v", s.persist)
	}
}

// Re-enabling persistence is idempotent (no second goroutine
// spawned, no DB re-open thrash).
func TestPersistence_EnableIsIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "idem.db")

	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("first enable: %v", err)
	}
	first := s.persist
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("second enable: %v", err)
	}
	if s.persist != first {
		t.Errorf("expected idempotent enable, got new persistence")
	}
	s.ClosePersistence()
}

// The pruner deletes rows past their retention window.  We don't
// wait the real hour — drive `runPrune` directly with a back-dated
// row inserted into the test DB.
func TestPersistence_PrunerDropsOldRows(t *testing.T) {
	pinFlushInterval(t, time.Hour)
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "prune.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()

	// Insert one back-dated row directly + one fresh one via the
	// writer.  Then drive runPrune and assert only the fresh one
	// remains.
	rdb, _ := sql.Open("sqlite", dbPath)
	defer rdb.Close()
	// This second connection contends with the store's writer for the WAL write
	// lock; give it the same busy_timeout so it waits rather than tripping (or
	// being tripped by) SQLITE_BUSY under CI load.
	_, _ = rdb.Exec(`PRAGMA busy_timeout=5000`)
	rdb.SetMaxOpenConns(1)
	old := time.Now().Add(-48 * time.Hour).UTC().Format("2006-01-02 15:04:05.000")
	if _, err := rdb.Exec(`INSERT INTO telemetry_log (level, message, created_at) VALUES (?, ?, ?)`,
		"info", "stale", old); err != nil {
		t.Fatalf("seed stale log: %v", err)
	}

	s.AppendLog(LogEntry{Level: "info", Message: "fresh"})
	s.FlushPersistence()

	if err := s.persist.runPrune(); err != nil {
		t.Fatalf("runPrune: %v", err)
	}

	var remaining int
	_ = rdb.QueryRow(`SELECT COUNT(*) FROM telemetry_log WHERE message = 'stale'`).Scan(&remaining)
	if remaining != 0 {
		t.Errorf("expected stale log pruned, %d remain", remaining)
	}
	_ = rdb.QueryRow(`SELECT COUNT(*) FROM telemetry_log WHERE message = 'fresh'`).Scan(&remaining)
	if remaining != 1 {
		t.Errorf("expected fresh log kept, got %d", remaining)
	}
}

// ─── UNBOUNDED-WORK regression: the hourly metric prune ───────────
//
// The pruner's `DELETE FROM telemetry_metric WHERE observed_at < ?`
// could not use the table's only index — (name, observed_at DESC)
// leads on `name`, so a bare range on observed_at fell back to a
// full table scan of up to ~300M rows every hour, on the shared
// pool the session store also draws from. The schema now carries a
// single-column observed_at index; EXPLAIN QUERY PLAN is the
// regression oracle (SCAN vs SEARCH ... USING INDEX).
func TestPrune_MetricDeleteUsesIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "console.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	for _, stmt := range consoleDBSchemaStmts("sqlite") {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	rows, err := db.Query(`EXPLAIN QUERY PLAN DELETE FROM telemetry_metric WHERE observed_at < ?`, "2020-01-01 00:00:00.000")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	joined := strings.Join(plan, " | ")
	if !strings.Contains(joined, "USING INDEX") || !strings.Contains(joined, "observed_at") {
		t.Errorf("hourly metric prune runs as a full table scan; plan: %s", joined)
	}
}

// SILENTLY-DEAD-FEATURE regression (telemetry side): the env vars
// that select a persistence backend can legitimately appear AFTER
// the first EnablePersistenceFromEnv call — under `./app --embed`,
// DATABASE_URL is exported by the embed supervisor from main, long
// after rt's init() ran the boot-time call. A later re-invocation
// (wired in pg_embed.go; gated by
// TestEmbeddedDSNHandoff_ReenablesTelemetryPersistence) must then
// actually enable persistence.
func TestEnablePersistenceFromEnv_HonoursEnvSetAfterBoot(t *testing.T) {
	t.Setenv("SKY_CONSOLE_DB_PATH", "")
	t.Setenv("DATABASE_URL", "")
	s := NewStore()
	if err := s.EnablePersistenceFromEnv(); err != nil {
		t.Fatalf("boot-time call with empty env: %v", err)
	}
	s.persistMu.RLock()
	active := s.persist != nil
	s.persistMu.RUnlock()
	if active {
		t.Fatal("persistence active with no env configured")
	}
	// The embed supervisor exports the DSN...
	t.Setenv("SKY_CONSOLE_DB_PATH", filepath.Join(t.TempDir(), "console.db"))
	// ...and the handoff re-invokes.
	if err := s.EnablePersistenceFromEnv(); err != nil {
		t.Fatalf("re-invocation after env export: %v", err)
	}
	defer s.ClosePersistence()
	s.persistMu.RLock()
	active = s.persist != nil
	s.persistMu.RUnlock()
	if !active {
		t.Fatal("persistence still dead after the env appeared and EnablePersistenceFromEnv re-ran")
	}
}
