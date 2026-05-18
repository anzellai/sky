# Sky.Live overview

> **v0.13 state**: typed Go output end-to-end. Whole-program Sky DCE
> prunes unused FFI bindings (Stripe-SDK scale: −82 % source). LSP 100 %
> coverage; runtime verification across all 26 examples. See
> [`../compiler/journey.md`](../compiler/journey.md) for the changelog.


**Server-driven UI with the TEA architecture** (`init` / `update` / `view` / `subscriptions`). Sky.Live lets you build interactive web apps where all state, logic, and rendering live on the server. The browser runs no client-side framework — just minimal JavaScript for DOM patching and SSE reconnection.

```elm
module Main exposing (main)

import Sky.Live as Live
import Html exposing (..)
import Html.Events exposing (onClick)


type Msg
    = Increment
    | Decrement


type alias Model =
    { count : Int }


init : () -> ( Model, Cmd Msg )
init _ =
    ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Increment ->
            ( { model | count = model.count + 1 }, Cmd.none )

        Decrement ->
            ( { model | count = model.count - 1 }, Cmd.none )


view : Model -> Html Msg
view model =
    div []
        [ button [ onClick Increment ] [ text "+" ]
        , span [] [ text (String.fromInt model.count) ]
        , button [ onClick Decrement ] [ text "-" ]
        ]


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


main =
    Live.app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        , routes =
            [ Live.route "/" HomePage
            ]
        , notFound = HomePage
        }
```

## How it works

1. **Initial page load:** Server renders `view model` as complete HTML. The browser receives a full static page, not a JS bundle.
2. **Event subscription:** Browser opens a Server-Sent Events (SSE) stream to receive updates.
3. **User interaction:** Click / input / submit triggers a minimal fetch to `/_sky/event` with a message payload.
4. **Server update:** `update msg model` runs on the server. The result is `(newModel, cmd)`.
5. **Diff:** Server diffs `view oldModel` against `view newModel` producing a VNode patch.
6. **Patch:** Patch is sent over SSE. Client-side Sky.js applies it to the DOM (< 2 KB gzipped).
7. **Command dispatch:** If `cmd` included `Cmd.perform task msgWrapper`, the task runs in a goroutine and its result is dispatched as a new `Msg` through the same loop.

See [architecture.md](architecture.md) for the detailed flow and session management.

## Advantages vs traditional SPAs

- **No client-side state.** No Redux, no React hooks, no "where does this state live" debate.
- **No JSON API layer.** You write Sky types once, not duplicated client + server contracts.
- **No bundler.** No Vite, no webpack, no npm audit alerts.
- **No fetch boilerplate.** Events are just messages.
- **Single binary deploy.** `sky build` produces one executable.

## When not to use Sky.Live

- **Offline-first apps.** Sky.Live requires a live server connection.
- **Heavy client-side computation.** The server is authoritative for all state; round-trips add latency for purely-local work (canvas animation, drag interactions).
- **Public-facing static content.** A plain `Sky.Http.Server` serving pre-rendered HTML is lighter if no interactivity is needed.

## Patterns

- Auth-gated pages: check `session` in `update` or in the route handler.
- Async work: `Cmd.perform (Http.get url) GotResponse` dispatches a task, the result comes back as `GotResponse (Result Error Response)`.
- Scheduled updates: `Sub.interval 1000 Tick` emits `Tick` every second.
- Multi-page: `routes` maps URL paths to route messages; `update` responds to navigation.

See [`examples/09-live-counter`](../../examples/09-live-counter/), [`examples/12-skyvote`](../../examples/12-skyvote/), [`examples/16-skychess`](../../examples/16-skychess/) for worked examples.

## Session stores

Sky.Live supports multiple backends for session state:

| Store | Configured via | Use case |
|-------|----------------|----------|
| `memory` | default | Single-instance dev / testing |
| `sqlite` | `[live] store = "sqlite", storePath = "./data.db"` | Single-instance prod |
| `redis` | `[live] store = "redis", storePath = "redis://..."` | Multi-instance deployments |
| `postgres` | `[live] store = "postgres", storePath = "postgres://..."` | Shared SQL backend |
| `firestore` | `[live] store = "firestore"` | Serverless GCP |

Configure in `sky.toml`:

```toml
[live]
port = 8000
store = "sqlite"
storePath = "./data.db"
ttl = 1800
```

## Connection status banner

Sky.Live's runtime injects a bottom-pinned banner the user's `view` doesn't have to manage. Three states:

| State | Trigger | Default chrome |
|-------|---------|----------------|
| `connected` | normal operation | `display:none` |
| `reconnecting` | SSE drops, POST `/_sky/event` fails, or proxy wedge detected | amber `Reconnecting…` (after 500 ms grace) |
| `offline` | `SKY_LIVE_RETRY_MAX_ATTEMPTS` consecutive retry failures | red `Connection lost — refresh to retry` |

After reaching `offline` the runtime keeps retrying SSE in the background at the max delay so a healed network recovers without a forced refresh. POST failures during the outage land in a FIFO queue (capped at `SKY_LIVE_QUEUE_MAX`) and replay automatically on reconnect.

### Reverse-proxy wedge protection

Some edges (Cloudflare without the right page rule, fly.io, custom Nginx) can rewrite an upstream 502 into a 200 OK with a non-SSE body, leaving `EventSource` to fire `open` and silently never deliver a frame. The user-visible symptom was the page pinned at `Reconnecting…` even after the server itself had recovered. The runtime defends against this on three layers:

1. **Server-side hygiene.** Every `/_sky/sse` response sets `X-Accel-Buffering: no`, sends a 2 KB padding line so proxy buffers flush, then immediately sends `event: hello\ndata: {"v":1,"sid":...}\n\n`. A heartbeat fires every 15 s. Every `/_sky/event` POST response carries `X-Sky-Live: 1`.
2. **Client SSE.** `connected` only flips on the `hello` event, never on raw `EventSource.open`. A 5 s watchdog tears down + reopens the stream if no hello arrives within `SKY_LIVE_HELLO_TIMEOUT_MS` (8 s default) or no heartbeat within `SKY_LIVE_HEARTBEAT_TTL_MS` (35 s default ≈ 2× heartbeat).
3. **Client POST.** A 200 OK without `X-Sky-Live: 1` is treated as a wedged proxy response — never applied as a patch, always rerouted through the retry path.

### Localising the banner

Override the banner strings via the `status` field on `Live.app`. No type signature change is needed — `Live.app`'s record is open via the kernel's `appExt` extension.

```elm
main =
    Live.app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ Live.route "/" HomePage ], notFound = HomePage
        , status =
            { reconnecting = "Reconnexion…"
            , offline = "Connexion perdue — actualisez la page"
            }
        }
```

Either field is optional — partial overrides fall back to the English defaults. Strings are JSON-encoded into the JS template (newlines, quotes, non-ASCII, emoji round-trip safely) and rendered via DOM `textContent`, never `innerHTML`, so user-supplied content can't break out of the banner context.

## Input preservation across re-renders

Sky.Live's input-authority protocol (full spec: `input-authority-protocol.md`) keeps the user's typing safe from server-driven re-renders. Three failure modes that previously slipped through the contract have been closed:

1. **Empty patches stay on the JSON ack path.** When the server-side diff aligns away every patch (the model advanced but the client already has the typed value — the steady-state outcome of typing in a controlled field), the response is an empty JSON envelope with seq + ackInputs metadata, never a full HTML body. Before this fix, empty patches triggered the HTML fallback, which `innerHTML`-replaced the entire `sky-root` and recreated every input — blanking uncontrolled fields like password.

2. **Full-body swaps preserve every uncontrolled input.** When a full HTML replacement is genuinely needed (legitimately structural diff, navigation, first interaction), the runtime now walks every `<input>` / `<textarea>` / `<select>` in the live container and splices any whose server-rendered placeholder is uncontrolled (no `value` / `checked` / `selected` attr) across the swap. The previously-special focused-input preservation is unified into the same loop. Result: an unfocused password field survives across SSE-pushed full-body re-renders. Controlled fields still let the server win — the existing authority discipline is preserved.

3. **Open `<select>` defence.** Native dropdowns close on any DOM mutation in their subtree or in an ancestor that re-mounts them. While `document.activeElement` is a `<select>`, both the per-element patch handler (`__skyApplyPatches`) and the SSE patch handler (full-body) skip patches that touch the SELECT or any element that contains it (or is contained by it). The next user interaction (option click, blur) triggers reconciliation. Active user paths (sky-nav clicks, popstate, POST text fallback) are deliberately NOT defended — dropping them would freeze navigation. Trade-off: while a dropdown stays open, scheduled re-renders accumulate "pending" state on the server until the user blurs the SELECT.

These rules play together. **For password / secret fields specifically**: don't round-trip the value through `Model`. The form-submit pattern — `onSubmit DoSignIn` with a typed-record `args : LoginForm` — is canonical. The server never sees the secret in `Model` (so it never enters the session store) AND, with the preservation rules above, never accidentally blanks it on a server-driven re-render.

### Env-var namespace prefix

Sky.Live reads its config from env vars under the `SKY_` prefix by default — `SKY_LIVE_PORT`, `SKY_LIVE_STORE`, `SKY_LIVE_TTL`, etc. Two Sky binaries running on the same host share that namespace, which is fine for most setups but causes collision when each binary needs a different port/store/TTL.

Switch the binary's namespace via `sky.toml`:

```toml
[env]
prefix = "FENCE"
```

The runtime then reads `FENCE_LIVE_PORT`, `FENCE_LIVE_STORE`, `FENCE_AUTH_TOKEN_TTL`, etc. The `.env` file and shell env you supply use the prefixed names too. The prefix is trimmed of any trailing `_` so `prefix = "FENCE"` and `prefix = "FENCE_"` are equivalent.

What's affected:
- All Sky-internal namespaces: `LIVE_*`, `AUTH_*`, `LOG_*`, `DB_*`, `ENV`, `STATIC_DIR`.
- The corresponding sky.toml-derived defaults (`SetSkyDefault` calls in the generated `init()`).

What's NOT affected:
- User-supplied env-var names passed to `System.getenv` / `System.getenvOr` etc. — those read raw.
- Standard non-Sky fallbacks: `DATABASE_URL`, `REDIS_URL`, `PORT` (consulted by Sky.Live's session-store config when the prefixed override is unset).
- The compile-time-only `SKY_SOLVER_BUDGET` knob, read by the Haskell compiler itself.

Backwards-compatible: omit `[env] prefix` and behaviour matches every prior Sky version exactly.

## Dev console — auto-mounted at `/_sky/console`

Every Sky.Live (and Sky.Http.Server) app **auto-mounts a Std.Ui-written dev console at `/_sky/console`** in dev mode. A floating "🔍 Console" anchor injected into every rendered page links straight to it. Zero user code needed.

The console is a fully-isolated Sky.Live mini-app (`sky-bundled/console/`) spawned as a child process and reverse-proxied behind your app — same port, same origin, no shared state, no Sky.Live wire collision. The child dies when the parent exits.

**Production gate**: console + banner are gated on the same `productionFromEnv()` rule that governs `/_sky/metrics` auth. `ENV` (then `SKY_ENV`) unset OR set to `dev` / `development` / `local` → console on. Anything else (`production`, `prod`, `staging`, `qa`, `preview`, …) → console off, banner gone, `/_sky/metrics` gated. Intentionally bias-to-gate: if you bother setting `ENV` at all, you mean it.

```bash
# Dev — console at http://localhost:PORT/_sky/console
sky run

# Staging / prod — console + banner suppressed
ENV=production ./sky-out/app

# Ad-hoc standalone (no host app needed)
sky console            # browser at :8025
sky console --tui      # ... or terminal via Sky.Tui
```

### Env knobs

- `SKY_CONSOLE_EMBED=off` — opt out of the auto-mount.
- `SKY_DEV_BANNER=off` — suppress the floating link without unmounting.
- `SKY_CONSOLE_URL=<url>` — override the banner's `href` (e.g. point at a remote dashboard). Default: `/_sky/console` relative to same origin.
- `SKY_SUBAPP_VERBOSE=1` — surface spawned children's stdout / stderr for debugging.
- `SKY_BIN=<path>` — override the `sky` binary used by the auto-spawn (defaults to `exec.LookPath("sky")`).

## Sub-app mount — host any Sky app under a URL prefix

The console is the first user of the runtime's general-purpose `rt.MountSubApp` API. The same primitive lets you mount any other Sky app (or arbitrary HTTP server) under a path prefix on your parent's mux. Each sub-app runs as its own child process — independent session store, independent observability, zero shared state — but the user sees a single port and the OS sees a single process tree.

```go
import "sky-app/rt"

// Mount external Sky binaries:
rt.MountSubApp(mux, "/billing", rt.SpawnBinary("./billing-app"))
rt.MountSubApp(mux, "/admin",   rt.SpawnBinary("./admin-app"))

// Or any non-Sky HTTP server (any binary that listens on a localhost port):
rt.MountSubApp(mux, "/docs", rt.SpawnBinary("./hugo-server"))
```

A Sky-side ergonomic API (`Live.app { subApps = [Live.subApp "/admin" "./admin-app", ...] }`) is on the v0.14 list. The Go-side API is the contract for v0.13.x.

### Sub-app-aware `SKY_LIVE_BASE_PATH`

When a Sky.Live runtime starts with `SKY_LIVE_BASE_PATH` set (which `MountSubApp` does automatically for child processes), four behaviours flip:

1. `<meta name="sky-base" content="<prefix>">` injected into every page wrap. The inlined JS reads `__skyBase` and prefixes every hardcoded `/_sky/event` / `/_sky/sse` / `/_sky/config` URL — without this, sub-app fetches would hit the parent's wire endpoint and silently drop.
2. Dev banner suppressed (no recursive "click for console" inside the console).
3. `MountObservabilityEndpoints` skipped (the parent owns `/_sky/{healthz,readyz,metrics,buildinfo}` — sub-app duplicates would just pollute).
4. `maybeAutoMountConsole` early-returns (sub-apps don't get to spawn sub-apps — fork-bomb prevention).

Set `SKY_LIVE_BASE_PATH` manually for advanced reverse-proxy setups where Sky.Live runs behind a fronting Nginx / Cloudflare / Envoy that does the prefix-strip.

### Process supervision + shutdown

`MountSubApp` registers each child in a process-tree-wide registry. Sky.Live's existing SIGINT/SIGTERM/SIGHUP handler calls `rt.ShutdownSubApps()` before `srv.Close`; Sky.Http.Server gained a minimal handler specifically for this. Children get 2 s of SIGTERM grace then SIGKILL.

Children spawn in their own process group (`Setpgid: true`) so a Ctrl-C on the parent's terminal doesn't double-kill — the parent's handler tears them down cleanly via context cancel. If the parent crashes hard (SIGKILL, OOM), children orphan — best-effort cleanup, not a hard guarantee.

### What's NOT shipped yet

Sub-app metrics federation into the parent's `/_sky/metrics` with namespace prefix (so `billing_requests_total` etc. surface alongside parent metrics on a single Prometheus scrape) — planned for v0.14. Until then, sub-app metrics are accessible at `<prefix>/_sky/metrics` through the proxy after setting `SKY_LIVE_OBSERVABILITY=on` on the child; aggregate in your scraper.
