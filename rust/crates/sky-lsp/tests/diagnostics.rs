//! Diagnostics regression coverage — the LSP capability with the weakest
//! assertions elsewhere (hover/completion/goto-def are exercised in-process and
//! in the Neovim parity gate; diagnostics were not driven through an editor and
//! the Go-FFI-alias false-positive class had no guard).
//!
//! These tests build a full on-disk fixture (so the stdlib, the fetched Sky dep
//! `Foo`, and the real `github.com/google/uuid` Go-FFI surface are all loaded
//! exactly as the build path loads them), then inject each scenario's buffer via
//! `set_document` — the same path the editor's `didChange` takes — and assert on
//! the published diagnostics.
//!
//! The invariant under test throughout: the LSP's diagnostics must MATCH
//! `sky build` acceptance — no spurious errors on valid FFI/cross-module code,
//! and genuine errors still reported at the right span + severity.

mod common;

use common::*;
use sky_lsp::Analysis;
use tower_lsp::lsp_types::{Diagnostic, DiagnosticSeverity, Position, Url};

/// A loaded analysis for the extdeps fixture (stdlib + fetched `Foo` dep + the
/// uuid Go-FFI surface). `Main` starts as the fixture's own text; individual
/// tests overwrite it (and/or add sibling modules) via `set_document`.
fn analysis_for(root: &std::path::Path) -> Analysis {
    ensure_stdlib_env();
    let mut a = Analysis::new();
    a.ensure_project_for(&main_path(root));
    a.set_document(main_url(root), main_text());
    a
}

/// The subset of published diagnostics with ERROR severity.
fn errors(diags: &[Diagnostic]) -> Vec<&Diagnostic> {
    diags
        .iter()
        .filter(|d| d.severity == Some(DiagnosticSeverity::ERROR))
        .collect()
}

/// URL for an arbitrary sibling module file under the project's `src/`.
fn sibling_url(root: &std::path::Path, file: &str) -> Url {
    Url::from_file_path(root.join("src").join(file)).unwrap()
}

// ---------------------------------------------------------------------------
// 1. Go-FFI alias produces ZERO spurious diagnostics.
//
// The specific false-positive regression guard: a buffer that imports a Go-FFI
// package under an alias and calls `Alias.member` must publish NO error
// diagnostic (the historical bug published a bogus
// `Undefined name: <FfiAlias>.<name>` the CLI never emitted).
// ---------------------------------------------------------------------------

#[test]
fn ffi_alias_call_zero_spurious_diagnostics() {
    let root = build_fixture(true);
    let mut a = analysis_for(&root);

    // A minimal, self-contained buffer whose ONLY external surface is the
    // Go-FFI alias `Uuid` and its member `newString`.
    let buf = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)
import Github.Com.Google.Uuid as Uuid


main =
    let
        newId =
            Uuid.newString ()
    in
    println (String.fromInt (String.length (Result.withDefault \"\" newId)))
";
    a.set_document(main_url(&root), buf.to_string());

    let diags = a.diagnostics(&main_url(&root));
    let errs = errors(&diags);
    assert!(
        errs.is_empty(),
        "a Go-FFI alias call (`Uuid.newString ()`) must publish NO error \
         diagnostic — this is the `Undefined name: <FfiAlias>.<name>` \
         false-positive regression guard; got errors: {errs:#?}"
    );
}

// ---------------------------------------------------------------------------
// 2. A genuine type error → EXACTLY ONE diagnostic, severity ERROR, at the
//    offending span.
// ---------------------------------------------------------------------------

#[test]
fn genuine_type_error_one_diagnostic_at_span() {
    let root = build_fixture(true);
    let mut a = analysis_for(&root);

    // `x : Int` bound to a String literal — a genuine HM mismatch. The offending
    // span is the `"str"` literal on line 6 (0-based line 5).
    let buf = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

x : Int
x = \"str\"

main =
    println (String.fromInt x)
";
    a.set_document(main_url(&root), buf.to_string());

    let diags = a.diagnostics(&main_url(&root));
    let errs = errors(&diags);
    assert_eq!(
        errs.len(),
        1,
        "a single genuine type error must publish EXACTLY ONE error diagnostic; \
         got: {errs:#?}"
    );
    let d = errs[0];
    assert_eq!(d.severity, Some(DiagnosticSeverity::ERROR));

    // The span must land on the `"str"` literal, not the whole binding / module.
    let str_pos = pos_in(buf, "\"str\"", 0);
    assert_eq!(
        d.range.start.line, str_pos.line,
        "the type error must be anchored on the offending `\"str\"` line \
         ({}), got range {:?}",
        str_pos.line, d.range
    );
    assert!(
        d.range.start.character <= str_pos.character
            && d.range.end.character >= str_pos.character,
        "the type error range must cover the `\"str\"` literal column ({}), \
         got range {:?}",
        str_pos.character, d.range
    );
}

// ---------------------------------------------------------------------------
// 3. A genuine undefined-name still produces a diagnostic — the FFI/cross-module
//    false-positive fix did NOT over-suppress real errors.
// ---------------------------------------------------------------------------

#[test]
fn genuine_undefined_name_still_reported() {
    let root = build_fixture(true);
    let mut a = analysis_for(&root);

    // `thisNameDoesNotExist` is not bound anywhere.
    let buf = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

main =
    println thisNameDoesNotExist
";
    a.set_document(main_url(&root), buf.to_string());

    let diags = a.diagnostics(&main_url(&root));
    let errs = errors(&diags);
    assert!(
        !errs.is_empty(),
        "a genuine undefined name must still publish an error diagnostic — \
         the false-positive fix must not over-suppress; got no errors. \
         All diags: {diags:#?}"
    );
    let hit = errs.iter().find(|d| {
        d.message.contains("thisNameDoesNotExist")
            || d.message.to_lowercase().contains("undefined")
            || d.message.to_lowercase().contains("not in scope")
            || d.message.to_lowercase().contains("unknown")
    });
    assert!(
        hit.is_some(),
        "the undefined-name error must name the culprit / read as an \
         undefined-name error; got errors: {errs:#?}"
    );
    // And it must be anchored on the offending reference's line (line 6, 0-based).
    let ref_pos = pos_in(buf, "thisNameDoesNotExist", 0);
    let d = hit.unwrap();
    assert_eq!(
        d.range.start.line, ref_pos.line,
        "undefined-name error must anchor on the reference line ({}), got {:?}",
        ref_pos.line, d.range
    );
}

// ---------------------------------------------------------------------------
// 4. Cross-module externals — a project module referencing ANOTHER project
//    module's exported binding publishes NO spurious diagnostic (mirrors the
//    v0.17.3 cross-module externals false-positive fix).
// ---------------------------------------------------------------------------

#[test]
fn cross_module_project_ref_zero_spurious_diagnostics() {
    let root = build_fixture(true);
    let mut a = analysis_for(&root);

    // A sibling project module exporting an annotated value AND an unannotated
    // (HM-inferred) wrapper — the two shapes the v0.17.3 fix covered.
    let helper = "\
module Helper exposing (answer, wrap)

answer : Int
answer = 42

wrap n =
    n + 1
";
    a.set_document(sibling_url(&root, "Helper.sky"), helper.to_string());

    // Main consumes both the annotated export and the unannotated wrapper.
    let buf = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)
import Helper exposing (answer, wrap)


main =
    println (String.fromInt (wrap answer))
";
    a.set_document(main_url(&root), buf.to_string());

    let diags = a.diagnostics(&main_url(&root));
    let errs = errors(&diags);
    assert!(
        errs.is_empty(),
        "referencing another PROJECT module's exported bindings (annotated \
         `answer` + HM-inferred `wrap`) must publish NO error diagnostic — \
         the v0.17.3 cross-module externals guard; got errors: {errs:#?}"
    );
}

// ---------------------------------------------------------------------------
// 5. Combined valid surface (FFI alias + cross-module + stdlib) → clean.
//
// Belt-and-braces: the three external surfaces together, in one buffer, must
// still publish zero errors — a spurious diagnostic that only fires when
// multiple external kinds coexist would slip past the single-surface guards.
// ---------------------------------------------------------------------------

#[test]
fn mixed_valid_externals_zero_spurious_diagnostics() {
    let root = build_fixture(true);
    let mut a = analysis_for(&root);

    let helper = "\
module Helper exposing (answer)

answer : Int
answer = 7
";
    a.set_document(sibling_url(&root, "Helper.sky"), helper.to_string());

    let buf = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)
import Helper exposing (answer)
import Github.Com.Google.Uuid as Uuid


main =
    let
        newId =
            Uuid.newString ()
    in
    println
        (String.append
            (String.fromInt answer)
            (Result.withDefault \"\" newId)
        )
";
    a.set_document(main_url(&root), buf.to_string());

    let diags = a.diagnostics(&main_url(&root));
    let errs = errors(&diags);
    assert!(
        errs.is_empty(),
        "a buffer combining a Go-FFI alias, a cross-module project ref, and \
         stdlib calls — all valid — must publish NO error diagnostic; got: \
         {errs:#?}"
    );
}

// A tiny compile-time nudge that `Position` is used (keeps imports honest if a
// future edit drops the range assertions).
const _: fn() -> Position = || Position {
    line: 0,
    character: 0,
};
