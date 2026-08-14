//! The G2.x engine gates — the ones whose subject is `runtime-go/bluedb`'s
//! commit path, and which therefore could not be written until P1 Stage 2
//! landed `committer.go` (see [`super::pending::p1_engine`]).
//!
//! # Why every body here runs `go test` behind THREE assertions
//!
//! `go test -run 'TestNoSuchThing'` **exits 0**. Reproduced on this repo, on
//! this Go toolchain. A gate body that shells out and classifies on the exit
//! status alone therefore reports PASS having executed nothing — which is the
//! precise shape of green lie this whole harness exists to make impossible, and
//! the one the plan names as the trap for these two gates specifically.
//!
//! No one of the three is sufficient, so all three are mandatory:
//!
//! 1. **The population is pinned in source, not discovered from the run.** Each
//!    gate declares the EXACT set of test function names it certifies, and
//!    cross-checks that set against the `func Test…` declarations parsed out of
//!    the Go file. A deleted test is a FAIL, not a smaller green run; an added
//!    one is a FAIL until it is recorded here. This is the mechanism
//!    `harness/bodies.rs`'s `CLI_VERBS_EXPECTED` already uses, sharpened from a
//!    count to a set (a count alone cannot see a rename, and a rename is how a
//!    test silently stops matching `-run`).
//! 2. **`-count=1`.** Without it Go serves `ok (cached)` from the test result
//!    cache, having run no test binary at all — an exit-0 with no execution,
//!    the same failure as (1) by a different road.
//! 3. **`-json`, parsed for exactly N `pass` actions.** Exit status is not
//!    evidence of what ran. The runner requires the set of tests that reported
//!    `Action:"pass"` to be EQUAL to the pinned set — not a superset, not a
//!    non-empty subset.
//!
//! # Why the failure detail carries the Go test output verbatim
//!
//! `mutations.rs` classifies a mutation by whether the gate's declared `expect`
//! string appears in its output. That string is copied from a REAL observed
//! failure of the Go assertion (never composed, per the plan's risk 6), so the
//! body must surface the test's own log lines rather than a summary of its own
//! authorship. A gate that paraphrased its subject's failure could be made to
//! emit the magic words without the property ever having been violated.

use std::collections::BTreeSet;
use std::process::Command;
use std::time::Duration;

use super::gates_g0::capped;
use super::registry::{Ctx, GateOutcome};

// ---------------------------------------------------------------------------
// The shared `go test` runner
// ---------------------------------------------------------------------------

/// Where the Go module lives, relative to `ctx.root()`.
const GO_MODULE_DIR: &str = "runtime-go";
/// The package under test.
const GO_PACKAGE: &str = "./bluedb/";

/// The build tag every shipped BlueDB build carries (G0.5). Running the gates
/// without it would certify a build configuration no app ever executes.
const ZSTD_TAG: &str = "pebblegozstd";

/// How many lines of a failing run are quoted into the findings. Bounded
/// because an unbounded quote is not a report: one C8 fixture emitted 121,145
/// lines, which is the same as emitting none.
const QUOTE_LINES: usize = 40;

pub(super) struct GoTestRun {
    /// Tests that reported `Action:"pass"` — the only evidence that counts.
    pub passed: BTreeSet<String>,
    /// Tests that reported `Action:"fail"`.
    pub failed: BTreeSet<String>,
    /// Per-test `Output` lines, in order, for every test that did not pass.
    pub failure_log: Vec<String>,
    /// Raw interleaved stdout+stderr, for the cases `-json` cannot describe
    /// (a build error, a panic that kills the binary, a timeout).
    pub raw: String,
    pub exit_ok: bool,
    pub timed_out: bool,
}

/// Run exactly `tests` under `budget`, with the three anti-vacuity flags.
///
/// The `-run` pattern is **anchored and closed** (`^(A|B)$`): an unanchored
/// pattern silently pulls in every test whose name has one of these as a
/// prefix, and then the "exactly N passes" assertion would be measuring a
/// population the gate never declared.
pub(super) fn go_test(ctx: &Ctx, tests: &[&str], budget: Duration) -> Result<GoTestRun, String> {
    let pattern = format!("^({})$", tests.join("|"));

    let mut cmd = Command::new("go");
    cmd.arg("test")
        .arg("-count=1") // (2) defeat the result cache
        .arg("-tags")
        .arg(ZSTD_TAG)
        .arg("-json") // (3) machine-readable per-test verdicts
        .arg("-run")
        .arg(&pattern)
        .arg(GO_PACKAGE)
        .current_dir(ctx.path(GO_MODULE_DIR))
        // H3: the child must resolve the tree under test, never the developer's.
        // `current_dir` is derived from `ctx`, and nothing here reads an
        // absolute path or an ambient `cwd`.
        .env("GOTOOLCHAIN", "local");

    let run = capped(cmd, budget).map_err(|e| format!("could not run `go test`: {e}"))?;

    let mut passed = BTreeSet::new();
    let mut failed = BTreeSet::new();
    let mut output_by_test: Vec<(String, String)> = Vec::new();

    for line in run.out.lines() {
        let Ok(v) = serde_json::from_str::<serde_json::Value>(line) else {
            continue; // non-JSON noise (a go build error, a runtime panic)
        };
        let action = v.get("Action").and_then(|a| a.as_str()).unwrap_or("");
        // A package-level event has no `Test` field; only per-test events are
        // evidence about which tests executed.
        let Some(test) = v.get("Test").and_then(|t| t.as_str()) else {
            continue;
        };
        match action {
            "pass" => {
                passed.insert(test.to_string());
            }
            "fail" => {
                failed.insert(test.to_string());
            }
            "output" => {
                if let Some(o) = v.get("Output").and_then(|o| o.as_str()) {
                    output_by_test.push((test.to_string(), o.trim_end().to_string()));
                }
            }
            _ => {}
        }
    }

    let failure_log = output_by_test
        .into_iter()
        .filter(|(t, _)| !passed.contains(t))
        .map(|(t, o)| format!("{t}: {o}"))
        .take(QUOTE_LINES)
        .collect();

    Ok(GoTestRun {
        passed,
        failed,
        failure_log,
        raw: run.out,
        exit_ok: run.ok,
        timed_out: run.timed_out,
    })
}

/// Parse the `func TestXxx(` declarations out of a Go test source.
///
/// Deliberately syntactic and deliberately strict: it reads only declarations
/// at column 0, so a `func Test…` mentioned in a comment or a string does not
/// count, and a method with a receiver does not either.
pub(super) fn go_test_names(src: &str) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    for line in src.lines() {
        let Some(rest) = line.strip_prefix("func Test") else {
            continue;
        };
        let Some(open) = rest.find('(') else { continue };
        let name = &rest[..open];
        if !name.is_empty() && name.chars().all(|c| c.is_alphanumeric() || c == '_') {
            out.insert(format!("Test{name}"));
        }
    }
    out
}

/// Assertion (1): the population that will be run is the population that was
/// declared. Returns the findings; empty means the source agrees with the pin.
///
/// `where_` names the file so a finding says which pin to update.
pub(super) fn check_pinned_population(
    declared: &BTreeSet<String>,
    pinned: &[&str],
    where_: &str,
    pin_name: &str,
) -> Vec<String> {
    let pinned: BTreeSet<String> = pinned.iter().map(|s| s.to_string()).collect();
    let mut findings = Vec::new();

    for missing in pinned.difference(declared) {
        findings.push(format!(
            "{missing} is pinned in {pin_name} but no longer declared in {where_} — \
             `go test -run` would match nothing for it and STILL EXIT 0"
        ));
    }
    for extra in declared.difference(&pinned) {
        findings.push(format!(
            "{extra} is declared in {where_} but not pinned in {pin_name} — a new test in this \
             file is either part of the property this gate certifies (record it) or it is not \
             (move it); an unrecorded test is neither run nor accounted for"
        ));
    }
    findings
}

/// Assertion (3): exactly the pinned tests reported `pass`.
pub(super) fn check_run_evidence(run: &GoTestRun, pinned: &[&str]) -> Vec<String> {
    let pinned: BTreeSet<String> = pinned.iter().map(|s| s.to_string()).collect();
    let mut findings = Vec::new();

    if run.timed_out {
        findings.push("`go test` exceeded the gate's budget and its process group was killed".into());
    }

    for missing in pinned.difference(&run.passed) {
        findings.push(format!(
            "{missing} did not report a passing `go test -json` event ({})",
            if run.failed.contains(missing) {
                "it FAILED"
            } else {
                "it did not run at all — exit status is not evidence"
            }
        ));
    }
    for extra in run.passed.difference(&pinned) {
        findings.push(format!(
            "{extra} passed but is not in the pinned set — the `-run` anchor is leaking"
        ));
    }

    if findings.is_empty() && !run.exit_ok {
        findings.push(format!(
            "every pinned test passed yet `go test` exited non-zero — the package itself failed \
             (build error, TestMain, or a panic outside a test):\n{}",
            crate::harness::layer2::tail(&run.raw, 20)
        ));
    }
    findings
}

// ---------------------------------------------------------------------------
// G2.9a — durability on ack (embedded / pebble), arms (a)–(c)
// ---------------------------------------------------------------------------

/// The crash + durability corpus this gate certifies, pinned by NAME.
///
/// Scope is arms (a)–(c) — fsync before ack, survives crash, no reorder — all
/// of which are questions about the embedded pebble commit path. Arm (d)
/// (`durability="normal"`) and arm (e) (the sqlite WAL policy) belong to
/// **G2.9b**, which stays on `pending::p3_isolation`: the engine has no
/// durability knob to test (`pebble_engine.go`'s config carries no such field
/// and the committer hard-codes `Apply(pebble.Sync)`), and sqlite is not P1
/// substrate at all. Writing them here would mean writing a gate against
/// nothing, which is the failure mode `pending.rs` was narrowed to avoid.
///
/// Every name below is a `func Test…` in `crashsim_test.go`, and the gate
/// asserts that file declares EXACTLY these — see
/// [`check_pinned_population`].
pub const G2_9A_CRASH_TESTS: &[&str] = &[
    // (b) survives crash: every acked write is in the crash clone.
    "TestCrashAckedWritesSurvive",
    // (b) + concurrency: nothing acked under concurrent writer load is lost.
    "TestCrashConcurrentNoAckedLoss",
    // (c) no reorder: a backward wall clock cannot re-issue a commitTs, so no
    //     key ever carries two versions at one timestamp across a restart.
    "TestCrashHLCNoReissue",
    // (b) atomicity: a multi-write commit is all-or-nothing on disk.
    "TestCrashNoTornBatch",
    // (a) fsync before ack: an injected WAL-fsync fault must produce an errored
    //     ack — a nil ack always means durable — and the engine must seal.
    "TestInjectedFaultsReopenConsistent",
    // (a) the seal contract: once sealed, every write path refuses loudly.
    "TestSealContractRefusesWrites",
];

const G2_9A_SOURCE: &str = "runtime-go/bluedb/crashsim_test.go";

pub fn g2_9a_durability_on_ack(ctx: &Ctx) -> GateOutcome {
    let Some(src) = ctx.read(G2_9A_SOURCE) else {
        return GateOutcome::fail(
            format!("cannot read {G2_9A_SOURCE}"),
            vec!["the crash corpus is the gate's subject; without it there is nothing to certify".into()],
        );
    };

    // ── (1) the population is pinned, not discovered ──
    let declared = go_test_names(&src);
    let mut findings = check_pinned_population(
        &declared,
        G2_9A_CRASH_TESTS,
        G2_9A_SOURCE,
        "G2_9A_CRASH_TESTS (bluedb_gates/gates_g2.rs)",
    );
    if !findings.is_empty() {
        return GateOutcome::fail(
            format!(
                "the crash corpus does not match its pinned population ({} declared, {} pinned)",
                declared.len(),
                G2_9A_CRASH_TESTS.len()
            ),
            findings,
        );
    }

    // ── (2) + (3) run them, with the cache defeated and per-test evidence ──
    // 840s of the gate's 900s budget: the remainder covers this body's own
    // parsing and leaves headroom for `capped` to kill the group and reap.
    let run = match go_test(ctx, G2_9A_CRASH_TESTS, Duration::from_secs(840)) {
        Ok(r) => r,
        Err(e) => return GateOutcome::fail(e, vec!["a gate that cannot run has not passed".into()]),
    };

    findings.extend(check_run_evidence(&run, G2_9A_CRASH_TESTS));
    findings.extend(run.failure_log.iter().cloned());

    if findings.is_empty() {
        GateOutcome::pass(format!(
            "acked ⇒ durable: {} crash/durability tests pinned in source and observed passing via \
             `go test -json -count=1` (arms a–c, embedded/pebble)",
            G2_9A_CRASH_TESTS.len()
        ))
    } else {
        GateOutcome::fail(
            format!(
                "durability on ack is not proven: {}/{} pinned crash tests reported a passing event",
                run.passed.len(),
                G2_9A_CRASH_TESTS.len()
            ),
            findings,
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_test_names_reads_declarations_and_nothing_else() {
        let src = "\
package bluedb\n\
// func TestInAComment(t *testing.T) — must not count\n\
const s = \"func TestInAString(\"\n\
func TestReal(t *testing.T) {}\n\
func (e *engine) TestMethod(t *testing.T) {}\n\
func BenchmarkThing(b *testing.B) {}\n\
func TestOther(t *testing.T) {\n}\n";
        let got = go_test_names(src);
        assert_eq!(
            got,
            ["TestOther", "TestReal"]
                .iter()
                .map(|s| s.to_string())
                .collect::<BTreeSet<_>>()
        );
    }

    /// The whole point of assertion (1): a DELETED test must be a finding, not
    /// a smaller green run. `go test -run` on a name that matches nothing exits
    /// 0, so without this the gate would pass having certified less.
    #[test]
    fn a_deleted_test_is_a_finding_not_a_smaller_green_run() {
        let declared: BTreeSet<String> = ["TestA"].iter().map(|s| s.to_string()).collect();
        let f = check_pinned_population(&declared, &["TestA", "TestB"], "x_test.go", "PIN");
        assert_eq!(f.len(), 1);
        assert!(f[0].contains("TestB"), "{f:?}");
        assert!(f[0].contains("STILL EXIT 0"), "{f:?}");
    }

    #[test]
    fn an_unrecorded_new_test_is_also_a_finding() {
        let declared: BTreeSet<String> =
            ["TestA", "TestNew"].iter().map(|s| s.to_string()).collect();
        let f = check_pinned_population(&declared, &["TestA"], "x_test.go", "PIN");
        assert_eq!(f.len(), 1);
        assert!(f[0].contains("TestNew"), "{f:?}");
    }

    fn run_with(passed: &[&str], failed: &[&str], exit_ok: bool) -> GoTestRun {
        GoTestRun {
            passed: passed.iter().map(|s| s.to_string()).collect(),
            failed: failed.iter().map(|s| s.to_string()).collect(),
            failure_log: vec![],
            raw: String::new(),
            exit_ok,
            timed_out: false,
        }
    }

    /// The reproduced defect, asserted directly: a run in which NOTHING
    /// executed but the process exited 0 must be RED.
    #[test]
    fn exit_zero_with_no_passing_events_is_red() {
        let f = check_run_evidence(&run_with(&[], &[], true), &["TestA", "TestB"]);
        assert_eq!(f.len(), 2, "{f:?}");
        assert!(
            f.iter().all(|s| s.contains("exit status is not evidence")),
            "{f:?}"
        );
    }

    #[test]
    fn a_leaking_run_anchor_is_red() {
        let f = check_run_evidence(&run_with(&["TestA", "TestAExtra"], &[], true), &["TestA"]);
        assert_eq!(f.len(), 1, "{f:?}");
        assert!(f[0].contains("leaking"), "{f:?}");
    }

    #[test]
    fn the_full_pinned_set_passing_is_green() {
        assert!(check_run_evidence(&run_with(&["TestA", "TestB"], &[], true), &["TestA", "TestB"]).is_empty());
    }

    /// G2.9b must stay pending: this gate covers arms (a)–(c) only, and the
    /// separation is what stops arm (d)/(e) being quietly folded into a green
    /// G2.9a. If someone re-points G2.9b at this body, this test says no.
    #[test]
    fn g2_9b_is_a_separate_gate_and_stays_pending() {
        let b = super::super::registry::find("G2.9b").expect("G2.9b is registered");
        assert_eq!(
            b.run as usize,
            super::super::pending::p3_isolation as usize,
            "G2.9b's arms (d) durability=normal and (e) sqlite WAL are P3 substrate; \
             they are not certified by G2.9a's crash corpus"
        );
    }
}
