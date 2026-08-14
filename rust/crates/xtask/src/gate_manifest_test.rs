//! Gate-manifest test — the class-level guard behind the exit-code fix in
//! `main.rs`.
//!
//! Making an unknown subcommand exit 2 turns a typo'd gate name into a LOUD CI
//! failure instead of a silently-green no-op. That closes the instance. This
//! test closes the CLASS on the other side: it walks every file under
//! `.github/workflows/` and `scripts/`, extracts the gate name from every
//! `xtask` invocation it finds, and asserts each one is dispatched by
//! [`crate::GATES`]. A rename in `main.rs` that leaves CI behind (or a typo in
//! CI that `main.rs` never dispatches) fails `cargo test -p xtask` — before the
//! push, not after a green-but-dead CI run.
//!
//! It also closes the REVERSE direction — see
//! [`bluedb_gates_is_invoked_by_at_least_one_workflow`]. Asserting only
//! CI → xtask leaves a registered subcommand that CI never runs completely
//! invisible, which is how the whole BlueDB gate harness came to be off CI
//! while its own unit tests stayed green.
//!
//! The extraction itself lives in [`crate::ci_scan`], NOT here, because
//! `coverage_ledger` scores surfaces from the same references. Two readings of
//! what CI runs would drift silently: this test would keep passing against one
//! of them while the ledger reported coverage against the other.

use crate::ci_scan::{scan_xtask_refs, GateRef};
use std::collections::BTreeSet;

/// Subcommands asserted to be invoked by `.github/workflows/**`.
///
/// THE OTHER DIRECTION. [`every_ci_gate_name_is_dispatched_by_xtask`] walks
/// CI → xtask, so a gate name CI invokes that xtask does not dispatch is loud.
/// The converse — a REGISTERED subcommand that no automation ever invokes — is
/// invisible to it, and that is not a hypothetical: the entire BlueDB gate
/// harness sat off CI. `grep -rin bluedb .github/ scripts/` returned nothing,
/// so every gate body, `--check`, `--verify-mutations` and Stage 1's 13 Go
/// tests ran only when a human typed the command, while the `#[cfg(test)]`
/// units inside `bluedb_gates/*` stayed green forever under
/// `cargo test --workspace` and made the harness look alive.
///
/// THE LIST IS DELIBERATELY SHORT, because the general claim is FALSE today and
/// asserting it would be a lie that happens to be red. Measured against
/// `.github/workflows/` at the time of writing, these registered subcommands
/// appear in NO workflow by name:
///
/// * `corpus`, `shared-world`, `welltyped` — reached through
///   `harness --tier tN`, which nightly and release do invoke. Naming them here
///   would assert the wrong mechanism.
/// * `corpus-bench`, `errloc` — developer tools; neither is a pass/fail gate.
/// * `diff` — a stub that deliberately exits 2. Wiring it would give a
///   permanently red step that verifies nothing.
///
/// Add a name here only when the workflow invocation is the thing being
/// protected, and say which job protects it.
const MUST_BE_INVOKED_BY_A_WORKFLOW: &[(&str, &str)] = &[(
    "bluedb-gates",
    "nightly-sweep.yml's `bluedb-harness` job (--verify-mutations, then --tier=full)",
)];

/// Gate names that MUST be found by the extractor. A refactor that breaks the
/// extractor (or moves the gate suite out of these trees) would otherwise leave
/// the test asserting nothing at all, forever green and worthless.
const MUST_FIND: &[&str] = &["build-run", "reject", "coerce-floor", "repro", "roundtrip"];

/// Lower bound on total extracted references. Deliberately far below the real
/// count (42 at the time of writing) so ordinary CI edits do not trip it, but
/// non-zero so a broken extractor cannot pass vacuously.
const MIN_REFS: usize = 20;

#[test]
fn every_ci_gate_name_is_dispatched_by_xtask() {
    let root = crate::repo_root();
    let workflows = root.join(".github/workflows");
    let scripts = root.join("scripts");
    assert!(
        workflows.is_dir(),
        "expected {} to exist — repo_root() resolved to {}",
        workflows.display(),
        root.display()
    );
    assert!(
        scripts.is_dir(),
        "expected {} to exist — repo_root() resolved to {}",
        scripts.display(),
        root.display()
    );

    let (refs, unresolved) = scan_xtask_refs(&root, &[workflows, scripts]);

    assert!(
        unresolved.is_empty(),
        "xtask gate references the manifest test could not read:\n  {}",
        unresolved.join("\n  ")
    );

    // Visible under `--nocapture`: the extractor's reach, so a shrinking count
    // is noticeable during review as well as at the MIN_REFS floor.
    let mut per_file: BTreeSet<String> = BTreeSet::new();
    for r in &refs {
        per_file.insert(r.file.clone());
    }
    println!(
        "gate-manifest: {} xtask gate references across {} file(s): {:?}",
        refs.len(),
        per_file.len(),
        per_file
    );

    // Anti-vacuity: a matcher that finds nothing asserts nothing.
    assert!(
        refs.len() >= MIN_REFS,
        "gate-manifest extractor found only {} xtask references (expected >= {}). The \
         extractor is broken or the gate suite moved — a vacuous manifest test is the exact \
         failure class this test exists to prevent.",
        refs.len(),
        MIN_REFS
    );
    let found: BTreeSet<&str> = refs.iter().map(|r| r.gate.as_str()).collect();
    for expect in MUST_FIND {
        assert!(
            found.contains(expect),
            "gate-manifest extractor never found a reference to `{expect}` — it is invoked \
             from CI, so the extractor (not CI) is what changed. Found: {found:?}"
        );
    }

    // The actual manifest assertion.
    let known: BTreeSet<&str> = crate::GATES.iter().map(|(name, _)| *name).collect();
    let bad: Vec<&GateRef> = refs
        .iter()
        .filter(|r| !known.contains(r.gate.as_str()))
        .collect();
    assert!(
        bad.is_empty(),
        "CI/scripts invoke xtask gate names that xtask does not dispatch (they would exit 2 \
         and fail the build — or, before the exit-code fix, silently pass while running \
         nothing):\n  {}\nxtask dispatches: {:?}",
        bad.iter()
            .map(|r| format!("{}:{}: `{}`", r.file, r.line_no, r.gate))
            .collect::<Vec<_>>()
            .join("\n  "),
        known
    );
}

/// The xtask → CI direction, for the names in [`MUST_BE_INVOKED_BY_A_WORKFLOW`].
///
/// Scoped to `.github/workflows/` ONLY, deliberately. `scripts/` is where the
/// local pre-push suites live, and a gate that runs only there is exactly the
/// state this test exists to reject: it runs when someone remembers, which is
/// indistinguishable from not running.
#[test]
fn bluedb_gates_is_invoked_by_at_least_one_workflow() {
    let root = crate::repo_root();
    let workflows = root.join(".github/workflows");
    assert!(
        workflows.is_dir(),
        "expected {} to exist — repo_root() resolved to {}",
        workflows.display(),
        root.display()
    );

    let (refs, unresolved) = scan_xtask_refs(&root, &[workflows]);
    assert!(
        unresolved.is_empty(),
        "xtask gate references this test could not read:\n  {}",
        unresolved.join("\n  ")
    );
    // Anti-vacuity: an extractor that reads nothing would let every name below
    // pass by finding nothing to contradict them.
    assert!(
        !refs.is_empty(),
        "found no xtask invocations under .github/workflows — the extractor is broken, and a \
         broken extractor makes this test assert nothing at all"
    );

    let found: BTreeSet<&str> = refs.iter().map(|r| r.gate.as_str()).collect();
    let known: BTreeSet<&str> = crate::GATES.iter().map(|(name, _)| *name).collect();
    for (name, where_) in MUST_BE_INVOKED_BY_A_WORKFLOW {
        // A name that is not dispatched cannot be invoked, and would otherwise
        // fail below with a misleading message about CI.
        assert!(
            known.contains(name),
            "`{name}` is listed in MUST_BE_INVOKED_BY_A_WORKFLOW but xtask does not dispatch \
             it — the list names a subcommand that no longer exists"
        );
        assert!(
            found.contains(name),
            "`xtask {name}` is dispatched but NO workflow under .github/workflows invokes it. \
             It is supposed to run in {where_}.\n\
             A registered-but-uninvoked gate is the failure this assertion exists for: its own \
             `#[cfg(test)]` units keep passing under `cargo test --workspace`, so the harness \
             looks alive while every gate body it supports executes nowhere.\n\
             Workflows currently invoke: {found:?}"
        );
    }
}
