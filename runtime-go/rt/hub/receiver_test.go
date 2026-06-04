package hub

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// newTestStore + newTestReceiver wire a Store backed by a t.TempDir
// + a receiver in front of it for the httptest.ResponseRecorder
// tests below.
func newTestHub(t *testing.T, cfg HubConfig) (*receiver, *Store, func()) {
	t.Helper()
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(t.TempDir(), "data")
	}
	if cfg.MaxPayloadBytes == 0 {
		cfg.MaxPayloadBytes = DefaultMaxPayloadBytes
	}
	store, err := newStore(cfg.DataDir, storeOptions{
		retentionHours: 24,
		pruneInterval:  1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	recv := newReceiver(cfg, store)
	cleanup := func() {
		_ = store.Close()
	}
	return recv, store, cleanup
}

func mux(recv *receiver) *http.ServeMux {
	m := http.NewServeMux()
	recv.attach(m)
	return m
}

func protoLogBody(t *testing.T, service, level, message string) []byte {
	t.Helper()
	req := &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: service}}},
				},
			},
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: uint64(time.Now().UnixNano()),
					SeverityText: strings.ToUpper(level),
					Body:         &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: message}},
				}},
			}},
		}},
	}
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	return b
}

func TestReceiver_AcceptsProtobufLogs(t *testing.T) {
	recv, store, cleanup := newTestHub(t, HubConfig{AuthMode: "off"})
	defer cleanup()

	body := protoLogBody(t, "test-service", "info", "hello")
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	w := httptest.NewRecorder()
	mux(recv).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	store.FlushSync(2 * time.Second)
	logs, _, _, err := store.Counts()
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if logs != 1 {
		t.Fatalf("log rows = %d, want 1", logs)
	}
}

func TestReceiver_AcceptsSkyJSON(t *testing.T) {
	recv, store, cleanup := newTestHub(t, HubConfig{AuthMode: "off"})
	defer cleanup()

	jsonBody := `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"sky-app"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"1750000000000000000","severityText":"WARN","body":{"stringValue":"oops"}}]}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux(recv).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	store.FlushSync(2 * time.Second)
	rows, err := store.QueryLogs(LogFilter{ServiceName: "sky-app"})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Level != "warn" {
		t.Errorf("level = %q, want warn", rows[0].Level)
	}
	if rows[0].Message != "oops" {
		t.Errorf("message = %q, want oops", rows[0].Message)
	}
}

func TestReceiver_TokenAuth(t *testing.T) {
	const token = "atttttttttttttttttttttttttttttttt"
	recv, _, cleanup := newTestHub(t, HubConfig{AuthMode: "token", Token: token})
	defer cleanup()

	body := protoLogBody(t, "svc", "info", "hi")
	cases := []struct {
		name     string
		header   string
		wantCode int
	}{
		{"missing header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer wrong-token-very-wrong-indeed-yes-yes", http.StatusUnauthorized},
		{"wrong scheme", "Basic foo", http.StatusUnauthorized},
		{"correct token", "Bearer " + token, http.StatusOK},
		{"lowercase scheme", "bearer " + token, http.StatusOK},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/x-protobuf")
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			mux(recv).ServeHTTP(w, req)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantCode)
			}
		})
	}
}

func TestReceiver_PayloadCap(t *testing.T) {
	const cap = 256
	recv, _, cleanup := newTestHub(t, HubConfig{AuthMode: "off", MaxPayloadBytes: cap})
	defer cleanup()

	big := make([]byte, cap+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(big))
	req.Header.Set("Content-Type", "application/x-protobuf")
	w := httptest.NewRecorder()
	mux(recv).ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", w.Code, w.Body.String())
	}
}

func TestReceiver_BadProtobuf(t *testing.T) {
	recv, _, cleanup := newTestHub(t, HubConfig{AuthMode: "off"})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader("not-a-proto"))
	req.Header.Set("Content-Type", "application/x-protobuf")
	w := httptest.NewRecorder()
	mux(recv).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestReceiver_UnsupportedContentType(t *testing.T) {
	recv, _, cleanup := newTestHub(t, HubConfig{AuthMode: "off"})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader("x"))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	mux(recv).ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", w.Code)
	}
}

func TestReceiver_MethodNotAllowed(t *testing.T) {
	recv, _, cleanup := newTestHub(t, HubConfig{AuthMode: "off"})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/v1/logs", nil)
	w := httptest.NewRecorder()
	mux(recv).ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestReceiver_Healthz(t *testing.T) {
	recv, _, cleanup := newTestHub(t, HubConfig{AuthMode: "token", Token: "xx"})
	defer cleanup()

	// healthz is NOT auth-gated.
	req := httptest.NewRequest(http.MethodGet, "/_hub/healthz", nil)
	w := httptest.NewRecorder()
	mux(recv).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if body, _ := io.ReadAll(w.Body); string(body) != "ok" {
		t.Fatalf("body = %q, want ok", string(body))
	}
}

func TestReceiver_AcceptsTracesAndMetrics(t *testing.T) {
	recv, store, cleanup := newTestHub(t, HubConfig{AuthMode: "off"})
	defer cleanup()

	traceJSON := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc-t"}}]},"scopeSpans":[{"spans":[{"traceId":"abc","spanId":"def","name":"GET /","startTimeUnixNano":"1750000000000000000","endTimeUnixNano":"1750000001000000000"}]}]}]}`
	metricJSON := `{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc-m"}}]},"scopeMetrics":[{"metrics":[{"name":"reqs","sum":{"dataPoints":[{"timeUnixNano":"1750000000000000000","asDouble":42}]}}]}]}]}`

	for _, tc := range []struct{ path, body string }{
		{"/v1/traces", traceJSON},
		{"/v1/metrics", metricJSON},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux(recv).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200; body=%s", tc.path, w.Code, w.Body.String())
		}
	}
	store.FlushSync(2 * time.Second)
	_, metrics, spans, err := store.Counts()
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if metrics != 1 {
		t.Errorf("metrics = %d, want 1", metrics)
	}
	if spans != 1 {
		t.Errorf("spans = %d, want 1", spans)
	}
}
