//! The editor on a bind line `p <- e` (docs/design/let-bind-lines.md §7):
//! hover on `<-` shows the `andThen` the type checker chose, with its type
//! and the type the pattern binds; go-to-definition on `<-` goes to that
//! `andThen`; `<-` is a keyword token; a bound name gets an inlay hint.

use sky_lsp::Analysis;
use std::path::{Path, PathBuf};
use tower_lsp::lsp_types::{
    HoverContents, InlayHintLabel, Position, Range, SemanticTokensResult, Url,
};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found above the crate")
        .to_path_buf()
}

fn main_url() -> Url {
    Url::from_file_path("/tmp/lsp-bind-lines/src/Main.sky").unwrap()
}

// line 6: `        n <- String.toInt s`
// line 7: `        m <- Just (n + 1)`
const MAIN: &str = "module Main exposing (main)\n\
                    import Sky.Core.Prelude exposing (..)\n\
                    import Sky.Core.String as String\n\
                    \n\
                    parse s =\n    \
                    let\n        \
                    n <- String.toInt s\n        \
                    m <- Just (n + 1)\n    \
                    in\n    \
                    Just m\n\
                    \n\
                    main = parse \"1\"\n";

fn analysis() -> Analysis {
    let mut a = Analysis::new();
    a.load_stdlib(Some(&repo_root()));
    a.set_document(main_url(), MAIN.to_string());
    a
}

fn at(line: u32, character: u32) -> Position {
    Position { line, character }
}

#[test]
fn hover_on_the_arrow_shows_the_chosen_and_then_and_the_bound_type() {
    let a = analysis();
    let h = match a.hover(&main_url(), at(6, 11)).map(|h| h.contents) {
        Some(HoverContents::Markup(m)) => m.value,
        other => panic!("no hover on `<-`: {other:?}"),
    };
    assert!(h.contains("Sky.Core.Maybe.andThen"), "{h}");
    assert!(h.contains("Maybe"), "the andThen type: {h}");
    assert!(h.contains("binds `Int`"), "the bound type: {h}");
}

#[test]
fn go_to_definition_on_the_arrow_goes_to_the_chosen_and_then() {
    let a = analysis();
    let loc = a
        .goto(&main_url(), at(7, 11))
        .expect("go-to-definition on `<-`");
    assert!(
        loc.uri.path().ends_with("Sky/Core/Maybe.sky"),
        "the chosen andThen is Sky.Core.Maybe.andThen: {}",
        loc.uri
    );
}

#[test]
fn the_arrow_is_a_keyword_token() {
    let a = analysis();
    let toks = match a.semantic_tokens(&main_url()) {
        Some(SemanticTokensResult::Tokens(t)) => t.data,
        _ => Vec::new(),
    };
    let (mut line, mut ch) = (0u32, 0u32);
    let mut found = None;
    for t in toks {
        if t.delta_line == 0 {
            ch += t.delta_start;
        } else {
            line += t.delta_line;
            ch = t.delta_start;
        }
        if (line, ch) == (6, 10) {
            found = Some(t.token_type);
        }
    }
    // Legend index 6 is KEYWORD (the frozen legend in lib.rs).
    assert_eq!(found, Some(6));
}

#[test]
fn a_bound_name_gets_an_inlay_hint() {
    let a = analysis();
    let whole = Range {
        start: at(0, 0),
        end: at(100, 0),
    };
    let hints: Vec<(u32, u32, String)> = a
        .inlay_hints(&main_url(), whole)
        .into_iter()
        .map(|h| {
            let label = match h.label {
                InlayHintLabel::String(s) => s,
                _ => String::new(),
            };
            (h.position.line, h.position.character, label)
        })
        .collect();
    assert!(
        hints.contains(&(6, 9, " : Int".to_string())),
        "`n` binds an Int: {hints:?}"
    );
    // `m` is bound from `Just (n + 1)` through `Maybe.andThen`, whose
    // annotation gives the declarations world its full scheme (phase G2).
    assert!(
        hints.contains(&(7, 9, " : Int".to_string())),
        "`m` binds an Int: {hints:?}"
    );
}

// line 5: `    String.toInt s |> Maybe.andThen (\x -> Just x)`
// line 9: `    Ok 1 |> Result.andThen (\y -> Ok y)`
const CHAIN: &str = "module Main exposing (main)\n\
                     import Sky.Core.Prelude exposing (..)\n\
                     import Sky.Core.String as String\n\
                     \n\
                     parse s =\n    \
                     String.toInt s |> Maybe.andThen (\\x -> Just x)\n\
                     \n\
                     \n\
                     step =\n    \
                     Ok 1 |> Result.andThen (\\y -> Ok y)\n\
                     \n\
                     main = parse \"1\"\n";

fn hover_text(a: &Analysis, url: &Url, line: u32, character: u32) -> String {
    match a.hover(url, at(line, character)).map(|h| h.contents) {
        Some(HoverContents::Markup(m)) => m.value,
        other => panic!("no hover at {line}:{character}: {other:?}"),
    }
}

/// A hand-written `Maybe.andThen` / `Result.andThen` chain: the
/// continuation's parameter and the chain itself show their precise types
/// (phase G2), not a bare type variable.
#[test]
fn a_hand_written_chain_shows_precise_types() {
    let mut a = Analysis::new();
    a.load_stdlib(Some(&repo_root()));
    let url = Url::from_file_path("/tmp/lsp-bind-lines-chain/src/Main.sky").unwrap();
    a.set_document(url.clone(), CHAIN.to_string());
    let line5 = CHAIN.lines().nth(5).unwrap();
    let x = line5.find("\\x").unwrap() as u32 + 1;
    let h = hover_text(&a, &url, 5, x);
    assert!(h.contains("Int"), "`x` is an Int: {h}");
    let p = hover_text(&a, &url, 4, 1);
    assert!(p.contains("Maybe Int"), "`parse` returns a Maybe Int: {p}");
    let line9 = CHAIN.lines().nth(9).unwrap();
    let y = line9.find("\\y").unwrap() as u32 + 1;
    let h = hover_text(&a, &url, 9, y);
    assert!(h.contains("Int"), "`y` is an Int: {h}");
}
