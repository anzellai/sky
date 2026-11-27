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
    assert!(
        hints.contains(&(7, 9, " : Int".to_string())),
        "`m` binds an Int: {hints:?}"
    );
}
