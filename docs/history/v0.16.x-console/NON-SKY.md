# Sky Console v0.16.x — Non-Sky service integration

> How Python, Node.js, Go, Rust, Java apps push telemetry to a Sky Console hub
> via their standard OTel SDKs. Lands in v0.16.4 (recipes + validation).

## Why this matters

Sky's hub becomes a **universal observability backend**, not Sky-only. Teams with mixed-language stacks (Sky for new code + Python for the ML pipeline + Node for an existing API) all push to one hub via the OpenTelemetry Protocol (OTLP) — the open standard.

This is Sky Console's biggest market wedge. Teams adopt the hub for their EXISTING Python/Node/Go services first (immediate value), THEN learn that Sky exists, THEN try writing new services in Sky. The observability tool is the trojan horse for language adoption.

## OTLP is the contract

Every supported language has an OTel SDK that emits OTLP. The hub accepts OTLP gRPC (port 4317) and HTTP (port 4318). No protocol translation needed.

Recipes below show the minimal config for each language to push to a Sky Console hub. Each recipe is verified end-to-end in v0.16.4 and shipped as part of the docs.

## Python

```python
# pip install opentelemetry-api opentelemetry-sdk opentelemetry-exporter-otlp

from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource

resource = Resource.create({
    "service.name": "my-python-service",
    "service.version": "1.0.0",
})

provider = TracerProvider(resource=resource)
provider.add_span_processor(BatchSpanProcessor(
    OTLPSpanExporter(
        endpoint="https://obs.your-company.com:4317",
        headers={"authorization": f"Bearer {YOUR_HUB_TOKEN}"},
    )
))
trace.set_tracer_provider(provider)

# Now any code using tracer.start_as_current_span emits to the hub
```

For logs: `opentelemetry-sdk` includes a logs API. For metrics: similar pattern with `MeterProvider` and `OTLPMetricExporter`.

**Easiest path** for Python web frameworks: auto-instrumentation via `opentelemetry-bootstrap`:

```bash
pip install opentelemetry-distro opentelemetry-exporter-otlp
opentelemetry-bootstrap --action=install
OTEL_EXPORTER_OTLP_ENDPOINT="https://obs.your-company.com:4317" \
  OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer ${TOKEN}" \
  OTEL_SERVICE_NAME="my-flask-app" \
  opentelemetry-instrument python app.py
```

Zero code changes. Auto-instruments Flask, Django, FastAPI, requests, SQLAlchemy, redis-py, etc.

## Node.js

```bash
npm install @opentelemetry/api @opentelemetry/sdk-node \
  @opentelemetry/exporter-trace-otlp-grpc \
  @opentelemetry/auto-instrumentations-node
```

```javascript
// otel-init.js — loaded before app code
const { NodeSDK } = require('@opentelemetry/sdk-node');
const { OTLPTraceExporter } = require('@opentelemetry/exporter-trace-otlp-grpc');
const { getNodeAutoInstrumentations } = require('@opentelemetry/auto-instrumentations-node');

const sdk = new NodeSDK({
    serviceName: 'my-node-service',
    traceExporter: new OTLPTraceExporter({
        url: 'https://obs.your-company.com:4317',
        headers: { authorization: `Bearer ${process.env.HUB_TOKEN}` },
    }),
    instrumentations: [getNodeAutoInstrumentations()],
});

sdk.start();
```

```bash
node -r ./otel-init.js app.js
```

Auto-instruments Express, Fastify, Koa, HTTP client, MongoDB, Redis, etc.

## Go

```go
// go get go.opentelemetry.io/otel/sdk go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc

import (
    "context"
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
    "go.opentelemetry.io/otel/sdk/resource"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

func InitOTel(ctx context.Context, hubURL, hubToken string) (*sdktrace.TracerProvider, error) {
    exporter, err := otlptracegrpc.New(ctx,
        otlptracegrpc.WithEndpoint(hubURL),
        otlptracegrpc.WithHeaders(map[string]string{"authorization": "Bearer " + hubToken}),
    )
    if err != nil {
        return nil, err
    }
    res := resource.NewWithAttributes(
        semconv.SchemaURL,
        semconv.ServiceName("my-go-service"),
    )
    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exporter),
        sdktrace.WithResource(res),
    )
    otel.SetTracerProvider(tp)
    return tp, nil
}
```

Auto-instrumentation via `otelhttp.NewHandler` wrap for HTTP servers; `otelgrpc.UnaryClientInterceptor` for gRPC; OS-level via OpenTelemetry's Go agent.

## Rust

```toml
# Cargo.toml
[dependencies]
opentelemetry = "0.27"
opentelemetry_sdk = "0.27"
opentelemetry-otlp = { version = "0.27", features = ["grpc-tonic"] }
tonic = "0.12"
```

```rust
use opentelemetry::trace::TracerProvider;
use opentelemetry_otlp::WithExportConfig;
use opentelemetry_sdk::Resource;

fn init_otel(hub_url: &str, hub_token: &str) -> Result<(), Box<dyn std::error::Error>> {
    let mut metadata = tonic::metadata::MetadataMap::new();
    metadata.insert("authorization", format!("Bearer {}", hub_token).parse()?);

    let provider = opentelemetry_otlp::new_pipeline()
        .tracing()
        .with_exporter(
            opentelemetry_otlp::new_exporter()
                .tonic()
                .with_endpoint(hub_url)
                .with_metadata(metadata),
        )
        .with_trace_config(opentelemetry_sdk::trace::config().with_resource(
            Resource::new(vec![opentelemetry::KeyValue::new("service.name", "my-rust-service")])
        ))
        .install_batch(opentelemetry_sdk::runtime::Tokio)?;

    opentelemetry::global::set_tracer_provider(provider);
    Ok(())
}
```

## Java / JVM

```bash
# OpenTelemetry Java Agent — zero-code instrumentation
curl -L -O https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/latest/download/opentelemetry-javaagent.jar

OTEL_EXPORTER_OTLP_ENDPOINT="https://obs.your-company.com:4317" \
  OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer ${TOKEN}" \
  OTEL_SERVICE_NAME="my-jvm-service" \
  java -javaagent:opentelemetry-javaagent.jar -jar app.jar
```

Works for Spring Boot, Quarkus, plain JVM apps. Auto-instruments JDBC, HTTP servers, Kafka, etc.

## Already-instrumented existing services

If a service already pushes telemetry somewhere else (Datadog, Jaeger, Zipkin), three options:

1. **Replace the exporter** — point the existing OTel SDK at the Sky hub instead. Simplest, no parallel infra.
2. **Use the OTel Collector** as a fan-out: app → OTel Collector → both old destination + Sky hub. Allows gradual migration.
3. **Run an OTLP-to-Jaeger / OTLP-to-Zipkin shim** on the existing infra. Sky hub speaks OTLP, the shim translates.

The hub can also be configured to FORWARD a subset of incoming OTLP to upstream destinations (e.g., critical errors also go to PagerDuty). That's an Ops feature in v0.16.5.

## Validation matrix for v0.16.4

Each language gets a verified end-to-end recipe:

| Language | Auto-instrument | Manual span | Logs | Metrics | Validated |
|---|---|---|---|---|---|
| Python | Flask/Django/FastAPI | ✓ | ✓ | ✓ | Day 1 |
| Node.js | Express/Fastify | ✓ | ✓ | ✓ | Day 1 |
| Go | net/http + gRPC | ✓ | ✓ | ✓ | Day 2 |
| Rust | actix-web | ✓ | ✓ | ✓ | Day 2 |
| JVM | Java agent (auto) | ✓ | ✓ | ✓ | Day 3 |
| Ruby | otel-ruby | ✓ | ✓ | partial | Day 3 (best-effort) |
| PHP | otel-php | ✓ | ✓ | partial | Day 3 (best-effort) |

Each row is a demo app + recipe + screenshot of the data appearing in the hub UI. Shipped as `docs/v0.16.x-console/recipes/<lang>/`.

## Caveats and pitfalls

**Sampling configuration matters at scale.** OTel SDKs default to 100% sampling, which can flood the hub. For high-traffic non-Sky services, the SDK should be configured with a sampler:

```python
# Python — Tail-based not supported in SDK; use head sampling
provider = TracerProvider(sampler=ParentBased(TraceIdRatioBased(0.1)))  # 10%
```

Sky's in-process exporter handles sampling at the exporter (errors 100%, slow 10%, fast 1%). Non-Sky services need explicit SDK config to match.

**Trace context propagation across services.** OTel uses W3C Trace Context (`traceparent` header) by default. If services communicate over HTTP, the SDKs propagate automatically. For raw TCP / WebSocket / custom protocols, manual propagation needed.

**Resource attributes are critical.** Always set `service.name` (mandatory) and ideally `service.version` + `service.instance.id`. Without these the hub can't distinguish services. Recipes above include them; copy carefully.

**HTTPS vs gRPC plaintext.** gRPC default port 4317 expects plaintext or TLS depending on `--insecure` flag. For production hubs (recommended TLS), use `WithTLSCredentials(...)` or equivalent.

**Egress firewalls.** Some corporate networks block outbound gRPC. Recipes include the HTTP fallback for these cases — same OTLP, port 4318, HTTP/2 supported.

## Hub-side: receiver compatibility

The hub uses `go.opentelemetry.io/collector/receiver/otlpreceiver` — the canonical OTel receiver. It speaks OTLP versions:

- OTLP/gRPC v1.x (current spec)
- OTLP/HTTP v1.x with protobuf or JSON payloads

Backwards-compatible across OTel SDK versions: a 2-year-old Python OTel SDK can push to a v0.16.x hub successfully. Forward-compatible: new SDK versions stay compatible per OTel's stability guarantees.

## Operator's quick-start

Stand up a hub + register a non-Sky service in ~10 minutes:

```bash
# On hub VM (e2-small, $13/mo):
sky console serve --port 9000 --auth token

# Set token (printed at startup; persist via env)
echo "SKY_CONSOLE_HUB_TOKEN=<token-from-startup>" >> /etc/sky-console/.env

# On any service VM (Python example):
pip install opentelemetry-distro opentelemetry-exporter-otlp
opentelemetry-bootstrap --action=install
export OTEL_EXPORTER_OTLP_ENDPOINT="https://obs.your-company.com:4317"
export OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer ${TOKEN}"
export OTEL_SERVICE_NAME="my-python-service"
opentelemetry-instrument python app.py

# Open https://obs.your-company.com:9000 — service appears in dashboard
```

That's the entire onboarding flow. Zero Sky-specific knowledge needed for the Python team.
