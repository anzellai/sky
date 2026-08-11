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
//! The extraction itself lives in [`crate::ci_scan`], NOT here, because
//! `coverage_ledger` scores surfaces from the same references. Two readings of
//! what CI runs would drift silently: this test would keep passing against one
//! of them while the ledger reported coverage against the other.

use crate::ci_scan::{scan_xtask_refs, GateRef};
use std::collections::BTreeSet;

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
