//! L10a codegen coverage: `emit_program` must emit the whole-binary
//! `func init(){ rt.RegisterSkyGobTypes([]any{…}) }` boot registration so that
//! EVERY process (encoder and, after a restart, a fresh decoder) registers the
//! concrete Sky record/ADT types with gob. Without this a type that only ever
//! lives in an `any`-typed Model field decodes-fails after a restart (see
//! runtime-go/rt/live_store_restart_test.go for the cross-process runtime
//! proof). This test locks the emission side so a codegen regression can't
//! silently drop it.

use codegen::emit_program;
use lower::ir::{GoItem, GoTy, GoTypeDef, Prim};

#[test]
fn emits_register_sky_gob_types_for_record_struct() {
    let items = vec![GoItem::Type(
        "Model_R".into(),
        GoTypeDef::Struct(vec![("Count".into(), GoTy::Bare(Prim::Int))]),
    )];
    let out = emit_program(&items, false);
    assert!(
        out.contains("rt.RegisterSkyGobTypes("),
        "codegen must emit the boot gob registration; got:\n{out}"
    );
    assert!(
        out.contains("func init() { rt.RegisterSkyGobTypes([]any{Model_R{}}) }"),
        "record struct must be in the whole-binary gob list; got:\n{out}"
    );
}

#[test]
fn emits_register_for_adt_variant_structs() {
    // An ADT lowers to a sealed interface + one `Name_<Ctor>_V` struct per
    // variant; each variant struct must be gob-registered at boot.
    let items = vec![GoItem::Type(
        "Msg".into(),
        GoTypeDef::SealedIface(vec![
            ("Increment".into(), 0, vec![]),
            ("SetName".into(), 1, vec![GoTy::Bare(Prim::Str)]),
        ]),
    )];
    let out = emit_program(&items, false);
    assert!(out.contains("rt.RegisterSkyGobTypes("), "missing boot registration:\n{out}");
    assert!(out.contains("Msg_Increment_V{}"), "missing nullary variant:\n{out}");
    assert!(out.contains("Msg_SetName_V{}"), "missing payload variant:\n{out}");
}

#[test]
fn skips_generic_record_structs_in_gob_list() {
    // A generic record (`Foo_R[T1]`) can't be zero-valued without type args, so
    // it must NOT appear in the list. With ONLY a generic struct present there
    // are no registrable types → no init line is emitted at all.
    let items = vec![GoItem::Type(
        "Cfg_R[T1]".into(),
        GoTypeDef::Struct(vec![("OnSubmit".into(), GoTy::Any)]),
    )];
    let out = emit_program(&items, false);
    assert!(
        !out.contains("Cfg_R[T1]{}"),
        "generic struct must be skipped (can't zero-value); got:\n{out}"
    );
    assert!(
        !out.contains("rt.RegisterSkyGobTypes("),
        "with only a generic struct, no gob init line should be emitted; got:\n{out}"
    );
}
