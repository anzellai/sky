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
