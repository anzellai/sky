# Sky — Road to v1.0

> **Status:** living document. Sky's compiler is now the Rust implementation
> under [`rust/`](rust/); the Haskell compiler is preserved under
> `legacy-haskell-compiler/` as a differential oracle. This roadmap describes
> what "v1.0" means and the work between here and there. It covers the
> compiler (correctness + performance), the developer experience, the standard
> library, production app usage, and **SkyDeploy** — the companion platform for
> shipping and operating Sky apps.

---

## 1. Why Sky exists

Sky is a small, Elm-family, pure-functional language that compiles to typed Go.
The thesis is narrow and opinionated:

1. **Easy to learn.** One small, consistent surface (Elm-shaped syntax,
   Hindley–Milner types, exhaustive `case`, no runtime exceptions). A developer
   productive in a week, not a quarter.
2. **AI-native, not just AI-friendly.** Deterministic compilation, "if it
   compiles, it works," machine-readable diagnostics, and no hidden runtime mean
   an LLM can write, refactor, and verify Sky with a tight, reliable loop. The
   language is designed so an agent's output is *checkable*, not just plausible.
3. **The stdlib is the moat.** Every recurring production/enterprise pain point —
   auth, DB access, money, background jobs, caching, email, observability,
   rate-limiting, sessions, CSV/config, compression, websockets — is a reviewed,
   secure, scalable **standard-library or built-in** feature. Teams stop
   re-litigating these per project; the "correct, secure, scalable" default is
   the *only* default.
4. **One language, every app shape.** Web UI, JSON API, CLI, cron worker,
   terminal UI, desktop — the same `init/update/view` model and the same stdlib,
   all compiling to a single static Go binary.
5. **Deploy is part of the language story.** SkyDeploy turns "I wrote an app"
   into "it's running, observable, and scalable" in minutes — no YAML
   archaeology, no per-project infra bikeshedding.

**v1.0 is the point where all five of these are *true in production*, not just
in demos.**

---

## 2. Where we are today (honest snapshot)

**Compiler (Rust, primary):**

- Pipeline: Parse → Canonicalise/Resolve → Type (HM inference) → Lower → Emit Go.
- Incremental by construction (salsa query DAG; no global mutable state).
- **~93–95% oracle parity** on the type checker; **48/48** non-FFI examples
  build + run; runtime output byte-verified against committed goldens.
- Full CLI (`build/run/check/fmt/test/lsp/doc/watch/add/remove/db/console/…`),
  an LSP at or ahead of the previous implementation, an opinionated formatter,
  and an `xtask` gate suite (roundtrip / resolve / infer / reject / fuzz /
  coerce-floor / repro / build-run / golden) run in cross-platform CI.
- FFI: the Rust compiler generates FFI surfaces via the same embedded Go
  introspector the oracle used (proven to the Stripe-SDK scale — 76k symbols).

**Standard library (Layer 3 — every kernel surfaced as Sky source):** Core
(String/List/Dict/Set/Maybe/Result/Math/Regex/Crypto/JSON/…), Task/Cmd/Sub
effect model, `Std.Db` (SQLite + Postgres, typed params, migrations),
`Std.Auth` (bcrypt + JWT), `Std.Money`/`Std.Decimal`, `Std.Ui` (typed no-CSS
DSL) with Sky.Live / Sky.Tui / Sky.Webview backends, `Sky.Http.Server`,
observability (`Std.Log`/`Std.Trace` + `/_sky/console`), plus `Std.Cache`,
`Std.Email`, `Std.Compression`, `Std.Csv`, `Std.Config`, WebSocket, and more.

**SkyDeploy (WIP / MVP):** a control plane with SSO, one-target deploy, an
in-dashboard editor with `sky fmt`/`check`/LSP, and console/telemetry
federation. Enough to deploy and watch an app; not yet the full "operate at
scale" surface.

**The gap to v1 is real but bounded** — it is *finishing* and *hardening*, not
*inventing*.

---

## 3. What "v1.0" means — the bar

v1.0 ships when all of the following hold, each backed by an automated gate:

| Promise | v1.0 acceptance criterion |
|---|---|
| **If it compiles, it works** | `sky check` ≡ `go build`; no well-typed program panics at runtime (three-leg soundness: runtime classification + emission-time gate + real-world/fuzz). |
| **Deterministic** | Byte-identical Go across processes/platforms (repro gate); same input → same output, always. |
| **Fast enough to disappear** | Warm incremental rebuild < 1 s on a large project; emitted-Go runtime within a small constant of hand-written Go on the hot paths. |
| **The stdlib covers the pain points** | A production SaaS can be built end-to-end from the stdlib with no user-written FFI and no "roll your own auth/db/jobs/secrets." |
| **Great errors, everywhere** | Every diagnostic is Elm-quality (source span + caret + fix-it) and machine-readable for agents. |
| **Deploy is trivial** | `sky` app → running, observable, scalable on SkyDeploy in one flow, with rollbacks + secrets + a DB. |

---

## 4. Pillars & workstreams

The work is organised into six pillars. Each names concrete, gated deliverables.
(Tier labels below reference the internal v1 gap + reliability analyses.)

### Pillar 1 — Correctness: the "if it compiles, it works" floor

*The non-negotiable core. Every item lands with a regression test that is the
discovery artefact.*

- **Close the last oracle-parity gaps (~5–7%).** Drive the checker to full
  accept/reject parity on the corpus; each closed gap becomes a `reject`/`infer`
  gate lock. *(Tier 1 — in progress; wildcard-`any` result soundness, field
  order, row-poly call results, func-field records already closed.)*
- **Typed-Go ceiling.** Continue narrowing `rt.Coerce`/`rt.As*` toward the
  §8 irreducible floor (FFI return, wire decode, TEA dispatch). Typed tuples
  (P0/P1) done; extend to the remaining element positions.
- **No-panic guarantee.** Maintain the three-leg soundness stool
  (`panic_recover` runtime tests + emission-time panic-class gates + example
  sweep / fuzzer). Every reachable-from-Sky panic site stays classified and
  net-caught.
- **A floor-lock gate for coercions.** *(Reliability B1)* Add an
  emission-time per-example `rt.Coerce`/`rt.As*` count golden that **fails on
  increase**, so future codegen work can never silently widen the runtime-cast
  floor.
- **Fuzz + differential + golden as standing gates.** Keep the well-typed
  fuzzer (no-panic + determinism), the differential oracle (during the
  transition), and the runtime-correctness golden suite green on every push.

### Pillar 2 — Performance

*Fast enough that the compiler and the runtime both "disappear."*

**Compiler:**
- Incremental salsa granularity (per-def `sig`/`infer` queries) so a one-line
  body edit recomputes only that def. *(Done — keep it gated; extend coverage.)*
- **A4 — coercion codegen perf.** Reduce the hot coercion sites (currently far
  above the floor) so large projects lower quickly; prerequisite for the B1
  floor-lock. *(Reliability A-tier.)*
- Warm-rebuild benchmark in CI with a regression budget; target sub-second on a
  multi-module app.

**Runtime (emitted Go):**
- Keep the typed-dispatch + auto-TCO + bounded-allocation work (all 13 core list
  ops on constant stack; `SkyTuple2` fast-path; typed record generics).
- **A runtime benchmark suite** — measure the emitted Go against hand-written Go
  on representative hot paths (TEA dispatch, JSON decode, DB row → record, list
  pipelines) with a published budget and CI tracking.
- Prove the scale ceiling holds: Stripe-SDK-scale FFI + a large multi-module
  app stay within compile-time + binary-size budgets.

### Pillar 3 — Developer experience & AI-native tooling

*Sky's second mandate: DX-first. And the differentiator: an agent can drive it.*

- **Elm-quality errors everywhere** — source span + caret + fix-it on every
  diagnostic class; no bare `undefined: X` from a downstream `go build`.
- **LSP to full parity + beyond** — hover, completion, goto-def, references,
  rename, code actions; the 17/17 Neovim integration suite stays green and grows.
- **`sky` as the one tool** — `init/build/run/check/fmt/test/watch/doc/db/
  add/remove/upgrade/doctor` cover the whole lifecycle with predictable output.
- **AI-native surface (the wedge):**
  - Deterministic, machine-readable diagnostics (JSON mode) an agent can parse
    and act on.
  - Stable templates + `CLAUDE.md` scaffolding shipped by `sky init`, so an
    agent starts every project on the paved path (Std.Ui + Std.Auth + Std.Db).
  - A tight "generate → `sky check` → fix" loop where the type checker is the
    verifier — the property that makes AI-written Sky *trustworthy*, not just
    fast.
  - MCP / agent hooks for building and operating apps on SkyDeploy.

### Pillar 4 — The stdlib as the moat: pain points → built-in

*The strategic bet. Enumerate the arguments teams have on every project and
delete them by making the correct answer the standard-library answer.*

**Already shipped (v1 baseline):** auth (bcrypt + JWT), DB (SQLite/Postgres +
typed params + migrations), money/decimal, background-ish tasks (`Task`/`Cmd`),
cache (LRU + TTL), email (multi-provider), CSV/config, compression, websockets,
rate-limit, CORS/CSRF/logging middleware, structured logging + tracing +
metrics, sessions (memory/sqlite/redis/postgres/firestore), typed UI.

**Close for v1 (the pain points still worth owning):**
- **Background jobs / scheduling** — a first-class durable job queue + cron
  surface (beyond in-process `Task.parallel`), with retries + visibility.
- **Multi-tenancy** — tenant scoping as a stdlib concern (the SQL-WHERE gate
  exists at the runtime layer; surface it as a typed, hard-to-misuse API).
- **Secrets & config management** — typed secret handling end-to-end (never
  `fmt.Sprintf("%v", secret)`), pluggable providers, rotation.
- **Blob / file storage** — a provider-agnostic object-store API (local / S3 /
  GCS) mirroring the `Std.Db`/`Std.Email` provider pattern.
- **Payments** — a typed payments surface (the Stripe FFI works today; wrap the
  common flows so apps don't hand-roll them).
- **Search & full-text** — a stdlib query surface over SQLite FTS / Postgres.
- **i18n / localisation** — typed message catalogs (the Sky.Live status strings
  pattern generalised).
- **Feature flags & config-driven behaviour** — typed flag evaluation.
- **Email/notification + webhooks** — inbound webhook verification + outbound
  delivery as reviewed defaults.

Each new stdlib module ships the v0.15.46 convention (`default*` constructor +
`with*` builder per field), is reviewed for security + scalability, and is
documented so an AI writes it correctly the first time.

### Pillar 5 — Production apps usage

*The proof: real apps that survive a restart, scale horizontally, refuse
cross-tenant reads, and emit traceable logs — all from the stdlib defaults.*

- **The app-shape matrix as a guarantee**, not advice: Sky.Live (web), Sky.Http
  (API), Sky.Cli (jobs/CLI), Sky.Tui (terminal), Sky.Webview (desktop) — each
  with a hardened, documented production path.
- **Production-grade defaults surfaced and enforced** — `ENV=production` gates
  (console/banner/metrics), `SKY_AUTH_TOKEN_SECRET` length, non-memory session
  store for multi-replica, tenant SQL enforcement.
- **A reference-application suite** — small, open, canonical apps (a SaaS
  dashboard, an API service, a job worker, a real-time app) that double as
  end-to-end tests *and* as the templates `sky init` and agents start from.
- **Runtime verification on every release** — Playwright web flows + CLI/TUI
  verification, so "the click is a no-op" and "the deploy paints a permanent
  error banner" regressions are caught, not shipped.

### Pillar 6 — SkyDeploy (MVP → GA)

*The companion platform. Sky's promise is incomplete until shipping is as easy
as writing.*

**MVP (WIP today):** control plane with SSO, deploy to a managed target,
in-dashboard editor with `sky fmt`/`check`/LSP autocomplete, and
console/telemetry federation for a deployed app.

**Road to GA (what "manage deployed apps very easily" requires):**
- **One-flow deploy** from repo or dashboard → build → run, with health checks
  and instant rollback.
- **Managed data + secrets** — provision a database, manage env/secrets with
  rotation, wire them into the app without leaving the platform.
- **Scale controls** — autoscaling, multiple replicas with a shared session
  store by default, region selection.
- **Custom domains + TLS**, zero-config.
- **Observability built in** — logs, metrics, traces, and the Sky Console for
  every deployed app; alerting on the common failure modes.
- **Tenant isolation + cost visibility** for multi-tenant SaaS built on Sky.
- **Agent-assisted operation** — an agent can scaffold, deploy, inspect logs,
  and remediate through the same MCP surface a developer uses.

The version pairing is a release rule: every tagged Sky compiler release is
matched by a SkyDeploy redeploy of the same version, so the platform never lags
the language.

---

## 5. Sequencing (v0.18 → v1.0)

Rough ordering; each phase is gated and shippable in isolation.

1. **v0.18 — Correctness & perf floor.** Close the remaining oracle-parity gaps;
   land the coercion floor-lock gate (B1) after the coercion-perf reduction
   (A4); stand up the compiler warm-rebuild + runtime benchmark suites. *Bar:
   full accept/reject parity on the corpus; no-panic soundness re-verified;
   perf budgets in CI.*
2. **v0.19 — Stdlib pain-point sweep, wave 1.** Durable jobs, secrets/config,
   blob storage. Each with reviewed security + scalability + docs + templates.
3. **v0.20 — Stdlib wave 2 + DX.** Multi-tenancy API, payments, search, i18n,
   feature flags; JSON diagnostics + agent hooks; reference-app suite lands.
4. **v0.21 — SkyDeploy MVP → beta.** One-flow deploy, managed DB + secrets,
   rollback, custom domains, built-in observability for deployed apps.
5. **v1.0 — Hardening + the promise.** All six acceptance criteria green under
   automated gates; reference apps run in production on SkyDeploy; docs +
   templates complete; the differential oracle is retired once parity is total.

---

## 6. What v1.0 is deliberately NOT

Scope discipline is a feature. v1.0 does **not** add:

- Higher-kinded types, typeclasses, or custom operators (HM stays simple —
  predictability beats expressiveness here).
- A macro system or user-written FFI (the FFI generator + stdlib own the
  unsafe boundary).
- A second syntax or a second effect model.

These are not oversights; keeping the surface small is what makes Sky learnable
and AI-checkable. Additive `where`-clause ergonomics and similar quality-of-life
items may land, but the core stays boring on purpose.

---

## 7. How we get there (the process)

- **Differential oracle** during the transition — the Haskell compiler under
  `legacy-haskell-compiler/` is the ground truth until Rust parity is total.
- **Gates over vibes** — every claim is an automated gate (parity, repro, fuzz,
  golden, coverage, perf budget). "It works on my machine" is not a milestone.
- **Every bug enters the pipeline** — spotted is filed; the fix, not a
  workaround, closes it.
- **Reference apps as tests** — the canonical apps are both the demo and the
  regression suite.
- **AI in the loop** — the same agent workflow Sky is designed for is used to
  build Sky: generate, `sky check`, verify, adversarially review.

---

*This roadmap is intentionally ambitious and intentionally bounded. Sky wins by
making the correct, secure, scalable way to build and ship an app the **easy,
default, only** way — for humans and agents alike.*
