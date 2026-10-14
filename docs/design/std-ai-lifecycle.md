# The AI application lifecycle in Sky: build, data, train, eval, registry, serve

> Status: **phase 1, the dataset and registry parts of phase 2, phase 3 and
> phase 4 on Google Cloud implemented (v0.28.0). The rest is design.** Phase 4
> adds `Train.Vertex`, `Std.Ai.Serve` and `Std.Ai.Cloud.Google` (section 8.8);
> what differs from this design is under "Shipped" in section 14, P4. Phase 3 adds `Std.Ai.Train` (section 8.5) with the `Mlx`,
> `Unsloth` and `Fake` backends, the managed Python toolchains, the durable
> training workflow, GGUF export and `Local.withAdapter`. User guide:
> [`../ai/train.md`](../ai/train.md). Where it differs from this design is
> recorded under "Shipped" in section 14, P3. Phase 2 adds `Std.Ai.Registry` and `Std.Ai.Store`
> (section 8.7, with the record store moved to the app's database by the
> user's decision), judge calibration, pairwise judging and the judge journal
> (section 8.6), the startup-report lines and `sky doctor` checks (section
> 5.5), the registry view in the console (section 8.10) and `sky ai promote |
> rollback | lineage` (section 9). User guide: [`../ai/registry.md`](../ai/registry.md).
> `Std.Ai.Endpoint` (section 8.1), `Std.Ai.Local` (section 8.2, without
> adapters, draft models and registry references), the `Provider` additions
> `chatWith` and `embed` (section 8.3), and `sky ai provision | pull | ps |
> start | stop` ship; the user guide is [`../ai/local.md`](../ai/local.md).
> Part A reports failures with an existing `ErrorKind` and the failure's name
> in the message; the typed `Std.Ai.Failure` values are part B. Everything
> else in this document does not exist yet unless the text says it ships. It extends `Std.Ai` (see
> [`std-ai.md`](std-ai.md)) from "call a model" to "own the whole lifecycle of
> a model-backed application", written only in Sky. The live API for what
> already ships is `sky doc Std.Ai.Provider` / `Agent` / `Tool` / `Policy` /
> `Trace` / `Memory.Pg`.

## Summary

A Sky developer writes one language for the whole AI application. The same
Sky code builds the dataset, starts a fine-tuning job, evaluates the result,
gates it, registers it, promotes it and serves it. The app that uses the model
gets an ordinary `Provider` and never knows which tier it runs in.

Sky does not do the numeric work. Matrix arithmetic runs in upstream engines:
`llama-server` from llama.cpp, vLLM, MLX-LM, Unsloth, or a cloud training and
serving service. Sky owns everything around the numbers. It owns the workflow,
the data, the configuration, the gates, the lineage, the supervision and the
cost. This is the shape the field settled on in 2026 (Ollama now runs upstream
`llama-server` as a child process), and it is the shape Sky already uses for
embedded PostgreSQL (see [`../skydb/embedded-postgres.md`](../skydb/embedded-postgres.md)).

Configuration is code. There is no `sky.toml` section for AI. Each setting is a
`withX` builder in code that holds the local default, and an operator overrides
it per environment with an environment variable or a `.env` file. The
precedence is the one the runtime already uses for every other setting:
operator env, then the `withX` value, then the stdlib fallback.

## 1. What exists today, and what this design builds on

| Existing piece | Where | What this design uses it for |
|---|---|---|
| `Std.Ai.Provider` (`openai`, `compatible`, `router`, `custom`, `customTools`, `chat`, `chatTools`, `PriceBook`, `cost`) | `sky-stdlib/Std/Ai/Provider.sky` | The single type every tier hands the app. Local engines, GPU tiers and cloud endpoints all become a `Provider`. |
| `Std.Ai.Agent` (`oneShot`, `toolLoop`, `nativeToolLoop`) | `sky-stdlib/Std/Ai/Agent.sky` | Unchanged. Agents run on any `Provider` this design produces. |
| `Std.Ai.Policy`, `Std.Ai.Tool` | `sky-stdlib/Std/Ai/` | Unchanged. The firewall also gates promotion and paid launches (section 11). |
| `Std.Ai.Trace` (`forResponse`, `record`, `totalCost`) | `sky-stdlib/Std/Ai/Trace.sky` | Extended with latency, tokens per second, slot and adapter (section 8.10). |
| `Std.Ai.Memory.Pg` | `sky-stdlib/Std/Ai/Memory/Pg.sky` | Gains an embedding source: `Provider.embed` on a local or remote engine. |
| `Std.Durable` (`register`, `step`, `sleep`, `awaitSignal`, `poll`, `compact`) | `sky-stdlib/Std/Durable.sky`, [`durable-execution.md`](durable-execution.md) | Training, evaluation and deployment run as durable workflows. A paid launch is a journalled step, so it is exactly-once across a restart. |
| `Process.spawn` (`command`, `withArgs`, `withEnv`, `withClearEnv`, `readFrom`, `kill`, `wait`) | `sky-stdlib/Sky/Core/Process.sky` | Supervises `llama-server`, vLLM and the trainer drivers as child processes. |
| `Std.Watch` | `sky-stdlib/Std/Watch.sky` | Notices a new adapter file or checkpoint in development and reloads it. |
| Embedded PostgreSQL bundle | [`../skydb/embedded-postgres.md`](../skydb/embedded-postgres.md) | The precedent for a managed engine: pinned build, `SHA256SUMS` fetched first, atomic install, discovery order, registry of running instances, typed failure modes, no runtime fetch in production. |
| Config precedence (`configLayers`) | `runtime-go/rt/live_config_precedence.go` | One precedence for every AI setting: operator env > `withX` > fallback. |
| Process-private settings (`procenv`, v0.27.7) | `runtime-go/rt/procenv/procenv.go` | A value the program sets for itself never leaks into a child. The engine child gets an explicit, cleared environment. |
| Env prefix (`skyEnvName`) | `runtime-go/rt/env_prefix.go` | Every new AI variable is prefix-aware, like `SKY_LIVE_PORT`. |
| `Std.Codec`, `Std.Db.Store` | `sky-stdlib/Std/Codec.sky`, `sky-stdlib/Std/Db/Store.sky` | Dataset rows, eval cases, reports and registry manifests are records with one codec for JSON and the database. |
| `Std.Money`, `Std.Decimal` | `sky-stdlib/Std/Money.sky` | Every cost (inference, training, evaluation, budgets) is exact money, never a float. |
| `Sky.Core.Secret` | `sky-stdlib/Sky/Core/Secret.sky` | Every key and service token is a `Secret`. |
| `sky test` mock mode (`SKY_TEST_MODE`) | [`../tooling/testing.md`](../tooling/testing.md) | Offline tests answer every model call from fixtures. |

`Std.Ai` today adds no Go kernels. It is pure Sky over `Std.Durable` and
`Sky.Core.Http`. This design keeps that property wherever it can. The new Go
code is limited to what Sky cannot express without it: accelerator detection,
the artefact fetcher (shared with the PostgreSQL bundle code) and the setting
resolver that reuses `configLayers`.

## 2. Goals

1. **One language.** A user writes Sky only. No Python, no YAML, no shell
   script, no `sky.toml` section. The CLI verbs run the user's Sky program.
2. **Six lifecycle stages, each a typed module.** Build (serve models locally
   and remotely), data, train, eval, registry and serve.
3. **The app never knows its tier.** It receives a `Provider`. Development,
   QA, CI and production differ only in environment variables.
4. **Zero configuration in development.** A fresh clone runs the app with a
   managed local model and no setup, on a laptop with no GPU.
5. **Evaluation is a gate, not a report.** A candidate model cannot be promoted
   without a passing gate value, and the gate fails CI.
6. **Lineage is total.** Every served adapter names the dataset version, base
   model, training configuration, toolchain build and eval reports it came from.
7. **Typed failure for hardware facts.** "If it compiles, it works" cannot prove
   that a model fits in memory. Those facts become typed errors a program must
   handle, and Sky predicts them before it loads anything.

## 3. Non-goals

- **No GPU kernels and no tensor library.** llama.cpp, MLX, PyTorch and vLLM
  cover every backend. Ollama deleted about 430,000 lines of its own Go engine
  code in 2026 to stop doing this.
- **No model graphs in Go.** Sky never ports a transformer to Go.
- **No inference inside the Go wasm client.** Go's wasm port is wasm32 with a
  4 GB memory cap and runs tight loops at about a fifth of native speed. The
  browser and phone targets bridge to platform engines (phase 5).
- **No full pre-training.** Full fine-tuning needs about 16 bytes per parameter
  for model states alone. Sky supports adapter fine-tuning (LoRA, QLoRA) and
  says so. Full fine-tuning of small models is an open question, not a promise.
- **No own quantisation format and no own model file format.** GGUF for
  llama.cpp, safetensors for training and vLLM.
- **No in-process native engine.** A C abort inside the Go process bypasses the
  whole-app panic net. A child process that dies is an ordinary typed error.
- **No hidden Python for the user.** Python exists only inside Sky-managed,
  pinned, checksummed toolchains. The user never installs, edits or activates it.

## 4. Principles

1. **Typed values, Task effects.** Specs, jobs, suites and tiers are pure
   values built with `withX`. Anything that touches the world returns
   `Task Error a`. Hardware facts are typed failures (`ModelTooLarge`,
   `NoAccelerator`, `EngineCrashed`), not log lines.
2. **If it compiles, it works, and where it cannot, it fails before it costs.**
   `Local.fit`, `Train.plan` and `Serve.plan` predict memory, time and cost
   before anything loads, launches or bills.
3. **The code holds the local default. The operator holds the environment.**
   Section 5.
4. **Secrets are typed.** Every key is a `Secret`. A setting that carries a key
   reads its env fallback through `Secret.fromEnv`, never as a `String`.
5. **No user FFI.** Cloud APIs are reached over `Sky.Core.Http` with request
   signing written in Sky (`Crypto.hmacSha256` for AWS SigV4, `Jwt.rs256` for a
   Google service-account token). Engines are reached over HTTP and child
   process pipes.
6. **Exactly-once for anything that costs money.** A paid model call, a cloud
   training launch, an endpoint deployment and a judge call are each a
   `Durable.step`.
7. **Pin everything, verify before use.** Engines, toolchains, base models,
   adapters and datasets are content-addressed by SHA-256. A digest mismatch
   installs nothing.
8. **No silent fallback.** An unreachable remote endpoint never falls back to a
   local model, and a missing GPU never falls back to a slow CPU run in
   production. Both are typed errors. This mirrors the PostgreSQL rule that an
   unreachable cluster never falls back to SQLite.

## 5. Configuration: code holds the default, the environment overrides it

### 5.1 The rule

There is no AI configuration file. Every setting is a `withX` builder on a Sky
value. The value in code is the **local default**. QA, CI and production set an
environment variable (in the shell, a container, or a `.env` file) and that
value wins.

The resolver is the runtime's existing `configLayers`
(`runtime-go/rt/live_config_precedence.go`), called through one new kernel. It
gives the same order as `Live.withX` and `Sky.Config.withX`:

```
operator env  >  withX value in code  >  stdlib fallback
```

There is no seeded layer for AI, because there is no `sky.toml` to seed from.
Because the resolver is shared, the prefix rule, the `procenv` isolation and
the "an empty value is unset" rule are the same as for every other runtime
setting. Resolution happens when a value is first used (section 8.1), not at
compile time, so one binary runs in every tier.

### 5.2 Slots and variable names

An app may use several models: a chat model, an embedding model, a judge. Each
named use is a **slot**. A slot is a lower-case literal (`"chat"`, `"embed"`,
`"judge"`) given to the constructor. Its variables are:

```
<PREFIX>_AI_<SLOT>_<KEY>
```

`<PREFIX>` is the existing runtime namespace prefix. It is `SKY` unless the
project changes it, so the chat URL is read from the `SKY` prefix plus
`_AI_CHAT_URL`. A project whose prefix is `FENCE` reads `FENCE_AI_CHAT_URL`.

> **Why this document spells the names with `<PREFIX>`.** The
> `config-surface` census (`rust/crates/xtask/src/config_surface.rs`) counts
> every `SKY_*` name in the live docs and fails when the number of documented
> names with no reader rises. None of these variables has a reader yet. The
> concrete names enter the docs in the same commit that adds their reader, in
> phase 1. That is also the correct order for the census. *(Done in phase 1
> part A: the concrete names and their readers are in
> [`../ai/local.md`](../ai/local.md) and `runtime-go/rt/ai_config.go`.)*

The slot must be a string literal or a top-level `String` constant, read at
build time, as `App.withAppUrl` already is. That lets the build list every AI
variable the app can read. The build feeds that list to the startup report,
to `sky doctor` and to the census, and it rejects a slot that is not
`[a-z][a-z0-9_]*` or that collides with a reserved word (`home`, `offline`,
`fetch`, `llama`, `vllm`, `accel`, `train`, `eval`, `registry`, `serve`, `db`).

### 5.3 Per-slot variables

| Builder (code default) | Variable | Type and parse | Fallback when neither is set |
|---|---|---|---|
| `Endpoint.withMode` | `<PREFIX>_AI_<SLOT>_MODE` | `local` or `remote` | inferred (section 8.1) |
| `Endpoint.withUrl` | `<PREFIX>_AI_<SLOT>_URL` | OpenAI-compatible base URL ending `/v1` | none: managed local engine if `withLocal` is set |
| `Endpoint.withKey` | `<PREFIX>_AI_<SLOT>_KEY` | `Secret` via `Secret.fromEnv` | no key (local engines use a per-run key) |
| `Endpoint.withModel` | `<PREFIX>_AI_<SLOT>_MODEL` | remote: served model name. Local: a model reference (section 8.2) | the `Local.Spec` model |
| `Endpoint.withAdapter` | `<PREFIX>_AI_<SLOT>_ADAPTER` | adapter name, or `registry:<name>@<alias>` | no adapter |
| `Endpoint.withTimeout` | `<PREFIX>_AI_<SLOT>_TIMEOUT_MS` | positive integer | 120000 |
| `Endpoint.withMaxInFlight` | `<PREFIX>_AI_<SLOT>_MAX_INFLIGHT` | positive integer | local: engine slots. Remote: 16 |
| `Endpoint.withQueue` | `<PREFIX>_AI_<SLOT>_QUEUE` | non-negative integer (waiting calls) | 4 x max in flight |
| `Endpoint.withQueueWait` | `<PREFIX>_AI_<SLOT>_QUEUE_WAIT_MS` | positive integer | 30000 |
| `Endpoint.withRetry` | `<PREFIX>_AI_<SLOT>_RETRIES` | non-negative integer | 2 (only on `Overloaded` and connect errors) |
| `Local.withContext` | `<PREFIX>_AI_<SLOT>_CONTEXT` | tokens | 8192, reduced to fit memory |
| `Local.withGpu` | `<PREFIX>_AI_<SLOT>_GPU` | `auto`, `cpu`, `all`, or a layer count | `auto` |
| `Local.withParallel` | `<PREFIX>_AI_<SLOT>_PARALLEL` | engine slots | 2 |
| `Local.withIdleUnload` | `<PREFIX>_AI_<SLOT>_IDLE_UNLOAD_S` | seconds, `0` never | 600 in development, `0` otherwise |
| `Local.withPort` | `<PREFIX>_AI_<SLOT>_PORT` | port, `0` picks a free one | 0 |

### 5.4 Global variables

| Builder or source | Variable | Meaning | Fallback |
|---|---|---|---|
| none (machine setting) | `<PREFIX>_AI_HOME` | root of the engine, toolchain and model cache | `$SKY_HOME/ai`, else the user cache directory (`~/Library/Caches/sky/ai` on macOS, `~/.cache/sky/ai` on Linux): one copy per user, shared by every project and test |
| none (machine setting) | `<PREFIX>_AI_LLAMA_BIN` | operator's own `llama-server` directory. Set but incomplete is an error, as with `SKY_POSTGRES_BIN` | the pinned cache |
| none (machine setting) | `<PREFIX>_AI_ACCEL` | force `cpu`, `metal`, `cuda`, `rocm` or `vulkan` | detected |
| none (machine setting) | `<PREFIX>_AI_FETCH` | `on` or `off`. Whether a missing artefact may be downloaded at run time | `on` in development, `off` otherwise |
| `Pipeline.withRegistry` | `<PREFIX>_AI_REGISTRY` | `file://…`, `gs://bucket/prefix` or `s3://bucket/prefix` | `file://.sky/ai/registry` |
| `Train.withBackend` | `<PREFIX>_AI_TRAIN_BACKEND` | `mlx`, `unsloth`, `vertex`, `sagemaker`, `fake` | detected local backend |
| `Train.withBudget` | `<PREFIX>_AI_TRAIN_BUDGET` | money, for example `USD 40.00` | none: a cloud launch without a budget is refused |
| `Train.withCloud` | `<PREFIX>_AI_TRAIN_PROJECT`, `_REGION`, `_MACHINE`, `_STAGING` | cloud project or account, region, machine type, staging bucket | none (required for a cloud backend) |
| `Serve.withEngine` | `<PREFIX>_AI_SERVE_ENGINE` | `llama` or `vllm` | `llama` |
| `Serve.withBind` | `<PREFIX>_AI_SERVE_BIND` | listen address | `127.0.0.1:8080` in development, `0.0.0.0:8080` otherwise |
| `Serve.withToken` | `<PREFIX>_AI_SERVE_TOKEN` | `Secret`, the service token callers send | required outside development |
| `Serve.withMaxAdapters` | `<PREFIX>_AI_SERVE_MAX_ADAPTERS` | adapters resident at once | 4 |

"Development" means what it means everywhere else in Sky: `ENV` unset or one of
`dev`, `development`, `local`.

Cloud credentials are not Sky settings. They come from the platform the way
each cloud expects: the GCE or GKE metadata server and workload identity, AWS
instance roles (IMDSv2) or the standard AWS variables. Sky reads those
standard sources and never asks the user to copy a cloud key into a Sky
variable.

### 5.5 What the operator sees

The startup report (the checklist printed under `listening`) gains one line per
slot. It names the source of each resolved setting and never prints a secret or
the full URL (only scheme, host and port):

```
ai chat   remote  https://gpu-tier.internal:8443  model base-8b  adapter support@prod   (url: env, key: env, model: env)
ai embed  local   llama-server b<pinned> metal    small-embed Q8_0  ctx 2048          (all: code)
```

`sky doctor` checks the same list: every slot resolves, every local model is
provisioned when fetch is off, every remote URL answers its health probe.

*(Implemented in phase 2. The build reads the slot literals
(`project/src/ai_slots.rs`) into a generated `ai_slots_gen.go`; at startup a
slot has not resolved yet, so a line says what the environment decides and
that the code decides the rest at the first call. The health probe is a TCP
connection to the URL's host and port, a warning when it fails.)*

### 5.6 Why not `Sky.Config`

`Sky.Config.withX` writes one value per process into the builder layer. AI
settings belong to values, and an app has several of them (three slots, a
pipeline, a GPU tier). Builders on those values keep the setting next to the
code that uses it. The resolution order is identical because the resolver is
the same function.

## 6. The honest split: what is Sky and what is not

| Concern | Runs in | Sky's part |
|---|---|---|
| Token generation (local) | `llama-server` child process | fetch, verify, fit, start, health-check, restart, route, queue, trace |
| Token generation (GPU tier) | vLLM or `llama-server` on a GPU machine | the same, plus multi-adapter loading, autoscale signals, deployment |
| Embeddings | the same engines (`/v1/embeddings`) | typed `Provider.embed`, storage in `Memory.Pg` |
| Gradient descent (Mac) | MLX-LM in a Sky-managed Python toolchain | job spec, data staging, launch, progress, checkpoints, resume, cost |
| Gradient descent (NVIDIA, AMD) | Unsloth core (Apache-2.0) in a Sky-managed toolchain | the same |
| Gradient descent (cloud) | Vertex AI custom training or SageMaker training jobs, running the same pinned trainer image | launch by REST, exactly-once, poll, collect, cost |
| Adapter conversion to GGUF | Sky's Go converter in the runtime (v0.28.0, where the design said llama.cpp `convert_lora_to_gguf.py`) | a journalled step with a verified output digest |
| Data preparation, splits, dedupe, scrubbing, versioning | Sky | all of it |
| Scoring, statistics, gating | Sky | all of it, except the judge model's own generation |
| Registry, lineage, promotion, rollback | Sky (over a file store, GCS or S3) | all of it |

Every Python program Sky runs is a Sky-shipped **driver** with a fixed
line-oriented JSON protocol on stdout (`progress`, `metric`, `checkpoint`,
`artefact`, `error`). Sky parses that protocol with `Process.readFrom`. The
driver is versioned with its toolchain, and the protocol is a contract that is
tested on every toolchain bump (section 13).

## 7. Module map

| Module | One line |
|---|---|
| `Std.Ai.Provider` (extended) | Adds `chatWith` options (temperature, seed, max tokens, adapter), `embed`, and typed failure classification. |
| `Std.Ai.Failure` (new) | One typed ADT for every AI failure (`Overloaded`, `TimedOut`, `EngineCrashed`, `ModelTooLarge`, …) carried inside `Error`. |
| `Std.Ai.Endpoint` (new) | A named slot with `withX` builders and env fallback. Resolves to remote or managed local and yields a `Provider` with a bounded queue. |
| `Std.Ai.Local` (new) | The managed local engine: fetch, verify, fit to memory, supervise `llama-server`, idle unload. Yields a `Provider`. |
| `Std.Ai.Dataset` (new) | Typed datasets from JSONL, CSV or `Store` rows into chat or instruction records. Splits, dedupe, scrubbing, validation, frozen versions with digests. |
| `Std.Ai.Dataset.Scrub` (new) | Pure PII scrubbers (email, phone, card number with a Luhn check, custom patterns) and a scrub report. |
| `Std.Ai.Train` (new) | LoRA and QLoRA job specs, a resource and cost plan, backends (MLX, Unsloth, Vertex, SageMaker, fake), run as a durable workflow. Outputs a registered adapter. |
| `Std.Ai.Eval` (new) | Suites as Sky values, scorers, LLM-as-judge with a pinned judge, baseline comparison with statistics, reports, and a gate that fails CI. |
| `Std.Ai.Registry` (new) | Content-addressed model and dataset versions in a file store, GCS or S3. Lineage, eval evidence, aliases, promote and rollback. |
| `Std.Ai.Serve` (new) | Stands up a GPU tier (vLLM or `llama-server`) with multi-adapter serving, health, metrics, autoscale signals and cloud deployment targets. |
| `Std.Ai.Pipeline` (new) | Names the project's datasets, jobs, suites and tiers in one value, so the `sky ai` verbs can run them. |
| `Std.Ai.Toolchain` (new, mostly internal) | The pinned engine and Python toolchain artefacts: discovery, fetch, checksum, atomic install, registry of running engines. |
| `Std.Ai.Trace` (extended) | Adds latency, time to first token, tokens per second, slot, adapter and tier to each row. Adds training and eval cost rows. |
| `Std.Ai.Device` (phase 5) | A slot on the user's device: llama.cpp in the browser (WebGPU or CPU WebAssembly), the iOS, Android and Mac apps, with the server's slot as the fallback (§14 P5 records why not WebLLM, transformers.js, Foundation Models or LiteRT-LM). |

## 8. API sketches

These sketches are design, not shipped API. The names follow the existing
stdlib: a constructor names the value, `withX` refines it, a Task verb acts on
it. The code style follows AGENTS.md: flat pipelines and named steps.

### 8.1 `Std.Ai.Endpoint`: the slot the app uses

```elm
type Endpoint

endpoint : String -> Endpoint                      -- the slot, a literal

withMode : Mode -> Endpoint -> Endpoint            -- Local | Remote
withUrl : String -> Endpoint -> Endpoint
withKey : Secret -> Endpoint -> Endpoint
withModel : String -> Endpoint -> Endpoint
withAdapter : String -> Endpoint -> Endpoint
withLocal : Local.Spec -> Endpoint -> Endpoint     -- the local default engine
withTimeout : Int -> Endpoint -> Endpoint
withMaxInFlight : Int -> Endpoint -> Endpoint
withQueue : Int -> Endpoint -> Endpoint
withQueueWait : Int -> Endpoint -> Endpoint
withRetry : Int -> Endpoint -> Endpoint
withPriceBook : Provider.PriceBook -> Endpoint -> Endpoint

provider : Endpoint -> Provider                    -- pure, resolves on first call
warm : Endpoint -> Task Error Resolved             -- resolve and start now
describe : Endpoint -> Task Error Description      -- sources per setting, no secrets
```

`provider` is pure. It returns a `Provider` whose first call resolves the slot
and memoises the result, so a top-level binding is enough and the app gets a
`Provider` with no `Task` at the top level:

```elm
chatSlot : Endpoint
chatSlot =
    Endpoint.endpoint "chat"
        |> Endpoint.withLocal
            (Local.model Models.devChat
                |> Local.withContext 8192
                |> Local.withIdleUnload 600
            )
        |> Endpoint.withMaxInFlight 8

chat : Provider
chat =
    Endpoint.provider chatSlot
```

**The local-or-remote rule.** Resolution takes the first rule that applies:

1. A resolved `MODE` is obeyed. `local` together with a resolved `URL` is an
   error that names both sources. This is the `--embed` plus explicit DSN rule
   from embedded PostgreSQL: a silent choice between them would let a deploy
   ignore the operator.
2. A resolved `URL` means remote.
3. A `withLocal` spec means a managed local engine.
4. Otherwise the call fails with `NotConfigured`, naming the variable to set.

A remote endpoint that is down is `Unreachable`. It never falls back to a local
model.

**The bounded queue.** Each resolved slot owns a semaphore of `MAX_INFLIGHT`
permits and a waiting queue of `QUEUE` places, built on `Std.Sync`. A call that
finds the queue full fails at once with `Overloaded`. A call that waits longer
than `QUEUE_WAIT_MS` fails with `TimedOut`. A remote `429` or `503` becomes
`Overloaded` with the server's `Retry-After`. Retries apply only to
`Overloaded` and connection errors, use jittered backoff, and never apply to a
call inside a `Durable.step` that already reached the server, because the step
journal is what makes that call exactly-once.

### 8.2 `Std.Ai.Local`: the managed local engine

```elm
type Spec
type Engine
type ModelRef

huggingFace : { repo : String, file : String, sha256 : String } -> ModelRef
file : { path : String, sha256 : String } -> ModelRef
fromRegistry : String -> ModelRef                  -- "support-base@dev"

model : ModelRef -> Spec
withContext : Int -> Spec -> Spec
withGpu : Gpu -> Spec -> Spec                      -- Auto | CpuOnly | AllLayers | Layers Int
withParallel : Int -> Spec -> Spec
withIdleUnload : Int -> Spec -> Spec
withAdapters : List Adapter -> Spec -> Spec        -- LoRA in GGUF form
withDraft : ModelRef -> Spec -> Spec               -- speculative decoding
withEmbeddings : Spec -> Spec                      -- serve /v1/embeddings
withPort : Int -> Spec -> Spec

fit : Spec -> Task Error Fit                       -- predict before loading
start : Spec -> Task Error Engine
provider : Engine -> Provider
status : Engine -> Task Error Status
stop : Engine -> Task Error ()

type alias Fit =
    { accelerator : Accelerator
    , weightsBytes : Int
    , kvCacheBytes : Int
    , availableBytes : Int
    , gpuLayers : Int
    , contextTokens : Int
    , expectedTokensPerSecond : Maybe Int
    }
```

A model reference carries its SHA-256 in code. `sky ai pull <repo> <file>`
downloads the file once and prints the `Local.huggingFace { … }` value with the
digest filled in, ready to paste. A model with no digest cannot be written, so
an unpinned model cannot reach production. The model store is
content-addressed: `<AI_HOME>/models/sha256/<hex>.gguf`.

**What `start` does**, in order, each with a typed failure:

1. Discover a `llama-server` build: `<PREFIX>_AI_LLAMA_BIN` (set but incomplete
   is an error), else the pinned cache `<AI_HOME>/engines/llama/<build>-<variant>/`.
   `PATH` is not consulted, because an unpinned build breaks the launch-argument
   and log-line contracts the supervisor relies on.
2. If missing and fetch is allowed, fetch the variant for the host (CPU, Metal,
   CUDA 12, CUDA 13, ROCm, Vulkan). `SHA256SUMS` is fetched first, the archive
   digest is taken from the bytes on disk, the tree is extracted outside the
   cache and renamed into place, and the binary is run once (`--version`) before
   the install counts. This is the PostgreSQL bundle procedure, reused.
   With fetch off: `NotProvisioned`, naming `sky ai provision`.
3. Fetch and verify the model the same way (resumable range requests).
4. `fit`: read GGUF metadata (tensor sizes, layer count, head dimensions) and the
   machine (cgroup memory limit first, unified memory on Apple silicon, VRAM per
   device). Choose GPU layers and a context that fit. Refuse with
   `ModelTooLarge { needBytes, availableBytes }` rather than page to disk.
   Predict decode speed from memory bandwidth divided by bytes read per token.
5. Write a per-run API key to a `0600` file and pass `--api-key-file`, never
   the key on the command line (which `ps` shows). Bind `127.0.0.1` only.
6. Spawn with `Process.withClearEnv` plus an explicit allow-list (`PATH`,
   `HOME`, the accelerator variables), so no operator secret reaches the engine.
7. Poll `/health` until ready, with a deadline. Read the log tail on failure.
8. Supervise: a child exit becomes `EngineCrashed { exitCode, logTail }` on
   in-flight calls. In development the engine restarts with backoff (at most
   three times a minute, then it stays down and reports). Outside development
   the app exits non-zero, as with a dead embedded PostgreSQL, so the platform
   supervisor restarts the whole tree instead of hiding a fault.

**Sharing.** Engines are shared machine-wide by the key (engine build, model
digest, adapters, context). A registry at `<AI_HOME>/engines.json` with the
same two-legged liveness check as `clusters.json` lets `sky ai ps` see engines
it did not start, and a reference count stops an engine only when its last user
leaves. Idle unload stops the child after the idle window and restarts it on
the next call.

### 8.3 `Std.Ai.Provider` additions and `Std.Ai.Failure`

```elm
-- Std.Ai.Provider, additions
type alias Options =
    { temperature : Maybe Float
    , seed : Maybe Int
    , maxTokens : Maybe Int
    , adapter : Maybe String
    }

defaultOptions : Options
chatWith : Options -> Provider -> List Message -> Task Error ChatResponse
embed : Provider -> List String -> Task Error (List (List Float))

-- Std.Ai.Failure (as shipped in phase 1 part B)
type Failure
    = NotConfigured { setting : String }        -- names the variable to set
    | InvalidSetting { setting : String, reason : String }
    | Unreachable { target : String }           -- scheme, host and port only
    | Overloaded { retryAfterMs : Maybe Int, status : Maybe Int }
    | TimedOut { afterMs : Maybe Int }
    | EngineCrashed { exitCode : Maybe Int, logTail : String }
    | EngineFailed { reason : String }
    | ModelTooLarge { needBytes : Int, availableBytes : Int }
    | NoAccelerator { wanted : String }
    | NotProvisioned { what : String, command : String }   -- names the provision command
    | ChecksumMismatch { expected : String, actual : String }
    | RemoteRejected { status : Int }
    | BudgetExceeded { budget : Money, estimate : Money }
    | ToolchainFailed { tool : String, logTail : String }
    | GateFailed { suite : String, reasons : String }      -- Std.Ai.Eval

fromError : Error -> Maybe Failure
toError : Failure -> Error
```

`Failure` travels inside the existing `Error`, so every signature stays
`Task Error a` and the "never `String` as an error" rule holds. Each failure
also maps to an `ErrorKind` that existing handlers already understand:
`Overloaded`, `Unreachable`, `EngineCrashed` and `NoAccelerator` are
`Unavailable`. `TimedOut` is `Timeout`. `ModelTooLarge` and `NotConfigured`
are `InvalidInput`. `ChecksumMismatch` is `Conflict`. `NotProvisioned` is
`NotFound`. `BudgetExceeded` is `PermissionDenied` (below).

**Decided (2026-10-11): the payload is a generic `Domain` variant.** Open
question 8 is closed. `Sky.Core.Error.ErrorDetails` gains exactly one new
variant, `Domain DomainFailure`. It is generic, so it is the last change the
core type needs: later domains (database, durable runs, auth, payments) reuse
it and add no variant of their own.

- `DomainFailure` is opaque. It holds a domain name (`String`, for example
  `"ai"`), a stable code (`String`), whether the failure is retryable (`Bool`),
  an optional retry delay (`Maybe Int`, milliseconds) and a typed payload. The
  payload is encoded with a `Codec`, so it crosses the Sky.Spa wire and goes
  into logs safely. It never holds a secret.
- `Sky.Core.Error` gains three generic helpers that work for every domain and
  never break when a domain adds a failure: `Error.domain : Error -> Maybe
  String`, `Error.isRetryable : Error -> Bool` and `Error.retryAfter : Error ->
  Maybe Int`.
- `Std.Ai.Failure` holds the typed AI ADT (`Overloaded { retryAfterMs }`,
  `TimedOut`, `EngineCrashed { exitCode }`, `ModelTooLarge { needBytes,
  availableBytes }`, `NoAccelerator`, `NotProvisioned`, `ChecksumMismatch`,
  `BudgetExceeded`, `NotConfigured`, and the rest of the list above), with
  `fromError : Error -> Maybe Failure` and `toError : Failure -> Error`.
- `toError` always sets an existing `ErrorKind`, so code that matches only on
  `ErrorKind` keeps working. `Overloaded`, `Unreachable`, `EngineCrashed` and
  `NoAccelerator` are `Unavailable`. `TimedOut` is `Timeout`.
  `ModelTooLarge` and `NotConfigured` are `InvalidInput`. `ChecksumMismatch`
  is `Conflict`. `NotProvisioned` is `NotFound`. `BudgetExceeded` is
  `PermissionDenied`: the call is refused by a rule the operator set, and
  retrying it unchanged does not help, which `Unavailable` would suggest.
- The two stdlib files that match `ErrorDetails` exhaustively are updated. The
  compiler gives a hint for user code whose exhaustive `ErrorDetails` match
  breaks: it names the new variant and suggests a `_ ->` branch.
- Tests: a Sky.Spa wire round trip of a `Domain` failure, Go runtime tests,
  cargo tests and `Sky.Test` tests. The CHANGELOG entry goes under `v0.28.0`.
  `docs/errors/` and the AI guide recommend the helpers, or a `_ ->` branch,
  for code that must not depend on future AI failures.

Phasing. Phase 1 part A returns each AI failure as an `Error` with the
`ErrorKind` above and a clear message, and keeps the classification in one
internal function. Phase 1 part B adds `Domain`, `DomainFailure`, the helpers
and `Std.Ai.Failure`, and switches that one function to typed values.
*(Done in phase 1 part B. The retryable flag decides `Error.isRetryable`:
`Unreachable`, `Overloaded`, `TimedOut` and a remote 5xx are retryable;
`EngineCrashed` is `Unavailable` and not retryable, because the supervisor
restarts a crashed engine and a crash loop must not be retried blindly. The
runtime's table is `aiFailSpecs` in `runtime-go/rt/ai_failure.go`, and a Go
test keeps it equal to `Std.Ai.Failure`. User guide:
[`../ai/local.md`](../ai/local.md#failures);
[`../errors/error-system.md`](../errors/error-system.md#domain-failures).)*

### 8.4 `Std.Ai.Dataset`

```elm
type Dataset row                -- a lazy, streaming pipeline
type Version row                -- a frozen, content-addressed dataset

type alias Chat =
    { messages : List Provider.Message }

type alias Instruction =
    { prompt : String, completion : String }

fromJsonl : Codec row -> String -> Dataset row
fromCsv : (Dict String String -> Result Error row) -> String -> Dataset row
fromStore : Db -> Store a -> (a -> Maybe row) -> Dataset row
fromList : List row -> Dataset row

map : (a -> b) -> Dataset a -> Dataset b
filterMap : (a -> Maybe b) -> Dataset a -> Dataset b
dedupeBy : (row -> String) -> Dataset row -> Dataset row
scrub : Scrub.Scrubber row -> Dataset row -> Dataset row
toChat : (row -> Chat) -> Dataset row -> Dataset Chat
toInstruction : (row -> Instruction) -> Dataset row -> Dataset Instruction

split : { train : Int, eval : Int, test : Int } -> (row -> String) -> Dataset row -> Splits row
validate : Limits -> Dataset Chat -> Task Error Report
freeze : Registry -> String -> Codec row -> Dataset row -> Task Error (Version row)
load : Registry -> String -> Codec row -> Task Error (Version row)
rows : Version row -> Task Error (List row)
```

Design points:

- **Streaming.** A `Dataset` is a description. Rows flow through it when it is
  frozen or validated, so a large file never sits in memory. `dedupeBy` keeps a
  hash set of keys, spilled to a temporary SQLite table past a bound.
- **Deterministic splits.** `split` assigns each row by a hash of its key, not
  by a random draw. The same row is always in the same split across versions,
  so the eval set never leaks into training when new data arrives. The
  durable-workflow determinism contract needs this too.
- **Validation.** `validate` checks token lengths against the base model's
  tokenizer (through the local engine's `/tokenize` route), empty turns, role
  order, duplicate rate and split sizes. It returns a typed report and an error
  above the limits.
- **Frozen versions.** `freeze` writes canonical JSONL (sorted keys, one row per
  line, `\n` endings), takes its SHA-256 and writes a manifest: source
  description, row counts per split, scrub report, schema shape from the codec,
  creation time and an optional `consentBasis` and `retention` field
  (section 11.5). A training job accepts only a `Version`, never a live
  `Dataset`, so lineage is guaranteed by the type.
- **Scrubbing** (`Std.Ai.Dataset.Scrub`) is pure and composable:
  `Scrub.email`, `Scrub.phone`, `Scrub.cardNumber` (Luhn-checked),
  `Scrub.pattern name regex`, `Scrub.custom`, and `Scrub.dropIf`. Each scrubber
  counts what it changed, and the counts go into the manifest. Scrubbing is a
  hook the user composes, not a guarantee of anonymity, and the document for it
  says so.
- **Synthetic data** (`Std.Ai.Dataset.Synthetic`). A generator asks a teacher
  model, the slot `"teacher"` (the local pinned model by default), for rows in
  a codec's shape, about topics, for personas, with few-shot examples. The plan
  is pure and seeded (call `i` uses seed `seed + i` and walks every topic and
  persona), quality filters drop unparsed rows, duplicates, rows a predicate
  rejects and rows the eval judge fails, and the run stops at a target, a call
  limit or a money budget. `toDataset` records the generator, teacher and seed
  as the dataset's source.

*(Phase 2 part A ships this section: `Std.Ai.Dataset`, `Scrub` and
`Synthetic`, the typed `DatasetInvalid` failure, and `sky ai data freeze`,
which runs a bundled program over a JSONL file because the project's
`src/Ai.sky` entry arrives with `Std.Ai.Pipeline`. Changes from the sketch:
`freeze` and `load` take a `Folder` (a directory and a name) instead of a
registry, and the manifest is the lineage the registry records; `split` takes
a seed (`{ train, eval, test, seed }`) and `freeze` takes the `Splits`, so
the counts per split are in the manifest; `validate` estimates tokens unless
`withTokenizer` is given; scrub rules also cover IBANs, US SSNs, UK NINOs and
secrets, and `Scrub.pattern` returns a `Result`. Still later: storing versions
in the registry, the engine tokenizer route, spilling the dedupe set to disk,
and journalled teacher and judge calls. User guide:
[`../ai/dataset.md`](../ai/dataset.md).)*

### 8.5 `Std.Ai.Train`

*Shipped in v0.28.0; the `Vertex` backend is phase 4 (section 14, P4), and
`SageMaker` is not shipped. The shipped API and the
changes from this sketch (a Go GGUF converter, no Convert toolchain, a
detached trainer, `drive` and `poll`, CUDA 13) are in section 14, P3, and in
[`../ai/train.md`](../ai/train.md).*

```elm
type Job
type Backend
    = Mlx                                       -- local, Apple silicon
    | Unsloth                                   -- local, NVIDIA or AMD
    | Vertex                                    -- cloud, Google
    | SageMaker                                 -- cloud, AWS
    | Fake                                      -- tests, no GPU

lora : BaseModel -> Version Chat -> Job
withQuantisedBase : Job -> Job                  -- QLoRA, 4-bit base
withRank : Int -> Job -> Job
withAlpha : Int -> Job -> Job
withLearningRate : Float -> Job -> Job
withEpochs : Int -> Job -> Job
withMaxSteps : Int -> Job -> Job
withBatch : { perDevice : Int, accumulate : Int } -> Job -> Job
withSequenceLength : Int -> Job -> Job
withSeed : Int -> Job -> Job
withCheckpointEvery : Int -> Job -> Job
withBackend : Backend -> Job -> Job
withCloud : { project : String, region : String, machine : String, staging : String } -> Job -> Job
withBudget : Money -> Job -> Job
withMachinePrices : MachinePriceBook -> Job -> Job
withGgufExport : Job -> Job                     -- also produce a GGUF adapter

plan : Job -> Task Error Plan                   -- fit, time and cost, before launch
workflow : String -> Job -> Durable.WorkflowDef Input Adapter
start : Db -> String -> Job -> String -> Task Error ()
progress : Db -> String -> Task Error Progress
cancel : Db -> String -> Task Error ()
```

`plan` refuses a job that will not fit, using the published QLoRA minimums
(about 5 GB for 7B, 8.5 GB for 14B, 22 GB for 27B, 41 GB for 70B on Unsloth)
and unified memory on a Mac. It returns `ModelTooLarge` or `NoAccelerator` and
says plainly that a 70B adapter needs a 48 GB card or a Mac with 64 GB or more.
It estimates cost from a user-supplied, versioned `MachinePriceBook` (the same
idea as `Provider.PriceBook`: the stdlib does not ship prices that go stale)
and refuses a cloud job whose estimate exceeds its budget.

**The training workflow.** `workflow` is a `Std.Durable` workflow. Each line is
a journalled step:

```
validate     Dataset.validate on the frozen version (pure on the journal)
stage        copy the version's JSONL to the backend (local dir, gs:// or s3://)
provision    ensure the pinned toolchain exists and verify it
launch       start the trainer, journal its id         <- the paid, exactly-once step
watch        Durable.sleep + poll progress until done   (passive, no process held)
collect      fetch adapter + metrics, verify digests
convert      convert_lora_to_gguf, if withGgufExport
register     Registry.register as a candidate with full lineage
```

**Exactly-once and resume, stated precisely.** The `launch` step is
exactly-once. For a cloud backend the job name is derived from the durable run
id, so a resumed run that finds the job already created reattaches instead of
creating a second paid job. Gradient steps are not exactly-once, and nothing
can make them so. They are resumable at checkpoint granularity: if a local
trainer process dies, the `watch` step relaunches the driver with
`--resume-from` the newest verified checkpoint, and at most
`withCheckpointEvery` steps are repeated. Progress, loss and throughput arrive
as driver protocol lines and are written to `Trace` and to the console.

**Backends.**

| Backend | Engine | Where it runs | Output |
|---|---|---|---|
| `Mlx` | MLX-LM (MIT) | Apple silicon, in a Sky-managed Python toolchain | adapter in safetensors, GGUF on request |
| `Unsloth` | Unsloth core (Apache-2.0). Never the AGPL Studio | NVIDIA, AMD, in a Sky-managed toolchain | the same |
| `Vertex` | Vertex AI custom training, running the pinned trainer container | Google Cloud GPUs | the same, written to the staging bucket |
| `SageMaker` | SageMaker training job, same container | AWS GPUs | the same |
| `Fake` | a Sky-shipped driver that emits a scripted protocol | anywhere, no GPU | a tiny valid adapter for a tiny test base |

The cloud backends run the same driver in a pinned container image, so a job
gives the same artefacts locally and in the cloud. Only the launcher differs.

**Example, the user's whole training definition:**

```elm
supportJob : Train.Job
supportJob =
    Train.lora Models.prodBase Data.supportV
        |> Train.withQuantisedBase
        |> Train.withRank 16
        |> Train.withEpochs 2
        |> Train.withSeed 7
        |> Train.withCheckpointEvery 200
        |> Train.withGgufExport
        |> Train.withBudget (Money.fromMajor Money.USD 40)
```

The backend is not in the code. On a Mac the detected default is `Mlx`. In CI
the backend variable says `fake`. On the training account it says `vertex`.

### 8.6 `Std.Ai.Eval`

```elm
type Suite input
type alias Case input =
    { id : String, input : input, expect : Expect, tags : List String }

type Expect
    = Exact String
    | Contains String
    | Matches String                            -- a regex
    | JsonShape Codec.Shape                     -- parses and matches the codec shape
    | CallsTool { name : String, args : String -> Bool }
    | Refuses
    | Rubric String                             -- scored by the judge
    | Custom String (ChatResponse -> Score)

suite : String -> (input -> List Message) -> List (Case input) -> Suite input
withSamples : Int -> Suite input -> Suite input          -- repeats per case
withConcurrency : Int -> Suite input -> Suite input
withJudge : Judge -> Suite input -> Suite input
withLatencyBudget : Int -> Suite input -> Suite input    -- ms, p95
withCostBudget : Money -> Suite input -> Suite input     -- per case, mean
withOptions : Provider.Options -> Suite input -> Suite input

judge : Provider -> String -> Judge                      -- provider, rubric prompt version
withCalibration : List Labelled -> Float -> Judge -> Judge

run : Db -> Provider -> Suite input -> Task Error Report
compare : Report -> Report -> Comparison                 -- pure
gate : Thresholds -> Comparison -> Result Error Passed   -- pure
toJunit : Report -> String
toMarkdown : Comparison -> String
```

**Scorers.** Exact, contains, regex, JSON shape (decoded through the codec, so
the expected shape is the same type the app consumes), tool-call correctness
(name and an argument predicate), refusal, p95 latency, mean cost per case in
`Std.Money`, and LLM-as-judge.

**Reproducibility.** A run uses temperature 0 and a fixed seed by default, and
records the provider's model, adapter and engine build in the report. Where an
engine is not deterministic, `withSamples` repeats each case and the statistics
use every sample.

**Judge discipline.** The judge is just a slot (`Endpoint.endpoint "judge"`),
pinned by model digest or a fixed remote model version recorded in the report.
Rubric prompts are versioned strings. Every judge call is a `Durable.step`
keyed by (judge identity, rubric version, case id, output digest), so a re-run
of an unchanged output costs nothing and returns the same verdict. Pairwise
judging runs both orders to cancel position bias. `withCalibration` runs the
judge on human-labelled cases first and refuses to gate when its agreement is
below the threshold, so a drifting judge cannot pass a bad model.

**Statistics.** `compare` pairs each case across baseline and candidate.
Pass-or-fail scorers use an exact McNemar test on the discordant pairs.
Continuous scores use a paired bootstrap of the mean difference with a fixed
seed, so the result is reproducible. `gate` fails when:

- any scorer's candidate score is below its absolute floor, or
- the lower bound of the confidence interval of (candidate minus baseline) is
  below minus the allowed regression, or
- p95 latency or mean cost exceeds its budget, or
- the judge failed calibration.

*(Phase 2 part C ships `sky ai eval` through the project's pipeline: the
candidate and the alias's version on the suite's slot, `compare`, `gate`,
JUnit and Markdown, exit 1 on failure, and `--promote-to` with the `Passed`
it gave. Eval runs another process wrote to the app's database appear in the
console. `judgeAnswerWith` journals a judge call outside a suite.)*

*(Phase 2 ships `withCalibration` (with `calibrate`, Cohen's kappa and a
gate refusal below the agreement, plus `withCalibratedJudge`), `pairwise`
in both orders, and the judge journal. The journal is a table in the run's
database keyed by the judge's identity, version and exact prompt, not a
`Durable.step`: the property (an unchanged judge call is answered once) is
the same, and it needs no durable run around `Eval.run`.)*

*(Phase 1 part B ships the core: suites, every scorer above, the judge as the
`"judge"` slot with the local pinned model by default, seeded samples, the
typed `Report`, latency and cost budgets, `compare` with the exact McNemar
test and the seeded paired bootstrap, `gate` / `check`, `Passed`, `toJunit`,
`toMarkdown`, and run history in the database. Calibration, pairwise judging
and journalled judge calls are phase 2. User guide:
[`../ai/eval.md`](../ai/eval.md).)*

`gate` returns `Passed`, an opaque value only `gate` can construct. It carries
the candidate's digest and the report's digest. `Registry.promote` requires a
`Passed` (section 8.7), so the type system enforces "no promotion without a
passing evaluation".

**In CI.** `sky ai eval support --against prod` runs the suite against the
candidate and the `prod` alias, prints the Markdown comparison, writes JUnit
XML and exits 1 when the gate fails. Inside `sky test`, a suite is an ordinary
test, because `Task.run` gives a `Result`:

```elm
supportGate : Test
supportGate =
    Test.test "support suite does not regress" (\_ ->
        Pipeline.evalAgainst Ai.pipeline "support" "prod"
            |> Task.run
            |> Test.ok
    )
```

### 8.7 `Std.Ai.Registry`

```elm
type Registry
type alias Version =
    { name : String, number : Int, digest : String }

open : String -> Task Error Registry                     -- file://, gs://, s3://
register : Registry -> Artefact -> Lineage -> Task Error Version
attachReport : Registry -> Version -> Eval.Report -> Task Error ()
promote : Registry -> String -> Version -> Eval.Passed -> Task Error ()
forcePromote : Registry -> String -> Version -> String -> Task Error ()   -- reason, audited
rollback : Registry -> String -> Task Error Version
resolve : Registry -> String -> Task Error Version       -- "support-lora@prod"
fetch : Registry -> Version -> Task Error String         -- verified local path
history : Registry -> String -> Task Error (List AliasEvent)
```

*(Implemented in phase 2 with one change, the user's decision: the records
(versions, aliases, the alias log and attached reports) are rows in the app's
own database through `Std.Db`, applied as `_sky_ai_registry_*` migrations in
the `_sky_migrations` ledger, so the same code runs on SQLite and PostgreSQL
and an alias move is a database compare-and-swap in one transaction. The
object store (`Std.Ai.Store`) holds the artefacts only, under
`blobs/sha256/<hex>`. `open` takes the `Db` and the store; `promote` returns
the `AliasEvent`; `rollback` takes `"<name>@<alias>"`; `promoteIf`,
`promoteChecked`, `evidence`, `block`, `trainedOn`, `lineage` and the
resolver for `Endpoint.fromRegistry` complete it. Request signing for S3 is
Sky (`Store.signV4`); the byte transfers are runtime kernels that stream a
file and check its digest. See [`../ai/registry.md`](../ai/registry.md).)*

**Layout.** The registry is plain objects, so it works on a local directory, a
GCS bucket and an S3 bucket with one code path:

```
blobs/sha256/<hex>                         adapters, GGUF files, dataset JSONL, reports
models/<name>/versions/<n>.json            manifest: digests, base, lineage, reports
datasets/<name>/versions/<n>.json          manifest: digest, splits, scrub report
aliases/<name>/<alias>.json                current version
aliases/<name>/<alias>.log.jsonl           append-only promote and rollback history
```

**Lineage** on every model version: dataset version digest, base model digest,
the full `Job` encoded through its codec, toolchain build and digest, trainer
seed, backend and cloud job id, and the durable run id. `sky ai lineage
support-lora@prod` prints it.

**Safe concurrent promotion.** Alias writes are compare-and-swap: a GCS
generation precondition, an S3 conditional write (`If-Match`), or a file lock
and atomic rename locally. Two CI jobs racing to promote cannot both win.
`promote` re-checks that the `Passed` value names this version's digest and
that the report is attached. `rollback` moves the alias to the previous entry
in its log, and that is an ordinary logged event too.

**Access** is written in Sky over `Sky.Core.Http`: AWS SigV4 with
`Crypto.hmacSha256`, Google OAuth tokens from the metadata server or a
service-account JWT through `Jwt.rs256`. No Go kernel and no cloud SDK.

### 8.8 `Std.Ai.Serve`: the GPU tier

*Shipped in v0.28.0 with vLLM and the Cloud Run target. The changes from this
sketch are in section 14, P4, and in [`../ai/serve.md`](../ai/serve.md).*

```elm
type Tier
type Engine = Llama | Vllm

tier : Engine -> ModelRef -> Tier
withAdapters : List String -> Tier -> Tier            -- registry aliases, "support-lora@prod"
withMaxAdapters : Int -> Tier -> Tier
withMaxModelLength : Int -> Tier -> Tier
withGpuMemoryFraction : Float -> Tier -> Tier
withParallel : Int -> Tier -> Tier
withBind : String -> Tier -> Tier
withToken : Secret -> Tier -> Tier
withAliasPolling : Int -> Tier -> Tier                -- seconds, reload on promote

plan : Tier -> Task Error Fit
run : Tier -> Task Error ()                           -- the tier's main
deploy : Target -> Tier -> Task Error Deployment      -- phase 4
```

`Serve.run` is a Sky program that stands in front of the engine on a GPU
machine. It:

- verifies and loads the base model and the adapters its aliases resolve to,
- starts the engine (vLLM with `--enable-lora` and its adapter list, or
  `llama-server` with `--lora`) on loopback and supervises it,
- serves the OpenAI-compatible routes on `withBind` behind the service token,
  so callers keep using `Provider.compatible` through an `Endpoint`,
- maps the requested `model` name to a resident adapter, so one GPU serves many
  fine-tuned variants of one base (multi-LoRA),
- watches the registry aliases and hot-loads a newly promoted adapter, keeping
  the previous one until in-flight requests finish,
- exposes `/health` (ready only when the base and every alias are loaded) and
  autoscale signals: queue depth, running requests, KV cache use, tokens per
  second. It reads them from the engine's Prometheus `/metrics` and re-exports
  them through the Sky console and OpenTelemetry.

vLLM is a Python and CUDA program, so on the GPU tier it runs from a pinned
upstream container image, referenced by digest. `sky ai serve --image` writes a
container definition with that image and the compiled Sky tier binary as the
entry point, so the Sky program supervises vLLM inside one container.

**Deployment targets (phase 4).** Each is a typed `Target` whose `deploy` is a
durable workflow over the cloud's REST API: Cloud Run with GPU (scales to
zero, good for QA), GKE or EKS (a Deployment with a GPU node pool, the HPA
reading the queue-depth signal), a Vertex AI endpoint or a SageMaker endpoint
(managed GPU serving of the same container), and Bedrock Custom Model Import
(managed, but it imports merged weights for supported architectures, so it
cannot do multi-adapter serving, and its support matrix must be checked when
the target is built).

### 8.9 `Std.Ai.Pipeline`: the project's AI entry

```elm
pipeline : Pipeline.Pipeline
pipeline =
    Pipeline.pipeline "support-assistant"
        |> Pipeline.withDb Db.app
        |> Pipeline.withRegistry "file://.sky/ai/registry"
        |> Pipeline.withDataset "support" Data.support
        |> Pipeline.withJob "support-lora" Jobs.supportJob
        |> Pipeline.withSuite "support" Suites.support
        |> Pipeline.withTier "gpu" Tiers.gpu
```

The build discovers `pipeline` in `src/Ai.sky` the way it discovers `main` and
`config`. The `sky ai` verbs compile that module and call the named member.

*(Implemented in phase 2 part C. A member is added with `withSlot`,
`withDataset` (`datasetOf codec build`, `withFolder`, `withRegistered`),
`withSuite` (`suiteOn slot suite`, `suiteWith`, `forModel`,
`withThresholds`) and `withTask` (a `Context -> Task Error ()`), the seam
phase 3's `withJob` follows; `withDb` and `withRegistry` choose the database
and the store. The CLI synthesises an entry whose `main` is
`Pipeline.commandLine Ai.pipeline`, builds it with only `Ai` and its imports
checked, caches it under `.sky/ai/cli/<key>/` and runs it in the project
directory. The app's build is not affected: slots only the pipeline uses stay
out of the app's binary and startup report. When the app's entry exposes a
`Sky.Config` `config`, the pipeline applies it too. See
[`../ai/pipeline.md`](../ai/pipeline.md).)*

### 8.10 Console and Trace

`Trace` rows gain: slot, tier (`local` or `remote`), adapter, latency, time to
first token, tokens per second, queue wait, and outcome (`ok` or the `Failure`
tag). Training and eval runs record their own cost rows, so `totalCost` covers
the whole lifecycle in exact money.

The Sky console gains an **AI** panel:

- slots, with resolved source per setting (no secrets) and live state,
- local engines: model, accelerator, memory, idle timer, restarts,
- queue depth, in-flight calls, `Overloaded` rate, p50 and p95 latency,
  tokens per second,
- GPU tier signals: KV cache use, resident adapters, GPU memory,
- training runs: step, loss curve, throughput, checkpoint, spend against budget,
- eval history per alias: scores over time and each gate verdict,
- registry: aliases, current version and the promotion log.

*(Phase 1 part B ships the AI tab with the slots (resolved source per
setting, in-flight and waiting calls, failures, `Overloaded` count, p50 and
p95 latency), the local engines, the recent calls (filtered by outcome and by
slot) and the eval runs (each case's verdict and reason). It reads
`/_sky/console/api/ai` (`runtime-go/rt/ai_console.go`), whose rings hold no
prompt, no answer and no secret. The GPU tier and training views
arrive with their phases. Phase 2 adds the registry view: each model's
aliases, versions, alias log and eval history.)*

## 9. CLI verbs

The CLI holds no hidden logic. Engine verbs run a bundled Sky program (in
`sky-bundled/`, like the console and the doc server) over the same runtime code
`Local.start` uses. Lifecycle verbs build and run the project's own
`src/Ai.sky`.

| Verb | Runs | Sky API |
|---|---|---|
| `sky ai provision [--variant cuda12]` | bundled program | `Toolchain.provision` for the pinned `llama-server` (and, with `--train`, the trainer toolchain) |
| `sky ai pull <repo> <file>` | bundled program | fetch and verify, print the `Local.huggingFace { … }` value with its digest |
| `sky ai ps [--all]` | bundled program | read `engines.json` with the liveness check |
| `sky ai start <slot>` / `stop <slot>` | project program | `Endpoint.warm` / `Local.stop` |
| `sky ai data freeze <name>` | project program | `Dataset.freeze` |
| `sky ai train <job> [--detach]` | project program | `Train.plan`, then `Train.start`, then poll to completion unless detached |
| `sky ai eval <suite> [--against <alias>]` | project program | `Eval.run`, `compare`, `gate`. Exit 1 on failure |
| `sky ai promote <name> <version> --to <alias>` | project program | re-reads the attached report, `gate`, `Registry.promote` |
| `sky ai rollback <name>@<alias>` | project program | `Registry.rollback` |
| `sky ai serve <tier> [--image]` | project program | `Serve.run`, or write the container definition |
| `sky ai lineage <name>@<alias>` | project program | `Registry.resolve` and the manifest |

Every verb accepts `--format json` for machine output, as the other verbs do.

*Phase 2 part C:* `eval`, `data freeze <dataset>`, `run <task>`, `members`,
`promote` and `start` for a slot the pipeline names run the project's program
(`src/Ai.sky`). `stop` stays in the bundled program: it stops engines by slot
name through the machine's engine registry, which needs no project. `promote`
in either program compares the attached evidence against the version the
alias serves (`Registry.promoteCompared`). `--test-mode` answers model calls
from `tests/mocks/`, as `sky test` does.

*Phase 1 part A:* `start` and `stop` run the bundled program too, because the
project's `src/Ai.sky` entry arrives with `Std.Ai.Pipeline` in phase 2. The
bundled `start` warms `Endpoint.endpoint <slot>` with `Local.devModel` (or
`--model`), so the slot's own `<PREFIX>_AI_<SLOT>_*` variables still apply, and
a project program whose slot has the same model and settings shares that
engine through `engines.json`.

## 10. The lifecycle, end to end

### 10.1 The flow

```
   Store rows / JSONL / CSV
            |
            v
   Dataset  --scrub, dedupe, split, validate-->  Version (sha256)  --freeze-->  Registry
            |
            v
   Train.workflow (Std.Durable)
     validate -> stage -> provision -> launch* -> watch -> collect -> convert -> register
            |                                                              |
            |                     (* exactly-once paid step)              v
            |                                                   candidate Version
            v                                                     + lineage
   Eval.run candidate  and  Eval.run baseline(@prod)
            |
            v
   compare -> gate --fail--> CI red, nothing promoted
            |
          Passed
            v
   Registry.promote "@prod"  (compare-and-swap, logged)
            |
            v
   Serve tier polls alias -> hot-loads adapter -> app's Provider uses it
```

### 10.2 The same code across tiers

```
                 app code (identical binary)
                 chat = Endpoint.provider chatSlot
                          |
     +--------------------+--------------------+----------------------+
     |                    |                    |                      |
    DEV                  CI                   QA                    PROD
  no env set        MODE=local           URL=qa gpu            URL=private gpu tier
  managed           tiny CPU model       scale-to-zero          vLLM, multi-LoRA
  llama-server      (MODEL=hf:..#sha)    Cloud Run GPU          over private network
  small GGUF,       Train backend=fake   or small prod-like     app on CPU instances
  same family                            stack                  token = Secret
```

### 10.3 Per-environment overrides

Development sets nothing. The code holds every default.

CI (end-to-end job), as a `.env` or job environment:

```
<PREFIX>_AI_CHAT_MODE=local
<PREFIX>_AI_CHAT_MODEL=hf:<org>/<tiny-model>-GGUF/<file>.gguf#sha256=<hex>
<PREFIX>_AI_CHAT_GPU=cpu
<PREFIX>_AI_CHAT_CONTEXT=2048
<PREFIX>_AI_TRAIN_BACKEND=fake
<PREFIX>_AI_FETCH=on
```

The CI image provisions the engine and the tiny model in a cached layer, so the
fetch normally finds them present.

QA:

```
<PREFIX>_AI_CHAT_URL=https://qa-gpu.example.test/v1
<PREFIX>_AI_CHAT_KEY=<service token>
<PREFIX>_AI_CHAT_MODEL=base-8b
<PREFIX>_AI_CHAT_ADAPTER=registry:support-lora@qa
<PREFIX>_AI_CHAT_TIMEOUT_MS=60000
<PREFIX>_AI_REGISTRY=gs://example-models/registry
```

Production:

```
<PREFIX>_AI_CHAT_URL=https://gpu-tier.internal:8443/v1
<PREFIX>_AI_CHAT_KEY=<service token>
<PREFIX>_AI_CHAT_MODEL=base-8b
<PREFIX>_AI_CHAT_ADAPTER=registry:support-lora@prod
<PREFIX>_AI_CHAT_MAX_INFLIGHT=32
<PREFIX>_AI_CHAT_QUEUE=128
<PREFIX>_AI_REGISTRY=gs://example-models/registry
```

The GPU tier machine itself:

```
<PREFIX>_AI_SERVE_ENGINE=vllm
<PREFIX>_AI_SERVE_BIND=0.0.0.0:8443
<PREFIX>_AI_SERVE_TOKEN=<service token>
<PREFIX>_AI_REGISTRY=gs://example-models/registry
```

### 10.4 Development models and adapters: the base-model trap

A LoRA adapter belongs to exactly one base model. An adapter trained on the
production 8B base does not load on a 1B development model of the same family.
The design offers three development modes and names them, so nobody discovers
this at run time:

1. **The same base, quantised.** An 8B base at Q4 is about 5 GB and fits a
   16 GB laptop. Development then uses the real production adapter converted to
   GGUF. This is the default when `Local.fit` says it fits.
2. **A small sibling, no adapter.** For UI and flow work on small machines.
   The registry refuses to load an adapter whose recorded base digest differs
   from the engine's base, with a typed error.
3. **A twin adapter.** The same dataset version trained on the small sibling
   (cheap and local), registered as `support-lora-dev`. Same lineage, different
   base. Behaviour is close, not identical, and eval runs on the real base only.

## 11. Security

### 11.1 Local engines

- Bind `127.0.0.1` only. A port of `0` picks a free port.
- A fresh random API key per engine run, in a `0600` file passed with
  `--api-key-file`, never on the command line. Other local users and other
  projects cannot call the engine without it.
- The child starts with a cleared environment and an allow-list, so operator
  secrets (database URLs, tokens) never reach it. This applies the `procenv`
  rule (v0.27.7) to the engine.
- On Linux, the engine runs with no new privileges and, where available, a
  read-only view of the model store. Sandboxing on macOS is open question 9.

### 11.2 Cloud

- The GPU tier sits on a private network (VPC, private service connect or an
  internal load balancer). The app calls it with a service token held as a
  `Secret`, and `Serve` compares tokens in constant time.
- Training and serving use workload identity or instance roles with least
  privilege: the trainer reads only its staging prefix and writes only its
  output prefix. The serving tier reads only the registry. Only the promote
  path (CI, or a named operator role) writes aliases.
- Staging and registry buckets use server-side encryption and deny public
  access. Signed URLs are short-lived.

### 11.3 Supply chain

- Engines, toolchains and container images are pinned by digest and verified
  before use. A mismatch installs nothing and names both digests.
- Toolchain Python wheels are locked with hashes. CUDA runtime wheels are
  fetched from their upstream index by hash at provision time rather than
  re-hosted (licensing is open question 10).
- Training refuses pickle checkpoints. Base weights and adapters must be
  safetensors, and `trust_remote_code` is always off in the driver.
- Every artefact carries an SBOM, as the PostgreSQL bundles do.

### 11.4 The GGUF parser

llama.cpp parses GGUF files in C, and parsers of untrusted files have had
memory-safety defects. The engine therefore loads only content-addressed files
whose digest was pinned in code or recorded in the registry, the parse happens
in a child process (a crash is `EngineCrashed`, not a compromised app), and the
child runs with reduced privileges. A user-supplied model from an unknown
source is a deliberate act: it needs `Local.file` with a digest the user wrote.

### 11.5 Training data governance

- A dataset version records its sources, scrub report, row counts and, when
  the user supplies them, a consent basis and retention period.
- A job declares where data may go. A cloud backend requires an explicit
  region, and `plan` refuses a staging bucket in another region.
- Lineage answers "which adapters were trained on data from source X". When a
  record must be erased, `sky ai lineage --dataset <name>@<n>` lists every
  model version that needs retraining, and the registry can mark them so they
  cannot be promoted again.
- Eval reports and judge transcripts can hold user data. They live in the
  registry under the same access control as the dataset.

### 11.6 The firewall still applies

A model never promotes a model or launches paid work. Promotion, rollback,
cloud launch and deployment are actions that a `Std.Ai.Policy` can gate, so an
agent that drives the pipeline proposes, and policy and a human approval
(through `Durable.awaitSignal`) decide.

## 12. Reliability and cost controls

### 12.1 Reliability

- **Supervision.** Engines are children with health checks, bounded restarts
  in development, and fail-fast exit outside development so the platform
  restarts the tree.
- **Typed errors** for every hardware and network fact (section 8.3). Handlers
  match on `Failure`, not on message text.
- **Exactly-once paid work.** Model calls in agents, judge calls, cloud
  training launches and deployments are `Durable.step`s. A resumed run replays
  the journal and does not pay twice.
- **Resumable training** at checkpoint granularity (section 8.5).
- **Load shedding.** A full queue answers `Overloaded` at once, which keeps
  latency bounded and lets a `router` send work elsewhere on purpose (for
  example to a cheaper hosted model), as an explicit routing decision in code.
- **Atomic installs and atomic promotions.** No half-extracted engine is ever a
  candidate, and no two promotions interleave.
- **Startup checks.** Outside development, a slot that cannot resolve, or a
  local model that is not provisioned while fetch is off, stops the app at
  start with the variable or command that fixes it.

### 12.2 Cost controls

- Every cost is `Std.Money`: inference (from `PriceBook`), training (from
  `MachinePriceBook` and measured run time), judge calls, and GPU tier hours
  where the target reports them.
- A cloud training job without a budget is refused. `plan` refuses an estimate
  over budget, and `watch` cancels a running job when measured spend reaches
  the budget, then reports `BudgetExceeded`.
- Eval suites have a cost budget, and judge results are cached by output
  digest, so re-running an unchanged suite costs nothing.
- QA serves from a target that scales to zero. Local engines unload when idle.
- `Trace.totalCost` per run, per tenant and per pipeline stage.

## 13. Testing strategy

### 13.1 The stdlib itself

| Layer | What it proves | Runs |
|---|---|---|
| Pure unit tests (`sky test`) | resolver precedence, slot naming, split determinism, scrubbers, statistics (McNemar, bootstrap), gate logic, plan arithmetic, failure mapping | every commit |
| Precedence gate (Go) | AI settings go through `configLayers`: operator env beats the builder, the builder beats the fallback, a seeded or applied value is never an operator value, the prefix is honoured | every commit |
| Mock-mode flows (`rust/crates/sky/tests/*_flow.rs`) | `Endpoint`, `Eval`, `Registry` (file store) and `Train` with the `Fake` backend, offline, with `SKY_TEST_MODE` fixtures | every commit |
| Fake trainer | the driver protocol, resume from checkpoint, cancel, budget stop, register with lineage, without a GPU | every commit |
| Tiny CPU model, real engine | the pinned `llama-server` starts, serves chat, embeddings, tokenize and an adapter, crashes into `EngineCrashed`, unloads when idle | every commit, cached model |
| Engine contract suite | per pinned build: launch arguments, health route, log lines the supervisor reads, SSE frame shapes, `/metrics` names, GGUF metadata fields, adapter routes. Modelled on the list Ollama reviews on each bump | on every engine or toolchain bump, and nightly |
| Toolchain contract suite | per pinned trainer toolchain: the driver runs a ten-step LoRA on a tiny base on the CI runner's CPU or a Mac runner, and the protocol lines parse | on every toolchain bump |
| Cloud backends | request shapes and response handling against recorded fixtures (Vertex, SageMaker, GCS, S3), plus a SigV4 test vector suite | every commit. A real-cloud smoke runs manually before a release that changes a backend |
| Accelerator matrix | Metal, CUDA and ROCm variants start and report the right accelerator | release workflow, on GPU runners when they exist (open question 11) |

Every live test follows the repository rule: when its prerequisite (a cached
model, a GPU) is missing, it fails and names what to install, unless
`SKY_LIVE_TESTS` says skip. New live tests gate through
`rust/crates/sky/src/live_gate.rs`, and every new gate declares a falsifying
mutation for the harness.

### 13.2 User apps

- Unit tests run against mocked model calls, offline, as they do today.
- An eval suite is a test. `sky test` runs it against whatever the slot
  resolves to: the tiny model in CI for flow checks, the real QA endpoint for
  quality gates.
- `sky ai eval <suite> --against prod` in the pipeline is the merge gate for a
  model change. A model change and a code change both need a green suite.
- `sky fuzz` keeps working on `update`, because the AI calls are effects behind
  a `Provider`.

## 14. Phased delivery

All durations below are estimates for one experienced engineer working on Sky,
not measured figures. They assume the PostgreSQL bundle machinery (fetcher,
checksum, atomic install, registry, CI bundle workflow) is reused, not
rewritten. The research report flagged two numbers to measure before committing
to phase 1 user promises: the download size of each GPU variant and the engine
cold-start time.

### P1: endpoints, the local engine, and the eval core (estimate 8 to 12 weeks)

Ships:
- `Std.Ai.Endpoint` with every per-slot builder and its variable, through
  `configLayers`, the local-or-remote rule, the bounded queue and retries.
- `Std.Ai.Failure` and the `Provider` additions (`chatWith`, `embed`).
- `Std.Ai.Local` with `fit`, `start`, supervision, idle unload, the engine
  registry, and `sky ai provision`, `pull`, `ps`, `start`, `stop`.
- A CI workflow that builds and publishes pinned `llama-server` bundles (CPU,
  Metal, CUDA 12, Vulkan first) with `SHA256SUMS` and SBOMs.
- `Std.Ai.Eval` core: suites, the deterministic scorers, latency and cost
  budgets, `compare`, `gate`, `Passed`, JUnit and Markdown output,
  `sky ai eval`. Judge without calibration.
- Startup-report lines, `sky doctor` checks, the console AI panel (slots,
  engines, queue, latency).
- The concrete variable names in `docs/sky-toml.md`, `AGENTS.md`,
  `templates/AGENTS.md` and `templates/CLAUDE.md`, with the census regenerated.

Exit criteria: a fresh clone runs `examples/66-slack-agent` on a managed local
model with no configuration. The same binary switches to a remote endpoint with
only environment variables. The precedence gate and the engine contract suite
are green and proven falsifiable. The tiny-model CI job runs on every commit.
`EngineCrashed`, `ModelTooLarge`, `Overloaded` and `TimedOut` each have a
red-green regression test.

### P2: datasets and the registry (estimate 5 to 7 weeks)

Ships `Std.Ai.Dataset` and `Scrub`, `Std.Ai.Registry` on the file store, GCS
and S3, `Std.Ai.Pipeline` and the `src/Ai.sky` entry, `sky ai data freeze`,
`promote`, `rollback`, `lineage`, judge calibration and judge caching, and the
registry view in the console.

Exit criteria: a dataset frozen twice from the same source has the same digest.
A split assignment is stable across versions. Promotion without `Passed` does
not compile, `forcePromote` is logged, and two concurrent promotions cannot
both win.

*(As shipped: the user's decision moved the registry's records into the app's
database (§8.7), so an alias move is a compare-and-swap in one database
transaction, the same on SQLite and PostgreSQL, and the object store holds
only content-addressed artefacts that are never overwritten. The concurrent
promotion criterion is therefore proven on the database (`promoteIf`, and
`RegistryTest` / `ai_registry_flow` racing two promotions from one state),
not by GCS generation or S3 `If-Match` preconditions on alias objects, which
no longer exist. "Promotion without `Passed` does not compile" is the reject
corpus case `ai_eval_passed_is_opaque`. Phase 2 part C adds
`Std.Ai.Pipeline`, `sky ai eval`, the project-mode verbs, dataset versions in
the registry, the synthetic generation journal, the compared promotion and
`examples/72-ai-support-desk`.)*

### P3: local training and GGUF export (estimate 8 to 12 weeks)

Ships `Std.Ai.Train` with `Mlx`, `Unsloth` and `Fake`, the managed Python
toolchains (pinned interpreter, hash-locked wheels, the Sky-shipped driver and
its protocol), `plan`, the durable training workflow with checkpoints and
resume, GGUF adapter export, adapter loading in `Local`, and the training view
in the console.

Exit criteria: on a 16 GB Apple-silicon Mac and on one NVIDIA card, a user
fine-tunes a small base from Sky code only, evaluates it against the base,
promotes it to `dev`, and the managed local engine serves it, with no Python
visible to the user. Killing the trainer mid-run resumes from the newest
checkpoint. The toolchain contract suite is green.

**Shipped (v0.28.0).**

- `Std.Ai.Train` with `Mlx`, `Unsloth` and `Fake`.
- The managed toolchains: uv 0.12.16, CPython 3.12.14 from
  python-build-standalone 20260924, and hash locks made by
  `scripts/ai/lock-toolchains.sh`.
- The driver and its protocol, `plan` and `estimateMemory`.
- The durable workflow with verified checkpoints, relaunch, resume and cancel.
- GGUF and merged export, `Local.withAdapter` and `sky ai start --adapter`.
- Registration with lineage (`Train.withRegistry`).
- The Training section of the console's AI tab.
- `sky ai provision --train`, `sky ai train`, `train status | runs | resume |
  cancel` and `sky ai export`.
- `examples/73-ai-train`.

The user guide is
[`../ai/train.md`](../ai/train.md). The live tests are
`rust/crates/sky/tests/ai_train_live.rs` (Fake under the managed CPython, a
killed trainer relaunched, the adapter served by the real engine) and
`ai_train_mlx_live.rs` (MLX on Apple silicon, the trainer's process group
killed after the first checkpoint, then resumed, served and evaluated against
the base). The Unsloth backend's driver part is tested without a GPU by a
recording fixture (`runtime-go/rt/testdata/train_driver_tests/test_unsloth_fixture.py`),
and its lock installs on Linux x86_64.

Measured on an Apple M-series Mac with 16 GB: a direct MLX run of the driver
on the development base (12 steps, 2 rows of 256 tokens, rank 4 on 4 layers,
4-bit base) took 8.5 s wall time with a peak memory footprint of 2.24 GB. Its
eval loss went from 2.83 at step 6 to 2.57 at step 12. A resume from the
step-8 checkpoint gave bit-identical losses for steps 9 to 12 and an adapter
with the same SHA-256 (the optimiser state is restored).

Where the shipped phase differs from this design and from its plan:

1. **GGUF export is a Go converter in the runtime**
   (`runtime-go/rt/ai_train_convert.go`), not llama.cpp's
   `convert_lora_to_gguf.py` (section 6 and section 8.5 `convert`). So no
   toolchain carries PyTorch for the export, and no Sky-owned converter bundle
   and no `ai-assets` workflow are needed. The converter writes the layout
   llama.cpp's converter writes, with F32 tensors (lossless, and an adapter is
   small). It is unit tested, and the live test loads its output into the real
   `llama-server` b11200 (`/lora-adapters` lists it). The adapter records
   `sky.adapter.base_id` and `sky.adapter.serving_sha256`, and a local engine
   refuses it on another model (`AdapterMismatch`).
2. **The merged export runs `llama-export-lora`** from the pinned engine build
   (`Train.withMergedExport`, `sky ai export <run> --merged`). It merges into
   the Q4_0 dev model. Adapters stay the product: the registry holds adapters
   only.
3. **There is no Convert toolchain.** `Train.provision` takes a `Backend`
   (`Mlx`, `Unsloth`, `Fake`). The section 7 `Std.Ai.Toolchain` module is not
   a module: the toolchain API is in `Std.Ai.Train` (`provision`,
   `provisionBase`, `toolchainStatus`).
4. **The Unsloth lock targets CUDA 13** (the PyPI torch 2.12 wheels, NVIDIA
   driver 580 or newer), not CUDA 12, and Linux x86_64 only (not AMD). The
   Unsloth backend uses `FastLanguageModel` and `get_peft_model` with the
   driver's own loop and AdamW, not TRL's `SFTTrainer`. So every backend shares
   one data order, one protocol and one safetensors checkpoint format (never
   `torch.save`).
5. **The API shape changed.** `progress`, `cancel` and `result` take the `Job`
   (the run directory is the source of truth, so they need no database).
   `start`, `poll`, `drive`, `resume` and `workflows` were added. `progressAt`,
   `cancelAt`, `resumeAt` and `exportAt` work on a run directory (the CLI uses
   them). `withBudget`, `withCloud` and `withMachinePrices` move to phase 4
   with the cloud backends. `start` checks the job and resolves the backend
   before it creates a durable run, so a bad setting starts no run. The
   workflow does not run `plan`: a program calls `plan` before `start` to get
   `ModelTooLarge` before anything is downloaded.
6. **The trainer is a detached process** (its own session on Unix), not a
   `Process.spawn` child, because such a child dies with the program and a
   training run must outlive it. The protocol is a file
   (`<run>/events.jsonl`, the driver's standard output) read from an offset,
   not `Process.readFrom`. The driver claims standard output for protocol
   lines and sends any library output to standard error. A launch token on the
   trainer's command line identifies it, so a recycled pid is never attached
   to or signalled.
7. **Test mode simulates the trainer.** Under `sky test` with a `.env.test`, no
   Python, no model and no network are used: the launch step writes the
   protocol, verified checkpoints and a no-op adapter, so a program's own tests
   run the whole workflow.
8. **The workflow steps** are `resolve`, `stage`, `toolchain`, `base`, `shape`,
   `plan` (the memory check, ModelTooLarge before a trainer starts), `origin`, `launch-<a>`, `watch-<a>-<n>` with `tick-<a>-<n>` (a watch of at
   most five minutes, then a one-second durable sleep, so no step outlives the
   30-minute lease), `resume-<a>`, `collect`, `convert`, `merge` and
   `register`. Validation is `Train.check`, run by `start` and again in the
   workflow. Progress goes to the run directory and the console, not yet to
   `Trace`.
9. **Progress states** come from the run directory: `Provisioning` before the
   first trainer starts, `Converting` after the trainer's `done` line, and
   `Registering` once the GGUF adapter is written.

### P4: cloud training and the GPU serving tier (estimate 10 to 14 weeks)

Ships the `Vertex` and `SageMaker` backends with exactly-once launch and budget
stop, `Std.Ai.Serve` with vLLM and `llama-server`, multi-adapter serving,
alias polling and hot-load, autoscale signals, `sky ai serve --image`, and the
deployment targets in the order chosen in open question 1.

Exit criteria: a pipeline trains on the chosen cloud, gates, promotes to `prod`,
and a private GPU tier hot-loads the adapter while the app keeps serving. A
crash between launch and journal write does not create a second paid job. A
second adapter on the same base is served from the same GPU.

**Shipped (v0.28.0), Google first.** Open question 1 is answered: Google
Cloud. `Train.Vertex` trains on a Vertex AI custom job with one L4,
`Std.Ai.Serve` runs the tier with vLLM, and `Serve.cloudRun` deploys it to
Cloud Run with one L4 that scales to zero. `Std.Ai.Cloud.Google` is the REST
layer, in Sky over HTTP. User guides: [`../ai/serve.md`](../ai/serve.md) and
[`../ai/deploy-gcp.md`](../ai/deploy-gcp.md); example `examples/74-ai-cloud`.

Where the shipped phase differs from this design and from its plan:

1. **One cloud, one target.** SageMaker, GKE or EKS, Vertex AI endpoints and
   Bedrock Custom Model Import are not in this phase. `Train.SageMaker` is
   still `InvalidSetting`.
2. **`Serve.Engine` is `Vllm` only.** A tier on llama.cpp would serve one
   adapter per engine, which `Std.Ai.Local` already does on a laptop.
   `Serve.plan` and `withParallel` are not shipped: vLLM sizes itself from
   `withGpuMemoryFraction` and `withMaxModelLength`.
3. **Two containers, not one.** On Cloud Run the tier binary (distroless, the
   ingress) and the upstream vLLM image (by digest) are two containers of one
   instance that share an in-memory volume, and vLLM starts after the tier's
   startup probe. The tier talks to vLLM on loopback and does not start it.
   `sky ai serve --image` writes the tier's image and the service.
4. **`deploy` is idempotent REST steps, not a durable workflow.** Create or
   update under the service's etag, wait for the revision, grant the invokers.
   Each step can run again after a failure, so a journal adds nothing.
5. **The tier reads the store, not the database.** Each registration and
   alias move writes pointer objects (`models/<name>/versions/<n>.json`,
   `aliases/<name>/<alias>.json`, guarded by the object's generation), so the
   tier needs read access to one bucket and none to the app's database.
   A trained version keeps its PEFT files as companions of its GGUF file:
   vLLM loads PEFT, llama.cpp loads GGUF.
6. **Exactly-once launch uses the bucket.** Vertex AI has no idempotency key
   for a custom job, so the launch first writes a create-only intent object
   (`ifGenerationMatch=0`) for the run's key and attempt, then creates the job
   with the key in its labels. A crash after the intent finds the job by its
   labels (the list can lag, so it is read again) and never creates a second.
   The run's key is the digest of the run's id, which is the digest of the
   job, so the same job is the same key.
7. **The container fetches the base.** The trainer image holds the toolchain
   and the driver, not the weights. The shim fetches the base's pinned files
   from Hugging Face inside the job and checks every SHA-256. The data, the
   checkpoints and the adapter move through the staging bucket.
8. **Images are built in the user's project.** `sky ai image build` runs Cloud
   Build into the project's own Artifact Registry and pins the result by
   digest. Nothing is published to a public registry.
9. **Autoscale signals go to `/metrics` and the console**, not yet to
   OpenTelemetry.
10. **The budget is enforced three ways**: `plan` refuses an estimate above
    it, the job's runtime ceiling is set from it (so Vertex stops the job with
    no Sky process left), and the watch cancels the job when the spend at the
    price book's rate reaches it (`BudgetExceeded`).

### P5: browser and device bridges (estimate 4 to 6 weeks per backend)

Ships `Provider` backends for WebLLM or transformers.js in Sky.Spa clients
(engine script served same-origin under the strict CSP, weights cached in OPFS
or the Cache API, a consent gate before a large download), Apple Foundation
Models in the iOS shell and LiteRT-LM in the Android shell. Each backend has
capability detection and falls back to the server `Provider` through a `router`.

Exit criteria: one Sky app answers on device when the device supports it and
from the server otherwise, with the same `Provider` code and the same eval
suite run against both.

**Shipped (v0.28.0).** `Std.Ai.Device` with `Endpoint.withDevice`, the
server's offer, blob and fallback routes, the browser worker, the iOS, Android
and Mac bridges, `Registry.allowOnDevice`, `Eval.prompts` with
`Device.recordPrompts` and `replay`, `sky ai provision --device`, `--ai-web-engine`,
`--ai-embed`, `Bundle.IncreasedMemoryLimit` and `examples/75-ai-everywhere`.
The guide is [`../ai/device.md`](../ai/device.md).

Where the shipped phase differs from this design:

1. **llama.cpp everywhere, not WebLLM, transformers.js, Apple Foundation
   Models or LiteRT-LM.** One engine (llama.cpp b11200 with Sky's engine core,
   `scripts/ai/device/sky_llama.cpp`) runs the same GGUF model and the same
   GGUF LoRA adapter on every target, with the same chat template and
   sampler, so a `Std.Ai.Train` adapter runs on a device unchanged and one
   eval suite compares like with like. WebLLM needs its own compiled model
   format and evaluates generated code (no strict CSP), transformers.js has no
   LoRA path for the Qwen GGUF files, Foundation Models runs only Apple's own
   model, and LiteRT-LM runs no Qwen GGUF or LoRA. The browser engine is built
   by Sky with Emscripten (WebGPU and CPU), the iOS engine is Sky's own
   xcframework (upstream's has no simulator slice), and the Android engine a
   JNI library per ABI, all from the pinned source by the `ai-device-bundles`
   workflow.
2. **The client owns the decision, not a `router`.** `Device.provider` is a
   `Provider.wire` whose transport reads the server's offer and the device's
   capabilities and applies the pure `Device.decide`; the server's slot is the
   fallback, reached through `POST /_sky/ai/device/<slot>/call`. A
   `DeviceOnly` slot has no fallback route at all.
3. **Consent is the app's.** `Device.status` gives the size and
   `Device.prepare` downloads; nothing downloads on its own. Files come from
   the app's own server, never a third-party CDN, checked by SHA-256 per
   64 MiB chunk and per file.
4. **Weights on devices need clearance.** A registry adapter runs on devices
   only after `Registry.allowOnDevice`, which needs a scrubbed dataset in the
   same registry.
5. **WebGPU needs JSPI.** The WebGPU engine waits on the GPU through
   JavaScript Promise Integration, so Safari and Firefox use the CPU engine;
   a WebGPU load that finds no GPU device is `DeviceUnsupported`, never a
   silent CPU run.
6. **Pins after publication.** The engines' archive digests are pinned in
   `rust/crates/sky/src/ai_device.rs` once the workflow publishes them; until
   then `sky ai provision --device --from <dir>` installs a local build of the
   same script, and provisioning from the release fails with a typed error
   that names the empty pin, the workflow and the release.
7. **Streaming, beyond the design.** `Provider.openStream` and `Provider.readStream`
   (one piece per update in a TEA app) and `Provider.chatStream` (a callback
   in a Task program) stream an answer from a server slot, the built-in
   backends and a device slot. The engine core gained a step API
   (`sky_llama_chat_begin` / `sky_llama_chat_next`), and the fallback gained a
   streaming route (`POST /_sky/ai/device/<slot>/stream`).

## 15. Risks

| Risk | Mitigation |
|---|---|
| llama.cpp moves fast and its interfaces drift | pin a build, contract suite on every bump, the HTTP surface rather than the C API |
| GPU artefact matrix size and cold start | measure in P1 before promising, fetch variants on demand, never embed them in the `sky` binary |
| Python toolchains break across OS updates | pinned interpreter and hash-locked wheels per platform, toolchain contract suite, cloud backends run the same pinned container |
| Users expect "fine-tuning" to mean full training | the docs, `plan` and its errors say adapter fine-tuning only, with the memory table |
| Judge drift or bias passes a bad model | pinned judge, calibration gate, both-order pairwise, cached verdicts |
| Cloud API changes | recorded fixtures, versioned request shapes, a manual smoke before release |
| Training data leaks to the wrong region or into an adapter | region checks in `plan`, scrub hooks and reports, lineage-driven retraining |

## 16. Open questions for the user

1. **Which cloud first** for P4 training and serving: Google (Vertex AI custom
   training, Cloud Run GPU, GKE) or AWS (SageMaker, EKS, Bedrock Custom Model
   Import)? The design supports both, and the order sets P4's scope.
2. **Which trainer backend first** in P3: MLX-LM (Macs, the development
   machines) or Unsloth (NVIDIA, the likely cloud GPUs)? Both ship in P3, but
   one lands first.
3. **The judge model.** A hosted frontier model (best agreement, costs money,
   data leaves the boundary) or a pinned open model on the GPU tier (private,
   reproducible, weaker)? A default for each tier is possible.
4. **Dataset and registry storage default.** The design uses the object store
   for blobs and manifests. Should manifests and alias history also live in
   `Std.Db` (PostgreSQL) so they can be queried and joined with app data?
5. **The first base model family** for dev, CI and prod, which decides the tiny
   CI model and the development sibling. Licence terms of the family matter for
   redistribution in the CI cache.
6. **The development default for adapters** (section 10.4): the quantised real
   base, the small sibling, or twin adapters?
7. **vLLM in development.** Should `Local` also manage vLLM on a Linux NVIDIA
   workstation for exact parity with production, or is `llama-server` enough
   locally?
8. **Error payload.** Decided 2026-10-11: one generic `ErrorDetails` variant,
   `Domain DomainFailure`, shared by every domain. See section 8.3.
9. **Engine sandboxing on macOS.** `sandbox-exec` is deprecated. Accept process
   isolation alone on macOS, or invest in an App Sandbox helper?
10. **Licensing of bundled toolchains.** Re-host the Python toolchains with CUDA
    wheels fetched from upstream at provision time (the current design), or
    publish complete bundles after a licence review?
11. **GPU runners for CI.** The accelerator matrix needs GPU runners. Use a
    self-hosted runner, a cloud GPU runner per release, or test GPU variants
    only manually?
12. **Full fine-tuning of small models** (under about 1B) is feasible on
    consumer hardware. Offer it, or keep the surface adapter-only?
