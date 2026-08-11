# AUTONOMOUS GOAL — CI/CD + test-suite overhaul (feat/ci-test-overhaul)

Set 2026-08-09, off `main` @ `08557e50` (immediately after the v0.19.13 release).

## Verbatim user mandate (2026-08-09)

> ok i think the cicd + test sweeps for sky repo need overhauled.
>
> we need test sweep maybe not run against examples, or that we need to rework on
> our examples (too many with similar use cases)
> and the cicd should test something for compiler + dedicated "real world projects"
> like use cases.
>
> please grill and design again for the cicd + test suites (or reworked examples),
> how efficiently we could run all these with reliable results (don't cut corners)
> we need to test what all the possible use cases we have for sky compiler, lsp,
> tooling + sky.live etc. modules effectively.
> with the mind of time aware when its running on cicd
>
> dedicated session to plan + implement please

## What "done" means

A CI/CD + test architecture that (a) covers every Sky surface — compiler, LSP,
tooling/CLI, stdlib, Sky.Live, Std.Ui/Tui/Webview, Std.Db/Persist, auth,
observability — through **deliberate real-world-shaped projects** rather than 58
overlapping examples; (b) runs in a **time-budget CI operators will actually
wait for**; and (c) **cannot report a false green**. "Don't cut corners" is the
user's explicit instruction: coverage is not to be traded for speed. Speed comes
from removing duplication and false work, never from removing assertions.

Only a fresh-context adversarial Judge may declare a phase done.

## Why this exists — evidence from the v0.19.13 release (2026-08-09)

Nine consecutive preflight attempts failed, none for a product reason. What the
day exposed:

**Cost.** The 55-example corpus is compiled **666 times per CI push (12.1x over)**
and 376 times per local preflight. Every one of those pipelines re-parses and
re-infers all 87 stdlib modules from cold — salsa memoisation never crosses a
gate boundary. CI wall-clock 31-34 min; `test-ty` alone is the critical path at
1857s, ~60% of it re-running the SAME 63-file reject corpus that `xtask reject`
also runs. Gates ran DEBUG-built: `reject` measured **780s debug vs 74s release**,
identical verdict.

**Correctness of the gates themselves — 23 defects, each demonstrated:**
- `xtask` exited **0** on an unknown subcommand: a typo in a CI gate name was a
  permanently green no-op.
- `verify-all-web.sh` used `if node … | tail -8; then` — testing `tail`'s status.
  The console e2e gate **could not fail**.
- `grep -qE "0 fail"` matched the substring in `"10 fail"` — a run ending
  `0 pass / 12 fail` with every Playwright check dead **passed** the gate.
- The sweep's server probe asserted "something answers on :8000" while 14
  examples share that port under parallel workers, and `kill -9 $!` killed the
  subshell leaving the app holding the port. Demonstrated: a squatter plus an
  example that panics and exits 2 → `SWEEP VERDICT: OK`.
- `SKIP` counted as `pass`: nightly reported `29 passed, 0 failed` with three
  examples never built.
- `doc-examples.sh` with `total=0` printed `0/0 … GATE: PASS`.
- `verify-cli.sh` hard-failed on missing binaries and six of its entries are
  built by nothing → **could never pass on a clean checkout**.
- A ui-showcase colour assertion was satisfied by the exact value proving it
  wrong; a missing snapshot baseline was silently written and passed.
- `conformance.sh` ran **unbounded** wherever GNU `timeout` is absent (every
  macOS runner), and **14 of 15 workflow jobs set no `timeout-minutes`**.
- `build_run_gate.rs` never consults `run_ok`/`matched` and excludes examples
  that fail to emit — the three `--shape live/http/tui --run` steps are
  decorative; an app that panics on boot exits 0. **UNVERIFIED LEAD — reproduce
  before acting.**
- `release.yml` runs **no test gate** and can publish a partial asset set green;
  its version check is an unanchored substring (`v0.19.1` matches `v0.19.10`).
- 10 Postgres tests in `rt/jobs` are proven dead (not in the CI test pattern,
  env var set nowhere); `webview_test.go` needs cgo while CI runs CGO_ENABLED=0;
  22 root Sky suites (~280 assertions) have no runner; the whole Playwright tier
  is CI-unreachable.
- `11-fyne-stopwatch` is skipped on Linux CI and unbuildable on macOS — verified
  **nowhere**, while the sweep read green on CI and red locally.

**Examples as a test corpus are the wrong shape.** 58 examples with heavy
overlap, 29 of them absent from the sweep table, several referenced nowhere at
all, three hardcoding port 8000 in Sky source. They are *documentation samples*
being used as a *regression corpus*, and they serve neither role well.

## REFINEMENT (user, 2026-08-09, after the first design brief) — TWO LAYERS

> we should aim for compiler + standard lib + built-in 100% coverage with many
> use cases, imagine those github issues we've been fixing? we could've caught
> them if our tests include different variation of usage of those syntax +
> imports etc.
>
> then on to practical realworld examples using sky.live sky server, sky.tui etc.

This reorders the whole design. The corpus is **two layers**, and layer 1 is the
one that would have caught the actual bug history:

**Layer 1 — exhaustive, combinatorial coverage of the language + stdlib + builtins.**
Not "more examples": systematic VARIATION. Every shipped defect in this repo's
history was ordinary usage in a combination nobody had tried. The pattern is
always the same — the simple case compiles clean, one axis changes, and it
breaks:
- **#164** same-named alias / import-alias collision (import shape varied)
- **#166** record update inside a tuple dropped un-updated fields (context varied)
- **#170/#172** tuple/record pattern destructure on an *erased* subject — through
  `List.foldr`, a `let`, an `any`-typed value (subject typing varied)
- **#171** row-polymorphic record update through `foldl`/`foldr` dropped fields
  (higher-order context varied)
- **#173** `Dict k (List Record)` — three defects (type nesting varied)
- **goty.rs record-fieldset collision** — same field NAMES, different field TYPES,
  and the erased-`any` recurrence via `fst`/`snd`/tuple destructure
- **kernel-alias variadic arity**; **bare `Math.pi` lowering to `any`**
- stdlib semantics: `Json.Decode.int` int64 platform-dependence,
  `Money.allocate` negative residue, `Bytes.length/slice` rune-vs-byte,
  `Time.addMonths` year-carry, `Time.timeString` host-TZ, `Uuid.parse` never
  `Just`, `Auth.passwordStrength` panic
So layer 1 must vary, systematically and by construction: **syntax forms x import
shapes (aliased/exposing/qualified/cross-module/same-name) x type shapes (records,
rows, generics, ADT payloads, nesting, Dict/List/Maybe/Result combinations) x
erasure contexts (higher-order, callback params, `any`-typed) x every stdlib
function's edge cases (boundaries, negatives, unicode, TZ, platform int width)**.
Generated or table-driven where the axes are enumerable; hand-written where a
bug taught us something a generator would not think of. Every past issue becomes
a permanent case, and its NEIGHBOURS in the variation space become cases too —
because the bug was never unique, only the combination was.

"100% coverage" is the user's target. Interpret it as: every public stdlib
function and every language construct has cases, and the *combinations* are
covered systematically rather than by whatever an example happened to use. Where
100% is not literally reachable, say what is uncovered and why — do not quietly
redefine the target.

**Layer 2 — practical real-world projects** using Sky.Live, Sky.Http.Server,
Sky.Tui, Sky.Webview, Std.Db/Persist, auth, jobs — apps as a user would build
them, exercising surfaces IN COMBINATION. This is the integration tier, and it
is what catches the class layer 1 structurally cannot: session/SSE lifecycle,
CSRF-idle strand, session hijack, `liveInto` silently not updating on Postgres,
"compiles clean, behaves wrong" at runtime.

Layer 1 is fast, deterministic, massively parallel — it belongs per-push. Layer 2
is slower and tiered. Design both; do not let layer 2's cost crowd out layer 1's
completeness.

## Design constraints (the user's words, made concrete)

1. **Real-world-shaped projects, not sample sprawl.** A small number of
   deliberately-designed apps that each exercise a coherent slice of the product
   as a user would actually build it, with the surfaces they combine.
2. **Time-aware tiering.** Per-push must be fast enough to gate a PR; deeper
   tiers run nightly/pre-release. State the budget per tier and hold to it.
3. **No corner-cutting.** Every surface that has a gate today keeps one. Where a
   gate is removed, its coverage must be demonstrably subsumed elsewhere — and
   that must be *shown*, not asserted.
4. **A gate that cannot fail is worse than no gate.** Every gate ships with a
   named falsifying mutation, proven by running it.
5. Examples keep their documentation role. If the regression corpus separates
   from them, say what each is for and who maintains it.

## Method (CLAUDE.md §0.4)

design → grill (>=2 fresh-context adversaries) → implement (worktree) →
three-leg verify → fresh-context Judge. Phase boundaries commit; push at phase
boundaries. No phase closes on my own say-so.

## Carry-in that must not be lost

- `docs/` audit artifacts from 2026-08-09: the ranked step->seconds->unique-value
  table, the corpus-compile census, and the must-NOT-touch list (repro + golden
  on BOTH platforms, conformance on both, the reject corpus itself — remove one
  face never both, fuzz, the sweep's clean-slate wipe + forced `sky install`,
  verify-all-web/verify-cli's "click is a no-op" coverage).
- `gate-fixes-for-main.patch` (777 lines, applies clean) — the unfixed half of
  the audit, including one more unanchored `grep "0 fail"` live in preflight's
  verify-cli arm.
- The parallel-build hazard: the sweep spawns thousands of `xcrun` processes and
  exhausts the per-uid process table, which kills mem-guard's ability to fork
  and makes unrelated things fail. Cap concurrency.

## AUTONOMOUS MANDATE (user, 2026-08-10)

> deploy first then in fully autonomous mode for all remaining test overhaul e2e
> phase 2/3 and whatever required

Deploys: DONE (sky-lang.org @ settleby/us-central1-a; the second project @ its
own VM). Both verified externally: HTTP 200 and a forged-session POST to
`/_sky/event` refused 403 — the v0.19.13 session-binding fix is live in prod.
The local toolchain was v0.19.11 before this; deploying without upgrading would
have shipped the OLD compiler and looked successful.

**Now autonomous for ALL remaining phases (2/3 and whatever required).**
Per CLAUDE.md §0: each phase is design → grill (>=2 fresh-context adversaries)
→ implement (worktree) → three-leg verify → fresh-context Judge. I cannot
declare a phase done; only a Judge can. Push at phase boundaries. Stop only on a
genuine blocker (surface it, keep the loop alive).

Phase 3 is the pivot: it MEASURES the per-case cost, and Phase 4's case counts
are DERIVED from that number against the break-even table (~80 ms/case at 1,500;
~24 ms/case at 5,000 single-threaded). Shrinking to fit is forbidden; the abort
branch is real.

Known-open items to fold in as they become blocking:
- `main : Task Error ()` that FAILS exits 0 and prints nothing (`lower.rs:6137`
  discards the Result). Every exit-status-keyed gate is blind to app failure.
  Its own change: it alters every app's exit contract and the emitted Go.
- `build_run_gate`'s `--shape … --run` steps still cannot fail on `matched`.
- fyne: the FFI inspector pins GOOS=linux/amd64 CGO_ENABLED=0 so a cgo-requiring
  package has no surface; needs a documented host-target fallback. Then reply to
  discussion #50.
- `test-rest` shells out to a real `go build` with no `actions/setup-go` pin.

## MANDATE EXTENSION (user, 2026-08-10)

> ok fully autonomous to implement + overhaul the tests for much better coverage
> and use cases until you're satisfied.
> then we can merge into main + then rebase bluedb feat branch and continue e2e
> for bluedb + new tests

**The arc, in order:**
1. Keep closing coverage gaps autonomously until the named gaps are closed or
   explicitly justified with a ratchet. "Until satisfied" is NOT "until tired" —
   the bar is the ledger's own gap list reaching zero-unjustified.
2. Merge `feat/ci-test-overhaul` → `main`.
3. Rebase `feat/bluedb-v2` onto the new main, then continue BlueDB e2e under the
   new test architecture (its own gates, its own Layer-2 member).

**The named gaps as of 2026-08-10 (measured, not estimated):**
- **5 stdlib modules imported by NOTHING**: `Std.Jobs`, `Std.Db.Schema`,
  `Std.Db.Migrate`, `Std.Markdown`, `Std.Email`. Consequence: the **file-based
  migration verbs are exercised by no project at all**.
- `Std.Config` and `Sky.Http.Middleware` reached 1 importer via the new apps —
  thin, not covered.
- `release.yml` runs **no test gate**.
- `ci-green` is RED on budget (~2217s vs a 990s ceiling). **Do not raise the
  ceiling to make it pass** — close the gap or report it.
- The **LSP corpus is a single synthetic file**; `lsp-fleet-sweep.cjs` hardcodes
  an absolute path to one machine. The surface #164 fell through has never been
  tested on a multi-module project.
- The **Playwright tier is CI-unreachable**.
- `tests/Db/DbTest.sky` has `Result Error List Row` (3 type args) at
  :179/:198/:389/:396 — those suites never built.

**Standing rules (unchanged, and they are why this worked):**
- No gate weakened to go green; no coverage deleted to simplify.
- Every gate ships a falsifier that is DEMONSTRATED to go red.
- Anything unfixable lands BLOCKED with a repro and an expiry — never silenced.
- Report premise failures rather than working around them. Every phase so far
  found two or more, including in my own briefs.

### Endgame addendum (user, 2026-08-10)

> once merge into main, we need cicd green + tag release

So the arc is: close gaps → merge to `main` → **CI/CD green on main** → **tag a
release** → rebase `feat/bluedb-v2` → continue BlueDB e2e.

**Two things this forces, both of which must NOT be resolved by cheating:**

1. **`ci-green` is currently RED on budget (~2217s vs a 990s ceiling).** "CI green"
   must be achieved by closing the gap (dedupe, cache hits, parallelism, tier
   placement) — **never by raising the ceiling**, and never by demoting the check.
   The two-step rollout stands: `ci-green` is additive until it is genuinely
   green, and only then becomes required.
2. **This release contains behaviour changes, so the version is a USER decision.**
   A failing `main : Task Error ()` now exits NON-ZERO with output (it previously
   exited 0 silently) — that is a breaking change for anything relying on exit 0.
   Emitted Go also changed (fieldset resolution + Store.insert kernel arity), so
   goldens moved. Per `feedback_release_strategy`, do not invent the version or a
   multi-version split — surface the choice (v0.19.14 vs v0.20.0) with the
   breaking-change evidence and let the user decide.

Release discipline carried in: run `scripts/preflight-tag.sh` (it now gates far
more than before), CHANGELOG entry with a ⚠ Breaking-changes section so
`sky upgrade` surfaces the banner, then tag + `gh release`.

---

## STATE @ d20fddac (2026-08-11) — read this first after any context break

Branch `feat/ci-test-overhaul`, PR **#175** open against `main`. CI only runs on
pushes to `main` and PRs targeting `main`, which is why this branch showed no
runs at all until the PR existed.

### The budget, closed — and closed the right way

`ci-green` was RED on the T1 budget. It is not red because the ceiling moved.
Measured on run 31440657449:

```
setup                          202s
+ slowest setup-dependent       784s   (repro)
= dependent chain               986s   against 990s allowed
```

Four seconds of headroom is a coin flip, not a budget. The `repro` GATE itself
was 690s of that (the workspace build is only 80s — the shared cache works), and
it was a serial `for` over a corpus of independent per-directory work. Now
parallel across examples: **551.8s → 245.5s (2.25x)** on the full corpus, with
BYTE-IDENTICAL tables (202 rows) and the same verdict, and proven still able to
fail (inject the pid into each emission → red on every probe example). Expected
chain on a 4-core runner: ~202 + ~345 = ~550s against 990s.

### What is green, and what is honestly not

* CI run 31442455250: `codegen-build` now PASSES (the coerce-floor golden needed
  `apps/dispatch` blessed — the gate was correct to fail). `macos-determinism`
  was still running at the time of writing; everything else green.
* `sky-suites` PASSES at **320/320**, with `Sky_Core_PointFreePolyTest` and
  `Sky_Core_PureTest` declared BLOCKED and contributing **ZERO** cases.

**Do not bump `SKY_SUITES_EXPECTED` to 330 until the suites actually pass.** I
did, on an agent's report that the two lower.rs defects were fixed, and the gate
correctly answered 320/330. The agent verified them in its own worktree at a base
54 commits older; on this tree both still fail:

```
joinStr : String -> String -> String / joinStr = String.append
  → rt.String_append (func(any,any) any) in a func(string,string) string slot
Task.perform (Pure.uuidV7 ())
  → rt.Uuid_v7() (any) bare in an rt.SkyTask[...] slot
```

The kernel-value fix (merged, e5b32e46, 7/7 contract tests, coerce-floor delta
zero) does NOT cover the `sky test` path. Sent back to the same agent with two
leads: arity ≥ 2 (curried HM type vs Go's flattened signature), and the test
runner emitting its own `main.go` so `expected` never reaches `lower_var`.

### Also fixed this session

Three sky.toml keys honoured by nothing, one root cause (`_ => {}` dropped
unrecognised keys silently): `[jobs]` (parsed by no one while the runtime's error
told operators to set it), `[live] input` (hardcoded `inputMode` behind a
`// or "blur"` comment), and `[auth] session_ttl` (i.e. `tokenTtl` misspelled, in
two examples). Unknown keys in runtime config sections now warn. CLAUDE.md's
end-of-mission `pkill` is scoped to the agent's own worktree — the unscoped form
kills sibling agents' in-flight gates.

### Still open

* Layer-1 families S and R/E — two agents in flight; only `Family::L` is
  generated today (`gen.rs:746` is the sole construction site).
* The LSP corpus is still one synthetic fixture (`scripts/lsp-test-nvim.lua`).
  The GATE is genuinely enforced in CI (nvim is installed, `lsp-fuzz` runs it) —
  it is the CORPUS that is thin, not the gate that is fake.
* Then: merge → CI green on `main` → **tag (version is the user's call)** →
  rebase `feat/bluedb-v2`.

Claims in this file that name a file:line or a number were run, not recalled.
Anything I did not verify says so.

### CORRECTION to the block above (same session)

The "the fix is incomplete" finding recorded above was **WRONG**, and the reason
is worth more than the finding was.

`scripts/build.sh` copied `rust/target/release/sky` while cargo, honouring the
`CARGO_TARGET_DIR` set on this machine, had written the binary somewhere else.
So `sky-out/sky` was a PRE-FIX compiler with a fresh mtime from the `cp`. The
two suites failed for that reason alone. The kernel-value fix was complete when
it was merged.

Fixed in `b958958e` at all three script sites via `scripts/lib/cargo-target.sh`,
which asks cargo for the executable path (`--message-format=json`) instead of
guessing it. `preflight-tag.sh` had the same bug, which means a release tag could
have shipped a binary none of its six checks had examined.

`sky-suites` is now **330/330 across 22 suites with SKY_SUITES_BLOCKED empty**.

The lesson for anyone reading this later: when a gate contradicts a fix that its
own unit tests say is present, suspect the ARTEFACT before the fix. `cargo test`
and `sky-out/sky` are built by different paths, and only one of them was lying.
