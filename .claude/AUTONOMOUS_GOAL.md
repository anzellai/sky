# AUTONOMOUS GOAL — known-but-unclosed close-out

## The user's goal, VERBATIM (2026-08-11)

> yes please known-but-unclosed list, fully autonomous mode until e2e fully
> implemented + tested + verified, usual workflow then main cicd + green + tag
> release

Prior standing decisions that remain in force:
> this branch is to optimise + overhaul tests so we can tag release v0.20 once
> everything goes green + well. after that we can then look at the
> known-but-unclosed items? before the rebased bluedb work continues

So: close the known-but-unclosed list → CI green on main → tag a release →
THEN rebase `feat/bluedb-v2`.

## THE LIST — this is what "done" is measured against

Every item below was declared honestly during the v0.20.0 cycle rather than
hidden. None silently passes today.

1. **93 kernel members have no Sky signature** across 15 pseudo-modules.
   Frozen by `rust/crates/project/tests/kernel_signature_coverage.rs`
   (ratchets DOWN only); `lower::reject_over_application` stops any of them
   reaching `go build` with a raw Go error. Closing them is per-module
   signature work in `sky-stdlib/`.

2. **Type-namespace ambiguity is not covered.** `[E1012]` covers values and
   constructors. Several type paths synthesise a `DefId` leniently when a
   module does not really export the name (kernel-implicit `Decoder`/`Value`/
   `Error`, re-exports), so two modules can yield two `DefId`s for one
   conceptual type — keying on that manufactures false rejections, the #164
   failure mode. The lenient synthesis has to be fixed FIRST.

3. **67 of 87 stdlib modules are dark to Family S.** Most are `Task`-valued or
   render `Element`s, which a value assertion cannot reach. `Sky.Core.Bytes`,
   `Sky.Core.Jwt`, `Std.Codec`, `Std.Markdown`, `Std.Compression` are largely
   pure and assertable — real, closeable gaps.

4. **Family S does not cross key TYPE against iteration OPERATION.** This is
   why issue #174's `Dict.foldl` panic reached a release despite the new
   corpus. Added 2026-08-11; the most direct evidence the suite has a shape
   gap, not merely a size gap.

5. **15 reject-corpus files declare no diagnostic code** — their rejections are
   unpinned, so any diagnostic satisfies them.

6. **The LSP corpus is one synthetic fixture** (`scripts/lsp-test-nvim.lua`).
   The GATE is genuinely enforced in CI (nvim installed, `lsp-fuzz` runs it);
   it is the CORPUS that is thin.

7. **`Std.Email` SMTP silently drops attachments**; `Std.Markdown` is thin.

8. **`toString`/`modBy`/`compare`/`negate` are asserted but uncountable** —
   they come from the kernel `Basics` pseudo-module, appear in no `exposing`
   list, so `api/symbols.json` has no entry and the coverage denominator cannot
   see them.

9. **The Playwright tier is CI-unreachable** — `verify-all-web.sh` runs only in
   `scripts/preflight-tag.sh`. That is how the `sky_sid` idle-eviction bug
   survived: its only gate never ran in CI. Four Go tests now carry that
   specific invariant, but the TIER is still release-only.

## Rules for this run (CLAUDE.md §0, plus what this session learned)

* **I cannot declare done.** A fresh-context adversarial Judge, given this file
  verbatim, returns the verdict. Any "but/except/mostly/for the scope of" in a
  PASS verdict means NOT done.
* **Every closure needs a gate that can FAIL**, proven by mutation. An item is
  not closed because code was written; it is closed when something would go red
  if it regressed.
* **MEMORY: at most TWO heavy agents at once.** Running two concurrent
  cargo+go builds OOM'd this 16 GB host earlier today and killed both agents.
  `mem-guard` must be alive before spawning any (`pgrep -f mem-guard.sh`).
* **Verify agent claims myself.** This session: a `db`-pool diagnosis of mine
  was wrong, `skydeploy/control-plane` "fails on main" did not reproduce three
  times, and a stale artefact produced four wrong verdicts. If a result
  contradicts the source, suspect the artefact first.
* **No new false greens.** A gate that cannot fail is worse than no gate — that
  is the premise this whole cycle was built on.

## Definition of done

Every item above either CLOSED (with a falsifiable gate) or explicitly
RE-DECLARED with evidence for why it cannot close now and a dated expiry — then
`main` CI green, then a release tagged.

---

## DISPOSITIONS — recorded 2026-08-12

Written after an adversarial Judge, given this file verbatim, returned
NOT ACHIEVED on `c77b6ec5` with 11 findings. Every disposition below
names the gate and the CI job that runs it, because "closed" here means
*something goes red if it regresses*, not *code was written*.

| # | Disposition | Gate | Runs in |
|---|---|---|---|
| 1 | **RE-DECLARED**, review by **2027-02-12** | `kernel_signature_coverage.rs` — exact-set ratchet + dated review test + a test asserting the declared count matches the list | `test-rest`, `macos-behaviour` |
| 2 | **CLOSED** | lenient synthesis narrowed first, `[E1012]` pinned by `ty/tests/reject/corpus/ambiguous_type_name.sky` + census | `test-ty`, `parity-reject` |
| 3 | **PARTIAL — five named gaps CLOSED, residual RE-DECLARED, review by 2027-02-12** | `DARK_MODULE_CEILING = 62` fail-on-increase + `ASSERTED_MODULES` exact pin | `test-rest` (structural), `behaviour-corpus` (behavioural) |
| 4 | **PARTIAL — structural half per-push, behavioural half NIGHTLY** | 5x3 `Dict` key-type x ACCESS-SHAPE crossing (operations are crossed inside each cell), 15 manifest rows; the behavioural half now executes at all, which it previously did not anywhere | structural: `test-rest`; behavioural: `nightly-sweep.yml` `behaviour-corpus` (NEW) |
| 5 | **CLOSED** | `EXPECTED_FILES_WITHOUT_DECLARED_CODE = 0`, three-way census, empty-corpus guard | `test-ty`, `parity-reject` |
| 6 | **CLOSED** — the count ratchet moved to the per-push face in 6f5048fb; this row said "release-only" after that stopped being true | 49 cases; the skip-to-green path now FAILS when `CI` is set | `lsp-fuzz` |
| 7 | **CLOSED** (`Std.Email`) / **RE-DECLARED to 2027-02-12** (`Std.Markdown`) | 10 wire-level Go tests; `declared_stdlib_gaps.rs` with expiry | Go wire-level tests: `codegen-build` + `macos-behaviour`; gaps test: `test-rest`; Sky-level suite: `behaviour-docs` |
| 8 | **CLOSED** | `the_four_uncountable_basics_are_now_counted` asserts both ends | `test-rest` |
| 9 | **PARTIAL — CI-reachable, not merge-blocking** | `nightly-sweep.yml` `web-runtime`, verdict from exit status | nightly only |

### What is honestly still open

* **Item 9 is not merge-blocking.** It HAS since been green on `main` — scheduled run 31678446956 at `9d2f6c30`, `web-runtime` success. An earlier line here said "never green on main", which was true when written and false within a day; the nightly that disproved it was dispatched deliberately to find out. Its snapshot
  arm also cannot see paragraph rendering: the compared snapshots target
  `section-*` ids that the paragraph/textColumn demo does not carry, which
  is why it could not have caught the `<div>`-inside-`<p>` defect fixed in
  `5b62285a`. Promoting it to per-push needs its runtime to be bounded
  first; that is a separate change, not a line in this table.
* **Item 6's `LSP_EXPECTED = 49` anti-shrink ratchet runs only at release.**
  The corpus is enforced per-push; a SHRINK of it is caught one tier later.
* **Item 3's residual 62 dark modules** are ratcheted against growth, not
  against staying at 62.
* **The T2 tier is NIGHTLY, not merge-blocking.** Review by **2027-02-12**.
  It was first wired per-push on a warm-cache estimate of 274s. The real cost
  is ~700s locally cold and **over 25 minutes on a GitHub runner**, where it
  was killed by its own timeout — all 335 cases are `go build`-ed. T1 has a
  900s ceiling that `ci-green` asserts, so keeping it there meant blowing the
  budget or raising the ceiling to fit a job I had just added, which is the
  drift that assertion exists to catch.

  So a regression in the 383 behavioural assertions lands and is caught the
  next night rather than at review. That is a real weakening versus per-push
  and it is stated here rather than rounded up to "closed". The path to
  closing it is SHARDING: `corpus --run` has no shard or filter flag, so
  partitioning the 335 cases across a CI matrix is a change to the gate
  itself. Until then this is strictly better than what it replaced, which
  was no execution anywhere.

### The lesson this round, recorded because it recurred

Three release gates (`conformance` census, `denominators`, `coverage-ledger`)
were red on `main` while per-push CI was green, because only `release.yml`
runs them — the same structural shape as item 9. And the T2 tier, holding
383 assertions including the crossing built after #174 escaped, ran in **no**
workflow at all.

Wiring T2 in closes those instances. `ci_green_needs_every_other_job_in_its_workflow`
is what stops the next one: the fan-in's `needs` list is what makes a job
merge-blocking, it was hand-maintained, and nothing checked it.

**A tier nobody runs is indistinguishable from a tier that does not exist.**
Before declaring any future item closed, name the workflow job that runs its
gate, and confirm that job is in `ci-green.needs`.

### Second Judge pass — 2026-08-13, and what it corrected

A fresh Judge reviewed the table above and returned NOT ACHIEVED. It was right
on every count below; each is now fixed or restated.

* **The same defect, two tiers over.** T2 was wired in and T3/T4 were still
  invoked by NOTHING — `apps-ledger-postgres`, `apps-dispatch-postgres`,
  `apps-fleet` (T3) and `apps-ffi-scale` (T4, the PRE-RELEASE tier, whose whole
  purpose is to run at release). T3 now runs nightly with a Postgres service;
  T4 runs in `release.yml`. `every_harness_tier_with_gates_is_invoked_somewhere`
  now fails if any tier with registered gates is invoked by no workflow and no
  script — mutation-proven.
* **Dates that nothing enforced.** Items 3 and 9, and the T2-is-nightly
  declaration, carried review dates in PROSE only, while items 1 and 7 got real
  dated gates in the same session. They are now tests in
  `rust/crates/xtask/tests/mandate_declarations.rs` that go red on their date.
* **`main` has NO required status checks.** `required_status_checks: null` —
  PR review is required, green CI is not. So "in `ci-green.needs` ⇒
  merge-blocking" was false, and the docstring asserting it has been corrected.
  Enabling required checks is a repo SETTING, outside this tree; until it
  happens, every gate in this cycle is advisory at merge time.
* **Row inaccuracies**: item 7`s 10 wire-level Go tests run in `codegen-build` + `macos-behaviour` (not `behaviour-docs`); item 4`s second axis is ACCESS SHAPE, not operation. NOTE: an earlier bullet here claimed item 2`s census runs in `test-rest` rather than the two jobs originally named — a third Judge showed THAT correction was itself wrong (the census is `ty/src/reject_corpus.rs`, invoked from `ty/tests/reject.rs` -> `test-ty` and `xtask/src/reject_gate.rs` -> `parity-reject`). The original row was right; the correction was the error.
* **Item 9`s gate was RED on `main`, and is now GREEN** — the 5-failure run predated the resilience fixes; scheduled run 31678446956 at `9d2f6c30` passed `web-runtime`. Recorded because the earlier entry was left standing after the evidence changed.

**The rule this pass adds:** a claim about WHERE a gate runs is itself a claim
that needs checking. Three rows named the wrong job, and one named a property of
branch protection that was not true. Verify the job, not the intention.
