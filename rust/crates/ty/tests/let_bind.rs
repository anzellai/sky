//! Bind lines in `let` (`p <- e`): the type checker chooses each line's
//! `andThen` from the type of `e` (docs/design/let-bind-lines.md §4).
//!
//! The choice is the head type constructor's home module's EXPOSED
//! `andThen` (the kernel three by a fixed table, the JSON `Decoder` by its
//! kernel-implicit home), never a name lookup. Every refusal has its code:
//! `[E2015]` (the head is still unknown), `[E2016]` (no fitting `andThen`),
//! `[E3001]` (a refutable pattern), `[E2001]` with a note (mixed types).

use base::{DefId, FileId, ModuleId};
use hir::{Res, SkyDb, SourceDb};
use std::path::PathBuf;
use std::sync::OnceLock;

fn stdlib() -> &'static Vec<(String, syntax::Parse)> {
    static S: OnceLock<Vec<(String, syntax::Parse)>> = OnceLock::new();
    S.get_or_init(|| {
        let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        while !dir.join("sky-stdlib").is_dir() {
            assert!(dir.pop(), "no sky-stdlib ancestor");
        }
        ty::reject_corpus::load_stdlib(&dir)
    })
}

const HEADER: &str = "import Sky.Core.Prelude exposing (..)\n\
                      import Sky.Core.Task as Task\n\
                      import Sky.Core.Maybe as Maybe\n\
                      import Sky.Core.Result as Result\n\
                      import Sky.Core.Error as Error exposing (Error)\n\
                      import Std.Log exposing (println)\n";

/// `Main` with `body`, plus the extra project modules `extra`.
fn db_of(body: &str, extra: &[(&str, &str)]) -> (SourceDb, Vec<ModuleId>) {
    let mut db = SourceDb::new();
    for (n, p) in stdlib() {
        db.add_module(n, p.clone());
    }
    let mut ids = Vec::new();
    for (i, (name, src)) in extra.iter().enumerate() {
        ids.push(db.add_module(name, syntax::parse(src, FileId(100 + i as u32))));
    }
    let src = format!(
        "module Main exposing (main)\n\n{HEADER}{}\n\n{body}\n\n\nmain =\n    println \"x\"\n",
        extra
            .iter()
            .map(|(n, _)| format!("import {n}\n"))
            .collect::<String>()
            .replace("import Lib.Parsing\n", "import Lib.Parsing as P\n")
    );
    ids.push(db.add_module("Main", syntax::parse(&src, FileId(0))));
    (db, ids)
}

fn errors_in(body: &str, extra: &[(&str, &str)]) -> Vec<(String, String)> {
    let (db, ids) = db_of(body, extra);
    ty::check_modules(&db, &ids)
        .diagnostics
        .into_iter()
        .filter(|d| d.severity != diagnostics::Severity::Info)
        .map(|d| (d.code.0, d.message))
        .collect()
}

fn errors(body: &str) -> Vec<(String, String)> {
    errors_in(body, &[])
}

fn clean(body: &str) {
    let got = errors(body);
    assert!(got.is_empty(), "{got:#?}");
}

fn codes(got: &[(String, String)]) -> Vec<&str> {
    got.iter().map(|(c, _)| c.as_str()).collect()
}

/// The `andThen` each bind line of `def` chose, as `Module.name`, in no
/// particular order.
fn chosen(body: &str, extra: &[(&str, &str)], def: &str) -> Vec<String> {
    let (db, _) = db_of(body, extra);
    let main = db.module_by_name("Main").expect("Main");
    let d: DefId = db
        .resolve(main)
        .top_defs
        .iter()
        .find(|t| t.name.as_str() == def)
        .expect("def")
        .def;
    let world = ty::World::build(&db);
    let mut out: Vec<String> = ty::compute_bind_elaboration(&world, &db, main, d)
        .choices
        .values()
        .map(|r| match r {
            Res::Def(c) => {
                let loc = db.def_loc(*c).expect("loc");
                format!("{}.{}", db.module_name(loc.module), loc.name.as_str())
            }
            other => format!("{other:?}"),
        })
        .collect();
    out.sort();
    out
}

// ---- accepted heads ------------------------------------------------------

#[test]
fn a_task_chain_checks_and_chooses_task_and_then() {
    let body = "step : Task Error Int -> Task Error Int\nstep t =\n    let\n        n <- t\n        m = n + 1\n        k <- Task.succeed (m * 2)\n    in\n    Task.succeed (k + n)";
    clean(body);
    assert_eq!(
        chosen(body, &[], "step"),
        vec!["Sky.Core.Task.andThen", "Sky.Core.Task.andThen"]
    );
}

#[test]
fn a_result_chain_checks() {
    let body = "half : Int -> Result Error Int\nhalf n =\n    if n > 0 then Ok (n // 2) else Err (Error.invalidInput \"neg\")\n\n\nquarter : Int -> Result Error Int\nquarter n =\n    let\n        h <- half n\n        q <- half h\n    in\n    Ok q";
    clean(body);
    assert_eq!(
        chosen(body, &[], "quarter"),
        vec!["Sky.Core.Result.andThen", "Sky.Core.Result.andThen"]
    );
}

#[test]
fn a_maybe_chain_checks() {
    let body = "both : Maybe Int -> Maybe Int -> Maybe Int\nboth a b =\n    let\n        x <- a\n        y <- b\n    in\n    Just (x + y)";
    clean(body);
    assert_eq!(
        chosen(body, &[], "both"),
        vec!["Sky.Core.Maybe.andThen", "Sky.Core.Maybe.andThen"]
    );
}

/// `body` whose first lines are extra `import`s (they join the header).
fn split_imports(body: &str) -> (String, String) {
    let mut imports = String::new();
    let mut rest = Vec::new();
    for line in body.lines() {
        if line.starts_with("import ") {
            imports.push_str(line);
            imports.push('\n');
        } else {
            rest.push(line);
        }
    }
    (imports, rest.join("\n"))
}

fn db_with_imports(body: &str, extra: &[(&str, &str)]) -> (SourceDb, Vec<ModuleId>) {
    let (imports, rest) = split_imports(body);
    let mut db = SourceDb::new();
    for (n, p) in stdlib() {
        db.add_module(n, p.clone());
    }
    let mut ids = Vec::new();
    for (i, (name, src)) in extra.iter().enumerate() {
        ids.push(db.add_module(name, syntax::parse(src, FileId(100 + i as u32))));
    }
    let src = format!(
        "module Main exposing (main)\n\n{HEADER}{imports}\n\n{rest}\n\n\nmain =\n    println \"x\"\n"
    );
    ids.push(db.add_module("Main", syntax::parse(&src, FileId(0))));
    (db, ids)
}

fn errors_with_import(body: &str) -> Vec<(String, String)> {
    errors_with_import_in(body, &[])
}

fn errors_with_import_in(body: &str, extra: &[(&str, &str)]) -> Vec<(String, String)> {
    let (db, ids) = db_with_imports(body, extra);
    ty::check_modules(&db, &ids)
        .diagnostics
        .into_iter()
        .filter(|d| d.severity != diagnostics::Severity::Info)
        .map(|d| (d.code.0, d.message))
        .collect()
}

fn chosen_with_import(body: &str, extra: &[(&str, &str)], def: &str) -> Vec<String> {
    let (db, _) = db_with_imports(body, extra);
    let main = db.module_by_name("Main").expect("Main");
    let d = db
        .resolve(main)
        .top_defs
        .iter()
        .find(|t| t.name.as_str() == def)
        .expect("def")
        .def;
    let world = ty::World::build(&db);
    let mut out: Vec<String> = ty::compute_bind_elaboration(&world, &db, main, d)
        .choices
        .values()
        .map(|r| match r {
            Res::Def(c) => {
                let loc = db.def_loc(*c).expect("loc");
                format!("{}.{}", db.module_name(loc.module), loc.name.as_str())
            }
            other => format!("{other:?}"),
        })
        .collect();
    out.sort();
    out
}

#[test]
fn json_xml_and_config_decoders_each_use_their_own_and_then() {
    let body = "import Sky.Core.Json.Decode as Decode\nimport Std.Xml.Decode as XmlDecode\nimport Std.Config as Config\npairJ : Decode.Decoder ( Int, String )\npairJ =\n    let\n        n <- Decode.field \"n\" Decode.int\n        s <- Decode.field \"s\" Decode.string\n    in\n    Decode.succeed ( n, s )\n\n\npairX : XmlDecode.Decoder Int\npairX =\n    let\n        n <- XmlDecode.int\n    in\n    XmlDecode.succeed (n + 1)\n\n\npairC : Config.Decoder ( Int, String )\npairC =\n    let\n        n <- Config.field \"n\" Config.int\n        s <- Config.field \"s\" Config.string\n    in\n    Config.succeed ( n, s )";
    let got = errors_with_import(body);
    assert!(got.is_empty(), "{got:#?}");
    assert_eq!(
        chosen_with_import(body, &[], "pairJ"),
        vec![
            "Sky.Core.Json.Decode.andThen",
            "Sky.Core.Json.Decode.andThen"
        ]
    );
    assert_eq!(
        chosen_with_import(body, &[], "pairX"),
        vec!["Std.Xml.Decode.andThen"]
    );
    assert_eq!(
        chosen_with_import(body, &[], "pairC"),
        vec!["Std.Config.andThen", "Std.Config.andThen"]
    );
}

#[test]
fn a_mixed_json_and_xml_block_is_refused() {
    let body = "import Sky.Core.Json.Decode as Decode\nimport Std.Xml.Decode as XmlDecode\nbad : Decode.Decoder Int\nbad =\n    let\n        n <- XmlDecode.int\n    in\n    Decode.succeed n";
    let got = errors_with_import(body);
    assert!(codes(&got).contains(&"E2001"), "{got:#?}");
}

const PARSING: &str = "module Lib.Parsing exposing (Parser, succeed, andThen, run, digit)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.String as String\n\n\n\
type Parser a\n    = Parser (String -> Maybe ( a, String ))\n\n\n\
succeed : a -> Parser a\nsucceed v =\n    Parser (\\s -> Just ( v, s ))\n\n\n\
andThen : (a -> Parser b) -> Parser a -> Parser b\nandThen f (Parser p) =\n    Parser\n        (\\s ->\n            case p s of\n                Just ( v, rest ) ->\n                    let\n                        (Parser q) =\n                            f v\n                    in\n                    q rest\n\n                Nothing ->\n                    Nothing\n        )\n\n\n\
digit : Parser Int\ndigit =\n    Parser\n        (\\s ->\n            case String.uncons s of\n                Just ( c, rest ) ->\n                    Maybe.map (\\n -> ( n, rest )) (String.toInt (String.fromChar c))\n\n                Nothing ->\n                    Nothing\n        )\n\n\n\
run : Parser a -> String -> Maybe a\nrun (Parser p) s =\n    Maybe.map Tuple.first (p s)\n";

#[test]
fn a_user_type_imported_under_an_alias_uses_its_modules_and_then() {
    let body = "twoDigits : P.Parser Int\ntwoDigits =\n    let\n        a <- P.digit\n        b <- P.digit\n    in\n    P.succeed (a * 10 + b)";
    let extra = [("Lib.Parsing", PARSING)];
    let got = errors_in(body, &extra);
    assert!(got.is_empty(), "{got:#?}");
    assert_eq!(
        chosen(body, &extra, "twoDigits"),
        vec!["Lib.Parsing.andThen", "Lib.Parsing.andThen"]
    );
}

#[test]
fn an_app_type_named_decoder_uses_the_apps_and_then_not_the_json_one() {
    // D1: the app's own `Decoder` is not the kernel-implicit JSON `Decoder`.
    let body = "type Decoder a\n    = Decoder (String -> Maybe a)\n\n\nsucceed : a -> Decoder a\nsucceed v =\n    Decoder (\\_ -> Just v)\n\n\nandThen : (a -> Decoder b) -> Decoder a -> Decoder b\nandThen f (Decoder d) =\n    Decoder\n        (\\s ->\n            case d s of\n                Just v ->\n                    let\n                        (Decoder g) =\n                            f v\n                    in\n                    g s\n\n                Nothing ->\n                    Nothing\n        )\n\n\nboth : Decoder ( Int, Int )\nboth =\n    let\n        a <- succeed 1\n        b <- succeed 2\n    in\n    succeed ( a, b )";
    clean(body);
    assert_eq!(
        chosen(body, &[], "both"),
        vec!["Main.andThen", "Main.andThen"]
    );
}

// ---- annotation state (D3) ----------------------------------------------

#[test]
fn an_annotated_unannotated_or_any_holed_e_all_check() {
    clean("go : Int -> Task Error Int\ngo n =\n    let\n        t : Task Error Int\n        t =\n            Task.succeed n\n        a <- t\n    in\n    Task.succeed (a + 1)");
    clean("go : Int -> Task Error Int\ngo n =\n    let\n        t =\n            Task.succeed n\n        a <- t\n    in\n    Task.succeed (a + 1)");
    clean("fetch : Int -> Task any Int\nfetch n =\n    Task.succeed n\n\n\ngo : Int -> Task Error Int\ngo n =\n    let\n        a <- fetch n\n    in\n    Task.succeed (a + 1)");
    // Unannotated def, head from a stdlib call.
    clean("go n =\n    let\n        a <- Task.succeed n\n    in\n    Task.succeed (a + 1)");
}

#[test]
fn a_head_known_only_after_the_bind_is_chosen_at_the_end_of_the_def() {
    let body = "go t =\n    let\n        a <- t\n        u = Task.map identity t\n    in\n    Task.succeed a";
    clean(body);
    assert_eq!(chosen(body, &[], "go"), vec!["Sky.Core.Task.andThen"]);
}

#[test]
fn a_local_and_then_in_scope_is_never_picked() {
    let body = "go : Task Error Int -> Task Error Int\ngo t =\n    let\n        andThen f m =\n            m\n        a <- t\n    in\n    Task.succeed (a + 1)";
    let got = errors(body);
    assert!(
        got.iter().all(|(c, _)| c != "E2015" && c != "E2016"),
        "{got:#?}"
    );
    assert_eq!(chosen(body, &[], "go"), vec!["Sky.Core.Task.andThen"]);
}

#[test]
fn a_bind_line_in_a_let_bound_function_checks() {
    clean("go : Int -> Task Error Int\ngo n =\n    let\n        step : Int -> Task Error Int\n        step k =\n            let\n                a <- Task.succeed k\n            in\n            Task.succeed (a + 1)\n    in\n    step n");
}

#[test]
fn an_irrefutable_tuple_or_record_pattern_checks() {
    clean("go : Task Error ( Int, Int ) -> Task Error Int\ngo t =\n    let\n        ( a, b ) <- t\n    in\n    Task.succeed (a + b)");
    clean("go : Task Error { x : Int } -> Task Error Int\ngo t =\n    let\n        { x } <- t\n    in\n    Task.succeed x");
}

// ---- refusals ------------------------------------------------------------

#[test]
fn an_unknown_head_is_e2015() {
    let got = errors("go t =\n    let\n        a <- t\n    in\n    a");
    assert!(
        got.iter()
            .any(|(c, m)| c == "E2015" && m.contains("`a <- …`") && m.contains("Annotate")),
        "{got:#?}"
    );
}

#[test]
fn a_head_with_no_and_then_is_e2016() {
    let got = errors("go : Int -> Int\ngo n =\n    let\n        a <- n\n    in\n    a");
    assert!(got.iter().any(|(c, _)| c == "E2016"), "{got:#?}");
    let got =
        errors("go : List Int -> List Int\ngo xs =\n    let\n        a <- xs\n    in\n    [ a ]");
    assert!(
        got.iter().any(|(c, m)| c == "E2016"
            && m.contains("`List` (module Sky.Core.List) has no `andThen : (a -> List b)")
            && m.contains("; use `case`.")
            && !m.contains("add one")
            && !m.contains("by name")),
        "{got:#?}"
    );
}

const BOX_UNEXPOSED: &str = "module Lib.Box exposing (Box, box)\n\n\
import Sky.Core.Prelude exposing (..)\n\n\n\
type Box a\n    = Box a\n\n\n\
box : a -> Box a\nbox v =\n    Box v\n\n\n\
andThen : (a -> Box b) -> Box a -> Box b\nandThen f (Box v) =\n    f v\n";

const BOX_SHAPE: &str = "module Lib.Box exposing (Box, box, andThen)\n\n\
import Sky.Core.Prelude exposing (..)\n\n\n\
type Box a\n    = Box a\n\n\n\
box : a -> Box a\nbox v =\n    Box v\n\n\n\
andThen : Box a -> (a -> Box b) -> Box b\nandThen (Box v) f =\n    f v\n";

#[test]
fn an_unexposed_and_then_is_e2016() {
    let got = errors_in(
        "go : Lib.Box.Box Int -> Lib.Box.Box Int\ngo b =\n    let\n        a <- b\n    in\n    Lib.Box.box a",
        &[("Lib.Box", BOX_UNEXPOSED)],
    );
    assert!(
        got.iter()
            .any(|(c, m)| c == "E2016" && m.contains("does not expose it")),
        "{got:#?}"
    );
}

#[test]
fn a_misshaped_and_then_is_e2016() {
    let got = errors_in(
        "go : Lib.Box.Box Int -> Lib.Box.Box Int\ngo b =\n    let\n        a <- b\n    in\n    Lib.Box.box a",
        &[("Lib.Box", BOX_SHAPE)],
    );
    assert!(
        got.iter().any(|(c, m)| c == "E2016"
            && m.contains(
                "`Box` (module Lib.Box) has no `andThen : (a -> Box b) -> Box a -> Box b`"
            )
            && m.contains("has another shape")
            && m.contains("use `case`, or add one to Lib.Box")
            && !m.contains("by name")),
        "{got:#?}"
    );
}

/// An `andThen` of another type (here `Box`'s module exposes one over
/// `List`): the message never says to call it by name, since it does not fit.
const BOX_OTHER_TYPE: &str = "module Lib.Box exposing (Box, box, andThen)\n\n\
import Sky.Core.Prelude exposing (..)\n\n\n\
type Box a\n    = Box a\n\n\n\
box : a -> Box a\nbox v =\n    Box v\n\n\n\
andThen : (a -> List b) -> List a -> List b\nandThen f xs =\n    []\n";

#[test]
fn an_and_then_of_another_type_is_e2016_without_call_it_by_name() {
    let got = errors_in(
        "go : Lib.Box.Box Int -> Lib.Box.Box Int\ngo b =\n    let\n        a <- b\n    in\n    Lib.Box.box a",
        &[("Lib.Box", BOX_OTHER_TYPE)],
    );
    assert!(
        got.iter().any(|(c, m)| c == "E2016"
            && m.contains("has no `andThen : (a -> Box b) -> Box a -> Box b`")
            && m.contains("use `case`, or add one to Lib.Box")
            && !m.contains("by name")),
        "{got:#?}"
    );
}

/// The `[E1001]` across a bind split names a fix, and that fix compiles.
#[test]
fn the_split_hint_names_a_fix_that_compiles() {
    let broken = "f : Task Error Int\nf =\n    let\n        a = b + 1\n        x <- Task.succeed 1\n        b = x\n    in\n    Task.succeed a";
    let got = errors(broken);
    assert!(
        got.iter().any(|(c, m)| c == "E1001"
            && m.contains("Undefined name: b")
            && m.contains("move `a` below `x <- …`")),
        "{got:#?}"
    );
    // The fix applied: `a` below `x <- …`.
    clean("f : Task Error Int\nf =\n    let\n        x <- Task.succeed 1\n        a = b + 1\n        b = x\n    in\n    Task.succeed a");
}

#[test]
fn a_bind_line_in_and_then_itself_is_e2016() {
    let body = "type Box a\n    = Box a\n\n\nandThen : (a -> Box b) -> Box a -> Box b\nandThen f b =\n    let\n        v <- b\n    in\n    f v\n\n\nunbox : Box a -> a\nunbox (Box v) =\n    v";
    let got = errors(body);
    assert!(
        got.iter()
            .any(|(c, m)| c == "E2016" && m.contains("in `andThen` itself")),
        "{got:#?}"
    );
}

#[test]
fn a_refutable_bind_pattern_is_e3001_naming_the_missing_constructor() {
    let got = errors("go : Task Error (Maybe Int) -> Task Error Int\ngo t =\n    let\n        Just x <- t\n    in\n    Task.succeed x");
    assert!(
        got.iter().any(|(c, m)| c == "E3001"
            && m.contains("`Just x <- …`")
            && m.contains("`Nothing`")
            && m.contains("case")),
        "{got:#?}"
    );
}

#[test]
fn mixed_types_in_one_block_are_e2001_with_a_conversion_note() {
    let got = errors("go : Task Error Int\ngo =\n    let\n        a <- Task.succeed 1\n        b <- String.toInt \"2\"\n    in\n    Task.succeed (a + b)");
    assert!(
        got.iter().any(|(c, m)| c == "E2001"
            && m.contains("Task.fromResult")
            && m.contains("Maybe.toResult")),
        "{got:#?}"
    );
}

#[test]
fn an_unknown_head_in_a_recursive_group_is_reported_once() {
    let got = errors("ping t n =\n    let\n        a <- t\n    in\n    if n > 0 then pong t (n - 1) else a\n\n\npong t n =\n    ping t n");
    let n = got.iter().filter(|(c, _)| c == "E2015").count();
    assert_eq!(n, 1, "{got:#?}");
}

/// A block that mixes types gives ONE `[E2001]`, at the first bind line whose
/// type does not fit the block, not one at each `<-` (C1 gave two at each).
#[test]
fn a_mixed_block_is_one_e2001_at_the_first_mismatching_bind_line() {
    let body = "go : Task Error Int\ngo =\n    let\n        a <- Task.succeed 1\n        b <- String.toInt \"2\"\n        c <- Task.succeed 3\n    in\n    Task.succeed (a + b + c)";
    let (db, ids) = db_of(body, &[]);
    let src = db
        .module_parse(*ids.last().unwrap())
        .syntax()
        .text()
        .to_string();
    let e2001: Vec<_> = ty::check_modules(&db, &ids)
        .diagnostics
        .into_iter()
        .filter(|d| d.code.0 == "E2001")
        .collect();
    assert_eq!(e2001.len(), 1, "{e2001:#?}");
    let label = e2001[0].labels.first().expect("a label").span.range;
    let line = src[..label.0 as usize].lines().count();
    let b_line = src
        .lines()
        .position(|l| l.contains("b <- String.toInt"))
        .unwrap()
        + 1;
    assert_eq!(
        line,
        b_line,
        "at the `<-` of `b`: {}",
        &src[label.0 as usize..label.1 as usize]
    );
}

/// A type error inside the expression after `<-` is reported once, as that
/// error: the bind line adds no `[E2015]` on the same line (phase G1).
#[test]
fn a_type_error_in_e_gives_no_extra_e2015() {
    let body = "import Std.Sync as Sync exposing (Ref)\ngo : Bool -> Ref Int -> Task Error Int\ngo c ref =\n    let\n        _ <- Task.unless c (Sync.update (\\n -> n + 1) ref)\n    in\n    Task.succeed 1";
    let got = errors_with_import(body);
    assert!(got.iter().any(|(c, _)| c == "E2001"), "{got:#?}");
    assert!(got.iter().all(|(c, _)| c != "E2015"), "{got:#?}");
}

/// The conversion note is for two different type constructors only. Two
/// `Maybe` lines whose contents clash (Float against Int) get the plain
/// mismatch, the same message as the hand-written `Maybe.andThen` chain.
#[test]
fn a_clash_inside_one_head_gets_no_conversion_note() {
    let bind = errors("go : Maybe Float\ngo =\n    let\n        a <- Just 1.5\n        b <- Just 2\n    in\n    Just (a * b)");
    assert!(bind.iter().any(|(c, _)| c == "E2001"), "{bind:#?}");
    assert!(
        bind.iter()
            .all(|(_, m)| !m.contains("One block cannot mix types")),
        "{bind:#?}"
    );
    let unannotated = errors(
        "go =\n    let\n        a <- Just 1.5\n        b <- Just 2\n    in\n    Just (a * b)",
    );
    assert!(
        unannotated
            .iter()
            .all(|(_, m)| !m.contains("One block cannot mix types")),
        "{unannotated:#?}"
    );
    let chain = errors("go : Maybe Float\ngo =\n    Just 1.5 |> Maybe.andThen (\\a -> Just 2 |> Maybe.andThen (\\b -> Just (a * b)))");
    let msgs = |v: &[(String, String)]| v.iter().map(|(c, _)| c.clone()).collect::<Vec<_>>();
    assert_eq!(msgs(&bind), msgs(&chain), "{bind:#?}\n{chain:#?}");
}

/// An unknown or misspelt name in the expression after `<-` is the error:
/// `[E1001]` names it, and the bind line adds no `[E2015]` / `[E2016]` of its
/// own (Judge 6, u1). Before, the name was typed as a fresh variable, the
/// head stayed unknown, and `[E2015]` "annotate it" was the only report.
#[test]
fn an_unknown_name_in_e_is_e1001_without_e2015_or_e2016() {
    for line in [
        "b <- fetchh a",
        "b <- Sync.new 0",
        "b <- nosuch 0",
        "b <- Task.nosuch 0",
        // A head known from a correct function, an unknown argument: still
        // the name error alone, no `[E2016]` for an `Int`.
        "b <- String.length (nosuch 0)",
    ] {
        let body = format!(
            "import Std.Sync as Sync\ngo : Int -> Task Error Int\ngo n =\n    let\n        a <- Task.succeed n\n        {line}\n    in\n    Task.succeed a"
        );
        let got = errors_with_import(&body);
        assert!(
            got.iter().any(|(c, m)| c == "E1001" && m.contains("nosuch")
                || c == "E1001" && m.contains("fetchh")
                || c == "E1001" && m.contains("Sync.new")),
            "{line}: {got:#?}"
        );
        assert!(
            got.iter().all(|(c, _)| c != "E2015" && c != "E2016"),
            "{line}: {got:#?}"
        );
    }
}

/// A head known from `e` with an unknown name in an argument still gets the
/// mixed-types `[E2001]` (the mismatch is real), and the `[E1001]` is kept.
#[test]
fn an_unknown_argument_in_a_mixed_block_keeps_e1001_and_the_note() {
    let got = errors("go : Task Error Int\ngo =\n    let\n        a <- Task.succeed 1\n        b <- String.toInt (nosuch 2)\n    in\n    Task.succeed (a + b)");
    assert!(
        got.iter()
            .any(|(c, m)| c == "E1001" && m.contains("nosuch")),
        "{got:#?}"
    );
    assert!(
        got.iter().all(|(c, _)| c != "E2015" && c != "E2016"),
        "{got:#?}"
    );
}

/// A function after `<-` (an argument missing): `[E2016]` names the
/// function and how many arguments it still needs.
#[test]
fn a_function_after_the_arrow_is_e2016_naming_the_missing_arguments() {
    let got = errors(
        "go : Task Error Int\ngo =\n    let\n        a <- Task.succeed\n    in\n    Task.succeed 1",
    );
    assert!(
        got.iter().any(|(c, m)| c == "E2016"
            && m.contains("`Task.succeed` is a function (`")
            && m.contains("it needs 1 more argument(s) before `<-` can run it")),
        "{got:#?}"
    );
    let got = errors("pair : Int -> Int -> Maybe Int\npair x y =\n    Just (x + y)\n\n\ngo : Maybe Int\ngo =\n    let\n        a <- pair\n    in\n    Just a");
    assert!(
        got.iter().any(|(c, m)| c == "E2016"
            && m.contains("`pair` is a function (`Int -> Int -> Maybe Int`)")
            && m.contains("it needs 2 more argument(s)")),
        "{got:#?}"
    );
}
