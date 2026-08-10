# Relay — Layer-2 CI corpus, app B

A headless JSON API + event gateway written in Sky. Token-authenticated REST,
an SSE feed, a WebSocket channel, an outbound HTTP fetch pipeline, rate
limiting, CORS, a CSV bulk-import endpoint, and config from the environment.

Spec: `docs/ci-corpus-proposal.md` → "App B — Relay".

## Build + run

```bash
cd apps/relay
../../sky-out/sky build src/Main.sky

RELAY_PORT=8791 \
SKY_AUTH_TOKEN_SECRET="relay-test-secret-0123456789abcdefghij" \
RELAY_CONFIG_FILE=config/relay.toml \
RELAY_ADMIN_USER=admin \
RELAY_ADMIN_PASSWORD=hunter2hunter2 \
  ./sky-out/app
```

### Readiness

The process prints exactly one greppable line to stdout:

```
relay: listening on <port>
```

It is emitted after the port is resolved and the router is built, immediately
before `Server.listen` binds. `Sky.Http.Server` exposes no post-bind hook (its
`listen` Task only resolves on shutdown or a fatal listen error), so a harness
should grep for the line **and** then poll `GET /health` until it answers 200 —
that pair is what proves the socket is actually accepting.

### There is no port literal in bind position

`Server.listen cfg.port` is the only bind site (`src/Main.sky`). The port comes
from `RELAY_PORT`, then `PORT`, then a last-resort default in
`Config.listenPort` (`src/Config.sky`). The environment always wins.

## Environment

| Var | Meaning |
|---|---|
| `RELAY_PORT` / `PORT` | bind port (`RELAY_PORT` wins) |
| `SKY_AUTH_TOKEN_SECRET` | HS256 signing secret |
| `RELAY_CONFIG_FILE` | path to a TOML/JSON/YAML config document |
| `RELAY_CONFIG_TOML` | inline config document (used when no file is set) |
| `RELAY_ADMIN_USER` | Basic-auth + admin-token user (default `admin`) |
| `RELAY_ADMIN_PASSWORD` | admin password, bcrypt-hashed at boot |
| `RELAY_TOKEN_TTL` | token lifetime in seconds (default 3600) |
| `RELAY_UPSTREAM_PATH` | default outbound-fetch path (default `/upstream`) |

Config document precedence: `RELAY_CONFIG_FILE` → `RELAY_CONFIG_TOML` → the
built-in default in `Config.defaultToml`. Each document is tried as TOML, then
JSON, then YAML, so `RELAY_CONFIG_TOML` accepts a JSON body too.

## Endpoints

| Method + path | Behaviour |
|---|---|
| `GET /` | HTML index |
| `GET /health` | `{"ok":true,...}` — `Std.Cache`-backed, inside a `Std.Trace` span |
| `GET /api/config` | what `Std.Config` decoded |
| `GET /api/token?sub=X` | issues an HS256 JWT (`Sky.Core.Jwt` builders) |
| `GET /api/me` | 401 without a valid `Authorization: Bearer`, 200 with |
| `POST /api/admin/token` | body = admin password (bcrypt); 401 on mismatch |
| `GET /api/admin/whoami` | verifies the admin token via `Std.Auth.verifyToken` |
| `GET /api/limited` | 429 after `limits.capacity` reqs — `Middleware.withRateLimit` |
| `GET /api/bucket` | 429 after `limits.capacity` reqs — `RateLimit.allow` in-handler |
| `GET /api/fetch[?url=]` | outbound `Http.request`; 502 + `Error.toString` on failure |
| `GET /upstream` | loopback target for `/api/fetch` |
| `POST /api/import[?delim=&file=]` | CSV bulk import (`Std.Csv`) |
| `POST /api/broadcast` | `Std.PubSub`; 503 + reason in a non-Live process |
| `GET /events` | bounded SSE — `stream.ticks` ticks then `done`, then EOF |
| `GET /ws` | WebSocket echo (`echo: <msg>`) |
| `GET /api/csrf` | `Middleware.withCsrf` probe |
| `GET /admin/stats` | `Middleware.withBasicAuth` + `Std.Cache` counters |

## The two kernel-alias arity shapes

Relay carries both shapes the v0.18.9 / #155 regression needs:

* **Variadic (over-count)** — `src/Handlers.sky:424-431`. A real
  `Http.defaultRequest |> Http.withHeader |> … |> Http.request |> Task.andThen`
  chain, with a live `Task.onError` arm that reaches `Error.toString`
  (`src/Handlers.sky:446-460`). `Http_request` is
  `func(firstArg any, rest ...any)` in Go — declared arity 1, scanned arity 2.
* **Non-variadic (counter-example)** — `src/Main.sky:93`
  (`cors h = Mw.withCors cfg.corsOrigins h`). `Middleware_withCors` is
  `func(origins, handler any)` — declared arity 2, scanned arity 2, so it must
  curry exactly as declared. `Mw.withRateLimit` (`src/Main.sky:128`) is the
  4-arg non-variadic case.

## Known compiler/stdlib findings surfaced by this app

1. **`Std.Config` applicative record decoding panics at runtime.** The shape in
   `Std.Config`'s own module docstring compiles clean and then panics with
   `TypeMismatch — rt.skyCallDirect: argument 0 type mismatch`. See the comment
   block at `src/Config.sky:126-146`. Relay decodes field-by-field instead.
2. **`Middleware.withCsrf` cookie name drift.** The docstring says
   `__Host-sky_csrf`; the runtime issues `__sky_csrf`. `csrfProbe` accepts both.
3. **`Std.PubSub.publish` is unavailable in a pure `Sky.Http.Server` process** —
   documented behaviour, surfaced as a 503 with the reason rather than hidden.
