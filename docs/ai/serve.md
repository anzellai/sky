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
  signals (`sky_ai_engine_running`, `_waiting`, `_kv_cache`). The Sky
  Console's AI tab shows the same under "GPU tiers".

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
the aliases. The live test (stage l5) measures it on every run and prints
it in the scenario's JSON line (`ai_gcp_live: l5: …`): `coldStartMs` for the
first call after the deploy and `idleColdStartMs` for the first call after a
16-minute idle wait. The ledger, `scripts/ai/live/gcp-ledger.jsonl`, does not
keep these figures. Warm the tier before
traffic with `Endpoint.warm`, or keep an instance with
`Serve.withMinInstances 1` (billed while idle).

## Tests

* `runtime-go/rt/ai_serve_test.go`: routing, the token, on-demand loading and
  eviction, the hot-load with zero failed requests, health gating, SSE
  pass-through, signals; against `runtime-go/rt/testdata/fakevllm`.
* `tests/Std/Ai/ServeTest.sky`: vLLM's arguments, the Cloud Run service, its
  Knative YAML, the Dockerfile.
* `rust/crates/sky/tests/ai_serve_flow.rs`: `sky ai serve` on the fake engine
  with a registry store (two adapters, a promotion under load), and
  `deploy --dry-run`, `deploy`, `cloud ls` and `teardown` against the Google
  Cloud emulator.
* `rust/crates/sky/tests/ai_gcp_live.rs` (stage l5): the real tier on Cloud
  Run, through `scripts/ai/gcp-live.sh`.
