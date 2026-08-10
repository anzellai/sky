//! Reject-corpus regression test (doc 11 §2b; self-host §7 R1-D1).
//!
//! The accept-only differential oracle is structurally blind to soundness
//! regressions (an ill-typed program the Haskell rejects emits no Go). This
//! test closes that half: it runs the versioned rejection corpus
//! (`tests/reject/corpus/*.sky`) — each an ill-typed program the Haskell oracle
//! rejects with a specific diagnostic — through the Rust checker and asserts
//! every HARD-gate program is REJECTED, and that the rejection carries the
//! diagnostic CODE the file's header declares.
//!
//! **This file owns NO criterion of its own.** "Rejected", corpus discovery, the
//! exact corpus size, and the declared-code rule all live in
//! [`ty::reject_corpus`] — the single declaration shared with the CLI gate
//! (`cargo run -p xtask -- reject`). The two faces used to carry private copies
//! and drifted (v2 §1.5); they now cannot.

use std::path::PathBuf;
use ty::reject_corpus as rc;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        if !dir.pop() {
            panic!("could not locate repo root (no sky-stdlib ancestor)");
        }
    }
}

#[test]
fn rejection_corpus_is_rejected() {
    let root = repo_root();
    let rows = rc::scan(&root).unwrap_or_else(|e| panic!("{e}"));
    assert_eq!(
        rows.len(),
        rc::EXPECTED_CORPUS_FILES,
        "corpus size ratchet (ty::reject_corpus::EXPECTED_CORPUS_FILES)"
    );

    let mut hard = 0usize;
    let mut lenient = 0usize;
    let mut holes: Vec<String> = Vec::new();
    let mut code_gaps: Vec<String> = Vec::new();
    for r in &rows {
        if r.known_leniency {
            lenient += 1;
            continue;
        }
        hard += 1;
        if !r.rejected() {
            holes.push(r.name.clone());
            continue;
        }
        let missing = r.missing_codes();
        if !missing.is_empty() {
            code_gaps.push(format!(
                "{}: declared {:?}, observed {:?} (missing {:?})",
                r.name, r.declared_codes, r.observed_codes, missing
            ));
        }
    }

    assert!(
        holes.is_empty(),
        "SOUNDNESS HOLE — Rust checker ACCEPTED oracle-rejected program(s): {holes:?}"
    );
    assert!(
        code_gaps.is_empty(),
        "DIAGNOSTIC-CODE MISMATCH — rejected, but NOT by the declared code. Each \
         file is rejected for a different reason than its header claims; fix the \
         checker or correct the header deliberately, do NOT relax this gate:\n  {}",
        code_gaps.join("\n  ")
    );

    // The declared-code census ratchets: every corpus file SHOULD pin its
    // diagnostic code, and the files that do not are named here rather than
    // passing silently.
    rc::check_code_census(&rows).unwrap_or_else(|e| panic!("{e}"));
    let (with_code, without_code) = rc::code_census(&rows);
    eprintln!(
        "reject corpus: {hard} hard-gate programs all rejected; {lenient} documented leniency; \
         {with_code} pin a diagnostic code"
    );
    eprintln!(
        "reject corpus: {} file(s) declare NO diagnostic code (rejection is unpinned): {}",
        without_code.len(),
        without_code.join(", ")
    );

    assert_eq!(
        lenient,
        rc::EXPECTED_KNOWN_LENIENCY_FILES,
        "known-leniency ratchet (ty::reject_corpus::EXPECTED_KNOWN_LENIENCY_FILES)"
    );
    assert_eq!(
        hard,
        rc::EXPECTED_HARD_GATE_FILES,
        "hard-gate program count ratchet — an EXACT count, not a floor: deleting a \
         corpus file must fail here (ty::reject_corpus::EXPECTED_HARD_GATE_FILES)"
    );
}
