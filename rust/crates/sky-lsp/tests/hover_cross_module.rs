//! Hover resolves the type of a reference to a def in ANOTHER user module —
//! including an UNANNOTATED one (the common case). Before the `def_loc`-owner
//! fallback, an unannotated cross-module value hovered as `?` because the
//! body-inference fallback only looked in the hovered document's bodies.

use sky_lsp::Analysis;
use std::path::{Path, PathBuf};
use tower_lsp::lsp_types::{HoverContents, Position, Url};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found above the crate")
        .to_path_buf()
}

fn url(name: &str) -> Url {
    Url::from_file_path(format!("/tmp/lsp-xmod-hover/src/{name}.sky")).unwrap()
}

fn hover_at(a: &Analysis, u: &Url, line: u32, ch: u32) -> String {
    match a.hover(u, Position { line, character: ch }) {
        Some(h) => match h.contents {
            HoverContents::Markup(m) => m.value,
            _ => String::new(),
        },
        None => String::new(),
    }
}

// Helper exports an ANNOTATED fn (`bump`), an ANNOTATED value (`greeting`), and
// an UNANNOTATED value (`tag`, inferred String).
const HELPER: &str = "module Helper exposing (greeting, bump, tag)\n\
                      \n\
                      greeting : String\n\
                      greeting = \"hi\"\n\
                      \n\
                      bump : Int -> Int\n\
                      bump n = n + 1\n\
                      \n\
                      tag = \"x\"\n";

// line 6: `x = String.fromInt (Helper.bump 41)`
// line 8: `main = println (Helper.greeting ++ Helper.tag)`
const MAIN: &str = "module Main exposing (main)\n\
                    import Sky.Core.Prelude exposing (..)\n\
                    import Sky.Core.String as String\n\
                    import Std.Log exposing (println)\n\
                    import Helper\n\
                    \n\
                    x = String.fromInt (Helper.bump 41)\n\
                    \n\
                    main = println (Helper.greeting ++ Helper.tag)\n";

#[test]
fn cross_module_unannotated_ref_hovers_inferred_type() {
    let mut a = Analysis::new();
    a.load_stdlib(Some(&repo_root()));
    a.set_document(url("Helper"), HELPER.to_string());
    let u = url("Main");
    a.set_document(u.clone(), MAIN.to_string());

    // `Helper.tag` at char 42 on line 8 — UNANNOTATED (`tag = "x"`), inferred String.
    let tag = hover_at(&a, &u, 8, 42);
    assert!(
        tag.contains("String"),
        "unannotated cross-module value `Helper.tag` must hover its INFERRED String type \
         (was `?` before the def_loc-owner fallback), got: {tag:?}"
    );

    // Regression guard: annotated cross-module + stdlib still resolve.
    let bump = hover_at(&a, &u, 6, 27); // Helper.bump : Int -> Int
    let greeting = hover_at(&a, &u, 8, 23); // Helper.greeting : String
    let from_int = hover_at(&a, &u, 6, 11); // String.fromInt : Int -> String
    assert!(bump.contains("Int"), "annotated cross-module fn regressed: {bump:?}");
    assert!(greeting.contains("String"), "annotated cross-module value regressed: {greeting:?}");
    assert!(
        from_int.contains("Int") && from_int.contains("String"),
        "stdlib ref regressed: {from_int:?}"
    );
}
