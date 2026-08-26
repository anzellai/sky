# Migrating to the `Secret` type

Secret-bearing arguments across the stdlib are now the opaque
`Sky.Core.Secret.Secret` type instead of `String`. This is a security change:
a `Secret` cannot be printed, logged, interpolated, or JSON-serialised — every
one of those paths redacts to `[REDACTED]` — so a signing key can no longer
leak into a log line or an HTTP response by accident. The raw bytes come back
only through the single, greppable `Secret.reveal`.

If you upgrade Sky and your existing project stops compiling with

```
-- TYPE ERROR --
[main] type mismatch: `Secret` vs `String`
```

on a call to `Auth.signToken`, `Auth.verifyToken`, `Auth.signSlidingToken`, or
`Jwt.hs256`, this page is the fix.

## What changed

| Function | Before | After |
|---|---|---|
| `Std.Auth.signToken` | `String -> a -> Int -> Result Error String` | `Secret -> a -> Int -> Result Error String` |
| `Std.Auth.verifyToken` | `String -> String -> Result Error a` | `Secret -> String -> Result Error a` |
| `Std.Auth.signSlidingToken` | `String -> a -> {…} -> …` | `Secret -> a -> {…} -> …` |
| `Sky.Core.Jwt.hs256` | `String -> Algorithm` | `Secret -> Algorithm` |
| `Sky.Core.Jwt.rs256` (RSA sign) | `String -> Algorithm` | `Secret -> Algorithm` |
| `Sky.Core.Jwt.rs256Verify` (RSA verify) | *(new)* | `String -> Algorithm` |
| `Crypto.aesGcmEncrypt`/`Decrypt`, `chacha20*` | `String -> String -> …` | `Secret -> String -> …` (key) |
| `Crypto.aesKeyFromPassword`/`chachaKeyFromPassword` | `String -> String -> String` | `Secret -> String -> Secret` |
| `Std.Cli.readPassword` | `() -> Task Error String` | `() -> Task Error Secret` |

**RSA (`RS256`) splits by direction.** Signing uses the PEM *private* key (a
secret) → `Jwt.rs256 : Secret -> Algorithm`, passed to `encode`. Verifying uses
the PEM *public* key (not secret) → `Jwt.rs256Verify : String -> Algorithm`,
passed to `decode`. Handing `encode` a public key, or `decode` a private key, is
a clear `Err` — the type no longer forces a public key to masquerade as a
`Secret`.

`Crypto.hmacSha256` is unchanged — it is a general HMAC primitive whose "key" is
not always a secret (domain-separation labels are a legitimate use), so the
`Secret` opacity lives at the semantic layer (`Auth`, `Jwt.Algorithm`), and
`Jwt.sign` reveals at the one crypto boundary where the bytes must enter the
HMAC. A DB **DSN** is likewise NOT a `Secret` (it is a compound value, mostly
env-sourced and rarely in your code); the runtime instead redacts a DSN
password wherever a connection error could echo it into a log.

## The fix

Add the import, then wrap the secret at the boundary where it enters your
program — never as a String literal in source:

```elm
import Sky.Core.Secret as Secret exposing (Secret)
```

**Read it from the environment** (the recommended path — the value never
appears in source or in the binary):

```elm
secret = Secret.fromEnv "SKY_AUTH_TOKEN_SECRET"
token  = Auth.signToken secret { uid = "u1" } 3600
```

**Promote a value you already hold at runtime** (e.g. a config field, or a
token you fetched from an external endpoint):

```elm
-- `getenvOr` returns a runtime String; fromString promotes it to a Secret.
secret = Secret.fromString (System.getenvOr "SKY_AUTH_TOKEN_SECRET" devFallback)
```

**A config record field** becomes `Secret`, so the secret is redacted even if
the whole config is logged:

```elm
type alias Config = { port : Int, jwtSecret : Secret }
```

## Secrets fetched from an external endpoint

A token you fetch at runtime and then send in an outgoing header is exactly the
case `Secret` protects — it keeps the fetched value out of your logs while it
lives in memory:

```elm
fetchAndCall : Task Error String
fetchAndCall =
    Http.get "https://issuer.example/token"
        |> Task.map (\resp -> Secret.fromString resp.body)
        |> Task.andThen
            (\apiKey ->
                Http.defaultRequest "https://api.example/thing"
                    |> Http.withBearer apiKey        -- takes a Secret; reveals internally
                    |> Http.request
            )
```

`Http.withBearer : Secret -> HttpRequest -> HttpRequest` (and
`Http.withApiKey headerName : Secret -> …` for a custom header) reveal the
secret INSIDE the stdlib, at the one boundary where the bytes must reach the
wire — so the token never appears as a `String` in your own code. Prefer them
over `withHeader "Authorization" ("Bearer " ++ Secret.reveal apiKey)`.

## Escaping opacity

When you genuinely need the raw bytes — writing an interop boundary a stdlib
helper does not cover — `Secret.reveal : Secret -> String` is the one way out.
It is deliberately greppable: a security review can find every place a secret
is un-wrapped by searching for `reveal`. Prefer a stdlib helper that takes a
`Secret` (so the reveal happens inside the audited runtime) over revealing in
your own code.

`Secret.unsafeFromString` exists for the rare case where a literal really is
the intended value (a fixed test fixture); the `unsafe` prefix marks it for
review. Never use it for a real secret — read that from the environment.
