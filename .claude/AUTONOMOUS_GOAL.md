# AUTONOMOUS GOAL (captured verbatim 2026-09-14)

> all of the known gaps/follow-ups please, unattended + autonomous + PIV

## Scope — the "known gaps/follow-ups" this refers to (from the state report given
just before the mandate):

1. **Native OpenAI function-calling** — `Provider.chatTools`. The tool protocol is
   prompt-based today; add native function-calling (tools param + tool_calls in the
   response) and let `Agent.toolLoop` use it.

2. **`Std.Ai.Tool` codegen bug — ROOT fix.** A named let-binding lambda that
   destructures a single-ctor union parameter under `List.map` erases the param to
   `any` then emits `.Fields` on it (type-checks; `go build` fails). Fix at the
   compiler root (lower.rs / goty.rs / codegen), then REMOVE the accessor workaround
   in `sky-stdlib/Std/Ai/Tool.sky` (`instructions`/`run` back to `\(Tool t) ->`),
   with a regression test that fails before the fix. Compiler work → MUST begin with
   Architecture-Consult on docs/rust-rewrite/14 (§3 origin, §5 lever, §1 floor test,
   the gate that goes red on regression) per CLAUDE.md §0.3.

3. **Durable v2** (docs/design/durable-execution.md "v2" section):
   - transparent Msg-replay (durable without explicit `step`, via the mock/replay seam)
   - worker versioning + migration for in-flight runs
   - history compaction

## Definition of done (PIV = Plan → Implement → Verify, per CLAUDE.md §0.4)
Each gap: Plan (architecture-consult + adversarial grill for compiler/large work) →
Implement (regression-test-first; phase-boundary commits) → Verify (narrow gate per
change; full milestone gate — cargo test --workspace + xtask harness + coerce-floor +
T2 corpus + example-sweep + conformance — at each gap boundary; a fresh-context Judge
confirms the LITERAL gap is closed, no "but/except/mostly").

NOT done until an independent Judge verifies every gap above is closed AND the full
release gate is green. A gap that proves to be an irreducible floor (doc 14 §4) or a
genuine multi-week design item is escalated to the user with the citation — not
silently narrowed.

## Ground rules
- Single branch `feat/std-ai-durable-followups` until done.
- Compiler changes: doc 14 architecture-consult first; regression-test-first; run
  xtask gates (incl. coerce-floor) locally before any push.
- No tag / no merge to main / no deploy without explicit user sign-off (user owns
  tag scope). This mandate is BUILD + VERIFY on the branch.
