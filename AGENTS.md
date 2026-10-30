# AGENTS.md

> Agent-agnostic guide for AI tools (Claude, Copilot, Cursor, Codex, …)
> working in the Sky repository. It is the **source of truth**; tool-specific
> files layer on top of it (Claude Code reads `CLAUDE.md`, which imports this
> file and adds Claude-Code-only operational rules).
>
> **Progressive disclosure.** This file is deliberately lean. It teaches the
> language, the app-building decisions, and the non-negotiable rules, then
> points you at two authoritative, always-current sources for depth:
>
> - **`sky doc <Module>`** — the live stdlib API (typed signatures + summaries +
>   examples), generated from the stdlib source, so it never drifts. Prefer it
>   over any hand-copied API table. `sky doc --list` enumerates every module;
>   `sky doc --serve` opens a browsable server.
> - **`docs/`** — deep dives (architecture, Sky.Live, Std.Db, Std.Ui, auth,
>   tooling). The map is in the [Deep dives](#deep-dives) table below.

## What Sky is

Sky is an Elm-family, purely-functional language that compiles to **typed Go**.
One language for the whole stack: web UIs (Sky.Live), HTTP/JSON APIs, CLIs, TUIs,
desktop apps, background jobs. The design goal is **"if it compiles, it works"** —
no user-written FFI, no nulls, no **silent** wrong answers, and no runtime panic
from well-typed code, clear errors, batteries-included stdlib.

**The one honest carve-out: partial arithmetic primitives fail LOUDLY, never
silently.** A well-typed `10.0 / 0.0` has no answer Sky can represent (there is
no `Inf`/`NaN` shape), so it raises a *classified* `DivisionByZero` — recovered
per-request to a 500 in a server, or exit(1) + a structured log in a CLI — and
is caught by the whole-app panic net (`docs/architecture/sky-stdlib-correctness.md`
§2.9; `classifyPanic` in `runtime-go/rt/panic_recover.go` names the ten panic
classes). It is never a silent wrong value (integer `//`/`%` by zero *are* total
and return 0, matching Elm). `Json.Encode.encode` of a NaN or infinite `Float`
fails the same way, as a classified `JsonEncodeFailure`, never an empty
string. Code that divides by a value it does not control should zero-guard the
divisor; a fallible `Maybe`-returning division is an offered convenience, not a
soundness requirement. "No runtime panic" therefore means *no UNCLASSIFIED panic and no
silent corruption* — the property a service actually needs.

The compiler is the **Rust rewrite** (cargo workspace at `rust/`). The retired
Haskell compiler, once its byte-for-byte differential oracle, was removed from
the tree in v0.29.0 (`docs/history/legacy-compilers.md`; tag `v0.28.1` is the
last that has it). Current line: **v0.29.x**.

## Language essentials

Sky's surface is Elm. If your training on Elm is thin, read this section
carefully — it is the part that catches models out.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)      -- Result/Maybe/identity/… autoloaded
import Sky.Core.List as List
import Std.Log exposing (println)

type alias User = { name : String, age : Int }   -- record alias
type Msg = Increment | Decrement                 -- tagged union (ADT)

greet : User -> String                            -- type annotation (optional but preferred)
greet u =
    "Hi " ++ u.name

update : Msg -> Int -> Int
update msg count =
    case msg of                                   -- case is exhaustiveness-checked
        Increment -> count + 1
        Decrement -> count - 1

main =
    println (greet { name = "Ada", age = 40 })
```

Core syntax you must get right:

- **Pipelines**: `x |> f |> g` (left-to-right), `f <| x` (right-to-left).
- **Lambdas**: `\x -> x + 1`. **Cons**: `head :: tail`. **Lists**: `[ 1, 2, 3 ]`.
- **`let … in`** for locals; **`case … of`** for pattern matching (must be exhaustive).
  A let-bound function is polymorphic, like a top-level one (let-generalisation
  under the value restriction, `docs/language/types.md`). Local functions of one
  `let` may call each other (mutual recursion), in any source order.
- **Field access**: `model.user.name`; `Shape.origin.x` is field `x` of the
  qualified value `Shape.origin`.
- **Record update**: `{ user | age = 41 }` (keeps every other field).
- **Import aliasing**: `import Std.Db as Db exposing (Store)`.
- **Multiline strings**: triple-quoted with `{{expr}}` interpolation; `\{{` escapes.
- **Negative literal args**: `f -1` parses as `f (-1)`; `f - 1` is subtraction.
- **Int to Float**: `toFloat n` (Prelude, as in Elm). Float to Int: `Math.round` / `floor` / `ceil` / `trunc`.
- **Reserved words**: `module exposing import as type alias if then else case of let in`.
  One of them used as a name is one parse error that names it. `foreign`, `where`
  and `port` are ordinary names.
- **JSON**: `Pipeline.optional name dec default` gives `default` only when the
  field is absent or `null`; a present value `dec` rejects is an `Err` (since
  v0.27.12). `Decode.nullable` / `Decode.maybe` / `Decode.null` are as in Elm.
- No custom operators, no `where` clauses (use `let`), no higher-kinded types (HM only).

**Collection costs (v0.29.0) — every `List` / `Dict` / `Set` / `Array` /
`String` function states its cost in a `-- Cost:` line (`sky doc`).** `x :: xs`
is amortised O(1), so the accumulator loop `go (x :: acc) rest` plus one
`List.reverse` is O(n); replacing a head or consing twice onto one shared tail
copies (O(n)). To build a string, cons the pieces and call `String.concat
(List.reverse pieces)` once (`++` in a loop is O(n²)). Walk a string with
`String.uncons` / `foldl` / `toList`; every String index counts code points.
Use `Sky.Core.Array` for indexed access (`get` / `set` O(log32 n), `push`
amortised O(1)). A `Dict` write copies the map (O(n)): for many entries build
a list and call `Dict.fromList` once. Go code reached through FFI must treat a
list argument as read-only (`docs/ffi/boundary-philosophy.md`).

**Effect boundary — the single most important rule.** Every observable side
effect returns `Task Error a`. Pure code is bare `a`; fallible-pure is
`Result e a` / `Maybe a`; effects are `Task Error a`. Never use `String` as an
error type — always `Result Error a` / `Task Error a`.

```elm
-- pure          : String.length, List.map, Crypto.sha256
-- fallible-pure : String.toInt : String -> Maybe Int
--                 Encoding.base64Decode : String -> Result Error String
-- effect        : File.readFile, Http.get, Db.query, Time.now → Task Error a
-- Go FFI        : any `sky add` binding                   → Result Error a
```

**Go FFI is the one exception, on purpose.** A `sky add` binding returns
`Result Error a`, not a Task: the Go call runs where the expression is
evaluated, and the Result is its outcome (a Go error, a nil or a recovered
panic is `Err`). Handling it at each call site marks where the code leaves
Sky's guarantees, so prefer the stdlib and keep FFI for what it does not cover.
To run a call later, off `update`, wrap it yourself:
`Task.lazy (\_ -> Pkg.call args) |> Task.andThen Task.fromResult`. The checker
enforces the `Result` from the binding's pinned signature (v0.27.0): using a
call's result as the bare value is an `[E2001]` type error, and `Sky.Ffi`
(`kernel` / `call` / `callPure` / `callTask`) is stdlib-only (`[E1011]`, under
any qualifier: `Webview.kernel` is refused too): call the typed stdlib function
or a `sky add` binding. A fetched `.skydeps` package is type-checked like your
own code on every build and gets no `Sky.Ffi`. The Go surface (format 3, from
`sky install`) types every value by its Go type: a Go value with no Sky form is
its own nominal type (`Pkg.Router`), never usable at another type; a Go pointer
to a non-opaque type is a `Maybe` (nil is `Nothing`) in every position; a map
keeps its `int` / `float` / `bool` key type; a callback is typed from the Go
signature, result included; an integer out of range across the boundary is an
`Err`; and a Sky value where Go wants an interface is `[E2013]`. See
`docs/ffi/boundary-philosophy.md`.

**Upgrading a v0.28 project?** `docs/migration/v0.29.md` lists every change,
split into loud (fails to compile, the error shows the fix), loud at run time
and silent. Among them: `Sky.Core.Regex` takes a compiled `Regex`
(`Regex.fromString` is the only constructor, a literal pattern is checked at
compile time, `[E2014]`), a new stdlib name that two `exposing (..)` imports
both export is `[E1012]` (qualify it), a value defined in terms of itself is
refused, `Std.Image` refuses images over its decode limits, and a Sky.Live
session in a persistent store whose model holds a `Dict` starts fresh once
after the upgrade.

**Upgrading a v0.26 project?** `docs/migration/v0.27.md` lists every change
that stops the build (the error links its entry) and every change that
compiles but behaves differently, one bullet each. Among them: the value
restriction (`[E2012]`: a top-level value with no parameters is not
polymorphic inside a `Ref`, `Cache`, `Codec` or `Table`), `any` in your own
annotation is a hole the body fills (not a cast), ordering needs a
`comparable` type, `Codec.auto` / `App.withDurable` / `Auth.signToken` need an
encodable type (no function, `Secret`, key or handle), `Crypto.aesGcmEncrypt`
/ `chacha20Encrypt` return a `Task`, `Auth.verifyToken` returns `Result Error
Json.Value`, and a `Cookie` value goes through `Server.addCookie`.

`let _ = someTask` auto-forces the task (fires the effect). A top-level
zero-arg binding is **memoised** (a CAF — evaluated once, cached): `db =
Task.run (Db.connect ())` opens one shared pool; but `newId = Task.run Uuid.v4`
freezes to ONE uuid — for a fresh value per call make it a function
(`newId _ = …`; call `newId ()`). The compiler warns when a memoised binding
forces a fresh-value kernel (Uuid/Random/Time/Crypto.random).

Full effect surface, bridges (`Task.fromResult`, `Task.onError`, …), and the
two-level error pattern: `docs/stdlib.md` + `sky doc Sky.Core.Task`.

**Effect and control-flow style — flat and named, not nested.** `Task` code
chains with `andThen` / `map`. There is no do-notation and no `let!` bind, so
`let x = someTask in …` binds the TASK value, not its result — `let … in` names
PURE sub-expressions only (a key, a record, a string). Keep a chain readable by
keeping the pipeline flat and naming its steps; right-drift (each `andThen (\x ->
…)` holding the next `andThen` in its body) is the smell. The levers: a flat
`t |> andThen step1 |> andThen step2 |> map finish`; a NAMED continuation over a
long inline lambda (lift it out once it passes ~8 lines or holds another
`andThen`); `map` for the terminal pure transform (never `andThen (\x -> succeed
(f x))`); `map2` / `andMap` for INDEPENDENT tasks; `sequence` / `parallel` for a
LIST of effects (`Task.sequence [ t1, t2, t3 ] |> Task.map (\_ -> ())`, not three
nested `andThen`); and a local `bestEffort t = t |> Task.onError (\_ ->
Task.succeed ())` / `unless cond t = if cond then Task.succeed () else t` for the
two shapes the stdlib does not name. `templates/AGENTS.md` carries the worked
before/after for scaffolded projects.

**Recursion through `Task.andThen` is stack-safe; `Task.loop` is the clearer
form for a long loop.** Since v0.27.0 a Task is data run by one interpreter (the
Task trampoline, `runtime-go/rt/task_trampoline.go`), which pops an `andThen`
before it runs the task its continuation returns. So
`step n = work |> Task.andThen (\_ -> step (n + 1))` runs at a constant Go
stack, and two million iterations finish (before v0.27.0 they died with a fatal
stack overflow that no error handler saw). The same holds for recursion through
`Task.onError`, and for `Task.sequence` over a long list. Prefer
`Task.loop step initial` when the loop's state is explicit — `step : state ->
Task e (Step state a)` returns `Loop newState` to go again or `Done result` to
stop (`import Sky.Core.Task as Task exposing (Step(..))`) — and `Task.forever
task` for a loop that only stops on an error (a poller, a serve loop). Two limits
stay: a recursive call that is NOT behind a continuation (`step n = step (n - 1)
|> Task.map f`) is evaluated while the task is BUILT, which is ordinary strict
recursion and grows the stack as it does in Elm; and a recursion that maps its
own result (`\_ -> step (n + 1) |> Task.map f`) keeps one pending `map` per level
on the heap, which the semantics require.

**A Task program reads a WebSocket with Tasks, and can host a Live app.** Outside
a TEA loop, read a `Sky.Core.WebSocket` with `WebSocket.receive` /
`receiveWithin ms` / `forEachMessage` (a socket has one reader: a Sub or a Task,
never both). To serve a Sky.Live UI from a program that also does other work,
mark the app `App.withEmbedded` and `Task.spawn (App.run app)`: it then installs
no signal handler and never exits the process, and a failure to start is its
Task's `Err` (`docs/skylive/embedded.md`). To also STOP it, use `App.serve app`
(succeeds once listening, returns an `App.Running`), `App.address running`
(`host:port`; port `0` picks a free one) and `App.stop running` (graceful,
bounded, idempotent); each served app has its own session namespace (cookie
`sky_sid_<name>`, named by `App.withName "<name>"`, else by its port; port 0
with a durable store needs the name), and the Sky Console is one per process. A server that needs the frame type uses
`Ws.withOnFrame` (`Text` / `Binary`) instead of `withOnMessage`. A Sky.Spa
client (`web:app` and the other client targets) holds its own socket over the
browser WebSocket API: `WebSocket.connect "/ws"` runs in the client and is
same-origin (it passes `connect-src 'self'`); serve it with `App.api "GET /ws"`
and `Ws.upgrade`. A Sky.Spa client runs every Msg once, in arrival order, and
several server RPCs can be in flight together. A server arm whose model write
reads no server data runs async: the client makes that write when the Msg
runs, also through `case` / `if` / `let` and top-level helpers called with all
their arguments, and a `Cmd.none` branch sends no request and dispatches
nothing, as on Sky.Live. An arm whose model write needs server data (or reads
the signed session) holds later Msgs until it answers; the build prints a
`holds later Msgs` note naming each such arm and the change that makes it
async (`docs/skyspa/overview.md`, "Msg order and server calls"). While input
Msgs wait behind a hold, the client does not write that control's value (the
box keeps its letters and caret), and an input Msg the split proves commutes
with the hold (`Spa.withHoldFree`) runs at once.
The client re-sends a transiently failing request by itself (no answer, a
30 s timeout, 408 / 425 / 429 / 502 / 503 / 504; full-jitter backoff, `Retry-After`
wins, in failure order, 8 attempts or 60 s), shows a quiet "Reconnecting…" only
after 3 s and the red Retry bar only once the budget is spent, so
`App.withRpcError` receives FINAL errors only: write no app-level network
banner. `Sub.connection` (`Online | Reconnecting | Offline { pending }`) is for
an app that wants its own indicator. `Sub.every` is a wake-up, not a clock:
use `Sub.everyWithTime` and read the time the tick carries; a tick whose
update calls the server is coalesced and held back while the page is hidden
(`docs/skyspa/overview.md`, "Network resilience").

**Request host and binary data.** A `Sky.Http.Server` handler, an `App.api`
route and the `App.withRequest` hook get `req.host`: the name the client used,
port included (`"a.localhost:8000"`), also in `req.headers` under `"Host"`. An
app that serves several domains reads it. `Server.bytes contentType bytes`
answers binary data byte for byte, and `File.writeBytes path bytes` writes it
(a `Bytes` is a `String` whose bytes need not be UTF-8, e.g. from
`Bytes.fromBase64`). `List.repeat n x` is `n` copies of `x`, as in Elm.

**A host that cannot keep cookies** (a native shell whose custom scheme drops
`Set-Cookie`, some embedded web views) uses `App.withSessionTransport
HeaderToken` (or `SKY_LIVE_SESSION_TRANSPORT=header`): the Sky.Live session id
travels in the `X-Sky-Session` header, the server stores only its hash, and the
header is the CSRF defence. Sky.Live only (a `web:app` build refuses it), and a
full page reload starts a new session (`docs/skylive/architecture.md`).

## Writing a Sky app — interview first, then architect

**You are the front line. Lead with questions — do not guess.** When a user
says "build me X in Sky", your first job is not to write code; it's to
understand *what they are actually building* and *what it has to survive*, then
propose the scope + architecture. Production-grade code does not survive
guesswork, and over-engineering a weekend project wastes everyone's time. Ask
**one focused question per genuine ambiguity**, propose sensible defaults for
the rest, and confirm the shape before building beyond a proof of concept.

### Question 0 — what is this for? (the tier drives everything)

Ask this first. A single answer reshapes most of the decisions below:

| Tier | Meaning | Leads to |
|---|---|---|
| **Prototype / pet / internal** | one user or a trusted few, runs on one machine, "lost on restart is fine", ships today | `sqlite` (or `memory`) · session store `memory`/`sqlite` · auth optional · single local binary or one VM · local logs + the embedded console · **no** Postgres/Redis/K8s ceremony |
| **Production** | real users depend on it, must survive restarts + scale + failures, has an SLA | Postgres · **shared** session store (redis/postgres) · `Std.Auth` (or OAuth) · sticky sessions + cross-instance pub/sub if multi-replica · `ENV=production` gate · structured logs + OTel / console hub · file migrations committed |

Don't force production ceremony on a pet project, and don't let a "quick
internal tool" that's clearly headed for real users ship on `memory` sessions.
If the user is unsure, ask what happens if the process restarts and how many
people will use it — that usually settles the tier.

### The other decisions to confirm

Once the tier is known, most of these have an obvious default — surface the
non-obvious ones as questions:

1. **App shape** — the biggest fork. Web UI? headless API? terminal? desktop?
   one-shot job? (see the matrix below). Full-stack (server-driven UI) is Sky's
   sweet spot — **Sky.Live** — one language, no separate front-end. Reach for
   **Sky.Http.Server** only when there is no browser UI, **Sky.Tui** for a
   terminal UI, **Sky.Cli** for a job/CLI, **Sky.Webview** for desktop.
2. **Persistence** — SQLite (embeds, single-file — great default for tier-1) /
   PostgreSQL (multi-instance) / Firestore / Redis / none.
3. **Auth** — none / `Std.Auth` (you own users) / OAuth / external (Auth0/Clerk).
4. **Sky.Live session store** — memory (dev only) / sqlite / redis / postgres.
   *Required even when the primary DB differs.* Shared store the moment there is
   more than one replica.
5. **Deployment target** — local binary / Docker / Cloud Run / Kubernetes / VM.
   Ask what the host *is*, because a Sky.Live app plus its own embedded
   PostgreSQL fits comfortably in **1 GB**, and most people over-provision.
   The measured facts, each with its run, commit and caveats, are in
   **`docs/perf/sizing.md`** (moved there from this file in v0.29.0). The
   short form:

   - An idle app binary is **~21–27 MB**; a settled app under real traffic
     **~56 MB**. Embedded PostgreSQL adds **+21.9 MB** idle, **+28.4 MB** with
     sessions written through it. The whole machine before sessions measured
     **~382 MB** (app alone) and **~410 MB** (with embedded PostgreSQL).
   - PostgreSQL is a fixed block (a flat 6-connection pool), not a per-session
     tax.
   - **CPU binds ≈9–18× before memory.** An e2-micro holds ~450 sessions but
     is unusable past ~50; an e2-small sustains 64.3 int/s at 300 sessions
     (commit `3ed83c08`). Count physical cores, not vCPUs.
   - The runtime derives `GOMEMLIMIT` and sets `GOGC=400` under it; an explicit
     `GOGC` / `GOMEMLIMIT` always wins. There is no `sky.toml` knob.
   - On a burstable e2 instance the first run after idling overstates sustained
     capacity by ~2.7×. Plan with the sustained figure.
   - Interaction cost tracks view size (`view` re-runs in full), not the diff.
   - A single `--embed` instance has no replica and no backup timer: a
     `pg_dump` schedule is the operator's to add.

   Quote a per-session number **with its view size and its `GOGC`**, or it
   will be wrong. Never quote a number no run in `docs/perf/runs/` measured.
6. **Observability** — local logs + embedded console / central console hub /
   OTel collector (Honeycomb / Tempo / Datadog).

When in doubt, ask one focused question rather than heroically guessing — then
build with the stdlib defaults, which already make the app survive a restart,
scale horizontally, refuse cross-tenant reads, and emit traceable logs *when the
tier calls for it*.

### App-shape matrix

| User wants | Use | Entry point |
|---|---|---|
| Web app (forms, real-time, UI state) | **Sky.Live** | `App.app {…}` + `--target web` (default) |
| Client-rendered SPA / cross-platform client loop | **Sky.Spa** | `App.app {…}` + `--target web:app` (or `desktop`/`mobile`) |
| HTTP/JSON API (no browser UI) | **Sky.Http.Server** | `Server.listen 8000 [...]` (several ports: `Task.spawn (Server.listen 9001 [...])`; a failure to start is the Task's `Err`) |
| Background job / cron | **Sky.Cli** | `main = Task.run work` |
| Terminal UI | **Sky.Tui** | `App.app {…}` + `--target terminal:tui` |
| One-shot CLI | **Sky.Cli** | `main = Task.run cliCmd` |
| Desktop app | **Sky.Webview** | `App.app {…}` + `--target desktop` (macOS in v0.1) |
| WebSocket feed | **Sky.Http.Server.WebSocket** | `Server.upgrade req` |
| SSE / token stream | **Sky.Http.Server.Stream** | `Server.Stream.emit` |
| One source, many targets (web/terminal/desktop) | **Std.App** | `App.app { init, update, view, subscriptions }` + `--target` |

**`Std.App` — the unified builder (the cross-platform default; `Std.Ui` view).**
For an app that should run across several shapes from ONE source, write
`app = App.app { init, update, view : model -> Element msg, subscriptions }
|> App.withNotFound … |> …` and `main = App.run app`; a build-time `--target
family[:variant]` picks the backend (optional — bare defaults to `web`). You never
import `Std.Live`/`Spa`/`Tui`/`Cli`/`Webview` yourself. Delivery = family, native =
platform: `web` · `tablet` → **Sky.Live**; `desktop` → Sky.Live in a native
window; `terminal:tui|cli` → **Sky.Tui/Cli**; `web:app` · `desktop:mac|windows|
linux` · `tablet:ipad|android` · `mobile:ios|android` → **Sky.Spa** (client wasm).
An app that uses `Std.Ai.Device` is refused on `desktop:windows|linux` (the
desktop device engine runs behind the macOS window only): use `web:app` there.
The build rewrites `App.run` → the target's `run<Backend>` (DCE prunes the rest)
and, for the Spa targets, **synthesises a `Spa.app` from your `App.app`** and feeds
the existing auto-split — so client targets need **no** separate `Std.Spa` entry.
The native shells (`mobile:ios|android`, `desktop:<os>`) load the backend at
`App.withAppUrl "https://app.example.test/"` (a literal or top-level `String`
constant, read at build time), else `SKY_APP_URL` at build time (which wins over
the builder), else the dev default `localhost` / `10.0.2.2` / `127.0.0.1` on
`PORT` (8951). A device that must hold its own end-to-end keys (Noise, CPace,
Kx, Sign, Kdf) opts in with `App.withClientCrypto`: those run in the wasm client
and the build refuses any flow that would send a key to the server (a key field
must be a `Maybe`, which the first paint and the saved model leave out); see
`docs/skyspa/client-crypto.md`. Permissions carry their purpose string
(`Bundle.withUsage Bundle.Camera "…"`; a `Native.authenticate` / `capturePhoto`
/ `geolocation` / `scanCode` call without its permission fails the iOS and
Android builds, naming the `bundle` binding),
entitlements are typed (`Bundle.withEntitlement (Bundle.AppGroup "group.…")`),
`Native.secureSet`/`secureGet`/`secureRemove` (a `Secret`, Keychain / Android
Keystore, `Err Unavailable` in a browser, never `localStorage`) and
`Native.authenticate` (biometrics) and `Native.scanCode` (a QR code or barcode
from the camera: VisionKit on iOS, ZXing on Android; `Err Unavailable` in a
browser and on the iOS simulator) reach the device, and `sky package --release
--target mobile:ios|mobile:android|desktop:mac` makes the signed store artefact
(signing from `SKY_IOS_*` / `SKY_ANDROID_*` / `SKY_MACOS_*`; the dev backend
address is refused); `--upload testflight` then validates and uploads the
signed `.ipa` to App Store Connect with `xcrun altool` (API key from
`SKY_ASC_KEY_ID` / `SKY_ASC_ISSUER_ID` / `SKY_ASC_KEY_PATH`; needs
`Bundle.withId` and a `Bundle.withBuild` that rises per upload). See
`docs/skyapp/native.md`. An editor's "Insert" menu puts text at a textarea's
caret with `Native.insertAtCursor "body" "{{first_name}}"` (`Task Error String`,
the control's new value; it uses the selection the person left when focus moved
to the menu; a Sky.Spa client only, `Err Unavailable` on Sky.Live).
`Native.selection "body"` reads a control's selection (`{ start, end, text }`,
in characters) and `Native.setSelection "body" s e` sets it, with the same rules.
`web` requires `App.withNotFound` (compile-enforced). Invalid combos are rejected
at parse time (`web:ios` → *"did you mean `mobile:ios`?"*). `sky check` checks
the same target a bare `sky build` builds (`--target`, else `[app] target`, else
`web`), so check ≡ build. For a `Std.Html` view use
`App.web` instead of `App.app` (same builders, HTML family only); for a
hand-authored terminal `view : model -> String` use **`App.cli`** (line-oriented)
or **`App.tui`** (full-screen) — terminal-only, so pin the backend with `[app]
target = "terminal:cli"` (or `"terminal:tui"`) in `sky.toml` and a bare `sky
build`/`run` picks it. See `docs/skyapp/overview.md` + `sky doc Std.App`.

### Pinned defaults — the preferred way to write Sky

**Default to these. Deviate only when the user explicitly asks for something
else, or the use case genuinely rules the default out.** They are what keep
AI-written Sky consistent, secure, portable, and reviewable — each was chosen
for UX/DX/security/scalability, not by accident.

| Concern | Default (and why) |
|---|---|
| **View layer** | `Std.Ui` — the typed, no-CSS layout DSL — **not** `Std.Html`. The same `Element` view renders across **Sky.Live (web), Sky.Tui (terminal), and Sky.Webview (desktop)**, so one view function is cross-platform; `Std.Ui` also HTML-escapes everything. Reach for `Std.Html` *only* to wrap raw markup `Std.Ui` can't express. `Ui.text` outside a paragraph is its own wrapping box (`Ui.textNoWrap` keeps one line). **`Std.Ui.Canvas`** draws typed 2D scenes (SVG on Sky.Live, a batched `<canvas>` on the Sky.Spa client, Braille cells on a terminal; pointer events in scene units; `label` required). **`Std.Ui.Terminal`** binds a `Process.withPty` child to a built-in terminal widget (Sky.Live and desktop; a `web:app` build refuses it) — gate it behind sign-in. See `docs/skyui/overview.md`. |
| **Database** | **Tier-driven.** Pet / prototype / simple / single-instance → **SQLite** (embeds, single file, zero ops). Production / multi-instance → **PostgreSQL**. (Future: **BlueDB** — the Sky-native reactive data layer, WIP — will become the default once it lands; prefer it when it ships.) Always model records through **`Std.Db.Store` + `Std.Codec`** (one codec drives JSON + dialect-safe DB); grouped aggregates (`groupBy` + `aggregate`), projections (`select`), the newest row per key (`latestPer`), escaped text match (`contains` / `startsWith` / `endsWith` / `eqCi`), JSON keys (`jsonText`), joins of two stores (`join` / `leftJoin` → `toPairs` / `toPairsMaybe`) and subquery conditions (`inQuery` / `notInQuery` with `column`, `exists` / `notExists` with `correlate`) are Store builders with checked names; drop to raw `Std.Db` only for transactions / SQL the builders do not model (a join of three tables, a CTE). The *same app code* works on SQLite and Postgres — only the driver differs. |
| **Auth** | The internal **`Std.Auth`** module by default (bcrypt + HS256 JWT cookies — you own the users). OAuth (Google/GitHub) or external (Auth0/Clerk) only when the user needs them. Never `fmt.Sprintf("%v", secret)` — secrets are typed. |
| **Serialization** | `Std.Codec` (`Codec.auto blank`) for record↔JSON+DB from one definition. Raw `Json.Encode/Decode` only for a shape a codec can't express (legacy/third-party wire formats). The same codec reads and writes XML: `Codec.toXml "root" codec value` / `Codec.fromXml "root" codec text` (server side). |
| **XML / ZIP** | Read XML with **`Std.Xml`** (`parse` → a `Node`; safe by default: no DOCTYPE, no entities, bounded size / depth / nodes / attributes, UTF-8 / Latin-1 / Windows-1252) and decode it with **`Std.Xml.Decode`** (`Json.Decode` style, by local name, errors carry a path such as `/feed/entry[3]`); write it with **`Std.Xml.Encode`** (`element` / `text` / `cdata`, `encode` is total, `encodeStrict` refuses). Read and write archives with **`Std.Zip`** (`read` / `readEntry` / `list` / `write`, `Result` not `Task`; refuses zip-slip names, symlinks, encrypted entries and zip bombs under `Limits`; `write` is deterministic). All run on the server (a Sky.Spa split sends them to the backend). Failures are `InvalidInput` with the domain `"xml"` / `"zip"` and a code. |
| **Excel / Word files** | **`Std.Office.Xlsx`** reads a workbook (`read` → sheets of `Cell`: `Text` / `Number` / `Boolean` / `Date` / `Formula` with its cached value / `Failure` / `Empty`; rows keep their index; hidden sheets, merges, frozen panes, column widths and list validations read back; dates from the number format, 1904 workbooks) and writes one (`write` of `SheetSpec`s built from `text` / `number` / `date` / `formula` cells styled with `bold` / `fill` / `numberFormat` / …; formulas are computed when the file opens; deterministic bytes). **`Std.Office.Docx`** reads and writes a document as `Block`s (`Heading` / `Paragraph` / `ListItem` / `Table`, runs with bold, italic and links; `toText` for search). Both are `Result`s on the ZIP and XML readers above, bounded by `Limits` (sheets, rows, cells, columns; blocks, depth), never read macros or embedded objects, and refuse an unsafe link or a sheet name Excel would refuse. Server side. Failures are `InvalidInput` with the domain `"office"` and a code. Bytes come from `File.readFile` or an upload. |
| **Images** | **`Std.Image`** resizes, crops, trims and keys out a background (`resizeToFit` / `thumbnail` / `crop` / `trim` / `toAlpha`) as JPEG, PNG, `Gif` or `WebpLossless`; an animated GIF stays animated under `Gif` / `Preserve` (every frame composed, delays and loop kept; `frames`, `reduceFrames` to drop frames). **`Std.Image.Canvas`** draws on the server: a display list of shapes, masks, pasted images, blur, resize and text (TTF/OTF, `defaultFont` built in), written by `encode`; `pixelDiff` compares two images in tests. Decoding refuses an image over 100 megapixels, a GIF over 1,000 frames or 250 million frame pixels, before it decodes (`InvalidInput`, domain `"image"`). Server side. |
| **Money / decimals** | `Std.Money` on `Std.Decimal`. **Never** raw `Float` for currency. |
| **Regex / text patterns** | `Sky.Core.Regex` (v0.29.0): compile once with `Regex.fromString` (`Result Error Regex`; `import Sky.Core.Regex as Regex exposing (Regex)`), then `Regex.contains re s` / `find` / `findAll` / `replace` / `split`. Write the pattern triple-quoted so a backslash stays (`"""\d+"""`); a literal pattern is checked at compile time (`[E2014]`). There is no String-pattern form. |
| **Encryption / signatures** | Encrypt with **`Crypto.xchachaSeal`** / `xchachaOpen` (XChaCha20-Poly1305, 24-byte random nonce; seal is a `Task`, open is pure). Only when a protocol fixes the nonce: `Crypto.chacha20Poly1305Seal` / `xchacha20Poly1305Seal` (caller nonce, pure; never reuse a nonce under one key). Keys are 32-byte `Secret`s: `Crypto.keyFromPasswordStrong { iterations, salt }` from a password (PBKDF2, at least 600 000 iterations and a 16-byte salt; `aesKeyFromPassword` is the older fixed-cost form), `Kdf.derive` (`Std.Crypto.Kdf`, HKDF) from key material. Sign with `Std.Crypto.Sign` (Ed25519), agree keys with `Std.Crypto.Kx` (X25519; refuses a low-order peer key). An encrypted session with a pinned server key: `Std.Crypto.Noise` (IK). A short pairing code: `Std.Crypto.Cpace` (a PAKE, **experimental: awaiting external review**, not for production data yet). A QR code for a pairing URL: `Std.Qr`. Never a `String` secret key. |
| **Child processes / file watching** | Run a command to completion: `Process.run`, or `Process.runCommand` for `{ code, stdout, stderr }` with `withStdin` text (a non-zero exit is a result, not an `Err`). Talk to a long-running child: **`Process.spawn`** (`Process.command "x" \|> Process.withArgs [...]`, `withPty { cols, rows }` on Linux/macOS) with output in an offset-addressed ring (`readFrom` from a Task, or the `Process.events` Sub in a TEA app, never both), `write` / `closeStdin` / `resize` / `kill` (whole process group) / `wait` / `close`. Children are reaped, closed with their Sky.Live session, and killed on exit (a program with a child also ends them on SIGINT / SIGTERM / SIGHUP, exiting 130 / 143 / 129); `close` ends the child's whole tree (a descendant that clears its environment, leaves the session and double-forks is not found; on the BSDs close ends the process group only). A handle belongs to the Sky.Live session that opened it (another session gets `Err PermissionDenied`; open it in the session that uses it). PTY sizes are 1-500 columns by 1-200 rows. Watch files with **`Std.Watch`** (`watch` / `next` / `changes` Sub / `close`; coalesced batches, `Overflow` means rescan). |
| **Errors** | `Result Error a` / `Task Error a`. **Never** `String` as an error type. A domain's failure travels inside `Error` as the one open variant `Domain DomainFailure` (v0.28.0; domain, code, retryable flag, retry delay, string attributes, always with an `ErrorKind`): react generically with `Error.domain` / `Error.isRetryable` (the domain's flag wins) / `Error.retryAfter`, or through a domain's typed view (`Std.Ai.Failure.fromError`) with a `_ ->` branch. A `case` over `ErrorDetails` without `_ ->` is `[E3001]` with a hint. An app's own domain: `Error.failure "myapp.billing" "CardDeclined" \|> Error.withAttr "reason" r \|> Error.fromDomain Error.InvalidInput "…"`. See `docs/errors/error-system.md#domain-failures`. |
| **Concurrency** | `Cmd.batch` / `Task.parallel`; **`Task.parallelN limit tasks`** for bounded fan-out under load (`parallel` is unbounded — a goroutine/connection storm at scale); in-process pub/sub via `Cmd.publish` + `Sub.subscribeTopic`. Shared state between concurrent Tasks: **`Std.Sync`** (`Ref` with atomic `update` / `compareAndSwap`, `Mutex` + `withLock`, a bounded `Queue` with `push` / `pop` / `popWithin`); pure code needs none of it. A path a client sends is kept inside a directory with `File.resolveWithin root path` (follows symlinks and `..`). |
| **Durable workflows** | **`Std.Durable`** for a multi-step process that must survive a restart (checkout / payment sagas, order fulfilment, onboarding, approvals). Mark side-effect boundaries with `Durable.step`; each step's result is journalled, so a resumed run replays completed steps instead of re-running them. `Durable.sleep` / `awaitSignal` suspend passively (a waiting run holds no process); a worker `Durable.poll` claims + advances due runs. **Worker versioning:** `registerVersioned` stamps a run's version at `start`, and `pollWith` reconciles a resumed run against the registered defs (`FailSafe` / `PinToStart`) for rolling deploys. **History compaction:** `Durable.compact db retentionMs` collapses terminal runs' journals while keeping their summary. **Zero-annotation durable TEA:** `App.withDurable db modelCodec` (or `App.withDurableId "<id>" db modelCodec`) makes any `Std.App` app durable with NO change to `model` / `msg` / `update` — the backend loop restores the Model on start and snapshots it after each update (Cli / Tui key by run id; Live keys by session id, covering a memory session store across a restart). The Model must be a `Codec`-serialisable data value (no function fields). The Model snapshot is at-most-once for an in-flight effect; use `Durable.step` when an effect needs exactly-once. (Substrate: `Durable.saveSnapshot`/`loadSnapshot`.) Postgres or SQLite; the code between steps must be deterministic. See `docs/design/durable-execution.md` + `docs/skyapp/overview.md`. |
| **LLM / agents** | **`Std.Ai`** (v1). `Std.Ai.Provider` — a swappable chat backend over `Sky.Core.Http`: `openai` and `compatible` (any OpenAI-compatible endpoint — a gateway, vLLM, or a local llama.cpp), a `router` (a `Provider` that picks a backend per call by a pure policy, and composes), typed `Message`/`Usage`/`ChatResponse`, and `cost` pricing a usage in `Std.Money`. `Std.Ai.Agent.oneShot` and `Agent.toolLoop` run an agent as a `Std.Durable` workflow, so every model call and tool call is a journalled step and is EXACTLY-ONCE across a resume (a crashed run does not re-call the paid model). `Std.Ai.Tool` is the prompt-based tool protocol; native OpenAI function-calling is also available (`Provider.chatTools` + `Agent.nativeToolLoop`). **Typed tools and structured answers:** `Tool.typed name desc codec exec` sends `Codec.jsonSchema codec` as the tool's parameters and decodes the model's arguments with the codec (bad arguments are handed back to the model, never run); `Provider.chatStructured (Provider.responseShape "name" codec) opts provider messages` sends an OpenAI-compatible `response_format` json_schema and decodes the answer into the codec's type (a mismatch is a `Decode` error; a `custom` provider refuses with `InvalidSetting`); `Agent.structuredToolLoop shape …` is the durable native loop with a typed final answer. **`Std.Ai.Policy` is the action firewall** — the model proposes a typed `Action`, a pure `Policy` returns a `Decision`, and only `Allow` runs the effect, so the LLM is never the authorisation boundary (`NeedsApproval` pairs with `Durable.awaitSignal` for human approval). **`Std.Ai.Trace`** is the lifecycle ledger: `Trace.tracedChat db (Trace.ledger run |> Trace.priced book) opts provider messages` records each model call (tokens, cost in `Std.Money` as an exact decimal, never a float, and what the call did: slot, tier `local`/`remote`, adapter, queue wait, start, latency, time to first token, tokens per second, draft acceptance, outcome `ok` or the `Failure` code; a failed call is a row too); `Agent.oneShotWith { trace = Just book }` (and `toolLoopWith` / `nativeToolLoopWith`) records an agent's calls once across a resume; an `Eval` run records its candidate and judge calls under `report.traceRun` (`Eval.withJudgePriceBook`); a `Train` run records its steps (`Trace.trainingSteps`) and one cost row (a Vertex run's measured spend, a local run unpriced). `totalCost` adds a run's priced rows and refuses to add two currencies, `runSummary` gives totals per currency, `costs` the whole ledger by kind. `Provider.measure` / `measureStream` / `measureTools` give the facts without recording them. See `docs/ai/trace.md`. **`Std.Ai.Memory.Pg`** is long-term memory on PostgreSQL + pgvector — `search` (vector nearest-neighbour) and `hybridSearch` (vector blended with a keyword score). **`Std.Ai.Endpoint`** (v0.28.0) names a model **slot** (`Endpoint.endpoint "chat"`) whose `withX` builders hold the local default and fall back to `<PREFIX>_AI_<SLOT>_<KEY>` (operator env > code > default; no `sky.toml` keys): a `URL` makes it remote, `withLocal` makes it a **managed local engine**, `MODE=local` with a `URL` is an error, a down remote never falls back to local, and each slot has a bounded queue (`Overloaded` / `Timeout`). **`Std.Ai.Local`** runs that engine: a pinned `llama-server` build and a pinned GGUF model (`Local.devModel` is Qwen2.5 0.5B, Apache-2.0), both verified by SHA-256 into one per-user AI home, a memory fit planned before loading (`ModelTooLarge`), `127.0.0.1` plus a per-run key file, a cleared environment, restart in development, idle unload, stopped with the app; `Local.withDraft` adds a pinned draft model (speculative decoding; a draft of another vocabulary is `InvalidSetting`), and `Local.withAdapters` loads several named LoRA adapters that each call picks with `Provider.Options`' `adapter` (an unknown name is `NotRegistered`, never a base-model answer). An AI variable that does not parse is `InvalidSetting` naming it (never skipped, never its secret value); a server refuses to start on one. Both yield an ordinary `Provider`; `Provider.chatWith` adds temperature / seed / max tokens / adapter and `Provider.embed` embeddings; `Provider.openStream` + `Provider.readStream` read an answer one piece per update (a TEA app), `Provider.chatStream` hands each piece to a callback (a Task program), on server slots, the built-in backends and device slots alike. `sky ai provision | pull | ps | start | stop` drive engines. See `docs/ai/local.md`. Every AI failure is typed: **`Std.Ai.Failure`** (`fromError` / `toError`; `NotConfigured`, `Unreachable`, `Overloaded`, `TimedOut`, `EngineCrashed`, `ModelTooLarge`, `NoAccelerator`, `NotProvisioned`, `ChecksumMismatch`, `RemoteRejected`, `BudgetExceeded`, `GateFailed`, …) inside the ordinary `Error`, each with an existing `ErrorKind` and a retryable flag. **`Std.Ai.Eval`** evaluates a model with Sky values: a `Suite` of `Case`s, scorers (`Exact`, `Contains`, `Matches`, `JsonShape` of a codec's shape, `CallsTool`, `Refuses`, `Rubric` by a judge, `Custom`, `AllOf`), the judge is the slot `"judge"` (a local pinned model by default, `SKY_AI_JUDGE_*` switches it), runs are seeded and repeatable (temperature 0, `seed + sample`), the `Report` has per-case results, scorer means, p95 latency and mean cost, `compare` pairs a baseline and a candidate (exact McNemar, a seeded bootstrap interval), and `check` / `gate` apply thresholds and return an opaque `Passed` or a `GateFailed` error; `toJunit` / `toMarkdown` for CI. Runs are kept in the database (`Eval.history`) and shown, with the slots, engines and recent calls, in the Sky Console's **AI** tab. Judge calls are journalled in the run's database (an unchanged verdict is asked once), `withCalibration` measures the judge's agreement with human labels (the gate refuses a judge below it) and `pairwise` asks the judge in both orders. See `docs/ai/eval.md`. **`Std.Ai.Registry`** holds fine-tuned adapters (LoRA / QLoRA, each pinned to its base model's digest) with their lineage and attached eval reports: records in the app's own database (`Std.Db`, SQLite or PostgreSQL), files in a `Std.Ai.Store` (local dir, GCS or S3, by SHA-256). `promote` needs an `Eval.Passed` for the version, every alias move is a logged compare-and-swap, `rollback` pops the alias's log, and a slot with `Endpoint.fromRegistry` + `withAdapter "registry:<name>@<alias>"` serves the alias and follows it. `sky ai promote | rollback | lineage`. See `docs/ai/registry.md` and `examples/71-ai-registry`. **`Std.Ai.Dataset`** is training and eval data as Sky values: typed rows (`Chat`, `Instruction`, `Preference` and their codecs) from `fromJsonl` (read in chunks), `fromCsv`, `fromStore` or `fromList`; steps `map` / `filter` / `dedupeBy` / `scrub` / `upTo`; `split` puts each row in `Train` / `Eval` / `Test` by a seeded SHA-256 of its key, so a row never moves between parts; `validate` checks chats (empty turns, role order, tokens, turns, duplicates) and fails with the typed `DatasetInvalid`; `freeze` writes canonical JSONL named by its SHA-256 plus a manifest (sources, steps, counts per split, scrub report, schema, consent basis, retention) and the same rows give the same digest, `load` re-checks it (`ChecksumMismatch`); `sky ai data freeze <file.jsonl>` does it from the CLI. **`Std.Ai.Dataset.Scrub`** replaces secrets, email, IBAN (mod 97), card numbers (Luhn), US SSN, UK NINO and phone numbers with placeholders and counts them (never the text). **`Std.Ai.Dataset.Synthetic`** generates rows with a teacher slot (`"teacher"`, the local pinned model by default, `SKY_AI_TEACHER_*` moves it): a pure seeded `plan` over topics and personas, dedupe, filters, the eval judge (`Eval.judgeAnswer`), a call limit and a cost budget, and a typed `Report`. See `docs/ai/dataset.md`; examples `68-ai-dataset`, `69-ai-scrub`, `70-ai-synthetic`. `dedupeBy` spills its key digests to disk past a bound, `withEngineTokenizer` counts tokens with the engine's `/tokenize` route (falling back offline), `Synthetic.withJournal db` makes a re-run with unchanged inputs call no model, and `Registry.registerDataset` records a frozen version as `<name>@<n>`. **`Std.Ai.Pipeline`** is the project's AI lifecycle in one value, `pipeline` in `src/Ai.sky` (`module Ai exposing (pipeline)`): `withSlot`, `withDataset` (`datasetOf codec build`, `withRegistered`), `withSuite` (`suiteOn slot suite`, `forModel`, `withThresholds`), `withTask`. The CLI builds it and runs it: `sky ai eval <suite> --candidate <model>@v<n> --against prod --promote-to prod` (compare with production, gate, JUnit, exit 1 on failure, promote only on a pass), `sky ai data freeze <dataset>`, `sky ai run <task>`, `sky ai start <slot>`; `sky ai promote` compares the evidence with the alias's version; `--test-mode` answers model calls from `tests/mocks/`. **`sky ai eval` is the merge gate for a model change; never `--promote-to` or `--force` past a red gate.** See `docs/ai/pipeline.md` and the full-stack flagship `examples/72-ai-support-desk`. **`Std.Ai.Train`** fine-tunes a LoRA / QLoRA adapter on a frozen `Dataset.Version` (`Train.lora Train.devBase version |> Train.withX …`). The backend is not in code: `Mlx` on Apple silicon, `Unsloth` on Linux + NVIDIA, `SKY_AI_TRAIN_BACKEND=mlx|unsloth|fake` overrides `withBackend` and detection, and a host that cannot run it is `NoAccelerator`. The trainer needs no Python: `Mlx` trains with `sky-trainer` (Sky's native LoRA trainer, C on MLX through mlx-c, a pinned and hash-checked bundle from `sky ai provision --train mlx`, or `SKY_AI_TRAINER_BIN`), `Fake` trains in the runtime itself (the program starts itself again as the trainer), and only `Unsloth` / `Vertex` run the Sky driver in a managed Python (pinned uv + CPython + hash-locked wheels). The run is a `Std.Durable` workflow (`drive` / `start` + `poll`): a detached trainer that outlives the program, relaunch from the newest verified safetensors checkpoint, `cancel` / `resume`, a Go GGUF converter (`withGgufExport`, adapter bound to its serving model, else `AdapterMismatch`), `withRegistry` registration with lineage, and `Local.withAdapter` to serve it. Test mode simulates the trainer (no Python, no model). `sky ai train | train status | runs | resume | cancel | export`. See `docs/ai/train.md` and `examples/73-ai-train`. **`Train.Vertex`** runs the same job on Google Cloud Vertex AI on one L4 under a `Std.Money` budget (`withBudget`, a dated price book, exactly-once launch, the budget as the job's hard runtime ceiling); **`Std.Ai.Serve`** runs one base and many LoRA adapters on one GPU behind vLLM, hot-loads each registry promotion with no failed request, and `Serve.deploy` puts it on Cloud Run (one L4, scales to zero, never public). Test both offline with `SKY_AI_GCP_ENDPOINT=http://sky-gcp.invalid` in `.env.test` (an emulator in the runtime). See `docs/ai/serve.md`, `docs/ai/deploy-gcp.md` and `examples/74-ai-cloud`. **`Std.Ai.Device`** (v0.28.0) runs a slot on the user's own device: the server's slot gets `Endpoint.withDevice (Device.spec Device.Auto)` (placement `Auto` / `DeviceOnly` / `ServerOnly` is required; `SKY_AI_<SLOT>_PLACEMENT`, `_DEVICE_MODEL`, `_DEVICE_ADAPTER`, `_DEVICE_THREADS`, `_DEVICE_GPU_LAYERS` override it; `Device.withThreads` / `withGpuLayers` are the expert engine settings, and GPU layers above 0 skip a backend with no GPU, typed, never silently), and a Sky.Spa client asks `Device.provider "<slot>"`, an ordinary `Provider` that answers on the device when it holds the model and can run it, else from the server (a `DeviceOnly` slot fails typed and never reaches the server). One engine everywhere, llama.cpp b11200 with Sky's core: WebGPU and CPU WebAssembly in a same-origin Web Worker (strict CSP; WebGPU only with JSPI and `shader-f16`, never a silent CPU run: `DeviceUnsupported`), `SkyLlama.xcframework` in the iOS app, `libskyllama.so` in the Android app, the managed `llama-server` in the Mac app; the same GGUF model and LoRA adapter (a `Std.Ai.Train` adapter included). Nothing downloads before the app calls `Device.prepare` after the user's consent (`Device.status` gives the size, then the progress); files come from the app's own server (`/_sky/ai/device/<slot>`, an allow-listed `/_sky/ai/blob/<sha256>`), checked by SHA-256 chunk and file. A call on the device sends nothing to the server, no prompt is stored and nothing is reported; a registry adapter runs on devices only after `Registry.allowOnDevice` (a scrubbed, registered dataset). `Device.recordPrompts` + `Eval.prompts` + `Device.replay` score one eval suite on the device and the server. Engines: `sky ai provision --device web|ios|android`; build flags `--ai-web-engine webgpu|cpu|both|none` and `--ai-embed <slot>`. See `docs/ai/device.md` and `examples/75-ai-everywhere`. Test it all offline with mock-by-default HTTP. The end-to-end capstone is `examples/66-slack-agent` (a durable Slack bot: @mention → durable run → firewall-gated threaded reply); `examples/72-ai-support-desk` shows a typed tool and a structured answer (`Assist.triage`, `Assist.ask`). |
| **Observability** | `Std.Log` structured logs; the dev console auto-mounts at `/_sky/console`; `OTEL_EXPORTER_OTLP_ENDPOINT` for an external collector. Telemetry **storage** is tunable via `Sky.Config.withTelemetry*` builders (or `SKY_TELEMETRY_*` env, which overrides them): counter/histogram coalescing windows to cut DB rows, and `withTelemetryDbCapacity` for the hourly size-report "near full" flag. See `docs/observability.md` + `sky doc Sky.Config`. |
| **Sky.Live navigation** | Every internal link is `sky-nav` (one persistent SSE per session). Bare `<a href>` only to deliberately leave the app. The URL fragment (`#…`) never reaches the server: read it with `Sub.onFragment toMsg` (at load and on every change; Sky.Live, Sky.Spa and the `Std.App` web targets). To move the address bar from `update`, return `Nav.pushUrl "/path"` / `Nav.replaceUrl "/path"` / `Nav.clearFragment` (`Std.Nav`, same-origin only): it routes like a `sky-nav` link, with no reload. |
| **Password forms** | `Ui.form [Ui.onSubmit DoSignIn]` with a typed record; never per-keystroke `onInput` on a password field. The record's fields must be `String`/`Int`/`Float`/`Bool`/`Maybe` of those (`[E2010]`); each is decoded strictly from the control with the same `Ui.name`. |
| **No raw HTML/JS** | `Std.Ui` HTML-escapes everything. `data-sky-eval` is gone (no runtime path evaluates a string); use `data-sky-path` for URL sync. |
| **Third-party JS widgets** | A widget island: `Ui.island { name, id, props } attrs` (or `Html.island`) is an element the widget owns; the server never patches inside it and the client keeps it across re-renders. The widget registers from a same-origin file loaded with `<script defer>` (`window.Sky.island name { mount, update, command, destroy }`; no inline script, strict CSP). Its events arrive as typed Msgs through `Ui.onIslandEvent type decoder toMsg` (a rejected payload is logged and dropped); `Cmd.toIsland id name payload` sends it commands. Its state lives in the browser, so report what matters and feed it back through `props`: a remount starts from `props`. See `docs/skyui/overview.md` (Widget islands). |

The app-shape details (Sky.Live TEA loop, routing, session lifecycle, forms,
`Std.Ui` layout, Sky.Tui, Sky.Webview) live in `docs/skylive/`, `docs/skyui/`,
`docs/skytui/`, `docs/skywebview/`. Read the relevant one before building.

### Production gate (when the user says deploy / prod / Cloud Run / K8s)

**This is the same checklist the app prints on every dev start**, under its
`listening` line. One list, in two places, deliberately — see
`runtime-go/rt/startup_report.go`.

- `ENV` set to anything that is **not** `dev` / `development` / `local` (the
  runtime tests for the dev spellings, not for the literal `production`). Locks
  the dev console + banner + metrics.
- `SKY_CONSOLE_AUTH` = `token` \| `app` \| `off`. With `token`, also set
  **`SKY_CONSOLE_TOKEN`** — without it the console falls back to an
  auto-generated `.sky/console-token`, which a container regenerates every boot
  and no operator can read. With `app`, the app's own signed-in admins open the
  console: add **`App.withConsoleAuth check`**, where `check : Request -> model
  -> Task Error (Maybe Console.Identity)` reads the signed-in model (or the
  app's own cookie) and returns `Just` an identity for an admin; everything else gets 403 (fail closed). `app` without
  `withConsoleAuth` refuses every console request. With `token`/`app` set and no
  console mounted the app **exits 1**; `off` is the way to declare the surface
  intentionally absent.
- `SKY_ADMIN_TOKEN` for the `/_sky/metrics` bearer. (`SKY_METRICS_TOKEN` and
  `SKY_CONSOLE_TOKEN_SECRET` are back-compat aliases for it, not separate
  settings.)
- Multi-replica → a **shared** session store (`redis`/`postgres`), sticky
  sessions (a proxy-issued affinity cookie; the session cookie is `sky_sid`,
  `__Host-sky_sid` over HTTPS, and its value changes at sign-in), and
  cross-instance pub/sub (`store=redis` or
  `SKY_LIVE_BROKER_URL`). `memory` and `sqlite` are single-instance only.
- Health checks: probe **`/_sky/healthz`** (liveness, always 200) or
  **`/_sky/readyz`** (503 until the store and database answer, and while
  draining). Never probe a path the app does not declare (`/healthz`,
  `/health`): since v0.27.9 the app's `withNotFound` page is sent with **404**,
  so such a probe fails.
- Bind address: production binds **all interfaces** by design (containers,
  VMs and proxies need it); dev binds `127.0.0.1`. `SKY_HOST` overrides either
  (`SKY_HOST=127.0.0.1` to keep a production process on loopback behind a local
  proxy). The line under `listening` states the real bind, e.g. `bind
  0.0.0.0:8000  all interfaces (production default; SKY_HOST narrows)`. In
  production a loopback bind is not access control: the Host guard is off
  there, so put the proxy in front of it. A desktop window (`--target
  desktop`) binds loopback in every mode and mounts no console unless
  `SKY_CONSOLE_AUTH` names a mode.
- WebSocket upgrades (`Sky.Http.Server.WebSocket`) need
  `Ws.withOriginPatterns [...]` in production, or they get 403. A deploy that
  leaves `ENV` unset no longer accepts every origin: outside production only
  same-host, loopback and `SKY_ALLOWED_HOSTS` origins pass.
- Outside production only: the loopback listener refuses a request whose
  `Host` is not `localhost` / `*.localhost` / a loopback IP / `10.0.2.2` / the
  `SKY_APP_URL` host / a `SKY_PUBLIC_URL` host (anti DNS rebinding). A dev
  proxy name, a Codespaces URL or a LAN name needs `SKY_ALLOWED_HOSTS` (comma
  list, `*.app.github.dev` wildcards, `*` turns the check off) or
  `SKY_PUBLIC_URL`.
- Content-Security-Policy: every page Sky serves (Sky.Live, the Sky Console,
  Sky.Spa) works under `script-src 'self' 'wasm-unsafe-eval'` with **no hashes
  and no `'unsafe-inline'`** — the scripts are same-origin files
  (`/_sky/live.<hash>.js`, `spa-boot.<hash>.js`). Send the policy from the
  proxy, or set `SKY_CSP=strict` to have the runtime send it (it never
  overwrites a policy the app set). See `docs/sky-toml.md`.

> **`SKY_AUTH_TOKEN_SECRET` is not a runtime setting, and this gate used to say
> it was.** Nothing in `runtime-go/` reads it: `sky_sid` is unsigned random hex,
> and `Auth.signToken` takes its secret as a Sky-level *argument*. The name is a
> convention in user code (`System.getenvOr "SKY_AUTH_TOKEN_SECRET"`) that only
> `sky doctor` knows about. If you use `Std.Auth`, whatever variable you feed
> into `Auth.signToken` must be ≥ 32 bytes; if you don't, setting it changes
> nothing.

Env var reference: `docs/sky-toml.md` + `docs/skylive/architecture.md`.

## Build & test

```bash
sky init [name] [--production]   # new project
sky build src/Main.sky           # compile → sky-out/app
sky run src/Main.sky [-- args]   # build + run; args after -- reach System.args (--profile for pprof)
sky check src/Main.sky           # type-check + go build  (≡ sky build)
sky check --format json src/Main.sky   # NDJSON diagnostics on stdout (also build / test / fmt --check)
sky watch src/Main.sky           # file-watch rebuild + restart
sky verify                       # project pre-release gate: fmt + check + build + tests
sky fmt src/Main.sky             # opinionated formatter (idempotent)
sky test tests/MyTest.sky        # Sky.Test runner (SKY_TEST_JSON=<path> → per-case JSON report)
sky fuzz src/Main.sky [--target web:app]   # no-panic model fuzz of update (any TEA app); a Sky.Spa target adds the differential split oracle
sky doc <Module>                 # stdlib docs (--serve / --tui / --list / --export <dir>)
sky doc --diagram <kind>         # audit-grade diagram: journey (behaviour+DFD) | components (C4) | wire (API & call-paths) | telemetry | audit (whole SOC2/ISO pack → --out <dir>); --format puml|md|svg
sky doc --api openapi            # OpenAPI 3.1 spec of the app's HTTP API, generated from the typed source; --format yaml|json, --no-rpc for declared routes only (proto/grpc/asyncapi planned)
sky db init | migrate --gen | migrate | seed | status | push   # file-based migrations
sky db start | stop [--all] | ps [--all]                       # local PostgreSQL cluster
sky db provision --embed                                       # fetch + pin a PostgreSQL bundle
sky db provision --shared [--service] [--app <name>]           # one host cluster, role-per-app
sky build --embed src/Main.sky   # bundle PostgreSQL INTO the binary; ./sky-out/app --embed
sky spa-split src/Main.sky --out .split --build   # explicit Sky.Spa split (advanced)
sky add <go/module> | remove | install | update                # Go FFI deps
sky add ./local/dir              # local path dep: go.mod → Go module, sky.toml/src → Sky package (type-checked with your code)
sky package --release --target mobile:ios|mobile:android|desktop:mac   # store artefact → sky-out/release/
sky package --release --target mobile:ios --upload testflight          # + validate and upload to TestFlight (SKY_ASC_*)
sky ai provision | pull | ps | start | stop                    # Std.Ai managed local model engines
sky ai promote <name> <v> --to <alias> | rollback <name>@<alias> | lineage <name>@<alias>   # Std.Ai.Registry
sky ai eval <suite> [--against <alias>] [--candidate <m>@v<n>] [--promote-to <alias>]   # the project's Std.Ai.Pipeline (src/Ai.sky)
sky ai data freeze <dataset> | run <task> [-- args] | members   # --test-mode answers model calls from tests/mocks/
sky ai provision --train mlx|unsloth|fake [--base <base>] | train --data <dir>/<name> [--base dev|hf:<org>/<repo>@<commit>|file:<dir> --serving <model>] | export <run>   # Std.Ai.Train
sky ai train --backend vertex --budget "USD 2.00" ... | image build --train|--tier   # Std.Ai.Train on Google Vertex AI (one L4)
sky ai serve | deploy --service <n> | teardown --service <n>|--run <k>|--all-sky | cloud ls | cloud prices   # Std.Ai.Serve GPU tier on Cloud Run
sky ai provision --device web|ios|android [--from <dir>]   # Std.Ai.Device engines; sky build --ai-web-engine / --ai-embed <slot>
sky doctor [--fix] | upgrade | upgrade-claude | clean
```

**Sky.Spa entries auto-split.** `sky build src/Main.sky` on a `Spa.app` app
derives + builds the wasm frontend + native backend under `.split/` (no manual
`spa-split`); `sky run src/Main.sky` then runs the backend, which serves the
frontend + `/_rpc` same-origin. `--target desktop|ios|android` (frontend shell)
and `--embed` (bundle PostgreSQL into the backend) COMPOSE with the split, they
do not disable it. `sky check` type-checks the shared source directly; `sky
spa-split <entry> --out <dir>` is the explicit form when you want the artefacts
kept at a chosen path. (Recursion is impossible: the generated projects carry a
`[spa] generated = true` marker that `sky build`/`sky run` never re-split.)
Each `/_rpc/<Msg>` is a `Server.rpc` route: it takes only same-origin
`application/json` POSTs (403 otherwise; behind a proxy that rewrites `Host`,
set `SKY_PUBLIC_URL`), and `GET /_sky/sub` streams a topic only when the app's
own `subscriptions`, run on the verified session model, names it. A topic keyed
on a non-session model field is refused: key per-user topics on the session.
The split's session cookie is `sky_spa` (it was `sky_sid`; an old cookie moves
over on the next request, and a pre-v0.27.0 token is converted once), a signed
token with a session id: sign-out (the client
clearing its `Session` field, or a server branch that changes it) ends that id
in the session store, so a copy of the cookie from before sign-out is refused.
The cookie lives as long as the token (30 days; `App.withSessionLifetime` /
`SKY_SPA_SESSION_LIFETIME`), and `App.withPersistExclude [ … ]` names model
fields a reload never takes from an earlier page (server-owned values).
Several replicas need a shared store (`[live] store` / `SKY_LIVE_STORE` =
`postgres` or `redis`) as well as a shared `SKY_SPA_SESSION_SECRET`
(`docs/skyspa/auto-split.md` §25); production refuses to start with no store and
an unwritable data dir, and a development key (`spa-session-secret.dev`) is
never read in production. A link to a path with no client route (or a
`Spa.serverRoute` path) is a full page load. A handled client `Err` is never
logged as an RPC failure; call `Spa.reportRpcFailure` for a real unhandled one.
A `Dict k v` field crosses the wire through `Codec.dict`.

**Serving the wasm — precompressed.** A Go→wasm client is multi-MB raw (~2.5 MB
brotli), so `sky build` precompresses the hashed `main.<hash>.wasm` + `wasm_exec.js`
in `frontend/dist` to `.gz` (`gzip -9`) and `.br` (brotli quality 11, written by
`sky` itself with the `brotli` crate in parallel parts: no tool needed, ~3x
quicker than `brotli -q 11` for a file within 1% of its size).
`--precompress=max|fast|off` (or `SKY_PRECOMPRESS`) picks the level: `fast` is
brotli 9 + gzip -6 (seconds, ~18% larger `.br`), `off` writes none
(`--no-precompress` is its alias, and every level first removes the assets'
earlier `.br` / `.gz`); `max` is the default, and becomes `fast` with a warning
when the available memory does not hold its parallel brotli. The Sky backend's static
mounts send them: a request that accepts `br` (else `gzip`; a fixed order, not
by q-value) gets the `.br` (else `.gz`) file as it is, with the original's
Content-Type and Last-Modified and an ETag per encoding, only when the build's
`dist/sky-precompress.json` lists it and the source and sidecar still have the
size and SHA-256 it records. A Range request that accepts the encoding ranges
over the sidecar (206 with Content-Encoding); one that does not, a file with no
listed variant, and a `.gz` / `.br` a user committed in their own static dir get
the raw file (gzipped on the fly as before; `runtime-go/rt/static_precompressed.go`).
Every static response says `Vary: Accept-Encoding`.
A separate static host serves them with Caddy `file_server { precompressed br gzip }`
(Caddy's `encode` has no brotli), or nginx `gzip_static` + `ngx_brotli`. The wasm
is content-hashed + `immutable`, so allow a CDN to cache `*.wasm` (Cloudflare does
not by default). Deploy-time `brotli` bridges are no longer needed — the build
emits `.br`/`.gz` itself (`rust/crates/sky/src/main.rs`, `precompress_web_asset`).

**Embedded PostgreSQL — dev/prod engine parity.** `Std.Db` is dialect-safe
across SQLite and Postgres, and that gap is a real tax: `Codec.auto` cannot
encode `Money`/`Decimal`, and there is no `NUMERIC` DDL kind, while `Std.Money`
is the pinned currency default. Running the same engine in development that you
run in production is now the easy path.

The rule that makes it work: **the app never knows which tier it is in.** It
consumes a DSN (`<PREFIX>_DB_PATH`, or `DATABASE_URL`) — only the provisioner
changes. `sky run` supervises a per-project cluster; `./app --embed` runs its
own; an operator sets a DSN; a shared host cluster issues one per app. Opt in
with `[database] embedded = true`. `--embed` alongside an explicit DSN is an
**error**, not a precedence rule.

> **Bundles are published.** `sky db provision --embed` resolves
> **`postgres-bundle-v18.6`** (built by `.github/workflows/postgres-bundle.yml`,
> cut 2026-08-19) and fetches a self-contained, licence-clean PostgreSQL 18.6
> tarball for the host platform — linux/darwin × amd64/arm64, each with an SBOM
> and verified against the release's `SHA256SUMS`. The tree vendors its
> permissive deps (openssl/icu/xml2/zstd/lz4/zlib) under `@rpath`/`$ORIGIN`, so
> it drops into a **glibc** image (debian-slim, distroless-base) with no host
> packages. `SKY_POSTGRES_BIN` or a system PostgreSQL still works for `sky db
> start` if you'd rather not fetch. Full design: `docs/skydb/embedded-postgres.md`.

**Where embedded PostgreSQL can run.** The bundle is glibc-linked and needs a
glibc userland (with `libstdc++`), a **durable writable data dir**, and the run
user in `/etc/passwd`. Verified empirically (`sky db provision --embed` + a Sky
app querying it, under Apple `container`):

| Target | Embedded PG |
|---|---|
| macOS / glibc-Linux laptop · EC2 / GCE / any glibc Linux VM | ✅ (a VM's persistent disk is ideal) |
| Container: `debian-slim` / `distroless-base` **+ a mounted volume** | ✅ (`server_version=18.6` returned) |
| Container: **Alpine** (musl) or **`FROM scratch`** | ❌ no glibc loader; `gcompat` still dies on libstdc++ + glibc-fortify symbols — needs a musl/static bundle |
| **Cloud Run** | ⚠️ warm single instance **+ a mounted volume** only (FS is in-memory/ephemeral) |
| **AWS Lambda / GCP Cloud Functions** | ❌ stateless/ephemeral — pair with managed PG |
| **Windows** (native) | ❌ no Windows bundle + the machinery is unix-socket only → WSL2 or a system PostgreSQL via `DATABASE_URL` |

Rule of thumb: durable, single-owner storage → embedded PG on a **VM or a
container-with-a-volume**; stateless serverless → **managed PG** (Cloud SQL /
RDS). Where the bundle cannot run the runtime **refuses loudly** (the "PostgreSQL
binaries do not run" check), it does not silently lose data.

**Build speed — what the build reuses.** Every `go build` goes through one
builder (`rust/crates/project/src/go_cmd.rs`) and passes `-trimpath`, so a
package compiled in one directory is a cache hit in any other (a new checkout,
each Sky.Spa leg, a desktop shell); `--no-trimpath` opts out and `sky run
--profile` builds without it. `go build -p` is sized from the dependency
compiles (`go_jobs.rs`; `main` compiles alone), `--go-jobs <n>` overrides it.
Exactly pinned Go FFI modules skip `go get` on a rebuild (`go_get_memo.rs`).
An SSR client build's backend leg builds beside its frontend leg and links the
wasm name through `skybuildinfo`, handed over on the backend's stdin
(`spa_wasm_handoff.rs`); the wasm client links a constant stamp, so its hashed
name changes only with its source. The bundle's `.gz` /
`.br` are compressed at once, beside the backend leg. Over its cap the Go cache
loses its least recently used entries, not everything. For CI, cache
`~/.sky/go-build`, `~/go/pkg/mod` and `~/.cache/sky/precompress` — not
`~/.cache/go-build`, which `actions/setup-go` caches — and never restore a
cache an untrusted job can write into a release job (`docs/tooling/cli.md`,
"Caching builds in CI").

**Never run `sky build` from the repo root** — it overwrites the compiler binary
in `sky-out/`. Always `cd` into the example/project dir first.

Compiler development (Rust): `cd rust && cargo build --release -p sky`. The
verification sweep — `cargo test --workspace` + the xtask gate suite
(`cargo run -p xtask -- <gate>` for roundtrip / resolve / infer / reject /
build-run / coerce-floor / repro / golden / fuzz) + the example sweep
(`scripts/example-sweep.sh`) + behavioural conformance (`scripts/conformance.sh`)
— is the source of truth. Green-everywhere is a hard release gate. Corpus gates
are necessary but **not sufficient**: a change is not verified until it also
passes the full example sweep + a real app (see
`docs/rust-rewrite/13-change-verification-and-edge-cases.md`).

**Live tests fail rather than skip.** Some tests need a real environment — a
PostgreSQL installation, a Go toolchain, the `sqlite3` client — and `cargo test`
**fails** when one is missing, naming what to install. That is deliberate:
fourteen tests covering the shared-cluster security boundary used to end their
probe with `eprintln!(…); return;`, and with and without a cluster they printed
byte-identical verdict lines (`ok. 14 passed`), so CI — which installed no
PostgreSQL in the only job that ran them — had never run one of them. If you
genuinely cannot provide the environment, say so out loud:

```bash
SKY_LIVE_TESTS=skip cargo test --workspace   # the ONLY way to skip a live test
```

New live tests gate through `rust/crates/sky/src/live_gate.rs`
(`live_gate::required(Need::Postgres, <your probe>)`), and
`rust/crates/xtask/tests/live_tests_are_not_silently_skipped.rs` fails the build
on the shapes that used to be written instead. The Go runtime's tests follow
the same rule and the same variable: a test that needs Node.js or PostgreSQL
calls `requireNode(t)` / `requirePgBinDir(t)` (`runtime-go/rt/live_gate_test.go`),
which fails naming what to install unless `SKY_LIVE_TESTS=skip`.

`xtask coerce-floor` takes the same variable for the same reason. Its golden
locks a runtime-narrowing floor **per project**, and a project whose generated
FFI surface is absent (`sky-ffi/` and `.skydeps/` are `.gitignore`d, so a fresh
checkout has neither) cannot be measured — which used to be filed under "did not
emit (not gated)" while the run reported PASS on the remainder, measuring 56 of
61 rows. An unmeasurable row now FAILS, naming the `(cd <project-dir> && sky
install)` that fixes it; `SKY_LIVE_TESTS=skip` downgrades it to a loud
`UNMEASURED` block, and `--bless` refuses under a shortfall rather than write a
golden mixing measured rows with carried-forward ones. Both verdict lines state
how many of the golden's rows the run actually covered.

`cargo run --release -p xtask -- harness` runs the registered gates through the
gate harness, which enforces each gate's budget by `killpg`, requires an exact
assertion count, and refuses to report PASS when it cannot establish a verdict
(`NOT RUN` / `UNPROVEN` → `UNKNOWN`, exit non-zero). Every registered gate
declares a falsifying mutation — an empty set fails the build — and
`--verify-falsifiers` proves the mutation makes the gate red — incrementally:
it re-proves only gates whose proof inputs changed (`--all` re-proves every
gate; the nightly and the release pass it). Per change, `scripts/gates-for-change.sh`
runs the narrowest gates for the paths you changed; the merge/release gate is
the release workflow's full suite. See `docs/tooling/gate-harness.md`.

## Non-negotiable code rules (enforced by `cargo test`)

These apply to any Sky code you write or any compiler change you make:

- **No `Result String a` / `Task String a`** in public surfaces — use `Error`.
- **No runtime panic from well-typed Sky code.** Every known panic class has a
  regression test (`runtime-go/rt/*_test.go` / `test/**Spec` / conformance).
- **No `Std.IoError`, no `RemoteData`** — both removed pre-v1.
- **No silent numeric coercion** — `AsIntChecked` is fallible; `OrZero` marks
  display-only lenient helpers.
- **No raw `.(T)` assertions on any-typed thunks** — route via `rt.Coerce[T]`.
- **Record field enumeration sorts by `_fieldIndex`** before order-dependent emission.
- **Secrets are typed** — every secret-bearing argument is the opaque
  `Sky.Core.Secret.Secret`, never `String`/`any`: `Auth.signToken`/`verifyToken`/
  `signSlidingToken`, `Jwt.hs256` and `Jwt.rs256` (the RSA *signing* key; the
  public verify key is `Jwt.rs256Verify : String`), the `Crypto` AEAD keys
  (`aesGcmEncrypt`/`Decrypt`, `chacha20*`, `aesKeyFromPassword` — which also
  *returns* a `Secret`), and `Http.withBearer`/`withApiKey`. `Cli.readPassword`
  *returns* a `Secret`. A `Secret` redacts itself in every print/log/JSON path;
  wrap at the boundary (`Secret.fromEnv "VAR"` / `Secret.fromString runtimeStr`)
  and unwrap only via the greppable `Secret.reveal`. `Crypto.hmacSha256` stays a
  general `String`-keyed primitive (its key is not always a secret). The
  `Std.Crypto` key types (`Sign.SecretKey`, `Kx.SecretKey`) and protocol states
  (`Noise.Handshake`/`Transport`, `Cpace.Pending`) are opaque and redact
  themselves the same way; their bytes leave only as a `Secret`. See
  `docs/security/secret-migration.md`.
- **`sky check` ≡ `sky build`** — both invoke `go build` on the emitted Go.
- **Root-cause fixes only.** Never suppress a type error or warning; a defensive
  cover-up that hides a contract violation IS a violation.

### Engineering norms (repo work)

- **Run the formatter on every file you touch — before you commit.** A change to
  a Rust file runs `cargo fmt` (from `rust/`), a Go file `gofmt -w`, a Sky
  (`.sky`/`.skyi`) file `sky fmt`. This is not cosmetic: drift compounds, and a
  later blanket `cargo fmt`/`gofmt`/`sky fmt` then produces a huge diff that
  buries real changes and, worse, can silently break a source-scanning gate. A
  real example: an unrelated `cargo fmt` split a `cfg.extra_defaults.push(("DB_…"`
  call across lines, the config-surface detector (which matched a CONTIGUOUS
  literal) stopped seeing two DB env vars, they fell out of the census, and the
  config-matrix gate went red with a failure that pointed nowhere near the edit.
  Format-as-you-go keeps every layout-scanning gate honest and every diff small. For Sky source this is enforced: `cargo run -p xtask -- sky-fmt-check` (in `rust-ci`, and in `scripts/gates-for-change.sh` for any `.sky`/`.skyi` change) fails on any tracked `.sky`/`.skyi` file that `sky fmt --check` would change or cannot parse, apart from the entries in `rust/crates/xtask/sky-fmt-exclusions.txt`, each of which carries its reason.
  (Detectors that scan source for a literal must themselves be whitespace-robust
  — see `xtask/src/config_surface.rs::seeded_suffixes`.)
- **No-deferral principle.** A bug you spot — yours or pre-existing, in dev, CI,
  or a sweep — enters the pipeline immediately and is fixed in the next patch.
  "Pre-existing / defer / known issue" are not shipping excuses. The user may
  explicitly override with "ship without fixing X"; that is the only exception.
- **Every feature/bug becomes a regression test** before the fix lands — the
  failing test is the discovery artefact. Compile-time behaviour → cargo tests
  (`rust/crates/*/tests`, the `ty` corpora); runtime helpers → `runtime-go/rt/*_test.go`; stdlib semantics →
  `tests/**/*Test.sky`; behaviour → `tests/conformance/`.
- **Timeout-bound every long command — through the shim, never a bare
  `timeout`.** `cargo test` / xtask gates run under a 60 min ceiling. A test
  that hangs is a bug to bisect, not wait out. In a script,
  `source scripts/lib/with-timeout.sh` and call `with_timeout <secs> <cmd...>`;
  `rust/crates/xtask/tests/scripts_bound_time_portably.rs` fails the build on a
  bare `timeout`. GNU coreutils `timeout` is absent on stock macOS and was
  absent here when the nix shell supplying it went away — at which point
  `timeout 1200 go test -race ./rt/... | tail -8` printed `command not found`
  to stderr, took `tail`'s status, and reported **exit 0 having run nothing**.
  Where the bound only wraps `go test`, prefer its own `-timeout` (it dumps
  stacks and names the hung test) with `with_timeout` outside as the backstop.
- **A gate whose prerequisite is missing FAILS, naming what to install.** Never
  skip, never pass. Shell gates use `require_tool <name> <hint>` from
  `scripts/lib/require-tool.sh`; Rust live tests use
  `rust/crates/sky/src/live_gate.rs`. Both take the same opt-out —
  `SKY_LIVE_TESTS=skip` — and both treat any other value as an error rather
  than guessing.
- **A gate may not measure a compiler older than the tree.** `sky-out/sky` is
  installed by exactly one line (`scripts/build.sh:75`), so a bare
  `cargo build --release -p sky` leaves `rust/target/release/sky` fresh and
  `sky-out/sky` untouched — and every consumer then measures whatever
  `build.sh` last produced. Sixteen scripts read that path and none checked it.
  Source `scripts/lib/fresh-compiler.sh` and call
  `require_fresh_compiler "$SKY" "$ROOT"` (Node: `scripts/lib/fresh-compiler.mjs`;
  it runs the shell library rather than reimplementing it). It FAILS naming
  `./scripts/build.sh`, and unlike `require_tool` it has **no opt-out** — a tree
  that has the sources can always rebuild, so "I cannot fix this" is never true.
  The check is content-aware where content is provable: the build bakes a
  fingerprint of the embedded asset trees into the binary
  (`sky-embed-fp-v1:<sha256>`, `rust/crates/ffi/build.rs::fingerprint`), so a
  legitimate prebuilt binary under fresh-checkout mtimes passes when only
  embed-root mtimes moved and the content matches, and a `touch`ed binary whose
  embedded content is from another tree FAILS regardless of mtimes — never
  `touch sky-out/sky`. The compiler's own Rust sources carry no baked witness
  and remain mtime-compared.
  `rust/crates/xtask/tests/gates_measure_a_fresh_compiler.rs` fails the build on
  a new consumer that skips the check, on a script bash 3.2 cannot parse, on
  the root lists drifting from the `stage(…)` calls in
  `rust/crates/ffi/build.rs` (the staging authority — parsed, not copied), and
  on the shell and Rust fingerprint constructions disagreeing. The loud symptom
  cost a full diagnosis: a sweep after a bare `cargo build` reported 22 of 22
  conformance suites FAILED on a consistent tree. The quiet one is worse — a
  stale binary that PASSES certifies source it never compiled.
- **The embed never contains a hidden or gitignored file.** `build.rs` staging
  drops dot-files and dot-directories as a class: running a bundled console
  writes a runtime secret at `sky-bundled/<app>/.sky/console-token`
  (gitignored, 0600), and before the class rule it was staged into
  `embedded-assets/` and baked into every locally-built `sky` binary —
  recoverable by `grep` from the binary and re-materialised into
  `~/.cache/sky/assets/<hash>/` by any standalone run. Two gates in
  `gates_measure_a_fresh_compiler.rs` enforce it: staging a planted
  `.sky`/`.env`/`.skydata` fixture must drop them, and everything staged from
  the real repo is `git check-ignore`d. Consequence for contributors: a file a
  build must embed may not be hidden, and a hidden file is never a compiler
  input (the freshness walks skip them too, so a runtime-written token cannot
  turn gates red).
- **Disk hygiene.** `scripts/build.sh` + `scripts/example-sweep.sh` prune the Go
  build cache only under disk pressure (over 5 GB with under 30 GB free; build.sh
  also past 20 GB); the `xtask build-run` gate self-guards disk before the sweep;
  the gate build cache (`scripts/lib/gate-build-cache.sh`) is bounded by
  `SKY_GATE_CACHE_MAX_MB` (default 8192). Reclaim manually (`go clean -cache`,
  worktree cleanup) when under 5 GB free.
- **Template + doc sync (non-negotiable).** When stdlib / syntax / Sky.Live APIs /
  CLI verbs change, update **this file**, `templates/CLAUDE.md` (+ `templates/AGENTS.md`),
  and the matching `docs/*` in the **same commit** (see the [Deep dives](#deep-dives)
  table). There is ONE doc source for every stdlib module, kernel-only ones
  (`Std.Live`, `Std.Tui`, `Std.Jobs`, `Sky.Http.Server`) included: the `.sky`
  file under `sky-stdlib/`. Its `name : Type` lines and `--` doc comments feed
  the type checker, LSP hover and `sky doc` (`rust/crates/project/src/doc.rs`
  renders from the `.sky` alone). Drift fails CI in `xtask kernel-members` (the
  `hir` kernel tables match the runtime), `project/tests/kernel_surface.rs`
  (each `Ffi.kernel` alias names a real `rt` function),
  `kernel_signature_coverage.rs` (an advertised member has a signature) and
  `kernel_signature_runtime_arity.rs` (the signature arity matches the runtime).
  The public surfaces follow in the same commit too: `README.md` (when a module,
  CLI verb, target, install step or headline capability changes; every README
  sample is a copy of a file CI builds) and the docs index `docs/README.md`.
  The sky-lang.org site is checked and redeployed at each release.
- **Release = bump the version and write the notes in one commit, then the
  version claims follow.** The ONE version source is `[workspace.package]
  version` in `rust/Cargo.toml`: every crate inherits it, `sky --version` prints
  it (`sky v0.27.5`, plus ` (<commit>)` for a build from a source checkout), and
  `default.nix` / `flake.nix` read it. A release bumps it in the SAME commit as
  `CHANGELOG.md`'s new `## vX.Y.Z` heading;
  `rust/crates/xtask/tests/docs_state_the_current_version.rs` fails any commit
  where the two differ (or a crate or Nix file states its own version), and
  `release.yml` refuses a tag that differs from either before it builds. Files
  that state the CURRENT line — `README.md`'s status banner and this file's
  "Current line" — are checked by the same test, so a release that forgets them
  goes red. (`README.md` sat on "v0.19.x release candidate" through
  the whole v0.20 line before that gate existed: the first thing a reader or an
  agent learns about the project, a full minor out of date.) Historical mentions
  ("v0.17 closed …", "shipped in v0.16.6") are facts about the past and are
  deliberately NOT rewritten.
- **Live docs — examples can't rot.** `docs/` is *only* live reference; frozen
  per-version roadmaps and legacy material live under `docs/history/` (excluded
  from gating). `scripts/doc-examples.sh` is the live-docs gate: it `sky check`s
  every full-module (`module Main …`) Sky example in the live docs, so a doc
  example that stops compiling fails CI. Run it after touching docs; opt an
  intentionally-erroring example out with a `-- doc-example: skip` line. The
  stdlib API itself never drifts — it comes from `sky doc <Module>`, generated
  from source.

## Active limitations (verified against HEAD)

Real current constraints you must work around:

1. No higher-kinded types (HM only). 2. No `where` clauses (use `let`).
3. No custom operators.

Most historical limitations (negative-literal args, `Dict.toList` typed keys,
interface satisfaction, zero-arg call arity, recursive-list-op stack growth,
multi-line signatures, head-position alias unfolding, mixed-type SQL params,
`Maybe` SQL params) are **closed** in v0.17–v0.19. The authoritative,
version-tracked list is `docs/KNOWN_LIMITATIONS.md` — check it, not memory,
before assuming a limitation still holds.

## Deep dives

`sky doc <Module>` is the live API. These docs are the design + semantics behind it:

| Topic | Doc |
|---|---|
| Full stdlib reference | `docs/stdlib.md` (+ `sky doc <Module>`) |
| Collections and their costs (`List`, `Dict`, `Set`, `Array`, `String`), `Regex` | `docs/stdlib.md` (the `Array` and `Regex` sections; each function's `Cost:` line in `sky doc`) |
| XML and ZIP — `Std.Xml` (+ `Decode` / `Encode`), `Codec.toXml` / `fromXml`, `Std.Zip` | `docs/stdlib.md` (`sky doc Std.Xml` / `Std.Zip`) |
| Excel and Word — `Std.Office.Xlsx`, `Std.Office.Docx` | `docs/stdlib.md` (`sky doc Std.Office.Xlsx` / `Std.Office.Docx`) |
| Images — `Std.Image`, `Std.Image.Canvas` | `docs/stdlib.md` (`sky doc Std.Image` / `Std.Image.Canvas`) |
| Language + errors | `docs/language/`, `docs/errors/` |
| Compiler architecture (Rust, primary) | `docs/rust-rewrite/` |
| Change verification / edge cases | `docs/rust-rewrite/13-change-verification-and-edge-cases.md` |
| Runtime narrowing — origins, levers, **the floor authority** | `docs/rust-rewrite/14-runtime-narrowing-taxonomy.md` |
| Stdlib correctness (algebraic laws, invariants) | `docs/architecture/sky-stdlib-correctness.md` |
| Sky.Live runtime + architecture | `docs/skylive/overview.md`, `docs/skylive/architecture.md` |
| Sky.Spa — client-side TEA in wasm | `docs/skyspa/overview.md` (design: `docs/skyspa/design.md`, `docs/skyspa/auto-split.md`) |
| `Std.App` — one builder, one `--target` | `docs/skyapp/overview.md` (design: `docs/design/unified-app-builder.md`) |
| `Std.Ui` layout DSL | `docs/skyui/overview.md` |
| Sky.Tui / Sky.Webview | `docs/skytui/`, `docs/skywebview/` |
| `Std.Auth` | `docs/skyauth/overview.md` |
| `Std.Db` / Codec / Store / migrations | `docs/skydb/overview.md` |
| Durable workflows — `Std.Durable` | `docs/design/durable-execution.md` |
| LLM agents — `Std.Ai` | `docs/design/std-ai.md`, `docs/ai/local.md`, `docs/ai/eval.md`, `docs/ai/dataset.md` (+ `sky doc Std.Ai.Provider` / `Agent` / `Tool` / `Policy` / `Trace` / `Memory.Pg`); capstone `examples/66-slack-agent` |
| Model slots + managed local engine — `Std.Ai.Endpoint` / `Std.Ai.Local` | `docs/ai/local.md` (design: `docs/design/std-ai-lifecycle.md`; `sky doc Std.Ai.Endpoint` / `Local`) |
| Typed AI failures and evals — `Std.Ai.Failure` / `Std.Ai.Eval` | `docs/ai/local.md#failures`, `docs/ai/eval.md` (`sky doc Std.Ai.Failure` / `Eval`) |
| Model registry — `Std.Ai.Registry` / `Std.Ai.Store` | `docs/ai/registry.md` (`sky doc Std.Ai.Registry` / `Store`); example `examples/71-ai-registry` |
| The project's AI pipeline — `Std.Ai.Pipeline`, `sky ai eval` | `docs/ai/pipeline.md` (`sky doc Std.Ai.Pipeline`); full-stack example `examples/72-ai-support-desk` |
| Training and eval data — `Std.Ai.Dataset`, `Dataset.Scrub`, `Dataset.Synthetic` | `docs/ai/dataset.md` (`sky doc Std.Ai.Dataset`); examples `68-ai-dataset`, `69-ai-scrub`, `70-ai-synthetic` |
| Fine-tuning adapters: `Std.Ai.Train` | `docs/ai/train.md` (`sky doc Std.Ai.Train`), example `examples/73-ai-train` |
| Cost and call ledger — `Std.Ai.Trace` | `docs/ai/trace.md` (`sky doc Std.Ai.Trace`) |
| Cloud training and the GPU tier: `Train.Vertex`, `Std.Ai.Serve`, `Std.Ai.Cloud.Google` | `docs/ai/serve.md`, `docs/ai/deploy-gcp.md` (`sky doc Std.Ai.Serve`); example `examples/74-ai-cloud` |
| On-device models: `Std.Ai.Device` | `docs/ai/device.md` (`sky doc Std.Ai.Device`), example `examples/75-ai-everywhere` |
| Testing the AI lifecycle (offline, local engine, live GCP) | `docs/ai/testing-guide.md` |
| CLI + LSP | `docs/tooling/cli.md`, `docs/tooling/lsp.md` |
| Testing: `sky test`, test mode + mocks, `sky fuzz` | `docs/tooling/testing.md` |
| `sky.toml` + env vars | `docs/sky-toml.md` |
| Observability / console | `docs/observability.md` |
| Getting started | `docs/getting-started.md`, `README.md` |
| Upgrading between minors | `docs/migration/v0.27.md`, `v0.28.md`, `v0.29.md` (loud / loud at run time / silent) |
| Sizing: memory, CPU, sessions | `docs/perf/sizing.md` |

## The test corpus — two layers, and where `examples/` now sits

Three bodies of code carry regression duty, and they are not interchangeable.
Which one a change belongs in is decided by what the change can break.

| Layer | Where | What it is for |
|---|---|---|
| **Layer 1** — the combinatorial corpus | `corpus/manifest.toml` + `xtask corpus` | Systematic VARIATION of language + type shapes, generated, expected values *constructed* by the generator. Membership is the manifest; no gate calls `read_dir`. Every historical defect is a pinned coordinate whose distance-1 neighbourhood is expanded automatically. |
| **Layer 2** — real-world projects | `apps/manifest.toml` (`apps/ledger`, `apps/relay`, `apps/fieldbook`, `sky-bundled/`, `examples/13-skyshop`, `rust/crates/sky/tests/*_flow.rs`) | Surfaces exercised IN COMBINATION as a user would build them: session/SSE/CSRF lifecycle, a real SQL engine (**the only Postgres coverage in the repo**), multi-replica topology, cross-backend `Std.Ui`, the CLI verbs. The class Layer 1 is structurally blind to. |
| **`examples/`** | `examples/00`–`examples/76` (73 projects) | **Documentation samples** — and the standing corpus for the *compiler-facing* ratchets that key on them. |

### The `examples/` contract

`examples/` **keeps its path** (renaming would break every doc link for no gain)
and changes its **contract**:

- **Primary role: documentation.** An example exists to be *read*. It shows one
  coherent thing a user would build, and it is referenced from `docs/`.
- **Retained regression role: the compiler-facing ratchets only.** `roundtrip`,
  `infer`, `shared-world`, `coerce-floor` and the stdout goldens key on
  `examples/`, and those keys are load-bearing — `coerce-floor` locks a
  runtime-narrowing floor per project and the goldens pin whole-program stdout.
- **Product-facing regression duty moved to Layer 2.** "Does the session survive
  an idle SSE hold", "does `liveInto` actually deliver on Postgres", "does the
  app refuse to start when its shared store is unreachable" are asserted by
  `apps/*` gates, not by an example that happens to use the feature.
- **An example is not deleted to make CI faster.** It is deleted only when the
  coverage ledger shows every surface it owns is owned elsewhere. Several
  examples are the *sole* owner of a stdlib module or a `sky.toml` surface;
  `xtask coverage-ledger` generates that table and `--check` fails if a surface
  gets weaker without an accounted `[[weakening]]`.

`00-standard-libs` is the stdlib smoke test; `13-skyshop` the Stripe-SDK-scale
FFI benchmark (76k symbols) and Layer-2 member D; `19-skyforum` the canonical
multi-module Sky.Live form; `26-ui-showcase` every `Std.Ui` primitive. Each
builds clean from a wiped slate (`rm -rf sky-out .skycache .skydeps && sky
build`). Read the example closest to what you're building first.

### Coverage ledgers — three files, all generated

`docs/coverage/` is the accounting, and **no document, ledger or verdict may
quote a number a script did not produce**:

- `denominators.json` — `xtask denominators`. How much surface exists. A
  DECREASE fails unless `removals.toml` accounts for it with a `[[removal]]`.
- `ledger.json` / `ledger.md` — `xtask coverage-ledger`. How strongly each
  surface is covered, today versus under the new architecture, plus the
  mechanically generated sole-ownership table. A surface getting **weaker**
  fails unless `removals.toml` accounts for it with a `[[weakening]]`.
- `falsifier-proofs.json` — `xtask harness --verify-falsifiers`. Which gates
  have been proven able to fail, and when.

## Project layout

```
rust/                     Sky compiler (Rust, PRIMARY — cargo workspace)
  crates/{syntax,hir,ty,lower,codegen,ffi,fmt,sky-lsp,project,xtask,sky}
runtime-go/rt/            Go runtime (embedded)
sky-stdlib/               Sky-side stdlib (embedded)
sky-bundled/              Sky Console + doc-server mini-apps
templates/                `sky init` project templates (CLAUDE.md / AGENTS.md)
examples/                 example projects
docs/                     user + contributor documentation
```
