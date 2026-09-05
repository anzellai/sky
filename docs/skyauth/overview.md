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
import Sky.Core.Secret as Secret
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
                                            (Secret.fromString "your-secret-min-32-bytes-please-rotate")
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
| `Auth.signToken` | `Secret -> a -> Int -> Result Error String` | HMAC-SHA256 JWT, expirySeconds from now; `a` is your claims record / dict. The signing key is an opaque `Sky.Core.Secret` — wrap at the boundary (`Secret.fromEnv "VAR"` / `Secret.fromString runtimeStr`). |
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
import Sky.Core.Secret as Secret
import Sky.Core.Error as Error exposing (Error)


secret =
    Secret.fromString (System.getenvOr "AUTH_SECRET" "")


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

> The `Server.withCookie "sky_auth" token "…"` above sets a **fixed-expiry**
> cookie, which is the right shape for this Sky.Http.Server flow. For a
> **rolling** Sky.Live session that re-issues on activity under an absolute cap,
> use `Auth.signSlidingToken` + `Live.withAuthSliding` + the builder-owned
> `Auth.setSlidingCookie` setter instead — a hand-rolled `Server.withCookie` for
> the sliding cookie is unsupported (its attributes would drift from the
> re-issue). See [Sliding session tokens](#sliding-rolling-session-tokens).

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
secret = Secret.fromEnv "SKY_AUTH_TOKEN_SECRET"   -- opaque Secret; redacts itself in logs
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

## Sliding (rolling) session tokens

A fixed-expiry token forces a bad trade: a **short** `exp` logs active users
out mid-session; a **long** `exp` means a stolen token is valid for the whole
window. Sky.Live offers an **opt-in sliding token** that re-issues a fresh short
window on activity — so an active user stays signed in while an **idle** token
still lapses on schedule — under a hard **absolute-lifetime cap** that no amount
of activity can extend.

It has three parts:

1. **`Auth.signSlidingToken secret claims { windowSeconds, maxLifetimeSeconds }`**
   — issue the token at login. It stamps `iat`, `exp = iat + windowSeconds`,
   `aexp = iat + maxLifetimeSeconds` (the immutable cap), and `w = windowSeconds`
   as its own signed claim. It rejects `windowSeconds > maxLifetimeSeconds` with
   an `Err`.
2. **`Live.withAuthSliding { cookie, secretEnv, sameSite, revokedCheck }`** — the
   builder that mounts the re-issue middleware and OWNS the cookie's attributes.
3. **`Auth.setSlidingCookie req token resp`** — the builder-owned setter the
   **login handler** uses to write the cookie with the SAME attributes the
   re-issue will use.

The one-line opt-in. `Live.withAuthSliding` is a **Sky.Live low-level builder** —
`Std.App` composes Sky.Live but does not surface this sliding-session hook, so mount
it on a `Std.Live` app directly (a Sky.Live app with a login `api` route):

```elm
secret =
    System.getenvOr "SKY_AUTH_TOKEN_SECRET" "dev-secret-min-32-bytes-please-rotate"


-- login handler: sign a sliding token and set it with the builder-owned setter
login req =
    case Auth.signSlidingToken secret { sub = "42" } { windowSeconds = 900, maxLifetimeSeconds = 86400 } of
        Ok token ->
            Task.succeed (Server.json "{\"ok\":true}" |> Auth.setSlidingCookie req token)

        Err _ ->
            Task.succeed (Server.withStatus 500 (Server.text "sign failed"))


main =
    Live.app
        (Live.config
            { init = init, update = update, view = view
            , subscriptions = subscriptions
            , routes = [ Live.route "/" Home, Live.api "POST /login" login ]
            , notFound = Home
            }
            |> Live.withAuthSliding
                { cookie = "sky_auth"
                , secretEnv = "SKY_AUTH_TOKEN_SECRET"
                , sameSite = "Strict"
                , revokedCheck = Nothing
                }
        )
```

**The login handler MUST use `Auth.setSlidingCookie`, not a hand-rolled
`Server.withCookie`.** The middleware re-issues the cookie on later requests, but
it **cannot read cookie attributes off a request** — browsers send only
`name=value`, never `Path` / `SameSite` / `Secure`. So the login setter and the
re-issue both build the cookie from the ONE config the builder registered
(`Path=/`, HttpOnly, the builder's `SameSite`, `Secure` by the shared
`cookieSecureFor` rule, and the sliding Max-Age). A hand-rolled
`Server.withCookie` for the sliding cookie would drift from the re-issue's
attributes and is **unsupported**.

`secretEnv` is the **name** of the environment variable holding the HMAC secret
— never the secret value. The operator owns it; the middleware reads it at
request time.

### The stolen-token exposure delta

This is the standard sliding-session trade-off, stated plainly: **continuous use
of a stolen token slides it all the way to `aexp`, not just to the next `exp`.**
An attacker who keeps a stolen token active holds the session until the absolute
cap — the window does not save you against *continuous* abuse; it only expires an
*idle* token. Two things bound the exposure:

- **The absolute cap (`maxLifetimeSeconds`).** No activity extends the token past
  `aexp`. Pick a cap you are willing to have a stolen token live to.
- **`revokedCheck` — a per-subject revocation hook** `sub -> Task Error Bool`,
  consulted at **re-issue time only** (not per request — the hot path stays
  cheap). Return `True` to stop the slide; the token then lapses at its current
  `exp`. Wire it to your "session revoked / password changed / user disabled"
  check so a compromised session can be cut off within one window instead of
  waiting for the cap. If you pass `Nothing`, there is no check and
  **revocation latency equals `maxLifetimeSeconds`** — so keep the cap short when
  you skip the hook. (A revocation-check error fails **closed**: the slide stops,
  the token still lives to its current `exp`.)

### The SSE caveat

The token slides on **interaction** — an event `POST` or a page `GET`, where the
server writes response headers — **not on the SSE heartbeat.** An SSE stream's
headers are written once, at connect, so the heartbeat that keeps the *server*
session alive cannot re-issue the *cookie* mid-stream (the same limitation the
`sky_sid` session cookie has). A tab that sits idle under a live SSE longer than
`windowSeconds` between interactions will let its auth token lapse. **Set
`windowSeconds` comfortably above your expected SSE-idle gaps** (a few minutes of
inactivity between clicks is typical; `900` = 15 min is a reasonable floor).

## User revocation & suspension (PULL model)

Sliding `revokedCheck` stops a *token* from re-issuing, but it does nothing for a
session that is already live inside a Sky.Live app. For that — "log this user
out **now**", "ban this account" — Sky ships a **pull-model** revocation gate:
the state lives in one shared table, and every session checks **itself** against
it on each interaction, with no broker, no cross-instance fan-out, and no
session index.

There are **two independent states**, and the distinction matters:

| API | Meaning | Enforced by |
|---|---|---|
| `Auth.revokeUser db userId` | **Kill existing sessions/tokens.** Stamps `revoked_at = now`. Anything issued *before* now stops working; a **fresh login afterwards is fine**. Revoke ≠ ban. | The session gate + the sliding `revokedCheck` (wire it to `Auth.isRevoked`). |
| `Auth.disableUser db userId` | **Ban the account.** Sets `users.disabled_at`. The user **cannot log in again** (checked *before* the password verify) and any live session is evicted. Reverse with `Auth.enableUser`. | `Auth.login` + the session gate. |

Read the combined verdict with `Auth.userAccessState db userId issuedAt : Task Error AccessState` (`Active` / `Revoked` / `Disabled`, with `Disabled` taking precedence), or the booleans `Auth.isRevoked` / `Auth.isDisabled`.

### Wiring it into a Sky.Live app

Three steps:

1. **Register the gate** with `Live.withRevocation db` — hand it the **same `Db`
   you pass to `Std.Auth`** (the shared table lives there, *not* on the session
   store, which defaults to in-memory and is not shared across replicas). This
   is the enabling signal: until you call it, the gate is inert and public
   apps pay nothing.
2. **Bind sessions to users at login** with `Live.bindSessionUser userId`
   (performed as a `Cmd`), so the gate has a subject to check. A sliding-auth
   (token) app **auto-binds** from the verified token `sub` and never needs
   this call; a session-based app must make it. A session that reaches the gate
   **unbound** under an enabled gate raises a **loud runtime warning** and
   `sky doctor` lint — it is never silently treated as allowed.
3. **Revoke / disable** from an admin action. **Sky provides the mechanism; your
   app owns the "is the caller an admin?" authorization** — call `revokeUser` /
   `disableUser` only after your own admin check.

Like sliding tokens, `Live.withRevocation` / `Live.bindSessionUser` are **Sky.Live
low-level builders** not surfaced through `Std.App`, so wire them on a `Std.Live` app
directly:

```elm
import Std.Live as Live exposing (app, config, withRevocation, bindSessionUser)
import Std.Auth as Auth

-- At login, bind the session so revocation can reach it:
update msg model =
    case msg of
        LoggedIn userId ->
            ( { model | user = Just userId }
            , Cmd.perform (Live.bindSessionUser userId) (\_ -> Ignore)
            )
        -- Admin action (gate on YOUR OWN is-admin check first):
        AdminRevoke targetId ->
            ( model, Cmd.perform (Auth.revokeUser model.db targetId) (\_ -> Ignore) )
        AdminBan targetId ->
            ( model, Cmd.perform (Auth.disableUser model.db targetId) (\_ -> Ignore) )

main =
    app
        (config { init = init, update = update, view = view
                , subscriptions = subscriptions
                , routes = [ {- … -} ], notFound = Home }
            |> Live.withRevocation db   -- the SAME db Std.Auth uses
        )
```

On a `Revoked` / `Disabled` verdict the session is **evicted**: its goroutines
(tickers, subscribers, in-flight `Cmd.perform` completions) are retired, its
blob is dropped from the store, and the browser gets the standard
`session-lost` signal (a full reload). The gate sits inside the one dispatch
funnel every mutation path shares, so a revoked session **mutates nothing** —
not even a `Sub.every` tick or a completing effect races through.

### `userId` is a String — key it exactly

The admin APIs take a **`String`** user id, not an `Int`. That is deliberate:
an OAuth subject is a string, and a numeric id passed as a String is stored
**exactly**. A numeric JWT `sub` decodes as a float64 and **loses precision
above 2⁵³** — so if your ids can get that large, carry them as Strings
end-to-end. The runtime canonicalises every id (string verbatim; an integer to
its decimal text) on **both** the write and the read, so `revokeUser db 42`-style
callers and the gate never disagree.

### Read freshness and the ≤TTL latency knob

The gate reads `revoked_at` / `disabled_at` **fresh from the shared table on
every evaluation** by default, so a revoke on replica A stops a dispatch on
replica B immediately — the verdict is **never** stored on the session blob. If
that read is too hot for your scale, set **`SKY_LIVE_REVOCATION_CACHE_TTL`** to a
whole number of seconds: each replica then caches a user's verdict for up to
that window, trading **≤TTL of revocation latency** for fewer reads. The default
(`0`) is a fresh read every time — instant, at the cost of one indexed
point-lookup per interaction. A same-replica `revokeUser` / `disableUser`
invalidates that user's cache entry immediately regardless of the TTL.

## See also

- [`examples/12-skyvote`](../../examples/12-skyvote/) — full Sky.Live voting app with email + password auth
- [`examples/13-skyshop`](../../examples/13-skyshop/) — multi-role auth (customer / artist / admin) on Firestore
- [`examples/17-skymon`](../../examples/17-skymon/) — admin-only dashboard with JWT-cookie session
- [Sky.Db overview](../skydb/overview.md) — the database layer Auth.register / Auth.login uses
- [Standard library reference](../stdlib.md) — full kernel surface
