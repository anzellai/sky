//go:build !js

package telemetry

// The OTLP URL rules, the gRPC guard and the trace exporter's target. The
// trace exporter used to keep only host:port of OTEL_EXPORTER_OTLP_ENDPOINT,
// so a collector behind a path ("https://vendor.example/otlp") never saw a
// span.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sky-app/rt/procenv"
	"sync"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// otlpVars are every variable these tests read. Each test starts from none.
var otlpVars = []string{
	"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_METRICS_HEADERS",
	"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
	"SKY_SERVICE_NAME", "OTEL_SERVICE_NAME", "K_SERVICE",
}

func clearOTLPEnv(t *testing.T) {
	t.Helper()
	for _, n := range otlpVars {
		procenv.Delete(n)
		t.Setenv(n, "")
	}
}

func setProcenv(t *testing.T, name, value string) {
	t.Helper()
	procenv.Set(name, value, procenv.Applied)
	t.Cleanup(func() { procenv.Delete(name) })
}

func TestOTLPSignalURLKeepsTheBasePath(t *testing.T) {
	cases := []struct{ base, signalEP, signal, want string }{
		{"http://collector:4318", "", "metrics", "http://collector:4318/v1/metrics"},
		{"http://collector:4318/", "", "traces", "http://collector:4318/v1/traces"},
		{"https://vendor.example/otlp", "", "traces", "https://vendor.example/otlp/v1/traces"},
		{"https://vendor.example/otlp/", "", "metrics", "https://vendor.example/otlp/v1/metrics"},
		{"collector:4318", "", "traces", "https://collector:4318/v1/traces"},
		{"http://collector:4318/otlp", "http://other:9000/custom/metrics", "metrics", "http://other:9000/custom/metrics"},
		{"", "http://other:9000/x", "traces", "http://other:9000/x"},
		{"", "", "traces", ""},
	}
	for _, c := range cases {
		if got := OTLPSignalURL(c.base, c.signalEP, c.signal); got != c.want {
			t.Errorf("OTLPSignalURL(%q, %q, %q) = %q, want %q", c.base, c.signalEP, c.signal, got, c.want)
		}
	}
}

func TestOTLPEnvSignalEndpointAndHeaders(t *testing.T) {
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector:4318/base")
	if got := OTLPEndpointFromEnv("metrics"); got != "http://collector:4318/base/v1/metrics" {
		t.Fatalf("base endpoint: %q", got)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://m:4318/only/metrics")
	if got := OTLPEndpointFromEnv("metrics"); got != "http://m:4318/only/metrics" {
		t.Fatalf("the metrics endpoint is used as given: %q", got)
	}
	if got := OTLPEndpointFromEnv("traces"); got != "http://collector:4318/base/v1/traces" {
		t.Fatalf("the metrics endpoint does not move traces: %q", got)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "a=1, b=2")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "b=3")
	h := OTLPHeadersFromEnv("metrics")
	if h["a"] != "1" || h["b"] != "3" {
		t.Fatalf("metrics headers merge with the signal winning: %v", h)
	}
}

func TestOTLPGuardRefusesGRPC(t *testing.T) {
	clearOTLPEnv(t)
	if why := OTLPGuard("metrics", "http://collector:4317/v1/metrics"); why == "" {
		t.Fatal("port 4317 must trip the guard")
	}
	if why := OTLPGuard("metrics", "http://collector:4318/v4317/v1/metrics"); why != "" {
		t.Fatalf("4317 in a path is not the gRPC port: %s", why)
	}
	// A port that only starts with 4317 is not the gRPC port. CI picked
	// 127.0.0.1:43173 for a test collector and export was disabled.
	for _, u := range []string{"http://127.0.0.1:43173/x", "http://collector:43170", "http://[::1]:43179/v1/traces", "https://collector:4317000"} {
		if why := OTLPGuard("traces", u); why != "" {
			t.Fatalf("%s is not port 4317: %s", u, why)
		}
	}
	for _, u := range []string{"collector:4317", "https://collector:4317", "http://[::1]:4317/v1/traces", "http://user@collector:4317/"} {
		if why := OTLPGuard("traces", u); why == "" {
			t.Fatalf("%s is port 4317 and must trip the guard", u)
		}
	}
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	if why := OTLPGuard("traces", "http://collector:4318/v1/traces"); why == "" {
		t.Fatal("protocol grpc must trip the guard")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "grpc")
	if why := OTLPGuard("metrics", "http://collector:4318/v1/metrics"); why == "" {
		t.Fatal("a metrics protocol of grpc must trip the metrics guard")
	}
	if _, ok := OTLPMetricsTargetFromEnv(); ok {
		t.Fatal("no endpoint configured, no target")
	}
}

// traceCollector records each trace export's path and the service.name of
// its resource.
type traceCollector struct {
	mu       sync.Mutex
	paths    []string
	services []string
}

func (c *traceCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req coltracepb.ExportTraceServiceRequest
	svc := ""
	if proto.Unmarshal(body, &req) == nil {
		for _, rs := range req.ResourceSpans {
			for _, kv := range rs.GetResource().GetAttributes() {
				if kv.Key == "service.name" {
					svc = kv.GetValue().GetStringValue()
				}
			}
		}
	}
	c.mu.Lock()
	c.paths = append(c.paths, r.URL.Path)
	c.services = append(c.services, svc)
	c.mu.Unlock()
	w.WriteHeader(200)
}

func exportOneSpan(t *testing.T, cfg TracerConfig) {
	t.Helper()
	cfg.Serverless = true // a simple processor: the span is sent when it ends
	cfg.SampleRate = 1
	if err := InitTracer(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = InitTracer(TracerConfig{}) })
	_, span := Tracer().Start(context.Background(), "otlp-path-test")
	span.End()
	_ = ShutdownTracer(5 * time.Second)
}

func TestTracerExportsToTheBaseEndpointPath(t *testing.T) {
	clearOTLPEnv(t)
	t.Setenv("SKY_SERVICE_NAME", "svc-traces")
	col := &traceCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()
	// Set the way Sky.Config.withTelemetry sets it: in procenv, not the
	// process environment, so the OTel SDK's own env read cannot supply
	// the path.
	setProcenv(t, "OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL+"/otlp")
	exportOneSpan(t, LoadTracerConfigFromEnv(true))
	col.mu.Lock()
	defer col.mu.Unlock()
	if len(col.paths) == 0 || col.paths[0] != "/otlp/v1/traces" {
		t.Fatalf("spans must reach <base path>/v1/traces, got %v", col.paths)
	}
	if col.services[0] != "svc-traces" {
		t.Fatalf("the trace resource names SKY_SERVICE_NAME, got %q", col.services[0])
	}
}

func TestTracerUsesTheTracesEndpointAsGiven(t *testing.T) {
	clearOTLPEnv(t)
	col := &traceCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()
	setProcenv(t, "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", srv.URL+"/custom/spans")
	exportOneSpan(t, LoadTracerConfigFromEnv(true))
	col.mu.Lock()
	defer col.mu.Unlock()
	if len(col.paths) == 0 || col.paths[0] != "/custom/spans" {
		t.Fatalf("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is the whole URL, got %v", col.paths)
	}
}
