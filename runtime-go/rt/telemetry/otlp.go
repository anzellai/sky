//go:build !js

package telemetry

// otlp.go — what every OTLP/HTTP signal this runtime sends shares: the
// collector URL for a signal, the request headers, the gRPC guard, and the
// resource that names the service.
//
// The URL rules follow the OpenTelemetry exporter specification:
//
//   - OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT is the full URL for that signal.
//     It is used as given.
//   - OTEL_EXPORTER_OTLP_ENDPOINT is a base URL. It keeps its own path and
//     gets "v1/<signal>" appended. A base of "https://otlp.example/otlp"
//     sends traces to "https://otlp.example/otlp/v1/traces".
//
// A base with no scheme ("collector:4318") is read as https, which is what
// the trace exporter has always done with it.
//
// The runtime links OTLP/HTTP protobuf only. A gRPC protocol setting or an
// endpoint on port 4317 (the OTLP/gRPC port) disables the signal with one
// warning, because an HTTP request to a gRPC port only ever fails.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sky-app/rt/procenv"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// OTLPSignalURL is the collector URL for one signal ("traces", "metrics").
// signalEndpoint (OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT) wins and is used as
// given. Otherwise the base endpoint keeps its path and gets v1/<signal>
// appended. Both empty is "": the signal is not exported.
func OTLPSignalURL(base, signalEndpoint, signal string) string {
	if s := strings.TrimSpace(signalEndpoint); s != "" {
		return withScheme(s)
	}
	b := strings.TrimSpace(base)
	if b == "" {
		return ""
	}
	b = withScheme(b)
	if i := strings.IndexAny(b, "?#"); i >= 0 {
		b = b[:i]
	}
	return strings.TrimRight(b, "/") + "/v1/" + signal
}

func withScheme(u string) string {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return "https://" + u
}

// OTLPEndpointFromEnv is OTLPSignalURL over the standard variables, read
// through procenv so a Sky.Config.withTelemetry value counts.
func OTLPEndpointFromEnv(signal string) string {
	return OTLPSignalURL(
		procenv.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		procenv.Getenv("OTEL_EXPORTER_OTLP_"+strings.ToUpper(signal)+"_ENDPOINT"),
		signal)
}

// OTLPHeadersFromEnv merges OTEL_EXPORTER_OTLP_HEADERS with
// OTEL_EXPORTER_OTLP_<SIGNAL>_HEADERS. A signal header wins over a general
// header of the same name.
func OTLPHeadersFromEnv(signal string) map[string]string {
	out := parseHeaderList(procenv.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	for k, v := range parseHeaderList(procenv.Getenv("OTEL_EXPORTER_OTLP_" + strings.ToUpper(signal) + "_HEADERS")) {
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// OTLPGuard reports why a signal cannot be exported to url, or "" when it
// can. The protocol is OTEL_EXPORTER_OTLP_<SIGNAL>_PROTOCOL, else
// OTEL_EXPORTER_OTLP_PROTOCOL.
func OTLPGuard(signal, url string) string {
	protocol := procenv.Getenv("OTEL_EXPORTER_OTLP_" + strings.ToUpper(signal) + "_PROTOCOL")
	if strings.TrimSpace(protocol) == "" {
		protocol = procenv.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	}
	if strings.ToLower(strings.TrimSpace(protocol)) == "grpc" {
		return fmt.Sprintf("OTEL_EXPORTER_OTLP_PROTOCOL=grpc, but this Sky build only ships HTTP/protobuf. "+
			"Set OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf and point OTEL_EXPORTER_OTLP_ENDPOINT at the "+
			"collector's HTTP port (typically :4318). OTLP %s export DISABLED for this run.", signal)
	}
	if url != "" && strings.Contains(hostPart(url), ":4317") {
		return fmt.Sprintf("the OTLP %s endpoint %s targets port 4317 (the OTLP/gRPC port) but this Sky build "+
			"only ships HTTP/protobuf. Point at the collector's HTTP port (typically :4318). OTLP %s export "+
			"DISABLED for this run.", signal, url, signal)
	}
	return ""
}

// hostPart is the scheme and authority of a URL, so a path that happens to
// hold "4317" does not trip the guard.
func hostPart(u string) string {
	rest := u
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

var otlpWarned sync.Map

// warnOnce prints a guard message once per process.
func warnOnce(msg string) {
	if _, loaded := otlpWarned.LoadOrStore(msg, true); !loaded {
		fmt.Fprintf(os.Stderr, "[sky.otel] WARN %s\n", msg)
	}
}

// ─── the resource ──────────────────────────────────────────────────────

// ServiceIdentityFromEnv is the service name and version every OTLP signal
// reports: SKY_SERVICE_NAME, then OTEL_SERVICE_NAME, then K_SERVICE (Cloud
// Run), then "sky-app". The version is OTEL_SERVICE_VERSION, else "dev".
func ServiceIdentityFromEnv() (name, version string) {
	return coalesce(procenv.Getenv("SKY_SERVICE_NAME"), procenv.Getenv("OTEL_SERVICE_NAME"),
			procenv.Getenv("K_SERVICE"), "sky-app"),
		coalesce(procenv.Getenv("OTEL_SERVICE_VERSION"), "dev")
}

// ServiceInstanceID is service.instance.id: K_REVISION (Cloud Run), else
// HOSTNAME, else the host name, else "instance-0".
func ServiceInstanceID() string {
	if v := coalesce(procenv.Getenv("K_REVISION"), procenv.Getenv("HOSTNAME")); v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "instance-0"
}

// ServiceResource is the resource every OTLP signal carries, so traces,
// metrics and the console hub envelope name the same service. An empty name
// or version falls back to ServiceIdentityFromEnv.
func ServiceResource(name, version string) []attribute.KeyValue {
	envName, envVersion := ServiceIdentityFromEnv()
	return []attribute.KeyValue{
		attribute.String("service.name", orDefault(name, envName)),
		attribute.String("service.version", orDefault(version, envVersion)),
		attribute.String("service.instance.id", ServiceInstanceID()),
		attribute.String("telemetry.sdk.name", "sky"),
		attribute.String("telemetry.sdk.language", "go"),
	}
}

// ─── metrics push ──────────────────────────────────────────────────────

// MetricPoint is one value to push. A gauge by default. Monotonic marks a
// cumulative monotonic Sum (a counter) whose count began at Start.
type MetricPoint struct {
	Name      string
	Unit      string
	Help      string
	Value     float64
	Attrs     map[string]string
	Monotonic bool
	Start     time.Time
}

// OTLPMetricsTarget is where metrics go and with which headers.
type OTLPMetricsTarget struct {
	URL     string
	Headers map[string]string
}

// OTLPMetricsTargetFromEnv is the metrics target the environment configures.
// ok is false when no endpoint is set, or when the gRPC guard trips (which
// warns once).
func OTLPMetricsTargetFromEnv() (OTLPMetricsTarget, bool) {
	url := OTLPEndpointFromEnv("metrics")
	if url == "" {
		return OTLPMetricsTarget{}, false
	}
	if why := OTLPGuard("metrics", url); why != "" {
		warnOnce(why)
		return OTLPMetricsTarget{}, false
	}
	return OTLPMetricsTarget{URL: url, Headers: OTLPHeadersFromEnv("metrics")}, true
}

// EncodeOTLPMetrics builds the protobuf ExportMetricsServiceRequest for
// points, under the shared service resource and the given scope.
func EncodeOTLPMetrics(scope string, points []MetricPoint, now time.Time) ([]byte, error) {
	res := &resourcepb.Resource{}
	for _, kv := range ServiceResource("", "") {
		res.Attributes = append(res.Attributes, &commonpb.KeyValue{Key: string(kv.Key),
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: kv.Value.AsString()}}})
	}
	nowNs := uint64(now.UnixNano())
	var metrics []*metricspb.Metric
	for _, p := range points {
		dp := &metricspb.NumberDataPoint{
			TimeUnixNano: nowNs,
			Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: p.Value},
			Attributes:   protoAttrs(p.Attrs),
		}
		m := &metricspb.Metric{Name: p.Name, Unit: p.Unit, Description: p.Help}
		if p.Monotonic {
			dp.StartTimeUnixNano = uint64(p.Start.UnixNano())
			m.Data = &metricspb.Metric_Sum{Sum: &metricspb.Sum{
				DataPoints:             []*metricspb.NumberDataPoint{dp},
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				IsMonotonic:            true,
			}}
		} else {
			m.Data = &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{dp}}}
		}
		metrics = append(metrics, m)
	}
	req := &colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource:     res,
		ScopeMetrics: []*metricspb.ScopeMetrics{{Scope: &commonpb.InstrumentationScope{Name: scope}, Metrics: metrics}},
	}}}
	return proto.Marshal(req)
}

func protoAttrs(m map[string]string) []*commonpb.KeyValue {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*commonpb.KeyValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, &commonpb.KeyValue{Key: k,
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: m[k]}}})
	}
	return out
}

// PushOTLPMetrics sends points to the target once, as OTLP/HTTP protobuf.
// It does not retry: the caller pushes again on its next tick. The context
// bounds the request.
func PushOTLPMetrics(ctx context.Context, client *http.Client, target OTLPMetricsTarget, scope string, points []MetricPoint) error {
	if len(points) == 0 {
		return nil
	}
	body, err := EncodeOTLPMetrics(scope, points, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", target.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	for k, v := range target.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("otlp metrics: %s answered %d", target.URL, resp.StatusCode)
	}
	return nil
}
