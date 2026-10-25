# Testing the Std.Ai lifecycle: a guide

*(v0.28.0)* This guide tells you how to test the whole `Std.Ai` lifecycle:
data, training, evaluation, the model registry, serving, the cloud tier and
the device. It starts with checks that need no model and no network. It ends
with the paid live runs on Google Cloud.

Each section says what to run, what you should see, and how to tell that a
step failed. The API reference is `sky doc Std.Ai.<Module>`. The design is
[`../design/std-ai-lifecycle.md`](../design/std-ai-lifecycle.md). The pages
for each stage are in this directory: [`dataset.md`](dataset.md),
[`train.md`](train.md), [`eval.md`](eval.md), [`registry.md`](registry.md),
[`pipeline.md`](pipeline.md), [`local.md`](local.md), [`serve.md`](serve.md),
[`deploy-gcp.md`](deploy-gcp.md) and [`device.md`](device.md).

Two rules apply everywhere in this guide:

* Configuration is in code. Each setting is a `withX` builder, and an
  operator variable overrides it. There are no AI keys in `sky.toml`. See
  [Configuration](#configuration).
* Run `sky build`, `sky run` and `sky test` inside the project directory
  (for example `examples/73-ai-train`). Never run `sky build` from the
  repository root: it overwrites the compiler in `sky-out/`.

## Contents

1. [Prerequisites per tier](#prerequisites-per-tier)
2. [The quick path: 10 minutes, offline](#the-quick-path-10-minutes-offline)
3. [Each lifecycle stage](#each-lifecycle-stage)
4. [Each example app](#each-example-app)
5. [The automated suites](#the-automated-suites)
6. [Live GPU runs on Google Cloud](#live-gpu-runs-on-google-cloud)
7. [Known limits](#known-limits)
8. [Configuration](#configuration)

## Prerequisites per tier

Pick the tier you want to test. Each tier adds to the one before it.

| Tier | What you need | What it unlocks |
|---|---|---|
| **0. None** | `sky` and Go (the build runs `go build`). No model, no network, no account. | Every example's `sky test` suite, the pure examples (68, 69, 70 plan, 71), the `sky ai` pipeline verbs with `--test-mode`, the cloud workflow against the emulator in the runtime. |
| **1. Local engine** | `sky ai provision --dev-model` once. It installs the pinned `llama-server` (llama.cpp b11200) and the development model (Qwen2.5 0.5B Instruct, Q4_0, a 429 MB file) into the AI home. | Real answers in 72, 73 and 75. The judge and the teacher on a real model. `sky ai start`. |
| **2. Apple silicon (MLX)** | A Mac with Apple silicon. `sky ai provision --dev-model` and `sky ai provision --train mlx --base dev`. About 700 MB more. | A real QLoRA run in `examples/73-ai-train`. |
| **3. Managed Python** | Nothing to install yourself. Sky installs a pinned uv and CPython into the AI home. `sky ai provision --dev-model`, then `sky ai provision --train fake` (macOS arm64, Linux x86_64, Linux arm64), or `--train unsloth` (Linux x86_64 with an NVIDIA GPU and driver 580 or newer). The Fake trainer needs the development model for its shape. | The real driver process under a managed Python, outside test mode. |
| **4. Google Cloud** | A project of your own, a service-account key, one L4 of quota for Vertex AI training and one for Cloud Run. See [Live GPU runs](#live-gpu-runs-on-google-cloud). | Training on Vertex AI, the GPU tier on Cloud Run, `scripts/ai/gcp-live.sh`. |
| **Device** | The device engines: `sky ai provision --device web`, `--device ios` (Xcode), `--device android` (the Android SDK). | `examples/75-ai-everywhere` on `web:app`, `desktop:mac`, `mobile:ios` and `mobile:android`. |

The AI home is one directory per user. It is `SKY_AI_HOME` when you set it,
else `$SKY_HOME/ai`, else `~/Library/Caches/sky/ai` on macOS and
`$XDG_CACHE_HOME/sky/ai` or `~/.cache/sky/ai` on Linux.

The pinned `llama-server` runs on macOS arm64 (Metal) and Linux x64 (CPU). The
Linux build needs a glibc userland with `libstdc++`, `libgomp` and OpenSSL 3.
It does not run on Alpine (musl). On another host, set `SKY_AI_LLAMA_BIN` to
the directory of your own build.

In development (`ENV` unset, `dev`, `development` or `local`) a missing engine
or model downloads on first use. Elsewhere `SKY_AI_FETCH` is `off` by default,
and a missing file fails with `NotProvisioned`, which names the `sky ai`
command to run.

## The quick path: 10 minutes, offline

This path needs tier 0 only. It uses the examples' own data and test
fixtures. No model runs and no network is used.

### 1. Freeze a dataset

```
cd examples/68-ai-dataset
sky run src/Main.sky
```

You should see:

```
valid: 7 conversations, at most 57 tokens each (estimated)
froze tickets sha256:e1d2484a46825ddd053d4f26726e887ac4af6f21c16d55d6f1b211f0b74a9366
rows 7 (train 5, eval 2, test 0)
scrubbed card 1, email 1, phone 1
duplicates removed 1
steps scrub > dedupe
loaded sha256:e1d2484a4682…, eval questions: my parcel has not arrived. i am [email], call me on [phone]. | do you ship to ireland?
```

Run it again. The digest must be the same, because a version is named by the
SHA-256 of its canonical rows. A different digest on the same data is a
failure.

### 2. Scrub personal data

```
cd examples/69-ai-scrub
sky run src/Main.sky
```

You should see `kept 3 of 4 tickets`, three tickets with placeholders such as
`[EMAIL]`, `[CARD]`, `[IBAN]` and `[ORDER_ID]`, and
`replaced: card 1, email 1, iban 1, internal 1, order_id 2, phone 2, secret 1`
with `total: 9`. A real address, card number or key in the output is a
failure.

### 3. Plan a synthetic dataset

```
cd examples/70-ai-synthetic
sky run src/Main.sky
sky test tests/FaqTest.sky
```

`sky run` with no argument only prints the plan:
`plan for support-faq (seed 7): at most 12 teacher calls`, then one line per
call. The test runs the whole generation on fixtures and prints
`5 passed, 0 failed (5 total)`.

### 4. The registry: promote, refuse, roll back

```
cd examples/71-ai-registry
sky run src/Main.sky
sky test tests/RegistryExampleTest.sky
```

You should see these lines, in this order:

```
evaluated support-lora@v1  2 of 2 passed
evaluated support-lora@v2  1 of 2 passed
promoted support-lora@v1 to prod (promote)
support-lora@v2 to prod: refused by the gate: pass rate 50% is below 100%
qa rolled back: v2 -> v1
lineage of support-lora@prod: support-lora@v1
```

The refusal is the expected result. A promotion of v2 to `prod` is a failure.

### 5. Train, crash, resume and serve, offline

```
cd examples/73-ai-train
sky test tests/TrainExampleTest.sky
```

The project commits a `.env.test`, so `sky test` runs in test mode with the
Fake trainer. You should see `6 passed, 0 failed (6 total)`. The test kills
the first trainer after step 3 and expects a relaunch from the step-2
checkpoint.

### 6. Train on Vertex AI and deploy to Cloud Run, emulated

```
cd examples/74-ai-cloud
sky test tests/CloudTest.sky
```

The test points every Google Cloud call at an emulator in the runtime. It
needs no account and spends nothing. You should see `5 passed, 0 failed
(5 total)`, with the case names "the job trains on Vertex AI (emulated)" and
"the tier deploys to Cloud Run (emulated), ready, and tears down".

### 7. The device app, server side

```
cd examples/75-ai-everywhere
sky test tests/NotesTest.sky
```

You should see `5 passed, 0 failed (5 total)`.

### 8. The full-stack app and its pipeline

```
cd examples/72-ai-support-desk
sky test tests/SupportDeskTest.sky
sky test tests/AiPipelineTest.sky
```

The second suite runs the whole lifecycle (dataset, eval, register, promote,
serve) on fixtures. Each suite must end with `0 failed`.

If every step above passed, the offline lifecycle works on your machine. A
step that fails names the failed case and exits non-zero.

## Each lifecycle stage

### Dataset

**Run it.** From code: `examples/68-ai-dataset`. From the command line, with
no Sky code:

```
sky ai data freeze data/support.jsonl --name support --split 80/10/10 --seed 0
```

**Expected result.** The verb prints a line such as
`froze support: <n> rows (train <a>, eval <b>, test <c>, seed 0)`, then the
digest, then the paths of the data file and the manifest under
`.sky/ai/datasets/<name>/`. The same file with the same split and seed gives
the same digest every time. A split puts each row in a part by a hash of its
key and the seed, so a small file can give an empty part.

**How it fails.** A line that is not JSON exits non-zero with a typed
failure. A digest that changes between two runs on the same file is a defect.
`Dataset.load` on a changed file fails with `ChecksumMismatch`.

### Synthetic data

**Run it.**

```
cd examples/70-ai-synthetic
sky run src/Main.sky -- generate
```

This calls the `teacher` slot and the `judge` slot. With nothing set, both
are the local development model (tier 1). Move either one with
`SKY_AI_TEACHER_*` or `SKY_AI_JUDGE_*`.

**Expected result.** The kept rows are scrubbed and frozen into a dataset
version. Its lineage names the generator, the teacher and the seed. A 0.5B
teacher writes simple rows.

**How it fails.** A missing engine with fetching off is `NotProvisioned`. The
judge drops rows that guess, so a small output is not a failure. An
unscrubbed address in the frozen file is a failure.

### Training

**Run it.** From the command line (tier 2 on a Mac, or tier 3 with
`--backend fake`):

```
sky ai train --data .sky/ai/datasets/support --steps 12 --backend fake --register
sky ai train runs
sky ai train status <run>
```

From a web page: [`examples/73-ai-train`](#73-ai-train-train-an-adapter).

**Expected result.** The run reaches its last step, writes a GGUF adapter and,
with `--register`, registers it. `sky ai train status <run>` shows the status,
the step of the total, the train and eval loss and the last checkpoint.
Ctrl-C detaches: the trainer keeps training, and `sky ai train status <run>`
finds it again.

**How it fails.**

* A host that cannot run the backend fails with `NoAccelerator`. It never
  falls back to another backend in silence. The message names
  `SKY_AI_TRAIN_BACKEND=fake`.
* A missing toolchain with fetching off fails with `NotProvisioned`, which
  names `sky ai provision --train <backend>`.
* A trainer that dies is relaunched from its newest checkpoint up to three
  times. After that the run ends `TrainingFailed`.
* `sky ai train cancel <run>` ends the run `TrainingCancelled`.
  `sky ai train resume <run>` starts it again from its newest checkpoint.

Measured on an Apple M-series Mac with 16 GB: a direct MLX run of 12 steps
(2 rows of 256 tokens, rank 4 on 4 layers, the 4-bit base) took 8.5 s with a
peak memory footprint of 2.24 GB ([`train.md`](train.md#measured-on-a-mac)).

### Evaluation

**Run it.** In a project with an AI pipeline (`src/Ai.sky`):

```
sky ai eval support
sky ai eval support --test-mode --against prod --format json
```

**Expected result.** The suite's Markdown report on stdout, JUnit XML at
`.sky/ai/eval/<suite>/junit.xml`, and a row in the database that the app's
Sky Console shows in its AI tab. With `--format json`, one JSON object with
`suite`, `candidate`, `baseline`, `verdict`, `reasons`, `passRate`,
`reportDigest`, `version`, `markdown` and `junit`.

**How it fails.** The exit code is the verdict: 0 when the gate passes, 1 when
it fails or a call errs, 2 for a usage error. A failed gate prints each
reason. A judge below its calibration is refused by the gate. The judge is
the local pinned model by default (tier 1). With `--test-mode` its calls come
from `tests/mocks/` (tier 0).

### Cost and call facts (Std.Ai.Trace)

**Run it.** Offline, from the repository:

```
sky test tests/Std/Ai/TraceTest.sky
cd examples/73-ai-train && sky test tests/TrainExampleTest.sky
```

In your own app, call `Trace.setup db` once, record calls with `Trace.tracedChat db
(Trace.ledger run |> Trace.priced book) …`, and read `Trace.runSummary db run` or
`Trace.costs db`.

**Expected result.** `TraceTest` passes 10 cases: a call is a row with its
facts, a failed call is a row with its failure code, a keyed row is written
once, a traced agent records its call once, a table from an earlier release is
migrated in place, and `totalCost` refuses to add USD to EUR. The training
example's run leaves steps 1 to 12 in `Trace.trainingSteps` and one unpriced
`Training` row. An eval run's rows are under `report.traceRun`.

**How it fails.** `totalCost` fails with `InvalidInput` when a run is priced
in two currencies or a stored cost is not a decimal. A slot's queue wait,
start, latency and time to first token come from the runtime
(`runtime-go/rt/ai_call_facts_test.go`); a `custom` provider has only Sky's
clock, so its queue wait is empty.

### Registry

**Run it.**

```
sky ai promote support-lora 2 --to prod
sky ai rollback support-lora@prod
sky ai lineage support-lora@prod
sky ai lineage --dataset support@1
```

The verbs open the registry on the app's database. In a project with a
pipeline that is the database its code names. Else it is `--db <dsn>`, then
`SKY_DB_PATH`, then `DATABASE_URL`. The artefact store is `--registry <url>`,
else `SKY_AI_REGISTRY`, else `file://.sky/ai/registry`.

**Expected result.** `promote` moves the alias only on attached evidence that
passes, compared against the version the alias serves now. `lineage` names
the aliases, the dataset, the base model, the reports and the alias log.

**How it fails.** A refused promotion exits non-zero with `NotPromotable` (no
report attached), `GateFailed` (the evidence does not pass or regresses
against the alias) or `PromotionConflict` (the alias moved). These refusals
are correct behaviour. Use `--force --reason <text>` only when you mean to
bypass the gate.

### Serving, local

**Run it.**

```
sky ai provision --dev-model
sky ai start chat
sky ai ps
sky ai stop chat
```

**Expected result.** `sky ai start` warms the slot and holds its engine until
Ctrl-C, SIGTERM or `sky ai stop`. `sky ai ps` lists the running engines. With
none, it prints `no engines are running`. A server prints one `ai <slot>`
line per slot under its `listening` line.

**How it fails.** A remote slot that is down fails with `Unreachable`. It
never falls back to a local model. A full queue fails at once with
`Overloaded`. A model that does not fit in memory fails with `ModelTooLarge`.
An adapter trained on another base fails with `AdapterMismatch`. Run
`sky doctor` before a deploy: it checks every slot against the operator's
environment.

**A draft model and several adapters.** Give a local spec
`Local.withDraft` and `Local.withAdapters` (see
[`local.md`](local.md#several-adapters-on-one-engine)), or set
`SKY_AI_<SLOT>_DRAFT` and `SKY_AI_<SLOT>_ADAPTERS`. Call with
`Provider.Options`' `adapter` set to a loaded name, to none, and to a name
that is not loaded. **Expected result.** A loaded name and no name both
answer (the base model when no name is given). A name that is not loaded fails
with `NotRegistered`, and its message lists the loaded names. A draft with
another vocabulary fails the start with `InvalidSetting` naming
`SKY_AI_<SLOT>_DRAFT`. With a draft, each answer's `timings` carries
`draft_n` and `draft_n_accepted`. The live test
`draft_and_named_adapters_on_the_real_engine` in `ai_local_live` runs all of
this on the real engine, with the adapters on the synthetic model that
`go run ./cmd/sky-tiny-gguf <dir>` (from `runtime-go/`) writes.

### Serving, the GPU tier

**Run it offline.** The tier's tests use a fake vLLM
(`runtime-go/rt/testdata/fakevllm`):

```
cd rust
cargo test -p sky --test ai_serve_flow
cargo run --release -p xtask -- harness --only ai-serve-hot-load
```

**Run it for real.** `sky ai serve`, `sky ai serve --image`, `sky ai deploy`
and `sky ai teardown` (see [`serve.md`](serve.md)), or stage l5 of the live
runs.

**Expected result.** The tier loads two adapters on one GPU, lists both on
`/v1/models`, and hot-loads a promotion with no failed request.

**How it fails.** A call without a valid Google ID token gets 401 from Cloud
Run. A wrong service token gets 401 from the tier. A revision that never
becomes ready is `DeploymentFailed` with the platform's reason.

### Cloud training

**Run it offline.** `sky test tests/CloudTest.sky` in `examples/74-ai-cloud`
(emulator), and `cargo test -p sky --test ai_cloud_train_flow`.

**Run it for real.** Stages l2 to l4 of the live runs, or the app in
`examples/74-ai-cloud` with the settings of
[`deploy-gcp.md`](deploy-gcp.md).

**Expected result.** The plan prices the job before anything launches. The
launch is exactly once. The adapter comes back, is converted to GGUF, and is
registered with its lineage.

**How it fails.** An estimate above the budget is `BudgetExceeded`, and no
job exists after it. A missing setting is `NotConfigured`, which names the
variable, for example `SKY_AI_TRAIN_PROJECT (or Train.withCloud in code)`. No
L4 quota is `QuotaExceeded`. No free L4 in the region is
`CapacityUnavailable`, which you can retry. Sky never moves a job to another
region.

### Browser and device

**Run it.** See [`examples/75-ai-everywhere`](#75-ai-everywhere-on-device-notes).
The automated checks are `scripts/ai-device-e2e.sh` (Chromium and WebKit) and
`cargo test -p sky --test ai_device_native_flow -- --ignored` (the iOS
simulator, the Android emulator and the Mac app).

**Expected result.** A call that runs on the device sends no request to the
server. The model and the adapter are checked against their SHA-256 before
use.

**How it fails.** A WebGPU engine with no GPU device fails with
`DeviceUnsupported` and never runs on the CPU in silence. A `DeviceOnly`
slot with no storage fails with `StorageDenied` and never reaches the server.

## Each example app

The examples need no AI settings to build. Each web app sets its own port in
code with `App.withConfig`.

### 68-ai-dataset, 69-ai-scrub, 70-ai-synthetic

Command-line programs. See the [quick path](#the-quick-path-10-minutes-offline)
for the commands and the output. Their tests:

```
cd examples/68-ai-dataset && sky test tests/TicketsTest.sky    # 4 passed
cd examples/69-ai-scrub && sky test tests/RedactTest.sky       # 4 passed
cd examples/70-ai-synthetic && sky test tests/FaqTest.sky      # 5 passed
```

### 71-ai-registry

A command-line program. `sky run src/Main.sky` prints the registry story in a
fresh temporary directory. `sky test tests/RegistryExampleTest.sky` serves the
promoted adapter through a slot in test mode and prints `2 passed`. The
fixture answers only the promoted version's served name, so a test that
serves another adapter fails.

### 72-ai-support-desk

A Sky.Live support desk with its whole AI lifecycle in `src/Ai.sky`. Its
[README](../../examples/72-ai-support-desk/README.md) has each step.

```
cd examples/72-ai-support-desk
sky test tests/SupportDeskTest.sky
sky test tests/AiPipelineTest.sky
sky run src/Main.sky                      # tier 1
```

Open `http://localhost:8072`:

1. The strip at the top says `adapter: base model, no adapter`.
2. Pick a ticket in the **Inbox**. The assistant drafts a reply. Edit it and
   click **Send reply**. **Draft again** asks for a new draft.
3. **Check model** reads the status again and updates the strip.

Then, in a second terminal in the same directory:

```
sky ai members
sky ai data freeze support
sky ai eval support
sky ai run import-adapter -- --demo v1
sky ai eval support --candidate support-lora@v1 --promote-to prod
```

Within 30 seconds the strip says
`registry:support-lora@prod -> support-lora@v1`. Then import `--demo v2` and
evaluate it `--against prod --promote-to prod`. The gate must refuse it, exit
1, and leave `prod` on v1.

Every pipeline verb also takes `--test-mode`, which answers the model calls
from `tests/mocks/` but keeps the project's own database
(`support-desk.db`).

### 73-ai-train: train an adapter

```
cd examples/73-ai-train
sky test tests/TrainExampleTest.sky       # tier 0, 7 passed
sky run src/Main.sky                      # tier 2 on a Mac
SKY_AI_TRAIN_BACKEND=fake sky run src/Main.sky    # tier 3 elsewhere
```

Open `http://localhost:8073`:

1. The **Dataset** card shows the frozen version of `data/support.jsonl`.
2. Click **Train**. The **Training** card shows the step, the train and eval
   loss, the last checkpoint, the relaunches and the loss curve.
3. Stop the app with Ctrl-C mid-run and start it again. The page finds the
   run and follows it. The trainer kept training.
4. Click **Cancel**. The run ends as cancelled and the button reads
   **Resume**. Click it: the run continues from its newest checkpoint.
5. When the run is done, the **Adapter** card shows the registered version,
   and `dev` points at it.
6. Type a question in **Ask the adapter** and click **Ask**. The answer comes
   from the local engine with the adapter loaded.

From a terminal in the same directory, `sky ai train runs` lists the runs and
`sky ai train cancel <run>` cancels one.

### 74-ai-cloud: train in the cloud, serve from a GPU tier

```
cd examples/74-ai-cloud
sky test tests/CloudTest.sky              # tier 0, 5 passed, the emulator
sky run src/Main.sky                      # tier 4
```

Without the cloud settings the page opens and freezes its dataset, and
nothing reaches Google Cloud until you click a button. A click then shows a
`NotConfigured` failure that names the missing variable. For a real run, do
the setup in [`deploy-gcp.md`](deploy-gcp.md) and set the variables in the
example's [README](../../examples/74-ai-cloud/README.md). Then open
`http://localhost:8074`:

1. Click **Train on Vertex AI**. The page shows the step, the losses, the
   Vertex job's state and the spend against the budget (USD 2 in the code).
   **Cancel** stops it.
2. When the adapter is registered, click **Deploy the tier**.
3. Type a question and click **Ask**. With `SKY_AI_SUPPORT_URL` set, the
   answer comes from the tier. Without it, from the local engine with the
   version's GGUF file.
4. Delete everything: `sky ai run teardown-tier`, then
   `sky ai teardown --all-sky`. Then do the [leak check](#the-leak-check).

### 75-ai-everywhere: on-device notes

```
cd examples/75-ai-everywhere
sky test tests/NotesTest.sky                         # tier 0, 5 passed
sky ai provision --dev-model
sky ai provision --device web
sky run src/Main.sky                                 # the web app, port 8075
sky build --target web:app src/Main.sky              # the browser client
sky build --target desktop:mac src/Main.sky
sky build --target mobile:ios src/Main.sky
sky build --target mobile:android src/Main.sky
```

On `http://localhost:8075`:

1. Write a note in **Title** and **Your note**, and click **Save**.
2. Click **Summarise**. On the `web` target the summary comes from the
   server, and the badge says so.
3. On `web:app` in Chrome or Edge with a GPU, the consent card offers the
   download with its size. Click **Download** and watch the progress. The
   next summary is made on the device with WebGPU, and the badge says so. In
   Safari or Firefox the CPU engine runs it. In a private window the server
   answers.
4. **Remove from this device** deletes the model.
5. **Help** asks the server-only `help` slot.
6. **Self-check**, then **Run the self-check**: the device answers one eval
   suite, the server scores it and its own slot, and the page shows both pass
   rates.

The native apps load the backend at `App.withAppUrl` or `SKY_APP_URL` at
build time, else `localhost` (`10.0.2.2` on the Android emulator) on the
build's `PORT`. Start the backend first. Install the iOS app with
`xcrun simctl install booted …` and the Android app with `adb install -r …`.
`--ai-web-engine webgpu|cpu|both|none` picks the browser engines, and
`--ai-embed notes` puts the model in a native app.

Measured on an Apple M-series Mac (16 GB) in this example: a summary took
10.1 s on the CPU build and 0.8 s on WebGPU. The self-check took 27.1 s and
2.8 s, with both pass rates at 100%
([`device.md`](device.md#backends)).

### The bundled programs: sky-bundled/ai, ai-train, ai-cloud

The `sky ai` verbs hold no logic of their own. The CLI builds a bundled Sky
program once per Sky build and runs it with the verb's arguments:

| Program | Verbs |
|---|---|
| `sky-bundled/ai` | `provision`, `pull`, `ps`, `start`, `stop`, `promote`, `rollback`, `lineage`, `registry publish`, `data freeze <file.jsonl>` |
| `sky-bundled/ai-train` | `provision --train`, `train`, `train status`, `runs`, `resume`, `cancel`, `export` |
| `sky-bundled/ai-cloud` | `serve`, `deploy`, `teardown`, `cloud ls`, `cloud prices`, `image build` |

A project with `src/Ai.sky` adds `eval`, `data freeze <dataset>`, `run` and
`members`, built from the project and cached under `.sky/ai/cli/`. Outside
such a project, `sky ai members` exits 2 and names the module to write.
`sky ai --help` lists every verb.

To test the programs: `cargo test -p sky --test ai_cli_flow` (the engine
verbs against a fake engine), `ai_registry_flow`, `ai_train_flow`,
`ai_serve_flow` and `ai_pipeline_flow`.

## The automated suites

### Cargo tests

Run from `rust/`:

```
cargo test -p sky --test ai_dataset_flow
cargo test -p sky --test ai_train_flow
```

| Test | What it proves | Needs |
|---|---|---|
| `ai_cli_flow` | `sky ai` and `Std.Ai.Endpoint` against a fake engine | Go |
| `ai_dataset_flow` | Dataset, Scrub and Synthetic end to end | Go |
| `ai_eval_test_mode_flow` | the judge is mocked in test mode | Go |
| `ai_registry_flow` | register, promote, serve, the registry verbs | Go |
| `ai_pipeline_flow` | `Std.Ai.Pipeline` and the project verbs on example 72 | Go |
| `ai_train_flow` | `Std.Ai.Train` offline, in test mode | Go |
| `ai_cloud_train_flow` | the Vertex backend, offline, against the emulator | Go |
| `ai_serve_flow` | the GPU tier and the cloud verbs, offline | Go |
| `ai_device_flow`, `ai_device_engine_exports` | device build checks and the engine's exports | Go |
| `ai_trace_flow`, `ai_policy_router_flow`, `ai_native_tools_flow`, `ai_toolcall_continuation_flow` | Trace, Policy, the router, native tools | Go |
| `ai_local_live` | the real managed engine with no configuration, a draft model, and named adapters chosen per call | tier 1 |
| `ai_train_live` | the real driver under the managed Python, a crash and a relaunch | tier 3 (`--train fake`) |
| `ai_train_mlx_live` | a real MLX QLoRA run, killed and resumed | tier 2 |
| `ai_memory_pg_flow` (`--ignored`) | `Std.Ai.Memory.Pg` on a real cluster | PostgreSQL with pgvector |
| `ai_device_native_flow` (`--ignored`) | the iOS simulator, the Android emulator, the Mac app | Xcode, Android SDK |
| `ai_gcp_live` (`--ignored`) | the paid live stages | `scripts/ai/gcp-live.sh` only |

**`SKY_LIVE_TESTS`.** A live test whose environment is missing **fails**. It
does not skip. The failure names what to install, for example "run
`sky ai provision --dev-model` once". To skip on purpose, say so:

```
SKY_LIVE_TESTS=skip cargo test -p sky --test ai_local_live
```

Any other value of `SKY_LIVE_TESTS` is an error. `ai_gcp_live` run without
the wrapper fails and names `scripts/ai/gcp-live.sh`.

### Sky test suites

The stdlib's own suites are in `tests/Std/Ai/`: `DatasetTest`, `DeviceTest`,
`EvalTest`, `FailureTest`, `PipelineTest`, `ProviderEndpointTest`,
`RegistryStoreTest`, `RegistryTest`, `ScrubTest`, `ServeTest`, `StoreTest`,
`SyntheticTest`, `TrainCloudTest` and `TrainTest`. Run them with:

```
scripts/sky-suites.sh Std_Ai
```

The filter matches the suite's path with `/` changed to `_`. Each example
has its own suite, listed under [Each example app](#each-example-app).

### Go tests

The runtime's AI tests are `runtime-go/rt/ai_*_test.go`: the console tab,
datasets, dedupe, the device routes and worker, Google ID tokens, registry
adapters, the engine watcher, the Google Cloud emulator, the GPU tier,
startup lines, the store, streaming, the toolchains, the converter and the
trainer. They are in the `rt` package:

```
cd runtime-go
go test -timeout 30m ./rt/
```

Add `-run` with a pattern such as `'Ai|Train|Toolchain|Device|FakeGCP'` to
run fewer tests. Many AI test names start with `TestAi`, but not all of them.

### xtask gates

Run from `rust/`. A gate's verdict needs an exact count of assertions, and
each gate has a mutation that must turn it red.

```
cargo run --release -p xtask -- harness --only ai-train-resume,ai-train-exactly-once,ai-serve-hot-load
cargo run --release -p xtask -- harness --only ai-device-decide,ai-device-no-silent-fallback,ai-device-digest,ai-stream-pieces,ai-device-blob-allowlist
cargo run --release -p xtask -- harness --tier t2 --only ai-device-partition
cargo run --release -p xtask -- harness --verify-falsifiers --only ai-train-resume
```

| Gate | Tier | It proves |
|---|---|---|
| `ai-train-resume` | T1 | a relaunch resumes from the newest verified checkpoint and skips a torn one |
| `ai-train-exactly-once` | T1 | a Vertex launch whose journal row was lost attaches to its job: one create, never two |
| `ai-serve-hot-load` | T1 | the tier hot-loads a promotion and drains the old adapter with no failed request |
| `ai-device-decide` | T1 | the device decision, every rule in order |
| `ai-device-no-silent-fallback` | T1 | WebGPU with no GPU device is `DeviceUnsupported`, never a CPU run |
| `ai-device-digest` | T1 | a download is stored only after every chunk and the file match their SHA-256 |
| `ai-stream-pieces` | T1 | a streamed answer arrives piece by piece |
| `ai-device-blob-allowlist` | T1 | the blob route serves only the digests a current offer names |
| `ai-device-partition` | T2 | the device kernels run in the client and the slot registers on the server |
| `ai-device-privacy` | T4 | a call on the device sends nothing to the server (Chromium, strict CSP) |
| `ai-device-csp` | T4 | the worker and the engines run under the strict policy with no violation |
| `ai-device-ios-sim` | T4 | the iOS simulator runs a device slot on its own llama.cpp |

The `sky-suites` gate (T1) runs every `tests/Std/Ai/` suite with the other
root suites. The T4 device gates need browsers, the device engines and a
simulator.

### What the release workflow covers

`.github/workflows/release.yml` runs every gate job before a release, and on
a manual dispatch on a branch. For the AI lifecycle it runs:

* `cargo test --workspace`, which includes every `ai_*_flow` test, and the
  heavy real-database leg, which includes `ai_memory_pg_flow` with pgvector.
* The T1 harness shards, with the eight T1 `ai-*` gates and `sky-suites`,
  and the T2 shard with `ai-device-partition`. Each gate's falsifier is
  proven again with `--verify-falsifiers --all`.
* `gate-ai-local` (`ai_local_live`), `gate-ai-train` (`ai_train_live`) and
  `gate-ai-train-mlx` (`ai_train_mlx_live`, MLX on the CPU with
  `SKY_AI_ACCEL=cpu`).
* `gate-ai-device-web`: `scripts/ai-device-e2e.sh` in Chromium and WebKit on
  the CPU engine, and the `ai-device-privacy` and `ai-device-csp` gates.
* `gate-ai-device-native`: `ai_device_native_flow` in the iOS simulator and
  the Mac app, the `ai-device-ios-sim` gate, and a separate Android emulator
  run (API level 35).
* The example sweep, which runs 68, 69, 70 and 71 as programs and serves `/`
  of 72, 73, 74 and 75.

The release workflow does **not** run the paid live tests.
`.github/workflows/ai-cloud-live.yml` runs them on manual dispatch only: you
type `spend` in its `confirm` input and give the stages (default
`l0,l1,l2,l3`). It reads the project id from a repository secret.

## Live GPU runs on Google Cloud

These runs cost money. Read this whole section before you start.

### One-time setup

```
scripts/ai/gcp-bootstrap.sh --project <your-project>
```

This is free and idempotent. It turns on the APIs and creates two service
accounts with no project-wide role, `sky-trainer` and `sky-serve`. Then ask
for one L4 of quota for Vertex AI training and one for Cloud Run
([`deploy-gcp.md`](deploy-gcp.md#quotas)). A new project has none.

Sky signs as a service account. Set `GOOGLE_APPLICATION_CREDENTIALS` to a
service-account key file. A user login (`authorized_user`) is refused.
The wrapper never prints the key, its path or a token.

Build the compiler from the tree first (`./scripts/build.sh`). The wrapper
refuses a `sky-out/sky` that is older than the tree.

### The wrapper and its stages

```
scripts/ai/gcp-live.sh --project <your-project> --region <region> \
  [--serve-region <region>] [--stages l0,l1,l2,l3] [--cap 40.00]
```

The default region is `us-central1`, and the default stages are
`l0,l1,l2,l3`. `--serve-region` puts the Cloud Run tier (l5) in another
region, where your Cloud Run L4 quota is.

| Stage | What it does | Pass condition |
|---|---|---|
| `l0` | Preflight: the credential's type, the APIs, the L4 quotas, room in the ledger | free. It exits 5 if l4 or l5 is asked and its quota is 0 |
| `l1` | A bucket, an image repository, the trainer and tier images built with Cloud Build in your project (with l5: a Docker Hub remote repository and the tier's token secret) | both images built and pinned by digest |
| `l2` | A plan with a budget of USD 0.01 | `BudgetExceeded`, and no job exists after it |
| `l3` | Fake-driver jobs on `n2-standard-4` | exactly-once launch after a lost journal row, a relaunch from a checkpoint, a cancel, the hard runtime ceiling |
| `l4` | A real Unsloth QLoRA run on one L4 (`g2-standard-4`) | the adapter collected and registered |
| `l5` | The GPU tier on Cloud Run with one L4 | the base answers, again after a 16-minute idle wait (scale from zero), two adapters on one GPU, `/v1/models` lists both, 20 requests with no failure during a promotion, 401 without an ID token, 401 with a wrong service token |

### The budget guard

The wrapper sums the ledger `scripts/ai/live/gcp-ledger.jsonl`. A run starts
only when the ledger's total plus 1.25 times this run's worst case is at or
under `--cap` (default USD 40.00). The worst cases per stage are in
`scripts/ai/live/gcp-prices.json`:

Each worst case is the spend of the stage's enforced bound at the
catalogue's rates. The wrapper runs each of l2 to l5 under its own wall-clock
window (`stageLimits.stageSeconds` in the price file). After a stage, ended
or stopped at its window, it cancels the run's live Vertex jobs, waits at
most 600 s (`cancelWaitSeconds`) for them to end, and after l5 deletes the
Cloud Run service. The bound is the window plus that wait, from the job's
creation. A job's `scheduling.timeout` counts only from RUNNING, so it does
not bound the PENDING and provisioning time that the ledger counts (the l4
job's 40 minutes did not stop runs of 3155 s and 3797 s from creation to
end).

| Stage | Window | Worst case (USD) | Basis |
|---|---|---|---|
| l0 | | 0.00 | reads only |
| l1 | | 0.82 | two Cloud Builds, each stopped at its 3600 s timeout (USD 0.72), and an allowance of USD 0.10 for about 25 GB of images kept for under a day |
| l2 | 900 s | 0.00 | a refused plan creates nothing |
| l3 | 2700 s | 1.10 | four `n2-standard-4` job chains at once, each for 2700 + 600 s at USD 0.30 an hour |
| l4 | 4500 s | 1.35 | one `g2-standard-4` job chain for 4500 + 600 s at USD 0.95 an hour (1.3458, rounded up) |
| l5 | 3600 s | 1.68 | the l5-prep `n2-standard-4` job chain and one Cloud Run L4 instance, each for 3600 + 600 s at USD 0.30 and 1.14 an hour |

`rust/crates/sky/tests/ai_gcp_live_ledger.rs` fails when a worst case is
below its bound, below the estimate of any run of that stage in the ledger,
or when a window is shorter than a passing run's longest resource. It runs
offline in the normal test suite.

A refused run prints `REFUSED: the ledger has no room for this run under the
cap` and exits 4.

If the wrapper dies, three bounds still stop spend: each Vertex job's
`scheduling.timeout`, Cloud Run's `maxInstances 1` with scale to zero, and
the EXIT trap.

### Cost per stage, as measured

These figures come from `scripts/ai/live/gcp-ledger.jsonl`. Each is the
wrapper's estimate: the measured seconds at the list price of 2026-10-12.
`actualUsd` is empty on every line, so no figure is reconciled with a bill.

| Stage | Measured | Estimate (USD) |
|---|---|---|
| l1, trainer image | 557 to 1176 s of Cloud Build | 0.06 to 0.12 |
| l1, tier image | 17 to 89 s of Cloud Build | under 0.01 |
| l3, the Fake jobs of a passing run | up to 524 s per job on `n2-standard-4` (1359 s on 2026-10-17) | 0.08 (two passing runs: 0.085 and 0.076), 0.28 on 2026-10-17 |
| l4, the passing runs | 3155 s on `g2-standard-4`, from creation to end (334 s on 2026-10-17) | 0.83 (0.09 on 2026-10-17) |
| l5, the passing runs (europe-west4) | Cloud Run L4 for 1258 s and 1302 s, which include the 16-minute idle wait, and two Fake jobs (285 s and 141 s, then 636 s and 125 s) | 0.40 and 0.41 for Cloud Run, 0.035 and 0.063 for the jobs |

Whole passing runs: l0 to l3 cost USD 0.16 and USD 0.20, l0, l1 and l4 cost
USD 0.91, and l0, l1 and l5 cost USD 0.53. The v0.28.0 verification on
2026-10-17 ran l0 to l4 in us-central1 for USD 0.46
(`sky-live-20261017081033-5b9bed`) and l0, l1 and l5 in europe-west4 for
USD 0.56 (`sky-live-20261017085615-cbd1a8`). The leak check was clean after
each of them.

The ledger's l4 seconds count from the job's creation, so they include the
wait for a free L4.

**Cold start.** The l5 stage measures it. Its scenario prints one JSON line
(`ai_gcp_live: l5: …`) with `deploySeconds`, `coldStartMs` (the first call
after the deploy) and `idleColdStartMs` (the first call after a 960 s idle
wait, scale from zero). The l5 test writes that line to the wrapper's
measurement log, and the ledger line keeps its numbers under
`measurements.l5` (with `baseOk`, `idleColdOk`, `hotLoadOk` and the other
outcomes; response texts and error strings stay out). Recorded so far
(europe-west4, one L4, the development base):

| Run | `deploySeconds` | `coldStartMs` | `idleColdStartMs` | Tests |
|---|---|---|---|---|
| `sky-live-20261014042006-f7b821` | 167 | 2590 | not measured (no idle wait then) | failed |
| `sky-live-20261014045345-b61c00` | 145 | 2499 | 119946 | failed (the `/v1/models` check) |
| `sky-live-20261014053814-92a459` | 137 | 2263 | 107390 | passed |
| `sky-live-20261017085615-cbd1a8` | 169 | 2357 | 116896 | passed |

The first three lines were backfilled from the runs' logged `ai_gcp_live: l5:`
lines (`measurementsNote` says so). From now on the wrapper records them.

### Teardown

The wrapper's EXIT trap always runs, after a pass, a failure or Ctrl-C. It
cancels the run's Vertex jobs, runs `sky ai teardown --all-sky --label
live-run=<run id>`, deletes the Cloud Run service, the secret, the
repositories and the bucket, appends the ledger line, and runs the leak check.

Every resource carries `sky-managed=true`, `live-run=<run id>` and
`sky-kind`. To delete what an app of your own made:

```
sky ai cloud ls --project <your-project> --region <region>
sky ai teardown --service <service>
sky ai teardown --run <key> --staging gs://<bucket>/<prefix>
sky ai teardown --all-sky
```

### The leak check

The wrapper counts what is left in the training region and the serving
region: live Vertex jobs, Cloud Run services, repositories, buckets and
secrets with `sky-managed=true`. It tries three times, 20 seconds apart. You
must see these lines with 0, and the ledger line must say `leak check clean`:

```
gcp-live: leak check: 0 live Vertex jobs in <region>
gcp-live: leak check: 0 Cloud Run services in <region>
gcp-live: leak check: 0 repositories in <region>
gcp-live: leak check: 0 buckets
gcp-live: leak check: 0 secrets
gcp-live: ledger: estimate USD <x> (worst case USD <y>), leak check clean
```

A leak fails the run with exit 3, whatever the tests did. Then delete the
named resources by hand and check again with `sky ai cloud ls`. A cancelled
job can write one last file to its bucket after Vertex reports it ended. The
teardown deletes the bucket again until it is gone.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | the asked stages passed and the leak check is clean |
| 2 | a usage error, a missing or wrong credential, or an API that is off |
| 3 | the leak check found a resource that can cost money |
| 4 | the budget guard refused the run |
| 5 | l4 or l5 was asked and its L4 quota is 0 |
| other | the status of the live tests |

## Known limits

* **Cloud.** Google Cloud only. The AWS training backend (`SageMaker`) is not
  supported yet: `SKY_AI_TRAIN_BACKEND=sagemaker` is `InvalidSetting`. The registry
  store works on S3.
* **Cost figures.** The ledger holds estimates at list prices. No line is
  reconciled with a bill (`actualUsd` is empty). The budget guard therefore
  counts each past run at its worst case.
* **Worst cases.** Each stage's worst case is its window plus the cancel
  wait at the catalogue's rate, from the job's creation (see the budget
  guard). A stage that does not finish in its window is stopped and fails. The
  image storage part of l1 is an allowance, not a catalogue read.
* **Cold start.** A scale-from-zero start took 107 to 120 s in the two runs
  that measured it (`measurements.l5.idleColdStartMs` in the ledger). Warm
  the tier before traffic (`Endpoint.warm`) or keep one instance
  (`Serve.withMinInstances 1`, billed while idle).
* **Quotas and capacity.** A new project has no L4 quota. A region with no
  free L4 is `CapacityUnavailable`. Sky does not move a job to another region.
* **Local engine.** A named adapter in `Local.withAdapters` is a pinned
  file or Hugging Face reference, or a `registry:<name>@<alias>` reference
  when the slot has a registry (`Endpoint.fromRegistry`). The slot resolves it
  when it starts and again as the alias moves, and checks the version's digest
  and base model (`docs/ai/local.md`, "Several adapters on one engine"). The
  pins use the upstream llama.cpp release assets. The managed engine runs on
  macOS arm64 and Linux x64 with glibc.
* **Training backends.** MLX needs Apple silicon. Unsloth needs Linux x86_64
  with an NVIDIA GPU and driver 580 or newer. The CLI trains the
  development base or any base given as `--base hf:<org>/<repo>@<commit>`
  or `--base file:<directory>` with `--serving <model reference>`.
* **The pipeline.** Training runs as a pipeline task (`Pipeline.withTask`,
  as in `examples/74-ai-cloud`'s `train` task). There is no separate training
  member.
* **Journals.** The judge journal and the synthetic journal are tables in the
  database, not `Std.Durable` steps. An unchanged call is still asked once.
* **The browser.** WebGPU needs JavaScript Promise Integration and a GPU with
  `shader-f16`, so Safari and Firefox use the CPU engine. The CPU engine takes
  models up to 1 GiB. A private window keeps no storage, so the server
  answers.
* **The tier.** `sky ai serve` uses the development base. A tier on another
  base is a program of your own (`Serve.render`).

## Configuration

Every AI setting is a `withX` builder in code with an operator variable that
overrides it. The order is: **operator variable, then `withX` in code, then
the stdlib default.** An empty variable counts as unset. A value that does
not parse is never skipped: it is `InvalidSetting`, whose `setting` names the
variable (or the `withX` builder) and whose `reason` names the form expected.
The message shows the value only when it cannot be a secret (never a key, a
token or a URL). A server checks the AI variables of its declared slots and the
machine variables before it listens, and refuses to start on a bad one (exit
1, or the `Err` of an embedded app's Task). A setting read at a call (a
program that serves nothing, a device offer, a training host) fails that
call. Under a custom
`[env] prefix` the variables are `<PREFIX>_AI_...`. Nothing about AI is read
from `sky.toml`. Build-time choices are CLI flags.

### A model slot

A slot is a named use of a model, for example `chat`, `judge` or `teacher`.

| Builder | Variable | Default |
|---|---|---|
| `Endpoint.withMode` | `SKY_AI_<SLOT>_MODE` (`local`, `remote`) | inferred |
| `Endpoint.withUrl` | `SKY_AI_<SLOT>_URL` | none |
| `Endpoint.withKey` (a `Secret`) | `SKY_AI_<SLOT>_KEY` | none |
| `Endpoint.withGoogleAuth` | `SKY_AI_<SLOT>_AUTH` (`bearer`, `google`) | `bearer` |
| `Endpoint.withModel` | `SKY_AI_<SLOT>_MODEL` | the `withLocal` model |
| `Endpoint.withAdapter` | `SKY_AI_<SLOT>_ADAPTER` | none |
| `Endpoint.withTimeout` | `SKY_AI_<SLOT>_TIMEOUT_MS` | 120000 |
| `Endpoint.withRetry` | `SKY_AI_<SLOT>_RETRIES` | 2 |
| `Local.withContext` | `SKY_AI_<SLOT>_CONTEXT` | 8192, halved until it fits |
| `Local.withGpu` | `SKY_AI_<SLOT>_GPU` | `auto` |
| `Local.withDraft` | `SKY_AI_<SLOT>_DRAFT` (a model reference) | none |
| `Local.withDraftTokens` | `SKY_AI_<SLOT>_DRAFT_MAX`, `SKY_AI_<SLOT>_DRAFT_MIN` | the engine's |
| `Local.withAdapters` | `SKY_AI_<SLOT>_ADAPTERS` (`name[:scale]=<ref>,…`) | none |

The full list, with the queue settings, is in
[`local.md`](local.md#per-slot-settings). The judge is the slot `judge`
(`SKY_AI_JUDGE_*`), and the teacher is the slot `teacher`
(`SKY_AI_TEACHER_*`).

Example. The code holds the local default:

```elm
chatSlot : Endpoint
chatSlot =
    Endpoint.endpoint "chat"
        |> Endpoint.withLocal (Local.model Local.devModel)
        |> Endpoint.withTimeout 60000
```

The operator moves it to a GPU server in production with no code change:

```
SKY_AI_CHAT_URL=https://gpu.internal:8443/v1
SKY_AI_CHAT_KEY=<a secret from your secret store>
```

### The machine

| Variable | Meaning | Default |
|---|---|---|
| `SKY_AI_HOME` | the AI home | see [Prerequisites](#prerequisites-per-tier) |
| `SKY_AI_FETCH` | `on` or `off`: may a missing file download at run time | `on` in development, `off` otherwise |
| `SKY_AI_ACCEL` | force `cpu`, `metal`, `cuda`, `rocm` or `vulkan` | the engine build's accelerator |
| `SKY_AI_LLAMA_BIN` | your own `llama-server` directory | the pinned build |
| `SKY_AI_REGISTRY` | the artefact store: `file://`, `gs://` or `s3://` (code: `Store.configured`) | `file://.sky/ai/registry` |

### Training

| Builder | Variable |
|---|---|
| `Train.withBackend` | `SKY_AI_TRAIN_BACKEND` (`mlx`, `unsloth`, `vertex`, `fake`) |
| `Train.withCloud` | `SKY_AI_TRAIN_PROJECT`, `SKY_AI_TRAIN_REGION`, `SKY_AI_TRAIN_MACHINE`, `SKY_AI_TRAIN_STAGING` |
| `Train.withBudget` | `SKY_AI_TRAIN_BUDGET` (for example `USD 2.00`) |
| `Train.withImage` | `SKY_AI_TRAIN_IMAGE` (pinned by digest) |
| `Train.withServiceAccount` | `SKY_AI_TRAIN_SERVICE_ACCOUNT` |
| `Train.withLabels` | `SKY_AI_TRAIN_LABELS` (`team=support,env=qa`) |

How to test a bad value. Set one variable to a value that does not parse and
start the app, or call the slot:

```
SKY_AI_CHAT_TIMEOUT_MS=soon ./sky-out/app
```

**Expected result.** The server prints
`[sky.ai] FATAL: the app refuses to start: …InvalidSetting…` naming
`SKY_AI_CHAT_TIMEOUT_MS` and "a whole number above 0", and exits 1. The Go
test `TestAiSlotSettingIsRefusedNotSkipped` covers each kind of setting (whole
numbers, milliseconds, `MODE` / `AUTH` / `GPU`, the URL, model references,
the adapter list, the draft settings), with `TestAiGlobalSettingIsRefused`,
`TestAiDevicePlacementIsRefusedNotSkipped` and
`TestAiStartupRefusesABadSetting` for the machine, device and startup checks.

`Train.withMachinePrices`, `withMaxRuntimeMinutes`, `withSpot` and
`withKeepStaging` are code only. The `sky ai train` flags (`--project`,
`--region`, `--machine`, `--staging`, `--budget`, `--image`, `--price`) fall
back to the same variables.

### The GPU tier

| Builder | Variable | Default |
|---|---|---|
| `withBind` | `SKY_AI_SERVE_BIND` | `0.0.0.0:8080` |
| `withToken` | `SKY_AI_SERVE_TOKEN` | none (loopback only) |
| `withAdapters` | `SKY_AI_SERVE_ADAPTERS` | none |
| `withMaxAdapters` | `SKY_AI_SERVE_MAX_ADAPTERS` | 4 |
| `withAliasPolling` | `SKY_AI_SERVE_POLL_S` | 30 |
| `Serve.withTierImage` | `SKY_AI_SERVE_IMAGE` | none |

The Cloud Run target's project and region fall back to
`SKY_AI_SERVE_PROJECT` and `SKY_AI_SERVE_REGION`. The `sky ai deploy` target
flags are `--project`, `--region`, `--adapters`, `--engine-image`,
`--token-secret`, `--registry`, `--service-account`, `--invoker`,
`--min-instances` and `--max-instances`.

### The device

| Builder | Variable |
|---|---|
| `Device.spec Auto/DeviceOnly/ServerOnly` | `SKY_AI_<SLOT>_PLACEMENT` (`auto`, `device`, `server`) |
| `Device.withModel` | `SKY_AI_<SLOT>_DEVICE_MODEL` |
| `Device.withAdapter` | `SKY_AI_<SLOT>_DEVICE_ADAPTER` |

The build flags are `--ai-web-engine webgpu|cpu|both|none` and
`--ai-embed <slot>`.

### Tests

Test mode needs no variable of yours. A project that commits a `.env.test`
runs `sky test` in test mode: no engine starts, nothing downloads, and every
model call is answered by `tests/mocks/`. For training, the examples set
`SKY_AI_TRAIN_BACKEND=fake` there. For Google Cloud, they set
`SKY_AI_GCP_ENDPOINT=http://sky-gcp.invalid`, which sends every call to the
emulator in the runtime. See [`../tooling/testing.md`](../tooling/testing.md).
