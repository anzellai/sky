package telemetry

// Histogram coalescing (the deferred half of P3) — bucket-resolution, NOT
// lossless: today's rows carry the raw full-precision observation, so
// coalescing to cumulative bucket rows trades exact quantiles for a fixed row
// count per window. Gated by its OWN knob
// (SKY_TELEMETRY_HISTOGRAM_AGGREGATION_WINDOW, default off) because it is a
// breaking representation change for the out-of-repo SkyDeploy reader.
//
// These tests pin: (1) exploded rows equal a reference cumulative bucketing +
// exact sum/count, and are monotonic; (2) exactly boundaries+3 rows per window,
// not one-per-observation; (3) the emit-time monotonic CLAMP (the durable
// atomic-skew fix); (4) default-off writes raw rows; (5) the counter and
// histogram windows are independent.

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

func pinHistogramWindow(t *testing.T, d time.Duration) {
	t.Helper()
	metricHistogramWindowOverride.Store(int64(d))
	t.Cleanup(func() { metricHistogramWindowOverride.Store(0) })
}

// metricRow is one persisted telemetry_metric row.
type metricRow struct {
	name   string
	labels map[string]string
	value  float64
}

func readMetricRowsLike(t *testing.T, dbPath, namePrefix string) []metricRow {
	t.Helper()
	rdb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open read handle: %v", err)
	}
	defer rdb.Close()
	rows, err := rdb.QueryContext(context.Background(),
		`SELECT name, labels, value FROM telemetry_metric WHERE name LIKE ? ORDER BY name`,
		namePrefix+"%")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var out []metricRow
	for rows.Next() {
		var name, labelsJSON string
		var value float64
		if err := rows.Scan(&name, &labelsJSON, &value); err != nil {
			t.Fatalf("scan: %v", err)
		}
		labels := map[string]string{}
		if labelsJSON != "" {
			_ = json.Unmarshal([]byte(labelsJSON), &labels)
		}
		out = append(out, metricRow{name: name, labels: labels, value: value})
	}
	return out
}

// (1)+(2) Reconstruction: exploded rows == reference cumulative bucketing,
// exact sum/count, monotonic, and a FIXED row count independent of observation
// count. The reference is computed from the boundaries that appear in the rows
// themselves (self-describing) so it never hard-codes the profile.
//
// FALSIFIERS this catches: per-bucket deltas instead of cumulative (bucket
// values wrong); +Inf from the last finite bucket instead of count (wrong when
// a value exceeds the last boundary); the raw path (row count == N, zero
// _bucket rows); a non-monotonic vector (the sorted-le monotonic assertion).
func TestHistogramCoalesce_ExplodedRowsMatchReference(t *testing.T) {
	pinHistogramWindow(t, time.Hour) // ticker never fires; only FlushPersistence emits
	pinFlushInterval(t, time.Hour)
	dbPath := filepath.Join(t.TempDir(), "hist.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()

	observed := []float64{0.0003, 0.002, 0.02, 0.02, 0.4, 0.4, 0.4, 3.0, 12.0}
	var wantSum float64
	for _, v := range observed {
		s.Observe("test_hist", map[string]string{"k": "v"}, v)
		wantSum += v
	}
	s.FlushPersistence() // force-drain the dirty-set

	rows := readMetricRowsLike(t, dbPath, "test_hist")

	var buckets []struct {
		le  float64
		val float64
	}
	var gotSum, gotCount float64
	var haveSum, haveCount, haveInf bool
	var infVal float64
	for _, r := range rows {
		switch r.name {
		case "test_hist_bucket":
			leStr := r.labels["le"]
			if leStr == "+Inf" {
				haveInf, infVal = true, r.value
				continue
			}
			le, err := strconv.ParseFloat(leStr, 64)
			if err != nil {
				t.Fatalf("bad le label %q: %v", leStr, err)
			}
			buckets = append(buckets, struct {
				le  float64
				val float64
			}{le, r.value})
		case "test_hist_sum":
			haveSum, gotSum = true, r.value
		case "test_hist_count":
			haveCount, gotCount = true, r.value
		default:
			t.Fatalf("unexpected row name %q (raw rows means coalescing did not run)", r.name)
		}
	}
	if !haveSum || !haveCount || !haveInf {
		t.Fatalf("missing _sum/_count/+Inf: sum=%v count=%v inf=%v", haveSum, haveCount, haveInf)
	}

	// count + sum are exact (not lossy).
	if gotCount != float64(len(observed)) {
		t.Fatalf("_count = %v, want %d", gotCount, len(observed))
	}
	if infVal != float64(len(observed)) {
		t.Fatalf("+Inf bucket = %v, want %d", infVal, len(observed))
	}
	if math.Abs(gotSum-wantSum) > 1e-9 {
		t.Fatalf("_sum = %v, want %v", gotSum, wantSum)
	}

	// Each finite bucket == #{observed <= le} (cumulative), and monotonic.
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].le < buckets[j].le })
	var prev float64
	for _, b := range buckets {
		var want float64
		for _, v := range observed {
			if v <= b.le {
				want++
			}
		}
		if b.val != want {
			t.Fatalf("bucket le=%v = %v, want cumulative %v", b.le, b.val, want)
		}
		if b.val < prev {
			t.Fatalf("non-monotonic buckets: le=%v value %v < previous %v", b.le, b.val, prev)
		}
		prev = b.val
	}

	// Fixed row count = len(boundaries) + 1 (+Inf) + 2 (sum,count) — NOT
	// len(observed). This is the whole point: independent of observation rate.
	wantRows := len(buckets) + 1 + 2
	if len(rows) != wantRows {
		t.Fatalf("row count = %d, want %d (fixed per window, not %d per-observation)",
			len(rows), wantRows, len(observed))
	}
}

// (3) The emit-time monotonic clamp (M2) — the durable atomic-skew fix. Feed
// emitHistogramSeries a deliberately SKEWED sample (a finite bucket below a
// lower `le`, and count below the finite max) and assert the emitted vector is
// clamped monotonic non-decreasing with +Inf/_count = max(count, finite-max).
//
// FALSIFIER: remove the running-max clamp and le=0.5 emits 3 (< the 5 at
// le=0.1) — non-monotonic — and +Inf/_count emit 6 (< finite max 7).
func TestHistogramCoalesce_EmitClampsMonotonic(t *testing.T) {
	sm := MetricSample{
		Name:    "m",
		Type:    "histogram",
		Buckets: map[float64]uint64{0.1: 5, 0.5: 3, 1.0: 7}, // 0.5 skewed low
		Sum:     1.23,
		Count:   6, // skewed below the finite max (7)
	}
	got := map[string]float64{}
	emitHistogramSeries("m", sm, func(rowName, leValue string, value float64, _ bool) {
		key := rowName
		if leValue != "" {
			key += "{le=" + leValue + "}"
		}
		got[key] = value
	})
	// Cumulative running max: 5, max(5,3)=5, max(5,7)=7; +Inf/count = max(7,6)=7.
	want := map[string]float64{
		"m_bucket{le=0.1}":  5,
		"m_bucket{le=0.5}":  5,
		"m_bucket{le=1}":    7,
		"m_bucket{le=+Inf}": 7,
		"m_sum":             1.23,
		"m_count":           7,
	}
	for k, w := range want {
		if got[k] != w {
			t.Fatalf("clamp: %s = %v, want %v (full: %v)", k, got[k], w, got)
		}
	}
	// And nothing extra.
	if len(got) != len(want) {
		t.Fatalf("emitted %d rows, want %d: %v", len(got), len(want), got)
	}
}

// (4) Default off (histogram window 0) writes RAW per-observation rows — opt-in.
func TestHistogramCoalesce_DefaultOffWritesRawRows(t *testing.T) {
	pinHistogramWindow(t, 0) // explicit: the default
	pinFlushInterval(t, time.Hour)
	dbPath := filepath.Join(t.TempDir(), "histraw.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()

	const n = 20
	for i := 0; i < n; i++ {
		s.Observe("raw_hist", map[string]string{"k": "v"}, float64(i)*0.01)
	}
	s.FlushPersistence()

	rows := readMetricRowsLike(t, dbPath, "raw_hist")
	if len(rows) != n {
		t.Fatalf("default-off must write %d raw rows, got %d", n, len(rows))
	}
	for _, r := range rows {
		if r.name != "raw_hist" {
			t.Fatalf("default-off must write raw rows named 'raw_hist', got %q", r.name)
		}
	}
}

// (5) The counter and histogram windows are INDEPENDENT: counters coalesce,
// histograms stay raw, when only the counter window is set — and vice versa.
func TestHistogramCoalesce_WindowsAreIndependent(t *testing.T) {
	// Counter window ON, histogram window OFF.
	pinAggregationWindow(t, time.Hour)
	pinHistogramWindow(t, 0)
	pinFlushInterval(t, time.Hour)
	dbPath := filepath.Join(t.TempDir(), "indep.db")
	s := NewStore()
	if err := s.EnablePersistence(dbPath); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	defer s.ClosePersistence()

	for i := 0; i < 15; i++ {
		s.Inc("indep_counter", map[string]string{"k": "v"})
		s.Observe("indep_hist", map[string]string{"k": "v"}, float64(i)*0.01)
	}
	s.FlushPersistence()

	// Counter coalesced to 1 row; histogram left raw (15 rows, no _bucket).
	if got := len(readMetricRowsLike(t, dbPath, "indep_counter")); got != 1 {
		t.Fatalf("counter window on -> counter must coalesce to 1 row, got %d", got)
	}
	histRows := readMetricRowsLike(t, dbPath, "indep_hist")
	if len(histRows) != 15 {
		t.Fatalf("histogram window off -> histogram must stay raw (15 rows), got %d", len(histRows))
	}
	for _, r := range histRows {
		if r.name != "indep_hist" {
			t.Fatalf("histogram window off -> no _bucket rows, got %q", r.name)
		}
	}
}
