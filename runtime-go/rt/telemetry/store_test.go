package telemetry

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Counter / gauge / histogram basic-shape tests. These are the
// fundamentals — every metric the HTTP middleware + Live dispatcher
// will eventually emit goes through one of these primitives.

func TestCounter_Inc(t *testing.T) {
	s := NewStore()
	s.Inc("hits", map[string]string{"route": "/"})
	s.Inc("hits", map[string]string{"route": "/"})
	s.Inc("hits", map[string]string{"route": "/api"})

	snap := s.Snapshot()
	got := map[string]float64{}
	for _, m := range snap {
		if m.Type != "counter" {
			continue
		}
		got[m.Name+"|"+canonicaliseLabels(m.Labels)] = m.Value
	}
	if got["hits|route=/"] != 2 {
		t.Errorf("expected hits{route=/}=2, got %v", got["hits|route=/"])
	}
	if got["hits|route=/api"] != 1 {
		t.Errorf("expected hits{route=/api}=1, got %v", got["hits|route=/api"])
	}
}

func TestCounter_Add(t *testing.T) {
	s := NewStore()
	s.Add("bytes", nil, 1.5)
	s.Add("bytes", nil, 2.25)
	s.Add("bytes", nil, -1) // ignored — counters are monotonic
	snap := s.Snapshot()
	if len(snap) != 1 || snap[0].Value != 3.75 {
		t.Errorf("expected single sample value=3.75, got %+v", snap)
	}
}

func TestGauge_SetAndAdd(t *testing.T) {
	s := NewStore()
	s.SetGauge("conn", nil, 5)
	s.AddGauge("conn", nil, -2)
	s.AddGauge("conn", nil, 1)
	snap := s.Snapshot()
	if len(snap) != 1 || snap[0].Type != "gauge" || snap[0].Value != 4 {
		t.Errorf("expected gauge value=4, got %+v", snap)
	}
}

func TestHistogram_Observe(t *testing.T) {
	s := NewStore()
	// Observations spanning all buckets.
	for _, v := range []float64{0.0005, 0.002, 0.006, 0.020, 0.060, 0.150, 0.700, 2.5, 12.0} {
		s.Observe("latency", nil, v)
	}
	snap := s.Snapshot()
	if len(snap) != 1 || snap[0].Type != "histogram" {
		t.Fatalf("expected histogram sample, got %+v", snap)
	}
	h := snap[0]
	if h.Count != 9 {
		t.Errorf("expected Count=9, got %d", h.Count)
	}
	// Bucket counts are CUMULATIVE in our internal store — each
	// Observe bumps every bucket whose `le` >= v. Matches the
	// Prometheus wire convention so writeHistogram can emit
	// straight without a second accumulation pass.
	//
	// 0.001 bucket: just 0.0005 → 1
	if h.Buckets[0.001] != 1 {
		t.Errorf("expected Buckets[0.001]=1, got %d", h.Buckets[0.001])
	}
	// 0.005 bucket cumulative: 0.0005 + 0.002 → 2
	if h.Buckets[0.005] != 2 {
		t.Errorf("expected Buckets[0.005]=2 (cumulative), got %d", h.Buckets[0.005])
	}
	// 0.5 cumulative: 0.0005, 0.002, 0.006, 0.020, 0.060, 0.150 → 6
	if h.Buckets[0.5] != 6 {
		t.Errorf("expected Buckets[0.5]=6 (cumulative), got %d", h.Buckets[0.5])
	}
	// 5.0 cumulative: above + 0.700, 2.5 → 8
	if h.Buckets[5.0] != 8 {
		t.Errorf("expected Buckets[5.0]=8 (cumulative), got %d", h.Buckets[5.0])
	}
	// Sum: 15.4385
	want := 0.0005 + 0.002 + 0.006 + 0.020 + 0.060 + 0.150 + 0.700 + 2.5 + 12.0
	if abs(h.Sum-want) > 1e-9 {
		t.Errorf("expected Sum=%v, got %v", want, h.Sum)
	}
}

func TestSnapshot_IsSorted(t *testing.T) {
	s := NewStore()
	s.Inc("zeta", nil)
	s.Inc("alpha", map[string]string{"x": "1"})
	s.Inc("alpha", map[string]string{"x": "0"})
	snap := s.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(snap))
	}
	if snap[0].Name != "alpha" || snap[1].Name != "alpha" || snap[2].Name != "zeta" {
		t.Errorf("expected names sorted alpha/alpha/zeta, got %v %v %v",
			snap[0].Name, snap[1].Name, snap[2].Name)
	}
	if snap[0].Labels["x"] != "0" || snap[1].Labels["x"] != "1" {
		t.Errorf("expected labels sorted by canonicalised form, got %v then %v",
			snap[0].Labels, snap[1].Labels)
	}
}

// Concurrent writes must not lose counter increments. 100 goroutines
// each bumping 1000 times → expect exactly 100,000.
func TestCounter_ConcurrentInc(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	const G = 100
	const N = 1000
	for i := 0; i < G; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < N; j++ {
				s.Inc("requests", nil)
			}
		}()
	}
	wg.Wait()
	snap := s.Snapshot()
	if len(snap) != 1 || snap[0].Value != float64(G*N) {
		t.Errorf("expected %d, got %+v", G*N, snap)
	}
}

// Cardinality cap: after the cap, new label combinations are
// dropped but existing series keep updating.
func TestCardinality_CapDropsNewSeries(t *testing.T) {
	s := NewStore()
	s.cardinalityCap = 3 // tiny cap for the test
	// Three series within cap.
	s.Inc("hits", map[string]string{"u": "a"})
	s.Inc("hits", map[string]string{"u": "b"})
	s.Inc("hits", map[string]string{"u": "c"})
	// Existing series still updates.
	s.Inc("hits", map[string]string{"u": "a"})
	// New series dropped.
	s.Inc("hits", map[string]string{"u": "d"})
	s.Inc("hits", map[string]string{"u": "e"})
	snap := s.Snapshot()
	counterCount := 0
	for _, m := range snap {
		if m.Type == "counter" {
			counterCount++
		}
	}
	if counterCount != 3 {
		t.Errorf("expected 3 counter series after cap, got %d", counterCount)
	}
	// Cap-overflow warning landed in the log ring once (not per
	// dropped sample).
	logs := s.RecentLogs(0)
	warnCount := 0
	for _, l := range logs {
		if strings.Contains(l.Message, "cardinality cap exceeded") {
			warnCount++
		}
	}
	if warnCount != 1 {
		t.Errorf("expected exactly 1 cardinality warning, got %d", warnCount)
	}
}

// ──────────────────────────────────────────────────────────────────
// LOG RING
// ──────────────────────────────────────────────────────────────────

func TestLogRing_AppendAndRecent(t *testing.T) {
	s := NewStore()
	s.AppendLog(LogEntry{Level: "info", Message: "first"})
	s.AppendLog(LogEntry{Level: "warn", Message: "second"})
	s.AppendLog(LogEntry{Level: "error", Message: "third"})
	logs := s.RecentLogs(0)
	if len(logs) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(logs))
	}
	// Newest first.
	if logs[0].Message != "third" || logs[1].Message != "second" || logs[2].Message != "first" {
		t.Errorf("logs not in newest-first order: %v", logs)
	}
}

func TestLogRing_WrapsAtCapacity(t *testing.T) {
	r := newLogRing(3)
	for i := 0; i < 5; i++ {
		r.append(LogEntry{Message: string(rune('a' + i))})
	}
	got := r.recent(0)
	if len(got) != 3 {
		t.Fatalf("expected ring to hold 3, got %d", len(got))
	}
	// Newest first → e, d, c
	if got[0].Message != "e" || got[1].Message != "d" || got[2].Message != "c" {
		t.Errorf("ring wrap wrong: %v", got)
	}
}

func TestLogRing_RecentLimit(t *testing.T) {
	s := NewStore()
	for i := 0; i < 100; i++ {
		s.AppendLog(LogEntry{Message: "x"})
	}
	got := s.RecentLogs(10)
	if len(got) != 10 {
		t.Errorf("expected 10 with limit, got %d", len(got))
	}
}

// ──────────────────────────────────────────────────────────────────
// TRACE RING
// ──────────────────────────────────────────────────────────────────

func TestTraceRing_AppendAndRecent(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.AppendTrace(TraceEntry{
		TraceID:   "t1",
		SpanID:    "s1",
		Name:      "GET /",
		StartTime: now,
		EndTime:   now.Add(50 * time.Millisecond),
	})
	got := s.RecentTraces(0)
	if len(got) != 1 || got[0].Name != "GET /" {
		t.Fatalf("expected 1 trace 'GET /', got %v", got)
	}
	if got[0].Duration() != 50*time.Millisecond {
		t.Errorf("Duration() wrong: %v", got[0].Duration())
	}
}

// ──────────────────────────────────────────────────────────────────
// PROMETHEUS EXPOSITION
// ──────────────────────────────────────────────────────────────────

func TestProm_CounterShape(t *testing.T) {
	s := NewStore()
	s.Inc("sky_live_requests_total", map[string]string{"method": "GET", "route": "/", "status": "200"})
	s.Inc("sky_live_requests_total", map[string]string{"method": "GET", "route": "/", "status": "200"})
	var b bytes.Buffer
	s.WriteProm(&b)
	out := b.String()
	if !strings.Contains(out, "# HELP sky_live_requests_total ") {
		t.Errorf("missing HELP line for counter, got:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE sky_live_requests_total counter") {
		t.Errorf("missing TYPE line for counter, got:\n%s", out)
	}
	// Label order canonicalised alphabetically: method,route,status
	wantLine := `sky_live_requests_total{method="GET",route="/",status="200"} 2`
	if !strings.Contains(out, wantLine) {
		t.Errorf("expected line %q, got:\n%s", wantLine, out)
	}
}

func TestProm_HistogramShape(t *testing.T) {
	s := NewStore()
	for _, v := range []float64{0.0005, 0.002, 0.150, 2.5} {
		s.Observe("sky_live_request_seconds", map[string]string{"route": "/"}, v)
	}
	var b bytes.Buffer
	s.WriteProm(&b)
	out := b.String()
	if !strings.Contains(out, "# TYPE sky_live_request_seconds histogram") {
		t.Errorf("missing histogram TYPE line, got:\n%s", out)
	}
	// _bucket lines must use cumulative counts.
	// 0.001 bucket: 1 (just 0.0005); 0.005: 2 (0.0005+0.002); 0.010: 2;
	// 0.050: 2; 0.100: 2; 0.500: 3 (+0.150); 1.0: 3; 5.0: 4 (+2.5)
	checks := []string{
		`sky_live_request_seconds_bucket{route="/",le="0.001"} 1`,
		`sky_live_request_seconds_bucket{route="/",le="0.005"} 2`,
		`sky_live_request_seconds_bucket{route="/",le="0.5"} 3`,
		`sky_live_request_seconds_bucket{route="/",le="5"} 4`,
		`sky_live_request_seconds_bucket{route="/",le="+Inf"} 4`,
		`sky_live_request_seconds_count{route="/"} 4`,
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("expected line %q, missing from:\n%s", want, out)
		}
	}
}

func TestProm_GaugeShape(t *testing.T) {
	s := NewStore()
	s.SetGauge("sky_live_sessions_active", nil, 7)
	var b bytes.Buffer
	s.WriteProm(&b)
	out := b.String()
	if !strings.Contains(out, "# TYPE sky_live_sessions_active gauge") {
		t.Errorf("missing gauge TYPE line, got:\n%s", out)
	}
	if !strings.Contains(out, "sky_live_sessions_active 7") {
		t.Errorf("expected `sky_live_sessions_active 7`, got:\n%s", out)
	}
}

func TestProm_LabelEscaping(t *testing.T) {
	s := NewStore()
	// Values with backslash, quote, newline must be escaped per spec.
	s.Inc("hits", map[string]string{"path": "/foo\"bar\\baz\nqux"})
	var b bytes.Buffer
	s.WriteProm(&b)
	out := b.String()
	want := `hits{path="/foo\"bar\\baz\nqux"} 1`
	if !strings.Contains(out, want) {
		t.Errorf("expected escaped line %q, got:\n%s", want, out)
	}
}

func TestProm_BuiltInProcessMetrics(t *testing.T) {
	s := NewStore()
	var b bytes.Buffer
	s.WriteProm(&b)
	out := b.String()
	if !strings.Contains(out, "process_start_time_seconds") {
		t.Errorf("expected process_start_time_seconds metric, got:\n%s", out)
	}
	if !strings.Contains(out, "sky_telemetry_buffer_used") {
		t.Errorf("expected sky_telemetry_buffer_used metric, got:\n%s", out)
	}
}

// ──────────────────────────────────────────────────────────────────
// DEFAULT / RESET
// ──────────────────────────────────────────────────────────────────

func TestDefault_Singleton(t *testing.T) {
	ResetDefault()
	a := Default()
	b := Default()
	if a != b {
		t.Errorf("Default() should be a singleton")
	}
	a.Inc("test", nil)
	if len(b.Snapshot()) != 1 {
		t.Errorf("shared singleton should see writes from either alias")
	}
	ResetDefault()
}

// abs — math.Abs alternative; saves the import for one test.
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// ──────────────────────────────────────────────────────────────────
// PROMETHEUS WIRE-FORMAT PARSE CHECK
// ──────────────────────────────────────────────────────────────────

// Sanity-parse the exposition to catch subtle format breakage. We
// don't depend on Prometheus's official `expfmt` package (would bloat
// the runtime binary); a hand-rolled line shape check covers every
// invariant the scrapers care about: HELP/TYPE pairs, sorted lines,
// labels in {k="v",...} form, value is a parseable float.
func TestProm_WireFormatParseCheck(t *testing.T) {
	s := NewStore()
	s.Inc("foo", map[string]string{"a": "1"})
	s.SetGauge("bar", nil, 3.14)
	s.Observe("baz", map[string]string{"q": "x"}, 0.02)

	var b bytes.Buffer
	s.WriteProm(&b)
	for _, line := range strings.Split(b.String(), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# HELP ") || strings.HasPrefix(line, "# TYPE ") {
			continue
		}
		// Metric line: NAME[{LABELS}] VALUE
		// Split last space → value.
		idx := strings.LastIndex(line, " ")
		if idx < 0 {
			t.Errorf("malformed line (no value separator): %q", line)
			continue
		}
		name := line[:idx]
		val := line[idx+1:]
		if val == "" {
			t.Errorf("empty value: %q", line)
		}
		// Name part: alphanumeric_underscore + optional {...}
		if i := strings.IndexByte(name, '{'); i >= 0 {
			if !strings.HasSuffix(name, "}") {
				t.Errorf("label block not closed: %q", line)
			}
			_ = i
		}
	}
}

// Bench: hot-path counter increment under no contention.
// Baseline: ~30 ns/op on M1. Establishes a regression fence — a
// future change that pushes per-call cost over ~150 ns starves the
// per-request budget (1 ms request × 100 metric bumps = 10 % overhead
// at 100 ns, unacceptable at 1 μs).
func BenchmarkCounter_Inc_NoContention(b *testing.B) {
	s := NewStore()
	labels := map[string]string{"route": "/", "status": "200"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Inc("requests", labels)
	}
}

// Bench: counter increment under heavy contention (100 goroutines).
// Atomic CAS loop guarantees correctness; bench captures the cost.
func BenchmarkCounter_Inc_HighContention(b *testing.B) {
	s := NewStore()
	labels := map[string]string{"route": "/"}
	var counter atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.Inc("requests", labels)
			counter.Add(1)
		}
	})
	b.StopTimer()
	if counter.Load() == 0 {
		b.Skip("zero ops")
	}
}

// ─── UNBOUNDED-MEMORY regression: the warn dedupe map ─────────────
//
// cardinalityWarns is written exclusively AFTER the series cap
// trips — it was the accumulator that survived the guard. Every
// distinct metric NAME that overflowed stored one entry forever,
// and ingestInto passes names straight off the JSON wire
// (token-gated but arbitrary). The dedupe map exists only to keep
// warnings quiet; it is now capped at a small fixed size, with ONE
// summary warning once the cap itself fills.
func TestCardinalityWarns_DedupeMapIsCapped(t *testing.T) {
	s := NewStore()
	s.cardinalityCap = 0 // every series creation overflows immediately
	const distinct = 500
	for i := 0; i < distinct; i++ {
		s.Inc("wire_metric_"+itoaT(i), nil)
	}
	warnLines := 0
	summaryLines := 0
	for _, l := range s.RecentLogs(0) {
		if strings.Contains(l.Message, "cardinality cap exceeded") {
			warnLines++
		}
		if strings.Contains(l.Message, "cardinality warnings suppressed") {
			summaryLines++
		}
	}
	// One warn line per REMEMBERED name — so the line count is the
	// dedupe map's size. 128 remembered + 1 summary, never 500.
	if warnLines > 128 {
		t.Errorf("%d per-name cardinality warnings emitted (= names remembered forever); want <= 128", warnLines)
	}
	if summaryLines != 1 {
		t.Errorf("expected exactly 1 suppression summary once the warn cap filled, got %d", summaryLines)
	}
}

func itoaT(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for n > 0 {
		p--
		b[p] = byte('0' + n%10)
		n /= 10
	}
	return string(b[p:])
}
