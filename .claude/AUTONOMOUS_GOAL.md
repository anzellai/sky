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

## Remaining to close the mandate

- PIV: build darraghstudio UNCHANGED under `--target web:app` and run a browser
  e2e — forge-role rejected, real login persists across reload, uploaded images
  serve, consent stays dismissed. Then an independent Judge (fresh context,
  verbatim goal) confirms zero app edits + gaps closed root-cause.
- Full release gate green (§0.2.1) before any merge/tag.
- Verify a desktop/mobile target build carries the same behaviour (per the
  "applies to desktop/mobile too" directive); wire persistent web storage into
  the generated mobile/desktop shells so client scratch-state survives relaunch.
- Prod stays on Sky.Live until the SPA is Judge-verified; no prod SPA redeploy
  before then. Do NOT tag/release without explicit user ask (standing pref).

## Discipline
PIV per §0.3/§0.4: architecture-consult → adversarial grill → implement →
fresh-context Judge. Regression-test-first. No `Result String`; secrets typed;
root-cause only. Responses ASD-STE100 British-English, plain punctuation, no
filler. Never run git commits concurrently with a delegated committing agent
(2026-09-11 lesson: an executor's `git reset` orphaned a parallel commit).
