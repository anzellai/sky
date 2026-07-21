//! In-process coverage for `workspace/symbol` (multi-file navigation — the
//! symbol picker the SkyDeploy dashboard relies on). Driven directly against the
//! `sky_lsp::Analysis` engine (the same path `main.rs`'s `symbol` handler calls),
//! across a TWO-module workspace so the cross-file enumeration is exercised.

use sky_lsp::Analysis;
use std::path::{Path, PathBuf};
use tower_lsp::lsp_types::{SymbolKind, Url};

// Two modules, each with its own `update` — the classic TEA shape. `workspace/
// symbol("upd")` must surface BOTH, each pointing at its own file.
const COUNTER: &str = "module Counter exposing (update, Model)\n\ntype alias Model = { count : Int }\n\nupdate : Int -> Model -> Model\nupdate delta model =\n    { model | count = model.count + delta }\n";
const TIMER: &str = "module Timer exposing (update, reset)\n\nupdate : Int -> Int -> Int\nupdate tick acc =\n    acc + tick\n\nreset : Int -> Int\nreset _ =\n    0\n";

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found above the crate")
        .to_path_buf()
}

fn url(name: &str) -> Url {
    Url::from_file_path(format!("/tmp/lsp-rust-ws-symbol/src/{name}.sky")).unwrap()
}

fn engine() -> Analysis {
    let mut a = Analysis::new();
    a.load_stdlib(Some(&repo_root()));
    a.set_document(url("Counter"), COUNTER.to_string());
    a.set_document(url("Timer"), TIMER.to_string());
    a
}

#[test]
fn workspace_symbol_finds_update_across_two_modules() {
    let a = engine();
    let syms = a.workspace_symbol("upd");

    // The `update` def from BOTH modules must be present.
    let updates: Vec<_> = syms.iter().filter(|s| s.name == "update").collect();
    assert_eq!(
        updates.len(),
        2,
        "expected `update` from both Counter + Timer; got {:?}",
        syms.iter()
            .map(|s| (&s.name, s.location.uri.as_str()))
            .collect::<Vec<_>>()
    );

    // Each points at its own file's declaration line.
    let counter_hit = updates
        .iter()
        .find(|s| s.location.uri == url("Counter"))
        .expect("update in Counter.sky");
    assert_eq!(
        counter_hit.location.range.start.line, 5,
        "Counter.update value-def site is on line 5 (0-based)"
    );
    assert_eq!(counter_hit.kind, SymbolKind::FUNCTION);

    let timer_hit = updates
        .iter()
        .find(|s| s.location.uri == url("Timer"))
        .expect("update in Timer.sky");
    assert_eq!(
        timer_hit.location.range.start.line, 3,
        "Timer.update value-def site is on line 3 (0-based)"
    );
}

#[test]
fn workspace_symbol_subsequence_and_kinds() {
    let a = engine();
    // Subsequence match: `Mdl` matches `Model` (the type alias in Counter).
    let syms = a.workspace_symbol("Mdl");
    let model = syms.iter().find(|s| s.name == "Model");
    assert!(
        model.is_some(),
        "subsequence `Mdl` should match `Model`; got {syms:?}"
    );
    assert_eq!(
        model.unwrap().kind,
        SymbolKind::STRUCT,
        "type alias → STRUCT"
    );

    // `reset` (Timer value) is reachable by name.
    let r = a.workspace_symbol("reset");
    assert!(
        r.iter()
            .any(|s| s.name == "reset" && s.location.uri == url("Timer")),
        "reset should be found in Timer.sky; got {r:?}"
    );
}

#[test]
fn workspace_symbol_empty_query_returns_many() {
    let a = engine();
    // An empty query returns everything (stdlib is loaded), capped at the engine
    // limit — so we get a large-but-bounded set that includes our own symbols.
    let all = a.workspace_symbol("");
    assert!(
        all.len() > 3,
        "empty query returns multiple symbols; got {}",
        all.len()
    );
    assert!(
        all.len() <= 256,
        "empty query result is capped at 256; got {}",
        all.len()
    );
    // Our project symbols are enumerated too (Counter/Timer load before the loop
    // hits the cap for a two-module project + stdlib — they are registered last).
    // Guard only that the cap is a bound, not that every project symbol survives
    // it; the targeted queries above prove per-symbol reachability.
}
