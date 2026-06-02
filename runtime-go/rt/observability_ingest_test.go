package rt

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

// resetIngestState — tests share global state (ingestToken,
// telemetry.Default()). Reset between tests to avoid cross-pollution.
func resetIngestState(t *testing.T, fixedToken string) {
	t.Helper()
	telemetry.ResetDefault()
	ingestToken.Store(fixedToken)
}

// TestIngest_HappyPath — POST a logs+metrics+spans batch, verify
// every entry lands in the local store with the subapp label.
func TestIngest_HappyPath(t *testing.T) {
	resetIngestState(t, "test-token")
	payload := IngestPayload{
		Namespace: "billing",
		Logs: []IngestLog{
			{Level: "info", Message: "charge succeeded", TS: time.Now().UTC().Format(time.RFC3339Nano)},
		},
		Metrics: []IngestMetric{
			{Name: "requests_total", Type: "counter", Delta: 3, Labels: map[string]string{"endpoint": "/charge"}},
			{Name: "active_sessions", Type: "gauge", Value: 42},
		},
		Spans: []IngestSpan{
			{TraceID: "t1", SpanID: "s1", Name: "stripe.charge", StartMS: 1000, DurationMS: 412},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/_sky/observability/ingest", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Sky-Ingest-Token", "test-token")
	rr := httptest.NewRecorder()
	HandleObservabilityIngest(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	// Logs landed with subapp label.
	logs := telemetry.Default().RecentLogs(0)
	if len(logs) != 1 || logs[0].Message != "charge succeeded" || logs[0].Subapp != "billing" {
		t.Errorf("expected billing log entry; got %#v", logs)
	}
	// Counters landed with subapp label.
	snap := telemetry.Default().Snapshot()
	var sawCounter, sawGauge bool
	for _, s := range snap {
		if s.Name == "requests_total" && s.Type == "counter" && s.Labels["subapp"] == "billing" && s.Value == 3 {
			sawCounter = true
		}
		if s.Name == "active_sessions" && s.Type == "gauge" && s.Labels["subapp"] == "billing" && s.Value == 42 {
			sawGauge = true
		}
	}
	if !sawCounter {
		t.Errorf("counter with subapp=billing not found in snapshot: %#v", snap)
	}
	if !sawGauge {
		t.Errorf("gauge with subapp=billing not found in snapshot: %#v", snap)
	}
	// Spans landed.
	traces := telemetry.Default().RecentTraces(0)
	if len(traces) != 1 || traces[0].Name != "stripe.charge" || traces[0].Subapp != "billing" {
		t.Errorf("expected billing trace; got %#v", traces)
	}
}

func TestIngest_RejectsMissingToken(t *testing.T) {
	resetIngestState(t, "secret")
	req := httptest.NewRequest(http.MethodPost, "/_sky/observability/ingest",
		bytes.NewReader([]byte(`{"namespace":"x"}`)))
	rr := httptest.NewRecorder()
	HandleObservabilityIngest(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestIngest_RejectsBadToken(t *testing.T) {
	resetIngestState(t, "real-token")
	req := httptest.NewRequest(http.MethodPost, "/_sky/observability/ingest",
		bytes.NewReader([]byte(`{"namespace":"x"}`)))
	req.Header.Set("X-Sky-Ingest-Token", "wrong-token")
	rr := httptest.NewRecorder()
	HandleObservabilityIngest(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestIngest_RejectsMalformedJSON(t *testing.T) {
	resetIngestState(t, "tok")
	req := httptest.NewRequest(http.MethodPost, "/_sky/observability/ingest",
		bytes.NewReader([]byte(`{not-json`)))
	req.Header.Set("X-Sky-Ingest-Token", "tok")
	rr := httptest.NewRecorder()
	HandleObservabilityIngest(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestIngest_RejectsMissingNamespace(t *testing.T) {
	resetIngestState(t, "tok")
	req := httptest.NewRequest(http.MethodPost, "/_sky/observability/ingest",
		bytes.NewReader([]byte(`{"logs":[]}`)))
	req.Header.Set("X-Sky-Ingest-Token", "tok")
	rr := httptest.NewRecorder()
	HandleObservabilityIngest(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "missing namespace") {
		t.Errorf("expected missing-namespace error; got %q", rr.Body.String())
	}
}

func TestIngest_RejectsGET(t *testing.T) {
	resetIngestState(t, "tok")
	req := httptest.NewRequest(http.MethodGet, "/_sky/observability/ingest", nil)
	rr := httptest.NewRecorder()
	HandleObservabilityIngest(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestIngest_RejectsOversizePayload(t *testing.T) {
	resetIngestState(t, "tok")
	// Build a >1MiB payload — repeat a 1KB message many times.
	bigMsg := strings.Repeat("x", 1024)
	logs := make([]IngestLog, 1100) // 1100 × 1KB = 1.1 MiB raw, > 1 MiB after JSON
	for i := range logs {
		logs[i] = IngestLog{Level: "info", Message: bigMsg}
	}
	body, _ := json.Marshal(IngestPayload{Namespace: "x", Logs: logs})
	req := httptest.NewRequest(http.MethodPost, "/_sky/observability/ingest", bytes.NewReader(body))
	req.Header.Set("X-Sky-Ingest-Token", "tok")
	rr := httptest.NewRecorder()
	HandleObservabilityIngest(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestIngest_RejectsUnknownFields(t *testing.T) {
	resetIngestState(t, "tok")
	req := httptest.NewRequest(http.MethodPost, "/_sky/observability/ingest",
		bytes.NewReader([]byte(`{"namespace":"x","mystery_field":1}`)))
	req.Header.Set("X-Sky-Ingest-Token", "tok")
	rr := httptest.NewRecorder()
	HandleObservabilityIngest(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on unknown field; got %d", rr.Code)
	}
}

func TestIngest_NamespaceLabelCantBeSpoofed(t *testing.T) {
	resetIngestState(t, "tok")
	payload := IngestPayload{
		Namespace: "billing",
		Metrics: []IngestMetric{
			// Try to push under another namespace by setting subapp
			// label directly — handler must override.
			{Name: "x", Type: "counter", Delta: 1, Labels: map[string]string{"subapp": "admin"}},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/_sky/observability/ingest", bytes.NewReader(body))
	req.Header.Set("X-Sky-Ingest-Token", "tok")
	rr := httptest.NewRecorder()
	HandleObservabilityIngest(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rr.Code)
	}
	snap := telemetry.Default().Snapshot()
	for _, s := range snap {
		if s.Name == "x" {
			if s.Labels["subapp"] != "billing" {
				t.Errorf("namespace must be authoritative; got subapp=%q want %q",
					s.Labels["subapp"], "billing")
			}
			return
		}
	}
	t.Error("metric x not found in snapshot")
}

func TestIngest_AcceptsEmptyBatchCategories(t *testing.T) {
	resetIngestState(t, "tok")
	// All three optional; just namespace.
	req := httptest.NewRequest(http.MethodPost, "/_sky/observability/ingest",
		bytes.NewReader([]byte(`{"namespace":"empty"}`)))
	req.Header.Set("X-Sky-Ingest-Token", "tok")
	rr := httptest.NewRecorder()
	HandleObservabilityIngest(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Errorf("empty batch should be accepted; got %d", rr.Code)
	}
}

func TestIngest_DefaultsCounterTypeWhenAbsent(t *testing.T) {
	resetIngestState(t, "tok")
	payload := IngestPayload{
		Namespace: "ns",
		Metrics:   []IngestMetric{{Name: "implicit_counter", Delta: 5}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/_sky/observability/ingest", bytes.NewReader(body))
	req.Header.Set("X-Sky-Ingest-Token", "tok")
	rr := httptest.NewRecorder()
	HandleObservabilityIngest(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rr.Code)
	}
	for _, s := range telemetry.Default().Snapshot() {
		if s.Name == "implicit_counter" {
			if s.Type != "counter" || s.Value != 5 {
				t.Errorf("expected counter=5, got %s=%v", s.Type, s.Value)
			}
			return
		}
	}
	t.Error("implicit_counter not found")
}

// TestIngestToken_AutogenWhenEnvUnset — IngestTokenInit produces a
// non-empty token when SKY_INGEST_TOKEN is unset.
func TestIngestToken_AutogenWhenEnvUnset(t *testing.T) {
	t.Setenv("SKY_INGEST_TOKEN", "")
	ingestToken.Store("") // reset the singleton for this test
	got := IngestTokenInit()
	if got == "" {
		t.Fatal("token must be non-empty")
	}
	if len(got) < 32 {
		t.Errorf("token too short: %q", got)
	}
}

func TestIngestToken_HonoursEnv(t *testing.T) {
	t.Setenv("SKY_INGEST_TOKEN", "my-secret-token")
	ingestToken.Store("") // reset
	got := IngestTokenInit()
	if got != "my-secret-token" {
		t.Errorf("expected env value, got %q", got)
	}
}

// (v0.16.0 PR 2: TestSubAppNamespaceFromPath removed — the
// subprocess + reverse-proxy mount path is gone; the namespace
// helper that test exercised was in subapp.go which is now
// deleted. v0.16.1 reintroduces a similar helper for the
// exporter-side namespacing, at which point the test moves into
// its companion spec.)
