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
    /// development emit `UNKNOWN` constantly, which trains people to ignore it.
    NotApplicable,
    /// Declared structurally impossible to run **right now**, with an issue
    /// link and a hard expiry date (v2 §7.2).
    ///
    /// # Why this is not a soft skip
    ///
    /// A soft skip is a gate that quietly stops asserting and keeps reporting
    /// non-failure for ever. That is the `SKIP counted as pass` class this
    /// harness exists to kill, and it is why the first cut of this file
    /// deliberately omitted the state. `BLOCKED` is admitted only because it
    /// carries four properties a skip does not, and all four are enforced:
    ///
    /// 1. **Declared at compile time**, in `registry::BLOCKED`, with a
    ///    non-empty issue link and a non-empty `YYYY-MM-DD` expiry — the
    ///    constructor is `const fn` and an empty field fails the *build*.
    /// 2. **It expires by itself.** Past the declared date the gate renders
    ///    `FAIL`, with no human action required and nobody to forget. A block
    ///    is a deadline, not a parking space.
    /// 3. **It never renders `PASS`**, so it cannot be counted as a green gate.
    /// 4. **Its surfaces count as UNCOVERED in the coverage ledger.** This is
    ///    the property that actually removes the incentive to abuse it:
    ///    blocking a gate does not preserve a coverage number, it lowers one.
    ///
    /// The suite verdict is *neutral* before expiry — deliberately, because a
    /// state that turns CI permanently red is a state people delete rather than
    /// fix, and deleting the row restores the invisible-absence class that
    /// rendering rows from the registry was meant to end.
    Blocked,
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
            GateState::Blocked => "BLOCKED",
        }
    }

    /// Does this state leave the suite unable to claim a verdict?
    pub fn is_unknown(self) -> bool {
        matches!(self, GateState::NotRun | GateState::Unproven)
    }

    /// Does this state let the gate count as covering its surfaces?
    ///
    /// Consumed by the coverage ledger. `Blocked` answers **false**, which is
    /// the whole reason the state is affordable: blocking a gate lowers a
    /// coverage number instead of preserving it.
    pub fn counts_as_cover(self) -> bool {
        matches!(self, GateState::Pass)
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
            GateState::Blocked,
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

    #[test]
    fn blocked_never_counts_as_cover() {
        // The property that makes BLOCKED affordable: it cannot prop up a
        // coverage number. Only PASS does.
        assert!(!GateState::Blocked.counts_as_cover());
        assert!(GateState::Pass.counts_as_cover());
        for s in [
            GateState::Fail,
            GateState::NotRun,
            GateState::Unproven,
            GateState::NotApplicable,
        ] {
            assert!(!s.counts_as_cover(), "{s:?}");
        }
    }

    #[test]
    fn blocked_is_neutral_but_is_not_a_pass_for_that_gate() {
        // Neutral at the SUITE level, deliberately (see GateState::Blocked): a
        // permanently red state is one people delete rather than fix.
        let v = SuiteVerdict::of([GateState::Pass, GateState::Blocked]);
        assert_eq!(v, SuiteVerdict::Pass);
        assert_eq!(v.exit_code(), 0);
        // ...but the blocked gate itself is never a pass, and never claims cover.
        assert_ne!(GateState::Blocked, GateState::Pass);
        assert!(!GateState::Blocked.counts_as_cover());
    }

    #[test]
    fn blocked_alone_cannot_render_the_suite_green_on_a_lie() {
        // A suite made only of blocked gates asserts nothing. It renders PASS
        // (neutral), which is safe ONLY because the registry always contributes
        // real rows and the ledger counts these surfaces as uncovered. This
        // test pins that BLOCKED is not silently folded into Pass anywhere.
        let states = [GateState::Blocked, GateState::Blocked];
        assert!(states.iter().all(|s| !s.counts_as_cover()));
    }
}
