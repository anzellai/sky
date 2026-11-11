//! The `sky:` tag of a record field and of a custom type's payload field
//! names the key kind of every `Dict` it holds (`rt.SkyDict[@i:V]`). The Go
//! type `rt.SkyDict[V]` erases the key, and `Codec.auto` writes a non-String
//! key as its plain text (`"10"`), so the reflective walker reads the kind
//! from here to decode the key back to the stored Int key. Without it a
//! `Dict Int` in a record that keeps the reflective codec came back with
//! String keys, and a key-generic helper handed a String to code typed Int.
use base::FileId;
use codegen::emit_program;
use hir::SourceDb;

#[test]
fn dict_key_kinds_are_in_the_declared_tags() {
    let mut db = SourceDb::new();
    let mid = db.add_module(
        "Main",
        syntax::parse(
            r#"module Main exposing (main)

type Shape
    = Circle (Dict Int String)
    | Lines (List (Maybe (Dict Float Int)))

type alias Model =
    { byNum : Dict Int String
    , byName : Dict String Int
    , lists : List (Dict Int Int)
    , maybeNum : Maybe (Dict Bool String)
    , nested : Dict Int (Dict Char String)
    , pair : ( Int, Dict Int String )
    , shape : Shape
    }

main : Model -> Model
main model = model
"#,
            FileId(0),
        ),
    );
    let out = lower::lower_program(&db, mid);
    assert!(out.errors.is_empty(), "{:?}", out.errors);
    let go = emit_program(&out.items, false);
    for tag in [
        "sky:\"byNum,rt.SkyDict[@i:string]\"",
        "sky:\"byName,rt.SkyDict[int]\"",
        "sky:\"lists,[]rt.SkyDict[@i:int]\"",
        "sky:\"maybeNum,rt.SkyMaybe[rt.SkyDict[@b:string]]\"",
        "sky:\"nested,rt.SkyDict[@i:rt.SkyDict[@c:string]]\"",
        "sky:\"pair,rt.T2[int, rt.SkyDict[@i:string]]\"",
        "sky:\"v0,rt.SkyDict[@i:string]\"",
        "sky:\"v0,[]rt.SkyMaybe[rt.SkyDict[@f:int]]\"",
    ] {
        assert!(go.contains(tag), "missing {tag} in:\n{go}");
    }
    // The Go field types are unchanged: the key stays erased there.
    assert!(go.contains("ByNum rt.SkyDict[string]"), "{go}");
}
