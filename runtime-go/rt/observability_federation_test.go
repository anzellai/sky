package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

// TestFederation_MetricsCounterRoundTrip — RecordCounter on the
// child round-trips through ingest + lands in the parent's
// Snapshot with the subapp= label.
func TestFederation_MetricsCounterRoundTrip(t *testing.T) {
	resetPushExporter()
	resetIngestState(t, "tok")

	// Set up a parent ingest endpoint.
	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/observability/ingest", HandleObservabilityIngest)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("SKY_PARENT_URL", srv.URL)
	t.Setenv("SKY_LIVE_NAMESPACE", "billing")
	t.Setenv("SKY_INGEST_TOKEN", "tok")
	t.Setenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "50")
	defer resetPushExporter()
	if StartPushExporter() == nil {
		t.Fatal("no exporter")
	}

	// Simulate a sub-app HTTP request being recorded.
	RecordCounter("subapp_requests_total", map[string]string{"endpoint": "/charge"}, 1)
	RecordCounter("subapp_requests_total", map[string]string{"endpoint": "/charge"}, 1)
	RecordCounter("subapp_requests_total", map[string]string{"endpoint": "/refund"}, 1)

	// The local write (RecordCounter → Add) uses the labels AS-IS:
	// {endpoint: /charge}. The push goes over the wire to the
	// ingest handler, which overlays subapp=billing onto labels and
	// calls Add again. So local + ingested produce TWO distinct
	// series — one without subapp= and one with — accumulating
	// independently. In separate-process deployments the local
	// series lives only in the child's store; the ingested series
	// lives only in the parent's.
	deadline := time.Now().Add(3 * time.Second)
	var sawCharge, sawRefund bool
	for time.Now().Before(deadline) {
		for _, s := range telemetry.Default().Snapshot() {
			if s.Name != "subapp_requests_total" {
				continue
			}
			if s.Labels["subapp"] != "billing" {
				continue // only the ingested series is interesting here
			}
			switch s.Labels["endpoint"] {
			case "/charge":
				if s.Value >= 2 {
					sawCharge = true
				}
			case "/refund":
				if s.Value >= 1 {
					sawRefund = true
				}
			}
		}
		if sawCharge && sawRefund {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("expected subapp_requests_total{subapp=billing} for /charge (>=2) and /refund (>=1); didn't see them")
}

// TestFederation_GaugeAbsoluteValue — gauges push the absolute
// value; parent's gauge ends up == last pushed value, regardless
// of how many times pushed.
func TestFederation_GaugeAbsoluteValue(t *testing.T) {
	resetPushExporter()
	resetIngestState(t, "tok")
	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/observability/ingest", HandleObservabilityIngest)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("SKY_PARENT_URL", srv.URL)
	t.Setenv("SKY_LIVE_NAMESPACE", "ns")
	t.Setenv("SKY_INGEST_TOKEN", "tok")
	t.Setenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "50")
	defer resetPushExporter()
	StartPushExporter()

	RecordGauge("sessions_active", nil, 10)
	RecordGauge("sessions_active", nil, 25)
	RecordGauge("sessions_active", nil, 42) // last write wins for gauges

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range telemetry.Default().Snapshot() {
			if s.Name == "sessions_active" && s.Labels["subapp"] == "ns" && s.Value == 42 {
				return // pass
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Error("expected sessions_active{subapp=ns} = 42")
}

// TestFederation_HistogramObservation — histograms push individual
// observations; parent's bucket counts accumulate.
func TestFederation_HistogramObservation(t *testing.T) {
	resetPushExporter()
	resetIngestState(t, "tok")
	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/observability/ingest", HandleObservabilityIngest)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("SKY_PARENT_URL", srv.URL)
	t.Setenv("SKY_LIVE_NAMESPACE", "hist")
	t.Setenv("SKY_INGEST_TOKEN", "tok")
	t.Setenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "50")
	defer resetPushExporter()
	StartPushExporter()

	for i := 0; i < 5; i++ {
		RecordHistogram("latency_seconds", nil, 0.123)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range telemetry.Default().Snapshot() {
			if s.Name == "latency_seconds" && s.Labels["subapp"] == "hist" {
				// Local 5 + ingested 5 = 10 total in same-process
				// test. In separate-process deployments only the
				// ingested count would land on the parent.
				if s.Count >= 5 {
					return
				}
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Error("expected latency_seconds{subapp=hist} histogram count >= 5")
}

// TestFederation_TraceSpanRoundTrip — RecordTrace on the child
// round-trips through ingest + lands in the parent's trace ring
// with subapp= label.
func TestFederation_TraceSpanRoundTrip(t *testing.T) {
	resetPushExporter()
	resetIngestState(t, "tok")
	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/observability/ingest", HandleObservabilityIngest)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("SKY_PARENT_URL", srv.URL)
	t.Setenv("SKY_LIVE_NAMESPACE", "billing")
	t.Setenv("SKY_INGEST_TOKEN", "tok")
	t.Setenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "50")
	defer resetPushExporter()
	StartPushExporter()

	now := time.Now()
	RecordTrace(telemetry.TraceEntry{
		TraceID:    "trace-a",
		SpanID:     "span-1",
		Name:       "GET /charge",
		Kind:       "server",
		StartTime:  now,
		EndTime:    now.Add(100 * time.Millisecond),
		StatusCode: "ok",
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, tr := range telemetry.Default().RecentTraces(0) {
			if tr.Name == "GET /charge" && tr.Subapp == "billing" {
				return // pass
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Error("expected GET /charge span with subapp=billing in parent's trace ring")
}

// TestFederation_PrometheusExpositionIncludesSubappLabel —
// /_sky/metrics output for a sub-app metric carries the subapp=
// label in Prometheus exposition format. Real Prometheus scrapers
// parse this correctly.
func TestFederation_PrometheusExpositionIncludesSubappLabel(t *testing.T) {
	resetPushExporter()
	resetIngestState(t, "tok")
	// Inject some sub-app data directly via the ingest path (no
	// network — exercise the parent's store path).
	stats := ingestInto(telemetry.Default(), "billing", IngestPayload{
		Metrics: []IngestMetric{
			{Name: "stripe_charges_total", Type: "counter", Delta: 7,
				Labels: map[string]string{"status": "succeeded"}},
		},
	})
	if stats.metrics != 1 {
		t.Fatalf("expected 1 metric ingested; got %d", stats.metrics)
	}

	// Probe /_sky/metrics — should include the subapp label.
	req := httptest.NewRequest("GET", "/_sky/metrics", nil)
	rr := httptest.NewRecorder()
	HandleMetrics(rr, req)
	if rr.Code != 200 {
		t.Fatalf("expected 200 from /_sky/metrics, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "stripe_charges_total") {
		t.Errorf("metric name missing from exposition")
	}
	if !strings.Contains(body, `subapp="billing"`) {
		t.Errorf("subapp=billing label missing from exposition; body:\n%s", body)
	}
	if !strings.Contains(body, `status="succeeded"`) {
		t.Errorf("user label missing")
	}
}
