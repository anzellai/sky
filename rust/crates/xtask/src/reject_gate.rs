//! `xtask reject` — the accept/REJECT-parity gate (doc 11 §2b; self-host §7
//! R1-D1). The M3 differential oracle is ACCEPT-only: it compares emitted Go on
//! programs both compilers accept, and is structurally blind to soundness
//! regressions (an ill-typed program the Haskell rejects emits no Go → nothing
//! to byte-compare). This gate closes that half.
//!
//! It runs a versioned corpus of ILL-TYPED Sky programs
//! (`crates/ty/tests/reject/corpus/**.sky`) — each a single defect the HASKELL
//! ORACLE rejects with a specific diagnostic — through the Rust checker and
//! asserts EVERY one is REJECTED, and that the rejection carries the diagnostic
//! CODE the file's header declares.
//!
//! **This gate owns NO criterion of its own.** "Rejected", corpus discovery, the
//! exact corpus size, and the declared-code rule live in [`ty::reject_corpus`] —
//! the single declaration shared with the `cargo test -p ty --test reject` face.
//! The two used to carry private copies and drifted (v2 §1.5): the parse-error
//! criterion disagreed on severity, discovery disagreed on recursion, and both
//! floors had gone stale. Read the criterion there; do not restate it here.

use std::path::Path;
use ty::reject_corpus as rc;

pub(crate) type Row = rc::Verdict;

/// Run the reject corpus and return one [`Row`] per file.
///
/// Extracted so the gate harness (`xtask harness`) consults the SAME corpus
/// discovery and the SAME rejection criterion as the CLI gate. v2 §10 keeps
/// "one `corpus()` / `collect_sky` / `load_dir`" for a measured reason: extra
/// copies are how two gates silently come to disagree about what the corpus is.
pub(crate) fn scan(root: &Path) -> Result<Vec<Row>, String> {
    rc::scan(root)
}

pub fn run(_args: &[String], root: &Path) -> i32 {
    let rows = match scan(root) {
        Ok(r) => r,
        Err(msg) => {
            eprintln!("{msg}");
            return 1;
        }
    };

    // ---- report ----
    let w = rows.iter().map(|r| r.name.len()).max().unwrap_or(8).max(8);
    println!(
        "{:<w$}  {:>6}  {:>6}  {:>6}  {:>6}  {:>9}  {:>10}  SIGNAL / first diagnostic",
        "DEFECT FILE",
        "TYPE",
        "NAME",
        "EXHST",
        "PARSE",
        "VERDICT",
        "CODE",
        w = w
    );
    println!("{}", "-".repeat(w + 72));
    let mut hard_total = 0usize;
    let mut hard_rejected = 0usize;
    let mut code_gaps: Vec<String> = Vec::new();
    for r in &rows {
        let verdict = if r.known_leniency {
            if r.rejected() {
                "reject*"
            } else {
                "LENIENT*"
            }
        } else if r.rejected() {
            "REJECT"
        } else {
            "ACCEPT!!"
        };
        if !r.known_leniency {
            hard_total += 1;
            if r.rejected() {
                hard_rejected += 1;
            }
        }
        let missing = r.missing_codes();
        let code_col = if r.declared_codes.is_empty() {
            "unpinned".to_string()
        } else if missing.is_empty() {
            r.declared_codes.join("+")
        } else {
            format!("WRONG:{}", missing.join("+"))
        };
        if !r.known_leniency && r.rejected() && !missing.is_empty() {
            code_gaps.push(format!(
                "{}: declared {:?}, observed {:?} (missing {:?})",
                r.name, r.declared_codes, r.observed_codes, missing
            ));
        }
        println!(
            "{:<w$}  {:>6}  {:>6}  {:>6}  {:>6}  {:>9}  {:>10}  {} {}",
            r.name,
            r.type_errors,
            r.name_errors,
            r.exhaustiveness,
            r.parse_errors,
            verdict,
            code_col,
            r.signal(),
            r.first_msg,
            w = w
        );
    }
    println!("{}", "-".repeat(w + 72));
    let lenient: Vec<&str> = rows
        .iter()
        .filter(|r| r.known_leniency)
        .map(|r| r.name.as_str())
        .collect();
    println!(
        "REJECT-PARITY: rust rejects {hard_rejected}/{hard_total} ill-typed programs (hard gate)"
    );
    if !lenient.is_empty() {
        println!(
            "  * {} documented resolver-leniency case(s) (oracle rejects, Rust accepts by design — see file header): {}",
            lenient.len(),
            lenient.join(", ")
        );
    }

    // ---- declared-code census (ratchets; unpinned files are NAMED) ----
    let (with_code, without_code) = rc::code_census(&rows);
    println!(
        "CODE-PARITY: {with_code}/{} file(s) pin a diagnostic code in their `oracle: reject [E….]` header",
        rows.len()
    );
    if !without_code.is_empty() {
        println!(
            "  * {} file(s) declare NO code — rejection is UNPINNED (any diagnostic satisfies them): {}",
            without_code.len(),
            without_code.join(", ")
        );
    }
    let census = rc::check_code_census(&rows);

    let mut failed = false;
    if hard_rejected == hard_total {
        println!("REJECT GATE: PASS  (every hard-gate ill-typed program is rejected by the Rust checker)");
    } else {
        let holes: Vec<&str> = rows
            .iter()
            .filter(|r| !r.known_leniency && !r.rejected())
            .map(|r| r.name.as_str())
            .collect();
        println!(
            "REJECT GATE: FAIL  ({} soundness hole(s): {})",
            hard_total - hard_rejected,
            holes.join(", ")
        );
        failed = true;
    }
    if !code_gaps.is_empty() {
        println!(
            "CODE GATE: FAIL  ({} file(s) rejected, but NOT by the declared code — the \
             file exercises a different defect than its header claims):",
            code_gaps.len()
        );
        for g in &code_gaps {
            println!("  {g}");
        }
        failed = true;
    } else {
        println!("CODE GATE: PASS  (every declared diagnostic code was observed)");
    }
    if hard_total != rc::EXPECTED_HARD_GATE_FILES {
        println!(
            "COUNT GATE: FAIL  (expected EXACTLY {} hard-gate programs, ran {hard_total})",
            rc::EXPECTED_HARD_GATE_FILES
        );
        failed = true;
    }
    if let Err(msg) = census {
        println!("CENSUS GATE: FAIL  {msg}");
        failed = true;
    }

    if failed {
        1
    } else {
        0
    }
}

// ---- module loading -------------------------------------------------------

/// Recursive `.sky` loading for other gates (`corpus_bench`, `shared_world`).
///
/// The shared-world differential harness must load the stdlib and each example's
/// `src/` tree through the SAME discovery this gate uses, or the two paths would
/// be comparing different corpora — the exact failure v2 §1.5 catalogues.
pub(crate) fn load_dir_pub(dir: &Path, root_marker: &str) -> Vec<(String, syntax::Parse)> {
    rc::load_dir(dir, root_marker)
}
