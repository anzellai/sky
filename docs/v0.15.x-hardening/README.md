# Sky compiler v0.15.x hardening — autonomous loop protocol

> **Mandate (verbatim from user, 2026-05-25):**
> *"Never defer work, set a loop and schedule for each agent to ensure
> they don't stop, defer, or midway give me summary (that's the
> checkpoint I will stop here etc.)."*
> *"NO v0.16.x — keep these works under v0.15.x."*
> *"Regardless of how many sessions/days worth of work you encounter,
> don't defer, and set a loop/schedule for all the agents to actually
> complete the full scope e2e."*

This directory is the shared workspace for three specialised agents
that cycle indefinitely until the Sky compiler is **100 % reliable,
secure, efficient, performant, and long-term maintainable** — every
known gap closed, every audit clean, no soundness backdoors, no
unbounded resource use.

All work ships as **v0.15.x patch releases**. There is no v0.16.
The cascade plan formerly named "v0.16" in `improvement-plan-v0.16.md`
is renamed and continues under `v0.15.x`.

---

## Agents

### 1. Auditor — `subagent_type: Explore` + audit prompt
**Role.** Adversarially grills the compiler implementation. Hunts for
soundness gaps, panic surfaces, security holes (FFI escape hatches,
auth secret leaks, code-injection vectors), memory/CPU pathologies,
and footguns the user could trip without the compiler complaining.

**Forbidden vocabulary.** "defer", "later", "summary so far",
"checkpoint", "hand off mid-cycle".

**Output.** `audits/CYCLE-NN-auditor.md` — strict format:
```
## Gap N (severity: critical|high|medium|low)
File: src/Sky/.../X.hs:LINE
Symptom (≤2 sentences):
Reproducer (concrete Sky code OR shell sequence):
Root-cause hypothesis:
Why current tests miss it:
```

### 2. Planner — `subagent_type: Plan`
**Role.** Sky compiler architect. Takes Auditor output + open items
from `docs/fragility-audit-v0.15.3.md`. Produces a deep-analysis +
sequenced PR-sized plan that closes every gap WITHOUT introducing new
ones. Plans MUST respect the v0.13 fully-typed contract floor and the
non-regression rules in CLAUDE.md §4.

**Output.** `plans/CYCLE-NN-planner.md` — strict format:
```
## Item N: <gap title>
Auditor reference: CYCLE-MM/Gap-K
Architectural diagnosis (1-3 paragraphs):
Sequenced steps (each commit-sized):
  1. ...
  2. ...
Files touched: src/Sky/.../X.hs:LINES, runtime-go/rt/Y.go:LINES
New tests (cabal + go + .sky):
Rollout / regression gates:
Estimated session-cost (hours):
Risk register:
```

### 3. Developer — `subagent_type: general-purpose` (worktree-isolated)
**Role.** Implements one Planner item per cycle. Lands a feature
branch + opens a PR + drives CI green + merges + cuts the next
patch tag (`v0.15.N+1`).

**Hard non-negotiables (verbatim from CLAUDE.md):**
- mem-guard MUST be alive.
- Background tasks MUST be cleaned up.
- ALL 27 examples MUST build from clean slate.
- `scripts/verify-all-web.sh` + `scripts/verify-cli.sh` MUST pass.
- 306 cabal specs + 120 stdlib assertions MUST stay green.
- `sky fmt` on every changed .sky file.

**Output.** `implementations/CYCLE-NN-developer.md` — strict format:
```
## Item N implemented
Branch: feat/v0.15.x-hardening-N-<slug>
PR: #NNN
Tag: v0.15.M (if cut)
Files changed: ...
Test evidence (paste tail of test output):
Example sweep evidence:
Verification scripts evidence:
Live deploy evidence (skydeploy if applicable):
```

### 4. Head Arbitrator — spawned ONLY on agent disagreement
**Role.** "Head of compiler / performance / security". Re-reads the
Auditor + Planner + Developer trail, applies Sky's principles
(CLAUDE.md §3), and writes a single authoritative direction. All
agents commit to it on the next cycle.

**Output.** `arbitrations/CYCLE-NN-arbitration.md`.

---

## Cycle log

Every cycle appends one line to `CYCLE_LOG.md`:
```
CYCLE-NN | YYYY-MM-DD HH:MM | AUDITOR done (N gaps) | PLANNER done (M items) | DEVELOPER PR #NNN merged | tag v0.15.K
```

A cycle is **complete** only when the Developer's PR has merged AND
the next patch tag has been pushed AND the release workflow shows
green. If any of those fail, the cycle re-runs (Auditor takes the
failure as input).

---

## Stopping conditions

The loop stops ONLY when **all three** of the following hold for three
consecutive cycles:

1. Auditor finds zero NEW gaps that aren't already tracked.
2. All items in `docs/fragility-audit-v0.15.3.md` are marked
   `[CLOSED]` with the closing commit SHA.
3. `examples/13-skyshop` builds in under 60s wall-clock with peak
   RSS under 1 GB.

When the loop stops, the Coordinator opens a final PR titled
`docs(v0.15.x): hardening loop terminus — all gates green` that
removes this README and archives the workspace under `docs/archive/`.
