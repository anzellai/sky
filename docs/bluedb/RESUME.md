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
