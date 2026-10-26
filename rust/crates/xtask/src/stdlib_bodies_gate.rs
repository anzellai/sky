//! `xtask stdlib-bodies` — every stdlib module BODY type-checks.
//!
//! A build trusts the stdlib's signatures and never type-checks its bodies
//! (`project::build` checks only `check_ids`: the project, its path
//! dependencies and its `.skydeps` packages). This gate runs the checker a
//! user module gets over every `sky-stdlib/` module, so an ill-typed stdlib
//! body fails CI instead of shipping. The logic is `ty::stdlib_bodies`; the
//! per-commit face of the same check is `cargo test -p ty --test stdlib_bodies`.

use std::path::Path;

/// The number of stdlib modules the gate must check. EXACT, never `>=`: a
/// module that stops loading (or a new one) moves it, so the gate cannot go
/// quietly vacuous.
pub const EXPECTED_MODULES: u64 = 113;

/// `(passed, assertions, detail)`. One assertion per module checked.
pub fn check_body(root: &Path) -> (bool, u64, String) {
    let report = ty::stdlib_bodies::check_repo_stdlib(root);
    let assertions = report.modules.len() as u64;
    if !report.clean() {
        return (
            false,
            assertions,
            format!(
                "{} stdlib body error(s), {} stale allowance(s):\n{}",
                report.error_count(),
                report.stale_allowances.len(),
                report.render()
            ),
        );
    }
    (
        true,
        assertions,
        format!("{assertions} stdlib module(s), every body type-checks"),
    )
}

pub fn run(_args: &[String], root: &Path) -> i32 {
    let (passed, assertions, detail) = check_body(root);
    println!("stdlib-bodies: {detail}");
    if !passed {
        return 1;
    }
    if assertions != EXPECTED_MODULES {
        println!(
            "stdlib-bodies: checked {assertions} module(s), expected exactly \
             {EXPECTED_MODULES}; update EXPECTED_MODULES when a stdlib module \
             is added or removed"
        );
        return 1;
    }
    0
}
