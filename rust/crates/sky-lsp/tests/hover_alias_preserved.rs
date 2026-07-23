//! Hover on a value with a DECLARED type annotation shows the alias the user
//! wrote (`describe : User -> String`), not the alias-EXPANDED record. The `ty`
//! layer expands aliases eagerly for unification, so hover reads the annotation's
//! CST text instead. This also keeps record fields in written order (the
//! inferred `Ty::Record` is alpha-sorted for row-poly unify), removing the
//! signature-vs-parameter ordering inconsistency.

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

const SRC: &str = "module Main exposing (main)\n\
                   import Std.Log exposing (println)\n\
                   type alias User = { name : String, age : Int }\n\
                   describe : User -> String\n\
                   describe u =\n\
                   \x20   u.name\n\
                   main = println (describe { name = \"a\", age = 1 })\n";

fn hover_text(a: &Analysis, u: &Url, line: u32, character: u32) -> String {
    match a.hover(u, Position { line, character }) {
        Some(h) => match h.contents {
            HoverContents::Markup(m) => m.value,
            _ => String::new(),
        },
        None => String::new(),
    }
}

#[test]
fn hover_on_annotated_value_shows_the_alias_name() {
    let mut a = Analysis::new();
    a.load_stdlib(Some(&repo_root()));
    let u = Url::from_file_path("/tmp/lsp-alias/src/Main.sky").unwrap();
    a.set_document(u.clone(), SRC.to_string());

    // Hover on `describe` at its declaration (line 3, `describe : User -> String`).
    let h = hover_text(&a, &u, 3, 2);
    assert!(
        h.contains("User -> String"),
        "hover should show the written alias `User -> String`, got: {h:?}"
    );
    assert!(
        !h.contains("{ name"),
        "hover must NOT expand the `User` alias to its record form, got: {h:?}"
    );
}
