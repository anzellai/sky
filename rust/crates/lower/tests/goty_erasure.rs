//! `goty` unit depth: the Sky-type → Go-type map (`lower::goty::sky_ty_to_go`).
//! This is the exact layer that produced the #166 regressions
//! (record-field-drop, `Foo_R[any]` panic, Dict-field collapse) and the
//! v0.19.1 record-fieldset name-collision codegen bug. The map is total and
//! pure over a `TypeEnv`, so it unit-tests directly — no full pipeline needed.
//!
//! Each test cites the D-dimension it guards from
//! `docs/rust-rewrite/13-change-verification-and-edge-cases.md` (§1, D2 record
//! shapes / row-poly; D1 type-reference resolution).

use base::Name;
use lower::goty::{sky_ty_to_go, Nominal, NominalKind, TypeEnv};
use lower::ir::{GoTy, Prim};
use ty::Ty;

// ---- tiny builders --------------------------------------------------------

fn app0(n: &str) -> Ty {
    Ty::App(Name::new(n), vec![])
}
fn field(n: &str, t: Ty) -> (Name, Ty) {
    (Name::new(n), t)
}
fn nominal(go: &str, kind: NominalKind, arity: usize, opaque: bool) -> Nominal {
    Nominal {
        go_name: go.into(),
        kind,
        type_arity: arity,
        opaque,
    }
}
/// Register a nominal under both the flat and module-scoped maps.
fn reg(env: &mut TypeEnv, module: &str, name: &str, n: Nominal) {
    env.nominal.insert(name.into(), n.clone());
    env.nominal_by_module
        .insert((module.into(), name.into()), n);
}

// =========================================================================
// D2 (row 66) — a concrete record whose field-NAME set matches a registered
// `_R` alias lowers to the NOMINAL `_R` struct, NOT `any` — whether the record
// is CLOSED or OPEN (shares an instantiation row var). The open case is the
// exact #166-fix-B trap: an open row that matches a nominal must stay nominal.
// =========================================================================
#[test]
fn concrete_record_resolves_to_nominal_closed_and_open() {
    let mut env = TypeEnv::default();
    env.record_fieldsets
        .insert(vec!["count".into(), "name".into()], vec!["Main_Model_R".into()]);

    let fields = vec![
        field("count", app0("Int")),
        field("name", app0("String")),
    ];
    let closed = Ty::Record(fields.clone(), None);
    assert_eq!(
        sky_ty_to_go(&closed, &env),
        GoTy::Named("Main_Model_R".into(), vec![]),
        "closed record matching a fieldset must be nominal, not any/struct"
    );

    // OPEN record (row var present) — must still resolve to the same nominal.
    let open = Ty::Record(fields, Some(Name::new("r")));
    assert_eq!(
        sky_ty_to_go(&open, &env),
        GoTy::Named("Main_Model_R".into(), vec![]),
        "open record sharing a row var must NOT erase to any (the #166 fix-B trap)"
    );
}

// =========================================================================
// D2 (row 68) — the #166 Std.Db shape: a record with a `Dict String String`
// field keeps the nominal `_R`; and the Dict field itself lowers to
// `map[string]V` with the key PINNED to string (never `map[int]V`, even for a
// `Dict Int _`) — the exact pin that avoids the rt.Coerce runtime panic.
// =========================================================================
#[test]
fn record_with_dict_field_stays_nominal_and_dict_key_pinned() {
    let mut env = TypeEnv::default();
    env.record_fieldsets
        .insert(vec!["id".into(), "meta".into()], vec!["Main_Row_R".into()]);

    let dict_ss = Ty::App(Name::new("Dict"), vec![app0("String"), app0("String")]);
    let rec = Ty::Record(
        vec![field("id", app0("String")), field("meta", dict_ss.clone())],
        None,
    );
    assert_eq!(
        sky_ty_to_go(&rec, &env),
        GoTy::Named("Main_Row_R".into(), vec![]),
        "a Dict-valued-field record must stay the nominal _R, not erase to any"
    );

    // The Dict field's own lowering — value kept, key pinned to string.
    assert_eq!(
        sky_ty_to_go(&dict_ss, &env),
        GoTy::Map(Box::new(GoTy::Bare(Prim::Str)), Box::new(GoTy::Bare(Prim::Str)))
    );
    let dict_is = Ty::App(Name::new("Dict"), vec![app0("Int"), app0("String")]);
    assert_eq!(
        sky_ty_to_go(&dict_is, &env),
        GoTy::Map(Box::new(GoTy::Bare(Prim::Str)), Box::new(GoTy::Bare(Prim::Str))),
        "Dict Int _ must still key on string (runtime stores stringified keys)"
    );
}

// =========================================================================
// D2 (rows 70/71) — the type-level guard against the #166 record-update
// field-drop: an unannotated `view`/`update` helper infers a SUBSET row over
// the app's single Model; that subset must resolve to the NOMINAL Model `_R`
// (the full record the runtime actually holds), not a narrowed struct.
// =========================================================================
#[test]
fn subset_of_model_resolves_to_nominal_model() {
    let mut env = TypeEnv::default();
    // model field-set must be sorted (binary_search is used).
    env.model = Some((
        vec!["active".into(), "age".into(), "name".into()],
        "Main_Model_R".into(),
    ));

    // A one-field subset row `{ name }` (as a helper that only reads .name
    // would infer) must land on the full nominal Model _R.
    let subset = Ty::Record(vec![field("name", app0("String"))], Some(Name::new("r")));
    assert_eq!(
        sky_ty_to_go(&subset, &env),
        GoTy::Named("Main_Model_R".into(), vec![]),
        "a subset row over the Model must resolve to the nominal Model _R"
    );
}

// =========================================================================
// D2 — a record matching NEITHER a fieldset NOR the Model falls to an
// anonymous Go struct whose fields are sorted by Go name (struct field order is
// part of Go type identity — two orderings would be distinct types and reject
// at `go build`).
// =========================================================================
#[test]
fn unmatched_record_is_sorted_anonymous_struct() {
    let env = TypeEnv::default();
    let rec = Ty::Record(
        vec![
            field("name", app0("String")),
            field("age", app0("Int")),
            field("active", app0("Bool")),
        ],
        None,
    );
    assert_eq!(
        sky_ty_to_go(&rec, &env),
        GoTy::Struct(vec![
            (Name::new("Active"), GoTy::Bare(Prim::Bool)),
            (Name::new("Age"), GoTy::Bare(Prim::Int)),
            (Name::new("Name"), GoTy::Bare(Prim::Str)),
        ]),
        "anonymous struct fields must be sorted by Go name for a stable type identity"
    );
}

// =========================================================================
// D1/D2 (v0.19.1 record-fieldset collision) — two aliases share a field-NAME
// set but differ in field TYPES (`EnvForm {key,value:String}` vs
// `EventProp {key,value:PropValue}`). The resolver must pick by field TYPE, not
// let the first-registered win (which mis-typed the other and broke go build).
// =========================================================================
#[test]
fn fieldset_collision_selects_by_field_type() {
    let mut env = TypeEnv::default();
    reg(
        &mut env,
        "Std.Analytics",
        "PropValue",
        nominal("Analytics_PropValue", NominalKind::Adt, 0, false),
    );
    env.record_fieldsets.insert(
        vec!["key".into(), "value".into()],
        vec!["User_EnvForm_R".into(), "Analytics_EventProp_R".into()],
    );
    env.record_templates.insert(
        "User_EnvForm_R".into(),
        vec![("key".into(), app0("String")), ("value".into(), app0("String"))],
    );
    env.record_templates.insert(
        "Analytics_EventProp_R".into(),
        vec![("key".into(), app0("String")), ("value".into(), app0("PropValue"))],
    );

    // {key:String, value:String} → the user's EnvForm (value:String).
    let user_rec = Ty::Record(
        vec![field("key", app0("String")), field("value", app0("String"))],
        None,
    );
    assert_eq!(
        sky_ty_to_go(&user_rec, &env),
        GoTy::Named("User_EnvForm_R".into(), vec![]),
        "a String-valued record must pick the String-typed alias"
    );

    // {key:String, value:PropValue} → the Analytics EventProp (value:PropValue).
    let prop_rec = Ty::Record(
        vec![field("key", app0("String")), field("value", app0("PropValue"))],
        None,
    );
    assert_eq!(
        sky_ty_to_go(&prop_rec, &env),
        GoTy::Named("Analytics_EventProp_R".into(), vec![]),
        "a PropValue-valued record must pick the PropValue-typed alias"
    );
}

// =========================================================================
// D2 — a PARAMETRIC record alias application propagates its type args:
// `Cfg Int` → `Cfg_R[int]` (NOT the erased bare `Cfg_R`, which would be the
// `Foo_R[any]`-panic class of building a generic type with no args).
// =========================================================================
#[test]
fn parametric_record_alias_propagates_type_args() {
    let mut env = TypeEnv::default();
    reg(
        &mut env,
        "Main",
        "Cfg",
        nominal("Main_Cfg_R", NominalKind::Record, 1, false),
    );
    let t = Ty::App(Name::new("Cfg"), vec![app0("Int")]);
    assert_eq!(
        sky_ty_to_go(&t, &env),
        GoTy::Named("Main_Cfg_R".into(), vec![GoTy::Bare(Prim::Int)]),
        "Cfg Int must instantiate Cfg_R[int], not erase to bare Cfg_R"
    );
}

// =========================================================================
// D1 — kernel-opaque handle types (`Route`/`Server`/`Cookie`, `Cmd`, `Sub`,
// `Decoder`, `Value`) resolve to `any`: their runtime value is a kernel handle,
// not the placeholder `int`/nominal the decl would suggest. Coercing to the
// placeholder panics — so the map must floor them to `any`.
// =========================================================================
#[test]
fn opaque_and_kernel_handles_erase_to_any() {
    let mut env = TypeEnv::default();
    reg(
        &mut env,
        "Sky.Http.Server",
        "Route",
        nominal("Server_Route", NominalKind::Iota, 0, true),
    );
    assert_eq!(sky_ty_to_go(&app0("Route"), &env), GoTy::Any, "opaque handle → any");
    assert_eq!(
        sky_ty_to_go(&Ty::App(Name::new("Cmd"), vec![app0("Msg")]), &env),
        GoTy::Any,
        "Cmd _ → any"
    );
    assert_eq!(
        sky_ty_to_go(&Ty::App(Name::new("Sub"), vec![app0("Msg")]), &env),
        GoTy::Any,
        "Sub _ → any"
    );
    assert_eq!(
        sky_ty_to_go(&Ty::App(Name::new("Decoder"), vec![app0("User")]), &env),
        GoTy::Any,
        "Decoder _ → any"
    );
}

// =========================================================================
// D2 — the well-known kernel wrappers map to their runtime generic heads.
// A wrong head here silently mistypes every effect/optional/list value.
// =========================================================================
#[test]
fn builtin_wrappers_map_to_runtime_heads() {
    let env = TypeEnv::default();
    assert_eq!(
        sky_ty_to_go(&Ty::App(Name::new("List"), vec![app0("Int")]), &env),
        GoTy::Slice(Box::new(GoTy::Bare(Prim::Int)))
    );
    assert_eq!(
        sky_ty_to_go(&Ty::App(Name::new("Maybe"), vec![app0("Int")]), &env),
        GoTy::Named("rt.SkyMaybe".into(), vec![GoTy::Bare(Prim::Int)])
    );
    assert_eq!(
        sky_ty_to_go(
            &Ty::App(Name::new("Result"), vec![app0("String"), app0("Int")]),
            &env
        ),
        GoTy::Named(
            "rt.SkyResult".into(),
            vec![GoTy::Bare(Prim::Str), GoTy::Bare(Prim::Int)]
        )
    );
    assert_eq!(
        sky_ty_to_go(
            &Ty::App(Name::new("Task"), vec![app0("String"), app0("Int")]),
            &env
        ),
        GoTy::Named(
            "rt.SkyTask".into(),
            vec![GoTy::Bare(Prim::Str), GoTy::Bare(Prim::Int)]
        )
    );
}
