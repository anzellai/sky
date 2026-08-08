# Auth

When you own the users, `Std.Auth` is the default: bcrypt password hashing, JWT
cookies, and database-backed register/login — no separate auth service, no
hand-rolled crypto.

```elm
import Std.Auth as Auth
```

## Register and log in

`register` and `login` touch the database, so they're `Task`s. If you don't have a
users table, `register` creates one (`id`, `email`, `password_hash`, `role`,
`created_at`):

```elm
signUp : Db -> Task Error String
signUp db =
    Auth.register db "alice@example.com" "correct horse battery staple"
        |> Task.andThen (\_ -> Auth.login db "alice@example.com" "correct horse battery staple")
        |> Task.andThen
            (\user ->
                -- issue a signed session token, valid 24h
                case Auth.signToken sessionSecret user 86400 of
                    Ok token -> Task.succeed token
                    Err e -> Task.fail e
            )
```

## Hashing and verifying directly

If you manage your own user rows, the primitives are pure-fallible (`Result`), so
they're easy to test:

```elm
-- Auth.hashPassword   : String -> Result Error String       (bcrypt)
-- Auth.verifyPassword : String -> String -> Result Error Bool (constant-time)

checkLogin : String -> String -> Bool
checkLogin password storedHash =
    Result.withDefault False (Auth.verifyPassword password storedHash)
```

## Tokens

`signToken` and `verifyToken` are an HMAC-SHA256 JWT pair. `signToken` takes the
secret, your claims (a record or dict), and an expiry in seconds; `verifyToken`
decodes back into whatever type the call site annotates:

```elm
-- Auth.signToken   : String -> a -> Int -> Result Error String
-- Auth.verifyToken : String -> String -> Result Error a
```

## Two rules

- **Secrets are typed `String`, and never interpolated.** The signing key comes
  from an environment variable (`SKY_AUTH_TOKEN_SECRET`), never a literal in
  source, and never `fmt.Sprintf`-style stringified.
- **Gate at the view, not per route.** Let routing pick the page as usual, then in
  your view outer-`case` on `model.session`: signed-out always renders the sign-in
  surface, whatever page was requested. One `currentPath` function keeps the URL
  honest. The [Sky.Live guide](../skylive/overview.md) shows the pattern.

For OAuth (Google/GitHub) or an external provider (Auth0/Clerk), reach past
`Std.Auth` only when the product needs it — otherwise the built-in module is the
secure, reviewed default. Full surface: the [Std.Auth guide](../skyauth/overview.md).

**[Next → Deploying](17-deploying.md)**
