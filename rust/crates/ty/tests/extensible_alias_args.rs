//! Regression (v0.29.2, Judge round 8): an extensible-record alias applied to
//! a record keeps the record's fields.
//!
//! `type alias Named r = { r | name : String }` with `Named { count : Int }`
//! expanded to `{ r | name : String }`: the alias substitution replaced the
//! field types but kept the row variable, so `count` was lost. Under rigid
//! rows a correct `init : Named { count : Int }; init = { name = "a", count =
//! 2 }` was refused ("add `count` to the record type"). v0.29.1 accepted it.
//!
//! The cases are generated: every alias shape (a row bound to a closed
//! record, to another extensible alias, to an open record, to a variable, a
//! two-parameter alias, a nested row, a cross-module alias) crossed with
//! every use (build a value, read the argument's field, update it, inside a
//! container), each with a rejected twin.

use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        if !dir.pop() {
            panic!("could not locate repo root (no sky-stdlib ancestor)");
        }
    }
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for p in entries {
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

/// Error messages (`[code] message`) of `main` plus any extra modules.
fn errors_with(main: &str, extra: &[(&str, &str)]) -> Vec<String> {
    let mut files = Vec::new();
    collect_sky(&repo_root().join("sky-stdlib"), &mut files);
    let mut db = hir::SourceDb::new();
    for path in files {
        let src = std::fs::read_to_string(&path).unwrap();
        let parse = syntax::parse(&src, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .unwrap_or_default();
        if !name.is_empty() {
            db.add_module(&name, parse);
        }
    }
    let mut ids = Vec::new();
    for (i, (name, src)) in extra.iter().enumerate() {
        ids.push(db.add_module(name, syntax::parse(src, base::FileId(2 + i as u32))));
    }
    let mid = db.add_module("Main", syntax::parse(main, base::FileId(1)));
    ids.push(mid);
    ty::check_modules(&db, &ids)
        .diagnostics
        .into_iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .map(|d| format!("[{}] {}", d.code.0, d.message))
        .collect()
}

const HEAD: &str = "module Main exposing (main)\n\
    import Sky.Core.Prelude exposing (..)\n\
    import Sky.Core.List as List\n\
    import Sky.Core.String as String\n\
    import Std.Log exposing (println)\n\n";

/// The aliases every case may use.
const ALIASES: &str = "type alias Named r =\n    { r | name : String }\n\n\
    type alias Aged r =\n    { r | age : Int }\n\n\
    type alias Person =\n    { age : Int }\n\n\
    type alias Pair a r =\n    { r | first : a }\n\n\
    type alias Wrap r =\n    { inner : { r | x : Int } }\n\n";

fn errors(body: &str) -> Vec<String> {
    errors_with(
        &format!("{HEAD}{ALIASES}{body}\n\nmain =\n    println \"x\"\n"),
        &[],
    )
}

/// One alias shape: the annotation type, a value of it, an expression that
/// reads a field the ARGUMENT added (`p` is the value), its type, and a field
/// the type does not have.
struct Shape {
    ty: &'static str,
    value: &'static str,
    read: &'static str,
    read_ty: &'static str,
    absent: &'static str,
}

const SHAPES: &[Shape] = &[
    Shape {
        ty: "Named { age : Int }",
        value: "{ name = \"a\", age = 3 }",
        read: "p.age",
        read_ty: "Int",
        absent: "height",
    },
    Shape {
        ty: "Named (Aged {})",
        value: "{ name = \"a\", age = 3 }",
        read: "p.age",
        read_ty: "Int",
        absent: "height",
    },
    Shape {
        ty: "Named Person",
        value: "{ name = \"a\", age = 3 }",
        read: "p.age",
        read_ty: "Int",
        absent: "height",
    },
    Shape {
        ty: "Named { age : Int, tags : List String }",
        value: "{ name = \"a\", age = 3, tags = [] }",
        read: "List.length p.tags",
        read_ty: "Int",
        absent: "height",
    },
    Shape {
        ty: "Pair Int { second : String }",
        value: "{ first = 1, second = \"b\" }",
        read: "p.second",
        read_ty: "String",
        absent: "third",
    },
    Shape {
        ty: "Wrap { y : Int }",
        value: "{ inner = { x = 1, y = 2 } }",
        read: "p.inner.y",
        read_ty: "Int",
        absent: "z",
    },
];

#[test]
fn an_alias_argument_keeps_its_fields_in_every_use() {
    let mut failures = Vec::new();
    for s in SHAPES {
        let cases = [
            // a value of the alias type
            format!("v : {}\nv =\n    {}\n", s.ty, s.value),
            // a function reading the argument's field
            format!("get : {} -> {}\nget p =\n    {}\n", s.ty, s.read_ty, s.read),
            // a function returning the value it was given, in a container
            format!("keep : List ({}) -> List ({})\nkeep ps =\n    ps\n", s.ty, s.ty),
            format!("first : Maybe ({}) -> Maybe ({})\nfirst p =\n    p\n", s.ty, s.ty),
            // building and reading in one body
            format!(
                "v : {}\nv =\n    {}\n\nuse : {}\nuse =\n    let\n        p =\n            v\n    in\n    {}\n",
                s.ty, s.value, s.read_ty, s.read
            ),
        ];
        for c in cases {
            let errs = errors(&c);
            if !errs.is_empty() {
                failures.push(format!("{c}\n  => {}", errs.join("\n     ")));
            }
        }
        // The twin: a field the type does not have is still refused.
        let bad = format!("get : {} -> Int\nget p =\n    p.{}\n", s.ty, s.absent);
        if errors(&bad).is_empty() {
            failures.push(format!("accepted a field the type lacks:\n{bad}"));
        }
        // The twin: a value missing the argument's field is refused.
        let missing = format!("v : {}\nv =\n    {{ name = \"a\" }}\n", s.ty);
        if !s.ty.starts_with("Pair") && !s.ty.starts_with("Wrap") && errors(&missing).is_empty() {
            failures.push(format!("accepted a value missing a field:\n{missing}"));
        }
    }
    assert!(failures.is_empty(), "\n{}", failures.join("\n\n"));
}

/// The update from the Judge's `alias.sky`, and the alias of an alias
/// (`type alias Model = Named { count : Int }`, the Judge's `alias4.sky`).
#[test]
fn an_alias_of_an_applied_alias_and_an_update_check() {
    for body in [
        "older : Named { age : Int } -> Named { age : Int }\nolder p =\n    { p | age = p.age + 1 }\n",
        "type alias Model =\n    Named { count : Int }\n\n\
         init : Model\ninit =\n    { name = \"a\", count = 2 }\n\n\
         label : Model -> String\nlabel m =\n    m.name ++ String.fromInt m.count\n",
    ] {
        let errs = errors(body);
        assert!(errs.is_empty(), "{body}\n{}", errs.join("\n"));
    }
}

/// A row variable bound to a variable stays a row of its own:
/// `Named a` and `Named b` are two rows, so two different records check
/// (the Judge's `alias3.sky`), and making them one is refused.
#[test]
fn a_row_argument_that_is_a_variable_keeps_its_own_row() {
    let ok = "both : Named a -> Named b -> String\nboth x y =\n    x.name ++ y.name\n\n\
              call : String\ncall =\n    both { name = \"a\", age = 3 } { name = \"b\" }\n";
    let errs = errors(ok);
    assert!(errs.is_empty(), "{ok}\n{}", errs.join("\n"));
    let bad = "pick : Named a -> Named b -> Named a\npick x y =\n    y\n";
    assert!(
        errors(bad).iter().any(|e| e.contains("[E2001]")),
        "accepted two rows made one:\n{bad}"
    );
    // An open record argument: its own row variable is the result's.
    let open = "get : Named { r | age : Int } -> Int\nget p =\n    p.age\n\n\
                call : Int\ncall =\n    get { name = \"a\", age = 3, extra = True }\n";
    let errs = errors(open);
    assert!(errs.is_empty(), "{open}\n{}", errs.join("\n"));
}

/// The alias declared in another module, applied qualified.
#[test]
fn a_cross_module_alias_argument_keeps_its_fields() {
    let types = "module Types exposing (Named)\n\n\
                 type alias Named r =\n    { r | name : String }\n";
    let main = "module Main exposing (main)\n\
                import Sky.Core.Prelude exposing (..)\n\
                import Std.Log exposing (println)\n\
                import Types\n\n\
                init : Types.Named { count : Int }\ninit =\n    { name = \"a\", count = 2 }\n\n\
                main =\n    println init.name\n";
    let errs = errors_with(main, &[("Types", types)]);
    assert!(errs.is_empty(), "{}", errs.join("\n"));
}
