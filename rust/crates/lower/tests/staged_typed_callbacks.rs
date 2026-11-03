//! Staging must preserve concrete callback information at erased boundaries.
use base::FileId;
use hir::SourceDb;
use lower::ir::GoItem;

fn emitted(db: &SourceDb, module: base::ModuleId, name: &str) -> String {
    let out = lower::lower_program(db, module);
    assert!(out.errors.is_empty(), "{:?}", out.errors);
    out.items
        .iter()
        .find_map(|item| match item {
            GoItem::Func(f) if f.name == name => Some(format!("{f:?}")),
            _ => None,
        })
        .expect("function is emitted")
}

#[test]
fn erased_callback_slot_keeps_concrete_record_reads() {
    let mut db = SourceDb::new();
    let module = db.add_module(
        "Main",
        syntax::parse(
            r#"module Main exposing (main)

type alias User = { name : String, age : Int }

apply : (a -> b) -> a -> b
apply f x = f x

read : User -> String
read user = apply (\u -> u.name ++ String.fromInt u.age) user

main = read { name = "Ada", age = 40 }
"#,
            FileId(0),
        ),
    );
    let code = emitted(&db, module, "Main_read");
    assert!(
        !code.contains("rt.Field"),
        "typed callback fields must stay direct: {code}"
    );
    assert!(code.contains("Selector"), "{code}");
}

#[test]
fn staged_fold_callback_reaches_the_fully_applying_typed_twin() {
    let mut db = SourceDb::new();
    db.add_module(
        "Sky.Core.List",
        syntax::parse(
            r#"module Sky.Core.List exposing (foldl)

foldl : (a -> b -> b) -> b -> List a -> b
foldl f acc xs =
    case xs of
        [] -> acc
        x :: rest -> foldl f (f x acc) rest
"#,
            FileId(0),
        ),
    );
    let module = db.add_module(
        "Main",
        syntax::parse(
            r#"module Main exposing (main)

import Sky.Core.List as List

sum : List Int -> Int
sum xs = List.foldl (\x acc -> x + acc) 0 xs

main = sum [1, 2, 3]
"#,
            FileId(1),
        ),
    );
    let code = emitted(&db, module, "Main_sum");
    assert!(code.contains("rt.List_foldlElemFirstT"), "{code}");
    assert!(
        !code.contains("Coerce"),
        "typed fold must not rebuild or narrow: {code}"
    );
}
