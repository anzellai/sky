# Session protocol for working with Claude Code on Sky

> Read this at the start of every Claude Code session on this repo. It exists because Claude's effective context budget on long compiler-internals sessions is consumed by harness behaviors that can be substantially mitigated with explicit upfront instructions.

## Why this document exists

Claude has a finite context window per session. On the Sky compiler codebase specifically, two patterns burn context fast:

1. **Task-list reminders.** The harness appends the full task list (~400+ entries, ~3-5k tokens) after every Bash call that goes long enough to trigger the reminder cadence. With 50+ Bash calls per session that's 200k+ tokens of pure overhead.
2. **Reactive grep-read-edit cycles.** When debugging compiler internals, ad-hoc exploration produces dozens of small Reads and Bash invocations that each individually look cheap but compound rapidly.

The combination has caused multiple sessions to burn out at 30-60% of useful work, with the remainder consumed by harness noise. This document encodes the practices that work around both.

## Session-start instruction block

Paste this (or equivalent) at the start of every compiler-internals session:

```
v0.17 finish — Session N: <single specific goal in one sentence>.

Operating rules for this session:
- Don't use TaskCreate / TaskUpdate / TaskGet unless I explicitly ask. The task list reminders compound and burn context fast on this repo.
- Don't clean up the task list. It's 400+ entries deep; cleanup costs more than skipping it.
- For multi-file searches, codebase audits, or "find every site that does X" work, dispatch an Explore agent (subagent_type: Explore). Their context is separate from yours.
- For 5+ sequential Bash+Edit cycles on one well-scoped change, dispatch a general-purpose agent with explicit safety gates.
- Use Read with offset+limit, never naked Read on files > 1000 lines.
- Stop condition: <specific, falsifiable>.
- If you can't finish: write docs/v0.17/session-N-checkpoint.md with (a) current state, (b) what's done, (c) what's next, (d) what you tried and reverted. Then stop. Don't grind.
- If the path forward isn't clear after 3 attempts, halt and ask. Don't reframe the goal mid-session to fit what you can do.
```

For stdlib / SkyDeploy / sky-lang.org sessions where compiler internals aren't involved, the rules are less critical but still useful — task list discipline matters most when there are lots of sequential Bash calls.

## What I (Claude) commit to

### Context discipline

- **Read targeted slices.** `Read` with `offset`/`limit`, not whole-file. For 23k-line `Compile.hs`, I should know roughly the line number before reading.
- **Bash for shell-only ops.** Prefer `Read` over `cat`/`head`/`tail`. Prefer `Grep` (the dedicated tool) over `bash grep`. Both avoid the task-list reminder cadence the way Bash triggers it.
- **Delegate exploration to agents.** Audit-style questions ("classify every rt.Coerce site", "find all IORef readers") go to `Explore` agents. The agent's context isolates the burn.
- **Batch related Bash into single calls** when independent. `cabal build && cabal install && verify` as one chained command beats three separate ones.
- **Scripts over individual invocations.** `scripts/cabal-test.sh` beats individual `cabal test --test-options ...`. The scripts also encapsulate timeouts and resource guards.

### Stop conditions and honesty

- **Bounded surface = bounded session.** If I touch >5 files or >200 lines in one session without a clear delegation strategy, that's a flag I'm grinding rather than executing.
- **Strike count.** If a "fix" reverts twice, the next attempt requires a written reclassification of the problem, not another attempt at the same lever. This is a CLAUDE.md §0.3 rule already; I should follow it strictly.
- **Honest stop.** "I can't finish this in this session" is a valid outcome with a checkpoint file. "I'll keep trying" without a path is not.

### Artifact discipline

- **One artifact per session.** Either a code change with a clear commit message OR a written document (audit / checkpoint / design). Mixed unstructured changes are how state gets confused.
- **Checkpoint files live under `docs/v0.17/`.** Format: `session-N-<topic>.md`. Each must include: (a) goal, (b) state at start, (c) actions taken, (d) state at end, (e) next session pickup point.

### Agent delegation patterns

When to use which:

| Task shape | Tool |
|---|---|
| Find files matching a pattern, count occurrences | `Grep` tool directly (not Bash) |
| Read a known section of a known file | `Read` with `offset`/`limit` |
| "Find every place that does X across the codebase" | Dispatch `Explore` agent |
| "Write code change touching 3+ files with safety gates" | Dispatch `general-purpose` agent with explicit pass/fail gates in the prompt |
| Audit / classification work (no code change) | Dispatch `Explore` agent, ask for written output to `docs/v0.17/` |

When NOT to use agents:

- One-line edits where the location is already known.
- Verifying a known fact (re-reading a doc).
- Anything where the prompt to the agent would be longer than just doing it.

## The v0.17 finish protocol

Reframed from "100% typed e2e" (where I struggle) to **"rock solid + ~100% sound, with documented surface for remaining `rt.Coerce`"** (where the work is bounded audit + bounded fixes).

### Session structure

Each session targets one of these stages. Stop at session boundary, write the artifact, move to next session.

**S1 — Audit.** Inventory remaining `rt.Coerce` / `rt.MaybeCoerce` / `rt.CoerceString` etc sites in emitted Go for representative examples (`00-standard-libs`, `26-ui-showcase`, `13-skyshop`). For each pattern, classify:
- **Identity:** source Go type ≡ target type (provably safe, no panic possible)
- **Bounded:** source provenance comes from Sky-side HM that always satisfies the target
- **FFI boundary:** crosses Sky↔Go runtime, panic guarded by Task/Result boundary
- **Unknown:** needs investigation

Artifact: `docs/v0.17/coerce-audit.md`. No code changes.

**S2 — Soundness contracts.** For each "Bounded" class, write the Sky-side HM invariant that guarantees safety. For each "FFI boundary" class, document the boundary contract. For each "Unknown", either reclassify after investigation or list as a v0.17 blocker.

Artifact: `docs/v0.17/soundness-contracts.md`. No code changes.

**S3 — Property test extension.** Extend `WellTypedFuzzerSpec` from 10k to 100k runs. Add panic-class targeted generators: heterogeneous slices, deeply-nested record updates, polymorphic HOF chains, FFI return shapes. Run.

Artifact: clean 100k run OR a list of new panics discovered.

**S4 — Panic fixes.** For each panic found in S3: one regression test + one code fix. Bounded surface per fix; if a fix needs >50 lines, split into its own session.

Artifact: green property tests, list of fixes shipped.

**S5 — Stdlib gaps.** Close G1 (Task.parallel early-termination), G2 (Sky.Tui warnings), G3 (Math.isNaN export), G4 (Db.migrate tenant docs), G5 (Maybe/Result/Task law tests). Each is its own commit.

Artifact: 5 commits, each independently verifiable.

**S6 — Release.** Sweep examples, run verify-cli + verify-all-web, update CLAUDE.md non-regression rules with the accepted Coerce surface, draft `docs/v0.17-release-notes.md`, prep merge.

Artifact: branch ready to merge, release notes complete.

### Stop conditions for v0.17

The release gate (replaces "100% typed e2e"):

1. Zero new panic classes reachable from well-typed Sky code (100k property test runs clean + 32 example sweep + verify scripts all green).
2. Every remaining `rt.Coerce` site has a written soundness justification (Identity / Bounded / FFI-boundary).
3. Stdlib gaps G1-G5 closed.
4. CLAUDE.md updated with the accepted Coerce surface and the soundness contract.
5. Release notes drafted; branch passes CI.

If any of (1) (2) (3) blocks on a real codegen change that requires the architectural surgery I'm not suited for, surface it explicitly as a Gemini handoff item rather than thrash.

## When to call this protocol off

This document encodes practices for long compiler-internals sessions. For:

- Quick bug fixes with clear scope: ignore most of this, just do the fix.
- Stdlib feature additions: most rules still apply but task discipline is less critical.
- Documentation / planning sessions: just do the work, the harness reminders don't compound.

If a session is clearly bounded (one file, <100 lines, known goal), skip the formal protocol and do it directly. The protocol exists for the failure mode, not as ceremony.

## Maintenance

This document should be updated when:

- A new context-burn pattern is discovered.
- The agent / tool surface changes meaningfully.
- The v0.17 protocol stages need refinement based on what worked / didn't work.

Last updated: 2026-06-27.
