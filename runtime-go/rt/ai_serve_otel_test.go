//go:build !js

package rt

// The GPU tier's autoscale signals reach OpenTelemetry, and the engine's
// queue wait is a signal. A fake engine serves vLLM-shaped /metrics and a
// fake OTLP collector decodes what the tier pushes.

import (
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

// fakeEngineMetrics serves a /metrics body the test swaps between scrapes.
type fakeEngineMetrics struct {
	mu   sync.Mutex
	body string
}

func (f *fakeEngineMetrics) set(s string) { f.mu.Lock(); f.body = s; f.mu.Unlock() }

func (f *fakeEngineMetrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	io.WriteString(w, f.body)
}

// engineBody is a vLLM /metrics page: two models, a queue-time histogram
// with buckets the tier ignores.
func engineBody(sumA, countA, sumB, countB float64, preemptions int) string {
	var b strings.Builder
	b.WriteString("# HELP vllm:num_requests_running running\n")
	b.WriteString("vllm:num_requests_running{model_name=\"qwen-base\"} 2\n")
	b.WriteString("vllm:num_requests_waiting{model_name=\"qwen-base\"} 1\n")
	b.WriteString("vllm:kv_cache_usage_perc{model_name=\"qwen-base\"} 0.25\n")
	b.WriteString("vllm:num_preemptions_total{model_name=\"qwen-base\"} " + ftoa(float64(preemptions)) + "\n")
	b.WriteString("vllm:request_queue_time_seconds_bucket{le=\"0.1\",model_name=\"a\"} 3\n")
	b.WriteString("vllm:request_queue_time_seconds_sum{model_name=\"a\"} " + ftoa(sumA) + "\n")
	b.WriteString("vllm:request_queue_time_seconds_count{model_name=\"a\"} " + ftoa(countA) + "\n")
	b.WriteString("vllm:request_queue_time_seconds_sum{model_name=\"b\"} " + ftoa(sumB) + "\n")
	b.WriteString("vllm:request_queue_time_seconds_count{model_name=\"b\"} " + ftoa(countB) + "\n")
	return b.String()
}

func ftoa(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// signalTier is a tier over a fake engine, with no listener: the test drives
// scrapeEngine and reads status, /metrics and the OTLP points directly.
func signalTier(t *testing.T) (*aiTier, *fakeEngineMetrics) {
	t.Helper()
	eng := &fakeEngineMetrics{}
	srv := httptest.NewServer(eng)
	t.Cleanup(srv.Close)
	tier := newAiTier(aiTierConfig{engine: "vllm", bind: "10.0.0.7:8000", engineURL: srv.URL,
		baseID: tierBaseID, baseName: "qwen-base", maxAdapters: 4, poll: time.Hour, modelsDir: t.TempDir()})
	t.Cleanup(tier.close)
	tier.engineUp = true
	tier.resident["support-v1"] = &aiResident{served: "support-v1", inflight: 2}
	return tier, eng
}

func tierMetricsText(tier *aiTier) string {
	rec := httptest.NewRecorder()
	tier.metrics(rec)
	return rec.Body.String()
}

func TestAiTierQueueWaitIsTheMeanOverTheScrapeWindow(t *testing.T) {
	tier, eng := signalTier(t)
	// The console shows status(): the value checked here is the one it shows.
	wait := func() (float64, bool) {
		v, ok := tier.status().Signals["queueWaitMs"]
		return v, ok
	}

	// The first scrape has no window yet.
	eng.set(engineBody(0.25, 4, 0.75, 6, 0))
	tier.scrapeEngine()
	if v, ok := wait(); ok {
		t.Fatalf("one scrape is no window, got queueWaitMs %v", v)
	}

	// 0.5 s more over 4 more requests: 125 ms each.
	eng.set(engineBody(0.5, 6, 1.0, 8, 0))
	tier.scrapeEngine()
	if v, ok := wait(); !ok || v != 125 {
		t.Fatalf("queueWaitMs = %v (set %v), want 125", v, ok)
	}
	if m := tierMetricsText(tier); !strings.Contains(m, "sky_ai_engine_queue_wait_ms 125\n") {
		t.Fatalf("/metrics has no queue wait:\n%s", m)
	}

	// No request finished queueing: no value, not 0.
	tier.scrapeEngine()
	if v, ok := wait(); ok {
		t.Fatalf("an empty window reports nothing, got %v", v)
	}
	if m := tierMetricsText(tier); strings.Contains(m, "queue_wait_ms") {
		t.Fatalf("an empty window leaves queue_wait_ms off /metrics:\n%s", m)
	}

	// The engine restarted: its totals went down. Skip the window.
	eng.set(engineBody(0.25, 1, 0, 0, 0))
	tier.scrapeEngine()
	if v, ok := wait(); ok {
		t.Fatalf("a reset window reports nothing, got %v", v)
	}

	// The next window counts from the restarted totals: 0.5 s over 2.
	eng.set(engineBody(0.75, 3, 0, 0, 0))
	tier.scrapeEngine()
	if v, ok := wait(); !ok || v != 250 {
		t.Fatalf("after a reset queueWaitMs = %v (set %v), want 250", v, ok)
	}
}

// otlpCollector decodes each ExportMetricsServiceRequest it receives.
type otlpCollector struct {
	mu   sync.Mutex
	got  []*colmetricspb.ExportMetricsServiceRequest
	reqs []*http.Request
	recv chan struct{}
}

func newOTLPCollector() *otlpCollector { return &otlpCollector{recv: make(chan struct{}, 16)} }

func (c *otlpCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req colmetricspb.ExportMetricsServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		w.WriteHeader(400)
		return
	}
	c.mu.Lock()
	c.got = append(c.got, &req)
	c.reqs = append(c.reqs, r)
	c.mu.Unlock()
	w.WriteHeader(200)
	c.recv <- struct{}{}
}

func (c *otlpCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

// otlpEnv clears every OTLP and service-name variable for the test.
func otlpEnv(t *testing.T) {
	for _, n := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_METRICS_HEADERS",
		"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
		"SKY_SERVICE_NAME", "OTEL_SERVICE_NAME", "K_SERVICE",
	} {
		resetLiteralEnv(t, n)
	}
}

func TestAiTierPushesItsSignalsToOTLP(t *testing.T) {
	otlpEnv(t)
	col := newOTLPCollector()
	srv := httptest.NewServer(col)
	defer srv.Close()
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL+"/otlp")
	os.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-team=abc")
	os.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "x-metrics=1")
	os.Setenv("SKY_SERVICE_NAME", "svc-tier")

	tier, eng := signalTier(t)
	eng.set(engineBody(0.25, 4, 0.75, 6, 3))
	tier.scrapeEngine()
	eng.set(engineBody(0.5, 6, 1.0, 8, 3))
	tier.scrapeEngine()
	tier.pushOTel()
	select {
	case <-col.recv:
	case <-time.After(10 * time.Second):
		t.Fatal("the tier pushed nothing to the OTLP collector")
	}

	col.mu.Lock()
	defer col.mu.Unlock()
	r := col.reqs[0]
	if r.URL.Path != "/otlp/v1/metrics" {
		t.Fatalf("metrics go to <base path>/v1/metrics, got %s", r.URL.Path)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/x-protobuf" {
		t.Fatalf("Content-Type %q", ct)
	}
	if r.Header.Get("x-team") != "abc" || r.Header.Get("x-metrics") != "1" {
		t.Fatalf("OTLP headers missing: %v", r.Header)
	}
	rm := col.got[0].ResourceMetrics[0]
	svc := ""
	for _, kv := range rm.GetResource().GetAttributes() {
		if kv.Key == "service.name" {
			svc = kv.GetValue().GetStringValue()
		}
	}
	if svc != "svc-tier" {
		t.Fatalf("service.name = %q, want SKY_SERVICE_NAME", svc)
	}
	got := map[string]*metricspb.Metric{}
	for _, m := range rm.ScopeMetrics[0].Metrics {
		got[m.Name] = m
	}
	gauges := map[string]float64{
		"sky_ai_tier_ready":             1,
		"sky_ai_tier_inflight":          2,
		"sky_ai_tier_resident_adapters": 1,
		"sky_ai_engine_running":         2,
		"sky_ai_engine_waiting":         1,
		"sky_ai_engine_kv_cache":        0.25,
		"sky_ai_engine_queue_wait_ms":   125,
	}
	for name, want := range gauges {
		m := got[name]
		if m == nil || m.GetGauge() == nil {
			t.Fatalf("no gauge %s in %v", name, otlpMetricNames(got))
		}
		dp := m.GetGauge().DataPoints[0]
		if math.Abs(dp.GetAsDouble()-want) > 1e-9 {
			t.Fatalf("%s = %v, want %v", name, dp.GetAsDouble(), want)
		}
		if a := attrOf(dp.Attributes, "tier"); a != "10.0.0.7:8000" {
			t.Fatalf("%s tier attribute = %q", name, a)
		}
	}
	pre := got["sky_ai_engine_preemptions"]
	if pre == nil || pre.GetSum() == nil {
		t.Fatalf("preemptions must be a Sum: %v", pre)
	}
	sum := pre.GetSum()
	if !sum.IsMonotonic || sum.AggregationTemporality != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
		t.Fatalf("preemptions must be cumulative and monotonic: %v", sum)
	}
	dp := sum.DataPoints[0]
	if dp.GetAsDouble() != 3 || dp.StartTimeUnixNano == 0 || dp.StartTimeUnixNano > dp.TimeUnixNano {
		t.Fatalf("preemptions point: %v", dp)
	}
	if attrOf(dp.Attributes, "tier") != "10.0.0.7:8000" {
		t.Fatalf("preemptions carry the tier: %v", dp.Attributes)
	}
}

func TestAiTierPushesNothingUnderTheGRPCGuard(t *testing.T) {
	otlpEnv(t)
	col := newOTLPCollector()
	srv := httptest.NewServer(col)
	defer srv.Close()
	tier, eng := signalTier(t)
	eng.set(engineBody(0.25, 4, 0.75, 6, 0))
	tier.scrapeEngine()

	// No endpoint: nothing is sent.
	tier.pushOTel()
	// A gRPC protocol: nothing is sent either.
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	os.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	tier.pushOTel()
	time.Sleep(300 * time.Millisecond)
	if n := col.count(); n != 0 {
		t.Fatalf("the gRPC guard must stop the push, the collector got %d", n)
	}

	// The control: the same tier over http/protobuf does push.
	os.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	tier.pushOTel()
	select {
	case <-col.recv:
	case <-time.After(10 * time.Second):
		t.Fatal("over http/protobuf the tier pushes")
	}
}

func attrOf(kvs []*commonpb.KeyValue, key string) string {
	for _, kv := range kvs {
		if kv.Key == key {
			return kv.GetValue().GetStringValue()
		}
	}
	return ""
}

func otlpMetricNames(m map[string]*metricspb.Metric) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
