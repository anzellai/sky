//! G2.13a–i — the nine audit-corpus properties, **one gate each**.
//!
//! The subject is `runtime-go/bluedb/audit_test.go`: the regression corpus the
//! C2–C8 fixes shipped with, one fixture per defect found in the Stage-2 port,
//! plus (G2.13i) the two Stage-1 remedies that shipped with no test at all.
//!
//! # Why one gate per PROPERTY — and why that is not a cap on mutations
//!
//! `mutations.rs` classifies a mutation with
//! `if red.exit_ok || !red.output.contains(m.expect)`. It checks only that
//! **this** mutation's `expect` string is PRESENT — never that the others are
//! ABSENT — and nothing anywhere required `expect` strings to be mutually
//! discriminating. With every property's mutation hung off ONE gate, a single
//! C1-era defect that broke several at once would mint a `PROVEN` for each out
//! of one undifferentiated failure, and the ledger would record nine proofs
//! that were really one. A gate per property makes the discrimination
//! structural, and gives `STATUS.md` a row per property.
//! (`docs/bluedb/P1-STAGE2-PLAN.md`, "Seven gates, not one gate with seven
//! mutations".)
//!
//! The companion half of that argument now exists too:
//! `expect_strings_are_pairwise_discriminating` in `registry.rs` asserts no
//! declared assertion is a substring of another ACROSS THE WHOLE REGISTRY, so
//! two gates can no longer be satisfied by one message.
//!
//! **What the split does NOT license is one mutation per gate.** That reading
//! was encoded here as an `assert_eq!(mutations.len(), 1)`, and it was actively
//! harmful: a gate pins several test leaves, one mutation typically reddens
//! some of them, and an empty Go test function emits `pass` — so every leaf no
//! mutation touches could have its body deleted with the gate staying green
//! **and** reporting `PROVEN`. Twelve leaves across four gates were in exactly
//! that state, and the equality assertion forbade the fix. The requirement is
//! now the right way round: at least one mutation per gate, and at least one
//! mutation per pinned LEAF, recorded in [`LEAF_COVERAGE`] and checked against
//! the RED transcript that justifies it.
//!
//! # The three anti-vacuity assertions
//!
//! Identical in kind to `gates_g2.rs`'s, and for the identical reason —
//! `go test -run 'TestNoSuchThing'` **exits 0**:
//!
//! 1. **The population is pinned as a SET, cross-checked against the Go
//!    source.** [`AUDIT_OWNERSHIP`] records every `func Test…` in the file
//!    together with the gate that runs it, and every gate body reconciles that
//!    table against [`super::gates_g2::go_test_names`]. A deleted or renamed
//!    test is a FAIL; an ADDED one is a FAIL until it is recorded — a count
//!    could not see either.
//! 2. **`-count=1`**, so Go cannot serve `ok (cached)` having run nothing.
//! 3. **`-json` parsed for a passing event per pinned test**, with the passing
//!    set required to EQUAL the pinned set under an anchored `-run` pattern.
//!
//! # The fourth assertion this corpus needs and G2.9a's does not
//!
//! Two of the nine properties live in ONE Go function
//! (`TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes` carries N1's
//! `collNameLen=…` table AND N1b's failed-scan sub-test), so the two gates
//! address SUB-TESTS, not functions — see
//! [`super::gates_g2::run_pattern`]. `go_test_names` cannot see a sub-test:
//! it parses `func Test…` declarations, and these names are `t.Run` arguments,
//! one of them built by `fmt.Sprintf` from a literal `[]int`. So each gate that
//! pins a sub-test also pins the SOURCE CONSTRUCTS that generate it
//! ([`SourceAnchor`]) and the exact number of `t.Run(` sites in the owning
//! function. Without that, a NEW sub-test could appear in a pinned function and
//! be neither run by any gate nor noticed by one — the same hole assertion (1)
//! closes at the function level.
//!
//! G2.13h is the third shape: it `-run`s a whole FUNCTION that happens to carry
//! sub-tests. Its `-run` pattern therefore stays at depth 1, and the sub-test
//! leaves are declared as [`AuditGate::required_subtests`] — evidence the run
//! must produce, not a population the pattern selects. Two things would go
//! wrong without that field. `check_run_evidence` would report the sub-test
//! `pass` events as `-run` leakage (they are descendants of the pinned name,
//! not ancestors); and, worse, a sub-test that silently stopped running would
//! leave its parent green, because a Go parent passes when its remaining body
//! passes. Declaring the leaves makes each one's absence a FAIL.
//!
//! # The mutations
//!
//! Each is the minimal revert of exactly one fix's hunk, and each `expect`
//! string is copied VERBATIM from the failure that revert actually produced —
//! never composed (`P1-STAGE2-PLAN.md` risk 6). Where two properties share a
//! function, the two patches touch different FILES (N1 is `reader.go`'s bound
//! construction; N1b is `txn.go`'s `materializeScan`), so neither triggers the
//! other; that was verified by applying each and observing which sub-tests went
//! red.

use std::collections::BTreeSet;
use std::time::Duration;

use super::gates_g2::{
    check_pinned_population, check_run_evidence, check_source_anchors, enumerate_injections,
    go_test, go_test_names, SourceAnchor,
};
use super::registry::{Ctx, GateOutcome};

/// The corpus under test. One file, nine gates, and one ownership table over
/// it — see [`AUDIT_OWNERSHIP`].
pub const AUDIT_SOURCE: &str = "runtime-go/bluedb/audit_test.go";

/// Where the ownership table lives, quoted into findings so a failure says
/// which pin to update.
const PIN_NAME: &str = "AUDIT_OWNERSHIP (bluedb_gates/gates_g2_13.rs)";

/// The owner of a fixture that is recorded but run by NO gate.
///
/// It is spelled out rather than left implicit because the alternative — an
/// ownership table that silently omits what nothing gates — is a table that
/// reports full coverage of whatever it happens to list. That honesty is what
/// surfaced the hole this vocabulary now describes in the past tense: the four
/// fail-open fixtures (N6 / C6b, commit `f776dd27`) sat here as `UNGATED` while
/// only CI's `go test ./bluedb/...` ran them — invisible to `--verify-mutations`,
/// to `STATUS.md`, and to any goal verdict. **G2.13h** owns them now.
///
/// It was then kept unused on the argument that a fixture can land ahead of its
/// gate again and the table must be able to SAY so in the interval. That
/// happened within the week: three N4/durability fixtures (commit `ad9b3900`)
/// landed alongside the fix they pin and carried this value until **G2.13j** and
/// **G2.13k** gave them one. Deleting the word would have made silence the only
/// available answer both times.
///
/// So it is unused AGAIN, deliberately, and the `allow` is the cost of keeping a
/// vocabulary the table has now needed twice. `every_owner_is_a_registered_gate_
/// or_explicitly_ungated` still reads it, so the two spellings of "nothing runs
/// this" — the value and the test that tolerates it — cannot drift apart.
#[allow(dead_code)]
const UNGATED: &str = "— (recorded, run by no gate)";

/// One `func Test…` in [`AUDIT_SOURCE`], and the gate that runs it.
pub struct Owned {
    pub test: &'static str,
    /// The gate id that puts this test under `-run`, or [`UNGATED`].
    pub owner: &'static str,
    /// The property it pins. Rendered into the owning gate's PASS detail, so
    /// `STATUS.md` says what the corpus actually covers.
    pub property: &'static str,
}

/// THE OWNERSHIP TABLE — every top-level test in [`AUDIT_SOURCE`].
///
/// Two-way checked by every gate body: a row with no declaration is a FAIL (the
/// gate would `-run` a name that matches nothing and STILL EXIT 0), and a
/// declaration with no row is a FAIL (a new fixture that is neither run nor
/// accounted for). That is why the table covers tests the nine gates do NOT
/// own — the two N3 fixtures belong to G2.6 — and why it has a word for a
/// fixture nothing runs ([`UNGATED`]): an ownership table may not be a list of
/// the things it already covers. Reading it that way is what found the four
/// fail-open rows that no gate ran, which is now G2.13h's population.
pub const AUDIT_OWNERSHIP: &[Owned] = &[
    Owned {
        test: "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes",
        // Two owners: G2.13a runs the `collNameLen=…` table, G2.13b runs the
        // N1b sub-test. The `-run` patterns are disjoint at level 1.
        owner: "G2.13a+G2.13b",
        property: "Iterate bounds do not leak across collections (N1); a failed scan is an error (N1b)",
    },
    Owned {
        test: "TestAuditN5CorruptHlcHiRefusesOpenAndNeverReissuesTs",
        owner: "G2.13c",
        property: "a mis-sized hlc_hi refuses to open and never re-issues a commitTs (N5)",
    },
    Owned {
        test: "TestAuditC1CommitOnClosedChannelReturnsError",
        owner: "G2.13d",
        property: "a commit against a closed engine does not ack success (C1)",
    },
    Owned {
        test: "TestAuditH3ReaderGetSurfacesIoErrors",
        owner: "G2.13g",
        property: "a failed point read is an error, not an absent row (H3)",
    },
    Owned {
        test: "TestAuditH1SnapshotReadTsIsPinnedWithItsSnapshot",
        owner: "G2.13e",
        property: "the deterministic arm: readTs is durableHi, chosen where the snapshot is pinned (H1)",
    },
    Owned {
        test: "TestAuditH1SnapshotSeesEveryCommitAtOrBelowItsReadTs",
        owner: "G2.13e",
        property: "the property arm: a reader's pinned view contains every commit at or below its readTs (H1)",
    },
    Owned {
        test: "TestAuditN4CloseDoesNotPanicConcurrentSnapshot",
        owner: "G2.13f",
        property: "a snapshot request racing Close either succeeds or reports ErrClosed — never panics (N4)",
    },
    Owned {
        test: "TestAuditN4CloseWaitsForLiveReaders",
        owner: "G2.13f",
        property: "Close does not complete while a transaction's reader is pinned, and does not deadlock against the release path (N4)",
    },
    Owned {
        test: "TestAuditN4CloseWithLeakedReaderReportsRatherThanHangs",
        owner: "G2.13f",
        property: "a leaked reader makes Close REPORT (ErrReadersLive) and stay retryable, rather than hang or force the handle shut (N4)",
    },
    Owned {
        test: "TestAuditN4BeginPathReaderClosesSnapshotBeforeItsPin",
        owner: "G2.13f",
        property: "pebbleReader.Close closes the pebble snapshot FIRST and releases its watermark token LAST (N4 residual)",
    },
    // -- owned by G2.6, not by the nine: these are the injection fixtures its
    //    manifest enumerates, and it runs them under the same three assertions.
    Owned {
        test: "TestAuditN3BackgroundFatalDoesNotKillTheProcess",
        owner: "G2.6",
        property: "a pebble Fatalf on a background flush goroutine latches instead of killing the process (N3)",
    },
    Owned {
        test: "TestAuditN3SynchronousWalFaultStillErrorsTheAck",
        owner: "G2.6",
        property: "a synchronous WAL fatal is folded into the ack rather than swallowed (N3)",
    },
    // -- the fail-open sweep (N6 / C6b, commit `f776dd27`), owned by G2.13h. --
    Owned {
        test: "TestAuditN6UndecodablePayloadCannotHoleTheValidationWindow",
        owner: "G2.13h",
        property: "an undecodable changelog payload must not let both txns commit (N6)",
    },
    Owned {
        test: "TestAuditC6bBlindPathRingAppendCannotBeHoledEither",
        owner: "G2.13h",
        property: "the blind-path ring append fails closed on an undecodable payload (C6b)",
    },
    Owned {
        test: "TestAuditC6bAdvanceOnAnUnknownTokenIsAnError",
        owner: "G2.13h",
        property: "Advance on an unknown watermark token is an error, not a silent no-op (C6b)",
    },
    Owned {
        test: "TestAuditC6bCorruptColdStartSeedRaisesTheRingFloor",
        owner: "G2.13h",
        property: "a corrupt cold-start seed raises the recent-changes ring floor (C6b)",
    },
    // -- the N4 lifecycle sweep (commit `ad9b3900`), owned by G2.13j. --
    //
    // These landed with their fix and sat here as `UNGATED` for exactly one
    // commit: CI's `go test ./bluedb/...` ran them and nothing else did, so they
    // were invisible to `--verify-mutations`, to `STATUS.md` and to every goal
    // verdict — the same hole reading this table the other way round found for
    // G2.13h's four fail-open rows, one week later.
    Owned {
        test: "TestAuditN4ChangelogAndGCDoNotRaceCloseIntoAPanic",
        owner: "G2.13j",
        property: "a Changelog handed out by the engine, and a GC call, answer ErrClosed across a \
                   Close instead of panicking on the caller's goroutine (N4)",
    },
    Owned {
        test: "TestAuditN4GCPassIsPinnedAgainstAConcurrentClose",
        owner: "G2.13j",
        property: "a GC pass in flight is pinned, so Close waits for it rather than closing the \
                   handle underneath it (N4)",
    },
    // -- the post-ack durability arm (commit `ad9b3900`), owned by G2.13k. --
    Owned {
        test: "TestAuditPostAckDurabilityPanicIsNotSilentlyAbsorbed",
        owner: "G2.13k",
        property: "a durability panic raised AFTER the acks have gone out seals and re-panics on \
                   both commit paths, rather than being absorbed",
    },
    // -- the two Stage-1 remedies that shipped WITHOUT a test, owned by G2.13i. --
    Owned {
        test: "TestAuditS1GcAbortsRatherThanSkippingUnboundedCorruptKeys",
        owner: "G2.13i",
        property: "an unparseable data key is skipped, COUNTED, and past the per-pass bound aborts the pass with ErrCorruptDataKeys (Stage 1)",
    },
    Owned {
        test: "TestAuditS1ChangelogTailFailsClosedOnACorruptKey",
        owner: "G2.13i",
        property: "a malformed changelog key fails the read closed instead of being skipped out of an SSI validation window (Stage 1)",
    },
];

// ---------------------------------------------------------------------------
// The gate descriptor
// ---------------------------------------------------------------------------

// [`SourceAnchor`] used to be declared here. It moved to `gates_g2.rs`, beside
// `enumerate_injections` and `strip_go_comments` — the functions that give it its
// meaning — because G2.9a needs the identical pin and is not an [`AuditGate`].

/// The exact number of `t.Run(` sites a pinned function may contain.
///
/// Zero for the five gates whose fixtures have no sub-tests, and that zero is
/// load-bearing: it is what makes a NEW sub-test in one of those functions a
/// FAIL rather than an unrun, unaccounted addition.
pub struct SubtestSites {
    pub func: &'static str,
    pub sites: usize,
}

const T_RUN: &str = "t.Run(";

struct AuditGate {
    id: &'static str,
    /// The pinned population, as FULL Go test names (`Parent/sub` for a
    /// sub-test). Uniform depth — see [`super::gates_g2::run_pattern`]. This is
    /// what `-run` addresses.
    tests: &'static [&'static str],
    /// Sub-test leaves BELOW a pinned function that must also report a passing
    /// event. Empty for every gate whose pinned functions have no sub-tests,
    /// and for the two that address sub-tests directly through [`Self::tests`].
    ///
    /// G2.13h and G2.13i need it: each `-run`s whole functions, one of which
    /// carries two `t.Run` arms. They are required evidence rather than pattern levels
    /// because a Go parent passes when its remaining body passes — so a
    /// sub-test that quietly stopped running would leave the parent green.
    /// `check_run_evidence` also treats a pinned name's DESCENDANTS as `-run`
    /// leakage unless they are declared, which is the other half of the reason.
    required_subtests: &'static [&'static str],
    anchors: &'static [SourceAnchor],
    sites: &'static [SubtestSites],
    /// `go test`'s share of the gate's budget; the rest covers this body's own
    /// parsing and leaves `capped` room to kill the group and reap.
    budget: Duration,
    /// The property, in the PASS detail's voice.
    property: &'static str,
}

/// The one body all nine share.
fn run_audit_gate(ctx: &Ctx, g: &AuditGate) -> GateOutcome {
    let Some(src) = ctx.read(AUDIT_SOURCE) else {
        return GateOutcome::fail(
            format!("cannot read {AUDIT_SOURCE}"),
            vec![format!(
                "{} certifies a fixture in that file; without it there is nothing to certify",
                g.id
            )],
        );
    };

    // ── (1a) the FILE's population is the recorded population ──
    let declared = go_test_names(&src);
    let recorded: Vec<&str> = AUDIT_OWNERSHIP.iter().map(|o| o.test).collect();
    let mut findings = check_pinned_population(&declared, &recorded, AUDIT_SOURCE, PIN_NAME);

    // The population the RUN must evidence: what `-run` selects, plus the
    // sub-test leaves below it. Identical to `g.tests` for the gates that
    // declare no leaves.
    let expected: Vec<&str> = g
        .tests
        .iter()
        .chain(g.required_subtests.iter())
        .copied()
        .collect();

    // ── (1b) this gate's own pins name real declarations ──
    for t in expected.iter() {
        let top = t.split('/').next().unwrap_or(t);
        if !declared.contains(top) {
            findings.push(format!(
                "{} pins {t}, but {AUDIT_SOURCE} declares no `func {top}` — `go test -run` would \
                 match nothing for it and STILL EXIT 0",
                g.id
            ));
        }
    }

    // ── (1c) the sub-test population, pinned in SOURCE ──
    let bodies = enumerate_injections(&src);
    let body_of = |name: &str| bodies.iter().find(|f| f.test == name).map(|f| &f.body);

    findings.extend(check_source_anchors(&bodies, g.anchors, g.id, AUDIT_SOURCE));
    for s in g.sites {
        match body_of(s.func) {
            None => findings.push(format!(
                "{}: {AUDIT_SOURCE} has no `func {}` to count sub-tests in",
                g.id, s.func
            )),
            Some(body) => {
                let found = body.matches(T_RUN).count();
                if found != s.sites {
                    findings.push(format!(
                        "{}::{} has {found} `{T_RUN}` site(s), the pin records {} — an unrecorded \
                         sub-test is neither run by this gate nor accounted for by it",
                        AUDIT_SOURCE, s.func, s.sites
                    ));
                }
            }
        }
    }

    if !findings.is_empty() {
        return GateOutcome::fail(
            format!(
                "the audit corpus does not match its pinned population ({} `func Test…` declared, \
                 {} recorded in {PIN_NAME})",
                declared.len(),
                recorded.len()
            ),
            findings,
        );
    }

    // ── (2) + (3) run them, cache defeated, per-test evidence required ──
    let run = match go_test(ctx, g.tests, g.budget) {
        Ok(r) => r,
        Err(e) => return GateOutcome::fail(e, vec!["a gate that cannot run has not passed".into()]),
    };

    findings.extend(check_run_evidence(&run, &expected));
    findings.extend(run.failure_log.iter().cloned());

    if findings.is_empty() {
        // The owned rows are rendered, not merely recorded: STATUS.md should say
        // what the corpus covers without anyone reading Rust — the same reason
        // G2.6 renders its injection manifest.
        let covered: Vec<&str> = AUDIT_OWNERSHIP
            .iter()
            .filter(|o| o.owner.split('+').any(|owner| owner == g.id))
            .map(|o| o.property)
            .collect();
        GateOutcome::pass(format!(
            "{}: {} pinned fixture(s) observed passing via `go test -json -count=1` under the \
             anchored pattern `{}` [{}]",
            g.property,
            expected.len(),
            super::gates_g2::run_pattern(g.tests),
            covered.join("; ")
        ))
    } else {
        GateOutcome::fail(
            format!(
                "{} is not proven: {}/{} pinned fixture(s) reported a passing event",
                g.property,
                run.passed.intersection(&pinned_set(&expected)).count(),
                expected.len()
            ),
            findings,
        )
    }
}

fn pinned_set(tests: &[&str]) -> BTreeSet<String> {
    tests.iter().map(|s| s.to_string()).collect()
}

// ---------------------------------------------------------------------------
// G2.13a — N1: Iterate bounds do not leak across collections
// ---------------------------------------------------------------------------

const N1_FUNC: &str = "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes";

/// The collection-name lengths the fixture straddles. Pinned here AND in the Go
/// source (see [`G2_13A_ANCHORS`]): 30 is the cross-collection LEAK regime, 31+
/// the silent-empty-collection regime, and 28/29 the by-luck-correct control.
/// Dropping one from the Go slice would leave its pin here with no passing
/// event, which assertion (3) reports.
pub const G2_13A_TESTS: &[&str] = &[
    "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=28",
    "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=29",
    "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=30",
    "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=31",
    "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=32",
    "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=33",
    "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=34",
    "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=130",
];

const G2_13A_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: N1_FUNC,
        needle: "for _, n := range []int{28, 29, 30, 31, 32, 33, 34, 130}",
        why: "that literal IS the sub-test population; shrinking it shrinks what the gate certifies",
    },
    SourceAnchor {
        func: N1_FUNC,
        needle: "t.Run(fmt.Sprintf(\"collNameLen=%d\", n)",
        why: "that format string IS the sub-test naming, and the `-run` anchor is written against it",
    },
];

/// Exactly two `t.Run(` sites in the N1 function: the `collNameLen` loop
/// (G2.13a) and the N1b sub-test (G2.13b). A third would belong to neither
/// gate.
const N1_SITES: &[SubtestSites] = &[SubtestSites {
    func: N1_FUNC,
    sites: 2,
}];

pub fn g2_13a_iterate_bounds(ctx: &Ctx) -> GateOutcome {
    run_audit_gate(
        ctx,
        &AuditGate {
            id: "G2.13a",
            tests: G2_13A_TESTS,
            required_subtests: &[],
            anchors: G2_13A_ANCHORS,
            sites: N1_SITES,
            budget: Duration::from_secs(240),
            property: "Iterate bounds do not leak rows across collections",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.13b — N1b: a failed scan surfaces an error, not an empty collection
// ---------------------------------------------------------------------------

pub const G2_13B_TESTS: &[&str] = &[
    "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/N1b/failed-scan-surfaces-an-error-not-an-empty-collection",
];

const G2_13B_ANCHORS: &[SourceAnchor] = &[SourceAnchor {
    func: N1_FUNC,
    needle: "t.Run(\"N1b/failed-scan-surfaces-an-error-not-an-empty-collection\"",
    why: "that literal IS the sub-test name the `-run` anchor addresses; renaming it makes the anchor match nothing",
}];

pub fn g2_13b_failed_scan_is_an_error(ctx: &Ctx) -> GateOutcome {
    run_audit_gate(
        ctx,
        &AuditGate {
            id: "G2.13b",
            tests: G2_13B_TESTS,
            required_subtests: &[],
            anchors: G2_13B_ANCHORS,
            sites: N1_SITES,
            budget: Duration::from_secs(240),
            property: "a failed scan surfaces an error, not an empty collection",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.13c — N5: a mis-sized hlc_hi refuses to open
// ---------------------------------------------------------------------------

pub const G2_13C_TESTS: &[&str] = &["TestAuditN5CorruptHlcHiRefusesOpenAndNeverReissuesTs"];

pub fn g2_13c_corrupt_hlc_hi(ctx: &Ctx) -> GateOutcome {
    run_audit_gate(
        ctx,
        &AuditGate {
            id: "G2.13c",
            tests: G2_13C_TESTS,
            required_subtests: &[],
            anchors: &[],
            sites: &[SubtestSites {
                func: "TestAuditN5CorruptHlcHiRefusesOpenAndNeverReissuesTs",
                sites: 0,
            }],
            budget: Duration::from_secs(240),
            property: "a mis-sized hlc_hi refuses to open and never re-issues a commitTs",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.13d — C1: a commit against a closed engine does not ack success
// ---------------------------------------------------------------------------

pub const G2_13D_TESTS: &[&str] = &["TestAuditC1CommitOnClosedChannelReturnsError"];

pub fn g2_13d_no_false_ack(ctx: &Ctx) -> GateOutcome {
    run_audit_gate(
        ctx,
        &AuditGate {
            id: "G2.13d",
            tests: G2_13D_TESTS,
            required_subtests: &[],
            anchors: &[],
            sites: &[SubtestSites {
                func: "TestAuditC1CommitOnClosedChannelReturnsError",
                sites: 0,
            }],
            budget: Duration::from_secs(240),
            property: "a commit against a closed engine does not ack success",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.13e — H1: a Snapshot's readTs is pinned with its snapshot
// ---------------------------------------------------------------------------

/// BOTH arms, and the pairing is deliberate. The first is deterministic — it
/// drives `hlc.next()` directly, which is exactly the state a committer leaves
/// between timestamp assignment and `Apply`. The second hammers `Snapshot()`
/// against a live committer and is racy by nature, so it SUPPORTS the proof
/// rather than carrying it. Pinning only the racy one would be a gate that can
/// go green by losing a race.
pub const G2_13E_TESTS: &[&str] = &[
    "TestAuditH1SnapshotReadTsIsPinnedWithItsSnapshot",
    "TestAuditH1SnapshotSeesEveryCommitAtOrBelowItsReadTs",
];

pub fn g2_13e_readts_pinned_with_snapshot(ctx: &Ctx) -> GateOutcome {
    run_audit_gate(
        ctx,
        &AuditGate {
            id: "G2.13e",
            tests: G2_13E_TESTS,
            required_subtests: &[],
            anchors: &[],
            sites: &[
                SubtestSites {
                    func: "TestAuditH1SnapshotReadTsIsPinnedWithItsSnapshot",
                    sites: 0,
                },
                SubtestSites {
                    func: "TestAuditH1SnapshotSeesEveryCommitAtOrBelowItsReadTs",
                    sites: 0,
                },
            ],
            budget: Duration::from_secs(240),
            property: "a Snapshot's readTs is pinned with its snapshot",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.13f — N4: Close quiesces readers instead of racing them
// ---------------------------------------------------------------------------

/// All four arms. The drain arms carry REAL timeouts — `closeWithin(20s)` with
/// a 20 s guard on the far side, and a 15 s guard on the leaked-reader arm — so
/// this gate is `Tier::Full` and its budget is sized for the failing case, not
/// the passing one (which is ~2 s).
pub const G2_13F_TESTS: &[&str] = &[
    "TestAuditN4CloseDoesNotPanicConcurrentSnapshot",
    "TestAuditN4CloseWaitsForLiveReaders",
    "TestAuditN4CloseWithLeakedReaderReportsRatherThanHangs",
    "TestAuditN4BeginPathReaderClosesSnapshotBeforeItsPin",
];

pub fn g2_13f_close_quiesces_readers(ctx: &Ctx) -> GateOutcome {
    run_audit_gate(
        ctx,
        &AuditGate {
            id: "G2.13f",
            tests: G2_13F_TESTS,
            required_subtests: &[],
            anchors: &[],
            sites: &[
                SubtestSites {
                    func: "TestAuditN4CloseDoesNotPanicConcurrentSnapshot",
                    sites: 0,
                },
                SubtestSites {
                    func: "TestAuditN4CloseWaitsForLiveReaders",
                    sites: 0,
                },
                SubtestSites {
                    func: "TestAuditN4CloseWithLeakedReaderReportsRatherThanHangs",
                    sites: 0,
                },
                SubtestSites {
                    func: "TestAuditN4BeginPathReaderClosesSnapshotBeforeItsPin",
                    sites: 0,
                },
            ],
            budget: Duration::from_secs(540),
            property: "Close quiesces readers instead of racing them",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.13g — H3: a failed point read is an error, not an absent row
// ---------------------------------------------------------------------------

/// Also enumerated by G2.6's injection manifest, and the overlap is the point:
/// G2.6 asks whether the fault-injection CORPUS is complete and armed, this
/// gate asks whether the reader's error handling is correct. They fail for
/// different reasons and carry different assertions, so neither substitutes for
/// the other.
pub const G2_13G_TESTS: &[&str] = &["TestAuditH3ReaderGetSurfacesIoErrors"];

pub fn g2_13g_failed_read_is_an_error(ctx: &Ctx) -> GateOutcome {
    run_audit_gate(
        ctx,
        &AuditGate {
            id: "G2.13g",
            tests: G2_13G_TESTS,
            required_subtests: &[],
            anchors: &[],
            sites: &[SubtestSites {
                func: "TestAuditH3ReaderGetSurfacesIoErrors",
                sites: 0,
            }],
            budget: Duration::from_secs(240),
            property: "a failed point read is an error, not an absent row",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.13h — N6 + C6b: the commit/validation route fails closed
// ---------------------------------------------------------------------------

const N6_FUNC: &str = "TestAuditN6UndecodablePayloadCannotHoleTheValidationWindow";

/// The fail-open sweep, as ONE property: **an error on the commit/validation
/// route fails the operation CLOSED**, rather than returning a plausible zero
/// value that a later transaction then validates against.
///
/// Four fixtures, four doors into the same failure. N6 is the `pending` half of
/// the SSI window (a job that commits contributing nothing to it); C6b's
/// blind-path arm is the recent-changes ring (a durable commit the ring never
/// learns of); C6b's `Advance` arm is the watermark registry (a nil return that
/// pins no GC floor); C6b's cold-start arm is the ring's seed (a floor that
/// claims a range the ring does not hold). Each returns a plausible zero — nil,
/// no changes, "nothing to do" — where an error was the fact, and each ends in
/// UNDER-rejection: a transaction validating against a window that is missing a
/// committed change. That is a serializability break, not a lost optimisation.
///
/// They are one gate because they are one property, and the argument that
/// splits G2.13a–g does not apply: those seven are seven DIFFERENT properties
/// that a single defect could have reddened together, so each needed its own
/// discriminating assertion. Here a defect on any of the four doors is the same
/// statement about the same route.
///
/// The gate is the reason the class stopped being invisible. These four sat in
/// [`AUDIT_OWNERSHIP`] as `UNGATED` — run only by CI's `go test ./bluedb/...`,
/// and therefore absent from `--verify-mutations`, from `STATUS.md`, and from
/// every goal verdict — while the class they guard is the one that produced N6
/// *and* a second instance in the same file, which is why the sweep exists at
/// all.
pub const G2_13H_TESTS: &[&str] = &[
    N6_FUNC,
    "TestAuditC6bBlindPathRingAppendCannotBeHoledEither",
    "TestAuditC6bAdvanceOnAnUnknownTokenIsAnError",
    "TestAuditC6bCorruptColdStartSeedRaisesTheRingFloor",
];

/// N6's two arms, required as EVIDENCE (see [`AuditGate::required_subtests`]).
///
/// The control arm is not decoration and is pinned as hard as the main one: it
/// proves `pending` + `validate()` really do catch this conflict for a
/// WELL-FORMED payload, which is what makes "both committed" in the main arm
/// mean *the payload was missing from the window* rather than *validation is
/// broken generally*. A run in which the control silently stopped executing
/// would leave the main arm asserting nothing in particular.
pub const G2_13H_SUBTESTS: &[&str] = &[
    "TestAuditN6UndecodablePayloadCannotHoleTheValidationWindow/control/a-well-formed-payload-makes-the-later-txn-conflict",
    "TestAuditN6UndecodablePayloadCannotHoleTheValidationWindow/N6/an-undecodable-payload-must-not-let-both-commit",
];

/// The `t.Run` literals that generate [`G2_13H_SUBTESTS`]. `go_test_names`
/// cannot see a sub-test, so a rename would otherwise turn a required leaf into
/// a name that never appears — and "did not report a passing event" would then
/// read as a real failure of a property nobody had actually stopped running.
const G2_13H_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: N6_FUNC,
        needle: "t.Run(\"control/a-well-formed-payload-makes-the-later-txn-conflict\"",
        why: "that literal IS the control arm's name, and the control is what makes the main arm's \
              `both committed` mean the window was holed rather than validation being broken",
    },
    SourceAnchor {
        func: N6_FUNC,
        needle: "t.Run(\"N6/an-undecodable-payload-must-not-let-both-commit\"",
        why: "that literal IS the main arm's name, and the arm carries the assertion the mutation \
              is classified on",
    },
];

/// Two `t.Run(` sites in the N6 function — the control arm and the main arm —
/// and zero in each C6b fixture. Both numbers are load-bearing: a third arm in
/// N6 would be required evidence nobody declared, and a first arm in a C6b
/// fixture would run UNFILTERED under a depth-1 `-run` while contributing no
/// leaf this gate names.
const G2_13H_SITES: &[SubtestSites] = &[
    SubtestSites {
        func: N6_FUNC,
        sites: 2,
    },
    SubtestSites {
        func: "TestAuditC6bBlindPathRingAppendCannotBeHoledEither",
        sites: 0,
    },
    SubtestSites {
        func: "TestAuditC6bAdvanceOnAnUnknownTokenIsAnError",
        sites: 0,
    },
    SubtestSites {
        func: "TestAuditC6bCorruptColdStartSeedRaisesTheRingFloor",
        sites: 0,
    },
];

pub fn g2_13h_commit_route_fails_closed(ctx: &Ctx) -> GateOutcome {
    run_audit_gate(
        ctx,
        &AuditGate {
            id: "G2.13h",
            tests: G2_13H_TESTS,
            required_subtests: G2_13H_SUBTESTS,
            anchors: G2_13H_ANCHORS,
            sites: G2_13H_SITES,
            // Measured: 0.39s wall for all four with a warm build cache. The
            // budget is the file's convention for a fixture set with no timed
            // arm, not an estimate of this one.
            budget: Duration::from_secs(240),
            property: "the commit/validation route fails closed",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.13i — the two Stage-1 remedies that shipped with no test at all
// ---------------------------------------------------------------------------

const S1_GC_FUNC: &str = "TestAuditS1GcAbortsRatherThanSkippingUnboundedCorruptKeys";

/// **Corrupt keys fail the operation CLOSED.**
///
/// Two remedies landed in Stage 1 with no regression behind either of them, and
/// an audit found both. They are one property from two directions:
///
/// * `gc.go`'s pass may SKIP a data key `decodeDataVersion` cannot parse — it is
///   the only evidence of the fault, so deleting it would destroy the evidence —
///   but it must COUNT the skip (`GCStats.CorruptKeys`) and ABORT the pass with
///   `ErrCorruptDataKeys` past `maxCorruptKeysPerPass`. An uncounted skip is an
///   invisible, permanent leak; an unbounded one turns a damaged keyspace into a
///   silent no-op that every future pass repeats.
/// * `changelog.go`'s `Tail` must fail closed with `errCorruptChangelogKey`
///   rather than `continue` past a malformed key. `P1-STAGE2-PLAN.md` ranks that
///   `continue` — "by mechanical analogy with `gc.go`" — risk **#5**, and names
///   its consequence exactly: a committed change silently missing from a
///   transaction's SSI validation window, i.e. under-rejection, i.e. a
///   serializability break.
///
/// The pairing is deliberate and is why this is one gate: the two files invite
/// the SAME wrong move (skip and carry on), and only one of them can afford it.
pub const G2_13I_TESTS: &[&str] = &[
    S1_GC_FUNC,
    "TestAuditS1ChangelogTailFailsClosedOnACorruptKey",
];

/// The GC fixture's two arms, required as EVIDENCE (see
/// [`AuditGate::required_subtests`]) — the `-run` pattern stays at depth 1 and a
/// Go parent passes when its remaining body passes, so an arm that quietly
/// stopped running would leave the parent green. The two are not
/// interchangeable: the first proves a bounded number of corrupt keys is skipped
/// AND counted AND the pass still completes; the second proves the pass STOPS at
/// the bound and applies nothing.
pub const G2_13I_SUBTESTS: &[&str] = &[
    "TestAuditS1GcAbortsRatherThanSkippingUnboundedCorruptKeys/a-few-are-skipped-counted-and-the-pass-still-completes",
    "TestAuditS1GcAbortsRatherThanSkippingUnboundedCorruptKeys/past-the-per-pass-bound-the-pass-aborts-and-deletes-nothing",
];

const G2_13I_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: S1_GC_FUNC,
        needle: "t.Run(\"a-few-are-skipped-counted-and-the-pass-still-completes\"",
        why: "that literal IS the counted-skip arm's name; without it the gate would require a leaf \
              nothing generates",
    },
    SourceAnchor {
        func: S1_GC_FUNC,
        needle: "t.Run(\"past-the-per-pass-bound-the-pass-aborts-and-deletes-nothing\"",
        why: "that literal IS the abort arm's name, and the abort is the half `gc.go`'s bound exists \
              for",
    },
];

const G2_13I_SITES: &[SubtestSites] = &[
    SubtestSites {
        func: S1_GC_FUNC,
        sites: 2,
    },
    SubtestSites {
        func: "TestAuditS1ChangelogTailFailsClosedOnACorruptKey",
        sites: 0,
    },
];

pub fn g2_13i_corrupt_keys_fail_closed(ctx: &Ctx) -> GateOutcome {
    run_audit_gate(
        ctx,
        &AuditGate {
            id: "G2.13i",
            tests: G2_13I_TESTS,
            required_subtests: G2_13I_SUBTESTS,
            anchors: G2_13I_ANCHORS,
            sites: G2_13I_SITES,
            // The abort arm plants `maxCorruptKeysPerPass + 176` keys through a
            // raw pebble handle; measured at ~0.6s for both fixtures.
            budget: Duration::from_secs(240),
            property: "corrupt keys fail the operation closed rather than being skipped",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.13j — the exported non-reader surface is pinned against Close
// ---------------------------------------------------------------------------

const N4_LIFECYCLE_FUNC: &str = "TestAuditN4ChangelogAndGCDoNotRaceCloseIntoAPanic";
const N4_GC_PIN_FUNC: &str = "TestAuditN4GCPassIsPinnedAgainstAConcurrentClose";

/// **Every exported operation that touches the Pebble handle is pinned against
/// Close, so a concurrent Close answers it rather than racing it.**
///
/// G2.13f states this for the READER path (`beginSnapshot` / `snapshotAtChecked`
/// take the check-and-pin, and Close's drain waits on the tokens). These two
/// fixtures state it for the paths that hold no reader at all: `Engine.Changelog()`
/// hands back a value the caller may keep across a Close, and `Engine.GC()` runs a
/// whole multi-phase pass on the CALLER's goroutine. Both were shipped with the
/// wrong shape — a raw `*pebble.DB` with no lifecycle, and `if e.isClosed()`, a
/// check with NO pin — and pebble does not degrade on a closed handle, it panics
/// unconditionally, on that goroutine, where no recover in the package can reach
/// it.
///
/// **Two fixtures because the two defects need different shapes, and that is the
/// finding, not an accident.** `isClosed()` DOES answer a call made after Close
/// returned, so the spin-workers-then-Close shape passes against the broken GC
/// code (verified by mutation: `G2.13j/gc-checks-closed-without-pinning` leaves
/// `…DoNotRaceCloseIntoAPanic` GREEN). The GC arm therefore puts Close INSIDE a
/// pass whose length it measures first. One fixture could not have covered both.
pub const G2_13J_TESTS: &[&str] = &[N4_LIFECYCLE_FUNC, N4_GC_PIN_FUNC];

/// One anchor per fixture, on its own property assertion — see
/// [`super::gates_g2::SourceAnchor`]. Both fixtures END in a `t.Fatalf`/`t.Errorf`
/// that names the defect; delete it (or the body around it) and this gate goes
/// red, which is what stops an emptied fixture reporting `pass`.
const G2_13J_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: N4_LIFECYCLE_FUNC,
        needle: "A pebble handle operation on a closed DB panics unconditionally, on the CALLER's",
        why: "that IS the panic verdict this fixture exists to raise — the workers keep calling \
              AFTER Close has returned, so on the broken code the panic is certain, not a race to win",
    },
    SourceAnchor {
        func: N4_LIFECYCLE_FUNC,
        needle: "on a fully closed engine returned NO error; a terminal engine must ",
        why: "that IS the fail-OPEN half: a post-close Tail answering (nil, nil) looks like an \
              empty changelog to a caller, which is the same defect wearing a different face",
    },
    SourceAnchor {
        func: N4_GC_PIN_FUNC,
        needle: "Close PANICKED with an unpinned GC pass in flight:",
        why: "that IS the TOCTOU verdict, seen from Close's side (pebble unrefs the file cache and \
              panics on a live iterator from the racing pass)",
    },
    SourceAnchor {
        func: N4_GC_PIN_FUNC,
        needle: "The gate needs a pass ",
        why: "that IS the fixture's own width guard — it FAILS rather than silently passing when a \
              GC pass is too quick for Close to land inside it, which is the only thing that makes \
              the assertion above meaningful",
    },
];

const G2_13J_SITES: &[SubtestSites] = &[
    SubtestSites {
        func: N4_LIFECYCLE_FUNC,
        sites: 0,
    },
    SubtestSites {
        func: N4_GC_PIN_FUNC,
        sites: 0,
    },
];

pub fn g2_13j_lifecycle_pins_the_exported_surface(ctx: &Ctx) -> GateOutcome {
    run_audit_gate(
        ctx,
        &AuditGate {
            id: "G2.13j",
            tests: G2_13J_TESTS,
            required_subtests: &[],
            anchors: G2_13J_ANCHORS,
            sites: G2_13J_SITES,
            // Both arms carry REAL waits sized for the failing case: a 30s
            // per-worker report deadline and a 5s `closeWithin` on the first, a
            // 60s `closeWithin` and a 90s pass deadline on the second. Measured
            // at 0.8s passing; the budget is for the hang.
            budget: Duration::from_secs(420),
            property: "the exported non-reader surface is pinned against Close",
        },
    )
}

// ---------------------------------------------------------------------------
// G2.13k — a post-ack durability panic is never absorbed
// ---------------------------------------------------------------------------

const POST_ACK_FUNC: &str = "TestAuditPostAckDurabilityPanicIsNotSilentlyAbsorbed";

/// **A durability panic raised after the acks have gone out seals the engine and
/// RE-PANICS — it is never absorbed.**
///
/// A separate gate from G2.13j, and separate for the reason the module doc gives:
/// these are two properties, not two doors onto one. G2.13j is about a handle's
/// lifecycle; this is about what the single writer goroutine does with a fault
/// nobody is left to be told about. No single defect breaks both, and `STATUS.md`
/// should carry a row for each.
///
/// `processBlindPhase1`'s guard read `if r := recover(); r != nil && !acked`.
/// `recover()` is not conditional on the `&&` — it runs first and CONSUMES the
/// panic — so once the acks had gone out the fault was swallowed whole: no seal,
/// no repanic, no log, and the writer goroutine returned to its range loop from an
/// unexplained fault. `processTxn` carried the same shape expressed against its
/// `acked` set. Both arms are pinned, and they are pinned SEPARATELY (see
/// [`G2_13K_SUBTESTS`]) because each is reverted by its own hunk.
pub const G2_13K_TESTS: &[&str] = &[POST_ACK_FUNC];

/// The two commit paths, required as EVIDENCE (see [`AuditGate::required_subtests`]).
///
/// The `-run` pattern stays at depth 1 and a Go parent passes when its remaining
/// body passes, so an arm that quietly stopped running would leave the parent
/// green. They are not interchangeable: the blind path's guard is a `bool` flag,
/// the transactional path's is "is the acked SET already full", and each mutation
/// below reverts exactly one of them — verified by observing that each leaves the
/// OTHER arm passing.
pub const G2_13K_SUBTESTS: &[&str] = &[
    "TestAuditPostAckDurabilityPanicIsNotSilentlyAbsorbed/blind-path",
    "TestAuditPostAckDurabilityPanicIsNotSilentlyAbsorbed/txn-path",
];

const G2_13K_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: POST_ACK_FUNC,
        needle: "t.Run(\"blind-path\"",
        why: "that literal IS the blind arm's name; without it the gate would require a leaf \
              nothing generates",
    },
    SourceAnchor {
        func: POST_ACK_FUNC,
        needle: "t.Run(\"txn-path\"",
        why: "that literal IS the transactional arm's name",
    },
    SourceAnchor {
        func: POST_ACK_FUNC,
        needle: "a panic raised AFTER processBlindPhase1's acks went out was SILENTLY ABSORBED.",
        why: "that IS the blind arm's assertion, and the mutation is classified on it",
    },
    SourceAnchor {
        func: POST_ACK_FUNC,
        needle: "a panic raised AFTER processTxn's acks went out was SILENTLY ABSORBED.",
        why: "that IS the transactional arm's assertion",
    },
    SourceAnchor {
        func: POST_ACK_FUNC,
        needle: "must not retroactively fail a commit that was applied and acked",
        why: "that IS the half neither mutation reddens: a fault after the ack must not rewrite \
              the verdict of a commit that was already durable. An arm that only checked the \
              panic escaped would pass a `seal + repanic` that also corrupted the results",
    },
];

/// Exactly two `t.Run(` sites: the two commit paths. A third would be required
/// evidence nobody declared.
const G2_13K_SITES: &[SubtestSites] = &[SubtestSites {
    func: POST_ACK_FUNC,
    sites: 2,
}];

pub fn g2_13k_post_ack_panic_is_never_absorbed(ctx: &Ctx) -> GateOutcome {
    run_audit_gate(
        ctx,
        &AuditGate {
            id: "G2.13k",
            tests: G2_13K_TESTS,
            required_subtests: G2_13K_SUBTESTS,
            anchors: G2_13K_ANCHORS,
            sites: G2_13K_SITES,
            // Measured at 0.15s wall — the fault is injected through a seam and
            // neither arm waits on anything. The budget is the file's convention.
            budget: Duration::from_secs(240),
            property: "a post-ack durability panic is never absorbed",
        },
    )
}

// ---------------------------------------------------------------------------
// Leaf coverage — which mutation reddens which pinned leaf
// ---------------------------------------------------------------------------

/// One mutation, and the pinned leaves its **recorded RED transcript** shows it
/// turning red.
///
/// # Why this table exists
///
/// The three anti-vacuity assertions prove a pinned leaf RAN. Nothing proved its
/// body ASSERTS anything — and an empty Go test function emits `pass`. So a leaf
/// that no mutation reddens can be gutted with the gate staying green, and the
/// gate still reports `PROVEN`, because the proof is a statement about whichever
/// leaf the one recorded mutation happened to touch. Twelve leaves across four
/// gates were in that state when this table was written.
///
/// # Why it is evidence rather than a claim
///
/// Every row is checked against the artefact `--verify-mutations` writes: the
/// mutated gate's verbatim output at `<patch>.expected.txt`, in which a Go
/// failure appears as `--- FAIL: <full test name>`. See
/// `every_pinned_leaf_is_reddened_by_a_recorded_mutation`. A row cannot be
/// written ahead of the run that justifies it.
#[allow(dead_code)]
pub struct LeafCoverage {
    /// The registered mutation id.
    pub mutation: &'static str,
    /// Pinned leaves of that mutation's gate which its RED run turned red.
    /// Never a superset of the gate's pinned leaves, and never empty.
    pub leaves: &'static [&'static str],
}

#[allow(dead_code)] // read by `every_pinned_leaf_is_reddened_by_a_recorded_mutation`
pub const LEAF_COVERAGE: &[LeafCoverage] = &[
    // -- G2.13a: the bound-construction revert reddens the leak regime (30) and
    //    the inverted regime (31+). Lengths 28/29 are correct BY LUCK of the
    //    length, so no revert of that fix can redden them; the degenerate-upper
    //    mutation below is what covers the two controls.
    LeafCoverage {
        mutation: "G2.13a/iterate-bounds-end-in-a-user-byte",
        leaves: &[
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=30",
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=31",
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=32",
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=33",
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=34",
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=130",
        ],
    },
    LeafCoverage {
        mutation: "G2.13a/degenerate-upper-bound",
        leaves: &[
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=28",
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=29",
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=30",
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=31",
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=32",
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=33",
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=34",
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/collNameLen=130",
        ],
    },
    LeafCoverage {
        mutation: "G2.13b/failed-scan-reads-as-an-empty-collection",
        leaves: &[
            "TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes/N1b/failed-scan-surfaces-an-error-not-an-empty-collection",
        ],
    },
    LeafCoverage {
        mutation: "G2.13c/corrupt-hlc-hi-reads-as-a-fresh-store",
        leaves: &["TestAuditN5CorruptHlcHiRefusesOpenAndNeverReissuesTs"],
    },
    LeafCoverage {
        mutation: "G2.13d/commit-on-a-closed-engine-acks-success",
        leaves: &["TestAuditC1CommitOnClosedChannelReturnsError"],
    },
    LeafCoverage {
        mutation: "G2.13e/snapshot-readts-is-the-in-memory-high-water",
        leaves: &["TestAuditH1SnapshotReadTsIsPinnedWithItsSnapshot"],
    },
    LeafCoverage {
        mutation: "G2.13f/close-does-not-quiesce-readers",
        leaves: &[
            "TestAuditN4CloseWaitsForLiveReaders",
            "TestAuditN4CloseWithLeakedReaderReportsRatherThanHangs",
        ],
    },
    LeafCoverage {
        mutation: "G2.13e/mvcc-visibility-excludes-the-readts-itself",
        leaves: &["TestAuditH1SnapshotSeesEveryCommitAtOrBelowItsReadTs"],
    },
    LeafCoverage {
        mutation: "G2.13f/closed-engine-reads-as-an-empty-store",
        leaves: &["TestAuditN4CloseDoesNotPanicConcurrentSnapshot"],
    },
    LeafCoverage {
        mutation: "G2.13f/token-released-before-the-snapshot",
        leaves: &["TestAuditN4BeginPathReaderClosesSnapshotBeforeItsPin"],
    },
    LeafCoverage {
        mutation: "G2.13g/failed-point-read-reads-as-an-absent-row",
        leaves: &["TestAuditH3ReaderGetSurfacesIoErrors"],
    },
    LeafCoverage {
        mutation: "G2.13h/undecodable-payload-validates-as-no-changes",
        leaves: &[
            "TestAuditN6UndecodablePayloadCannotHoleTheValidationWindow",
            "TestAuditN6UndecodablePayloadCannotHoleTheValidationWindow/N6/an-undecodable-payload-must-not-let-both-commit",
            "TestAuditC6bBlindPathRingAppendCannotBeHoledEither",
        ],
    },
    LeafCoverage {
        mutation: "G2.13h/pending-window-does-not-see-the-batch",
        leaves: &[
            "TestAuditN6UndecodablePayloadCannotHoleTheValidationWindow/control/a-well-formed-payload-makes-the-later-txn-conflict",
        ],
    },
    LeafCoverage {
        mutation: "G2.13h/advance-on-an-unknown-token-returns-nil",
        leaves: &["TestAuditC6bAdvanceOnAnUnknownTokenIsAnError"],
    },
    LeafCoverage {
        mutation: "G2.13h/corrupt-cold-start-seed-leaves-the-floor-low",
        leaves: &["TestAuditC6bCorruptColdStartSeedRaisesTheRingFloor"],
    },
    // -- G2.13i: two files, two doors, one mutation each.
    LeafCoverage {
        mutation: "G2.13i/gc-skips-corrupt-keys-without-bound",
        leaves: &[
            "TestAuditS1GcAbortsRatherThanSkippingUnboundedCorruptKeys",
            "TestAuditS1GcAbortsRatherThanSkippingUnboundedCorruptKeys/a-few-are-skipped-counted-and-the-pass-still-completes",
            "TestAuditS1GcAbortsRatherThanSkippingUnboundedCorruptKeys/past-the-per-pass-bound-the-pass-aborts-and-deletes-nothing",
        ],
    },
    LeafCoverage {
        mutation: "G2.13i/changelog-skips-a-corrupt-key",
        leaves: &["TestAuditS1ChangelogTailFailsClosedOnACorruptKey"],
    },
    // -- G2.13j: two defects, two shapes, one leaf each. The GC row is the
    //    interesting one — `G2.13j/gc-checks-closed-without-pinning` leaves the
    //    OTHER fixture green (its `isClosed()` does answer a call made after
    //    Close returned), which is exactly why the two fixtures exist.
    LeafCoverage {
        mutation: "G2.13j/changelog-handed-out-without-a-pin",
        leaves: &["TestAuditN4ChangelogAndGCDoNotRaceCloseIntoAPanic"],
    },
    LeafCoverage {
        mutation: "G2.13j/gc-checks-closed-without-pinning",
        leaves: &["TestAuditN4GCPassIsPinnedAgainstAConcurrentClose"],
    },
    // -- G2.13k: one function, two arms, one hunk each. Each mutation reddens
    //    its own arm and leaves the other PASSING (observed), so the parent's
    //    red is attributable.
    LeafCoverage {
        mutation: "G2.13k/post-ack-panic-absorbed-on-the-blind-path",
        leaves: &[
            "TestAuditPostAckDurabilityPanicIsNotSilentlyAbsorbed",
            "TestAuditPostAckDurabilityPanicIsNotSilentlyAbsorbed/blind-path",
        ],
    },
    LeafCoverage {
        mutation: "G2.13k/post-ack-panic-absorbed-on-the-txn-path",
        leaves: &[
            "TestAuditPostAckDurabilityPanicIsNotSilentlyAbsorbed",
            "TestAuditPostAckDurabilityPanicIsNotSilentlyAbsorbed/txn-path",
        ],
    },
    // -- G2.9a, brought under this rule ------------------------------------
    //
    // The `NoSync` revert turns all seven red, and the recorded transcript says
    // so — but read the transcript rather than the count. For THREE of the seven
    // the line it reddened is the fixture's own PRECONDITION guard, not its
    // property:
    //
    //   * `TestSealContractRefusesWrites`  → "the WAL-fsync injector fired ZERO
    //     times … this test proves NOTHING about the seal contract"
    //   * `TestInjectedFaultsReopenConsistent` → the same guard
    //   * `TestCrashDurablePrefixNoReorder` → "not one of the 322 acked commits
    //     survived … the prefix property below was never exercised"
    //
    // A leaf whose only falsifier stops it before it tests anything is falsified
    // in name only, so two of the three get a mutation that reaches the property
    // itself. The third does not, and that is recorded rather than papered over —
    // see the note below the last row.
    LeafCoverage {
        mutation: "G2.9a/ack-before-fsync",
        leaves: &[
            "TestCrashAckedWritesSurvive",
            "TestCrashConcurrentNoAckedLoss",
            "TestCrashDurablePrefixNoReorder",
            "TestCrashHLCNoReissue",
            "TestCrashNoTornBatch",
            "TestInjectedFaultsReopenConsistent",
            "TestSealContractRefusesWrites",
        ],
    },
    // The seal contract's OWN assertion: the engine seals (the fixture's earlier
    // arms still pass) and then a write path is asked to refuse. GC is that write
    // path — "once sealed every write path refuses loudly" includes the one that
    // deletes — and reverting its `sealed` check reddens this fixture and nothing
    // else in the corpus.
    LeafCoverage {
        mutation: "G2.9a/sealed-engine-still-runs-gc",
        leaves: &["TestSealContractRefusesWrites"],
    },
    // Arm (a)'s own assertion. Deleting N3 consumption point 3/5 leaves the
    // injector firing (so the precondition guard passes) while a latched WAL
    // fatal never reaches the ack — the commit acks nil and its write is absent
    // after reopen. `acked ⇒ durable`, falsified at the point it is claimed.
    LeafCoverage {
        mutation: "G2.9a/wal-fatal-never-reaches-the-ack",
        leaves: &["TestInjectedFaultsReopenConsistent"],
    },
    // NO ROW REACHES `TestCrashDurablePrefixNoReorder`'s prefix assertion, and it
    // is not for want of trying. The property — what survives a crash is a PREFIX
    // of the commit history in commitTs order — is a consequence of two facts that
    // no fix hunk in this package owns: Pebble writes one WAL, and the committer is
    // a single goroutine that assigns commitTs and Applies in the same order. A
    // HOLE requires commitTs order to differ from WAL order, so the only lever is
    // moving timestamp assignment out of the committer into the caller
    // (`committer.go`'s doc names that choice as the serialization point C1 relies
    // on) — a redesign, not a revert, and one whose inversion is scheduler-dependent
    // even then. A mutation that only sometimes fires records VACUOUS the times it
    // does not, which this file already refuses (see G2.13f's two deterministic
    // mutations).
    //
    // Its falsifier is therefore the SOURCE-SIDE one — `G2_9A_ANCHORS` pins the
    // `durable prefix has a HOLE` assertion itself, so gutting the fixture, or
    // deleting the assertion out of it, turns G2.9a red on the next run. See
    // [`SOURCE_SIDE_FALSIFIERS`].
];

/// Mutations that redden their gate **before it runs a single test**, and
/// therefore carry no per-leaf evidence by construction.
///
/// [`LEAF_COVERAGE`] reads `--- FAIL: <leaf>` lines out of a recorded transcript.
/// A gate that fails its SOURCE-side reconciliation returns before `go test` is
/// invoked, so its transcript has no such line to read — not because the proof is
/// weak, but because the defect it reintroduces is not a defect in any fixture.
/// `G2.6/disable-injection-point` deletes an `errorfs` injector; G2.6 answers
/// "fewer injection sites than the recorded manifest" and stops.
///
/// This is a marker, not an excuse, and it is checked in the direction that
/// matters: `a_structural_mutation_really_produced_no_per_test_failure` asserts the
/// transcript contains NO `--- FAIL:` line at all. A mutation that DID redden a
/// fixture cannot be filed here to avoid recording which one.
#[allow(dead_code)] // read by `every_pinned_leaf_is_reddened_by_a_recorded_mutation`
pub const STRUCTURAL_MUTATIONS: &[(&str, &str)] = &[(
    "G2.6/disable-injection-point",
    "deleting an injection site fails G2.6's manifest reconciliation, which returns before \
     `go test` runs — the recorded transcript is the manifest finding, with no per-test events \
     in it",
)];

// ---------------------------------------------------------------------------
// Source-side falsifiers — the leaves whose falsifier is the gate's own pin
// ---------------------------------------------------------------------------

/// A pinned leaf whose falsifier is a **source pin the gate enforces on every
/// run**, rather than a recorded mutation transcript.
///
/// # Why this is the same rule, not a weaker one
///
/// The rule [`LEAF_COVERAGE`] exists to enforce is: *an emptied fixture body must
/// make something go RED*. An empty Go test function emits `pass`, so a leaf no
/// falsifier reaches can be gutted with its gate green and its proof still
/// `PROVEN`. A recorded mutation is one way to satisfy that. A source anchor on
/// the leaf's own property assertion is another, and it is strictly stronger on
/// both axes that matter:
///
/// * **It is checked on every run**, from the tree as it is, rather than against
///   an artefact recorded once that must be re-taken to stay true.
/// * **It fires earlier and more precisely** — the GATE itself goes red naming the
///   fixture and the assertion, instead of a falsification quietly turning
///   `VACUOUS` at the next `--verify-mutations`.
///
/// # Why some leaves have no other option
///
/// Two situations, both represented here:
///
/// 1. **G2.6.** Its property is "the fault-injection corpus is complete and
///    armed". The RUN outcome of each of its five fixtures is a statement about
///    somebody else's property — H3's reader (G2.13g), Fix-1's durability and the
///    seal contract (G2.9a), N3's Fatalf latch — so a mutation registered on G2.6
///    that reddened one of them would mint a second `PROVEN` out of one defect.
///    That is precisely what the one-gate-per-property split and
///    `expect_strings_are_pairwise_discriminating` exist to forbid. Its per-leaf
///    falsifier is the pin it already enforces: the injector construction plus the
///    three fired-count needles, in EXECUTING code. Empty any of the five bodies
///    and G2.6 says so by name.
/// 2. **`TestCrashDurablePrefixNoReorder`.** No honest revert reddens the prefix
///    property — see the note at the end of [`LEAF_COVERAGE`].
#[allow(dead_code)] // read by the source-side coverage tests
pub struct SourceSideFalsifier {
    pub gate: &'static str,
    pub leaf: &'static str,
    pub why: &'static str,
}

#[allow(dead_code)] // read by `every_pinned_leaf_is_reddened_by_a_recorded_mutation`
pub const SOURCE_SIDE_FALSIFIERS: &[SourceSideFalsifier] = &[
    SourceSideFalsifier {
        gate: "G2.9a",
        leaf: "TestCrashDurablePrefixNoReorder",
        why: "the prefix property follows from Pebble's single WAL plus a single-writer committer \
              that assigns commitTs and Applies in one order; no fix hunk's revert holes it, and \
              the one that could (moving assignment to the caller) is a redesign whose inversion \
              is scheduler-dependent",
    },
    SourceSideFalsifier {
        gate: "G2.6",
        leaf: "TestAuditH3ReaderGetSurfacesIoErrors",
        why: "its RUN outcome is G2.13g's property (the reader surfaces an I/O fault as an error); \
              a mutation here would mint a second PROVEN out of G2.13g's defect",
    },
    SourceSideFalsifier {
        gate: "G2.6",
        leaf: "TestAuditN3BackgroundFatalDoesNotKillTheProcess",
        why: "reverting the Fatalf latch does not redden this fixture, it KILLS the test binary — \
              there is no `--- FAIL:` line to record, and a gate cannot report on a process that \
              no longer exists",
    },
    SourceSideFalsifier {
        gate: "G2.6",
        leaf: "TestAuditN3SynchronousWalFaultStillErrorsTheAck",
        why: "the hunk that reddens it is N3 consumption point 3/5, which is already the subject \
              of `G2.9a/wal-fatal-never-reaches-the-ack`; two mutations of one hunk are one proof \
              counted twice",
    },
    SourceSideFalsifier {
        gate: "G2.6",
        leaf: "TestInjectedFaultsReopenConsistent",
        why: "its RUN outcome is G2.9a's arm (a); both of G2.9a's mutations already reach it",
    },
    SourceSideFalsifier {
        gate: "G2.6",
        leaf: "TestSealContractRefusesWrites",
        why: "its RUN outcome is G2.9a's seal contract, reached by \
              `G2.9a/sealed-engine-still-runs-gc`",
    },
];

/// The needles a gate enforces over one pinned fixture's EXECUTING body.
///
/// Empty means the gate would not notice that body being replaced by `{}` — which
/// is what [`SOURCE_SIDE_FALSIFIERS`] rows are checked against.
#[allow(dead_code)] // read by `every_source_side_falsifier_is_a_pin_the_gate_actually_enforces`
fn enforced_needles(gate: &str, leaf: &str) -> Vec<&'static str> {
    // G2.6 does not use SourceAnchor: it enforces the injector construction plus
    // the three C3 fired-count needles on every manifest row, which is the same
    // pin by a different spelling and predates it.
    if gate == "G2.6" {
        if super::gates_g2::INJECTION_MANIFEST
            .iter()
            .any(|m| m.test == leaf)
        {
            return super::gates_g2::G2_6_FIXTURE_PINS
                .iter()
                .map(|(n, _)| *n)
                .collect();
        }
        return Vec::new();
    }
    let anchors: &[SourceAnchor] = match gate {
        "G2.9a" => super::gates_g2::G2_9A_ANCHORS,
        "G2.13a" => G2_13A_ANCHORS,
        "G2.13b" => G2_13B_ANCHORS,
        "G2.13h" => G2_13H_ANCHORS,
        "G2.13i" => G2_13I_ANCHORS,
        "G2.13j" => G2_13J_ANCHORS,
        "G2.13k" => G2_13K_ANCHORS,
        _ => &[],
    };
    anchors
        .iter()
        .filter(|a| a.func == leaf)
        .map(|a| a.needle)
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The eleven gate bodies, paired with their ids. Cross-checked against
    /// [`ALL_SETS`] so the two cannot drift.
    const BODIES: &[(&str, fn(&Ctx) -> GateOutcome)] = &[
        ("G2.13a", g2_13a_iterate_bounds),
        ("G2.13b", g2_13b_failed_scan_is_an_error),
        ("G2.13c", g2_13c_corrupt_hlc_hi),
        ("G2.13d", g2_13d_no_false_ack),
        ("G2.13e", g2_13e_readts_pinned_with_snapshot),
        ("G2.13f", g2_13f_close_quiesces_readers),
        ("G2.13g", g2_13g_failed_read_is_an_error),
        ("G2.13h", g2_13h_commit_route_fails_closed),
        ("G2.13i", g2_13i_corrupt_keys_fail_closed),
        ("G2.13j", g2_13j_lifecycle_pins_the_exported_surface),
        ("G2.13k", g2_13k_post_ack_panic_is_never_absorbed),
    ];

    const ALL_SETS: &[(&str, &[&str])] = &[
        ("G2.13a", G2_13A_TESTS),
        ("G2.13b", G2_13B_TESTS),
        ("G2.13c", G2_13C_TESTS),
        ("G2.13d", G2_13D_TESTS),
        ("G2.13e", G2_13E_TESTS),
        ("G2.13f", G2_13F_TESTS),
        ("G2.13g", G2_13G_TESTS),
        ("G2.13h", G2_13H_TESTS),
        ("G2.13i", G2_13I_TESTS),
        ("G2.13j", G2_13J_TESTS),
        ("G2.13k", G2_13K_TESTS),
    ];

    /// The sub-test leaves each gate requires as evidence, alongside its `-run`
    /// population — the gates that `-run` whole FUNCTIONS carrying `t.Run` arms.
    const ALL_SUBTESTS: &[(&str, &[&str])] = &[
        ("G2.13h", G2_13H_SUBTESTS),
        ("G2.13i", G2_13I_SUBTESTS),
        ("G2.13k", G2_13K_SUBTESTS),
    ];

    /// **Every gate the per-leaf rule governs**, with the leaves each pins.
    ///
    /// [`ALL_SETS`] is the audit-corpus family; this adds the two engine gates
    /// whose populations live in `gates_g2.rs`. Extending the rule to them is the
    /// whole point: G2.9a pins seven crash fixtures and G2.6 five injection
    /// fixtures, and until this list existed neither was asked whether an emptied
    /// body would be noticed. G2.6's answer was `no` for all five, from the
    /// transcript side — its mutation never reaches `go test` at all.
    fn governed_gates() -> Vec<(&'static str, Vec<&'static str>)> {
        let mut out: Vec<(&'static str, Vec<&'static str>)> = ALL_SETS
            .iter()
            .map(|(id, set)| {
                let mut leaves: Vec<&'static str> = set.to_vec();
                for (sid, subs) in ALL_SUBTESTS {
                    if sid == id {
                        leaves.extend_from_slice(subs);
                    }
                }
                (*id, leaves)
            })
            .collect();
        out.push((
            "G2.9a",
            super::super::gates_g2::G2_9A_CRASH_TESTS.to_vec(),
        ));
        out.push((
            "G2.6",
            super::super::gates_g2::INJECTION_MANIFEST
                .iter()
                .map(|m| m.test)
                .collect(),
        ));
        out
    }

    fn repo() -> std::path::PathBuf {
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../..")
            .canonicalize()
            .expect("repo root")
    }

    fn audit_src() -> String {
        std::fs::read_to_string(repo().join(AUDIT_SOURCE)).expect("read audit_test.go")
    }

    /// The ownership table IS the file's population. Checked here as well as in
    /// the gate so drift fails `cargo test`, not only a full-tier run.
    #[test]
    fn the_ownership_table_matches_the_corpus_on_disk() {
        let declared = go_test_names(&audit_src());
        let recorded: BTreeSet<String> =
            AUDIT_OWNERSHIP.iter().map(|o| o.test.to_string()).collect();
        assert_eq!(
            declared, recorded,
            "AUDIT_OWNERSHIP has drifted from {AUDIT_SOURCE}"
        );
    }

    #[test]
    fn ownership_rows_are_unique() {
        let mut seen: Vec<&str> = AUDIT_OWNERSHIP.iter().map(|o| o.test).collect();
        let before = seen.len();
        seen.sort_unstable();
        seen.dedup();
        assert_eq!(before, seen.len(), "duplicate row in AUDIT_OWNERSHIP");
    }

    /// Every pinned name resolves to a real `func Test…`, and every gate's set
    /// is of uniform depth — the precondition
    /// [`super::super::gates_g2::run_pattern`] documents.
    #[test]
    fn every_pinned_set_names_real_tests_at_a_uniform_depth() {
        let declared = go_test_names(&audit_src());
        for (id, set) in ALL_SETS {
            assert!(!set.is_empty(), "{id} pins nothing");
            let depths: BTreeSet<usize> = set.iter().map(|t| t.split('/').count()).collect();
            assert_eq!(depths.len(), 1, "{id} mixes sub-test depths: {set:?}");
            for t in *set {
                let top = t.split('/').next().unwrap();
                assert!(declared.contains(top), "{id} pins {t}, but {top} is not declared");
            }
        }
    }

    /// Every required sub-test leaf sits BELOW one of its own gate's pinned
    /// functions, and is generated by a `t.Run` literal in that function.
    ///
    /// The first half stops a leaf that `-run` would never select (it would
    /// report "did not report a passing event" forever, red for a reason that
    /// is about the pin rather than the property). The second stops a leaf
    /// whose name was copied wrong — which fails in exactly the same way, and
    /// would be read as a real regression.
    #[test]
    fn every_required_subtest_sits_under_a_pinned_function_and_is_generated_there() {
        let src = audit_src();
        for (id, leaves) in ALL_SUBTESTS {
            let run_set = ALL_SETS
                .iter()
                .find(|(gid, _)| gid == id)
                .map(|(_, s)| *s)
                .unwrap_or_else(|| panic!("{id} declares leaves but pins no run set"));
            for leaf in *leaves {
                let (parent, sub) = leaf
                    .split_once('/')
                    .unwrap_or_else(|| panic!("{id}: {leaf} is not a sub-test path"));
                assert!(
                    run_set.contains(&parent),
                    "{id} requires {leaf}, but {parent} is not in its `-run` population — the leaf \
                     could never run"
                );
                let needle = format!("t.Run(\"{sub}\"");
                assert!(
                    src.contains(&needle),
                    "{id} requires {leaf}, but {AUDIT_SOURCE} has no `{needle}` to generate it"
                );
            }
        }
    }

    /// No two gates may run the same test: an overlap would make one mutation
    /// redden two gates, which is the very thing the per-property split exists
    /// to prevent. (The N1 function is shared, but at DIFFERENT sub-test paths.)
    #[test]
    fn the_pinned_sets_are_disjoint() {
        let mut seen: Vec<(&str, &str)> = Vec::new();
        for (id, set) in ALL_SETS.iter().chain(ALL_SUBTESTS.iter()) {
            for t in *set {
                if let Some((other, _)) = seen.iter().find(|(_, s)| s == t) {
                    panic!("{id} and {other} both pin {t}");
                }
                seen.push((id, t));
            }
        }
    }

    /// The claim in [`AUDIT_OWNERSHIP`] that G2.6 runs the two N3 fixtures is
    /// verified against G2.6's manifest rather than asserted in prose. An
    /// ownership row nobody checks is a row that can quietly become false.
    #[test]
    fn the_rows_attributed_to_g2_6_really_are_in_its_injection_manifest() {
        for o in AUDIT_OWNERSHIP.iter().filter(|o| o.owner == "G2.6") {
            assert!(
                super::super::gates_g2::INJECTION_MANIFEST
                    .iter()
                    .any(|m| m.test == o.test && m.file == AUDIT_SOURCE),
                "{} is attributed to G2.6 but is not in INJECTION_MANIFEST",
                o.test
            );
        }
    }

    /// Every row is owned by a gate that exists, or explicitly by nothing. A
    /// typo'd gate id would otherwise read as coverage.
    #[test]
    fn every_owner_is_a_registered_gate_or_explicitly_ungated() {
        for o in AUDIT_OWNERSHIP {
            if o.owner == UNGATED {
                continue;
            }
            for id in o.owner.split('+') {
                assert!(
                    super::super::registry::find(id).is_some(),
                    "{} names owner {id}, which is not in the registry",
                    o.test
                );
            }
        }
    }

    /// The source anchors must actually anchor: each needle is present in the
    /// function it names, TODAY. A needle that never matched would make its
    /// gate permanently red, and one copied wrong would make it red for the
    /// wrong reason.
    #[test]
    fn every_source_anchor_resolves_against_the_corpus() {
        let src = audit_src();
        let bodies = enumerate_injections(&src);
        for a in G2_13A_ANCHORS
            .iter()
            .chain(G2_13B_ANCHORS.iter())
            .chain(G2_13H_ANCHORS.iter())
            .chain(G2_13I_ANCHORS.iter())
            .chain(G2_13J_ANCHORS.iter())
            .chain(G2_13K_ANCHORS.iter())
        {
            let body = bodies
                .iter()
                .find(|f| f.test == a.func)
                .unwrap_or_else(|| panic!("no func {}", a.func))
                .body
                .clone();
            assert!(
                body.contains(a.needle),
                "{}::{} does not contain `{}`",
                AUDIT_SOURCE,
                a.func,
                a.needle
            );
        }
    }

    /// The `t.Run(` counts are facts about the file, not hopes about it.
    #[test]
    fn every_recorded_subtest_count_matches_the_corpus() {
        let src = audit_src();
        let bodies = enumerate_injections(&src);
        let check = |s: &SubtestSites| {
            let body = &bodies
                .iter()
                .find(|f| f.test == s.func)
                .unwrap_or_else(|| panic!("no func {}", s.func))
                .body;
            assert_eq!(
                body.matches(T_RUN).count(),
                s.sites,
                "{}::{} `t.Run(` count",
                AUDIT_SOURCE,
                s.func
            );
        };
        for s in N1_SITES
            .iter()
            .chain(G2_13H_SITES.iter())
            .chain(G2_13I_SITES.iter())
            .chain(G2_13J_SITES.iter())
            .chain(G2_13K_SITES.iter())
        {
            check(s);
        }
        // The five sub-test-free fixtures, reached through their gate
        // descriptors so a new gate cannot forget the pin.
        for func in [
            "TestAuditN5CorruptHlcHiRefusesOpenAndNeverReissuesTs",
            "TestAuditC1CommitOnClosedChannelReturnsError",
            "TestAuditH1SnapshotReadTsIsPinnedWithItsSnapshot",
            "TestAuditH1SnapshotSeesEveryCommitAtOrBelowItsReadTs",
            "TestAuditN4CloseDoesNotPanicConcurrentSnapshot",
            "TestAuditN4CloseWaitsForLiveReaders",
            "TestAuditN4CloseWithLeakedReaderReportsRatherThanHangs",
            "TestAuditN4BeginPathReaderClosesSnapshotBeforeItsPin",
            "TestAuditH3ReaderGetSurfacesIoErrors",
        ] {
            check(&SubtestSites { func, sites: 0 });
        }
    }

    /// Each declared `expect` string must be VERBATIM in the Go fixture that
    /// emits it. That is what makes "copied from a real observed failure, never
    /// composed" (`P1-STAGE2-PLAN.md` risk 6) checkable rather than a promise —
    /// a composed string would not be found here.
    ///
    /// G2.9a's three mutations are held to the same rule against
    /// `crashsim_test.go`. G2.6's ONE mutation is not, and that exemption is
    /// named rather than implied: its assertion is the GATE's own wording (a
    /// manifest reconciliation finding, emitted before `go test` runs), and
    /// `the_declared_expect_string_is_the_gate_s_own_wording` in `gates_g2.rs`
    /// checks it against the body that must emit it. Requiring it to appear in a
    /// fixture would be requiring evidence the design says does not exist —
    /// exactly the shape [`STRUCTURAL_MUTATIONS`] records.
    #[test]
    fn every_declared_assertion_is_verbatim_in_the_fixture_that_emits_it() {
        let corpus = format!(
            "{}\n{}",
            audit_src(),
            std::fs::read_to_string(repo().join("runtime-go/bluedb/crashsim_test.go"))
                .expect("read crashsim_test.go")
        );
        for (id, _) in governed_gates() {
            let g = super::super::registry::find(id).expect("registered");
            for m in g.mutations.as_slice() {
                if STRUCTURAL_MUTATIONS.iter().any(|(mid, _)| *mid == m.id) {
                    continue;
                }
                assert!(
                    corpus.contains(m.expect),
                    "{}: declares `{}`, which appears nowhere in the Go corpus \
                     ({AUDIT_SOURCE} + crashsim_test.go) — an `expect` string must be copied from \
                     the failure the mutation actually produces",
                    m.id,
                    m.expect
                );
            }
        }
    }

    /// A [`STRUCTURAL_MUTATIONS`] entry is a claim that the mutation's recorded
    /// RED transcript carries no per-test failure at all. Checked against the
    /// artefact, in the direction that can be abused: a mutation that DID redden
    /// a fixture must record WHICH, not be filed here to avoid saying.
    #[test]
    fn a_structural_mutation_really_produced_no_per_test_failure() {
        let root = repo();
        for (id, why) in STRUCTURAL_MUTATIONS {
            let m = super::super::registry::REGISTRY
                .iter()
                .flat_map(|g| g.mutations.as_slice())
                .find(|m| m.id == *id)
                .unwrap_or_else(|| panic!("{id} is not a registered mutation"));
            let transcript =
                std::fs::read_to_string(root.join(super::super::gates_g0::expected_path(m.patch)))
                    .unwrap_or_else(|e| {
                        panic!("{id}: cannot read the recorded RED transcript: {e}")
                    });
            assert!(
                !transcript.contains("--- FAIL: "),
                "{id} is recorded as structural ({why}), but its RED transcript DOES carry a \
                 `--- FAIL:` line — record the leaves it reddened in LEAF_COVERAGE instead"
            );
        }
    }

    /// **Every [`SOURCE_SIDE_FALSIFIERS`] row names a pin the gate really
    /// enforces, and gutting that leaf's body really trips it.**
    ///
    /// The row is a claim of the form "this leaf cannot be emptied without the
    /// gate noticing". It is verified, not asserted, in three steps: the gate
    /// must declare at least one needle over that fixture; the needle must be
    /// present in the fixture as it stands today (a pin that never matched is a
    /// permanently red gate, not a falsifier); and an EMPTY body must fail the
    /// same check the gate runs, which is the gut-the-body property spelled out.
    #[test]
    fn every_source_side_falsifier_is_a_pin_the_gate_actually_enforces() {
        let mut bodies = enumerate_injections(&audit_src());
        bodies.extend(enumerate_injections(
            &std::fs::read_to_string(repo().join("runtime-go/bluedb/crashsim_test.go"))
                .expect("read crashsim_test.go"),
        ));

        for r in SOURCE_SIDE_FALSIFIERS {
            let needles = enforced_needles(r.gate, r.leaf);
            assert!(
                !needles.is_empty(),
                "{}::{} is recorded as falsified by a source-side pin ({}), but {} declares no \
                 needle over that fixture — the body could be replaced by `{{}}` and the gate \
                 would still report PASS",
                r.gate,
                r.leaf,
                r.why,
                r.gate
            );
            let body = &bodies
                .iter()
                .find(|f| f.test == r.leaf)
                .unwrap_or_else(|| panic!("{}: no `func {}` in the corpus", r.gate, r.leaf))
                .body;
            for n in &needles {
                assert!(
                    body.contains(n),
                    "{}::{} is claimed to be pinned by `{n}`, which is not in its EXECUTING body",
                    r.gate,
                    r.leaf
                );
                // The gut-the-body property, asserted directly: an empty function
                // satisfies no needle, so the gate's check reports it.
                let gutted = format!("func {}(t *testing.T) {{\n}}\n", r.leaf);
                assert!(
                    !gutted.contains(n),
                    "{}::{}: `{n}` survives an emptied body — it is not a pin on the assertion",
                    r.gate,
                    r.leaf
                );
            }
        }
    }

    /// No row may claim a leaf its gate does not pin, and no gate may be named
    /// that the rule does not govern — either would be coverage recorded against
    /// nothing.
    #[test]
    fn every_source_side_falsifier_names_a_pinned_leaf_of_a_governed_gate() {
        let governed = governed_gates();
        for r in SOURCE_SIDE_FALSIFIERS {
            let (_, leaves) = governed
                .iter()
                .find(|(id, _)| *id == r.gate)
                .unwrap_or_else(|| panic!("{} is not under the per-leaf rule", r.gate));
            assert!(
                leaves.contains(&r.leaf),
                "{} claims {}, which is not one of its pinned leaves",
                r.gate,
                r.leaf
            );
        }
    }

    /// Every one of the nine is a REAL body, not a `pending` probe. The
    /// substrate has landed, so a pending gate here would be the ratchet
    /// failing open.
    ///
    /// **This test used to require EXACTLY one mutation per gate**, on the
    /// argument that the one-gate-per-property split is what makes the
    /// falsifications discriminating. That argument is about `expect` strings —
    /// which `registry.rs`'s `expect_strings_are_pairwise_discriminating`
    /// enforces directly — and as a cap on the mutation COUNT it did active
    /// harm: it forbade the second mutation that would have covered a pinned
    /// leaf the first one leaves green. Six leaves were falsified by nothing at
    /// all, and this assertion is what stopped anyone fixing that. It is now a
    /// floor, and the coverage requirement below is the real check.
    #[test]
    fn all_nine_are_registered_with_real_bodies_and_at_least_one_mutation_each() {
        for (id, f) in BODIES {
            let g = super::super::registry::find(id).unwrap_or_else(|| panic!("{id} unregistered"));
            assert_eq!(g.goal, 2, "{id} belongs to goal 2");
            assert_eq!(g.run as usize, *f as usize, "{id} is not wired to its body");
            assert!(
                !g.mutations.as_slice().is_empty(),
                "{id} declares no mutation"
            );
        }
        // The list is the population: a gate added to ALL_SETS but not here (or
        // vice versa) would slip past every check in this module.
        let bodies: BTreeSet<&str> = BODIES.iter().map(|(id, _)| *id).collect();
        let sets: BTreeSet<&str> = ALL_SETS.iter().map(|(id, _)| *id).collect();
        assert_eq!(bodies, sets, "BODIES and ALL_SETS disagree about the population");
    }

    /// **Every pinned leaf is turned red by at least one recorded mutation.**
    ///
    /// The hole this closes: an empty Go test function emits `pass`. A gate's
    /// three anti-vacuity assertions prove the leaf RAN — not that its body
    /// asserts anything. So a leaf that no mutation reddens could have its body
    /// gutted and the gate would stay green **and** report `PROVEN`, because the
    /// proof is about whichever leaf the one recorded mutation happens to touch.
    ///
    /// Found by audit across three gates at once: G2.13h's mutation left both
    /// C6b doors and the N6 control green, G2.13e's left the property arm green,
    /// G2.13f's left two of four green, and G2.13a's leaves the two
    /// correct-by-luck control lengths green. Twelve leaves, one blind spot.
    ///
    /// **The claim is checked against the RECORDED TRANSCRIPT, not asserted.**
    /// `--verify-mutations` writes the mutated gate's output verbatim to
    /// `<patch>.expected.txt` on a PROVEN verdict, and a Go failure prints
    /// `--- FAIL: <full test name>`. So "this mutation reddens that leaf" is a
    /// fact about a committed artefact — which is what makes the table below
    /// evidence rather than a promise, and what makes it decay honestly (edit a
    /// patch, and the transcript it belongs to must be re-taken).
    #[test]
    fn every_pinned_leaf_is_reddened_by_a_recorded_mutation() {
        let root = repo();
        for (id, leaf_list) in governed_gates() {
            let id = &id;
            let g = super::super::registry::find(id).expect("registered");
            let leaves: BTreeSet<&str> = leaf_list.iter().copied().collect();

            let mut covered: BTreeSet<&str> = BTreeSet::new();
            for m in g.mutations.as_slice() {
                if STRUCTURAL_MUTATIONS.iter().any(|(mid, _)| *mid == m.id) {
                    continue; // reddens the gate before `go test` runs; see the const's doc
                }
                let row = LEAF_COVERAGE
                    .iter()
                    .find(|r| r.mutation == m.id)
                    .unwrap_or_else(|| {
                        panic!(
                            "{} has no LEAF_COVERAGE row — every mutation of a gate under the \
                             per-leaf rule must record which pinned leaves its RED run actually \
                             reddened (or be recorded in STRUCTURAL_MUTATIONS)",
                            m.id
                        )
                    });
                let transcript = std::fs::read_to_string(
                    root.join(super::super::gates_g0::expected_path(m.patch)),
                )
                .unwrap_or_else(|e| {
                    panic!(
                        "{}: cannot read the recorded RED transcript {}: {e}. A mutation's leaf \
                         coverage is read from the transcript `--verify-mutations` writes, so a \
                         new mutation is not finished until it has been RUN: \
                         `cargo run -p xtask -- bluedb-gates --verify-mutations --only={id}`",
                        m.id,
                        super::super::gates_g0::expected_path(m.patch)
                    )
                });
                for leaf in row.leaves {
                    assert!(
                        leaves.contains(leaf),
                        "{} claims to redden {leaf}, which is not a pinned leaf of {id}",
                        m.id
                    );
                    assert!(
                        transcript.contains(&format!("--- FAIL: {leaf}")),
                        "{} claims to redden {leaf}, but its recorded RED transcript carries no \
                         `--- FAIL: {leaf}` line. The claim is checked against the artefact; \
                         re-run --verify-mutations, or fix the claim",
                        m.id
                    );
                    covered.insert(leaf);
                }
            }

            // The second evidence kind: a pin the gate enforces on every run.
            // Verified by `every_source_side_falsifier_is_a_pin_the_gate_actually_
            // enforces`, so this is a lookup, not a second claim.
            for r in SOURCE_SIDE_FALSIFIERS.iter().filter(|r| r.gate == *id) {
                covered.insert(r.leaf);
            }

            let uncovered: Vec<&str> = leaves.difference(&covered).copied().collect();
            assert!(
                uncovered.is_empty(),
                "{id}: {} pinned leaf/leaves are falsified by NOTHING: {uncovered:?}\n\n\
                 An empty Go test emits `pass`, so those bodies could be gutted and this gate \
                 would stay green AND report PROVEN. Close it one of the two ways: author a \
                 mutation that reddens the leaf (see docs/bluedb/mutations/) and record it in \
                 LEAF_COVERAGE, or anchor the gate on the leaf's own property assertion (see \
                 SourceAnchor) and record it in SOURCE_SIDE_FALSIFIERS with the argument for why \
                 no honest revert reaches it.",
                uncovered.len()
            );
        }
    }

    /// No LEAF_COVERAGE row may name a mutation that is not registered — a
    /// stale row would otherwise sit there looking like coverage.
    #[test]
    fn every_leaf_coverage_row_names_a_registered_mutation() {
        let registered: BTreeSet<&str> = super::super::registry::REGISTRY
            .iter()
            .flat_map(|g| g.mutations.as_slice().iter().map(|m| m.id))
            .collect();
        for r in LEAF_COVERAGE {
            assert!(
                registered.contains(r.mutation),
                "LEAF_COVERAGE names {}, which is not a registered mutation",
                r.mutation
            );
            assert!(
                !r.leaves.is_empty(),
                "{}: a row claiming no leaves is not coverage",
                r.mutation
            );
        }
    }
}
