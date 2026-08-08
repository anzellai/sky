# CLAUDE.md

> **Entry point for Claude Code in the Sky repository.** The agent-agnostic
> source of truth is **[AGENTS.md](AGENTS.md)** (imported below) — it teaches the
> language, the app-building decisions, the stdlib (via `sky doc`), build & test,
> and the non-negotiable code rules for any AI tool. This file adds only the
> **Claude-Code-specific operational rules** that do not apply to other agents:
> the autonomous-loop protocol, mem-guard, and background-task hygiene.
>
> Read AGENTS.md first. Then the rules below govern *how Claude Code operates*
> when driving multi-step / autonomous work in this repo.

@AGENTS.md

## Claude Code — operational rules

These are Claude-Code-specific. They sit on top of AGENTS.md's engineering norms
(no-deferral, regression-test-first, timeout-bounding, disk hygiene, template +
doc sync) — which apply to every agent — and add the loop/session mechanics that
are specific to Claude Code's tools (`/loop`, Judge/Architect agents, the
`Workflow` tool, `ScheduleWakeup`, `Monitor`, the file-based memory).

### 0. Goal fidelity in autonomous loops — INVIOLABLE

When the user gives an autonomous mandate (`/loop AUTONOMOUS until
<goal>`, `/loop AUTONOMOUS <goal>`, or any equivalent multi-iteration
directive), the goal as the user worded it is the ONLY authority on
"done".

This rule applies to ALL autonomous mandates — current and future,
v0.17 / v0.18 / every compiler-cycle close, every product mandate,
every session resumed after compaction. It is **structural**: it
survives compaction, new sessions, and any redefinition I might
attempt under pressure.

#### The four hard rules

1. **The user's goal is captured VERBATIM** at mandate start and
   stored at `.claude/AUTONOMOUS_GOAL.md` in the project repo (so it
   survives clones, compactions, and new sessions). Subsequent
   iterations READ this file at entry and quote the goal back BEFORE
   doing anything else. If the file doesn't exist and an autonomous
   mandate is live, I reconstruct it from the user's most recent
   goal-setting message — using their words, not mine.

2. **I cannot declare "done".** Only an independent adversarial
   **Judge agent** spawned with a fresh context, given the verbatim
   goal, and verifying the ACTUAL claim (not a narrower lens I
   picked) can return "100% achieved". I MUST NOT scope, soften, or
   interpret the goal to fit what I shipped. Any "but/except/
   however/caveat/modulo/essentially/mostly" in the Judge's report
   → NOT done.

3. **Drift detection at every iteration.** Before each
   implementation step, I cross-check the planned step against the
   verbatim goal. If the step addresses a redefined / narrower /
   unrelated scope, I reset to the goal. Phrases that signal drift
   and are FORBIDDEN in any "complete" framing:
   - "criterion B OR clause", "load-bearing-but-pure", "documented
     as X" (when the goal said "deleted" / "removed" / "no impurity")
   - "shipped for the scope of [my chosen subtask]"
   - "iter N criteria all green" (when "iter N criteria" are MY
     definition, not the user's verbatim goal)
   - "deferred to Stage 6+", "spec backlog", "technical debt",
     "pre-existing", "out of scope for this iter"
   - "session boundary", "clean handoff"

4. **The only stop condition is a genuine implementation blocker.**
   I halt ONLY when I cannot proceed without user input (external
   auth wall, irreversible action requiring sign-off, ambiguous
   user-decision required). I describe the blocker concretely,
   await user direction, then CONTINUE the loop with their decision
   — I do NOT treat the blocker as "done".

#### The continuous-Judge loop protocol

```
iter_entry:
  1. Read .claude/AUTONOMOUS_GOAL.md (create from user's words if
     missing AND mandate is live)
  2. Quote the goal verbatim in a 1-line restate (drift gate)
  3. Spawn Judge agent (fresh context, see template below) — pass
     the verbatim goal + current branch SHA + read access to repo

  IF Judge says "100% ACHIEVED + VERIFIED":
    → PushNotification user with final outcome
    → Stop. Do NOT spawn another iteration.

  IF Judge says "NOT 100%":
    → Architect agent plans the closure of Judge's top gaps
    → Adversarial grillers attack the plan (>=2 in parallel)
    → Refine plan if grillers flag blocking concerns
    → Executor agents implement (parallel where independent)
    → SINGLE milestone verification at end of batch
    → Re-spawn Judge for re-verdict

  IF implementation blocker:
    → Document the blocker
    → PushNotification user describing what direction is needed
    → Wait for user response
    → On response: incorporate direction, resume the loop
```

#### Judge agent prompt template

> You are an INDEPENDENT adversarial Judge verifying whether the
> user-set goal has been 100% achieved on the Sky compiler at
> `/Users/anzel/works/playground/sky`, branch `<branch>` @ `<SHA>`.
>
> USER'S VERBATIM GOAL (read it from
> `.claude/AUTONOMOUS_GOAL.md`):
> ```
> <verbatim_goal_block>
> ```
>
> VERIFY the LITERAL claim, not a narrower interpretation.
> Examples of disqualifying findings:
>   * Goal says "100% fully typed e2e" → ANY `rt.Coerce` in
>     well-typed user code disqualifies. ANY `any` in emitted Go
>     for a fully-HM-typed expression disqualifies.
>   * Goal says "no runtime panics" → ANY panic class with a
>     known unfixed reproduction disqualifies.
>   * Goal says "if it compiles it works" → ANY Sky program that
>     passes `sky check` but fails `go build` OR panics at
>     runtime under well-typed semantics disqualifies.
>   * Goal says "rock solid + future-proof" → ANY architectural
>     band-aid, ANY "deferred to later" item, ANY known pending
>     compiler task in the umbrella scope disqualifies.
>
> Map every disqualifying finding to a concrete file:line +
> reproduction. List in priority order.
>
> Final verdict — EXACTLY one of:
>   * "VERDICT: 100% ACHIEVED + VERIFIED — <one-line proof>"
>   * "VERDICT: NOT ACHIEVED — <N> gaps; highest priority: <gap>"
>
> Forbidden in PASS verdict: "but", "except", "however",
> "caveat", "mostly", "essentially", "for the scope of", "modulo".

#### Workflow tool — auto-launch without re-asking

Once an autonomous mandate is live (`.claude/AUTONOMOUS_GOAL.md`
exists), `Workflow` tool invocations targeting that mandate run
WITHOUT a separate permission prompt to the user. The mandate
itself IS the durable permission. Re-prompting per workflow
invocation pauses progress and violates the continuous-Judge loop
protocol above. The user's edits to the goal file count as
ongoing direction; explicit instructions in chat take precedence
over default settings.

If the user has not granted a session-scoped allowance for
`Workflow` already, I add it once (settings.json) and proceed.
I do NOT pause iterations waiting for click-through approval.

### 0.1 Remote-push discipline — minimize CI noise

Local commits are checkpoints. Pushing to remote triggers CI for
every push. Constant per-commit pushes burn CI minutes, fail-spam
the branch status, and obscure real progress.

#### Rules

1. **Local commits are free; pushes are expensive.** Commit
   liberally to checkpoint progress on the feature branch. Only
   push to `origin` at meaningful milestones.

2. **A "meaningful milestone" is one of**:
   - A Judge agent verified phase boundary (e.g. "T1 leak class
     architecturally closed + verified")
   - An umbrella task closed (#383, #595, #644, #660, etc.)
   - A user-requested checkpoint (e.g. user said "push the
     current state")
   - A genuine blocker preventing further local work where the
     user needs to see what's pushed

3. **A new commit is NOT a milestone.** Neither is "all 3
   sequential gates green" if those gates verify only my narrow
   scope. Neither is "iter N shipped".

4. **Squash before push when sensible.** Many checkpoint commits
   at one milestone → squash to one well-described commit at push
   time. Preserve a tag/branch locally if I want to keep history.

5. **The user can override.** If they say "push now", push.

Forbidden patterns:
  * Pushing per /loop iteration just because gates went green.
  * Pushing a docstring fix as its own commit + push.
  * "I want CI to validate this" → that's what local gates are for.

### 0.2 Test-cadence discipline — no needless full-suite + wakeup cycles

The slow-progress pattern: edit → full cargo test suite → schedule
25-30 min wakeup → repeat. **This pattern is FORBIDDEN.**

#### Rules

1. **During implementation work, use the narrowest gate that
   proves the change is correct.** Targeted spec match (`--match
   "FooBar"`), single-example build, incremental build. Run these
   in seconds, not minutes.

2. **Full cabal test suite + full example sweep + verify scripts
   run ONLY at milestone boundaries.** A milestone is the same
   definition as 0.1 above. Not "I made a change". Not "I want to
   be safe".

3. **ScheduleWakeup is a SAFETY NET, not a pacing mechanism.** Its
   purpose is recovering from a genuinely stuck workflow / external
   event we cannot directly observe. It is NEVER used to "wait for
   cabal-test to finish" — `timeout N cabal-test` with `Bash`
   returns when done; no wakeup needed.

4. **Architecture + planning happen UP FRONT.** I do not edit
   code, run tests, edit again, run tests again — that's the
   debugging anti-pattern. I plan the full closure path with an
   architecture agent first, the executor agent(s) implement it
   coherently, THEN tests verify.

5. **Workflows over loop-of-edits.** When orchestrating multi-step
   work, use the `Workflow` tool (deterministic JS script that
   fans out agents). The Workflow runs to completion in one
   invocation. No ScheduleWakeup gaps between steps.

6. **Long-running test runs in the background.** When a full test
   suite IS warranted at a milestone, use `Bash run_in_background:
   true` so I am NOT blocked waiting. I do not ScheduleWakeup; the
   notification arrives when the test completes.

Forbidden patterns:
  * "Iter N shipped → run full suite → wake up in 30min for iter
    N+1" (a) iter N isn't a milestone, (b) full suite isn't
    justified, (c) wakeup wastes 30min.
  * "Wait for cabal-test for 25min via ScheduleWakeup" — use
    `run_in_background` instead.
  * Re-running the example sweep more than once per milestone.

Concrete cadence:
  * **Per change**: `cargo test -p <crate> <testname>` (narrowest
    crate + test filter that proves the change)
  * **Per phase boundary (multiple changes)**: rebuild + a couple
    of representative `cargo test -p <crate>` runs
  * **Per milestone**: full `cargo test --workspace` + the xtask
    gate suite (`cargo run -p xtask -- <gate>`) + example-sweep +
    verify-cli, in background, notified when complete

### 0.3 Architectural-mechanism citation — INVIOLABLE for compiler workflows

A compiler-level workflow that proposes closing a strategic goal via
a tactic MUST cite an architectural mechanism from the canonical
reference. Optimism without mechanism is forbidden in agent prompts
and judge verdicts.

#### The five hard rules

1. **Architecture reference is Phase 0.** All compiler-level
   workflows MUST begin by consulting `docs/rust-rewrite/` (the
   primary Rust-compiler architecture reference; the legacy
   `docs/architecture/sky-compiler-architecture.md` documents the
   retired Haskell pipeline and is kept for historical context)
   and, where stdlib semantics are touched,
   `docs/architecture/sky-stdlib-correctness.md`,
   before claiming a tactic closes a strategic goal. The first
   phase of every compiler workflow's JS DAG is
   `phase('Architecture-Consult')`. Tactics proposed without
   consulting the reference document are rejected at workflow
   entry.

   **Criterion #3 deletion-target wording (locked 2026-06-24).**
   Earlier framings of `.claude/AUTONOMOUS_GOAL.md` criterion #3
   read "`globalCgEnv` + `globalGoSigMap` IORefs DELETED". That
   wording UNDER-SPECIFIED the bridge IORefs (`scopeStateRef`)
   and the successor CAFs (`getCgEnvFromScope`, env-CAFs) that
   surfaced during the iter 17 / 37 / 42 / Class-A swap attempts.
   The locked wording is:

   > Criterion #3 = `{globalCgEnv, globalGoSigMap, scopeStateRef,
   > env-CAFs}` DELETED **AND** any residual IORef in `Compile.hs`
   > carries a machine-verified single-writer / single-reader
   > monotonic contract (see
   > `docs/v0.17-roadmap/phase-A-iter-0-anonrecords-contract.md`
   > for the `globalAnonRecords` precedent and the
   > `Sky.Build.AnonRecordWriterAuditSpec` verification gate).

   **This is NOT a relaxation** — it is a precise specification
   of the substantive purity guarantee. The original "DELETE"
   wording is satisfied by deleting the named IORefs; the
   "machine-verified contract" clause closes the loophole that
   would otherwise let an unnamed bridge IORef survive under a
   "load-bearing-but-pure" reframe (forbidden per §0 hard rule
   3). The contract has TWO parts:
   - Source-level contract docstring naming the writer site +
     reader sites + monotonic invariant (e.g. "register-on-
     first-mention; never overwrites; end-of-module barrier").
   - Spec gate (cabal-test) that builds a multi-module fixture
     and asserts the invariant programmatically — a write that
     overwrites OR reads a stale value MUST fail the gate.

   Any "close" claim against criterion #3 cites BOTH the named-
   IORef deletions AND the surviving-IORef contract+spec
   pair. Judge verdicts that PASS without the second citation
   are rejected.

2. **Tactical vs strategic feasibility.** Agents claim TACTICAL
   feasibility ("can I implement this change in N hours / one
   session?"). STRATEGIC feasibility ("does this tactic close the
   user goal?") is a USER-level decision taken AFTER the
   architecture reference is consulted and a mechanism is cited.
   An agent that conflates the two — claims "this closes the goal"
   without architectural citation — is wrong by construction.

3. **N-strikes circuit-breaker.** If 3 consecutive iterations fail
   to materially close the same criterion via the same lever, the
   next workflow MUST start with re-classification — NOT another
   attempt. Re-classification means: re-read the architecture
   reference, identify whether the criterion is in the irreducible
   floor (§8 of the reference), and escalate to the user with the
   floor citation. Continuing to retry the same lever past 3
   strikes is forbidden and counts as drift under §0 rule 3.

4. **Optimism-without-citation is forbidden.** Agent prompts must
   require, and judge verdicts must check, that any "close" claim
   names:
   - The Compile.hs / runtime / Solve site (with line citation)
   - The LowerCtx field, Solve reader, or runtime contract being
     consulted
   - The §6 origin category and §7 lever being activated
   A claim of "this closes rt.Coerce category X" without the §7
   lever name + the source-line citation is rejected. A judge
   that returns PASS without verifying the citations failed its
   adversarial duty.

5. **Floor-touching tactics need user authorisation.** Tactics
   that touch the irreducible floor (§8 of the reference — Go FFI
   return, gob/JSON wire decode, TEA reflect.MakeFunc dispatch)
   MUST escalate to the user before spending iterations.
   **AUTHORIZED 2026-06-23**: user has explicitly authorised
   floor-touching tactics for v0.17 close (literal-zero
   rt.Coerce via runtime rewrite — see
   `docs/v0.17-roadmap/literal-zero-close-plan.md`).

#### Workflow Phase-0 template (mandatory entry phase)

```js
phase('Architecture-Consult')
const archRef = await agent({
  prompt: `Read docs/architecture/sky-compiler-architecture.md.
For the proposed tactic <X>:
  1. Locate the §6 rt.Coerce origin category it would target.
  2. Identify the §7 architectural lever it would activate.
  3. Verify the lever is NOT in §8 (the irreducible floor) — OR
     confirm user-authorisation for floor-touching tactics is
     present.
  4. Cite the Compile.hs site (with line) + LowerCtx field /
     Solve reader / runtime contract being consulted.
If you cannot make all four citations, return cannotJustify=true
with a description of what's missing.`,
  schema: ARCH_REF_SCHEMA
})
if (archRef.cannotJustify) {
  return { halted: 'no architectural justification', missing: archRef.missing }
}
if (archRef.inFloor && !userAuthorizedFloor) {
  return { halted: 'tactic touches irreducible floor; user authorization required' }
}
// proceed to tactical phases
```

#### Forbidden patterns

* Agent prompts: "design and implement a fix for X" without
  requiring the architecture reference be consulted first.
* Judge verdicts: "VERDICT: 100% ACHIEVED" without listing the
  §6 categories closed + §7 levers activated + §8 floor sites
  documented.
* Workflows: skipping `phase('Architecture-Consult')` to "save
  time" — the architecture phase IS the time-saver because it
  short-circuits re-discovering the floor.
* Iteration N+1 after 3 consecutive failures on the same lever
  without re-classification.

#### Companion canonical references

- `docs/rust-rewrite/` — primary Rust-compiler architecture
  reference: pipeline (Parse → Canon → Type → Lower → Emit),
  workspace + crate layout, rt.Coerce origin catalog,
  architectural levers, irreducible floor, verbatim-goal verdict.
- `docs/architecture/sky-compiler-architecture.md` — legacy
  Haskell-pipeline reference, retained for historical context.
- `docs/architecture/sky-stdlib-correctness.md` — Sky.Core
  algebraic laws, Std.Ui layout invariants, Std.Html + Sky.Live
  TEA architecture, Std.Db + Std.Auth security invariants,
  cross-backend parity, per-module correctness verdicts.
- `docs/rust-rewrite/13-change-verification-and-edge-cases.md` —
  **read before landing any `hir`/`ty`/`lower` change.** The
  edge-case matrix (D1 type-reference resolution, D2 record shapes /
  row-poly, D3 annotation state, D4 module structure, D5 app shape)
  + the MANDATORY verification protocol. The corpus gates
  (`infer`/`roundtrip`/`reject`/`repro`) are necessary but NOT
  sufficient — a change is not "verified" until it has also passed
  `scripts/example-sweep.sh` (the FULL sweep, incl. Std.Db/FFI
  examples) + a real app. Multiple fixes this cycle passed every
  corpus gate and still regressed real apps (#164 import-alias,
  #164 stdlib-name collision, #166 Std.Db `Dict` field); this doc
  catalogues those traps so they are considered up front.

These are the durable ground truth across sessions, agents, and
workflows. `docs/rust-rewrite/` is the FIRST source consulted on
any compiler change (stdlib-correctness for stdlib changes) — not
the in-memory model, not prior session context, not optimistic
"we can do it" framing.

### 0.4 Session methodology — phase pattern + agents + grilling + verify

The patterns below are the durable approach used on every non-trivial
work item. They are LOAD-BEARING — sessions that skipped them
historically produced cascade regressions (iter 17 / 37 / 42 /
Class-A swap attempts). Future sessions follow these by default.

#### Phase pattern (decide → plan → execute → verify)

1. **Decide what's IN scope** before doing any work. Write an explicit
   scope decision with rationale (what's in, what's deferred, what
   the success criterion is). Per CLAUDE.md §0 rule 1 — verbatim
   user goal is captured at `.claude/AUTONOMOUS_GOAL.md` for
   autonomous mandates.
2. **Plan** the execution as discrete additive phases. Each phase
   ships its own commit. Phase boundaries are checkpoints —
   verifiable, revertable, and shippable in isolation.
3. **Execute** one phase at a time. Per CLAUDE.md §0.2 — narrow
   gates per change, full sweep at milestone boundaries only.
4. **Verify** at every phase boundary. Per CLAUDE.md §0 — Judge
   agent verification at the close, fresh-context, adversarial.

This is the pattern that shipped v0.17.0 (typed-emit fix +
documented rt.Coerce surface + scopeStateRef contract + panic-class
gate locks) in additive phases with zero regression.

#### Agent + grilling pattern (for non-trivial work)

For any work where solo execution carries cascade risk
(Compile.hs surgery, multi-system changes, broad audits), the
DEFAULT pattern is:

1. **Architecture-Consult agent** (Phase 0) — fresh-context agent
   reads `docs/architecture/sky-compiler-architecture.md` +
   `docs/architecture/sky-stdlib-correctness.md`, cites §6
   rt.Coerce origin + §7 lever + §8 floor for the proposed
   tactic. Returns PROCEED / REVISE / ABORT.
2. **Adversarial grill** (Phase 0b) — the architecture proposal
   is grilled BEFORE implementation. Grill questions:
   - G1: Could this produce false negatives (gaps the regression
     wouldn't catch)?
   - G2: Could this produce false positives (over-eager
     rejection)?
   - G3: Estimated cost? Time budget bounded?
   - G4: Layering clean? Dependency direction correct?
   - G5: Does this close the criterion, or just document a partial
     close?
3. **Implement** with the grilled plan — phase boundaries
   commit + verify.
4. **Judge re-verify** at close — fresh-context Judge agent runs
   the actual verification commands, returns PASS / NOT ACHIEVED
   with concrete file:line citations.

Agent prompts include FORBIDDEN PHRASES in PASS verdicts
("but / except / however / caveat / mostly / essentially / for
the scope of / modulo"). These signals indicate the verdict is
drifting from the literal claim.

#### Three-leg soundness stool (for soundness claims)

A soundness claim ("no runtime panics from well-typed Sky") is
verified by THREE independent legs, not one:

1. **Runtime classification leg** — Go-side tests (e.g.
   `runtime-go/rt/panic_recover_test.go`) proving the panic
   surface is correctly classified.
2. **Emission-time leg** — Sky.Build specs (e.g.
   `Sky.Build.PanicClassGateSpec`) proving the lowering does NOT
   emit raw panic-prone Go ops AND the safety net is wired.
3. **Real-world e2e leg** — example sweep + verify-cli + Playwright
   + fuzzer (`Sky.Build.WellTypedFuzzerSpec` 10k iter) proving
   real-world + random programs do not panic.

A single-leg "proof" is NOT a proof; ship all three legs.

#### N-strikes circuit-breaker (per CLAUDE.md §0.2, reinforced)

3 consecutive failures on the same architectural lever (iter
17 / 37 / 42 / Class-A swap pattern) FORBIDS a 4th attempt without
re-classification. Re-classification means:

1. Re-read `docs/architecture/sky-compiler-architecture.md`
   §6/§7/§8.
2. Identify whether the criterion sits in the irreducible floor
   (§8). If yes — escalate to user with the floor citation.
3. Author a postmortem of what the 3 prior attempts missed.
4. Get explicit user authorization for the 4th attempt with the
   postmortem cited.

Without re-classification, a 4th attempt counts as drift per CLAUDE.md
§0 rule 3 and the session is forfeit.

#### Reframed vs literal goal handling

When the user reframes a goal mid-mandate (e.g. v0.17 "100% fully
typed" → "rock solid + ~100% sound with documented surface"):

- The verbatim goal at `.claude/AUTONOMOUS_GOAL.md` REMAINS the
  literal anchor. Don't overwrite it without user direction.
- The reframe is a SHIPPING SCOPE decision, not a goal change.
  Both readings must be verified at close — Judge returns
  separate LITERAL and REFRAMED verdicts. The reframe says which
  is required for the release; the literal verdict tracks
  long-term progress.
- Per CLAUDE.md §0 rule 3 — phrases like "for the scope of",
  "shipped under the reframe", "essentially closed" in a literal
  verdict are forbidden. Be precise about which goal a closure
  satisfies.

#### Push discipline (per CLAUDE.md §0.1, reinforced)

Local commits are checkpoints; remote pushes are CI invocations.
The right cadence is BATCH at milestones, not per-commit. A
milestone is one of:

- A Judge-verified phase boundary
- An umbrella task closed
- A user-requested checkpoint
- A genuine blocker requiring CI cross-platform verification

Per-commit pushes burn CI minutes, fail-spam branch status, and
obscure real progress. The pattern that worked: ship 3-5
related commits LOCALLY, run a milestone Judge verification,
then push once. v0.17.0 closure shipped this way (5 commits in
2 pushes vs the 6 individual pushes that preceded the
correction).

#### Context discipline (the underlying constraint)

Per `docs/session-protocol.md` (folded here): Claude has a
finite context window. On this codebase, two patterns burn it
fast: (1) task-list reminders compound after every Bash call,
(2) reactive grep-read-edit cycles produce dozens of small
operations.

Mitigations:

- **Read with `offset` + `limit`**, never naked Read on files >
  1000 lines. For `Compile.hs` (23k lines), know the line number
  before reading.
- **Delegate exploration to agents.** Audit-style questions go
  to `Explore` subagents whose context isolates the burn.
- **Batch Bash calls** when independent. Chained `&&` beats
  three separate invocations.
- **Scripts over individual invocations.** `scripts/cabal-test.sh`
  encapsulates timeouts + resource guards; raw `cabal test` does
  not.
- **Don't use TaskCreate / TaskUpdate / TaskGet** unless the user
  explicitly asks. The task list (~400 entries deep) is appended
  after every Bash call as a reminder; cleanup costs more than
  skipping it. Plan + execute via phase boundary commits instead.

#### Stop conditions and honesty

- **Bounded surface = bounded session.** Touching >5 files or
  >200 lines without a clear delegation strategy is a flag —
  delegate to a focused agent or stop and checkpoint.
- **"I can't finish this in this session" is a valid outcome**
  with a checkpoint file (e.g. `docs/v0.17/session-N-checkpoint.md`).
  "I'll keep trying" without a path forward is not.
- **3 attempts on the same approach → halt and reclassify**, not
  a 4th retry. Per N-strikes above.

### 1. Memory safety — `scripts/mem-guard.sh` MUST run during dev

A runaway `sky` / `cargo` / `rustc` / `rust-analyzer` process
has previously force-powered-off the host Mac. Treat the absence of
mem-guard like a missing `set -e`.

```bash
nohup ./scripts/mem-guard.sh > /tmp/mem-guard.out 2>&1 &
disown                                # survives shell exit
```

Defaults (16 GB Mac): per-process kill at 6 GB RSS for compiler
tooling (`sky` / `cargo` / `rustc` / `rust-analyzer` / `cc1` /
`ld` / `go` / `gopls` / `sky-ffi-inspect`; legacy `cabal` / `ghc`
/ `ghc-iserv` / `haskell-language-server` still covered); 10 GB
panic tier for the dev-session host
(`claude` / `node` / `ghostty`); system-pressure floor kicks in
when free + inactive + speculative memory drops below 1.2 GB. Tune
via `MEM_GUARD_PROC_MB` / `MEM_GUARD_PANIC_MB` /
`MEM_GUARD_SYS_FLOOR_MB`. `MEM_GUARD_DRY=1` runs in log-only mode.
Never silence a kill by raising the threshold — the kill means the
process was on a path to OOM the machine. Fix the underlying
compiler bug.

### 2. Background-task hygiene — clean up before declaring "done"

Long sessions accumulate orphan `run_in_background` zsh wait-loops
that eventually exhaust the per-uid process table
(`fork: retry: Resource temporarily unavailable`). When that
happens `mem-guard.sh` silently dies and the user's binaries get
killed instantly on launch.

End-of-mission checklist:

```bash
# Orphan polling loops
ps -u $USER -o pid,command | awk '/while pgrep|until ! pgrep/ && /\/bin\/zsh -c/ {print $1}' | xargs -n1 kill -9 2>/dev/null

# Stray sleeps + verification leftovers
ps -u $USER -o pid,ppid,command | awk '$3 == "sleep" && $2 != 1 {print $1}' | xargs -n1 kill -9 2>/dev/null
pkill -f "playwright"; pkill -f "chromium"
pkill -f "examples/.*/sky-out/app"

# mem-guard alive?
pgrep -f mem-guard.sh >/dev/null || (nohup ./scripts/mem-guard.sh > /tmp/mem-guard.out 2>&1 & disown)
```

**Prefer the Monitor tool** over `run_in_background` + polling.
Monitor delivers events without leaving a wait-loop subprocess.

