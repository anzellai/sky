package telemetry

// P3 — counter-only window coalescing. When SKY_TELEMETRY_AGGREGATION_WINDOW
// (here, the test override) is > 0, the flusher keeps only the last cumulative
// row per (name,labels) counter series within the window; gauges and
// histograms are never coalesced. These tests pin:
//
//   1. a burst of N counter increments persists ONE row (the last value), and
//      that FlushPersistence still force-drains it (read-your-writes holds);
//   2. histograms are NOT coalesced (all N rows survive) — the SkyDeploy
//      reader rebuilds distributions from per-observation rows;
//   3. with the window at 0 (the default) every counter row persists, so the
//      feature is strictly opt-in.
//
// FALSIFIER for (1): set the window to 0 and it reaches N rows (== test 3),
// proving the coalescing — not some other batching — is what collapses them.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func pinAggregationWindow(t *testing.T, d time.Duration) {
	t.Helper()
	metricAggregationWindowOverride.Store(int64(d))
	t.Cleanup(func() { metricAggregationWindowOverride.Store(0) })
}

func countRowsNamed(t *testing.T, dbPath, name string) int {
	t.Helper()
	rdb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open read handle: %v", err)
	}
	defer rdb.Close()
	var n int
	if err := rdb.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM telemetry_metric WHERE name = ?`, name).Scan(&n); err != nil {
		t.Fatalf("count rows for %q: %v", name, err)
	}
	return n
}

func lastValueNamed(t *testing.T, dbPath, name string) float64 {
	t.Helper()
	rdb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open read handle: %v", err)
	}
	defer rdb.Close()
	var v float64
	if err := rdb.QueryRowContext(context.Background(),
		`SELECT value FROM telemetry_metric WHERE name = ? ORDER BY observed_at DESC LIMIT 1`,
		name).Scan(&v); err != nil {
		t.Fatalf("last value for %q: %v", name, err)
	}
	return v
}

// (1) A burst of counter increments coalesces to one row, and FlushPersistence
// still commits it (force-drain of the pending-map).
func TestCoalesce_CounterBurstCollapsesToOneRow_AndFlushDrainsIt(t *testing.T) {
	// Window an hour out so the window ticker NEVER fires during the test —
	// the only thing that emits the survivor is FlushPersistence's force-drain.
	pinAggregationWindow(t, time.Hour)
	pinFlushInterval(t, time.Hour) // and the batch ticker can't sneak a flush in
	dbPath := filepath.Join(t.TempDir(), "coalesce.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()

	const n = 200
	for i := 0; i < n; i++ {
		s.Inc("coalesce_counter", map[string]string{"k": "v"})
	}
	s.FlushPersistence() // must force-drain the coalesced survivor

	if got := countRowsNamed(t, dbPath, "coalesce_counter"); got != 1 {
		t.Fatalf("counter burst must coalesce to 1 row, got %d", got)
	}
	// The surviving row must carry the LAST cumulative value (n), not an
	// intermediate one — proving it kept the latest sample, not the first.
	if got := lastValueNamed(t, dbPath, "coalesce_counter"); got != float64(n) {
		t.Fatalf("coalesced survivor must hold the cumulative value %d, got %v", n, got)
	}
}

// (2) Histograms are never coalesced — every observation persists.
func TestCoalesce_HistogramsAreNotCoalesced(t *testing.T) {
	pinAggregationWindow(t, time.Hour)
	pinFlushInterval(t, time.Hour)
	dbPath := filepath.Join(t.TempDir(), "hist.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()

	const n = 40
	for i := 0; i < n; i++ {
		s.Observe("hist_series", map[string]string{"k": "v"}, float64(i))
	}
	s.FlushPersistence()

	if got := countRowsNamed(t, dbPath, "hist_series"); got != n {
		t.Fatalf("histograms must NOT coalesce: expected %d rows, got %d", n, got)
	}
}

// (3) Window 0 (default) writes every counter row — the feature is opt-in.
// This is the falsifier baseline for test (1): same burst, no coalescing.
func TestCoalesce_DefaultWindowZeroWritesEveryRow(t *testing.T) {
	pinAggregationWindow(t, 0) // explicit: the default
	pinFlushInterval(t, time.Hour)
	dbPath := filepath.Join(t.TempDir(), "raw.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()

	const n = 25
	for i := 0; i < n; i++ {
		s.Inc("raw_counter", map[string]string{"k": "v"})
	}
	s.FlushPersistence()

	if got := countRowsNamed(t, dbPath, "raw_counter"); got != n {
		t.Fatalf("window=0 must write every row: expected %d, got %d", n, got)
	}
}

// (4) The window ticker (not just FlushPersistence) emits survivors. Pins that
// coalesced counters are committed on the window cadence during normal running,
// without a synchronous flush.
func TestCoalesce_WindowTickerEmitsSurvivor(t *testing.T) {
	pinAggregationWindow(t, 30*time.Millisecond)
	pinFlushInterval(t, time.Hour) // isolate: only the window ticker can commit
	dbPath := filepath.Join(t.TempDir(), "tick.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()

	for i := 0; i < 10; i++ {
		s.Inc("tick_counter", map[string]string{"k": "v"})
	}
	// No FlushPersistence — wait for the window ticker to emit the survivor.
	deadline := time.After(3 * time.Second)
	for {
		if countRowsNamed(t, dbPath, "tick_counter") == 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("window ticker did not emit the coalesced counter survivor")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
