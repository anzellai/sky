//! In-process coverage for the three endpoints added on top of the M7 breadth
//! set (doc 10 §request→query map): `textDocument/formatting`,
//! `textDocument/inlayHint`, and `textDocument/signatureHelp`. Driven directly
//! against the `sky_lsp::Analysis` engine — the same code path the `tower-lsp`
//! server calls per request (mirrors `scenarios.rs`).

use sky_lsp::Analysis;
use std::path::{Path, PathBuf};
use tower_lsp::lsp_types::{InlayHintLabel, ParameterLabel, Position, Range, Url};

// Same fixture the 17-scenario suite uses (0-based line, UTF-16 char columns).
const FIXTURE: &str = "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Sky.Core.Task as Task\nimport Sky.Core.String as String\nimport Std.Log exposing (println)\nimport Std.Ui as Ui\n\ntype alias Model = { count : Int, label : String }\n\nstringify : Model -> String\nstringify model =\n    String.fromInt model.count\n\nletDemo : Int\nletDemo =\n    let abcLocal = 1\n    in abcLocal\n\ntype Msg = Increment | Decrement | SetCount Int\n\napplyMsg : Msg -> Int -> Int\napplyMsg msg current =\n    case msg of\n        Increment -> current + 1\n        Decrement -> current - 1\n        SetCount n -> n\n\ndoubleIt : Int -> Int\ndoubleIt = \\x -> x * 2\n\nmain =\n    Task.run (Task.succeed (applyMsg Increment 41))\n";

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found above the crate")
        .to_path_buf()
}

fn main_url() -> Url {
    Url::from_file_path("/tmp/lsp-rust-new-endpoints/src/Main.sky").unwrap()
}

fn analysis_with(src: &str) -> Analysis {
    let mut a = Analysis::new();
    a.load_stdlib(Some(&repo_root()));
    a.set_document(main_url(), src.to_string());
    a
}

/// A range covering the whole buffer — `end` past the last line is clamped to
/// the document end inside the engine's offset conversion.
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

// ---- textDocument/formatting ------------------------------------------

#[test]
fn formatting_normalises_whitespace() {
    // Extra spaces inside the call are collapsed by `fmt::format_source`.
    let src = "module M exposing (main)\n\nmain =\n    println   \"hi\"\n";
    let a = analysis_with(src);
    let edits = a.formatting(&main_url()).expect("formatting returns edits");
    assert_eq!(edits.len(), 1, "one whole-file replacement edit");
    let out = &edits[0].new_text;
    assert!(
        out.contains("println \"hi\""),
        "collapsed spaces; got: {out:?}"
    );
    // The replacement covers from the start of the document.
    assert_eq!(
        edits[0].range.start,
        Position {
            line: 0,
            character: 0
        }
    );
}

#[test]
fn formatting_already_formatted_is_noop() {
    // A buffer that is already canonical yields NO edits (no client churn).
    let src = "module M exposing (main)\n\n\nmain =\n    println \"hi\"\n";
    let a = analysis_with(src);
    let once = fmt::format_source(src);
    // Feed the canonical form back in.
    let a2 = analysis_with(&once);
    let _ = a; // first analysis unused beyond documenting intent
    let edits = a2.formatting(&main_url()).expect("some");
    assert!(
        edits.is_empty(),
        "already-formatted → no edits; got {edits:?}"
    );
}

#[test]
fn formatting_is_idempotent_via_edit() {
    // Applying the formatting edit once, then formatting again, is a no-op.
    let a = analysis_with(FIXTURE);
    let edits = a.formatting(&main_url()).expect("edits");
    let formatted = if edits.is_empty() {
        FIXTURE.to_string()
    } else {
        edits[0].new_text.clone()
    };
    let a2 = analysis_with(&formatted);
    assert!(
        a2.formatting(&main_url()).unwrap().is_empty(),
        "second format pass must be a no-op (idempotent)"
    );
}

// ---- textDocument/inlayHint -------------------------------------------

#[test]
fn inlay_hint_on_let_binding() {
    // `let abcLocal = 1` (line 16) is unannotated → hint ` : Int` after the name.
    let a = analysis_with(FIXTURE);
    let hints = a.inlay_hints(&main_url(), whole_doc());
    let on_16 = hints.iter().find(|h| h.position.line == 16);
    let label = on_16.map(|h| match &h.label {
        InlayHintLabel::String(s) => s.clone(),
        _ => String::new(),
    });
    assert_eq!(
        label.as_deref(),
        Some(" : Int"),
        "let-binding inlay hint; got {label:?} (all: {:?})",
        hints
            .iter()
            .map(|h| (
                h.position.line,
                match &h.label {
                    InlayHintLabel::String(s) => s.clone(),
                    _ => String::new(),
                }
            ))
            .collect::<Vec<_>>()
    );
}

#[test]
fn inlay_hint_on_unannotated_top_level() {
    // `main =` (line 31) has no annotation → a hint is emitted after `main`.
    let a = analysis_with(FIXTURE);
    let hints = a.inlay_hints(&main_url(), whole_doc());
    assert!(
        hints.iter().any(|h| h.position.line == 31),
        "expected a hint on the unannotated `main`; got {:?}",
        hints.iter().map(|h| h.position.line).collect::<Vec<_>>()
    );
}

#[test]
fn inlay_hint_skips_annotated_bindings() {
    // `stringify : Model -> String` (annotated, line 11 decl on 12) gets NO hint.
    let a = analysis_with(FIXTURE);
    let hints = a.inlay_hints(&main_url(), whole_doc());
    // `stringify` is declared on line 11 (annotation) / 12 (value); neither line
    // should carry an inlay hint because the def is annotated.
    assert!(
        !hints.iter().any(|h| h.position.line == 11),
        "annotated def must not get an inlay hint"
    );
}

// ---- textDocument/signatureHelp ---------------------------------------

#[test]
fn signature_help_on_kernel_call() {
    // Cursor inside `Task.run (…)` at line 32 (arg territory, char 14) → the
    // signature of `Task.run` (`Task a -> …`).
    let a = analysis_with(FIXTURE);
    let sh = a
        .signature_help(
            &main_url(),
            Position {
                line: 32,
                character: 14,
            },
        )
        .expect("signature help at a call site");
    assert_eq!(sh.signatures.len(), 1);
    let label = &sh.signatures[0].label;
    assert!(
        label.starts_with("run :") || label.starts_with("Task.run :"),
        "label names the callee + type; got {label:?}"
    );
    assert!(
        label.contains("Task"),
        "Task.run type mentions Task; got {label:?}"
    );
    // At least one parameter slot (the arrow LHS) is reported.
    let params = sh.signatures[0].parameters.as_ref().expect("params");
    assert!(!params.is_empty(), "at least one parameter slot");
    // The parameter labels are offset pairs into the signature label.
    assert!(matches!(params[0].label, ParameterLabel::LabelOffsets(_)));
}

#[test]
fn signature_help_active_parameter_advances() {
    // `applyMsg Increment 41` inside `Task.succeed (…)` — after the first
    // argument (`Increment`), the active parameter is the second slot.
    let a = analysis_with(FIXTURE);
    // Line 32: `    Task.run (Task.succeed (applyMsg Increment 41))`
    // Place the cursor right after `Increment ` (before `41`).
    let line = "    Task.run (Task.succeed (applyMsg Increment 41))";
    let col = line.find("41").unwrap() as u32;
    let sh = a
        .signature_help(
            &main_url(),
            Position {
                line: 32,
                character: col,
            },
        )
        .expect("signature help inside applyMsg call");
    let label = &sh.signatures[0].label;
    assert!(
        label.starts_with("applyMsg :"),
        "callee is applyMsg; got {label:?}"
    );
    // One argument (`Increment`) is fully before the cursor → active param 1.
    assert_eq!(
        sh.active_parameter,
        Some(1),
        "active parameter after first arg"
    );
}

#[test]
fn signature_help_none_outside_call() {
    // A position on the module header is not inside any call.
    let a = analysis_with(FIXTURE);
    assert!(
        a.signature_help(
            &main_url(),
            Position {
                line: 0,
                character: 3
            }
        )
        .is_none(),
        "no signature help outside a call"
    );
}

// ---- bug (b): full inferred signature on unannotated functions ---------
//
// `BodyTypes.result` is the body-ROOT type only; an unannotated function must
// hover / inlay-hint as its full arrow (`p0 -> … -> result`), reconstructed
// from `BodyTypes.signature`. Regression for the "inlay/hover drop parameter
// types" bug. `add`/`greet` have NO annotation and are NOT stdlib `Result`
// combinators, so `value_sig` AND `inferred_sig` are both None — the arrow can
// only come from the new `signature` field.

const UNANNOTATED_SRC: &str = "module M exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Sky.Core.String as String\nimport Std.Log exposing (println)\n\ntype Msg = Inc | Dec\n\nstep : Msg -> Int -> Int\nstep msg n =\n    case msg of\n        Inc -> n + 1\n        Dec -> n - 1\n\nadd x y = x + y\n\nbump n = step Inc n\n\ngreet name = String.append \"hi \" name\n\nmain =\n    println (greet \"a\")\n";

fn hover_md(a: &Analysis, line: u32, ch: u32) -> String {
    match a.hover(
        &main_url(),
        Position {
            line,
            character: ch,
        },
    ) {
        Some(h) => match h.contents {
            tower_lsp::lsp_types::HoverContents::Markup(m) => m.value,
            _ => String::new(),
        },
        None => String::new(),
    }
}

fn inlay_label(a: &Analysis, line: u32) -> Option<String> {
    a.inlay_hints(&main_url(), whole_doc())
        .into_iter()
        .find(|h| h.position.line == line)
        .map(|h| match h.label {
            InlayHintLabel::String(s) => s,
            _ => String::new(),
        })
}

#[test]
fn inlay_hint_unannotated_function_shows_full_arrow() {
    let a = analysis_with(UNANNOTATED_SRC);
    // `add x y = x + y` (line 14): the WHOLE arrow, not the body-root `number`.
    let add = inlay_label(&a, 14);
    // `add x y = x + y` is fully polymorphic `a -> a -> a` now that Sky separates
    // Int/Float and `+` is unconstrained (was the over-constrained `number -> …`
    // before the Int/Float separation — `+` accepts any single type, e.g.
    // `add "s" "t"`). Hover/inlay render internal vars as clean `a`, `b`, …
    // (`render_pretty`).
    assert_eq!(
        add.as_deref(),
        Some(" : a -> a -> a"),
        "unannotated 2-arg fn must hint its full arrow, not the body result; got {add:?}"
    );
    // Regression guard: the old bug rendered the body-root type only (` : a`).
    assert_ne!(add.as_deref(), Some(" : a"), "must NOT be body-result only");

    // `bump n = step Inc n` (line 16): concrete `Int -> Int` (task's Int-arrow case).
    let bump = inlay_label(&a, 16);
    assert_eq!(
        bump.as_deref(),
        Some(" : Int -> Int"),
        "unannotated fn with concrete param must hint `Int -> Int`; got {bump:?}"
    );
}

#[test]
fn hover_unannotated_function_shows_full_arrow() {
    let a = analysis_with(UNANNOTATED_SRC);
    // On the `add` DECLARATION name (line 14) — full arrow.
    let add_decl = hover_md(&a, 14, 1);
    // Poly `a -> a -> a` (see the inlay test) — `+` is unconstrained now that
    // Int and Float are fully separate.
    assert!(
        add_decl.contains("a -> a -> a"),
        "hover on unannotated decl shows full arrow; got {add_decl:?}"
    );
    // On the `bump` declaration name (line 16) — concrete `Int -> Int`.
    let bump_decl = hover_md(&a, 16, 1);
    assert!(
        bump_decl.contains("Int -> Int"),
        "hover on `bump` decl shows `Int -> Int`; got {bump_decl:?}"
    );
    // On a USE site of `greet` (line 21, inside `println (greet "a")`) — isolates
    // bug (b) from bug (a): `ref_type_string` for a `Res::Def` use must also fall
    // back to the full inferred signature, not the body result (`String`).
    let greet_use = hover_md(&a, 21, 15);
    assert!(
        greet_use.contains("String -> String"),
        "hover on unannotated fn USE shows full arrow, not body result; got {greet_use:?}"
    );
    assert!(
        !greet_use.contains(": String\n"),
        "hover on use must NOT be the body-result-only `String`; got {greet_use:?}"
    );
}
