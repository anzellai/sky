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

## CLOSED — the 38 tests we INHERITED are gated (2026-08-14)

The harness gated the two Go test files P1 **wrote** — `audit_test.go` (23
fixtures, G2.13a–m) and `crashsim_test.go` (7, G2.9a) — and none of the eight it
**inherited**. 38 of the package's 68 `func Test` names appeared nowhere under
`rust/crates/xtask/src/`: all 10 of `engine_test.go` (every group-commit test,
snapshot isolation, the HLC restart floor, the changelog), all 11 of
`gc_test.go` (watermark + GC + the Fix-3 clamp), the 9 comparer/key-format
behaviour tests, both lock tests, the spill test, and `stage2_readset_test.go`.
Under RULE ZERO that made group commit, GC, the watermark, the changelog and the
key format **unproven**, however green `go test ./bluedb/` was: no gate ran them,
no pin named them, no mutation targeted them, and nothing in the harness could
tell a corpus that asserts them from one that had been deleted.

**The cost was measured.** An adversarial Judge deleted four lines —
`tx.WitnessCollection(coll)` from `ScanCollection` (`txn.go:604-610`) and the
`if !rs.collWitness[coll]` assertion that is its only witness
(`stage2_readset_test.go:93-95`) — and a scan-then-insert transaction that
conflicts at HEAD **committed clean**. A phantom, with no serial order to explain
it, while `go test`, `cargo test -p xtask`, `--tier=full` and
`--verify-mutations` (20 mutations, 11 gates, all PROVEN) stayed green.
`ScanCollection` recording `collWitness` is the ONLY live producer of the arm
Stage 2's serializability claim rests on. A documented scope reduction had become
an unguarded one.

**G2.14–G2.25** (`bluedb_gates/gates_runtime.rs`) are that corpus's gates — one
per PROPERTY, not one per file and not one per test:

| Gate | Property | Leaves |
|---|---|---|
| G2.14 | the excised read-set arms have no producer; the live ones record | 1 |
| G2.15 | every job in a drained batch gets its own commitTs | 3 |
| G2.16 | a read resolves the newest version at or below its readTs | 5 |
| G2.17 | the commit clock never re-issues a timestamp across a restart | 2 |
| G2.18 | the changelog round-trips verbatim and tails strictly after | 1 |
| G2.19 | GC never collects a version a live reader can still need | 4 |
| G2.20 | the GC threshold is durable, monotone, never above durableHi | 5 |
| G2.21 | a GC pass trims below T and writes no other logical state | 2 |
| G2.22 | the key encoding satisfies Pebble's comparer contract | 4 |
| G2.23 | the key-shortening hooks stay inside the contract | 4 |
| G2.24 | every key parser rejects corrupt bytes without panicking | 4 |
| G2.25 | a store admits one writer and one immutable format name | 3 |

Each carries the four pins the G2.13* family carries: a population parsed from
the Go source and reconciled **two-way** against `RUNTIME_OWNERSHIP` (and against
the FILE each row names, so a fixture cannot move house silently); `-count=1`;
`-json` parsed for a passing event per leaf under an anchored `^(…)$` pattern;
and a `SourceAnchor` on **every** leaf's own property assertion, because an empty
Go test emits `pass` and the first three prove only that a leaf RAN.

Re-running the Judge's four-line attack now gives:

```
G2.14   FAIL   …::TestStage2ReadSetRangesHaveNoProducer no longer contains
        `if !rs.collWitness[coll] {` in EXECUTING code — THE non-vacuity
        assertion. ScanCollection's WitnessCollection call is the only live
        producer of the arm Stage 2's serializability claim rests on …
```

Twelve mutations, one per gate, each a minimal revert of a named fix hunk
(Fix-2's per-job `hlc.next()`, Fix-3's `minHLC(candidate, dur)`, §5.2's
min-over-live floor, §3.3's restart seed, H3's tombstone arm, the F2/bounds
guards, the format name). `G2.15`'s `expect` was authored against the
changelog-loss message and **re-taken** against `per-job commitTs not strictly
increasing`, because the fixture trips that assertion first and the original
would have recorded VACUOUS.

Two leaves are recorded in `SOURCE_SIDE_FALSIFIERS` as unreddenable by any honest
revert, with the argument: `TestSecondOpenFailsSingleProcessLock` (the exclusive
directory lock is Pebble's, relied on by design §6 rather than reimplemented) and
`TestWrongComparerNameRefusesOpen` (dropping `Comparer: skydbComparer` from
`openWith` does NOT redden it — the store is then created under Pebble's default
name and the fixture's deliberately-wrong name still mismatches). Their live
sibling `TestComparerName` IS reddenable, and `G2.25/comparer-name-drifts` proves
it.

A new `*_test.go` under `runtime-go/bluedb/` owned by no family is now a
`cargo test` failure: the three families must partition the directory, discovered
by `read_dir` rather than read from a list.

### Round 4 — `validate()` itself (G2.26 / G2.27), and the class (G0.8)

G2.14 gates the fact that a transaction **records** its dependencies. A fourth
Judge round measured what that leaves open: deleting the four lines of
`validate()`'s collection-witness arm (`validate.go:48-51`) let a
scan-then-insert transaction commit clean — the same phantom, one file over from
round 3's fix — with `go test`, `cargo test -p xtask`, `--tier=full` and
`--verify-mutations` all green. Gutting `validate()` entirely was caught by
**one** assertion in the whole corpus, and only on the point arm.

`runtime-go/bluedb/validate_test.go` asserts the CONSEQUENCE — the conflicting
transaction is REFUSED — arm by arm, each fixture with a control (a concurrent
change that must NOT conflict) so that `return false` fails one arm and
`return true` fails the other, and each isolating its arm (the point fixture
asserts it carries no witnesses; the phantom fixture asserts the inserted key is
not a point read):

| Gate | Property | Leaves |
|---|---|---|
| G2.26 | a point read superseded after its readTs is refused, and only then | 1 |
| G2.27 | a phantom insert into a witnessed collection is refused, and the collection id it matches on survives the wire | 2 |

**G0.8 is the class.** Round 3 hardened the producer of `collWitness`; round 4
deleted the consumer. Each fix was local to the site attacked, and the
mechanically answerable question nobody asked was: *which engine sources are
touched by NO recorded mutation?* Across the 51 patches then in
`docs/bluedb/mutations/`, **eight of the seventeen** non-test sources were —
`changefeed.go`, `engine.go`, `hlc.go`, `hotkey.go`, `keychange.go`,
`readset.go`, `recent_changes.go`, `validate.go`. Two of them are named verbatim
in P1's scope row.

G0.8 asks it on every run. The population is `read_dir`; coverage is read from
the patches' own `diff --git` paths, never from `Mutation.targets` (a
declaration deliberately broader than the diff). A source may instead sit in
`DELIBERATELY_UNMUTATED` with an argument **and** a `funcs` pin reconciled both
ways, so an exemption written once cannot silently cover behaviour written
later. Five sources are there today: `engine.go` (declares no function at all),
`readset.go` (its one function is structurally unreachable in Stage 2, and
`txn.go`'s excision note *requires* that mutating it change no gate),
`hotkey.go` and `changefeed.go` (liveness-only / post-Apply, referenced by no
test, so a mutation records VACUOUS) and `hlc.go` (every honest revert trips an
assertion `G2.15`'s and `G2.17`'s mutations already own).

G0.8's own falsifier is worth reading before writing another: it re-points
`recent_changes.go`'s only mutation at a different diff while leaving
`Mutation.targets` still naming the file, so a gate that trusted the declaration
stays green. The obvious alternative — a patch that ADDS an unmutated source —
was tried first and observed GREEN, because the patch that creates the file
names the file.

**And one harness bug the exercise surfaced.** `--- FAIL: <name>` is the only
line that says WHICH leaf failed, and Go emits it after that test's output — so
the 40-line head-truncated quote dropped exactly it for any table-driven fixture
with many sub-failures. `G2.24`'s transcript came back as thousands of
`panicked on key …` lines and no verdict line, and the per-leaf rule could not
confirm a leaf the run had plainly reddened. The budget now bounds the NOISE and
nothing bounds the VERDICT.

**G2.13d was retitled.** It said "a commit against a closed engine"; the fixture
closes the commit CHANNEL and asserts the engine is neither closed nor sealed
(`audit_test.go:271-281`) — that is what makes the send meet a closed channel at
all. A reader checking STATUS.md against the corpus would have found no fixture
for the old title.
