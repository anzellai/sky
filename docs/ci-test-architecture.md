# Sky CI/CD + Test Architecture

> ## ⚠️ SUPERSEDED by [`docs/ci-test-architecture-v2.md`](ci-test-architecture-v2.md)
>
> This document was one of **two** parallel designs (the other is
> `docs/ci-corpus-proposal.md`). Both were adversarially grilled and both
> returned 5 blocking findings. **v2 is the single reconciled design; where this
> document conflicts with v2, v2 wins.** This file is retained for its evidence
> — the measured gate defects, the surface inventory, and the CI topology audit
> — not for its conclusions.
>
> Corrections v2 makes to this document (see v2 §0):
> - **§0 Finding 1 / §4.2 are wrong about the cost.** The per-case tax is
>   `World::build`, not `SourceDb` construction; the stdlib parses are already
>   hoisted and the per-item db work is 0.2 %. **Route A is a no-op on the
>   dominant term.** v2 §1.
> - The "three measurements, one consistent model" corroboration is a sampling
>   artefact (dropped); the `resolve` "58 world rebuilds" claim is falsified
>   (measured 0.164 s total).
> - `sky check` ≡ `sky build` — the "cheap, no `go build`" premise for
>   `examples/` is false (v2 §9.3).
> - Batching is **not** semantics-preserving for four case families (v2 §3.2).
> - Falsifiability must be **per item**, not per gate (v2 §4).
> - T1 = `setup + max(jobs)`; the 9-minute arithmetic omitted `setup` (v2 §8).
> - The stdlib denominator is 1,744 / 1,623 / 121, not 1,762 / 1,640 / 122;
>   "assertion" is redefined in v2 §5.4.
> - `DEGRADED` is deleted as a state; the BlueDB harness is **built**, not
>   adopted (v2 §7).
>
> **Status:** design, for grilling. Not implemented. Supersedes nothing until
> §8's phases land.
> **Mandate:** `.claude/AUTONOMOUS_GOAL.md` (verbatim user goal + the
> 2026-08-09 REFINEMENT that makes the corpus two layers).
> **Branch:** `feat/ci-test-overhaul`, off `main` @ `08557e50`.

---

## 0. The decision, in one page

The user asked for three things: coverage that would have **caught the bugs we
actually shipped**, a CI that is **time-aware**, and **no corner-cutting**.
Those are not in tension, because almost all of today's CI time is spent on
duplicated and false work rather than on assertions.

**Three findings drive the whole design. All three are measured, not asserted.**

**Finding 1 — the corpus is compiled ~12× per push, and the per-item cost is
~100 % fixed overhead we do not need to pay.** Measured on this worktree with
the release-built `xtask`:

| Gate | Corpus | Wall time | Per item |
|---|---|---|---|
| `roundtrip` (parse only, no world) | 173 files | **0.138 s** | 0.8 ms |
| `divergences` (2 items × full world) | 2 files | **2.716 s** | — |
| `reject` (63 items × full world) | 63 files | **81.6 s** | — |

Solving the two-point linear model `t = s + n·X` from `divergences` and
`reject`: **X = 1.293 s per case, s = 0.130 s fixed startup.** The model
predicts the 63-case run at 81.6 s against a measured 81.6 s, and predicts a
startup of 0.130 s against the independently measured `roundtrip` startup of
0.138 s. Three measurements, one consistent model.

`X` is **not the cost of checking the case**. The case's own parse is 0.8 ms.
`X` is the cost of tearing down and rebuilding an 87-module stdlib world for
every single corpus item, because `reject_gate.rs:187-190`,
`infer_gate.rs:148-151`, `resolve_gate.rs:101-109` and
`ty/tests/reject.rs:81-88` each construct a fresh non-memoising
`hir::SourceDb` (`rust/crates/hir/src/db.rs:44-49` — a plain `Vec` +
`HashMap` + `RefCell<DefTable>`, no salsa) *inside the per-item loop*.

> **This single fact decides the whole design.** At 1.293 s/case, the
> combinatorial Layer 1 the user is asking for is impossible — 5 000 cases
> would be 108 minutes. Against a **shared, pre-built world**, the marginal
> cost of a case is its own parse plus its own inference: bounded above by
> 15 ms (the stdlib's own average per-module check cost, and stdlib modules are
> far larger than a test case), expected 2–5 ms. The same 5 000 cases become
> ~25–75 s single-threaded, and under 20 s on four cores.
>
> Layer 1 is feasible **if and only if** we fix the world-rebuild. That is
> §4's central item, and it is a pure-speed fix that removes no assertion.

**Finding 2 — the gates cannot be trusted to fail.** The mandate catalogues 23
demonstrated false-green/false-red defects. I reproduced four of them directly
on this branch:

```
$ ./rust/target/release/xtask definitely-not-a-gate ; echo $?     → 0
$ ./rust/target/release/xtask ; echo $?                           → 0
$ ./rust/target/release/xtask build-run --only=zz-nonexistent     → "TOTALS | build 0/0 | run-ok 0/0 | examples 0", exit 0
$ ./rust/target/release/xtask build-run --shape=live --run ; echo $?  → 0   (the `=` form is silently ignored)
```

Every CI gate is invoked as `cargo run -q -p xtask -- <name>`
(`rust-ci.yml:77-85, 99-101, 115, 144-167, 184-186, 251-253, 265`). A typo in
any of them is a permanently green no-op. And `scripts/verify-all-web.sh:86`
still reads `if node … | tail -8; then` on this branch — the console e2e gate
tests `tail`'s exit status and **cannot fail**.

Point-fixing 23 defects does not solve this; a 24th will appear. §5 replaces
the *shape* of a gate with a registry contract, reusing the working precedent
already built on `feat/bluedb-v2`.

**Finding 3 — examples are the wrong corpus, and the coverage they give is not
the coverage we think.** 58 example directories; **22 shipped stdlib modules
are imported by no example at all** — including `Std.Jobs` (while
`18-job-queue` hand-rolls a queue on `Std.Db`), `Sky.Core.Uuid`,
`Sky.Core.File`, `Sky.Core.Path`, `Sky.Core.Bytes`, `Sky.Core.Set`,
`Std.Email`, and the entire `Std.Db.{Migrate,Schema,Table,Decode}` family.
Meanwhile 16 modules have exactly **one** example carrying them, and 10 CLI
examples share the byte-identical import signature `{Sky.Core.Prelude,
Std.Log}`. We are paying ~12 full compiles per push for a corpus that is
simultaneously redundant and full of holes.

### What this design does

1. **Two corpora, Layer 1 primary** (§2). Layer 1 is systematic combinatorial
   variation over the language + stdlib + builtins, generated and table-driven,
   sized in the thousands of cases, running per-push. Layer 2 is six
   real-world-shaped projects exercising surfaces in combination.
2. **One corpus walk, many assertions** (§4). Emit once per item into a
   content-addressed artifact; every gate becomes an assertion over artifacts.
   Share the stdlib world across cases. Build `xtask` release.
3. **A gate registry that cannot lie** (§5). Four states, mandatory falsifying
   mutations, a canary, unknown names are errors, no shell pipelines as gate
   entry points, machine-readable results instead of `grep`.
4. **Per-push budget: 9 minutes wall-clock**, down from 31–34 (§3), with the
   arithmetic shown, and *more* assertions running than today.

### What this design refuses to do

Remove an assertion to go faster. Every gate that exists today keeps a
successor, and §2.4 carries an explicit **coverage-loss ledger** showing, for
each thing deleted, the named replacement that subsumes it.

---

## 1. Surface inventory

For each surface: what can regress, and what class of test can actually catch
it. Where something is currently untestable, it says so.

Legend for **Catchable by**: `L1s` = Layer-1 static case (compile-only
assertion), `L1e` = Layer-1 emit-shape assertion (read the generated Go, no
`go build`), `L1b` = Layer-1 behavioural case (`sky test` assertion, batched),
`L2` = Layer-2 real-world project drive, `RT` = Go runtime unit test,
`UT` = Rust unit test.

### 1.1 Compiler pipeline

| Surface | What regresses | Catchable by | Today |
|---|---|---|---|
| **Parse** (`syntax`) | error recovery, reprint fidelity, span accuracy | `L1s` roundtrip + `UT` | `xtask roundtrip` (183 files, 0.14 s — cheap and good) |
| **Canon / name resolution** (`hir`) | import-alias resolution, qualifier rules, same-name collisions, cross-module refs | `L1s` accept/reject over the **import-shape axis** | `xtask resolve` — but it loads the *whole* example dir, so two modules named `Main` in `39-hub-demo` collide and one is silently never resolved (`resolve_gate.rs:51`, `hir/src/db.rs:73-77`) |
| **Type inference** (`ty`) | over-reject, under-reject, row-poly, annotation-vs-body divergence | `L1s` accept + `L1s` reject | `xtask infer` (accept), `xtask reject` + `ty/tests/reject.rs` (reject, 63 cases, run twice) |
| **Lowering** (`lower`) | erasure at ABI boundaries, record-update field drop, fieldset selection | **`L1e`** — the highest-value untapped class | nothing direct; caught only when a runtime panic happens to be in an example |
| **Codegen / emit** (`codegen`) | `any` where fully typed, `rt.Coerce` growth, non-determinism | `L1e` + `repro` + `coerce-floor` | `xtask repro` (byte-stability), `xtask coerce-floor` (token census) |
| **Go build of emitted code** | emitted Go that does not compile | `L1e` batch-build + `L2` | `build-run` — but it **excludes examples that fail to emit** (`build_run_gate.rs:1488`, the `r.emitted &&` conjunct), so an example that stops emitting drops out silently |

> **The single biggest coverage gap in the compiler is `L1e`.** Bug classes
> #166 (record-update drops fields), #171 (row-poly through `foldl` zeroes
> fields), #173 (`Dict k (List Record)` returns empty), and the `goty.rs`
> record-fieldset collision are all **"compiles clean, behaves wrong"** — they
> are invisible to `build-run` and to the differential oracle. But every one of
> them is *visible in the emitted Go* as a wrong struct selection, a stray
> `any`, or a dropped field. Asserting on the emitted Go costs no `go build`
> and no run, and converts the most expensive bug class into the cheapest test.

### 1.2 Type soundness + codegen determinism

| Surface | What regresses | Catchable by | Today |
|---|---|---|---|
| Reject-parity (unsound accepts) | checker accepts an ill-typed program | `L1s` reject corpus | `reject` — **but both faces assert `>= 13` against an actual 63** (`ty/tests/reject.rs:121, 147`). Deleting 50 corpus files keeps it green. |
| Byte-stable emit | map iteration order leaking into output | fresh-process re-emit | `xtask repro` — correctly guards the vacuous pass (`repro_gate.rs:400-407`) |
| Coercion floor | `rt.Coerce` growth | token census vs golden | `xtask coerce-floor` — correctly guards vacuity (`coerce_floor_gate.rs:449-451`) |
| Robustness | panic on malformed input | mutation fuzz | `xtask fuzz` — budget exhaustion is printed but **never fails** (`fuzz_gate.rs:696-700`) |

### 1.3 Runtime (`runtime-go/rt`)

> BlueDB is **not on this branch** — it lives on `feat/bluedb-v2` / `exp/bluedb`
> and only appears here as documentation. When it merges it arrives with its own
> 48-gate registry (§5), which is the harness this design generalises.

**210 `_test.go` files, 1 369 `Test*` funcs.** CI runs
`CGO_ENABLED=0 go test ./rt/...` (`rust-ci.yml:146, 255`) — a pattern that
misses **22 test funcs entirely**: `rt/webview_test.go` (12 funcs, build-tagged
`cgo && darwin`, unsatisfiable while CI sets `CGO_ENABLED=0` on *both*
runners) and `rt/jobs/postgres_store_test.go` (10 funcs, gated on
`SKY_PG_TEST_URL` which is set nowhere — and excluded a second time because the
integration job targets `./rt/` rather than `./rt/...`). `rt/live_js_syntax_test.go`
also skips silently because no workflow installs Node, and
`runtime-go/cmd/sky-hub/` is outside `./rt/...` so CI never compiles it at all.

| Surface | What regresses | Catchable by | Today |
|---|---|---|---|
| Coercion / reflect helpers | `rt.Coerce`, `AsListT`, `AsMapT` fallbacks | `RT` | covered |
| Panic classification | a new panic class | `RT` + `L1b` | covered |
| Webview / cgo | native window, loopback assets | `RT` needs **cgo** | **untestable in CI today** — `CGO_ENABLED=0` everywhere |
| Jobs / Postgres | queue semantics on Postgres | `RT` + `L2` | `integration-postgres` runs `go test -tags integration ./rt/ -run Postgres` — note `./rt/` not `./rt/...`, so subpackages are excluded |

### 1.4 Stdlib (87 modules)

| Surface | What regresses | Catchable by | Today |
|---|---|---|---|
| Per-function semantics at boundaries (negatives, unicode, TZ, int64 width) | `Money.allocate` residue, `Json.Decode.int` platform width, `Bytes` rune-vs-byte, `Time.addMonths` year-carry, `Uuid.parse` never `Just`, `Auth.passwordStrength` panic | **`L1b`** | `tests/conformance/` — the right idea, but small; and **22 modules have no example and no conformance suite** |
| Algebraic laws (functor/monad identities) | law-breaking refactors | `L1b` property cases | not covered |
| Cross-backend parity of `Std.Ui` | same `Element` renders differently on Live / Tui / Webview | `L2` tri-backend project | `38-composite-ui-multibackend` exists and is **in no sweep table** |

### 1.5 Sky.Live

| Surface | What regresses | Catchable by | Today |
|---|---|---|---|
| SSE lifecycle, reconnect-resync | strand / desync after redeploy | `L2` browser drive | `verify-live-resilience.mjs` — invoked by **no workflow** |
| Session store (memory / sqlite / redis / postgres) | cross-instance session loss | `L2` × store matrix | only memory + sqlite, never in CI |
| CSRF + idle survival | the darraghstudio idle-403 incident | `L2` browser drive (~80 s) | `verify-all-web.sh:155` — CI-unreachable |
| Forms, `sky-nav`, history | double-push, back-button | `L2` browser drive | CI-unreachable |
| Multi-tab / pub-sub fan-out | one tab updates, the other doesn't | `L2` two-context drive | `verify-pubsub-multitab.sh` — CI-unreachable |

> **The entire Playwright tier is invoked by no workflow.** `rust-ci.yml` names
> no `verify-*` script; `nightly-sweep.yml:59` runs only `example-sweep.sh`.
> This is the largest *unreachable* body of real coverage in the repo, and §7
> puts it back in CI rather than rewriting it.

### 1.6 Std.Ui / Tui / Webview

| Surface | Catchable by | Today |
|---|---|---|
| Layout primitives, flex chains | `L2` + computed-style assertions | `verify-ui-showcase.sh` on `26-ui-showcase`, CI-unreachable |
| Visual regression | snapshot diff | 22 PNGs in `examples/26-ui-showcase/snapshots/`, CI-unreachable; a missing baseline self-blessed as a pass |
| Tui keystroke interaction | **PTY drive** | not covered — `verify-cli.sh` `tui-start` only spawns and looks for a panic; `build_run_gate.rs:981`'s hang branch is dead code because `wait_bounded` never returns `None` (`:1023-1053`), so a wedged TUI reports OK |
| Webview | `L2` on macOS + cgo | `--shape webview` is **never run in CI** |

### 1.7 Std.Db / Persist / Codec / Auth

| Surface | Catchable by | Today |
|---|---|---|
| Dialect-safe DDL, migrations | `L1b` + `L2` × {sqlite, postgres} | `Std.Db.{Migrate,Schema,Table,Decode}` have **zero example coverage**; `36-composite-server` hand-rolls migrations |
| Codec round-trip (JSON + DB from one def) | `L1b` property cases | partial |
| Store partial update | `L1e` + `L1b` | `55-store-partial-update` (one case) |
| Auth: bcrypt, JWT, cookies, session hijack | `L1b` + `L2` | `tests/Auth/`, no runner |

### 1.8 Observability / console

Embedded `/_sky/console`, exporter, hub tiering, `ENV=production` gate.
`verify-console-e2e.mjs` exists and is invoked from a pipeline that **cannot
fail** (`verify-all-web.sh:86`). `34-multi-tier-console` and `39-hub-demo`
are the topology examples; `39-hub-demo` is **built by nothing at all**.

### 1.9 LSP

Better covered than expected: **88 test fns across 13 files**, including 7 that
drive the real server binary over JSON-RPC stdio. `xtask lsp` additionally
shells `scripts/lsp-test-nvim.sh` (49 Neovim editor-parity cases: 17 symbol-class
+ 32 corpus covering cross-module resolution, the import shapes, editor-visible
diagnostic codes+ranges, and a real `examples/` app) — but **unbounded**
(`lsp_gate.rs:46-50`) and **exit 0 when `nvim` is absent** (`lsp_gate.rs:20-27`).

Genuine gaps: `prepareRename`'s *success* path (only the decline path is
tested, `scenarios.rs:331`), `completionItem/resolve`, `semanticTokens/range`
and `/delta`, `workspace/didChangeWatchedFiles`, `$/cancelRequest`. And
structurally — **`server.rs` implements no `did_close` and no `did_save`**
(`:135` `did_open` and `:151` `did_change` are the only document-lifecycle
handlers), so stale diagnostics on a closed document are not merely untested,
they are unimplemented. `sky-lsp/src/lib.rs` has exactly **one** unit test for
the whole analysis engine; everything else is integration-level.
`sky-lsp` also holds the only long-lived salsa db in the tree
(`sky-lsp/src/lib.rs:74`), and incremental-invalidation correctness under edits
is covered by just 2 tests (`incremental.rs`).

### 1.10 CLI tooling

Verbs dispatched at `rust/crates/sky/src/main.rs:45-69`: `build check run fmt
test lsp clean init doc watch db add remove install update doctor
upgrade-claude verify console console-serve upgrade`.

| Verb | Catchable by | Today |
|---|---|---|
| `build` / `check` | `L1e`, `L2` | heavily covered |
| `fmt` | `L1s` idempotency + comment-multiset | `xtask fmt` over 270 files — good |
| `test` | `L1b` | drives conformance |
| `db init/gen/migrate/status/seed/push` | `L2` × dialect | **no gate** |
| `add/remove/install/update` (Go FFI deps) | `L2` FFI project | only via example builds |
| `doctor`, `upgrade`, `init`, `watch`, `clean` | CLI-drive cases | **no gate** |
| `doc` | doc-export non-empty assertion | `build-docs-site.sh` deploys even if 0 module pages are written |

### 1.11 FFI / cgo

`sky-ffi-inspect` pins `GOOS=linux/amd64 CGO_ENABLED=0`
(`rust/crates/ffi/src/inspect.rs:169-173`). Consequence:
`11-fyne-stopwatch` cannot generate a surface on macOS and is skipped as a GUI
example on Linux — **it is verified on no platform**, and because
`example-sweep.sh:450` counts `SKIP` as `pass`, it reads green. Large-surface
FFI (13-skyshop, 76 k symbols) is real coverage and must be kept.

### 1.12 Docs

`scripts/doc-examples.sh` `sky check`s doc code fences — but only those whose
first line matches `^module Main[ (]` (`:53`). Measured on this tree: **284
live `elm`/`sky` fences exist and exactly 13 pass the filter — the gate
verifies 4.6 % of documented code.** Partial/library snippets are excluded by
design, but that means the overwhelming majority of doc examples can rot
silently.

Worse, it has no zero-total floor: with `total=0` it prints
`0/0 … GATE: PASS` and exits 0 (`:84-92`). Three realistic ways to reach 0
without noticing: a docs reorg changing the `find` path; a fence gaining an
info string (`` ```elm title=Main.sky ``) since the regex is end-anchored
`^```(elm|sky)[ \t]*$`; or the `module Main` convention drifting. Compare
`conformance.sh:69-72`, which correctly `exit 2`s on `ran == 0`.

### 1.13 Honest "currently untestable" list

1. **cgo / Fyne surface generation** — structural (the inspector's pinned
   target). Not a test problem; a compiler-tooling problem. §9 keeps it
   `BLOCKED`, never `SKIP`.
2. **Native Webview window behaviour** — needs a macOS GUI session. The
   loopback asset pipeline *is* testable headlessly; the window is not.
3. **Real cross-instance deployment** (sticky sessions across replicas) —
   approximable with two local processes + a shared store; not identical to
   production.
4. **`sky upgrade` self-replacement** — mutates the running binary; testable
   only in a throwaway container.

### 1.14 Orphaned test estate (exists, runs nowhere)

Beyond the 398 Sky assertions and 22 Go funcs already noted:

- **`legacy-haskell-compiler/test/`** — 201 `*Spec.hs` files, ~1 014 `it`
  cases, declared as `test-suite sky-tests` in `sky-compiler.cabal:240`. The
  only runner is `scripts/cabal-test.sh`, which no workflow invokes. The
  Haskell compiler is the differential oracle, so this is *deliberately*
  dormant — but it should be an explicit `BLOCKED` row, not silence.
- **`test-files/`** — 75 `.sky`, reachable only through
  `scripts/build.sh --self-tests`, which no workflow calls.
- **`legacy-ts-compiler/`, `legacy-sky-compiler/`** — vitest configs, no runner.
- **5 of 23 shell scripts under `scripts/` are reachable from CI** (6 counting
  `lsp-test-nvim.sh` via `xtask lsp`). Notably `scripts/test-ci.sh` — named
  "CI release gate" — is invoked by no workflow.
- **`cargo test --doc`** (`rust-ci.yml:66`) runs over a workspace containing
  exactly **one** doctest.

None of this is load-bearing today. All of it is *reported as if it were*,
which is the same failure mode as a green skip.

---

## 2. The corpus — two layers

The user's refinement is explicit: **Layer 1 is primary.** It is the layer that
would have caught the bugs we actually shipped. Layer 2 catches the class
Layer 1 structurally cannot.

### 2.0 Why examples fail as a regression corpus

Not an opinion — the measured shape of `examples/`:

- **58 directories**, of which the build corpus is 55 (`simple` and `test_pkg`
  are excluded by both xtask gates and have no `entry` in their `sky.toml`).
- **29 of 58 are absent from `scripts/example-sweep.sh`'s table** (the table
  has 29 entries; its own header still claims "canonical 20-example fence").
- **22 shipped stdlib modules are imported by no example**: `Sky.Core.{Basics,
  Bytes, Char, File, Io, Path, Set, Uuid, WebSocket}`, `Sky.Http.Middleware`,
  `Std.{Compression, Config, Email, Jobs, Markdown, Trace, Live.Console,
  Ui.Events}`, `Std.Db.{Decode, Migrate, Schema, Table}`.
- **16 modules have exactly one example carrying them** — including
  `Std.Ui.Keyed` (keyed diffing, the highest-risk Live surface),
  `Std.Cli` (the entire surface = one 103-line example), and
  `Sky.Http.Server.WebSocket` (whose sweep entry probes plain HTTP `/` and
  never opens the `/ws` upgrade).
- **10 CLI examples share the byte-identical import signature
  `{Sky.Core.Prelude, Std.Log}`** and total 262 lines between them. Nine are
  typechecker/codegen regressions wearing a project costume: each carries a
  `sky.toml`, a `go.mod` resolution, a full `go build`, and a slot in the
  ~12×-per-push compile census — to assert one thing about lowering.
- **`39-hub-demo` is built by nothing**: no top-level `src/` so both xtask
  gates skip it, absent from the sweep, absent from `coerce_floor.golden`.
- **`11-fyne-stopwatch` is verified on no platform**, and reads green because
  `example-sweep.sh:450` scores `SKIP` as `pass`.
- **21 examples set `port = 8000`**; three hard-bind it in Sky source that no
  env var can move (`05-mux-server/src/Main.sky:45`,
  `08-notes-app/src/Main.sky:495`, `15-http-server/src/Main.sky:13`).

They are documentation samples being used as a regression corpus. They serve
neither role well: as docs they are redundant, as a corpus they are
simultaneously duplicative and full of holes.

### 2.1 Layer 1 — combinatorial coverage of language + stdlib + builtins

**The thesis, in the project's own words.** `docs/rust-rewrite/13-change-verification-and-edge-cases.md:25-30`:

> "The pattern is always the same: **the corpus lacks the specific
> combination** … A synthetic repro that 'matches the shape' is not enough — it
> reproduces the shape you *thought of*."

Layer 1 varies **by construction** rather than by what an example happened to
use. It has three sub-kinds with very different costs:

| Kind | What it asserts | Cost per case | Needs `go build`? |
|---|---|---|---|
| **L1s — static** | accepts / rejects; inferred type; reprint; fmt idempotency | ~3 ms (bounded ≤15 ms) | no |
| **L1e — emit-shape** | assertions on the *generated Go*: no stray `any` in a fully-typed expression; the record-update keeps every field; the right struct is selected; `rt.Coerce` sites are in the documented floor | ~5 ms | **no** |
| **L1b — behavioural** | stdlib semantics at boundaries; algebraic laws | amortised — many assertions per compiled binary | yes, but **batched** |

> **L1e is the design's highest-leverage idea.** #166, #171, #173 and the
> `goty.rs` fieldset collision are all "compiles clean, behaves wrong" — the
> class that `build-run` and the differential oracle are both blind to. Each is
> nonetheless *visible in the emitted Go*. Asserting on emitted text needs no
> `go build`, no run, no port, no browser — it turns the most expensive bug
> class into one of the cheapest tests we have.

#### 2.1.1 The axes

Mined from the actual bug history (issues #51, #54, #153–#173, the conformance
findings C1–C3, and the regression examples). Each value below is one that a
real shipped defect required.

| # | Axis | Values |
|---|---|---|
| A1 | **Import & module shape** | single-module · 2 modules qualified · bare import, unqualified ctor · `exposing (T)` · `exposing (T(..))` · alias == last path segment · **alias ≠ last path segment** · nested/dotted path · external `.skydeps` dep · same-named module in project *and* dep · transitively-pulled stdlib module · alias + `exposing` together |
| A2 | **Name-collision kind** | none · same alias name ×2 modules · alias vs union of same name · **app type name == stdlib type name** · same ADT *and* variant names ×2 modules · **identical field-NAME set, different field TYPES** · user type shadowing a prelude ADT · two bare imports binding one auto-qualifier |
| A3 | **Type nesting / shape** | scalar · closed record · anonymous record · record with function-valued fields · parametric record alias · parametric alias as a field · ADT · parametric ADT · generic alias field inside an ADT variant · ADT payload = `Maybe` (nested pattern) · tuple 2/3 · tuple ≥4 · `List Record` · `Dict` with Int/Float keys · **`Dict k (List Record)`** · `Dict String String` as a record field · record in ADT variant in parent model |
| A4 | **Row state** | closed · genuinely row-poly · **open row from a subset field-access view** · concrete record sharing an instantiation row var · **update touching ALL fields** vs **update touching a SUBSET** · open row on the erased `List.map` path |
| A5 | **Erasure context** | none · HOF callback (`map` / `foldl` / `foldr` / `find`) · pipe `\|>` stage boundary · erased `[]any` map-chain · ADT payload slot typed `any` · Dict value slot / `AsMapT` fallback · generic type-param slot · zero-arg CAF reference · `Task.andThen` continuation · **Tui gob round-trip** · reflective codec decode · Std.Db row decode |
| A6 | **Syntactic position** | top-level def · **let-bound local fn** · lambda · **function-parameter pattern** · tuple destructure with ignored elements · record pattern in a case arm · record update · record literal · `.field` as a value · **record update returned inside a tuple** · ctor as a first-class value · kernel as a first-class value |
| A7 | **Annotation state** | fully annotated · unannotated · bare type var · anonymous-record annotation · head-position alias · annotation more general than body · wildcard `any` · declared arity ≠ body arity · local let-fn (unannotatable) |
| A8 | **Higher-order / application shape** | first-order · passed to a HOF · **`foldl` vs `foldr` vs `map` (distinct values)** · `>>` / `<<` · nested lambda chain · stepwise partial application · over-application · **variadic Go kernel fully applied** · **variadic Go kernel partially applied** · recursive let helper |
| A9 | **App / runtime shape** | *stratifier, not a cross factor* — CLI · Sky.Cli · Sky.Live · Sky.Tui · Std.Db · Store/Persist · FFI-heavy · nested TEA |

#### 2.1.2 The generation strategy — full-cross where it paid, pairwise elsewhere

A naive cartesian product of A1–A8 is astronomically large and mostly
meaningless. The bug history says exactly where the density is. Counting
distinct shipped defects per co-occurring axis pair:

| Rank | Axis pair | Distinct shipped bugs |
|---|---|---|
| 1 | **A5 erasure × A3 type nesting** | ~13 |
| 2 | **A4 subset-update × A6 position** | ~9 |
| 3 | **A1 module shape × A2 collision kind** | 7 |
| 4 | **A7 annotation × A4 row state** | 6 |
| 5 | **A8 higher-order × A5 erasure** | 5 |
| 6 | A8 kernel variadic-ness × application completeness | 2 of 4 cells |

So the generator uses **four strata**, each with a stated coverage guarantee:

**S1 — full cross on the three historically-proven triples.**
- *T1 = A7 annotation × A4 row × A5 erasure.* Justified by architecture, not
  just history: `ty::check` seeds annotated params from the signature and
  `ty::db::compute_body_types` (which feeds lowering) does not
  (`13-change-verification-and-edge-cases.md:88-92`) — so "what type-checks"
  and "what is emitted" can diverge along exactly this triple. #173 is the pure
  witness: identical body, the annotation flips the runtime result from correct
  to empty. **~6 × 6 × 10 ≈ 360 cases.**
- *T2 = A1 module shape × A2 collision kind × construct side (construction /
  pattern / annotation / field-set).* C3 is exactly this — construction already
  honoured `pinned_union_go`, patterns did not. **~12 × 8 × 4 ≈ 384 cases**
  (many cells are trivially valid; they still cost ~3 ms).
- *T3 = erasure source × container kind × consumer read-set.* Four source kinds
  (CAF / parameter / map-chain / `foldl` accumulator) × 3 containers × 2
  read-set relations. It is small and has a **proven 4/4 hit rate**: the
  DarraghStudio-#2 fix `bd18f1c5` closed only the CAF cell and `5914a111` was
  needed for the other three. **24 cases.**

**S2 — pairwise (t=2) covering array over all of A1–A8.** A covering array
gives every *pair* of axis values at least one case in ~150–250 cases rather
than the millions a full cross would need. This is the "systematic rather than
whatever an example happened to use" guarantee, and the coverage metric is
computable and reportable.

**S3 — pinned historical repros.** Every fixed issue becomes a permanent case,
carrying its issue number. ~80 cases (the 63-file reject corpus stays as-is —
see §9 — plus the regression examples migrated from `examples/35,40–51,53–55`).

**S4 — the neighbourhood of every pinned repro.** For each S3 case, generate
the variants that differ in **exactly one axis value**. This is the cheapest
high-yield strategy available and it is directly evidence-backed: `acf9d10d`
superseded `11630fc1` because the neighbouring cell (mixed-element-type tuple)
was untested; ex.53 had to be extended from 1 to 4 variants. **~80 × 8 ≈ 640
cases.**

**Layer 1 static total: ≈ 1 400–1 600 cases at landing, designed to grow to
~5 000** as the ledger fills.

#### 2.1.3 Stdlib coverage — a machine-checkable "100 %"

The user asked for 100 %. That is only meaningful if it is *measured*, so the
target is a **coverage ledger** with a computable denominator:

- **Denominator — and it already has a machine-readable source.** 87 stdlib
  modules exposing **≈1 762 public symbols (1 640 values/functions + 122
  types)**. This does **not** need new code: `sky doc --export <dir>` already
  writes `api/symbols.json` (`rust/crates/project/src/doc.rs:234`), enumerated
  by `module_symbols` (`doc.rs:604`), which parses each module, computes the
  `exposing_set` (`doc.rs:666` — returning "everything" for the 6 modules that
  use `exposing (..)`), and filters declarations by `is_exported`
  (`doc.rs:657`). The ledger reads that file. Corroborated independently: a
  raw scan of top-level type-annotated definitions across `sky-stdlib/` counts
  1 796 (a superset, since it includes non-exported helpers).
  Constructors exported via `Type(..)` are deliberately *not* in the 122
  (`doc.rs:664-666`); if the ledger wants them it must separately enumerate
  `UnionVariant` nodes — an explicit, stated gap rather than a silent one.
- **Numerator:** each public symbol must have **≥1 L1s case** (its type is
  exercised) and **≥1 L1b case** (its behaviour is asserted). Boundary
  obligations — negatives, zero, unicode, TZ, platform int width — are declared
  per-function in the ledger.
- **The gate:** `xtask gate stdlib-ledger` fails if any public symbol has zero
  cases.
- **Exemptions are explicit and counted.** A symbol that genuinely cannot be
  covered (platform-gated `Std.Webview` entry points, kernel verbs that are
  lowered specially rather than being runtime functions) carries an exemption
  row with a reason and an owner. The gate reports **covered / exempt /
  uncovered** and the true percentage. It never reports 100 % by redefining the
  denominator.

> **The in-tree precedent, and the lesson it already learned.** The right model
> is `rust/crates/project/tests/kernel_surface.rs:99`
> (`migrated_kernel_modules_have_sky_source_and_runtime_symbols`): a checked-in
> table (`kernel_surface.rs:31-64`, 8 modules / 41 bindings) asserting each
> listed binding exists in `.sky` source *and* that every `Ffi.kernel "Sym"`
> has a matching `func Sym(` in `runtime-go/rt/*.go`.
>
> Critically, `kernel_surface.rs:14-16` records **why it is an allowlist rather
> than a total scan**: "a blanket scan of all 400+ stdlib `Ffi.kernel` symbols
> has legitimate exceptions (e.g. `Task.run` / `succeed` are lowered specially,
> not runtime funcs), which would false-positive here." That is the design
> lesson for a 100 % ledger — **totality requires an explicit exception list,
> not just an enumeration** — and it is why the exemption column above is
> load-bearing rather than an escape hatch.
>
> **Correction to `AGENTS.md:255-258`, which this design must not build on:**
> it documents `rust/crates/project/src/kernel_api.rs` and a
> `kernel_api_covers_registered_kernel_functions` gate. **Both were deleted**
> (`054f6d26`; recorded at `docs/v0.19/kernel-metadata-unification.md:279-285`)
> when `.sky` became the single source of truth. `AGENTS.md` is stale and is
> fixed in Phase 1 (§8).

**Same mechanism for the language.** The parser's kind space is
`SyntaxKind` (`rust/crates/syntax/src/kind.rs:15`): **124 variants**, of which
**72 are user-written construct nodes** (25 expr / 17 pattern / 11 type /
9 decl / 10 module-import; `kind.rs:96-171`). The ledger walks every corpus
file's rowan tree, collects the `SyntaxKind` set actually produced, and asserts
set-equality against the expected 72. Uncovered constructs are listed, not
hidden.

> **One cheap blocker to clear first:** `SyntaxKind::KINDS` (`kind.rs:20`) is
> the only total enumeration of the kind space and it is **private** — no test
> can reach it. Phase 1 makes it `pub`. Note also that the `can_cast`
> `matches!` lists (`ast.rs:155, 208, 297, 364`) are *not* compiler-checked, so
> a newly added `SyntaxKind` omitted from them is a live hole today; the
> set-equality assertion closes exactly that.

> **Honest statement of what "100 %" will and will not mean.** Definition-level
> coverage (every public function has cases) is reachable and will be reported
> exactly. **Combination coverage is not 100 % and cannot be** — the axis space
> is combinatorially infinite. What is guaranteed is: full cross on T1/T2/T3,
> all-pairs on A1–A8, and the one-axis neighbourhood of every historical bug.
> The ledger reports the pairwise coverage percentage as a number. Anyone
> claiming "100 % coverage" without those qualifiers is overclaiming, and this
> document declines to.

#### 2.1.4 Feasibility — the arithmetic that makes Layer 1 possible

| | Per case | 1 500 cases | 5 000 cases |
|---|---|---|---|
| **Today's architecture** (fresh `SourceDb` + 87-module world per item) | **1.293 s** (measured) | 32 min | 108 min |
| **Shared world**, conservative bound (15 ms) | ≤15 ms | 23 s | 75 s |
| **Shared world**, expected (3–5 ms) | 3–5 ms | 5–8 s | 15–25 s |
| …on 4 cores | | ~2 s | ~6 s |

Layer 1 is impossible under the current gate architecture and comfortable under
§4's. That is the whole argument for §4, and it removes no assertion.

**Batching L1b.** Behavioural cases need a real binary. One `go build` per case
would dominate everything (~1–3 s each). So L1b cases are `sky test` assertions
grouped into **~30–40 suite modules** (one per stdlib area), giving ~40
`go build`s for thousands of assertions. This is the existing
`tests/conformance/` shape, scaled up — not a new mechanism.

### 2.2 Layer 2 — six real-world-shaped projects

Each is an app a person would actually build; its surface set is the union that
*that product* naturally needs. Overlap is deliberate only where a surface is
load-bearing enough to want two independent witnesses.

| | Project | Shape | Uniquely owns | Absorbs |
|---|---|---|---|---|
| **P1** | **`storefront`** | Sky.Live + Std.Ui commerce app | **Large-scale Go FFI** (Stripe, 76 k symbols) · `Std.Money`/`Decimal` · `Std.Jobs` · `sky db` migrations · Sky-package dep (`.skydeps`) · static assets · `Std.Ui.{Keyed,Lazy,Responsive}` · **Postgres primary** | 13-skyshop, 37-composite-live-shop, 55-store-partial-update, 18-job-queue, 07-todo-cli |
| **P2** | **`forum`** | Sky.Live realtime | **SSE lifecycle + reconnect-resync** · pub/sub fan-out · multi-tab sync · `sky-nav` + history · forms · `Std.Auth` sessions + CSRF-idle · **session-store matrix** (memory/sqlite/redis/postgres) | 19-skyforum, 27-multi-session-chat, 12-skyvote, 09-live-counter, 10-live-component, 16-skychess |
| **P3** | **`api`** | Headless HTTP/JSON service | `Sky.Http.Server` routing + `Middleware` · `Http.RateLimit` · **WebSocket server *and* client** (round-trip) · SSE emit + `Http.Stream` consume · `Std.{Cache,Csv,PubSub,Persist}` · `Sky.Ffi` · small Go FFI | 05-mux-server, 15-http-server, 30-sse-server-demo, 32-sse-relay, 33-websocket-echo, 36-composite-server, 02-go-stdlib, 28-streaming-chat |
| **P4** | **`ops`** | Internal ops dashboard | `Std.Analytics` · embedded `/_sky/console` · **hub / exporter tiering** · `Std.{Log,Trace}` · **`ENV=production` gate** · admin auth · `Sky.Core.Process` | 17-skymon, 25-sky-console, 34-multi-tier-console, **39-hub-demo**, 52-blog-analytics |
| **P5** | **`devkit`** | Developer CLI + TUI | `Std.Cli` · **`Sky.Tui` under a real PTY** · `Sky.Core.{File,Path,Io,Bytes,Char,Set,Uuid}` · `Std.{Config,Markdown,Compression,Email}` · local **and** external Sky package · `sky test` · `sky db` CLI verbs | 20-cli-counter, 21/22/23/24-tui-*, 14-task-demo, 04-local-pkg, 03-tea-external, 00-standard-libs |
| **P6** | **`desktop`** | Desktop app | `Sky.Webview` · loopback asset pipeline · **tri-backend `Std.Ui` parity** (one view fn → Live + Tui + Webview) · cgo build path | 29-webview-threejs-spike, 31-webview-stopwatch-ui, 38-composite-ui-multibackend, 11-fyne-stopwatch (see §9) |

**Why this shape catches what sample apps do not.** #166's reporter could not
reproduce it in isolation and had to list the co-occurring legs: a sub-page
model with a `Status (List ExerciseDb)` field, populated via
`Task.run (Db.query …)`, nested inside a parent `AppModel` ADT variant,
alongside other fields, under nested-TEA dispatch. No single-purpose example
has that shape; a real product does, by accident, because products accrete.
P1–P6 are deliberately built to accrete the same way — nested TEA, DB-fed
models, multi-module views, an FFI boundary — so the combinations arise
naturally rather than being enumerated.

**Three conformance suites** sit beside the projects. They are harnesses, not
products, and calling them projects would be dishonest:

- **C1 `stdlib-conformance`** — Layer 1b's home, and it is **already half
  built**. `tests/conformance/tests/` holds 19 suites / **772 `Test.test`
  cases** driven by `scripts/conformance.sh` and wired into CI on both
  platforms (`rust-ci.yml:157, 269`) — this is the single healthiest test asset
  in the repo and §9 marks it must-not-touch. C1 absorbs it, plus
  `00-standard-libs`, plus the orphans — **22 root suites in
  `tests/{Auth,Core,Db,Hardening,Json,Lang,Live,Server,Sky,Std}` (335
  assertions) and 6 suites under `examples/*/tests/` (63 assertions), none of
  which has a runner** — and then grows to meet the 1 762-symbol ledger.
  Measured totals: **1 170 Sky assertions exist; 398 of them (34 %) never
  execute.** `tests/sky.toml` even declares `entry = "Core/CoreTest.sky"` and
  nothing reads it; the `examples/*/tests/` suites are parsed by `roundtrip`
  and `fmt` but never type-checked (`infer_gate.rs:67-71` scopes to `src/`) and
  never run.
- **C2 `ui-matrix`** — every `Std.Ui` primitive × computed-style + snapshot.
  Absorbs `26-ui-showcase` and its 22 PNG baselines.
- **C3 `repro`** — Layer 1s/1e's home. Absorbs `examples/{35,40–51,53–55}` and
  hosts the S3/S4 case families. The 63-file reject corpus stays where it is.

### 2.3 What happens to `examples/`

`examples/` **keeps its documentation role and loses its regression role.**

- **Purpose:** teaching. One example per concept, chosen for readability.
- **Owner:** docs. Changes ship with the doc that references them.
- **Gate:** `sky check` only (cheap — no `go build`, no run, no port), plus the
  existing `doc-examples.sh` live-docs gate, plus a new **reachability gate**:
  every example must be referenced by at least one doc page *or* be deleted.
  That is how "an example that rots must fail something" is enforced without
  paying `go build` for 55 projects twelve times a push.
- **Pruned:** the nine bug-repro examples with the `{Prelude, Log}` signature
  move to C3 and their directories are deleted — they teach nothing. `simple`
  and `test_pkg` are deleted (dead scratch, no `entry`, excluded by every
  gate).
- **Kept and merged:** the duplicative Live/TUI chains
  (`21 ⊂ 22 ⊂ 23 ⊂ 24`, `09/10`, `12/16/17`) collapse to one teaching example
  each; the others' *coverage* moves to P1–P6, not to nothing.

### 2.4 Coverage-loss ledger — what is lost, and what replaces it

The mandate's constraint 3: where a gate is removed, its coverage must be
**demonstrably subsumed**, and that must be shown. Every row below is a
deletion with its named replacement. **No row is closed until the replacement
is green and the falsifying mutation for the replacement has been run** (§5).

| Coverage today | Carried by | Risk if naively dropped | Replacement | Proof obligation |
|---|---|---|---|---|
| Local Sky package resolution | `04-local-pkg` | package path resolution untested | P5 ships a local package under `packages/` | P5 build fails if the package is removed |
| External Sky package (`.skydeps`) | `13-skyshop` (sky-tailwind) | dep fetch/lock untested | P1 keeps an external Sky pkg dep | P1 build fails on a corrupted `sky.lock` |
| **Small**-surface Go FFI | `02-go-stdlib`, `03-tea-external` | `safePkgName` aliasing bugs differ from large-surface ones | P3 declares 4–6 small Go deps | distinct from P1; both must exist |
| **Large**-surface Go FFI (76 k symbols) | `13-skyshop` | FFI-scale perf + generation regressions | P1 keeps Stripe/Firebase | FFI-surface regeneration timing tracked |
| Cross-module ADT / generics / row-poly | `35`, `40`–`50`, `53`–`55` | the highest-value compiler coverage in the repo | **C3 repro corpus**, one file each, plus S4 neighbourhoods | each migrated case must reproduce its original defect on a reverted fix |
| CLI stdout golden | `xtask golden` (24 goldens) | silent output drift | **kept as-is**, retargeted at C3 + P5 | golden count must not decrease |
| Std.Ui primitive matrix | `26-ui-showcase` + 22 PNGs | visual regressions | **C2**, baselines carried over unchanged | a missing baseline must FAIL, not self-bless |
| Sky.Live "click is a no-op" | `verify-all-web.sh`, `verify-scenarios.mjs` | the v0.13 event-emission class | P2 + P4 browser drives, same assertions | must run **in CI**, unlike today |
| Live resilience (desync, idle-80 s) | `verify-live-resilience.mjs` | the darraghstudio incidents | P2 nightly drive | reproduce both incidents on reverted fixes |
| Multi-tab pub/sub | `verify-pubsub-multitab.sh` | fan-out regressions | P2 two-context drive | — |
| Console e2e | `verify-console-e2e.mjs` | console wire breakage | P4 drive | currently in a pipeline that **cannot fail**; replacement must be falsifiable |
| Tri-backend `Std.Ui` parity | `38-composite-ui-multibackend` | one view fn diverging across backends | P6 | three renderers, one view fn, asserted |
| WebSocket | `33-websocket-echo` | upgrade path | P3 — and P3 **adds the client half**, closing a gap that exists today | `/ws` must actually be opened |
| Postgres session store | nothing in CI | cross-instance session loss | P2 store matrix (nightly) | new coverage, not replacement |
| `Std.Jobs`, `Std.Db.{Migrate,Schema,Table,Decode}`, `Sky.Core.{File,Path,Io,Bytes,Char,Set,Uuid}`, `Std.{Email,Config,Markdown,Compression,Trace}` | **nothing** | already uncovered | P1/P3/P5 + C1 ledger | new coverage |
| `11-fyne-stopwatch` cgo | **nothing** (verified nowhere) | — | see §9 — becomes `BLOCKED`, never `SKIP` | must be visibly not-green |

Net: three rows are *new* coverage for surfaces that have none today. No row is
a reduction.


---

## 3. Tiers and time budgets

**Measurement basis and derating.** All per-case timings in this document were
measured on the development host (Apple silicon, release build). GitHub's
`ubuntu-latest` is a 4-vCPU container and is materially slower for
single-threaded work. **Every budget below applies a 2× derating factor to
measured host numbers**, and says so where it matters. Budgets are *ceilings
enforced by `timeout-minutes`*, not aspirations — a tier that exceeds its
ceiling fails, which is what makes a budget real.

### 3.1 The tiers

| Tier | Trigger | Wall-clock ceiling | Rule for earning a place |
|---|---|---|---|
| **T0 — pre-commit** | local git hook | **60 s** | Only checks that need no build of anything but the changed file. Never blocks on network. |
| **T1 — per-push / PR** | `push`, `pull_request` | **9 min** | Deterministic, hermetic, no browser, no external service. **All of Layer 1 lives here** — that is the point. A gate earns T1 if it is deterministic *and* its falsifying mutation runs in T1 too. |
| **T2 — merge queue** | `merge_group` | **20 min** | Needs a browser, a second platform, or a service container; still deterministic. Happy-path Layer-2 drives. |
| **T3 — nightly** | `schedule` 06:00 UTC | **90 min** | Matrix explosions (session stores × dialects × platforms), long-hold timing tests (the 80 s idle-survival), visual snapshots, big fuzz, FFI-scale regeneration. |
| **T4 — pre-release** | `push: tags: v*` **and** manual preflight | **3 h** | Everything in T3 on every platform, plus asset build + install-from-asset verification. **A tag cannot publish unless T4 is green** (§7.4). |

> **T1 is the budget that matters** — it is the one a human waits for. Nine
> minutes is chosen because it is under the ten-minute threshold at which
> developers context-switch away from a PR, and because the arithmetic in §3.3
> supports it without dropping an assertion.

### 3.2 What runs in each tier

**T0 (60 s)** — `cargo fmt --check` on changed crates · `sky fmt --check` on
changed `.sky` · `cargo check -p <changed crate>` · the Layer-1 cases whose
declared axis values intersect the changed files · **the gate-name manifest
check** (§5) because it is a file read and costs milliseconds.

**T1 (9 min)** — everything below, in parallel jobs (§7.1):

| Job | Contents | Est. |
|---|---|---|
| `lint` | `cargo fmt --check` · **`cargo clippy -D warnings`** (today it is `\|\| true`, `rust-ci.yml:59` — zero enforcement) · `xtask gate s8` · gate-name manifest · stdlib + language ledgers | 4 min |
| `compiler-unit` | `cargo test --workspace` under a `ci` profile (opt-level=1, debug-assertions on) · LSP protocol suite | 6 min |
| `corpus` | **one walk** producing one artifact per item, then all static assertions over it: roundtrip · resolve · infer · reject · fmt · divergences · coerce-floor · **L1s (~1 500 cases)** · **L1e emit-shape** | 4 min |
| `emitted-build` | batch `go build` of the corpus walk's artifacts · repro (3 fresh-process emissions, byte-compare) · golden stdout | 7 min |
| `runtime-go` | `go test ./rt/...` **plus a `CGO_ENABLED=1` leg** (today cgo tests never run) | 5 min |
| `conformance` | C1 stdlib behavioural suites — all **1 170** assertions, including the 398 that have no runner today | 6 min |
| `projects-smoke` | build all 6 Layer-2 projects · boot-probe each (HTTP 200 / exit 0 / PTY no-panic) · tear down | **9 min** |

Critical path: `projects-smoke` at 9 min. Everything else finishes sooner.

**T2 (20 min)** — T1 plus: macOS leg (repro, golden, conformance, coerce-floor —
the must-not-touch both-platform set, §9) · `xtask fuzz` · full pairwise Layer-1
covering array · happy-path browser drive per web project (P2, P4) · Postgres
service integration · `examples/` `sky check` sweep + doc-examples +
example-reachability.

**T3 (90 min)** — T2 plus: session-store matrix (memory × sqlite × redis ×
postgres) for P2 · Std.Db dialect matrix (sqlite × postgres) for P1 · Live
resilience drives (desync + the 80 s idle-survival) · multi-tab pub/sub ·
C2 visual snapshots · P6 Webview on macOS with cgo · FFI-surface regeneration
for P1 · extended fuzz · `welltyped` differential against the Haskell oracle ·
**`--verify-falsifiers` over the whole gate registry** (§5).

**T4 (3 h)** — T3 on every platform · release asset build · **install the built
asset and run it** · anchored version + CHANGELOG check · full clean-slate
example sweep with the forced `sky install` (§9).

### 3.3 The arithmetic — how 31–34 min becomes 9

**Today, per push.** 9 flat parallel jobs, no fan-in
(`rust-ci.yml:34-269`; zero `needs:` in the file). Wall clock is the slowest
job: `test-ty` at **1 857 s ≈ 31 min**. The corpus is emitted **≈ 670 times**
across both platforms (≈368 Linux + ≈300 macOS) — **12.1× over** the 55-item
corpus. **14 of 16 cargo steps run DEBUG**, including every wall-clock-dominant
gate; `reject` alone measures **780 s debug vs 74 s release** for an identical
verdict.

Four multipliers, applied in order:

| # | Change | Mechanism | Effect |
|---|---|---|---|
| 1 | **Build gates release** | `--release` (or a `ci` profile) for `xtask` and the `sky` it drives; `lsp_gate.rs:97` currently hardcodes `target/debug/sky` | ~10× on every corpus gate. `reject`: 780 s → 74 s |
| 2 | **Share the stdlib world** | build the 87-module world once per process; add each case to a fork instead of a fresh `SourceDb` | **1.293 s → ≤15 ms per case** (measured tax, §0). `reject` 63 cases: 81.6 s → **~1.6 s** |
| 3 | **One walk, many assertions** | emit each item once into a content-addressed artifact; gates assert over artifacts (§4) | corpus emissions 670 → ~200 |
| 4 | **Right-size the corpus** | the 9 `{Prelude,Log}` bug-repros become ~5 ms C3 cases instead of full `go build` projects; Layer-2 is 6 projects, not 55 | ~49 fewer `go build`s per platform |

**Worked example — the current critical path.** `test-ty` is 1 857 s. Its
dominant term is `ty/tests/reject.rs`, which rebuilds the 87-module world for
each of 63 files (`reject.rs:81-88`) in a **debug** build — measured directly
on this machine at **823 s** for that single test
(`cargo test -p ty --test reject`, 14:02 wall). The rest of the job is largely
the same tax: **11 of `ty`'s 13 integration test files load and re-typecheck
the whole 87-module stdlib**. Applying (1) and (2): 63 × ≤15 ms + one world
build ≈ **2 s**. The critical path moves off `test-ty` entirely.

**Where the 9 minutes goes, and why it is not a cut.** More assertions run in
T1 after this change than before it:

| | Today (T1-equivalent) | After |
|---|---|---|
| Layer-1 static cases | 0 | **~1 500** |
| Layer-1 emit-shape assertions | 0 | **~1 500** |
| Stdlib behavioural assertions in CI | 772 | **1 170** (the 398 orphans get a runner) |
| Clippy enforcement | none (`\|\| true`) | `-D warnings` |
| cgo runtime tests | 22 funcs never run | run |
| Doc fences verified | 13 of 284 (4.6 %) | raised, and a floor added |
| Corpus emissions/push | ~670 | ~200 |
| Wall clock | 31–34 min | **9 min** |

**Honest risk on this number.** The 9-minute ceiling depends on
`projects-smoke` fitting in 9 minutes on a 4-vCPU runner, and P1 carries a
76 k-symbol FFI surface. That surface is content-hash cached
(`.skycache/ffi`), and §7.3 keys a CI cache on the Go dep set — but a
cache-cold P1 will not fit. **Mitigation, stated up front:** if the FFI cache
misses, `projects-smoke` degrades to the other five projects in T1 and P1's
full build is promoted to T2 for that run, and the degradation is *reported as
a distinct state* (`DEGRADED`), never as a pass. This is the one budget in the
document I would expect a griller to attack, and §8's Phase 6 measures it
before the tier is declared.

---

## 4. Killing the duplication, not the coverage

Every item here removes **repeated or fixed-overhead work**. None removes an
assertion. No gate is deleted for being slow.

### 4.1 The measured duplication

| Overlap | Evidence | Cost |
|---|---|---|
| `build-run --all` builds all 55, then `--shape cli/live/http/tui` rebuild ~52 more with no artifact reuse | `rust-ci.yml:148,150,163,165,167`; `--shape` implies the full corpus (`build_run_gate.rs:242-246`) | ~90 redundant emissions/push |
| `repro`'s `go_builds` duplicates `build-run --all` exactly — same `BuildOptions`, same `sky-out-rust` dir, same `go build` | `repro_gate.rs:302-316` vs `build_run_gate.rs:472-482` | 55 emissions + 55 `go build`s |
| `coerce-floor` re-emits all 55 purely to count `rt.*` tokens | `coerce_floor_gate.rs:130` | 55 emissions |
| `ty/tests/reject.rs` and `xtask reject` run the identical 63-file corpus | `reject.rs:16-17` says so explicitly | 63 world-rebuilds, twice; one face is the CI critical path |
| `resolve` and `infer` each independently parse the stdlib and every example; `infer` redoes everything `resolve` did | `resolve_gate.rs:101`, `infer_gate.rs:148` | 58 + 57 world rebuilds |
| `corpus()` is copy-pasted **byte-identically 3×**; `collect_sky` **6×**; `load_dir`/`module_name` **4×** | `build_run_gate.rs:282-296`, `repro_gate.rs:318-331`, `coerce_floor_gate.rs:460-473` | drift risk, not just time |
| Every pipeline re-parses + re-infers all 87 stdlib modules cold | `hir::SourceDb` has no memoisation (`hir/src/db.rs:44-49`) | **the dominant term** — 1.293 s/case |

### 4.2 The fix — one corpus walk, one shared world, one artifact

**(a) A shared, immutable stdlib world.** Build the 87-module world **once per
gate process**; each case is checked against a *fork* of it rather than a fresh
`SourceDb`. This is the single highest-value change in the document (§0,
Finding 1) and it is what makes Layer 1 possible at all.

Two routes, and the design deliberately does not pick one on paper:

- **Route A (cheap, local):** hoist the world construction out of the per-item
  loop in `reject_gate.rs`, `infer_gate.rs`, `resolve_gate.rs`,
  `divergences_gate.rs`, `fuzz_gate.rs` and `ty/tests/reject.rs`, and give
  `hir::SourceDb` an "add module to a cloned world" entry point. No new
  dependency, ~6 call sites.
- **Route B (architectural):** route the gates through `skydb::SkyDatabase`
  — the salsa db that already exists (`rust/crates/skydb/src/lib.rs:45-64`) and
  that `sky-lsp` already holds long-lived (`sky-lsp/src/lib.rs:74`). Today
  `project::build.rs:112` constructs a fresh `SkyDatabase` **per emission**, so
  salsa's memoisation never crosses an item boundary either.

Phase 2 (§8) implements **A first, measures it, then evaluates B** — because A
is provably sufficient for the T1 budget and B is a larger blast radius.

**(b) One walk, many assertions.** Replace N pipelines with one:

```
for item in corpus:
    artifact = emit_once(item)      # parse → canon → type → lower → emit
    # every assertion below reads `artifact`; none re-compiles
    assert_roundtrip(artifact)  assert_resolve(artifact)   assert_infer(artifact)
    assert_reject(artifact)     assert_coerce_floor(artifact)
    assert_emit_shape(artifact)   # L1e — the new one
    stage_for_go_build(artifact)
```

The artifact is **content-addressed on `hash(compiler binary) + hash(item)`**,
so an unchanged item with an unchanged compiler is not re-emitted at all —
across steps *and* across CI runs (§7.3). A docs-only PR does no corpus work.

**(c) What must stay multiple, and why.** `repro` exists to detect
nondeterminism (HashMap iteration order leaking into output,
`repro_gate.rs:6-12`), so it *requires* ≥2 **fresh-process** emissions —
collapsing it to the shared artifact would delete the assertion. It keeps its 3
seeds; the first doubles as the shared artifact, so it costs 2 extra emissions
per item rather than 4 total. Likewise the reject corpus keeps **both faces**
(§9) — the duplication there is deliberate, and only the *world rebuild* inside
each is removed.

**(d) Release-built gates.** Already justified in-tree
(`preflight-tag.sh:65-74`: "an unoptimized xtask made them ~10× slower"). CI
never got the fix.

**(e) De-duplicate the helpers.** One `corpus()`, one `collect_sky`, one
`load_dir` in a shared module. Six copies of a corpus walker is how two gates
silently come to disagree about what the corpus *is* — `resolve` already loads
the *whole* example dir while `infer` scopes to `src/`, which is why
`39-hub-demo`'s two `Main` modules collide and one is never resolved
(`resolve_gate.rs:51`, `hir/src/db.rs:73-77`).

### 4.3 Before / after

| Metric | Today | After | Mechanism |
|---|---|---|---|
| Corpus emissions per push (both platforms) | **≈670** (12.1×) | **≈200** | one walk + artifact reuse + right-sized corpus |
| Stdlib world rebuilds per push | **≈240** | **≈10** (one per gate process) | shared world |
| Cost per static case | **1.293 s** | **≤15 ms** | shared world |
| `go build`s per push | ≈228 | ≈120 | no `repro`/`build-run` duplication |
| Gate binaries built debug | 14 of 16 steps | 0 | `--release` / `ci` profile |
| Wall clock (T1) | **31–34 min** | **9 min** | all of the above |
| Assertions running in T1 | baseline | **+3 000 Layer-1, +335 stdlib, +clippy, +cgo** | §2 |


---

## 5. Gates that cannot lie

The mandate's constraint 4: *a gate that cannot fail is worse than no gate.*
The 23 catalogued defects are not 23 mistakes — they are one missing
abstraction, instantiated 23 times. Every one of them is possible because a
"gate" today is *an arbitrary shell command whose exit status nobody validated*.

**This is not a design from scratch.** A working implementation of exactly this
harness already exists on `feat/bluedb-v2`
(`rust/crates/xtask/src/bluedb_gates/`, 4 302 lines across 8 files, 48
registered gates, specified in `docs/bluedb/v2-architecture.md` §9). It is
adopted wholesale and generalised from BlueDB to the whole repo. What follows
names what is reused, and the four things that must be **added** for this
larger scope.

### 5.1 The registry — a gate that does not declare a falsifier does not compile

```rust
pub struct Gate {
    pub id:        &'static str,   // "corpus.reject"
    pub area:      Area,           // Compiler | Runtime | Stdlib | Live | Lsp | Cli | Docs | Release
    pub tier:      Tier,           // T0 | T1 | T2 | T3 | T4
    pub platforms: &'static [Platform],   // where it is EXPECTED to run
    pub run:       fn(&Ctx) -> GateOutcome,
    pub budget_s:  u64,            // hard timeout; exceeding it is a FAIL, not a hang
    pub mutations: Mutations,      // NOT a slice — see below
}

/// The empty case is unrepresentable, not merely forbidden. Because REGISTRY is
/// a `static`, this assert is const-evaluated: `Mutations::new(&[])` fails the
/// BUILD. (bluedb_gates/registry.rs:170-191 — the implementation is stronger
/// than the doc that specified it.)
pub struct Mutations(&'static [Mutation]);
impl Mutations {
    pub const fn new(m: &'static [Mutation]) -> Mutations {
        assert!(!m.is_empty(), "every gate must declare at least one mutation");
        Mutations(m)
    }
}

pub struct Mutation {
    pub id:      &'static str,   // "corpus.reject/accept-everything"
    pub patch:   &'static str,   // gates/mutations/corpus.reject.accept-everything.patch
    pub expect:  &'static str,   // the assertion that must go RED, verbatim
    pub targets: &'static [&'static str],  // drives UNVERIFIED-SINCE decay
}
```

Four independent layers keep a gate falsifiable: compile-time const-eval, a
static check before any gate runs, a runtime backstop that records `UNPROVEN`
rather than executing, and a unit test over the registry.

### 5.2 Five states, and a verdict function that cannot collapse

| State | Meaning | Effect on the area verdict |
|---|---|---|
| `PASS` | ran, all assertions held, **and `assertions > 0`** | contributes PASS |
| `FAIL` | ran, an assertion broke, **or** `budget_s` exceeded | **FAIL** |
| `NOT RUN` | registered but not executed (wrong tier, `--only`, harness error) | **UNKNOWN** — never PASS |
| `UNPROVEN` | declares no mutation | **FAIL** |
| `BLOCKED` | structurally impossible right now, with an issue link **and an expiry date** | **FAIL after expiry**; `BLOCKED` before it, never PASS |

```
FAIL     if any gate FAIL or UNPROVEN
UNKNOWN  else if any gate NOT RUN, or any proof is unrevalidated
PASS     else
```

**The rows are rendered from the REGISTRY, not from the run's results.** A gate
cannot disappear by not executing — that single mechanical property is what
kills the "SKIP counted as pass" class at the root rather than at
`example-sweep.sh:450`.

### 5.3 The four additions this scope needs beyond the BlueDB precedent

**(a) `PASS` requires `assertions > 0`.** BlueDB's `GateOutcome::Pass` carries
no count. Ours does, and the runner rejects a zero-assertion pass. This is the
structural kill for `doc-examples.sh`'s `0/0 → GATE: PASS`, for
`golden_gate`'s "PASS (0 CLI examples matched)", for `s8`'s missing empty-corpus
guard, and for `build-run --only=typo` returning 0 on an empty selection — all
four reproduced or cited in §0/§4. It generalises the anti-vacuity guards that
`repro_gate.rs:400-407` and `coerce_floor_gate.rs:449-451` already got right,
and makes them the default instead of a per-gate act of diligence.

**(b) A platform coverage ledger.** `platforms` declares where a gate is
*expected* to run; on other platforms it reports `NOT APPLICABLE`. Then the T4
tier asserts: **every registered gate reports `PASS` on at least one platform.**
This is the structural fix for `11-fyne-stopwatch`, which is skipped as a GUI
example on Linux and unbuildable on macOS and therefore verified *nowhere*
while reading green. Under the ledger, a gate that passes on zero platforms
fails the release tier. "Verified nowhere" becomes impossible to express.

**(c) `xtask gate <id>` is the only entry point — no gate is a shell pipeline.**
Every `- name: Gate — …` step in `rust-ci.yml` becomes `xtask gate <id>`. This
structurally removes the entire class that `verify-all-web.sh:86`
(`if node … | tail -8; then`) belongs to. Note how sharp the current hazard is:
the *identical* idiom is safe in `conformance.sh:52` (because `set -uo pipefail`
is on at `:16`) and broken in `verify-all-web.sh:86` (because only `set -u` is
on at `:8`). Correctness hinging on a `set` line eighty lines away is precisely
why the fix must be structural. The existing `.mjs`/`.sh` verifiers are **kept
as implementations** — they are real coverage (§9) — but they are invoked *by a
gate*, which owns the timeout, parses their JSON result, and decides the state.

**(d) Machine-readable results — no `grep` in a verdict path.** Gates emit
`{gate, state, assertions, failures[]}`. The runner parses it. This kills the
unanchored-substring class by construction: `grep -qE "0 fail"` matching inside
`"10 fail"` (still live at `preflight-tag.sh:113`), the release version check
`grep -qF "sky v$TAG"` where `v0.19.1` matches `v0.19.10`
(`release.yml:91`), and `release-notes.sh:34`'s `index()`-based CHANGELOG
match. Where a numeric floor is genuinely wanted, it is an **exact** expected
count in the registry, not a `>=`. `ty/tests/reject.rs:121,147` asserts
`>= 13` against an actual corpus of 63 — deleting 50 corpus files keeps it
green today.

### 5.4 Falsifier verification, and the canary

`xtask gate --verify-falsifiers` per mutation: create a **scratch git
worktree** outside the repo, `git apply` the patch *in the worktree*, rebuild
and run the gate **from and against the worktree**, assert non-zero exit **and**
that the recorded assertion string appears, then discard.

Five proof outcomes, all from the precedent:
`PROVEN` · `VACUOUS` (patch applied, gate stayed green → **harness FAIL**) ·
`MUTATION-STALE` (patch no longer applies → FAIL, the anti-rot mechanism) ·
`INCONCLUSIVE-BASELINE-RED` (the gate was *already* red, so the patch proved
nothing) · `WRONG-TREE` (the runner measured the developer's tree → harness
FAIL).

**The canary is the only construction that can catch a verifier whose every
answer is "green".** A permanent gate `canary` asserts `true` and pairs with a
no-op patch. A correct verifier must report `VACUOUS` for it. Reporting
`PROVEN` is a harness failure, because a gate that asserts `true` cannot go red
— so a red verdict proves the runner is not measuring what it claims. It is the
one place where a *passing* gate is the failure signal, and that inversion is
deliberate.

`PROVEN @ <sha>` **decays**: the default command diffs each mutation's declared
`targets` between the recorded sha and `HEAD`, and renders
`UNVERIFIED-SINCE <sha>` — a non-PASS state — when they have moved. Not `FAIL`
(the proof is unrevalidated, not known-broken); conflating the two trains
people to ignore the signal.

### 5.5 Unknown names, timeouts, and one bug in the precedent to fix

**Unknown gate id → exit 2**, distinct from 1 = gate failure. And beyond the
precedent, a **gate-name manifest test** parses the gate ids out of
`.github/workflows/*.yml` and asserts every one exists in the registry. That
turns "a typo'd CI gate name is a permanently green no-op" — reproduced at §0,
Finding 2 — from a runtime accident into a compile-time-adjacent failure. It
runs in T0 because it is a file read.

**Timeouts live in the harness**, not in GNU `timeout`. Each gate runs on its
own thread with `recv_timeout(budget_s)`; expiry synthesises a `FAIL`. This
works identically on macOS, where `timeout`/`gtimeout` are both absent — the
exact hole that leaves `conformance.sh:31` running unbounded on every macOS
runner today, backed by a comment claiming the CI job has an outer timeout when
14 of 15 jobs set none.

> **One defect in the precedent that must NOT be carried over.** BlueDB's
> runner leaves a timed-out worker thread **orphaned** rather than killing it —
> acceptable there because its gate bodies are pure reads. Our gates spawn
> servers, `go build`s, browsers and PTYs. A timed-out gate here must kill the
> whole **process group** (§6.2), or a timeout leaks a process holding a port
> and poisons every later gate. This is the single place where copying the
> precedent verbatim would be a bug.

### 5.6 The defect classes, and what structurally kills each

| Defect class | Live example | Structural kill |
|---|---|---|
| Unknown/typo'd gate is a green no-op | reproduced, §0 | exit 2 + gate-name manifest (5.5) |
| Exit status swallowed by a pipe | `verify-all-web.sh:86` | `xtask gate` is the only entry point (5.3c) |
| Unanchored numeric match | `preflight-tag.sh:113`, `release.yml:91` | JSON results; exact counts (5.3d) |
| SKIP counted as PASS | `example-sweep.sh:450` | 5 states; rows from the registry (5.2) |
| `0/0 → PASS` | `doc-examples.sh:84-92` | `assertions > 0` (5.3a) |
| Gate can never pass on a clean checkout | `verify-cli.sh` (pre-fix) | `BLOCKED` with expiry, and the T4 platform ledger (5.3b) |
| Verified on no platform | `11-fyne-stopwatch` | platform coverage ledger (5.3b) |
| Unbounded command | `conformance.sh:31`, `lsp_gate.rs:46-50` | in-harness `budget_s` (5.5) |
| Assertion satisfied by the value disproving it | ui-showcase colour check | mandatory falsifying mutation (5.4) |
| Missing baseline self-blesses | snapshot gate | mutation: delete a baseline → must go RED |
| "Did not run" indistinguishable from "passed" | nightly `29 passed, 0 failed` | `NOT RUN` → area `UNKNOWN` (5.2) |
| Gate ignores its own result fields | `build_run_gate.rs:1461-1514` never reads `matched`; `run_ok` only for `01-hello-world` | mutation: make a served page differ → must go RED |
| Hang reported as success | `wait_bounded` never returns `None` (`build_run_gate.rs:1023-1053`), so `verify_tui`'s "tui hung" branch is dead | timeout is a distinct `FAIL` (5.5) |

---

## 6. The Layer-2 project harness

Six projects, driven by one harness. The harness owns lifecycle; a project
declares *what* to assert, never *how* to start or stop.

### 6.1 The project manifest

```toml
# projects/forum/project.toml
name = "forum"
shape = "live"                      # live | http | cli | tui | webview
owns  = ["sky.live.sse", "sky.live.session", "std.auth.csrf"]

[build]
dialects = ["sqlite", "postgres"]   # T3 expands this into a matrix
stores   = ["memory", "sqlite", "redis", "postgres"]

[boot]
port_env    = "SKY_LIVE_PORT"       # the harness ALLOCATES; the app must read it
ready_probe = "GET /health -> 200"
ready_timeout_s = 20

[[drive]]                            # tier-gated
tier = "T1"; kind = "http"; name = "home renders"
steps = [ "GET / -> 200 contains '<main'" ]

[[drive]]
tier = "T2"; kind = "browser"; name = "post a reply"
script = "drives/post-reply.mjs"

[[drive]]
tier = "T3"; kind = "browser"; name = "idle survival (80s)"
script = "drives/idle-survival.mjs"; budget_s = 180
```

### 6.2 Flakiness prevention — the three failures that actually happened

**(a) Ports.** Today 21 examples set `port = 8000` and three hard-bind it in
Sky source that no env var can move (`05-mux-server/src/Main.sky:45`,
`08-notes-app/src/Main.sky:495`, `15-http-server/src/Main.sky:13`), while the
sweep runs them under `xargs -P` — so its probe asserted only that *something*
answers on `:8000`. Demonstrated: a squatter plus an example that panics and
exits 2 produced `SWEEP VERDICT: OK`.

The fix is not "assign distinct ports". It is:
1. **The harness binds `:0`**, reads the actual port, and passes it in the env.
   No port literal exists anywhere in the harness or the projects.
2. **A gate forbids the regression**: `xtask gate projects.no-port-literals`
   scans project Sky source and `project.toml` for bind-position port literals
   and fails. The env-port pattern is already proven in-tree
   (`30-sse-server-demo/src/Main.sky:29` reads `PORT`;
   `36-composite-server/src/Main.sky:60` reads `SKY_COMPOSITE_PORT`) — this
   makes it universal and enforced.
3. **The readiness probe targets the allocated port and asserts a
   project-specific body**, not merely "2xx from something".

**(b) Process lifecycle.** `example-sweep.sh:381-390` does
`(cd "$dir" && "$bin") & pid=$!` then `kill -9 "$pid"` — `$!` is the
**subshell**, so the app survives and holds the port for the next worker. The
harness spawns every child in its **own process group** (`setsid`) and tears
down with `killpg`, then **asserts the port is released** before reporting
done. A project that will not die is a `FAIL`, not a silent leak.

**(c) Concurrency and the process table.** The sweep exhausts the per-uid
process table with thousands of `xcrun` spawns, which kills mem-guard's ability
to fork and makes unrelated things fail. The cause is **multiplicative
nesting**, not the worker count: `scripts/lib/concurrency.sh` correctly caps
*outer* workers, but each worker's `sky build` invokes `go build`, which fans
out to `GOMAXPROCS` compile processes, each spawning `xcrun` on macOS.
`compute_max_workers()` never sees the inner factor.

The harness therefore budgets **total concurrent processes**, not workers:
`outer_workers × go_build_p ≤ cores`, by passing `go build -p N` and
`GOMAXPROCS=N` explicitly with `N = max(1, cores / outer_workers)`. It also
checks `RLIMIT_NPROC` headroom before fanning out and refuses to start rather
than wedging the host.

### 6.3 Drive kinds

| Kind | Mechanism | Closes |
|---|---|---|
| `http` | in-harness client against the allocated port; asserts status + body predicate | the "something answers on :8000" hole |
| `browser` | Playwright, launched **by the harness** with the allocated base URL | the entire currently-CI-unreachable `.mjs` tier (§7.2) |
| `cli` | spawn with argv/stdin, assert exit code **and** stdout predicate | `verify-cli.sh:101` captures the exit code into a string (`\|\| echo "__EXIT_$?"`) and **never checks it** — a CLI example that exits non-zero without printing "panic" is reported `✓ (no panic)` |
| `tui` | **real PTY** (`portable-pty`), send keystrokes, assert rendered frames | the `pty-skip` gap: today TUI verification is spawn-and-look-for-panic, and a wedged TUI reports OK (§5.6) |
| `webview` | macOS, loopback asset pipeline, headless where possible | `--shape webview` never runs in CI |

### 6.4 Teardown and evidence

Every drive produces a structured result plus artifacts (server log, browser
console, screenshots on failure) into a per-run directory. On failure the
harness prints the failing assertion and the artifact paths. Note the current
anti-pattern this replaces: the sweep points at `/tmp/sky-build-<name>.log`,
which the CI runner no longer has by the time logs are archived.

---

## 7. CI topology

### 7.1 The job graph

Today `rust-ci.yml` has **zero `needs:`** — 9 flat jobs, no fan-in, therefore
**no single "CI green" status check**. Branch protection must enumerate all
nine by name, and adding a job silently un-gates it. The new graph fixes that:

```
                    ┌─ lint ──────────────┐
                    ├─ compiler-unit ─────┤
  setup (build once)┼─ corpus ────────────┼─→ ci-green (fan-in, required check)
   └ artifacts:     ├─ emitted-build ─────┤
     sky, xtask     ├─ runtime-go ────────┤
     (release)      ├─ conformance ───────┤
                    └─ projects-smoke ────┘
```

`setup` builds `sky` and `xtask` **once, in release**, and uploads them as job
artifacts. Today six jobs each run `cargo build --workspace --locked` in debug
(`rust-ci.yml:75, 97, 113, 142, 182, 249`) and race each other for one shared
cache key. `ci-green` is a trivial job that fails if any dependency did not
succeed — it is the only required status check, so adding a job cannot silently
un-gate the branch.

### 7.2 Linux vs macOS, and what is load-bearing on both

`macos-determinism` is the only macOS job today. The must-not-touch set (§9)
requires **both** platforms for: `repro`, `golden`, `coerce-floor`, and
`conformance` — because they are the gates that catch *platform-dependent*
behaviour, which is exactly the class that produced the `Json.Decode.int`
int64 bug (macOS passed, Linux failed).

| Job | Linux | macOS | Why |
|---|---|---|---|
| `repro`, `golden`, `coerce-floor`, `conformance` | ✅ | ✅ | platform-dependent behaviour — the whole point |
| `corpus` static/L1e | ✅ | T2 | deterministic; second platform confirms, does not gate T1 |
| `runtime-go` incl. **`CGO_ENABLED=1` leg** | ✅ | ✅ | 12 cgo webview tests run on **neither** platform today |
| `projects-smoke` | ✅ | T2 | |
| P6 Webview + cgo | — | T3 | macOS-only by construction |
| browser drives | ✅ | T3 | one platform is enough for DOM behaviour |

**macOS minutes cost 10× on GitHub.** That is the honest reason macOS work is
concentrated in T2/T3 rather than T1 — not a coverage judgement. The T1 macOS
omission is the one place I am trading latency for cost, and §9 registers it as
a risk.

### 7.3 Caching

| Cache | Key | Fixes |
|---|---|---|
| cargo (`Swatinem/rust-cache`) | `Cargo.lock` + rustc version, **distinct `shared-key` per job**, `save-if` on default branch only | today 6 jobs share `sky-gates-linux`; the cache is write-once per key, so whichever job finishes first wins and the saved contents are **nondeterministic run-to-run** |
| Go build (`GOCACHE`) | `go.sum` + **hash of `runtime-go/` + the emitted-Go artifact hash** + Go version | today keyed on `go.sum`/`go.mod` alone — almost always a hit, so `actions/cache` **skips the save** and the build cache for *Sky-emitted* Go (which changes every commit) is never refreshed. Every example `go build` is effectively cold after the first day |
| Go module (`GOMODCACHE`) | `go.sum`, **with a gate forbidding `latest` floats** | the module cache reached **75 GB** from `"latest"` version floats; pin every project dep and commit `go.sum` |
| Corpus artifacts | `hash(sky binary) + hash(item)` | a docs-only PR does no corpus work at all (§4.2b) |
| FFI surface | hash of the project's Go dep set | P1's 76 k-symbol surface; the §3.3 budget risk |

Also: `actions/setup-go` at `release.yml:51-54` sets `cache: true` with no
`cache-dependency-path`, and there is **no `go.sum` at the repo root** — the
cache is keyed on nothing relevant to the module it actually builds
(`tools/sky-ffi-inspect`).

### 7.4 `timeout-minutes` everywhere, and a release that gates

**Every job gets `timeout-minutes` = 1.5× its tier budget.** Today 14 of 15
jobs set none and inherit GitHub's 6-hour default — which, combined with
`conformance.sh` running unbounded on macOS, is a 6-hour × 10× -cost hang
waiting to happen.

**`release.yml` today runs no test gate at all**, and because `rust-ci.yml`
triggers only on `push` to `main`/`rewrite/rust-compiler` (`:17-18`), **a tag
push does not start Rust CI**. Nothing forces the tagged commit to have ever
been green. Three fixes:

1. **`build` `needs:` a T4 job** that runs the full pre-release tier on the
   tagged ref. No green T4, no assets.
2. **Assert the asset set is complete.** `if-no-files-found: ignore`
   (`release.yml:116`) makes an upload resolving zero files a *green* step, and
   nothing downstream counts assets — `find … -exec cp` returns 0 on zero
   matches and the step ends with `ls -la`, whose status nobody checks. So a
   3-of-4-platform release publishes green today. The staging step asserts an
   **exact expected asset count** and that each named artifact is present.
3. **Anchor the version check.** `grep -qF "sky v${TAG_VERSION}"`
   (`release.yml:91`) is an unanchored substring: tag `v0.19.1` is satisfied by
   a binary reporting `sky v0.19.10`. Replace with an anchored exact match, and
   apply the same to `release-notes.sh:34`'s `index()` CHANGELOG match.
4. **Install and run the built asset**, not just `--version` on the build
   output.

Also: `docker` is `continue-on-error: true` (`release.yml:166`), so a failed
image publish reads green; and `docker-publish.yml` has **no concurrency
group** while pushing `anzel/sky:latest`, so a manual dispatch can race the
tag-time job. Both are fixed in Phase 6.

### 7.5 Making the unreachable tiers reachable

The Playwright layer is 19 verifier scripts (including a 2 113-line
`verify-stdui-matrix.mjs`) and **no workflow ever runs `npm ci` or
`actions/setup-node`**; there is no `playwright.config.*`. This is real,
valuable coverage — the "click is a no-op" class that a `go build` gate cannot
see — sitting entirely outside CI. Phase 7 adds `setup-node` + `npm ci` +
cached browser binaries to the T2/T3 jobs and routes each verifier through a
registered gate. **No verifier is rewritten**; they are wrapped.

Same treatment for the other orphans: `scripts/test-ci.sh` (invoked by no
workflow), `scripts/example-e2e.sh` and the 21 `e2e.json`/`verify.json`
configs, `scripts/fuzz-well-typed.sh`, `xtask welltyped`, and
`runtime-go/cmd/sky-hub` (outside `./rt/...`, so CI never even compiles it).

---

## 8. Migration plan

Each phase is independently shippable, leaves the repo green, and states what
is **deleted**, what is **kept**, and what must be **proven equivalent** first.
No phase deletes anything before its replacement is green *and* its falsifying
mutation has been run.

**Phase 0 — apply the pending fixes, change nothing structural.**
Apply `docs/ci-gate-fixes-pending.patch` (777 lines, applies clean) plus the
four defects it explicitly left out as higher blast radius:
`build_run_gate`'s `gate_result` ignoring `run_ok`/`matched` and non-emitting
examples; `welltyped`'s both-reject-everything PASS; `wait_bounded` fabricating
a success `ExitStatus`; `reject_gate` accepting rejection for the wrong reason.
Add `timeout-minutes` to all 15 jobs and `-D warnings` to clippy.
*Deleted:* nothing. *Proof:* each fixed gate must go RED on its named defect
reproduction before the fix, GREEN after.

**Phase 1 — the gate harness, wrapping every existing gate unchanged.**
Port `bluedb_gates/` to `xtask gate`; register every current gate with its body
**unchanged**; add the five states, the canary, the manifest test, JSON
results, in-harness timeouts with **process-group kill** (§5.5). Make
`SyntaxKind::KINDS` `pub`. Fix `AGENTS.md:255-258` (it documents a deleted
file).
*Deleted:* nothing. *Proof:* run the registry against three known-good and
three known-bad commits and assert **verdict-identical** results to today's
gates. Any divergence is a bug in the port, not an improvement.

**Phase 2 — falsifiers for every registered gate.**
Author a mutation per gate; run `--verify-falsifiers`. Every `VACUOUS` result
is a gate that cannot fail — fix it. This is where the remaining false-greens
die, found by mechanism rather than by audit.
*Deleted:* nothing. *Proof:* zero `VACUOUS`, zero `UNPROVEN`, canary reports
`VACUOUS`.

**Phase 3 — the shared world and the single corpus walk.**
Route A (§4.2a) at the six call sites; one `corpus()`/`collect_sky`;
content-addressed artifacts; release-built gates.
*Deleted:* the duplicated emissions in `repro`'s `go_builds` and
`coerce-floor`'s re-emit — **but not their assertions**.
*Proof:* a differential harness runs old gates and the new walk over the same
commits and asserts **identical pass/fail per item**. Only then are the old
pipelines removed. Measure the new per-case cost and publish it against the
1.293 s baseline.

**Phase 4 — Layer 1.**
Build the generator, the axis tables, the S1–S4 strata, and the two ledgers
(stdlib symbols via `sky doc --export`'s `api/symbols.json`; language
constructs via `SyntaxKind::KINDS`). Land L1e emit-shape assertions. Migrate
`examples/{35,40–51,53–55}` into C3.
*Deleted:* those 16 example directories, **after** each migrated case is shown
to reproduce its original defect against a reverted fix.
*Proof:* every migrated case fails on the reverted fix; ledger coverage
reported as a number.

**Phase 5 — Layer 2.**
Build the harness (§6), then the six projects **one at a time**. A project is
done when the coverage ledger shows it owns the surfaces its absorbed examples
carried.
*Deleted:* an example leaves the regression corpus only when the ledger shows
its surface owned elsewhere; it stays in `examples/` as documentation or is
pruned as duplicative (§2.3).
*Proof:* the §2.4 ledger, every row green.

**Phase 6 — CI topology.**
The job graph, `setup` + artifacts, `ci-green` fan-in, the cache keys, the
tiers, and a gating `release.yml`.
*Deleted:* the six duplicate `cargo build --workspace` steps.
*Proof:* measure T1 wall-clock on ten real PRs; it must hold 9 minutes. **This
is where the §3.3 budget risk is settled with data, not argument.**

**Phase 7 — reconnect the unreachable tiers.**
`setup-node` + `npm ci` + browser caching; wrap the 19 Playwright verifiers,
`example-e2e.sh`, `welltyped`, `fuzz-well-typed.sh`, and `sky-hub` in
registered gates; wire the 398 orphaned Sky assertions to a runner.
*Deleted:* orphan scripts with no caller **and** no unique assertion, only
after listing what each asserted.
*Proof:* every one of the 398 orphaned assertions either runs or carries a
`BLOCKED` row with an issue and an expiry.

**Phase 8 — prune `examples/`.**
Collapse the duplicative chains, delete `simple` and `test_pkg`, add the
example-reachability gate.
*Proof:* every surviving example is referenced by a doc page and `sky check`s.

---

## 9. Risk register and what must NOT be touched

### 9.1 Must NOT be touched

Carried from the 2026-08-09 audit, with reasoning:

| Item | Why it stays |
|---|---|
| **`repro` + `golden` on BOTH platforms** | they exist to catch platform-dependent divergence; one platform is not a weaker version of this gate, it is a different gate |
| **`conformance` on both platforms** | the `Json.Decode.int` int64 bug passed on macOS and failed on Linux. This is the single healthiest test asset in the repo (19 suites, 772 assertions, already wired both sides) |
| **The reject corpus itself — remove one face, never both** | `ty/tests/reject.rs` and `xtask reject` are *not* byte-identical checks: the test counts Warning-severity parse diagnostics (`reject.rs:113`) while the gate counts only Errors (`reject_gate.rs:213-219`), and they discover the corpus differently (recursive vs flat). The test is strictly more lenient. **Collapsing them requires reconciling those two semantics first** — otherwise "dedup" silently drops the stricter check. §4 removes only the *world rebuild* inside each |
| **`fuzz`** | the only robustness gate; keep it, and make budget exhaustion a FAIL instead of a printed note |
| **The sweep's clean-slate wipe + forced `sky install`** | it is the only thing that proves a fresh clone builds. Moves to T4, keeps the wipe |
| **`verify-all-web` / `verify-cli`'s "click is a no-op" coverage** | the class that shipped the v0.13 Std.Ui event-emission regression. Wrapped, never rewritten |
| **`coerce-floor`'s FAIL-ON-INCREASE** | a ratchet; ratchets only work if never relaxed |
| **`repro`'s ≥2 fresh-process emissions** | the assertion *is* the multiplicity |

### 9.2 Risk register

| # | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R1 | **The ≤15 ms shared-world per-case cost is a bound, not a measurement.** If real cost is 50 ms, Layer 1 at 5 000 cases is 4 min, not 25 s | Medium | High — the T1 budget | Phase 3 measures it before Phase 4 commits to case counts. The 1.293 s baseline is measured; the improvement factor is not |
| R2 | **`projects-smoke` does not fit 9 min on a 4-vCPU runner**, especially P1 cache-cold | Medium | High | §3.3's stated degradation: P1 promoted to T2, reported as `DEGRADED`, never as pass. Settled with data in Phase 6 |
| R3 | **Layer 1 generates false confidence** — thousands of green cases that never had a chance to fail | Medium | High | every generated *family* carries a falsifying mutation (§5.4); a family that cannot go red is `VACUOUS` and fails the harness |
| R4 | **Combinatorial explosion in maintenance** — 5 000 cases nobody can triage | Medium | Medium | cases are generated from axis tables, not hand-written; a failing case reports its **axis tuple**, which localises the bug to a mechanism rather than a file |
| R5 | **Six projects rot into six more sample apps** | Medium | High | each project's `owns` list is checked by the coverage ledger; a project that stops exercising a surface it owns fails the ledger gate |
| R6 | **Deleting the 16 repro examples loses a defect nobody re-derived** | Low | High | Phase 4's proof obligation: each migrated case must fail against the reverted fix before the directory is deleted |
| R7 | **The gate harness itself becomes the green-lie generator** — the precedent's own stated top defect | Medium | Critical | the canary (§5.4), plus `--verify-falsifiers` in T3, plus the harness self-integrity static checks |
| R8 | **T1 has no macOS leg**, so a macOS-only regression reaches `main` | Medium | Medium | accepted trade (10× minutes); T2 runs on merge_group, so it gates the merge, not just the nightly |
| R9 | **`BLOCKED` becomes the new SKIP** | Medium | High | `BLOCKED` requires an issue link **and an expiry date**; expired → `FAIL`. And it never contributes PASS to an area verdict |
| R10 | **Phase 3's differential harness masks a real behaviour change** as an expected diff | Low | High | it asserts *identical* verdicts per item, not "no new failures". Any divergence blocks the phase |
| R11 | The 2× CI derating factor is wrong | Medium | Medium | Phase 6 measures on ten real PRs; budgets are ceilings enforced by `timeout-minutes`, so being wrong fails loudly rather than silently drifting |

### 9.3 Open questions for the grillers

1. **Route A vs Route B for the shared world** (§4.2a). A is 6 call sites and
   provably enough for the budget; B (salsa via `skydb`) is architecturally
   right and would also help the LSP, but has a much larger blast radius.
   Design says A-then-measure. A griller may reasonably argue that doing A
   guarantees B never happens.
2. **Six projects, or fewer/more?** Six is chosen so each owns a coherent
   surface set with minimal overlap. P4 (`ops`) is the weakest — it may be
   two thin projects (console topology vs analytics) wearing one name.
3. **Is `examples/` `sky check`-only enough?** It drops `go build` coverage for
   58 directories. The claim is that Layer 1 + the six projects subsume it, and
   §2.4 is the evidence — but "an example that compiles but no longer runs" is
   a real state that only the nightly sweep would catch under this design.
