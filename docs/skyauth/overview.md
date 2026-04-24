# Sky.Auth overview

**Authentication, in the box.** Sky ships with bcrypt password hashing, JWT signing/verification, and database-backed user registration / login as kernel modules. No `passport`, no `bcrypt-cost-finder`, no separate auth service — `import Std.Auth as Auth` and you have the surface every web app needs.

```elm
module Main exposing (main)

import Std.Auth as Auth
import Std.Db as Db
import Sky.Core.Task as Task
import Std.Log exposing (println)


main =
    Db.open "users.db"
        |> Task.andThen
            (\db ->
                Auth.register db "alice@example.com" "correct horse battery staple"
                    |> Task.andThen
                        (\userId ->
                            Auth.login db "alice@example.com" "correct horse battery staple"
                                |> Task.andThen
                                    (\user ->
                                        Auth.signToken
                                            "your-secret-min-32-bytes-please-rotate"
                                            user
                                            3600
                                            |> Task.fromResult
                                            |> Task.andThen
                                                (\jwt ->
                                                    println ("Token for user " ++ String.fromInt userId ++ ": " ++ jwt)
                                                )
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
| `Auth.passwordStrength` | `String -> Int` | 0–4 score (length + diversity heuristic) |
| `Auth.signToken` | `String -> Dict String any -> Int -> Result Error String` | HMAC-SHA256 JWT, expirySeconds from now |
| `Auth.verifyToken` | `String -> String -> Result Error (Dict String any)` | returns claims dict on success |

These return `Result` (synchronous CPU work), so they compose naturally inside any handler:

```elm
import Std.Auth as Auth
import Sky.Core.Result as Result


-- Sign a session token from your own user record
issueToken : User -> Result Error String
issueToken user =
    Auth.signToken
        secret
        (Dict.fromList [("sub", user.id), ("role", user.role)])
        86400  -- 24h
```

### Layer 2 — built-in user table (zero schema work)

If you don't already have a users table, `Auth.register` / `Auth.login` create one for you (`id`, `email`, `password_hash`, `role`, `created_at`) and return `Task` because they touch the database:

| Function | Type | Notes |
|---|---|---|
| `Auth.register` | `Db -> String -> String -> Task Error Int` | Creates `users` table on first call. Returns user id. |
| `Auth.login` | `Db -> String -> String -> Task Error (Dict String any)` | Returns `{id, email, role}` row on success |
| `Auth.setRole` | `Db -> Int -> String -> Task Error Int` | Promote / demote. Returns affected rows. |

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
    Db.open "app.db"
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
        ( Just email, Just password ) ->
            Auth.register db email password
                |> Task.andThen
                    (\uid ->
                        Task.succeed
                            (Server.json ("{\"id\":" ++ String.fromInt uid ++ "}"))
                    )

        _ ->
            Task.succeed (Server.withStatus 400 (Server.text "email + password required"))


-- POST /login — verifies, signs a token, sets it as an HttpOnly cookie
handleLogin : Db -> Request -> Task Error Response
handleLogin db req =
    case ( Server.formValue "email" req, Server.formValue "password" req ) of
        ( Just email, Just password ) ->
            Auth.login db email password
                |> Task.andThenResult
                    (\user -> Auth.signToken secret user 86400)
                |> Task.andThen
                    (\token ->
                        Task.succeed
                            (Server.text "ok"
                                |> Server.withCookie "sky_auth" token
                            )
                    )

        _ ->
            Task.succeed (Server.withStatus 400 (Server.text "email + password required"))


-- GET /me — reads the cookie, verifies, returns the claims
handleMe : Db -> Request -> Task Error Response
handleMe db req =
    case Server.getCookie "sky_auth" req of
        Just token ->
            case Auth.verifyToken secret token of
                Ok claims ->
                    Task.succeed (Server.json (claimsToJson claims))

                Err _ ->
                    Task.succeed (Server.withStatus 401 (Server.text "invalid token"))

        Nothing ->
            Task.succeed (Server.withStatus 401 (Server.text "not signed in"))
```

`Task.andThenResult` is the bridge that chains `Auth.signToken` (Result) after `Auth.login` (Task) without nested case-matching. See [Effect Boundary](../../CLAUDE.md#effect-boundary-task-everywhere-v0100) for the bridge cheatsheet.

## Configuration — `[auth]` section

`sky.toml` keys seed env vars at startup (process env still wins):

```toml
[auth]
secret     = "REPLACE-WITH-32+-BYTE-RANDOM-STRING"   # SKY_AUTH_SECRET
tokenTtl   = 86400                                    # SKY_AUTH_TOKEN_TTL (seconds)
cookieName = "sky_auth"                               # SKY_AUTH_COOKIE
driver     = "jwt"                                    # SKY_AUTH_DRIVER (jwt | session | oauth)
```

Three-layer precedence (highest wins): `SKY_AUTH_*` env var → `.env` file → `sky.toml`. See [environment-variable precedence](../../CLAUDE.md#environment-variable-precedence) for the full doctrine.

**Never commit a real secret to `sky.toml`.** The intended pattern is:
- `sky.toml` ships the *defaults* (timeouts, cookie name, driver) so a fresh `sky build` works
- `SKY_AUTH_SECRET` lives in `.env` (gitignored) for local dev and in the deployment env for production

## Production checklist

- **Rotate `SKY_AUTH_SECRET` periodically.** All outstanding tokens become invalid on rotation. Plan a deploy window.
- **Minimum 32 bytes** for the secret. `Auth.signToken` rejects shorter values with an error rather than producing weak HMACs (P1-4 in the audit).
- **Set cookie attrs**. `Server.withCookie` defaults to `HttpOnly; Secure; SameSite=Lax`. Use `Server.cookie` to override only when you actually need cross-site flow.
- **Bcrypt cost**. Default is 12, which is ~250ms on a 2024 laptop. Raise to 13–14 in production if you can spare the latency budget; lower to 10 only for CI/test fixtures.
- **Rate-limit `/login` and `/register`.** Use [`Sky.Http.Middleware.withRateLimit`](../../CLAUDE.md#standard-library) on those routes — credential stuffing is the #1 attack on any auth endpoint.
- **Validate password strength at registration**. `Auth.passwordStrength password >= 2` is a sensible minimum (length 8+ AND mixed character classes).

## Sky.Live integration

Inside a Sky.Live app, the auth flow lives in `update`:

```elm
type Msg
    = SubmitLogin LoginForm
    | LoginResult (Result Error (Dict String any))


update msg model =
    case msg of
        SubmitLogin form ->
            ( { model | loading = True }
            , Cmd.perform (Auth.login model.db form.email form.password) LoginResult
            )

        LoginResult (Ok user) ->
            ( { model | session = Just user, loading = False }, Cmd.none )

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
