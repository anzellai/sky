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
