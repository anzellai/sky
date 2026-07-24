//! A4 (v1-blocker closure): the checker REJECTS app-module import cycles
//! (Elm-like posture). A cycle among unannotated defs otherwise leaves them at
//! wildcard flex, defeating the `go build` backstop. Diagnostic code E1010.

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

fn check_two(a_src: &str, b_src: &str) -> ty::CheckOutput {
    let root = repo_root();
    let stdlib = load_stdlib(&root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");
    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    let a = db.add_module("A", syntax::parse(a_src, base::FileId(0)));
    let b = db.add_module("B", syntax::parse(b_src, base::FileId(0)));
    ty::check_modules(&db, &[a, b])
}

fn has_e1010(out: &ty::CheckOutput) -> bool {
    out.diagnostics.iter().any(|d| d.code.0 == "E1010")
}

#[test]
fn direct_import_cycle_is_rejected() {
    // A imports B, B imports A → a 2-module cycle.
    let a = "module A exposing (aVal)\nimport B exposing (bVal)\naVal = bVal ++ \"a\"\n";
    let b = "module B exposing (bVal)\nimport A exposing (aVal)\nbVal = \"b\"\n";
    let out = check_two(a, b);
    assert!(out.name_errors > 0, "an import cycle must be an error");
    assert!(has_e1010(&out), "cycle must be reported with code E1010");
}

#[test]
fn acyclic_two_modules_are_accepted() {
    // A imports B; B imports nothing app-level → NO cycle, no E1010.
    let a = "module A exposing (aVal)\nimport B exposing (bVal)\naVal = bVal ++ \"a\"\n";
    let b = "module B exposing (bVal)\nbVal = \"b\"\n";
    let out = check_two(a, b);
    assert!(
        !has_e1010(&out),
        "an acyclic import must NOT be flagged as a cycle"
    );
}
