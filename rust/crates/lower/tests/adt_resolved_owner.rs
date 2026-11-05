//! Legacy ADT aliases retain their resolved Sky parent, even when ambiguous
//! payload names force a user union off the sealed-interface representation.
use base::FileId;
use hir::SourceDb;
use lower::ir::GoItem;

#[test]
fn legacy_bags_emit_resolved_parent_and_scoped_wire_registration() {
    let mut db = SourceDb::new();
    db.add_module(
        "Left",
        syntax::parse(
            "module Left exposing (..)\ntype alias Token = { value : Int }\n",
            FileId(0),
        ),
    );
    db.add_module(
        "Right",
        syntax::parse(
            "module Right exposing (..)\ntype alias Token = { label : String }\n",
            FileId(1),
        ),
    );
    let mid = db.add_module(
        "Main",
        syntax::parse(
            r#"module Main exposing (main)
import Left
import Right

type User = Money String Int | A Left.Token | B Right.Token
type Exact = Decimal__Internal String

main = { price = Money "keep" 7, amount = Decimal__Internal "exact" }
"#,
            FileId(2),
        ),
    );
    let out = lower::lower_program(&db, mid);
    assert!(out.errors.is_empty(), "{:?}", out.errors);
    let raw = out
        .items
        .iter()
        .filter_map(|i| match i {
            GoItem::Raw(s) => Some(s.as_str()),
            _ => None,
        })
        .collect::<Vec<_>>()
        .join("\n");
    assert!(
        raw.contains("SkyOwner: \"sky:Main.User\""),
        "legacy bag constructor must carry the actual parent: {raw}"
    );
    assert!(
        raw.contains("rt.RegisterAdtOwner(\"main.Main_User\", \"Main_User\", \"sky:Main.User\")"),
        "{raw}"
    );
    assert!(
        raw.contains(
            "rt.RegisterAdtOwner(\"main.Main_Exact\", \"Main_Exact\", \"sky:Main.Exact\")"
        ),
        "{raw}"
    );
    assert!(
        !raw.contains("SkyOwner: \"builtin:Std.Money.Money\""),
        "a Money constructor spelling cannot choose the owner: {raw}"
    );
}
