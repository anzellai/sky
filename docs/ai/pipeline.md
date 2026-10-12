# The project's AI pipeline: `Std.Ai.Pipeline` and `sky ai`

*(v0.28.0)* A project declares its AI lifecycle in one value: the datasets it
freezes, the eval suites that gate a model, the model slots it runs and the
tasks it needs. The `sky ai` verbs build that value and run it. There is no
configuration file for any of it: the pipeline is Sky code, and an operator
overrides a slot with its `SKY_AI_<SLOT>_*` variables as usual
([`local.md`](local.md)).

The design is [`../design/std-ai-lifecycle.md`](../design/std-ai-lifecycle.md)
§8.9 and §9. The live API is `sky doc Std.Ai.Pipeline`.
`examples/72-ai-support-desk` is a full-stack app built this way, with a guide
in its README.

## Declare the pipeline in `src/Ai.sky`

The pipeline is the value `pipeline` in the module `Ai` at the root of the
source tree, the way `main` is in `Main`:

```elm
module Ai exposing (pipeline)

import Std.Ai.Pipeline as Pipeline exposing (Pipeline)
import Std.Ai.Dataset as Dataset
import Std.Ai.Dataset.Synthetic as Synthetic
import Std.Ai.Eval as Eval
import Assist
import Data
import Suites
import Tasks


pipeline : Pipeline
pipeline =
    Pipeline.pipeline "support-desk"
        |> Pipeline.withRegistry "file://.sky/ai/registry"
        |> Pipeline.withSlot Assist.chatSlot
        |> Pipeline.withSlot Synthetic.teacherSlot
        |> Pipeline.withSlot Eval.judgeSlot
        |> Pipeline.withDataset "support"
               (Pipeline.datasetOf Dataset.chatCodec Data.support
                   |> Pipeline.withFolder (Dataset.withConsentBasis "synthetic")
                   |> Pipeline.withRegistered)
        |> Pipeline.withSuite "support"
               (Pipeline.suiteOn Assist.chatSlot Suites.support
                   |> Pipeline.forModel "support-lora"
                   |> Pipeline.withThresholds Suites.thresholds)
        |> Pipeline.withTask "import-adapter" Tasks.importAdapter
```

| Builder | What it adds | Verb |
|---|---|---|
| `pipeline name` | a pipeline on the app's database (`Db.connect ()`) | |
| `withDb task` | another database for eval runs, the judge journal and the registry | `--db <dsn>` overrides it |
| `withRegistry url` | the registry's artefact store in code | `SKY_AI_REGISTRY` overrides it, `--registry <url>` overrides both |
| `withSlot endpoint` | a model slot the pipeline names | `sky ai start <slot>` |
| `withDataset name spec` | a dataset (`datasetOf codec build`, `datasetJson build`) | `sky ai data freeze <name>` |
| `withSuite name spec` | an eval suite (`suiteOn slot suite`, `suiteWith providerFor suite`) | `sky ai eval <name>` |
| `withTask name f` | a task over the pipeline's `Context` | `sky ai run <name> -- <args>` |

A dataset's build function and a task get a `Context`: the pipeline's name,
its open database, its registry (opened when the task uses it) and the
arguments after `--`.

The app itself does not need to import `Ai`. When it does not, its build (and
its startup report) carry only the slots the app uses; a teacher or a judge
that only the pipeline uses stays out of the app's binary. `sky doctor` checks
the pipeline's slots too and marks them `(pipeline)`.

## The verbs

```
sky ai eval <suite> [--against <alias>] [--candidate <model>@v<n>]
                    [--promote-to <alias>] [--junit <file>] [--markdown <file>]
                    [--min-pass-rate <x>] [--max-regression <x>]
sky ai data freeze <dataset>
sky ai run <task> [-- <args>]
sky ai start <slot>
sky ai promote <name> <version> --to <alias> [--min-pass-rate <x>]
sky ai members
```

Every verb takes `--format json`, `--db <dsn>`, `--registry <url>` and
`--test-mode` (answer every model call from `tests/mocks/`, as `sky test`
does; the verb still acts on the project's own database).

The first verb builds the pipeline program (`Std.Ai.Pipeline.commandLine
Ai.pipeline`) with the project's modules and caches it under
`.sky/ai/cli/<key>/`, where the key is the SHA-256 of the Sky version,
`sky.toml` and every source file. An unchanged project runs the cached program
at once. Build progress goes to stderr, so `--format json` stays one JSON
value on stdout. A project without `src/Ai.sky` gets a one-line refusal (exit
2) that names the module to write, and nothing is built.

If the app's entry module defines a `Sky.Config` `config` and exposes it
(`module Main exposing (main, config)`), or `Ai` exposes one, the pipeline
applies the same config, so it reads the same database as the app. One that is
defined but not exposed is reported as a warning.

### `sky ai eval`

```
sky ai eval support                                    the slot as configured, alone
sky ai eval support --candidate support-lora@v3        a registry version
sky ai eval support --candidate support-lora@v3 --against prod --promote-to prod
```

The suite runs on the candidate: by default the suite's slot with its code
defaults and its operator variables; with `--candidate <model>@v<n>` (or
`<model>@<alias>`) the same slot serving that registry version's adapter.
With `--against <alias>` it also runs on the version the alias serves (the
registry model is the suite's `forModel`), and `Eval.compare` pairs the two
case by case. The comparison is gated with the suite's thresholds
(`withThresholds`, overridden for one run by `--min-pass-rate` and
`--max-regression`). The candidate's report is attached to its registry
version, so the evidence stays with it.

Output: the Markdown comparison on stdout (or one JSON object with
`--format json`: `suite`, `candidate`, `baseline`, `verdict`, `reasons`,
`passRate`, `reportDigest`, `version`, `markdown`, `junit`), JUnit XML at
`.sky/ai/eval/<suite>/junit.xml` (or `--junit <file>`) for CI, and the Markdown
at `--markdown <file>`. Exit 0 when the gate passes, 1 when it fails or a call
errs, 2 for a usage error.

`--promote-to <alias>` moves the alias to the candidate's version with the
`Eval.Passed` the gate just gave, only when it passed. An alias that already
serves a version moves only when the run compared against it (`--against`
the same alias), and only while it still serves the version compared against
(`Registry.promoteIf`): of two CI jobs that promote from the same state, at
most one wins.

While `SKY_AI_<SLOT>_ADAPTER` pins the slot's adapter, every call of the slot
uses it, so `--candidate` and `--against` would evaluate the pinned adapter
instead. The verb refuses with `InvalidSetting`, naming the variable and
never its value.

Every run is recorded in the database (`_sky_ai_eval_runs`). The running app's
Sky Console reads that table when its AI tab opens, so a `sky ai eval` run in
CI or a terminal appears there with the app's own runs.

### `sky ai data freeze <dataset>`

The dataset's build runs, its rows are frozen into a content-addressed version
under `.sky/ai/datasets/<dataset>/` (or its `withFolder`), and, with
`withRegistered`, the version is registered (`Registry.registerDataset`) as
`<dataset>@<n>`: the data file is stored by digest in the registry's store and
the manifest is a row in the database. Freezing unchanged rows gives the same
digest and the same registered number. A synthetic dataset built with
`Synthetic.withJournal ctx.db` makes no model call the second time.

`sky ai data freeze <file.jsonl>` (a path, or a name with a dot) is the
pipeline-free form: it freezes a JSONL file with the bundled program
([`dataset.md`](dataset.md)).

### `sky ai run <task> -- <args>`

Runs a task with the pipeline's context. Phase 3 of the lifecycle adds a
training member (`withJob`, `sky ai train`) the same way; until then a task
imports an adapter trained elsewhere (`examples/72-ai-support-desk`'s
`import-adapter`).

### `sky ai start <slot>` and `sky ai promote`

`sky ai start <slot>` warms a slot the pipeline names with its code defaults
(its own model, context and adapter) and holds its engine, like the
pipeline-free `sky ai start` ([`local.md`](local.md)). `--model` and
`--context` do not apply to a pipeline slot. A slot the pipeline does not name
starts as before.

`sky ai promote <name> <version> --to <alias>` in a project with a pipeline
re-gates the version's attached evidence against the version the alias
serves (`Registry.promoteCompared`), with each suite's own thresholds from the
pipeline, and moves the alias only while it still serves that version.

## In `sky test`

A suite is an ordinary test:

```elm
supportGate : Test
supportGate =
    Test.test "the support suite passes" (\_ ->
        Pipeline.evalSuite Ai.pipeline "support"
            |> Task.run
            |> Test.ok)
```

`evalSuite` fails with `GateFailed` when the gate fails, and `evalAgainst
pipeline "support" "prod"` gates against an alias. In test mode (a project
with `.env.test`) every slot is answered by `tests/mocks/` and no engine
starts, so the whole lifecycle (freeze, eval, register, promote, serve) runs
offline in CI. `Pipeline.commandLineWith pipeline [ "eval", "support", … ]`
drives a verb from a test.

## Failures

Every failure is a typed `Std.Ai.Failure`: an unknown suite, dataset, slot or
task is `InvalidSetting` naming the members the pipeline has; a failed gate is
`GateFailed` naming each reason; a version that is not registered is
`NotRegistered`; a promotion that lost a race is `PromotionConflict`.
