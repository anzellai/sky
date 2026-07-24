# Sky Self-Hosting — Feasibility & Architecture (v1 track)

> **Branch:** `feat/self-hosted-compiler`
> **Status:** ANALYSIS — grilled draft. No implementation until §7 grill is
> survived and the user signs off on scope.
> **Mandate:** Get the architecture right *upfront*. The prior attempt (v0.7.x)
> retreated after hitting language ceilings mid-way. This time the dead-ends are
> surfaced and decided before a line of the self-hosted compiler is written.

## 0. Headline verdict

**Self-hosting is feasible and was already achieved once.** Four independent
forensic passes (prior post-mortem, typechecker mutation, backend/codegen deps,
frontend + gap audit) converge:

- **It already worked.** v0.7.x reached a verified **3-pass fixpoint bootstrap**:
  ~29k LOC of Sky, 2,933 declarations, 15–17 examples building, 6 MB single
  binary. The full source survives at **`legacy-sky-compiler/src/Compiler/*.sky`**
  (and git `896ce28c`).
- **Zero hard language BLOCKERS** on the core compile path
  (Parse → Canonicalise → Type → Lower → Emit → Fmt → Doc).
- Some mechanical fears dissolved (IORef sprawl is mostly gravestone comments;
  string-building is `[String]`-then-join O(n); laziness is a workaround-artifact;
  exceptions are all I/O-boundary → `Task Error`).
- **CORRECTION (grill, all 3 agents):** the "no `Std.Ref` needed for the
  typechecker" claim below in §2 was WRONG. The v0.7 compiler that "already
  worked" imported `Sky.Core.Ref` and used a **live mutable cell ~95× across 9
  files** (incl. a global mutable `typeAliasRegistry`, `Types.sky:freshVar`).
  `Sky.Core.Ref` is **deleted from modern stdlib.** The pure-threaded typechecker
  is therefore UNPROVEN, not "resolved." See §7.
- **The real risks are two, and both are now historically grounded:** the
  type-system-self-check ceiling (#R1) and build reproducibility (#R2). The
  grill (§7) targets these.

The question was never "can we." It is "can we do it **without re-hitting the
five things that killed v0.7.x**" — and modern Sky has already fixed the biggest.

---

## 1. Prior-attempt post-mortem (v0.7.x, April 2026)

Lived on this same branch name; sources moved to `legacy-sky-compiler/` by the
Haskell-rewrite commit `67fc860d` (2026-04-17). Ran ~Apr 2 → Apr 17.

**How far it got:** fully self-compiled (3-pass fixpoint). Subsystems in Sky
(LOC at `896ce28c`, `src/Compiler/`, 17,269 lines; full legacy tree ~29,272
across 33 files incl. LSP/Formatter/FFI/Main):

| Subsystem | ~LOC |
|---|---|
| Lexer + Token | 547 |
| Parser (Parser/Expr/Core/Pattern/Ast) | 2,153 |
| Canonicaliser/Resolver + Env | 1,609 |
| Typechecker (Infer/Unify/Types/Checker) | 2,660 |
| Exhaustiveness | 539 |
| Lowering/codegen (Lower 4268, Pipeline 3562, LowerTyped 1166, …) | 9,432 |
| ADT registry | 259 |

**The five things that killed it** (and their status in modern Sky):

1. **Map-based `any`-boxed ADTs → no exhaustiveness, no tag safety.** The
   `getDeclName` panic (`896ce28c`: match on 4 `GoDecl` variants; a malformed
   map value → missing key → `nil` → panic) and the struct↔map thrash (2 reverts
   in ~50 min) are both symptoms. The struct form failed on a **"Dict ordering
   bug in tag index assignment"** — integer tags where constructor-site and
   match-site disagreed on order because order came from an unordered `Dict`.
   → **✅ FIXED.** Modern Sky emits typed structs aliasing `rt.SkyADT` with a
   stable tag registry + `_fieldIndex` sorting + wildcard-`any` soundness gate
   (`a1842ceb`). Also directly foreshadows risk **#R2** (determinism): the tag
   bug *was* a Go-map-ordering bug.
2. **Compiler too weak to catch its own bugs** — 33 type-safety gaps in audit;
   "no HKT, no type classes, no row-poly made the compiler's own invariants
   brittle." → **⚠️ #R1** — partially persists. Typed codegen closed the
   `any`-boxing half; HKT/typeclasses remain absent by design. **This is the
   deep risk.**
3. **FFI-inspector non-reproducibility** (`f6e3ecdd`): "self-hosting on CI
   produces different output due to platform-dependent Go inspector results" →
   degraded to a hand-committed Go blob. → **⚠️ #R2** — build-model decision.
4. **Parser fragility** — indentation-sensitive, manual column tracking.
   → addressable; modern legacy parser reformulation in §4.
5. **Error quality** — cryptic Go errors vs Sky diagnostics. → product concern,
   not a feasibility blocker.

**Definitive verdict quote** (`67fc860d`): the self-hosted compiler "hit
fundamental ceilings: type safety (33 gaps; couldn't catch bugs in itself),
error quality, parser fragility, no single-binary release." Note #1 and its
ADT-representation cause are now fixed — so this verdict is **partially stale**,
which is the whole reason a second attempt is rational.

---

## 2. Typechecker — the mutation crux (CONTESTED — see §7 correction)

> ⚠️ The analysis below is a *theoretical portability* argument. The grill (§7)
> proved the **actual** v0.7 typechecker used `Sky.Core.Ref` pervasively (deleted
> from modern Sky), so "no `Std.Ref` needed" is unproven, not resolved.

Every mutable cell reduces to three buckets, all pure-replaceable:

- **Fresh-Int supply** (`Constrain/Expression.hs:133-139` counter; `Unify.hs:42`
  `rowExtCounter`) → one State-threaded `Int`.
- **Monotonic accumulators** (`Solve.hs` `_locals`, `_callInstances`,
  `_regionVars`, `_varCache`, `_solverSteps`) → append/last-write registers in
  threaded State. Solver already returns `(Maybe String, SolverState)` — **errors
  are values, no exceptions.**
- **Union-find** (`UnionFind.hs`) — the only genuinely mutation-shaped part — is
  used **linearly (solver never backtracks)**, so a persistent `Map Int Node`
  keeps path-compression-by-rebuild + union-by-rank at a ~log-factor cost. Net
  **~O(N log N)**; softened by per-module solving. No exceptions, no laziness in
  `Type/`.

**One real design task:** `Variable` currently uses IORef *pointer identity* →
becomes a **unique-`Int` scheme** (fresh-supply already exists). Vestigial
Descriptor `_rank`/`_mark`/`_copy` (Elm pool machinery) are unused — delete.

**Precedent:** the v0.17 IORef-defusing (Env fields, `UnifyState`,
`SolvedTypes`) already ran this exact IORef→pure migration at scale, zero
behaviour change.

---

## 3. Backend / codegen (RESOLVED: mechanical `CompileCtx` threading)

- **Live mutable surface is tiny:** the "480 IORefs" are gravestone comments;
  one live `newIORef` (`scopeStateRef`) + two monotonic caches. Pure successor
  datatype **`CompileCtx.hs` already exists** — just not threaded everywhere.
  Class-A push/pop = Reader-style parameter passing; Class-B accumulate =
  register-on-first-mention.
- **String building** — already `[String]`-then-join → `String.concat`, O(n)
  (`String_*` are `strings.Builder`-backed). *Discipline: keep "build a list,
  join once"; naive `append`-in-a-fold is O(n²).*
- **Laziness** — load-bearing only as a prop for IORef-in-pure-code
  (`seq`/NOINLINE/`readIORefNoCse`). Strictness **deletes** it; no lazy algorithm.
- **Exceptions** — all I/O-boundary → `Task Error`; 9 `error` calls are
  impossible-case assertions → `Task.fail (CompilerBug)`.
- **Ord-keyed Maps** — Sky `Dict` uses runtime `rt.cmp`, so tuple keys work; but
  **ADT keys must be stringified** (see §4 gap #5) and **`cmp` panics on ADTs**.
- **New gap surfaced:** **Template-Haskell asset embedding**
  (`EmbeddedRuntime.hs` bakes `runtime-go/` + `sky-stdlib/` into the binary).
  Sky has no metaprogramming. Options: (a) a Sky embed directive (new primitive),
  (b) sibling-files read at runtime (changes single-binary model), (c) codegen a
  Sky module with assets as string literals. **Decision needed (→ §5/#R2).**

---

## 4. Frontend + master gap table

**Frontend:** nothing fundamentally blocked.
- **Parser** (`Parse/Primitives.hs`) is a CPS parser with a **rank-N type** →
  reformulate to explicit `ParseResult a = POk a State | PErrConsumed | PErrEmpty`
  (HM-only). Typeclass instances → named combinators (`Parser.map`/`.andThen`).
  No `do` → explicit pipelines (verbose). Backtracking is bounded consumed/empty.
- **Canonicalise** already threads env explicitly + **topo-sorts let-bindings
  (no knot-tying)** — already written the way strict Sky requires.
- **Format** = strict Wadler-Lindig `Doc` ADT (not lazy). Clean port.
- **Doc** = pure render; `ToJSON` → hand-written encoders.
- **LSP** = the outlier: 8+ IORefs, `forever` stdin loop, `forkIO` background
  checker with shared mutable state. **DEFER for v1** — not on the compile path.

**Master gap table** (BLOCKER / ERGONOMIC / NON-ISSUE):

| # | Gap | Severity | Closure |
|---|---|---|---|
| 1 | Mutable `Std.Ref` | ERGONOMIC | Not required for core path; add minimal `Ref` only when LSP in scope |
| 2 | State monad / implicit threading | ERGONOMIC | Thread tuples/accumulators; verbose but total |
| 3 | Efficient string build | **NON-ISSUE** | `List String` + `String.concat` (O(n), Builder-backed) |
| 4 | Deep non-tail recursion stack safety | **NON-ISSUE** | Depth ∝ nesting not file length; Go stacks grow ~1 GB. Add a parser nesting-depth guard |
| 5 | Structural (ADT) Dict keys | ERGONOMIC | `keyToString` projection per site; compiler keys are String/Int → non-event |
| 6 | **Deterministic Dict/Set iteration** | **ERGONOMIC — sneakiest** | Go-map order is randomized + Sky doesn't sort. RULE: never emit by iterating; `Dict.keys \|> List.sort` first. Add `Dict.toListSorted` |
| 7 | panic-catch in pure code | NON-ISSUE | Failure modeled as `Result`/parser-err; effects wrapped at Task boundary |
| 8 | Generic traversal w/o typeclasses | ERGONOMIC | Monomorphic named combinators; no deriving |
| 9 | `compare`/Ord on ADTs | ERGONOMIC | Comparator projecting to primitive key (`List.sortWith`) |
| 10 | Unique-int supply | ERGONOMIC | Thread an `Int` (or 1-line `Std.Ref`) |
| 11 | CLI arg parsing | NON-ISSUE | `System.args` + pure walker |
| 12 | File/process I/O driver | NON-ISSUE | `File`/`Process`/`System`, all Task |
| 13 | Rank-N (CPS parser) | ERGONOMIC | Explicit `ParseResult` ADT |
| 14 | where / custom ops / HKT | NON-ISSUE | `let…in`, named fns, monomorphic combinators |
| 15 | Long-running stateful+concurrent LSP | BLOCKER *if in scope* → **resolved by DEFERRAL** | `Std.Ref` + supervised-goroutine model, later |

**Two things to design around (not blockers, but they bite):**
- **Determinism (#6)** — highest-risk silent gap; *was itself a historical CI
  killer* (§1 #3 + the tag-order bug). Make the sorted path the default.
- **Verbosity (#2, #8)** — every env/counter threaded by hand; every typeclass a
  named combinator. Expect the self-hosted source noticeably more explicit than
  the Haskell.

---

## 5. Enabling primitives to implement on this branch ("what's missing")

Ranked. Each ships with a verification gate before it's "done."

| P | Primitive / change | Why | Blocking? |
|---|---|---|---|
| **1** | **Determinism kit**: `Dict.toListSorted` / `Dict.keysSorted` / `Set.toListSorted` + a lint/discipline that compiler code never iterates unsorted for output | Kills the #R2 nondeterminism class at the stdlib level; directly answers the v0.7 CI killer | Yes — foundational |
| **2** | **Reproducible build model** (→ #R2): decide FFI-inspector determinism + asset-embedding (§3). Leading option: the compiler's *own* sources are FFI-light → compile with a pinned/checked-in FFI surface; ship runtime+stdlib via option (c) codegen-string-literals or (b) sibling files | The other historical CI killer | Yes — foundational |
| **3** | **Parser nesting-depth guard** | Stack-safety insurance vs pathological inputs | Cheap, do early |
| **4** | **`Std.Ref` (minimal, Task-tier)** — `Ref a` + `new/get/set : … -> Task Error …`, runtime `*atomic.Value` | Removes threading verbosity; **required only for LSP** | **DEFER** — build core path pure first to *prove* the pure model |
| **5** | Unique-int supply convention (State-threaded) | Fresh vars / gensym | Convention, not a primitive |

**Deliberate non-goals for v1 self-host:** LSP (#15), `sky doc --serve`/`console`
web mini-apps, `sky watch` file-watching daemon. Keep those on the Haskell binary.

---

## 6. Architecture decision + phase ordering

**Strategy — informed resurrection, not blank rewrite.** `legacy-sky-compiler/`
is 29k LOC that *already self-compiled*. But it used the doomed map-ADT model and
the fragile parser. So: **port subsystem-by-subsystem onto modern-Sky idioms**
(typed struct ADTs, exhaustiveness, determinism kit, reformulated parser),
validating each against the Haskell compiler's output as an oracle. Strangler,
never big-bang.

**Bootstrap staging (permanent):** Haskell compiler = **stage 0**, kept as the
bootstrap + differential oracle *indefinitely*. Self-hosted binary must reproduce
stage-0's Go output byte-for-byte on the example corpus before it's trusted. This
is the anti-#R1 safety net: the Haskell type system keeps guarding correctness
while the Sky compiler is validated against it.

**Phase order (each phase = self-hosted subsystem that diff-matches stage 0):**
1. **P0 — Foundation:** determinism kit (§5/P1) + reproducible build model
   (§5/P2) + parser depth guard. Land on this branch first; these are stdlib/
   toolchain changes, independently shippable.
2. **P1 — Lexer + Parser** (reformulated, HM-only). Oracle: token/AST dump parity.
3. **P2 — Canonicalise** (explicit env threading; already topo-sorted).
4. **P3 — Typechecker** (pure State + unique-Int union-find). The crux; oracle:
   inferred-type parity on the corpus.
5. **P4 — Lowering + Go emit** (`CompileCtx` threading; typed struct ADTs). Oracle:
   **byte-identical Go** on all examples.
6. **P5 — Fmt + Doc.** Then **P6 — 3-pass fixpoint** re-established + CI reproducibility gate.

**Circuit-breakers (N-strikes per CLAUDE §0.3):** if any phase fails diff-parity
3× on the same lever → stop, re-classify against this doc, escalate. Each phase
commits locally; push only at Judge-verified phase boundaries (§0.1).

**Risk register:**
| ID | Risk | Mitigation | Kills project if… |
|---|---|---|---|
| #R1 | Sky's type system can't keep a 30k-LOC compiler correct (the 33-gap ghost) | Stage-0 differential oracle guards every phase; modern typed ADTs + exhaustiveness (the v0.7 `any`-box cause is fixed) | …bugs the Haskell caught are systematically un-catchable in Sky AND the oracle can't cover them |
| #R2 | Non-reproducible self-compiled output | Determinism kit (P0) + pinned FFI surface + byte-diff gate | …FFI inspector is inherently platform-variant on the compiler's own deps |
| #R3 | Union-find log-factor perf unacceptable | Per-module solving; measure vs stage 0 | …compile time regresses >10× on skyshop-scale |
| #R4 | Verbosity makes the Sky source unmaintainable | Named-combinator conventions; measure LOC delta | …source balloons past ~2× Haskell |
| #R5 | AI-corpus is near-zero for Sky (the original motivation!) | You're the world expert + exhaustive docs; but honestly assess vs Rust | …AI velocity on Sky < Haskell, defeating the premise |

---

## 7. Adversarial grill log — THREE agents, all landed. Plan does NOT survive as written.

### Grill #R1 (type-system self-check) — VERDICT: **NOT MITIGATED. Live dead-end.**
The 33-gap v0.7 failure was **accepts-too-much (soundness)**, and neither plan
pillar addresses soundness:
- **R1-D1 [airtight]** The differential oracle is structurally blind to
  *rejection parity*. It only runs on programs both compilers ACCEPT and emit Go
  for; an ill-typed program the Haskell rejects emits no Go → nothing to
  byte-compare. The corpus is 15–17 **well-typed** examples, **zero rejection
  tests.** The v0.7 killer ("couldn't catch bugs in itself" = failed to reject)
  is exactly the class the oracle cannot observe.
- **R1-D2 [airtight]** The doc MISDIAGNOSED the cause. The real holes are
  algorithmic shortcuts in the legacy unifier, present in the **already-struct-
  based** source — `Unify.sky:99-100` `isOpaqueFfiType a && isOpaqueFfiType b ->
  Ok emptySub` makes **every pair of unrelated FFI types unify** (`Customer` ≡
  `Widget`, `Token` ≡ `Iter`); plus `isUniversalUnifier`, string-suffix nominal
  identity. Typed ADTs + exhaustiveness **cannot** fix these — they are checker
  *logic*, not data representation. Haskell does it soundly via
  `Unify.hs:isFfiInterfacePair`/`implementsInterface`; the oracle can't tell
  whether the port reproduces the sound or the permissive version.
- **R1-D3 [proven, and a real WIN]** Modern Sky exhaustiveness (`[E3001]`
  hard-error on missing arms) is genuinely **stronger than GHC-as-configured**
  (`sky.cabal:97` ships `-Wno-incomplete-patterns` — the Haskell compiler does
  NOT warn its own missing arms). BUT it's silenced by wildcards, and the legacy
  code to be resurrected is wildcard-saturated (`Lower.sky` 33, `Pipeline.sky`
  17, `LowerTyped.sky` 17, `Types.sky` 15; `Types.sky:typeToGo` ends `_ -> "any"`
  → a new `Type` ctor silently emits `any`). Only holds if the port is
  **rewritten wildcard-free** — which "strangler resurrection" does not commit to.
- **R1-D4 [proven]** No redundancy/reachability analysis in Sky (Haskell has
  `-Woverlapping-patterns`). Dead-arm compiler bugs are invisible to a
  self-hosted checker *and* to the oracle.
- **Minimum bar to call #R1 mitigated:** (1) a **rejection corpus** (ill-typed
  programs, both compilers reject with matching diagnostics); (2) port committed
  **wildcard-free** (lint, not hope); (3) self-hosted `Unify` reproduces
  `isFfiInterfacePair`-grade nominal soundness, independently verified.

### Grill #R2 (reproducibility) — VERDICT: **splits; (A) tractable-unbuilt, (B) collapses.**
- **(A) Compiler self-compile** is verified **FFI-free** (all `Compiler/*.sky`
  import stdlib + internal only) and byte-exact is valid (no timestamp/banner in
  emitted Go) — but ONLY under strict, unbuilt conditions.
- **(B) Example-corpus oracle collapses:** `.skycache/` is gitignored, **zero
  `.skyi` committed**, so CI regenerates the platform-variant inspector FFI fresh
  — *literally the f6e3ecdd killer, unchanged.* Inspector is platform-variant
  (differing symbol sets) AND run-to-run nondeterministic (unsorted `Implements`
  slices, `sky-ffi-inspect/main.go:341`).
- **"Sort keys before iterating" is wrong at the top-stakes site:** record fields
  must sort by `_fieldIndex` (source order), not lexical — `Dict.toListSorted`
  would be deterministic-but-WRONG, reproducing the v0.7 tag-order corruption.
  Coverage unprovable (59× `Map.toList` in `Compile.hs`; a green byte-diff
  doesn't prove clean — Go randomizes per run → needs multi-seed × multi-toolchain
  diffing).
- **Asset embedding:** no option keeps determinism AND single-binary — (c) 4.5 MB
  string-literal Sky module blows the HM heap + escaping hazards; (b) breaks
  single-binary (a named v0.7 killer); (a) is undecided net-new language surface.

### Grill #R5 (over-optimism + strategic fit) — VERDICT: **strategically inverted.**
- The "already worked / mechanical / no-Ref" framing is stale: the working
  artifact used the now-deleted `Sky.Core.Ref` and emitted **pre-v0.13 Go** the
  modern oracle rejects on line 1; parity target is the **24k-line modern
  `Compile.hs`** (type-directed lowering, Go generics), not the legacy backend.
- Real size **40k–70k LOC of Sky**, multi-quarter; §8 rules forbid legacy idioms
  wholesale (`Result String` ×141, `-> ()` I/O ×16 vs Task-everywhere).
- **The kill-shot:** the user's goal is **AI-assisted velocity** against
  compiler/tooling/LSP holes. Self-hosting moves the hardest 24k lines into a
  language with **~zero AI training corpus** (Sky is the user's own language) and
  **defers Sky's own LSP** — regressing the exact axis the user cited to buy an
  axis they never raised (self-referential credibility). Opportunity cost is
  severe: SkyDeploy (commercial) rides on a stage-0 that keeps moving.

## 8. FINAL VERDICT & RECOMMENDATION

**The upfront analysis did its job: it found the dead-ends before a line was
written.** Self-hosting is *technically* achievable for the FFI-free
self-compile target under demanding, currently-unbuilt conditions — but as the
**v1 path motivated by AI velocity, it is the wrong call**, on evidence:

1. **#R1 is a live dead-end**, not mitigated — the oracle can't see soundness
   regressions, and the real v0.7 holes were unifier logic that typed ADTs don't
   touch. Closing it needs a rejection corpus + wildcard-free port + independently
   verified sound unification (none in scope).
2. **#R2(B) reproduces the exact CI killer** until FFI is committed + frozen.
3. **#R5**: it *regresses* the stated goal (AI velocity) and defers the tooling
   the user complained about.

**Recommended path (in order):**
- **A. Split `Compile.hs` (24,436 lines) now, in Haskell.** A single file too
  large for any model to hold in context IS the concrete AI-velocity "hole."
  Zero product risk; continues the `CompileCtx`/IORef-defusing work already begun.
- **B. If the real intent is to leave Haskell → spike Rust**, which delivers the
  cited holes (sound ADTs, exhaustiveness, best-in-class LSP/diagnostics) WITH a
  large AI corpus.
- **C. Reserve self-hosting as a separately-budgeted v2 credibility milestone** —
  a business call, scoped after Compile.hs is split and the product is stable,
  with the §7 minimum bars (rejection corpus, wildcard-free, sound unify,
  committed FFI) as explicit entry gates. Never funded from the AI-velocity
  motivation it defeats.

**One genuine, unhedged positive to carry forward:** modern Sky's compile-time
exhaustiveness (`[E3001]`) is stronger than the Haskell compiler as configured
(R1-D3) — a real language win worth advertising, independent of self-hosting.
