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

// ---------------------------------------------------------------------------
// 5. `[E2008]` — an unsupported `Dict` key surfaces IN THE EDITOR.
//
// The point of moving this defect from a runtime panic to a type error is that
// the user finds out while typing, not while running. `Analysis::diagnostics`
// forwards everything `ty::check_modules` produces without a code filter, so
// this SHOULD hold by construction — but "should, by construction" is exactly
// how the CLI's `E2001 || E2007` allowlist swallowed this same diagnostic and
// made `sky check` exit 1 with an empty message. Verified, not assumed.
// ---------------------------------------------------------------------------

#[test]
fn unsupported_dict_key_publishes_e2008_at_the_annotation() {
    let root = build_fixture(true);
    let mut a = analysis_for(&root);

    let buf = "\
module Main exposing (main)

import Sky.Core.Dict as Dict exposing (Dict)
import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

grid : Dict ( Int, Int ) String
grid =
    Dict.insert ( 1, 2 ) \"wall\" Dict.empty

main =
    println (String.fromInt (Dict.size grid))
";
    a.set_document(main_url(&root), buf.to_string());

    let diags = a.diagnostics(&main_url(&root));
    let errs = errors(&diags);
    assert_eq!(
        errs.len(),
        1,
        "one unsupported Dict key must publish EXACTLY ONE error diagnostic; \
         got: {errs:#?}"
    );
    let d = errs[0];
    assert_eq!(
        d.code,
        Some(tower_lsp::lsp_types::NumberOrString::String(
            "E2008".to_string()
        )),
        "the editor must see the [E2008] code, got {:?}",
        d.code
    );
    assert!(
        d.message.contains("( Int, Int )") && d.message.contains("`Int`"),
        "the published message must name the offending key type AND the \
         supported set; got: {}",
        d.message
    );

    // Anchored on the written annotation type — the text the user edits — and
    // NOT at the `0:0` fallback a label-less diagnostic would land on.
    let anno = pos_in(buf, "Dict ( Int, Int ) String", 0);
    assert_eq!(
        d.range.start, anno,
        "the [E2008] range must start at the annotation's type, got {:?}",
        d.range
    );
    assert!(d.range.end.character > d.range.start.character);
}

/// The accept twin, in the editor: the five supported key types and a
/// key-polymorphic helper publish NOTHING. An LSP that red-squiggles ordinary
/// `Dict k v` code would be worse than the runtime panic this check replaces.
#[test]
fn supported_and_polymorphic_dict_keys_publish_nothing() {
    let root = build_fixture(true);
    let mut a = analysis_for(&root);

    let buf = "\
module Main exposing (main)

import Sky.Core.Dict as Dict exposing (Dict)
import Sky.Core.List as List
import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

keysOf : Dict k v -> List k
keysOf d =
    Dict.keys d

byString : Dict String Int
byString =
    Dict.insert \"a\" 1 Dict.empty

byInt : Dict Int String
byInt =
    Dict.insert 1 \"a\" Dict.empty

byFloat : Dict Float String
byFloat =
    Dict.insert 1.5 \"a\" Dict.empty

byChar : Dict Char String
byChar =
    Dict.insert 'a' \"a\" Dict.empty

byBool : Dict Bool String
byBool =
    Dict.insert True \"a\" Dict.empty

main =
    println
        (String.fromInt
            (List.length (keysOf byInt)
                + Dict.size byString
                + Dict.size byFloat
                + Dict.size byChar
                + Dict.size byBool
            )
        )
";
    a.set_document(main_url(&root), buf.to_string());

    let diags = a.diagnostics(&main_url(&root));
    let errs = errors(&diags);
    assert!(
        errs.is_empty(),
        "the five supported Dict key types and a key-polymorphic `Dict k v` \
         helper are ordinary valid Sky and must publish NO diagnostic; got: \
         {errs:#?}"
    );
}

// A tiny compile-time nudge that `Position` is used (keeps imports honest if a
// future edit drops the range assertions).
const _: fn() -> Position = || Position {
    line: 0,
    character: 0,
};

// ---------------------------------------------------------------------------
// 7. A resolve error is published EXACTLY ONCE.
//
// `Analysis::diagnostics` takes its own copy of `db.resolve(module).diagnostics`
// AND appends `ty::check_modules`, whose `name_errors` loop re-publishes every
// ERROR-severity resolve diagnostic. Before the dedupe, that made the editor
// draw every naming error twice while `sky check` printed it once — two
// identical squiggles and two quickfix rows for one mistake.
//
// The existing `genuine_undefined_name_still_reported` above cannot catch this:
// it asserts `!errs.is_empty()` and then `find`s a match, so N copies pass just
// as well as one. This test is the counting one.
//
// Falsifiability twin: the same buffer with the name DEFINED publishes zero, so
// the assertion is not satisfied by a build that reports nothing.
// ---------------------------------------------------------------------------

/// Every distinct (code, message, range) triple, with how many times it was
/// published. Anything above 1 is a duplicate the user sees twice.
fn publish_counts(diags: &[Diagnostic]) -> Vec<(String, u32)> {
    let mut counts: Vec<(String, u32)> = Vec::new();
    for d in diags {
        let key = format!(
            "{:?}|{}|{:?}",
            d.code,
            d.message.lines().next().unwrap_or(""),
            d.range
        );
        match counts.iter_mut().find(|(k, _)| *k == key) {
            Some((_, n)) => *n += 1,
            None => counts.push((key, 1)),
        }
    }
    counts
}

#[test]
fn undefined_name_is_published_exactly_once() {
    let root = build_fixture(true);
    let mut a = analysis_for(&root);

    let buf = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

main =
    println thisNameDoesNotExist
";
    a.set_document(main_url(&root), buf.to_string());

    let diags = a.diagnostics(&main_url(&root));
    let dupes: Vec<_> = publish_counts(&diags)
        .into_iter()
        .filter(|(_, n)| *n > 1)
        .collect();
    assert!(
        dupes.is_empty(),
        "each diagnostic must reach the editor ONCE — `sky check` prints one \
         line per naming error and the editor must agree. Duplicated: {dupes:#?}\n\
         all diags: {diags:#?}"
    );
    assert_eq!(
        errors(&diags).len(),
        1,
        "one undefined name is one error; got: {:#?}",
        errors(&diags)
    );
}

#[test]
fn ambiguous_name_is_published_exactly_once_in_every_namespace() {
    // `[E1012]` fires in three namespaces. Each is produced by `hir::resolve`,
    // so each was doubled by the same path; each is asserted here so a partial
    // regression (say, types only) still goes red.
    for (label, deps, buf) in [
        (
            "value",
            [
                ("Alpha.sky", "module Alpha exposing (..)\n\nlabel : String\nlabel =\n    \"A\"\n"),
                ("Beta.sky", "module Beta exposing (..)\n\nlabel : String\nlabel =\n    \"B\"\n"),
            ],
            "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Std.Log exposing (println)\nimport Alpha exposing (..)\nimport Beta exposing (..)\n\nmain =\n    println label\n",
        ),
        (
            "constructor",
            [
                ("Alpha.sky", "module Alpha exposing (..)\n\ntype One\n    = Same\n"),
                ("Beta.sky", "module Beta exposing (..)\n\ntype Two\n    = Same\n"),
            ],
            "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Alpha exposing (..)\nimport Beta exposing (..)\n\nmain =\n    Same\n",
        ),
        (
            "type",
            [
                ("Alpha.sky", "module Alpha exposing (..)\n\ntype Shape\n    = Circle\n"),
                ("Beta.sky", "module Beta exposing (..)\n\ntype Shape\n    = Square\n"),
            ],
            "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Alpha exposing (..)\nimport Beta exposing (..)\n\nmain : Shape\nmain =\n    Circle\n",
        ),
    ] {
        let root = build_fixture(true);
        let mut a = analysis_for(&root);
        for (file, src) in deps {
            a.set_document(sibling_url(&root, file), src.to_string());
        }
        a.set_document(main_url(&root), buf.to_string());

        let diags = a.diagnostics(&main_url(&root));
        let e1012: Vec<_> = diags
            .iter()
            .filter(|d| {
                d.code
                    == Some(tower_lsp::lsp_types::NumberOrString::String(
                        "E1012".to_string(),
                    ))
            })
            .collect();
        assert_eq!(
            e1012.len(),
            1,
            "the {label}-namespace ambiguity must publish EXACTLY ONE [E1012] \
             (it was published twice before the dedupe); got: {e1012:#?}"
        );
    }
}

#[test]
fn unambiguous_qualified_reference_publishes_no_e1012() {
    // The twin for the three cases above: qualifying the reference removes the
    // defect, so a compiler that flagged everything would fail here.
    let root = build_fixture(true);
    let mut a = analysis_for(&root);
    a.set_document(
        sibling_url(&root, "Alpha.sky"),
        "module Alpha exposing (..)\n\nlabel : String\nlabel =\n    \"A\"\n".to_string(),
    );
    a.set_document(
        sibling_url(&root, "Beta.sky"),
        "module Beta exposing (..)\n\nlabel : String\nlabel =\n    \"B\"\n".to_string(),
    );
    let buf = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)
import Alpha exposing (..)
import Beta exposing (..)

main =
    println Alpha.label
";
    a.set_document(main_url(&root), buf.to_string());

    let diags = a.diagnostics(&main_url(&root));
    let errs = errors(&diags);
    assert!(
        errs.is_empty(),
        "a QUALIFIED reference resolves the ambiguity and must publish nothing \
         — otherwise the [E1012] assertions above would pass against a build \
         that rejects every program; got: {errs:#?}"
    );
}

#[test]
fn over_application_publishes_e2007_at_the_call() {
    // `[E2007]` had no LSP-level coverage at all: `ty/src/infer.rs`'s arity gate
    // was asserted only through the reject corpus, which reads exit codes, not
    // what an editor renders. Over-application is a top-3 typo class, so the
    // range matters — it must underline the CALL, not the enclosing def.
    let root = build_fixture(true);
    let mut a = analysis_for(&root);

    let buf = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)


twice : Int -> Int
twice n =
    n * 2


main =
    println (String.fromInt (twice 1 2))
";
    a.set_document(main_url(&root), buf.to_string());

    let diags = a.diagnostics(&main_url(&root));
    let e2007: Vec<_> = diags
        .iter()
        .filter(|d| {
            d.code
                == Some(tower_lsp::lsp_types::NumberOrString::String(
                    "E2007".to_string(),
                ))
        })
        .collect();
    assert_eq!(
        e2007.len(),
        1,
        "over-applying a 1-arg function must publish exactly one [E2007]; \
         got: {diags:#?}"
    );
    let d = e2007[0];
    assert!(
        d.message.contains("twice") && d.message.contains("1-arg"),
        "the message must name the callee and its declared arity; got: {}",
        d.message
    );
    // The span is the call EXPRESSION — `(twice 1 2)`, parens included — on the
    // body line, not the `main =` header the def-level fallback would pick.
    let call = pos_in(buf, "(twice 1 2)", 0);
    assert_eq!(
        d.range.start, call,
        "the [E2007] range must underline the over-applied CALL, not the \
         enclosing def; got: {:?}",
        d.range
    );
    assert_eq!(
        d.range.end.line, call.line,
        "the [E2007] range must not spill past the call's own line; got: {:?}",
        d.range
    );
}

#[test]
fn correct_arity_call_publishes_no_e2007() {
    // Twin for `over_application_publishes_e2007_at_the_call`.
    let root = build_fixture(true);
    let mut a = analysis_for(&root);

    let buf = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)


twice : Int -> Int
twice n =
    n * 2


main =
    println (String.fromInt (twice 1))
";
    a.set_document(main_url(&root), buf.to_string());

    let diags = a.diagnostics(&main_url(&root));
    let errs = errors(&diags);
    assert!(
        errs.is_empty(),
        "the correctly-applied twin must publish nothing; got: {errs:#?}"
    );
}
