# Design: how a `Std.App` app configures itself — `withX` + `SKY_*` env

> **Status: DESIGN — decisions LOCKED (§7), not yet implemented.** Answers "what's
> the right config story for Sky apps going forward, now that everything unifies on
> `Std.App`?" (@anzel, 2026-08-25). The direction is agreed; implementation is
> additive and phased with `std-app-consolidation-roadmap.md`. Companion to
> `docs/design/unified-app-builder.md`.
>
> **The whole design in one screen (no mental overhead):**
> - **Value:** `App.withX v` in code; `SKY_X` env overrides it at deploy. Env wins.
> - **Your secret:** `Secret.fromEnv "VAR"` → a typed handle you use in code
>   (never a `String`, boot-validated, no unregistered-access API).
> - **Sky's built-in secret** (console/metrics/auth token, DSN): `App.withXFromEnv
>   "VAR"` — names the env var, never a literal.
> - **Precedence, always:** `SKY_* env  >  App.withX  >  built-in default`.
> - **`sky.toml`:** project metadata only (runtime sections retired into the above).

## 1. The problem — config comes from THREE systems

To configure a Sky app today you learn three overlapping mechanisms:

1. **`Sky.Config.withX` builders (code)** — `withLog`, `withDatabase`,
   `withSessions`, `withJobs`, `withCsrf`, `withTelemetry*`, `withLiveBroker`.
2. **`sky.toml` runtime sections** — `[live]` (port, static, store, storePath,
   ttl, maxBodyBytes, input), `[database]`, `[log]`, `[analytics]`, `[jobs]`,
   `[env]`, `[security]`.
3. **Env vars** — `ENV`, `DATABASE_URL`, `OTEL_*`, and ~40 `SKY_*`
   (`SKY_CONSOLE_*`, `SKY_ADMIN_TOKEN`, `SKY_DB_*`, `SKY_SESSION_*`,
   `SKY_LIVE_BROKER_URL`, …).

The same concern lives in more than one place — **sessions** are settable via
`sky.toml [live] store/ttl`, `Sky.Config.withSessions`, AND `SKY_SESSION_*`;
**database** via `[database]`, `withDatabase`, AND `DATABASE_URL`/`SKY_DB_*`. A
reader can't tell which wins, and an author can't tell where to set a thing. This
is exactly the fragmentation `Std.App` set out to remove at the *builder* layer —
it should be removed at the *config* layer too.

## 2. The model — `App.withX` declares, `SKY_*` overrides, `sky.toml` = metadata

One config surface, two layers:

- **`App.withX` (code) = the app's DECLARED config.** The `withX` pattern already
  exists (`Sky.Config`); surface it through the unified builder so it composes
  with the app: `App.app { … } |> App.withPort 8000 |> App.withSessions …`. Each
  builder is target-relevant (below) and, for the values a deploy varies, has a
  canonical `SKY_*` twin.
- **`SKY_*` env = the deploy-time OVERRIDE.** The same artifact ships to
  dev/staging/prod; env is what makes one binary behave three ways *without
  recompiling*. So **env wins over `withX`.**
- **`sky.toml` → project metadata only** (`name`, `version`, `entry`,
  `[source]`, `[dependencies]`, `[go.dependencies]`). Its runtime sections
  (`[live]`, `[database]`, …) are **deprecated** into `withX` + env (phased, per
  the consolidation roadmap). One fewer system to learn; no more "is it in the
  toml or the code?".

### Precedence (the crux) — `SKY_* env  >  App.withX  >  built-in default`

`App.withPort 3000` sets the app's default to 3000; `SKY_PORT=80` at deploy
overrides to 80; with neither, the built-in 8000 applies. This reconciles the two
framings that sound opposed:

- *"`withX` is primary"* — yes: in code you reach for `withX`, and if you never
  touch env it's fully authoritative.
- *"env is the fallback"* — also yes: env fills the gaps you didn't set in code
  (a deploy-only value). The SAME mechanism is a *fallback* for what you left
  unset and an *override* for what you set — because a hardcoded value you can't
  override at deploy is a footgun (you'd recompile to change a port). 12-factor
  wins here: **env is the escape hatch, and an escape hatch that can't override is
  not one.**

### Secrets — a typed `Secret` HANDLE, not a string lookup (DECIDED)

The question that shaped this: if a secret builder just *declares* "read env var
X" and you then read X directly in code, the builder buys nothing — why not read
env directly? And can "using an unregistered secret" be a compile error?

**A true compile error is impossible** — env names are runtime strings and Sky's
HM has no type-level string keys, so "you may only read a *registered* string key"
can't be enforced at compile time. But the *intent* is better served by a
**handle**, which sidesteps the string entirely:

```elm
-- Declare ONCE at top level. Returns a typed `Secret` handle AND registers the
-- var for boot validation (a kernel registry, like the anonRecords precedent).
stripeKey : Secret
stripeKey =
    Secret.fromEnv "MY_STRIPE_KEY"

-- Use the HANDLE where the secret is needed — never the string again.
-- Trusted APIs consume a `Secret` without exposing its String:
authedRequest =
    Http.withBearer stripeKey request
```

Why this beats a direct `System.getenvOr "MY_STRIPE_KEY"`, point by point:

1. **Typed `Secret`, not `String`.** A `Secret` has no `toString`/`++`/log path —
   it can only be *consumed* by a trusted API (`Http.withBearer`,
   `Auth.signToken`, …). A `getenvOr` returns a `String` that leaks the moment
   someone logs it. This is the "secrets are typed, never in code" rule made
   structural.
2. **Boot-time fail-fast.** Creating the handle registers the var; at startup the
   runtime checks every registered secret is present and **refuses to boot**
   naming a missing one — instead of an empty string silently reaching production
   and "authenticating" with `""` at first use. A `getenvOr "X" ""` defers the
   failure to the worst possible moment.
3. **No unregistered-access API exists.** You use the `stripeKey` *handle*, not a
   `Secret.get "MY_STRIPE_KEY"` string lookup — so there is simply no way to
   reference a secret you didn't declare. That is *stronger* than a compile error
   on a string: the unsafe shape isn't in the API at all.
4. **A manifest.** Because handles register, `sky doctor` / the deploy gate can
   enumerate exactly which secrets the app needs — impossible with scattered
   `getenvOr` calls.

So the rule for **user secrets** is: `Secret.fromEnv "VAR"` at top level → a typed
handle you thread where needed. No `App.withSecretFromEnv` builder is needed (the
handle carries everything); no secret string literal is ever accepted.

**Sky's own built-in secrets** — the console/metrics/auth tokens and the DB DSN,
which the *runtime itself* consumes (not user code) — are wired through the
builder, each defaulting to a canonical `SKY_*` var and accepting an override:
`App.withConsoleTokenFromEnv "SKY_CONSOLE_TOKEN"`,
`App.withMetricsTokenFromEnv "SKY_ADMIN_TOKEN"`,
`App.withAuthSecretFromEnv "SKY_AUTH_TOKEN_SECRET"`,
`App.withDatabaseFromEnv "DATABASE_URL"`. These take a **var name**, never a
literal, and feed the same typed-`Secret` path internally.

**Non-secret config still reads env freely** — a plain config string
(`System.getenvOr "SOME_FLAG" "default"`) is fine; the `Secret` machinery is only
for values that must never be logged or leaked. Values you configure through the
app use `withX` + `SKY_X` (env-wins); secrets use the handle. Two shapes, and
which one applies is never ambiguous.

### How you USE a `Secret` — trusted consumers + one loud `reveal` (DECIDED)

A `Secret` has to become bytes *somewhere* — the question is *where*, and by whom.
The answer: **inside a trusted native library, never in user code.**

- **Trusted Sky APIs take a `Secret` directly** and do the extraction at the FFI
  boundary — your code passes the *handle*, the library reads the bytes
  internally and never hands you a `String`. The common ones:
  - `Http.withBearer : Secret -> Request -> Request` — `Authorization: Bearer …`.
  - `Http.withApiKey : String -> Secret -> Request -> Request` — a very common
    need; the `String` is the *header name* (they vary: `X-API-Key`, `Api-Key`,
    `X-Api-Token`, …), the `Secret` is the key: `Http.withApiKey "X-API-Key"
    stripeKey`. **Header-only** — an API key never goes in a URL/query (URLs are
    logged; and the privacy rule forbids secrets in query strings).
  - `Auth.signToken : Secret -> String -> …` — HS256 signing.
  - `Db.connect` — the DSN/password.

  This covers the common cases with zero exposure.
- **Custom need → one deliberate escape:** `Secret.reveal : Secret -> String`,
  explicitly named so extracting the raw value is a *visible, intentional* act
  (greppable in review, flaggable by `sky doctor`), and — because it consumes a
  `Secret` — callable **only server-side** (the split keeps it off the client).
  There is exactly one way to turn a `Secret` into a `String`, and it announces
  itself.

So there is no silent path from `Secret` to `String`: either a trusted library
consumes it, or you write `Secret.reveal` and own that decision.

**`Secret`-only, not `Secret | String` (DECIDED).** The secret-implying slots take
a `Secret` and nothing else — `Http.withBearer : Secret -> …`, `Auth.signToken :
Secret -> …`. Accepting a `String` too would let a raw literal or `getenvOr` slip
past the type and defeat the whole point (and Sky has no untagged `Secret | String`
union anyway). The rare case of a genuinely *public* token in a header uses the
generic `Http.withHeader "Authorization" (…)` — a `String` API, clearly distinct —
so the safe path is the default for anything that reads as a secret, and the
`String` escape is a visibly different call.

**Go-side representation — a redacting struct, so the value is tainted (DECIDED).**
A `Secret` lowers to a Go **struct with an unexported field**, not a bare `string`
— so the value cannot leak by printing or logging, and only the runtime's own
trusted functions reach it:

```go
type Secret struct { v string }                              // unexported → outside rt cannot read it
func (Secret) String() string      { return "[REDACTED]" }   // fmt %v / %s
func (Secret) GoString() string    { return "Secret(REDACTED)" } // %#v
func (Secret) Format(f fmt.State, verb rune) { io.WriteString(f, "[REDACTED]") } // catch-all
func (Secret) MarshalJSON() ([]byte, error)  { return []byte(`"[REDACTED]"`), nil } // json/log encoders
// internal — the ONLY reader, used by rt.Http/rt.Auth and the Secret.reveal kernel:
func secretReveal(s Secret) string { return s.v }
```

So even a `log.Printf("%v", secret)` or a `json.Marshal` of a struct that embeds
one prints `[REDACTED]`; the real bytes are behind an unexported field only
`rt`-internal code (and the explicit `Secret.reveal`) can reach. (Zeroizing `v`
after use is a defensible add-on, not required.) This is what makes the type's
guarantee hold all the way down to the runtime, not just in Sky source.

### `Secret` is BACKEND-ONLY on split — the type guarantees it (DECIDED)

This is the property that makes the `Secret` type earn its keep in the unified
world: **a `Secret` can never reach the wasm client.** The Sky.Spa auto-split
already partitions an app into a client (wasm) frontend and a backend, excluding
*server-tainted* values (`File.`, `Db.`, `System.`) from the frontend. `Secret`
(and `Secret.fromEnv`, which reads the environment) is **server-tainted by
construction** — so:

- Any code path that touches a `Secret` is partitioned to the **backend**; its
  result reaches the client only through a typed RPC boundary (never the secret
  itself).
- Using a `Secret` in a **client-side** path — most importantly `view`, which
  runs in the browser — is a **split-time error**, exactly as a `Db.query` in
  `view` is today. You cannot compile a client that carries a secret.

So the same `Secret` type that stops a secret from being logged (§ above) *also*
stops it from being shipped to the browser — one type, both guarantees. A user
never has to reason about "will this leak into the client"; the split refuses to
build one that would.

### All env is read on the BACKEND; the client gets public config FROM it (DECIDED)

The Stripe publishable key makes the model click: a **publishable** key is *public
config*, not a `Secret` — it's *designed* to sit in the browser. So it is a plain
value, and it reaches the client the one consistent way anything reaches the
client — **from the backend**. The wasm frontend has no environment to read; every
env value is read on the backend, and the frontend receives the *public* subset it
needs via the rendered page / initial model, or via RPC.

```elm
-- BACKEND reads env (it's the only place with an environment):
stripePublishable : String                       -- PUBLIC → a plain String, not a Secret
stripePublishable =
    System.getenvOr "STRIPE_PUBLISHABLE_KEY" ""

stripeSecret : Secret                            -- SECRET → a handle, backend-only
stripeSecret =
    Secret.fromEnv "STRIPE_SECRET_KEY"

-- The PUBLIC key flows to the client in the model — the split ships this:
init _ =
    ( { stripeKey = stripePublishable, … }, Cmd.none )

-- view (client) uses model.stripeKey freely — it's public.
-- stripeSecret is used ONLY in a backend effect (Http.withBearer stripeSecret …);
-- putting it in the model or view is a split-time error.
```

So the classification the user makes is the honest one — *publishable = public,
secret = `Secret`* — and each type carries its own guarantee: the public value can
flow to the client (via the backend, explicitly, in the model), the `Secret`
cannot. Consistency: **there is exactly one place env is read (the backend), and
the client is handed only what the app decided to hand it.** A wasm client never
"reaches for an env var" — there is nothing to reach.

(If you *mistype* a secret as a plain `getenvOr` String and put it in the model,
you lose the protection — the type helps but can't force correct classification.
`sky doctor` can flag `getenvOr` reads of `*_SECRET*`/`*_KEY*`-shaped names as a
lint, guiding you to `Secret.fromEnv`; that's a nicety, not the guarantee.)

### Loading values — `.env` / `.env.<profile>` (DECIDED)

Declaring every value in code is the wrong tax for an app with a long list of
vars/secrets. Sky loads **dotenv** files at startup so the *values* live in a
file, and code only references the ones it uses:

- Load order (later wins, and a real process env var always wins over any file):
  `.env` → `.env.<ENV>` (e.g. `.env.production`, keyed off the `ENV` gate) →
  `.env.local` (git-ignored, personal overrides) → the actual process
  environment. This keeps the precedence rule intact: **real env > `.env*` files
  > `App.withX` > default** — the files are just a convenient *source* of env,
  not a new layer above it.
- `.env*` are **git-ignored by default** (a `sky init` `.gitignore` includes
  them) and never staged into a build embed (the embed already drops dot-files),
  so a secret in `.env` cannot be baked into a binary or committed.
- The files populate the environment that `Secret.fromEnv "VAR"` and
  `System.getenvOr "VAR"` read — so nothing about the two shapes changes; `.env`
  just means you didn't have to `export` thirty vars by hand.
- **Boot validation still applies**: a `Secret.fromEnv "VAR"` whose `VAR` is
  absent from *both* the `.env*` files and the process env fails fast at startup,
  naming it. So `.env` reduces typing without weakening the fail-fast guarantee.

DX balance: a long list of vars/secrets goes in `.env` (values), a `.env.example`
(committed, no values) documents the manifest, and code carries only the handles
for the secrets it actually consumes — no upfront wall of declarations.

## 3. Target-awareness — config is a capability, like the builders

The unified builder already treats `withRoutes`/`withWindow`/`withInput` as
target-relevant (ignored by targets that don't use them, `notFound` *mandatory*
for web via a phantom flag). Config extends the same idea:

- `App.withPort` / `withSessions` / `withBroker` → **web/Live** targets; a
  terminal or desktop-native build ignores them.
- `App.withWindow` → **desktop**.
- Mandatory-for-a-target config stays **compile-enforced** (the phantom pattern);
  optional config is simply ignored where it doesn't apply — never an error.

This keeps "one `App`, every target" true: you set what your targets need, and a
target that doesn't need a thing doesn't see it.

## 4. The surface, mapped (illustrative)

| Concern | `App.withX` (declared default) | `SKY_*` env (deploy override) | Notes |
|---|---|---|---|
| HTTP port | `App.withPort` | `SKY_PORT` (accept `PORT` too) | web only |
| Static dir | `App.withStatic` | `SKY_STATIC` | web only |
| Session store | `App.withSessions` (exists) | `SKY_SESSION_STORE` | web; shared store for multi-replica |
| Session TTL | `App.withSessionTtl` | `SKY_SESSION_TTL` | web |
| Database | `App.withDatabase` (exists) | `DATABASE_URL` / `<PREFIX>_DB_PATH` | DSN is deploy/secret → env-first |
| Jobs | `App.withJobs` (exists) | `SKY_JOBS_*` | |
| CSRF | `App.withCsrf` (exists) | `SKY_CSRF` | |
| Telemetry | `App.withTelemetry*` (exists) | `SKY_TELEMETRY_*` / `OTEL_*` | |
| Multi-replica broker | `App.withBroker` (`withLiveBroker` exists) | `SKY_LIVE_BROKER_URL` | |
| Console (dev tools) | `App.withConsole mode` | `SKY_CONSOLE_AUTH` + `SKY_CONSOLE_TOKEN` | token is a secret → env |
| Metrics endpoint | `App.withMetrics` | `SKY_ADMIN_TOKEN` | token is a secret → env |
| Log format + level | `App.withLog` (exists) | `SKY_LOG` | see §5 |
| Env gate (dev/prod) | — | `ENV` | deploy-only by design (an artifact is promoted dev→prod; a compile-time answer can't be right for all three — this one STAYS env-only) |

Most of the `withX` column already exists in `Sky.Config`; the work is (a)
surfacing it through `Std.App`, (b) giving each deploy-relevant one a documented
`SKY_*` twin with the env-wins rule, (c) folding the `sky.toml` sections into it.

## 5. Observability + the log-prefix question this started from

The `[sky.live]` / `Sky.Live listening` runtime lines are the **shared backend
runtime** — correct for a direct Sky.Live app, but a leak of an internal name for
a `Std.App` user. In this model:

- **Prefix → neutral.** `[sky.live]` / `[sky.console]` become `[sky]` (or the
  app's name), driven by `App.withLog` / `SKY_LOG`. The backend is an
  implementation detail; the log shouldn't name it.
- **Level + format** are `App.withLog` (exists) + `SKY_LOG` (e.g.
  `SKY_LOG=debug`, `SKY_LOG=json`).
- **`Sky.Live listening` is test-coupled** (`apps/fieldbook/verify.sh`,
  `TestNoAddedStartupLineLooksLikeAListeningLine`) — neutralising it means
  updating those in the same commit. Do it as part of this work, not ad hoc.

## 6. Migration + phasing (additive; tied to the consolidation roadmap)

1. **Surface `withX` on `Std.App`** (additive) — thread a `Sky.Config` through
   `App.app`, or add `App.withPort`/`withSessions`/… that populate it. No
   behaviour change; the existing `Sky.Config` builders keep working.
2. **Document the env twins + the env-wins rule** — one table, one precedence,
   in `docs/observability.md` + `docs/sky-toml.md`. Give every deploy-relevant
   `withX` a `SKY_*` and apply env-over-`withX` centrally in the runtime (not
   per-builder — one place decides precedence).
3. **Neutralise the runtime log prefix** (+ update the coupled test/verify.sh).
4. **Deprecate `sky.toml` runtime sections** — soft note → warning → remove,
   exactly the phased shape of the consolidation roadmap. `sky.toml` ends as
   project metadata. The existing `unknown_config_keys` machinery already warns
   on stray sections; this repurposes it to guide the migration.

## 7. Decisions (LOCKED 2026-08-25 with @anzel)

- **Precedence: `SKY_* env > App.withX > built-in default`.** Env always wins —
  the escape hatch that fills gaps *and* overrides for deploy. Resolved
  **centrally** in the runtime (one place applies the layering) so it can't drift
  per builder.
- **Secrets: a typed `Secret` handle** (§2). User secrets are `Secret.fromEnv
  "VAR"` → a handle (typed, boot-validated, no string-keyed accessor, so
  unregistered access is impossible by construction — stronger than a compile
  error, which HM can't give on a runtime string). Sky's built-in secrets use
  `App.withXFromEnv "VAR"` (names the var, never a literal). No secret literal is
  ever accepted anywhere.
- **Port: `SKY_PORT`** — a deliberate one-off canonical name (also accept bare
  `PORT` for Cloud Run, `SKY_PORT` canonical). Not every value gets a bespoke env;
  port earns one because it's the single most-overridden deploy value.
- **`DATABASE_URL` stays** the ecosystem-standard name (with `<PREFIX>_DB_PATH`
  for the embedded/sqlite tier); it is not renamed to `SKY_DB_URL`.
- **`ENV` stays env-only** — an artifact is promoted dev→staging→prod, so a
  compile-time default can't be right for all three. It is the one value that must
  NOT have a `withX`.
- **`sky.toml` runtime sections are retired** into `withX` + env; `sky.toml` ends
  as project metadata only (phased, per the consolidation roadmap).
- **`Secret` is consumed by trusted native APIs** (`Http.withBearer`,
  `Auth.signToken`, `Db.connect`) that extract the bytes at the FFI boundary; the
  ONLY way to a raw `String` is `Secret.reveal` — explicit, greppable,
  server-only. No silent `Secret → String`.
- **Secret slots are `Secret`-only, not `Secret | String`** — a `String` overload
  would let a raw literal past the type. A genuinely public token uses the generic
  `Http.withHeader` (`String`) instead.
- **Go representation is a redacting struct** (unexported field; `String`/
  `GoString`/`Format`/`MarshalJSON` all return `[REDACTED]`; a single `rt`-internal
  reader) — so the taint holds at runtime, not just in Sky source.
- **`Secret` is backend-only on split** — server-tainted by construction, so it
  is partitioned to the backend and a `Secret` in a client path (`view`) is a
  split-time error. One type stops logging, `++`, AND shipping-to-browser.
- **All env is read on the backend; the client gets only the PUBLIC subset the
  app hands it** (via the model/render or RPC). A publishable key is *public
  config* (a plain `String`), not a `Secret`; it flows to the client from the
  backend like anything else. The wasm client has no env to read.
- **`.env` / `.env.<profile>` are loaded at startup** (dotenv), git-ignored,
  never embedded. They are a *source* of env, not a new precedence layer: real
  env > `.env*` > `withX` > default. Boot validation still fires on a missing
  declared secret. A committed `.env.example` documents the manifest.
- **Log prefix goes neutral** (`[sky.live]`/`[sky.console]` → `[sky]`), driven by
  `App.withLog`/`SKY_LOG`; the test/`verify.sh` coupling to `Sky.Live listening`
  is updated in the same commit.

Still genuinely open (small): whether an embedded sub-app (console, a mounted
sub-app) inherits the parent's `withX` or configures independently — decide when
sub-app config is actually wired.

## 7b. `Secret` migration surface (from the stdlib sweep, 2026-08-26)

A read-only sweep of `sky-stdlib/` + `runtime-go/rt/` catalogued exactly what
migrates. Summary; the sequencing is what matters.

**Migrate (secret currently `String`/`any` → `Secret`)** — ~20 surfaces:
- **Auth** (`Std/Auth.sky`): `signToken`, `verifyToken`, `signSlidingToken` — the
  HMAC secret arg. *Widest blast radius + the flagged rule* (§ below).
- **Jwt** (`Sky/Core/Jwt.sky`): `hs256`, `rs256`, the `Algorithm` constructors.
- **Crypto** (`Sky/Core/Crypto.sky`): `hmacSha256/512` (key), `rsaSha256Sign`
  (private key), `aesGcm*`/`chacha20*` encrypt+decrypt (key), `*KeyFromPassword`
  (password in *and* derived key out → `Secret`).
- **Email** (`Std/Email.sky`): SES secret, SMTP pass, `Resend`/`SendGrid` API-key
  constructors.
- **Http** (`Sky/Http/Middleware.sky`): `withBasicAuth` *password* (username stays).
- **Db** (`Std/Db.sky`): `open`'s Postgres DSN — but see the judgment call below.

**The rt chokepoint:** `coerceAuthSecret` (`runtime-go/rt/db_auth.go:1848`) backs
all three Auth token kernels — migrate that one function (accept `rt.Secret`,
`secretReveal` → bytes, keep the ≥N-byte check post-reveal) and Auth is covered.

**New surfaces to ADD (additive, no breakage):**
- `Secret` type + `Secret.fromEnv` + `Secret.reveal` — a new
  `Sky/Core/Secret.sky` (nothing exists today).
- `Http.withBearer` / `Http.withApiKey` / `Http.withBasicAuth` (client) — **none
  exist today**; users hand-build `withHeader "Authorization" ("Bearer " ++ tok)`.
- `App.withConsoleTokenFromEnv` / `withMetricsTokenFromEnv` /
  `withAuthSecretFromEnv` / `withDatabaseFromEnv` — `Std.App` has **no** secret/
  config builders yet; the whole `withXFromEnv` family is new work.

**Boundary — STAYS `String` (deliberately):** public/verify keys (RS256 verify,
`rsaSha256Verify`), usernames, SES access-key *id*, the OTLP endpoint URL,
`Cache`, and `System.getenvOr` (the non-secret config reader). Three judgment
calls worth stating:
- **`hashPassword`/`verifyPassword` stay `String`.** They take *per-request form
  plaintext* at the edge, not a *configured* secret — `Secret` is for configured
  values (env/handles), and forcing every login field through the handle
  machinery is wrong.
- **`Db.open`'s DSN**: migrating it over-taints the SQLite *path* (public) it also
  accepts. Prefer sanctioning **`Db.connect ()`** (already env-driven, DSN-secret
  handled entirely in rt — *not source-breaking*) as the path, and leave `open`
  for advanced/SQLite use.
- **`Jwt.Algorithm`** holds one field used for both sign (private=secret) and
  verify (public). A clean migration splits it: `HS256 Secret`, and RS256 into a
  private-sign (`Secret`) vs public-verify (`String`) shape.
- **Literal-secret ban bites tests**: `Jwt.hs256 "topsecret"`,
  `Crypto.hmacSha256 "secret"` in `examples/00-standard-libs` become illegal. Need
  a clearly-named test-only `Secret.fromString`/`Secret.unsafeFromString` (loud,
  greppable) so tests can construct a `Secret` — the ban is on *accidental*
  literals in app code, not on a deliberate test constructor.

**Rule/doc changes (same-commit):** `AGENTS.md:492` "**Secrets are typed** —
`Auth.signToken`/`verifyToken` take `String`, never `any`" → **take `Secret`**;
the Auth pinned-default + production-gate `SKY_AUTH_TOKEN_SECRET` prose;
`templates/AGENTS.md` + `templates/CLAUDE.md` (template-sync rule);
`Std/Email.sky` docstrings go advisory → structural. `sky doc` regenerates;
`scripts/doc-examples.sh` examples must switch to `Secret.fromEnv`.

**Breakage (source-breaking user code):** `apps/relay` (Tokens/Config/Main),
`examples/13-skyshop` (Auth/OAuth), `examples/36-composite-server`,
`examples/00-standard-libs` (literal keys), `examples/17/18` (Db.open). **NOT
breaking:** `Db.connect ()` (the dominant DB entry — DSN read in rt), and every
new surface (additive).

**Sequencing:** (1) `Secret` type + `fromEnv`/`reveal` + the Go redacting struct
+ `coerceAuthSecret` accepting it; (2) `Http.withBearer`/`withApiKey` +
`App.withXFromEnv` — *additive, ship first, zero breakage*; (3) **Auth** (widest
blast radius, the flagged rule) with its examples/apps; (4) Crypto + Jwt + Email;
(5) `Db.open` as a design decision, not a mechanical rename. Each step ships with
its callers + the doc/rule sync.

*(Notable: `Std/Persist.sky` does not exist on this branch; `Std/Bundle.sky` has
no signing-key surface — iOS/Android signing isn't modelled in the stdlib.)*

## 7c. Runtime-derived secrets (fetched, not configured)

The env model (`Secret.fromEnv`) covers *configured* secrets. But a large class
of secrets are **fetched at runtime**: you POST to an OAuth token endpoint (or an
API login), the response body carries an `access_token`, and you put that token
in the `Authorization` header of every downstream call. That token arrives as a
`String` (JSON-decoded from the response) and must become a `Secret` to flow into
`Http.withBearer`. Three entry paths, in order of preference:

**(a) Decode straight into `Secret` — the clean path.**
`Secret.decoder : Decoder Secret` (+ a `Codec` twin). The `access_token` field
decodes *directly* into a `Secret`, so it is tainted + redacting from the moment
it leaves the wire — a bare `String` never exists:

```elm
type alias TokenResp = { accessToken : Secret, expiresIn : Int }

tokenDecoder : Decoder TokenResp
tokenDecoder =
    Decode.map2 TokenResp
        (Decode.field "access_token" Secret.decoder)   -- String on the wire → Secret in Sky
        (Decode.field "expires_in" Decode.int)

callApi : Secret -> Task Error Json
callApi token =
    Http.get "https://api.example.com/me"
        |> Http.withBearer token                       -- consumes the Secret, sets the header
        |> Http.expectJson
```

**(b) Promote an existing runtime `String` — `Secret.fromString : String -> Secret`.**
For when you already hold the string (built it, got it from a source the decoder
didn't cover). It is the loud, greppable *twin of `Secret.reveal`* — the two
named crossings of the taint boundary. A `sky check` **lint bans a string-literal
argument** (a purely syntactic check on the `StringLit` node): `Secret.fromString
oauthToken` is fine; `Secret.fromString "sk_live_…"` is a compile error naming
`Secret.fromEnv`. That is how "no secret literals" is enforced without blocking
runtime promotion. (Tests that genuinely need a literal use
`Secret.unsafeFromString`, lint-exempt — the name is the warning.)

**(c) The exchange call's OWN secret uses `reveal` when the sink isn't a typed
builder.** OAuth token exchange sends `client_secret` in a
`x-www-form-urlencoded` **body**, not a header — no typed consumer fits. That is
the legitimate use of the escape hatch: `Secret.reveal clientSecret` at the point
you build the form body, loud and server-only. `withBearer`/`withApiKey`/
`withBasicAuth` cover the header cases so `reveal` stays rare.

**What `Secret` does and does not manage.** It manages *leakage* — a fetched
token in your model, logs, or an accidental `MarshalJSON` is `[REDACTED]`; on a
split app it is backend-only, so a runtime-fetched token can no more reach the
client than an env one can. It does **not** manage *lifetime* — `expiresIn`,
refresh, and rotation are ordinary model state (store the `Secret` + an expiry
`Time`, refresh when stale). The redacting `MarshalJSON` is a bonus safety net:
you cannot accidentally echo a fetched token back in a JSON response.

**Storing a fetched token** (DB/cache): reveal at the write boundary, or better,
encrypt with `Crypto.aesGcmEncrypt` (which itself now takes a `Secret` key). The
DSN/at-rest story is out of scope here; the taint model just makes the write an
explicit, greppable act rather than an accidental `++`.

So the full constructor set is: `fromEnv` (configured), `decoder` (fetched via
wire), `fromString` (runtime promotion, literal-banned), `unsafeFromString`
(tests) — and the single exit `reveal`. Every crossing of the boundary is named.

## 7d. Migration diagnostics — turning the breakage into a guided fix

The breakage in §7b (existing `.sky` code passing a `String` where `Secret` is
now required) must not surface as a bare `type mismatch: String vs Secret`. Sky
already has the machinery to make it a guided fix, and it was built for exactly
this: `builder_cfg_migration_hint` (`crates/ty/src/check.rs:157`), wired into the
diagnostic via the `suggestion:` field (`check.rs:455`), turned the cryptic v0.19
`AppConfig _ _ vs record` error into an actionable `Try: …` note pointing at a
migration doc. We reuse that pattern. Four layers, cheapest first:

**(1) Targeted type-error hint at the exact call site.** A `secret_migration_hint`
twin: when a mismatch is exactly `String` (found) vs `Secret` (expected) **at an
argument of a known-migrated stdlib symbol**, attach a `Try:` note instead of the
bare message. Keyed on `(symbol, arg-index)` so it fires only at the migrated
sinks (`Auth.signToken`, `Crypto.hmacSha256`, …) — a user's own String-taking
function still gets the plain error. Two flavours:

```
error[E2001]: type mismatch — Auth.signToken expects a Secret, found String
   ┌─ src/Tokens.sky:62:24
   │
62 │     Auth.signToken secret claims 3600
   │                    ^^^^^^ this is a String
   │
   = Secret migration (v0.23): Auth.signToken now takes a typed Secret, not a
     String, so a secret can never be logged, ++'d, or serialised by accident.
   Try: replace the source of `secret` with `Secret.fromEnv "SKY_AUTH_TOKEN_SECRET"`
        (configured), `Secret.decoder` (fetched over the wire), or
        `Secret.fromString secret` (an existing runtime String).
   See docs/v0.23/migration-secret.md
```

When the offending argument is a **string literal**, the note is stronger and
different — this is the literal-ban from §7c, not a wrap suggestion:

```
   = Secrets may not be literals. Move it to an env var and read it with
     Secret.fromEnv "VAR" (a literal in source is committed, logged, and shared).
```

**(2) A migration registry, not scattered `if message.contains`.** A static table
`MIGRATED_SECRET_SINKS: &[(symbol, arg_index, since_version, env_hint)]` the hint
function consults. Keeps every note in one place, versioned, and lets a gate
(`crates/xtask/tests/…`) assert **every symbol migrated in §7b has a registry
row** — so a future migration can't ship a sink without its note.

**(3) `sky doctor` scans the whole project up front — no whack-a-mole.** A user
with 40 call sites shouldn't fix-one-recompile-repeat. A `sky doctor` pass (it
already exists, `[--fix]` and all) walks the project and lists **every** site at
once, grouped by file, each with the same `Try:` note. Run it right after
`sky upgrade` and you get the full worklist in one shot.

**(4) `--fix` for the mechanical cases.** The dominant pattern is
`System.getenvOr "X" ""` feeding a now-`Secret` sink → `sky doctor --fix` rewrites
it to `Secret.fromEnv "X"` (and adds the `import` if missing). Non-mechanical
cases (a runtime String, a fetched token) are left with the note, not auto-touched
— `--fix` never guesses at a `fromString` where a `decoder` was meant.

**(5) A one-time upgrade banner.** `sky upgrade` prints a single breaking-changes
line on the version that lands Secret, pointing at `docs/v0.23/migration-secret.md`
— the same doc the per-site notes link to. One destination, three ways to reach
it (compile error, `sky doctor`, upgrade banner).

Net effect: a user upgrades, recompiles, and instead of a wall of `String vs
Secret` they get — at the failing line — *which* env var to read and *which*
constructor to use, or one `sky doctor` report listing all of them, or a
`--fix` that does the mechanical 80%. The migration doc is written **with** the
change (same commit, per the template-sync rule), not after.

## 8. Recommendation

Adopt **`App.withX` (declared) + `SKY_*` (env override, env wins) + `sky.toml` as
metadata only**, target-aware, with a neutral log prefix — and phase it additively
alongside the consolidation roadmap. It removes the three-system fragmentation,
keeps 12-factor deploys, preserves the "secrets never in code" rule, and gives the
unified `Std.App` a single, discoverable, typed config surface with one clear
override mechanism. The `withX` half largely exists already; the value is making
it *the* surface, wiring the env twins with one precedence rule, and retiring the
`sky.toml` runtime sections.
