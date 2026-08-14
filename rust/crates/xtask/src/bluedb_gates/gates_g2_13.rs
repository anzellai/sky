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
    check_pinned_population, check_run_evidence, enumerate_injections, go_test, go_test_names,
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
/// happened within the week: three N4/durability fixtures landed alongside the
/// fix they pin, and they carry this value until the commit that lands those
/// fixes gives them a gate. Deleting the word would have made silence the only
/// available answer.
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
    // -- landed ahead of their gate, 2026-08-14 -----------------------------
    //
    // Three fixtures added by the concurrent fix of the two engine defects this
    // audit also turned up. They are recorded here — with the word for it —
    // rather than omitted, because an ownership table that lists only what it
    // already covers reports full coverage of whatever it happens to list, and
    // reading it the other way round is what found G2.13h's four fail-open
    // rows. Right now CI's `go test ./bluedb/...` runs them and nothing else
    // does: they are invisible to `--verify-mutations`, to `STATUS.md` and to
    // every goal verdict.
    //
    // OWED by the commit that lands those fixes: a gate that `-run`s them, and
    // a mutation per leaf (see [`LEAF_COVERAGE`]). The N4 pair belongs with
    // G2.13f's property; the post-ack one is a durability-route statement.
    Owned {
        test: "TestAuditN4ChangelogAndGCDoNotRaceCloseIntoAPanic",
        owner: UNGATED,
        property: "the changelog and GC paths do not race Close into a panic (N4)",
    },
    Owned {
        test: "TestAuditN4GCPassIsPinnedAgainstAConcurrentClose",
        owner: UNGATED,
        property: "a GC pass is pinned against a concurrent Close (N4)",
    },
    Owned {
        test: "TestAuditPostAckDurabilityPanicIsNotSilentlyAbsorbed",
        owner: UNGATED,
        property: "a durability panic AFTER the ack is not silently absorbed",
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

/// A literal construct that must appear in a pinned function's body.
///
/// The source-side pin for names `go_test_names` cannot see. A sub-test name is
/// a `t.Run` argument, and one of the two here is built by `fmt.Sprintf` from a
/// literal `[]int`, so the only honest way to pin the population in SOURCE is
/// to pin the construct that generates it.
pub struct SourceAnchor {
    /// The enclosing `func Test…`.
    pub func: &'static str,
    pub needle: &'static str,
    pub why: &'static str,
}

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

    for a in g.anchors {
        match body_of(a.func) {
            None => findings.push(format!(
                "{}: {AUDIT_SOURCE} has no `func {}` to anchor against",
                g.id, a.func
            )),
            Some(body) => {
                if !body.contains(a.needle) {
                    findings.push(format!(
                        "{}::{} no longer contains `{}` — {}. The sub-test names this gate `-run`s \
                         are generated by that construct, and a `-run` that matches nothing EXITS 0.",
                        AUDIT_SOURCE, a.func, a.needle, a.why
                    ));
                }
            }
        }
    }
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
];

#[cfg(test)]
mod tests {
    use super::*;

    /// The nine gate bodies, paired with their ids. Cross-checked against
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
    ];

    /// The sub-test leaves each gate requires as evidence, alongside its `-run`
    /// population — the two gates that `-run` whole FUNCTIONS carrying `t.Run`
    /// arms.
    const ALL_SUBTESTS: &[(&str, &[&str])] =
        &[("G2.13h", G2_13H_SUBTESTS), ("G2.13i", G2_13I_SUBTESTS)];

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
    #[test]
    fn every_declared_assertion_is_verbatim_in_the_fixture_that_emits_it() {
        let src = audit_src();
        for (id, _) in ALL_SETS {
            let g = super::super::registry::find(id).expect("registered");
            for m in g.mutations.as_slice() {
                assert!(
                    src.contains(m.expect),
                    "{}: declares `{}`, which appears nowhere in {AUDIT_SOURCE} — an `expect` \
                     string must be copied from the failure the mutation actually produces",
                    m.id,
                    m.expect
                );
            }
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
        for (id, set) in ALL_SETS {
            let g = super::super::registry::find(id).expect("registered");
            let leaves: BTreeSet<&str> = set
                .iter()
                .chain(
                    ALL_SUBTESTS
                        .iter()
                        .filter(|(sid, _)| sid == id)
                        .flat_map(|(_, l)| l.iter()),
                )
                .copied()
                .collect();

            let mut covered: BTreeSet<&str> = BTreeSet::new();
            for m in g.mutations.as_slice() {
                let row = LEAF_COVERAGE
                    .iter()
                    .find(|r| r.mutation == m.id)
                    .unwrap_or_else(|| {
                        panic!(
                            "{} has no LEAF_COVERAGE row — every mutation of an audit gate must \
                             record which pinned leaves its RED run actually reddened",
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

            let uncovered: Vec<&str> = leaves.difference(&covered).copied().collect();
            assert!(
                uncovered.is_empty(),
                "{id}: {} pinned leaf/leaves are falsified by NO mutation: {uncovered:?}\n\n\
                 An empty Go test emits `pass`, so those bodies could be gutted and this gate \
                 would stay green AND report PROVEN. Author a mutation that reddens them (see \
                 docs/bluedb/mutations/) and record it in LEAF_COVERAGE.",
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
