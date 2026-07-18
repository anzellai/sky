//! The 17 nvim-parity scenarios (doc 10 §"The 17-test compat gate"), driven
//! directly against the `sky_lsp::Analysis` engine — the *same* code path the
//! `tower-lsp` server calls per request. Positions + expectations are lifted
//! verbatim from `scripts/lsp-test-nvim.sh` / `scripts/lsp-test-nvim.lua`
//! (0-based line, UTF-16 character). The separate `jsonrpc.rs` test drives the
//! actual server binary end-to-end.

use sky_lsp::Analysis;
use std::path::{Path, PathBuf};
use tower_lsp::lsp_types::{Position, Url};

// NB: written as a single literal with explicit `\n` — a `\`-continuation would
// strip each line's leading indentation and corrupt the fixture's columns.
const FIXTURE: &str = "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Sky.Core.Task as Task\nimport Sky.Core.String as String\nimport Std.Log exposing (println)\nimport Std.Ui as Ui\n\ntype alias Model = { count : Int, label : String }\n\nstringify : Model -> String\nstringify model =\n    String.fromInt model.count\n\nletDemo : Int\nletDemo =\n    let abcLocal = 1\n    in abcLocal\n\ntype Msg = Increment | Decrement | SetCount Int\n\napplyMsg : Msg -> Int -> Int\napplyMsg msg current =\n    case msg of\n        Increment -> current + 1\n        Decrement -> current - 1\n        SetCount n -> n\n\ndoubleIt : Int -> Int\ndoubleIt = \\x -> x * 2\n\nmain =\n    Task.run (Task.succeed (applyMsg Increment 41))\n";

fn repo_root() -> PathBuf {
    // .../sky/rust/crates/sky-lsp → climb to .../sky
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found above the crate")
        .to_path_buf()
}

fn main_url() -> Url {
    Url::from_file_path("/tmp/lsp-rust-scenarios/src/Main.sky").unwrap()
}

/// Build an Analysis with the real stdlib + a Main.sky whose text is `src`.
fn analysis_with(src: &str) -> Analysis {
    let mut a = Analysis::new();
    a.load_stdlib(Some(&repo_root()));
    a.set_document(main_url(), src.to_string());
    a
}

fn hover_text(a: &Analysis, line: u32, ch: u32) -> String {
    match a.hover(&main_url(), Position { line, character: ch }) {
        Some(h) => match h.contents {
            tower_lsp::lsp_types::HoverContents::Markup(m) => m.value,
            _ => String::new(),
        },
        None => String::new(),
    }
}

fn goto_line(a: &Analysis, line: u32, ch: u32) -> Option<u32> {
    a.goto(&main_url(), Position { line, character: ch })
        .map(|loc| loc.range.start.line)
}

fn completion_labels(a: &Analysis, src_override: Option<&str>, line: u32, ch: u32) -> Vec<(String, Option<String>)> {
    let a2;
    let a = match src_override {
        Some(s) => {
            a2 = analysis_with(s);
            &a2
        }
        None => a,
    };
    a.completion(&main_url(), Position { line, character: ch })
        .into_iter()
        .map(|i| (i.label, i.insert_text))
        .collect()
}

// ---- HOVER (7) --------------------------------------------------------

#[test]
fn hover_task_run() {
    let a = analysis_with(FIXTURE);
    assert!(hover_text(&a, 32, 9).contains("Task"), "hover-task-run");
}

#[test]
fn hover_field() {
    let a = analysis_with(FIXTURE);
    assert!(hover_text(&a, 12, 25).contains("Int"), "hover-field");
}

#[test]
fn hover_type_name() {
    let a = analysis_with(FIXTURE);
    assert!(hover_text(&a, 10, 13).contains("Model"), "hover-type-name");
}

#[test]
fn hover_function_use() {
    let a = analysis_with(FIXTURE);
    assert!(hover_text(&a, 32, 30).contains("Int"), "hover-function-use");
}

#[test]
fn hover_ctor_use() {
    let a = analysis_with(FIXTURE);
    assert!(hover_text(&a, 32, 37).contains("Msg"), "hover-ctor-use");
}

#[test]
fn hover_lambda_param() {
    let a = analysis_with(FIXTURE);
    assert!(hover_text(&a, 29, 12).contains("Int"), "hover-lambda-param");
}

#[test]
fn hover_case_pattern() {
    let a = analysis_with(FIXTURE);
    assert!(hover_text(&a, 26, 17).contains("Int"), "hover-case-pattern");
}

#[test]
fn hover_kernel_call() {
    let a = analysis_with(FIXTURE);
    assert!(hover_text(&a, 12, 14).contains("Int"), "hover-kernel-call");
}

// ---- GOTO-DEF (7) -----------------------------------------------------

#[test]
fn goto_def_type_name() {
    let a = analysis_with(FIXTURE);
    assert_eq!(goto_line(&a, 10, 13), Some(8), "goto-def-type-name");
}

#[test]
fn goto_def_function() {
    let a = analysis_with(FIXTURE);
    let l = goto_line(&a, 32, 30);
    assert!(l == Some(21) || l == Some(22), "goto-def-function got {l:?}");
}

#[test]
fn goto_def_ctor() {
    let a = analysis_with(FIXTURE);
    assert_eq!(goto_line(&a, 32, 37), Some(19), "goto-def-ctor");
}

#[test]
fn goto_def_let_binding() {
    let a = analysis_with(FIXTURE);
    assert_eq!(goto_line(&a, 17, 8), Some(16), "goto-def-let-binding");
}

#[test]
fn goto_def_lambda_param() {
    let a = analysis_with(FIXTURE);
    assert_eq!(goto_line(&a, 29, 17), Some(29), "goto-def-lambda-param");
}

#[test]
fn goto_def_field() {
    let a = analysis_with(FIXTURE);
    assert_eq!(goto_line(&a, 12, 25), Some(8), "goto-def-field");
}

// ---- COMPLETION (3) ---------------------------------------------------

#[test]
fn completion_qualified_insert_text() {
    let src = format!("{FIXTURE}x = Ui.\n");
    let items = completion_labels(&Analysis::new(), Some(&src), 33, 7);
    let layout = items.iter().find(|(l, _)| l == "Ui.layout");
    assert!(layout.is_some(), "Ui.layout not offered; got {items:?}");
    assert_eq!(
        layout.unwrap().1.as_deref(),
        Some("layout"),
        "insertText must be bare 'layout'"
    );
}

#[test]
fn completion_field() {
    let src = format!(
        "{FIXTURE}\ndescribe : Model -> String\ndescribe m =\n    String.fromInt m.\n"
    );
    // appended: line33="", 34=anno, 35="describe m =", 36="    String.fromInt m."
    let items = completion_labels(&Analysis::new(), Some(&src), 36, 21);
    let labels: Vec<&str> = items.iter().map(|(l, _)| l.as_str()).collect();
    assert!(labels.contains(&"count"), "count missing; got {labels:?}");
    assert!(labels.contains(&"label"), "label missing; got {labels:?}");
}

#[test]
fn completion_let_binding() {
    let a = analysis_with(FIXTURE);
    let items = completion_labels(&a, None, 17, 9);
    let labels: Vec<&str> = items.iter().map(|(l, _)| l.as_str()).collect();
    assert!(labels.contains(&"abcLocal"), "abcLocal missing; got {labels:?}");
}
