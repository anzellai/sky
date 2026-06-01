# Observability — tracing & spans (user guide)

> Design rationale + internals: [`observability-design.md`](observability-design.md).
> This page is the *how do I use it* guide.

Sky traces your app automatically. You get a useful trace tree with
**zero configuration and zero code** — and an opt-in API when you
want application-level spans.

## What you get for free

Every Sky.Live / Sky.Http.Server app, with no env vars and no
spans written, produces a trace per HTTP request:

```
GET /checkout                                  240ms   server span (root)
├─ session.load   store=redis                   12ms
├─ msg SubmitOrder                              180ms   the TEA update
│  ├─ db.query   "SELECT … FROM cart WHERE …"    40ms
│  ├─ db.exec    "INSERT INTO orders …"          85ms
│  └─ http POST  api.stripe.com/v1/charges       50ms   outbound, traceparent injected
└─ render        vnode-diff                       15ms
```

Auto-instrumented (Tier 1 — always on):

- HTTP request (server span, the root)
- `Db.query` / `Db.exec` / `Db.insertRow` / `Db.withTransaction`
- `Auth.login` / `Auth.register`
- `Http.get` / `Http.post` (outbound — also injects W3C
  `traceparent` so the downstream service joins the trace)
- `File.readFile` / `File.writeFile` / `File.append`
- Sky.Live Msg dispatch + `Cmd.perform` tasks

## Where the traces go

- **No config** → traces land in an in-process ring buffer.
  Open `/_sky/console` → **Traces** tab. No Jaeger, no collector.
- **`OTEL_EXPORTER_OTLP_ENDPOINT` set** → *also* exported OTLP to
  that collector (Tempo / Jaeger / Honeycomb / Datadog / Cloud
  Trace — anything that speaks OTLP).

## Opt-in: application-level spans

When you want a named, logical span that groups the auto-spans
underneath it, use `Std.Trace`:

```elm
import Std.Trace as Trace

checkout : Cart -> Task Error Receipt
checkout cart =
    Trace.span "checkout"
        (reserveStock cart
            |> Task.andThen chargeCard
            |> Task.andThen issueReceipt)
```

The `db.*` / `http.*` spans opened inside `reserveStock` /
`chargeCard` / `issueReceipt` nest under `checkout` in the trace.

| Function | Type | Use |
|---|---|---|
| `Trace.span` | `String -> Task e a -> Task e a` | Wrap a Task in a named span. Value flows through untouched. |
| `Trace.event` | `String -> Task Error ()` | Mark a point in time on the current span ("cache miss", "retry"). |
| `Trace.attr` | `String -> String -> Task Error ()` | Annotate the current span (`sky.trace.<key> = <value>`). |

## What is captured — and what is not

Captured (OTEL semantic conventions):

- `http.route` / `http.method` / `http.status_code`
- `db.system` / `db.operation` / `db.statement` — the
  **parameterised** SQL (`WHERE id = $1`)
- `sky.session.store` / `sky.session.op`
- `sky.msg` — the Msg constructor name
- error status + `exception.*` on failure

**Never** captured (hard default — not a config knob):

- Passwords, tokens, secrets
- SQL bind *values* (PII risk)
- Request / response bodies
- Session contents

## Sampling

| Mode | Default |
|---|---|
| dev (`ENV` unset / dev / local) | 100% |
| serverless | 100% |
| production | 5% (interim — a rate-limited head sampler lands in a later release) |

Override with `OTEL_TRACES_SAMPLER_ARG=<0.0–1.0>`.

## Environment variables

| Env | Default | Meaning |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (unset) | Export OTLP here in addition to the in-process ring. |
| `OTEL_TRACES_SAMPLER_ARG` | (mode default) | Fixed sample fraction `0.0–1.0`. |
| `SKY_SERVICE_NAME` / `OTEL_SERVICE_NAME` | `sky-app` | `service.name` the backend groups by. |
| `OTEL_EXPORTER_OTLP_HEADERS` | (unset) | Comma-separated `k=v` headers (auth tokens for managed collectors). |
| `SKY_CONSOLE_DB_PATH` | (unset) | When set, dual-writes every log / metric / span to the SQLite file at this path so the bundled console mini-app can render history beyond the 10 k-line / 1 k-span in-RAM caps. WAL mode, 24 h log/span retention, 7 d metric retention. SkyDeploy injects `/data/console.db` on Pro+ tenants; OSS / dev keeps the pure in-RAM path. |
