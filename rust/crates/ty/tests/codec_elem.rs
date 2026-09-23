//! **`[E2009]` — the un-derivable-codec-element check, and its ACCEPTED TWINS.**
//!
//! `Std.Codec.auto` derives a codec by RUNTIME reflection over a witness record
//! VALUE's Go type. A `List <Rec>` field whose witness list is EMPTY, with no
//! `: Codec T` annotation pinning the element, lowers to Go `[]any` and PANICS at
//! decode (`Codec.auto: cannot decode kind interface`) out of a program that
//! passed `sky check`. It is now a check-time type error.
//!
//! # Why this file is not just "more reject cases"
//!
//! An over-rejecting checker is strictly worse than the runtime panic it
//! replaces, so the reject assertion is worth nothing without the accept
//! assertions beside it. The discriminator is one distinction: the element is a
//! genuinely-FREE inference var (fires) versus a CONCRETE type or a rigid user
//! quantifier (silent). Each reject below is paired with the minimally-different
//! program that must still compile.

use hir::SourceDb;
use std::path::PathBuf;
use ty::reject_corpus as rc;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        if !dir.pop() {
            panic!("could not locate repo root (no sky-stdlib ancestor)");
        }
    }
}

/// Check ONE `Main` module against the real stdlib.
fn check(src: &str) -> ty::CheckOutput {
    let mut db = SourceDb::new();
    for (name, parse) in rc::load_stdlib(&repo_root()) {
        db.add_module(&name, parse);
    }
    let mid = db.add_module("Main", syntax::parse(src, base::FileId(0)));
    ty::check_modules(&db, &[mid])
}

fn e2009(out: &ty::CheckOutput) -> Vec<&diagnostics::Diagnostic> {
    out.diagnostics
        .iter()
        .filter(|d| d.code.0 == "E2009")
        .collect()
}

/// A whole module around `decls`, with the imports every case needs.
fn module(decls: &str) -> String {
    format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Codec as Codec exposing (Codec)\n\
         import Std.Log exposing (println)\n\n\n{decls}"
    )
}

/// Assert a program is ACCEPTED — no `[E2009]`, and no type error at all (an
/// accept twin that trips a DIFFERENT diagnostic proves nothing).
fn assert_accepted(label: &str, decls: &str) {
    let out = check(&module(decls));
    assert!(
        e2009(&out).is_empty(),
        "{label}: must NOT be rejected by [E2009] — over-rejection is worse than \
         the runtime panic this check replaces. Got: {:?}",
        e2009(&out).iter().map(|d| &d.message).collect::<Vec<_>>()
    );
    assert_eq!(
        out.type_errors,
        0,
        "{label}: accepted twin must type-check cleanly, got {:?}",
        out.diagnostics
            .iter()
            .map(|d| format!("[{}] {}", d.code.0, d.message))
            .collect::<Vec<_>>()
    );
}

/// Assert a program is REJECTED by exactly one `[E2009]` naming `container`.
fn assert_rejected(label: &str, decls: &str, container: &str) -> diagnostics::Diagnostic {
    let out = check(&module(decls));
    let ds = e2009(&out);
    assert_eq!(
        ds.len(),
        1,
        "{label}: expected exactly one [E2009] (one defect, one diagnostic), got {:?}",
        ds.iter().map(|d| &d.message).collect::<Vec<_>>()
    );
    let d = ds[0];
    assert!(
        d.message.contains(container),
        "{label}: the diagnostic must NAME the container `{container}`; got: {}",
        d.message
    );
    assert!(
        d.severity == diagnostics::Severity::Error,
        "{label}: [E2009] must be an Error"
    );
    d.clone()
}

// ---- REJECTION ---------------------------------------------------------

/// The core bug: an unannotated codec binding whose witness has an empty
/// list-of-record field. The element is a free inference var def-locally.
#[test]
fn rejects_empty_list_witness_without_annotation() {
    let d = assert_rejected(
        "empty-list witness, no annotation",
        "type alias Item =\n    { name : String, qty : Int }\n\n\n\
         badCodec =\n    Codec.auto { id = \"\", items = [] }\n\n\n\
         main =\n    println \"hi\"\n",
        "List",
    );
    assert_eq!(
        d.labels.len(),
        1,
        "[E2009] must carry a source label, got {:?}",
        d.labels
    );
    // The panic it prevents is named, and the workaround offered.
    assert!(d.message.contains("cannot decode kind interface"), "{}", d.message);
    let sug = d.suggestion.clone().unwrap_or_default();
    assert!(sug.contains("Codec") && sug.contains("non-empty"), "{sug}");
}

/// `[E2009]` must be counted as a TYPE error, or `sky check` would print it and
/// carry on to `go build`.
#[test]
fn counts_as_a_type_error() {
    let out = check(&module(
        "badCodec =\n    Codec.auto { id = \"\", items = [] }\n\n\n\
         main =\n    println \"hi\"\n",
    ));
    assert!(
        out.type_errors >= 1,
        "[E2009] must count in `type_errors`, got {}",
        out.type_errors
    );
    assert_eq!(e2009(&out).len(), 1);
}

// ---- ACCEPTED TWINS ----------------------------------------------------

/// The annotation pins the element to a concrete record — the recorded witness
/// carries `List Item`, an `App`, not a free var. Must stay silent.
#[test]
fn accepts_annotated_codec_binding() {
    assert_accepted(
        "annotated: element pinned to Item",
        "type alias Item =\n    { name : String, qty : Int }\n\n\n\
         type alias Tmpl =\n    { id : String, items : List Item }\n\n\n\
         tmplCodec : Codec Tmpl\n\
         tmplCodec =\n    Codec.auto { id = \"\", items = [] }\n\n\n\
         main =\n    println \"hi\"\n",
    );
}

/// A non-empty witness list pins the element through the element value.
#[test]
fn accepts_non_empty_witness_list() {
    assert_accepted(
        "non-empty witness",
        "goodCodec =\n    Codec.auto { id = \"\", items = [ { name = \"\", qty = 0 } ] }\n\n\n\
         main =\n    println \"hi\"\n",
    );
}

/// THE trap: a genuinely-polymorphic codec helper carries a RIGID user
/// quantifier `a`, not a flex var. Firing on it would break every generic codec
/// helper. A record witness whose list element is the rigid `a` must stay silent.
#[test]
fn accepts_polymorphic_codec_helper() {
    assert_accepted(
        "polymorphic helper: rigid quantifier element",
        "listCodec : List a -> Codec (List a)\n\
         listCodec witness =\n    Codec.auto witness\n\n\n\
         main =\n    println \"hi\"\n",
    );
}

/// A witness with no collection at all is never our business.
#[test]
fn accepts_flat_record_witness() {
    assert_accepted(
        "flat record, no list field",
        "flatCodec =\n    Codec.auto { id = \"\", n = 0 }\n\n\n\
         main =\n    println \"hi\"\n",
    );
}
