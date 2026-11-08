//! Drive real lowering and codegen, rather than hand-authoring ideal sky tags.
//! Nominal alias names survive Go alias erasure in non-generic record metadata.
use base::FileId;
use codegen::emit_program;
use hir::SourceDb;

#[test]
fn builtin_declared_types_survive_all_record_container_slots() {
    let mut db = SourceDb::new();
    // Minimal API declarations with the actual resolved stdlib parent keys.
    // The conformance fixture separately compiles against the real stdlib.
    db.add_module(
        "Std.Decimal",
        syntax::parse(
            "module Std.Decimal exposing (..)\ntype Decimal = Decimal__Internal Float\n",
            FileId(0),
        ),
    );
    db.add_module("Std.Money", syntax::parse(
        "module Std.Money exposing (..)\nimport Std.Decimal exposing (Decimal)\ntype Currency = GBP\ntype Money = Money Decimal Currency\n", FileId(1)));
    let mid = db.add_module(
        "Main",
        syntax::parse(
            r#"module Main exposing (main)
import Std.Decimal as Decimal
import Std.Money as Money

type alias Amount = Money.Money
type alias NestedAmount = Amount
type alias Exact = Decimal.Decimal
type alias Coin = Money.Currency
type alias Amounts = List NestedAmount

type alias Model =
    { money : Money.Money
    , decimal : Decimal.Decimal
    , prices : List Money.Money
    , maybeAmount : Maybe Decimal.Decimal
    , amounts : Dict String Decimal.Decimal
    , pair : ( Money.Money, Decimal.Decimal )
    , aliasedMoney : NestedAmount
    , aliasedDecimal : Exact
    , aliasedCurrency : Coin
    , aliasList : Amounts
    , aliasMaybe : Maybe Coin
    , aliasDict : Dict String Exact
    , aliasPair : ( NestedAmount, Coin )
    }

main : Model -> Model
main model = model
"#,
            FileId(2),
        ),
    );
    let out = lower::lower_program(&db, mid);
    assert!(out.errors.is_empty(), "{:?}", out.errors);
    let go = emit_program(&out.items, false);
    for tag in [
        "sky:\"money,Std_Money_Money\"",
        "sky:\"decimal,Std_Decimal_Decimal\"",
        "sky:\"prices,[]Std_Money_Money\"",
        "sky:\"maybeAmount,rt.SkyMaybe[Std_Decimal_Decimal]\"",
        "sky:\"amounts,rt.SkyDict[Std_Decimal_Decimal]\"",
        "sky:\"pair,rt.T2[Std_Money_Money, Std_Decimal_Decimal]\"",
        "sky:\"aliasedMoney,Std_Money_Money\"",
        "sky:\"aliasedDecimal,Std_Decimal_Decimal\"",
        "sky:\"aliasedCurrency,Std_Money_Currency\"",
        "sky:\"aliasList,[]Std_Money_Money\"",
        "sky:\"aliasMaybe,rt.SkyMaybe[Std_Money_Currency]\"",
        "sky:\"aliasDict,rt.SkyDict[Std_Decimal_Decimal]\"",
        "sky:\"aliasPair,rt.T2[Std_Money_Money, Std_Money_Currency]\"",
    ] {
        assert!(
            go.contains(tag),
            "actual emitted metadata lost {tag}:\n{go}"
        );
    }
    assert!(
        go.contains("type Std_Money_Money = rt.SkyADT"),
        "fixture must exercise actual erased alias representation: {go}"
    );
}

#[test]
fn lower_default_cannot_grant_builtin_identity_to_shadow_modules() {
    for (module, typename, ctor, payload) in [
        ("Std.Money", "Money", "Money", "String Int"),
        ("Std.Decimal", "Decimal", "Decimal__Internal", "String"),
    ] {
        let mut db = SourceDb::new();
        let source = format!("module {module} exposing (..)\ntype {typename} = {ctor} {payload}\nmain : {typename} -> {typename}\nmain value = value\n");
        let mid = db.add_module(module, syntax::parse(&source, FileId(0)));
        let untrusted = lower::lower_program(&db, mid);
        assert!(untrusted.errors.is_empty(), "{:?}", untrusted.errors);
        let go = emit_program(&untrusted.items, false);
        assert!(
            go.contains(&format!("SkyOwner: \"sky:{module}.{typename}\"")),
            "default API granted builtin identity: {go}"
        );
        assert!(
            !go.contains(&format!("SkyOwner: \"builtin:{module}.{typename}\"")),
            "{go}"
        );
        // Only the driver can supply this capability after comparing the live
        // module SourceFile to the originally loaded canonical source input.
        let mut cfg = lower::LowerConfig::default();
        cfg.trusted_builtin_adt_modules.insert(mid);
        let trusted = lower::lower_program_cfg(&db, mid, &cfg);
        assert!(trusted.errors.is_empty(), "{:?}", trusted.errors);
        let go = emit_program(&trusted.items, false);
        assert!(
            go.contains(&format!("SkyOwner: \"builtin:{module}.{typename}\"")),
            "explicit source grant missing: {go}"
        );
    }
}
