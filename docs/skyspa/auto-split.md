# Sky.Spa — the auto-split: `Task`-type tracing + the effects-via-`Cmd` dialect

> **Status:** design (v2 target). This is the corrected, *stronger* mechanism for
> the compiler-derived client/server partition — the one the measurement in
> [design.md §0.1](design.md) did **not** evaluate. That measurement falsified a
> *weak* mechanism (classify a branch by its returned `Cmd`); this document
> specifies the mechanism that survives it (trace `Task` in the branch **body**),
> the one obstacle it hits (inline effect *interleaving*), and the dialect that
> removes the obstacle. Grounded in real Sky surfaces (file:line).
>
> **Front door vs. mechanism.** The user-facing entry is
> [`Std.App`](../skyapp/overview.md): write an `App.app` and build it with a
> client `--target` (`web:app` / `mobile:*` / `tablet:*`). The `Spa.app` /
> `Spa.config` / `Spa.postJson` / `import Std.Spa` names below are the low-level
> `Std.Spa` runtime **and the generator internals** that `Std.App` drives — how
> the split is computed and emitted, not surfaces user code writes.

## 1. Goal

Auto-derive the client/server split of a TEA app: pure UI transitions run on the
client (zero round-trip), server effects become **generated** RPCs — **no
hand-written API routes**. The property to earn: *if it compiles, the split is
sound.* This is the "why do we need API routes — the AST already knows which
`update` is effectful?" idea, made real.

## 2. Why the weak mechanism failed, and the right one

Sky's effect boundary is **in the type system**: every effect is `Task Error a`
(`sky-stdlib/Sky/Core/Task.sky`), distinct from a pure `a`. So the information is
there. The falsified mechanism looked in the wrong place:

- **Weak (falsified):** classify a branch by its returned `Cmd` (`cmdT.kind`,
  `runtime-go/rt/live.go:1823`). Blind, because Sky discharges the `Task` *before*
  it reaches the `Cmd`. `Task.run`/the `let _ =` auto-force has type
  `Task Error a → a` — it **executes** the task and yields a plain `a`
  (`AGENTS.md`: "`let _ = someTask` auto-forces the task"; "`db = Task.run (…)`").
  So in `examples/13-skyshop/src/Main.sky:248,251,295`, `refreshProducts` reads
  the DB inline and the branch returns `Cmd.none` — the model field and the `Cmd`
  are both pure-typed. A `Cmd`-keyed classifier sees 98% "pure" and ships the DB
  read to the client. Falsified by measurement (`spike/spa_classify.py`, 47%
  ceiling).

- **Strong (this design):** trace `Task` in the branch **body**. The `Task` type
  is still fully visible — at the **run-site argument** (the thing passed to
  `Task.run`/forced), not at the model field. Walk each `update` branch's typed
  HIR + call graph; a branch is **effectful** iff its dataflow reaches a
  `Task`-producing kernel or a `Task.run`/force site, transitively through the
  functions it calls. This catches exactly the inline effects the weak mechanism
  missed.

**Decidability.** Pure-vs-effectful per branch is decidable in practice: it is a
static call-graph walk over the typed HIR, and `Task` appears in the type wherever
a task value flows. The one unsound corner — an effectful function *stored in the
Model* and invoked dynamically — is **conservatively rejected** (a compile error),
never guessed. Soundness direction is fixed: the analysis may over-approximate
toward *server* (a needless RPC) but must **never** classify a server effect as
client (which would leak the DB/secret to the browser).

## 3. Client vs server — classify by the producing kernel

"Any `Task` ⇒ backend" is one step too coarse: some `Task`s run **client-side**.
The refinement is a fixed table over the effect kernels:

| Kernel family | Target | Why |
|---|---|---|
| `Db.*`, `Std.Db`, `File.*`, `Auth` sign/verify (secret), server `Http` to own backend | **server** | needs the DB / secrets / server identity |
| `Time.*`, `Random`/`Uuid`, external `Http`, browser storage, navigation | **client** | runs in the client runtime |

A branch's effect target is the join of the kernels its body reaches. A branch
reaching **both** a server and a client kernel is a *mixed* branch (§6).

## 4. The one real obstacle: interleaving (and the two ways out)

Detecting the effect is easy (§2). The obstacle is that today's apps **execute**
the effect *inline*, weaving it into the pure model computation:

```elm
-- interleaved: effect sits in the MIDDLE of pure client work
SetSearch q ->
    ( { model | data = rank q (Task.run Products.listProducts) model.filters }
    , Cmd.none )
```

Detecting the `Task.run` here is trivial. Running this branch on a *stateless
client* is not: the effect is bracketed by pure client work (`rank … model.filters`),
so you'd have to **split the branch at each run-site** into
`[client-before] → [server RPC] → [client-after]` — a continuation/CPS transform.
That restructuring is the real work; "effects hide inline" precisely means
"effects are *interleaved* with pure logic."

Two ways out:

- **Option A — mandate the dialect (recommended; §5).** Forbid inline `Task.run`
  in `update`; effects return as `Cmd`s. Then the branch body is pure, the effect
  is a tail `Cmd`, and §2's trace yields the partition with **no transform**.
- **Option B — support the inline idiom (optional, later).** Implement the CPS
  branch-split. Real compiler work; not required for v2; kept only if we ever want
  to auto-migrate existing inline-style apps.

## 5. The effects-via-`Cmd` dialect — the v2 contract

Two rules. Together they make the auto-split sound *and* cheap.

**Rule 1 — `Model = { ui, data }`.** `ui` = client-owned, ephemeral, no `Std.Codec`
(never crosses the wire). `data` = a cached projection of server truth, has a
`Codec`. "Has a codec ⇒ server-backed" is the boundary.

- *Buys:* write-sets become **coarse but decidable** — the analysis needs only
  "did this branch touch `ui`, `data`, or both?", not field precision (which the
  grill showed dies at row-poly record-update / helper delegation). And a pure
  branch writing `data` (an optimistic update) becomes **syntactically visible**.

**Rule 2 — effects only through `Cmd`/`Task`-return; never inline `Task.run`/force
inside `update` or its transitive helpers.**

- This is just **the Elm discipline**: `update` is pure, effects are `Cmd`s. It is
  not alien to Sky — `examples/52-blog-analytics` (`Tip → Cmd.perform (Analytics.track …)`)
  and `examples/18-job-queue` (`LoadHistory → Cmd.perform loadHistory HistoryLoaded`)
  already write this way, and the measurement flagged exactly those as the clean
  server branches. Sky *added* inline `Task.run` as a convenience because Sky.Live
  runs `update` server-side; the dialect simply declines that convenience.
- *Buys:* no interleaving (§4 obstacle gone); the effect is visible in the type at
  the branch tail; §2's trace is clean and complete.

**Enforcement is a compile gate (very Sky — "if it compiles it works").** Both
rules are decidable checks with clear errors:

- Rule 2 gate: no `Task.run` / auto-force (`let _ = <Task-typed>`) site in the
  transitive body of `update`. Detectable in HIR.
- Rule 1 gate: `Model` is `{ ui, data }`-shaped; `ui` fields carry no `Codec`;
  `data` fields do.

A Spa app that violates either fails to compile with a message pointing at the
inline effect or the mis-placed field — it never silently mis-partitions.

## 6. The partition, mechanically (once the dialect holds)

Per `update` branch, the compiler now knows: **effect target** (§2 + §3) and
**write-set at `ui`/`data` granularity** (Rule 1). It derives:

- **pure + writes only `ui`** → client-local; zero round-trip.
- **client-effect** → runs in the client effect interpreter (`interpretCmd` over
  the same `cmdT`, `live.go:1823`).
- **server-effect** → a **generated RPC**: the client sends the `Msg` (+ any `ui`
  inputs the effect needs); the server runs the effect and the `data`-producing
  continuation; returns the **`data` delta**; the client applies it.
- **mixed** (server + client, or a `Cmd.batch` crossing the boundary) → the batch
  is split by target; if a single indivisible effect is genuinely both, it is
  rejected with a message (rare — the measurement found 2/111).

**The split-conflict / soundness check.** A `data` field written by *both* a
client-pure branch (optimistic) *and* a server branch is a conflict. The compiler
either rejects it or requires a **declared reconciliation policy** (§8). This is
the check that makes "if it compiles, the split is sound" literally true.

## 7. Security — untrusted client stays first-class

Auto-generation gives *plumbing*, never *trust*. The generated server endpoint:

- **ignores client-sent `data`/inputs for anything authoritative** — it re-reads
  the authoritative value from the DB (a client can lie about any field it sends);
- **requires a typed authorization combinator** on any `Db`/secret-reaching branch
  — the compiler generates the RPC but *fails the build* if a server-effect branch
  reaches `Db`/secrets without passing through the authz combinator. The trust rule
  is author-declared and compiler-**required**, not prose.

Sky's typed secrets (`Auth.signToken` takes `String`, never `any`), `Std.Auth`,
and the prod gate carry over unchanged.

## 8. The honest residuals (not detection — that's solved)

- **Optimistic writes to `data`.** A pure branch appending to `data.comments`
  before the server confirms is idiomatic and the disjointness rule forbids it
  outright. Resolution: allow it *with* **per-field versioning / optimistic-concurrency
  tokens** and a typed `Conflict` variant surfaced to the author — the split makes
  it visible; reconciliation is explicit, not "trivial."
- **Concurrent `data`-vs-`data` writes** (two in-flight server effects on one
  field) need the same per-field versioning to avoid lost updates. Independent of
  the split.
- **The CPS transform** (Option B, §4) — only if we ever support the inline idiom
  instead of mandating Rule 2. Not on the v2 path.

## 9. What is decidable vs what needs design (summary)

| Question | Status |
|---|---|
| Is a branch effectful? (body `Task`-trace) | ✅ decidable (conservative reject of dynamic-effect-value) |
| Client vs server effect? | ✅ decidable (kernel table, §3) |
| Write-set at `ui`/`data` granularity? | ✅ decidable **given Rule 1** |
| Dialect enforcement (Rules 1 & 2)? | ✅ decidable compile gate |
| Generate the server-effect RPC + `data` delta? | ✅ mechanical **given the dialect** |
| Effect interleaving (inline `Task.run`)? | ⚠️ needs CPS transform — **avoided by Rule 2** |
| Reconcile concurrent/optimistic `data` writes? | ⚠️ needs per-field versioning design (§8) |
| Enforce untrusted-client authz? | ⚠️ author-declared + required compile gate (§7) |

## 10. Staging — this is v2; v1 is forward-compatible

- **v1 (explicit boundary):** the author declares server calls (explicit `Http`,
  shared `Codec`), client owns `ui`. Buildable now; the runtime-partition +
  client renderer prototype (design.md §8) proves it.
- **v2 (this document):** the `{ui,data}` + effects-via-`Cmd` dialect + the
  body-`Task`-trace auto-split. **v1 apps written in the dialect are forward-compatible**
  — the dialect is a superset discipline, so adopting the auto-split later is
  additive, not a rewrite.

The auto-split is therefore **not struck** — it is **reachable via body-level
`Task`-type tracing once effects are mandated into `Cmd`.** The measurement priced
it (a dialect); this document is the mechanism that spends that price soundly.

## 11. Architecture-consult (2026-08-22): the design vs the real Rust compiler

A fresh-context consult mapped every §2–§9 claim onto the actual `hir` / `ty` /
`lower` / `project` crates (file:line). The **effect-detection** half holds; the
**Rule 1 codec-boundary** half does **not**; the **codegen** half hits a
structural wall. Corrections, so a future session does not re-derive them:

**Holds — effect detection is decidable against real structures:**
- **Identify `update`** — `Spa.config` is a top-level `Def` (`Std/Spa.sky:129`),
  so its call is `Expr::Call(Var(Res::Def(config)), [Record …])`; the `update`
  field value is `Var(Res::Def(update))`, statically resolvable
  (`hir/src/hir.rs:24-38`). Reject non-name shapes (inline lambda / partial app)
  conservatively.
- **`Task`-trace** — per-expression solved types are in **`BodyTypes.exprs :
  HashMap<ExprId, Ty>`** (`ty/check.rs:60-71`), and `lower::expr_is_task`
  (`lower/src/lower.rs:2182-2196`) is the **reference implementation** to lift
  into shared analysis (do not re-implement — `feedback_reuse_dont_parallel`).
  The auto-force site is a `let _ = <task>` empty-binder `LocalDef`
  (`lower/src/lower.rs:2708-2711`). A call graph does **not** exist — it is a
  greenfield arena walk over `resolve(module).bodies` (`hir/src/resolve.rs:135`),
  bounded but real (medium).
- **Kernel classification (§3)** — key off `Res::Kernel.module`; the tables exist
  (`hir/src/kernel.rs:29-117`, `lower/src/kernel.rs:104-640`). Two soft edges:
  `Http` is one pseudo-module (cannot split own-backend vs external — default
  **server** for soundness), and `Auth.*` is all one module (all correctly
  server).

**Does NOT hold — Rule 1's "has a codec ⇒ server-backed" is not decidable:**
- A `Codec a` is an ordinary **runtime value**, not a type-class instance:
  `Codec.auto : a -> Codec a` is reflection-driven and works for essentially any
  type (`Std/Codec.sky:250`), and there is no codec-derivability predicate or
  type-keyed registry. So a type cannot be asked "do you have a codec." The
  `{ui,data}` **structural** check is fine (the Model is already detected as a
  closed record, `lower/src/lower.rs:306-346`), but the *boundary* must be
  **nominal/syntactic** — e.g. `data`-field types declared in a designated
  `Shared` module, or an explicit marker — not "has a codec." **This is the open
  design fork to settle before Phase 1 codes the Model-shape gate.** (The coarse
  `ui`/`data` write-set benefit is separable and survives; keep the check coarse
  — per-field write-sets would hit the row-poly `any`-update floor, doc 14.)

**Structural wall — the codegen half (§6) is not a small extension:**
- The build emits exactly **one** artifact, native binary *or* `main.wasm`
  (`project/src/build.rs:57-63,686`). Auto-split needs **dual-target emission**
  (one source → wasm client + native stateless server) — a new build-driver
  capability that does not exist.
- There is **no endpoint-generation facility**: `Server.api`
  (`runtime-go/rt/rt_server.go:402`) registers a route from user code at runtime;
  `Spa.getJson/postJson` are the client half only. Synthesizing the matching
  server handler + shared-codec wiring + the §7 authz gate is greenfield.

**Where a Phase-1 gate lives:** model on `ty::check_modules`'s `[E2008]`
precedent (`ty/src/check.rs:459-522`) — a post-inference, pre-lowering pass
emitting typed `Diagnostic`s; wire it **whole-program** between
`project/src/build.rs:319` (`ty::check_modules`) and `:442` (lowering).

**Opt-in:** a new `Spa.autoApp` kernel sibling of `Spa.app`/`config`
(`Std/Spa.sky:129-137`); the gate fires iff the entry calls it (same
callee-DefId match as identifying `update`). This avoids wrongly gating existing
v1 inline-`Task.run` apps.

**Revised phasing (grounded):**
1. **Phase 1 — opt-in dialect-conformance gate** (Rules 1&2, no codegen).
   SMALL–MEDIUM, floor-free. Blocked only on settling the Rule 1 mechanism
   (above). Ships standalone value: "your app is auto-split-ready / here is the
   inline effect that isn't."
2. **Phase 2 — partition report** (`sky` sub-command emitting the derived
   per-branch client/server/mixed split, still no codegen). Cheap given Phase 1;
   de-risks classification.
3. **Phase 3 — the RPC-generating auto-split** (Candidate B). LARGE; needs
   **explicit user authorization** for (i) dual-target emission and (ii) the
   security-relevant §7 authz-required gate, and a dedicated doc-14 consult
   before touching emission.

Phase 1 + Phase 2 touch **no runtime-narrowing floor** (read-only analysis over
typed HIR). Phase 3 does and must be re-consulted at that point.

## 12. Settled approach (2026-08-23): source-to-source + infer-first with effectful-origin taint

The user reframed the mechanism away from §6's in-compiler dual emission. It is
**simpler and does not touch the compiler IR at all**, which dissolves the §11
"dual-target emission" and "endpoint-generation" walls:

**Source-to-source into two ordinary Sky projects.** The auto-split is a
**generator** that reads one annotated/inferred project and emits **two normal
Sky source projects**, each built by the *existing* compiler + targets:

- **Backend** = the app as a normal Sky server, unchanged, **plus generated
  RPC endpoints** — one per effectful `update` branch (`POST /_rpc/<Msg>`), each
  running that branch's real effect server-side and returning the updated
  `Model` (JSON via the shared codec). This is exactly the per-action
  `Server.api` shape the todos server already hand-writes.
- **Frontend** = the same app built to **wasm**, with each effectful branch
  **rewritten to an RPC call** (`Spa.postJson … "/_rpc/<Msg>" … Applied`) whose
  response *is* the updated `Model`; pure branches run client-local (zero
  round-trip). Server-only helpers + secret env vars are **not emitted** into the
  frontend project.

**`examples/60-spa-todos` (client + server + shared) IS the hand-written target
shape** — the generator's job is to produce that split from one project. Parse
↔ render already exists (`sky fmt`: syntax parses, fmt pretty-prints), so the
generator is "parse the one project → rewrite `update` → emit two projects."

**Inference (infer-first; annotation is the fallback).** "Non-pure updates are
server-side" is the rule. A branch is **server** iff it transitively:
1. performs a **server effect** — a `Db`/`File`/`Auth`/server-`Http` kernel, or
   an inline `Task.run` / `let _ =` auto-force over one; **or**
2. references an **effectful-origin value** — a top-level binding whose
   initialiser reaches an effect: a `Task.run` CAF (`db = Task.run (Db.connect …)`),
   an env/secret read (`System.getenv…`). These values are **tainted**; anything
   touching them is server, and they are excluded from the client build.

Both seeds propagate transitively over the call/reference graph; the analysis
**over-approximates to server on any ambiguity** (e.g. `Http` whose target it
cannot prove is external) — sound direction: a needless RPC, never a client
leak. The compiler already detects effectful CAFs (the memoised-fresh-value
warning) and has the `Task`-type + kernel machinery (§11), so both seeds are
recoverable.

**Fallback if inference proves ambiguous / bad DX** (open question — the user
is unsure it infers cleanly): mark server branches explicitly, via either a
**comment pragma** or a **Msg-constructor marker** (`Private T`). Infer-with-
annotation-override is the likely end state.

**First build = the inference + an inspectable report, no codegen.** For one
project it prints each `update` branch as *client* / *server* with the taint
reason (which effect or tainted value forced it), and flags a value used by both
a client branch and a server-tainted path. This is read-only, floor-free,
needs no authorization, and it **directly answers "can it be inferred well?"**
before any generator or RPC exists. If the split it derives on a real app
(todos, + a crafted effectful-CAF/env fixture) is correct and unambiguous,
Infer wins; if not, we add the annotation fallback. Only then: the generator
(source-to-source) + the runtime RPC glue.

## 13. Phase 1 DONE + verified (2026-08-23): `sky spa-partition` + the inference verdict

The inference + report shipped as **`sky spa-partition <entry>`**
(`rust/crates/project/src/spa_partition.rs`, dispatched from
`crates/sky/src/main.rs`; fixture + test in `crates/sky/tests/`). It walks the
resolved + typed HIR only — no codegen, no IR change. Verified by running it on a
crafted fixture, a direct-inline-effect app, and the real todos client, all
classifications hand-checked.

**Correction to §11 (found empirically — the consult was wrong here).** §11 said
"classify off `Res::Kernel.module`." **That misses every server effect.** With
the full stdlib loaded, `Http.post` / `System.getenv` / `Db.query` / `Auth.*` /
`File.*` resolve to **`Res::Def`** in their ordinary Sky-*source* modules
(`Sky.Core.Http`, `Std.Db`, … are `.sky` whose bodies are
`Ffi.kernel "Http_post"` etc.) — NOT `Res::Kernel`. The real effect origin is the
**`Ffi.kernel "<Symbol>"` string-literal prefix** (`Db_`, `Http_`, `System_`,
…). Keying off `Res::Kernel.module` alone silently classified the todos DB
mutations as CLIENT — the exact leak the analysis must never produce. The shipped
analyzer classifies by the FFI-symbol prefix + follows `Res::Def` callees into
stdlib bodies to a taint fixpoint. (Types are read for the `update` body, but the
server/client decision is symbol-identity + reference-graph, not type-based — an
env read via pure-typed `getenvOr` proves types alone are insufficient.)

**The FINAL rule (user, 2026-08-23) — dead simple, secure by default: pure →
client, ANY effect → server; the client is 100% pure UI.** We considered a
client-capable-Http refinement (wasm can `fetch`) and a public-vs-secret env
distinction, and the user rejected both as too much for an author to hold in
their head — the model must fit 99% of cases with an exception only for the 1%.
So **every** effect is server-side — not just the physically server-only
families (`Db`/`File`/`Auth`/`System`/`Server`/`Process`/`Io`) but also the
client-capable ones (`Http`/`Time`/`Random`/`Uuid`). An external Http call routes
through the backend; a client-local uuid/timestamp is a documented *later*
optimisation, not the v1 rule.

**Why this is the right call:** it is **secure by default** — an effectful
value or function can never reach client code, because effects don't run on the
client at all. No secret / DB handle / env value is ever in the bundle,
auditable at a glance. And every operational worry dissolves: the client only
calls its own backend (**same-origin → no CORS**, `connect-src 'self'` →
**trivial CSP**), and all env/config lives in server code (**no env semantics to
learn**). The only cost is an extra hop for external HTTP — accepted for v1.

Verified: the todos **client** reads **4 SERVER / 6 CLIENT** — the four
`Spa.postJson` mutations become RPCs (they reach `Http`), the six pure UI
branches stay client — the correct auto-split shape; the effectful-CAF/env
fixture reads 2 SERVER / 2 CLIENT.

**Residual for the enforcing GATE — SHIPPED (2026-08-23): fail-closed guard.**
`classify_kernel` no longer defaults an unrecognised family to Neutral(client).
It now classifies against two explicit, exhaustive lists in `spa_partition` —
`EFFECT_KERNELS` (all → server, incl. `Log`/`Live`/`Jobs`/`Cli`/`Tui`/`Webview`/
`Context`/`Ffi` alongside the physically-server and client-capable families) and
`KNOWN_PURE_KERNELS` (`Basics`/`String`/`List`/`Dict`/`Set`/`Maybe`/`Result`/
`Task`/`Math`/`Regex`/`Crypto`/`Encoding`/`Char`/`Path`/`Cmd`/`Sub`/`JsonEnc`/
`JsonDec`/`JsonDecP`/`Fmt`) — and a family in **neither** falls through to a
conservative **SERVER** verdict (never client). Two enforcement legs:

- **Compile-time completeness test** (`spa_partition::tests::classification_is_exhaustive`):
  enumerates every kernel pseudo-module the compiler knows from the authoritative
  `hir::KERNEL_MODULES` table (no hardcoded copy) and asserts each appears in
  `EFFECT_KERNELS` or `KNOWN_PURE_KERNELS`. Adding a kernel without deciding its
  split side is now a **build failure**. A companion test
  (`unclassified_kernel_is_rejected`) proves the guard bites on a synthetic
  unclassified family.
- **Runtime fail-closed** — `classify_kernel` returns `ServerOnly` for an unknown
  family (conservative), `analyze_loaded` emits a `FAIL-CLOSED:` note naming any
  unclassified family, and `spa_split::generate` **refuses to emit** (returns an
  error naming the culprit) rather than risk leaking an undecided kernel into the
  wasm frontend.

## 14. Generator — the e2e implementation plan (authorised 2026-08-23)

The user authorised the full e2e generator. Approach: **source-to-source, two
normal Sky projects, built by the existing compiler** (§12). Phased, each phase
verified before the next.

**B0 — Msg-constant precision (in progress).** update's own arms resolve
`update <LiteralMsg>` to that arm; helpers stay conservative. Sound; removes
false-server on pure composition.

**B1 — read/write-set analysis (per server branch).** Extend the partition with,
for each SERVER branch: the **read-set** (Model fields + Msg args the branch
reads → the RPC *inputs*) and the **write-set** (Model fields it writes → the RPC
*outputs*). Field-precise for direct `model.field` access / `{ model | f = … }`;
**over-approximate to "whole model" when a branch threads `model` into a helper**
(sound: bigger payload, never a wrong value — under-approximating reads is a
correctness bug, so unknown ⇒ send more). This is what keeps payloads ∝ effect
I/O, not Model size (§ the large-Model answer).

**B2 — the RPC shape + runtime glue (prove on a minimal app first).** One generic
per-server-branch endpoint. Client → server: `{ msg args + read-set fields }`;
server runs the whole branch server-side (dodges interleaving) → returns
`{ write-set fields }`; client applies them. Reuse `Spa.postJson` (client) +
`Server.api` (server) + `Codec.auto` for the I/O records. **Trust boundary:** the
server treats client-sent fields as effect *inputs* only — anything authoritative
is re-read from the DB / derived from the signed `sky_sid`, never trusted from the
wire (§7). Hand-write the two projects for a counter-with-one-effect first, prove
the round-trip, THEN generate.

**B3 — the generator.** `sky spa-split <entry> [--out]` emits two projects:
- **shared/** — Model / Msg / codecs, copied to both.
- **backend/** — the full app (normal Sky) + a generated `Server.api "POST
  /_rpc/<Msg>"` per server branch (decode inputs → run the branch → encode
  outputs) + `Server.listen` serving the frontend `dist/`.
- **frontend/** — Model/Msg/view + pure branches verbatim; each server branch
  rewritten to `(model, Spa.postJson … "/_rpc/<Msg>" inputs Applied<Msg>)` + a
  generated `Applied<Msg>` apply-branch; `main = Spa.app`; built `--wasm`.
Parse↔render via the `sky fmt` machinery (syntax parse → arm rewrite → render).
`examples/60-spa-todos` (client+server+shared) is the hand-written TARGET the
output must match in shape.

**B4 — build both + e2e verify + fail-closed guard.** Build backend (native) +
frontend (wasm); run the round-trip (pure branch = zero network; server branch =
RPC persists). Wire the **fail-closed** effect-family guard (§ residual). Then
generalise to a real app.

Security is the spine: every phase preserves "an effectful value/function never
reaches client code," and B4's guard makes it a build failure, not a hope.

## 15. B3/B4 DONE (2026-08-23): `sky spa-split <entry> --out <dir>`

The generator shipped as **`sky spa-split`**
(`rust/crates/project/src/spa_split.rs`, dispatched from `crates/sky/src/main.rs`;
fixture + acceptance test in `crates/sky/tests/spa_split_flow.rs` +
`tests/fixtures/spa-split/`). It is **source-to-source only** — it reuses
`spa_partition`'s analysis (now split into `analyze` + `analyze_loaded`, plus a
`model_fields` typed field-list on the report) and the syntax crate's CST for
verbatim slicing. **No compiler-IR change; the runtime-narrowing floor is
untouched.**

**Running it — one command (2026-08-25).** `sky build src/Main.sky` on a
`Spa.app` entry AUTO-SPLITS (wasm frontend + native backend under `.split/`,
`--out` to override) and builds both; `sky run src/Main.sky` does that and then
runs the backend — which serves the wasm frontend + `/_rpc` same-origin, one
binary. Detection keys on the entry's `import Std.Spa`
(`crates/sky/src/main.rs`, `is_spa_app_entry` → `spa_split_and_build`).

**`--target` and `--embed` COMPOSE with the split** — they are not escape
hatches. `sky build --target ios src/Main.sky` splits and builds the frontend
for the iOS shell; `sky build --embed src/Main.sky` splits and bundles
PostgreSQL into the backend; the two combine. `sky run --embed` runs the backend
with its embedded cluster. Only three things skip the split: `sky check` (it
type-checks the shared source directly), an explicit `--wasm` (a raw client
build, advanced), and a project already generated by a prior split — the
generator stamps `[spa] generated = true` into the frontend/backend `sky.toml`,
and `is_generated_split_project` reads it so building the generated frontend
(itself a `Spa.app`) never re-splits, whether the split's own `--target`
sub-build or a user rebuilds it by hand.

The explicit form remains `sky spa-split <entry> --out <dir> --build` (+
`--broker`, `--target`, `--embed`) then `cd <dir>/backend && ./sky-out/app` —
reach for it when you want the split artefacts kept at a specific path.

**What it emits** (matching the `examples/60-spa-todos` client+server+shared
target shape):
- **shared/Shared.sky** — per SERVER branch `M`, `type alias MReq` (read-set) /
  `type alias MResp` (write-set) + their `Codec.object … |> Codec.field … |>
  Codec.buildObject` codecs, copied into BOTH projects' `src/`.
- **backend/** — the input app copied **verbatim** (Model, Msg, init, `update`,
  all helpers incl. the server ones), `main` replaced by a `Server.listen` with
  one `Server.api "POST /_rpc/M" MHandler` per SERVER branch + `Server.static
  "/" "../frontend/dist"`. Each handler decodes the read-set, **reuses the app's
  own `init` + `update`** to run the REAL effect server-side (dodging inline
  interleaving), and answers with the write-set. The effect body is never
  rewritten.
- **frontend/** — Model/init/view/subscriptions/`main` verbatim (view's
  annotation adjusted to `-> any` for the wasm renderer); Msg extended with an
  `AppliedM (Result Error MResp)` variant per SERVER branch; `update`'s pure arms
  kept verbatim, each SERVER arm rewritten to `Spa.postJson MReqCodec MRespCodec
  "/_rpc/M" <read-set> AppliedM` with a generated `AppliedM` apply-arm.
  **Server-tainted top-level bindings (from the analysis) are OMITTED** — the
  security spine, asserted by the test (the frontend source contains no `File.` /
  `saveN` / `Db.` / `System.`).

**Verified end-to-end** on the counter-with-one-File-effect skeleton: `sky
spa-split` → both projects build (`sky build backend`, `sky build --target web
frontend`); `POST /_rpc/Persist -d '{"n":7}'` returns `{"log":"saved: 7"}`,
`count.txt` is written with `7`, `GET /` serves the wasm bootstrap and
`/main.wasm` is 200.

**Handled fully (generalised 2026-08-23 to a REAL one-project app —
`tests/fixtures/spa-split-todos`, a todos app with `Model { todos : List Todo,
draft : String }`, `Msg = DraftChanged String | Add | Toggle Int | Remove Int`,
user-defined `todoCodec`/`todoListCodec`):**
- **Single-entry-module app** — pure + N effectful branches, field-precise
  read/write sets, primitive (`Int`/`String`/`Bool`/`Float`) field types.
- **Msg-arg-typed RPC inputs** — a server branch that binds a Msg arg
  (`Toggle Int`) puts a *typed* field into the request (`ToggleReq { id : Int }`
  + `Codec.int`); the backend RECONSTRUCTS the Msg (`update (Toggle p.id) m`, not
  a bare ctor); the frontend SENDS it (`… "/_rpc/Toggle" { id = id } AppliedToggle`).
  The arg types come from the typed HIR (`BranchVerdict.msg_arg_tys`), distinct
  from the read-set model fields.
- **Non-primitive field codecs** — a Req/Resp field of a non-primitive type
  (`List Todo`) is resolved in priority order: (a) a project `Codec <T>` binding
  (the user's `todoListCodec : Codec (List Todo)`) — referenced AND copied into
  `Shared` together with the type + helper codec it needs (`Todo` + `todoCodec`),
  never re-declared in either Main; (b) `List X` / `Maybe X` → `Codec.list` /
  `Codec.maybe`; (c) a JSON primitive → `Codec.int`/…; (d) otherwise a **clear
  Err** naming the field + type — never a placeholder codec that will not
  compile.
- **Whole-model fallback** — a branch reading/writing `model` opaquely carries
  the whole `Model` (every field wired through the same codec resolver).

**Refused, not mis-generated:** a **multi-module** app (the entry importing
sibling project modules) returns a clear Err rather than emitting a backend that
references uncopied modules; a field whose codec cannot be resolved is an Err.

**Fail-closed classification guard — SHIPPED (2026-08-23).** The §13 residual is
closed: `spa_partition::classify_kernel` classifies against exhaustive
`EFFECT_KERNELS` / `KNOWN_PURE_KERNELS` lists with an unknown family falling
through to a conservative **SERVER** verdict, `spa_split::generate` refuses to
emit when the compiler knows an unclassified kernel, and the compile-time
`classification_is_exhaustive` test (over the real `hir::KERNEL_MODULES`) makes
"add a kernel without deciding its split side" a build failure. See §13.

## 16. Server→client PUSH (SSE) — `Cmd.publish` → `Sub.subscribeTopic` (2026-08-23)

B1–B4 (§14/§15) generate the **client→server** direction: an effectful branch
becomes a `POST /_rpc/<Msg>` the client calls. This section adds the
**server→client** direction, so a Sky.Spa app whose `subscriptions` subscribes
to a topic is *pushed* messages when a server-effect branch publishes to it. It
is the auto-split's counterpart of Sky.Live pub/sub, delivered over **SSE**
(not WebSocket), and it **reuses the existing runtime** — the same in-process
broker Sky.Live uses, the same `Sky.Http.Server.Stream` chunk-writer, the same
`Sub.subscribeTopic` surface. No runtime-narrowing floor is touched (runtime Go
+ generator only).

**The wire path, end to end:**

```
client A: Increment ─▶ POST /_rpc/Increment ─▶ backend runs update (real File
                                                effect) ─▶ returns (m2, Cmd.publish
                                                "count" n) ─▶ spaInterpretPublish
                                                fans it through the broker
                                                        │
broker.Publish("count", n) ─────────────────────────────┤
                                                        ▼
every client subscribed via GET /_sky/sub?topic=count receives an SSE
`data: <json>\n\n` frame ─▶ EventSource onmessage ─▶ JSON→Sky decode ─▶
sky_call(toMsg, payload) ─▶ GotCount n ─▶ update ─▶ re-render
```

**What the generator emits (push mode).** Push mode turns on when the app
reaches `Cmd.publish` / `Cmd.publishNoEcho` **or** `Sub.subscribeTopic`
(`SpaPartitionReport.{publishes, subscribes_topics}`, detected by walking the
reachable defs for the kernel-alias symbols). Then `sky spa-split` adds to the
**backend**:

- **A standalone broker** — `spaBroker = spaNewBroker ()`, a memoised CAF over
  `rt.Spa_newBroker`, which constructs a bare `*topicRegistry`
  (`runtime-go/rt/live_topics.go`). It does **not** use `PubSub_publish` /
  `Std.PubSub`, which need a `Live.app`-registered process broker
  (`live_pubsub_task.go`) — a plain `Sky.Http.Server` backend registers none.
- **Publish-interpreting RPC handlers** — each handler now binds the `Cmd` its
  `update` returns and feeds it to `rt.Spa_interpretPublish(broker, cmd)` before
  answering (previously the `Cmd` was discarded, §15). The interpreter
  pattern-matches `publish` / `publishNoEcho` (recursing through `Cmd.batch`) and
  calls `broker.Publish(topic, SessionEvent{Payload, …})`; every other `Cmd`
  kind is ignored (a stateless backend delivers broadcasts, not client effects).
  It lives in **package `rt`** because `cmdT`'s fields are unexported.
- **The SSE endpoint** — `Server.api "GET /_sky/sub" subHandler`, where
  `subHandler` reads `?topic=` and returns
  `Stream.stream "text/event-stream" (spaStreamTopic spaBroker topic)`.
  `rt.Spa_streamTopic` subscribes to the topic, primes a ≥2 KB proxy pad, then
  loops emitting each published payload as `data: <json>\n\n` until the client
  disconnects (a failed write) — then cancels the subscription and finishes. A
  15 s heartbeat comment detects dead connections. `serveStreamingResponse` now
  sets `Cache-Control: no-cache`, `Connection: keep-alive`, and
  `X-Accel-Buffering: no` for any `text/event-stream` response (parity with
  Sky.Live's SSE headers), so proxies don't buffer.

The **frontend** keeps `subscriptions` verbatim; the client driver
(`runtime-go/rt/live_wasm.go`) reconciles `Sub.subscribeTopic` leaves (identity
= the topic string, same diff shape as `Sub.every`): an added topic opens
`new EventSource("/_sky/sub?topic=" + topic)` whose `onmessage` JSON-decodes
`e.data` to a Sky `any` and runs `step(sky_call(toMsg, payload))`; a removed
topic closes the EventSource and releases its callback. The decode is
structural (`JSON.parse` → Sky `any`, integral numbers → `int`), reconstructing
the value's Sky shape rather than a `.(T)` assertion.

**Security carries over unchanged.** The client has no effects, so no secret /
DB handle ever reaches it; the SSE endpoint only *delivers* what a server branch
chose to publish. A publish payload is server-authored — never echoed from a
client-sent field for anything authoritative (§7). The client only ever talks to
its own backend (same-origin → no CORS).

**Multi-replica — wired, and configurable in code.** `Spa_newBroker urlArg`
routes through `maybeOverrideBroker(newTopicRegistry(0), effectiveBrokerUrl(url))`:
the **default is in-process** (single replica — a publish on A reaches only SSE
connections on A). A broker URL upgrades it to the SAME cross-instance **Redis
broker Sky.Live uses** (the `Broker` interface, `live_redis_broker.go`) — so a
publish on replica A reaches an SSE subscriber on replica B — with **no session
store required** (the broker is app-scoped, not store-scoped).

The URL comes from one of two places, reconciled by `effectiveBrokerUrl`
(**env wins**):

* **`sky spa-split --broker <url>`** bakes the URL into the generated backend
  (`spaBroker = spaNewBroker "redis://host:6379"`). This is the auto-split
  analogue of `Sky.Config.withLiveBroker` — the generated backend is a stateless
  `Sky.Http.Server`, so it has no `config` binding of its own; the flag is how
  the URL gets into the source. Without the flag the backend emits
  `spaNewBroker ""` (in-process; env still applies).
* **`SKY_LIVE_BROKER_URL`** (operator env) overrides the baked value at runtime.

An undialable URL degrades to in-process (logged); `SKY_LIVE_BROKER=inprocess`
forces local. A multi-replica deploy still needs **sticky routing** so a
client's `/_sky/sub` and `/_rpc/*` hit a coherent set. Verified end-to-end
against a live Redis: two backend instances (no shared session store), a POST
`/_rpc/Increment` on instance A delivers a `data:` frame to an SSE subscriber on
instance B — driven by the **baked** `--broker` URL (no env), by the
**`SKY_LIVE_BROKER_URL`** env (no bake), and the default (no URL) keeps
single-instance in-process push.

**Verified.** `tests/fixtures/spa-push-counter` (a shared counter:
`Increment` writes count+1 to disk inline and publishes `"count"`; `GotCount n`
folds a pushed count; `subscriptions = Sub.subscribeTopic "count" GotCount`)
generates, both projects build, and a live run proves push deterministically:
an SSE reader on `/_sky/sub?topic=count` receives `data: 1` then `data: 2` as
two `POST /_rpc/Increment` calls fire — no browser needed. `rt` unit tests
(`spa_push_test.go`) cover the `publish → broker` leg; the generator wiring +
build are asserted in `spa_split_flow.rs`
(`wires_server_to_client_push_when_the_app_uses_publish_and_subscribe_topic`).

## 17. Multi-module apps (2026-08-23): pure modules → both trees, effectful modules → backend-only

§15 shipped the single-entry-module generator and **refused** any project whose
`src/` spanned more than the entry module. Real apps span modules — the Model/Msg
loop in `Main`, the domain types + codecs in a `Domain` module, the effects in a
`Store` module — so the generator now splits them. The mechanism is
source-to-source still; no compiler IR, no runtime-narrowing floor is touched.

**The routing rule (simpler + sound).** Every project module other than the
entry is classified by whether it contains a **server-tainted def** (the
`spa_partition` taint analysis already tracks tainted top-level bindings across
*every* module, not just the entry):

- A module with **no** tainted def is **pure** → copied **verbatim into BOTH
  trees** (frontend wasm + backend native). Pure domain types, codecs and pure
  helpers are shared unchanged.
- A module with **any** tainted def is routed to the **backend only** — the
  **whole module**, never emitted into the wasm frontend and never imported by
  it. This is the simpler of the two options (the alternative — splitting a
  mixed module's pure parts into the frontend — is unnecessary and error-prone);
  it is sound because the client keeps zero effects.

`Shared` still holds the generated `<Msg>Req`/`<Msg>Resp` records + codecs. When
a wire field's codec or type is **declared in a pure sibling module** (e.g.
`todoListCodec` / `Todo` in `Domain`), `Shared` **imports** that module rather
than re-copying the def — the module is already present in both trees. Only
codecs/types declared in the **entry** module are copied into `Shared` (as
before, to avoid a duplicate definition, since the entry is transformed).

**The mixed-module rule → a pure def that lives in a backend-only module.** A
module can be backend-only (it has ≥1 effect) yet also contain a pure helper.
That pure helper is backend-only too (the whole module goes backend). This is
sound **as long as no frontend def needs it**. The generator verifies exactly
that: it walks every frontend-retained def (the entry's non-tainted defs — with
`update` rewritten so its server branches no longer reference the module — plus
every pure sibling module's defs) and, if any references a pure def in a
backend-only module, **refuses with a clear Err** ("a pure client value cannot
depend on a server-tainted module — move it into a pure module shared by both
trees"). Fail-closed: a real error rather than a silent leak or a frontend that
won't compile. A referenced codec that lives in a backend-only module is refused
the same way.

**What each tree gets:**
- `frontend/src/` — the transformed `Main` + `Shared` + every **pure** sibling
  module. It imports neither the effectful modules nor `Std.Spa`'s server side;
  the leak-check (`grep -rnE 'File\.|Db\.|System\.|Store\.|load|save'
  frontend/src/`) is clean.
- `backend/src/` — the transformed `Main` (RPC handlers) + `Shared` + **every**
  sibling module, pure and effectful alike (it runs the real effects
  server-side).

**Verified end-to-end** on `tests/fixtures/spa-split-multimodule` — a todos app
split across `Main` (Model/Msg/TEA loop), a pure `Domain` (the `Todo` type +
`todoCodec`/`todoListCodec`) and an effectful `Store` (`File` load/save). `sky
spa-split` routes `Domain` into both trees and `Store` into the backend only;
the frontend leak-check is clean; both projects build (backend native, frontend
wasm); and a live round-trip (`POST /_rpc/Add`, `POST /_rpc/Toggle`) persists to
`todos.json` and returns the write-set. The generator wiring + build + routing
are asserted in `spa_split_flow.rs`
(`splits_a_multi_module_app_routing_pure_and_effectful_modules`).
