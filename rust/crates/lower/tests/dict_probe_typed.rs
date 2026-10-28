//! Regression (design 1b, stage D1): `Dict.get` / `Dict.member` over a typed
//! `map[string]V` lower to the probe-key twins `rt.Dict_getProbeT[V]` /
//! `rt.Dict_memberProbeT[V]`, not to the erased kernels, which reflect-copy
//! the whole map on every lookup. The key stays erased: the probe needs its
//! runtime kind to try the tagged key and then the bare `%v` key that an FFI
//! or Std.Db map holds. Doc 14 R5, lever §5.3.

use base::FileId;
use hir::SourceDb;
use lower::ir::GoItem;

fn func_debug(src: &str, name: &str) -> String {
    let mut db = SourceDb::new();
    let mid = db.add_module("Main", syntax::parse(src, FileId(0)));
    let out = lower::lower_program(&db, mid);
    assert!(out.errors.is_empty(), "lowering errors: {:?}", out.errors);
    out.items
        .iter()
        .find_map(|it| match it {
            GoItem::Func(f) if f.name == name => Some(format!("{f:?}")),
            _ => None,
        })
        .unwrap_or_else(|| panic!("{name} is emitted"))
}

const SRC: &str = "module Main exposing (main)\n\n\
                   look : Int -> Dict Int String -> Maybe String\nlook k d =\n    Dict.get k d\n\n\
                   has : String -> Dict String Int -> Bool\nhas k d =\n    Dict.member k d\n\n\
                   count : Dict String Int -> ( Int, Bool )\ncount d =\n    ( Dict.size d, Dict.isEmpty d )\n\n\
                   main =\n    ( look 1 Dict.empty, has \"a\" Dict.empty, count Dict.empty )\n";

#[test]
fn a_typed_get_uses_the_probe_twin() {
    let f = func_debug(SRC, "Main_look");
    assert!(f.contains("rt.Dict_getProbeT"), "{f}");
    assert!(!f.contains("\"rt.Dict_get\""), "{f}");
    assert!(!f.contains("Coerce"), "no narrowing of the result: {f}");
}

#[test]
fn a_typed_member_uses_the_probe_twin() {
    let f = func_debug(SRC, "Main_has");
    assert!(f.contains("rt.Dict_memberProbeT"), "{f}");
    assert!(!f.contains("Coerce"), "{f}");
}

#[test]
fn a_typed_size_and_is_empty_read_the_length() {
    let f = func_debug(SRC, "Main_count");
    assert!(
        f.contains("rt.Dict_sizeT") && f.contains("rt.Dict_isEmptyT"),
        "{f}"
    );
}
