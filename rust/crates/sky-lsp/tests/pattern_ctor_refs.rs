//! A constructor used in a `case` PATTERN is a real use-site: hover shows its
//! type, and find-references / rename include it. Before recording the pattern
//! ctor as a ref, rename silently skipped the pattern occurrence → the renamed
//! file no longer compiled.

use sky_lsp::Analysis;
use std::path::{Path, PathBuf};
use tower_lsp::lsp_types::{HoverContents, Position, Url};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found")
        .to_path_buf()
}

fn url() -> Url {
    Url::from_file_path("/tmp/lsp-patctor/src/Main.sky").unwrap()
}

// 0-based lines. `Green` occurs at: decl (L2), PATTERN (L6), expr (L7).
const SRC: &str = "module Main exposing (main)\n\
                   import Std.Log exposing (println)\n\
                   type Color = Red | Green Int\n\
                   toInt c =\n\
                   \x20   case c of\n\
                   \x20       Red ->\n\
                   \x20           0\n\
                   \x20       Green n ->\n\
                   \x20           n\n\
                   main = println (String.fromInt (toInt (Green 5)))\n";

fn setup() -> (Analysis, Url) {
    let mut a = Analysis::new();
    a.load_stdlib(Some(&repo_root()));
    let u = url();
    a.set_document(u.clone(), SRC.to_string());
    (a, u)
}

#[test]
fn pattern_ctor_hover_resolves() {
    let (a, u) = setup();
    // `Green` in the pattern `Green n ->` (line 7, char 8).
    let h = match a.hover(
        &u,
        Position {
            line: 7,
            character: 8,
        },
    ) {
        Some(h) => match h.contents {
            HoverContents::Markup(m) => m.value,
            _ => String::new(),
        },
        None => String::new(),
    };
    assert!(
        h.contains("Green") && h.contains("Color"),
        "hover on a case-pattern constructor should show `Green : Int -> Color`, got: {h:?}"
    );
}

#[test]
fn pattern_ctor_is_found_by_references_and_rename() {
    let (a, u) = setup();
    // references from the DECL (line 2) must include the PATTERN occurrence (L7).
    let refs = a.references(
        &u,
        Position {
            line: 2,
            character: 20,
        },
        true,
    );
    let lines: Vec<u32> = refs.iter().map(|l| l.range.start.line).collect();
    assert!(
        lines.contains(&7),
        "find-references from the `Green` declaration must include the case-pattern \
         use on line 7 (else rename corrupts the file); got lines {lines:?}"
    );
    // rename must edit the pattern occurrence too.
    let edit = a
        .rename(
            &u,
            Position {
                line: 2,
                character: 20,
            },
            "Blue",
        )
        .expect("rename should produce an edit");
    let edited_lines: Vec<u32> = edit
        .changes
        .as_ref()
        .and_then(|c| c.values().next())
        .map(|es| es.iter().map(|e| e.range.start.line).collect())
        .unwrap_or_default();
    assert!(
        edited_lines.contains(&7),
        "rename `Green`->`Blue` must rewrite the pattern occurrence on line 7, \
         else the file no longer compiles; edited lines {edited_lines:?}"
    );
}
