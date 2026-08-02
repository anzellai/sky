//! Codegen snapshot depth for the Go IR printer (`codegen::render_expr` /
//! `render_ty` / `emit_program`). The emitter had ~1 unit test; a mis-rendered
//! construct that still `go build`s (a dropped record-update field, a wrong
//! coercion head, a `Foo_R` vs `Foo_R[T1]` mixup) could slip through the
//! oracle-golden corpus. These lock the EXACT Go text of the shapes most prone
//! to silent breakage.
//!
//! Each test cites the D-dimension it guards from
//! `docs/rust-rewrite/13-change-verification-and-edge-cases.md`.

use codegen::{emit_program, render_expr, render_ty};
use lower::ir::{
    CoerceReason, GoExpr, GoExprKind, GoItem, GoStmt, GoTy, GoTypeDef, Prim,
};

// ---- small IR constructors so each test reads as its Go shape -------------

fn ident(n: &str) -> GoExpr {
    GoExpr::new(GoExprKind::Ident(n.into()), GoTy::Any)
}
fn int_lit(n: i64) -> GoExpr {
    GoExpr::new(GoExprKind::IntLit(n), GoTy::Bare(Prim::Int))
}
fn str_lit(s: &str) -> GoExpr {
    GoExpr::new(GoExprKind::StrLit(s.into()), GoTy::Bare(Prim::Str))
}
fn named(n: &str) -> GoTy {
    GoTy::Named(n.into(), vec![])
}

// =========================================================================
// D2 — record shapes: a record LITERAL renders every field it is handed.
// =========================================================================
#[test]
fn renders_record_literal_all_fields() {
    // Model_R{Count: 0, Name: "Ada"} — the printer must not drop a field.
    let e = GoExpr::new(
        GoExprKind::StructLit(
            "Main_Model_R".into(),
            vec![
                ("Count".into(), int_lit(0)),
                ("Name".into(), str_lit("Ada")),
            ],
        ),
        named("Main_Model_R"),
    );
    assert_eq!(render_expr(&e), "Main_Model_R{Count: 0, Name: \"Ada\"}");
}

// =========================================================================
// D2/D3 — the #166 record-UPDATE shape. `{ base | f = v }` lowers to a typed
// IIFE that COPIES the whole base (`_u := base`) then overwrites one field, so
// every un-updated field survives. This is the exact fix for the
// record-update-field-drop regression: the printer must emit `_u := base` (a
// whole-record copy), NOT a narrow struct literal listing only `f`.
// =========================================================================
#[test]
fn renders_record_update_block_preserves_base() {
    // func() Main_Model_R { _u := m; _u.Count = 1; return _u }()
    let uref = GoExpr::new(GoExprKind::Ident("_u".into()), named("Main_Model_R"));
    let block = GoExpr::new(
        GoExprKind::Block(vec![
            GoStmt::Short("_u".into(), ident("m")),
            GoStmt::AssignField(uref.clone(), "Count".into(), int_lit(1)),
            GoStmt::Return(Some(uref)),
        ]),
        named("Main_Model_R"),
    );
    assert_eq!(
        render_expr(&block),
        "func() Main_Model_R { _u := m; _u.Count = 1; return _u }()"
    );
}

// =========================================================================
// D2 — a GENUINELY row-polymorphic update (`bump r = { r | age = … }`, base
// erased to `any`) routes through the reflective `rt.RecordUpdate` — NOT a
// struct field assignment (there is no static struct to assign into).
// =========================================================================
#[test]
fn renders_row_poly_record_update_reflective() {
    let map_lit = GoExpr::new(
        GoExprKind::StructLit(
            "map[string]any".into(),
            vec![("\"Age\"".into(), GoExpr::new(GoExprKind::Widen(Box::new(int_lit(30))), GoTy::Any))],
        ),
        GoTy::Any,
    );
    let e = GoExpr::new(
        GoExprKind::Call(
            Box::new(GoExpr::new(GoExprKind::Ident("rt.RecordUpdate".into()), GoTy::Any)),
            vec![GoExpr::new(GoExprKind::Widen(Box::new(ident("r"))), GoTy::Any), map_lit],
        ),
        GoTy::Any,
    );
    assert_eq!(
        render_expr(&e),
        "rt.RecordUpdate(any(r), map[string]any{\"Age\": any(30)})"
    );
}

// =========================================================================
// D2/D5 — a TUPLE literal is `rt.TN[…]{V0: …, V1: …}`; its type is `rt.TN[…]`.
// =========================================================================
#[test]
fn renders_tuple_literal_and_type() {
    // ("x", 1) : (String, Int)
    let tup = GoExpr::new(
        GoExprKind::StructLit(
            "rt.T2[string, int]".into(),
            vec![("V0".into(), str_lit("x")), ("V1".into(), int_lit(1))],
        ),
        GoTy::Tuple(vec![GoTy::Bare(Prim::Str), GoTy::Bare(Prim::Int)]),
    );
    assert_eq!(render_expr(&tup), "rt.T2[string, int]{V0: \"x\", V1: 1}");
    // the type side
    assert_eq!(
        render_ty(&GoTy::Tuple(vec![GoTy::Bare(Prim::Str), GoTy::Bare(Prim::Int)])),
        "rt.T2[string, int]"
    );
    // arity ≥ 10 → the slice-backed SkyTupleN (must match lower_tuple's split)
    let big: Vec<GoTy> = std::iter::repeat(GoTy::Bare(Prim::Int)).take(10).collect();
    assert_eq!(render_ty(&GoTy::Tuple(big)), "rt.SkyTupleN");
}

// =========================================================================
// D2 — coercion heads. The `to` type selects the runtime helper; a wrong head
// still `go build`s but panics or mis-narrows at runtime, so lock each arm.
// =========================================================================
#[test]
fn renders_coerce_generic_named() {
    let e = GoExpr::new(
        GoExprKind::Coerce {
            inner: Box::new(ident("x")),
            from: GoTy::Any,
            to: named("Main_Model_R"),
            reason: CoerceReason::GenericErase,
        },
        named("Main_Model_R"),
    );
    assert_eq!(render_expr(&e), "/* generic erase */ rt.Coerce[Main_Model_R](x)");
}

#[test]
fn renders_coerce_slice_via_aslistt() {
    // A `List a` narrowing must go through rt.AsListT[T], never rt.Coerce[[]T].
    let e = GoExpr::new(
        GoExprKind::Coerce {
            inner: Box::new(ident("xs")),
            from: GoTy::Any,
            to: GoTy::Slice(Box::new(GoTy::Bare(Prim::Int))),
            reason: CoerceReason::FfiReturn,
        },
        GoTy::Slice(Box::new(GoTy::Bare(Prim::Int))),
    );
    assert_eq!(render_expr(&e), "/* FFI return */ rt.AsListT[int](xs)");
}

#[test]
fn renders_coerce_dict_via_asmapt() {
    // The #166 Std.Db shape: a Dict-valued field narrows via rt.AsMapT[V]
    // (rebuilds map[string]V) — rt.Coerce[map[…]…] would panic (Go maps are
    // invariant). The key is pinned to `string` at the goty layer.
    let e = GoExpr::new(
        GoExprKind::Coerce {
            inner: Box::new(ident("d")),
            from: GoTy::Any,
            to: GoTy::Map(Box::new(GoTy::Bare(Prim::Str)), Box::new(GoTy::Bare(Prim::Str))),
            reason: CoerceReason::WireDecode,
        },
        GoTy::Any,
    );
    assert_eq!(render_expr(&e), "/* wire decode */ rt.AsMapT[string](d)");
}

#[test]
fn renders_coerce_primitives() {
    let mk = |to: GoTy| {
        render_expr(&GoExpr::new(
            GoExprKind::Coerce {
                inner: Box::new(ident("x")),
                from: GoTy::Any,
                to: to.clone(),
                reason: CoerceReason::PrimitiveJoin,
            },
            to,
        ))
    };
    assert_eq!(mk(GoTy::Bare(Prim::Int)), "/* primitive join */ rt.AsInt(x)");
    assert_eq!(mk(GoTy::Bare(Prim::Str)), "/* primitive join */ rt.AsString(x)");
    assert_eq!(mk(GoTy::Bare(Prim::Bool)), "/* primitive join */ rt.AsBool(x)");
    assert_eq!(mk(GoTy::Bare(Prim::Float)), "/* primitive join */ rt.AsFloat(x)");
}

#[test]
fn renders_coerce_task_result_maybe_wrappers() {
    let task = GoExpr::new(
        GoExprKind::Coerce {
            inner: Box::new(ident("t")),
            from: GoTy::Any,
            to: GoTy::Named("rt.SkyTask".into(), vec![named("E"), GoTy::Bare(Prim::Int)]),
            reason: CoerceReason::GenericErase,
        },
        GoTy::Any,
    );
    assert_eq!(render_expr(&task), "/* generic erase */ rt.TaskCoerceT[E, int](t)");
    let res = GoExpr::new(
        GoExprKind::Coerce {
            inner: Box::new(ident("r")),
            from: GoTy::Any,
            to: GoTy::Named("rt.SkyResult".into(), vec![named("E"), GoTy::Bare(Prim::Str)]),
            reason: CoerceReason::GenericErase,
        },
        GoTy::Any,
    );
    assert_eq!(render_expr(&res), "/* generic erase */ rt.ResultCoerce[E, string](r)");
    let may = GoExpr::new(
        GoExprKind::Coerce {
            inner: Box::new(ident("m")),
            from: GoTy::Any,
            to: GoTy::Named("rt.SkyMaybe".into(), vec![GoTy::Bare(Prim::Int)]),
            reason: CoerceReason::GenericErase,
        },
        GoTy::Any,
    );
    assert_eq!(render_expr(&may), "/* generic erase */ rt.MaybeCoerce[int](m)");
}

#[test]
fn renders_coerce_identity_elided() {
    // from == to: no runtime op, no comment — just the inner expression.
    let e = GoExpr::new(
        GoExprKind::Coerce {
            inner: Box::new(ident("x")),
            from: GoTy::Bare(Prim::Int),
            to: GoTy::Bare(Prim::Int),
            reason: CoerceReason::GenericErase,
        },
        GoTy::Bare(Prim::Int),
    );
    assert_eq!(render_expr(&e), "x");
}

#[test]
fn renders_widen_to_any() {
    let e = GoExpr::new(GoExprKind::Widen(Box::new(ident("x"))), GoTy::Any);
    assert_eq!(render_expr(&e), "any(x)");
}

// =========================================================================
// D5 — an ADT variant is a `Name_Ctor_V{V0: …}` struct literal.
// =========================================================================
#[test]
fn renders_adt_variant_construction() {
    let e = GoExpr::new(
        GoExprKind::StructLit("Main_Msg_SetName_V".into(), vec![("V0".into(), str_lit("Ada"))]),
        named("Main_Msg"),
    );
    assert_eq!(render_expr(&e), "Main_Msg_SetName_V{V0: \"Ada\"}");
}

// =========================================================================
// D2 — the NULLARY record struct decl (`Foo_R`) carries a `sky:"<field>,<ty>"`
// tag per field (Codec.auto metadata). Locks the tag shape + field emission.
// =========================================================================
#[test]
fn emits_nullary_record_struct_with_sky_tags() {
    let items = vec![GoItem::Type(
        "Main_Model_R".into(),
        GoTypeDef::Struct(vec![
            ("Count".into(), GoTy::Bare(Prim::Int)),
            ("Name".into(), GoTy::Bare(Prim::Str)),
        ]),
    )];
    let out = emit_program(&items, false);
    assert!(
        out.contains(
            "type Main_Model_R struct { Count int `sky:\"count,int\"`; Name string `sky:\"name,string\"` }"
        ),
        "nullary record struct decl / sky tags drifted; got:\n{out}"
    );
}

// =========================================================================
// D2 — the GENERIC record decl (`Foo_R[T1]`) is emitted by the lowerer as a
// `GoItem::Raw` (its generic clause is not modelled structurally). Codegen must
// (a) print it verbatim and (b) EXCLUDE it from the boot gob-registration list
// (a generic type cannot be zero-valued). This is the `Foo_R` vs `Foo_R[T1]`
// split the task calls out.
// =========================================================================
#[test]
fn emits_generic_record_struct_raw_and_skips_gob() {
    let generic_decl = "type Main_Cfg_R[T1 any] struct { OnSubmit T1; Label string }";
    let items = vec![
        GoItem::Raw(generic_decl.into()),
        // a sibling nullary struct so a gob-init line IS emitted (and we can
        // prove ONLY the nullary one lands in it).
        GoItem::Type(
            "Main_Model_R".into(),
            GoTypeDef::Struct(vec![("Count".into(), GoTy::Bare(Prim::Int))]),
        ),
    ];
    let out = emit_program(&items, false);
    // (a) verbatim
    assert!(out.contains(generic_decl), "generic struct not emitted verbatim; got:\n{out}");
    // (b) not in the gob list; the nullary one IS.
    assert!(
        out.contains("rt.RegisterSkyGobTypes([]any{Main_Model_R{}})"),
        "nullary struct must be the only gob type; got:\n{out}"
    );
    assert!(
        !out.contains("Main_Cfg_R[T1 any]{}") && !out.contains("Main_Cfg_R{}"),
        "generic struct must NOT be zero-valued in the gob list; got:\n{out}"
    );
}

// =========================================================================
// D5 — a sealed-interface ADT emits the interface + one concrete `_V` struct
// per variant with the two marker methods; a payload variant gets typed `Vi`
// fields, a nullary variant an empty struct.
// =========================================================================
#[test]
fn emits_sealed_iface_with_variant_structs() {
    let items = vec![GoItem::Type(
        "Main_Msg".into(),
        GoTypeDef::SealedIface(vec![
            ("Increment".into(), 0, vec![]),
            ("SetName".into(), 1, vec![GoTy::Bare(Prim::Str)]),
        ]),
    )];
    let out = emit_program(&items, false);
    assert!(out.contains("type Main_Msg interface {"), "iface decl missing:\n{out}");
    assert!(out.contains("type Main_Msg_Increment_V struct {}"), "nullary variant struct:\n{out}");
    assert!(
        out.contains("type Main_Msg_SetName_V struct { V0 string }"),
        "payload variant struct with typed V0:\n{out}"
    );
    assert!(
        out.contains("func (Main_Msg_Increment_V) SkyVariantTag() int { return 0 }"),
        "variant tag marker:\n{out}"
    );
    assert!(
        out.contains("func (Main_Msg_SetName_V) SkyVariantName() string { return \"SetName\" }"),
        "variant name marker:\n{out}"
    );
}
