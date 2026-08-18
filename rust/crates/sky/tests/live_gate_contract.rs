//! The live gate's own contract.
//!
//! `live_gate.rs` decides whether every live test in this crate runs or not, so
//! its behaviour is asserted rather than described. It lives here, as an
//! integration test, rather than as a `#[cfg(test)] mod tests` inside the module
//! itself: the module is `#[path]`-included by eight test crates (integration
//! tests cannot import from a binary crate), and `cfg(test)` is true in every
//! one of them — so an inner test module would compile and run eight times, in
//! eight verdict lines, for one set of claims.

#[path = "../src/live_gate.rs"]
mod live_gate;

use live_gate::{mode_from, required_in, Mode, Need};



#[test]
fn an_available_need_runs_the_test() {
    assert!(required_in(Mode::Require, Need::Postgres, true));
    assert!(required_in(Mode::Skip, Need::Postgres, true));
}

#[test]
fn network_is_never_required() {
    // Under REQUIRE, which is the whole point of the carve-out.
    assert!(!required_in(Mode::Require, Need::Network, false));
}

#[test]
fn an_unmet_need_returns_false_only_when_skipping_was_asked_for() {
    assert!(!required_in(Mode::Skip, Need::Postgres, false));
    assert!(!required_in(Mode::Skip, Need::Go, false));
}

/// The mode parse is the gate's single point of failure: read it wrong and
/// every live test in the repo silently changes behaviour. Note which way
/// the unset case falls — REQUIRE. A default of `Skip` would restore the
/// original defect on every machine that has not heard of this variable.
#[test]
fn the_mode_parse_defaults_to_require() {
    assert_eq!(mode_from(None), Mode::Require);
    assert_eq!(mode_from(Some("")), Mode::Require);
    assert_eq!(mode_from(Some("require")), Mode::Require);
    assert_eq!(mode_from(Some("skip")), Mode::Skip);
}

#[test]
#[should_panic(expected = "is not a mode")]
fn an_unrecognised_mode_is_refused_rather_than_guessed() {
    // `SKY_LIVE_TESTS=1` meaning "require" to whoever typed it and "skip"
    // to this function is how a gate ends up not running.
    let _ = mode_from(Some("1"));
}

#[test]
#[should_panic(expected = "A live test that did not run has not passed")]
fn an_unmet_need_fails_under_the_default_mode() {
    // The headline property, asserted rather than described. Under the
    // shape this replaces, the same call site returned and the test
    // reported `ok`.
    let _ = required_in(mode_from(None), Need::Postgres, false);
}

/// The reason travels with the verdict.
///
/// "PostgreSQL is not available" was a complete answer while the only probe was
/// "are the binaries discoverable". It is not one on a host where PostgreSQL is
/// installed, discoverable, and cannot start — 32 SysV shared-memory ids, all
/// held. Thirteen shared-cluster SECURITY tests failed there in one
/// `cargo test --workspace`, and a red run naming thirteen security tests is
/// indistinguishable from a real regression until someone reads the gate's
/// source. `why` is what puts the machine's own words in the failure.
#[test]
#[should_panic(expected = "could not create shared memory segment")]
fn an_unmet_need_reports_the_machines_own_reason() {
    let _ = live_gate::required_in_because(
        Mode::Require,
        Need::Postgres,
        false,
        "FATAL:  could not create shared memory segment: No space left on device",
    );
}

/// A classifier that turns a failure into a skip is a mechanism for laundering
/// real defects into green. These are the failures it must NEVER absorb.
///
/// Every one is a verdict a shared-cluster SECURITY test exists to produce.
/// `db_shared/live_tests.rs` asserts that app A's credentials cannot reach app
/// B's database, that a cluster which does not ask for a password is refused,
/// and that a `pg_hba.conf` which does not parse is refused rather than
/// reloaded. If any of these classified as "the environment is unavailable",
/// the boundary would be reported as a skip and the suite would go green over
/// a hole in the security model — strictly worse than the panic it replaced.
const SECURITY_BOUNDARY_FAILURES: &[(&str, &str)] = &[
    (
        "FATAL:  password authentication failed for user \"alpha\"\nSQLSTATE 28P01",
        "the credential-isolation verdict itself: app A refused against app B's database",
    ),
    (
        "ERROR:  permission denied for database beta\nSQLSTATE 42501",
        "the ACL half of the same boundary — connected, and refused at the object",
    ),
    (
        "sky db provision --shared: refusing to adopt a cluster whose pg_hba.conf does \
         not parse: line 92: invalid connection type \"hosts\"",
        "an unparseable HBA must be REFUSED, not reloaded; a skip here means every \
         REVOKE behind it is decoration",
    ),
    (
        "sky db provision --shared: refusing a cluster that does not ask for a password \
         (local all all trust)",
        "a trust cluster is the state in which the whole boundary is inert",
    ),
    (
        "initdb: error: directory \"/x/pg\" exists but is not empty",
        "a fixture defect: the state dir was not cleaned",
    ),
    (
        "could not write to file \"pg_wal/xlogtemp.7\": No space left on device",
        "a genuinely full DISK. Same three words as the shm diagnostic, different call \
         — the reason the patterns match PostgreSQL's startup diagnostics rather than \
         `No space left on device`",
    ),
];

/// The classifier, checked on every machine — including the ones that could
/// never run the tests it gates.
#[test]
fn only_an_environment_that_cannot_start_a_postmaster_classifies_as_unavailable() {
    let shm = "sky db start: initdb failed:\n\
               2026-08-16 20:43:19 BST [88483] FATAL:  could not create shared memory \
               segment: No space left on device\n\
               DETAIL:  Failed system call was shmget(key=496079915, size=56, 03600).";
    let why = live_gate::postgres_cannot_start(shm).expect("shmget ENOSPC is the environment");
    assert!(
        why.lines().next().unwrap_or_default().contains("could not create shared memory segment"),
        "the MATCHED line must lead, or a one-line marker says `initdb failed:` — true \
         and useless. Got: {why}"
    );

    let mut checked = 0;
    for (defect, why_it_matters) in SECURITY_BOUNDARY_FAILURES {
        checked += 1;
        assert!(
            live_gate::postgres_cannot_start(defect).is_none(),
            "the classifier absorbed a real failure as an unavailable environment, so it \
             would become a SILENT SKIP: {why_it_matters}\n  {defect}"
        );
        // The end-to-end form: `gate_if_postgres_cannot_start` must decline to
        // gate, so the caller reaches its own `panic!` and the test goes red.
        // Asserted under SKIP mode deliberately — that is the permissive mode,
        // the one in which an over-eager classifier would do its damage
        // silently rather than by panicking.
        assert!(
            !live_gate::gate_if_postgres_cannot_start(defect),
            "gate_if_postgres_cannot_start gated on a real failure: {why_it_matters}"
        );
    }
    assert_eq!(
        checked,
        SECURITY_BOUNDARY_FAILURES.len(),
        "a loop that checks nothing passes silently"
    );
}
