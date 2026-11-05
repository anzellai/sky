//! Named Go slice/map/array surface types stay opaque interface slots, rather
//! than being mistaken for raw immutable Sky containers in a compiled model.
use base::Name;
use lower::goty::{sky_ty_to_go, TypeEnv};
use lower::ir::GoTy;
use ty::Ty;

#[test]
fn opaque_container_model_fields_lower_to_any() {
    let env = TypeEnv::default();
    for name in ["NamedList", "NamedMap", "NamedArray"] {
        let ty = Ty::App(
            Name::new(&format!("go@Example.Test.Reusedbuffers.{name}")),
            vec![],
        );
        assert_eq!(sky_ty_to_go(&ty, &env), GoTy::Any);
        let record = Ty::Record(vec![(Name::new("held"), ty)], None);
        let go = sky_ty_to_go(&record, &env);
        let GoTy::Struct(fields) = &go else {
            panic!("expected structural model record: {go:?}")
        };
        assert_eq!(fields.len(), 1);
        assert_eq!(
            fields[0].1,
            GoTy::Any,
            "opaque field must not become a named Go slice"
        );
    }
}
