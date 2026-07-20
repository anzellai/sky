//! The 17 nvim-parity scenarios (doc 10 §"The 17-test compat gate"), driven
//! directly against the `sky_lsp::Analysis` engine — the *same* code path the
//! `tower-lsp` server calls per request. Positions + expectations are lifted
//! verbatim from `scripts/lsp-test-nvim.sh` / `scripts/lsp-test-nvim.lua`
//! (0-based line, UTF-16 character). The separate `jsonrpc.rs` test drives the
//! actual server binary end-to-end.

use sky_lsp::Analysis;
use std::path::{Path, PathBuf};
use tower_lsp::lsp_types::{Position, SemanticTokensResult, Url};

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

// ---- REFERENCES (2) ---------------------------------------------------

/// Sorted start-lines of the reference locations at a position.
fn ref_lines(a: &Analysis, line: u32, ch: u32, include_decl: bool) -> Vec<u32> {
    let mut ls: Vec<u32> = a
        .references(&main_url(), Position { line, character: ch }, include_decl)
        .into_iter()
        .map(|l| l.range.start.line)
        .collect();
    ls.sort_unstable();
    ls
}

#[test]
fn references_let_binding() {
    // Cursor on the `abcLocal` USE (line 17); its uses are the binder (line 16)
    // and this use (line 17). Local binder is itself an occurrence, so
    // include-declaration covers both regardless.
    let a = analysis_with(FIXTURE);
    let with = ref_lines(&a, 17, 8, true);
    assert_eq!(with, vec![16, 17], "references-let-binding got {with:?}");
}

#[test]
fn references_top_level_function() {
    // Cursor on the `applyMsg` use (line 32). With the declaration: annotation
    // (21), value decl (22), use (32). Without: just the use (32).
    let a = analysis_with(FIXTURE);
    let with = ref_lines(&a, 32, 30, true);
    assert_eq!(with, vec![21, 22, 32], "references(incl-decl) got {with:?}");
    let without = ref_lines(&a, 32, 30, false);
    assert_eq!(without, vec![32], "references(excl-decl) got {without:?}");
}

// ---- RENAME (3) -------------------------------------------------------

/// Total edit count across all files in the rename WorkspaceEdit.
fn rename_edit_count(a: &Analysis, line: u32, ch: u32, new: &str) -> Option<usize> {
    a.rename(&main_url(), Position { line, character: ch }, new)
        .and_then(|w| w.changes)
        .map(|c| c.values().map(|v| v.len()).sum())
}

#[test]
fn rename_local_binding() {
    let a = analysis_with(FIXTURE);
    // abcLocal has 2 occurrences (binder + use).
    assert_eq!(rename_edit_count(&a, 17, 8, "renamed"), Some(2), "rename-local");
}

#[test]
fn rename_top_level_function() {
    let a = analysis_with(FIXTURE);
    // applyMsg: annotation + decl + use = 3 edit sites, and every edit carries
    // the new name.
    let edit = a
        .rename(&main_url(), Position { line: 32, character: 30 }, "applyMsgV2")
        .expect("rename should produce an edit");
    let changes = edit.changes.expect("changes");
    let total: usize = changes.values().map(|v| v.len()).sum();
    assert_eq!(total, 3, "rename-function edit count");
    assert!(
        changes.values().flatten().all(|e| e.new_text == "applyMsgV2"),
        "every edit uses the new name"
    );
}

#[test]
fn rename_builtin_is_rejected() {
    let a = analysis_with(FIXTURE);
    // Cursor on the builtin `Int` in `letDemo : Int` (line 14) — a Prelude type
    // with no Sky definition site, so it is not renameable.
    assert!(a.rename(&main_url(), Position { line: 14, character: 11 }, "Foo").is_none());
    // prepareRename must also decline it.
    assert!(a.prepare_rename(&main_url(), Position { line: 14, character: 11 }).is_none());
    // A malformed identifier is rejected even on a renameable target (local).
    assert!(a.rename(&main_url(), Position { line: 17, character: 8 }, "1bad").is_none());
}

// ---- SEMANTIC TOKENS (2) ----------------------------------------------

/// Decode the delta-encoded token stream into absolute (line, char) → tokenType.
fn decoded_tokens(a: &Analysis) -> Vec<(u32, u32, u32)> {
    let toks = match a.semantic_tokens(&main_url()) {
        Some(SemanticTokensResult::Tokens(t)) => t.data,
        _ => Vec::new(),
    };
    let mut out = Vec::new();
    let (mut line, mut ch) = (0u32, 0u32);
    for t in toks {
        if t.delta_line == 0 {
            ch += t.delta_start;
        } else {
            line += t.delta_line;
            ch = t.delta_start;
        }
        out.push((line, ch, t.token_type));
    }
    out
}

// Legend indices (mirror of the frozen legend in lib.rs).
const T_TYPE: u32 = 1;
const T_FUNCTION: u32 = 2;
const T_PROPERTY: u32 = 11;

#[test]
fn semantic_tokens_kernel_call_is_function() {
    let a = analysis_with(FIXTURE);
    let toks = decoded_tokens(&a);
    // `fromInt` in `String.fromInt` at line 12, char 11 → function.
    let hit = toks.iter().find(|(l, c, _)| *l == 12 && *c == 11);
    assert_eq!(hit.map(|(_, _, t)| *t), Some(T_FUNCTION), "fromInt should be function; toks={toks:?}");
    // `count` field at line 12, char 25 → property.
    let field = toks.iter().find(|(l, c, _)| *l == 12 && *c == 25);
    assert_eq!(field.map(|(_, _, t)| *t), Some(T_PROPERTY), "count should be property");
}

#[test]
fn semantic_tokens_type_name_is_type() {
    let a = analysis_with(FIXTURE);
    let toks = decoded_tokens(&a);
    // `Model` in the annotation `stringify : Model -> String` at line 10, char 12.
    let hit = toks.iter().find(|(l, c, _)| *l == 10 && *c == 12);
    assert_eq!(hit.map(|(_, _, t)| *t), Some(T_TYPE), "Model should be type; toks={toks:?}");
}

// ---- DOCUMENT SYMBOLS (1) ---------------------------------------------

#[test]
fn document_symbols_top_level() {
    use tower_lsp::lsp_types::SymbolKind;
    let a = analysis_with(FIXTURE);
    let syms = a.document_symbols(&main_url());
    let by_name = |n: &str| syms.iter().find(|s| s.name == n).map(|s| s.kind);
    assert_eq!(by_name("stringify"), Some(SymbolKind::FUNCTION), "stringify fn");
    assert_eq!(by_name("Model"), Some(SymbolKind::STRUCT), "Model alias");
    assert_eq!(by_name("Msg"), Some(SymbolKind::ENUM), "Msg union");
    assert_eq!(by_name("applyMsg"), Some(SymbolKind::FUNCTION), "applyMsg fn");
    // Constructors are not surfaced as top-level symbols.
    assert!(syms.iter().all(|s| s.name != "Increment"), "ctors excluded");
}

// ---- bug (a): cursor ON a declaration / annotation name ----------------
//
// Resolution funnelled through `best_candidate`, which scanned only the three
// USE channels (field/ref/type occs) — never `def_spans` nor the annotation
// name. So with the cursor ON a value-def name, a type/alias decl name, or a
// `foo : T` annotation name, hover / references / goto / rename all returned
// nothing. The fix scans `def_spans` in `best_candidate` and adds the
// annotation-name fallback in `cand_at`, mapping each to the SAME
// `Target::Global(def)` a use site resolves to.

#[test]
fn hover_on_value_def_name() {
    // Cursor ON the `applyMsg` VALUE-def name (line 22) — not a use.
    let a = analysis_with(FIXTURE);
    let md = hover_text(&a, 22, 3);
    assert!(md.contains("applyMsg"), "hover on def name names it; got {md:?}");
    assert!(md.contains("Msg -> Int -> Int"), "hover on def name shows its type; got {md:?}");
}

#[test]
fn hover_on_annotation_name() {
    // Cursor ON the `applyMsg` ANNOTATION name (line 21) — the `foo : T` site.
    let a = analysis_with(FIXTURE);
    let md = hover_text(&a, 21, 3);
    assert!(md.contains("Msg -> Int -> Int"), "hover on annotation name; got {md:?}");
}

#[test]
fn hover_on_type_decl_names() {
    let a = analysis_with(FIXTURE);
    // `Model` alias decl name (line 8, char 13).
    let model = hover_text(&a, 8, 13);
    assert!(model.contains("Model"), "hover on alias decl name; got {model:?}");
    // `Msg` union decl name (line 19, char 6).
    let msg = hover_text(&a, 19, 6);
    assert!(msg.contains("Msg"), "hover on union decl name; got {msg:?}");
}

#[test]
fn references_from_decl_equal_references_from_use() {
    let a = analysis_with(FIXTURE);
    // The canonical use-site answer (mirrors `references_top_level_function`).
    let from_use = ref_lines(&a, 32, 30, true); // use of applyMsg
    assert_eq!(from_use, vec![21, 22, 32], "baseline from-use");
    // From the VALUE-def name (line 22) — identical set.
    let from_def = ref_lines(&a, 22, 3, true);
    assert_eq!(from_def, from_use, "references from value-def name == from use");
    // From the ANNOTATION name (line 21) — identical set.
    let from_anno = ref_lines(&a, 21, 3, true);
    assert_eq!(from_anno, from_use, "references from annotation name == from use");
    // include_decl == false from the decl is still just the use.
    assert_eq!(ref_lines(&a, 22, 3, false), vec![32], "excl-decl from def name");
}

#[test]
fn references_from_type_decl_equal_from_use() {
    let a = analysis_with(FIXTURE);
    // `Model` used in `stringify : Model -> String` (line 10) + declared (line 8).
    let from_use = ref_lines(&a, 10, 13, true);
    let from_decl = ref_lines(&a, 8, 13, true);
    assert_eq!(from_decl, from_use, "type references from decl == from use; got {from_decl:?} vs {from_use:?}");
    assert!(from_decl.contains(&8) && from_decl.contains(&10), "covers decl+use; got {from_decl:?}");
}

#[test]
fn rename_from_decl_name() {
    // Rename initiated ON the value-def name (line 22) renames all 3 sites.
    let a = analysis_with(FIXTURE);
    assert_eq!(rename_edit_count(&a, 22, 3, "renamedFn"), Some(3), "rename from def name");
    // And from the annotation name (line 21).
    assert_eq!(rename_edit_count(&a, 21, 3, "renamedFn"), Some(3), "rename from annotation name");
}
