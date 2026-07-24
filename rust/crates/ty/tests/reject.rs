//! Reject-corpus regression test (doc 11 §2b; self-host §7 R1-D1).
//!
//! The accept-only differential oracle is structurally blind to soundness
//! regressions (an ill-typed program the Haskell rejects emits no Go). This
//! test closes that half: it runs the versioned rejection corpus
//! (`tests/reject/corpus/*.sky`) — each an ill-typed program the Haskell oracle
//! rejects with a specific diagnostic — through the Rust checker and asserts
//! every HARD-gate program is REJECTED (emits a type / name-resolution /
//! exhaustiveness diagnostic). Diagnostic-text parity is not required; REJECTION
//! is the hard requirement.
//!
//! Files tagged `-- gate: known-leniency` are documented resolver accept-parity
//! cases (see the file header) — verified present but not asserted rejected.
//!
//! The gate binary (`cargo run -p xtask -- reject`) shares this corpus; this
//! test is the `cargo nextest run` face of the same check.

use hir::SourceDb;
use std::path::{Path, PathBuf};

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

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for p in entries {
        let skip = p.components().any(|c| {
            matches!(
                c.as_os_str().to_str(),
                Some("sky-out") | Some(".skycache") | Some(".skydeps")
            )
        });
        if skip {
            continue;
        }
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

fn load_stdlib(root: &Path) -> Vec<(String, syntax::Parse)> {
    let mut files = Vec::new();
    collect_sky(&root.join("sky-stdlib"), &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .filter(|s| !s.is_empty())
            .unwrap_or_default();
        out.push((name, parse));
    }
    out
}

/// (rejected?, known_leniency?)
fn check(file: &Path, stdlib: &[(String, syntax::Parse)]) -> (bool, bool) {
    let src = std::fs::read_to_string(file).unwrap_or_default();
    let known_leniency = src
        .lines()
        .take(3)
        .any(|l| l.contains("gate: known-leniency"));

    let mut db = SourceDb::new();
    for (n, parse) in stdlib {
        db.add_module(n, parse.clone());
    }
    let parse = syntax::parse(&src, base::FileId(0));
    // Parser-recovery diagnostics (`[E0001]` class) — mirrors the driver's
    // parse-error gate (`crates/project/src/build.rs`): a syntactically broken
    // program the oracle rejects at parse time (e.g. a bare operator section
    // `(+)`) is REJECTED here too. Read BEFORE the parse is moved into the db.
    let parse_error = !parse.errors().is_empty() || parse.error_node_count() > 0;
    let mname = parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "Main".to_string());
    let mid = db.add_module(&mname, parse);
    let out = ty::check_modules(&db, &[mid]);
    let rejected = parse_error
        || out.type_errors > 0
        || out.name_errors > 0
        || out.exhaustiveness_warnings > 0;
    (rejected, known_leniency)
}

#[test]
fn rejection_corpus_is_rejected() {
    let root = repo_root();
    let stdlib = load_stdlib(&root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");

    let corpus_dir = root.join("rust/crates/ty/tests/reject/corpus");
    let mut files = Vec::new();
    collect_sky(&corpus_dir, &mut files);
    assert!(
        files.len() >= 13,
        "expected >= 13 corpus files, found {}",
        files.len()
    );

    let mut hard = 0usize;
    let mut lenient = 0usize;
    let mut holes: Vec<String> = Vec::new();
    for f in &files {
        let (rejected, known_leniency) = check(f, &stdlib);
        let name = f.file_name().unwrap().to_string_lossy().to_string();
        if known_leniency {
            lenient += 1;
            continue;
        }
        hard += 1;
        if !rejected {
            holes.push(name);
        }
    }

    assert!(
        holes.is_empty(),
        "SOUNDNESS HOLE — Rust checker ACCEPTED oracle-rejected program(s): {holes:?}"
    );
    assert!(hard >= 13, "expected >= 13 hard-gate programs, ran {hard}");
    eprintln!(
        "reject corpus: {hard} hard-gate programs all rejected; {lenient} documented leniency"
    );
}
