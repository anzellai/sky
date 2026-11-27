//! v0.32.0 phase B: bind lines (`p <- e`) parse and resolve, but the type
//! checker does not choose their `andThen` yet (docs/design/let-bind-lines.md
//! §4). Until it does, a bind line is a hard error, never a fresh type that a
//! build would lower to wrong code. This file goes when phase C lands.

use std::path::PathBuf;
use std::sync::OnceLock;

fn stdlib() -> &'static Vec<(String, syntax::Parse)> {
    static S: OnceLock<Vec<(String, syntax::Parse)>> = OnceLock::new();
    S.get_or_init(|| {
        let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        while !dir.join("sky-stdlib").is_dir() {
            assert!(dir.pop(), "no sky-stdlib ancestor");
        }
        ty::reject_corpus::load_stdlib(&dir)
    })
}

fn errors(body: &str) -> Vec<String> {
    let src = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.Task as Task\n\
         import Std.Log exposing (println)\n\n\n\
         {body}\n\n\nmain =\n    println \"x\"\n"
    );
    let mut db = hir::SourceDb::new();
    for (n, p) in stdlib() {
        db.add_module(n, p.clone());
    }
    let m = db.add_module("Main", syntax::parse(&src, base::FileId(0)));
    ty::check_modules(&db, &[m])
        .diagnostics
        .into_iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .map(|d| d.message)
        .collect()
}

#[test]
fn a_bind_line_is_refused_until_the_checker_types_it() {
    let got = errors(
        "step : Task Error Int -> Task Error Int\nstep t =\n    let\n        n <- t\n    in\n    Task.succeed (n + 1)",
    );
    assert!(
        got.iter()
            .any(|m| m.contains("internal compiler error") && m.contains("cannot type them yet")),
        "{got:?}"
    );
    // The same block written with `andThen` checks.
    let got = errors(
        "step : Task Error Int -> Task Error Int\nstep t =\n    t |> Task.andThen (\\n -> Task.succeed (n + 1))",
    );
    assert!(got.is_empty(), "{got:?}");
}
