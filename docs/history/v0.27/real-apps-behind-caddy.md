# v0.27.0 — real apps behind Caddy (sky-lang.org, darraghstudio)

This is the recorded evidence for the Phase 1 verify item "run the real apps
behind Caddy against this branch". It covers the v0.27.0 changes that alter a
deployed app: session-id rotation, the `__Host-sky_sid` / Sky.Spa `sky_sid`
cookies, and the `/_rpc` origin guard. An earlier run on 2026-09-28 used an
older build and kept its evidence only in a scratch directory. This run
replaces it.

**Verdict: NOT clean.** The run found one Sky regression that breaks a real
shop flow (finding 1). The guard, cookie and console checks pass.

## What was measured

| Item | Value |
|---|---|
| Date | 2026-09-30 |
| Sky commit | `afd32383` (`origin/release/v0.27.0`, branch `verify/v027-real-apps`) |
| Compiler | `sky-out/sky` from `./scripts/build.sh`, `sky --version` = `sky dev` |
| Compiler fingerprint | `sky-embed-fp-v1:b0718c6ae6f2cac0ff78e4cf0dfc12e618a71481fdfcfcb30b2b695533ad3a23` |
| Compiler sha256 | `8399d4b0dfa89e2179846378161c7479b7e658f7669162c4e17fd97c592efa6d` (same value before the first build and after the last check) |
| Comparison compiler | `sky v0.26.1` (the release the two sites run today), used only to classify differences |
| Go / Caddy | go1.26.1 darwin/arm64, Caddy v2.11.4 |
| Browsers | Google Chrome 154.0.8037.58 (`channel: "chrome"`), Playwright WebKit 26.4. Both headed. Fresh context for each check. |
| PostgreSQL | Homebrew PostgreSQL 14.21 (`SKY_POSTGRES_BIN`) |

The apps ran from scratch copies of their working trees (tracked and
untracked files, no ignored files, no production env files). No app
repository was changed. No deploy, remote host or third-party OAuth was used.

## Topology

Each app runs as production runs it: the Sky.Spa split backend binary from
`.skyapp/web-app/.split/backend`, with `../frontend/dist` beside it, behind
Caddy with the app's own Caddyfile. `ENV=production` for every run.

Adaptations to each Caddyfile, and nothing else:

- The site address is a `*.localhost` name with `tls internal`
  (`sky-lang.localhost`, `darraghstudio.localhost`, and
  `www.darraghstudio.localhost` for the www redirect block).
- A global block: `admin localhost:2999`, `http_port 8081`,
  `https_port 9443`, `skip_install_trust`, and a scratch `storage` dir. Port
  443 was not usable: another local Caddy already listens on `*:443` with
  `SO_REUSEPORT`, so connections were split between the two. The public origin
  is therefore `https://<name>.localhost:9443`.
- `/opt/<app>/frontend/dist` → the scratch build's `frontend/dist`; the
  upstream port `8000` → `8611` (sky-lang.org) / `8612` (darraghstudio); the
  log `output file` → a scratch file; the certificate pair of
  `Caddyfile.spa` → `tls internal`.
- A second site, `evil.localhost`, returns a static page. The browser uses it
  as a real foreign origin for the cross-origin POST check.

Caddy keeps the `Host` header, so `SKY_PUBLIC_URL` was not set.

## Commands

```bash
git fetch origin && git checkout -B verify/v027-real-apps origin/release/v0.27.0
./scripts/build.sh
export PATH=$PWD/sky-out:$PATH            # in each scratch copy:
sky install
sky check src/Main.sky
rm -rf sky-out .skycache .skyapp .split && sky build --target web:app src/Main.sky
```

sky-lang.org, `[database] embedded = true`:

```bash
# seed once with the Sky.Live build (deploy/SPA-SSR-RUNBOOK.md, "Schema migrate + seed")
sky build --target web src/Main.sky
SKY_DATA_DIR=~/.cache/<dir>/pgdata ./.skyapp/web/sky-out/app --embed     # stopped after "[BOOT] ready"
cd .skyapp/web-app/.split/backend && PORT=8611 ./sky-out/app --embed      # same SKY_DATA_DIR
```

Env: `ENV=production`, `SKY_CONSOLE_AUTH=app` (then a second pass with
`token` + `SKY_CONSOLE_TOKEN`), `SKY_ADMIN_TOKEN`, a random
`SKYLANG_SESSION_SECRET`, `SKYLANG_ADMIN_GITHUB_LOGINS=localadmin`, and
`SKYLANG_DEV_MODE=1` so that `/admin/dev-login` signs in without GitHub.

darraghstudio, PostgreSQL through `sky db start` (own cluster, own socket):

```bash
SKY_POSTGRES_BIN=… sky db start
DATABASE_URL='postgresql:///postgres?host=/tmp/sky-<hash>' sky db seed
cd .skyapp/web-app/.split/backend && PORT=8612 ./sky-out/app
```

Env: `ENV=production`, `SKY_CONSOLE_AUTH=app`, `SKY_ADMIN_TOKEN`, a random
`SKY_AUTH_TOKEN_SECRET`, `DS_ADMIN_EMAILS=<two local test addresses>`,
`DS_EMAIL_DRY_RUN=1` (the verify link is read from the log; no mail is sent),
`DS_SITE_URL=https://darraghstudio.localhost:9443`. No Stripe keys.

Browser checks: `node harness.mjs <cfg> chrome|webkit <out.json>` with the
files in [`real-apps-behind-caddy/`](real-apps-behind-caddy/).

## Build and tests

| Check | sky-lang.org | darraghstudio |
|---|---|---|
| `sky install` | fetched `github.com/anzellai/sky-github` v0.1.0 | generated `sky-ffi/sqlite.*` |
| `sky check` | OK, 34 modules = 31 app + 3 from the registry package `sky-github` (v0.27.0 type-checks registry packages; the package passes) | OK, 19 modules |
| clean `sky build --target web:app` | OK, 53 s | OK, 40 s |
| build warnings | `withConfig` not carried into the client entry (unchanged from v0.26.1) | the same, plus the split notes |
| app tests | none (`tests/` is empty) | ShopTest 39/39, OrderTxnTest 5/5, CheckoutWebhookTest 7/7 (with `DS_STRIPE_WEBHOOK_SECRET=whsec_test DS_EMAIL_DRY_RUN=1`, as the test header says) |
| source changes for v0.27.0 | none | none |

## Browser checks

Every value below is from the JSON files in
[`real-apps-behind-caddy/`](real-apps-behind-caddy/). "C" is Chrome, "W" is
WebKit. The two browsers gave the same values in every row.

### Pages

| App | Pages | Status | wasm booted | Console errors | CSP violations | CSP header |
|---|---|---|---|---|---|---|
| sky-lang.org | `/`, `/blog`, `/blog/why-i-built-sky-lang`, `/admin` | 200 ×4 (C, W) | 4/4 (C, W) | 0 (C, W) | 0 (C, W) | none (this Caddyfile sends none) |
| darraghstudio | `/`, `/about`, `/products`, `/products/big-red-bus-sticker-pack`, `/basket`, `/signin`, `/signup`, `/suggest`, `/terms`, `/privacy` | 200 ×10 (C, W) | 10/10 (C, W) | 0 (C, W) | 0 (C, W) | none (this Caddyfile sends none) |

Page loads have zero console errors. The console errors in finding 1 come
from an interaction, not a page load.

### `/_rpc` origin guard

The target is `/_rpc/LoadPosts` (sky-lang.org) and `/_rpc/AddToBasket`
(darraghstudio). The explicit-header rows use Playwright's request API. The
two browser rows use the browser's own network stack.

| Request | sky-lang.org (C, W) | darraghstudio (C, W) |
|---|---|---|
| POST JSON, `Origin: https://evil.example`, `Sec-Fetch-Site: cross-site` | 403 `rpc_origin` | 403 `rpc_origin` |
| POST JSON, `Origin: https://other.localhost:9443`, `Sec-Fetch-Site: same-site` | 403 `rpc_origin` | 403 `rpc_origin` |
| POST JSON, `Origin: null` | 403 `rpc_origin` | 403 `rpc_origin` |
| POST `text/plain`, same origin | 403 `rpc_content_type` | 403 `rpc_content_type` |
| POST form-urlencoded, same origin | 403 `rpc_content_type` | 403 `rpc_content_type` |
| GET | 405 `rpc_method` | 405 `rpc_method` |
| POST JSON, same origin (control, partial body) | 400 decode error: passes the guard | 400 decode error: passes the guard |
| Browser: `fetch` POST JSON from the app page | 400: passes the guard | 400: passes the guard |
| Browser: `fetch` POST `text/plain` (`no-cors` and `cors`) from `https://evil.localhost:9443` | 403 | 403 |
| v0.26.1, POST `text/plain` from `Origin: https://evil.example` | 400: reached the handler (no guard) | not run |
| v0.26.1, GET | 400 (no method check) | not run |

### Cookies

| App | Cookie | Secure | HttpOnly | SameSite | When |
|---|---|---|---|---|---|
| both | `__sky_csrf` | yes | yes | Strict | first page load |
| sky-lang.org | (no `sky_sid`) | | | | none before or after admin sign-in: the admin session is a model field that no server branch writes, so the split signs no session cookie for it |
| darraghstudio | `sky_sid` | yes | yes | Lax | after e-mail verify and after password sign-in |
| both | `__Host-sky_console` | yes | yes | Strict | console sign-in |
| both | `__Host-sky_sky_console_sid` | yes | yes | Lax | the console is a Sky.Live sub-app named `sky_console`, so its session cookie is `__Host-sky_<name>_sid` over HTTPS |
| both | `__sky_csrf_sky_console` | yes | yes | Strict | console |

This matches the CHANGELOG. The Sky.Spa session cookie stays `sky_sid` and
gets `Secure` behind the TLS proxy. The Sky.Live session cookie (here the
console sub-app's) is `__Host-`-prefixed.

### Sign-in and session cookie (darraghstudio; sky-lang.org has none)

| Step | C | W |
|---|---|---|
| sign up (`/_rpc/DoSignUp`) | 200 | 200 |
| open the logged verify link (`/_rpc/RunVerify`) | 200, `sky_sid` set | 200, `sky_sid` set |
| sign out (`/_rpc/__spaSignOut`) | 200, `sky_sid` removed | 200, `sky_sid` removed |
| password sign in (`/_rpc/DoSignIn`) | 200, new `sky_sid` | 200, new `sky_sid` |
| value changed between the two sign-ins | yes | yes |
| SSR of `/account` with no cookie | signed out | signed out |
| SSR of `/account` with the current value | signed in | signed in |
| SSR of `/account` with the value from before sign-out | **signed in** | **signed in** |

The last row is finding 3. It is the design of the stateless Sky.Spa
session, not a v0.27.0 change.

### An `/_rpc` interaction through the UI

| App | Interaction | C | W |
|---|---|---|---|
| sky-lang.org | `/admin/dev-login` → Admin → "+ New post" → "Save draft" | `/_rpc/EditorSaveDraft` 200, flash shown, 0 console errors | same |
| darraghstudio | product page → "Add to basket" | `/_rpc/AddToBasket` 200, **basket stays empty**, 1 console error | same |

### Console

| App | Anonymous `/_sky/console/` | Anonymous `/_sky/console/api/overview` | With the local credential |
|---|---|---|---|
| sky-lang.org, `SKY_CONSOLE_AUTH=app` | 403 (C, W) | 403 (C, W) | 403 after admin sign-in (C, W): finding 4 |
| sky-lang.org, `SKY_CONSOLE_AUTH=token` | 401 (C, W) | 401 (C, W) | wrong token 401; right token 303 → `/_sky/console` 200 (C, W) |
| darraghstudio, `SKY_CONSOLE_AUTH=app` | 403 (C, W) | 403 (C, W) | 200 after an admin signs in (C, W) |

### Precompressed wasm and `/_sky/sub`

| App | Browser fetch of `main.<hash>.wasm` | `Accept-Encoding: br` | `gzip` | `identity` | `wasm_exec.js` |
|---|---|---|---|---|---|
| sky-lang.org | 200, `br`, `immutable` (C, W) | 200 `br`, 2,013,334 B | 200 `gzip`, 2,968,809 B | 200, no encoding | 200 `br` |
| darraghstudio | 200, `br`, `immutable` (C, W) | 200 `br`, 2,201,651 B | 200 `gzip`, 3,552,256 B | 200, no encoding (15,226,369 B) | 200 `br` |

Neither app names a push topic (`Sub.none` / `Sub.every` only), so the
generated backend mounts no `/_sky/sub`. That check does not apply.

## Findings

### 1. Sky regression: a server-internal follow-up Msg discards the RPC's model write (Sky.Spa)

Severity: release blocker. It breaks "Add to basket" in darraghstudio on
v0.27.0, in Chrome and WebKit. The same app built with v0.26.1 works. The
same `track` follow-up is on the sign-up, sign-in and suggest branches, and
the sign-in scenario logged the same console error twice. Of the RPCs in
that scenario, only sign-up and sign-in call `track`, so their writes take
the same path.

What happens. `AddToBasket` returns `Cmd.perform (Notify.event …) Tracked`.
`Tracked` is also reached from branches that settle a chain on the server,
so the split classes it as server-internal (build note: "Msg(s) [… Tracked]
are server-internal"). The backend still collects it as a follow-up, and
`spaEncodeFollow_` encodes it with its wildcard arm as `["", ""]`
(`rust/crates/project/src/spa_split.rs:1561`). The response carries
`"spaFollow_":"[[\"\",\"\"]]"`. The client decoder has no arm for `""` and
returns `Err "sky.spa: unknown follow-up Msg "`
(`spa_split.rs:1673`). The generated `Applied<Msg> (Ok resp)` arm then takes
its `Err e_ -> ( model, Spa.reportError e_ )` branch, so the whole write-set
of the RPC (the basket, the notice) is dropped. The console shows
`[sky.spa] a server branch's follow-up could not be applied: Unexpected:
sky.spa: unknown follow-up Msg ` (`runtime-go/rt/live_wasm.go:1015`).

Observed, same page and click:

| Build | `/_rpc/AddToBasket` | Response `spaFollow_` | Notice shown | Basket after reload | Console errors |
|---|---|---|---|---|---|
| v0.27.0 (`afd32383`) | 200 | `[["",""]]` | no | "Your basket is empty" | 1 |
| v0.26.1 | 200 | (no field) | yes | 1 item, £4.50 | 0 |

Minimal repro: [`real-apps-behind-caddy/repro-follow-up/`](real-apps-behind-caddy/repro-follow-up/).
`sky build src/Main.sky`, run the backend, click "bump": the server answers
`{"count":1,…,"spaFollow_":"[[\"\",\"\"]]"}` and the page still shows
`count=0`, with the same console error. Without the `Save` / `Saved`
branches, `Tracked` is not server-internal and the generated encoder has a
`Tracked` arm (seen in the first build of the repro).

### 2. Stale runbook note (app documentation, not Sky)

`deploy/SPA-SSR-RUNBOOK.md` in sky-lang.org says the split backend does not
run the app's bootstrap. On v0.27.0 it does: the backend logs
`[SEED] content/posts not readable … skipping seed` and `[BOOT] sky-lang.org
ready`. The seed then finds no `content/` beside `backend/`. The schema and
the posts from the one-off Sky.Live run were in place, so the site served
them.

### 3. A Sky.Spa session cookie stays valid after sign-out (design, not a regression)

The split backend signs the session into a 30-day `sky_sid` token and keeps
no server state. Sign-out removes the cookie from the browser, but a copy of
the old value still authenticates (the SSR seed carries the signed-in user).
The generated code is the same on v0.26.1 (`Auth.signToken … 2592000`, and
sign-out only clears the cookie). The v0.27.0 CHANGELOG makes the rotation
and revocation claims for Sky.Live only, so this is not a broken claim. It
is recorded because the Sky.Live fixes in this release do not reach it.

### 4. sky-lang.org console in `app` mode refuses its own admin (app gap)

`Auth.Console.consoleAdmin` reads the app's own `sky_sid` JWT cookie, and no
code path of the app sets that cookie (the sign-in hands the session over
through `?sso=` into the model). So in `SKY_CONSOLE_AUTH=app` mode every
request gets 403, the admin too. The Sky side works: anonymous is refused,
and `token` mode opens the console with the local token.

### 5. Reported to the owner, not detailed here

sky-lang.org has one more app-level authorisation defect in its admin RPCs.
It is present on v0.26.1 too, so it is not a v0.27.0 regression. The
details went to the owner directly.

## What was not tested locally, and why

- Real GitHub OAuth (sky-lang.org) and Stripe checkout (darraghstudio): they
  need third-party accounts and paid or external APIs. The dev sign-in and
  the offline webhook test cover the paths that can run locally.
- `/_sky/sub`: neither app has a push topic.
- Session-id rotation in Sky.Live proper (the `X-Sky-Status:
  session-rotating` grace window): neither app is a Sky.Live app. Only the
  console sub-app uses Sky.Live here, and its sign-in is the token or app
  gate, not `Live.bindSessionUser`.
- Port 443: another local Caddy holds it, so the origin carries `:9443`.
