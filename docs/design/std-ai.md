# LLM agents — `Std.Ai`

> Status: v1. `Std.Ai` is the LLM / agentic layer of the stdlib. It is built ON
> `Std.Durable` (an agent run is one kind of durable workflow) and on the typed
> effect and money layers already in the stdlib. The live API is `sky doc
> Std.Ai.Provider` / `Std.Ai.Agent` / `Std.Ai.Tool` / `Std.Ai.Policy` /
> `Std.Ai.Trace` / `Std.Ai.Memory.Pg`; this doc is the architecture behind it.

## Two principles the design is built around

1. **The harness is the product, not the model.** A model is a swappable
   component behind a typed boundary. Everything durable, auditable, and
   safety-bearing lives in Sky code that does not change when the model does.
2. **The LLM is never the authorisation boundary.** A model proposes; a pure,
   reviewable policy decides; only an allowed decision runs an effect. This is
   `Std.Ai.Policy`, and it is structural, not advisory.

## `Std.Ai.Provider` — a swappable chat backend

A `Provider` is a value. The first adapter speaks the OpenAI chat-completions
wire format, which is one format for hosted OpenAI, vLLM, llama.cpp, and most
gateways, so a local model needs no new code:

```elm
provider = Provider.openai (Secret.fromEnv "OPENAI_API_KEY") "gpt-4o-mini"
local    = Provider.compatible "http://localhost:8080/v1/chat/completions" key "llama-3"
```

`chat` returns the assistant text AND a typed `Usage` (token counts). `cost`
prices a usage in `Std.Money` from a versioned `PriceBook`, exact and never a
float, so per-task and per-tenant roll-ups are honest.

### The router

A `router` is a `Provider` that picks a concrete backend per call by a pure
function over the messages (task shape, sensitivity, length). It is itself a
`Provider`, so it drops in anywhere one does, and it composes (a route may return
another router). The route decision is pure, so it can be logged and later learnt
from outcomes:

```elm
Provider.router (\messages ->
    if sensitive messages then local else Provider.openai key "gpt-4o-mini")
```

## `Std.Ai.Agent` — an agent as a durable workflow

`oneShot` is one system prompt plus the caller's messages, one journalled model
call, the answer and usage out. `toolLoop` is the tool-calling loop: each model
turn and each tool call is its own journalled `Durable.step`. Because the agent
IS a durable workflow, an expensive, non-idempotent model call becomes
exactly-once across a resume — a crashed run that comes back after the call
replays the recorded answer and does not re-bill the model. Between-turn state
(the message list) is rebuilt deterministically from the journalled steps, so
replay is exact.

```elm
agent = Agent.toolLoop "assistant" provider "You are terse." [ lookupTool ] 6
Durable.start db agent "chat-42" { messages = [ Provider.user "..." ] }
Durable.poll db [ Durable.erase agent ]
```

## `Std.Ai.Tool` — the tool protocol

A `Tool` is a name, a description, and an `exec : String -> Task Error String`.
The loop tells the model the available tools and asks it to reply with a small
JSON protocol (`{"tool":…,"args":…}` or `{"answer":…}`); the loop runs the
requested tool as a durable step and feeds the result back. The prompt-based
protocol is the portable floor that works on any OpenAI-compatible endpoint.

**Native function-calling** is also available: `Provider.chatTools` sends the
OpenAI `tools` request and returns the model's real `tool_calls`, and
`Agent.nativeToolLoop` drives it as a durable workflow (each turn and each tool
call a journalled step). Tools are advertised by name + description and receive
free-form JSON arguments; a typed, `Codec`-derived parameter schema is the
remaining increment.

## `Std.Ai.Policy` — the action firewall

This is where "the LLM is never the authorisation boundary" becomes structural.
The model does not call an effect; it proposes a typed `Action` (a stable
capability name, its arguments, a risk tag). A `Policy` is a pure `Action ->
Decision`. Only `Allow` runs the effect; `Deny` and `NeedsApproval` short-circuit
without running it:

```elm
firewall act = Policy.requireApprovalAbove Policy.Low act

Policy.gate firewall (Policy.action "refund" orderId Policy.High) (Payments.refund orderId)
    -- => Pending "…"  — a High-risk action; the effect did NOT run
```

A `NeedsApproval` decision is a suspension point: pair it with
`Durable.awaitSignal` so a human approval resumes the workflow exactly where it
paused. The policy is ordinary Sky code — reviewable, testable, and diffable in a
pull request, unlike a system prompt.

## `Std.Ai.Trace` — the token + cost ledger

Every model call records a row: the run it belongs to, the model, the token
counts, and the cost priced in `Std.Money` as an exact decimal string. `totalCost`
sums a run's per-call costs in Sky over `Std.Decimal` — never a float `SUM` in
SQL, because a float would corrupt the total, which is the whole reason cost lives
in `Std.Money`. The rows are the per-run / per-tenant cost figure and the audit
record of what the agent did.

## `Std.Ai.Memory.Pg` — long-term memory

Long-term memory on PostgreSQL + pgvector (which ships in the embedded bundle).
`remember` upserts a passage with its embedding; `search` returns the passages
closest to a query by cosine distance (pgvector `<=>`); `hybridSearch` blends the
vector distance with a keyword score, so an exact term match is not lost to a
merely-close embedding. The caller supplies the embedding vector, so the module
owns storage and ranking, not the embedding model. It is PostgreSQL-only by design
(the `.Pg` suffix says so).

## Testing offline

Every model and HTTP boundary is `Sky.Core.Http`, so the mock-by-default test mode
(`SKY_TEST_MODE=1` + `.env.test` + `tests/mocks/*.json`) answers the LLM and any
integration call with no network and no credentials. Every `Std.Ai` module and the
Slack capstone are proven this way in `rust/crates/sky/tests/*_flow.rs`.

## The capstone — `examples/66-slack-agent`

A 24/7 Slack bot that ties the whole stack together: an @mention starts a durable
workflow, a background poller drains it, and each run is three journalled steps —
the model call (exactly-once on resume), the Slack post behind the `Policy`
firewall, and a `Trace` cost record keyed by the run id. It ships on SQLite and
runs on embedded PostgreSQL in production unchanged. See its README.

## Relationship to `Std.Durable`

`Std.Ai` adds no new Go kernels: it is pure Sky over `Std.Durable` and
`Sky.Core.Http`. `Std.Durable` is the general server primitive (it is not
AI-specific); `Std.Ai.Agent` is one workflow shape built on it. See
[`durable-execution.md`](durable-execution.md).
