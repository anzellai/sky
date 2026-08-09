//! Gate + suite states.
//!
//! Five values, four of which a gate can actually be *in* after a run
//! (`NOT APPLICABLE` is a selection outcome, not a run outcome). The design
//! authority is `docs/ci-test-architecture-v2.md` §7.2; the one deliberate
//! renaming is documented in [`GateState::Unproven`].
//!
//! The property that matters more than the names: **a run that cannot say
//! whether a gate passed has not passed.** `NOT RUN` and `UNPROVEN` both
//! render the suite `UNKNOWN`, and `UNKNOWN` exits non-zero. v1 left this
//! ambiguous, which is how "SKIP counted as pass" shipped.

use std::fmt;

/// The state of a single registered gate after a harness run.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum GateState {
    /// Ran, every assertion held, **and `assertions > 0`**.
    ///
    /// The `assertions > 0` clause is not decoration: it is the structural
    /// kill for the `0/0 … GATE: PASS` class (`doc-examples.sh` with
    /// `total=0`) and for `verify-all-web.sh`'s empty-run green.
    Pass,
    /// An assertion broke, **or** the budget was exceeded, **or** the gate
    /// produced no assertions at all (vacuous), **or** the body could not be
    /// spawned.
    ///
    /// A gate that could not fork did not test anything, so `EAGAIN` on spawn
    /// is a FAIL and never a retry loop or a silent skip (v2 §7.6).
    Fail,
    /// Registered but not executed — a harness error, or a body that never
    /// reported. Renders the suite `UNKNOWN`.
    NotRun,
    /// Ran and passed, but its falsifying mutation is unproven: never
    /// verified, or last verified outside its declared window.
    ///
    /// v2 §7.2 calls this proof state `UNVERIFIED-SINCE`. The name here is
    /// `UNPROVEN` per the Phase-1 brief; the contract is identical and is the
    /// reason it is distinct from `Fail`: the proof is unrevalidated, not
    /// known-broken. Conflating the two trains people to ignore the signal.
    Unproven,
    /// Outside the selected tier or platform, or deselected by `--only`.
    ///
    /// Deliberate selection is **not** an unknown. Conflating them makes local
    /// development emit `UNKNOWN` constantly, which trains people to ignore it
    /// — the same failure mode as a soft `BLOCKED` (v2 §7.2).
    NotApplicable,
}

impl GateState {
    /// The wire/report spelling. Stable — CI parses these.
    pub fn label(self) -> &'static str {
        match self {
            GateState::Pass => "PASS",
            GateState::Fail => "FAIL",
            GateState::NotRun => "NOT RUN",
            GateState::Unproven => "UNPROVEN",
            GateState::NotApplicable => "NOT APPLICABLE",
        }
    }

    /// Does this state leave the suite unable to claim a verdict?
    pub fn is_unknown(self) -> bool {
        matches!(self, GateState::NotRun | GateState::Unproven)
    }
}

impl fmt::Display for GateState {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.label())
    }
}

/// The verdict over a whole selection of gates.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum SuiteVerdict {
    Pass,
    Fail,
    /// At least one gate is `NOT RUN` or `UNPROVEN`. **Never renders PASS.**
    Unknown,
}

impl SuiteVerdict {
    /// Fold gate states into the suite verdict.
    ///
    /// Order matters: `Fail` dominates `Unknown` so a real breakage is never
    /// masked by an unrelated unknown, and `Unknown` dominates `Pass` so an
    /// unknown can never be rounded up.
    pub fn of<I: IntoIterator<Item = GateState>>(states: I) -> SuiteVerdict {
        let mut unknown = false;
        for s in states {
            match s {
                GateState::Fail => return SuiteVerdict::Fail,
                s if s.is_unknown() => unknown = true,
                _ => {}
            }
        }
        if unknown {
            SuiteVerdict::Unknown
        } else {
            SuiteVerdict::Pass
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            SuiteVerdict::Pass => "PASS",
            SuiteVerdict::Fail => "FAIL",
            SuiteVerdict::Unknown => "UNKNOWN",
        }
    }

    /// Process exit code. **Both** non-PASS verdicts exit non-zero; they are
    /// given distinct codes so CI can tell "something broke" from "the run
    /// could not establish a verdict".
    pub fn exit_code(self) -> i32 {
        match self {
            SuiteVerdict::Pass => 0,
            SuiteVerdict::Fail => 1,
            SuiteVerdict::Unknown => 3,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn all_pass_is_pass_and_exits_zero() {
        let v = SuiteVerdict::of([GateState::Pass, GateState::Pass]);
        assert_eq!(v, SuiteVerdict::Pass);
        assert_eq!(v.exit_code(), 0);
    }

    #[test]
    fn not_applicable_never_blocks_a_pass() {
        // `--only` must not turn every deselected gate into an unknown.
        let v = SuiteVerdict::of([GateState::Pass, GateState::NotApplicable]);
        assert_eq!(v, SuiteVerdict::Pass);
        assert_eq!(v.exit_code(), 0);
    }

    #[test]
    fn not_run_renders_unknown_and_exits_non_zero() {
        let v = SuiteVerdict::of([GateState::Pass, GateState::NotRun]);
        assert_eq!(v, SuiteVerdict::Unknown);
        assert_ne!(v.exit_code(), 0);
    }

    #[test]
    fn unproven_renders_unknown_and_exits_non_zero() {
        let v = SuiteVerdict::of([GateState::Pass, GateState::Unproven]);
        assert_eq!(v, SuiteVerdict::Unknown);
        assert_ne!(v.exit_code(), 0);
    }

    #[test]
    fn a_suite_with_an_unknown_can_never_render_pass() {
        // The literal claim from v2 §7.2, asserted over every combination that
        // contains an unknown.
        for other in [
            GateState::Pass,
            GateState::Fail,
            GateState::NotApplicable,
            GateState::NotRun,
            GateState::Unproven,
        ] {
            for unknown in [GateState::NotRun, GateState::Unproven] {
                let v = SuiteVerdict::of([other, unknown]);
                assert_ne!(v, SuiteVerdict::Pass, "{other:?} + {unknown:?}");
                assert_ne!(v.exit_code(), 0, "{other:?} + {unknown:?}");
            }
        }
    }

    #[test]
    fn fail_dominates_unknown() {
        let v = SuiteVerdict::of([GateState::NotRun, GateState::Fail]);
        assert_eq!(v, SuiteVerdict::Fail);
    }
}
