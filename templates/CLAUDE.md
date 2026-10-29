# CLAUDE.md — Sky Language Project

> **Entry point for Claude Code.** The coding guide for this project lives in
> **[AGENTS.md](AGENTS.md)** — it's agent-agnostic (Claude, Copilot, Cursor,
> Codex, …) and is imported below. Read it before writing any Sky.
>
> Keep AGENTS.md as the single source of truth; this file only exists so Claude
> Code picks the guide up automatically. `sky doc <Module>` is the live stdlib
> API (never drifts — generated from source); prefer it over any hand-copied
> table.
>
> Upgrading a project to a new Sky minor? Each migration guide lists every
> change, old code and new code, as loud, loud at run time or silent:
> https://github.com/anzellai/sky/tree/main/docs/migration (`v0.27.md`,
> `v0.28.md`, `v0.29.md`).

## Response style (INVIOLABLE)

Every response to the user, and every agent report, follows these rules.

1. Write in ASD-STE100 Simplified Technical English. Keep each sentence short.
   Put one idea in each sentence. Use the active voice. Use the present tense
   where you can. Use common, approved words.
2. Use British English spelling. For example, write behaviour, colour, organise,
   licence, and whilst.
3. Use plain punctuation. Do not use em-dashes, en-dashes, semicolons, or emoji.
   Use full stops and commas.
4. Do not use filler or marketing words. For example, do not write robust,
   seamless, leverage, delve, comprehensive, powerful, or effortless. State the
   fact plainly.

This rule governs prose in chat and in reports. It does not change code, code
comments, identifiers, or quoted output.

@AGENTS.md

## Claude Code — operational notes

Everything about *writing* Sky is in AGENTS.md. This section covers only what
Claude Code has to do differently because it runs commands in a session.

### `sky db start` leaves a process running after the command exits

With `[database] embedded = true`, the cluster's lifetime follows the verb:

- **`sky run` / `sky watch` are ephemeral** — they start this project's cluster
  and stop it when they exit (ref-counted, so two concurrent runs don't stop
  each other's database). Nothing to clean up.
- **`sky db start` is persistent** — it starts the cluster in `.skydata/pg/` on
  a unix socket and *returns*, leaving it up until `sky db stop`. That is the
  point (it's the mode for running `./sky-out/app` repeatedly), but it means the
  usual "the command finished, so nothing of mine is running" assumption is
  wrong here.

So: check with `sky db ps` (this project) or `sky db ps --all` (every cluster on
the machine) rather than assuming; and stop what you started with `sky db stop`
before declaring a task done, unless the user is going to keep working against
it. Never `pkill postgres` — a machine can carry one cluster per project and the
user may have a system PostgreSQL of their own, and `sky db stop` already
targets exactly this project's.

### Don't run `sky db provision` speculatively

`sky db provision --embed` fetches a PostgreSQL bundle over the network and pins
it; `--shared` provisions against a host cluster. Both are deliberate,
side-effecting acts an operator makes once — not something to try in order to
see what happens. Ask the user first.

`provision --embed` fetches a **published** bundle (`postgres-bundle-v18.6`) for
the host platform — **linux and macOS only** (amd64/arm64). It needs a **glibc**
userland: it runs on any normal Linux VM/laptop and on debian-slim/distroless
containers, but **not** on Alpine (musl), bare `FROM scratch`, or Windows — and a
database wants durable storage, so it fits a VM or a container-with-a-volume, not
a stateless function. On an unsupported host use `sky db start` with a system
PostgreSQL, or a `DATABASE_URL`. Full matrix: `docs/skydb/embedded-postgres.md`.

### Embedded PostgreSQL and an explicit DSN together are an error

The app never knows which tier it is in — it consumes a DSN, and only the
provisioner changes. So `embedded = true` (or `--embed`) alongside `path` /
`url` / `SKY_DB_PATH` / `DATABASE_URL` is refused *before the build*, not
resolved by precedence: preferring the cluster would have the app writing to a
throwaway local directory while the user believes it is talking to the server
they named. If you are handed a DSN, do not add `--embed` "to be safe" — pick
the tier the user asked for.

### A slow build in CI is usually a cold cache

When a CI build of this project is slow, check the cache first, not the code:
the job must restore `~/.sky/go-build`, `~/go/pkg/mod` and
`~/.cache/sky/precompress` (not `~/.cache/go-build`, which is what
`actions/setup-go` caches). See AGENTS.md "Build speed and CI caches". Do not
add a cache restore to a release or deploy job that a fork's pull request
could have written.

### A Sky.Spa client handles network blips itself

Do not write retry loops, "network error, try again" banners or reconnect
logic for a `web:app` (or desktop / mobile) client. The runtime re-sends a
transiently failing request, shows "Reconnecting…" after 3 s and the Retry
bar only once its budget is spent; `App.withRpcError` sees FINAL errors only.
For an app-owned indicator use `Sub.connection` (see AGENTS.md).

### A local model slot starts a process

An `Std.Ai.Endpoint` slot with `Endpoint.withLocal` starts a `llama-server`
child process on its first call (and downloads the pinned engine and model
once, about 450 MB, into the per-user AI home). It stops with the program.
`sky ai start <slot>` is persistent: it holds the engine until `sky ai stop
<slot>`. Check with `sky ai ps` before declaring a task done, and stop what you
started. Never `pkill llama-server`: other programs on the machine may share an
engine. Do not run `sky ai provision` or `sky ai pull` speculatively; they
download hundreds of megabytes.
`Local.withDraft` loads a second (draft) model into the same engine, so
budget its memory too. With `Local.withAdapters`, a call that names an adapter
the engine did not load fails with `NotRegistered`: fix the name, do not drop
it to get a base-model answer. An `SKY_AI_*` value that does not parse is
`InvalidSetting`; fix the value rather than unsetting it.

`Std.Ai.Eval` scores `Rubric` cases with a judge slot (`"judge"`) whose
default is the same local pinned model, so a real eval run (outside `sky
test`) can start an engine too. Under `sky test` in test mode (the project has
a `.env.test`) no engine starts: give the
judge a `tests/mocks/` fixture, or pass `Eval.withJudge` a `Provider.custom`.
An eval is a test: let its `Eval.check` thresholds fail it, and do not loosen
a threshold to make a run pass.

`Std.Ai.Trace` rows can hold prompts' answers (`output`): never print a
run's rows to a log, and read costs with `Trace.runSummary` or `Trace.costs`.
Call `Trace.setup db` once at start-up before you record.

`Std.Ai.Registry` promotion needs evidence: never use `Registry.forcePromote`
(or `sky ai promote --force`) to get past a failing gate unless the user asks,
and never `block` or `rollback` a production alias on your own. Read with
`sky ai lineage <name>@<alias>` first.

`Std.Ai.Dataset.Synthetic.run` calls a teacher slot (`"teacher"`) and,
with `withJudge`, the judge slot, once per planned call: both are the local
pinned model by default, so a real generation starts an engine and can make
many calls. Bound it with `withTarget`, `withMaxCalls` and `withCostBudget`,
and test it under `sky test` with `tests/mocks/` fixtures (`match.bodyContains`
picks the answer for a topic). Never print scrubbed or raw dataset rows to a
log: `Scrub` reports counts only, and so should your code.

A project's AI lifecycle lives in `src/Ai.sky` (`Std.Ai.Pipeline`), and
`sky ai eval <suite> --against prod` is the merge gate for any change to a
model, a prompt or an adapter: run it (with `--test-mode` when there is no
engine), read its comparison, and treat exit 1 as a red build. Never use
`--promote-to` or `sky ai promote --force` past a red gate, and never loosen
a suite's thresholds to turn it green, unless the user asks. `sky ai data
freeze <dataset>` and `sky ai run <task>` can call a teacher model and write
to the registry: run them when the task needs them, not speculatively.

`Std.Ai.Train` starts a trainer that outlives your program: `Train.drive`,
`Train.start` and `sky ai train` launch a detached process that keeps training
after a Ctrl-C. Before you declare a task done, list the runs with `sky ai
train runs` and cancel a run you started and no longer need with `sky ai train
cancel <run>`. Never kill a trainer by a process pattern. Do not run `sky ai
provision --train` or a real training run speculatively: the native trainer,
the base weights and the dev model are about 700 MB, and a run uses the GPU and
gigabytes of memory. Test training under `sky test` with
`SKY_AI_TRAIN_BACKEND=fake` in `.env.test`: test mode simulates the trainer
and downloads nothing.

Cloud training (`Train.Vertex`) and the GPU tier (`Std.Ai.Serve` on Cloud
Run) spend money. Never launch a cloud job, deploy a tier or build an image
unless the user asks, and always with a budget (`Train.withBudget`). Before
you declare a task done, list what Sky made with `sky ai cloud ls` and delete
what you started with `sky ai teardown --run <key>` or `--service <name>`.
Never print a credential file or an access token. Test the cloud workflow
under `sky test` with `SKY_AI_GCP_ENDPOINT=http://sky-gcp.invalid` in
`.env.test`: an emulator in the runtime answers every Google Cloud call and
nothing is spent. See `docs/ai/deploy-gcp.md`.
`Std.Ai.Device` runs a slot on the user's device: never call `Device.prepare`
before the user agreed to the download (the page shows the size from
`Device.status`), and never make a `DeviceOnly` slot answer from the server
another way: its prompts must not reach the server. A registry adapter runs on
devices only after `Registry.allowOnDevice`; do not clear one the user has not
reviewed. `sky ai provision --device ios|android|web` and `--ai-embed` download
hundreds of megabytes: run them when the task needs them.

### After an upgrade to v0.27.12, run the tests that decode JSON

`Pipeline.optional` now returns an `Err` for a present, non-null value its
decoder rejects (it used to return the default). Run `sky test` and exercise
each JSON input once after the upgrade. Where a lenient fallback is intended,
write it out (`Decode.oneOf [ dec, Decode.succeed default ]`); do not loosen a
decoder only to make a test pass. See AGENTS.md "JSON decoding is strict".

### After an upgrade to v0.28.0, read the migration guide first

Sky's `docs/migration/v0.28.md` (in the Sky repository) lists every change.
A `case` over `ErrorDetails`
without `_ ->` is now `[E3001]`: add `_ ->` rather than listing the new
`Domain` variant by hand. An `[E1012]` on `Options`, `defaultOptions` or
`embed` is a new `Std.Ai.Provider` name: qualify the name. An app with an
`SKY_AI_*` value that does not parse no longer starts: fix the value, do not
unset it to get the default. `Trace.totalCost` refuses two currencies: read
`Trace.runSummary`. Give a new tool typed arguments with `Tool.typed` and a
`Codec`, and ask for a typed answer with `Provider.chatStructured`, rather
than parsing a string by hand.

### After an upgrade to v0.29.0, prefer the O(1) and O(n) shapes

`x :: xs` is now amortised O(1) and `String.uncons` / `foldl` exist, so a
character loop or an accumulator loop is O(n). Rewrite a loop that used
`String.slice i (i + 1)` or `++` per step to `uncons` and a reversed list of
pieces. Use `Sky.Core.Array` where a list needed an index. Read the `Cost:`
line in `sky doc` before choosing a collection function.

### XML and ZIP input is hostile: use Std.Xml and Std.Zip

Read XML with `Std.Xml.parse` / `Std.Xml.Decode` (or `Codec.fromXml`) and
archives with `Std.Zip`. They refuse a DOCTYPE, entity tricks, zip-slip names,
encrypted entries and zip bombs, and bound size and depth. Do not parse XML
with `String` functions or a Go FFI binding, and do not raise the limits to
get one file through: pass a `Limits` record that fits the input you expect.

### Images: Std.Image and Std.Image.Canvas, on the server

Resize, crop and re-encode uploads with `Std.Image` in the backend (it also
strips metadata, since it decodes and encodes again). Keep an animated GIF
animated with `Image.Gif` or `Image.Preserve`, and make it smaller with
`Image.reduceFrames`. Check `Image.dimensions` / `Image.frames` before work if
you want to refuse early: images over 100 megapixels and GIFs over 1,000 frames
are refused with `InvalidInput` in the domain `"image"`. Draw cards, badges and
thumbnails with `Std.Image.Canvas` (`blank` or `fromImage`, shapes, `paste`
with a mask, `text` with `Canvas.defaultFont` or `loadFont`, then `encode`).
Do not shell out to ImageMagick or Pillow.

### Excel and Word files: use Std.Office.Xlsx and Std.Office.Docx

Read and write `.xlsx` with `Std.Office.Xlsx` and `.docx` with
`Std.Office.Docx`, on the server. Read the bytes with `File.readFile` (or take
an upload), not `File.readFileBytes` (that gives a `List Int`). Write a date
with `Xlsx.date`, not a number, so it reads back as a `Date`. Write a total as
`Xlsx.formula "SUM(B2:B9)"`: the spreadsheet computes it when the file opens.
An inline drop-down list holds 255 characters at most; put longer lists in
cells and use `ListFromRange`. A `Docx` link must be http, https, mailto or an
internal `#anchor`. Bound a read of an upload with `readWith` and `Limits`
that fit the files you expect.
