# Local and remote models: `Std.Ai.Endpoint` and `Std.Ai.Local`

*(v0.28.0)* An app names each use of a model (a chat model, an embedding
model, a judge) as a **slot**. The code holds the slot's local default. An
operator overrides any of it per environment with environment variables. The
app always receives an ordinary `Std.Ai.Provider`, so `Std.Ai.Agent`, `Tool`,
`Policy` and `Trace` work unchanged, and the app never knows whether the model
runs on the laptop or on a GPU server.

In development a slot can run a **managed local engine**: Sky fetches a pinned
`llama-server` build (llama.cpp) and a pinned model, verifies both by SHA-256,
checks that the model fits in memory, starts the engine as a child process,
and stops it with the app. Sky does not run the model itself.

The design behind this page is [`../design/std-ai-lifecycle.md`](../design/std-ai-lifecycle.md).
The live API is `sky doc Std.Ai.Endpoint`, `sky doc Std.Ai.Local` and
`sky doc Std.Ai.Provider`.

## Quick start: no configuration

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.Error exposing (Error)
import Std.Log exposing (println)
import Std.Ai.Provider as Provider exposing (Provider)
import Std.Ai.Endpoint as Endpoint exposing (Endpoint)
import Std.Ai.Local as Local


chatSlot : Endpoint
chatSlot =
    Endpoint.endpoint "chat"
        |> Endpoint.withLocal (Local.model Local.devModel)


chat : Provider
chat =
    Endpoint.provider chatSlot


main : Task Error ()
main =
    Provider.chat chat [ Provider.user "What colour is the sky on a clear day?" ]
        |> Task.andThen (\r -> println r.content)
```

`sky run src/Main.sky` with nothing set: the first call fetches the engine and
the model into the AI home, starts the engine on `127.0.0.1`, waits until it is
healthy, and answers. Later runs find both in the cache. When the program ends,
the engine stops.

`Local.devModel` is Qwen2.5 0.5B Instruct, Q4_0 (Apache-2.0, a 429 MB file). It
is the smallest Qwen build that answers a chat and serves embeddings on a
laptop CPU, and CI uses the same file.

## The same binary in every environment

The code holds the default. An operator changes it with variables in the
shell, the container or a `.env` file:

```
# CI: the tiny model on the CPU
SKY_AI_CHAT_MODE=local
SKY_AI_CHAT_MODEL=hf:Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_0.gguf#sha256=7671c0c304e6ce5a7fc577bcb12aba01e2c155cc2efd29b2213c95b18edaf6ed

# production: a private GPU server
SKY_AI_CHAT_URL=https://gpu-tier.internal:8443/v1
SKY_AI_CHAT_KEY=<service token>
SKY_AI_CHAT_TIMEOUT_MS=60000
```

The order is the one every Sky setting uses: **operator env > `withX` in code
> stdlib default.** There is no `sky.toml` section for AI. A value set but empty
counts as unset. A value that does not parse (a timeout of `soon`) is skipped
and the next layer applies. Under a custom `[env] prefix` the variables are
`<PREFIX>_AI_<SLOT>_<KEY>`.

### Local or remote

The first rule that applies wins:

1. A resolved `MODE` is obeyed. `MODE=local` together with a resolved `URL` is
   an error that names both sources: a silent choice would let a deploy ignore
   the operator.
2. A resolved `URL` means remote.
3. A `withLocal` spec means a managed local engine.
4. Otherwise every call fails with `NotConfigured` (`InvalidInput`), naming the
   variable to set.

A remote endpoint that is down fails with `Unreachable` (`Unavailable`). It
never falls back to a local model.

### Per-slot settings

The slot is a lower-case literal, `[a-z][a-z0-9_]*`. These words are reserved,
because they name global variables: `home`, `offline`, `fetch`, `llama`, `vllm`,
`accel`, `train`, `eval`, `registry`, `serve`, `db`.

| Builder (code) | Variable | Default |
|---|---|---|
| `Endpoint.withMode Local` / `Remote` | `SKY_AI_<SLOT>_MODE` (`local`, `remote`) | inferred by the rule above |
| `Endpoint.withUrl` | `SKY_AI_<SLOT>_URL`, the OpenAI-compatible base ending `/v1` | none |
| `Endpoint.withKey` (a `Secret`) | `SKY_AI_<SLOT>_KEY` | no key |
| `Endpoint.withModel` | `SKY_AI_<SLOT>_MODEL`: remote, the served model name; local, a model reference | the `withLocal` model |
| `Endpoint.withAdapter` | `SKY_AI_<SLOT>_ADAPTER`, sent as the request's model name; `registry:<name>@<alias>` is resolved through the slot's registry ([`registry.md`](registry.md)) | none |
| `Endpoint.withAdapterRefresh` | `SKY_AI_<SLOT>_ADAPTER_REFRESH_S`: how often a registry adapter is resolved again (`0` once, at start) | 30 |
| `Endpoint.withTimeout` | `SKY_AI_<SLOT>_TIMEOUT_MS` | 120000 |
| `Endpoint.withMaxInFlight` | `SKY_AI_<SLOT>_MAX_INFLIGHT` | local: the engine's parallel slots; remote: 16 |
| `Endpoint.withQueue` | `SKY_AI_<SLOT>_QUEUE` (waiting calls) | 4 x max in flight |
| `Endpoint.withQueueWait` | `SKY_AI_<SLOT>_QUEUE_WAIT_MS` | 30000 |
| `Endpoint.withRetry` | `SKY_AI_<SLOT>_RETRIES` | 2 |
| `Local.withContext` | `SKY_AI_<SLOT>_CONTEXT` (tokens) | 8192, halved until it fits |
| `Local.withGpu` | `SKY_AI_<SLOT>_GPU` (`auto`, `cpu`, `all`, a layer count) | `auto` |
| `Local.withParallel` | `SKY_AI_<SLOT>_PARALLEL` | 2 |
| `Local.withIdleUnload` | `SKY_AI_<SLOT>_IDLE_UNLOAD_S` (`0` never) | 600 in development, 0 otherwise |
| `Local.withPort` | `SKY_AI_<SLOT>_PORT` (`0` a free port) | 0 |

`Endpoint.describe slot` reports where each setting came from (`env`, `code`
or `default`). It never shows a key, and it shows a remote URL as scheme, host
and port only, because a path or query can carry a token.

A model reference is `hf:<org>/<repo>/<file>#sha256=<hex>` or
`file:<path>#sha256=<hex>`. A reference without a digest is refused, so an
unpinned model cannot be loaded. `Local.reference` gives the string of a
`ModelRef`, and `Local.fromReference` reads one.

### Machine settings

| Variable | Meaning | Default |
|---|---|---|
| `SKY_AI_HOME` | the engine, model and registry directory, one per user | `$SKY_HOME/ai`, else the user cache directory (`~/Library/Caches/sky/ai` on macOS, `$XDG_CACHE_HOME/sky/ai` or `~/.cache/sky/ai` on Linux) |
| `SKY_AI_LLAMA_BIN` | the directory of your own `llama-server` build. Set but without `llama-server` in it is an error | the pinned build |
| `SKY_AI_FETCH` | `on` or `off`: may a missing engine or model be downloaded at run time | `on` in development, `off` otherwise |
| `SKY_AI_ACCEL` | force `cpu`, `metal`, `cuda`, `rocm` or `vulkan` for the fit plan | the engine build's accelerator |
| `SKY_AI_REGISTRY` | where the model registry's artefacts live: `file://<dir>`, `gs://<bucket>/<prefix>` or `s3://<bucket>/<prefix>` ([`registry.md`](registry.md)) | `Store.configured`'s value in code, else `file://.sky/ai/registry` |

"Development" means what it means everywhere in Sky: `ENV` unset, or `dev`,
`development` or `local`. With fetching off, a missing engine or model fails
with `NotProvisioned` (`NotFound`) and names the `sky ai` command that
provisions it. Provision production hosts ahead, in the image or at deploy.

## The bounded queue

Each slot admits at most its max-in-flight calls at once, and its queue holds
more that wait. A call that finds the queue full fails at once with
`Overloaded` (`Unavailable`). A call that waits longer than the queue wait
fails with `TimedOut` (`Timeout`). Latency stays bounded under load, and the
program decides what to do with the overflow, for example a `Provider.router`
that sends it to a hosted model on purpose.

Only an overloaded server (HTTP 429 or 503, honouring `Retry-After`) and a
connection that never reached the server are retried, with jittered backoff. A
call that timed out is never retried: the server may have done the work. Wrap
a paid call in `Durable.step` to make it exactly-once.

## The managed engine

`Local.start spec` (or the first call through a local slot) does this, and
each step has a typed failure:

1. **Find the engine.** `SKY_AI_LLAMA_BIN`, else the pinned build in the AI
   home. `PATH` is not consulted: an unpinned build can change the launch
   arguments the supervisor relies on. The pinned build is llama.cpp `b11200`
   from its GitHub release, for macOS arm64 (Metal) and Linux x64 (CPU),
   recorded with its SHA-256 in `runtime-go/rt/ai_pins.go`. On another host,
   set `SKY_AI_LLAMA_BIN`. The Linux build needs a glibc userland with
   `libstdc++`, `libgomp` and OpenSSL 3 (Ubuntu 22.04 or later, Debian 12, or a
   `debian-slim` image with `libgomp1`). It does not run on Alpine (musl).
2. **Fetch and verify.** A download is checked against its pinned SHA-256 from
   the bytes on disk. A mismatch installs nothing and names both digests
   (`ChecksumMismatch`, `Conflict`). An engine archive is unpacked outside the
   cache, run once with `--version`, and only then renamed into place. A model
   download resumes after an interruption.
3. **Plan the fit.** Sky reads the model's shape from its GGUF header (layers,
   KV heads, trained context) and the host's memory (a container's limit
   first), and chooses GPU layers and a context that fit. An explicit context
   that does not fit, or a model too large for the host, fails with
   `ModelTooLarge` (`InvalidInput`) before anything loads. GPU layers asked of a
   CPU-only build fail with `NoAccelerator`. `Local.fit spec` returns the plan
   without starting anything.
4. **Launch safely.** The engine binds `127.0.0.1` on a free port. A fresh
   random key per launch reaches it through a `0600` file
   (`--api-key-file`), never the command line, so another local user cannot
   call it. The engine starts with a cleared environment plus an allow-list
   (`PATH`, `HOME`, temporary directories, the accelerator variables), so no
   operator secret reaches it.
5. **Wait for health.** Calls go out only after `/health` answers.
6. **Supervise.** If the engine dies during a call, that call fails with
   `EngineCrashed` (`Unavailable`), naming the exit code and the end of the
   engine's log. In development the engine restarts with backoff, at most three
   times a minute. After that it stays down and every call says so. Outside
   development the app exits non-zero, as it does when its embedded PostgreSQL
   dies, so the platform restarts the whole program instead of hiding a fault.
7. **Unload when idle.** After the idle window with no call, the engine stops.
   The next call starts it again.

Engines are recorded in `<AI home>/engines.json`. A program that needs an
engine with the same model and settings as one already running on the machine
uses that one instead of loading the model again. An engine whose program was
killed (for example by `SIGKILL`) keeps running in its own process group. The
next program that starts an engine ends it. Engines are identified by the key
file on their command line, so a recycled process id is never killed.

`Local.withEmbeddings` serves `/v1/embeddings` for `Provider.embed`. llama.cpp
then serves embeddings only, so give chat and embeddings separate slots:

```elm
embedSlot : Endpoint
embedSlot =
    Endpoint.endpoint "embed"
        |> Endpoint.withLocal (Local.model Local.devModel |> Local.withEmbeddings)
```

## Provider additions

`Provider.chatWith options provider messages` sends per-call options:
`temperature`, `seed`, `maxTokens` and `adapter` (sent as the request's model
name, which is how vLLM and gateways select a LoRA adapter). A field left
`Nothing` is not sent. `Provider.chat` is `chatWith Provider.defaultOptions`.

`Provider.embed provider texts` returns one vector per text, in order.
`Provider.wire transport model` builds an OpenAI-compatible provider over your
own transport. `Endpoint` and `Local` build theirs with it.

## Failures

Every AI failure is an ordinary `Error` (so every signature stays
`Task Error a`) with an existing `ErrorKind`, a message that starts with
`Std.Ai <code>:`, and a typed `Domain` failure in the `"ai"` domain.
`Std.Ai.Failure.fromError` gives it back as a value:

```elm
import Sky.Core.Error as Error exposing (Error)
import Std.Ai.Failure as Failure exposing (Failure(..))


explain : Error -> String
explain err =
    case Failure.fromError err of
        Just (Overloaded o) ->
            "busy; try again in " ++ String.fromInt (Maybe.withDefault 1000 o.retryAfterMs) ++ " ms"

        Just (ModelTooLarge m) ->
            "the model needs " ++ String.fromInt m.needBytes ++ " bytes"

        Just (NotConfigured c) ->
            "set " ++ c.setting

        _ ->
            Error.toString err
```

Keep the `_ ->` branch: later releases add failures. Code that only decides
whether to retry needs no typed view: `Error.isRetryable err` and
`Error.retryAfter err` work for every domain
([`../errors/error-system.md`](../errors/error-system.md#domain-failures)).

| Failure | `ErrorKind` | Retryable | Fields |
|---|---|---|---|
| `NotConfigured` | `InvalidInput` | no | `setting`: the variable to set |
| `InvalidSetting` | `InvalidInput` | no | `setting`, `reason` |
| `Unreachable` | `Unavailable` | yes | `target`: scheme, host and port only |
| `Overloaded` | `Unavailable` | yes | `retryAfterMs` (the server's `Retry-After`), `status` (429 or 503) |
| `TimedOut` | `Timeout` | yes | `afterMs` when known |
| `EngineCrashed` | `Unavailable` | no | `exitCode` (`Nothing` when a signal ended it), `logTail` |
| `EngineFailed` | `Unavailable` | no | `reason` |
| `ModelTooLarge` | `InvalidInput` | no | `needBytes`, `availableBytes` |
| `NoAccelerator` | `Unavailable` | no | `wanted` |
| `NotProvisioned` | `NotFound` | no | `what`, `command` (the `sky ai` command that provisions it) |
| `ChecksumMismatch` | `Conflict` | no | `expected`, `actual` |
| `RemoteRejected` | by status: 400 / 422 `InvalidInput`, 401 / 403 `PermissionDenied`, 404 `NotFound`, 5xx `Unavailable`, else `Unexpected` | 5xx only | `status` |
| `BudgetExceeded` | `PermissionDenied` | no | `budget`, `estimate` (`Std.Money`) |
| `ToolchainFailed` | `Unavailable` | no | `tool`, `logTail` |
| `GateFailed` | `PermissionDenied` | no | `suite`, `reasons` (`Std.Ai.Eval`, [`eval.md`](eval.md)) |
| `DatasetInvalid` | `InvalidInput` | no | `dataset`, `reasons`: a file and a line or row, never the row's text (`Std.Ai.Dataset`, [`dataset.md`](dataset.md)) |

A crashed engine is not retryable even though it is `Unavailable`: the
engine's supervisor restarts it, and a crash loop must not be retried blindly.
A timed-out call is retryable by your code, but the endpoint never retries it
in place, because the server may have done the work.

`Failure.toError` builds the same `Error` (for a provider or a test that
reports an AI failure), and `Failure.code` is the constructor's name, which
is also the `error.code` field of a structured log line.

## Tests

Under `sky test` in test mode (a project that commits a `.env.test`) no engine starts and no network is used. A call
through any slot, local or remote, is answered by a fixture under
`tests/mocks/`. A local slot's calls go to a URL only a fixture can answer, so
a fixture with `"urlContains": "/chat/completions"` (or `"/embeddings"`)
answers both kinds.

## `sky ai`

| Verb | What it runs |
|---|---|
| `sky ai provision [--variant cpu\|metal] [--dev-model] [--model <ref>]` | `Local.provision ()`, then `Local.fetch` for each model |
| `sky ai pull <org>/<repo> <file>` | `Local.pull`; prints the `Local.huggingFace { … }` value with its digest |
| `sky ai ps [--all]` | `Local.engines ()`: running and orphaned engines (`--all` adds stale entries) |
| `sky ai start <slot> [--model <ref>] [--context <n>] [--embeddings]` | `Endpoint.warm`, then holds the engine until Ctrl-C, SIGTERM or `sky ai stop`; each stops the engine and removes its `engines.json` entry |
| `sky ai stop <slot>` / `--all` | `Local.stopSlot` |
| `sky ai promote <name> <version> --to <alias>` | `Registry.promoteCompared` (`--force --reason`, `--expect`, `--min-pass-rate`; [`registry.md`](registry.md)) |
| `sky ai rollback <name>@<alias>` | `Registry.rollback` |
| `sky ai lineage <name>@<alias>` / `--dataset <sha256>\|<name>@<n>` | `Registry.lineage` / `Registry.trainedOn` |
| `sky ai data freeze <file.jsonl> [--name <n>] [--out <dir>] [--key <field>] [--split <t>/<e>/<x>] [--seed <n>] [--consent <basis>] [--retention <period>]` | `Dataset.fromJsonLines`, `split`, `freezeJson`; prints the digest and the counts per split ([`dataset.md`](dataset.md)) |

Every verb takes `--format json`. The verbs hold no logic of their own: the CLI
builds a bundled Sky program (`sky-bundled/ai`) once per Sky build and runs it,
and the program calls the API above. `sky ai start` uses `Local.devModel`
unless `--model` (or the slot's `SKY_AI_<SLOT>_MODEL`) names another model.
A program whose slot has the same model and settings uses the held engine.

Stopping. Ctrl-C (SIGINT) or SIGTERM on `sky ai start` stops the engine,
removes its `engines.json` entry and exits 130 or 143. `sky ai stop <slot>`
sends the holder SIGTERM, so it ends the same way. A second Ctrl-C during that
cleanup exits at once. Any program that starts a local engine (or a
`Process.spawn` child) gets the same handling, unless a Sky.Live, HTTP or
terminal app in it already owns the signals: its shutdown then stops the
engine. An engine whose holder was killed outright (SIGKILL) is ended by the
next program that starts an engine on the machine.

The start proves the engine it waits for is its own. `/health` needs no key,
so a 200 there proves only that something listens on the port. The engine
must also accept the launch's fresh API key on `/props` and refuse any other
key. Another process on the port (always possible with a fixed
`Local.withPort`) fails the start with `EngineFailed`, naming the port.

A project with an AI pipeline (`src/Ai.sky`, [`pipeline.md`](pipeline.md))
adds `sky ai eval`, `sky ai data freeze <dataset>`, `sky ai run <task>` and
`sky ai members`, and runs `sky ai start <slot>` for a slot the pipeline names
with that slot's own code defaults (its model, context and adapter, so
`--model` and `--context` do not apply).

## Slot helpers

`Endpoint.slotName slot` is the slot's name. `Endpoint.inspect slot` resolves
the slot's settings and their sources like `describe`, without resolving a
registry adapter and without starting anything: its adapter setting shows the
reference as written, and its `source` says whether an operator variable set
it. `Endpoint.tokenize slot text` counts tokens with the engine's own
tokenizer (the `/tokenize` route of llama-server and vLLM); a local slot
starts its engine for it, like a call.

## Registry adapters

`Endpoint.fromRegistry (Registry.resolver registry)` with
`Endpoint.withAdapter "registry:support-lora@prod"` (or
`SKY_AI_<SLOT>_ADAPTER=registry:support-lora@prod`) serves the adapter a
registry alias points at. A remote slot sends the version's served name as
the model; a local slot loads the version's GGUF file into its engine with
`--lora`, after checking the file's digest and that the adapter was trained
on the engine's base model (`AdapterMismatch` otherwise). The slot resolves
the alias at start and again as it moves, so serving follows promote and
rollback. See [`registry.md`](registry.md).

## At startup and in `sky doctor`

The build reads the slots the source names (a string literal or a top-level
`String` constant given to `Endpoint.endpoint`; a project that imports
`Std.Ai.Eval` also has the `judge` slot). A server prints one line per slot
under its `listening` line, in production too:

```
  ai chat      remote  https://gpu.internal:8443  (url: env, key: env, model: env, adapter: env)
  ai judge     set in code, resolved at the first call
```

A line names the mode, the target (scheme, host and port only) and which
settings the environment sets, never a value of a key. A slot the environment
does not touch is decided by its code at the first call.

`sky doctor` applies the slot rule to the operator's environment, so a deploy
that would fail at the first call fails there first: a `MODE` that is not
`local` or `remote`, `MODE=local` with a `URL`, `MODE=remote` without one, a
URL that is not `http(s)://`, a `registry:` adapter without an alias, an
unpinned local `MODEL`, a remote target that does not accept a connection
(a warning). It checks the machine settings (`SKY_AI_FETCH`, `SKY_AI_ACCEL`,
`SKY_AI_REGISTRY`, `SKY_AI_LLAMA_BIN`), and with fetching off it reports a
local slot whose engine or model is not provisioned.

## Not yet

These parts of the design are later work: draft models in `Local`
(`withDraft`), several resident adapters per local engine, and Sky's own
published engine bundles (the pins use the upstream llama.cpp release assets
today). A slot name computed at run time works, but the build cannot list it
for the startup report.

## In the Sky Console

The console's **AI** tab (`/_sky/console?tab=ai`) shows this process's slots
(mode, target, in-flight and waiting calls, p50 and p95 latency, and where
each setting came from, never a key), its local engines (state, model,
accelerator, context, restarts, idle time, last crash), its recent model calls
(slot, kind, outcome, latency, token counts; "Failed" shows only failures, and
a slot's chip shows only that slot) and its `Std.Ai.Eval` runs (pass rate,
gate verdict, and each case's verdict and reason), and the model registry
(each model's aliases, and on a click its versions, alias log and eval
history). It holds no prompt and no answer.
