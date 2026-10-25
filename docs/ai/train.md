# Fine-tuning adapters: `Std.Ai.Train`

*(v0.28.0)* `Std.Ai.Train` fine-tunes a LoRA or QLoRA adapter from Sky code.
A job trains a base model on a frozen dataset version
([`dataset.md`](dataset.md)). The run is a durable workflow: the trainer runs
in a detached process, a trainer that dies is relaunched from its newest
checkpoint, and the finished adapter is converted to GGUF and registered with
its lineage ([`registry.md`](registry.md)). The managed local engine then
serves it ([`local.md`](local.md)).

The trainer is Python, but you never install or see Python. Sky installs a
pinned interpreter and a hash-locked set of wheels under the AI home, and runs
its own driver in it.

The design is [`../design/std-ai-lifecycle.md`](../design/std-ai-lifecycle.md)
§8.5 (the shipped notes in §14 P3 record where this release differs from it).
The live API is `sky doc Std.Ai.Train`. `examples/73-ai-train` is a web app
that trains, registers and serves an adapter, and its tests run the whole
lifecycle offline.

## Quick start

### From the command line

```bash
sky ai provision --dev-model              # the engine and the Q4_0 dev model
sky ai provision --train mlx --base dev   # the Mlx toolchain and the 4-bit base weights
sky ai data freeze data/support.jsonl --name support
sky ai train --data .sky/ai/datasets/support --steps 60 --gguf
```

`sky ai train` trains the newest frozen version of `support` on the
development base and prints the adapter, its losses and the GGUF file. A
Ctrl-C stops the watcher only. The trainer keeps training, and the same
command (or `sky ai train resume <run>`) attaches to it again.

### From code

```elm
import Std.Db as Db exposing (Db)
import Std.Ai.Dataset as Dataset
import Std.Ai.Train as Train


supportJob : Dataset.Version Dataset.Chat -> Train.Job
supportJob version =
    Train.lora Train.devBase version
        |> Train.withName "support"
        |> Train.withQuantisedBase
        |> Train.withRank 16
        |> Train.withMaxSteps 60
        |> Train.withSeed 7
        |> Train.withGgufExport


trainSupport : Db -> String -> Task Error Train.Adapter
trainSupport db digest =
    Dataset.load (Dataset.folder ".sky/ai/datasets" "support") digest Dataset.chatCodec
        |> Task.andThen (\version -> Train.drive db (supportJob version))
```

`Train.drive` starts the run, advances it until it ends and returns the
adapter. A program that must not block (a web app, a worker) uses
`Train.start` and `Train.poll` instead ([The durable
workflow](#the-durable-workflow)).

The backend is not in the code. On a Mac with Apple silicon the run uses MLX.
On Linux with an NVIDIA GPU it uses Unsloth. In CI and in tests,
`SKY_AI_TRAIN_BACKEND=fake` runs the scripted Fake trainer.

## The job

A job is a base model, a frozen dataset version and its settings.

| Function | Dataset | The loss counts |
|---|---|---|
| `lora base version` | `Dataset.Version Dataset.Chat` (`{"messages": [...]}`) | the last assistant turn of each conversation |
| `loraOnInstructions base version` | `Dataset.Version Dataset.Instruction` (`{"prompt", "completion"}`) | the completion |

A job takes a frozen `Version`, never a live dataset. So the type fixes the
lineage: the adapter records the version's name and digest.

| Builder | Default | Meaning |
|---|---|---|
| `withName n` | `adapter` | the run directory's prefix and the registry model name. Lower case letters, digits and `-`, at most 48 characters, not starting with `-` |
| `withQuantisedBase` | off | mark the job QLoRA (the base weights are 4-bit). Recorded in the lineage and the registry method |
| `withRank r` | 8 | the LoRA rank |
| `withAlpha a` | twice the rank | the update is scaled by alpha / rank |
| `withLayers n` | 16 | how many of the last decoder layers get adapters. `-1` for all |
| `withTargets ts` | the four attention projections | the projections that get adapters, as Hugging Face module paths |
| `withLearningRate r` | `1e-4` | the learning rate (Adam) |
| `withEpochs n` | 1 | passes over the train split. `withMaxSteps` wins |
| `withMaxSteps n` | 0 (not set) | stop after this many optimiser steps |
| `withBatch { perDevice, accumulate }` | `{ perDevice = 4, accumulate = 1 }` | rows per step, and the gradients accumulated over that many of them |
| `withSequenceLength n` | 1024 | tokens per row. A longer row is cut |
| `withSeed s` | 0 | the seed of the adapter's initial values and of the data order |
| `withCheckpointEvery n` | 100 | write a checkpoint every n steps (0: none) |
| `withEvalEvery n` | 50 | measure the eval split's loss every n steps (0: at the end only) |
| `withBackend b` | detected | train on this backend unless `SKY_AI_TRAIN_BACKEND` says otherwise |
| `withGgufExport` | off | also convert the adapter to GGUF |
| `withMergedExport` | off | also write a merged GGUF model. It implies `withGgufExport` |
| `withWorkDir d` | `.sky/ai/runs` | the directory that holds the run directories |
| `withRegistry r` | none | register the finished adapter in this registry |
| `withMaxRelaunches n` | 3 | how often a dead trainer is relaunched before the run fails |
| `withWatchWindow ms` | 300000 | the longest one watch of the trainer lasts (at most five minutes) |
| `fakeCrashAt n` | 0 | `Fake` only: the first attempt dies after step n |
| `fakeStepMs n` | 20 | `Fake` only: the time one step takes |

The targets are `self_attn.q_proj`, `self_attn.k_proj`, `self_attn.v_proj`,
`self_attn.o_proj` (the default) and `mlp.gate_proj`, `mlp.up_proj`,
`mlp.down_proj`.

`Train.check job` checks a job without I/O. A setting out of range is
`InvalidSetting` with the setting's name: a bad name, a rank below 1, a
negative alpha, `withLayers 0` or below `-1`, no target or an unknown one, a
learning rate of 0 or less, no epoch and no step, a batch of no rows, a
sequence below 16 tokens, a negative checkpoint or eval interval, or a negative
relaunch count. An empty train split or an empty eval split is
`DatasetInvalid`, because the eval split measures the adapter.

`Train.settings job` returns every setting. `Train.totalSteps job` is the
optimiser steps the job runs: `maxSteps` when set, else whole epochs of the
train split, rounded up.

### The run's identity

`Train.jobJson job` is the job as canonical JSON with sorted keys. It holds
the settings that decide the adapter: the base, the serving model, the dataset
name and digest, the format and the training settings. It does not hold the
backend (the environment picks it), the work directory, the watch window or the
relaunch count.

`Train.runId job` is the job's name and the first twelve hex digits of the
SHA-256 of that JSON, for example `support-3f9a1c0b2d4e`. `Train.runDir job`
is `<workDir>/<runId>`. The same job is the same run, so starting it again
attaches to it and does not train twice. A changed setting is a new run.

The backend is not part of the identity. A job that finished on `Fake` is the
same run on `Mlx`. To train the same job on another backend, give it another
name or another work directory.

## Base models

A `BaseModel` is an id, the pinned weights each backend trains on, and the GGUF
model that serves the adapter.

```elm
myBase : Train.BaseModel
myBase =
    Train.baseModel "Qwen/Qwen2.5-0.5B-Instruct@7ae557604adf67be50417f59c2c2f167def9a775" Local.devModel
        |> Train.withWeights Train.Mlx
            { repo = "mlx-community/Qwen2.5-0.5B-Instruct-4bit"
            , revision = "a5339a4131f135d0fdc6a5c8b5bbed2753bbe0f3"
            , files = [ { path = "config.json", sha256 = "b045e57e…" }, … ]
            }
```

- The id is `<org>/<repo>@<commit>` of the original weights. It goes into the
  lineage and into the GGUF adapter.
- A `Snapshot` is a Hugging Face repository at one 40-digit commit, with the
  SHA-256 of every file the trainer reads. The runtime downloads each file at
  that commit, checks its digest and renames the directory into place when
  every file is there. A file that does not hash to its digest is never used
  (`ChecksumMismatch`).
- A 4-bit snapshot (an `mlx-community` `-4bit` repository, an `unsloth`
  `-bnb-4bit` repository) makes the job QLoRA. Mark it with
  `withQuantisedBase`.
- The serving model is a pinned GGUF (`Local.ModelRef`). Its digest is the
  adapter's base digest in the registry, and an engine checks it before it
  loads the adapter.
- `Fake` needs no weights.

`Train.devBase` is the development base: Qwen2.5 0.5B Instruct (Apache-2.0).
`Mlx` trains on the `mlx-community` 4-bit snapshot (QLoRA, about 280 MB),
`Unsloth` on the `unsloth` bnb 4-bit snapshot, and the adapter is served on
`Local.devModel`, the Q4_0 GGUF of the same model.

Any other base can come from a reference, with the model that serves it:

```elm
Train.baseFromReference
    "hf:Qwen/Qwen2.5-1.5B-Instruct@989aa7980e4cf806f80c7fef2b1adb7bc71aa306"
    servingModel
    |> Task.andThen Train.pinWeights
```

- `hf:<org>/<repo>@<commit>` trains on the repository's safetensors weights
  at that 40-digit commit. A branch or a tag can move, so it is refused. The
  commit pins every file: its tree names an LFS file by the SHA-256 of its
  content and any other file by its git blob id.
- `file:<directory>` trains on a local directory with safetensors weights,
  `config.json` and the tokenizer files.
- Anything else is `InvalidSetting` (setting `base`), before any I/O.
- `Train.pinWeights` lists the files. A Hub base reads its list at the commit
  once (other files are fetched and checked against their blob id) and keeps
  it under the AI home, so a host with fetching off pins it again. A local
  base hashes its directory, and its id gains the digest
  (`file:<dir>@<digest>`), so a job on changed weights is another run. A file
  that changes after that is `ChecksumMismatch`.
- A local base needs `pinWeights` before its job is written. A Vertex job
  needs it for any base, and refuses a local base (the trainer runs in the
  cloud). `provisionBase` lists a Hub base's files itself when it has to.
- Pickled weights (`.bin`, `.pt`) are never listed or loaded.

`baseId`, `baseServing` and `baseWeights backend base` read a base.
`Train.provisionBase backend base` downloads and verifies a backend's weights
and returns their directory (`""` for `Fake`). A base without weights for the
backend is `InvalidSetting`.

## Backends

```elm
type Backend
    = Mlx       -- MLX-LM on Apple silicon
    | Unsloth   -- Unsloth core on Linux x86_64 with an NVIDIA GPU (CUDA 13)
    | Vertex    -- the same driver in a container, as a Vertex AI custom job
    | Fake      -- the scripted trainer: tests, CI, a machine with no accelerator
```

The backend is resolved in this order:

1. `SKY_AI_TRAIN_BACKEND` (`mlx`, `unsloth`, `vertex` or `fake`), set by the operator.
   Under a project prefix the name is `<PREFIX>_AI_TRAIN_BACKEND`.
2. `Train.withBackend` in code.
3. Detection: `Mlx` on macOS arm64, `Unsloth` on Linux with an NVIDIA driver.
   Elsewhere the run fails with `NoAccelerator`, and the message names
   `SKY_AI_TRAIN_BACKEND=fake`.

Any other value of the variable is `InvalidSetting` naming the variable.
`sagemaker` is `InvalidSetting` too: the AWS backend is not supported yet.
`vertex` is never detected: a cloud job is chosen in code or by the variable.

A backend this host cannot run is `NoAccelerator`, never a silent fallback to
another one. `Mlx` needs Apple silicon. `Unsloth` needs Linux x86_64 with an
NVIDIA GPU and a driver for CUDA 13 (the PyPI torch wheels need driver 580 or
newer). `Fake` runs wherever a pinned CPython does: macOS arm64, Linux x86_64
and Linux arm64.

`SKY_AI_ACCEL=cpu` runs MLX on the CPU (a CI runner without Metal).

`Train.plan job` returns the resolved backend and where it came from: `env`,
`code` or `detected`. `Train.backendName` gives the name the variable uses.

The three backends share one driver. It has its own training loop, its own
data order, its own protocol and its own checkpoint format, so a run gives the
same files and the same lines on each:

- **Mlx** uses MLX-LM (MIT) as a library: `mlx_lm.load` reads the local base
  snapshot and `linear_to_lora_layers` adds the adapters. The optimiser is Adam.
  MLX-LM's own trainer saves no optimiser state, so the driver saves it with
  the adapter and a resume continues it.
- **Unsloth** uses Unsloth core (Apache-2.0): `FastLanguageModel` loads the
  base and `get_peft_model` adds the adapters. The optimiser is AdamW. The
  driver does not use TRL's `SFTTrainer`. Its checkpoints are safetensors,
  never `torch.save`. The AGPL Unsloth Studio is never installed.
- **Fake** is standard-library Python. It writes the whole protocol, real
  checkpoints and a real adapter whose B matrices are zero (so the adapter
  changes nothing). `fakeCrashAt` makes its first attempt die, as a killed
  trainer does.

## The managed toolchains

A training toolchain is a pinned uv, a pinned CPython and a virtual
environment installed from a hash lock. The system Python is never used.

| Toolchain | Host | Lock |
|---|---|---|
| `mlx` | macOS arm64 | `mlx-darwin-arm64.txt`: 34 packages (mlx 0.32.2, mlx-lm 0.31.3, transformers 5.17.0) |
| `unsloth` | Linux x86_64, CUDA 13 | `unsloth-linux-x86_64-cuda13.txt` (unsloth 2026.9.11, torch 2.12.1) |
| `fake` | macOS arm64, Linux x86_64, Linux arm64 | none: the standard library only |

The interpreter is CPython 3.12.14 (python-build-standalone 20260924,
`install_only_stripped`) and the installer is uv 0.12.16. Each archive is
pinned by SHA-256 in `runtime-go/rt/ai_pins.go`.

Install a toolchain ahead of time:

```bash
sky ai provision --train mlx              # or unsloth, or fake
sky ai provision --train mlx --base dev   # also the dev base's weights
```

or from code with `Train.provision Mlx` (and `Train.provisionBase Mlx
Train.devBase`). Both are idempotent: an installed toolchain returns at once.
`Train.toolchainStatus ()` lists every toolchain this host can run, installed
or not, with its id, its python and its package versions.

### Layout

Everything goes under the AI home (`SKY_AI_HOME`, else `$SKY_HOME/ai`, else the
user cache directory, for example `~/Library/Caches/sky/ai` on macOS):

```
tools/uv/<version>/uv
python/<tag>-<target>/bin/python3
toolchains/<name>/<id>/venv/        the locked packages
toolchains/<name>/<id>/driver/      the Sky training driver
toolchains/<name>/<id>/sbom.json    every package with its version and hashes
toolchains/<name>/<id>/ok.json      written last: the toolchain is complete
bases/<org>--<repo>@<revision>/     a pinned base snapshot
cache/uv/                           uv's wheel cache
```

The toolchain id is the first 16 hex digits of a SHA-256 over every pinned
input: the lock, the CPython archive, the uv archive and the driver's files. A
changed pin or a changed driver installs a new toolchain beside the old one. It
never changes an installed one.

### How a toolchain is installed

1. uv and CPython are downloaded, checked against their digests, unpacked in a
   staging directory, run once to check their version, and renamed into place.
2. `uv venv --relocatable` makes the environment in a staging directory.
3. `uv pip sync --require-hashes --only-binary :all: --no-config` installs the
   lock from PyPI. No wheel installs unless its hash is in the lock, and no
   source build runs. The index is trusted for availability only, never for
   content.
4. The installed versions are read back from the environment's metadata and
   must equal the lock.
5. The driver is copied in and runs its self-check, which must print a `hello`
   line with the versions it imports.
6. `sbom.json` and then `ok.json` are written, and the directory is renamed
   into place.

The install runs under a lock file, so two programs that provision at once
take turns, and the second finds the first's toolchain. uv runs with a cleared
environment: the engine's allow-list (`PATH`, `HOME`, `TMPDIR`, the locale, the
library paths, the GPU device selectors) plus the proxy and certificate
variables. A `PIP_*` or `UV_*` setting of yours, a token or a key never reaches
it.

`scripts/ai/lock-toolchains.sh` regenerates the locks with `uv pip compile
--generate-hashes --only-binary :all:` for one platform and Python 3.12, with
a fixed publication cut-off, so running it again gives the same lock. A lock
change is a reviewed edit.

### Fetching

With fetching allowed (`SKY_AI_FETCH=on`, the default in development), the
workflow installs a missing toolchain and downloads a missing base. With
fetching off (the default in production), a missing toolchain or base is
`NotProvisioned`, and the failure names the command to run:
`sky ai provision --train <backend>`, or
`sky ai provision --train mlx --base dev` for the weights. Provision a
production host or an image ahead of time.

## Plan before you train

```elm
Train.plan job : Task Error Train.Plan
```

`plan` checks the job, resolves the backend, reads the host and the serving
model's shape (fetching the model when allowed) and estimates the memory. It
returns the run id, the backend and its source, the host, the shape, the steps,
the row counts, the estimate and the memory the backend may use. A job that
will not fit is `ModelTooLarge` before anything is downloaded or started. A
backend this host cannot run is `NoAccelerator`. `Fake` is never too large.

The workflow runs the same check as its `plan` step, after the toolchain and
the base weights are in place and before the trainer starts: a job that will
not fit ends as `ModelTooLarge` without launching a trainer. Call `plan`
yourself to learn this before anything is downloaded.

`Train.estimateMemory shape job` is the estimate, in bytes. It is a documented
formula, deliberately on the high side, not a measurement:

- weights: parameters × 0.5625 bytes for a 4-bit base (4.5 bits with the
  scales), × 2 for a 16-bit one
- the adapter: its parameters (rank × (in + out) for each adapted projection
  in each adapted layer) × 16 bytes (the value, its gradient and Adam's two
  moments, as f32)
- activations: rows × tokens × adapted layers × (16 × hidden + 4 ×
  intermediate) × 2 bytes
- the logits: rows × tokens × vocabulary × 8 bytes (values and gradient)
- 512 MiB for the runtime

Rows is `perDevice`. The memory the backend may use is three quarters of the
total memory on Apple silicon (Metal works within about that much of the
shared memory), and the memory available now elsewhere. `Train.host ()` reads
the machine (`accelerator` is `metal`, `cuda` or `cpu`), and
`Train.shapeOf base` reads the serving model's shape.

## The durable workflow

`Train.workflow name job` is a `Std.Durable` workflow. Each line is a
journalled step:

| Step | What it does |
|---|---|
| `resolve` | the backend (env > code > detection) |
| `stage` | the version's Train and Eval rows into the run directory |
| `toolchain` | the managed toolchain, installed if allowed |
| `base` | the backend's base weights, downloaded and verified |
| `shape` | the serving model's shape (fetched if allowed) |
| `origin` | the run directory's last attempt, if any |
| `launch-<a>` | the trainer, detached (attaches when it already runs) |
| `watch-<a>-<n>`, `tick-<a>-<n>` | watch up to the window, then a durable one-second sleep |
| `resume-<a>`, `launch-<a>` | a dead trainer relaunched from its newest checkpoint |
| `collect` | the PEFT adapter's digest, checked against the trainer's |
| `convert` | the GGUF adapter (`withGgufExport`) |
| `merge` | the merged GGUF model (`withMergedExport`) |
| `register` | the registry version (`withRegistry`) |

The run ends with its result in `<run>/result.json`: the adapter, or the typed
failure. `progress`, `result` and `drive` read it.

| Function | Use |
|---|---|
| `start db job` | create the run and return its id. Idempotent: a run that exists is left alone. It checks the job and resolves the backend first, so a bad setting or a backend this host lacks fails at once and starts no run |
| `poll db jobs` | advance the runs of these jobs that are due (one worker tick). `True` when it did work |
| `drive db job` | train in the foreground: `start`, then advance until the run ends, and return the adapter or the run's typed failure. A finished run returns its adapter at once |
| `workflows jobs` | the workflows of several jobs, for a worker's own `Durable.poll` list |
| `workflow name job` | the workflow under your own name |

`drive` needs a database for the durable journal (`Std.Db`, SQLite or
PostgreSQL). `sky ai train` uses `.sky/ai/train.db` (SQLite) unless `--db`
names another.

### The trainer runs detached

The trainer is not a `Process.spawn` child, because such a child dies with the
program, and a training run must outlive the program that started it. It
starts in a session of its own (on Unix), with its standard output going to
`<run>/events.jsonl` and its standard error to `<run>/stderr.log`. The workflow
reads the events file from an offset. So:

- A Ctrl-C in `sky ai train` reaches the watcher, not the trainer. The trainer
  keeps training.
- A program that restarts, or a worker that takes over the run, attaches to the
  live trainer instead of starting a second one.
- The trainer's identity is a launch token on its command line, recorded in
  `trainer.json`. A process that later gets the trainer's pid is never
  attached to and never signalled.

### Watching, and the lease

Each watch is a journalled step that waits up to the watch window for the
attempt to end (a terminal protocol line, or the trainer gone). Between two
watches, a one-second durable sleep releases the run. A watch lasts at most
five minutes, so every durable step ends well inside the 30-minute lease, and
a long run never holds a worker past it. `withWatchWindow` sets a shorter
window (a web app that polls each second uses 2000).

### Relaunch from the newest checkpoint

When a watch finds the trainer gone without a terminal line (a SIGKILL, an
out-of-memory kill, a power cut), the workflow relaunches it. The next attempt
starts from the newest checkpoint whose files match its manifest, so it repeats
at most `withCheckpointEvery` steps. After `withMaxRelaunches` relaunches the
run fails with `TrainingFailed`. With the default of 3, a run is launched at
most four times.

The data order depends only on the seed and the step: each epoch is a seeded
permutation of the train rows, and step s reads the rows at positions
s × batch to (s + 1) × batch of that order. So a resumed attempt reads exactly
the batches the first attempt would have read. The checkpoint holds the
optimiser state too, so the resumed attempt continues Adam where it stopped
([Measured on a Mac](#measured-on-a-mac)).

A job checks the durable run it advances: a worker whose job differs from the
job that started the run refuses it with `InvalidSetting` (a new job is a new
run).

## The run directory

`<workDir>/<runId>/` is the source of truth for a run. `progress`, `cancel`,
`result` and the CLI read it, so they need no database.

```
job.json              the job as Train.jobJson encodes it
stage.json            the staged version's digest and row counts
data/train.jsonl      the frozen version's Train rows
data/valid.jsonl      its Eval rows
driver.json           the configuration of the attempt the driver runs
trainer.json          pid, launch token, attempt, resumeFrom, start time, toolchain
events.jsonl          the driver's protocol lines (its standard output)
stderr.log            the driver's standard error
checkpoints/<step>/   adapter.safetensors, optimizer.safetensors, state.json, manifest.json
adapter/              the PEFT adapter (adapter_model.safetensors, adapter_config.json)
adapter.gguf          the GGUF adapter (withGgufExport)
merged.gguf           the merged model (withMergedExport)
cancel                present once a cancel was asked for
generation            how often the run was resumed
result.json           the finished run's record
cli.json              the sky ai train arguments that made the run (CLI runs only)
```

Staging hashes the version's data file first. A file whose bytes changed after
it was frozen is `ChecksumMismatch`. Each staged row is the row's own JSON (the
chat or instruction format). Staging the same version again writes nothing.

## Checkpoints

A checkpoint is `checkpoints/<step>/`, the step written with seven digits
(`checkpoints/0000040/`):

| File | Holds |
|---|---|
| `adapter.safetensors` | the adapter's weights |
| `optimizer.safetensors` | the optimiser state |
| `state.json` | the step, the loss, the attempt |
| `manifest.json` | the step and the SHA-256 of every other file |

The driver writes a checkpoint into a temporary directory, flushes every file
to disk, writes the manifest last and renames the directory into place. Only
then does it write the `checkpoint` line. A reader that sees the line sees a
complete checkpoint.

Before a resume, the runtime hashes every checkpoint's files against its
manifest. A checkpoint that does not match (a torn write, a disk error) is
skipped, and the resume falls back to the one before it. The driver checks the
manifest again before it loads the checkpoint.

Checkpoints hold safetensors and JSON only, never a pickle.

## Cancel and resume

```elm
Train.cancel job : Task Error Bool
Train.resume job : Task Error Int
```

`cancel` writes the `cancel` file, then sends SIGTERM to the trainer's process
group (only when the process is still the trainer). The driver stops at the
next step boundary, writes a checkpoint and a `cancelled` line, and exits. A
trainer that is still running after 60 seconds gets SIGKILL. `cancel` returns
`True` when it signalled a live trainer. The run ends with
`TrainingCancelled` the next time it is advanced (`poll` or `drive`). A cancel
of a directory with no run is `NotRegistered`.

Off Unix there is no SIGTERM: the driver reads the `cancel` file at every step,
and the kill after the grace period is the hard stop.

`resume` starts a failed or cancelled run again. It counts a new generation,
removes `result.json` and the `cancel` file, and returns the generation. Each
generation is its own durable run, so the next `start` or `drive` takes the run
up and relaunches the trainer from its newest verified checkpoint. A run whose
trainer still runs is not resumed (`InvalidSetting`: cancel it first, or wait).

`cancelAt dir`, `resumeAt dir`, `progressAt dir` and `exportAt dir merged` do
the same for a run directory, without the job. The CLI uses them.

## Progress

```elm
Train.progress job : Task Error Train.Progress
Train.result job : Task Error (Maybe Train.Adapter)
Train.runs workDir : Task Error (List Train.Progress)
```

`progress` folds the run's protocol lines into a `Progress`: the status, the
backend, the step of the total, the latest loss and eval loss, the tokens per
second, the attempt, the relaunches and the step the last one resumed from,
the last checkpoint, the loss curves (`losses` and `valLosses`, at most 200
points each, thinned evenly) and a message. A line it cannot decode is noted
in the message and skipped, so progress never fails on one bad line.

The status is one of `Queued` (no run directory yet), `Provisioning` (the data
is staged and the toolchain, the base and the plan are being prepared, before
the first trainer starts), `Running`, `Relaunching` (the trainer is gone and no
result is written yet), `Converting` (the trainer finished and the collect and
convert steps run), `Registering` (the GGUF adapter is written and the merge
and register steps run), `Done`, `Failed` and `Cancelled`.
`statusText` gives the lower-case name.

`result` is `Nothing` while the run has not finished, the adapter when it has,
and the run's typed failure when it failed. `runs workDir` lists every run in a
work directory, newest first.

`Status(..)` exposes `Done`, and `Sky.Core.Task` exposes `Step(..)` with a
`Done` too. Do not expose both unqualified in one module.

### In the Sky Console

The console's **AI** tab has a Training section. It lists the runs this process
touched and the runs under `.sky/ai/runs`, each with its status, its step of
the total, its losses, its relaunches, its last checkpoint, the version it
registered, the error of a failed or cancelled run, and a small loss curve.
A row holds no dataset text, no prompt and no secret
([`../observability.md`](../observability.md)).

## The driver protocol

The trainer writes one JSON object per line to `events.jsonl`. Every line has
`"v": 1` and a `type`. The driver writes the keys sorted, and writes a
non-finite number as `null` (which Sky reads as `-1`).

| `type` | Fields | Meaning |
|---|---|---|
| `hello` | `backend`, `toolchain`, `versions`, `attempt`, `resumeFrom`, `totalSteps`, `pid` | an attempt starts |
| `progress` | `step`, `total`, `loss`, `learningRate`, `tokensPerSecond`, `epoch` | one optimiser step is done |
| `metric` | `step`, `name`, `value` | a measurement. `name` is `val_loss` for the eval split's loss |
| `checkpoint` | `step`, `path` | a complete checkpoint is on disk |
| `artefact` | `kind`, `path`, `sha256` | a file the run produced. `kind` is `peft` for the adapter |
| `cancelled` | `step` | the attempt stopped after a cancel (terminal) |
| `done` | `step`, `trainLoss`, `valLoss` | the attempt finished (terminal) |
| `error` | `code`, `message` | the attempt failed (terminal) |

The `code` of an `error` line is a `Std.Ai.Failure` name: `DatasetInvalid`,
`NoAccelerator`, `ToolchainFailed`, `ModelTooLarge` (the trainer ran out of
memory) or `TrainingFailed`. Any other code is read as `TrainingFailed`.

`Train.decodeEvent line` decodes one line into an `Event` (`Hello`,
`StepProgress`, `Metric`, `Checkpoint`, `Artefact`, `DoneEvent`,
`CancelledEvent`, `DriverError`). A line of a type this release does not know
is `Ok Nothing` and is skipped, so a newer driver may add types. A line that is
not protocol JSON, a line without its type's fields, and a line whose `v` is
not 1 are `ToolchainFailed` (the toolchain and the runtime differ).
`Train.foldProgress event progress` folds one event into a `Progress`.

A watch reads complete lines only. A line still being written (no newline yet)
is left for the next watch.

The driver claims standard output for protocol lines. Anything else a library
prints to standard output (a `print`, a C extension's message) goes to
standard error, so the events file holds protocol lines only.

## GGUF and merged export

The training output is a PEFT adapter (`adapter/adapter_model.safetensors` and
`adapter_config.json`). The managed `llama-server` loads GGUF, so
`withGgufExport` converts it to `adapter.gguf`.

The converter is Go, in the runtime. It needs no Python and no PyTorch. It
writes the layout llama.cpp's own LoRA converter writes: one pair of F32
matrices for each adapted projection (`blk.<n>.<tensor>.weight.lora_a` and
`.lora_b`), F32 because an adapter is small and F32 loses nothing. The header
has these keys:

| Key | Value |
|---|---|
| `general.architecture` | the serving model's (`qwen2`, `llama`, …) |
| `general.type` | `adapter` |
| `adapter.type` | `lora` |
| `adapter.lora.alpha` | the adapter's alpha |
| `sky.adapter.base_id` | the base the adapter was trained on (`<org>/<repo>@<commit>`) |
| `sky.adapter.serving_sha256` | the serving GGUF's digest |

Every matrix is checked against the serving model's shape. An adapter that
does not fit (another layer count, another projection size) is
`AdapterMismatch` before an engine sees it. The written file is read back and
checked. Converting the same adapter again gives the same bytes.

`withMergedExport` also folds the adapter into the serving model's weights with
`llama-export-lora` from the pinned engine build and writes `merged.gguf`, a
model an engine serves with no adapter. With your own engine build
(`SKY_AI_LLAMA_BIN`), `llama-export-lora` must be next to `llama-server`, else
the merge is `NotProvisioned`. The adapter stays the product: the registry
holds adapters only.

`Train.exportAt dir merged` converts the finished adapter in a run directory,
whether or not the job asked for it, and with `merged` also writes the merged
model. The serving model and the base come from the run's `job.json`.
`sky ai export <run>` calls it.

## Registry and serving

`withRegistry reg` adds the `register` step. It calls `Registry.register` with
the job's name, the GGUF adapter when there is one (else the PEFT
safetensors), and the adapter's lineage:

- `base`: the serving model (its digest is what an engine checks)
- `dataset`: `<name>@<first 12 digits of the digest>`, and `datasetDigest`
- `metadata`: `runId`, `baseId`, `backend`, `toolchain`, `seed`, `steps`,
  `trainLoss`, `valLoss` and `job` (the job's JSON)

The method is `Qlora` for a job with `withQuantisedBase`, else `Lora`.
`Train.registryAdapter adapter` and `Train.lineage adapter` build the same
values for your own `Registry.register` call. The adapter's `registered` field
holds the label (`support@v1`). Registering the same bytes again returns the
same version, so a resumed workflow does not create a second one.
`Registry.trainedOn reg digest` then finds every version trained on a dataset.

Serve a registered adapter through a registry alias
([`registry.md`](registry.md#serving-follows-the-aliases)), or serve the file
directly with `Local.withAdapter`:

```elm
supportSpec : Train.Adapter -> Local.Spec
supportSpec adapter =
    case Train.adapterRef adapter of
        Just ref ->
            Local.model Local.devModel |> Local.withAdapter ref

        Nothing ->
            Local.model Local.devModel
```

`Train.adapterRef` is the GGUF adapter as a pinned file
(`Local.file { path, sha256 }`), `Nothing` for a job without
`withGgufExport`. The engine hashes the file before each load. An adapter that
`Std.Ai.Train` converted records the model it was converted for, and loading it
on another model is `AdapterMismatch`. `sky ai start <slot> --adapter
file:<path>#sha256=<hex>` does the same from the command line.

## The adapter record

`drive` and `result` return an `Adapter`:

| Field | Holds |
|---|---|
| `name`, `runId` | the job's name and the run |
| `baseId`, `serving` | the base's id and the serving model reference |
| `dataset`, `datasetDigest` | the frozen version |
| `backend`, `toolchain` | the backend, and `<name>@<id>` of the toolchain |
| `method` | `lora` or `qlora` |
| `seed`, `steps` | the seed and the steps trained |
| `peft` | the PEFT adapter's path and SHA-256 |
| `gguf`, `merged` | the GGUF adapter and the merged model, when asked for |
| `trainLoss`, `valLoss` | the last step's loss and the eval split's loss |
| `relaunches`, `resumedFrom` | the relaunches and the checkpoint step the last one resumed from |
| `registered` | the registry label, with `withRegistry` |
| `job` | the job's JSON |

`Train.adapterCodec` is its JSON form (the `adapter` field of `result.json`).

## Tests

Under `sky test` in test mode (a project that commits a `.env.test`,
[`../tooling/testing.md`](../tooling/testing.md)) no trainer process starts and
no network is used. The durable workflow runs for real on the test database,
and the launch step writes what a Fake trainer writes: the protocol lines,
verified checkpoints and a real, no-op PEFT adapter. The toolchain, the base
and the model shape need no download. The GGUF conversion runs. The merge
writes a model-shaped stand-in. So a program's own tests run the whole
workflow, including the registry, in milliseconds.

Put `SKY_AI_TRAIN_BACKEND=fake` in `.env.test`, so the test does not depend on
the host (on a Linux runner without a GPU, detection is `NoAccelerator`).

`fakeCrashAt n` makes the first attempt stop after step n with no terminal
line, as a killed trainer does, so a test sees the relaunch from the newest
checkpoint:

```elm
crashy : Train.Job -> Train.Job
crashy job =
    job
        |> Train.withMaxSteps 12
        |> Train.withCheckpointEvery 2
        |> Train.fakeCrashAt 3
```

A run of `crashy job` finishes at step 12 with `relaunches = 1` and
`resumedFrom = 2` (the newest checkpoint before step 3).
`examples/73-ai-train/tests/TrainExampleTest.sky` asserts this, then serves
the registered adapter through a slot answered by a fixture under
`tests/mocks/`.

Outside test mode, `Fake` runs the real driver under the managed CPython
(`sky ai provision --train fake`). It needs the serving model for its shape.

## Failures

Every failure is an `Error` with a typed `Std.Ai.Failure`
([`local.md`](local.md#failures)). Read it with `Failure.fromError`, and keep a
`_ ->` branch:

```elm
explain : Error -> String
explain err =
    case Failure.fromError err of
        Just (TrainingCancelled c) ->
            "cancelled at step " ++ String.fromInt c.step

        Just (TrainingFailed f) ->
            "failed at step " ++ String.fromInt f.step ++ ": " ++ f.reason

        Just (NotProvisioned p) ->
            "run " ++ p.command

        _ ->
            Error.toString err
```

| Failure | When | `ErrorKind` |
|---|---|---|
| `TrainingFailed { runId, step, reason, logTail }` | the trainer reported an error, or died more often than `withMaxRelaunches` allows, or a run file cannot be written | `Unavailable` (not retryable) |
| `TrainingCancelled { runId, step }` | the run was cancelled | `Conflict` |
| `ModelTooLarge` | `plan` finds the job will not fit, or the trainer ran out of memory | `InvalidInput` |
| `NoAccelerator` | the backend cannot run on this host | `Unavailable` |
| `DatasetInvalid` | an empty train or eval split, a data file that is not frozen rows | `InvalidInput` |
| `NotProvisioned` | a toolchain, a base or `llama-export-lora` is missing and fetching is off. It names the command | `NotFound` |
| `ToolchainFailed` | an install step, the self-check, the trainer's start, a protocol line, or `llama-export-lora` failed | `Unavailable` |
| `ChecksumMismatch` | a dataset file changed after it was frozen, a download, or the adapter's digest differs from the trainer's | `Conflict` |
| `AdapterMismatch` | the adapter does not fit the serving model, or an engine runs another model | `InvalidInput` |
| `InvalidSetting` | a setting out of range, a bad `SKY_AI_TRAIN_BACKEND`, `vertex` or `sagemaker`, a resume of a running trainer, a worker whose job differs | `InvalidInput` |
| `NotRegistered` | a cancel or resume of a directory with no run | `NotFound` |

## Security

- **No pickle.** Checkpoints and adapters are safetensors and JSON. The driver
  never calls `torch.save` or `torch.load`, and the Go converter reads
  safetensors with a bounded header and checks every offset.
- **No remote code.** The driver never passes `trust_remote_code`, so the
  libraries keep their default, off.
- **An offline trainer.** The trainer reads the base from a local directory
  and runs with `HF_HUB_OFFLINE=1`, `TRANSFORMERS_OFFLINE=1` and
  `HF_DATASETS_OFFLINE=1`. It runs as `python -I -B` (isolated: no user site,
  no environment variables of Python's, no bytecode written) in a cleared
  environment, so a token, a key or a pip setting of yours never reaches it.
- **Hash locks.** Every wheel is pinned by SHA-256 and installed from wheels
  only. A Go test checks the lock policy: every line hashed, no URL, VCS or
  editable requirement, and the AGPL Unsloth Studio is refused.
- **Pinned downloads.** uv, CPython, the base snapshots, the engine and the
  serving model are pinned by SHA-256. A mismatch installs nothing and names
  both digests.
- **No dataset text in the console.** A protocol line holds numbers, a
  checkpoint path and an error code with the driver's message.

## The CLI

```bash
sky ai provision --train mlx|unsloth|fake [--base <base>]
sky ai train --data <dir>/<name>[@<digest>] [--base <base> --serving <model>]
             [--name <n>]
             [--steps <n>] [--rank <r>] [--seed <s>] [--layers <n>]
             [--checkpoint-every <n>] [--eval-every <n>]
             [--backend mlx|unsloth|fake] [--gguf] [--merged] [--register]
             [--detach] [--work-dir <dir>] [--db <dsn>]
sky ai train status [<run>]
sky ai train runs
sky ai train resume <run>
sky ai train cancel <run>
sky ai export <run> [--out <file>] [--merged]
sky ai start <slot> --adapter file:<path>#sha256=<hex>
```

Every verb takes `--format json`. The verbs run the bundled Sky program
(`sky-bundled/ai-train`, built the first time a training verb runs, apart from the engine and registry program `sky-bundled/ai`), which calls the API above.

- `--base` is `dev` (the development base, the default),
  `hf:<org>/<repo>@<commit>` or `file:<directory>`, checked by
  `Train.baseFromReference`. A malformed base is its `InvalidSetting`, before
  anything trains. A base other than `dev` needs `--serving <model reference>`
  (`hf:<org>/<repo>/<file>#sha256=<hex>` or `file:<path>#sha256=<hex>`), the
  GGUF model that serves the adapter. The CLI pins the weights
  (`Train.pinWeights`) unless `--backend fake`, which reads no weights; a
  local base is always pinned.
- `provision --train` is `Train.provision`, and `--base` adds
  `Train.provisionBase` for that base.
- `train` builds a job on the version `--data` names (the newest when no digest
  is given): `Train.lora` when the version's rows have `messages`, else
  `Train.loraOnInstructions`. The base is `--base` (`Train.devBase` by
  default). The job is
  marked QLoRA, the name is the dataset's name unless `--name` says otherwise,
  and the work directory is `.sky/ai/runs` unless `--work-dir` says otherwise.
  It runs `Train.drive` on `.sky/ai/train.db` (or `--db`). `--register` opens
  the registry as the registry verbs do (`--db`, `--registry`).
- `--detach` starts the run, advances it until the trainer runs, and returns.
  `sky ai train status <run>` follows it, and the same command again (or
  `train resume <run>`) collects the adapter.
- `train status <run>` prints one run's progress, and `train status` or
  `train runs` lists every run in the work directory. A `<run>` is a run id or
  a path with a `/`.
- `train resume <run>` is `Train.resumeAt`, then the run's original command
  again (from its `cli.json`), in the foreground unless you add `--detach`. A
  run that `sky ai train` did not start has no `cli.json`: resume it from the
  program that started it, with `Train.resume`.
- `train cancel <run>` is `Train.cancelAt`.
- `export <run>` is `Train.exportAt`. `--out` copies the file there.

## Measured on a Mac

On an Apple M-series Mac with 16 GB, a direct MLX run of the driver on the
development base (12 steps, 2 rows of 256 tokens, rank 4 on 4 layers, the
4-bit base) took 8.5 s wall time with a peak memory footprint of 2.24 GB. Its
eval loss went from 2.83 at step 6 to 2.57 at step 12. A resume from the
step-8 checkpoint gave bit-identical losses for steps 9 to 12 and an adapter
with the same SHA-256, because the optimiser state is restored.

The same job driven from Sky (`ai_train_mlx_live.rs`, with the trainer's
process group killed by SIGKILL after its step-4 checkpoint) relaunched once
from step 4, finished at step 12 with an eval loss of 2.40, and its GGUF
adapter answered a chat on the managed engine.

The Go converter's output loads in the pinned `llama-server` b11200 (its
`/lora-adapters` route lists it), and `llama-export-lora` merges it into the
Q4_0 dev model.

The live tests are `rust/crates/sky/tests/ai_train_live.rs` (Fake under the
managed CPython, a crash and a relaunch, the real engine) and
`ai_train_mlx_live.rs` (a QLoRA run on Apple silicon whose trainer is killed
after its first checkpoint and resumed). Without their toolchain they fail and
name the `sky ai provision --train` command, unless `SKY_LIVE_TESTS=skip`.

## Training on Vertex AI

The `Vertex` backend runs the same driver in a container, as a Vertex AI
custom job on Google Cloud, and returns the same adapter a local run gives.
On a machine with GPUs (`g2-standard-4` carries one NVIDIA L4) the container
runs the Unsloth driver; on a CPU machine (`n2-standard-4`) it runs the Fake
driver, which tests the whole path for cents.

```elm
supportJob : Train.Job
supportJob =
    Train.lora Train.devBase version
        |> Train.withQuantisedBase
        |> Train.withBackend Train.Vertex
        |> Train.withCloud
               { project = "", region = "us-central1", machine = "g2-standard-4", staging = "" }
        |> Train.withBudget (Money.fromMajor Money.USD 2)
        |> Train.withMachinePrices prices
        |> Train.withMaxRuntimeMinutes 40
```

| Builder | Variable | Notes |
|---|---|---|
| `withCloud` | `SKY_AI_TRAIN_PROJECT`, `_REGION`, `_MACHINE`, `_STAGING` | the staging location is `gs://<bucket>/<prefix>`, in the job's region; machine default `g2-standard-4` |
| `withBudget` | `SKY_AI_TRAIN_BUDGET` (`USD 2.00`) | required |
| `withImage` | `SKY_AI_TRAIN_IMAGE` | the trainer image, pinned by digest (`sky ai image build --train`) |
| `withServiceAccount` | `SKY_AI_TRAIN_SERVICE_ACCOUNT` | what the job runs as |
| `withLabels` | `SKY_AI_TRAIN_LABELS` (`team=support,env=qa`) | added to Sky's own labels |
| `withMachinePrices` | none (code only) | `machinePrices currency version rates` |
| `withMaxRuntimeMinutes` | none | a ceiling below the budget's |
| `withSpot` | none | Spot capacity: a pre-empted attempt is relaunched |
| `withKeepStaging` | none | keep the run's objects and job records |

The image and the one-time project setup are in
[deploy-gcp.md](deploy-gcp.md). `Train.cloudSettings` resolves and checks the
settings without touching the cloud; `Train.start` runs that check first, so a
job with no project, no budget or an image without a digest starts no run.

### The price book and the budget

The stdlib ships no prices. A job states the ones it trusts:

```elm
prices : Train.MachinePriceBook
prices =
    Train.machinePrices Money.USD "2026-10-12"
        [ { machine = "g2-standard-4", region = "us-central1", perHour = "0.95" } ]
```

A rate is the machine's whole hourly price (cores, memory, its GPUs, the
management fee and the boot disk). `sky ai cloud prices` reads the Cloud
Billing catalogue (see [deploy-gcp.md](deploy-gcp.md#what-it-costs)). A
machine and region that are not in the book are `InvalidSetting`: with no
price there is no estimate.

`Train.plan` (and the workflow's `cloud-plan` step) estimates the job, pure:
15 minutes to provision and pull the image, the steps, and 5 minutes to export
and upload, at the book's rate. A step is the job's `fakeStepMs` plus 50 ms on
a CPU machine, and on an L4 the larger of 250 ms and parameters / 10⁹ ×
tokens per step / 4000 ms (`Train.cloudEstimate`). An estimate above the
budget is `BudgetExceeded`, and nothing launches. The plan also reads the
staging bucket's location (a bucket outside the job's region, or a
multi-region one, is `InvalidSetting`: the data stays in the region) and the
L4 training quota through Cloud Quotas (`QuotaExceeded` when it is below the
machine's GPUs).

The budget bounds the run three ways:

1. The job is created with a hard runtime ceiling, `scheduling.timeout`, the
   smaller of what the budget buys at the rate and `withMaxRuntimeMinutes`.
   Vertex stops the job even when no Sky process is left.
2. Each watch measures the spend: the time each attempt has run (from its
   start, or from its creation while it waits), at the rate, plus the earlier
   attempts'. At the budget the watch cancels the job and the run ends
   `BudgetExceeded`.
3. A relaunch needs at least its provisioning allowance of budget left, and
   gets only what is left as its ceiling.

### The workflow

`resolve`, `cloud` (the settings), `stage` (the frozen rows into the run
directory), `shape`, `cloud-plan`, `upload`, `origin`, `resume-<a>`,
`launch-<a>`, `watch-<a>-<n>` with `tick-<a>-<n>`, `download`, then the steps
a local run has: `collect`, `convert`, `merge`, `register`, and `cleanup`.

* **upload** stores the train and eval rows as content-addressed blobs in the
  staging bucket and writes `runs/<key>/inputs.json` once: the rows' digests,
  the driver's settings and the base's pinned snapshot. `<key>` is
  `Train.runKey (Train.runId job)`, `sky-` and 20 hex digits: the same job is
  the same key, so a resumed run finds its checkpoints.
* **launch** is exactly once. It first writes `runs/<key>/launch-<a>.intent`
  with a create-only precondition. Whoever writes it creates the custom job,
  labelled `sky-managed=true`, `sky-run=<key>`, `sky-attempt=<a>` and
  `sky-kind=train`. A launch that finds the intent already there (another
  worker, or this workflow resumed after a crash between the create and its
  journal row) attaches to the job its labels name, or to the one its
  container named in `attempt-<a>/hello.json`, and never creates a second
  paid job. An intent with no job after ten minutes is `TrainingFailed`.
* **watch** is one step that holds no process: the job's state, the protocol
  segments the container wrote since the last watch
  (`attempt-<a>/events/<seq>.jsonl`), and the spend. A durable sleep of at
  most 30 seconds (`withWatchWindow`) follows. The segments are mirrored into
  the run directory's `events.jsonl` and the job's state into `cloud.json`,
  so `Train.progress`, `Train.cloudStatus`, `sky ai train status` and the
  console show a cloud run as they show a local one.
* A `done` line ends the watch; an `error` line is the driver's typed failure;
  a cancelled job is `TrainingCancelled`; a stockout is
  `CapacityUnavailable`. A job that ended with no terminal line (a crash, a
  Spot pre-emption) is relaunched from the newest checkpoint whose manifest is
  in the bucket (the container checks every file's digest when it resumes), up
  to `withMaxRelaunches`, then `TrainingFailed` with the job's last log lines.
* **download** fetches the adapter files, each checked against the adapter's
  manifest, into the run directory's `adapter/`.
* **cleanup**, after the adapter is registered, deletes the run's job records
  and its staging prefix, unless `withKeepStaging`.

The container's shim (`runtime-go/rt/ai_train_assets/driver/sky_cloud.py`)
downloads the run's inputs and the base (each file checked against its
digest), runs the driver, and uploads each checkpoint before the protocol
line that names it, the adapter before `artefact` and `done`, and every
segment with a create-only precondition. It reads the `cancel` marker each
cycle and forwards Vertex's SIGTERM, so the driver checkpoints and stops.

### Cancel

`Train.cancel job` (and `sky ai train cancel <run>`) writes the run's `cancel`
marker and asks Vertex to cancel the job. The run ends `TrainingCancelled`.

### Offline

Under `sky test`, `SKY_AI_GCP_ENDPOINT=http://sky-gcp.invalid` in the
project's `.env.test` sends every Google Cloud call to the runtime's built-in
emulator, which runs the Fake protocol into an emulated bucket the way the
container does. `examples/74-ai-cloud` and
`rust/crates/sky/tests/fixtures/ai-cloud` test the whole workflow this way.

## In `Std.Ai.Trace`

A run records itself in `Std.Ai.Trace` under its id (`Train.runId job`, the
id `start` returns). Each step the watch reads is a progress row (the step,
the total, the loss, the tokens per second, the checkpoint written at it):
`Trace.trainingSteps db runId`. When the run ends, finished or failed, it records
one `Training` cost row. A Vertex run's row is its measured spend in exact
money, so `Trace.costs db` adds training to the calls and the evals of the
lifecycle. A run on this machine has no price, so its row is unpriced (no
cost, never a zero). Every row is keyed, so a resumed run writes nothing
twice. See [`trace.md`](trace.md).

## Not yet

The AWS backend (`SageMaker`) is not supported yet: `sagemaker` is
`InvalidSetting`.
