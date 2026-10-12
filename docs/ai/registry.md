# The model registry: `Std.Ai.Registry`

*(v0.28.0)* The registry holds the versions of your fine-tuned adapters, what
each was trained from, the eval reports that gate it, and the aliases
(`dev`, `qa`, `prod`) that serving follows. Promotion needs a passing
evaluation, and the type system enforces it: `Registry.promote` takes an
`Eval.Passed`, which only an eval gate can make ([`eval.md`](eval.md)).

The design is [`../design/std-ai-lifecycle.md`](../design/std-ai-lifecycle.md)
§8.7. The live API is `sky doc Std.Ai.Registry` and `sky doc Std.Ai.Store`.
`examples/71-ai-registry` runs the whole lifecycle with no model and no
network.

## Two halves: the records and the artefacts

- **The records** live in the app's own database (`Std.Db`): SQLite in
  development, PostgreSQL in QA and production, with the same code. Every
  version, alias, alias move and attached eval report is a row. The registry
  creates its tables when it opens, through the same checksummed
  `_sky_migrations` ledger `Db.migrate` uses (ids `_sky_ai_registry_*`), with
  dialect-neutral migrations, so it is safe on every start and on several
  replicas at once.
- **The artefacts** live in a `Std.Ai.Store`: a local directory, a Google
  Cloud Storage bucket or an Amazon S3 bucket. Each file is stored under
  `blobs/sha256/<hex>`, named by the SHA-256 of its bytes, and checked against
  that digest whenever it is read back.

Only adapters are registered: LoRA and QLoRA, as GGUF (what llama.cpp loads)
or safetensors (what training and vLLM use). `register` refuses a file that is
not one (a full model, a pickle checkpoint), and each version is pinned to the
digest of the base model it was trained on.

## Register, evaluate, promote

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.Crypto as Crypto
import Std.Log exposing (println)
import Std.Db as Db exposing (Db)
import Std.Ai.Provider as Provider exposing (Provider)
import Std.Ai.Local as Local
import Std.Ai.Eval as Eval exposing (Expect(..))
import Std.Ai.Store as Store
import Std.Ai.Registry as Registry exposing (Format(..), Method(..))


support : Eval.Suite String
support =
    Eval.suite "support"
        (\q -> [ Provider.user q ])
        [ { id = "refund", input = "How do I get a refund?", expect = Contains "refund", tags = [] } ]


-- The candidate: the base model with the new adapter (a slot that serves it).
candidate : Provider
candidate =
    Provider.custom (\_ -> Task.succeed { content = "Ask for a refund.", usage = { promptTokens = 1, completionTokens = 1, totalTokens = 2 } })


release : Db -> Registry.Registry -> Task Error Registry.AliasEvent
release db reg =
    Registry.register reg "support-lora"
        { path = "out/adapter.gguf", format = Gguf, method = Lora }
        { base = Local.devModel
        , dataset = "support@3"
        , datasetDigest = Crypto.sha256 "the frozen dataset's bytes"
        , metadata = [ ( "seed", "7" ), ( "backend", "mlx" ) ]
        }
        |> Task.andThen
            (\v ->
                Eval.runAs (Registry.label v) db candidate support
                    |> Task.andThen (\report -> Registry.attachReport reg v report |> Task.map (\_ -> report))
                    |> Task.andThen (\report -> Task.fromResult (Eval.check (Eval.thresholds |> Eval.withMinPassRate 1.0) report))
                    |> Task.andThen (\passed -> Registry.promote reg "prod" v passed)
            )


main : Task Error ()
main =
    Db.connect ()
        |> Task.andThen (\db -> Registry.openConfigured db "file://.sky/ai/registry" |> Task.andThen (release db))
        |> Task.andThen (\ev -> println ("prod now v" ++ String.fromInt ev.to))
```

- `register` stores the file by digest and records the next version number
  with its lineage. Registering the same bytes again returns the same version,
  so a resumed workflow does not create a second one.
- `Registry.label v` (`support-lora@v3`) is the candidate name to run the
  suite under, so the `Passed` names this version.
- `attachReport` records the report against the version.
- `promote` checks that the `Passed` names this version, that its report is
  attached and that the version is not blocked, then moves the alias.

`Registry.promoteChecked reg "prod" v thresholds` does the evaluation part
from the evidence already attached: it re-applies the gate to the version's
attached reports, newest first (`Registry.evidence`). This is what
`sky ai promote` runs.

## Aliases: promote, roll back, force

Every alias move is a compare-and-swap with a logged event, in one
transaction:

- `promote reg alias v passed` moves the alias from wherever it is now.
- `promoteIf reg alias expected v passed` moves it only while it still points
  at `expected` (`Nothing`: the alias does not exist yet). Use it when the gate
  compared the candidate with the alias's current version: if another
  promotion moved the alias meanwhile, the comparison is stale and the call
  fails with `PromotionConflict`. Of two promotions that start from the same
  state, at most one wins.
- `promoteCompared reg alias v thresholdsFor` re-gates the version's attached
  evidence against the version the alias serves now and moves the alias only
  while it still serves that version. For each attached report (newest first)
  the newest report of the same suite attached to the alias's current version
  is the baseline: the two are compared case by case (`Eval.compare`) and
  gated with that suite's thresholds (`thresholdsFor suiteName`). A suite the
  current version has no report for, or an alias that does not exist yet, is
  gated on its own. `evidenceAgainst` gives the `Passed` and the version it was
  compared with, without moving anything.
- `forcePromote reg alias v reason` moves it without eval evidence. The reason
  is required and recorded.
- `rollback reg "support-lora@prod"` returns the alias to the version before
  the current one. The alias's log works as a stack: a promotion pushes, a
  rollback pops, so two rollbacks go back two promotions.
- `block reg v reason` marks a version so it can never be promoted, forced or
  rolled back to again (for example after a data erasure request). Aliases
  that point at it keep serving it until they are moved.

An alias is lower case (`[a-z][a-z0-9_-]*`) and may not look like a version
number (`v3`), so `support-lora@v3` and `support-lora@prod` are never
ambiguous.

## Reading

| Function | Gives |
|---|---|
| `resolve reg "support-lora@prod"` / `"support-lora@v3"` | the version |
| `version reg name n`, `versions reg name` | one version, every version (newest first) |
| `aliases reg name` | every alias with its version and generation |
| `history reg name alias` | the alias's moves, newest first |
| `reports reg v`, `evalHistory reg name` | the reports of a version, of every version |
| `lineage reg ref` | the version, the aliases on it now, its reports and the moves to or from it |
| `trainedOn reg dataset` | every version trained on a dataset, named by its digest or as `<name>@<n>` (the ones to retrain after an erasure) |
| `datasetVersion reg "support@3"`, `datasetVersions reg name` | a registered dataset version, every version of a dataset |
| `fetchDataset reg d` | a verified local path of a dataset version's JSONL |
| `fetch reg v` | a verified local path of the adapter |

`versionValue`, `lineageValue`, `eventValue` and `reportLinkValue` give the
same records as JSON.

## Dataset versions

`registerDataset reg manifest dataPath` registers a frozen dataset version
(`Std.Ai.Dataset.freeze`, [`dataset.md`](dataset.md)): the data file goes into
the store under its digest (it must match the manifest's, `ChecksumMismatch`
otherwise), and the version is a row of `_sky_ai_registry_datasets` named
`<name>@<n>` with its manifest. Registering the same digest again returns the
version already there. A training lineage records `datasetLabel d` and
`d.digest`, which is what `trainedOn` and `sky ai lineage --dataset` search.

## Serving follows the aliases

A model slot serves a registry adapter when it has a resolver and its adapter
names a registry reference:

```elm
chatSlot : Endpoint
chatSlot =
    Endpoint.endpoint "chat"
        |> Endpoint.withLocal (Local.model Local.devModel)
        |> Endpoint.fromRegistry (Registry.resolver registry)
        |> Endpoint.withAdapter "registry:support-lora@prod"
```

where `registry : Task Error Registry.Registry` opens the registry (the slot
opens it on its first call, so the slot stays a pure top-level value). The
operator can set the adapter instead, with the slot's `SKY_AI_<SLOT>_ADAPTER` (for this slot, `CHAT`) set to `registry:support-lora@qa`.

- A **remote** slot sends the version's served name (`support-lora-v3`) as the
  request's model, which is how vLLM and a gateway pick a LoRA adapter.
- A **local** slot fetches the version's GGUF file (checked against its
  digest), checks that the version was trained on the engine's base model, and
  starts `llama-server` with `--lora <file>`. An adapter trained on another
  base is `AdapterMismatch`, never a model that answers nonsense. A
  safetensors adapter is refused on a local engine (convert it to GGUF first).
- The slot resolves the alias when it starts (`Endpoint.warm`, or its first
  call) and again on a call that comes after the refresh interval: 30 seconds
  by default, `Endpoint.withAdapterRefresh` or
  `SKY_AI_<SLOT>_ADAPTER_REFRESH_S` (`0` resolves once). An idle slot asks
  nothing. A local slot that moves to another version starts an engine with it
  and stops the old engine when its last call ends. A refresh that fails keeps
  the current adapter and says so in `Endpoint.describe`, on stderr and in the
  console.
- `Endpoint.describe` names the resolved version:
  `registry:support-lora@prod -> support-lora@v3 (sha256:…)`.

A slot name names one slot in a process: give two registries' adapters two
slot names.

## The artefact store

`Store.configured "file://.sky/ai/registry"` takes the location from code and
lets the operator override it with `SKY_AI_REGISTRY` (under the project's
prefix). `Registry.openConfigured db url` is `Store.configured` then `open`.

| Location | Store | Credentials |
|---|---|---|
| `file://<dir>` | `Store.local dir` | none |
| `gs://<bucket>/<prefix>` | `Store.gcs bucket prefix` | the service-account key file `GOOGLE_APPLICATION_CREDENTIALS`, else the metadata server of GCE, GKE or Cloud Run |
| `s3://<bucket>/<prefix>?region=<r>` | `Store.s3 region bucket prefix` | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, else the instance role through IMDSv2 |

Credentials come from the platform, never from a Sky variable. An upload is a
conditional create (GCS `ifGenerationMatch=0`, S3 `If-None-Match: *`): a blob
is never overwritten, and an upload of a blob that is already there succeeds.
A downloaded blob is kept in this machine's artefact cache (under the AI home)
after its digest is checked. `Store.withEndpoint` points a store at an
emulator or an S3-compatible server (path-style URLs), with `Store.withToken`
or `Store.withAwsKeys` for its credentials. S3 requests are signed with AWS
Signature Version 4, written in Sky (`Store.signV4`, tested against the AWS
examples).

## The CLI

The registry verbs open the registry on the app's database. In a project with
a pipeline (`src/Ai.sky`) that is the database its code names
(`Pipeline.withDb`); `--db <dsn>` wins over it, and without a pipeline the
database is `--db`, else `SKY_DB_PATH`, else `DATABASE_URL`. The store is
`--registry <url>` (which wins over the variable), else `SKY_AI_REGISTRY`, else
the pipeline's `withRegistry`. Nothing is read from `sky.toml`. Each takes
`--format json`.

```bash
sky ai promote support-lora 3 --to prod                 # on the attached evidence
sky ai promote support-lora 3 --to prod --min-pass-rate 0.95
sky ai promote support-lora 3 --to prod --expect 2      # only while prod is v2
sky ai promote support-lora 3 --to qa --force --reason "manual QA build"
sky ai rollback support-lora@prod
sky ai lineage support-lora@prod
sky ai lineage --dataset <sha256>                       # versions trained on a dataset
sky ai lineage --dataset support@3                      # the same, by registered version
```

`sky ai promote` runs `promoteCompared`: the attached evidence is compared
against the version the alias serves now, so a regression against production
is refused even when the candidate's own pass rate clears the floor, and a
promotion that raced it fails with `PromotionConflict` instead of overwriting
it. In a project with an AI pipeline ([`pipeline.md`](pipeline.md)) each
suite's report is gated with that suite's thresholds from the pipeline.
`sky ai eval … --candidate <model>@v<n> --against <alias> --promote-to <alias>`
evaluates, compares and promotes in one verb.

A refused promotion exits non-zero with the typed failure:
`NotPromotable` (no attached report, a report for another version, a blocked
version), `GateFailed` (the attached reports do not pass the thresholds, or
regress against the alias's version), or `PromotionConflict` (`--expect` did
not match, or the alias moved).

## Failures

| Failure | When | `ErrorKind` |
|---|---|---|
| `NotRegistered { ref }` | an unknown version or alias | `NotFound` |
| `NotPromotable { ref, reason }` | the evidence does not name the version, is not attached, or the version is blocked; nothing to roll back to | `PermissionDenied` |
| `PromotionConflict { ref, expected, actual }` | the alias moved since the caller looked | `Conflict` |
| `AdapterMismatch { adapter, adapterBase, engineBase }` | a local engine runs another base model than the adapter was trained on | `InvalidInput` |
| `InvalidSetting` | a file that is not an adapter, an unpinned base, a bad name or alias | `InvalidInput` |
| `ChecksumMismatch` | a stored or downloaded artefact does not hash to its name | `Conflict` |

## The console

The Sky Console's **AI** tab shows each registry model this process read or
changed: its aliases, and on a click its versions, its alias log and its eval
history ([`../observability.md`](../observability.md)).
