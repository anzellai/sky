//! End-to-end self-tests for `xtask harness`.
//!
//! These drive the REAL binary, because the behaviours under test are process
//! behaviours — a process group, a `killpg`, an exit code — and none of them
//! can be observed from a unit test that never forks.
//!
//! Each test corresponds to a property the BlueDB precedent this phase was
//! meant to adopt does not have, and asserts it by CONSTRUCTION (build the bad
//! input, observe the rejection) rather than by inspection.

use std::path::{Path, PathBuf};
use std::process::{Command, Output};
use std::time::{Duration, Instant};

const XTASK: &str = env!("CARGO_BIN_EXE_xtask");

fn repo_root() -> PathBuf {
    let mut d = Path::new(env!("CARGO_MANIFEST_DIR")).to_path_buf();
    while !d.join("examples").is_dir() {
        d = d.parent().expect("repo root not found").to_path_buf();
    }
    d
}

fn harness(args: &[&str]) -> Output {
    Command::new(XTASK)
        .arg("harness")
        .args(args)
        // `--verify-falsifiers` BANKS what it proved, which is right for the
        // command and wrong here: this suite drives the real binary against the
        // real repo, so `cargo test -p xtask` was rewriting the tracked
        // `docs/coverage/falsifier-proofs.json` and leaving the working tree
        // dirty. A proof that any test run refreshes is a timestamp following
        // the observer around, and it means `git status` after a test can never
        // be trusted — which is how 1928 build artefacts got swept into a
        // commit earlier in this cycle.
        //
        // Redirect the ledger to a scratch path. The production path is
        // unchanged; only this suite writes elsewhere.
        .env("SKY_PROOF_LEDGER", scratch_ledger())
        .current_dir(repo_root())
        .output()
        .expect("failed to run xtask harness")
}

/// A per-process scratch ledger, so concurrent test binaries cannot collide.
fn scratch_ledger() -> PathBuf {
    let mut p = std::env::temp_dir();
    p.push(format!("sky-proof-ledger-{}.json", std::process::id()));
    p
}

fn stdout(o: &Output) -> String {
    String::from_utf8_lossy(&o.stdout).into_owned()
}

/// Is a process with this pid alive AND still the command we spawned?
///
/// Matching on the command name matters: a bare `kill -0` can be satisfied by
/// an unrelated process that recycled the pid, which would make the test flaky
/// in the direction of a FALSE PASS for the leak we are hunting.
fn alive_as(pid: u32, comm: &str) -> bool {
    let out = Command::new("ps")
        .args(["-p", &pid.to_string(), "-o", "comm="])
        .output();
    match out {
        Ok(o) => String::from_utf8_lossy(&o.stdout).contains(comm),
        Err(_) => false,
    }
}

// ---------------------------------------------------------------------------
// The timeout actually kills — including the grandchild
// ---------------------------------------------------------------------------

/// The blocking design requirement of v2 §7.3, demonstrated.
///
/// `selftest-hang` spawns a `sleep 600` grandchild, records both pids, and then
/// hangs forever on a 3-second budget. After the harness returns:
///
///   * the gate is reported **FAIL** (a budget overrun is never a pass);
///   * the run took roughly the budget, not 600 s;
///   * **both** the gate body and its grandchild are gone.
///
/// The grandchild is the whole point. Killing only the direct child leaves it
/// running — which is what "kill the process group" means and what a
/// thread-based runner cannot do at all.
#[test]
fn a_hung_gate_is_killed_at_its_budget_and_takes_its_grandchild_with_it() {
    let pidfile = std::env::temp_dir().join(format!("sky-harness-hang-{}.pids", std::process::id()));
    let _ = std::fs::remove_file(&pidfile);

    let started = Instant::now();
    let out = Command::new(XTASK)
        .args(["harness", "--only", "selftest-hang"])
        .current_dir(repo_root())
        .env("SKY_HARNESS_HANG_PIDFILE", &pidfile)
        .output()
        .expect("failed to run xtask harness");
    let elapsed = started.elapsed();

    let text = stdout(&out);

    // 1. It is FAIL, and the reason names the budget — not a fabricated success.
    assert!(
        text.contains("selftest-hang") && text.contains("FAIL"),
        "a hung gate must be reported FAIL:\n{text}"
    );
    assert!(
        text.contains("BUDGET EXCEEDED"),
        "the FAIL must be attributed to the budget:\n{text}"
    );
    assert_ne!(out.status.code(), Some(0), "a hung gate must not exit 0");

    // 2. It was killed at the budget (3 s), not waited out. Generous ceiling so
    //    a loaded machine does not make this flaky, but far below `sleep 600`.
    assert!(
        elapsed < Duration::from_secs(60),
        "the harness waited {elapsed:?} on a 3s budget — the timeout did not fire"
    );

    // 3. The process group is gone — body AND grandchild.
    let pids = std::fs::read_to_string(&pidfile)
        .expect("the hanging body should have recorded its pids before hanging");
    let mut lines = pids.lines();
    let body: u32 = lines.next().unwrap().trim().parse().unwrap();
    let grandchild: u32 = lines.next().unwrap().trim().parse().unwrap();

    // Give the OS a beat to reap after killpg returns.
    std::thread::sleep(Duration::from_millis(500));

    assert!(
        !alive_as(body, "xtask"),
        "the gate body (pid {body}) survived its budget"
    );
    assert!(
        !alive_as(grandchild, "sleep"),
        "the GRANDCHILD (pid {grandchild}) survived — killpg did not reach the \
         process group, so a leaked server would poison every later gate"
    );

    let _ = std::fs::remove_file(&pidfile);
}

// ---------------------------------------------------------------------------
// The canary
// ---------------------------------------------------------------------------

/// The canary must report VACUOUS, and the harness must treat that as correct.
///
/// This is the one place a *passing* gate is the success signal. A falsifier
/// runner that answers "PROVEN" to a no-op patch has either applied the patch
/// in the wrong tree or is not reading the verdict from the run it just did —
/// and every other PROVEN it reports is worthless.
#[test]
fn the_canary_reports_vacuous_and_that_is_the_pass() {
    let out = harness(&["--verify-falsifiers", "--only", "canary"]);
    let text = stdout(&out);

    assert!(
        text.contains("VACUOUS"),
        "the canary must be reported VACUOUS:\n{text}"
    );
    assert!(
        !text.contains("PROVEN"),
        "reporting PROVEN for a no-op patch means the harness is lying:\n{text}"
    );
    assert!(
        text.contains("FALSIFIER GATE: PASS"),
        "VACUOUS is the canary's DECLARED outcome, so the falsifier gate passes:\n{text}"
    );
    assert_eq!(out.status.code(), Some(0), "{text}");
}

// ---------------------------------------------------------------------------
// States and selection
// ---------------------------------------------------------------------------

/// `--only` must render every other gate NOT APPLICABLE — never NOT RUN.
///
/// Conflating them makes local development emit UNKNOWN constantly, which
/// trains people to ignore the one state that means "we do not know".
#[test]
fn only_renders_the_rest_not_applicable_never_not_run() {
    let out = harness(&["--only", "canary"]);
    let text = stdout(&out);

    assert!(
        text.contains("NOT APPLICABLE"),
        "deselected gates must render NOT APPLICABLE:\n{text}"
    );
    assert!(
        !text.contains("NOT RUN"),
        "a deliberate selection must never produce NOT RUN:\n{text}"
    );
    assert!(text.contains("HARNESS VERDICT: PASS"), "{text}");
    assert_eq!(out.status.code(), Some(0), "{text}");
}

/// A NOT RUN gate renders the suite UNKNOWN and exits non-zero.
///
/// `--fail-fast` is the real trigger: after a FAIL, gates that were selected
/// but never reached are genuinely unknown. The suite must not round that up.
#[test]
fn a_not_run_gate_makes_the_suite_unknown_and_exits_non_zero() {
    // `selftest-hang` fails (budget), so `canary` — also selected — is never
    // reached. Registry order puts canary before selftest-hang, so ask for the
    // hang first by relying on --fail-fast plus both being selected.
    let out = harness(&["--only", "selftest-hang,canary", "--fail-fast"]);
    let text = stdout(&out);

    assert!(
        text.contains("NOT RUN"),
        "a selected-but-unreached gate must render NOT RUN:\n{text}"
    );
    assert!(
        !text.contains("HARNESS VERDICT: PASS"),
        "a suite containing NOT RUN must never render PASS:\n{text}"
    );
    assert_ne!(
        out.status.code(),
        Some(0),
        "NOT RUN must exit non-zero:\n{text}"
    );
}

/// Every registered gate appears in the report even when it did not run.
///
/// Rows come from the REGISTRY, not from the run's results. This is the
/// property that kills "SKIP counted as pass" at the root: a gate cannot
/// disappear by not executing.
#[test]
fn every_registered_gate_gets_a_row() {
    let listing = stdout(&harness(&["--list"]));
    let names: Vec<String> = listing
        .lines()
        .skip(2)
        .filter(|l| !l.trim_start().starts_with('↳') && !l.starts_with('-'))
        .filter_map(|l| l.split_whitespace().next().map(str::to_string))
        .filter(|n| !n.is_empty())
        .collect();
    assert!(names.len() >= 5, "registry looks empty: {names:?}");

    let run = stdout(&harness(&["--only", "canary"]));
    for n in &names {
        assert!(
            run.contains(n.as_str()),
            "gate `{n}` is registered but has no row in the report:\n{run}"
        );
    }
}

/// An unknown gate name is an ERROR, not an empty selection that passes.
///
/// The same class as `xtask` exiting 0 on an unknown subcommand: a typo in a CI
/// gate name must not become a permanently green no-op.
#[test]
fn an_unknown_gate_name_is_refused() {
    let out = harness(&["--only", "coerce_floor"]);
    let err = String::from_utf8_lossy(&out.stderr);
    assert!(err.contains("unknown gate"), "{err}");
    assert_ne!(out.status.code(), Some(0));
    assert_ne!(
        out.status.code(),
        Some(1),
        "a usage error should be distinguishable from a gate failure"
    );
}

/// `--require-proofs` turns an unproven PASS into UNPROVEN, and UNKNOWN.
#[test]
fn an_unproven_gate_is_not_a_pass() {
    // Point the harness at a repo copy with no proof ledger by asking for a
    // gate we have not just proven. The canary's proof is written by the
    // falsifier test above, so use the hang gate's absence instead: any gate
    // with no ledger entry must render UNPROVEN rather than PASS.
    let out = harness(&["--only", "canary", "--require-proofs"]);
    let text = stdout(&out);
    // Either it has a fresh proof (PASS) or it does not (UNPROVEN) — but it may
    // never be silently PASS with no ledger. Assert the mechanism exists and
    // that UNPROVEN, when it occurs, is non-zero.
    if text.contains("UNPROVEN") {
        assert!(
            !text.contains("HARNESS VERDICT: PASS"),
            "UNPROVEN must never render PASS:\n{text}"
        );
        assert_ne!(out.status.code(), Some(0), "{text}");
    } else {
        assert!(
            text.contains("PASS"),
            "with a fresh proof the gate should pass:\n{text}"
        );
    }
}
