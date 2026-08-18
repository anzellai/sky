# Std.Auth overview

> **Status**: the Rust compiler (`rust/`, `cargo build --release -p sky`)
> is the primary Sky compiler; the Haskell compiler is preserved under
> `legacy-haskell-compiler/`. Verified by the example sweep + compiler test
> suite (`cargo test` + xtask gates). See
> [`../compiler/journey.md`](../compiler/journey.md) for the changelog.


**Authentication, in the box.** Sky ships with bcrypt password hashing, JWT signing/verification, and database-backed user registration / login as kernel modules. No `passport`, no `bcrypt-cost-finder`, no separate auth service — `import Std.Auth as Auth` and you have the surface every web app needs.

```elm
module Main exposing (main)

import Std.Auth as Auth
import Std.Db as Db
import Sky.Core.Task as Task
import Std.Log exposing (println)


main =
    Db.open "sqlite" "users.db"
        |> Task.andThen
            (\db ->
                Auth.register db "alice@example.com" "correct horse battery staple"
                    |> Task.andThen
                        (\userId ->
                            Auth.login db "alice@example.com" "correct horse battery staple"
                                |> Task.andThenResult
                                    (\uid ->
                                        Auth.signToken
                                            "your-secret-min-32-bytes-please-rotate"
                                            { sub = uid }
                                            3600
                                    )
                                |> Task.andThen
                                    (\jwt ->
                                        println ("Token for user " ++ String.fromInt userId ++ ": " ++ jwt)
                                    )
                        )
            )
        |> Task.run
```

## What's in the surface

`Std.Auth` is intentionally small — these are the operations every app needs and nothing more. Pick the layer that fits your app:

### Layer 1 — primitives (bring your own user table)

If you already have a users table and just want to hash passwords + issue JWTs:

| Function | Type | Notes |
|---|---|---|
| `Auth.hashPassword` | `String -> Result Error String` | bcrypt, default cost 12 |
| `Auth.hashPasswordCost` | `String -> Int -> Result Error String` | explicit cost (10–14 typical) |
| `Auth.verifyPassword` | `String -> String -> Result Error Bool` | constant-time compare |
| `Auth.passwordStrength` | `String -> Result Error String` | `"weak" / "fair" / "strong"` category label |
| `Auth.signToken` | `String -> a -> Int -> Result Error String` | HMAC-SHA256 JWT, expirySeconds from now; `a` is your claims record / dict |
| `Auth.verifyToken` | `String -> String -> Result Error a` | parametric — decode into the claims record / dict the call site annotates |

These return `Result` (synchronous CPU work), so they compose naturally inside any handler:

```elm
import Std.Auth as Auth
import Sky.Core.Result as Result


-- Sign a session token from your own user record
type alias Claims = { sub : Int, role : String }

issueToken : User -> Result Error String
issueToken user =
    Auth.signToken
        secret
        ({ sub = user.id, role = user.role } : Claims)
        86400  -- 24h
```

`signToken`'s claims arg is parametric (`a`) — pass any record, dict, or primitive. `verifyToken` round-trips into the type the call site annotates.

### Layer 2 — built-in user table (zero schema work)

If you don't already have a users table, `Auth.register` / `Auth.login` create one for you (`id`, `email`, `password_hash`, `role`, `created_at`) and return `Task` because they touch the database:

| Function | Type | Notes |
|---|---|---|
| `Auth.register` | `Db -> String -> String -> Task Error Int` | Creates `users` table on first call. Returns user id. |
| `Auth.login` | `Db -> String -> String -> Task Error Int` | Returns the authenticated user id on success. |
| `Auth.setRole` | `Db -> Int -> String -> Task Error ()` | Promote / demote. |

Schema is portable across SQLite and Postgres — the `id` column uses `INTEGER PRIMARY KEY AUTOINCREMENT` on SQLite and `SERIAL PRIMARY KEY` on Postgres automatically.

## Walkthrough — register / login / protected route

A complete `Sky.Http.Server` flow. Three handlers: register a user, log them in (set a cookie), and gate a route on the cookie.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.Dict as Dict
import Sky.Http.Server as Server
import Sky.Http.Server exposing (Request, Response)
import Std.Auth as Auth
import Std.Db as Db
import System
import Sky.Core.Error as Error exposing (Error)


secret =
    System.getenvOr "AUTH_SECRET" ""


main =
    Db.connect ()
        |> Task.andThen
            (\db ->
                Server.listen 8000
                    [ Server.post "/register" (handleRegister db)
                    , Server.post "/login"    (handleLogin db)
                    , Server.get  "/me"       (handleMe db)
                    ]
            )


-- POST /register — creates a user, returns the new id
handleRegister : Db -> Request -> Task Error Response
handleRegister db req =
    case ( Server.formValue "email" req, Server.formValue "password" req ) of
        ( "", _ ) ->
            Task.succeed (Server.withStatus 400 (Server.text "email + password required"))

        ( _, "" ) ->
            Task.succeed (Server.withStatus 400 (Server.text "email + password required"))

        ( email, password ) ->
            Auth.register db email password
                |> Task.andThen
                    (\uid ->
                        Task.succeed
                            (Server.json ("{\"id\":" ++ String.fromInt uid ++ "}"))
                    )


-- POST /login — verifies, signs a token, sets it as an HttpOnly cookie.
-- The attrs are spelled out: the two-and-three-argument forms of
-- `withCookie` emit `Path=/; HttpOnly; SameSite=Lax` and add `Secure`
-- only when the process is in production (see "Production checklist").
handleLogin : Db -> Request -> Task Error Response
handleLogin db req =
    case ( Server.formValue "email" req, Server.formValue "password" req ) of
        ( "", _ ) ->
            Task.succeed (Server.withStatus 400 (Server.text "email + password required"))

        ( _, "" ) ->
            Task.succeed (Server.withStatus 400 (Server.text "email + password required"))

        ( email, password ) ->
            Auth.login db email password
                |> Task.andThenResult
                    (\uid -> Auth.signToken secret { sub = uid } 86400)
                |> Task.andThen
                    (\token ->
                        Task.succeed
                            (Server.text "ok"
                                |> Server.withCookie "sky_auth" token "Path=/; HttpOnly; Secure; SameSite=Lax"
                            )
                    )


-- GET /me — reads the cookie, verifies, returns the claims
type alias Claims = { sub : Int }


handleMe : Db -> Request -> Task Error Response
handleMe db req =
    case Server.getCookie "sky_auth" req of
        Just token ->
            case Auth.verifyToken secret token of
                Ok claims ->
                    Task.succeed (Server.json ("{\"sub\":" ++ String.fromInt claims.sub ++ "}"))

                Err _ ->
                    Task.succeed (Server.withStatus 401 (Server.text "invalid token"))

        Nothing ->
            Task.succeed (Server.withStatus 401 (Server.text "not signed in"))
```

`Task.andThenResult` is the bridge that chains `Auth.signToken` (Result) after `Auth.login` (Task) without nested case-matching. See [Effect Boundary](../../CLAUDE.md#effect-boundary-task-everywhere-v0100) for the bridge cheatsheet.

## Configuration — there is no `[auth]` section

**`Std.Auth` is not configured from `sky.toml`.** It is a library: `signToken`
takes the secret + TTL as **arguments**, and your handler sets the session
cookie (`Server.withCookie`). There was nothing for a config layer to seed, so
the inert `[auth]` block (`driver` / `cookieName` / `tokenTtl`) was **deleted** —
it was parsed, seeded into `SKY_AUTH_*` and read by nothing for four minor
versions. A residual `[auth]` key now raises the standard inert-key build
warning.

Configure `Std.Auth` from your own code, reading whatever environment variables
you choose at the call site:

```elm
secret = System.getenvOr "SKY_AUTH_TOKEN_SECRET" "dev-secret"
ttl    = System.getenvOr "SKY_AUTH_TOKEN_TTL" "86400" |> String.toInt |> Result.withDefault 86400

token  = Auth.signToken secret claims ttl
```

These `SKY_AUTH_*` reads are a **convention in your code**, not runtime settings —
nothing in `runtime-go/` reads them, and the compiler no longer seeds any of
them from `sky.toml`. Set them in the environment (shell, `.env`, secret
manager).

> **`SKY_AUTH_TOKEN_SECRET` is an environment variable, never a config-file
> value.** The production gate reads the literal, unprefixed name
> (`rust/crates/sky/src/main.rs:3791`) and `sky init` writes it into the
> generated `.env`. It must be ≥ 32 bytes; `Auth.signToken` and the runtime both
> reject a shorter one. Putting a signing key in a committed file is the worst
> outcome — it is ignored *and* leaked.

**Never commit a real secret.** `SKY_AUTH_TOKEN_SECRET` lives in `.env`
(gitignored) for local dev and in the deployment env for production.

## Production checklist

- **Rotate `SKY_AUTH_TOKEN_SECRET` periodically.** All outstanding tokens become invalid on rotation. Plan a deploy window.
- **Minimum 32 bytes** for the secret. `Auth.signToken` rejects shorter values with an error rather than producing weak HMACs; the runtime also refuses to start with a short `SKY_AUTH_TOKEN_SECRET`.
- **`Secure` is not in the attribute default — the runtime adds it, on two
  signals.** `Server.withCookie`'s two- and three-argument forms emit
  `Path=/; HttpOnly; SameSite=Lax` (`Server_withCookie` in
  `runtime-go/rt/rt.go`), with no `Secure` in the string. The runtime then
  adds `; Secure` when either signal is true (`cookieSecureFor` in
  `runtime-go/rt/cookie_secure.go`):

  1. **the response goes back over HTTPS** — direct TLS,
     `X-Forwarded-Proto: https`, or `X-Forwarded-Ssl: on`; or
  2. **the production gate is on** — `ENV` (or `<PREFIX>_ENV`) set to
     anything that is not `dev` / `development` / `local`
     (`productionFromEnv` in `rt/observability.go`).

  Signal 1 is checked at the point the response is written, where the
  request is in hand, so it covers a deployment that forgot to set `ENV`
  but does terminate TLS. **Neither signal fires under `sky run` on
  `http://localhost`, or in CI over plain HTTP with no `ENV`** — and that
  is deliberate: a `Secure` cookie on a plain-HTTP origin is never sent
  back, so adding it there would break every local login. If your CI or
  staging tier serves auth over plain HTTP, the cookie has no `Secure`
  attribute and nothing will tell you.

  Cookies whose name carries the `__Host-` / `__Secure-` prefix, and any
  cookie sent `SameSite=None`, are `Secure` unconditionally — the spec
  requires it.

  To pin the attributes yourself, use the **four-argument form**,
  `Server.withCookie name value attrs resp`, which passes your string
  through (the runtime may still append `; Secure`, never a second copy) —
  that is what the login handler above uses:

  ```elm
  resp |> Server.withCookie "sky_auth" token "Path=/; HttpOnly; Secure; SameSite=Strict"
  ```

  `Server.cookie` is **not** an override: it takes only a name and a value
  (`Server_cookie` in `rt.go`, `Sky/Http/Server.sky:254`) and carries no
  attribute control at all.

  > This bullet used to read "`Server.withCookie` defaults to
  > `HttpOnly; Secure; SameSite=Lax`. Use `Server.cookie` to override" —
  > both halves false. A reader following it shipped a session token with
  > no `Secure` attribute and had no way to notice, because the named
  > remedy has no parameter that could have fixed it.
  >
  > It was then corrected to say `Secure` is added "only when the process
  > is in production", which was accurate at the time and is now too
  > narrow: the decision moved to the point the response is written, so
  > the HTTPS signal applies as well. Line-number citations were dropped
  > in the same pass — they were stale within a day.
- **Bcrypt cost**. Default is 12, which is ~250ms on a 2024 laptop. Raise to 13–14 in production if you can spare the latency budget; lower to 10 only for CI/test fixtures.
- **Rate-limit `/login` and `/register`.** Use [`Sky.Http.Middleware.withRateLimit`](../../CLAUDE.md#standard-library) on those routes — credential stuffing is the #1 attack on any auth endpoint.
- **Validate password strength at registration**. `Auth.passwordStrength password` returns `Result Error String` where the body is `"weak" / "fair" / "strong"`; reject `"weak"` at registration as a baseline.

## Security-critical kernels require typed arguments

Every public Auth kernel — `hashPassword` / `hashPasswordCost` / `passwordStrength` / `signToken` / `verifyToken` / `register` / `login` / `setRole` — gates at **compile time** on every `String`-typed parameter slot. Bridging an `any`-typed binding into any of those slots is a compile-time `Sky.Auth.UntypedBoundary` (`E4006`) error, not a runtime surprise.

```elm
-- Compile-time error: bridge's static type carries `any`.
bridge : any
bridge = Ffi.kernel "Time_unixMillis"

main =
    case Auth.hashPassword bridge of           -- E4006
        Ok h  -> println h
        Err _ -> println "bad"
```

```text
-- CODEGEN ERROR ───────────────────────────── src/Main.sky:9:28 [E4006]
Sky.Auth.UntypedBoundary — argument 1 of `Auth.hashPassword` carries no
typed-String contract at the Sky type level.
```

The fix is always to annotate the bridging binding with a concrete type (`String`, or a type alias whose body is `String`) before it reaches the kernel.

**Runtime defence in depth.** If a non-String value reaches the kernel through some other path (an FFI return whose Go type doesn't match its Sky annotation, etc.), the runtime returns a typed Err with a **fixed** message — `<kernel>: expected String`. The actual Go type of the offending value is captured in a server-side audit log (`[WARN] auth.boundary kernel=<tag> goType=<%T> reason=non-string-arg`) and **never** leaks into the user-visible error message. That blocks the timing / log-scraping reconnaissance an attacker would otherwise use to learn how the upstream binding is shaped.

## Sky.Live integration

Inside a Sky.Live app, the auth flow lives in `update`:

```elm
type Msg
    = SubmitLogin LoginForm
    | LoginResult (Result Error Int)


update msg model =
    case msg of
        SubmitLogin form ->
            ( { model | loading = True }
            , Cmd.perform (Auth.login model.db form.email form.password) LoginResult
            )

        LoginResult (Ok userId) ->
            ( { model | session = Just userId, loading = False }, Cmd.none )

        LoginResult (Err _) ->
            ( { model | error = Just "invalid credentials", loading = False }, Cmd.none )
```

For password fields specifically, see [the form-with-passwords pattern](../../CLAUDE.md#forms-with-passwords-and-other-sensitive-inputs) — submit on form submit, never round-trip the secret through Model.

## See also

- [`examples/12-skyvote`](../../examples/12-skyvote/) — full Sky.Live voting app with email + password auth
- [`examples/13-skyshop`](../../examples/13-skyshop/) — multi-role auth (customer / artist / admin) on Firestore
- [`examples/17-skymon`](../../examples/17-skymon/) — admin-only dashboard with JWT-cookie session
- [Sky.Db overview](../skydb/overview.md) — the database layer Auth.register / Auth.login uses
- [Standard library reference](../stdlib.md) — full kernel surface
