//! Regression (design 1b, stages D1 and D2): `Dict.get` / `Dict.member` /
//! `Dict.insert` / `Dict.remove` over a typed Dict (`rt.SkyDict[V]`) lower to
//! typed twins, not to the erased kernels (which box the Dict into `any` and
//! need an `rt.AsMapT` narrowing of the result). An `Int` or `String` key takes
//! the `…IntT` / `…StrT` twin, which encodes the key without boxing it; any
//! other key stays erased for the probe (`…ProbeT`), which needs its runtime
//! kind to try the tagged key and then the bare `%v` key that an FFI or Std.Db
//! map holds. Doc 14 R5, lever §5.3.

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
fn a_typed_get_with_an_int_key_uses_the_int_twin() {
    let f = func_debug(SRC, "Main_look");
    assert!(f.contains("rt.Dict_getIntT"), "{f}");
    assert!(!f.contains("\"rt.Dict_get\""), "{f}");
    assert!(!f.contains("Coerce"), "no narrowing of the result: {f}");
}

#[test]
fn a_typed_member_with_a_string_key_uses_the_str_twin() {
    let f = func_debug(SRC, "Main_has");
    assert!(f.contains("rt.Dict_memberStrT"), "{f}");
    assert!(!f.contains("Coerce"), "{f}");
}

const UPDATES: &str = "module Main exposing (main)\n\n\
                       bump : Int -> Dict Int Int -> Dict Int Int\nbump k d =\n    Dict.insert k 1 d\n\n\
                       drop : String -> Dict String Int -> Dict String Int\ndrop k d =\n    Dict.remove k d\n\n\
                       keyed : Char -> Dict Char Int -> ( Dict Char Int, Maybe Int )\nkeyed c d =\n    ( Dict.insert c 2 d, Dict.get c d )\n\n\
                       main =\n    ( bump 1 Dict.empty, drop \"a\" Dict.empty, keyed 'a' Dict.empty )\n";

// D2: an update over a typed Dict returns the typed Dict directly: no boxing
// of the Dict and no `rt.AsMapT` narrowing of the result.
#[test]
fn a_typed_insert_and_remove_use_the_typed_twins() {
    let f = func_debug(UPDATES, "Main_bump");
    assert!(f.contains("rt.Dict_insertIntT"), "{f}");
    assert!(
        !f.contains("AsMapT") && !f.contains("\"rt.Dict_insert\""),
        "{f}"
    );
    let f = func_debug(UPDATES, "Main_drop");
    assert!(f.contains("rt.Dict_removeStrT"), "{f}");
    assert!(!f.contains("AsMapT"), "{f}");
}

// A key that is neither `Int` nor `String` keeps its runtime kind for the
// probe: the `…ProbeT` twins.
#[test]
fn a_char_key_uses_the_probe_twins() {
    let f = func_debug(UPDATES, "Main_keyed");
    assert!(
        f.contains("rt.Dict_insertProbeT") && f.contains("rt.Dict_getProbeT"),
        "{f}"
    );
    assert!(!f.contains("AsMapT"), "{f}");
}

#[test]
fn a_typed_size_and_is_empty_read_the_length() {
    let f = func_debug(SRC, "Main_count");
    assert!(
        f.contains("rt.Dict_sizeT") && f.contains("rt.Dict_isEmptyT"),
        "{f}"
    );
}
