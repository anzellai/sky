# CLAUDE.md — Sky Language Project

This is a [Sky](https://github.com/anzellai/sky) project: a pure functional,
Elm-family language that compiles to typed Go and ships as a single `sky` binary
(you also need Go 1.21+). **If it compiles, it works** — every side effect returns
`Task Error a`, every fallible value returns `Result Error a`, and `sky check`
runs `go build` on the emitted Go, so shape mismatches surface at check time. No
runtime panics from well-typed code, no nil leakage, no silent numeric coercion.

Type annotations are load-bearing: `f : String -> Int -> Result Error Profile`
rejects a body that would infer wider. Inline records aren't allowed in
signatures — give any record in a signature a `type alias`.

## The full API lives in `sky doc` — not here

This file is orientation only. For **complete, always-current signatures + docs**:

```sh
sky doc --list            # every module (stdlib + your project), incl. Std.Live etc.
sky doc Std.Ui            # one module's exported bindings, with types + summaries
sky doc --serve           # browsable HTML API at http://localhost:8080
```

Reach for `sky doc <Module>` whenever you need an exact signature. Do not
memorise or inline signatures here — they drift; `sky doc` doesn't.

## Choose the app shape first

| You want… | Use | Entry |
|---|---|---|
| Web app (forms, real-time, UI state) | **Sky.Live** | `Live.app (Live.config {…})` |
| HTTP/JSON API (no browser UI) | **Sky.Http.Server** | `Server.listen 8000 [...]` |
| Terminal UI | **Sky.Tui** | `Tui.app (Tui.config {…})` |
| Desktop app (macOS) | **Sky.Webview** | `Webview.app { … }` (closed record) |
| One-shot CLI / cron | **Sky.Cli** | `main = Task.run cmd` |

Before scaffolding more than a proof of concept, confirm with the user:
**persistence** (SQLite default / Postgres for multi-instance / none), **auth**
(`Std.Auth` / OAuth / external), **session store** for Sky.Live (memory dev /
sqlite single-instance / redis|postgres for multi-replica), and **deploy target**.

## Std.Ui is the default for application interfaces

Build UI with **`Std.Ui`** — a typed, no-CSS layout DSL (`row`/`column`/`el` +
typed attributes from `Background`/`Border`/`Font`/`Input`/`Region`). It
HTML-escapes everything and renders identically across Sky.Live, Sky.Tui, and
Sky.Webview. Reach for `Std.Html` only to wrap raw markup. Never write CSS
strings; never emit raw HTML/JS (`data-sky-eval` is forbidden).

```elm
import Std.Ui as Ui
import Std.Ui.Font as Font

view model =
    Ui.layout []
        (Ui.column [ Ui.spacing 12, Ui.padding 16 ]
            [ Ui.el [ Font.size 24, Font.bold ] (Ui.text model.title)
            , Ui.button [] { onPress = Just Save, label = Ui.text "Save" }
            ])
```

The `<main>` landmark element is `Std.Html.mainNode` (not `main`, which would
collide with your program's `main` entry point). Prefer `Std.Ui.Region` for
landmarks anyway.

## Pinned defaults (apply unless the user overrules)

| Concern | Default |
|---|---|
| UI | `Std.Ui` (typed, no CSS). `Std.Html` only for raw markup. |
| Sky.Live navigation | Every internal link is `sky-nav` (`Attr.attribute "sky-nav" ""` on `<a>`). ONE persistent SSE per session; a plain `<a href>` full-reload opens a fresh SSE each page and can freeze the tab. |
| Auth | `Std.Auth` — bcrypt + HS256 JWT cookies. `Auth.login` / `Auth.register` return `Task Error Int` (the user id). Never `fmt`-print a secret. |
| Password forms | `Ui.form [Ui.onSubmit DoSignIn]` with a typed record arg. Never per-keystroke `onInput` on a password field. |
| DB | `Std.Db` + SQLite for prototypes; PostgreSQL for multi-instance. |
| Money / decimals | `Std.Money` on `Std.Decimal`. Never raw `Float` for currency. |
| Concurrency | `Cmd.batch` / `Task.parallel`; in-process pub/sub via `Cmd.publish` + `Sub.subscribeTopic`. |
| Errors | `Result Error a` / `Task Error a`. Never `String` as the error type. |
| Logs | `Std.Log` structured logs; `/_sky/console` auto-mounts in dev. |
| Product analytics | `Std.Analytics` — typed events (`Money`/`Pii` props), consent-gated + anonymous by default, opt-in Sky.Live auto page-views (`analytics = { pageViews = True }`), SQLite store + Sky Console **Analytics** tab. |

## Effect boundary — Task everywhere

Every observable side effect returns `Task Error a` (`File.*`, `Http.*`, `Db.*`,
`Time.now`, `Random.*`, `Log.*`, `System.*` except `getenvOr`). Pure stays bare
(`String.*`, `List.*`, `Crypto.sha256`); fallible-pure returns `Result e a` /
`Maybe a` (`String.toInt`, JSON decoders). A discarded `let _ = TaskExpr` is
auto-forced. Bridge with `Task.fromResult` / `Result.andThenTask` /
`Task.onError`. Top-level `apiKey = System.getenv "K" |> Task.run |> Result.withDefault ""`
still needs the explicit `Task.run`.

**Top-level bindings are memoised — evaluated once, then cached.** A
zero-parameter top-level binding is a single VALUE: `apiKey` reads the env
once, `db = Task.run (Db.connect ())` opens ONE shared connection pool. If
you need a FRESH value per use — a UUID, the current time, a random number
— make it a function, not a binding: `newId : () -> String; newId _ =
Task.run Uuid.v4 |> Result.withDefault ""`, called `newId ()`. `newId =
Task.run Uuid.v4` at top level freezes to one UUID forever (the compiler
warns). A bare `x = Uuid.v4` (an un-forced `Task` value) is fine.

Two-level error pattern: log a structured line with a short `errId` server-side,
return a user-facing `Task.fail (Error.unexpected ("... ref " ++ errId))`.

## Language syntax

```elm
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

type Msg = Increment | Decrement

update : Msg -> Int -> Int
update msg count =
    case msg of
        Increment -> count + 1
        Decrement -> count - 1

main = println (String.fromInt (update Increment 0))
```

`|>` `<|` pipelines · `::` cons · `\x -> x + 1` lambdas · `let…in` ·
`case…of` (exhaustiveness-checked) · `{ rec | field = value }` update ·
`import M as Alias exposing (name)`. Triple-quoted multiline strings support
`{{expr}}` interpolation (escape as `\{{`).

Every non-aliased `import M exposing (..)` also binds `M.<name>` as an
auto-qualifier. An `exposing` name that the module doesn't export is a hard
error (`[E1011] NOT EXPOSED`).

## Sky.Live essentials

`Live.app` takes a typed **builder** config (v0.19): `Live.config { init, update,
view, subscriptions, routes, notFound }` builds an opaque `AppConfig`; attach
optional fields with `withX` builders in a pipe:

```elm
import Std.Live exposing (app, config, route, withHead)

main =
    app
        (config
            { init = init, update = update, view = view
            , subscriptions = subscriptions
            , routes = [ route "/" Home ], notFound = Home
            }
            |> withHead headFor      -- optional; also withGuard/withAnalytics/withStatic/…
        )
```

**Migrating an older project:** if you see the pre-v0.19 record literal
`Live.app { init = …, update = …, …, head = … }` (or `Tui.app` / `Tui.program` /
`Cli.program`), migrate it — that form is REMOVED and won't compile. Keep the six
required fields (`init`/`update`/`view`/`subscriptions`/`routes`/`notFound`) inside
`config { … }`, and move every OPTIONAL field to a `|> withX` in the pipe
(`head` → `withHead`, `guard` → `withGuard`, `analytics` → `withAnalytics`,
`onKey` → `withOnKey`, `onLine` → `withOnLine`, …); add `config` + the `withX`
names you use to the `exposing (…)` list. The compiler error for the old form
prints this same recipe. `Webview.app` keeps its closed record. Full guide:
`docs/v0.19/migration-builder-cfg.md`. Same pattern for `Tui.app` /
`Tui.program` (`Tui.config` + `withOnKey`) and `Cli.program` (`Cli.config` +
`withOnLine`). `init` runs
per-session (a reload restores Model from the store; it does NOT re-run `init`).
`init` receives a `req` with `path` / `query` / `params` / `method` / `headers` /
`cookies`. `update msg model` returns `(Model, Cmd Msg)`; `Cmd.perform task ToMsg`
runs a task in a goroutine and dispatches the result back over SSE.

Wire-event args: text/select → `[value : String]`; number/range → `[Float]`;
checkbox → `[Bool]`; submit → `[formData]` (a `Dict String String` or a typed
record alias); keydown → `[key : String]`. Radios: one `onClick (Choose v)` per
label, not `onInput`.

Password forms use `onSubmit` with a typed record (`DoSignIn AuthCreds`), never
`value=`/`onInput` on the password input — so the secret never enters the Model
or the session store, and password managers don't re-prompt.

## Commands

```sh
sky init [name] [--production]  # new project — SQLite default; --production = Postgres one-DB + docker-compose
sky build src/Main.sky       # compile → sky-out/app
sky run src/Main.sky         # build + run   (--profile for runtime CPU/mem/hang profiling)
sky check src/Main.sky       # type-check + go build (no binary)
sky verify                   # one-shot project gate: fmt + check + build + tests
sky test tests/MyTest.sky    # Sky.Test runner
sky fmt src/Main.sky         # format (always run after editing .sky)
sky doc <Module> | --list    # API docs (the source of truth for signatures)
sky watch src/Main.sky       # rebuild + restart on save
sky add <go/pkg> | remove | install | update   # Go FFI deps
```

Run `sky verify` before you consider a change done — it runs fmt-clean +
type-check + production build + every `tests/*.sky` suite, and exits non-zero on
any failure.

## sky.toml

```toml
name    = "myapp"
version = "0.1.0"
entry   = "src/Main.sky"
bin     = "app"          # output binary name

[source]
root = "src"

[live]                   # Sky.Live apps
port  = 8000
store = "sqlite"         # memory | sqlite | redis | postgres | firestore
ttl   = 1800

[database]               # persistence
driver = "sqlite"
path   = "app.db"

[auth]                   # Std.Auth
driver     = "jwt"
cookieName = "sky_sid"   # secret comes from SKY_AUTH_TOKEN_SECRET (>=32 bytes), never committed
```

## Non-negotiables

- **Types over strings for errors** — `Result Error a` / `Task Error a`, never `Result String a`.
- **No raw HTML/JS** — `Std.Ui` escapes everything; `data-sky-eval` is forbidden.
- **Secrets are typed** — `Auth.signToken`/`verifyToken` take `String`; never `fmt.Sprintf("%v", secret)`.
- **Money is `Std.Money`**, never `Float`.
- **`sky fmt` after editing**, **`sky verify` before shipping.**
- **Production gate**: with `ENV=production`, set `SKY_AUTH_TOKEN_SECRET` (>=32 bytes) and `SKY_CONSOLE_AUTH`; use a shared session store (redis/postgres) + sticky sessions when you run more than one replica.

When a signature or module is unclear, run `sky doc <Module>` — it is complete
and current. This file is not.
