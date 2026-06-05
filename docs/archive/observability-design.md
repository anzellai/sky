# Observability design — tracing & spans (v0.14.2+)

> Status: design agreed 2026-05-19. Target: v0.14.2.
> Implementation tracked in task #235 (phased — see end of doc).

This document is the source of truth for how Sky does distributed
tracing. It exists because tracing, done wrong, becomes a graveyard
of bolted-on instrumentation calls that nobody maintains. The goal
here is the opposite: **one mechanism, threaded through one seam,
that every current and future module gets for free.**

## Goals

1. **Reliable** — propagation never silently breaks across
   goroutines, processes, or hosts.
2. **Scalable** — cost is bounded regardless of traffic; works for
   single-process, multi-instance, and serverless deployments.
3. **Future-proof** — a new stdlib/extended-lib module is traced by
   writing one line, not by re-plumbing context.
4. **Simple to reason about** — two questions, ever (see "Mental
   model").
5. **Useful with zero configuration** — a dev who writes no spans
   and sets no env vars still gets a correct, useful trace tree.
6. **No type regression** — the typed-codegen contract (v0.13/v0.14)
   is preserved; no public surface widens to `any`.

## Non-goals (explicit scope guard)

- Per-`String.length`-call spans. Tracing is for work that crosses
  a runtime seam, not every function call.
- A bespoke trace backend. We export OTLP and let Tempo / Jaeger /
  Honeycomb / Datadog / Cloud Trace consume it.
- Custom span processors / samplers beyond what the OTEL SDK gives.
- Log↔trace correlation via a slog handler — a later phase only if
  real demand surfaces.

## Rejected alternatives (and why)

| Approach | Why rejected |
|---|---|
| Goroutine-local span via `runtime.Goid()` keyed map | `Goid` is a discouraged hack; `Cmd.perform` spawns a goroutine with a fresh ID, so propagation breaks **silently**. |
| `context.Context` as a hidden arg on every kernel | Invasive to every codegen path; breaks the "users never think about FFI" rule; doesn't thread cleanly through `Task.andThen`. |
| Global `sync.Map` keyed by `requestID` | Works for HTTP, breaks for Tui / Cli / queue workers / scheduled jobs. Not future-proof. |
| Per-call manual `Trace.span` wrapping everywhere | Users forget; stdlib authors have no uniform seam. Not simple long-term. |
| Package-level `currentCtx` global | Unsafe — two in-flight requests are two goroutines needing two ctxs at once. |

## Core insight

**`Task` is already the propagation vehicle.** Every observable
side effect in Sky is a `Task Error a`. `Task.andThen`,
`Cmd.perform`, `Task.parallel`, `Task.run` already shape the causal
graph. If the trace token travels *with the Task*, propagation is
free — no goroutine-ID hacks, no global maps, no per-kernel
plumbing in user code.

## Architecture — five layers

```
┌──────────────────────────────────────────────────────────────┐
│ Layer 5 — Sampling + back-pressure                            │
│   Bounded export queue, drop-on-overflow, head sampling.      │
├──────────────────────────────────────────────────────────────┤
│ Layer 4 — Cross-process W3C TraceContext                      │
│   traceparent header in/out. Sub-app boundary forwards.       │
├──────────────────────────────────────────────────────────────┤
│ Layer 3 — Sky-level explicit spans (opt-in user API)          │
│   Std.Trace.span "checkout" task — one line, Sky-shape.       │
├──────────────────────────────────────────────────────────────┤
│ Layer 2 — Auto-instrumented kernels (one seam per observable) │
│   Db.* Auth.* Http.* File.* sessionStore — one wrap each.     │
├──────────────────────────────────────────────────────────────┤
│ Layer 1 — goroutine-context trace propagation                 │
│   ctx in a goroutine-keyed map; spawn sites RunWithTraceContext│
├──────────────────────────────────────────────────────────────┤
│ Layer 0 — OTEL SDK + OTLP exporter                            │
│   Industry-standard wire format. Vendor-neutral.              │
└──────────────────────────────────────────────────────────────┘
```

## The propagation token

The token **is `context.Context`**. It already carries the OTEL
span (traceID / spanID / sampled bit), the OTEL SDK's
`tracer.Start(ctx, …)` consumes it directly, and the W3C propagator
serialises it to / from a `traceparent` header. Inventing a custom
struct alongside it would just add conversion code.

### Propagation mechanism — revised after grilling the design

The original sketch threaded a token as a Task-thunk argument
(`func() any` → `func(skyTrace) any`). Surveying the runtime found
that costs **~130 edits** (85 thunk-creation sites + ~40 consumers)
and **duplicates infrastructure that already exists**:
`runtime-go/rt/goroutine_context.go` already carries a per-goroutine
propagation value (`requestID`) and already stamps child goroutines
across the `Cmd.perform` spawn boundary via `RunWithRequestID`;
`tracing.go` already has the OTEL SDK, the inbound HTTP server span,
and outbound `traceparent` injection.

Decision: **generalise the existing goroutine-context store** to
hold a `context.Context` (which subsumes `requestID` — the req-id
lives inside the ctx). The thunk-threading is theoretically cleaner
but produces an *identical* span tree — it buys no observable
benefit, at 10× the edit surface and far more risk for a patch
release.

Mechanism:

- `goroutine_context.go` stores `context.Context` in a `sync.Map`
  keyed by goroutine ID (was: `requestID string`).
- `CurrentTraceContext()` returns the calling goroutine's ctx, or
  `context.Background()` when unstamped.
- `RunWithTraceContext(ctx, fn)` (generalised `RunWithRequestID`)
  stamps a spawned goroutine and `defer`-clears on exit.
- **Every** goroutine-spawn site in the runtime — `Cmd.perform`,
  `Task.parallel`, the SSE subscription tick, sub-app managers (12
  sites total) — wraps its child in `RunWithTraceContext`.
- `WithSpan` reads `CurrentTraceContext()`, opens a child span,
  stamps the child ctx for the duration of `fn`, restores after.

Reliability is enforced two ways:

1. All 12 spawn sites audited to use `RunWithTraceContext` —
   tractable and verifiable, unlike 130 scattered edits.
2. A CI grep-gate forbids a bare `go func` in `runtime-go/rt/`
   outside the blessed spawn helpers, so a future un-wrapped spawn
   fails the build instead of silently dropping the trace.

Cost: `runtime.Stack` goroutine-ID parse is ~150 ns, paid **per
goroutine spawn** (not per span). Negligible against per-Task
latency.

This keeps Layers 0 and 2–5, the sane defaults, and the typed
`Std.Trace` API **completely unchanged** — only Layer 1's internal
mechanism differs, and it is invisible to everything above it.

Why this is **not** a typed-codegen regression:

- The Task representation `func() any` is **unchanged** — no thunk
  signature churn, no Sky-source change, no typed-codegen change.
- `WithSpan` is Go-runtime-internal; never a kernel, never in Sky
  source.
- The Sky-facing `Std.Trace.span : String -> Task e a -> Task e a`
  is fully parametric (see Layer 3).

## The auto-instrumentation seam

Every kernel that touches the outside world is wrapped in one
helper. `WithSpan` is **Go-runtime-internal** — never a kernel,
never in `Kernel.hs`, never in `lookupKernelType`, never in Sky
source. Internal Go helpers being `any`-shaped is the FFI boundary,
not a regression.

```go
// runtime-go/rt/tracing.go
// Reads the calling goroutine's current token, opens a child span,
// stamps the child token for the duration of fn, restores on exit.
func WithSpan(name string, kind trace.SpanKind,
              attrs []attribute.KeyValue, fn func() any) any {
    parent := CurrentTrace()                       // goroutine-context lookup
    ctx, span := tracer.Start(ctxFromToken(parent), name,
        trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
    defer span.End()
    child := tokenFromCtx(ctx)                     // new spanID, same traceID
    prev := CurrentTrace()
    SetGoroutineTrace(child)
    defer SetGoroutineTrace(prev)                  // stack-disciplined restore
    out := fn()
    if err := errOf(out); err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
    }
    return out
}
```

A kernel implementation then reads as:

```go
func Db_query(db, query, args any) any {
    return WithSpan("db.query", trace.SpanKindClient,
        []attribute.KeyValue{
            attribute.String("db.system", dbKind(db)),
            attribute.String("db.operation", sqlVerb(query)),
            attribute.String("db.statement", parameterise(query)),
        },
        func() any { return dbQueryImpl(db, query, args) })
}
```

The kernel signature is **unchanged** — `WithSpan` reads the token
from goroutine-context, not from a new parameter.

One line of wrap per kernel. Every future kernel author writes one
line. That is the whole maintenance burden.

## Sky-facing API (Layer 3 — opt-in)

```elm
Trace.span  : String -> Task e a -> Task e a    -- preserves `a` — same tier as Task.map
Trace.event : String -> Task e ()               -- instantaneous marker
Trace.attr  : String -> String -> Task e ()      -- annotate the current span
```

`Trace.span` is `Forall [e, a]. String → Task e a → Task e a` —
fully parametric. The runtime wraps the *execution* (span
open/close); the *value* flows through untouched. No `any`.

## Sane defaults — what a dev sees with ZERO config

The bar: a dev runs `sky run`, clicks around in a browser, opens
`/_sky/console`, and can answer *"why was that slow?"* and *"what
did that touch?"* — having written no spans and set no env vars.

### Default span set (Tier 1 — always on, structural)

Every HTTP request auto-produces this tree, no config:

```
GET /checkout                                  240ms   ← root (http server span)
├─ session.load   store=redis                   12ms
├─ msg SubmitOrder                              180ms   ← TEA dispatch — the causal middle layer
│  ├─ db.query   "SELECT … FROM cart WHERE …"    40ms
│  ├─ db.exec    "INSERT INTO orders …"          85ms
│  └─ http.post  api.stripe.com/v1/charges       50ms   ← outbound, traceparent injected
├─ session.save   store=redis                     8ms
└─ render        vnode-diff                       15ms
```

Tier 1 = the seams the runtime already owns:

- HTTP server span (request in → response out) — the root.
- Session `load` / `save` — one each, tagged with store kind.
- **Msg dispatch** — one span per `update msg model`. Non-obvious
  but essential: it is the parent that groups the DB spans under
  *"which Msg caused them."* Without it the tree is flat and far
  less useful.
- DB `query` / `exec` / `insertRow` / `getById` / `findOneByField`
  / `withTransaction` — one span per operation.
- Auth `login` / `register` / `setRole` — one each (NO secrets).
- Outbound HTTP (`Http.get` / `Http.post`) — one per call,
  `traceparent` injected.
- `Cmd.perform` task execution — one per spawned task.
- Render (vnode diff) — one per render pass.

A dev never asks for these. They are just *there*. An N+1 query
shows as 200 `db.query` spans under one Msg — the pathology
surfaces for free.

### Where traces go with zero config

- **No `OTEL_EXPORTER_OTLP_ENDPOINT`** → traces still land in an
  in-process bounded ring buffer that the Sky Console Traces tab
  reads. **No Jaeger, no collector, no Docker.** Open the console,
  see the tree. This is the killer zero-config default.
- **Endpoint set** → *also* export OTLP to it.

### Sampling — defaults that need no arithmetic

A percentage forces the dev to know their traffic volume. A rate
limit does not.

| Mode | Default sampler | Rationale |
|---|---|---|
| dev (`ENV` unset / dev) | 100% | Local debugging — keep everything; the ring buffer is bounded anyway. |
| production | **rate-limited head sampler: 50 traces/sec, plus always-keep for `status ≥ 400` and `duration > 1s`** | A 10-req/s app keeps everything; a 100k-req/s app keeps 50/s. Cost is bounded *regardless of traffic*; the dev never computes a percentage. Errors and slow requests are never dropped. |

Children inherit the root's `sampled` bit — one decision per
trace, deterministic, cheap.

### Attributes — useful, and safe-by-default

Default-captured (OTEL semantic conventions, so the data outlives
any one backend):

- `http.route` `http.method` `http.status_code`
  `http.response.body.size`
- `db.system` `db.operation` `db.statement` — the **parameterised**
  SQL (`WHERE id = $1`), **not** the bound values
- `session.store` `session.id` (hashed)
- `sky.msg` (the Msg constructor name)
- `error` + `exception.type` / `exception.message` on failure

**Never captured by default** — a hard security default, not a
config knob:

- Passwords, tokens, secrets
- SQL bind *values* (PII risk)
- Request / response bodies
- Session contents

A pro who understands the footgun can opt into body/value capture
explicitly. Default posture: *structural metadata yes, payload
data no.*

### Cost & reliability ceilings (also defaults)

- Ring buffer bounded: `SKY_TRACE_BUFFER` (default 1000 traces).
- Export queue bounded: drop-on-overflow, `sky_traces_dropped_total`
  metric so silent loss is visible.
- `defer span.End()` + `span.RecordError` on panic — a crash inside
  a span still closes cleanly.

## Mental model — two questions, ever

1. **"Is this work observable from the outside?"** → wrap the
   kernel in `WithSpan`. Done forever.
2. **"Do I want my Sky app code to show up as a named span?"** →
   `Trace.span "name" task`. Otherwise it is invisible — which is
   correct; application-level spans should be opt-in or you drown
   in noise.

Nobody — user, stdlib author, or runtime — ever thinks about
`context.Context`, goroutine IDs, or samplers.

## Two-tier summary

| | Tier 1 — zero config | Tier 2 — opt-in (pro) |
|---|---|---|
| Who | everyone | senior devs who know what they want |
| Spans | HTTP, session, Msg, DB, Auth, outbound HTTP, Cmd, render | `Trace.span` app-logical spans, `Trace.event`, `Trace.attr` |
| Sink | in-process ring → Sky Console | + OTLP endpoint |
| Sampling | dev 100% / prod 50-per-sec + keep-errors | tune rate, switch to %, add a tail-sampling collector |
| Attributes | structural only | bind values, bodies, baggage |

## Environment variables

| Env | Default | Meaning |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (unset) | When set, export OTLP there in addition to the in-process ring. |
| `SKY_TRACE_BUFFER` | 1000 | In-process ring buffer size (traces). |
| `SKY_TRACE_SAMPLE_RATE` | (rate-limited) | Pro override — force a fixed fraction `0.0–1.0`. |
| `SKY_TRACE_RATE_LIMIT` | 50 | Prod head-sampler ceiling (traces/sec). |
| `SKY_TRACE_QUEUE_MAX` | 10000 | Export queue cap; beyond it spans drop. |
| `SKY_TRACE_CAPTURE_BODIES` | off | Pro opt-in — capture request/response bodies + SQL bind values. Footgun: may log PII. |

## Phased implementation (task #235)

Each phase is independently shippable; nothing in phase N blocks
phase N+1 from shipping. User code is unaffected through phase 3.

| Phase | Scope | Est. | User-visible? |
|---|---|---|---|
| **1** | Generalise `goroutine_context.go` (`requestID` → `context.Context`). `rt.WithSpan` + `CurrentTraceContext` / `RunWithTraceContext`. Audit all 12 goroutine-spawn sites. CI grep-gate vs bare `go func`. | ~4 h | No — internal. |
| **2** | Auto-instrument `Db.*`, `Auth.*`, `Http.*`, `File.*`, session store. One `WithSpan` wrap per kernel. | ~4 h | No — additive spans. |
| **3** | `Sky.Http.Server` inbound `traceparent` extract; `Sky.Core.Http` outbound inject. Sub-app forwarding. | ~3 h | No — interop unlock. |
| **4** | `Std.Trace.span / .event / .attr` Sky API. This doc + `docs/observability.md` user guide. | ~2 h | Yes — opt-in API. |
| **5** | Sky Console Traces tab renders the span tree. Playwright e2e asserts request → session → db → response. Sampling env wiring. | ~3 h | Yes — closes the loop. |

Total ≈ 18 h. Suggested PR grouping: **(1+2)** foundation +
immediate value, **(3)** interop, **(4+5)** UX.

## Verification gates (per phase)

- `cabal test` — zero failures, pending count unchanged.
- 26-example sweep builds + runs clean.
- Phase 5 adds a Playwright assertion that the Console Traces tab
  shows the full request → session → db → response chain.
- No public Sky surface widens to `any` (typed-codegen contract).
- `scripts/mem-guard.sh` running throughout compiler dev.
