# Std.Ai.Trace: what model calls did and cost

`Std.Ai.Trace` is the ledger of an app's AI lifecycle. It keeps one row per
model call, per eval call and per training run, in the app's own database
(SQLite or PostgreSQL), with the cost in exact money.

A row holds:

| Field | What it is |
|---|---|
| run, kind | The run the row belongs to, and `Call`, `Evaluation`, `Judge` or `Training`. |
| model, tokens, output | The model or adapter, the prompt and completion tokens, the answer. |
| cost, currency | The cost from a `Provider.PriceBook`, as an exact decimal string. Empty for an unpriced row, never a zero. |
| slot, tier, adapter | The `Std.Ai.Endpoint` slot, `local` or `remote`, and the adapter the server answered with. |
| queue wait | Time waiting for a place in the slot's queue. |
| start | Time to resolve a registry adapter and start or load a local engine (0 when warm). |
| latency | Time from the request being sent to the whole answer, with the slot's retries. For a stream, to its end. |
| time to first token | For a stream, time from the request to the first piece. |
| tokens per second | A local engine's own figure, otherwise completion tokens over the generation time. |
| draft acceptance | For a local engine with a draft model, the share of drafted tokens the base accepted. |
| outcome | `ok`, the `Std.Ai.Failure` code (`Overloaded`, `TimedOut`, …), or the error's kind. |

A value the call never reached is empty, never 0. A call that failed is a row
too: its outcome says why, and it has no tokens.

## Recording calls

Create the tables once, at start-up:

```elm
Trace.setup db
```

`setup` uses the same migration ledger as the eval and registry tables
(`_sky_ai_trace_*`). A table made by an earlier release gains the new columns
in place, and its rows read as model calls with outcome `ok`.

Record a chat as you make it:

```elm
Trace.tracedChat db (Trace.ledger "chat-42" |> Trace.priced book) Provider.defaultOptions assistant messages
```

`Trace.tracedChatStream` does the same for a streamed answer and records the time to
the first token. `Provider.measure`, `measureStream` and `measureTools` give the
facts without recording them, and `Trace.recordCall` records a measured call
later.

A `Ledger` names the run, the kind and the price book. Its `key`, when it is
not `""`, makes the row idempotent: a second row with the same run and key is
not written, so a `Std.Durable` step that records it is safe to replay. A row
with no price book is unpriced.

### Agents

`Agent.oneShotWith`, `toolLoopWith` and `nativeToolLoopWith` take
`{ trace = Just book }`. Each model call and its facts are journalled together
in the run, and the row is written by its own keyed step, so a resumed run
neither calls the model again nor records the call twice. A failed call is
recorded each time it is tried, because each try is a real call.

### Evals

An `Eval` run records each candidate call as an `Evaluation` row, priced with
`Eval.withPriceBook`, and each judge call that reached the model as a `Judge`
row, priced with `Eval.withJudgePriceBook`. A judge answered from the journal
costs nothing and records nothing. The rows go under the run's own id,
`report.traceRun`. The eval's p95 latency stays the call's wall time.

### Training

A `Train` run records each step it reads (the step, the total, the loss, the
tokens per second and the checkpoint) in `Trace.trainingSteps db runId`, and one
`Training` cost row when it ends, finished or failed. A Vertex run's row is
its measured spend. A run on this machine is unpriced.

## Reading

- `Trace.runRows db run`: a run's rows, oldest first.
- `Trace.totalCost db run`: the run's priced rows, summed exactly. It fails
  with `InvalidInput` when they are in more than one currency, or when a
  stored cost is not a decimal.
- `Trace.runSummary db run`: rows, failures, unpriced rows, tokens, and the total
  per currency.
- `Trace.costs db`: the whole ledger by kind and currency, for the cost of the
  lifecycle (calls, evals, judges, training).
- `Trace.spanCount db run`: the model calls of a run.
- `Trace.trainingSteps db run`: a training run's steps.

## Where the facts come from

A slot (`Endpoint.provider`) and a local engine (`Local.provider`) measure in
the runtime. The facts reach `Provider.measure` by a per-call token. They never
travel inside the answer, because the device bridge forwards answers to
browsers. A built-in backend (`Provider.openai`, `compatible`), a `wire` and a
`custom` provider have no runtime facts: Sky's clock gives the latency and the
time to the first piece, and the tier is `remote`.

The GPU tier (`Std.Ai.Serve`) reports its own queue wait as a tier signal (on
`/metrics`, in the console and through OpenTelemetry). A row's queue wait is
the client slot's own queue. See [`serve.md`](serve.md).

Rows can hold answers (`output`). Do not print a run's rows to a log; read
costs with `runSummary` or `costs`.
