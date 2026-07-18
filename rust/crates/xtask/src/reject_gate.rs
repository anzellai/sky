//! `xtask reject` — the accept/REJECT-parity gate (doc 11 §2b; self-host §7
//! R1-D1). The M3 differential oracle is ACCEPT-only: it compares emitted Go on
//! programs both compilers accept, and is structurally blind to soundness
//! regressions (an ill-typed program the Haskell rejects emits no Go → nothing
//! to byte-compare). This gate closes that half.
//!
//! It runs a versioned corpus of ILL-TYPED Sky programs
//! (`crates/ty/tests/reject/corpus/*.sky`) — each a single defect the HASKELL
//! ORACLE rejects with a specific diagnostic — through the Rust checker and
//! asserts EVERY one is REJECTED (emits at least one rejecting diagnostic).
//!
//! "Rejected" = the Rust checker surfaces any of:
//!   * a TYPE error (unify clash — the `[E2001]` class), OR
//!   * a NAME-resolution error (unresolved name — the `[E1001]` class), OR
//!   * an EXHAUSTIVENESS error (`[E3001]` — Sky treats a non-exhaustive `case`
//!     as a hard error, stronger than GHC-as-configured; self-host R1-D3).
//!
//! Diagnostic-text parity is NOT required (the rewrite may improve prose);
//! REJECTION is the hard requirement (doc 11 §2b).

use hir::SourceDb;
use std::path::{Path, PathBuf};

struct Row {
    name: String,
    type_errors: usize,
    name_errors: usize,
    exhaustiveness: usize,
    /// A file tagged `-- gate: known-leniency` is a program the ORACLE rejects
    /// but the Rust checker deliberately accepts for a documented accept-parity
    /// reason (see the file's header). Tracked + reported, but NOT counted
    /// against the hard reject gate.
    known_leniency: bool,
    first_msg: String,
}

impl Row {
    fn rejected(&self) -> bool {
        self.type_errors > 0 || self.name_errors > 0 || self.exhaustiveness > 0
    }
    /// Which signal caught it (for the report).
    fn signal(&self) -> &'static str {
        if self.type_errors > 0 {
            "type"
        } else if self.name_errors > 0 {
            "name"
        } else if self.exhaustiveness > 0 {
            "exhaustive"
        } else {
            "-"
        }
    }
}

pub fn run(_args: &[String], root: &Path) -> i32 {
    let stdlib = load_dir(&root.join("sky-stdlib"), "sky-stdlib");
    if stdlib.is_empty() {
        eprintln!("reject: no stdlib modules under {}/sky-stdlib", root.display());
        return 1;
    }

    let corpus_dir = root.join("rust/crates/ty/tests/reject/corpus");
    let mut files: Vec<PathBuf> = match std::fs::read_dir(&corpus_dir) {
        Ok(rd) => rd
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.extension().and_then(|e| e.to_str()) == Some("sky"))
            .collect(),
        Err(e) => {
            eprintln!("reject: cannot read corpus dir {}: {e}", corpus_dir.display());
            return 1;
        }
    };
    files.sort();
    if files.is_empty() {
        eprintln!("reject: empty corpus under {}", corpus_dir.display());
        return 1;
    }

    let mut rows: Vec<Row> = Vec::new();
    for f in &files {
        rows.push(check_one(f, &stdlib));
    }

    // ---- report ----
    let w = rows.iter().map(|r| r.name.len()).max().unwrap_or(8).max(8);
    println!(
        "{:<w$}  {:>6}  {:>6}  {:>6}  {:>9}  SIGNAL / first diagnostic",
        "DEFECT FILE", "TYPE", "NAME", "EXHST", "VERDICT",
        w = w
    );
    println!("{}", "-".repeat(w + 60));
    let mut hard_total = 0usize;
    let mut hard_rejected = 0usize;
    for r in &rows {
        let verdict = if r.known_leniency {
            if r.rejected() { "reject*" } else { "LENIENT*" }
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
        println!(
            "{:<w$}  {:>6}  {:>6}  {:>6}  {:>9}  {} {}",
            r.name, r.type_errors, r.name_errors, r.exhaustiveness, verdict, r.signal(), r.first_msg,
            w = w
        );
    }
    println!("{}", "-".repeat(w + 60));
    let lenient: Vec<&str> = rows
        .iter()
        .filter(|r| r.known_leniency)
        .map(|r| r.name.as_str())
        .collect();
    println!("REJECT-PARITY: rust rejects {hard_rejected}/{hard_total} ill-typed programs (hard gate)");
    if !lenient.is_empty() {
        println!(
            "  * {} documented resolver-leniency case(s) (oracle rejects, Rust accepts by design — see file header): {}",
            lenient.len(),
            lenient.join(", ")
        );
    }
    if hard_rejected == hard_total {
        println!("REJECT GATE: PASS  (every hard-gate ill-typed program is rejected by the Rust checker)");
        0
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
        1
    }
}

fn check_one(file: &Path, stdlib: &[(String, syntax::Parse)]) -> Row {
    let name = file
        .file_name()
        .and_then(|s| s.to_str())
        .unwrap_or("?")
        .to_string();

    let mut db = SourceDb::new();
    for (n, parse) in stdlib {
        db.add_module(n, parse.clone());
    }
    let src = std::fs::read_to_string(file).unwrap_or_default();
    let known_leniency = src
        .lines()
        .take(3)
        .any(|l| l.contains("gate: known-leniency"));
    let parse = syntax::parse(&src, base::FileId(0));
    let mname = parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "Main".to_string());
    let mid = db.add_module(&mname, parse);
    let out = ty::check_modules(&db, &[mid]);

    let first_msg = out
        .diagnostics
        .iter()
        .find(|d| {
            d.severity == diagnostics::Severity::Error
                || d.code.0 == "E3001"
        })
        .map(|d| {
            let m = d.message.replace('\n', " ");
            let m: String = m.chars().take(70).collect();
            format!("[{}] {}", d.code.0, m)
        })
        .unwrap_or_default();

    Row {
        name,
        type_errors: out.type_errors,
        name_errors: out.name_errors,
        exhaustiveness: out.exhaustiveness_warnings,
        known_leniency,
        first_msg,
    }
}

// ---- module loading (mirrors infer_gate) ---------------------------------

fn load_dir(dir: &Path, root_marker: &str) -> Vec<(String, syntax::Parse)> {
    let mut files = Vec::new();
    collect_sky(dir, &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let name = module_name(&parse, &path, root_marker);
        out.push((name, parse));
    }
    out
}

fn module_name(parse: &syntax::Parse, path: &Path, root_marker: &str) -> String {
    let tree = parse.tree();
    if let Some(n) = tree.module_header().and_then(|h| h.name()).map(|n| n.text()) {
        if !n.is_empty() {
            return n;
        }
    }
    let comps: Vec<&str> = path.iter().filter_map(|c| c.to_str()).collect();
    let start = comps
        .iter()
        .rposition(|c| *c == root_marker)
        .map(|i| i + 1)
        .unwrap_or(0);
    let mut segs: Vec<String> = comps[start..].iter().map(|s| s.to_string()).collect();
    if let Some(last) = segs.last_mut() {
        *last = last.trim_end_matches(".sky").to_string();
    }
    segs.join(".")
}

fn is_generated(path: &Path) -> bool {
    path.components().any(|c| {
        matches!(
            c.as_os_str().to_str(),
            Some("sky-out") | Some(".skycache") | Some(".skydeps")
        )
    })
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let mut entries: Vec<PathBuf> = match std::fs::read_dir(dir) {
        Ok(rd) => rd.filter_map(|e| e.ok().map(|e| e.path())).collect(),
        Err(_) => return,
    };
    entries.sort();
    for path in entries {
        if is_generated(&path) {
            continue;
        }
        if path.is_dir() {
            collect_sky(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}
