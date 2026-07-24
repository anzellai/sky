//! A function parameter (and any `Var` pattern binder) must record its LowerIdent
//! TOKEN span, not the enclosing pattern-node span (which includes the leading
//! whitespace). Recording the node span made rename/goto edit the space too, so
//! renaming `pick maybeVal` → `pick mv` produced `pickmv` and corrupted the file.

use sky_lsp::Analysis;
use std::path::{Path, PathBuf};
use tower_lsp::lsp_types::{Position, Url};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found")
        .to_path_buf()
}

// 0-based. Line 2 is `pick maybeVal =`; `maybeVal` starts at column 5 (after
// `pick ` = 5 chars). Line 3 uses `maybeVal`.
const SRC: &str = "module Main exposing (main)\n\
                   import Std.Log exposing (println)\n\
                   pick maybeVal =\n\
                   \x20   maybeVal\n\
                   main = println (String.fromInt (pick 4))\n";

#[test]
fn param_rename_span_starts_at_the_ident_not_the_space() {
    let mut a = Analysis::new();
    a.load_stdlib(Some(&repo_root()));
    let u = Url::from_file_path("/tmp/lsp-parambinder/src/Main.sky").unwrap();
    a.set_document(u.clone(), SRC.to_string());

    // Rename from the USE site on line 3 (char 4 = the `m` of `maybeVal`).
    let edit = a
        .rename(
            &u,
            Position {
                line: 3,
                character: 4,
            },
            "mv",
        )
        .expect("rename should produce an edit");
    let edits = edit
        .changes
        .as_ref()
        .and_then(|c| c.values().next())
        .cloned()
        .unwrap_or_default();

    // The DECLARATION edit (line 2) must start at column 5 (the `m`), NOT 4 (the
    // space after `pick`) — else applying it yields `pickmv =`.
    let decl = edits
        .iter()
        .find(|e| e.range.start.line == 2)
        .expect("rename must edit the parameter declaration on line 2");
    assert_eq!(
        decl.range.start.character, 5,
        "param-binder rename span must start at the ident (col 5), not eat the \
         leading space (col 4) — got {decl:?}"
    );
    // And it must not extend into `pick` (start > end of `pick `).
    assert!(
        decl.range.start.character < decl.range.end.character,
        "decl edit range must be non-empty over the ident: {decl:?}"
    );
}
