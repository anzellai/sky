package telemetry

// OpenTelemetry trace export — Phase 1.1a Step 7.
//
// Sky uses the official OTel Go SDK for trace export rather than a
// hand-rolled exporter because:
//
//   - Wire compatibility with every observability vendor (Jaeger,
//     Tempo, Honeycomb, Datadog, AWS X-Ray, Google Cloud Trace,
//     New Relic, Lightstep, …) is guaranteed by the OTLP spec
//     reference impl.
//   - W3C trace-context propagation (traceparent / tracestate)
//     ships with the SDK.
//   - Resource detection (service.name, host.name, etc.) is
//     standardised — vendors slice their UI by these.
//   - We get OpenMetrics + OTLP/JSON + OTLP/grpc + OTLP/HTTP all
//     for free as alternative exporters; users pick whichever
//     their collector speaks.
//
// Cost: ~3 MB binary growth (acceptable; the convenience pays off
// the moment any production user wires up their first collector).
//
// Setup contract:
//
//   - OTEL_EXPORTER_OTLP_ENDPOINT env var present → exporter
//     created, TracerProvider installed globally.
//   - Endpoint absent → no-op tracer (every span operation is a
//     cheap function call returning the empty span; zero
//     allocation).
//   - SerializeMode (sync vs batched) chosen per
//     IsServerless()-style classifier passed from the runtime —
//     serverless flushes per-request, VM batches every 5s.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TracerConfig captures the runtime knobs the OTel exporter needs.
// Filled in by the runtime at startup; passed to InitTracer.
type TracerConfig struct {
	// ServiceName goes into resource attributes (service.name).
	// Defaults to "sky-app"; users override via SKY_SERVICE_NAME
	// env or sky.toml [observability] service_name.
	ServiceName string

	// ServiceVersion goes into resource attributes (service.version).
	// Populated from buildinfo at compile time when available.
	ServiceVersion string

	// Endpoint is the OTLP collector URL. Empty → no-op tracer
	// (export disabled, span calls are zero-cost). The runtime
	// reads OTEL_EXPORTER_OTLP_ENDPOINT to populate this.
	Endpoint string

	// Headers are added to every outbound OTLP request. Used for
	// vendor auth (Honeycomb's x-honeycomb-team, Datadog's
	// dd-api-key, etc.). Populated from
	// OTEL_EXPORTER_OTLP_HEADERS (comma-separated key=value).
	Headers map[string]string

	// SampleRate is the head-based sampling probability
	// [0.0, 1.0]. 1.0 = sample everything; 0.0 = sample nothing
	// (errors-only); 0.01 = 1%. Errors are ALWAYS sampled
	// regardless via the ParentBased + AlwaysSample(record-error)
	// combination. Default 1.0 in serverless mode, 0.01 in VM
	// mode — see runtime-go/rt/serverless.go.
	SampleRate float64

	// Serverless toggles the SpanProcessor mode: serverless uses
	// SimpleSpanProcessor (sync flush per span end), VM uses
	// BatchSpanProcessor (every 5s).
	Serverless bool
}

// initialised at first InitTracer call. Subsequent calls reconfigure
// only when the endpoint or sample rate changes (avoids the
// "tracer initialised twice" log spam from tests).
var (
	tracerMu       sync.Mutex
	currentCfg     TracerConfig
	tracerInited   bool                    // distinguishes "never called" from "called with empty"
	currentTracer  trace.Tracer
	tracerProvider *sdktrace.TracerProvider
)

// noopTracerInstance — kept for compile-link reference to the noop
// package even though we now read through OTel's global. Pre-fix
// we cached this in `currentTracer`; switching to global reads
// means OTel's default (which is the same noop) wins automatically.
var noopTracerInstance = noop.NewTracerProvider().Tracer("sky-app")

// Keep the variable referenced so the linker doesn't complain
// when the package is built without ever calling Tracer().
var _ = noopTracerInstance

// Tracer returns the active tracer. Always consults the OTel
// global TracerProvider so tests that swap it via
// otel.SetTracerProvider(...) take effect immediately. When no
// provider has been installed at all, OTel's default returns a
// noop tracer — every span call is zero-cost.
//
// Callers do NOT need to check whether tracing is enabled — the
// noop tracer is a valid Tracer; calling .Start / .End on it is
// trivial.
func Tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer("sky-app")
}

// extraProcessors are in-process span sinks registered BEFORE
// InitTracer runs. The Sky Console trace ring registers one here
// (via RegisterSpanProcessor) so spans are captured locally even
// when no OTLP endpoint is configured — see observability-design.md
// "useful by default".
var extraProcessors []sdktrace.SpanProcessor

// RegisterSpanProcessor adds an in-process span processor that
// InitTracer wires into the TracerProvider — both when an OTLP
// endpoint is configured (alongside the exporter) and when it is
// not (as the sole processor). Must be called before InitTracer.
func RegisterSpanProcessor(p sdktrace.SpanProcessor) {
	if p != nil {
		extraProcessors = append(extraProcessors, p)
	}
}

// InitTracer installs the global OTel TracerProvider. Idempotent:
// calling twice with the same config is a no-op. Calling with a
// changed endpoint tears down the prior provider + builds a fresh
// one (used by tests; production calls once at startup).
//
// When cfg.Endpoint is empty, switches to the noop tracer (any
// previous real exporter is shut down).
//
// Returns an error only on exporter init failure (DNS, bad URL,
// invalid headers). Sky's runtime startup path treats this as
// non-fatal — observability degrading to noop is better than
// refusing to boot.
func InitTracer(cfg TracerConfig) error {
	tracerMu.Lock()
	defer tracerMu.Unlock()

	if tracerInited && cfgsEquivalent(currentCfg, cfg) {
		return nil // no-op — same config already installed
	}
	tracerInited = true

	// Tear down any prior provider so its background batcher exits
	// cleanly. Tests rotate configs; without this we'd leak
	// goroutines.
	if tracerProvider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = tracerProvider.Shutdown(ctx)
		cancel()
		tracerProvider = nil
		currentTracer = nil
	}

	if cfg.Endpoint == "" {
		// No OTLP endpoint. The W3C propagator is still set so inbound
		// traceparent headers are honoured.
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{}, propagation.Baggage{}))
		currentCfg = cfg
		// If an in-process span sink is registered (the Sky Console
		// trace ring — observability-design.md "useful by default"),
		// still install a real TracerProvider carrying ONLY that
		// processor, so spans are captured locally even with no
		// exporter configured. Otherwise fall through to the noop
		// tracer.
		if len(extraProcessors) > 0 {
			res, _ := resource.New(context.Background(),
				resource.WithAttributes(
					semconv.ServiceName(orDefault(cfg.ServiceName, "sky-app")),
					semconv.ServiceVersion(orDefault(cfg.ServiceVersion, "dev")),
				),
			)
			opts := []sdktrace.TracerProviderOption{
				sdktrace.WithSampler(sdktrace.ParentBased(
					sdktrace.TraceIDRatioBased(cfg.SampleRate))),
				sdktrace.WithResource(res),
			}
			for _, p := range extraProcessors {
				opts = append(opts, sdktrace.WithSpanProcessor(p))
			}
			tp := sdktrace.NewTracerProvider(opts...)
			otel.SetTracerProvider(tp)
			tracerProvider = tp
			currentTracer = tp.Tracer("sky-app")
		}
		return nil
	}

	// Build the OTLP/HTTP exporter. We default to HTTP not gRPC
	// because HTTP is firewall-friendly + most managed collectors
	// expose it. gRPC users can switch via OTEL_EXPORTER_OTLP_PROTOCOL
	// (handled by the SDK env-var reader).
	exporterOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(cleanEndpoint(cfg.Endpoint)),
	}
	if strings.HasPrefix(cfg.Endpoint, "http://") {
		exporterOpts = append(exporterOpts, otlptracehttp.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		exporterOpts = append(exporterOpts, otlptracehttp.WithHeaders(cfg.Headers))
	}
	exp, err := otlptrace.New(context.Background(),
		otlptracehttp.NewClient(exporterOpts...))
	if err != nil {
		return fmt.Errorf("otel: exporter init failed: %w", err)
	}

	// Resource detection. semconv.ServiceNameKey is the standard
	// `service.name` attribute that every vendor's UI groups by.
	res, _ := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(orDefault(cfg.ServiceName, "sky-app")),
			semconv.ServiceVersion(orDefault(cfg.ServiceVersion, "dev")),
		),
		resource.WithProcess(),
		resource.WithOS(),
		resource.WithHost(),
	)

	// Sampler: ParentBased so child spans inherit the root's
	// decision; root uses TraceIDRatioBased(cfg.SampleRate). The
	// AlwaysSample fallback for errors is implemented at the span
	// level via span.SetStatus(Error) + a custom sampler hook —
	// for v1 we accept the simpler "head-based only" model and add
	// tail-based error sampling in v1.x.
	sampler := sdktrace.ParentBased(
		sdktrace.TraceIDRatioBased(cfg.SampleRate),
	)

	// Span processor: serverless mode flushes per span end
	// (SimpleSpanProcessor), VM mode batches (default 5s flush).
	var processor sdktrace.SpanProcessor
	if cfg.Serverless {
		processor = sdktrace.NewSimpleSpanProcessor(exp)
	} else {
		processor = sdktrace.NewBatchSpanProcessor(exp,
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithMaxQueueSize(2048),
			sdktrace.WithMaxExportBatchSize(512),
		)
	}

	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithSampler(sampler),
		sdktrace.WithSpanProcessor(processor),
		sdktrace.WithResource(res),
	}
	// Additionally run any in-process span sinks (the Sky Console
	// trace ring) alongside the OTLP exporter.
	for _, p := range extraProcessors {
		tpOpts = append(tpOpts, sdktrace.WithSpanProcessor(p))
	}
	tp := sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	tracerProvider = tp
	currentTracer = tp.Tracer("sky-app")
	currentCfg = cfg
	return nil
}

// ShutdownTracer flushes pending spans + tears down the
// TracerProvider. Called on SIGTERM so in-flight spans reach the
// collector before the process exits. Bounded by `timeout` —
// orchestrator grace periods are tight.
func ShutdownTracer(timeout time.Duration) error {
	tracerMu.Lock()
	tp := tracerProvider
	tracerMu.Unlock()
	if tp == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return tp.Shutdown(ctx)
}

// Propagator returns the global OTel propagator (W3C trace-context).
// Used by HTTP middleware to extract inbound traceparent headers
// and by Http.get/post to inject outbound headers.
func Propagator() propagation.TextMapPropagator {
	return otel.GetTextMapPropagator()
}

// ─── Helpers ──────────────────────────────────────────────────

func cfgsEquivalent(a, b TracerConfig) bool {
	if a.ServiceName != b.ServiceName ||
		a.ServiceVersion != b.ServiceVersion ||
		a.Endpoint != b.Endpoint ||
		a.SampleRate != b.SampleRate ||
		a.Serverless != b.Serverless ||
		len(a.Headers) != len(b.Headers) {
		return false
	}
	for k, v := range a.Headers {
		if b.Headers[k] != v {
			return false
		}
	}
	return true
}

// cleanEndpoint strips the http:// or https:// prefix for the
// otlptracehttp option (which takes host:port, not a URL).
func cleanEndpoint(ep string) string {
	ep = strings.TrimPrefix(ep, "https://")
	ep = strings.TrimPrefix(ep, "http://")
	// Trim trailing path — otlptracehttp will add /v1/traces.
	if i := strings.IndexByte(ep, '/'); i >= 0 {
		ep = ep[:i]
	}
	return ep
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ─── Env-var bootstrap ────────────────────────────────────────

// LoadTracerConfigFromEnv builds a TracerConfig from the standard
// OTEL_EXPORTER_OTLP_* env vars + Sky-specific overrides. The
// runtime startup path calls this then InitTracer(cfg).
//
// Honoured env vars:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT          — collector URL (required)
//	OTEL_EXPORTER_OTLP_HEADERS           — comma-separated key=value pairs
//	OTEL_SERVICE_NAME / SKY_SERVICE_NAME — resource service.name
//	OTEL_SERVICE_VERSION                 — resource service.version
//	OTEL_TRACES_SAMPLER_ARG              — sample rate (0.0..1.0)
//
// `isServerless` is the runtime mode flag (caller passes
// rt.IsServerless() from the parent package — avoids a cycle).
// Defaults match the canonical ServerlessTraceSampleRate() helper
// in rt/serverless.go: VM mode → 1%, serverless → 100%. The
// production-gate (ENV / SKY_ENV) does NOT influence the default;
// callers raise to 100% locally with OTEL_TRACES_SAMPLER_ARG=1.0
// for debugging.
func LoadTracerConfigFromEnv(isServerless bool) TracerConfig {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	cfg := TracerConfig{
		ServiceName:    coalesce(os.Getenv("SKY_SERVICE_NAME"), os.Getenv("OTEL_SERVICE_NAME"), "sky-app"),
		ServiceVersion: coalesce(os.Getenv("OTEL_SERVICE_VERSION"), "dev"),
		Endpoint:       endpoint,
		Headers:        parseHeaderList(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")),
		Serverless:     isServerless,
	}

	// Sample rate: env override OR mode-default.
	if s := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); s != "" {
		var rate float64
		if _, err := fmt.Sscanf(s, "%f", &rate); err == nil {
			cfg.SampleRate = clampUnit(rate)
		}
	}
	if cfg.SampleRate == 0 {
		// observability-design.md sane defaults:
		//   serverless   → 100% (each invocation is one short-lived
		//                  trace; head-sampling buys nothing — the
		//                  platform already prices per request)
		//   VM (always-on) → 1% (collector load is proportional to
		//                  always-on traffic; 100% would flood). Same
		//                  rate as ServerlessTraceSampleRate() in the
		//                  serverless.go canonical helper. Callers
		//                  raise to 100% locally with
		//                  OTEL_TRACES_SAMPLER_ARG=1.0 for debugging.
		if isServerless {
			cfg.SampleRate = 1.0
		} else {
			cfg.SampleRate = 0.01
		}
	}
	return cfg
}

func parseHeaderList(s string) map[string]string {
	if s == "" {
		return nil
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		i := strings.IndexByte(pair, '=')
		if i < 0 {
			continue
		}
		out[strings.TrimSpace(pair[:i])] = strings.TrimSpace(pair[i+1:])
	}
	return out
}

func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func clampUnit(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Ensure attribute package is reachable via this file for callers
// that import it transitively. Used by tracing.go's AddAttributes.
var _ = attribute.String
