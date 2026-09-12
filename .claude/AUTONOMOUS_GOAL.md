# Autonomous goal (verbatim)

Set 2026-09-11. Supersedes the 2026-09-06 sky-lang.org SSR mandate (shipped) and
the partitioning-maturity mandate (achieved prerequisite: darraghstudio
partitions + builds `--target web:app`, shipped as SPA at tag v1.1.1).

> we will need to understand user may start off from sky.live pattern, and if
> they render/target wasm app, it shouldn't require them 'refactor' everything
> to work, so all the gaps you identified should work behind the scene, without
> user changes their code
>
> also remember all the issues we found for wasm web, will apply for desktop
> mobile apps too
>
> ok please proceed fully unattended + autonomous + PIV

Later steers (2026-09-11): reuse Sky.Live's session logic so a developer can
switch `--target` between Live and SPA and the end user gets the same session
experience; but the SPA MUST stay the SCALABLE target (stateless, no server
session store, no sticky sessions).

## The mandate

A Sky.Live app that targets a wasm client (`--target web:app` AND `desktop:*` /
`mobile:*` / `tablet:*`) must JUST WORK with ZERO app refactor. Every gap closes
in the COMPILER auto-split + the Sky.Spa RUNTIME, never by the author adding
code. darraghstudio, UNCHANGED, is the driving real-world proof.

## The gaps + status (branch feat/spa-transparent-carry)

1. FORMS method=post — DONE (8940542a).
2. STATIC runtime uploads — DONE (655fc969): live `Server.static` mount for the
   app's dir before the dist catch-all + `backend/<dir>` seed. Verified in a real
   darraghstudio web:app build.
3. AUTH-TRUST (forge-role privilege escalation) — DONE (cfd3876a): STATELESS
   signed session. The backend signs the identity projection into an httpOnly
   `sky_sid` cookie on login (Auth.signToken, reuses Sky.Live's Std.Auth) and
   VERIFIES it on every RPC + SSR, taking the session from the cookie, never the
   wire. No server store → the SPA stays scalable. Secret = `Spa_sessionSecret`
   kernel (env `SKY_SPA_SESSION_SECRET` >=32B for multi-replica, else
   auto-mint+persist for a single VM). Sign-out endpoint `POST /_rpc/__spaSignOut`.
   Gates green + full spa_split_flow suite 52/52.
4. RELOAD PERSISTENCE (consent/cart/page) — DONE (2107468b): the wasm client
   persists the whole model to localStorage after each step and, on boot, merges
   it over the SSR seed but takes the session from the server-verified seed, not
   localStorage. Sign-out forwarded. Cross-target note left for the mobile/desktop
   shells to enable persistent storage (session already survives everywhere via
   cookie+SSR). Pure merge/cap logic host-tested.

## SCOPE CORRECTION (2026-09-11) — a drift in this file, and the honest close

This file called the b78311d "migrate to Sky.Spa" commit an "achieved
prerequisite" and scoped the mandate to four gaps. That was drift (§0 rule 3
signal phrase). b78311d is a real 1861/1161-line app refactor: pre-migration
darraghstudio read the Db and env INSIDE views (`Data.listActive ()`,
`Config.companyName ()`), which works in Sky.Live only because Live views run
server-side. The migration moved that into model-loaded `data` + pure views. The
verbatim goal says a Live-pattern app should reach wasm "without refactor
everything." So the migration IS in the goal's field of view, not outside it.

The architecture reference settles what is closeable (docs/skyspa/design.md, the
§0.3 authority):
- §0.1 — the TRANSPARENT auto-partition of an arbitrary Live app is FALSIFIED by
  measurement (real apps run effects inline and return `Cmd.none`; a classifier
  ships the DB read to the client). The working mechanism needs a MANDATED
  DIALECT: `Model = {ui, data}` + effects via `Cmd`/`Task`, no inline server
  reads.
- §3.1 / §4 / §8 — that dialect is the v1 contract; full AST-derived auto-RPC
  (the CPS branch-split, auto-split.md §4 Option B) is a v2 RESEARCH target.
- §2 — a server effect reachable from client code must be REJECTED with a clear
  error, and must NEVER be silently classified as client.

So the data-partitioning refactor is architectural FLOOR for v1 (a falsified
auto-derivation), not a closeable gap. The compiler's honest v1 job is: close the
RUNTIME gaps transparently AND reject dialect violations with a clear error.

PROBE (2026-09-11, v0.24.1 compiler, real darraghstudio worktree): a view
reverted to read a server-only CAF (`Data.listActive ()` + `Config.companyName ()`)
under `--target web:app` → VERDICT (a) CLEAN REJECT. The build fails with a
precise error naming the tainted view (`spaView_` via `View.view`), the server
kernel (`System.getenvOr`), the rule (client view must be pure), and the fix
(move the read to `init`/`update`, embed in the Model). NOT a silent ship
(soundness holds), NOT transparent hydration.

## Remaining to close the mandate

- DONE (proven): the FOUR runtime gaps (forms/static/auth/persistence) close in
  compiler+runtime with ZERO app edit — PIV curl e2e all-pass + Playwright 13/13
  on the migrated darraghstudio built UNCHANGED under web:app (see
  [[spa_transparent_autosplit_gaps]] §PIV + §P5).
- DONE: shell client-state persistence wired — Android `domStorageEnabled`, iOS
  `websiteDataStore.default()`, desktop system-webview per-bundle store; session
  rides the signed cookie + SSR on every shell. On-device emulator relaunch NOT
  verified (honest caveat in main.rs:3132).
- DONE (probe): dialect violation → CLEAN compile error, never a silent
  server-effect-to-client leak.
- FLOOR (user decision, §0.3 rule 5): the data-partitioning dialect refactor is
  required for a Live app that used the server-side-view affordance. Transparent
  auto-derivation is a v2 research target (design.md §0.1 falsification). Decide:
  accept the v1 dialect + clear-error as the close, OR authorise the v2
  CPS-branch-split research (Option B, auto-split.md §4).
- Independent fresh-context Judge renders the verdict (in flight) — I must not
  self-certify the floor.
- Follow-on (tracked, not blocking): Playwright e2e CI gate for the js-only
  form/upload glue.
- Prod: user chose to deploy the SPA to prod ("let's do it, i will verify in
  prod", 2026-09-11) — SUPERSEDES the earlier "prod stays Live" line here. SPA is
  live on darraghstudio.org (v1.1.2); pre-existing image thumbnails backfilled.
  Do NOT tag/release without explicit user ask (standing pref).

## Discipline
PIV per §0.3/§0.4: architecture-consult → adversarial grill → implement →
fresh-context Judge. Regression-test-first. No `Result String`; secrets typed;
root-cause only. Responses ASD-STE100 British-English, plain punctuation, no
filler. Never run git commits concurrently with a delegated committing agent
(2026-09-11 lesson: an executor's `git reset` orphaned a parallel commit).
