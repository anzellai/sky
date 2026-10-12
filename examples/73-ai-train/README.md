# 73 · Train an adapter

`Std.Ai.Train` in one web app: freeze a support dataset, fine-tune a QLoRA
adapter on it while the page shows the steps and the loss curve, register the
adapter with its lineage, point `dev` at it, and ask it questions through the
local engine.

```bash
sky run src/Main.sky     # then open http://localhost:8073
sky test                 # the whole lifecycle offline (test mode, the Fake trainer)
```

## What the app does

1. At start it freezes `data/support.jsonl` (40 chat rows) into a dataset
   version under `.sky/ai/datasets/support`: 80 % of the rows train the
   adapter and 20 % measure it.
2. **Train** starts the durable training run (`Train.start`). Each second the
   page advances it (`Train.poll`) and reads its progress from the run
   directory (`Train.progress`): the status, the step of the total, the train
   and eval loss, the last checkpoint, the relaunches and the loss curve.
3. When the run is done, the adapter is converted to GGUF and registered as
   `support` with its lineage. The page points `dev` at the new version.
4. **Ask** sends a question to the slot that serves `support@dev`: the
   development model on the managed local engine, with the adapter loaded
   after its digest and its base are checked.

The job (in `src/Support.sky`) is QLoRA on `Train.devBase` (Qwen2.5 0.5B
Instruct, 4-bit): rank 8 on the attention projections of the last 8 layers,
60 steps of 2 rows of up to 512 tokens, seed 7, a checkpoint and an eval every
20 steps. The backend is not in the code.

The app's database (`train.db`, SQLite) holds the durable runs and the
registry's records. The runs are under `.sky/ai/runs/` and the registry's
files under `.sky/ai/registry/`.

## A real run on Apple silicon

On a Mac with Apple silicon the backend is detected as `Mlx`. Install the
engine, the dev model, the Mlx toolchain and the 4-bit base weights once:

```bash
sky ai provision --dev-model
sky ai provision --train mlx --base dev
sky run src/Main.sky
```

The downloads go to the Std.Ai home (`~/Library/Caches/sky/ai` on macOS, or
`SKY_AI_HOME`): about 700 MB for the MLX toolchain, the 4-bit base weights and
the dev model. Python is managed by Sky. You do not install it.

In development a missing toolchain or base is also downloaded on the first
run, so `sky run` alone works too. It then waits for the downloads before the
first step.

How long a run takes: a direct MLX run of 12 steps of 2 rows of 256 tokens,
rank 4 on 4 layers, took 8.5 s on an Apple M-series Mac with 16 GB, with a
peak memory footprint of 2.24 GB ([`docs/ai/train.md`](../../docs/ai/train.md#measured-on-a-mac)).
This job has five times the steps, longer rows and more layers, so it takes
longer.

On a machine without Apple silicon or an NVIDIA GPU, run the scripted Fake
trainer instead. It runs the real driver in a managed CPython and writes a
no-op adapter:

```bash
sky ai provision --dev-model
sky ai provision --train fake
SKY_AI_TRAIN_BACKEND=fake sky run src/Main.sky
```

## Stop, cancel, resume

- **Stop the app** mid-run (Ctrl-C) and start it again. The trainer runs
  detached and keeps training. The page finds the run and follows it again.
- **Cancel** stops the trainer: it writes a checkpoint and exits, and the run
  ends as cancelled. From a terminal in this directory,
  `sky ai train cancel <run>` does the same, and `sky ai train runs` lists the
  runs.
- **Resume.** After a cancel or a failure the button reads **Resume**: it
  calls `Train.resume job` and starts the run again from its newest
  checkpoint (the same job is the same run). A trainer
  that dies by itself (killed, out of memory) is relaunched from its newest
  checkpoint without any action, up to three times.

## The test

`sky test` runs in test mode, because the project has a `.env.test`. It sets
`SKY_AI_TRAIN_BACKEND=fake`, and in test mode no trainer starts and nothing is
downloaded: the durable workflow runs for real on SQLite, and the launch step
writes the protocol, checkpoints and a no-op adapter. The test trains the app's
own job with 12 steps, a checkpoint every 2 steps and a first trainer that dies
after step 3. It checks that the run relaunches from the step-2 checkpoint and
finishes, that the adapter is converted to GGUF and registered as `support@v1`
with its lineage, that driving the finished run again returns the same adapter,
and that the slot serving `support@dev` sends the version's served name. The
fixture in `tests/mocks/chat.json` answers only that name.

## Files

| File | What it holds |
|---|---|
| `src/Main.sky` | the web app: the dataset, training, adapter and ask cards |
| `src/Support.sky` | the frozen dataset, the training job, the registry, the promotion to `dev` and the slot that serves `support@dev` |
| `data/support.jsonl` | 40 support conversations in the chat format |
| `tests/TrainExampleTest.sky` | the offline lifecycle test |
| `tests/mocks/chat.json` | the answer of the served adapter, for test mode |
| `.env.test` | turns on test mode and selects the Fake backend |
| `sky.toml` | the project |

Guide: [`docs/ai/train.md`](../../docs/ai/train.md).
