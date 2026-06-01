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
