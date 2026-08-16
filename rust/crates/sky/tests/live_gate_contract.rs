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
