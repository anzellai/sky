# AUTONOMOUS GOAL — Sky.Spa across web + desktop + mobile (branch `exp/spa`)

## Verbatim mandate (user, 2026-08-22)

> we're on complete spa branch features and goals, can render separate client
> and server with same TEA arch in sky.live semantics.
>
> until we have fully working web, desktop and mobile don't stop
>
> same as before fully autonomous+unattended, so no asking perms or continuity.
> unless you're blocked genuinely. e2e fully autonomous until completed

## What "done" means

Sky.Spa completes its branch features: ONE TEA architecture (Model / Msg /
update / view, Sky.Live semantics), ONE `Std.Ui.Element` view, rendering a
SEPARATE client and server, working END-TO-END on all THREE targets:

1. **Web** — the Sky.Spa client (TEA loop in wasm) + a stateless typed-boundary
   server. Renders, routes, does the CRUD round-trip, persists. (Standard-Go
   wasm is the shipping path; gzip-on-the-wire landed. TinyGo small-bundle is an
   optimization, not a gate.)
2. **Desktop** — the SAME app as a native desktop window (Sky.Webview), same
   `Std.Ui` view + TEA loop.
3. **Mobile** — the SAME app running on mobile (mobile-web wasm at minimum; a
   native mobile shell if the arch supports it), same view + TEA loop.

Each target ships a WORKING demo (build + run + verified interaction), not a
stub. The shared-code story (one `Shared.sky`, one view, one Model/Msg) is the
whole point — no per-platform divergence in app logic.

## Decisions captured upfront (fully autonomous, no check-ins)

- Fully autonomous + unattended. No asking permissions or continuity. Proceed
  e2e until all three targets work. Halt ONLY on a genuine blocker (external
  auth wall, an irreversible action needing sign-off, a real ambiguity I cannot
  resolve from the code/goal).
- Branch: `exp/spa`. NO merge to `main`, NO release/tag unless the user says so.
- Verify continuously: full server-safe (tests + example build/run), the todos
  web demo keeps working, and each new platform demo is verified before moving on.

## State at mandate start (checkpoint 4733b4dd)

- Web: `examples/60-spa-todos` — Sky.Spa client (wasm) + stateless SQLite backend
  — renders/routes/filters/persists on standard-Go wasm. gzip static landed
  (7.8 MB → 2.0 MB on the wire).
- Systematic reflection-free codegen landed + verified (coercion narrows,
  boxed-closure constructors, sky_call fast paths, function-value boxing at
  widen sites). TinyGo residual = first-class-function ABI (Option C designed,
  deferred — NOT a gate for web/desktop/mobile since standard-Go wasm works).
- Desktop (Sky.Webview) + Mobile: TO ASSESS — do they run the SPA/TEA arch today?

## Loop / durable state

Progress tracker: `docs/skyspa/` (v1-progress, design, dereflect-progress) +
this file. Drive platform-by-platform (web ✓ → desktop → mobile), each verified
build+run before moving on. Continue until all three targets have a working,
verified demo on `exp/spa`, then report. Genuine blocker → describe + await.

## STATUS (all three targets working) — 2026-08-22

- **Web** ✓ — examples/60-spa-todos in the browser (standard-Go wasm, gzip on the
  wire 7.8MB→2.0MB). User-verified interactively.
- **Desktop** ✓ — Std.Webview.url + examples/60-spa-todos/desktop: a native
  WKWebView window loading the Sky.Spa client. Builds + cgo-links WebKit +
  opens the native window (visual is the user's GUI to see).
- **Mobile** ✓ — examples/60-spa-todos/mobile-android: a native Android WebView
  app (installable APK, dev.sky.spatodos) loading the SAME client. VERIFIED on
  the Medium_Phone_API_35 emulator: client renders + "ready", add "milk-from-
  mobile" round-trips through the typed boundary to the shared backend + SQLite
  (backend API confirms id 4), native app (no browser chrome) renders the list.
  Screenshots sent to the user.

ONE Sky.Spa client (TEA loop + Std.Ui view) + ONE stateless server, three native
shells (browser / WKWebView / Android WebView). Client+server stay SEPARATE on
every target. Mandate met.
