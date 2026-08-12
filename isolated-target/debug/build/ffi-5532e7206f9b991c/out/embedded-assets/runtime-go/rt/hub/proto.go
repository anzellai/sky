package hub

// OTLP/HTTP decoder. We accept both `application/x-protobuf` (the
// canonical OTLP/HTTP wire format used by every OTel SDK) and
// `application/json` (Sky's HubExporter at runtime-go/rt/
// exporter.go encodes JSON for dep-surface reasons — see
// EXPORTER.md "Implementation milestones").
//
// Each decoder returns a slice of `pendingItem`s — the SQLite-row-
// shaped intermediate form fed to the Store batcher. We keep
// pendingItem deliberately lean so the receiver hot path does
// minimal allocation: no full re-construction of the proto graph
// once we've extracted the fields we persist.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cmnpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	cmnmpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	cmntpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// signalKind identifies which OTLP signal a pendingItem represents.
type signalKind uint8

const (
	signalLog signalKind = iota
	signalMetric
	signalSpan
)

// pendingItem is the receiver-to-store intermediate form. A single
// instance carries enough info for one row in one of telemetry_log
// / telemetry_metric / telemetry_span; the writer dispatches on
// `kind` and reads only the fields it needs.
//
// `serviceName` is extracted from the OTLP Resource attribute
// `service.name`; when missing it falls back to "unknown" (see
// HUB.md §"Service identity").
type pendingItem struct {
	kind        signalKind
	ts          time.Time
	serviceName string
	attrs       map[string]string

	// log
	level   string
	message string
	traceID string
	spanID  string

	// metric
	metricName string
	metricType string // "gauge" | "sum" | "histogram"
	value      float64

	// span
	spanName  string
	parentID  string
	startTime time.Time
	endTime   time.Time
}

// unknownService is the fallback service-name when the incoming
// resource lacks a `service.name` attribute. Aligns with OTel
// SDK convention.
const unknownService = "unknown"

// contentJSON / contentProto recognise the OTLP HTTP content types.
// We tolerate parameters (`application/json; charset=utf-8`) by
// matching on the bare media-type prefix.
func isJSON(ct string) bool {
	ct = strings.TrimSpace(strings.ToLower(ct))
	return strings.HasPrefix(ct, "application/json")
}

func isProtobuf(ct string) bool {
	ct = strings.TrimSpace(strings.ToLower(ct))
	return strings.HasPrefix(ct, "application/x-protobuf") ||
		strings.HasPrefix(ct, "application/protobuf")
}

// ─── traces ──────────────────────────────────────────────────────

func decodeTracesProto(body []byte) ([]pendingItem, error) {
	var req cmntpb.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("proto unmarshal traces: %w", err)
	}
	return tracesFromResource(req.ResourceSpans), nil
}

func decodeTracesJSON(body []byte) ([]pendingItem, error) {
	var req cmntpb.ExportTraceServiceRequest
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := opts.Unmarshal(body, &req); err != nil {
		// Sky's HubExporter emits a non-standard envelope that lacks
		// the `resourceSpans:[]` outer wrapping the otlp-collector
		// service request expects. Try the bare list shape as a
		// fallback so v0.16.1+ Sky apps push successfully without
		// a protobuf upgrade.
		if items, err2 := decodeTracesSkyJSON(body); err2 == nil {
			return items, nil
		}
		return nil, fmt.Errorf("protojson unmarshal traces: %w", err)
	}
	return tracesFromResource(req.ResourceSpans), nil
}

// decodeTracesSkyJSON handles the bare `{"resourceSpans":[...]}`
// shape emitted by Sky's HubExporter (encodeOtlpEnvelope in
// exporter.go). protojson accepts this directly via
// ExportTraceServiceRequest's JSON encoding, but older Sky apps
// may emit minor schema drift (e.g. `intValue` as a string per
// the OTLP JSON spec). The fallback path takes a permissive
// json.Unmarshal first to extract what we need without strict
// proto schema enforcement.
func decodeTracesSkyJSON(body []byte) ([]pendingItem, error) {
	var raw struct {
		ResourceSpans []skyResourceSpans `json:"resourceSpans"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]pendingItem, 0, 16)
	for _, rs := range raw.ResourceSpans {
		svc := serviceNameFromSkyAttrs(rs.Resource.Attributes)
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				item := pendingItem{
					kind:        signalSpan,
					serviceName: svc,
					ts:          parseNanosString(sp.StartTimeUnixNano),
					spanName:    sp.Name,
					traceID:     sp.TraceID,
					spanID:      sp.SpanID,
					parentID:    sp.ParentSpanID,
					startTime:   parseNanosString(sp.StartTimeUnixNano),
					endTime:     parseNanosString(sp.EndTimeUnixNano),
					attrs:       skyAttrsToMap(sp.Attributes),
				}
				out = append(out, item)
			}
		}
	}
	return out, nil
}

func tracesFromResource(rs []*tracepb.ResourceSpans) []pendingItem {
	out := make([]pendingItem, 0, 16)
	for _, r := range rs {
		svc := serviceNameOf(r.GetResource())
		for _, ss := range r.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				start := time.Unix(0, int64(sp.GetStartTimeUnixNano())).UTC()
				end := time.Unix(0, int64(sp.GetEndTimeUnixNano())).UTC()
				out = append(out, pendingItem{
					kind:        signalSpan,
					serviceName: svc,
					ts:          start,
					spanName:    sp.GetName(),
					traceID:     hex.EncodeToString(sp.GetTraceId()),
					spanID:      hex.EncodeToString(sp.GetSpanId()),
					parentID:    hex.EncodeToString(sp.GetParentSpanId()),
					startTime:   start,
					endTime:     end,
					attrs:       kvToMap(sp.GetAttributes()),
				})
			}
		}
	}
	return out
}

// ─── logs ────────────────────────────────────────────────────────

func decodeLogsProto(body []byte) ([]pendingItem, error) {
	var req cmnpb.ExportLogsServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("proto unmarshal logs: %w", err)
	}
	return logsFromResource(req.ResourceLogs), nil
}

func decodeLogsJSON(body []byte) ([]pendingItem, error) {
	var req cmnpb.ExportLogsServiceRequest
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := opts.Unmarshal(body, &req); err != nil {
		if items, err2 := decodeLogsSkyJSON(body); err2 == nil {
			return items, nil
		}
		return nil, fmt.Errorf("protojson unmarshal logs: %w", err)
	}
	return logsFromResource(req.ResourceLogs), nil
}

func decodeLogsSkyJSON(body []byte) ([]pendingItem, error) {
	var raw struct {
		ResourceLogs []skyResourceLogs `json:"resourceLogs"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]pendingItem, 0, 16)
	for _, rl := range raw.ResourceLogs {
		svc := serviceNameFromSkyAttrs(rl.Resource.Attributes)
		for _, sl := range rl.ScopeLogs {
			for _, rec := range sl.LogRecords {
				ts := parseNanosString(rec.TimeUnixNano)
				out = append(out, pendingItem{
					kind:        signalLog,
					serviceName: svc,
					ts:          ts,
					level:       normaliseLevel(rec.SeverityText, int(rec.SeverityNumber)),
					message:     rec.Body.StringValue,
					traceID:     rec.TraceID,
					spanID:      rec.SpanID,
					attrs:       skyAttrsToMap(rec.Attributes),
				})
			}
		}
	}
	return out, nil
}

func logsFromResource(rl []*logspb.ResourceLogs) []pendingItem {
	out := make([]pendingItem, 0, 16)
	for _, r := range rl {
		svc := serviceNameOf(r.GetResource())
		for _, sl := range r.GetScopeLogs() {
			for _, rec := range sl.GetLogRecords() {
				ts := time.Unix(0, int64(rec.GetTimeUnixNano())).UTC()
				if ts.IsZero() || rec.GetTimeUnixNano() == 0 {
					ts = time.Unix(0, int64(rec.GetObservedTimeUnixNano())).UTC()
				}
				if ts.IsZero() {
					ts = time.Now().UTC()
				}
				msg := bodyToString(rec.GetBody())
				out = append(out, pendingItem{
					kind:        signalLog,
					serviceName: svc,
					ts:          ts,
					level:       normaliseLevel(rec.GetSeverityText(), int(rec.GetSeverityNumber())),
					message:     msg,
					traceID:     hex.EncodeToString(rec.GetTraceId()),
					spanID:      hex.EncodeToString(rec.GetSpanId()),
					attrs:       kvToMap(rec.GetAttributes()),
				})
			}
		}
	}
	return out
}

// ─── metrics ─────────────────────────────────────────────────────

func decodeMetricsProto(body []byte) ([]pendingItem, error) {
	var req cmnmpb.ExportMetricsServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("proto unmarshal metrics: %w", err)
	}
	return metricsFromResource(req.ResourceMetrics), nil
}

func decodeMetricsJSON(body []byte) ([]pendingItem, error) {
	var req cmnmpb.ExportMetricsServiceRequest
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := opts.Unmarshal(body, &req); err != nil {
		if items, err2 := decodeMetricsSkyJSON(body); err2 == nil {
			return items, nil
		}
		return nil, fmt.Errorf("protojson unmarshal metrics: %w", err)
	}
	return metricsFromResource(req.ResourceMetrics), nil
}

func decodeMetricsSkyJSON(body []byte) ([]pendingItem, error) {
	var raw struct {
		ResourceMetrics []skyResourceMetrics `json:"resourceMetrics"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]pendingItem, 0, 16)
	for _, rm := range raw.ResourceMetrics {
		svc := serviceNameFromSkyAttrs(rm.Resource.Attributes)
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Sum != nil {
					for _, dp := range m.Sum.DataPoints {
						out = append(out, pendingItem{
							kind:        signalMetric,
							serviceName: svc,
							ts:          parseNanosString(dp.TimeUnixNano),
							metricName:  m.Name,
							metricType:  "sum",
							value:       dp.AsDouble,
							attrs:       skyAttrsToMap(dp.Attributes),
						})
					}
				}
				if m.Gauge != nil {
					for _, dp := range m.Gauge.DataPoints {
						out = append(out, pendingItem{
							kind:        signalMetric,
							serviceName: svc,
							ts:          parseNanosString(dp.TimeUnixNano),
							metricName:  m.Name,
							metricType:  "gauge",
							value:       dp.AsDouble,
							attrs:       skyAttrsToMap(dp.Attributes),
						})
					}
				}
			}
		}
	}
	return out, nil
}

func metricsFromResource(rm []*metricspb.ResourceMetrics) []pendingItem {
	out := make([]pendingItem, 0, 16)
	for _, r := range rm {
		svc := serviceNameOf(r.GetResource())
		for _, sm := range r.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				switch d := m.GetData().(type) {
				case *metricspb.Metric_Sum:
					for _, dp := range d.Sum.GetDataPoints() {
						out = append(out, numberPointToItem(svc, m.GetName(), "sum", dp))
					}
				case *metricspb.Metric_Gauge:
					for _, dp := range d.Gauge.GetDataPoints() {
						out = append(out, numberPointToItem(svc, m.GetName(), "gauge", dp))
					}
				case *metricspb.Metric_Histogram:
					for _, dp := range d.Histogram.GetDataPoints() {
						ts := time.Unix(0, int64(dp.GetTimeUnixNano())).UTC()
						out = append(out, pendingItem{
							kind:        signalMetric,
							serviceName: svc,
							ts:          ts,
							metricName:  m.GetName(),
							metricType:  "histogram",
							value:       dp.GetSum(),
							attrs:       kvToMap(dp.GetAttributes()),
						})
					}
				}
			}
		}
	}
	return out
}

func numberPointToItem(svc, name, mtype string, dp *metricspb.NumberDataPoint) pendingItem {
	ts := time.Unix(0, int64(dp.GetTimeUnixNano())).UTC()
	val := dp.GetAsDouble()
	if v, ok := dp.GetValue().(*metricspb.NumberDataPoint_AsInt); ok {
		val = float64(v.AsInt)
	}
	return pendingItem{
		kind:        signalMetric,
		serviceName: svc,
		ts:          ts,
		metricName:  name,
		metricType:  mtype,
		value:       val,
		attrs:       kvToMap(dp.GetAttributes()),
	}
}

// ─── shared helpers ──────────────────────────────────────────────

func serviceNameOf(r *resourcepb.Resource) string {
	if r == nil {
		return unknownService
	}
	for _, kv := range r.GetAttributes() {
		if kv.GetKey() == "service.name" {
			if s := anyValueString(kv.GetValue()); s != "" {
				return s
			}
		}
	}
	return unknownService
}

func anyValueString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	if s, ok := v.GetValue().(*commonpb.AnyValue_StringValue); ok {
		return s.StringValue
	}
	return v.GetStringValue()
}

func bodyToString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_IntValue:
		return fmt.Sprintf("%d", x.IntValue)
	case *commonpb.AnyValue_DoubleValue:
		return fmt.Sprintf("%g", x.DoubleValue)
	case *commonpb.AnyValue_BoolValue:
		return fmt.Sprintf("%t", x.BoolValue)
	}
	return ""
}

func kvToMap(kvs []*commonpb.KeyValue) map[string]string {
	if len(kvs) == 0 {
		return nil
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		out[kv.GetKey()] = anyValueAnyString(kv.GetValue())
	}
	return out
}

func anyValueAnyString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_IntValue:
		return fmt.Sprintf("%d", x.IntValue)
	case *commonpb.AnyValue_DoubleValue:
		return fmt.Sprintf("%g", x.DoubleValue)
	case *commonpb.AnyValue_BoolValue:
		return fmt.Sprintf("%t", x.BoolValue)
	case *commonpb.AnyValue_BytesValue:
		return hex.EncodeToString(x.BytesValue)
	}
	return ""
}

// normaliseLevel canonicalises severity text. Returns lowercase
// "debug"/"info"/"warn"/"error". Falls back to numeric severity
// when text is empty.
func normaliseLevel(text string, num int) string {
	if text != "" {
		t := strings.ToLower(text)
		switch t {
		case "trace":
			return "debug"
		case "debug", "info", "warn", "warning", "error", "fatal":
			if t == "warning" {
				return "warn"
			}
			return t
		}
	}
	switch {
	case num >= 17:
		return "error"
	case num >= 13:
		return "warn"
	case num >= 5:
		return "info"
	case num > 0:
		return "debug"
	}
	return "info"
}

// ─── Sky JSON fallback shapes ───────────────────────────────────
//
// Mirror the field subset Sky's HubExporter at
// runtime-go/rt/exporter.go encodes. Field names match the
// `json:` tags exactly so json.Unmarshal works without any
// translation layer.

type skyAttrValue struct {
	StringValue string  `json:"stringValue"`
	IntValue    string  `json:"intValue"` // OTLP int = string
	DoubleValue float64 `json:"doubleValue"`
	BoolValue   *bool   `json:"boolValue"`
}

type skyAttribute struct {
	Key   string       `json:"key"`
	Value skyAttrValue `json:"value"`
}

type skyResource struct {
	Attributes []skyAttribute `json:"attributes"`
}

type skyResourceSpans struct {
	Resource   skyResource     `json:"resource"`
	ScopeSpans []skyScopeSpans `json:"scopeSpans"`
}

type skyScopeSpans struct {
	Spans []skySpan `json:"spans"`
}

type skySpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId"`
	Name              string         `json:"name"`
	Kind              int            `json:"kind"`
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	EndTimeUnixNano   string         `json:"endTimeUnixNano"`
	Attributes        []skyAttribute `json:"attributes"`
}

type skyResourceLogs struct {
	Resource  skyResource    `json:"resource"`
	ScopeLogs []skyScopeLogs `json:"scopeLogs"`
}

type skyScopeLogs struct {
	LogRecords []skyLogRecord `json:"logRecords"`
}

type skyLogRecord struct {
	TimeUnixNano   string         `json:"timeUnixNano"`
	SeverityNumber int            `json:"severityNumber"`
	SeverityText   string         `json:"severityText"`
	Body           skyAttrValue   `json:"body"`
	Attributes     []skyAttribute `json:"attributes"`
	TraceID        string         `json:"traceId"`
	SpanID         string         `json:"spanId"`
}

type skyResourceMetrics struct {
	Resource     skyResource       `json:"resource"`
	ScopeMetrics []skyScopeMetrics `json:"scopeMetrics"`
}

type skyScopeMetrics struct {
	Metrics []skyMetric `json:"metrics"`
}

type skyMetric struct {
	Name  string        `json:"name"`
	Sum   *skySum       `json:"sum"`
	Gauge *skyGaugeShim `json:"gauge"`
}

type skySum struct {
	DataPoints []skyDataPoint `json:"dataPoints"`
}

type skyGaugeShim struct {
	DataPoints []skyDataPoint `json:"dataPoints"`
}

type skyDataPoint struct {
	TimeUnixNano string         `json:"timeUnixNano"`
	Attributes   []skyAttribute `json:"attributes"`
	AsDouble     float64        `json:"asDouble"`
}

func serviceNameFromSkyAttrs(attrs []skyAttribute) string {
	for _, a := range attrs {
		if a.Key == "service.name" && a.Value.StringValue != "" {
			return a.Value.StringValue
		}
	}
	return unknownService
}

func skyAttrsToMap(attrs []skyAttribute) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]string, len(attrs))
	for _, a := range attrs {
		switch {
		case a.Value.StringValue != "":
			out[a.Key] = a.Value.StringValue
		case a.Value.IntValue != "":
			out[a.Key] = a.Value.IntValue
		case a.Value.DoubleValue != 0:
			out[a.Key] = fmt.Sprintf("%g", a.Value.DoubleValue)
		case a.Value.BoolValue != nil:
			out[a.Key] = fmt.Sprintf("%t", *a.Value.BoolValue)
		}
	}
	return out
}

// parseNanosString parses a string-encoded UnixNano (OTLP JSON
// represents int64 as a quoted string per the OTLP/JSON spec). An
// empty / unparseable value returns time.Now() so a malformed
// receiver entry doesn't sink a whole batch.
func parseNanosString(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	// Manual int64 parse to avoid importing strconv twice for what's
	// already a hot path.
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return time.Now().UTC()
		}
		n = n*10 + int64(c-'0')
	}
	if n == 0 {
		return time.Now().UTC()
	}
	return time.Unix(0, n).UTC()
}
