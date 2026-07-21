//! In-process coverage for `textDocument/codeAction` (the one LSP parity gap
//! vs the Haskell server — `Server.hs:2346`). Two v1 quick-fixes: "Add type
//! annotation" on an unannotated top-level value, and "Organize imports". Driven
//! directly against `sky_lsp::Analysis`, the same path `tower-lsp` calls.

use sky_lsp::Analysis;
use std::path::{Path, PathBuf};
use tower_lsp::lsp_types::{
    CodeActionContext, CodeActionKind, CodeActionOrCommand, Position, Range, TextEdit, Url,
};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found above the crate")
        .to_path_buf()
}

fn main_url() -> Url {
    Url::from_file_path("/tmp/lsp-rust-code-actions/src/Main.sky").unwrap()
}

fn analysis_with(src: &str) -> Analysis {
    let mut a = Analysis::new();
    a.load_stdlib(Some(&repo_root()));
    a.set_document(main_url(), src.to_string());
    a
}

fn whole_doc() -> Range {
    Range {
        start: Position {
            line: 0,
            character: 0,
        },
        end: Position {
            line: u32::MAX,
            character: 0,
        },
    }
}

fn ctx() -> CodeActionContext {
    CodeActionContext::default()
}

/// Titles of every returned action, in order.
fn titles(actions: &[CodeActionOrCommand]) -> Vec<String> {
    actions
        .iter()
        .map(|a| match a {
            CodeActionOrCommand::CodeAction(ca) => ca.title.clone(),
            CodeActionOrCommand::Command(c) => c.title.clone(),
        })
        .collect()
}

/// The single edit carried by the action whose title starts with `prefix`.
fn edit_for(actions: &[CodeActionOrCommand], prefix: &str) -> TextEdit {
    for a in actions {
        if let CodeActionOrCommand::CodeAction(ca) = a {
            if ca.title.starts_with(prefix) {
                let changes = ca.edit.as_ref().unwrap().changes.as_ref().unwrap();
                let edits = changes.values().next().unwrap();
                assert_eq!(edits.len(), 1, "one edit per action");
                return edits[0].clone();
            }
        }
    }
    panic!(
        "no action titled with prefix {prefix:?}; got {:?}",
        titles(actions)
    );
}

// Imports deliberately UNSORTED (Std.Log before Sky.Core.*) so organize fires.
const SRC: &str = "module Main exposing (main)\n\
import Std.Log exposing (println)\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.String as String\n\
\n\
answer =\n\
    42\n\
\n\
label : String\n\
label =\n\
    \"hi\"\n\
\n\
main =\n\
    println (String.fromInt answer)\n";

// ---- Add type annotation ---------------------------------------------

#[test]
fn add_annotation_offered_on_unannotated_value() {
    let a = analysis_with(SRC);
    let actions = a.code_actions(&main_url(), whole_doc(), &ctx());
    let ts = titles(&actions);
    assert!(
        ts.iter().any(|t| t == "Add type annotation: answer : Int"),
        "expected an Int annotation fix for `answer`; got {ts:?}"
    );
}

#[test]
fn add_annotation_edit_inserts_sig_line_above_decl() {
    let a = analysis_with(SRC);
    let actions = a.code_actions(&main_url(), whole_doc(), &ctx());
    let e = edit_for(&actions, "Add type annotation: answer");
    // `answer =` is on line 5 (0-based); the sig is inserted at col 0 of that line.
    assert_eq!(
        e.range.start,
        Position {
            line: 5,
            character: 0
        }
    );
    assert_eq!(
        e.range.end,
        Position {
            line: 5,
            character: 0
        }
    );
    assert_eq!(e.new_text, "answer : Int\n");
}

#[test]
fn add_annotation_not_offered_for_annotated_value() {
    let a = analysis_with(SRC);
    let actions = a.code_actions(&main_url(), whole_doc(), &ctx());
    let ts = titles(&actions);
    assert!(
        !ts.iter()
            .any(|t| t.starts_with("Add type annotation: label")),
        "`label` is already annotated — must not be offered; got {ts:?}"
    );
}

#[test]
fn add_annotation_range_gated_to_intersecting_decl() {
    let a = analysis_with(SRC);
    // A zero-width range on line 5 (`answer =`) only.
    let at_answer = Range {
        start: Position {
            line: 5,
            character: 0,
        },
        end: Position {
            line: 5,
            character: 6,
        },
    };
    let actions = a.code_actions(&main_url(), at_answer, &ctx());
    let ts = titles(&actions);
    assert!(
        ts.iter().any(|t| t == "Add type annotation: answer : Int"),
        "answer decl intersects the range; got {ts:?}"
    );
    assert!(
        !ts.iter()
            .any(|t| t.starts_with("Add type annotation: main")),
        "main decl is outside the range — must not be offered; got {ts:?}"
    );
}

// ---- Organize imports -------------------------------------------------

#[test]
fn organize_imports_offered_when_unsorted() {
    let a = analysis_with(SRC);
    let actions = a.code_actions(&main_url(), whole_doc(), &ctx());
    let e = edit_for(&actions, "Organize imports");
    // Sorted order puts Sky.Core.* before Std.Log.
    assert!(
        e.new_text.starts_with("import Sky.Core.Prelude"),
        "sorted block must lead with Sky.Core.Prelude; got {:?}",
        e.new_text
    );
    assert!(
        e.new_text
            .trim_end()
            .ends_with("import Std.Log exposing (println)"),
        "Std.Log sorts last; got {:?}",
        e.new_text
    );
    // The three import nodes are preserved verbatim (alias / exposing kept).
    assert!(e.new_text.contains("import Sky.Core.String as String"));
}

#[test]
fn organize_imports_noop_when_already_sorted() {
    let sorted = "module Main exposing (main)\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.String as String\n\
import Std.Log exposing (println)\n\
\n\
main =\n\
    println \"hi\"\n";
    let a = analysis_with(sorted);
    let actions = a.code_actions(&main_url(), whole_doc(), &ctx());
    let ts = titles(&actions);
    assert!(
        !ts.iter().any(|t| t == "Organize imports"),
        "already-sorted imports must not offer organize; got {ts:?}"
    );
}

#[test]
fn organize_imports_noop_with_single_import() {
    let one = "module Main exposing (main)\n\
import Std.Log exposing (println)\n\
\n\
main =\n\
    println \"hi\"\n";
    let a = analysis_with(one);
    let actions = a.code_actions(&main_url(), whole_doc(), &ctx());
    assert!(
        !titles(&actions).iter().any(|t| t == "Organize imports"),
        "≤1 import — nothing to organize"
    );
}
