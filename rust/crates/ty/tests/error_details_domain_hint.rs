//! v0.28.0: `Sky.Core.Error.ErrorDetails` gained `Domain DomainFailure`, the one
//! open-ended variant every stdlib domain (Std.Ai first) and every app shares.
//! A program that matched every older variant stops compiling with `[E3001]`.
//! The diagnostic must say why and give the fix that never breaks again: a
//! `_ ->` branch or the generic helpers (`Error.domain`, `Error.isRetryable`,
//! `Error.retryAfter`). A non-exhaustive match on any other union keeps the
//! plain diagnostic, and a same-named user constructor does not trigger it.

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
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

fn e3001(main: &str) -> Vec<diagnostics::Diagnostic> {
    let mut files = Vec::new();
    collect_sky(&repo_root().join("sky-stdlib"), &mut files);
    let mut db = hir::SourceDb::new();
    for path in files {
        let src = std::fs::read_to_string(&path).unwrap();
        let parse = syntax::parse(&src, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .unwrap_or_default();
        if !name.is_empty() {
            db.add_module(&name, parse);
        }
    }
    let mid = db.add_module("Main", syntax::parse(main, base::FileId(1)));
    ty::check_modules(&db, &[mid])
        .diagnostics
        .into_iter()
        .filter(|d| d.code.0 == "E3001")
        .collect()
}

const OLD_MATCH: &str = "module Main exposing (main)\n\
    import Sky.Core.Prelude exposing (..)\n\
    import Sky.Core.Error as Error exposing (ErrorDetails(..))\n\
    import Std.Log exposing (println)\n\n\
    label : ErrorDetails -> String\n\
    label d =\n    case d of\n        FfiPanic p ->\n            p.message\n\n        TypeMismatch t ->\n            t.expected\n\n        HttpStatus n ->\n            String.fromInt n\n\n        JsonDecode s ->\n            s\n\n        Custom s ->\n            s\n\n\
    main =\n    println (label (Custom \"x\"))\n";

#[test]
fn a_match_written_before_domain_gets_the_open_union_hint() {
    let ds = e3001(OLD_MATCH);
    assert_eq!(ds.len(), 1, "one E3001 expected, got {ds:?}");
    let d = &ds[0];
    assert!(
        d.message.contains("Domain"),
        "message names the missing variant: {}",
        d.message
    );
    let hint = d.suggestion.as_deref().unwrap_or_default();
    for want in [
        "_ ->",
        "Error.domain",
        "Error.isRetryable",
        "Error.retryAfter",
        "v0.28",
    ] {
        assert!(hint.contains(want), "hint lacks {want:?}: {hint:?}");
    }
}

#[test]
fn the_same_match_with_a_wildcard_is_accepted() {
    let main = OLD_MATCH.replace(
        "        Custom s ->\n            s\n",
        "        Custom s ->\n            s\n\n        _ ->\n            \"other\"\n",
    );
    assert!(e3001(&main).is_empty(), "a `_ ->` branch covers Domain");
}

#[test]
fn another_union_keeps_the_plain_diagnostic() {
    let main = "module Main exposing (main)\n\
        import Sky.Core.Prelude exposing (..)\n\
        import Std.Log exposing (println)\n\n\
        type Shape\n    = Circle\n    | Domain\n\n\
        name : Shape -> String\n\
        name s =\n    case s of\n        Circle ->\n            \"c\"\n\n\
        main =\n    println (name Circle)\n";
    let ds = e3001(main);
    assert_eq!(ds.len(), 1, "{ds:?}");
    assert!(
        ds[0].suggestion.is_none(),
        "a user union named its constructor Domain: no hint, got {:?}",
        ds[0].suggestion
    );
}
