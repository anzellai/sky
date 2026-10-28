# 72-ai-support-desk: a full-stack app with its AI lifecycle in Sky

A support desk whose assistant drafts each reply, and everything around the
model in the same language: a synthetic training dataset, scrubbed and frozen;
an eval suite that gates every model against production; fine-tuned adapters
registered and promoted in a model registry; and the app serving the promoted
adapter. There is no AI configuration file. Every AI setting is a `withX`
builder in code, and an operator overrides it with `SKY_AI_<SLOT>_*`
variables.

This guide walks the lifecycle on your machine. This app imports an adapter
(step 5), and the guide uses small demo adapters so it runs anywhere. To train
an adapter in Sky, see `examples/73-ai-train` (on this machine) and
`examples/74-ai-cloud` (on Vertex AI).

## What is in it

| File | What it is |
|---|---|
| `src/Main.sky` | the app: Sky.Live with Std.Ui, an inbox, a ticket view with the drafted reply, edit and send, a status strip saying which model serves; JSON routes `GET /api/assistant` and `GET /api/draft?q=…` |
| `src/Tickets.sky` | the tickets (`Std.Db.Store`, SQLite); sent replies become training data |
| `src/Assist.sky` | the `"chat"` model slot, the registry, and the serving rule: `support-lora@prod` once it exists, the base model before |
| `src/Data.sky` | the datasets: a synthetic one (teacher, judge, scrub, dedupe, split, journalled) and the replies agents sent |
| `src/Suites.sky` | the eval suite: refund policy, opening hours, a refusal, a JSON triage, and politeness scored by a calibrated judge |
| `src/Tasks.sky` | `import-adapter`: register an adapter with its lineage |
| `src/Ai.sky` | the pipeline the `sky ai` verbs run (`Std.Ai.Pipeline`) |
| `tests/` | the app's logic and the whole lifecycle, offline, answered by `tests/mocks/` |

The app does not import `src/Ai.sky`, so its build and its startup report
carry only the chat slot. The teacher and the judge belong to the pipeline.

## 0. Run it offline first

```
sky test tests/SupportDeskTest.sky
sky test tests/AiPipelineTest.sky
```

The project commits a `.env.test`, so `sky test` runs in test mode: every
model call (chat, teacher, judge) is answered by a fixture under
`tests/mocks/`, no engine starts and nothing is downloaded. The second suite
runs the full lifecycle below in a few seconds.

## 1. The app

```
sky run src/Main.sky
```

Open http://localhost:8072. Pick a ticket: the assistant drafts a reply on the
chat slot, which is the managed local engine on the pinned development model
(Qwen2.5 0.5B; the first call downloads the engine and the model once, about
450 MB, into the per-user AI home). Edit the draft and send it. The strip at
the top says `adapter: base model, no adapter`.

The same answers come from the JSON routes:

```
curl localhost:8072/api/assistant
curl 'localhost:8072/api/draft?q=How%20do%20I%20get%20a%20refund%3F'
```

These routes spend compute on every call. In production put them behind
sign-in, or remove them.

## 2. The pipeline

```
sky ai members
```

The first `sky ai` verb builds the pipeline program from `src/Ai.sky` and
caches it under `.sky/ai/cli/`. It lists the datasets (`support`,
`accepted`), the suite (`support`), the slots (`chat`, `teacher`, `judge`) and
the task (`import-adapter`).

## 3. A dataset

```
sky ai data freeze support
```

The teacher slot writes questions and answers for three topics and two kinds
of customer, the judge drops the answers that guess, a filter drops the short
ones, the rows are scrubbed (an e-mail address becomes a placeholder), deduped
and split by a hash of the question, and the result is frozen into
`.sky/ai/datasets/support/<sha256>.jsonl` and registered as `support@1`. Run
it again: the teacher and judge answers come from the journal in the app's
database, so no model is called and the digest is the same.

`sky ai data freeze accepted` freezes the replies agents sent from the app,
with its consent basis and retention period in the manifest.

## 4. The gate

```
sky ai eval support
```

The suite runs on the chat slot as configured: five cases, the judge
calibrated on two labelled answers first. It prints the report as Markdown,
writes JUnit XML to `.sky/ai/eval/support/junit.xml`, and exits 1 if the gate
fails (at least four cases in five, a calibrated judge, no scorer dropping by
more than 0.05 against a baseline). Every run is recorded in the app's
database, and the app's Sky Console shows it in its AI tab.

## 5. Import, evaluate and promote an adapter

```
sky ai run import-adapter -- path/to/support-lora.gguf
sky ai run import-adapter -- --demo v1        # a small demo adapter instead
sky ai eval support --candidate support-lora@v1 --promote-to prod
```

`import-adapter` registers the file as the next version of `support-lora`,
pinned to the chat slot's base model and recording `support@1` as its
dataset. `--candidate` runs the suite on the chat slot serving that adapter,
attaches the report to the version, and `--promote-to prod` points `prod` at
it, but only because the gate passed.

Within 30 seconds the running app follows: its strip says
`registry:support-lora@prod -> support-lora@v1`, and its drafts come from the
adapter.

## 6. A regression is refused

```
sky ai run import-adapter -- --demo v2
sky ai eval support --candidate support-lora@v2 --against prod --promote-to prod
sky ai promote support-lora 2 --to prod
```

The second command runs the suite on v2 and on the version `prod` serves,
compares them case by case, and fails the gate if v2 regresses: it exits 1,
prints why, and `prod` stays where it was. `sky ai promote` re-checks the
attached evidence against `prod` the same way.

To read what is serving and why:

```
sky ai lineage support-lora@prod
sky ai lineage --dataset support@1
```

## 7. In CI

```
sky ai eval support --test-mode --against prod --format json
```

`--test-mode` answers the model calls from `tests/mocks/`, so the gate runs in
CI with no engine. `--format json` prints one JSON object (verdict, reasons,
digest, the Markdown and the JUnit), and the exit code is the verdict.

## Moving to production

Nothing in the code changes. Point the slots at your servers and the registry
at a bucket:

```
SKY_AI_CHAT_URL=https://gpu.internal:8443/v1
SKY_AI_CHAT_KEY=...                       # a secret, never in code
SKY_AI_JUDGE_URL=https://judge.internal/v1
SKY_AI_REGISTRY=gs://my-bucket/registry
SKY_DB_PATH=postgres://...                # tickets, the registry's records, the eval history
```

Every setting is in code, with the operator's variable winning over it: the
slots are `withX` builders (src/Assist.sky, src/Suites.sky, src/Data.sky), and
the database is `Tickets.database` (`Config.withDatabase (Config.Sqlite
"support-desk.db")`), which `SKY_DB_PATH` replaces. `sky.toml` holds only the
project's name, entry and source root.

`sky doctor` checks every slot (the pipeline's are marked `(pipeline)`) before
you deploy.
