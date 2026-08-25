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
- **Log prefix goes neutral** (`[sky.live]`/`[sky.console]` → `[sky]`), driven by
  `App.withLog`/`SKY_LOG`; the test/`verify.sh` coupling to `Sky.Live listening`
  is updated in the same commit.

Still genuinely open (small): whether an embedded sub-app (console, a mounted
sub-app) inherits the parent's `withX` or configures independently — decide when
sub-app config is actually wired.

## 8. Recommendation

Adopt **`App.withX` (declared) + `SKY_*` (env override, env wins) + `sky.toml` as
metadata only**, target-aware, with a neutral log prefix — and phase it additively
alongside the consolidation roadmap. It removes the three-system fragmentation,
keeps 12-factor deploys, preserves the "secrets never in code" rule, and gives the
unified `Std.App` a single, discoverable, typed config surface with one clear
override mechanism. The `withX` half largely exists already; the value is making
it *the* surface, wiring the env twins with one precedence rule, and retiring the
`sky.toml` runtime sections.
