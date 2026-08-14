//! G2.14–G2.25 — **the engine corpus we INHERITED**, brought under the gates.
//!
//! # The gap this file closes
//!
//! `gates_g2_13.rs` gates `audit_test.go` and `gates_g2.rs` gates
//! `crashsim_test.go`: the two files P1 *wrote*. Everything P1 *inherited* —
//! `engine_test.go`, `gc_test.go`, `comparer_test.go`,
//! `comparer_property_test.go`, `keys_test.go`, `lock_test.go`,
//! `bench_test.go` and `stage2_readset_test.go` — was run by NO gate. 38 of the
//! package's 68 `func Test…` names appeared nowhere in `rust/crates/xtask/src/`.
//! Under RULE ZERO that made group commit, GC, the watermark, the changelog and
//! the key format **unproven**, however green `go test ./bluedb/` was, because
//! nothing in the harness could tell a corpus that asserts them from a corpus
//! that had been deleted.
//!
//! The concrete cost was measured, not theorised. An adversarial Judge deleted
//! FOUR lines —
//!
//! ```text
//! txn.go:604-610              drop `tx.WitnessCollection(coll)` from ScanCollection
//! stage2_readset_test.go:93-95  drop the `if !rs.collWitness[coll]` assertion
//! ```
//!
//! — and a scan-then-insert transaction that conflicts at HEAD committed clean:
//! a phantom, with no serial order explaining it. `go test`, `cargo test -p
//! xtask`, `--tier=full` and `--verify-mutations` all stayed green, because
//! `ScanCollection`'s `collWitness` write is the ONLY live producer of the arm
//! Stage 2's serializability claim rests on and its one assertion lived in a
//! fixture no gate ran, no pin named and no mutation targeted. **G2.14** is that
//! assertion's gate; the anchors on [`G2_14_ANCHORS`] are what make the second
//! of those two deletions a red gate rather than a silent scope reduction.
//!
//! # The shape, which is `gates_g2_13.rs`'s
//!
//! Nothing here is a new mechanism. Each gate carries the same four pins:
//!
//! 1. **The population is a SET parsed from the Go source and reconciled
//!    TWO-WAY** against [`RUNTIME_OWNERSHIP`] — a deleted or renamed test is a
//!    FAIL, and so is an ADDED one until it is recorded. The reconciliation
//!    spans the WHOLE family, not one file per gate: a test that moves between
//!    these files must be re-recorded, and a new file's worth of tests cannot
//!    arrive unowned. (`audit_test.go`'s table reconciles one file because one
//!    file is all it owns.)
//! 2. **`-count=1`**, so Go cannot serve `ok (cached)` having run nothing.
//! 3. **`-json` parsed for a passing event per pinned leaf**, with the passing
//!    set required to EQUAL the pinned set under an anchored `^(…)$` pattern —
//!    `go test -run 'TestNoSuchThing'` exits 0, so exit status is not evidence.
//! 4. **A [`SourceAnchor`] on every pinned leaf's own property assertion.** An
//!    empty Go test function emits `pass`; (1)–(3) prove a leaf RAN, never that
//!    its body asserts anything. The anchor is checked against comment-stripped
//!    EXECUTING text on every run, so gutting a fixture — or deleting the
//!    assertion out of it, which is what the Judge did — turns the gate red by
//!    name. This is the second of the two per-leaf falsifier kinds
//!    `gates_g2_13.rs` documents, and the one it calls strictly stronger.
//!
//! # One gate per PROPERTY
//!
//! Twelve gates rather than one gate with twelve mutations, for the reason
//! `gates_g2_13.rs` sets out: `mutations.rs` classifies with
//! `red.exit_ok || !red.output.contains(m.expect)`, which asks only whether
//! THIS mutation's assertion fired. Hanging every property off one gate would
//! let one defect mint a dozen `PROVEN`s out of one undifferentiated failure.
//! Equally, this is not one gate per test: `STATUS.md` would carry 38 rows that
//! say nothing a reader could use, and a gate is a statement about a property,
//! not about a file's table of contents.
//!
//! The grouping is by what breaks together:
//!
//! | Gate | Property | Leaves |
//! |------|----------|--------|
//! | G2.14 | the excised read-set arms have no producer; the live ones record | 1 |
//! | G2.26 | validate() REFUSES a superseded point read, and only then | 1 |
//! | G2.27 | validate() REFUSES a phantom into a witnessed collection | 2 |
//! | G2.15 | every job in a drained batch gets its own commitTs | 3 |
//! | G2.16 | a read resolves the newest version at or below its readTs | 5 |
//! | G2.17 | the commit clock never re-issues a timestamp across a restart | 2 |
//! | G2.18 | the changelog round-trips verbatim and tails by commitTs | 1 |
//! | G2.19 | GC never collects a version a live reader can still need | 4 |
//! | G2.20 | the GC threshold is durable, monotone, never above durableHi | 5 |
//! | G2.21 | a GC pass is physical: it trims below T and writes nothing else | 2 |
//! | G2.22 | the on-disk key encoding satisfies Pebble's comparer contract | 4 |
//! | G2.23 | the key-shortening hooks stay inside the contract | 4 |
//! | G2.24 | every key parser rejects corrupt bytes without panicking | 4 |
//! | G2.25 | a store admits one writer and one immutable format name | 3 |
//!
//! # The mutations
//!
//! One per gate, each the minimal revert of a named fix hunk (Fix-2's per-job
//! `hlc.next()`, Fix-3's `minHLC(candidate, dur)` clamp, §5.2's min-over-live
//! floor, §3.3's restart seed, H3's tombstone arm, the F2/bounds guards, the
//! format name), and each `expect` copied VERBATIM from the failure that revert
//! actually produced. Which pinned leaves each one reddened is recorded in
//! [`super::gates_g2_13::LEAF_COVERAGE`] against the transcript
//! `--verify-mutations` wrote, never against a prediction.
//!
//! Three leaves are reachable by no honest revert and say so, with the argument,
//! in [`super::gates_g2_13::SOURCE_SIDE_FALSIFIERS`]; their falsifier is the
//! anchor, which is checked on every run.

use std::collections::BTreeSet;
use std::time::Duration;

use super::gates_g2::{
    check_pinned_population, check_run_evidence, check_source_anchors, enumerate_injections,
    go_test, go_test_names, SourceAnchor, T_RUN,
};
use super::registry::{Ctx, GateOutcome};

/// Every Go test source this family owns, and therefore every source in
/// `runtime-go/bluedb/` that `gates_g2.rs` and `gates_g2_13.rs` do not.
///
/// `runtime_sources_plus_the_two_older_families_are_the_whole_package` asserts
/// the union is the directory, so a NEW `*_test.go` file cannot arrive owned by
/// nobody — the exact hole this module exists to close, one level up.
pub const RUNTIME_SOURCES: &[&str] = &[
    "runtime-go/bluedb/bench_test.go",
    "runtime-go/bluedb/comparer_property_test.go",
    "runtime-go/bluedb/comparer_test.go",
    "runtime-go/bluedb/engine_test.go",
    "runtime-go/bluedb/gc_test.go",
    "runtime-go/bluedb/keys_test.go",
    "runtime-go/bluedb/lock_test.go",
    "runtime-go/bluedb/stage2_readset_test.go",
    "runtime-go/bluedb/validate_test.go",
];

/// Quoted into findings so a failure says which pin to update.
const PIN_NAME: &str = "RUNTIME_OWNERSHIP (bluedb_gates/gates_runtime.rs)";

/// One `func Test…` in one of [`RUNTIME_SOURCES`], and the gate that runs it.
pub struct RuntimeOwned {
    /// The file it is declared in. Recorded as well as the name so a test that
    /// MOVES between these files is a finding rather than a silent re-home.
    pub file: &'static str,
    pub test: &'static str,
    pub owner: &'static str,
    /// The property it pins, rendered into the owning gate's PASS detail.
    pub property: &'static str,
}

pub const RUNTIME_OWNERSHIP: &[RuntimeOwned] = &[
    // -- G2.14 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/stage2_readset_test.go",
        test: "TestStage2ReadSetRangesHaveNoProducer",
        owner: "G2.14",
        property: "the excised range/index arms have no producer and the live arms do record",
    },
    // -- G2.26 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/validate_test.go",
        test: "TestValidateDetectsAPointReadOverwrittenConcurrently",
        owner: "G2.26",
        property: "a point read superseded after its readTs is REFUSED, and an untouched one is not",
    },
    // -- G2.27 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/validate_test.go",
        test: "TestValidateDetectsAPhantomInsertIntoAWitnessedCollection",
        owner: "G2.27",
        property: "a phantom insert into a witnessed collection is REFUSED, and a change elsewhere is not",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/validate_test.go",
        test: "TestChangelogPayloadCarriesTheCollectionIdTheWitnessMatchesOn",
        owner: "G2.27",
        property: "the collection id the witness matches on survives the changelog wire format",
    },
    // -- G2.15 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/engine_test.go",
        test: "TestGroupCommitPerJobDistinctChangelog",
        owner: "G2.15",
        property: "distinct changelog payloads in one batch survive at distinct commitTs",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/engine_test.go",
        test: "TestGroupCommitPerJobSameKeyDistinctVersions",
        owner: "G2.15",
        property: "same-key writes in one batch become distinct MVCC versions",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/engine_test.go",
        test: "TestGroupCommitBasic",
        owner: "G2.15",
        property: "200 concurrent commits form one strictly-increasing distinct-ts order",
    },
    // -- G2.16 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/engine_test.go",
        test: "TestVersionedRoundTrip",
        owner: "G2.16",
        property: "each version resolves at its own readTs and none below the first",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/engine_test.go",
        test: "TestSnapshotIsolation",
        owner: "G2.16",
        property: "a frozen reader is unmoved by later commits, incl. the C1 equal-length boundary",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/engine_test.go",
        test: "TestTombstone",
        owner: "G2.16",
        property: "a tombstone resolves as absent at/after its ts and not before",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/engine_test.go",
        test: "TestIterateOrdered",
        owner: "G2.16",
        property: "the scan is ordered, newest-per-key, tombstone-skipping and prefix-bounded",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/bench_test.go",
        test: "TestSpillToDiskNoRAMCeiling",
        owner: "G2.16",
        property: "data spilled past the memtable reads back identically (no RAM ceiling)",
    },
    // -- G2.17 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/engine_test.go",
        test: "TestHLCMonotonicRestartFloor",
        owner: "G2.17",
        property: "a reopen under a rewound wall clock still issues a strictly greater ts",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/engine_test.go",
        test: "TestMetadataInBatch",
        owner: "G2.17",
        property: "hlc_hi is recovered from the batch it was committed in, and a batch without it is refused",
    },
    // -- G2.18 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/engine_test.go",
        test: "TestChangelogWrite",
        owner: "G2.18",
        property: "payloads come back verbatim, ascending, and Tail(after) is strictly after",
    },
    // -- G2.19 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/gc_test.go",
        test: "TestGCDropsStaleVersionsBelowT",
        owner: "G2.19",
        property: "shadowed versions strictly below the newest-below-T are dropped",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/gc_test.go",
        test: "TestGCKeepsNewestBelowFloorAndSoleVersion",
        owner: "G2.19",
        property: "a key's sole or newest-below-floor version is never dropped",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/gc_test.go",
        test: "TestGC2aReaderProtected",
        owner: "G2.19",
        property: "a live reader pins the floor (grill 2a TOCTOU) and is released only by Close",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/gc_test.go",
        test: "TestGCConcurrentWithCommitter",
        owner: "G2.19",
        property: "GC interleaved with a commit firehose loses no newest version",
    },
    // -- G2.20 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/gc_test.go",
        test: "TestGCPersistsThresholdMonotone",
        owner: "G2.20",
        property: "T survives a reopen and never regresses",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/gc_test.go",
        test: "TestAdvanceThresholdClampsToDurableHi",
        owner: "G2.20",
        property: "advanceThreshold clamps the candidate to durableHi (Fix-3 b)",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/gc_test.go",
        test: "TestGCThresholdNeverExceedsDurableHi",
        owner: "G2.20",
        property: "no GC pass leaves T above durableHi (Fix-3 a)",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/gc_test.go",
        test: "TestGCThresholdClampSurvivesCrashNoReaderWedge",
        owner: "G2.20",
        property: "a crash in the assigned-but-not-applied window leaves no wedged reader (Fix-3 c)",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/gc_test.go",
        test: "TestGCSnapshotTooOld",
        owner: "G2.20",
        property: "a readTs below T is refused at Register and at Advance",
    },
    // -- G2.21 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/gc_test.go",
        test: "TestGC2bPhysicalOnly",
        owner: "G2.21",
        property: "a GC pass bumps no hlc_hi and appends no changelog entry (grill 2b)",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/gc_test.go",
        test: "TestGCChangelogRetentionTrimsBelowT",
        owner: "G2.21",
        property: "retention range-deletes strictly below T and leaves T itself",
    },
    // -- G2.22 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/comparer_test.go",
        test: "TestCheckComparer",
        owner: "G2.22",
        property: "Pebble's own base.CheckComparer passes over an adversarial key set",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/comparer_test.go",
        test: "TestSplitTagIndependent",
        owner: "G2.22",
        property: "Split reads the trailing length byte arithmetically and is tag-independent",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/comparer_test.go",
        test: "TestVersionOrderingNewestFirst",
        owner: "G2.22",
        property: "the inverted suffix sorts a larger commitTs first",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/comparer_test.go",
        test: "TestPrefixBoundaryDistinctKeys",
        owner: "G2.22",
        property: "equal-length distinct user-keys have distinct prefix BYTES (C1)",
    },
    // -- G2.23 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/comparer_property_test.go",
        test: "TestSeparatorProperties",
        owner: "G2.23",
        property: "Separator lands in [a,b), shortens only to a bare prefix, preserves dst",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/comparer_property_test.go",
        test: "TestSuccessorProperties",
        owner: "G2.23",
        property: "Successor really shortens whenever a shorter greater key exists",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/comparer_property_test.go",
        test: "TestImmediateSuccessorProperties",
        owner: "G2.23",
        property: "ImmediateSuccessor clears every version of its prefix and nothing more",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/comparer_property_test.go",
        test: "TestAbbreviatedKeyMonotonicity",
        owner: "G2.23",
        property: "the uint64 fast path never disagrees with Compare, and is prefix-only",
    },
    // -- G2.24 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/keys_test.go",
        test: "TestDecodeDataVersionRejectsCorruptKeysWithoutPanic",
        owner: "G2.24",
        property: "decodeDataVersion survives every truncation/mutation of the corpus",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/keys_test.go",
        test: "TestChangelogTsOfRejectsCorruptKeysWithoutPanic",
        owner: "G2.24",
        property: "changelogTsOf survives the same corpus and never accepts a non-round-tripper",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/keys_test.go",
        test: "TestDecodersAcceptWellFormedKeys",
        owner: "G2.24",
        property: "the guards did not close: well-formed keys still decode exactly",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/keys_test.go",
        test: "TestSplitNeverPanicsOnCorruptKeys",
        owner: "G2.24",
        property: "skydbSplit — the parser Pebble calls on every key — stays in range (F2)",
    },
    // -- G2.25 ------------------------------------------------------------
    RuntimeOwned {
        file: "runtime-go/bluedb/lock_test.go",
        test: "TestSecondOpenFailsSingleProcessLock",
        owner: "G2.25",
        property: "a second Open of the same directory is refused (single-writer dir lock)",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/lock_test.go",
        test: "TestWrongComparerNameRefusesOpen",
        owner: "G2.25",
        property: "a store refuses to open under any other Comparer.Name",
    },
    RuntimeOwned {
        file: "runtime-go/bluedb/comparer_test.go",
        test: "TestComparerName",
        owner: "G2.25",
        property: "the format string `skydb.mvcc.v1` is pinned and cannot drift",
    },
];

// ---------------------------------------------------------------------------
// The gate descriptor and its one body
// ---------------------------------------------------------------------------

struct FileGate {
    id: &'static str,
    /// The pinned population, as FULL Go test names. Every one of these files
    /// is `t.Run`-free (asserted per leaf below), so every leaf is a function
    /// and the `-run` pattern stays at depth 1.
    tests: &'static [&'static str],
    /// One per pinned leaf — see the module doc, pin (4).
    anchors: &'static [SourceAnchor],
    /// `go test`'s share of the gate's budget; the rest covers this body's own
    /// parsing and leaves `capped` room to kill the group and reap.
    budget: Duration,
    /// The property, in the PASS detail's voice.
    property: &'static str,
}

/// What [`read_family`] could read, and what it could not.
struct Family {
    declared: BTreeSet<String>,
    bodies: Vec<super::gates_g2::EnumeratedTest>,
    per_file: Vec<(String, BTreeSet<String>)>,
    /// Sources in [`RUNTIME_SOURCES`] that are not on disk. A finding, never a
    /// reason to stop — see [`read_family`].
    unreadable: Vec<String>,
}

/// Read every source in [`RUNTIME_SOURCES`], reporting what was missing rather
/// than refusing to continue.
///
/// This used to return `Err` on the first unreadable source and `run_file_gate`
/// returned on it. That is the same short-circuit
/// [`super::gates_g2::merge_static_and_behavioural`] documents, one level
/// earlier, and it is wrong here for a reason specific to this family: nine
/// files feed fourteen gates, but each gate pins fixtures in only one or two of
/// them. A missing `gc_test.go` says nothing about whether G2.25's comparer
/// fixtures still hold — and `go test` runs them either way, because the package
/// still builds from the files that ARE there. A missing source is a finding
/// about the corpus, merged with everything the fixtures reported.
fn read_family(ctx: &Ctx, gate: &str) -> Family {
    let mut f = Family {
        declared: BTreeSet::new(),
        bodies: Vec::new(),
        per_file: Vec::new(),
        unreadable: Vec::new(),
    };
    for src in RUNTIME_SOURCES {
        let Some(text) = ctx.read(src) else {
            f.unreadable.push(format!(
                "{gate}: cannot read {src} — a pinned corpus that is not on disk has not passed"
            ));
            continue;
        };
        let names = go_test_names(&text);
        f.declared.extend(names.iter().cloned());
        f.per_file.push(((*src).to_string(), names));
        f.bodies.extend(enumerate_injections(&text));
    }
    f
}

/// The one body all twelve share.
fn run_file_gate(ctx: &Ctx, g: &FileGate) -> GateOutcome {
    let Family {
        declared,
        bodies,
        per_file,
        unreadable,
    } = read_family(ctx, g.id);

    // ── (1a) the FAMILY's population is the recorded population, both ways ──
    let recorded: Vec<&str> = RUNTIME_OWNERSHIP.iter().map(|o| o.test).collect();
    let where_ = "runtime-go/bluedb/{bench,comparer,comparer_property,engine,gc,keys,lock,stage2_readset,validate}_test.go";
    let mut findings = unreadable;
    findings.extend(check_pinned_population(&declared, &recorded, where_, PIN_NAME));

    // ── (1a') and each row is recorded against the file that really declares it ──
    for o in RUNTIME_OWNERSHIP {
        let declared_here = per_file
            .iter()
            .find(|(f, _)| f == o.file)
            .is_some_and(|(_, names)| names.contains(o.test));
        if !declared_here {
            findings.push(format!(
                "{PIN_NAME} records {} in {}, which does not declare it — a fixture that moved \
                 between these files must be re-recorded, or the gate that runs it is running a \
                 name it cannot locate",
                o.test, o.file
            ));
        }
    }

    // ── (1b) this gate's own pins name real declarations ──
    for t in g.tests {
        if !declared.contains(*t) {
            findings.push(format!(
                "{} pins {t}, which no file in RUNTIME_SOURCES declares — `go test -run` would \
                 match nothing for it and STILL EXIT 0",
                g.id
            ));
        }
    }

    // ── (1c) the leaf-level pins: the assertion, and the absence of sub-tests ──
    findings.extend(check_source_anchors(&bodies, g.anchors, g.id, where_));
    for t in g.tests {
        match bodies.iter().find(|f| f.test == *t) {
            None => findings.push(format!(
                "{}: no `func {t}` body to count sub-tests in",
                g.id
            )),
            Some(f) => {
                // Zero is load-bearing: these fixtures are flat, so the `-run`
                // pattern is depth 1 and a NEW `t.Run` arm would be neither run
                // by this gate nor accounted for by it. `gates_g2_13.rs` records
                // the same count for its sub-test-free fixtures, for the same
                // reason.
                let sites = f.body.matches(T_RUN).count();
                if sites != 0 {
                    findings.push(format!(
                        "{}::{t} has {sites} `{T_RUN}` site(s); this family pins flat fixtures, so \
                         an arm added here would run under no gate and be counted by none. Either \
                         drop it or give the gate a depth-2 pattern and declare the leaf",
                        g.id
                    ));
                }
            }
        }
    }

    // The static half is COMPLETE here, and it does not return. Fourteen gates
    // share this body and nine source files feed it, so a single unrecorded
    // `func Test…` anywhere in that corpus would otherwise have silenced the
    // behavioural half of all fourteen at once — the shape `614b1517` found on
    // G2.24, which is itself one of the fourteen. See
    // [`super::gates_g2::merge_static_and_behavioural`].
    let static_detail = format!(
        "the inherited engine corpus does not match its pinned population ({} `func Test…` \
         declared across {} file(s), {} recorded in {PIN_NAME})",
        declared.len(),
        RUNTIME_SOURCES.len(),
        recorded.len()
    );

    // ── (2) + (3) run them, cache defeated, per-test evidence required ──
    let behaviour = match go_test(ctx, g.tests, g.budget) {
        Err(e) => GateOutcome::fail(e, vec!["a gate that cannot run has not passed".into()]),
        Ok(run) => {
            let mut behavioural = check_run_evidence(&run, g.tests);
            behavioural.extend(run.failure_log.iter().cloned());
            if behavioural.is_empty() {
                let covered: Vec<&str> = RUNTIME_OWNERSHIP
                    .iter()
                    .filter(|o| o.owner == g.id)
                    .map(|o| o.property)
                    .collect();
                GateOutcome::pass(format!(
                    "{}: {} pinned fixture(s) observed passing via `go test -json -count=1` under \
                     the anchored pattern `{}` [{}]",
                    g.property,
                    g.tests.len(),
                    super::gates_g2::run_pattern(g.tests),
                    covered.join("; ")
                ))
            } else {
                let pinned: BTreeSet<String> = g.tests.iter().map(|s| s.to_string()).collect();
                GateOutcome::fail(
                    format!(
                        "{} is not proven: {}/{} pinned fixture(s) reported a passing event",
                        g.property,
                        run.passed.intersection(&pinned).count(),
                        g.tests.len()
                    ),
                    behavioural,
                )
            }
        }
    };

    super::gates_g2::merge_static_and_behavioural(
        static_detail,
        findings,
        "population reconciliation",
        behaviour,
    )
}

// ---------------------------------------------------------------------------
// G2.14 — the Stage-2 read-set scope, and the arms that ARE live
// ---------------------------------------------------------------------------

pub const G2_14_TESTS: &[&str] = &["TestStage2ReadSetRangesHaveNoProducer"];

/// Three anchors, because the fixture makes three separate claims and the
/// Judge's four-line attack deleted exactly ONE of them.
///
/// The claim `len(rs.ranges) != 0` is the documented scope reduction; the
/// `collWitness` claims are what stop that reduction becoming "the read-set
/// records nothing". Dropping the `collWitness[coll]` assertion (so that
/// dropping `tx.WitnessCollection(coll)` out of `ScanCollection` goes unnoticed)
/// leaves a scan-then-insert transaction with NO recorded dependency, which
/// validate() reads as a clean transaction: a phantom commit with no serial
/// order. That is the deletion the third anchor refuses.
pub const G2_14_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestStage2ReadSetRangesHaveNoProducer",
        needle: "if len(rs.ranges) != 0 {",
        why: "that IS the excision claim — without it the fixture asserts nothing about the \
              range arm validate() cannot reach",
    },
    SourceAnchor {
        func: "TestStage2ReadSetRangesHaveNoProducer",
        needle: "if len(rs.indexWitness) != 0 {",
        why: "the index-witness half of the same claim; ScanFallback was excised and nothing may \
              write the index-level witness",
    },
    SourceAnchor {
        func: "TestStage2ReadSetRangesHaveNoProducer",
        needle: "if !rs.collWitness[coll] {",
        why: "THE non-vacuity assertion. ScanCollection's WitnessCollection call is the only live \
              producer of the arm Stage 2's serializability claim rests on; delete this and the \
              call can be deleted too, and a scan-then-insert txn that conflicts at HEAD commits \
              clean",
    },
];

pub fn g2_14_readset_scope(ctx: &Ctx) -> GateOutcome {
    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.14",
            tests: G2_14_TESTS,
            anchors: G2_14_ANCHORS,
            budget: Duration::from_secs(120),
            property: "the excised Stage-2 read-set arms have no producer and the live arms do record",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.26 / G2.27 — validate() ITSELF: the two arms Stage 2 still claims
// ---------------------------------------------------------------------------
//
// # Why these two gates exist, when G2.14 already gates the read-set
//
// G2.14 certifies that a transaction body RECORDS its dependencies. It says
// nothing about whether anything ENFORCES them, and the distance between those
// two is the whole of SERIALIZABLE. A fourth adversarial Judge measured it: the
// four-line deletion of `validate()`'s collection-witness arm let a
// scan-then-insert transaction commit clean — a phantom with no serial order —
// with `go test ./bluedb/...`, `cargo test -p xtask`, `--tier=full` and
// `--verify-mutations` ALL green, because G2.14's fixture asserts the read-set
// is populated and stops there. Gutting `validate()` entirely was caught by
// exactly ONE assertion in the corpus, and only on the point arm.
//
// P1's scope row and `txn.go`'s excision note put the range and index-witness
// arms structurally out of reach, so the point arm and the collection witness
// are ALL of what P1 still claims about SERIALIZABLE. One gate per arm, for the
// reason the whole family is split that way: a single gate would let one defect
// mint two PROVENs out of one undifferentiated failure.
//
// # What makes each gate's fixture non-vacuous
//
// Each fixture runs its shape TWICE — once where the concurrent change really
// does intersect the read-set, once where it provably does not. A `validate()`
// gutted to `return false` fails the first arm; one gutted to `return true`
// fails the second. And each fixture isolates ITS arm: the point fixture
// asserts its read-set carries no witnesses, the witness fixture asserts the
// phantom key is not a point dependency. That isolation is what makes the two
// registered mutations discriminating — the observed transcripts show each
// reddening its own fixture and leaving the other's PASSING.

pub const G2_26_TESTS: &[&str] = &["TestValidateDetectsAPointReadOverwrittenConcurrently"];

/// Both arms of the one fixture, because a control that is deleted stops being a
/// control silently.
pub const G2_26_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestValidateDetectsAPointReadOverwrittenConcurrently",
        needle: "validate()'s point arm did not detect the conflict",
        why: "THE consequence assertion. Deleting `validate()`'s point arm leaves the read-set \
              fully populated and every other fixture in the package green, while a transaction \
              whose row was superseded after its readTs commits a value derived from a version \
              that no longer exists — the lost update, silently",
    },
    SourceAnchor {
        func: "TestValidateDetectsAPointReadOverwrittenConcurrently",
        needle: "validate() over-rejects, so the conflict arm above proves nothing",
        why: "the control. Without it the fixture is satisfied by a validator that conflicts \
              EVERYTHING, which refuses to commit rather than committing serializably",
    },
];

pub fn g2_26_point_arm_enforces(ctx: &Ctx) -> GateOutcome {
    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.26",
            tests: G2_26_TESTS,
            anchors: G2_26_ANCHORS,
            budget: Duration::from_secs(120),
            property: "validate() REFUSES a transaction whose point read was superseded, and only then",
        },
    )
}

pub const G2_27_TESTS: &[&str] = &[
    "TestValidateDetectsAPhantomInsertIntoAWitnessedCollection",
    "TestChangelogPayloadCarriesTheCollectionIdTheWitnessMatchesOn",
];

/// Three: the phantom consequence, its control, and the wire-format half one
/// layer below — the SSI window is built from the DECODED payload, so a
/// collection id that does not survive the round-trip disables the witness arm
/// globally without `validate.go` being touched at all.
pub const G2_27_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestValidateDetectsAPhantomInsertIntoAWitnessedCollection",
        needle: "validate()'s collection-witness arm is the only thing that detects it",
        why: "THE consequence assertion, and the Judge's four-line deletion stated as a history: \
              the committed summary says 1 row, the store holds 2, and the inserted key was never \
              read, so no other arm of validate() can see it",
    },
    SourceAnchor {
        func: "TestValidateDetectsAPhantomInsertIntoAWitnessedCollection",
        needle: "and a validator that rejects everything would satisfy",
        why: "the control — a change to a collection the transaction never witnessed must NOT \
              conflict, or the phantom arm above is satisfied by an engine that cannot commit",
    },
    SourceAnchor {
        func: "TestChangelogPayloadCarriesTheCollectionIdTheWitnessMatchesOn",
        needle: "The SSI validation window is built by DECODING this payload",
        why: "the wire half of the same property: the window's KeyChanges come from \
              DecodeChangelogPayload, so a dropped collection id is the witness arm deleted at a \
              distance, with every line of validate.go intact",
    },
];

pub fn g2_27_collection_witness_enforces(ctx: &Ctx) -> GateOutcome {
    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.27",
            tests: G2_27_TESTS,
            anchors: G2_27_ANCHORS,
            budget: Duration::from_secs(120),
            property: "validate() REFUSES a phantom insert into a witnessed collection, and the \
                       collection id it matches on survives the wire",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.15 — group commit: one commitTs per JOB, not per batch
// ---------------------------------------------------------------------------

pub const G2_15_TESTS: &[&str] = &[
    "TestGroupCommitPerJobDistinctChangelog",
    "TestGroupCommitPerJobSameKeyDistinctVersions",
    "TestGroupCommitBasic",
];

pub const G2_15_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestGroupCommitPerJobDistinctChangelog",
        needle: "multi-job batch LOST changelog entries: got %d want 3 (per-job commitTs must not collide)",
        why: "Fix-2 (a): one commitTs for the whole batch makes every changelog key identical, so \
              each b.Set overwrites the last and all but one entry is silently lost",
    },
    SourceAnchor {
        func: "TestGroupCommitPerJobSameKeyDistinctVersions",
        needle: "same-key jobs must get distinct increasing commitTs, got %+v then %+v",
        why: "Fix-2 (b): two writes of one key in a batch collapse to one data-version key under a \
              shared commitTs, and last-Set-wins silently drops the first",
    },
    SourceAnchor {
        func: "TestGroupCommitBasic",
        needle: "distinct commitTs not strictly increasing at %d: %+v then %+v",
        why: "the total order group commit exists to preserve: no group may re-issue an earlier ts",
    },
];

pub fn g2_15_group_commit_per_job(ctx: &Ctx) -> GateOutcome {
    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.15",
            tests: G2_15_TESTS,
            anchors: G2_15_ANCHORS,
            budget: Duration::from_secs(180),
            property: "every job in a drained batch commits at its own strictly-increasing commitTs",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.16 — MVCC read resolution, memtable or SSTable
// ---------------------------------------------------------------------------

pub const G2_16_TESTS: &[&str] = &[
    "TestVersionedRoundTrip",
    "TestSnapshotIsolation",
    "TestTombstone",
    "TestIterateOrdered",
    "TestSpillToDiskNoRAMCeiling",
];

pub const G2_16_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestVersionedRoundTrip",
        needle: "Get(K,t0<t1)=%q,%v want absent",
        why: "the lower half of `newest version <= readTs`: a readTs below every version resolves \
              to absent, not to the oldest one",
    },
    SourceAnchor {
        func: "TestSnapshotIsolation",
        needle: "C1 leak: Get(aa)@tab=%q returned present; want absent (aa is newer)",
        why: "the C1 equal-length-prefix boundary: a frozen reader must not resolve a NEIGHBOURING \
              user-key's version as its own",
    },
    SourceAnchor {
        func: "TestTombstone",
        needle: "Get(K,t2 after delete)=%q,%v want absent",
        why: "a delete is a version, and resolving it must yield absent rather than the marker byte",
    },
    SourceAnchor {
        func: "TestIterateOrdered",
        needle: "iterate[%d]=%+v want %+v (full: %v)",
        why: "the ordered-scan contract element by element: ascending, newest-per-key, tombstones \
              skipped, prefix-bounded",
    },
    SourceAnchor {
        func: "TestSpillToDiskNoRAMCeiling",
        needle: "spilled key %s not found (RAM ceiling / lost on flush)",
        why: "§8.1 #5: a value that has left the memtable for an SSTable resolves identically — \
              the assertion that there is no MaxKeys/ErrFull cliff",
    },
];

pub fn g2_16_read_resolution(ctx: &Ctx) -> GateOutcome {
    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.16",
            tests: G2_16_TESTS,
            anchors: G2_16_ANCHORS,
            // The spill fixture writes ~15 MB through a 256 KiB memtable.
            budget: Duration::from_secs(300),
            property: "a read resolves the newest version at or below its readTs, from memtable or SSTable",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.17 — the commit clock across a restart
// ---------------------------------------------------------------------------

pub const G2_17_TESTS: &[&str] = &["TestHLCMonotonicRestartFloor", "TestMetadataInBatch"];

pub const G2_17_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestHLCMonotonicRestartFloor",
        needle: "restart floor violated: persisted hi=%+v, next=%+v (must be strictly greater despite backward clock)",
        why: "§3.3: the clock is floored at the PERSISTED high-water, so a backward wall clock \
              cannot re-issue a commitTs that is already on disk",
    },
    SourceAnchor {
        func: "TestMetadataInBatch",
        needle: "logical batch missing hlc_hi should be refused, got %v",
        why: "§3.4: hlc_hi rides in the commit batch, and a logical batch without it is refused \
              rather than applied — the invariant that makes the recovered floor trustworthy",
    },
];

pub fn g2_17_restart_floor(ctx: &Ctx) -> GateOutcome {
    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.17",
            tests: G2_17_TESTS,
            anchors: G2_17_ANCHORS,
            budget: Duration::from_secs(120),
            property: "the commit clock never re-issues a timestamp across a restart",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.18 — the changelog
// ---------------------------------------------------------------------------

pub const G2_18_TESTS: &[&str] = &["TestChangelogWrite"];

pub const G2_18_ANCHORS: &[SourceAnchor] = &[SourceAnchor {
    func: "TestChangelogWrite",
    needle: "tail(after t0) = %d entries, first=%q; want 2 starting chg-b",
    why: "Tail(after) is the SSI validation window's source: it must be strictly after, or a \
          transaction re-sees a change it already accounted for",
}];

pub fn g2_18_changelog_roundtrip(ctx: &Ctx) -> GateOutcome {
    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.18",
            tests: G2_18_TESTS,
            anchors: G2_18_ANCHORS,
            budget: Duration::from_secs(120),
            property: "the changelog round-trips verbatim, ascending, and tails strictly after a commitTs",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.19 — what GC may collect
// ---------------------------------------------------------------------------

pub const G2_19_TESTS: &[&str] = &[
    "TestGCDropsStaleVersionsBelowT",
    "TestGCKeepsNewestBelowFloorAndSoleVersion",
    "TestGC2aReaderProtected",
    "TestGCConcurrentWithCommitter",
];

pub const G2_19_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestGCDropsStaleVersionsBelowT",
        needle: "stale version K@t1 (strictly older than newest<T) should be physically deleted",
        why: "the non-vacuity half: a GC that collects NOTHING satisfies every safety assertion in \
              this gate, so one fixture must require a real deletion",
    },
    SourceAnchor {
        func: "TestGCKeepsNewestBelowFloorAndSoleVersion",
        needle: "K's sole version (newest < T) must be kept",
        why: "the retention rule a reader AT the floor depends on: the newest version below T is \
              the one that resolves for it",
    },
    SourceAnchor {
        func: "TestGC2aReaderProtected",
        needle: "2a violation: version the live reader needs (K@t1) was GC'd",
        why: "grill 2a: min-over-live pins the floor, so a version a registered reader can still \
              resolve is never collected",
    },
    SourceAnchor {
        func: "TestGCConcurrentWithCommitter",
        needle: "newest version of %s lost after concurrent GC",
        why: "GC's physical deletes are disjoint from the committer's fresh-commitTs writes (C1 \
              amendment) — asserted under a live firehose, where a shared key would show",
    },
];

pub fn g2_19_gc_collects_only_the_dead(ctx: &Ctx) -> GateOutcome {
    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.19",
            tests: G2_19_TESTS,
            anchors: G2_19_ANCHORS,
            budget: Duration::from_secs(240),
            property: "GC never collects a version a live reader can still need",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.20 — the GC threshold
// ---------------------------------------------------------------------------

pub const G2_20_TESTS: &[&str] = &[
    "TestGCPersistsThresholdMonotone",
    "TestAdvanceThresholdClampsToDurableHi",
    "TestGCThresholdNeverExceedsDurableHi",
    "TestGCThresholdClampSurvivesCrashNoReaderWedge",
    "TestGCSnapshotTooOld",
];

pub const G2_20_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestGCPersistsThresholdMonotone",
        needle: "persisted T not recovered: got %+v want %+v",
        why: "T is durable state: a threshold that does not survive a reopen would let a later \
              pass regress it and re-expose collected versions",
    },
    SourceAnchor {
        func: "TestAdvanceThresholdClampsToDurableHi",
        needle: "advanceThreshold must clamp candidate (tNew=%+v) to durableHi (tOld=%+v): got %+v advanced=%v",
        why: "Fix-3 (b) at the unit: the clamp is the single line that keeps persisted T at or \
              below what is durable",
    },
    SourceAnchor {
        func: "TestGCThresholdNeverExceedsDurableHi",
        needle: "GC threshold %+v exceeded durableHi %+v (Fix-3 clamp violated)",
        why: "Fix-3 (a) end to end: the invariant stated over a real pass rather than over the \
              registry in isolation",
    },
    SourceAnchor {
        func: "TestGCThresholdClampSurvivesCrashNoReaderWedge",
        needle: "reader WEDGED after reopen: %v — gc_threshold outran the durable hlc_hi",
        why: "Fix-3 (c): the CONSEQUENCE of an unclamped T — recovered hlc_hi < gc_threshold wedges \
              every reader on ErrSnapshotTooOld — asserted, not merely the numbers",
    },
    SourceAnchor {
        func: "TestGCSnapshotTooOld",
        needle: "Advance below T should be ErrSnapshotTooOld, got %v",
        why: "the other side of the floor: a token may not be advanced BELOW T, or a reader would \
              resolve versions GC has already collected",
    },
];

pub fn g2_20_threshold_is_durable_and_clamped(ctx: &Ctx) -> GateOutcome {
    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.20",
            tests: G2_20_TESTS,
            anchors: G2_20_ANCHORS,
            budget: Duration::from_secs(180),
            property: "the GC threshold is durable, monotone and never above the durable high-water",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.21 — what a GC pass writes
// ---------------------------------------------------------------------------

pub const G2_21_TESTS: &[&str] = &["TestGC2bPhysicalOnly", "TestGCChangelogRetentionTrimsBelowT"];

pub const G2_21_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestGC2bPhysicalOnly",
        needle: "GC bumped hlc_hi: before=%+v after=%+v",
        why: "grill 2b: GC is not a writer of logical state. A GC-advanced hlc_hi would be a \
              commitTs nobody issued, recovered as the high-water on the next open",
    },
    SourceAnchor {
        func: "TestGCChangelogRetentionTrimsBelowT",
        needle: "expected retention trim at T=%+v, got trimmed=%v T=%+v",
        why: "the one thing a pass DOES write to the changelog: a range-delete strictly below T. \
              Without this assertion the gate is satisfied by a GC that trims nothing",
    },
];

pub fn g2_21_gc_pass_is_physical(ctx: &Ctx) -> GateOutcome {
    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.21",
            tests: G2_21_TESTS,
            anchors: G2_21_ANCHORS,
            budget: Duration::from_secs(180),
            property: "a GC pass trims the changelog below T and writes no other logical state",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.22 — the comparer contract
// ---------------------------------------------------------------------------

pub const G2_22_TESTS: &[&str] = &[
    "TestCheckComparer",
    "TestSplitTagIndependent",
    "TestVersionOrderingNewestFirst",
    "TestPrefixBoundaryDistinctKeys",
];

pub const G2_22_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestCheckComparer",
        needle: "base.CheckComparer failed on skydb.mvcc.v1: %v",
        why: "§8.1: Pebble's own mechanical check over an adversarial key set. A failure here means \
              the format is unsound BEFORE the first SSTable is written",
    },
    SourceAnchor {
        func: "TestSplitTagIndependent",
        needle: "Split(corrupt-lenbyte)=%d, want %d (F2 guard)",
        why: "the F2 guard inside the comparer's own hook: an oversized trailing length byte must \
              not negative-index on a key Pebble hands us off disk",
    },
    SourceAnchor {
        func: "TestVersionOrderingNewestFirst",
        needle: "expected newer < older under Compare (newest first); got %d",
        why: "the inverted 12-byte suffix IS the MVCC read path: newest-first ordering is what \
              makes a single SeekGE resolve `newest version <= readTs`",
    },
    SourceAnchor {
        func: "TestPrefixBoundaryDistinctKeys",
        needle: "distinct user-keys must have distinct prefix BYTES (C1)",
        why: "grill C1: equal Split integers for equal-length keys are fine; equal prefix BYTES \
              would let one user-key's versions be read as another's",
    },
];

pub fn g2_22_comparer_contract(ctx: &Ctx) -> GateOutcome {
    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.22",
            tests: G2_22_TESTS,
            anchors: G2_22_ANCHORS,
            budget: Duration::from_secs(120),
            property: "the on-disk key encoding satisfies Pebble's comparer contract",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.23 — the key-shortening hooks
// ---------------------------------------------------------------------------

pub const G2_23_TESTS: &[&str] = &[
    "TestSeparatorProperties",
    "TestSuccessorProperties",
    "TestImmediateSuccessorProperties",
    "TestAbbreviatedKeyMonotonicity",
];

pub const G2_23_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestSeparatorProperties",
        needle: "Separator(%x, %x)=%x is shortened but not a bare prefix (Split=%d, len=%d)",
        why: "a separator truncated INSIDE the 13-byte suffix bakes a bogus trailing length byte \
              into an SSTable index block — an irreversible-format defect",
    },
    SourceAnchor {
        func: "TestSuccessorProperties",
        needle: "returned its input unchanged; a strictly greater shortened key exists (key-part %x)",
        why: "the clause that makes this fixture bite: without it a Successor that returns its \
              input satisfies every remaining assertion vacuously",
    },
    SourceAnchor {
        func: "TestImmediateSuccessorProperties",
        needle: "is not immediate: key %x with a greater prefix sorts below it",
        why: "the jump-seek target must skip the whole version chain and land no further — a \
              non-immediate successor silently skips a neighbouring prefix's rows",
    },
    SourceAnchor {
        func: "TestAbbreviatedKeyMonotonicity",
        needle: "share a prefix but abbreviate differently (%d vs %d)",
        why: "the prefix-only requirement: abbreviating over the whole key would make two versions \
              of one user-key disagree with Compare on the uint64 fast path",
    },
];

pub fn g2_23_shortening_hooks(ctx: &Ctx) -> GateOutcome {
    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.23",
            tests: G2_23_TESTS,
            anchors: G2_23_ANCHORS,
            budget: Duration::from_secs(120),
            property: "the comparer's key-shortening hooks stay inside Pebble's contract",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.24 — the key parsers
// ---------------------------------------------------------------------------

pub const G2_24_TESTS: &[&str] = &[
    "TestDecodeDataVersionRejectsCorruptKeysWithoutPanic",
    "TestChangelogTsOfRejectsCorruptKeysWithoutPanic",
    "TestDecodersAcceptWellFormedKeys",
    "TestSplitNeverPanicsOnCorruptKeys",
];

pub const G2_24_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestDecodeDataVersionRejectsCorruptKeysWithoutPanic",
        needle: "decodeDataVersion accepted malformed key %x",
        why: "the guard must reject, not merely survive: a parser that answers ok=true for a \
              truncated key hands validate() a version that never existed",
    },
    SourceAnchor {
        func: "TestChangelogTsOfRejectsCorruptKeysWithoutPanic",
        needle: "changelogTsOf accepted %x but it does not round-trip",
        why: "same contract on the changelog parser, whose output keys the SSI validation window",
    },
    SourceAnchor {
        func: "TestDecodersAcceptWellFormedKeys",
        needle: "decodeDataVersion rejected the well-formed key %x",
        why: "the other half of the guard contract: a guard that answered ok=false for everything \
              would pass both no-panic fixtures and silently break every reader",
    },
    SourceAnchor {
        func: "TestSplitNeverPanicsOnCorruptKeys",
        needle: "skydbSplit(%x)=%d out of range [0,%d]",
        why: "F2 over the same corpus: Pebble calls Split on every key it reads, so an out-of-range \
              answer is a panic or a mis-slice inside Pebble's own loops",
    },
];

// -- G2.24's population, reconciled against the source it claims -------------

/// The two files that define the FROZEN key format. `keys.go`'s own package doc
/// calls them frozen before the first SSTable is written, and between them they
/// hold every function that turns bytes into a key or a key into its parts.
///
/// This is the surface G2.24's title — "**every** key parser" — names. It used to
/// be reconciled against nothing: the gate ran four fixtures and pinned four
/// anchors, and a parser that was not one of the four was invisible. `decodeHLC`
/// was exactly that (see [`G2_24_PARSERS`]).
pub const KEY_FORMAT_SOURCES: &[&str] = &[
    "runtime-go/bluedb/keys.go",
    "runtime-go/bluedb/comparer.go",
];

/// What a key-format function owes the corrupt-input contract.
#[derive(PartialEq, Eq, Debug)]
pub enum ParserDuty {
    /// It reads structure out of an UNTRUSTED key and must refuse rather than
    /// index out of range. `why` names the G2.24 fixture that proves it.
    FailsClosed,
    /// It is TOTAL: every index and slice bound it forms is guarded by an
    /// explicit length test in its own body, on every path, so no input of any
    /// length can panic. `why` says which guard.
    Total,
    /// It reads at FIXED offsets with no guard of its own, and is unexported, so
    /// its safety is a contract on its callers. `why` states the contract; the
    /// callers are enumerated and checked by
    /// `every_decode_hlc_call_site_is_length_guarded`.
    GuardedByEveryCaller,
}

/// One function in [`KEY_FORMAT_SOURCES`] that takes a `[]byte`, and what it owes.
pub struct KeyParser {
    pub func: &'static str,
    pub file: &'static str,
    pub duty: ParserDuty,
    pub why: &'static str,
}

/// **Every `[]byte`-taking function in the two frozen key-format files.**
///
/// The population is not this list — it is read from the sources by
/// [`key_format_byte_funcs`] and reconciled against this list BOTH ways on every
/// gate run. A new parser is a FAILURE until it is recorded here with its duty,
/// which is the property "every key parser" was asserting and nothing was
/// checking.
///
/// The definition is deliberately syntactic (a top-level `func` in one of those
/// two files whose parameter list mentions `[]byte`) rather than a judgement
/// about which functions "really parse". A judgement is what a hand list is, and
/// the whole finding here is that a hand list reconciled against nothing lets a
/// member hide — `decodeHLC` sat outside the pinned set with two unguarded
/// slices, `b[0:8]` and `b[8:12]`, and nothing in the gate could see it.
pub const G2_24_PARSERS: &[KeyParser] = &[
    // -- keys.go ------------------------------------------------------------
    KeyParser {
        func: "decodeHLC",
        file: "runtime-go/bluedb/keys.go",
        duty: ParserDuty::GuardedByEveryCaller,
        why: "it slices b[0:8] and b[8:12] with NO guard of its own, so a buffer shorter than 12 \
              bytes would panic. It is unexported and has exactly three call sites, and each one \
              establishes the length BEFORE calling: decodeDataVersion rejects any key shorter \
              than 2+dataSuffixLen and then slices exactly hlcEncodedLen bytes out of it; \
              changelogTsOf requires len(key) == 1+hlcEncodedLen+1 EXACTLY; readMetaHLC refuses \
              any meta value whose length is not exactly hlcEncodedLen. That is the caller \
              contract keys.go states as `Callers guarantee len(b) >= 12`, and \
              `every_decode_hlc_call_site_is_length_guarded` is what keeps it true",
    },
    KeyParser {
        func: "invert12",
        file: "runtime-go/bluedb/keys.go",
        duty: ParserDuty::Total,
        why: "`for i := range b` over a fresh `make([]byte, len(b))` — no fixed offset, so it is \
              total on any length including zero",
    },
    KeyParser {
        func: "encodeDataKey",
        file: "runtime-go/bluedb/keys.go",
        duty: ParserDuty::Total,
        why: "append-only over the caller's userKey; it never reads at an offset",
    },
    KeyParser {
        func: "dataKeyPrefix",
        file: "runtime-go/bluedb/keys.go",
        duty: ParserDuty::Total,
        why: "append-only, same as encodeDataKey",
    },
    KeyParser {
        func: "decodeDataVersion",
        file: "runtime-go/bluedb/keys.go",
        duty: ParserDuty::FailsClosed,
        why: "TestDecodeDataVersionRejectsCorruptKeysWithoutPanic",
    },
    KeyParser {
        func: "changelogTsOf",
        file: "runtime-go/bluedb/keys.go",
        duty: ParserDuty::FailsClosed,
        why: "TestChangelogTsOfRejectsCorruptKeysWithoutPanic",
    },
    // -- comparer.go --------------------------------------------------------
    KeyParser {
        func: "skydbSplit",
        file: "runtime-go/bluedb/comparer.go",
        duty: ParserDuty::FailsClosed,
        why: "TestSplitNeverPanicsOnCorruptKeys",
    },
    KeyParser {
        func: "skydbCompare",
        file: "runtime-go/bluedb/comparer.go",
        duty: ParserDuty::Total,
        why: "every bound it forms comes from skydbSplit, which is proven to stay in [0, len] by \
              TestSplitNeverPanicsOnCorruptKeys; bytes.Compare is total",
    },
    KeyParser {
        func: "skydbEqual",
        file: "runtime-go/bluedb/comparer.go",
        duty: ParserDuty::Total,
        why: "delegates to skydbCompare and forms no bound of its own",
    },
    KeyParser {
        func: "skydbComparePointSuffixes",
        file: "runtime-go/bluedb/comparer.go",
        duty: ParserDuty::Total,
        why: "guards both empties and then calls bytes.Compare, which is total",
    },
    KeyParser {
        func: "skydbCompareRangeSuffixes",
        file: "runtime-go/bluedb/comparer.go",
        duty: ParserDuty::Total,
        why: "guards both empties and then compares through stripLenByte, itself guarded",
    },
    KeyParser {
        func: "stripLenByte",
        file: "runtime-go/bluedb/comparer.go",
        duty: ParserDuty::Total,
        why: "returns s unchanged when len(s) == 0, so s[:len(s)-1] is only ever formed on a \
              non-empty slice",
    },
    KeyParser {
        func: "skydbAbbrev",
        file: "runtime-go/bluedb/comparer.go",
        duty: ParserDuty::Total,
        why: "key[:skydbSplit(key)] — the bound is skydbSplit's answer, which never leaves \
              [0, len(key)]",
    },
    KeyParser {
        func: "skydbSeparator",
        file: "runtime-go/bluedb/comparer.go",
        duty: ParserDuty::Total,
        why: "every read goes through keyPartNoSentinel, which returns ok=false rather than a bad \
              bound; the rest is append",
    },
    KeyParser {
        func: "skydbSuccessor",
        file: "runtime-go/bluedb/comparer.go",
        duty: ParserDuty::Total,
        why: "guards the empty input, then reads only through keyPartNoSentinel",
    },
    KeyParser {
        func: "skydbImmediateSuccessor",
        file: "runtime-go/bluedb/comparer.go",
        duty: ParserDuty::Total,
        why: "append-only",
    },
    KeyParser {
        func: "keyPartNoSentinel",
        file: "runtime-go/bluedb/comparer.go",
        duty: ParserDuty::Total,
        why: "n := skydbSplit(key) is in [0, len(key)]; n < 1 returns ok=false, so key[:n-1] is \
              only formed for n >= 1",
    },
];

/// The three call sites of `decodeHLC`, and the length test each one establishes
/// before it.
///
/// `decodeHLC` is [`ParserDuty::GuardedByEveryCaller`], which is only a safety
/// argument while the callers are the recorded ones. Counted across the package's
/// NON-test sources so a fourth call site cannot appear unrecorded.
pub struct DecodeHlcCallSite {
    pub file: &'static str,
    /// The call, verbatim and unique in `file`.
    pub call: &'static str,
    /// The length test that dominates it, verbatim and unique in `file`.
    pub guard: &'static str,
}

pub const DECODE_HLC_CALL_SITES: &[DecodeHlcCallSite] = &[
    DecodeHlcCallSite {
        file: "runtime-go/bluedb/keys.go",
        call: "return decodeHLC(invert12(inv)), true",
        guard: "if len(key) < 2+dataSuffixLen || key[len(key)-1] != dataLenByte {",
    },
    DecodeHlcCallSite {
        file: "runtime-go/bluedb/keys.go",
        call: "return decodeHLC(key[1 : 1+hlcEncodedLen]), true",
        guard: "if len(key) != 1+hlcEncodedLen+1 || key[0] != tagChangelog || key[len(key)-1] != unversioned {",
    },
    DecodeHlcCallSite {
        file: "runtime-go/bluedb/pebble_engine.go",
        call: "return decodeHLC(v), nil",
        guard: "if len(v) != hlcEncodedLen {",
    },
];

/// Every non-test Go source in the package, for the `decodeHLC` call count. A
/// call in a FIXTURE is a test choosing its own input, not the engine parsing an
/// untrusted key.
pub const DECODE_HLC_SEARCH_SOURCES: &[&str] = &[
    "runtime-go/bluedb/changefeed.go",
    "runtime-go/bluedb/changelog.go",
    "runtime-go/bluedb/committer.go",
    "runtime-go/bluedb/comparer.go",
    "runtime-go/bluedb/engine.go",
    "runtime-go/bluedb/gc.go",
    "runtime-go/bluedb/hlc.go",
    "runtime-go/bluedb/hotkey.go",
    "runtime-go/bluedb/keychange.go",
    "runtime-go/bluedb/keys.go",
    "runtime-go/bluedb/pebble_engine.go",
    "runtime-go/bluedb/reader.go",
    "runtime-go/bluedb/readset.go",
    "runtime-go/bluedb/recent_changes.go",
    "runtime-go/bluedb/txn.go",
    "runtime-go/bluedb/validate.go",
    "runtime-go/bluedb/watermark.go",
];

/// Every top-level `func` in [`KEY_FORMAT_SOURCES`] whose parameter list mentions
/// `[]byte`, as `(file, name)`.
///
/// Syntactic on purpose — see [`G2_24_PARSERS`]. Methods (a `func (r recv) …`) are
/// skipped: there are none in these two files, and a method arriving is caught by
/// the same reconciliation because it would not be in the list either way.
pub fn key_format_byte_funcs(read: impl Fn(&str) -> Option<String>) -> Option<Vec<(&'static str, String)>> {
    let mut out = Vec::new();
    for file in KEY_FORMAT_SOURCES {
        let text = read(file)?;
        for line in super::gates_g2::strip_go_comments(&text).lines() {
            let Some(rest) = line.strip_prefix("func ") else {
                continue;
            };
            if rest.starts_with('(') {
                continue; // a method; the receiver is not the parameter list
            }
            let Some(open) = rest.find('(') else { continue };
            // The PARAMETER list only — balanced from `open`, so a `[]byte`
            // RETURN type (encodeHLC, encodeMetaKey, …) does not make a builder
            // look like a parser. Those are functions that produce keys; the
            // surface here is the ones that consume them.
            let bytes_after: Vec<char> = rest[open..].chars().collect();
            let mut depth = 0i32;
            let mut close = None;
            for (i, c) in bytes_after.iter().enumerate() {
                match c {
                    '(' => depth += 1,
                    ')' => {
                        depth -= 1;
                        if depth == 0 {
                            close = Some(i);
                            break;
                        }
                    }
                    _ => {}
                }
            }
            let Some(close) = close else { continue };
            let params: String = bytes_after[1..close].iter().collect();
            if !params.contains("[]byte") {
                continue;
            }
            out.push((*file, rest[..open].trim().to_string()));
        }
    }
    Some(out)
}

/// Reconcile the source against [`G2_24_PARSERS`] in BOTH directions.
fn check_key_parser_population(read: impl Fn(&str) -> Option<String>) -> Vec<String> {
    let Some(found) = key_format_byte_funcs(&read) else {
        return vec![format!(
            "G2.24: cannot read {} — the key-format sources are the surface its title names",
            KEY_FORMAT_SOURCES.join(", ")
        )];
    };
    let mut findings = Vec::new();
    for (file, name) in &found {
        if !G2_24_PARSERS
            .iter()
            .any(|p| p.func == name && p.file == *file)
        {
            findings.push(format!(
                "{file} declares `func {name}(… []byte …)`, which G2_24_PARSERS does not record. \
                 G2.24's title is `every key parser`; a parser nobody recorded is a parser nobody \
                 asked whether it fails closed — `decodeHLC` sat in exactly that state with two \
                 unguarded slices"
            ));
        }
    }
    for p in G2_24_PARSERS {
        if !found.iter().any(|(f, n)| *f == p.file && n == p.func) {
            findings.push(format!(
                "G2_24_PARSERS records `{}` in {}, which declares no such `[]byte`-taking \
                 function — a row that answers for code that is gone reads as coverage while \
                 being none",
                p.func, p.file
            ));
        }
        if p.duty == ParserDuty::FailsClosed && !G2_24_TESTS.contains(&p.why) {
            findings.push(format!(
                "`{}` is recorded as failing closed under fixture `{}`, which is not one of \
                 G2.24's pinned tests",
                p.func, p.why
            ));
        }
    }
    findings
}

/// Reconcile `decodeHLC`'s call sites against [`DECODE_HLC_CALL_SITES`], both
/// ways: a new call site is a finding, and a recorded one that moved or lost its
/// guard is a finding.
fn check_decode_hlc_call_sites(read: impl Fn(&str) -> Option<String>) -> Vec<String> {
    let mut findings = Vec::new();
    let mut calls = 0usize;
    for file in DECODE_HLC_SEARCH_SOURCES {
        let Some(text) = read(file) else {
            findings.push(format!("G2.24: cannot read {file}"));
            continue;
        };
        let code = super::gates_g2::strip_go_comments(&text);
        calls += code.matches("decodeHLC(").count();
        // The declaration itself is a `decodeHLC(` occurrence; discount it.
        calls -= code.matches("func decodeHLC(").count();
    }
    if calls != DECODE_HLC_CALL_SITES.len() {
        findings.push(format!(
            "the engine sources carry {calls} call(s) to decodeHLC; DECODE_HLC_CALL_SITES records \
             {}. decodeHLC slices b[0:8] and b[8:12] with no guard of its own, so an unrecorded \
             call site is a potential index-out-of-range on a short buffer — and a call site that \
             DISAPPEARED means this reconciliation is answering for code that no longer runs",
            DECODE_HLC_CALL_SITES.len()
        ));
    }
    for s in DECODE_HLC_CALL_SITES {
        let Some(text) = read(s.file) else { continue };
        let code = super::gates_g2::strip_go_comments(&text);
        for (what, needle) in [("call", s.call), ("guard", s.guard)] {
            if code.matches(needle).count() != 1 {
                findings.push(format!(
                    "{}: the recorded {what} `{needle}` is not present exactly once. decodeHLC is \
                     safe only because every caller establishes the length first",
                    s.file
                ));
            }
        }
    }
    findings
}

pub fn g2_24_key_parsers_fail_closed(ctx: &Ctx) -> GateOutcome {
    // ── The two-way reconciliation the title needs, ALONGSIDE the fixtures ──
    //
    // Four fixtures prove four parsers fail closed. "Every key parser" is a
    // claim about a POPULATION, and until this ran the population was a hand
    // list checked against nothing.
    //
    // It runs alongside the fixtures and NEVER instead of them. It used to
    // return early on the first population finding, and that made the gate's
    // behavioural evidence unobservable precisely when a source edit touched one
    // of the pinned strings — which is the case the fixtures exist for.
    // `decodeDataVersion`'s bounds guard IS one of those pinned strings (it is
    // the guard recorded for the first `DECODE_HLC_CALL_SITES` row), so deleting
    // it — G2.24's own registered mutation — tripped the reconciliation and the
    // gate returned before `go test` ran the panic the deletion creates. The
    // gate was red either way, but red on a string comparison rather than on the
    // recovered panic, and a proof that a gate detects a defect has to be a
    // proof that it observed the DEFECT.
    let mut findings = check_key_parser_population(|f| ctx.read(f));
    findings.extend(check_decode_hlc_call_sites(|f| ctx.read(f)));

    let behaviour = run_file_gate(
        ctx,
        &FileGate {
            id: "G2.24",
            tests: G2_24_TESTS,
            anchors: G2_24_ANCHORS,
            budget: Duration::from_secs(120),
            property: "every key parser rejects corrupt bytes without panicking and still accepts well-formed ones",
        },
    );

    // The merge itself is [`super::gates_g2::merge_static_and_behavioural`] —
    // one implementation, because this gate's defect turned out to be a class
    // and the same combine is now used by `run_file_gate`, `run_audit_gate`,
    // G2.6 and G2.9a.
    super::gates_g2::merge_static_and_behavioural(
        format!(
            "the key-format parser population does not match its pin ({} recorded)",
            G2_24_PARSERS.len()
        ),
        findings,
        "population reconciliation",
        behaviour,
    )
}

// ---------------------------------------------------------------------------
// G2.25 — one writer, one format name
// ---------------------------------------------------------------------------

pub const G2_25_TESTS: &[&str] = &[
    "TestSecondOpenFailsSingleProcessLock",
    "TestWrongComparerNameRefusesOpen",
    "TestComparerName",
];

pub const G2_25_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestSecondOpenFailsSingleProcessLock",
        needle: "second Open of the same directory must FAIL — Pebble holds an exclusive lock",
        why: "BlueDB relies on Pebble's directory lock instead of reinventing a flock (design §6); \
              the reliance is only sound while it is asserted",
    },
    SourceAnchor {
        func: "TestWrongComparerNameRefusesOpen",
        needle: "opening under a different Comparer.Name must be REFUSED",
        why: "§7 G1 / §2.4: the cheapest insurance against the irreversible-format bug is that a \
              store will not open under a comparer it was not written with",
    },
    SourceAnchor {
        func: "TestWrongComparerNameRefusesOpen",
        needle: "the second Open was refused, but NOT by the comparer check",
        why: "the fixture was green for the WRONG REASON without it: a bare `err != nil` is \
              satisfied by ANY refusal, so leaking the Pebble handle in Close makes the second \
              open fail on the DIRECTORY LOCK and the comparer check is never reached. This half \
              requires the error to name `comparer name` and both names, which only the manifest \
              check produces",
    },
    SourceAnchor {
        func: "TestComparerName",
        needle: "comparer name drifted: %q",
        why: "the name is the format's identity. It is what makes the refusal above possible, and \
              editing it is the one change that silently makes every existing store unopenable",
    },
];

/// **The single-writer guarantee's BlueDB half.**
///
/// `TestSecondOpenFailsSingleProcessLock` is recorded in `SOURCE_SIDE_FALSIFIERS`
/// as reddened by no registered patch, and the old argument for that was "the
/// lock is Pebble's, so no revert of BlueDB source can make a second Open
/// succeed". That is false, and a Judge round said so: Pebble takes the directory
/// lock **inside `pebble.Open`** (v2.1.6 `open.go:128-132`, unconditional), so the
/// guarantee holds only while BlueDB actually CALLS `pebble.Open` eagerly. A
/// lazy-open refactor that deferred it to the first use would let two `Open`s of
/// one directory both return, and it would redden that fixture for exactly the
/// right reason — which makes the eagerness BlueDB's contract, not Pebble's.
///
/// So it is pinned. The needle is the eager call itself.
const G2_25_EAGER_OPEN_PIN: (&str, &str) = (
    "runtime-go/bluedb/pebble_engine.go",
    "db, err := pebble.Open(cfg.dir, opts)",
);

pub fn g2_25_one_writer_one_format(ctx: &Ctx) -> GateOutcome {
    let (file, needle) = G2_25_EAGER_OPEN_PIN;
    let occurrences = ctx
        .read(file)
        .map(|s| super::gates_g2::strip_go_comments(&s).matches(needle).count());
    if occurrences != Some(1) {
        return GateOutcome::fail(
            "the single-writer guarantee's own half is not where it is pinned".to_string(),
            vec![format!(
                "{file}: `{needle}` occurs {} time(s), want exactly 1. Pebble's exclusive \
                 directory lock is taken INSIDE pebble.Open, so `a second Open of the same \
                 directory fails` is BlueDB's guarantee only while openWith calls it EAGERLY. \
                 Deferring it to first use would let two Opens of one directory both return, and \
                 TestSecondOpenFailsSingleProcessLock is recorded as reddened by no registered \
                 patch — so nothing else would notice",
                occurrences.map(|n| n.to_string()).unwrap_or_else(|| "«unreadable»".into())
            )],
        );
    }

    run_file_gate(
        ctx,
        &FileGate {
            id: "G2.25",
            tests: G2_25_TESTS,
            anchors: G2_25_ANCHORS,
            budget: Duration::from_secs(120),
            property: "a store admits one writer and one immutable format name",
        },
    )
}

// ---------------------------------------------------------------------------
// G0.8 — every engine SOURCE is reachable by a recorded mutation
// ---------------------------------------------------------------------------
//
// # The class, not the instance
//
// Four adversarial Judge rounds have now each found one unfalsified leaf, and
// each fix has been local to the site attacked. Round 3 hardened the PRODUCER of
// `collWitness` (`Txn.ScanCollection`); round 4 deleted the CONSUMER
// (`validate()`'s witness arm) and every gate stayed green. The two are one file
// apart.
//
// The question nobody asked, and the reason the next round would have found the
// next instance, is mechanically answerable: **which engine source files are
// touched by NO recorded mutation?** Across the 51 patches in
// `docs/bluedb/mutations/`, ZERO touched `validate.go` or `readset.go` — both
// named verbatim in P1's scope row. Eight of the seventeen non-test sources in
// `runtime-go/bluedb/` were in that state.
//
// This gate asks it on every run. The population comes from `read_dir`, never
// from a list, because a list is exactly where a new file hides — the same
// argument `runtime_sources_plus_the_two_older_families_are_the_whole_package`
// makes one level up for `*_test.go`. Coverage is read from the PATCHES' own
// `diff --git` paths rather than from `Mutation.targets`: `targets` is a
// declaration whose purpose is the `UNVERIFIED-SINCE` decay check and is
// deliberately broader than the diff (`mutations.rs` says so where it refuses to
// read it for the same reason), while `git apply` changes precisely what the diff
// headers name.
//
// # The exemption, and why it is not an escape hatch
//
// A source may be listed in [`DELIBERATELY_UNMUTATED`] instead — the idiom
// `SOURCE_SIDE_FALSIFIERS` already establishes for leaves that no honest revert
// can redden. An exemption carries three things:
//
// 1. **An argument** for why no honest revert of THIS file reddens a gate with a
//    discriminating assertion. "Nobody got to it yet" is not one.
// 2. **A `funcs` pin** — every top-level `func` the file declares today,
//    reconciled BOTH ways on every run. An exemption is a statement about the
//    behaviour a file contains; the first function to arrive in an exempt file
//    turns this gate red and forces the argument to be re-made. That is what
//    stops an exemption written once from covering code written later.
// 3. **Mutual exclusion** — an exempt file that a mutation DOES touch is a stale
//    exemption, and is reported as one. The two lists cannot both be right.

/// The engine directory whose non-test sources this gate enumerates.
const ENGINE_DIR: &str = "runtime-go/bluedb";

/// A source deliberately left unmutated, with the argument and the pin.
pub struct UnmutatedSource {
    pub file: &'static str,
    /// Every top-level `func` the file declares, canonicalised as `Recv.method`
    /// (receiver type without `*`) or a bare name. Reconciled both ways.
    pub funcs: &'static [&'static str],
    pub why: &'static str,
}

pub const DELIBERATELY_UNMUTATED: &[UnmutatedSource] = &[
    UnmutatedSource {
        file: "runtime-go/bluedb/engine.go",
        funcs: &[],
        why: "it declares NO function at all — it is the package's type surface (the Engine / \
              Reader / Cursor / Changelog / WatermarkRegistry interfaces, CommitReq, ReadSet, \
              CommitResult, the sentinel errors). There is no behaviour to revert: every edit \
              here is either a compile error or a change to some other file's behaviour. The \
              `funcs` pin is EMPTY, so the first function to land in it turns this gate red and \
              the exemption has to be argued again",
    },
    UnmutatedSource {
        file: "runtime-go/bluedb/readset.go",
        funcs: &["inRangeClosed"],
        why: "its one function, `inRangeClosed`, has a single consumer — `validate()`'s range arm \
              — and `ReadSet.ranges` has NO producer in Stage 2 (Txn.Scan / ScanRange / \
              ScanFallback were excised; TestStage2ReadSetRangesHaveNoProducer pins it, G2.14 \
              gates it). `txn.go`'s excision note states the corollary as a REQUIREMENT rather \
              than an observation: *mutating inRangeClosed to `return false` must not change any \
              Stage-2 gate*. A mutation here is therefore obliged to record VACUOUS; one that \
              recorded PROVEN would mean some gate had started asserting the range arm, which \
              Stage 2 forbids. The remainder of the file is type declarations",
    },
    UnmutatedSource {
        file: "runtime-go/bluedb/hotkey.go",
        funcs: &[
            "hotKeyTable.anyHot",
            "hotKeyTable.decay",
            "hotKeyTable.hotSubset",
            "hotKeyTable.isHot",
            "hotKeyTable.recordAbort",
            "leaseManager.acquire",
            "leaseManager.grantHeadLocked",
            "leaseManager.hasWaiters",
            "leaseManager.reap",
            "leaseManager.release",
            "leaseManager.removeLocked",
            "newHotKeyTable",
            "newLeaseManager",
        ],
        why: "the hot-key / lease layer is a LIVENESS mechanism (§6.2, §6.4): it decides which \
              path a contended transaction takes and how long it waits, never whether the \
              committer validates it. `validate()` runs inside the single committer for every \
              transactional job either way, and `recordAbort` is fed BY the validator's verdict \
              (committer.go, gated on `pointConflict`) rather than consulted by it — so no revert \
              of this file can produce a non-serializable history, which is what every Stage-2 \
              gate asserts. It is also referenced by NO test in the package today, so any revert \
              would record VACUOUS: that is a coverage statement about §6.2, which P1 makes no \
              claim about, not a hole in a claim P1 does make",
    },
    UnmutatedSource {
        file: "runtime-go/bluedb/changefeed.go",
        funcs: &[
            "changeFeedSub.Overflowed",
            "pebbleEngine.emitChangeBatch",
            "pebbleEngine.hasChangeSubs",
            "pebbleEngine.subscribeChanges",
        ],
        why: "the Phase-4 reactive fan-out. It sits entirely AFTER Apply — `emitChangeBatch` is \
              called on already-durable, already-validated changes — so the worst a revert can do \
              is lose a notification, never a durable write, a commitTs or a conflict verdict. No \
              P1 gate asserts anything over it and no test in the package references it, so a \
              mutation records VACUOUS. P4's gates are the ones that will own it",
    },
    // `recent_changes.go` was in this list's shape too — the ring that IS the
    // SSI validation window, touched by no mutation — and is NOT exempt: its
    // falsifier is `G2.13h/ring-answers-for-a-range-it-does-not-hold`, the
    // revert its own docstring names as "the exact N6 shape".
    UnmutatedSource {
        file: "runtime-go/bluedb/hlc.go",
        funcs: &[
            "HLC.IsZero",
            "HLC.Less",
            "hlcClock.highWater",
            "hlcClock.next",
            "newHLCClock",
            "nonNegative",
            "systemWallClock",
        ],
        why: "every honest revert of this file surfaces as ONE assertion — the commit clock \
              re-issued or reordered a timestamp — and two registered mutations already own that \
              assertion, both reaching it THROUGH `hlcClock.next()`: \
              `G2.17/reopen-does-not-floor-the-clock` (classified on `restart floor violated:`) \
              and `G2.15/one-committs-for-the-whole-batch` (on `per-job commitTs not strictly \
              increasing:`). A third mutation inside `hlc.go` would trip one of those two strings \
              and mint a second PROVEN out of a defect that is already proven, which \
              `expect_strings_are_pairwise_discriminating` forbids by construction and which the \
              one-gate-per-property split exists to prevent. The only behaviour those two do not \
              reach is the logical-overflow borrow at `math.MaxUint32`, which needs 2^32 commits \
              inside one wall-millisecond: a mutation of it records VACUOUS, which is a statement \
              about the corpus rather than a proof",
    },
];

/// Every top-level `func` declaration in a Go source, canonicalised.
///
/// A line beginning `func ` at column zero is a declaration — comments start
/// `//` and every nested closure is indented — so this needs no parser.
fn go_func_names(src: &str) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    for line in src.lines() {
        let Some(rest) = line.strip_prefix("func ") else {
            continue;
        };
        let ident = |s: &str| -> String {
            s.trim_start()
                .chars()
                .take_while(|c| c.is_alphanumeric() || *c == '_')
                .collect()
        };
        if let Some(after) = rest.trim_start().strip_prefix('(') {
            // A method: `(recv *Type) Name(`.
            let Some((recv, tail)) = after.split_once(')') else {
                continue;
            };
            let ty = recv
                .split_whitespace()
                .next_back()
                .unwrap_or("")
                .trim_start_matches('*');
            let name = ident(tail);
            if !ty.is_empty() && !name.is_empty() {
                out.insert(format!("{ty}.{name}"));
            }
        } else {
            let name = ident(rest);
            if !name.is_empty() {
                out.insert(name);
            }
        }
    }
    out
}

/// The repo-relative paths a unified diff declares it changes.
fn patch_paths(patch: &str) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    for line in patch.lines() {
        let Some(rest) = line.strip_prefix("diff --git ") else {
            continue;
        };
        for p in rest.split_whitespace() {
            let p = p
                .strip_prefix("a/")
                .or_else(|| p.strip_prefix("b/"))
                .unwrap_or(p);
            out.insert(p.to_string());
        }
    }
    out
}

/// The non-test `.go` sources in `runtime-go/bluedb/`, discovered on disk.
fn engine_sources(ctx: &Ctx) -> Result<BTreeSet<String>, String> {
    let dir = ctx.path(ENGINE_DIR);
    let mut out = BTreeSet::new();
    for e in std::fs::read_dir(&dir).map_err(|e| format!("read {ENGINE_DIR}: {e}"))? {
        let p = e.map_err(|e| format!("dir entry in {ENGINE_DIR}: {e}"))?.path();
        let name = p
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or_default()
            .to_string();
        if name.ends_with(".go") && !name.ends_with("_test.go") {
            out.insert(format!("{ENGINE_DIR}/{name}"));
        }
    }
    Ok(out)
}

/// Every engine source a REGISTERED mutation's patch touches.
///
/// A patch that is not on disk contributes NO coverage and raises no finding
/// here, and both halves of that are deliberate. Gates for goals 1–5 gate a
/// substrate that has not landed: they are registered (§9.6 check 1 requires it),
/// their bodies are `pending` probes, and their mutations are declared before the
/// patch can be authored. Reporting each of them here would drown this gate's own
/// findings behind rows that say only "P2 has not landed" — and the missing patch
/// itself is already G0.6's finding, on the gates that RUN, in the words that
/// belong to it.
///
/// Silence is safe because the direction is fail-CLOSED: an unread patch shrinks
/// the covered set, so it can only make this gate redder, never greener.
fn mutation_covered(ctx: &Ctx) -> BTreeSet<String> {
    let mut covered = BTreeSet::new();
    for gate in super::registry::REGISTRY {
        for m in gate.mutations.as_slice() {
            if let Some(text) = ctx.read(m.patch) {
                covered.extend(patch_paths(&text));
            }
        }
    }
    covered
}

/// The uncovered set, computed the same way in the gate body and in
/// `cargo test`. `(uncovered, findings)`.
fn coverage_findings(
    sources: &BTreeSet<String>,
    covered: &BTreeSet<String>,
    func_names: impl Fn(&str) -> Option<BTreeSet<String>>,
) -> (Vec<String>, Vec<String>) {
    let exempt: BTreeSet<&str> = DELIBERATELY_UNMUTATED.iter().map(|u| u.file).collect();
    let mut findings = Vec::new();

    let uncovered: Vec<String> = sources
        .iter()
        .filter(|s| !covered.contains(*s) && !exempt.contains(s.as_str()))
        .cloned()
        .collect();
    for f in &uncovered {
        findings.push(format!(
            "{f}: no recorded mutation's patch touches this engine source, and it is not in \
             DELIBERATELY_UNMUTATED. Nothing has shown that any line of it is load-bearing — \
             which is the state `validate.go` was in when a four-line deletion of the SSI \
             validator's collection-witness arm left every gate green. Author a mutation whose \
             assertion lives in a gated test, or record it as deliberately-unmutated WITH the \
             argument for why no honest revert reddens a gate"
        ));
    }

    for u in DELIBERATELY_UNMUTATED {
        if !sources.contains(u.file) {
            findings.push(format!(
                "{}: DELIBERATELY_UNMUTATED names a file that is not a non-test source in \
                 {ENGINE_DIR} — an exemption for a file that does not exist exempts nothing and \
                 hides the rename that moved it",
                u.file
            ));
            continue;
        }
        if covered.contains(u.file) {
            findings.push(format!(
                "{}: exempted as deliberately-unmutated, but a recorded mutation's patch DOES \
                 touch it. The exemption is stale — delete the row; the file is covered",
                u.file
            ));
        }
        let Some(on_disk) = func_names(u.file) else {
            findings.push(format!(
                "{}: cannot read the exempted source to reconcile its `funcs` pin",
                u.file
            ));
            continue;
        };
        let pinned: BTreeSet<String> = u.funcs.iter().map(|s| (*s).to_string()).collect();
        if on_disk != pinned {
            let added: Vec<&String> = on_disk.difference(&pinned).collect();
            let gone: Vec<&String> = pinned.difference(&on_disk).collect();
            findings.push(format!(
                "{}: the `funcs` pin has drifted — arrived: {added:?}, gone: {gone:?}. An \
                 exemption is an argument about the behaviour a file CONTAINS, so behaviour that \
                 arrived after the argument was written is not covered by it. Re-make the \
                 argument for the new surface, or give the file a mutation",
                u.file
            ));
        }
    }

    (uncovered, findings)
}

pub fn g0_8_engine_sources_are_mutation_covered(ctx: &Ctx) -> GateOutcome {
    let sources = match engine_sources(ctx) {
        Ok(s) => s,
        Err(e) => {
            return GateOutcome::fail(
                "G0.8 cannot enumerate the engine sources it certifies",
                vec![e],
            )
        }
    };
    if sources.is_empty() {
        return GateOutcome::fail(
            "G0.8 found no engine sources",
            vec![format!(
                "G0.8: {ENGINE_DIR} holds no non-test `.go` file — an empty population makes \
                 every coverage claim below vacuously true"
            )],
        );
    }

    let covered = mutation_covered(ctx);
    let (uncovered, findings) = coverage_findings(&sources, &covered, |f| {
        ctx.read(f).map(|t| go_func_names(&t))
    });

    if findings.is_empty() {
        GateOutcome::pass(format!(
            "all {} non-test source(s) in {ENGINE_DIR} are accounted for: {} touched by a \
             recorded mutation's patch, {} deliberately unmutated with an argument and a \
             reconciled `funcs` pin",
            sources.len(),
            sources.len() - DELIBERATELY_UNMUTATED.len(),
            DELIBERATELY_UNMUTATED.len()
        ))
    } else {
        GateOutcome::fail(
            format!(
                "{} of {} non-test source(s) in {ENGINE_DIR} are falsified by nothing",
                uncovered.len(),
                sources.len()
            ),
            findings,
        )
    }
}

// ---------------------------------------------------------------------------
// The family, as data — read by `gates_g2_13.rs`'s per-leaf rule
// ---------------------------------------------------------------------------

/// Every gate in this file, with the leaves it pins and the anchors it enforces.
///
/// `gates_g2_13.rs` owns the per-leaf falsifier rule for the whole crate, so it
/// reads this rather than duplicating the table. Keyed by gate id for the same
/// reason `gate_anchors` is: G2.9a is not an `AuditGate` either.
pub const RUNTIME_GATES: &[(&str, &[&str], &[SourceAnchor])] = &[
    ("G2.14", G2_14_TESTS, G2_14_ANCHORS),
    ("G2.26", G2_26_TESTS, G2_26_ANCHORS),
    ("G2.27", G2_27_TESTS, G2_27_ANCHORS),
    ("G2.15", G2_15_TESTS, G2_15_ANCHORS),
    ("G2.16", G2_16_TESTS, G2_16_ANCHORS),
    ("G2.17", G2_17_TESTS, G2_17_ANCHORS),
    ("G2.18", G2_18_TESTS, G2_18_ANCHORS),
    ("G2.19", G2_19_TESTS, G2_19_ANCHORS),
    ("G2.20", G2_20_TESTS, G2_20_ANCHORS),
    ("G2.21", G2_21_TESTS, G2_21_ANCHORS),
    ("G2.22", G2_22_TESTS, G2_22_ANCHORS),
    ("G2.23", G2_23_TESTS, G2_23_ANCHORS),
    ("G2.24", G2_24_TESTS, G2_24_ANCHORS),
    ("G2.25", G2_25_TESTS, G2_25_ANCHORS),
];

#[cfg(test)]
mod tests {
    use super::*;

    fn repo() -> std::path::PathBuf {
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../..")
            .canonicalize()
            .expect("repo root")
    }

    fn read(rel: &str) -> String {
        std::fs::read_to_string(repo().join(rel)).unwrap_or_else(|e| panic!("read {rel}: {e}"))
    }

    /// The ownership table IS the family's population, checked BOTH ways and
    /// per file. Asserted at `cargo test` time as well as in every gate body, so
    /// drift fails the build rather than only a full-tier run.
    #[test]
    fn the_ownership_table_matches_the_corpus_on_disk() {
        let mut declared: BTreeSet<(String, String)> = BTreeSet::new();
        for src in RUNTIME_SOURCES {
            for t in go_test_names(&read(src)) {
                declared.insert(((*src).to_string(), t));
            }
        }
        let recorded: BTreeSet<(String, String)> = RUNTIME_OWNERSHIP
            .iter()
            .map(|o| (o.file.to_string(), o.test.to_string()))
            .collect();
        assert_eq!(
            declared, recorded,
            "RUNTIME_OWNERSHIP has drifted from the corpus (left = on disk, right = recorded)"
        );
    }

    /// **No `*_test.go` in `runtime-go/bluedb/` is owned by nobody.**
    ///
    /// This module exists because 38 tests in eight files were run by no gate;
    /// the same thing can happen again one FILE at a time. The three families —
    /// G2.9a's `crashsim_test.go`, G2.13*'s `audit_test.go`, and
    /// [`RUNTIME_SOURCES`] — must therefore partition the directory, discovered
    /// by `read_dir` rather than read from a list, because a list is exactly
    /// where a new file hides.
    #[test]
    fn runtime_sources_plus_the_two_older_families_are_the_whole_package() {
        let dir = repo().join("runtime-go/bluedb");
        let mut on_disk: BTreeSet<String> = BTreeSet::new();
        for e in std::fs::read_dir(&dir).expect("read runtime-go/bluedb") {
            let p = e.expect("dir entry").path();
            let name = p.file_name().and_then(|n| n.to_str()).unwrap_or("").to_string();
            if name.ends_with("_test.go") {
                on_disk.insert(format!("runtime-go/bluedb/{name}"));
            }
        }
        let mut owned: BTreeSet<String> = RUNTIME_SOURCES.iter().map(|s| s.to_string()).collect();
        owned.insert(super::super::gates_g2_13::AUDIT_SOURCE.to_string());
        owned.insert("runtime-go/bluedb/crashsim_test.go".to_string());
        assert_eq!(
            on_disk, owned,
            "a `*_test.go` in runtime-go/bluedb is owned by no gate family (left = on disk, \
             right = owned). Add it to RUNTIME_SOURCES and record its tests in RUNTIME_OWNERSHIP, \
             or the tests in it run under no gate — the exact state this module was written to end"
        );
    }

    /// **G2.24's population is the key-format source, both ways.**
    ///
    /// The gate body runs the same reconciliation; this runs it in `cargo test`
    /// where no `go test` budget is in the way, so a new parser is caught by the
    /// cheapest gate that can see it.
    #[test]
    fn every_key_format_byte_func_is_recorded_with_its_duty() {
        let root = repo();
        let read = |f: &str| std::fs::read_to_string(root.join(f)).ok();
        let findings = super::check_key_parser_population(read);
        assert!(findings.is_empty(), "{findings:#?}");
    }

    /// **`decodeHLC` is safe because of its callers, and these are its callers.**
    ///
    /// It slices `b[0:8]` and `b[8:12]` with no guard of its own — the one
    /// [`ParserDuty::GuardedByEveryCaller`] row in [`G2_24_PARSERS`]. That is a
    /// real argument only while the call sites are the recorded three and each
    /// still establishes the length first, so both are checked, in both
    /// directions: a fourth call site fails on the count, and a recorded site
    /// that moved or lost its guard fails on the needle.
    ///
    /// This is what a [`ParserDuty::GuardedByEveryCaller`] row costs. The
    /// alternative — a guard inside `decodeHLC` — is not available: `keys.go` is
    /// FROZEN (its bytes are pinned by a sha256 in `frozen_stage1.rs`, because
    /// `Comparer.Name` is baked into every SSTable already written), so changing
    /// it is a deliberate act with a re-taken pin, not a drive-by hardening.
    #[test]
    fn every_decode_hlc_call_site_is_length_guarded() {
        let root = repo();
        let read = |f: &str| std::fs::read_to_string(root.join(f)).ok();
        let findings = super::check_decode_hlc_call_sites(read);
        assert!(findings.is_empty(), "{findings:#?}");
    }

    /// [`DECODE_HLC_SEARCH_SOURCES`] is every NON-test Go source in the package,
    /// from `read_dir` — because a hand list is exactly where a new file with a
    /// new `decodeHLC` call hides.
    #[test]
    fn decode_hlc_search_sources_are_the_whole_non_test_package() {
        let dir = repo().join("runtime-go/bluedb");
        let mut on_disk: BTreeSet<String> = BTreeSet::new();
        for e in std::fs::read_dir(&dir).expect("read runtime-go/bluedb") {
            let p = e.expect("dir entry").path();
            let name = p.file_name().and_then(|n| n.to_str()).unwrap_or("").to_string();
            if name.ends_with(".go") && !name.ends_with("_test.go") {
                on_disk.insert(format!("runtime-go/bluedb/{name}"));
            }
        }
        let recorded: BTreeSet<String> = DECODE_HLC_SEARCH_SOURCES
            .iter()
            .map(|s| s.to_string())
            .collect();
        assert_eq!(
            on_disk, recorded,
            "DECODE_HLC_SEARCH_SOURCES is not the non-test package (left = on disk, right = \
             recorded). decodeHLC's safety is a contract on its CALLERS; a source this list does \
             not search is a source whose calls are not counted"
        );
    }

    /// Every gate's `-run` set is exactly the rows that name it as owner, and
    /// the sets are disjoint. An overlap would let one mutation redden two
    /// gates, which is what the per-property split exists to prevent.
    #[test]
    fn every_gate_runs_exactly_the_rows_that_name_it() {
        let mut seen: Vec<&str> = Vec::new();
        for (id, tests, _) in RUNTIME_GATES {
            let owned: BTreeSet<&str> = RUNTIME_OWNERSHIP
                .iter()
                .filter(|o| o.owner == *id)
                .map(|o| o.test)
                .collect();
            let pinned: BTreeSet<&str> = tests.iter().copied().collect();
            assert_eq!(owned, pinned, "{id}'s `-run` set disagrees with RUNTIME_OWNERSHIP");
            for t in *tests {
                assert!(!seen.contains(t), "{t} is pinned by more than one gate");
                seen.push(t);
            }
        }
        // And every row's owner is one of the twelve — a typo'd id would
        // otherwise read as coverage while running nothing.
        for o in RUNTIME_OWNERSHIP {
            assert!(
                RUNTIME_GATES.iter().any(|(id, _, _)| *id == o.owner),
                "{} names owner {}, which is not a gate in this family",
                o.test,
                o.owner
            );
        }
    }

    /// Every gate is registered, on goal 2, wired to its own body, and declares
    /// a mutation. (The empty-mutation case is a const-eval error in
    /// `registry.rs`; this catches a gate that never made it into the registry
    /// at all, which nothing else would.)
    #[test]
    fn every_gate_is_registered_and_wired_to_its_own_body() {
        let bodies: &[(&str, fn(&Ctx) -> GateOutcome)] = &[
            ("G2.14", g2_14_readset_scope),
            ("G2.26", g2_26_point_arm_enforces),
            ("G2.27", g2_27_collection_witness_enforces),
            ("G2.15", g2_15_group_commit_per_job),
            ("G2.16", g2_16_read_resolution),
            ("G2.17", g2_17_restart_floor),
            ("G2.18", g2_18_changelog_roundtrip),
            ("G2.19", g2_19_gc_collects_only_the_dead),
            ("G2.20", g2_20_threshold_is_durable_and_clamped),
            ("G2.21", g2_21_gc_pass_is_physical),
            ("G2.22", g2_22_comparer_contract),
            ("G2.23", g2_23_shortening_hooks),
            ("G2.24", g2_24_key_parsers_fail_closed),
            ("G2.25", g2_25_one_writer_one_format),
        ];
        for (id, f) in bodies {
            let g = super::super::registry::find(id).unwrap_or_else(|| panic!("{id} unregistered"));
            assert_eq!(g.goal, 2, "{id} belongs to goal 2");
            assert_eq!(g.run as usize, *f as usize, "{id} is not wired to its body");
            assert!(!g.mutations.as_slice().is_empty(), "{id} declares no mutation");
        }
        let listed: BTreeSet<&str> = bodies.iter().map(|(id, _)| *id).collect();
        let table: BTreeSet<&str> = RUNTIME_GATES.iter().map(|(id, _, _)| *id).collect();
        assert_eq!(listed, table, "RUNTIME_GATES and the body list disagree");
    }

    /// Every anchor resolves against the corpus TODAY, in EXECUTING code, and
    /// names a leaf its own gate pins. A needle that never matched would make
    /// its gate permanently red; one copied wrong would make it red for the
    /// wrong reason; one on a leaf the gate does not run would be a pin nobody's
    /// falsifier needs.
    #[test]
    fn every_anchor_resolves_and_belongs_to_its_gate() {
        let mut bodies = Vec::new();
        for src in RUNTIME_SOURCES {
            bodies.extend(enumerate_injections(&read(src)));
        }
        for (id, tests, anchors) in RUNTIME_GATES {
            for a in *anchors {
                assert!(
                    tests.contains(&a.func),
                    "{id} anchors {} which it does not pin",
                    a.func
                );
                let body = &bodies
                    .iter()
                    .find(|f| f.test == a.func)
                    .unwrap_or_else(|| panic!("{id}: no `func {}` in the corpus", a.func))
                    .body;
                assert!(
                    body.contains(a.needle),
                    "{id}: `func {}` does not contain `{}` in executing code",
                    a.func,
                    a.needle
                );
            }
            // The per-leaf rule, asserted here too rather than only through
            // `gates_g2_13.rs`: this family's falsifier kind is the anchor, so a
            // leaf without one is a leaf that could be gutted to `{}` with the
            // gate green — an empty Go test emits `pass`.
            for t in *tests {
                assert!(
                    anchors.iter().any(|a| a.func == *t),
                    "{id} pins {t} with no SourceAnchor over it — an empty body would still report \
                     a passing event and this gate would stay green"
                );
            }
        }
    }

    /// **G0.8's subject, asserted at `cargo test` time too.**
    ///
    /// The gate body computes this from `ctx.root()`; this computes it from the
    /// repo, so a source that arrives with no falsifier fails the BUILD rather
    /// than waiting for a full-tier run. Same helpers, so the two cannot drift.
    #[test]
    fn every_engine_source_is_mutation_covered_or_argued() {
        let root = repo();
        let mut sources: BTreeSet<String> = BTreeSet::new();
        for e in std::fs::read_dir(root.join(ENGINE_DIR)).expect("read runtime-go/bluedb") {
            let p = e.expect("dir entry").path();
            let name = p.file_name().and_then(|n| n.to_str()).unwrap_or("").to_string();
            if name.ends_with(".go") && !name.ends_with("_test.go") {
                sources.insert(format!("{ENGINE_DIR}/{name}"));
            }
        }
        assert!(!sources.is_empty(), "no engine sources found");

        // Same rule as the gate body: an unauthored patch (the goal-1..5 probes)
        // contributes no coverage and is G0.6's finding, not this one's.
        let mut covered: BTreeSet<String> = BTreeSet::new();
        for gate in super::super::registry::REGISTRY {
            for m in gate.mutations.as_slice() {
                if let Ok(text) = std::fs::read_to_string(root.join(m.patch)) {
                    covered.extend(patch_paths(&text));
                }
            }
        }

        let (_, findings) = coverage_findings(&sources, &covered, |f| {
            std::fs::read_to_string(root.join(f)).ok().map(|t| go_func_names(&t))
        });
        assert!(findings.is_empty(), "{}", findings.join("\n\n"));
    }

    /// The parser G0.8's `funcs` pin rests on, over the two shapes Go has.
    #[test]
    fn go_func_names_reads_methods_and_functions() {
        let src = "\
package bluedb\n\
\n\
// func notADeclaration()\n\
func inRangeClosed(lo, hi, key []byte) bool {\n\
\tf := func(x int) int { return x }\n\
\treturn f(1) == 1\n\
}\n\
\n\
func (c *hlcClock) next() HLC {}\n\
func (h HLC) Less(o HLC) bool {}\n";
        let got = go_func_names(src);
        let want: BTreeSet<String> = ["inRangeClosed", "hlcClock.next", "HLC.Less"]
            .iter()
            .map(|s| (*s).to_string())
            .collect();
        assert_eq!(got, want);
    }

    #[test]
    fn g0_8_is_registered_cross_cutting_and_wired_to_its_body() {
        let g = super::super::registry::find("G0.8").expect("G0.8 unregistered");
        assert_eq!(g.goal, 0, "G0.8 is cross-cutting");
        assert_eq!(
            g.run as usize, g0_8_engine_sources_are_mutation_covered as usize,
            "G0.8 is not wired to its body"
        );
        assert!(!g.mutations.as_slice().is_empty());
    }

    /// These fixtures are flat. The gate bodies assert it at run time (a new
    /// `t.Run` arm would run under no gate); asserted here so the fact is
    /// checked without a full-tier run.
    #[test]
    fn every_pinned_fixture_is_subtest_free() {
        let mut bodies = Vec::new();
        for src in RUNTIME_SOURCES {
            bodies.extend(enumerate_injections(&read(src)));
        }
        for o in RUNTIME_OWNERSHIP {
            let body = &bodies
                .iter()
                .find(|f| f.test == o.test)
                .unwrap_or_else(|| panic!("no `func {}` in the corpus", o.test))
                .body;
            assert_eq!(
                body.matches(T_RUN).count(),
                0,
                "{} has a `t.Run(` arm; this family's `-run` patterns are depth 1, so the arm \
                 would be neither selected nor counted",
                o.test
            );
        }
    }
}
