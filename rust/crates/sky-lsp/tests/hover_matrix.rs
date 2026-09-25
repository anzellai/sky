//! Hover + go-to-definition across the three symbol classes users hover most:
//! record fields, imported functions, and types. Each case names the site and
//! what the answer must contain.
//!
//! Before this matrix: a field hovered only at `r.field` (a record literal,
//! update, pattern, alias declaration, or `.field` accessor answered nothing, and
//! a field of a record alias rendered its alias-EXPANDED record instead of the
//! alias the user wrote); a function hover carried no doc comment; a type hover
//! was the bare `type Name` (no definition, no doc), a qualified type (`T.User`)
//! and a type variable answered nothing, and a builtin constructor (`Just`)
//! hovered as `?`. Go-to-definition on a field of a record alias declared in
//! ANOTHER module found nothing.

use sky_lsp::Analysis;
use std::path::{Path, PathBuf};
use tower_lsp::lsp_types::{HoverContents, Position, Url};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found above the crate")
        .to_path_buf()
}

fn url(name: &str) -> Url {
    Url::from_file_path(format!("/tmp/lsp-hover-matrix/src/{name}.sky")).unwrap()
}

const TYPES: &str = r#"module Types exposing (User, Address, Shape(..), describe, mkUser, area)

import Sky.Core.Prelude exposing (..)


-- | A user of the system.
type alias User =
    { name : String
    , age : Int
    , address : Address
    }


-- | A postal address.
type alias Address =
    { city : String
    , zip : String
    }


-- | A geometric shape.
type Shape
    = Circle Float
    | Rect Float Float


-- | Describe a user by name.
describe : User -> String
describe u =
    u.name


-- | Build a user from a name.
mkUser : String -> User
mkUser n =
    { name = n, age = 1, address = { city = "x", zip = "y" } }


-- | The area of a shape.
area : Shape -> Float
area s =
    case s of
        Circle r ->
            r * r

        Rect w h ->
            w * h
"#;

const MAIN: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Sky.Core.String as String
import Std.Cmd as Cmd
import Std.Log exposing (println)
import Types exposing (User, Shape(..), describe)
import Types as T


type alias Model =
    { user : User
    , count : Int
    , label : String
    }


init : Model
init =
    { user = T.mkUser "ada", count = 0, label = "é-start" }


bump : Model -> Model
bump m =
    { m | count = m.count + 1 }


city : Model -> String
city model =
    model.user.address.city


names : List User -> List String
names users =
    List.map .name users


ageOf : User -> Int
ageOf { age } =
    age


lamb : List User -> List Int
lamb us =
    List.map (\u -> u.age) us


letted : Model -> String
letted m =
    let
        usr =
            m.user
    in
    usr.name


shapeName : Shape -> String
shapeName s =
    case s of
        Circle _ ->
            "circle"

        Rect _ _ ->
            "rect"


pick : Maybe a -> Result Error a -> List a
pick mb r =
    []


qual : T.User -> String
qual u =
    u.name


fx : Model -> ( Model, Cmd.Cmd msg )
fx m =
    ( m, Cmd.none )


flags : Model -> ( Maybe Int, Bool, Int )
flags m =
    ( Just 1, True, modBy 3 m.count )


unic : Model -> String
unic m = "日本語" ++ describe m.user ++ String.fromInt (T.area (Circle 1.0)) ++ identity "k"


main =
    println (city (bump init) ++ letted init ++ String.fromInt (String.length (Crypto.sha256 "x")))
"#;

struct World {
    a: Analysis,
    main: Url,
    types: Url,
    main_text: String,
}

fn world_with(main_text: &str) -> World {
    let mut a = Analysis::new();
    a.load_stdlib(Some(&repo_root()));
    let types = url("Types");
    a.set_document(types.clone(), TYPES.to_string());
    let main = url("Main");
    a.set_document(main.clone(), main_text.to_string());
    World {
        a,
        main,
        types,
        main_text: main_text.to_string(),
    }
}

fn world() -> World {
    world_with(MAIN)
}

/// LSP position (UTF-16 columns) of `needle`'s `nth` occurrence, shifted by
/// `delta` UTF-16 units — computed in UTF-16 exactly as an editor sends it.
fn pos_in(text: &str, needle: &str, nth: usize, delta: u32) -> Position {
    let mut from = 0;
    let mut at = 0;
    for _ in 0..=nth {
        at = from + text[from..].find(needle).expect("needle not in text");
        from = at + needle.len();
    }
    let line_start = text[..at].rfind('\n').map_or(0, |i| i + 1);
    let line = text[..at].matches('\n').count() as u32;
    let col: u32 = text[line_start..at].encode_utf16().count() as u32;
    Position {
        line,
        character: col + delta,
    }
}

impl World {
    fn hover_main(&self, needle: &str, delta: u32) -> String {
        self.hover_on(&self.main, &self.main_text.clone(), needle, 0, delta)
    }

    fn hover_on(&self, u: &Url, text: &str, needle: &str, nth: usize, delta: u32) -> String {
        match self.a.hover(u, pos_in(text, needle, nth, delta)) {
            Some(h) => match h.contents {
                HoverContents::Markup(m) => m.value,
                _ => String::new(),
            },
            None => String::new(),
        }
    }

    /// `(file stem, 1-based line)` go-to-definition lands on.
    fn goto_main(&self, needle: &str, delta: u32) -> Option<(String, u32)> {
        let loc = self
            .a
            .goto(&self.main, pos_in(&self.main_text, needle, 0, delta))?;
        let stem = loc
            .uri
            .path()
            .rsplit('/')
            .next()
            .unwrap_or_default()
            .trim_end_matches(".sky")
            .to_string();
        Some((stem, loc.range.start.line + 1))
    }
}

fn has(h: &str, want: &[&str], what: &str) {
    for w in want {
        assert!(h.contains(w), "{what}: hover must contain {w:?}, got {h:?}");
    }
}

fn line_of(text: &str, needle: &str) -> u32 {
    text[..text.find(needle).expect("needle")]
        .matches('\n')
        .count() as u32
        + 1
}

// ---- (a) record fields -------------------------------------------------

#[test]
fn field_hover_at_every_site() {
    let w = world();
    let cases: &[(&str, u32, &[&str], &str)] = &[
        (
            "m.count + 1",
            3,
            &["count : Int", "Field of `Model`"],
            "r.field access",
        ),
        (
            "count = 0",
            1,
            &["count : Int", "Field of `Model`"],
            "record literal field",
        ),
        (
            "| count =",
            3,
            &["count : Int", "Field of `Model`"],
            "record update field",
        ),
        (
            "{ age }",
            3,
            &["age : Int", "Field of `User`"],
            "record pattern field",
        ),
        (
            "count : Int",
            1,
            &["count : Int", "Field of `Model`"],
            "alias declaration field",
        ),
        (
            "model.user.address",
            7,
            &["user : User", "Field of `Model`"],
            "nested a.b",
        ),
        (
            "user.address.city",
            7,
            &["address : Address", "Field of `User`"],
            "nested a.b.c",
        ),
        (
            "address.city",
            9,
            &["city : String", "Field of `Address`"],
            "nested a.b.c.d",
        ),
        (
            "usr.name",
            5,
            &["name : String", "Field of `User`"],
            "let-bound receiver",
        ),
        (
            "u.age)",
            3,
            &["age : Int", "Field of `User`"],
            "lambda-bound receiver",
        ),
        (
            ".name users",
            2,
            &["name : String", "Field of `User`"],
            "accessor function",
        ),
        (
            "m.user ++",
            3,
            &["user : User"],
            "after a non-ASCII line prefix",
        ),
        (
            "label = ",
            1,
            &["label : String"],
            "literal line with a non-ASCII value",
        ),
    ];
    for (needle, delta, want, what) in cases {
        has(&w.hover_main(needle, *delta), want, what);
    }
    // The alias the user wrote, never the expanded record.
    let nested = w.hover_main("model.user.address", 7);
    assert!(
        !nested.contains("{ address"),
        "a field of alias type must render the alias, not its expansion: {nested:?}"
    );
    // A field declared in another module's alias.
    let x = w.hover_on(&w.types, TYPES, "age : Int", 0, 1);
    has(
        &x,
        &["age : Int", "Field of `User`"],
        "cross-module alias decl field",
    );
}

#[test]
fn field_goto_reaches_alias_in_any_module() {
    let w = world();
    let main_line = |n: &str| line_of(MAIN, n);
    let types_line = |n: &str| line_of(TYPES, n);
    let cases: &[(&str, u32, (&str, u32))] = &[
        ("m.count + 1", 3, ("Main", main_line(", count : Int"))),
        ("count = 0", 1, ("Main", main_line(", count : Int"))),
        ("| count =", 3, ("Main", main_line(", count : Int"))),
        ("{ age }", 3, ("Types", types_line(", age : Int"))),
        (
            "user.address.city",
            7,
            ("Types", types_line(", address : Address")),
        ),
        ("address.city", 9, ("Types", types_line("{ city : String"))),
        ("usr.name", 5, ("Types", types_line("{ name : String"))),
        ("u.age)", 3, ("Types", types_line(", age : Int"))),
        (".name users", 2, ("Types", types_line("{ name : String"))),
    ];
    for (needle, delta, (file, line)) in cases {
        assert_eq!(
            w.goto_main(needle, *delta),
            Some((file.to_string(), *line)),
            "goto on field at {needle:?}"
        );
    }
}

#[test]
fn field_declaration_reference_covers_only_the_name() {
    let w = world();
    let refs =
        w.a.references(&w.main, pos_in(MAIN, "m.count + 1", 0, 3), true);
    let decl_line = line_of(MAIN, ", count : Int") - 1;
    let decl = refs
        .iter()
        .find(|l| l.uri == w.main && l.range.start.line == decl_line)
        .expect("declaration listed");
    // `, count : Int` — the range is `count` (5 UTF-16 units), not `count : Int`.
    assert_eq!(
        (decl.range.start.character, decl.range.end.character),
        (6, 11),
        "a field declaration occurrence must cover only its name: {decl:?}"
    );
}

#[test]
fn a_record_pattern_binder_is_not_renamed_as_a_plain_local() {
    // `ageOf { age } = age` — the binder IS the field name, so a rename from its
    // use would rewrite the pattern to read a field that does not exist.
    let w = world();
    let use_site = pos_in(MAIN, "    age\n\n\nlamb", 0, 4);
    assert!(
        w.a.rename(&w.main, use_site, "years").is_none(),
        "renaming a punned record-pattern binder must be refused"
    );
    // An ordinary local still renames.
    assert!(w
        .a
        .rename(&w.main, pos_in(MAIN, "usr.name", 0, 1), "who")
        .is_some());
}

// ---- (b) imported functions --------------------------------------------

#[test]
fn function_hover_carries_signature_and_doc() {
    let w = world();
    let cases: &[(&str, u32, &[&str], &str)] = &[
        (
            "List.map .name",
            6,
            &[
                "map : (a -> b) -> List a -> List b",
                "apply `fn` to each element",
            ],
            "stdlib List.map",
        ),
        (
            "T.mkUser",
            3,
            &["mkUser : String -> User", "Build a user from a name."],
            "aliased project import",
        ),
        (
            "describe m.user",
            2,
            &["describe : User -> String", "Describe a user by name."],
            "exposed import used bare (after non-ASCII)",
        ),
        (
            "T.area",
            3,
            &["area : Shape -> Float", "The area of a shape."],
            "T.area",
        ),
        (
            "println (city",
            2,
            &["println : String -> Task Error ()"],
            "exposed stdlib",
        ),
        (
            "Crypto.sha256",
            8,
            &["sha256 : String -> String"],
            "kernel, no import",
        ),
        (
            "identity \"k\"",
            2,
            &["identity : a -> a"],
            "builtin Prelude value",
        ),
        (
            "modBy 3",
            2,
            &["modBy : Int -> Int -> Int"],
            "builtin kernel",
        ),
    ];
    for (needle, delta, want, what) in cases {
        let h = w.hover_main(needle, *delta);
        has(&h, want, what);
        assert!(!h.contains(": ?"), "{what}: no `?` type, got {h:?}");
    }
}

#[test]
fn function_goto_reaches_source() {
    let w = world();
    assert_eq!(
        w.goto_main("T.mkUser", 3),
        Some(("Types".into(), line_of(TYPES, "mkUser n =")))
    );
    assert_eq!(
        w.goto_main("describe m.user", 2),
        Some(("Types".into(), line_of(TYPES, "describe u =")))
    );
    // A kernel function jumps to its stdlib stub.
    let (file, _) = w.goto_main("Crypto.sha256", 8).expect("kernel goto");
    assert_eq!(file, "Crypto");
    let (file, _) = w.goto_main("identity \"k\"", 2).expect("builtin goto");
    assert_eq!(file, "Basics");
}

// ---- (c) types ---------------------------------------------------------

#[test]
fn type_hover_shows_definition_and_doc() {
    let w = world();
    let cases: &[(&str, u32, &[&str], &str)] = &[
        (
            "bump : Model",
            8,
            &["type alias Model =", "count : Int"],
            "local alias",
        ),
        (
            "ageOf : User",
            9,
            &[
                "type alias User =",
                "address : Address",
                "A user of the system.",
            ],
            "imported alias",
        ),
        (
            "qual : T.User",
            9,
            &["type alias User =", "A user of the system."],
            "qualified imported type",
        ),
        (
            "shapeName : Shape",
            13,
            &[
                "type Shape",
                "= Circle Float",
                "| Rect Float Float",
                "A geometric shape.",
            ],
            "union",
        ),
        (
            "Circle _",
            2,
            &["Circle : Float -> Shape"],
            "ctor in pattern",
        ),
        (
            "(Circle 1.0)",
            3,
            &["Circle : Float -> Shape"],
            "ctor in expression",
        ),
        ("Maybe a ->", 6, &["a", "Type variable"], "type variable"),
        (
            "pick : Maybe",
            9,
            &["type Maybe a", "= Just a", "| Nothing"],
            "Maybe",
        ),
        (
            "Result Error",
            2,
            &["type Result error value", "= Ok value"],
            "Result",
        ),
        (
            "Result Error",
            9,
            &["type Error"],
            "Error (stdlib-sourced builtin)",
        ),
        (
            "Cmd.Cmd msg",
            6,
            &["type Cmd"],
            "kernel-implicit qualified type",
        ),
        ("Just 1", 2, &["Just : a -> Maybe a"], "builtin ctor"),
        ("True, modBy", 2, &["True : Bool"], "Bool literal"),
    ];
    for (needle, delta, want, what) in cases {
        let h = w.hover_main(needle, *delta);
        has(&h, want, what);
        assert!(!h.contains(": ?"), "{what}: no `?` type, got {h:?}");
    }
}

#[test]
fn type_goto_reaches_definition() {
    let w = world();
    assert_eq!(
        w.goto_main("ageOf : User", 9),
        Some(("Types".into(), line_of(TYPES, "type alias User")))
    );
    assert_eq!(
        w.goto_main("qual : T.User", 9),
        Some(("Types".into(), line_of(TYPES, "type alias User"))),
        "qualified type"
    );
    assert_eq!(
        w.goto_main("Circle _", 2),
        Some(("Types".into(), line_of(TYPES, "= Circle Float")))
    );
    let (file, _) = w.goto_main("Result Error", 9).expect("Error goto");
    assert_eq!(file, "Error");
}

// ---- robustness ----------------------------------------------------------

#[test]
fn hover_survives_a_type_error_elsewhere_and_unsaved_edits() {
    let mut w = world();
    // An unsaved edit (didChange) that adds an ill-typed def and a new function.
    let edited = MAIN.replace(
        "main =\n",
        "bad : Int\nbad =\n    \"not an int\"\n\n\nfresh : Model -> String\nfresh mm =\n    mm.label\n\n\nmain =\n",
    );
    w.a.set_document(w.main.clone(), edited.clone());
    w.main_text = edited;
    has(
        &w.hover_main("m.count + 1", 3),
        &["count : Int"],
        "field after error",
    );
    has(
        &w.hover_main("mm.label", 4),
        &["label : String"],
        "field in unsaved fn",
    );
    has(
        &w.hover_main("describe m.user", 2),
        &["describe : User -> String", "Describe a user"],
        "fn after error",
    );
    has(
        &w.hover_main("ageOf : User", 9),
        &["type alias User ="],
        "type after error",
    );
    assert_eq!(
        w.goto_main("mm.label", 4),
        Some(("Main".into(), line_of(MAIN, ", label : String")))
    );
}
