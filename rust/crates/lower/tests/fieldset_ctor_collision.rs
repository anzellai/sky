//! Regression: the `record_fieldsets` collision that makes a well-typed
//! 10-line program compile clean and panic at runtime.
//!
//! ```elm
//! type alias Kv = { key : String, value : String }
//! mk : String -> String -> Kv
//! mk k v = { key = k, value = v }
//! main = println (mk "a" "42").value
//! ```
//! → `Types OK` … then `rt.Coerce: expected rt.SkyADT, got string (42)`.
//!
//! WHY. `record_fieldsets` (`lower/src/lower.rs`) is keyed on the sorted
//! field-NAME vector, so a user `{ key, value : String }` shares its key with
//! the stdlib's `Std.Analytics.EventProp = { key : String, value : PropValue }`
//! — which is in EVERY compilation, no import required.
//! `select_record_candidate` is meant to disambiguate by field TYPE, but the
//! lowerer's typed table (`Typer::body_types`, deliberately NOT seeded from
//! annotations so codegen stays byte-stable) records a constructor's
//! param-valued fields as unsolved `Ty::Var`s. No candidate then compares
//! equal, and the resolver fell back to `candidates.first()` — an ARBITRARY,
//! registration-order pick that fabricated `EventProp_R` and coerced a
//! `string` into an ADT slot.
//!
//! The contract these tests pin: **a record is never resolved onto a nominal
//! the field types contradict, and never onto one of several type-distinct
//! candidates that the field types cannot discriminate.** An undiscriminated
//! record falls through to its structural form, which is always sound.
//!
//! D1/D2 of `docs/rust-rewrite/13-change-verification-and-edge-cases.md`.

use base::Name;
use lower::goty::{sky_ty_to_go, Nominal, NominalKind, TypeEnv};
use lower::ir::GoTy;
use ty::Ty;

fn app0(n: &str) -> Ty {
    Ty::App(Name::new(n), vec![])
}
fn field(n: &str, t: Ty) -> (Name, Ty) {
    (Name::new(n), t)
}

/// `Std.Analytics.EventProp {key:String, value:PropValue}` registered FIRST
/// (it is stdlib — it always precedes user modules), then the user's alias.
/// `also_user` mirrors a program that declares `type alias Kv`; without it the
/// program used a bare anonymous record and only the stdlib alias exists.
fn env_with_collision(also_user: bool) -> TypeEnv {
    let mut env = TypeEnv::default();
    env.nominal.insert(
        "PropValue".into(),
        Nominal {
            go_name: "Std_Analytics_PropValue".into(),
            kind: NominalKind::Adt,
            type_arity: 0,
            opaque: false,
        },
    );
    let mut cands = vec!["Std_Analytics_EventProp_R".to_string()];
    env.record_templates.insert(
        "Std_Analytics_EventProp_R".into(),
        vec![
            ("key".into(), app0("String")),
            ("value".into(), app0("PropValue")),
        ],
    );
    if also_user {
        cands.push("Main_Kv_R".to_string());
        env.record_templates.insert(
            "Main_Kv_R".into(),
            vec![
                ("key".into(), app0("String")),
                ("value".into(), app0("String")),
            ],
        );
    }
    env.record_fieldsets
        .insert(vec!["key".into(), "value".into()], cands);
    env
}

/// THE REPRO, at the unit layer. `mk k v = { key = k, value = v }` records the
/// literal as `{ key : t0, value : t1 }` — both field types unsolved. Two
/// type-distinct aliases share the `[key, value]` name key and NEITHER is
/// discriminated by `t0`/`t1`. Picking either is a guess; picking the ADT-valued
/// one emits `rt.Coerce[PropValue](string)` and panics.
#[test]
fn undiscriminated_record_does_not_pick_an_arbitrary_colliding_alias() {
    let env = env_with_collision(true);
    let rec = Ty::Record(
        vec![
            field("key", Ty::Var(Name::new("t0"))),
            field("value", Ty::Var(Name::new("t1"))),
        ],
        None,
    );
    let got = sky_ty_to_go(&rec, &env);
    assert_ne!(
        got,
        GoTy::Named("Std_Analytics_EventProp_R".into(), vec![]),
        "a record whose field types are unknown must NOT be resolved onto the \
         stdlib EventProp alias by registration order — that is the coercion \
         that panics at runtime"
    );
}

/// One known field is enough to be wrong: `anon v = { key = "a", value = v }`
/// records `{ key : String, value : t0 }`. `value` cannot discriminate, so the
/// two aliases are still tied — and `key` matches both.
#[test]
fn partially_unknown_record_does_not_pick_an_arbitrary_colliding_alias() {
    let env = env_with_collision(true);
    let rec = Ty::Record(
        vec![
            field("key", app0("String")),
            field("value", Ty::Var(Name::new("t0"))),
        ],
        None,
    );
    assert_ne!(
        sky_ty_to_go(&rec, &env),
        GoTy::Named("Std_Analytics_EventProp_R".into(), vec![]),
        "one unsolved field must not hand the record to the type-distinct alias"
    );
}

/// The no-user-alias face: the program writes a bare `{ key : String, value :
/// String }` and declares no alias, so `EventProp_R` is the ONLY candidate. A
/// lone candidate was returned with no type check at all — but its `value` is a
/// `PropValue` ADT and this record's is a `String`. That is a definite
/// contradiction, not an ambiguity, and must never be nominalised.
#[test]
fn lone_type_contradicting_candidate_is_not_nominalised() {
    let env = env_with_collision(false);
    let rec = Ty::Record(
        vec![
            field("key", app0("String")),
            field("value", app0("String")),
        ],
        None,
    );
    assert_ne!(
        sky_ty_to_go(&rec, &env),
        GoTy::Named("Std_Analytics_EventProp_R".into(), vec![]),
        "a String-valued record must not resolve onto an ADT-valued alias just \
         because it is the only one sharing the field NAMES"
    );
}

// ---- the behaviour that must NOT change ---------------------------------

/// Fully-determined fields still resolve to the matching alias. This is the
/// v0.19.1 fix and the whole point of the structural→nominal path.
#[test]
fn determined_record_still_resolves_to_the_type_matching_alias() {
    let env = env_with_collision(true);
    let user = Ty::Record(
        vec![
            field("key", app0("String")),
            field("value", app0("String")),
        ],
        None,
    );
    assert_eq!(
        sky_ty_to_go(&user, &env),
        GoTy::Named("Main_Kv_R".into(), vec![]),
        "String-valued record → the String-typed alias"
    );

    let prop = Ty::Record(
        vec![
            field("key", app0("String")),
            field("value", app0("PropValue")),
        ],
        None,
    );
    assert_eq!(
        sky_ty_to_go(&prop, &env),
        GoTy::Named("Std_Analytics_EventProp_R".into(), vec![]),
        "PropValue-valued record → the PropValue-typed alias"
    );
}

/// A SINGLE candidate that the record does not contradict still resolves, even
/// with unknown field types. This is the ordinary structural→nominal case (an
/// unannotated helper inferring a subset/erased view of the one alias with that
/// field-name set) and the baseline every example's emitted Go depends on.
#[test]
fn lone_uncontradicted_candidate_still_resolves_under_unknown_fields() {
    let mut env = TypeEnv::default();
    env.record_fieldsets.insert(
        vec!["count".into(), "name".into()],
        vec!["Main_Model_R".into()],
    );
    env.record_templates.insert(
        "Main_Model_R".into(),
        vec![
            ("count".into(), app0("Int")),
            ("name".into(), app0("String")),
        ],
    );
    let rec = Ty::Record(
        vec![
            field("count", Ty::Var(Name::new("t0"))),
            field("name", Ty::Var(Name::new("t1"))),
        ],
        None,
    );
    assert_eq!(
        sky_ty_to_go(&rec, &env),
        GoTy::Named("Main_Model_R".into(), vec![]),
        "an unknown-typed record with exactly one uncontradicted alias must \
         still resolve nominally — the structural→nominal baseline"
    );
}
