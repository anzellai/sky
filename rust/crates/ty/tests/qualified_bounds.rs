//! C-11 and B-1/C-9 (v0.27.0 audit): qualified type bounds.
//!
//! **C-11.** Ordering was unconstrained. `compare`, `<`, `List.sort`,
//! `List.sortBy`, `min`/`max` and `Set` accepted any type, so `compare` on two
//! functions type-checked, `Green 2 < Red` compiled, and `List.sort` over a
//! union returned the list unchanged (repros `C/cmp-7`, `C/cmp-9`). Since
//! v0.27.0 these need a real `Comparable` bound: the primitives, `Bool`,
//! `List`/`Maybe`/`Result`/tuples of comparables, closed records of
//! comparables, and custom types whose constructors hold only comparables.
//! The bound survives generalisation: a type variable named `comparable*`
//! carries it through a scheme, so an unannotated `mySort xs = List.sort xs`
//! still refuses a list of functions at its callers.
//!
//! **B-1 / C-9.** `Codec.auto` over a record holding a function panicked at
//! run time, and over a `Secret` or a crypto key encoded `{}` and then failed
//! to decode (the durable snapshot silently stopped). Since v0.27.0
//! `Codec.auto*`, `App.withDurable`, `Std.Db.Table.table`/`insert`,
//! `Jobs.define`/`enqueue` and `Auth.signToken` require an `Encodable` type:
//! no function, `Secret`, crypto key or state, or runtime handle, in either
//! direction. The bound is carried through generic helpers, annotated or not.

use hir::SourceDb;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        if !dir.pop() {
            panic!("could not locate repo root");
        }
    }
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for p in entries {
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

fn stdlib_db() -> SourceDb {
    let root = repo_root();
    let mut files = Vec::new();
    collect_sky(&root.join("sky-stdlib"), &mut files);
    let mut db = SourceDb::new();
    for path in files {
        let Ok(s) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&s, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .filter(|s| !s.is_empty())
            .unwrap_or_default();
        db.add_module(&name, parse);
    }
    db
}

/// The error diagnostics of a one-module program, as `CODE message` lines.
fn errors(src: &str) -> Vec<String> {
    let mut db = stdlib_db();
    let mid = db.add_module("Main", syntax::parse(src, base::FileId(0)));
    ty::check_modules(&db, &[mid])
        .diagnostics
        .iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .map(|d| format!("{} {}", d.code.0, d.message))
        .collect()
}

const HDR: &str = "module Main exposing (main)\n\
                   import Sky.Core.Prelude exposing (..)\n\
                   import Sky.Core.List as List\n\
                   import Sky.Core.Set as Set\n\
                   import Sky.Core.Dict as Dict\n\
                   import Sky.Core.Math as Math\n\
                   import Sky.Core.Task as Task\n\
                   import Sky.Core.Secret as Secret\n\
                   import Sky.Core.Process as Process\n\
                   import Std.Sync as Sync\n\
                   import Std.Codec as Codec exposing (Codec)\n\
                   import Std.Db exposing (Db)\n\
                   import Std.Db.Table as Table\n\
                   import Std.Jobs as Jobs\n\
                   import Std.Auth as Auth\n\
                   import Std.App as App\n\
                   import Std.Decimal as Decimal exposing (Decimal)\n\
                   import Std.Log exposing (println)\n\n\
                   type Color\n    = Red\n    | Green Int\n    | Blue String\n\n\
                   type Boxed\n    = Boxed (Int -> Int)\n\n";

fn program(body: &str) -> String {
    format!("{HDR}{body}\n\nmain : Task Error ()\nmain =\n    println \"ok\"\n")
}

#[track_caller]
fn assert_rejects_with(body: &str, code: &str, needles: &[&str]) -> Vec<String> {
    let errs = errors(&program(body));
    let hit = errs
        .iter()
        .find(|e| e.starts_with(code) && needles.iter().all(|n| e.contains(n)));
    assert!(
        hit.is_some(),
        "expected a [{code}] rejection containing {needles:?}, got: {errs:#?}\n--- program ---\n{body}"
    );
    errs
}

#[track_caller]
fn assert_rejects(body: &str) -> Vec<String> {
    assert_rejects_with(body, "E2001", &[])
}

#[track_caller]
fn assert_accepts(body: &str) {
    let errs = errors(&program(body));
    assert!(
        errs.is_empty(),
        "expected no error, got: {errs:#?}\n--- program ---\n{body}"
    );
}

// ---- C-11: Comparable ------------------------------------------------------

/// Repro family `C/cmp-*`: `compare` on two functions type-checked.
#[test]
fn compare_on_functions_is_rejected() {
    assert_rejects_with(
        "bad : Int\nbad =\n    compare (\\x -> x + 1) (\\x -> x * 2)",
        "E2001",
        &["cannot be ordered", "#comparable-bound"],
    );
}

#[test]
fn less_than_on_functions_is_rejected() {
    assert_rejects("bad : Bool\nbad =\n    (\\x -> x + 1) < (\\x -> x * 2)");
}

/// A record holding a `Secret` has no order.
#[test]
fn ordering_a_record_holding_a_secret_is_rejected() {
    assert_rejects(
        "bad : Bool\nbad =\n    { s = Secret.fromString \"a\" } < { s = Secret.fromString \"b\" }",
    );
}

/// A runtime handle (`Std.Sync.Ref`) has no meaningful order.
#[test]
fn ordering_handles_is_rejected() {
    assert_rejects("bad : Sync.Ref Int -> Sync.Ref Int -> Bool\nbad a b =\n    a < b");
}

/// The bound survives generalisation: an UNANNOTATED helper's scheme carries
/// `comparable`, so its caller cannot hand it a list of functions.
#[test]
fn unannotated_sort_helper_carries_the_bound() {
    assert_rejects(
        "mySort xs =\n    List.sort xs\n\n\nbad : List (Int -> Int)\nbad =\n    mySort [ \\x -> x ]",
    );
}

/// The same helper at a comparable type is fine.
#[test]
fn unannotated_sort_helper_accepts_comparables() {
    assert_accepts(
        "mySort xs =\n    List.sort xs\n\n\ngood : List Int\ngood =\n    mySort [ 3, 1, 2 ]",
    );
}

/// Elm's rule: an annotation whose plain variable the body orders is too
/// general. The message says what changed and how to fix it.
#[test]
fn plain_variable_forced_comparable_gets_the_migration_message() {
    assert_rejects_with(
        "largest : List a -> Maybe a\nlargest xs =\n    List.head (List.reverse (List.sort xs))",
        "E2001",
        &["`a`", "comparable", "#comparable-bound"],
    );
}

/// The same with the variable only in argument position (the checker keeps
/// such a variable flexible, so the obligation is found after the body).
#[test]
fn argument_only_variable_forced_comparable_is_rejected() {
    assert_rejects_with(
        "before : a -> a -> Bool\nbefore x y =\n    x < y",
        "E2001",
        &["`a`", "comparable", "#comparable-bound"],
    );
}

/// The fixed forms: `comparable` in the annotation.
#[test]
fn comparable_named_variable_is_accepted() {
    assert_accepts(
        "largest : List comparable -> Maybe comparable\nlargest xs =\n    List.head (List.reverse (List.sort xs))\n\n\n\
         before : comparable -> comparable -> Bool\nbefore x y =\n    x < y\n\n\n\
         good : Maybe Int\ngood =\n    largest [ 1, 2 ]",
    );
}

/// An annotated `comparable` helper refuses a non-comparable argument.
#[test]
fn comparable_annotated_helper_refuses_functions() {
    assert_rejects(
        "largest : List comparable -> Maybe comparable\nlargest xs =\n    List.head (List.sort xs)\n\n\n\
         bad : Maybe (Int -> Int)\nbad =\n    largest [ \\x -> x ]",
    );
}

/// `sortBy`'s key must be comparable. A union key is fine (derived order); a
/// union that holds a function is not.
#[test]
fn sort_by_a_union_key() {
    assert_accepts("good : List Int\ngood =\n    List.sortBy (\\n -> Green n) [ 3, 1 ]");
    assert_rejects("bad : List Int\nbad =\n    List.sortBy (\\n -> Boxed (\\m -> m + n)) [ 3, 1 ]");
    assert_rejects("bad : List Int\nbad =\n    List.sortBy (\\n -> \\m -> m + n) [ 3, 1 ]");
}

/// `Set` elements must be comparable: a union is fine, a function or a
/// `Secret` is not.
#[test]
fn set_elements_must_be_comparable() {
    assert_accepts("good : Int\ngood =\n    Set.size (Set.fromList [ Red, Green 1, Blue \"x\" ])");
    assert_rejects("bad : Int\nbad =\n    Set.size (Set.fromList [ \\x -> x + 1 ])");
    assert_rejects("bad : Bool\nbad =\n    Set.member (Secret.fromString \"a\") Set.empty");
}

/// `min`/`max` (bare and `Math.`) need comparables.
#[test]
fn min_max_need_comparables() {
    assert_accepts("good : Color\ngood =\n    Math.max Red (Green 9)");
    assert_accepts("good : Int\ngood =\n    max 1 (min 2 3)");
    assert_rejects("bad : Int -> Int\nbad =\n    max (\\x -> x) (\\x -> x + 1)");
    assert_rejects("bad : Boxed\nbad =\n    Math.min (Boxed identity) (Boxed identity)");
}

/// Records: a closed record of comparables orders field by field; an open
/// row cannot be checked, so it is refused.
#[test]
fn records_order_only_when_closed() {
    assert_accepts("good : Int\ngood =\n    compare { a = 1, b = \"x\" } { a = 2, b = \"y\" }");
    assert_rejects("bad r =\n    if r.x > 0 then\n        compare r r\n\n    else\n        0");
}

/// Tuples and lists of comparables are comparable; a tuple holding a function
/// is not.
#[test]
fn tuples_and_lists_propagate_the_bound() {
    assert_accepts("good : Bool\ngood =\n    ( 1, \"a\" ) < ( 1, \"b\" ) && [ Red ] < [ Green 1 ]");
    assert_rejects("bad : Bool\nbad =\n    ( 1, \\x -> x ) < ( 1, \\x -> x )");
    assert_rejects("bad : List (List (Int -> Int))\nbad =\n    List.sort [ [ \\x -> x ] ]");
}

/// A parameterised custom type is comparable exactly when its arguments are.
#[test]
fn parameterised_custom_types() {
    assert_accepts(
        "type Pair a\n    = Pair a a\n\n\ngood : List (Pair Int)\ngood =\n    List.sort [ Pair 1 2 ]",
    );
    assert_rejects(
        "type Pair a\n    = Pair a a\n\n\nbad : List (Pair (Int -> Int))\nbad =\n    List.sort [ Pair identity identity ]",
    );
}

/// A recursive custom type terminates and is comparable.
#[test]
fn recursive_custom_type_is_comparable() {
    assert_accepts(
        "type Tree\n    = Leaf\n    | Node Tree Int Tree\n\n\ngood : List Tree\ngood =\n    List.sort [ Leaf, Node Leaf 1 Leaf ]",
    );
}

/// Opaque stdlib types have no order (`Decimal` has its own `compare`).
#[test]
fn opaque_stdlib_types_are_not_comparable() {
    assert_rejects("bad : Decimal -> Decimal -> Bool\nbad a b =\n    a < b");
}

/// The bound survives let-generalisation too.
#[test]
fn let_bound_sort_helper_carries_the_bound() {
    assert_rejects(
        "bad : List (Int -> Int)\nbad =\n    let\n        s xs =\n            List.sort xs\n    in\n    s [ \\x -> x ]",
    );
    assert_accepts(
        "good : List String\ngood =\n    let\n        s xs =\n            List.sort xs\n    in\n    s [ \"b\", \"a\" ]",
    );
}

// ---- B-1 / C-9: Encodable --------------------------------------------------

/// Repro `C/cd-3`: `Codec.auto` over a record with a function field.
#[test]
fn codec_auto_over_a_function_field_is_rejected() {
    assert_rejects_with(
        "bad : String\nbad =\n    Codec.toJson (Codec.auto { f = \\x -> x + 1 }) { f = \\x -> x * 2 }",
        "E2001",
        &["cannot be encoded", "#encodable-bound"],
    );
}

/// Repro `B/codecsecret`: a `Secret` field encoded as `{}`.
#[test]
fn codec_auto_over_a_secret_is_rejected() {
    assert_rejects(
        "bad : Codec { name : String, secret : Secret.Secret }\nbad =\n    Codec.auto { name = \"\", secret = Secret.fromString \"\" }",
    );
}

/// A handle in the witness type is refused even when the witness holds no
/// handle value: a `Codec` decodes too, and client input must not decode into
/// a handle.
#[test]
fn codec_auto_over_a_handle_type_is_rejected_in_both_directions() {
    assert_rejects(
        "bad : Codec { p : Maybe Process.Process }\nbad =\n    Codec.auto { p = Nothing }",
    );
    assert_rejects(
        "bad : Codec { r : Maybe (Sync.Ref Int) }\nbad =\n    Codec.autoCamel { r = Nothing }",
    );
}

/// A custom type holding a function is refused through its constructors.
#[test]
fn codec_auto_over_a_union_holding_a_function_is_rejected() {
    assert_rejects("bad : Codec { b : Maybe Boxed }\nbad =\n    Codec.auto { b = Nothing }");
}

/// The bound is carried through an UNANNOTATED generic helper.
#[test]
fn unannotated_codec_helper_carries_the_bound() {
    assert_rejects(
        "persist b =\n    Codec.auto b\n\n\nbad : Codec { f : Int -> Int }\nbad =\n    persist { f = \\x -> x }",
    );
    assert_accepts(
        "persist b =\n    Codec.auto b\n\n\ngood : Codec { n : Int }\ngood =\n    persist { n = 0 }",
    );
}

/// The unannotated helper defined AFTER its user (pass 5 infers in source
/// order) still carries the bound.
#[test]
fn unannotated_codec_helper_defined_later_carries_the_bound() {
    assert_rejects(
        "useIt x =\n    persist x\n\n\npersist b =\n    Codec.auto b\n\n\nbad : Codec { f : Int -> Int }\nbad =\n    useIt { f = \\x -> x }",
    );
}

/// The bound is carried through an ANNOTATED generic helper whose variable
/// the body forces to be encodable (the bound is inferred, not an error).
#[test]
fn annotated_codec_helper_carries_the_bound() {
    assert_rejects(
        "persist : a -> Codec a\npersist b =\n    Codec.auto b\n\n\nbad : Codec { f : Int -> Int }\nbad =\n    persist { f = \\x -> x }",
    );
    assert_accepts(
        "persist : a -> Codec a\npersist b =\n    Codec.auto b\n\n\ngood : Codec { n : Int, d : Decimal }\ngood =\n    persist { n = 0, d = Decimal.zero }",
    );
}

/// Two levels of annotated helpers.
#[test]
fn nested_annotated_helpers_carry_the_bound() {
    assert_rejects(
        "persist : a -> Codec a\npersist b =\n    Codec.auto b\n\n\n\
         wrap : a -> Codec a\nwrap x =\n    persist x\n\n\n\
         bad : Codec (Int -> Int)\nbad =\n    wrap (\\x -> x)",
    );
}

/// `Std.Db.Table.table` over a record with a `Secret`.
#[test]
fn table_over_a_secret_is_rejected() {
    assert_rejects(
        "bad : Table.Table { key : Secret.Secret }\nbad =\n    Table.table \"keys\" { key = Secret.fromString \"\" }",
    );
    assert_accepts(
        "good : Table.Table { id : String, n : Int }\ngood =\n    Table.table \"rows\" { id = \"\", n = 0 }",
    );
}

/// `Jobs.define` decodes the payload, `Jobs.enqueue` encodes it.
#[test]
fn job_payload_must_be_encodable() {
    assert_rejects(
        "bad : Jobs.Job (Int -> Int)\nbad =\n    Jobs.define \"j\" (\\f -> Task.succeed ())",
    );
    assert_accepts(
        "good : Jobs.Job { n : Int }\ngood =\n    Jobs.define \"j\" (\\p -> Task.succeed ())",
    );
}

/// `Auth.signToken` encodes its claims by reflection.
#[test]
fn sign_token_claims_must_be_encodable() {
    assert_rejects(
        "bad : Result Error String\nbad =\n    Auth.signToken (Secret.fromString \"k\") { s = Secret.fromString \"x\" } 60",
    );
    assert_accepts(
        "good : Result Error String\ngood =\n    Auth.signToken (Secret.fromString \"k\") { sub = \"u1\" } 60",
    );
}

/// `App.withDurable` refuses a model holding a handle (Breaking: a restored
/// handle would be dead anyway).
#[test]
fn with_durable_model_holding_a_handle_is_rejected() {
    assert_rejects(
        "wire : Db -> Codec { p : Process.Process } -> App.App f s pg { p : Process.Process } msg k -> App.App f s pg { p : Process.Process } msg k\n\
         wire db c a =\n    App.withDurable db c a",
    );
}

/// Ordinary persisted shapes stay legal: primitives, lists, `Maybe`, `Dict`,
/// `Set`, tuples, records, custom types, `Decimal`.
#[test]
fn ordinary_persisted_shapes_are_accepted() {
    assert_accepts(
        "type alias Row =\n    { id : String\n    , tags : List String\n    , score : Maybe Float\n    , counts : Dict.Dict String Int\n    , seen : Set Int\n    , pair : ( Int, Bool )\n    , color : Color\n    , price : Decimal\n    }\n\n\n\
         codec : Codec Row\ncodec =\n    Codec.auto { id = \"\", tags = [ \"a\" ], score = Nothing, counts = Dict.empty, seen = Set.empty, pair = ( 0, False ), color = Red, price = Decimal.zero }",
    );
}

// ---- the public API S4's Sky.Spa split calls --------------------------------

#[test]
fn encodable_check_api() {
    use ty::Ty;
    let db = stdlib_db();
    let secret = Ty::app("Sky.Core.Secret.Secret", vec![]);
    let rec = Ty::Record(
        vec![
            (base::Name::new("n"), Ty::app("Int", vec![])),
            (base::Name::new("s"), Ty::app("Maybe", vec![secret])),
        ],
        None,
    );
    let bad = ty::encodable::check(&rec, &db).expect("a Secret field is unencodable");
    assert!(bad.offending.contains("Secret"), "{bad:?}");
    let func = Ty::Fun(
        Box::new(Ty::app("Int", vec![])),
        Box::new(Ty::app("Int", vec![])),
    );
    assert!(ty::encodable::check(&Ty::app("List", vec![func]), &db).is_some());
    let good = Ty::Record(
        vec![
            (base::Name::new("n"), Ty::app("Int", vec![])),
            (base::Name::new("d"), Ty::app("Std.Decimal.Decimal", vec![])),
        ],
        None,
    );
    assert!(ty::encodable::check(&good, &db).is_none());
}

/// Every stdlib API that encodes a free type by reflection carries the
/// `Encodable` bound in the check-only channel. Fails if a signature's
/// variable is renamed (the override would then silently bound nothing).
#[test]
fn every_encodable_api_carries_the_bound() {
    use hir::SkyDb;
    let db = stdlib_db();
    let world = ty::World::build(&db);
    for (module, name) in [
        ("Std.Codec", "auto"),
        ("Std.Codec", "autoCamel"),
        ("Std.Codec", "autoWith"),
        ("Std.App", "withDurable"),
        ("Std.App", "withDurableId"),
        ("Std.Db.Table", "table"),
        ("Std.Db.Table", "insert"),
        ("Std.Jobs", "define"),
        ("Std.Jobs", "enqueue"),
        ("Std.Jobs", "enqueueIn"),
        ("Std.Auth", "signToken"),
        ("Std.Auth", "signSlidingToken"),
    ] {
        let m = db
            .module_by_name(module)
            .unwrap_or_else(|| panic!("{module} is loaded"));
        let def = db.intern_def(m, &base::Name::new(name), hir::DefKind::Value);
        let s = world
            .bound_check_sigs
            .get(&def)
            .unwrap_or_else(|| panic!("{module}.{name} has a bounded check-only scheme"));
        assert!(
            s.vars.iter().any(|v| v.as_str().starts_with("encodable")),
            "{module}.{name} must carry an `encodable` variable, got {}",
            s.ty.render()
        );
    }
}
