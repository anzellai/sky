# BlueDB v2 — RESUME

1. Read `.claude/AUTONOMOUS_GOAL.md` (the mandate).
2. Read `docs/bluedb/v2-architecture.md` (this design).
3. Run: `cargo run -p xtask -- bluedb-gates --tier=full`
   Its output IS the state. `docs/bluedb/STATUS.md` is that output, committed.
   The FAST tier alone leaves the hardest gates NOT RUN, which renders their
   goals UNKNOWN — never PASS. If you only have time for the fast tier, read
   the UNKNOWNs as "not yet known", not as "fine".
4. A goal is closed only when its row says PASS with zero ⊘ and zero ⊗.
5. Do not trust any prose in any doc about what is done. Run the gates.

At phase boundaries also run:

```
cargo run -p xtask -- bluedb-gates --verify-mutations
```

It re-derives every gate's falsification proof in a scratch git worktree. The
canary `G0.C` must report `VACUOUS`; anything else means the verifier is not
verifying, and every `PROVEN` above it is worthless.

No phase table with ✅ marks exists anywhere on this branch. The prior branch's
phase table is precisely the artefact that survived compaction while the
evidence behind it evaporated.

## OPEN — G0.3's falsification is VACUOUS in the mutation harness (2026-08-14)

**State:** G0.3 PASSes in the dev tree with all four arms measured. Its
falsification does NOT work, and `--verify-mutations` correctly reports
`VERIFY-MUTATIONS: FAIL` / `G0.3/persistglue-unconditional VACUOUS`. So the gate
is green with no proof it can fail. That is visible, not hidden, and it must be
closed before P1 is called done.

**Diagnosis (verified, not theorised).** The mutation applies cleanly — it adds
`import _ "sky-app/bluedb"` to `rt`, the exact defect shape the prior attempt
shipped. The gate then fails, but for the WRONG reason, so the runner classifies
it VACUOUS on the discriminating assertion:

  * `sky_compiler(ctx)` (gates_g0.rs) looks for `rust/target/release/sky` or
    `sky-out/sky` **under `ctx.root` only** — correct, and required by the H3
    invariant in `registry.rs`: "a gate body that reaches outside `ctx.root`
    breaks the mutation runner's guarantee silently".
  * The runner's scratch worktree is a fresh `git worktree add`. It contains no
    build artefacts, so neither path exists.
  * G0.3 returns "neither rust/target/release/sky nor sky-out/sky exists", which
    does not contain its declared `expect` ("pebble symbols in a non-Persist
    binary"). Red for the wrong reason = VACUOUS. The classifier worked.

**Options, and the trade-off that has to be decided:**

1. Build the compiler inside the worktree (`cargo build --release -p sky`).
   Honest and self-contained; a release build may not fit the 900s budget.
2. Let the runner pass a compiler path explicitly (e.g. `SKY_BLUEDB_COMPILER`)
   while ASSETS keep resolving from `ctx.root`. The subject stays inside the
   worktree, so H3 holds for what is being certified, and the tool is external
   BY DECLARATION rather than by accident. **Caveat that decides the design:**
   for any mutation whose `targets` include compiler source
   (`rust/crates/project/src/build.rs` — G0.5's does), a prebuilt external
   compiler would NOT contain the mutation, silently weakening exactly those
   proofs. So this must be gated on the mutation's declared targets.
3. Re-point G0.3's mutation at a compiler-independent arm (arm (b), "ships no
   `bluedb/`"). Cheapest, and weakest — arm (a) is the arm that matters.

Recommendation: (2), scoped by `targets`, with (1) as the fallback for
compiler-source mutations. Do NOT take (3) — arm (a) is the property.
