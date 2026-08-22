# Sky.Spa — the auto-split: `Task`-type tracing + the effects-via-`Cmd` dialect

> **Status:** design (v2 target). This is the corrected, *stronger* mechanism for
> the compiler-derived client/server partition — the one the measurement in
> [design.md §0.1](design.md) did **not** evaluate. That measurement falsified a
> *weak* mechanism (classify a branch by its returned `Cmd`); this document
> specifies the mechanism that survives it (trace `Task` in the branch **body**),
> the one obstacle it hits (inline effect *interleaving*), and the dialect that
> removes the obstacle. Grounded in real Sky surfaces (file:line).

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

**The verdict — can it be inferred well? Mostly YES; one class needs annotation.**
- **Clean / unambiguous:** pure branches → CLIENT; `Db`/`File`/`Auth`/`System`
  (incl. pure-typed `getenvOr`) / `Process` / `Io` → SERVER; effectful-origin
  CAFs + env reads and anything transitively referencing them → SERVER via the
  taint fixpoint. For the auto-split's INTENDED input (one unsplit app doing
  `Db`/env work inline in `update`), the partition is exact.
- **The one ambiguous class — `Http.*`.** A client-issued `fetch` to a stateless
  backend is statically indistinguishable from a server-side HTTP call (one
  pseudo-family, relative URLs). Marked SERVER conservatively (sound: never a
  client leak). Consequence: on the *already-split* todos **client**, its four
  `Spa.postJson` mutations read as SERVER even though they run client-side — the
  analyzer cannot tell "client half of a split app" from "inline server call to
  become an RPC." **This is the case that needs the §12 annotation fallback** (a
  marker distinguishing a client-issued fetch from a server effect).

**Residual for the enforcing GATE (not the advisory report).** `classify_kernel`
is an allowlist (client-safe: `Time`/`Random`/`Uuid`; server: the families above)
with unknown families → Neutral(client). Fine for an advisory report, but the
Phase-3 generator/gate — where a mis-classification becomes a real leak — MUST
**fail-closed** on any unrecognized effect-kernel family (a new server kernel
added without updating the table would otherwise default client). Guard it with a
test that enumerates effect families and asserts each is classified.
