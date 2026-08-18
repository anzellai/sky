package telemetry

// P1 — database size report. reportSizes measures the runtime-owned telemetry
// tables' footprint on the hourly prune cadence and emits ONE telemetry_log
// event. These tests pin: (a) the event lands with a size figure + driver,
// (b) it NEVER writes a telemetry_metric row (measuring the table you are
// trying to shrink by growing it is the trap it avoids), and (c) a second
// cycle reports a growth rate.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// findStorageSizeLog returns the most recent telemetry.storage_size ring entry,
// or a zero LogEntry + false.
func findStorageSizeLog(s *Store) (LogEntry, bool) {
	for _, e := range s.RecentLogs(200) {
		if e.Message == "telemetry.storage_size" {
			return e, true
		}
	}
	return LogEntry{}, false
}

func countMetricRows(t *testing.T, s *Store, dbPath string) int {
	t.Helper()
	s.FlushPersistence()
	rdb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open read handle: %v", err)
	}
	defer rdb.Close()
	var n int
	if err := rdb.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM telemetry_metric`).Scan(&n); err != nil {
		t.Fatalf("count telemetry_metric: %v", err)
	}
	return n
}

func TestReportSizes_EmitsLogEventNotMetric(t *testing.T) {
	pinFlushInterval(t, time.Hour)
	dbPath := filepath.Join(t.TempDir(), "size.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()

	// Seed some metric rows so the table has a footprint.
	for i := 0; i < 50; i++ {
		s.Inc("seed_counter", map[string]string{"k": "v"})
	}
	metricRowsBefore := countMetricRows(t, s, dbPath)

	// Run the report directly (bypassing the hourly timer).
	s.persist.reportSizes(s, time.Now())

	e, ok := findStorageSizeLog(s)
	if !ok {
		t.Fatalf("reportSizes did not emit a telemetry.storage_size log event")
	}
	if e.Fields["driver"] != "sqlite" {
		t.Fatalf("expected driver=sqlite, got %q", e.Fields["driver"])
	}
	if e.Fields["telemetry_total_bytes"] == "" || e.Fields["telemetry_total_bytes"] == "0" {
		t.Fatalf("expected a non-zero telemetry_total_bytes, got %q",
			e.Fields["telemetry_total_bytes"])
	}
	// Free space must be reported (or explicitly "unknown"), never silently absent.
	if e.Fields["fs_free_bytes"] == "" {
		t.Fatalf("expected fs_free_bytes to be reported (value or unknown)")
	}

	// The report must not have written a telemetry_metric row — that would be
	// the self-referential-growth trap. The only new persisted row is the LOG
	// event itself.
	metricRowsAfter := countMetricRows(t, s, dbPath)
	if metricRowsAfter != metricRowsBefore {
		t.Fatalf("reportSizes wrote %d telemetry_metric row(s); it must write a LOG event only",
			metricRowsAfter-metricRowsBefore)
	}
}

func TestReportSizes_SecondCycleReportsGrowth(t *testing.T) {
	pinFlushInterval(t, time.Hour)
	dbPath := filepath.Join(t.TempDir(), "growth.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()

	t0 := time.Now()
	// Cycle 1 — establishes the baseline; no prior, so no growth field.
	s.persist.reportSizes(s, t0)
	if e, ok := findStorageSizeLog(s); ok {
		if _, has := e.Fields["growth_bytes_per_day"]; has {
			t.Fatalf("first cycle must not report growth (no prior sample)")
		}
	}

	// Grow the table, then a second cycle an hour later must project growth.
	for i := 0; i < 200; i++ {
		s.Inc("growth_counter", map[string]string{"i": "x"})
		s.Observe("growth_hist", map[string]string{"n": "y"}, float64(i))
	}
	s.FlushPersistence()
	s.persist.reportSizes(s, t0.Add(time.Hour))

	e, ok := findStorageSizeLog(s)
	if !ok {
		t.Fatalf("second cycle emitted no storage_size event")
	}
	if _, has := e.Fields["growth_bytes_per_day"]; !has {
		t.Fatalf("second cycle must report growth_bytes_per_day; fields=%v", e.Fields)
	}
}
