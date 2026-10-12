# Training and evaluation data: `Std.Ai.Dataset`, `Scrub` and `Synthetic`

*(v0.28.0)* A dataset is a Sky value. You load rows from JSONL, CSV, a
`Std.Db.Store` table or a list, clean them, split them, check them, and freeze
them into a content-addressed **version**. A training job and an eval take a
version, so the exact rows behind a model are always known. You can also
generate rows with a teacher model and keep only the rows a judge passes.

Three modules:

| Module | What it does |
|---|---|
| `Std.Ai.Dataset` | typed rows (`Chat`, `Instruction`, `Preference`), sources, steps, seeded splits, validation, frozen versions with a SHA-256 digest and a manifest |
| `Std.Ai.Dataset.Scrub` | find personal data and secrets in text, replace them with a placeholder, count what was replaced |
| `Std.Ai.Dataset.Synthetic` | generate rows with a teacher model, with topics, personas, examples, quality filters, a judge and a budget |

The design is [`../design/std-ai-lifecycle.md`](../design/std-ai-lifecycle.md)
§8.4. The live API is `sky doc Std.Ai.Dataset`, `sky doc
Std.Ai.Dataset.Scrub` and `sky doc Std.Ai.Dataset.Synthetic`. The examples are
`examples/68-ai-dataset`, `examples/69-ai-scrub` and
`examples/70-ai-synthetic`.

## A dataset, end to end

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.Error exposing (Error)
import Sky.Core.String as String
import Std.Log exposing (println)
import Std.Ai.Dataset as Dataset exposing (Instruction)
import Std.Ai.Dataset.Scrub as Scrub


support : Dataset.Dataset Instruction
support =
    Dataset.fromList
        [ { prompt = "How do I get a refund?", completion = "Open the order and choose Return items." }
        , { prompt = "Where is my parcel?", completion = "Mail help@example.com with the order number." }
        , { prompt = "How do I get a refund?", completion = "A repeated question." }
        ]
        |> Dataset.scrub (Scrub.forInstruction Scrub.defaults)
        |> Dataset.dedupeBy .prompt


main : Task Error ()
main =
    support
        |> Dataset.split Dataset.defaultSplit .prompt
        |> Dataset.freeze (Dataset.folder ".sky/ai/datasets" "support") Dataset.instructionCodec
        |> Task.andThen
            (\v ->
                println
                    ("froze " ++ String.fromInt (Dataset.manifest v).rows ++ " rows as sha256:" ++ Dataset.digest v))
```

## Rows and sources

The row type is yours. Three shapes cover the usual fine-tuning formats, each
with a codec:

| Type | JSON | Codec |
|---|---|---|
| `Chat` | `{"messages":[{"role":"user","content":"…"},…]}` | `chatCodec` |
| `Instruction` | `{"prompt":"…","completion":"…"}` | `instructionCodec` |
| `Preference` | `{"prompt":"…","chosen":"…","rejected":"…"}` | `preferenceCodec` |

| Source | Reads |
|---|---|
| `fromJsonl codec path` | one JSON object per line, decoded with the codec; blank lines are skipped |
| `fromJsonLines path` | each line as its canonical JSON text, for rows with no Sky type |
| `fromCsv decode path` | a CSV file with a header line; `decode` gets each row as a `Dict` from column to cell |
| `fromStore db store toRow` | the rows of a `Std.Db.Store` table |
| `fromList rows` | rows in memory |

A dataset is a description. Nothing is read until you freeze it, validate it,
or ask for its rows (`rows`, `countRows`). A JSONL file is read 1000 lines at a
time, so a large file never sits in memory. `fromCsv` and `fromStore` read
their source whole: a quoted CSV field can span lines, and a store is one
query.

A file that cannot be read, a line that is not JSON, or a row that does not
decode is `Std.Ai.Failure.DatasetInvalid { dataset, reasons }` (`InvalidInput`).
It names the file and the line or row, never the row's text, because a row can
hold personal data.

## Steps

`map`, `filter`, `filterMap`, `dedupeBy key`, `scrub scrubber`, `upTo n`,
`toChat` and `toInstruction` run in order, chunk by chunk. `dedupeBy` keeps
the first row for each key and holds one SHA-256 per distinct key, not the
key. It keeps at most `dedupeMemoryKeys` (250000, about 20 MB) of them in
memory; past that it writes them, sorted, to files under
`.sky/ai/work/dedupe/` in the working directory and looks them up there, so a
dataset larger than memory dedupes in bounded memory. The files are removed
when the rows have been read. `dedupeBounded n key` sets the bound. `steps dataset` lists the steps and `sources dataset` the sources; both go
into the manifest. `withSource` replaces the source description (for example,
for generated data).

## Splits

```elm
Dataset.split { train = 80, eval = 10, test = 10, seed = 0 } .prompt support
```

A split puts each row in `Train`, `Eval` or `Test` by a hash of its key and
the seed, not by a random draw: the first 28 bits of
`sha256 (seed ++ ":" ++ key)`, modulo the sum of the weights. The same key and
seed always give the same part, so a row never moves between parts when new
rows arrive, and the eval rows never leak into training. `splitOf spec key` is
the pure function; `part Eval splits` is one part as a dataset; `unsplit` puts
every row in `Train`. Weights are relative: `{ train = 8, eval = 1, test = 1,
seed = 0 }` is 80 / 10 / 10.

## Validation

```elm
Dataset.validate
    (Dataset.limits |> Dataset.withMaxTokens 2048 |> Dataset.withMaxTurns 16)
    (Dataset.toChat (\r -> { messages = [ Provider.user r.prompt, Provider.assistant r.completion ] }) support)
```

`inspect limits chats` checks every conversation and returns a `Report`;
`validate` does the same and fails with `DatasetInvalid` (naming every limit
broken) when the dataset breaks a limit. The issues:

| `Issue` | When |
|---|---|
| `EmptyTurn` | a message is empty or only white space |
| `RoleOrder` | the roles are out of order: an optional system message first, then user and assistant in turn, starting with user and ending with assistant |
| `TooManyTokens` | the conversation has more tokens than `withMaxTokens` (default 4096) |
| `TooFewTurns` / `TooManyTurns` | the user and assistant messages are fewer than `withMinTurns` (2) or more than `withMaxTurns` (64) |
| `DuplicateRow` | the conversation repeats an earlier one exactly |

The limits are `withMaxIssues` (default 0), `withMaxDuplicateRate` (0.05) and
`withMinRows` (1). The duplicate check uses the same bounded set as
`dedupeBy`: it keeps the SHA-256 of each distinct conversation with the row
where it first appeared, at most `dedupeMemoryKeys` of them in memory
(`withMemoryKeys n` sets the bound), and spills the rest, sorted, to files
under `.sky/ai/work/dedupe`. A dataset larger than memory is inspected in
bounded memory, and the files are removed when the check ends. Tokens are estimated as a quarter of the characters plus 4
per message (`estimateTokens`). `withEngineTokenizer slot` counts with the
model's own tokenizer: each message goes to the slot's engine `/tokenize`
route (`Endpoint.tokenize`; llama-server and vLLM serve it). When the engine
does not answer (offline, fetching off, a server without the route), the run
falls back to the `withTokenizer` function if one is given, else to the
estimate, so a check still runs offline. `withTokenizer` alone gives your own
count.
`describeIssue` prints an issue with its row number and never the text.

## Frozen versions

`freeze target codec splits` writes the rows as canonical JSONL, one line
`{"row":…,"split":"train"}` per row, with object keys sorted, no white space
between tokens and `\n` after each line. The version's digest is the SHA-256
of that file. Two spellings of the same rows (another key order, other
spacing, blank lines) freeze to the same digest.

```
<dir>/<name>/<sha256>.jsonl     the rows
<dir>/<name>/<sha256>.json      the manifest
```

The data file appears whole or not at all: it is written beside its target
and renamed. Freezing the same rows again writes nothing new and returns the
version already there, with its first manifest.

The `Manifest` records the format (`sky-dataset-v1`), the name, the digest,
the row count per split, the split weights and seed, the sources and steps,
the scrub report, the rows removed as duplicates and by filters, the schema
of the row codec, the creation time, and an optional consent basis
(`withConsentBasis`) and retention period (`withRetention`). It is the
lineage a registry records. `manifestJson` and `manifestFromJson` convert it.

`load target digest codec` loads a version and checks that its data file still
has its digest: a changed file is `ChecksumMismatch`, a missing one
`DatasetInvalid`. `versionPart Eval version` reads one part, `versionRows` all
rows, `fromVersion` streams one part as a dataset, and `versions target` lists
every version's manifest, newest first. `freezeJson` and `loadJson` do the
same for rows held as JSON text.

### `sky ai data freeze`

```
sky ai data freeze data/support.jsonl --key prompt --split 80/10/10 --seed 0
sky ai data freeze data/support.jsonl --name support --out data/versions --format json
```

The verb freezes a JSONL file with no Sky code: each line becomes canonical
JSON, the split key is the top-level string field `--key` (else the whole
row), and the version goes to `--out` (default `.sky/ai/datasets`) under
`--name` (default the file name). `--consent` and `--retention` go into the
manifest. It prints the digest, the counts per split and the paths, and exits
non-zero with the typed failure when a line is not JSON.

In a project with an AI pipeline, `sky ai data freeze <name>` (a plain name,
not a file) freezes one of the pipeline's datasets instead, and registers it
when the dataset is `withRegistered` ([`pipeline.md`](pipeline.md)).

### Registered versions

`Registry.registerDataset registry (Dataset.manifest v) (Dataset.blobPath v)`
stores a frozen version's data file in the registry's store (by digest, the
manifest's) and records it as `<name>@<n>` with its manifest. The same digest
registered again is the same version. `Registry.datasetVersion registry
"support@3"` reads one back, `Registry.fetchDataset` gives a local path checked
against its digest, and a training lineage records `Registry.datasetLabel`
and the digest, so `sky ai lineage --dataset support@3` lists every model
trained on it ([`registry.md`](registry.md)).

## Scrubbing personal data

```elm
Scrub.scrubText Scrub.defaults "Mail ada@example.com or call +44 20 7946 0958"
--> ( "Mail [EMAIL] or call [PHONE]", Dict.fromList [ ( "email", 1 ), ( "phone", 1 ) ] )
```

| Rule | Replaces | Placeholder | Checked by |
|---|---|---|---|
| `secrets` | API keys with a known prefix (`sk-`, `sk_live_`, `ghp_`, `github_pat_`, `glpat-`, `hf_`, `xoxb-`, `AIza`, `AKIA`), JSON web tokens, PEM private keys, and a long value after `api_key` / `secret` / `token` / `password` with `=` or `:` (the label stays) | `[SECRET]` | shape |
| `email` | email addresses | `[EMAIL]` | shape |
| `iban` | IBANs, with or without spaces | `[IBAN]` | ISO 13616 mod 97 |
| `cardNumber` | payment card numbers, 13 to 19 digits | `[CARD]` | Luhn |
| `usSsn` | US social security numbers `123-45-6789` | `[SSN]` | issuing rules (no area 000, 666 or 900+, no group 00, no serial 0000) |
| `ukNino` | UK national insurance numbers | `[NINO]` | prefix rules |
| `phone` | international (`+…`), bracketed area code, North American with separators, and national numbers with a leading 0; 9 to 15 digits | `[PHONE]` | shape and length |
| `pattern name re` | every match of your RE2 pattern | `[NAME]` | none |
| `custom name f` | what your function replaces | yours | yours |

`defaults` is every built-in rule, secrets first and phone last, so the rules
stay apart (a card number is not read as a phone number). A number that fails
its check is left alone: an order number that only looks like a card number
survives. A date, a time or a decimal number is not a phone number.
`isCardNumber` and `isIban` are the checks on their own. `pattern` returns a
`Result`: a pattern that does not compile is an `InvalidInput` error, not a
rule that silently matches nothing.

A `Scrubber row` applies rules to the text fields of a row: `forInstruction`,
`forChat` (every message's content) and `forPreference` for the built-in
shapes, `forString` for plain strings, and `scrubber rules get set` for your own type.
`dropIf name predicate` drops whole rows and counts them under `name`.
`scrubRow` scrubs one row, `scrubRows` a list, and `Dataset.scrub` a dataset,
whose counts reach the manifest.

The `Report` is a `Dict` from rule name to count (`merge`, `total`,
`countOf`). It never holds the replaced text, and nothing in these modules
logs a value.

**Scrubbing is a hook you compose, not a guarantee of anonymity.** A name, an
address or a free-text description can still identify a person. Review a
sample of the scrubbed data before you train on it.

## Generating data with a teacher model

```elm
faq : Synthetic.Generator Instruction
faq =
    Synthetic.generator "support-faq" Dataset.instructionCodec
        |> Synthetic.withTopics [ "refunds", "delivery", "passwords" ]
        |> Synthetic.withPersonas [ "a first-time customer", "a busy shop owner" ]
        |> Synthetic.withExamples [ { prompt = "How do I change my email?", completion = "Open Settings and choose Account." } ]
        |> Synthetic.withPerCall 4
        |> Synthetic.withTarget 200
        |> Synthetic.withSeed 7
        |> Synthetic.withDedupe (\r -> String.toLower r.prompt)
        |> Synthetic.withFilter "answer too short" (\r -> String.length r.completion >= 30)
        |> Synthetic.withJudge Eval.defaultJudge "Correct, polite, says what to do next." (\r -> ( r.prompt, r.completion ))
        |> Synthetic.withCostBudget book (Money.fromMinor USD 500)


main =
    Synthetic.run faq
        |> Task.map Synthetic.toDataset
        |> Task.andThen (\ds -> ds |> Dataset.scrub (Scrub.forInstruction Scrub.defaults) |> Dataset.split Dataset.defaultSplit .prompt |> Dataset.freeze target Dataset.instructionCodec)
```

**The teacher** is the model slot `"teacher"` (`Synthetic.teacherSlot`): the
local pinned development model (Qwen2.5 0.5B Instruct) by default, so a fresh
clone generates with no configuration. An operator moves it to a larger or
remote model with the slot's variables, `SKY_AI_TEACHER_*` (the
`SKY_AI_<SLOT>_<KEY>` names in [`local.md`](local.md)). `withTeacherSlot`
uses another slot, and `withTeacher name provider` any `Provider` (the name is
what the report and the lineage record).

**The plan is pure.** `plan generator` lists every call the run may make. Call
`i` asks for `withPerCall` rows (default 4) about one topic for one persona.
The run walks every topic and persona combination from a starting point the
seed picks, and call `i` uses the sampling seed `seed + i` at temperature 0.7
(`withTemperature`). The same generator, teacher and seed ask the same
questions in the same order, so a run is repeatable where the teacher is.

**The prompt.** The system message is `defaultSystemPrompt`
(`withSystemPrompt`) and the user message `defaultTemplate` (`withTemplate`),
with `{name}`, `{persona}`, `{topic}`, `{count}`, `{fields}` (the codec's field
names) and `{examples}` (your `withExamples` rows as JSON) filled in. The
answer must be a JSON array of rows (a code fence or text around it is
fine); `parseRows` reads it.

**Quality.** A row is dropped and counted when it does not parse
(`unparsed`), repeats an earlier row (`duplicates`; by `withDedupe`, else the
whole row), fails a `withFilter` (`filtered`, by name), or fails the judge
(`judgedOut`). `withJudge judge rubric show` scores each row with
`Std.Ai.Eval`'s judge (`Eval.judgeAnswer`): the judge slot is the local pinned
model by default, switched with `SKY_AI_JUDGE_*`.

**Stopping.** The run stops when it has `withTarget` rows (default 20), when
it has made `withMaxCalls` calls (default three times the calls the target
needs), or when the teacher's spend, priced by `withCostBudget book money`,
reaches the budget, whichever comes first. The `Report` says which (`stop`:
`TargetReached`, `CallLimit`, `BudgetReached`) and holds the calls, judge
calls, token counts and cost. The judge's calls are counted, not priced. A
teacher call that fails ends the run with its typed error: the slot already
retries an overloaded or unreachable server.

`toDataset generated` makes the kept rows a dataset whose source records the
generator, the teacher, the seed and the counts, so the frozen version's
lineage says the data is synthetic and where it came from.

### The journal

`Synthetic.withJournal db` records every teacher answer in
`_sky_ai_synthetic_journal`, keyed by the teacher's identity (the slot's
resolved mode and model, or the `withTeacher` name), the call's seed,
temperature, token limit and exact messages, and every judge call in the eval
judge journal (`Eval.judgeAnswerWith`). A re-run with unchanged inputs is
answered from the journal: it makes no model call, `report.replayed` counts
the replayed teacher calls, and it gives the same rows (and so the same frozen
digest). A replayed call is priced at its recorded usage, so a budget stops
the re-run where it stopped the first. Another teacher, seed or prompt is a
new entry and is asked.

## Tests and test mode

An offline test of a generator needs no model. Under `sky test` in test mode
(a project with a committed `.env.test`, [`../tooling/testing.md`](../tooling/testing.md))
the teacher's and the judge's calls are answered by fixtures under
`tests/mocks/`. A local slot's calls go to
`http://sky-ai-local.invalid/<slot>/v1/…`, and `match.bodyContains` picks the
answer for a topic:

```json
{
  "match": { "method": "POST", "urlContains": "sky-ai-local.invalid/teacher/", "bodyContains": "Topic: refunds" },
  "status": 200,
  "body": "{\"choices\":[{\"message\":{\"content\":\"[{\\\"prompt\\\":\\\"How long does a refund take?\\\",\\\"completion\\\":\\\"Five working days.\\\"}]\"}}]}"
}
```

`examples/70-ai-synthetic/tests/mocks/` has a full set: one teacher fixture
per topic and two judge fixtures. In a unit test, `withTeacher name
(Provider.custom f)` answers from the request instead.

## Not yet

The journal is a table in the database rather than a `Std.Durable` step per
call: a re-run (or a resumed one) pays for no call it already made, which is
the property a durable step gives.
