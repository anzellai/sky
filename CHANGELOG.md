# Changelog

Notable user-visible changes. Keep this file additive — never rewrite history.

> **Release-notes source.** When a version is tagged, its section here is copied
> into the GitHub Release body, and `sky upgrade` prints those notes for every
> version between the user's current binary and the one they upgrade to
> (`sky upgrade --notes` previews without upgrading). To make a release surface a
> **⚠ breaking-change / migration banner** on upgrade, give the relevant
> subsection a heading containing the word **"Breaking"** or **"Migration"**
> (e.g. `### ⚠ Breaking changes`, `### Migration`). Keep migration steps concrete
> and copy-pasteable — this is the text a user sees the moment they upgrade.

## v0.19.11 — Sky.Live: bounded session-store memory (tiered cache) (2026-08-05)

### Fixed
- **Durable session stores now bound RAM to the *active* working set instead of
  every session held for the TTL.** Previously the sqlite / postgres / redis
  session stores kept the live `liveSession` pointer (~tens of KB each — Model +
  rendered tree + handlers) of EVERY session in an in-RAM `memCache` until its
  full TTL expired. Under sustained cookie-less traffic — crawlers and bots each
  minting a session — that accumulated `rate × TTL` sessions in RAM, enough to
  OOM a small VM over hours-to-days even with little *human* traffic (it wedged a
  1 GB instance on a 30-minute TTL, and was far worse on a 30-DAY TTL). The
  stores now **evict an idle session's live pointer from `memCache` after a short
  window** (default **5 min**) when it has no active SSE connection — persisting
  a fresh blob first, then tearing down its goroutines — and keep the blob on
  disk / in the external store until the full TTL, **resurrecting it from disk on
  the next request** (single-flight; the reconnecting SSE re-establishes its
  loops). RAM then tracks SSE-connected + recently-active sessions rather than
  everything-within-TTL. On-by-default; no app changes required — an abandoned
  tab / bot session evicts and a returning user resurrects transparently (one
  full re-render on wake). Sessions with a non-gob-encodable Model (the
  memCache-only fallback) are never evicted, so nothing is lost. The in-memory
  store is unaffected (no disk backing). Rebuild or `sky upgrade` to pick it up.

### Added
- **`SKY_LIVE_IDLE_EVICT`** (and `Std.Live.withIdleEvict`) — the idle-evict
  window for the tiered session cache above. Default `5m`; set `0` / `off` or a
  value `>= ttl` to disable (falls back to the previous all-within-TTL
  behaviour). Only the durable stores (sqlite / postgres / redis) honour it.

## v0.19.10 — Sky.Live: navigation scrolls the new page to the top (2026-08-04)

### Fixed
- **Sky.Live navigation now scrolls to the top of the new page**, like a normal
  browser navigation. Previously the runtime restored the pre-patch scroll
  position on every full-body/patch cycle — correct for in-place updates, but it
  meant navigating to a new page (a `sky-nav` link click, a programmatic
  `Navigate`, or any page change) landed you at the *old* page's scroll offset,
  so the new page often appeared anchored mid-page or at the bottom. The runtime
  now distinguishes a real page navigation (the URL pathname changed → scroll to
  top) from an in-place update (SSE tick, same-page event, a filter change that
  keeps the path → leave the scroll exactly where the user had it). Rebuild or
  `sky upgrade` to pick it up; no app changes required.

## v0.19.9 — Security: console API auth no longer trusts a loopback IP (2026-08-04)

### Security
- **The `/_sky/console` read APIs are no longer reachable unauthenticated behind
  a reverse proxy.** The console auth gate (`consoleAccessAllowed`) previously
  treated any request from a loopback `RemoteAddr` as trusted. Behind a reverse
  proxy — the app on `127.0.0.1`, the proxy terminating TLS — **every** request's
  `RemoteAddr` is loopback, so the console `overview` / `logs` / `traces` /
  `metrics-summary` / `errors` / `analytics` endpoints (telemetry that can carry
  PII and secrets) were served **without authentication** in production, and an
  app-side SSRF could reach them too. The gate now authenticates the in-process
  console sub-app by a per-boot internal token (and still accepts the operator's
  `SKY_ADMIN_TOKEN`), falling through to the existing cookie / `SKY_CONSOLE_AUTH`
  gate otherwise, and **never trusts a source IP**. No configuration change is
  required — rebuild or `sky upgrade` to pick up the fix. Anyone running a
  Sky.Live or Sky.Http.Server app behind a proxy with `SKY_CONSOLE_AUTH` set
  should upgrade.

## v0.19.8 — Reliability-hardening pass: 11 fixes + comprehensive e2e coverage (2026-08-02)

A ground-up test-coverage pass across the compiler, standard library, Sky.Live,
LSP, and tooling — driven by the observation that the existing gates prove
"compiles + matches the oracle", not "behaves correctly at runtime". Adversarial
behavioral tests (driving the real Sky-source API through the compiled binary,
with boundary / malformed / platform-dependent inputs) surfaced **11 real
"compiles-clean, behaves-wrong" bugs**, all fixed at root cause and each now
guarded by a permanent regression test. Rebuild with this version to pick up the
runtime fixes; no code changes required.

### Fixed — stdlib correctness (adversarial conformance suites)

- **`Json.Decode.int` / `Codec.int` corrupted large integers and was
  platform-dependent.** JSON numbers were parsed via `float64`, so any integer
  beyond 2^53 (a Snowflake ID, a nanosecond timestamp, a large counter) lost
  precision on round-trip — and `max int64` decoded correctly on macOS but errored
  on Linux (an out-of-range `float64`→`int64` conversion is implementation-defined
  in Go). Now parsed via `json.Number`: the full int64 range round-trips
  losslessly on every platform.
- **`Money.allocate` dropped a cent when splitting a NEGATIVE total.** `allocate 3`
  of `-$100.00` returned `[-33.33, -33.33, -33.33]` (sums to -99.99), violating the
  documented "parts sum to the input exactly" contract — a real defect for refunds
  / chargebacks. The residue is now distributed by sign + magnitude.
- **`Std.Time.addMonths` dropped the year when going BACKWARD across a year
  boundary.** `addMonths -1` of Jan 2023 gave Dec **2023** (should be Dec 2022) —
  the month floored correctly but the year used truncating integer division. Fixed
  via a single floored total-month index.
- **`Sky.Core.Time.timeString` was host-timezone-dependent.** A function documented
  as "pure formatting" returned different output on different machines; now pinned
  to UTC like every other formatter.
- **`Sky.Core.Bytes.length` / `slice` were rune-based on a byte buffer.**
  `Bytes.length "世界"` returned 2 (runes) instead of 6 (bytes), and `slice` used
  rune indices — silently corrupting binary payloads. Now byte-accurate.
- **`Std.Auth.passwordStrength` panicked on a valid password.** The kernel returned
  `Ok ()` where the type promised `Result Error String`, crashing on the success
  path. Now returns the documented `"weak"` / `"fair"` / `"strong"` category. (The
  auth-BYPASS surface — tampered-signature / `alg:none` / expired / wrong-secret —
  was already correctly rejected; that's now covered by an adversarial suite too.)
- **`Sky.Core.Uuid.parse` never returned `Just`.** It returned a `Result` where the
  type is `Maybe String`, so every outcome read as `Nothing` (a valid UUID couldn't
  be parsed). Fixed to return `Just` / `Nothing`.

### Fixed — Sky.Live production incidents

- **Idle sessions disconnected after ~20-30 minutes ("reconnecting… refresh fixes
  it").** The CSRF cookie's lifetime was keyed to the session TTL, so with
  `SKY_LIVE_TTL` set (the documented production pattern) it expired during idle
  while the server session kept sliding on the SSE heartbeat — the next event POST
  then 403'd and the tab stranded until a manual refresh. The CSRF cookie now
  outlives an idle-sliding session (30-day floor). This is the root cause behind
  the resilience work in v0.19.4-7; the earlier passes fixed adjacent modes but
  missed this one. Now covered by a browser end-to-end test.
- **A misconfigured session store silently became in-memory.** An unrecognised
  `store` value (a typo, or a documented-but-unimplemented backend) fell through to
  the memory store — losing every session on restart, never shared across replicas.
  An explicitly-configured unknown store now fails loud in production (warns +
  memory in dev), matching the v0.19.4 fail-loud policy for known stores.

### Fixed — tooling + compiler

- **`sky db migrate` silently dropped `UNIQUE`, serial `AUTOINCREMENT`/`BIGSERIAL`,
  and `DEFAULT` constraints** that `sky db push` preserved. The committed-migration
  path (the recommended production flow) rendered weaker DDL than the direct-create
  path — so a `UNIQUE` column accepted duplicates on SQLite, and on Postgres a
  serial primary key rendered as a plain `BIGINT` with no sequence, breaking every
  insert. Both paths now render through one shared renderer, so they cannot diverge.
- **Bare `Math` constants (`Math.pi`, `Math.e`, `Math.inf`, `Math.nan`, …) failed
  `go build` when used directly.** Passing one straight to a `Float -> Float`
  function, or using it in a comparison, type-checked but didn't compile (the
  constant lowered to `any`). Now lowered to its typed value.

### Changed

- **Firestore removed from the documented Sky.Live session-store options.** It was
  listed (`CLAUDE.md`, `sky.toml`, docs) but never implemented, and is a poor fit
  for a session store (per-request latency + cost, and it doesn't provide the
  cross-instance broadcast broker Redis does). Use `memory` / `sqlite` / `postgres`
  / `redis`. (Firestore as an application database via the Go SDK / FFI is
  unaffected — that's a separate, working capability.) An explicit
  `store = "firestore"` now fails loud rather than silently degrading.
- `Time.timeString` and `Bytes.length`/`slice` change output for non-ASCII / large
  inputs (see Fixed above) — these correct clearly-documented behavior; code that
  depended on the buggy output should adopt the corrected semantics.

### Added — test coverage (no user-facing API change)

Behavioral hardening so the above class of bug is caught going forward: 12 new
adversarial stdlib conformance suites (Decimal, Money, Jwt/Auth, Encoding, Csv,
Compression, Time, Random, Math, Dict/Set, Regex, Uuid) driving the real API
through the compiled binary; CORS/BasicAuth + real-Postgres + cross-process-gob
integration tests; browser end-to-end tests for the Sky.Live idle-survival and
handler-desync-recovery paths; LSP diagnostics + `sky` verb (watch/doctor/doc/
profile/add) coverage; compiler codegen/lower/inference snapshot tests; a nightly
full example sweep; and `xtask welltyped`, a type-directed differential fuzzer that
diffs the compiler against the reference implementation on generated well-typed
programs. macOS CI now runs the behavioral conformance + golden gates too, so a
platform-dependent regression (like the int64 one) is caught on both platforms.

## v0.19.7 — Sky.Live: SSE drop-resync + cross-restart session persistence (2026-08-01)

The final two fixes in the Sky.Live resilience pass — the two hardest silent-
failure modes, each designed test-first and adversarially grilled before landing.
Pure-runtime + codegen; rebuild with this version to pick them up.

### Fixed — a dropped SSE frame no longer silently diverges the page

Under backpressure (a slow tab, or a burst of ticker/pub-sub frames), the server
could drop a frame from an over-full per-connection buffer. If the next patch's
targets happened to still exist in the now-stale DOM, it applied over the wrong
base with nothing detecting it — the page silently diverged from the server until
the next detectable miss. The server already knows about every such drop, so it
now flags the affected connection and ships an inline full-body resync on the
same stream (fresh sequence number, so it supersedes any stale buffered frame).
A healthy connection never triggers one; verified race-free.

### Fixed — a session with an `any`-typed field could vanish on restart

Sky.Live persists a session's Model with `gob`. A concrete type that only ever
lived in an `any`-typed Model field (nil at init, set later by a Msg) was
invisible to the boot-time registration, and gob's type registry is per-process —
so after a restart the new process couldn't decode it, and the session was
silently dropped and lost. The compiler now emits a whole-binary registration of
every record and ADT type at boot, so every process (including the one that
restarts and decodes) can round-trip any `any`-field value. Deterministic
(byte-stable) output — no effect on any program that wasn't hitting this.

## v0.19.6 — Sky.Live session-persistence fidelity + view-determinism check (2026-08-01)

Two more runtime fixes from the resilience pass (the parts of the deep follow-up
that were sound to ship; two larger items — an SSE drop-resync and compiler-
emitted gob registration — remain designed for a dedicated change). Pure-runtime.

### Fixed — a session could look persisted but silently wasn't (gob registration)

The type-registration used to persist a session's Model marked a type as
"registered" *before* the registration call that can fail (recovering the
panic). A failed registration was then remembered as done, so it was never
retried, every later save of that session failed, and the session silently
dropped to an in-memory-only fallback — lost on the next restart. Registration
now caches the success flag only when it actually succeeds. The per-session
"couldn't persist, using memory" fallback also increments a
`sky_live_session_encode_fail_total{store}` metric, so a "looks-persisted-but-
isn't" session in a durable deploy is now visible instead of a buried log line.

### Added — opt-in `view()` determinism check

`view(model)` must be a pure function of the model — handler IDs and the SSE diff
both depend on the same model rendering to the same tree. A `view` that reads
`Time.now` / `Random` / `Uuid.v4`, or iterates a raw Go map instead of
`Dict.toList`, drifts the tree between renders (stale clicks, dropped patches).
Set `SKY_LIVE_VIEW_DETERMINISM_CHECK=1` in dev to have the runtime render `view`
a second time and warn if the tree shape differs. Opt-in (never on in production,
off by default) because the second render doubles the side effects of an impure
view — move nondeterminism into `update`/`Cmd` and keep `view` pure.

## v0.19.5 — Sky.Live resilience, part 2: CSRF/route/sub-app/panic hardening (2026-08-01)

Five more runtime fixes for hidden Sky.Live production-failure modes (the second
batch after v0.19.4). All pure-runtime — rebuild with this version to pick them up.

### Fixed — long-lived tabs no longer start 403-ing every click (CSRF cookie)

The CSRF cookie was a session cookie (no expiry). Browsers that clear session
cookies on tab-discard / sleep-wake (Safari/ITP, Chrome tab discard) dropped it
while a Sky.Live SPA stayed open; the next POST regenerated a new cookie but the
page still sent the old token → **403 on every click until a manual reload**. The
cookie is now persistent and re-issued on each request (sliding), keyed to the
session TTL, so it survives those evictions.

### Fixed — a typed route parameter no longer panics the page

A route whose `Page` constructor takes a non-`String` parameter (e.g.
`AppDetailPage Int`) panicked at request time (`reflect: Call using string as
type int`) — compile-clean, then aborting every visit to that URL. Route
parameters are now coerced to the constructor's parameter type (int/float/bool),
and an unconvertible value (`/product/abc` for `ProductPage Int`) degrades to a
warning instead of crashing the request.

### Fixed — a crashing handler is now observable instead of a silent dead button

A panic in a session's `update`/`view` was recovered but logged only to stderr —
so a deterministic panic for a given `Msg` turned that control into a permanent
silent no-op with nothing to grep. It now emits a structured Error log (visible
in `Std.Log` + the console + metrics) with a correlation id and sets a
user-visible notification.

### Fixed — the dev console no longer opens a second connection to your database

The inline `/_sky/console` mounts as a sub-app that ran its own store selection
and inherited the host's `SKY_LIVE_STORE`, opening a **second** pool against your
DB (SQLite writer contention; a redundant Postgres pool) — and, with v0.19.4's
fail-loud store policy, a console store-connect failure could take down the host.
Sub-app sessions are ephemeral, so a sub-app with no explicit store now uses an
in-process memory store.

### Added — multi-replica pub/sub heads-up

In production, if the pub/sub broker is in-process (any non-Redis store with no
`SKY_LIVE_BROKER_URL`), the runtime now logs a one-time note that cross-replica
broadcasts (`Cmd.publish`, cross-instance multi-tab fan-out) won't reach other
replicas — so a multi-replica deploy doesn't silently drop them. Single-instance
deploys can ignore it.

## v0.19.4 — Sky.Live production resilience: self-healing desync + fail-loud stores (2026-08-01)

A set of runtime fixes for a class of Sky.Live bugs that passed `sky check` +
`go build` + tests, looked healthy in production, then stranded or silently
degraded real users in ways that were very hard to debug. All pure-runtime — your
app picks them up by rebuilding with this version. No app-code or schema change.

### Fixed — a live client now always recovers from a view desync (no more "refresh to fix it")

After a deploy changed your `view` (or an SSE connection dropped and the DOM went
stale), a click could hit a handler ID the server's current render no longer had.
The server returned a bare `404 "handler not found"` the client couldn't recover
from — it showed a "reconnecting/disconnected" banner and only a **manual page
refresh** brought it back. This was the most common cause of the "idle for a while,
then it's disconnected, refresh fixes it" reports.

Now the server re-renders the current view and returns it with a typed
`X-Sky-Status: desync` signal; the client applies it, refreshing the DOM and its
handler IDs so the next click works — **self-heals in one round-trip, no manual
refresh**. Session-loss is a separate `X-Sky-Status: session-lost` signal that
reloads deterministically (no more sniffing the response body).

### Fixed — an explicitly-configured session store now fails loud instead of silently becoming in-memory

`[live] store = "postgres"` (or `sqlite`/`redis`) that couldn't connect at boot
used to silently fall back to an **in-memory** store — sessions then vanished on
every restart ("sessions randomly die"), while every health signal stayed green.
Now the runtime retries with backoff (to ride out the database-not-ready boot
race), then, if still unreachable, **fails loud in production** (refuses to start
so your orchestrator restarts it and you see the cause). Dev keeps a loud-warning
memory fallback so a DB-less `sky run` still works. Set `SKY_LIVE_STORE=memory`
to opt in to in-memory sessions deliberately.

### Fixed — `/_sky/readyz` no longer lies

The readiness endpoint returned `200` even when the session store / DB was
unreachable, so orchestrators kept routing to a broken replica. It now pings the
session store and the app DB, returning `503` when either is down.

### Fixed — a transient database blip at boot no longer bricks the app until a restart

`db = Task.run (Db.connect ())` is evaluated once and cached. A first-connect
failure (a boot race, a momentary blip) used to freeze that handle to an error
for the whole process lifetime — every query failed until a manual restart. The
connection is now a self-healing pool: a boot-time failure logs a warning and the
next query reconnects transparently once the database is available.

### Fixed — session-lifecycle edges (idle logouts / stale-connection 404s)

- The `sky_sid` cookie now **slides**: it's re-issued with a fresh lifetime on
  each page load, tracking the server-side TTL, so an actively-used session past
  the original cookie window is no longer silently logged out mid-use.
- An open SSE connection now **keeps its session alive** (and tears the
  connection down when the session is evicted) — a connected-but-idle tab is no
  longer evicted under a live connection.
- Reads slide the TTL on Postgres/SQLite/Redis stores too (previously only
  writes did), matching the in-memory store.

## v0.19.3 — onNavigate crash fix, cross-module `case` fix, conformance suite, `sky db reset`/`drop` (2026-08-01)

### Added — `sky db reset` + `sky db drop`

Two destructive dev-DB verbs, both accepting an optional `[table]` (default: all of
the project's declared `db : Store.Project` tables) and a `--yes`/`-y` to skip the
confirmation prompt:

- **`sky db reset [table]`** — empties data (keeps the schema + `_sky_migrations`
  ledger, resets autoincrement). Postgres `TRUNCATE … RESTART IDENTITY CASCADE`;
  SQLite `DELETE` + `sqlite_sequence` reset (FK-safe).
- **`sky db drop [table]`** — drops the declared tables **plus `_sky_migrations`**
  (a fresh "never ran migrate/push" state); a single `drop <table>` leaves the
  ledger. Postgres `DROP … CASCADE`; SQLite `DROP` (FK-safe).

Both prompt for confirmation on a TTY, refuse on a non-TTY (and in production)
without `--yes`, and scope to the app's **declared** tables — for a total wipe
(sessions/analytics/unrelated tables) use the database's own `DROP SCHEMA`. New
stdlib surface: `Store.resetProject`/`dropProject`/`resetTable`/`dropTable`.

### Fixed — `sky.toml` `[live]`/`[auth]` values with inline comments

A `[live]`/`[auth]`/`[log]`/`[database]` value carrying a trailing `# comment`
(e.g. `store = "postgres"   # sessions in the shared Postgres`) was baked into the
emitted `init()` as `SetSkyDefault("LIVE_STORE", "postgres\"   # …")` — the value
kept the closing quote **and** the comment, so it never matched `case "postgres"`
in the runtime and the setting silently fell back (e.g. sessions to the in-memory
store) on a **raw-binary deploy** (running the compiled `sky-out/app` directly,
not via `sky run`). The scalar parser now drops the inline comment and strips the
surrounding quotes (a `#` *inside* a quoted value is preserved), and section
headers tolerate a trailing comment. (`read_sky_toml_config` /
`parse_toml_scalar` in `crates/project/src/build.rs`; regression
`scalar_values_strip_inline_comments_and_quotes`.)

### Fixed — `Std.Live.withOnNavigate` crashed every page at runtime

`withOnNavigate` was typed `(String -> msg)`, but the runtime hands the callback
the **`Page` value** (it dispatches `model.Page` after each route change). A user
callback therefore lowered to `func(string)`, and the runtime's `reflect.Call`
passed it a `Page` — panicking with `reflect: Call using <Page>_V as type
string` on **every** page load (`onNavigate` fires on the initial mount too, so
every GET 500'd). `sky check` and `go build` both passed; the failure was
reflect-dynamic and only surfaced at runtime. The signature is now
`(page -> msg)` — a `\_ -> Msg` callback stays fully polymorphic, and
`\page -> case page of …` pins to your `Page` union. Regression:
`ty` test `withonnavigate_page_callback`.

If you wrote `withOnNavigate (\path -> …)` expecting a URL string, the value is
the destination `Page`, not the path — read the URL from the model instead.

### Added — console "Sign out" (embedded mode only)

The embedded Sky Console (mounted inside a user app) now shows a **Sign out**
link that clears the `__Host-sky_console` login cookie via a new
`/_sky/console/_logout` route. It appears **only** in embedded mode — a
standalone `sky console-serve` hub / aggregator has no login cookie, so it
renders no sign-out (driven by `SKY_CONSOLE_LOGOUT_URL`, which the embedded
mount injects and the hub does not).

### Fixed — `Std.Db.Store` multi-column `ORDER BY` was reversed

`orderAsc`/`orderDesc` prepend each term, so a chained
`q |> orderAsc "sort_pos" |> orderAsc "id"` rendered `ORDER BY id, sort_pos` —
the **last** call became the **primary** sort key, silently corrupting any
multi-column sort. It surfaced as a broken image-reorder gallery: the rows
displayed in `id` order (random UUIDs) instead of `sort_pos` order, so the
up/down arrows became no-ops. `orderTail` now reverses the accumulated terms so
the **first** call is the primary key. Single-column ordering is unchanged.

### Fixed — `Std.Ui.button` implicitly submitted enclosing forms

A `<button>` with no `type` defaults to `type="submit"`, so a `Ui.button` inside
a `Ui.form` submitted the form on click (firing the form's `onSubmit`, or
double-firing alongside the button's own `onPress`) — e.g. a "Cancel" action
button would SAVE the form. `Ui.button` now defaults to `type="button"`; a real
submit control overrides with `Ui.htmlAttribute "type" "submit"`.

### Fixed — `Std.Log.*With` dropped all structured fields

`Log.warnWith`/`infoWith`/`errorWith`/`with`/`debugWith` take a k-v list, but the
runtime only handled `[]any`. A homogeneous Sky list (all strings — the common
case) lowers to Go `[]string`, so every structured field was silently dropped
(logs showed just the message, no fields — in both plain and JSON output). The
attrs are now reflected into the field bag regardless of slice type.

### Added — compiler warning for memoised effect-reads (stale-CAF footgun)

A zero-arg top-level binding is a memoised CAF (evaluated once, cached for the
process). If it forces a **fresh-value** effect (`Time.now`/`Uuid.v4`/`Random.*`)
or a **mutable-store read** — even laundered through a helper (`listActive =
withConnList (\c -> Store.query …)`) — the value freezes and never reflects later
writes/clock ticks. The compiler now warns and suggests the `name () = …`
function form, while suppressing the blessed memoised-handle contract
(`db = Task.run (Db.connect())`).

### Fixed — `Codec.fromJson` on an ADT panicked; `Codec.auto` decoder was lenient

Two reflective-codec bugs the new conformance suite surfaced:
- **`Codec.fromJson` on a `Codec.enum`/`taggedUnion` PANICKED** (process abort,
  `CoerceFailure`) on a decode failure instead of returning `Err` — a runtime
  crash from well-typed code. `JsonDec_fail` returned a bare string rather than a
  proper `Error` ADT, which the `ResultCoerce[Error, a]` wrap on `fromJson`'s
  result couldn't narrow; now returns `ErrDecode`, plus a defensive guard in
  `coerceInner` so no decode path can crash the runtime.
- **`Codec.auto`/`autoCamel`/`autoWith` decoded permissively** — a missing
  required field or a wrong-typed field silently became the zero-value default
  (`Ok`, silent data corruption). Now STRICT: errors on an absent required
  (non-`Maybe`) field, a type mismatch, a fractional number where an `Int` is
  expected, and an unknown enum value — matching the explicit
  `object`/`field`/`buildObject` decoder. A `Maybe` field absent/null still
  decodes to `Nothing`.

### Fixed — `Sky.Core.List` ops were O(n²) in time (now O(n))

Every list-BUILDING op (`map`/`filter`/`reverse`/`append`/`concat`/`range`/`zip`/
`indexedMap`/`take`/`drop`) was a pure-Sky CPS loop that cons'd per element, and
immutable prepend on the `[]any` list is O(n) — so each op was **O(n²) in time**
(200k elements ≈ 7.5 min). The v0.17 CPS rewrites fixed constant *stack* but not
*time*. These ops are now O(n) runtime kernels (Go `append`-loops, same `[]any`
representation, constant stack) — a 1,000,000-element fold now runs in a fraction
of a second. `isEmpty`/`length` are kernel aliases too.

### Fixed — `String.toInt` now trims surrounding whitespace

`String.toInt "  42  "` returned `Nothing` while `String.toFloat` and the typed
`String_toIntT` both trim — trimming silently depended on the codegen path.
`String.toInt` now trims, consistently.

### Fixed — cross-module same-named ADT crashed `case` at runtime

Two modules that each declared a same-named ADT with the same variant names
(`type Prim = Leaf String | Node Int` in both) miscompiled every `case` on one
module's value: the pattern lowerer resolved the bare constructor name through a
last-writer-wins map, so `case alphaValue of Leaf s -> …` emitted its variant
type-assertions against the *other* module's variant struct. The value never
matched, so the exhaustiveness-checked `case` fell through to a runtime
`panic` (and, through the reflective `Codec.taggedUnion` decode path, an
`interface conversion` panic). Constructor *construction* already resolved the
correct module; only the pattern side didn't. Now every `case` arm asserts
against its own module's variant struct. Byte-identical output for any program
that wasn't hitting the collision.

### Added — stdlib behavioral conformance suite (`tests/conformance/`)

A `sky test` suite layer (`scripts/conformance.sh`, wired into CI + the release
gate) that asserts documented stdlib semantics with ADVERSARIAL inputs — the
behavioral layer the corpus gates + differential oracle don't cover (they prove
"builds" + "matches oracle", not "behaves correctly at runtime"). It already
caught several compiles-clean-behaves-wrong bugs (Store multi-column order,
memoised-CAF stale reads, `Log` dropped attrs, `fromJson`-ADT panic, list
stack-safety, cross-module same-named ADT `case`) — each is now a permanent
assertion.

## v0.19.2 — Analytics on Store, Store partial-column update, sign-out un-attribution (2026-07-31)

### ⚠ Breaking

- **`Analytics.recentEvents` now returns `List AnalyticsEvent`, not `List String`.**
  The built-in analytics aggregates moved onto `Std.Db.Store` (see below), so
  `recentEvents` hands back typed rows instead of JSON-object strings. If you
  render it, read fields off the record instead of treating each item as a string:

  ```elm
  -- before (each item was a JSON string):
  List.map (\s -> Ui.text s) (Analytics.recentEvents 20)

  -- after (typed AnalyticsEvent rows — read .event / .ts / .userId):
  List.map (\e -> Ui.text (e.event ++ " · " ++ String.fromInt e.ts)) rows
  ```

  `.props` / `.context` remain JSON text on the record. No other analytics API
  changed shape.

### Added — query analytics on `Std.Db.Store`

`Std.Analytics` events are now queryable, aggregatable and patchable with the
same typed `Std.Db.Store` API as any other table — so building custom insights
(and our own internal tooling) gets the "if it compiles it works" guarantee
instead of bespoke query kernels.

- **`Analytics.eventsStore : Store AnalyticsEvent`** — a Store over the
  `analytics_events` table. `AnalyticsEvent` is the stdlib envelope (typed
  columns: `id` / `ts` / `event` / `userId` / `anonymousId`) plus the open
  metadata bag (`props` JSON — any keys your `event`/`trackEvent` emit — and
  device `context` JSON).
- **`Analytics.openStore : () -> Task Error Db`** — a connection to the analytics
  store (the console DB or the `[analytics] dbPath` override), for use with
  `eventsStore`. Query the envelope columns directly; reach for
  `Store.selectRaw` + `json_extract`/`->>` for the JSON props — same on SQLite and
  Postgres. The consent-gated WRITE (`track`) stays in the runtime; this is the
  read / query / update / aggregate side.
- The built-in aggregates **`totalEvents` / `uniqueUsers` / `eventCounts` /
  `recentEvents` are now plain `Std.Db.Store` queries over `eventsStore`** (Sky,
  not Go kernels) — so a schema change is caught at compile time and they're a
  worked example of the Store API. **Breaking:** `recentEvents` now returns
  **`List AnalyticsEvent`** (typed rows — read `.event`/`.ts`/`.userId`) instead
  of `List String` (JSON-object strings); `.props`/`.context` stay JSON text.
- **Console recent-event stream shows the page path.** A `page_view` row in the
  Sky Console's Analytics tab now renders the `props.path` next to the event name
  (`page_view  /shop/necklaces`) instead of a bare, un-actionable `page_view`.
  The path is lifted from the props JSON in Go (dialect-agnostic — same on SQLite
  and Postgres); any event tagged with a `path` prop shows it. (`analyticsRecentEvents`
  in `runtime-go/rt/console_analytics.go`; regression `TestAnalyticsRecentEventsPath`.)

### Added — `Std.Db.Store` partial-column update

Closes the one basic single-table op Store couldn't do: a **partial-column
`UPDATE`** (`SET` a subset of columns and leave the rest untouched). `update` /
`updateWhere` are codec-driven and rewrite the WHOLE record — so patching one
column meant either dropping to raw SQL or a racy read-modify-write, and you
couldn't even name a column absent from the codec's read shape.

- **`Store.setFields conn store pkValue [ ( "col", SqlValue ) … ]`** — PATCH by
  primary key. `Store.updateFields conn store cond [ … ]` — PATCH by `Cond`.
  Only the named columns are written; column names accept the record field or
  the snake column; values bind as `SqlValue` params (injection-safe).
- **`Store.adjust conn store cond [ ( "col", delta ) … ]`** — atomic relative
  change (`SET col = col + delta`), the one write whose value depends on the
  column's CURRENT value (counters / stock / balances) without reading first.
- See `examples/55-store-partial-update`.

### Fixed

- **`Std.Analytics` — signing out now un-attributes the session.** With
  `Live.withAnalyticsIdentify`, the identify resolver is the session's identity
  authority: `Just id` identifies, but `Nothing` was a no-op that left the
  *previous* user id stamped on the session — so after sign-out
  (`model.session` → `Nothing` → resolver returns `Nothing`) every subsequent
  auto page-view (and the persisted session blob) kept attributing events to the
  signed-out user. The resolver is now symmetric: `Nothing` / `Just ""`
  **clears** the user id, reverting the session to anonymous, and the cleared
  state persists on the next render. Signed-in and explicit-`identify`-only apps
  are unaffected. (`runtime-go/rt/analytics_kernel.go`
  `analyticsApplyIdentity`; regression
  `TestAnalyticsApplyIdentityClearsOnSignOut`.)

- **`sky fmt` — multi-line record field values now break onto their own line,
  aligned.** A record field whose value spanned multiple lines (a list, nested
  record, or `case`) was left inline after `field = `, with its continuation
  `,`/`]` indented to a fixed depth that aligned with nothing — e.g.
  `routes = [ route "/" Home` followed by `,` items two columns in. The
  formatter now breaks a multi-line (or over-wide) value onto the next line,
  indented one step, so `[`/`,`/`]` (and `{`/`,`/`}`) line up:
  ```elm
  , routes =
      [ route "/" Home
      , route "/about" AboutPage
      ]
  ```
  Single-line values that fit stay inline. Idempotent + comment-preserving
  across the corpus (fmt gate); reformatted the stdlib + examples to match.

## v0.19.1 — record field-set collision codegen fix (2026-07-30)

### Fixed

- **Codegen — record field-set collision (`sky check` passed, `go build`
  failed).** Two record aliases that share a field-**name** set but differ in
  field **types** — e.g. a user's `EnvForm {key, value : String}` and the new
  `Std.Analytics.EventProp {key, value : PropValue}`, which `Std.Live` pulls in
  transitively — collided in the structural-record resolver. It keyed records by
  field name only, so the first-registered alias arbitrarily won and the other's
  function parameters were emitted with the WRONG Go struct (`form.value` typed as
  `PropValue` instead of `string`), producing a `go build` failure that
  `sky check` never caught. The resolver now keeps every candidate per
  field-name set and selects the one whose field **types** match; parametric
  aliases (`Cfg msg`) are unaffected (their type-var slots stay wildcards, with
  the concrete arg recovered as before). This surfaced in v0.19.0 because
  `Std.Analytics.EventProp` was new — any Sky.Live app with a `{key, value}`-ish
  record could hit it. Regression: `examples/54-record-fieldset-collision`.
- Docs carried over from the v0.19.0 line: the README quick-start sample + a sharp
  v0.18→v0.19 `Live.app` breaking-change callout, and the raw-`api`-handler change
  (`Dict String any -> Response` → `Request -> Task Error Response`, now in
  `routes`) documented in the migration guide.

## v0.19.0 — codec-driven persistence, file-based DB migrations + Std.Analytics (2026-07-30)

### Breaking — TEA app config is now a typed builder

`Live.app` / `Tui.app` / `Tui.program` / `Cli.program` no longer take a row-open
**record literal**. The six required fields go inside `config { … }` (which
produces an opaque, hover-able `AppConfig`), and optional fields
(`head` / `guard` / `consoleAuth` / `analytics` / `status` / …) attach with
`withX` builders in a pipe. `Webview.app` keeps its closed record (no optional
fields).

- **Why:** the row-open record was untyped — it hovered as `?` and drifted from
  the docs. The builder makes the config a real checkable type and unifies the
  kernel-module docs onto one source (`sky doc`, LSP hover, and the type-checker
  now all read the module's `.sky` file). New optional attributes like
  `withAnalyticsIdentify` are only reachable through it.
- **Migration (mechanical):**
  ```elm
  -- before
  main = Live.app { init = init, update = update, view = view
                  , subscriptions = subscriptions, routes = [...], notFound = Home
                  , head = headFor, analytics = { pageViews = True } }
  -- after
  main = Live.app
      (Live.config { init = init, update = update, view = view
                   , subscriptions = subscriptions, routes = [...], notFound = Home }
          |> Live.withHead headFor
          |> Live.withAnalytics { pageViews = True })
  ```
  Full field→builder table for every app shape:
  [`docs/v0.19/migration-builder-cfg.md`](docs/v0.19/migration-builder-cfg.md).
- **Raw `api` endpoints changed shape too.** The old separate `api` cfg field is
  gone; `api "METHOD /path" handler` now returns a `Route` and lives in the
  `routes` list next to `route`. The handler signature is
  **`Request -> Task Error Response`** — `Request` is a typed record
  (`.method` / `.path` / `.headers` / `.params` / `.query` / `.cookies` / …), and
  the return is `Task Error Response` (wrap a plain `Response` in `Task.succeed`).
  Pre-v0.19 `Dict String any -> Response` handlers must migrate to the record +
  Task shape.

### Added — file-based database migrations (`sky db`)

Define your schema with `Std.Db.Store` + `Std.Codec` and expose
`db : Store.Project`; Sky generates and applies committed migration files —
**no live database needed to diff**. One committed file is dialect-correct on
SQLite *and* PostgreSQL (verified end-to-end on both).

- `sky db init` — scaffold `db/migrations/` + `db/schema.json`.
- `sky db migrate --gen [name]` — diff the type-derived schema against the
  committed snapshot and write a migration file. New required columns get a safe
  `NOT NULL DEFAULT <zero>` backfill; `Maybe` fields become nullable;
  dropped/retyped columns are **quarantined** (never silently applied). On a TTY,
  gen asks whether a dropped column was renamed (rewritten to one `renameColumn`,
  data preserved), dropped for good, or skipped, and lets you set a custom
  backfill default.
- `sky db status` — ✓ applied / ○ pending per committed file vs the live
  `_sky_migrations` ledger; **exits non-zero while anything is pending** (a
  ready-made deploy gate).
- `sky db migrate` — apply the committed files through the checksummed ledger, at
  most once each, dialect-correct for the connection.
- `sky db seed` — run your entry module's `seed : Db -> Task Error ()`.
- `sky db push` — the no-migration-files dev loop: sync the live DB to your types
  (create missing tables, add new columns). `sky run` gains `--db-push` /
  `--db-migrate` / `--db-seed` flags to run those steps before serving.
- **Self-migrating binaries.** `sky build` embeds `db/migrations/` into the app,
  so a deployed `SKY_DB_OP=migrate ./app` applies them with no source tree and no
  `sky` toolchain on the host. Run it once as a deploy step; replicas booting
  without `SKY_DB_OP` never migrate (safe to scale out).

Walkthrough: [`docs/tooling/cli.md`](docs/tooling/cli.md#database).

### Added — codec-driven persistence (`Std.Codec` + `Std.Db.Store`)

Write **one `Codec` per type**, reused for JSON *and* dialect-safe DB
(schema + read + write) — no hand-written row mappers, no `SqlValue` lists.
The recommended default for record-shaped tables. See
[`docs/skydb/overview.md`](docs/skydb/overview.md).

- **`Std.Codec`** — `Codec.auto blank` reflection-derives a codec from a
  zero-value witness (scalars → columns, `Maybe` → nullable, list/nested/ADT →
  JSON blob, nullary enum → readable name). Columns/JSON keys are **snake_case
  by default** (`priceMinor` → `price_minor`); `Codec.autoCamel` keeps camelCase;
  **`Codec.autoWith [ ("col", codec) ] blank`** overrides specific fields while
  auto-deriving the rest (a `Bool` stored 0/1, a custom enum format) with no
  full hand-written codec. `toJson` / `fromJson` / `fromJsonSafe`.
- **`Std.Db.Store`** — codec-driven CRUD. Schema builders pipe onto the store
  (each takes the record field *or* snake column; a typo fails fast): `serial`
  (auto-increment PK), `unique`, `defaultNow` / `defaultText` / `defaultInt`,
  **`touchOnUpdate`** (a timestamp DB-stamped on insert *and* auto-bumped to
  `now()` on every update — no raw SQL), **`defaultWith`** (app-side computed
  default, e.g. a UUID PK), **`defaultBool`** (`TRUE`/`FALSE` on Postgres,
  `1`/`0` on SQLite), `generated`.
- **`Std.Db.Schema.toProject`** — bridge explicit `Schema.Table` definitions into
  a `Store.Project` so a `Schema.Table`-based app reaches the migration tooling
  (`sky db push` / `sky db migrate --gen`) with one line —
  `db = Schema.toProject allTables` — no rewrite into codec stores. Table
  name / PK / `UNIQUE` / `NOT NULL` / `DEFAULT` / autoincrement carry through;
  secondary indexes stay on `createSchema` (which renders `withIndex`).
- Writes: `insert`, **`insertMany`** (one multi-row INSERT for bulk/time-series),
  `update` (by PK), `updateWhere` (by `Cond`), **`upsert`** (`INSERT … ON
  CONFLICT DO UPDATE`), `delete`, `deleteWhere`.
- Reads: `all` / `findBy` + a composable, injection-safe query builder
  (`where_` / `and_` / `or_` / `not_` / `eq`…`inList` / `orderAsc` / `limit` /
  `toList` / `toMaybe` / `count`), `sqlOf` (filter by a typed value via its
  codec), and **`selectRaw codec sql params`** — run any SQL (JOIN / `GROUP BY` /
  aggregate) and decode each row into a typed projection record (the sqlx split;
  deliberately not an ORM).

### Added — `Std.Analytics` product analytics

Typed product analytics for Sky apps — open payload builder with typed prop
values (`Money` lossless, `Pii` a distinct redactable type), pluggable sinks
(stderr / JSONL / custom POST), a SQLite store, and a Sky Console **Analytics**
tab. See [`examples/52-blog-analytics`](examples/52-blog-analytics).

- Consent defaults to **`Granted`** — enabling analytics captures fully and
  `identify` attaches the user, which is what most apps want. Privacy-conscious
  apps show a consent banner and downgrade with `setConsent Anonymous` / `Denied`.
  Consent + identity are session-scoped. Only an **explicit** `setConsent` is
  persisted as a choice — a session that merely rode the framework default
  follows the *current* default on restore, so the `Granted` default reaches
  sessions already stored in a DB-backed store (they don't stay stuck on a
  previous default), while an app's explicit `Anonymous`/`Denied` is respected
  verbatim across restart / replica reshuffle.
- **Sky.Live auto page-views** — `Live.withAnalytics { pageViews = True }`; add
  **`Live.withAnalyticsIdentify (\model -> Maybe String)`** to attribute an
  already-authenticated session from the first render.

### Fixed

- **Codegen (subset-record).** A function that read only some fields of a record
  parameter *and* returned the whole record via `Ok`/`Just` narrowed the
  constructor's type-arg to an anonymous subset struct, failing `go build`. `Ok`/
  `Just` now take the payload type from the argument's own type.

### Added — `sky upgrade` prints release notes

After a successful upgrade, Sky prints the notes for every version between the
old and new binary, flagging any release that carries a breaking-change /
migration section. `sky upgrade --notes` previews the notes without upgrading.

## v0.17.0 — typed-emit soundness floor (2026-06-28)

Release plan: [`docs/v0.17/release-plan.md`](docs/v0.17/release-plan.md).
Judge re-verdict: REFRAMED 100% ACHIEVED + VERIFIED.

### Compiler

- **Typed-emit wrap-target gate.** `resolveWrapParams` +
  `resolveWrapParamsCtx` now gate HM-override on enclosing-scope T-var
  presence. Closes the wrong-typed wrap class (8 go-build errors on
  `examples/00-standard-libs` → 0; 131/131 runtime). Symbol-level
  diagnosis at `docs/v0.17/session-2026-06-28-diagnosis.md`.

- **rt.Coerce residual surface — documented sound.** All Coerce-family
  sites on the canonical `examples/26-ui-showcase` benchmark enumerated
  across 8 safety classes with explicit soundness proofs at
  `docs/v0.17/rt-coerce-residual-surface.md`. Zero "unknown / unsafe"
  remainders. Closes the rock-solid soundness claim under the reframed
  v0.17.0 goal.

- **`scopeStateRef` IORef contract + audit spec.** Per CLAUDE.md §0.3
  criterion #3 locked wording. Compile.hs:496-595 documents the
  bracket-scoped (Class A) + monotonic-accumulating (Class B) write
  semantics; `Sky.Build.ScopeStateRefAuditSpec` machine-verifies the
  writer counts (25 + 17) + the layering invariant. Pattern mirrors
  `Sky.Build.AnonRecordWriterAuditSpec`.

- **Per-panic-class emission-time regression locks.**
  `Sky.Build.PanicClassGateSpec` adds the emission leg of the
  three-leg soundness stool (runtime classification at
  `runtime-go/rt/panic_recover_test.go` + example sweep / verify-cli /
  WellTypedFuzzer real-world leg + this emission-time leg). 11 tests
  covering C1-C7 panic classes.

### Limitations closed in v0.17

See [`docs/KNOWN_LIMITATIONS.md`](docs/KNOWN_LIMITATIONS.md) for the
full current-state catalog. Items closed since v0.16.x:

- Negative literal arguments (`f -1` parses as `f (-1)`)
- Multi-line function signatures (both `: T` and `-> T` continuation)
- Zero-arg call shape arity gate (`[E2007]` StrictHmArityGate)
- `Css.*` keyword constants are bare values (`Css.zero`)
- `Dict.toList` typed-key inference works inline AND let-bound
- `sky check` validates Go interface satisfaction empirically
- All list ops on constant Go stack (CPS / accumulator rewrites)
- 3-tuple literals at top-level
- Sky.Live `init` receives full `Request`
- URL-driven route matches fire `Navigate` Msg

### Docs

- **Cleanup pass.** 22 v0.17 design notes + 2 v0.16.13 handoffs moved
  to `docs/archive/`. 39 MB of build artifacts under
  `docs/v0.16.x-console/parametric-cfg-repro/sky-out/` deleted.
- **`docs/session-protocol.md` folded into `CLAUDE.md` §0.4** as a
  durable Session Methodology section (phase pattern, agent +
  grilling, three-leg soundness stool, N-strikes circuit-breaker,
  reframed-vs-literal goal handling, push discipline, context
  discipline).
- **`docs/KNOWN_LIMITATIONS.md` refreshed** to v0.17.0 state.

## v0.15.3 — typed let-binding RHS + sibling-helper call sites (2026-05-25)

### Codegen

- **Closed `interface conversion: Cfg_R[Msg] vs Cfg_R[any]` panic
  in the REVERSE direction from v0.15.2.** Surface: a library
  module (sky-editor's `Editor.view`) defines `view : Cfg msg ->
  Element msg` whose body forwards `cfg` to sibling polymorphic
  helpers (`editorBody cfg`, `toolbar cfg onCheck`, `diagnostics
  cfg onDismissCheck`). v0.15.2 closed the *literal*-into-typed-
  slot direction (`Cfg_R[any]{...}` → `Cfg_R[Msg]`); v0.15.3
  closes the *typed-source*-into-erased-slot direction
  (`Cfg_R[Msg]` arg → `Cfg_R[any]` callee param at the sibling
  call site, plus `Cfg_R[T1]` arg → `Cfg_R[any]` sibling call
  inside the generic body itself).
  - **Symptom in production:** clicking the Source tab in the
    skydeploy file editor panicked at every render with
    `Cfg_R[State_Msg] vs Cfg_R[any]`.
  - **Fix mechanism (4 surgical changes, all in `Sky.Build.Compile`):**
    1. `letBindingType` — types the RHS of a zero-param let-
       binding from the source region or HM solver, gated on
       `canRouteTyped` (only record literals, lambdas, and
       control flow get typed routing — Can.Call/Access pass
       through untyped so FFI return wrappers like `rt.AsListT`
       don't strip Result-Ok wrappers).
    2. `Can.Access` typed-field-access path — now also fires
       when `inferExprType` returns an ambig TVar but
       `lookupLambdaType` carries the concrete `TAlias`
       (function param via `withScopedLambdaTypes` from the
       dep-emission registration). Includes a secondary check
       via `lookupLambdaGoStr` to catch the lazy-rendering race
       where the Go-string registry is active but the Sky-type
       registry isn't yet populated.
    3. `coerceArg` — short-circuits `any(arg).(Foo_R[any])`
       nominal cast when source's static Go type is the SAME
       parametric record alias base. Lets Go's call-site type
       inference pin the callee's T from the source's
       instantiation, which is the only correct behaviour
       across Go's nominal generic typing.
    4. Param registration in dep-emission (`goStringBindings` +
       `inferredArgTys`) now includes parametric record alias
       params, not just func-typed ones — so the call-arg short-
       circuit has the info to fire.
  - **Regression test:** `test-files/v0.15-stress/src/Widget/
    Form.sky` is a synthetic library mirroring sky-editor's
    `Editor.sky` shape (top-level polymorphic `view cfg`,
    sibling helpers, mixed `_ -> msg` + bare `msg` fields,
    Std.Ui body, let-extracted polymorphic fields). The L1-L7
    assertion in `examples/00-standard-libs`-style `Main.sky`
    fails on v0.15.2, passes on v0.15.3.

### `defToStmts` zero-param let-binding

- `Can.Def name [] body` now consults the same `letBindingType`
  helper before lowering, so `main`'s top-level let-bindings of
  record literals emit as `Setup_R[Msg]{...}` instead of the
  type-erased `Setup_R[any]{...}` shape that propagated the
  panic at downstream call sites.

### Known gap (documented in regression test)

- Passing a let-bound func-typed field-access (`let submit =
  cfg.wfSubmit in submitProbe cfg submit`) to a SAME-MODULE
  generic helper still emits `rt.Coerce[func(P) any](submit)`,
  which fails Go's call-site inference against the callee's
  `func(P) T1` slot. Workaround in user code: pass the field
  directly (`submitProbe cfg cfg.wfSubmit`). Sky-editor's
  actual code does NOT hit this — it passes such fields to
  Std.Ui kernels (`Ui.onSubmit cfg.onSubmit`) where the kernel's
  reflect-adapter handles the conversion. The synthetic
  `submitProbe` case is commented out in the regression test
  with a forward-looking note for the next iteration.

### Verification gates (all green pre-merge)

- Cabal test: 306 examples, 0 failures, 1 pending (matches v0.15.2).
- 27/27 examples build clean from wiped slate.
- `examples/00-standard-libs` stdlib smoke test: 120/120 assertions pass.
- `sky check` clean on `examples/{12-skyvote, 13-skyshop,
  19-skyforum, 26-ui-showcase, 00-standard-libs}` + synthetic
  stress test + skydeploy control plane.
- `scripts/verify-cli.sh`: 13 pass / 0 fail / 1 skip.
- `scripts/verify-all-web.sh`: 10 pass / 0 fail + console-e2e green.
- `scripts/lsp-test-nvim.sh`: 17/17 LSP requests pass (hover,
  completion, goto-def across kernel calls, field access, let-
  bindings, lambda params, case patterns).
- Skydeploy control plane: generated Go for `Editor_view` /
  `Editor_view__Msg_...` no longer emits the panic-causing
  `any(cfg).(Editor_Cfg_R[any])` cast at sibling helper calls.


## v0.15.2 — Cfg_R[any] panic fix + version propagation (2026-05-24)

### Codegen

- **Closed `interface conversion: Cfg_R[any] vs Cfg_R[Msg]` runtime
  panic** at every place a `Can.Record` literal sits in a typed
  call-arg slot whose Go target is a parametric record alias
  instantiation. Surfaced by skydeploy's Editor (`Editor.view
  editorCfg` at AppDetail.sky:Source tab) on every mount — Go
  generic types are nominal, so `any(Cfg_R[any]{...}).(Cfg_R[Msg])`
  fails at runtime even though Go's type checker accepts it.
  - **Fix:** call-arg lowering at every site (`zipWithDefault
    coerceArg exprToGo`, `coerceCallArgsAt`'s `coerceOne`,
    `kernelCoerceArg`, bare ctor-call zip) now routes
    `Can.Record` literals targeting parametric record slots
    through `exprToGoExpectGo` → `lowerRecordLiteralTo`, which
    emits the literal with the target's concrete type args
    directly (no nominal-type-assert wrapper).
  - **Symmetry:** the same pipeline also routes `Can.Lambda` at
    typed `func(...) ...` slots through `lowerTypedLambda` (was
    already happening at some call sites; now uniform across all
    five).
  - **Edge cases handled:** the new arms are uniformly gated on
    `not (containsGenericTypeParam ty)` so call sites where σ
    hasn't pinned the callee's TVar (`Cfg_R[T1]`) fall back to
    the legacy `coerceArg` path — emitting `Cfg_R[T1]{...}` at
    the caller would trigger `undefined: T1` since T1 names the
    callee's type variable, not in scope here. The existing
    `exprToGoExpectGo` arms (record-field-init, list-elem) are
    unchanged because they're only reached from contexts where σ
    is already concrete.
  - Stage E shipped the parametric record alias struct generation
    + Stage E.2 routed the record-field-init context; v0.15.2 closes
    the call-arg context that Stage E missed.

### `sky build`

- **`sky build` now injects `-ldflags "-X sky-app/rt.skyVersion=<compiler version>"`** into the underlying `go build`. Every Sky-built app's `/_sky/buildinfo` now reports the actual Sky version that built it instead of the default `"dev"`. No deploy-script ceremony — a tagged Sky binary built with `cabal install -ldflags="-X main.skyBuildVersion=0.15.2"` propagates that string to every app it compiles.
  - **Why:** pre-v0.15.2, the `rt.skyVersion` package-level var defaulted to `"dev"` and was only populated by the Sky compiler's own release CI (`-X main.skyBuildVersion=...`). The compiler's own version never reached the apps it built — every deployed Sky app reported `"skyVersion":"dev"` regardless of which tagged compiler had built it.
  - **Migration:** none. Existing apps rebuild → buildinfo flips from `"dev"` to the real version on next `sky build`. Deploy scripts that previously injected the ldflag manually (none in the public examples) can remove that step.

## v0.15.1 — Docs: `SKY_ADMIN_TOKEN` canonical (2026-05-24)

- **Docs: `SKY_ADMIN_TOKEN` is the canonical env var** for gating
  `/_sky/metrics` and `/_sky/console` in production. The v0.15.0 doc
  refresh accidentally kept `SKY_METRICS_TOKEN` (a v0.14.21 legacy
  alias) as the recommended name in `README.md` + `CLAUDE.md` +
  `templates/CLAUDE.md`. Runtime behaviour unchanged — both
  `SKY_METRICS_TOKEN` (v0.14.21) and `SKY_CONSOLE_TOKEN_SECRET`
  (v0.14.20) are still honoured by `adminTokenSecret()` in
  `runtime-go/rt/subapp.go`.

## v0.15.0 — Type-directed lowering (2026-05-24)

### Type system

- **Type-directed lowering throughout.** Sub-expressions at lambda
  bodies, record-field inits, list elements, and call args lower with
  the slot's typed Go form propagated. The solver writes a per-region
  type map (`globalRegionTypes`); `LowerCtx` threads the expected
  type down through `exprToGoExpectGo`. Closes the long-standing
  parametric-record-alias bug class (every Surface 1/2/3 is now
  shipped). Architecture: [`docs/v1-rfc/type-soundness-deep-analysis.md`](docs/v1-rfc/type-soundness-deep-analysis.md).
- **Go generics on parametric record aliases.** `type alias Cfg msg
  = { onSubmit : msg, label : String, ... }` now emits
  `type Cfg_R[T1 any] struct { OnSubmit T1; Label string; ... }`
  with per-instance type args (`Cfg_R[Msg]`, `Cfg_R[Int]`). Callback
  fields keep their typed callee parameter — no more `func(any) any`
  fallback at parametric-record slots.
- **Inline lambdas keep their typed shape at record-field slots.**
  `{ onSubmit = \s -> Tag ("L:" ++ s), ... }` against `Cfg Msg` now
  emits `func(string) Msg` for the lambda, not `func(any) any`.
- **Cross-alias call without the alias-chain workaround.** Structurally-
  equal records can be passed across module boundaries without the
  `type alias State.FileForm = Editor.Form` redirect. The redirect
  remains a valid idiom but is no longer required.
- **Same-module polymorphic call re-instantiation.** Annotated `f :
  Cfg msg -> msg` called with `msg=Int` AND `msg=Bool` in the SAME
  module both work — sibling references alpha-rename per call site.
  Previously the first call pinned `msg`.
- **Wildcard-`any` soundness gate.** `view : Model -> any` returning
  a String against an expected `Model -> Html msg` slot now correctly
  surfaces as a type error. Mid-development the v0.15 same-mod
  CForeign change wrongly treated wildcard-only sigs as polymorphic;
  the final gate requires at least one non-`any` freeVar before
  routing through CForeign. The pair `Canonicalise.Type.freeTypeVars`
  (collects wildcards) + `Instantiate.fromAnnotation` (filters them
  + per-occurrence fresh UF var) is documented in CLAUDE.md as
  load-bearing.

### Type errors / diagnostics

- **TAlias type-args propagate through readback + showType +
  typeStructEq.** Errors like `Cfg Msg vs Cfg Int` are now shown
  with their type args instead of the unhelpful `Cfg vs Cfg`.
- **Unify.hs App1 ↔ Alias same-name bridge.** Recursive parametric
  alias bodies (`type alias Tree a = { value : a, kids : List (Tree
  a) }`) unify with external `TAlias` references correctly.
- **Canonicaliser parametric-alias var substitution (Surface 1).**
  Sky source can now access fields on `Cfg msg`-typed function
  parameters without dropping to structural inference.

### Limitations closed in v0.15 (with the older list trimmed)

- ~~Let bindings with parameters after multi-line case~~
- ~~Zero-arity functions reading env vars memoised at init()~~
- ~~`exposing (Type(..))` for user-module ADT constructors~~
- ~~`import X as Alias` leaks the alias into codegen~~
- ~~`let` bindings don't support forward references~~
- ~~Parametric record alias bugs (Surfaces 1, 2, 3)~~

### Verification

- 27/27 examples clean-build from a wiped slate
- 120/120 stdlib Sky.Test assertions (`examples/00-standard-libs`)
- 21/21 v0.15 parametric-record-alias stress test sections
- 306/306 cabal tests (0 failures, 1 pending) — including the LSP
  `DiagnosticsSpec` "TEA with Live.app: wrong view return type
  surfaces as a real diagnostic" case
- `scripts/verify-all-web.sh` — 10/10 Sky.Live + Sky.Http.Server
  Playwright runs + console-e2e
- `scripts/verify-cli.sh` — 13/13 CLI / Tui / Cli (Fyne X11 skipped)
- Skydeploy clean rebuild + runtime probe (`/`, `/_sky/healthz`,
  `/_sky/buildinfo`, console mounted)

## Unreleased

### Std.Ui — surface complete

- **Background**: `image url`, `linearGradient angle stops`, `gradient css` (raw CSS escape).
- **Border**: `widthEach { top, right, bottom, left }`, `solid` / `dashed` / `dotted`, `shadow { offsetX, offsetY, blur, spread, color }`, `glow blur color`, `innerShadow {…}` (rendered with CSS `inset`).
- **Font**: `italic`, `underline`, `letterSpacing em`, `wordSpacing em`, plus weight helpers `semiBold` / `extraBold` / `black`.
- **Region** (new + wired through): semantic landmarks now route to real HTML tags via the renderer — `mainContent` → `<main>`, `navigation` → `<nav>`, `footer` → `<footer>`, `aside` → `<aside>`, `heading n` → `<h1>`..`<h6>`. Plus `label text` → `aria-label="..."`, `announce` → `aria-live="polite"`, `announceUrgently` → `aria-live="assertive"`. Previously these helpers existed but the renderer didn't dispatch — they all rendered as `<div>`.
- **Nearby positioning**: `above` / `below` / `onLeft` / `onRight` / `inFront` / `behind` — wraps the parent with `position: relative` and the nearby element with `position: absolute` + matching offsets. Use for tooltips, popovers, dropdown menus, badges.
- **Input**: typed wrappers for `email`, `username`, `search`, `currentPassword {show: Bool}`, `newPassword {show: Bool}`. New `radio` / `radioRow` / `slider` controls (radio uses string-valued `RadioOption` to sidestep deeply-polymorphic-record HM friction). `placeholder` text now actually renders as the HTML `placeholder=` attribute. `LabelHidden` emits `aria-label` for screen-reader access.
- **Overflow** (new): `clip` / `clipX` / `clipY` / `scrollbars` / `scrollbarX` / `scrollbarY`.
- **`Ui.html` escape hatch**: now wraps an arbitrary Std.Html VNode via the new `Raw any` Element variant. Previously collapsed to `Text ""` (placeholder).
- **Compiler-side**: `Html.aside` registered in the kernel registry so the renderer's `<aside>` dispatch resolves to `rt.Html_aside`. `Html.main` was already registered.
- **Limitation #14 doc clarification**: the documented "use `Ui.text ""` instead of `Ui.none`" workaround was misleading. `Ui.none` works fine when annotations use bare `Element Msg` (via `import Std.Ui exposing (Element)`) rather than the qualified `Ui.Element Msg`. Updated `docs/skyui/overview.md` accordingly.

### Licence + attribution

- **Relicensed to Apache License 2.0** (was MIT). Existing MIT releases (v0.10.0 and earlier) keep their original MIT terms; v0.10.1 onwards ships under Apache 2.0. The relicense brings:
  - **Patent grant** from contributors (Apache 2.0 §3) — perpetual, irrevocable patent licence for what their contribution covers.
  - **Patent-retaliation clause** — anyone initiating patent litigation against Sky users for the contribution loses their grant.
  - **Trademark clause** (§6) — the licence does not grant rights to use the "Sky" name / trademarks.
  - **NOTICE file mechanism** (§4(d)) — a structured way to propagate prior-art attribution through forks. `NOTICE.md` at the repo root.
  Same permissive philosophy as MIT (commercial use, modify, fork, sublicense all allowed). See [CONTRIBUTING.md](CONTRIBUTING.md) for what this means for contributors. Same week, the [Std.Ui — Sky.Live polish + 4 compiler reliability fixes](https://github.com/anzellai/sky/pull/36) PR also lands.
- **Per-file derivative-work attribution** strengthened on the ten files in `src/Sky/` adapted from elm/compiler (BSD-3-Clause, © Evan Czaplicki). Each file's header now names the upstream module + licence + copyright, and `NOTICE.md` lists every adapted file with its origin and reproduces the full BSD-3-Clause licence text. This satisfies BSD-3-Clause clauses 1 + 2 (source-form + binary-form attribution).
- **Defensive endorsement-clause cleanup**: removed promotional uses of "Elm" (and the prior promotional uses of "elm-ui") from user-facing docs / READMEs / runtime comments. Factual technical references — "Elm-compatible syntax", "matches Elm's behaviour", "Elm convention", per-file derivative-work attribution — stay because they are descriptive, not promotional.

### Effect boundary (stdlib)

- **Breaking — `Std.Db.*` migrated from `Result Error a` to `Task Error a`.** `Db.connect`, `Db.open`, `Db.exec`, `Db.execRaw`, and `Db.query` now return `Task Error a`. Their runtime helpers (`runtime-go/rt/db_auth.go`) wrap their bodies in `func() any { ... }` thunks so the actual SQL defers to the goroutine spawned by `Cmd.perform` instead of blocking Sky.Live's `update()`.
  - **Why:** DB ops can take hundreds of milliseconds, can fail meaningfully, and compose naturally with `Task.parallel` / `Task.andThen` / `Cmd.perform`. Typing them as Result was a pre-Sky.Live legacy that forced every effectful pipeline to either bridge through `Task.fromResult` or block the dispatcher.
  - **Migration in this branch:** every `Lib/Db.sky` (08-notes-app, 12-skyvote, 17-skymon) and `Lib/Games.sky` (16-skychess) wrapper kept its Result-shaped public API by bridging through `Task.run` internally — consumers (Main.sky, Page/*.sky) need no changes. `examples/07-todo-cli/src/Main.sky` was rewritten as a proper Task-chained CLI demonstrating the canonical error-propagation pattern. `examples/18-job-queue/src/Main.sky` was simplified to drop the now-unnecessary bridge helpers in `saveSnapshot`/`loadHistory`. `examples/13-skyshop` is unaffected (it uses Firestore, not Std.Db).
  - **For new app code:** prefer composing Task-returning Db calls directly (`Db.exec db "INSERT..." [...] |> Task.andThen ...`) and dispatch via `Cmd.perform`. Use the Lib-layer `Task.run` bridge only when wrapping a singleton conn for synchronous case-pattern matching inside an existing update branch.

- **Added — `Task.onError` and `Task.mapError`.** Mirror their Result counterparts. `Task.onError : (e -> Task e2 a) -> Task e a -> Task e2 a` recovers from a Task error by producing a new Task — the canonical primitive for converting DB / FFI errors into 4xx/5xx HTTP responses, Sky.Live notifications, or CLI exit codes. `Task.mapError : (e -> e2) -> Task e a -> Task e2 a` adds context to an error before propagation.

- **Added — kernel sigs for `File.*`, `Process.*`, `Io.*`, `Crypto.randomBytes`, `Crypto.randomToken`** (Bucket A2 of the audit). Type-only addition: the runtime helpers already returned Task thunks, the docs/stdlib tables already promised Task; HM now enforces what the runtime had silently delivered. Net-zero migration.

- **Codegen fix — `coerceArg` now handles `SkyTask` params.** Previously, passing a value to a function expecting a typed `rt.SkyTask[E, A]` param emitted `any(arg).(rt.SkyTask[E, A])` direct assertion, which panicked at runtime against `func() any` from runtime helpers and against `SkyTask[any, any]` from cross-instantiation pass-through (Go generics are nominal). Fixed by routing parametric SkyTask param targets through `rt.TaskCoerceT`, mirroring the existing `SkyResult`/`SkyMaybe` handling. Also extended the same wrap to the `VarLocal` call-result path. This unblocked the entire Db.* migration.

- **Doctrine clarification in CLAUDE.md ("Effect Boundary: Task — two-tier in practice").** The audit considered migrating *every* effectful op to Task (println / Slog / Os.getenv / Os.getcwd / Time.now / Time.unixMillis) and concluded these stay sync. Reasons documented in CLAUDE.md under "Why theory ≠ practical here" — `let _ = println …` discard pattern, module-level `apiKey = Os.getenv "X" |> Result.withDefault ""` config reads, "stamp this row" timestamp use sites. Sky picks the Elm-pragmatic position over the Haskell-purist one: real I/O that benefits from composition goes through Task; sync convenience effects that don't benefit stay sync.

### Sky.Live

- **Breaking — default HTML template no longer loads Inter from Google Fonts.** The shell document emitted by `Live.app` previously preconnected to `fonts.googleapis.com` / `fonts.gstatic.com`, fetched the Inter family, and forced `font-family: 'Inter' … !important` on `body` and `.font-sans`. All four lines have been removed.
  - **Why:** third-party request on every cold page load (offline dev, GDPR, every visitor's IP logged with Google), plus an `!important` rule that fought app-level typography. There was no opt-out.
  - **Behaviour now:** the `<head>` ships only `<meta charset>` and `<meta viewport>`. Headings and body inherit the browser default (Times/Arial) until the app sets typography itself.
  - **Migration:** apps that want a webfont add it explicitly — e.g. a `Html.styleNode` in the view's head fragment, a self-hosted `@font-face` in a `Css.stylesheet`, or a `<link>` served from `Server.static`. Apps that were silently relying on the default Inter will look unstyled until they set their own font.
  - **Privacy/a11y wins:** no third-party network request from the runtime, and no `!important` override blocking accessibility-first apps that self-host (e.g. Atkinson Hyperlegible).
