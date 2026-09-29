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
`Task`/`Math`/`Regex`/`Encoding`/`Char`/`Path`/`Cmd`/`Sub`/`JsonEnc`/
`JsonDec`/`JsonDecP`/`Fmt`/`Qr`) — and a family in **neither** falls through to a
conservative **SERVER** verdict (never client). Since v0.27.0 a third list,
`MIXED_KERNELS`, classifies a family **per function**: `Crypto` keeps only
`sha256`/`sha512`/`sha1`/`md5`/`constantTimeEqual`/`rsaSha256Verify` on the
client, `Sign` only `verify` and public-key import/export, `Kx` only public-key
import/export; every other member (key derivation, keyed MACs, AEAD, random
draws, signing, key agreement), and any member added later, is SERVER. `Kdf`,
`Noise` and `Cpace` are SERVER as a whole, and so (v0.27.0) are `Subprocess`
(the streaming `Process.spawn` family) and `Watch` (`Std.Watch`): a browser can
neither spawn a process nor watch a file system. An app whose device must hold
its own keys opts in with `Spa.withClientCrypto` / `App.withClientCrypto`: the
key-holding members of `Noise`, `Cpace`, `Kx`, `Sign` and `Kdf` then run in the
client, and the build refuses every flow that would move a key to the server
(`docs/skyspa/client-crypto.md`). Two enforcement legs:

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
A whole-model RESPONSE goes with a narrow request only when every response
leaf builds a fresh model (a record literal, `{ emptyModel | … }`); when one leaf
rebuilds the model and another keeps it (`{ model | error = … }` on a failed
sign-in), the request carries the whole model, or the keeping leaf would answer
with `init`'s defaults (R3, `BranchIo::fresh_response`).

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
  one `Server.rpc "POST /_rpc/M" MHandler` per SERVER branch (§21) + `Server.static
  "/" "../frontend/dist"`. Each handler decodes the read-set, **reuses the app's
  own `init` + `update`** to run the REAL effect server-side (dodging inline
  interleaving), and answers with the write-set. The effect body is never
  rewritten.
- **frontend/** — Model/init/view/subscriptions/`main` verbatim (view's
  annotation adjusted to `-> any` for the wasm renderer); Msg extended with an
  `AppliedM (Result Error MResp)` variant per SERVER branch; `update`'s pure arms
  kept verbatim, each SERVER arm rewritten to `Spa.rpc MReqCodec MRespCodec
  "/_rpc/M" (\spaM_ -> <read-set of spaM_>) AppliedM` with a generated
  `AppliedM` apply-arm. The request is a FUNCTION of the model, built when the
  RPC is sent (§20).
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
  `subHandler` reads `?topic=`, checks that the app's own `subscriptions` for the
  visitor's verified session names it (403 otherwise, §21), and returns
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
its own backend (same-origin → no CORS). Since v0.27 the endpoint also streams a
topic only to a visitor whose own `subscriptions` name it (§21).

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


## 18. Server-internal effect chaining (2026-09-10)

A server RPC branch that returns `Cmd.perform serverTask ToMsg` — where the
result `ToMsg` feeds back through `update` and is dispatched **only**
server-side — now runs the **whole** chain inside the triggering branch's RPC
and answers with the final settled model diff. This mirrors Sky.Live, where the
entire TEA loop is server-side.

**The bug it fixes.** The generated RPC handler used to bind `( m2, _ )` and
**discard** the returned command (`gen_backend`), so a returned `Cmd.perform`
ran nowhere and any field its result-`Msg` wrote was silently dropped. The
canonical shape:

```elm
Reload ->
    ( model, Cmd.perform (File.readFile "data/note.txt") Reloaded )

Reloaded (Ok raw) ->
    ( { model | note = raw }, Cmd.none )
```

`POST /_rpc/Reload` used to answer with an empty `note`; now it settles the read
server-side and returns `note` = the file contents.

**Server-internal Msgs.** A `Msg` is **server-internal** when it is the `toMsg`
of a server arm's `Cmd.perform`/`Cmd.batch`, is constructed **nowhere** on the
client (not in `view`, `subscriptions`, nor a client arm — an over-approximated,
transitive scan), and is not itself a wire (`/_rpc`) branch. A server-internal
Msg gets **no** `/_rpc/<Msg>` route, **no** `Applied<Msg>` variant, and its
client `update` arm plus its `Msg`-union constructor are **pruned** from the
frontend (`spa_partition::compute_server_chaining` →
`spa_split::union_text_without_variants` + the frontend arm drop).

**Write/read-set union (soundness).** A chaining branch's RPC response write-set
is the **union** over the triggering arm PLUS every server-internal continuation
arm reachable through the perform edges. `Reload`'s write-set gains `note`
(written by `Reloaded`). Under-approximating (dropping a real write) is a
correctness bug, so any shape the resolver cannot fully read widens to the whole
model — never narrows.

**Only server continuations chain (v0.27.0).** A continuation whose own arm is
CLIENT (reaches no server effect) does not join a chain: it must run in the
client when the task's result arrives, on the model the client holds then, as
Sky.Live runs it. Settled on the server it read the request's copy of the model
and its write overwrote what the client did meanwhile. Such a branch is a
client-result root (§19) or a follow-up branch (§20), whose result Msg runs in
the client. `Reload` → `Reloaded (Ok raw) -> { model | note = raw }` above is
therefore a client-result root today; a chain is `Ship` → `Shipped` where
`Shipped` itself reaches a server effect (`tests/fixtures/spa-server-chain`).
A chain root is a hold RPC (§20).

**Fail-closed (G5).** A branch is **not** chained when its transitive command
chain contains a `Std.Native` **client** effect (which cannot run server-side),
an **ambiguous** continuation (a `toMsg` that is also client- or
wire-dispatched), or an **opaque** command shape the static resolver cannot
read. The un-chained branch's continuations stay client arms, and the branch
becomes a **follow-up branch** (§20): its command still RUNS. (There used to be
a discard-and-warn floor here — the command ran nowhere. It is gone.)

**Runtime.** `runtime-go/rt/spa_chain_notjs.go`'s `Spa_settleServerChain`
(`Ffi.kernel "Spa_settleServerChain"`, the `spaChainSettle_` alias) folds every
server-runnable perform leaf back through `update` to a **fixpoint**, bounded by
a hard round cap (`spaChainMaxRounds = 64`) so a self-referential Msg cycle
terminates rather than hangs. It returns `( settledModel, residualCmd )`.

**Verified.** Classification + write-set union in
`crates/project/tests/spa_server_chain.rs` (against
`tests/fixtures/spa-server-chain`, which exercises both the chained `Reload`
and the fail-closed `SyncCopy` that batches a Native client effect); the
runtime fold + cycle termination in `runtime-go/rt/spa_chain_notjs_test.go`;
and the end-to-end `POST /_rpc/Reload` → `note` in `spa_split_flow.rs`
(`server_internal_chain_e2e_post_reload_returns_file_note`).


## 19. Client-result perform — pattern-2 (2026-09-10)

The complement of §18. Where server-internal chaining settles the **whole**
chain server-side (the result Msg is server-only), a **client-result perform**
runs the server task server-side but hands its **result** to the client, because
the result Msg is a **client** arm.

**The shape.** A server branch returns `Cmd.perform serverTask ResultMsg` —
directly or **through a guard/HOF wrapper** (`requireAdmin model (\_ -> …)`) —
where `serverTask` reaches a real
server effect and `ResultMsg`'s own arm is **client-pure** (a model update over
the task result):

```elm
Upload data ->
    guard model (\_ ->
        ( { model | busy = True }
        , Cmd.perform (saveBlob data) Saved
        ))

Saved (Ok url) ->
    ( { model | items = url :: model.items, busy = False }, Cmd.none )
```

**The bug it fixes.** Because the perform hides inside the guard thunk, §18's
tail walk sees no command, so `Upload` fell to a plain wire branch whose handler
bound `( m2, _ )` and **discarded** the command — `saveBlob` ran nowhere and
`Saved` was never dispatched. The upload silently did nothing.

**Behaviour.** The `Upload` RPC runs `saveBlob` server-side and answers with the
task **result** (`Result Error String`), carried in the branch's `UploadResp`
record as a single `result` field (`Codec.result Codec.error <valCodec>`). The
frontend's `AppliedUpload (Ok resp) -> update (Saved resp.result) model` then
dispatches the result Msg with the **whole** `Result` value into the client
`update`, so `Saved`'s arm runs in the wasm client. `Saved` stays a client arm —
**no** `/_rpc/Saved` route, **no** `SavedReq`, **no** decomposition of the
`Result` into `Ok`/`Err` binders (the `record is missing field(s): url` bug a
`Result`-typed wire branch produces). The server task never crosses to the
client; only its typed result does.

**Why not the residual-Cmd channel.** The alternative — return the
`Cmd.perform serverTask` to the client to run — would hand a **server** effect
to the wasm client, whose `!js` server-effect stub returns `Err`. Unsound. Only
option (a), the typed result crossing the wire, is used.

**Classification** (`spa_partition::compute_server_chaining`, after the §18
pass). A branch pattern-1 did **not** already own (not a chaining root, not a
server-internal continuation) is a **pattern-2 root** when its command — read
through the guard-aware walk `collect_guarded_tail_cmd_exprs` — is a **single**
`Cmd.perform serverTask ResultMsg` with a server task and a `ResultMsg` that is
**not** server-classified (client-pure). Recorded as
`ServerChaining::client_result` (`(root, result_msg)` pairs).

**Fail-closed.** The branch is not pattern-2 when `ResultMsg`'s arm **reaches a
server effect** (a deeper chain), the task is a `Std.Native` **client** effect,
or the command is not a single clean server perform. It is then a **follow-up
branch** (§20) — its command runs; nothing is discarded. Since v0.27.0 the
direct-tuple shape (`Reload` → `Reloaded` of §18, with no wrapper) is pattern-2
too: a client-pure `ResultMsg` runs in the client whether or not the perform
sits behind a wrapper. Only when `ResultMsg`'s argument has no wire codec does
it still settle server-side as a §18 chain (the build prints a note).

**Runtime.** `runtime-go/rt/spa_perform_notjs.go`'s `Spa_runServerPerform`
(`Ffi.kernel "Spa_runServerPerform"`, the `spaRunPerform_` alias) walks the
branch's command, runs the single server `perform` leaf's task, and returns its
`Result` **raw** — without folding it back through `update` (§18's job). A
command with no runnable perform returns a classified `Err`.

**Verified.** Generation contracts (no server-effect leak; `Saved` is not a wire
branch; the whole `Result` is carried; fail-closed `Stored` whose arm reaches a
server effect), both-trees build, and the end-to-end `POST /_rpc/Upload` →
`Ok "blob.txt"` in `spa_split_flow.rs`
(`client_result_perform_wires_task_result_to_client`,
`client_result_both_trees_build`,
`client_result_e2e_post_upload_returns_task_result`, against
`tests/fixtures/spa-client-result`); the runtime run-and-return + no-fold + Err
fallback in `runtime-go/rt/spa_perform_notjs_test.go`.


## 20. RPC consistency, follow-ups, guard and persistence (2026-09-23)

The client must give the answer Sky.Live gives for the same Msg sequence. Live
runs the whole TEA loop on the server, one Msg at a time, in dispatch order.
Proven end to end by `scripts/spa-rpc-consistency-e2e.sh` (fixture
`rust/crates/sky/tests/fixtures/spa-rpc-consistency`).

**Each Msg once, in arrival order (v0.27.0).** Every server-branch arm emits an
RPC Cmd (runtime `cmdT{kind: "rpc"}`), and the client's Msg scheduler
(`runtime-go/rt/spa_rpcqueue.go`) runs every Msg — a click, a timer tick, a
perform result, an RPC result — through `update` exactly once, in arrival order,
with its Cmds. The split gives each server constructor one of two kinds
(`spa_split::ArmRoutes`):

- **async** (`Spa.rpc`): the arm's own model write is empty, or reads no server
  data (`spa_partition` `own_model_client`: a direct `( model, cmd )` tuple whose
  model half has no server reason). The client keeps that model half and runs
  it when the Msg runs; only the command goes over the wire, built from the
  model the Msg ran on and sent at once. The `Applied<Msg>` arm applies no
  write-set (the client already made the write; later Msgs keep theirs) and
  dispatches the result / follow-up Msgs. Several async RPCs are in flight
  together; their results run in arrival order.
- **hold** (`Spa.rpcHold`): the arm's own write needs server data, or the
  branch is a server-internal chain root (§18). Sky.Live runs that update as one
  synchronous step, so the client holds every later Msg until the response has
  run (follow-ups first), then runs them in order, once each, with their Cmds.
  The `Applied<Msg>` arm applies the server's write-set.

This replaced the v0.26 design (one RPC at a time; Msgs applied during the RPC
recorded and REPLAYED on top of the response, without their Cmds). A replay
re-runs a client arm, which is not safe: an arm that spends a single-use state
(`Noise.encrypt` on the model's transport) fails the second time, and an arm
that decides on a field the response changed decides differently while its new
Cmd is dropped (a timer that starts a call only when `busy` is False stalled for
good). The differential property test `TestSpaSched_MatchesLiveOnRandomInterleavings`
drives random Msg sequences with interleaved RPC completions through the
scheduler and through a Sky.Live session model and requires the same update
trace and the same model; `scripts/spa-rpc-order-e2e.sh` compares web:app with
Sky.Live in Chromium and WebKit. The contract is documented for app authors in
[overview.md](overview.md), "Msg order and server calls".

**Retry without a second effect.** Each request carries `?rid=<id>`; a retry
re-sends the same id. The backend answers a repeated id from the response it
already produced (`spa_rpc_dedupe.go`: bounded cache, keyed by `sky_sid` + path
+ id), so a request whose response was lost is never run twice. A network
failure keeps the RPC in flight (a hold RPC keeps later Msgs waiting) and is
reported to the app at once; the Retry overlay re-runs every failed perform in
failure order (`spaRetryQueue`).

**Follow-ups: a server branch's command runs.** A server branch that is neither
a chaining root (§18) nor a client-result root (§19) but returns `Cmd.perform`
leaves is a follow-up branch (`SpaPartitionReport::follow_up`). Its handler runs
every server perform leaf (`Spa_collectFollowUps`), encodes the resulting Msgs
(one `SpaFollow<Ctor>Req` wire record per constructor, in `Shared`) into the
response field `spaFollow_`, and the client decodes them and dispatches them in
order through its own `update` (`Spa.followUps`), ahead of any Msg that waited
behind the branch — a pure arm runs locally, a server arm sends its own RPC. A follow-up that cannot be decoded is
routed to `App.withRpcError`, else reported on the console (`Spa.reportError`);
never dropped. The split reads the follow-up constructors from the command: a
`Cmd.perform _ Ctor` leaf, through helpers, `let` names, `if` / `case`, and a
`Cmd.batch` over a list, `xs ++ ys`, `c :: cs` or `List.map` / `List.indexedMap`
of a lambda or helper (each perform then runs once per element). A follow-up
constructor it reads whose argument has no wire codec fails the build, naming
it. When a command cannot be read (for example `Cmd.batch (List.filterMap …)`),
every `Msg` constructor may be its follow-up: those with a wire codec cross, and
those without one are named in a build warning. If one of them occurs at run
time, the backend drops it and logs the classified error
`SpaFollowUpOutsideWire` (`Spa_followUpOutsideWire`); the rest of the response
still applies (R1).

A `Std.Native` leaf (a client-only effect) cannot run on the server: the server
skips it, and the client runs it when the Msg runs, on the model the request is
built from (`Cmd.batch [ <native leaves>, Spa.rpc … ]`). A native leaf that
uses a `let`-bound value of the arm or a server-tainted binding fails the build,
naming it.

**Guard.** `App.withGuard` runs in the client before `update` for every Msg,
exactly like Live (`Spa.withGuard`, `spaGuardedUpdate`); a rejected Msg keeps the
model and runs no Cmd. The backend still runs the guard on every server branch
(the trusted check), and the fields the guard reads ride every request
(`spa_partition::guard_readset`), so the server guard sees the client's values.
A guard that reaches a server effect cannot run in the client; the build warns.

**Persistence.** Every `web:app` build with a derivable `init` model persists
the client model to `localStorage` and restores it on reload
(`Spa.withModelDecoder` + `Spa.withModelEncoder`). On a full page load
the client restores the stored model and takes from the SSR seed ONLY the fields
the server settled for THIS page (R2): the session fields, the fields the
`withRequest` hook writes (it runs on every request, so they are a constant list,
`Spa.withPersistSeedFields`), and the write-sets of the commands the page marks
finished in `data-sky-settled` — init's command chain and the route's
`onNavigate` Msg with its command chain. The page names these fields in
`data-sky-seed-fields` on `#app`; the backend computes them per `onNavigate`
constructor at build time (`spaNavSeedFields_`). Every other field keeps its
stored value, even one only server branches write: the seed holds `init`'s
default for a field this page did not load, and a default never paints over
data. An `onNavigate` Msg whose write-set cannot be read (a whole-model write, an
unreadable command) is not marked finished, so the client runs it after the
mount, as it did in v0.25.16. This is Sky.Live's rule: a Live reload keeps the
session model and runs `onNavigate` over it, so a notice that `onNavigate` clears
is cleared, and one it does not clear stays. A restore does not cancel `init`'s
own command.

**Persistence is per identity.** The stored model restores only when the
identity it was stored under equals the identity of the SSR seed. The identity
is the value of the session fields (`Spa.withPersistProtectedFields`),
canonically serialised (`spa_persist.go` `spaIdentityOf`). Any change of
identity (one user to another, signed out to signed in, signed in to signed out)
restores nothing, removes the stored copy, and boots from the seed
(`spaRestoreStored`). Two tabs with two identities share one `localStorage`, so
without this rule a full load of one tab restored the other identity's model
under its own session. An `update` that clears the session removes the stored
copy, and the page then writes no signed-out model (`spaPersistWrite`). See
[overview.md](overview.md), "Client persistence and identity".

**Seeded boot (SPA-10).** When the page is server-rendered, the client boots
from the `#sky-model` seed: the model the server rendered, with `init`'s read
and the route's `onNavigate` load already settled into it. The page carries
`data-sky-settled` on `#app`, naming what the server FINISHED: `init` (init's
command ran to the end, or was empty) and `nav` (the route's `onNavigate`
command ran to the end). "To the end" means every leaf ran, no follow-up was
left (the SSR settle runs one round) and no write was suppressed (a GET never
mutates). The client skips exactly the named commands, so a settled deep link
makes no RPC and `onNavigate` runs once per navigation, as on Sky.Live. A
command the server did not finish (a chained read, a suppressed write, an
`init` command that is not GET-safe) still runs once on the client.

**`update` without `case msg of`.** A wholly pure `update` with no `case` runs in
the client as written. One that reaches a server effect fails with a message
naming the change (write `update msg model = case msg of …`).

## 21. RPC and push security

The split backend authenticates the browser with the signed `sky_sid` cookie
(`HttpOnly; SameSite=Lax`, plus `Secure` on any request that arrived over
TLS or through a proxy that sent `X-Forwarded-Proto: https`). The browser
attaches that cookie by itself, so two endpoints need more than the cookie.

**`/_rpc/<Msg>` and `/_rpc/__spaSignOut` are `Server.rpc` routes.** Before
v0.27 they were `Server.api` routes: exempt from CSRF and with no other
check. A cross-origin page could send a CORS-simple `text/plain` POST, which
a browser sends without a preflight and with the cookie, and the handler ran
with the victim's session. SameSite=Lax limited this to same-site attackers
(a sibling subdomain, another port on localhost) and to browsers without
SameSite, but did not close it. The double-submit CSRF token cannot help: the
CSRF cookie is HttpOnly and the wasm client cannot read it.

A `Server.rpc` route keeps the CSRF exemption and runs a guard before the
handler (`runtime-go/rt/rpc_guard.go`):

| Request | Result |
|---|---|
| Wrong method (for example `GET /_rpc/Save`) | 405 |
| Body not `Content-Type: application/json` | 403 |
| `Sec-Fetch-Site: same-origin` or `none` | passes |
| `Origin` equal to the app's public origin | passes |
| `Origin: null`, or any other `Origin` | 403 |
| `Sec-Fetch-Site` other than same-origin with no `Origin` | 403 |
| No `Origin` and no `Sec-Fetch-Site` (curl, a server) | passes |

The public origin is `SKY_PUBLIC_URL` when it is set (one URL or a
comma-separated list), else the request's own scheme and `Host`. A proxy that
keeps the `Host` header (Caddy's default) needs no setting. A proxy that
rewrites `Host`, or a tunnel, needs `SKY_PUBLIC_URL`, and the 403 body says
so. `X-Forwarded-Host` is not read. The native shells (`mobile:*`,
`desktop:*`) load the backend's own http(s) URL, so their requests are
same-origin. The wasm client already sends every RPC as a same-origin JSON
POST, so an app needs no change.

**`GET /_sky/sub?topic=<t>` is authorised against the app's own
`subscriptions`.** Before v0.27 it streamed any topic named in the query
string, so a per-user topic reached anyone who asked for it. The handler now
rebuilds the model the visitor's app would hold: `init ()`, the `withRequest`
seed when the app has one, and every session field from the verified
`sky_sid` cookie (never from the query string). This is the same model the
RPC handlers and the console gate build (`verified_model_decl`). It runs the
app's `subscriptions` on that model and streams `<t>` only when the resulting
`Sub` names it (`Spa_subAllowsTopic`, `runtime-go/rt/spa_push.go`). Anything
else, and an empty topic, gets 403. No app change is needed.

The check is fail-closed:

- A subscription that depends on a model field the backend cannot know (a
  room the user navigated to, a filter in the client state) is computed from
  `init`'s value, so that topic is refused. Key such topics on an identity
  field (`"user:" ++ session.userId`), or subscribe to a topic `init` already
  names.
- A `Std.App` app carries its `subscriptions` into a backend-only
  `spaSubscriptions_` binding, whatever the App record held. A hand-written
  `Spa.app` entry is read by its top-level `subscriptions` name. With neither,
  the backend refuses every topic and `sky spa-split` prints a warning.

Tests: `runtime-go/rt/spa_rpc_guard_test.go` (the guard, method-keyed CSRF
exemptions, Secure over TLS, the topic check) and `spa_split_flow.rs`
(`spa_rpc_origin_guard_and_sub_topic_authorisation`, a live backend built from
`tests/fixtures/spa-sub-auth`).

## 22. Server arms that match inside their Msg arguments (2026-09-28)

A server arm may match inside the arguments of its message. Every pattern
shape splits: a nested constructor (`Report (Ok line)`), an Int or String
literal (`Pick 0`, `Named "admin"`), a tuple (`Pair ( 0, s )`), a record
(`Take { id, label }`), an `as` binding (`Wrap ((Just n) as whole)`) and a
wildcard (`Any _`).

**The rule: each arm keeps its side, and a server arm sends the whole
message.** The partition classifies each arm of `update`'s `case msg of`
(`BranchVerdict::arm`). The client `update` keeps every arm in its source
order. A client arm is copied as written. A server arm sends the message to
`POST /_rpc/<Ctor>`, and the backend runs the app's own `update` on it. The
backend takes the same arm the client took: every arm before it failed to
match on the client, Sky patterns are pure, and the arms keep their order, so
those arms fail on the backend too. There is one route per constructor
(`spa_partition::server_routes`). Its request and response are the union of
the I/O of the constructor's server arms, because any of them can be the arm
that runs. A client arm of a server constructor adds nothing to the route: a
message it matches never leaves the client.

**Positional arguments.** A constructor sends its arguments positionally when
one of its server arms matches inside an argument, or when it has more than
one server arm (their binder names can differ). The client wraps each argument
of each server arm in an `as` binding and sends the bound values:

```text
Report (Ok line) ->            client:  Report ((Ok line) as spaArg0_) ->
                                            ( model, Spa.rpc … { spaArg0_ = spaArg0_ } … )
                               backend: update (Report p.spaArg0_) m
```

The argument types are the constructor's declared types
(`ArmShape::ctor_args`), instantiated at the `case` subject's type, and each
gets a wire codec from the resolver (§15). A constructor whose only server arm
binds plain names (`Report result ->`) keeps the named form
(`update (Report p.result) m`), so the output for such an app is unchanged. A
tuple argument has a codec since this change: a JSON object keyed by position
(`{"0": …, "1": …}`), built with `Codec.object` / `Codec.field`.

The wire diagram, the OpenAPI document and the differential fuzzer read the
same routes. The fuzzer's harness writes the constructor's arms in order,
diffs each server arm through the wire and answers `Ok ()` for a client arm.

**What is still refused, and why.** A server arm cannot be split when the
backend cannot rebuild its message from a request:

- An argument whose type has no wire codec: a function, a data-carrying
  union with no `Codec` binding, an anonymous record (the resolver's errors in
  §15). The fix the error names is a `Codec <T>` binding or a named type.
- An argument whose type is still a type variable after instantiation (a
  polymorphic `Msg a` handled generically). No value of an unknown type can be
  decoded. The error names the arm.

The generator also fails closed when the `case` it rewrites does not have the
arms the partition classified (it never guesses which arm runs where).

Tests: `project/tests/spa_server_arm_args.rs` (every shape, the client arms
kept, one route per constructor, the plain form unchanged);
`spa_diff_harness` unit tests (a client arm keeps its place and is not
diffed); `spa_split_flow.rs`
`server_arms_that_match_inside_their_msg_arguments_behave_as_the_live_app`
(the fixture `tests/fixtures/spa-arm-patterns` built as a Sky.Live app and as
`web:app`, each message sent to both, the models compared); and
`fuzz_verb_flow.rs`
`the_split_oracle_diffs_server_arms_that_match_inside_their_arguments`.

## 23. Scope, types and shapes the split must carry (2026-09-29)

A downstream app found ten places where the split lost or misplaced part of a
program that builds as Sky.Live. Each rule below replaces a text or bare-name
heuristic with a fact from the resolver or the lexer.

**Every kernel symbol family is decided.** The partition reads the runtime
symbol of `Ffi.kernel "<Family>_<fn>"` and of `Ffi.callPure` / `Ffi.call`.
Families without a `hir::KERNEL_MODULES` pseudo-module used to fall to the
fail-closed default (server), so a pure `Bytes.slice` became an RPC. Now:

| Side | Families |
|---|---|
| Client effect | `Native`, `WebSocket` (the client holds its own socket, §20), `Nav` (§24) |
| Client (pure) | `Bytes`, `Decimal`, `Compression`, `DbDec` (Std.Db.Decode decoders), `Spa`, the `Std.Html` render helpers |
| Client members of a mixed family | `Csv` parse / encode; `Money` formatting and allocation; `Std.Config` decoders; `Std.Db.Table` descriptions; `Time` formatting, parsing and calendar arithmetic (UTC or a named zone; the runtime embeds `time/tzdata`); `Std.App` view conversions |
| Server | `Schema`, `Analytics`, `Cache`, `Email`, `PubSub`, `HttpStream`, `ServerStream`, `ServerWebSocket`, `Trace`; `Money`'s FX-rate table (`setRate`, `getRate`, `hasRate`, `clearRates`, so `convert`); `Csv.parseStreamFromFile`; `Config.loadFromFile` and the `Sky.Config` builders; the `Table` queries; `Time.now` / `unixMillis` / `sleep` / `every`; `Std_App_livePort` |

`spa_partition` `ffi_symbol_families_are_all_decided` fails on a family that is
in none of the lists, and on a listed member no stdlib module binds.

**Exclusion is by definition, per module scope.** A server-tainted binding is
removed from the client by its `DefId`. The names a module writes bare that
resolve to a tainted def are read from the resolver (`ref_occs`), so a
server-only `Net.send` no longer removes the entry's own `send`. The report
names the excluded binding with its module (`Net.send`).

**A type the client names keeps its module in the client.** A module with a
server function used to be left out of the client whole unless the client
reached one of its pure functions. Now any type or constructor a client module
names (`type_occs`, constructor references) keeps a client copy of the module:
its type declarations and pure defs, never a tainted def.

**`Shared` brings the types its copied records name.** A wire record copied
into `Shared` can name a type of another module (`at : Shape.Point`). The
references are read from `type_occs` inside the copied declarations: a pure
module is imported under the alias the record uses (`import Shape as S
exposing (..)`); a type of a server module is copied into `Shared` too, and
each qualified reference to it is rewritten to the bare name; a stdlib type
keeps its module's import line.

**A result Msg with captured arguments crosses whole.** `Cmd.perform task (Got
url)` is not a client-result RPC (§19), whose answer is the task result alone:
it is a follow-up (§20), whose answer is the Msg `Got url result`. The stdlib
`Http.HttpResponse` has a wire codec (`Codec.auto` over its three fields).

**Patterns are read with the lexer.** A constructor pattern's CST node can own
the comment lines above the arm. The split reads the head and the arguments
from non-trivia tokens, so a comment never changes how an arm is read.

**`init` and `update` in any form.** The generated backend and frontend call
`init ()` and `update msg model` by name. The synthesis defines them at top
level whatever the `App.app` record held: an inline, `let`-bound or hoisted
lambda becomes a function (its body kept, so the split reads its `case` and its
model expression); a function under another name is copied under the canonical
name; an eta-expanded `update` is `update` itself; anything else is
eta-expanded. A module that already defines a different top-level `update` is
an error naming both.

**A model that is not a record crosses whole.** The wire contract is per model
field. A `String`, union or `List` model rides in one field, `spaModel_`, and
every server branch reads and writes it whole. A wildcard or pattern model
parameter (`update msg _ =`) is bound under an `as` in the regenerated
`update`.

**The user's source is checked first.** A `Std.App` build for any target first
type-checks the entry as written (`App.run` not rewritten), so an error in it
is reported at the user's line, never at a rewritten `App.runLive` or in the
synthesised or generated code. A record-literal argument whose field does not
fit is reported at the field. The `Std.App` builders fix `init`'s seed to
`()`, so `init : Page -> …` is that one error, on every target.

Tests: the fixtures `spa-client-crypto-aead`, `spa-pure-kernels`,
`spa-tainted-name-scope`, `spa-server-module-types`, `spa-shared-foreign-type`,
`spa-captured-result-msg`, `spa-arm-comment`, `spa-inline-init`,
`spa-nonrecord-model` and `std-app-init-seed-error`, each driven by a
`spa_split_flow.rs` test that builds the backend and the wasm client; where a
server branch runs, the test sends it to the split backend and to the Sky.Live
build of the same source and compares the answers. Unit tests:
`spa_partition` `ffi_symbol_families_are_all_decided`,
`client_crypto_opt_in_makes_the_keyed_crypto_primitives_placement_neutral`,
`a_comment_above_an_arm_is_not_part_of_its_constructor`; `app_entry`
`config_fields_get_their_canonical_top_level_name`.

## 24. Dependency types, type identity and navigation (2026-09-29)

**The split loads the dependencies the build loads.** The analysis db
(`build::load_source_db`, which the split, `sky spa-partition`, the diagrams
and the fuzz harnesses read) loaded the fetched registry packages under
`.skydeps/` but not the local `[dependencies]` path packages. A record alias
from a path package then named a missing module in the synthesised client
entry, and the split failed with `[update] type mismatch: Point vs record` on a
program `sky check` accepted. Both loaders now share
`build::load_registry_dependencies` (registry packages, trusted) and
`build::load_path_dependency_sources` (path packages, loaded as app modules:
type-checked, and reported under their own path). The analysis db registers
path modules as dependencies, not project modules, so the split treats them as
the dependencies the generated projects declare.

**Every type is resolved by its module, never its bare name.** The codec
resolver keyed the project's records and unions by bare name, and read a
`Msg` payload's types from the syntax, which drops the qualifier. A payload of
the stdlib's `Cpace.Pending` (device key material) was derived a codec as the
app's own `type alias Pending`: the build failed, and the "a key never crosses
a wire" rule was decided on the wrong type. Now:

- a `Msg` payload's types are resolved through the resolver in the declaring
  module (`ty::World::variant_arg_types_resolved`), so they carry their
  module (`Std.Crypto.Cpace.Pending`);
- the record and union shapes are keyed by qualified name; a qualified name
  matches exactly one declaration, and a bare name (declaring module unknown)
  matches only when one declaration has it;
- a record field's default is classified from its resolved type, a user
  `Codec T` binding matches by nominal identity (`ty::nominal::same`), and the
  `Shared` type-copy seed takes only project types;
- the key-on-wire rule reads the resolved name, so it refuses the real key
  whatever the app calls its own types.

**A dependency's record crosses the wire.** A record alias from a
`[dependencies]` package (path or registry) is external: both generated
projects carry the dependency, so `Shared` imports its module under a
generated alias (`import Geo.Shape as SpaTy_Geo_Shape_`), names the type
`SpaTy_Geo_Shape_.Point`, and derives its codec (`autoGeo_Shape_PointCodec_`).
Two different project records with the same bare name on the wire are refused
with a message naming both (the generated module names a project type by its
bare name).

**An error in derived code names the user's construct.** The app's own source
type-checks before the split runs, so a type or name error reported from the
synthesised entry or a generated leg is a defect of the split. The build maps
each one back (`sky/src/split_diag.rs`): a definition carried over unchanged
is reported at the user's file and line; a rewritten one at the user's
definition; a generated one (`spaEncodeFollow_`, `spaModelBlank_`, …) names
the construct it came from (`Msg`, `Model`, `init`, the routes) and where it
is. "The failure above is in the SYNTHESISED client entry" is gone.

**Navigation (`Std.Nav`).** `Nav.pushUrl` / `Nav.replaceUrl` /
`Nav.clearFragment` are a client effect: an arm that only navigates stays in
the client, and the wasm client applies the History API and routes like a
link click (`spaNavigate`). A server arm that also navigates keeps its
navigation leaf in the client: the leaf is lifted out of the arm's command and
run when the client sends the request (the same residual `Std.Native` uses),
as Sky.Live runs it when the update returns. The backend's copy of the leaf
is then already applied. A navigation the split cannot isolate (a helper that
returns it) is refused at build time, never dropped. A continuation arm that
navigates is never settled on the server.

Tests: the fixtures `spa-path-dep-record` (with its `lib/` package),
`spa-follow-bare-name` and `nav-cmds`, driven by the `spa_split_flow.rs` tests
`a_record_alias_from_a_path_dependency_crosses_the_split`,
`a_follow_up_payload_type_is_resolved_by_its_module_not_its_bare_name`,
`a_device_key_on_a_continuation_is_still_refused`,
`a_server_arm_navigation_runs_in_the_client` and
`a_server_arm_navigation_the_split_cannot_isolate_is_refused`; the unit tests
`spa_split::type_identity_tests` and `split_diag::tests`; the browser e2e
`scripts/nav-e2e.sh` (Sky.Live and web:app, Chrome and WebKit, strict CSP).
