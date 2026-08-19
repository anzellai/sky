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

// waitForStartupSizeReport blocks until the pruner's STARTUP size report has
// landed. The pruner emits it as its first action, then parks on its timer, so
// after this returns a test's own reportSizes calls are the only writer of the
// growth state — deterministic and race-free (no lock needed, single-writer).
func waitForStartupSizeReport(t *testing.T, s *Store) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if _, ok := findStorageSizeLog(s); ok {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("startup size report never appeared")
		case <-time.After(5 * time.Millisecond):
		}
	}
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
	waitForStartupSizeReport(t, s) // sequence after the async startup report

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

	// The STARTUP report is the baseline (cycle 0) — it has no prior, so no
	// growth field. Wait for it, then our manual report is a genuine SECOND
	// cycle sequenced after it (pruner idle → single writer).
	waitForStartupSizeReport(t, s)
	if e, ok := findStorageSizeLog(s); ok {
		if _, has := e.Fields["growth_bytes_per_day"]; has {
			t.Fatalf("baseline (startup) report must not report growth (no prior sample)")
		}
	}

	// Grow the tables, then a later cycle must project growth vs the baseline.
	for i := 0; i < 200; i++ {
		s.Inc("growth_counter", map[string]string{"i": "x"})
		s.Observe("growth_hist", map[string]string{"n": "y"}, float64(i))
	}
	s.FlushPersistence()
	s.persist.reportSizes(s, time.Now().Add(time.Hour))

	e, ok := findStorageSizeLog(s)
	if !ok {
		t.Fatalf("second cycle emitted no storage_size event")
	}
	if _, has := e.Fields["growth_bytes_per_day"]; !has {
		t.Fatalf("second cycle must report growth_bytes_per_day; fields=%v", e.Fields)
	}
}

// P1 v2 — the human-size capacity parser. A typo must DISABLE the check
// (parse-fail), never yield a garbage non-zero threshold. FALSIFIER: change the
// overflow guard or the negative check and the marked rows go wrong.
func TestParseHumanBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"", 0, false}, // unset
		{"100GB", 100_000_000_000, true},
		{"100gb", 100_000_000_000, true},   // case-insensitive
		{"100 GB", 100_000_000_000, true},  // internal space tolerated
		{"100GB\n", 100_000_000_000, true}, // file-sourced trailing newline
		{"1.5TB", 1_500_000_000_000, true}, // decimal
		{"512MB", 512_000_000, true},
		{"1GiB", 1 << 30, true}, // binary unit
		{"2TiB", 2 << 40, true},
		{"1024", 1024, true},         // bare = bytes
		{"0", 0, true},               // explicit disable, NOT malformed
		{"-5GB", 0, false},           // negative → malformed
		{"abc", 0, false},            // not a number
		{"100XB", 0, false},          // unknown unit
		{"999999999999TB", 0, false}, // int64 overflow → malformed, never wraps negative
	}
	for _, c := range cases {
		got, ok := parseHumanBytes(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseHumanBytes(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// The report includes the WHOLE database size (distinct from the telemetry-only
// figure), so an operator sees total consumption, not just the telemetry tables.
func TestReportSizes_ReportsWholeDBSize(t *testing.T) {
	pinFlushInterval(t, time.Hour)
	dbPath := filepath.Join(t.TempDir(), "whole.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()
	waitForStartupSizeReport(t, s)

	e, ok := findStorageSizeLog(s)
	if !ok {
		t.Fatalf("no storage_size event")
	}
	if e.Fields["db_total_bytes"] == "" || e.Fields["db_total_bytes"] == "0" {
		t.Fatalf("expected a non-zero db_total_bytes, got %q", e.Fields["db_total_bytes"])
	}
	if e.Fields["fs_total_bytes"] == "" {
		t.Fatalf("owned path must report fs_total_bytes, got empty")
	}
}

// A configured capacity the DB already exceeds must raise the danger flag — the
// only "near full" signal available when free disk isn't measurable.
func TestReportSizes_CapacityDangerFlag(t *testing.T) {
	t.Setenv("SKY_TELEMETRY_DB_CAPACITY", "1KB") // any real DB is bigger
	pinFlushInterval(t, time.Hour)
	dbPath := filepath.Join(t.TempDir(), "cap.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()
	waitForStartupSizeReport(t, s)

	s.persist.reportSizes(s, time.Now())
	e, ok := findStorageSizeLog(s)
	if !ok {
		t.Fatalf("no storage_size event")
	}
	if e.Level != "warn" {
		t.Fatalf("db over capacity must warn, got level=%q fields=%v", e.Level, e.Fields)
	}
	if e.Fields["db_capacity_bytes"] != "1000" {
		t.Fatalf("expected db_capacity_bytes=1000 (1KB), got %q", e.Fields["db_capacity_bytes"])
	}
	if e.Fields["warning"] == "" {
		t.Fatalf("expected a warning message naming the capacity breach")
	}
}

// A malformed capacity value disables the check LOUDLY (one warn), never silently.
func TestReportSizes_MalformedCapacityWarnsOnce(t *testing.T) {
	t.Setenv("SKY_TELEMETRY_DB_CAPACITY", "100 gigs") // human, unparseable
	pinFlushInterval(t, time.Hour)
	dbPath := filepath.Join(t.TempDir(), "badcap.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()
	waitForStartupSizeReport(t, s)

	var warned int
	for _, e := range s.RecentLogs(200) {
		if e.Message == "SKY_TELEMETRY_DB_CAPACITY is set but unparseable; the DB-capacity danger flag is disabled" {
			warned++
		}
	}
	if warned == 0 {
		t.Fatalf("malformed capacity must emit a one-shot WARN, got none")
	}
	// And the capacity danger must NOT have fired on a bogus threshold.
	if e, ok := findStorageSizeLog(s); ok {
		if e.Fields["db_capacity_bytes"] != "" {
			t.Fatalf("malformed capacity must not surface a db_capacity_bytes, got %q", e.Fields["db_capacity_bytes"])
		}
	}
}
