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

## CLOSED — the per-leaf rule keys on the leaf's OWN assertion (2026-08-14)

`every_pinned_leaf_is_reddened_by_a_recorded_mutation` used to ask one thing:
does a committed RED transcript contain `--- FAIL: <leaf>`? That is a fact about
an artefact, but it is the wrong fact. A Go parent fails when any descendant
does, a mutation can redden a fixture through a shared helper, and — the case
that shipped — a mutation can be CLASSIFIED on an assertion living in a
different leaf of the same gate. `--- FAIL:` then appears beside a leaf whose
body has nothing to do with the proof, and the rule is satisfied by
RELABELLING.

The measured consequence, constructed end to end on this branch: revert C6b's
fail-closed decode in `committer.go` and empty
`TestAuditC6bBlindPathRingAppendCannotBeHoledEither` to `{}`. That is a real SSI
under-rejection — an all-blind drain durably acks a row whose changelog payload
does not decode, the ring never learns of it, and a concurrent txn validates
against a window missing a committed change — and `go test ./bluedb/...` was
`ok`, `cargo test -p xtask` green, `--tier=full` all-PASS including G2.13h "the
commit/validation route fails closed", and `--verify-mutations` PASS with all
four G2.13h mutations PROVEN.

A falsifier must now be keyed on the leaf's own text, one of three ways:

1. **A mutation whose `expect` is verbatim inside the LEAF's comment-stripped
   body** — resolved by `gates_g2::leaf_body`, which descends into the `t.Run`
   closure rather than stopping at the enclosing function, because sibling arms
   share a function. Gutting the leaf then deletes the string the runner
   classifies on, so the proof decays to `VACUOUS` at the next
   `--verify-mutations` instead of standing forever.
2. **A `SourceAnchor` whose needle is inside the LEAF's body**, required unique
   in the enclosing function (`every_per_leaf_anchor_is_unique_in_its_function`)
   — otherwise a sibling arm satisfies it and the pin survives the gutting it
   exists to catch. This is the stronger form: it is checked on every gate run
   from the tree as it stands.
3. **`SOURCE_SIDE_FALSIFIERS`**, unchanged: a pin the gate enforces by another
   spelling, each row carrying its argument.

Three leaves were guttable with their gate PASS and their proof PROVEN, and each
now goes RED naming the fixture and its assertion:
`TestAuditN4CloseWithLeakedReaderReportsRatherThanHangs` (G2.13f, which carried
`anchors: &[]`), `TestAuditC6bBlindPathRingAppendCannotBeHoledEither` (G2.13h),
and `…GcAborts…/past-the-per-pass-bound-the-pass-aborts-and-deletes-nothing`
(G2.13i). The protection that did exist was accidental: G2.13c's leaf happens to
contain its mutation's `expect`.

`<never>` is no longer a general escape hatch either. It exempts a mutation from
the discriminating-assertion check, from G0.6's recorded-output check and from
pairwise discrimination, and nothing asserted it belonged to the canary.
`the_never_sentinel_is_the_canary_s_alone` asserts it statically and
`mutations.rs` refuses it at run time.

## CLOSED — N3's latch consumption is gated 7-for-7, not 1-for-6 (2026-08-14)

`quietLogger.Fatalf` LATCHES instead of panicking; the whole value of that is the
CONSUMPTION, at every exit that would otherwise report success. Each consumption
point is an independently deletable hunk, and deleting each in turn and running
the whole suite left **five of six green** — including `pebble_engine.go`'s
Commit door, which the source itself calls decisive ("without this check the fix
trades a process kill for a silent, permanent hang of every writer"). The one
that was covered, `committer.go`'s blind-path fold, was covered by G2.9a, whose
subject is durability-on-ack rather than the latch;
`G2.6/disable-injection-point` mutates the test's INJECTOR, not the fix.

**G2.13l** is the gate for that property.
`TestAuditN3LatchIsConsumedAtEveryExitThatCouldClaimSuccess` has one arm per
consumption point, each anchored on its own assertion, and
`N3_CONSUMPTION_POINTS` records the population — reconciled against the engine
sources, so a NEW latch read is a FAIL until it is recorded with a falsifier.

The arms latch DIRECTLY (`e.fatal.record`) rather than through pebble, and that
is the decomposition rather than a shortcut: "a real Fatalf reaches the latch" is
`TestAuditN3BackgroundFatalDoesNotKillTheProcess`'s property, proven with a
counted errorfs MANIFEST fault. Reaching the CONSUMERS through a real fault is
impossible for most of them, because pebble does not degrade after a MANIFEST
fatal — it WEDGES, parking every writer inside `Apply`, so the exits behind it
are never reached at all.

Five mutations, and two points deliberately without one:

* `committer.go`'s blind-path fold — its honest revert already exists as
  `G2.9a/wal-fatal-never-reaches-the-ack`. Two mutations of one hunk are one
  proof counted twice. Its per-leaf falsifier is the anchor on its arm.
* **`pebble_engine.go`'s post-Open check — recorded UNREDDENABLE, with the
  measurement.** To redden it a fault would have to make pebble `Fatalf` during
  `Open` while `Open` still returned nil. Injecting write / sync / sync-data
  faults on `MANIFEST-*` through `errorfs` during a fresh `Open` was tried in all
  three shapes: `pebble.Open` returns the injected error itself every time
  (`err = injected error`, `ErrPebbleFatal` absent), so `openWith` fails at the
  line ABOVE the check and deleting it changes nothing observable. On a REOPEN
  the same fault wedges pebble entirely and `Open` never returns, so there is no
  verdict to observe. The check is defence-in-depth against a pebble contract
  that today does not need it.
