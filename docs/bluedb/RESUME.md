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

## CLOSED — G0.3's falsification is PROVEN (2026-08-14)

`G0.3/persistglue-unconditional` reports `PROVEN`, the full
`--verify-mutations` reports `PASS`, and the canary still reports `VACUOUS`.
Two distinct causes had to be removed, and the second is the one worth
remembering.

**Cause 1 — the worktree had no compiler.** `sky_compiler` resolved
`rust/target/release/sky` / `sky-out/sky` under `ctx.root` only (correct, per
the H3 invariant), and the runner's scratch worktree is a fresh `git worktree
add` with no build artefacts. G0.3 therefore went red with "neither
rust/target/release/sky nor sky-out/sky exists" — red for the WRONG reason, so
the discriminating classifier refused to call it PROVEN. It was right to.

Fixed by lending the probe a prebuilt compiler through `SKY_BLUEDB_COMPILER`
(option 2 of the three that were on the table): the TOOL comes from outside, the
SUBJECT never does — `sky-stdlib/`, `runtime-go/` and the witness project all
still resolve inside `ctx.root`. `mutations::mutation_touches_compiler` refuses
to lend it for any mutation whose PATCH touches `rust/`, because a prebuilt
binary would not contain such a mutation and the proof would be silently
weakened. That question is answered from the patch, never from the declared
`targets` — `targets` is deliberately broader (it drives `UNVERIFIED-SINCE`),
and reading it here would have left G0.3 permanently vacuous.

**Cause 2 — HEAD skew, which is why cause 1 looked unfixed after it was
fixed.** With the fix written but not yet committed, `--verify-mutations` kept
reporting `VACUOUS` against source that plainly contained the fix. The scratch
worktree is `git worktree add --detach HEAD`: the probe compiles and reads the
last COMMIT. The parent — which applies the patch, classifies the output and
decides the verdict — is the binary you just built from the WORKING TREE. So the
parent lent a compiler through an environment variable the child, built from
HEAD, had never heard of; the child found no compiler, went red for the old
wrong reason, and the classifier correctly said VACUOUS. Reading the parent's
own source, and even its `--verbose` echo of a dev-tree run, showed a fix that
was working. Committing it — changing nothing else — turned it `PROVEN`.

The skew is silent in both directions, and the other direction mints a `PROVEN`
for an uncommitted gate body that is not in the repository. `mutations::head_skew`
now refuses to start when the working tree differs from HEAD in anything the
probe measures (`rust`, `runtime-go`, `sky-stdlib`, `examples`, `docs/bluedb`),
excluding only what the runner itself reads or writes in the dev tree by design:
`gate-state.tsv`, `*.expected.txt`, and `mutations/*.patch`. An unrunnable `git
status` counts as skew — unknown provenance is not evidence of freshness.
