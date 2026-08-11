//! **`[E2008]` — the unsupported-`Dict`-key check, and its ACCEPTED TWINS.**
//!
//! A Sky `Dict k v` is a Go `map[string]v`: the key is stringified on the way in
//! and decoded on the way out, and the decode is only definable for five key
//! types. For a COMPOSITE key it can never be defined — `fmt.Sprintf("%v", key)`
//! is not injective, so ( "a b", "c" ) and ( "a", "b c" ) both render `{a b c}`,
//! two distinct keys collide, and one entry is silently lost. That used to be a
//! RUNTIME panic (`rt.Dict: unsupported key type`, classified
//! `UnsupportedDictKey`) out of a program that had passed `sky check`; it is now
//! a check-time type error.
//!
//! # Why this file is not just "more reject cases"
//!
//! **An over-rejecting checker is strictly worse than the runtime panic it
//! replaces**, so the reject assertions here are worth nothing without the
//! accept assertions beside them: the reject corpus entry
//! (`tests/reject/corpus/dict_composite_key.sky`) is satisfied by a compiler
//! that rejects EVERY program, and only these twins falsify that. Every reject
//! test below is therefore paired with the minimally-different program that must
//! still compile, and the two most load-bearing accepts —
//! [`accepts_every_supported_key_type`] and
//! [`accepts_key_polymorphic_signature`] — are named in that corpus file's
//! header as its twins.
//!
//! The single most important row is [`accepts_key_polymorphic_signature`]:
//! `keysOf : Dict k v -> List k` is ordinary, valid, widely-used Sky and appears
//! in essentially every codebase that touches dictionaries. A check that fires
//! on it would break all of them.

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

/// Check ONE `Main` module against the real stdlib — the same world
/// `ty::reject_corpus` runs the corpus in, so a verdict here means the same
/// thing it means there. The stdlib parse is built once per test.
fn check(src: &str) -> ty::CheckOutput {
    let mut db = SourceDb::new();
    for (name, parse) in rc::load_stdlib(&repo_root()) {
        db.add_module(&name, parse);
    }
    let mid = db.add_module("Main", syntax::parse(src, base::FileId(0)));
    ty::check_modules(&db, &[mid])
}

fn e2008(out: &ty::CheckOutput) -> Vec<&diagnostics::Diagnostic> {
    out.diagnostics
        .iter()
        .filter(|d| d.code.0 == "E2008")
        .collect()
}

/// A whole module around `decls`, with the imports every case needs.
fn module(decls: &str) -> String {
    format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Dict as Dict exposing (Dict)\n\
         import Sky.Core.List as List\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\n{decls}"
    )
}

/// Assert a program is ACCEPTED — no `[E2008]`, and no type error at all (an
/// accept twin that trips a DIFFERENT diagnostic proves nothing).
fn assert_accepted(label: &str, decls: &str) {
    let out = check(&module(decls));
    assert!(
        e2008(&out).is_empty(),
        "{label}: must NOT be rejected by [E2008] — over-rejection is worse than \
         the runtime panic this check replaces. Got: {:?}",
        e2008(&out).iter().map(|d| &d.message).collect::<Vec<_>>()
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

/// Assert a program is REJECTED by exactly one `[E2008]` naming `key`.
fn assert_rejected(label: &str, decls: &str, key: &str) -> diagnostics::Diagnostic {
    let out = check(&module(decls));
    let ds = e2008(&out);
    assert_eq!(
        ds.len(),
        1,
        "{label}: expected exactly one [E2008] (one defect, one diagnostic), got {:?}",
        ds.iter().map(|d| &d.message).collect::<Vec<_>>()
    );
    let d = ds[0];
    assert!(
        d.message.contains(key),
        "{label}: the diagnostic must NAME the offending key type `{key}`; got: {}",
        d.message
    );
    assert!(
        d.severity == diagnostics::Severity::Error,
        "{label}: [E2008] must be an Error"
    );
    d.clone()
}

// ---- ACCEPTED TWINS ----------------------------------------------------

/// THE twin for the reject corpus entry: all five supported key types, each in
/// the same program shape the corpus file rejects.
///
/// **Do not narrow this list.** `Float` and `Bool` keys decode and order
/// correctly on `main` today (float keys sort 1.5 before 2.5; bool keys visit
/// `False` then `True`), so rejecting them would break working programs — a
/// strictly worse outcome than the panic the check exists to remove.
#[test]
fn accepts_every_supported_key_type() {
    for (ty_name, lit) in [
        ("String", "\"k\""),
        ("Int", "1"),
        ("Float", "1.5"),
        ("Char", "'a'"),
        ("Bool", "True"),
    ] {
        assert_accepted(
            ty_name,
            &format!(
                "d : Dict {ty_name} String\n\
                 d =\n    Dict.insert {lit} \"v\" Dict.empty\n\n\n\
                 main =\n    println (String.fromInt (List.length (Dict.keys d)))\n"
            ),
        );
    }
    // The five names in the check's own table are exactly the five above.
    assert_eq!(
        ty::dictkey::SUPPORTED_DICT_KEYS,
        ["String", "Int", "Float", "Char", "Bool"]
    );
}

/// THE trap. `Dict k v` with `k` a type VARIABLE is ordinary Sky — every generic
/// dictionary helper has this shape. Firing here would break every codebase.
#[test]
fn accepts_key_polymorphic_signature() {
    assert_accepted(
        "key-polymorphic helper",
        "keysOf : Dict k v -> List k\n\
         keysOf d =\n    Dict.keys d\n\n\n\
         countKeys : Dict k v -> Int\n\
         countKeys d =\n    List.length (keysOf d)\n\n\n\
         main =\n    println (String.fromInt (countKeys (Dict.insert 'a' 1 Dict.empty)))\n",
    );
}

/// `Dict.empty` with no key type inferred yet: the key is an unresolved flexible
/// var, which is `Unknown`, which is silent.
#[test]
fn accepts_dict_empty_with_no_inferred_key() {
    assert_accepted(
        "Dict.empty unconstrained",
        "mt : Dict k v\n\
         mt =\n    Dict.empty\n\n\n\
         main =\n    println (String.fromInt (Dict.size mt))\n",
    );
}

/// An alias that resolves to a SUPPORTED key must stay accepted — the same
/// alias expansion that closes the `type alias Coord = ( Int, Int )` bypass must
/// not mistake `UserId` for a nominal type of its own.
#[test]
fn accepts_alias_to_a_supported_key() {
    assert_accepted(
        "alias to String",
        "type alias UserId =\n    String\n\n\n\
         d : Dict UserId Int\n\
         d =\n    Dict.insert \"u1\" 1 Dict.empty\n\n\n\
         main =\n    println (String.fromInt (Dict.size d))\n",
    );
}

/// A composite is fine as a `Dict` VALUE — only the KEY has to decode.
#[test]
fn accepts_composite_in_value_position() {
    assert_accepted(
        "composite value",
        "d : Dict String ( Int, Int )\n\
         d =\n    Dict.insert \"a\" ( 1, 2 ) Dict.empty\n\n\n\
         main =\n    println (String.fromInt (Dict.size d))\n",
    );
}

// ---- REJECTIONS --------------------------------------------------------

/// The corpus case, in-process: a tuple key written through an alias. Pins that
/// alias expansion happens BEFORE classification, so `type alias Coord =
/// ( Int, Int )` is not a bypass.
#[test]
fn rejects_tuple_key_through_an_alias() {
    let d = assert_rejected(
        "alias to tuple",
        "type alias Coord =\n    ( Int, Int )\n\n\n\
         grid : Dict Coord String\n\
         grid =\n    Dict.insert ( 1, 2 ) \"wall\" Dict.empty\n\n\n\
         main =\n    println (String.fromInt (Dict.size grid))\n",
        "( Int, Int )",
    );
    // Source context is the whole point of moving this to check time.
    assert_eq!(
        d.labels.len(),
        1,
        "[E2008] must carry a source label, got {:?}",
        d.labels
    );
    // ... anchored on the WRITTEN annotation type (`Dict Coord String`), which
    // is the text the user has to edit, not the binding one line below it.
    let src = module(
        "type alias Coord =\n    ( Int, Int )\n\n\n\
         grid : Dict Coord String\n\
         grid =\n    Dict.insert ( 1, 2 ) \"wall\" Dict.empty\n\n\n\
         main =\n    println (String.fromInt (Dict.size grid))\n",
    );
    let s = d.labels[0].span;
    assert_eq!(
        &src[s.range.0 as usize..s.range.1 as usize],
        "Dict Coord String",
        "the caret must sit on the annotation's type"
    );
    // The workaround is offered, not just the refusal.
    let sug = d.suggestion.clone().unwrap_or_default();
    assert!(sug.contains("String") && sug.contains("List"), "{sug}");
}

/// No annotation anywhere: the composite key exists ONLY in an inferred type, so
/// an annotation-only check would miss it entirely. The caret falls back to the
/// earliest expression that builds the dictionary.
#[test]
fn rejects_tuple_key_that_is_only_inferred() {
    let d = assert_rejected(
        "inferred tuple key",
        "main =\n    let\n        grid =\n            \
         Dict.insert ( 1, 2 ) \"a\" Dict.empty\n    in\n    \
         println (String.fromInt (Dict.size grid))\n",
        "( Int, Int )",
    );
    assert_eq!(d.labels.len(), 1, "must still carry a source label");
}

/// A key-polymorphic helper stays silent; the CALL SITE that instantiates its
/// `k` to a composite is where the composite actually exists, and is rejected
/// there. This is the pair that shows the check is precise rather than blunt.
#[test]
fn rejects_at_the_call_site_that_instantiates_a_composite() {
    assert_rejected(
        "generic helper, composite call site",
        "countKeys : Dict k v -> Int\n\
         countKeys d =\n    List.length (Dict.keys d)\n\n\n\
         main =\n    println (String.fromInt (countKeys (Dict.insert ( 1, 2 ) \"a\" Dict.empty)))\n",
        "( Int, Int )",
    );
}

#[test]
fn rejects_custom_union_record_and_list_keys() {
    assert_rejected(
        "union key",
        "type Color\n    = Red\n    | Green\n\n\n\
         d : Dict Color String\n\
         d =\n    Dict.insert Red \"r\" Dict.empty\n\n\n\
         main =\n    println (String.fromInt (Dict.size d))\n",
        "Color",
    );
    assert_rejected(
        "record key",
        "type alias P =\n    { x : Int }\n\n\n\
         d : Dict P String\n\
         d =\n    Dict.insert { x = 1 } \"a\" Dict.empty\n\n\n\
         main =\n    println (String.fromInt (Dict.size d))\n",
        "x : Int",
    );
    assert_rejected(
        "list key",
        "d : Dict (List Int) String\n\
         d =\n    Dict.insert [ 1 ] \"a\" Dict.empty\n\n\n\
         main =\n    println (String.fromInt (Dict.size d))\n",
        "List Int",
    );
    assert_rejected(
        "Maybe key",
        "d : Dict (Maybe Int) String\n\
         d =\n    Dict.insert (Just 1) \"a\" Dict.empty\n\n\n\
         main =\n    println (String.fromInt (Dict.size d))\n",
        "Maybe Int",
    );
}

/// ONE defect, ONE diagnostic — even when the offending `Dict` is a field of a
/// `Model` that flows through every def in the module. Per-occurrence (or even
/// per-def) reporting would bury a Sky.Live app under dozens of copies of the
/// same message with the same single fix.
#[test]
fn a_model_wide_offending_key_reports_exactly_once() {
    assert_rejected(
        "Model field",
        "type alias Model =\n    { grid : Dict ( Int, Int ) String, n : Int }\n\n\n\
         init : Model\n\
         init =\n    { grid = Dict.empty, n = 0 }\n\n\n\
         bump : Model -> Model\n\
         bump m =\n    { m | n = m.n + 1 }\n\n\n\
         size : Model -> Int\n\
         size m =\n    Dict.size m.grid\n\n\n\
         main =\n    println (String.fromInt (size (bump init)))\n",
        "( Int, Int )",
    );
}

/// Two DIFFERENT offending key types are two different defects, and each gets
/// its own diagnostic — the per-module dedup collapses copies, never distinct
/// defects.
#[test]
fn two_distinct_offending_keys_report_twice() {
    let out = check(&module(
        "type Color\n    = Red\n    | Green\n\n\n\
         a : Dict ( Int, Int ) String\n\
         a =\n    Dict.insert ( 1, 2 ) \"a\" Dict.empty\n\n\n\
         b : Dict Color String\n\
         b =\n    Dict.insert Red \"r\" Dict.empty\n\n\n\
         main =\n    println (String.fromInt (Dict.size a + Dict.size b))\n",
    ));
    let ds = e2008(&out);
    assert_eq!(
        ds.len(),
        2,
        "expected one [E2008] per distinct key type, got {:?}",
        ds.iter().map(|d| &d.message).collect::<Vec<_>>()
    );
}

/// `[E2008]` must be counted as a TYPE error, or `sky check` would print it and
/// carry on to `go build` (the driver gates on `type_errors > 0`).
#[test]
fn counts_as_a_type_error() {
    let out = check(&module(
        "d : Dict ( Int, Int ) String\n\
         d =\n    Dict.empty\n\n\n\
         main =\n    println (String.fromInt (Dict.size d))\n",
    ));
    assert_eq!(out.type_errors, 1, "[E2008] must count in `type_errors`");
}
