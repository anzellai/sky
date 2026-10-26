//! `[E1017]` CONSTRUCTOR CLASH: a bare constructor that an import binds over a
//! DIFFERENT constructor of the same name is refused at the use site.
//!
//! The case that found it: `import Sky.Config as Config exposing (LogLevel(..))`
//! binds `LogLevel`'s `Error` over the Prelude's `Error`. A bare `Error kind
//! info` then meant `LogLevel.Error`, and the user got `type mismatch: LogLevel
//! vs ErrorKind -> _` with no mention of the import. Every rejection here has an
//! accepted twin, so the pair cannot be satisfied by a resolver that rejects
//! everything.

use base::FileId;
use hir::{resolve, ResolveResult, SourceDb};
use std::path::{Path, PathBuf};

fn collect(dir: &Path, out: &mut Vec<PathBuf>) {
    for e in std::fs::read_dir(dir).expect("read sky-stdlib").flatten() {
        let p = e.path();
        if p.is_dir() {
            collect(&p, out);
        } else if p.extension().is_some_and(|x| x == "sky") {
            out.push(p);
        }
    }
}

/// The real stdlib plus the given modules, resolved at `Main`.
fn resolve_main(modules: &[(&str, &str)]) -> ResolveResult {
    let mut root = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !root.join("sky-stdlib").is_dir() {
        assert!(root.pop(), "no sky-stdlib ancestor");
    }
    let mut files = Vec::new();
    collect(&root.join("sky-stdlib"), &mut files);
    let mut db = SourceDb::new();
    for f in files {
        let src = std::fs::read_to_string(&f).expect("read stdlib file");
        let parse = syntax::parse(&src, FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .expect("stdlib module header");
        db.add_module(&name, parse);
    }
    for (name, src) in modules {
        db.add_module(name, syntax::parse(src, FileId(0)));
    }
    resolve(&db, db.module_by_name("Main").expect("Main registered"))
}

fn codes(r: &ResolveResult, code: &str) -> Vec<String> {
    r.diagnostics
        .iter()
        .filter(|d| d.code.0 == code)
        .map(|d| d.message.clone())
        .collect()
}

const HEAD: &str = "module Main exposing (main)\n\n\
                    import Sky.Core.Prelude exposing (..)\n\
                    import Sky.Core.Error as E\n\
                    import Sky.Config as Config exposing (LogLevel(..))\n\n\n";

fn main_with(body: &str) -> String {
    format!("{HEAD}{body}")
}

#[test]
fn a_bare_clashing_constructor_in_an_expression_is_refused() {
    let r = resolve_main(&[(
        "Main",
        &main_with("bad : E.Error\nbad =\n    Error E.Io { message = \"x\", details = Nothing }\n\n\nmain =\n    bad\n"),
    )]);
    let msgs = codes(&r, "E1017");
    assert_eq!(msgs.len(), 1, "{:?}", r.diagnostics);
    let m = &msgs[0];
    assert!(m.contains("`Sky.Config`"), "{m}");
    assert!(m.contains("`Sky.Core.Error`"), "{m}");
    assert!(m.contains("`E.Error`"), "{m}");
    assert!(m.contains("`Config.Error`"), "{m}");
    // The site points at the reference, not at the import line.
    let d = r.diagnostics.iter().find(|d| d.code.0 == "E1017").unwrap();
    assert_eq!(d.labels.len(), 1, "{d:?}");
}

#[test]
fn a_bare_clashing_constructor_in_a_pattern_is_refused() {
    let r = resolve_main(&[(
        "Main",
        &main_with(
            "name : LogLevel -> String\nname lvl =\n    case lvl of\n        Error ->\n            \"error\"\n\n        _ ->\n            \"other\"\n\n\nmain =\n    name Info\n",
        ),
    )]);
    let msgs = codes(&r, "E1017");
    assert_eq!(msgs.len(), 1, "{:?}", r.diagnostics);
    assert!(msgs[0].contains("`Error`"), "{}", msgs[0]);
}

#[test]
fn the_qualified_forms_are_accepted() {
    let r = resolve_main(&[(
        "Main",
        &main_with(
            "name : LogLevel -> String\nname lvl =\n    case lvl of\n        Config.Error ->\n            \"error\"\n\n        _ ->\n            \"other\"\n\n\nlvl : LogLevel\nlvl =\n    Config.Error\n\n\nerr : E.Error\nerr =\n    E.Error E.Io { message = \"x\", details = Nothing }\n\n\nmain =\n    name lvl\n",
        ),
    )]);
    assert!(codes(&r, "E1017").is_empty(), "{:?}", r.diagnostics);
    assert!(
        r.diagnostics
            .iter()
            .all(|d| d.severity != diagnostics::Severity::Error),
        "{:?}",
        r.diagnostics
    );
}

#[test]
fn an_unused_clashing_import_is_accepted() {
    // Real apps write exactly this and use
    // only `Info`.
    let r = resolve_main(&[(
        "Main",
        &main_with("lvl : LogLevel\nlvl =\n    Info\n\n\nmain =\n    lvl\n"),
    )]);
    assert!(codes(&r, "E1017").is_empty(), "{:?}", r.diagnostics);
}

#[test]
fn a_local_constructor_still_wins_silently() {
    // doc 05 C7: a local definition shadows an import with no diagnostic.
    let r = resolve_main(&[(
        "Main",
        &main_with(
            "type Level\n    = Warn\n    | Fatal\n\n\nlvl : Level\nlvl =\n    Warn\n\n\nmain =\n    lvl\n",
        ),
    )]);
    assert!(codes(&r, "E1017").is_empty(), "{:?}", r.diagnostics);
}

#[test]
fn the_builtin_constructors_are_not_a_clash() {
    // `Just` / `Ok` / `True` are the Prelude's spelling of the stdlib's own
    // constructors; importing `Maybe(..)` restates them.
    let main = "module Main exposing (main)\n\n\
                import Sky.Core.Prelude exposing (..)\n\
                import Sky.Core.Maybe exposing (Maybe(..))\n\
                import Sky.Core.Result exposing (Result(..))\n\n\n\
                f : Maybe Int -> Result String Int\nf m =\n    case m of\n        Just n ->\n            Ok n\n\n        Nothing ->\n            Err \"none\"\n\n\n\
                main =\n    f (Just 1)\n";
    let r = resolve_main(&[("Main", main)]);
    assert!(codes(&r, "E1017").is_empty(), "{:?}", r.diagnostics);
}

#[test]
fn an_explicit_import_over_an_open_one_is_refused_and_one_layer_stays_e1012() {
    let a = "module Lib.A exposing (..)\n\ntype Colour\n    = Red\n    | Green\n";
    let b = "module Lib.B exposing (..)\n\ntype Light\n    = Red\n    | Amber\n";
    // Explicit `exposing (Colour(..))` over an open `exposing (..)`.
    let mixed = "module Main exposing (main)\n\n\
                 import Lib.A as A exposing (Colour(..))\n\
                 import Lib.B as B exposing (..)\n\n\n\
                 main =\n    Red\n";
    let r = resolve_main(&[("Lib.A", a), ("Lib.B", b), ("Main", mixed)]);
    let msgs = codes(&r, "E1017");
    assert_eq!(msgs.len(), 1, "{:?}", r.diagnostics);
    assert!(
        msgs[0].contains("`A.Red`") && msgs[0].contains("`B.Red`"),
        "{}",
        msgs[0]
    );
    // Accepted twin: the constructor only one import binds.
    let twin = mixed.replace("    Red\n", "    Amber\n");
    let r = resolve_main(&[("Lib.A", a), ("Lib.B", b), ("Main", &twin)]);
    assert!(codes(&r, "E1017").is_empty(), "{:?}", r.diagnostics);
    // Two imports in ONE layer remain `[E1012]`, not `[E1017]`.
    let same = mixed.replace("exposing (Colour(..))", "exposing (..)");
    let r = resolve_main(&[("Lib.A", a), ("Lib.B", b), ("Main", &same)]);
    assert_eq!(codes(&r, "E1012").len(), 1, "{:?}", r.diagnostics);
    assert!(codes(&r, "E1017").is_empty(), "{:?}", r.diagnostics);
}
