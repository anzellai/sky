# Std.Ai.Serve: the GPU tier

`Std.Ai.Serve` runs one base model and many fine-tuned LoRA adapters on one
GPU, behind a service token, and follows the registry's aliases. The app keeps
calling an `Endpoint`; only the slot's URL changes.

```elm
import Std.Ai.Serve as Serve

tier : Serve.Tier
tier =
    Serve.tier Serve.Vllm Serve.devBase
        |> Serve.withAdapters [ "support@prod", "support@qa" ]
        |> Serve.withMaxAdapters 4

main =
    Task.run (Serve.run tier)
```

## What the tier does

`Serve.run` is the tier's main. It stages the base model's pinned files below
the AI home (each file checked against its SHA-256), then stands in front of
vLLM, which runs next to it on the same machine with LoRA enabled and runtime
adapter loading on. The tier:

* serves `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings` and
  `/v1/models` behind the service token (`Authorization: Bearer`), compared in
  constant time. A tier on all interfaces with no token does not start
  (`NotConfigured`);
* maps the request's `model` to an adapter. The base answers to its served
  name (default: the repository's name in lower case, `qwen2.5-0.5b-instruct`)
  and to an empty `model`. An adapter answers to its registry version's served
  name, `<name>-v<n>`, which is what an app's `Endpoint` sends for
  `registry:<name>@<alias>`;
* keeps each alias in `withAdapters` loaded, and loads any other version on
  demand. When it holds `withMaxAdapters` adapters, it evicts the least
  recently used one that no alias pins and no request uses;
* reads the registry's pointer objects in its store every
  `withAliasPolling` seconds (default 30). A promoted version is loaded at
  once; the previous version stays loaded until its last request finishes,
  then it is unloaded. No request fails across a promotion;
* answers `/health` 200 only when vLLM is up and every alias is loaded, and
  503 with the reason otherwise (`AdapterMismatch` for an alias trained on
  another base);
* answers `/metrics` (Prometheus text) with its own counters
  (`sky_ai_tier_requests_total`, `_inflight`, `_resident_adapters`,
  `_adapter_loads_total`, `_adapter_unloads_total`, `_ready`) and vLLM's
  signals (`sky_ai_engine_running`, `_waiting`, `_kv_cache`,
  `_preemptions`, `_queue_wait_ms`). The Sky Console's AI tab shows the
  same under "GPU tiers";
* pushes its autoscale signals to OpenTelemetry when an OTLP endpoint is
  set (see [The signals in OpenTelemetry](#the-signals-in-opentelemetry)).

### The signals in OpenTelemetry

Every 15 seconds the tier scrapes vLLM's `/metrics`. After each scrape, when
`OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` is
set, it pushes its signals to the collector as OTLP/HTTP protobuf
(`Content-Type: application/x-protobuf`).

| Variable | Meaning |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | The collector's base URL. The tier keeps its path and appends `v1/metrics`: `https://otlp.example/otlp` sends to `https://otlp.example/otlp/v1/metrics`. |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | The full metrics URL, used as given. It wins over the base. |
| `OTEL_EXPORTER_OTLP_HEADERS` | `key=value` pairs, comma-separated, sent with each push (a vendor's API key). |
| `OTEL_EXPORTER_OTLP_METRICS_HEADERS` | The same, for metrics only. A name here wins over the general one. |
| `OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_EXPORTER_OTLP_METRICS_PROTOCOL` | `grpc` turns the push off with one warning. Sky sends HTTP/protobuf only. An endpoint on port 4317 (the gRPC port) does the same. |
| `SKY_SERVICE_NAME`, then `OTEL_SERVICE_NAME`, then `K_SERVICE` | The resource's `service.name`. Traces carry the same one. |

The metrics have the names `/metrics` uses. Each carries the attributes
`tier` (the tier's bind address), `engine` and `base`.

| Metric | Kind | Meaning |
|---|---|---|
| `sky_ai_tier_ready` | gauge | 1 when the engine is up and every alias is loaded, else 0 |
| `sky_ai_tier_inflight` | gauge | requests the tier serves now |
| `sky_ai_tier_resident_adapters` | gauge | adapters loaded in the engine |
| `sky_ai_engine_running` | gauge | requests vLLM runs now |
| `sky_ai_engine_waiting` | gauge | requests in vLLM's queue |
| `sky_ai_engine_kv_cache` | gauge | KV-cache use, 0 to 1 |
| `sky_ai_engine_queue_wait_ms` | gauge | the mean queue wait in the last window, in milliseconds |
| `sky_ai_engine_preemptions` | cumulative monotonic sum | requests vLLM preempted since it started |

`queueWaitMs` (`sky_ai_engine_queue_wait_ms`) comes from vLLM's
`vllm:request_queue_time_seconds` histogram: the change in its sum divided
by the change in its count between two scrapes. It lags. It covers only the
requests that left the queue in that window, so a request still waiting is
not in it: read `sky_ai_engine_waiting` for those. A window in which no
request left the queue has no value (the signal is absent, not 0). A window
in which the totals went down (vLLM restarted) is skipped.

A push has a 5-second timeout and is not retried: the next scrape sends
fresh values. It runs apart from the scrape, so a slow collector never
delays the tier's work or its shutdown. A failed push prints one line, and
again only when the failure changes.

The tier reads only the registry's **store**, never the app's database. Each
`Registry.register` writes the version's manifest
(`models/<name>/versions/<n>.json`) and each alias move rewrites its pointer
(`aliases/<name>/<alias>.json`). See [registry.md](registry.md).

### The base-model check

A version's manifest records the original weights it was trained on
(`baseId`, `<org>/<repo>@<commit>`). The tier serves only adapters of its own
base: an adapter trained on another model is `AdapterMismatch`. A QLoRA
adapter trained on the 4-bit Unsloth snapshot of a model serves on that
model's original 16-bit weights: both have the same `baseId`.

### What vLLM loads

vLLM loads an adapter in the PEFT layout: `adapter_model.safetensors` and
`adapter_config.json`. A version that `Std.Ai.Train` registered carries both
(the version is the GGUF file, and the PEFT files are its companions, see
[registry.md](registry.md)). A version registered as a GGUF file alone cannot
be served by vLLM (`InvalidSetting`); `Std.Ai.Local` serves it.

## Settings

Each setting is a `withX` builder in code, and an operator's variable wins:

| Builder | Variable | Default |
|---|---|---|
| `withBind` | `SKY_AI_SERVE_BIND` | `0.0.0.0:8080` |
| `withToken` | `SKY_AI_SERVE_TOKEN` | none (loopback only) |
| `withAdapters` | `SKY_AI_SERVE_ADAPTERS` (comma list) | none |
| `withMaxAdapters` | `SKY_AI_SERVE_MAX_ADAPTERS` | 4 |
| `withAliasPolling` | `SKY_AI_SERVE_POLL_S` | 30 |
| `withEngineUrl` | `SKY_AI_SERVE_ENGINE_URL` | `http://127.0.0.1:8000` |
| `withModelsDir` | `SKY_AI_SERVE_MODELS` | `/models` |
| `withRegistry` | `SKY_AI_REGISTRY` | `file://.sky/ai/registry` |
| the engine | `SKY_AI_SERVE_ENGINE` | `vllm` |

`withMaxModelLength` and `withGpuMemoryFraction` set vLLM's
`--max-model-len` and `--gpu-memory-utilization`. `Serve.vllmArgs` gives the
whole command line.

The base is code only: it is pinned by the digest of every file, which a
variable cannot carry.

## Calling the tier from an app

```elm
supportSlot : Endpoint
supportSlot =
    Endpoint.endpoint "support"
        |> Endpoint.withGoogleAuth
        |> Endpoint.fromRegistry (Registry.resolver openRegistry)
        |> Endpoint.withAdapter "registry:support@prod"
```

and, for the QA or production environment:

```
SKY_AI_SUPPORT_URL=https://<service>.run.app/v1
SKY_AI_SUPPORT_KEY=<the tier's service token>
```

A first request after the tier scaled to zero waits for a cold start, so
give the slot a long timeout (`Endpoint.withTimeout`, or
`SKY_AI_<SLOT>_TIMEOUT_MS`).
`Endpoint.withGoogleAuth` (or `SKY_AI_<SLOT>_AUTH=google`) adds a Google ID
token for the service's URL in `X-Serverless-Authorization`. Cloud Run checks
it and removes it before the request reaches the tier; `Authorization` keeps
the service token. The ID token comes from `GOOGLE_APPLICATION_CREDENTIALS`
(a service-account key file) or the metadata server, and is reused until five
minutes before it expires.

## Deploying

`Serve.cloudRun` deploys the tier to Google Cloud Run with one NVIDIA L4 that
scales to zero. See [deploy-gcp.md](deploy-gcp.md).

```
sky ai serve [--adapters <name>@<alias>,...] [--registry <url>]
                      run the tier on the development base (the container's entry)
sky ai serve --image --out <dir> --service <name> [target flags]
                      the linux/amd64 tier binary, its Dockerfile, service.json and service.yaml
sky ai deploy --service <name> --tier-image <ref> [target flags] [--dry-run]
sky ai teardown --service <name>
```

A tier on another base is a program of your own: build it with
`GOOS=linux GOARCH=amd64 sky build src/Tier.sky` (the runtime is pure Go, so
the binary is static) and use `Serve.render` for its Dockerfile and service.

## Cold start

A Cloud Run instance that scaled to zero starts again on the next request:
the platform pulls the two images (vLLM's is about 11 GB), the tier stages the
base (about 1 GB for the development base), vLLM loads it, and the tier loads
the aliases. The live test (stage l5) measures it on every run: `coldStartMs`
for the first call after the deploy and `idleColdStartMs` for the first call
after a 16-minute idle wait. The ledger, `scripts/ai/live/gcp-ledger.jsonl`,
keeps them under `measurements.l5`. The passing run of 2026-10-14
(europe-west4, one L4, the development base) measured 2263 ms for the first
call after the deploy, which met the instance the deploy had started, and
107390 ms (about 107 s) from zero after the idle wait. An earlier run measured
119946 ms from zero. See
[testing-guide.md](testing-guide.md#live-gpu-runs-on-google-cloud). Warm the
tier before traffic with `Endpoint.warm`, or keep an instance with
`Serve.withMinInstances 1` (billed while idle).

## Tests

* `runtime-go/rt/ai_serve_test.go`: routing, the token, on-demand loading and
  eviction, the hot-load with zero failed requests, health gating, SSE
  pass-through, signals; against `runtime-go/rt/testdata/fakevllm`.
* `runtime-go/rt/ai_serve_otel_test.go`: the queue wait over a scrape window
  (an empty window and a reset), and the OTLP push against a fake collector
  that decodes the protobuf (names, values, the `tier` attribute,
  `service.name`, headers, and no push under the gRPC guard).
* `tests/Std/Ai/ServeTest.sky`: vLLM's arguments, the Cloud Run service, its
  Knative YAML, the Dockerfile.
* `rust/crates/sky/tests/ai_serve_flow.rs`: `sky ai serve` on the fake engine
  with a registry store (two adapters, a promotion under load), and
  `deploy --dry-run`, `deploy`, `cloud ls` and `teardown` against the Google
  Cloud emulator.
* `rust/crates/sky/tests/ai_gcp_live.rs` (stage l5): the real tier on Cloud
  Run, through `scripts/ai/gcp-live.sh`.
