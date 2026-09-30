//! C-2 and D-ANY (v0.27.0 audit): the relaxed value restriction for top-level
//! CAFs, and `any` in a user annotation as a partial-signature hole.
//!
//! A top-level def with no parameters is a CAF: its body runs once and every
//! use shares the value. Before v0.27.0 such a def was generalised freely, so
//!
//! ```elm
//! shared : Result Error (Sync.Ref (List a))
//! shared = Task.run (Sync.newRef [])
//! ```
//!
//! let one use store `[ 1, 2, 3 ]` and another read it back as a list of
//! records: `sky check` and `sky build` passed and the program printed
//! `<nil>!,<nil>!,<nil>!` (repro `C/t6`). The same hole was open through an
//! unannotated CAF used from two defs, and through `any`, which was a fresh
//! variable at every use and so an unchecked cast (`coerce : a -> any`).
//!
//! Rule: a CAF whose body is an application may be generalised only over type
//! variables in covariant positions (`List a`, `Cmd msg`, `Element msg`).
//! Stdlib types are invariant unless allowlisted (`crate::variance`); user
//! types get their variance by fixpoint. `any` in a user signature is filled
//! from the body. A variable the rule does not generalise becomes WEAK (one
//! fixed opaque type, as OCaml keeps a `_weak` variable): a use that needs a
//! specific type there is rejected `[E2012]`, a use that does not care stays
//! legal. A misuse of a filled `any` is an ordinary `[E2001]` with a migration
//! note.

use hir::SourceDb;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        if !dir.pop() {
            panic!("could not locate repo root");
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

/// The error diagnostics of a one-module program, as `CODE message` lines.
fn errors(src: &str) -> Vec<String> {
    let root = repo_root();
    let mut files = Vec::new();
    collect_sky(&root.join("sky-stdlib"), &mut files);
    let mut db = SourceDb::new();
    for path in files {
        let Ok(s) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&s, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .filter(|s| !s.is_empty())
            .unwrap_or_default();
        db.add_module(&name, parse);
    }
    let mid = db.add_module("Main", syntax::parse(src, base::FileId(0)));
    ty::check_modules(&db, &[mid])
        .diagnostics
        .iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .map(|d| format!("{} {}", d.code.0, d.message))
        .collect()
}

const HDR: &str = "module Main exposing (main)\n\
                   import Sky.Core.Prelude exposing (..)\n\
                   import Sky.Core.List as List\n\
                   import Sky.Core.Task as Task\n\
                   import Std.Sync as Sync\n\
                   import Std.Cache as Cache\n\
                   import Std.Cmd as Cmd\n\
                   import Std.Ui as Ui\n\
                   import Std.App as App\n\
                   import Std.Log exposing (println)\n\n";

fn program(body: &str) -> String {
    format!("{HDR}{body}\n\nmain : Task Error ()\nmain =\n    println \"ok\"\n")
}

#[track_caller]
fn assert_rejects(body: &str, code: &str) {
    let errs = errors(&program(body));
    assert!(
        errs.iter().any(|e| e.starts_with(code)),
        "expected a [{code}] rejection, got: {errs:#?}\n--- program ---\n{body}"
    );
}

#[track_caller]
fn assert_accepts(body: &str) {
    let errs = errors(&program(body));
    assert!(
        errs.is_empty(),
        "expected no error, got: {errs:#?}\n--- program ---\n{body}"
    );
}

// ---- C-2: the value restriction -------------------------------------------

/// Repro `C/t6`: the annotated polymorphic `Ref` CAF used at two types.
#[test]
fn annotated_polymorphic_ref_caf_is_rejected() {
    assert_rejects(
        r#"shared : Result Error (Sync.Ref (List a))
shared =
    Task.run (Sync.newRef [])


writeInts : Task Error ()
writeInts =
    case shared of
        Ok r ->
            Sync.set [ 1, 2, 3 ] r

        Err e ->
            Task.fail e


readNames : Task Error String
readNames =
    case shared of
        Ok r ->
            Sync.get r |> Task.map (\xs -> String.join "," (List.map (\x -> x.name ++ "!") xs))

        Err e ->
            Task.fail e"#,
        "E2012",
    );
}

/// The unannotated form, used from TWO defs (pass 5 generalised it with no
/// value restriction, so each use instantiated it afresh).
#[test]
fn unannotated_ref_caf_used_from_two_defs_is_rejected() {
    assert_rejects(
        r#"shared =
    Task.run (Sync.newRef [])


writeInts : Task Error ()
writeInts =
    case shared of
        Ok r ->
            Sync.set [ 1, 2, 3 ] r

        Err e ->
            Task.fail e


readStrings : Task Error String
readStrings =
    case shared of
        Ok r ->
            Sync.get r |> Task.map (String.join ",")

        Err e ->
            Task.fail e"#,
        "E2012",
    );
}

/// The unannotated form whose initial value is a LATER sibling: pass 5 infers
/// in source order, so the CAF first saw `initial` as a fresh variable.
#[test]
fn unannotated_ref_caf_over_a_later_sibling_is_rejected() {
    assert_rejects(
        r#"shared =
    Task.run (Sync.newRef initial)


initial =
    []


useIt : Task Error ()
useIt =
    case shared of
        Ok r ->
            Sync.set [ 1 ] r

        Err e ->
            Task.fail e"#,
        "E2012",
    );
}

/// `Std.Cache` is phantom-typed over an `Int` handle; it must be invariant.
#[test]
fn polymorphic_cache_caf_is_rejected() {
    assert_rejects(
        r#"cache : Result Error (Cache.Cache String v)
cache =
    Task.run (Cache.new Cache.defaultCfg)


useIt : Task Error ()
useIt =
    case cache of
        Ok c ->
            Cache.put c "k" 1

        Err e ->
            Task.fail e"#,
        "E2012",
    );
}

/// The `Std.Codec.auto` shape (a point-free application of a function type
/// whose variable reaches an invariant position), on a local fixture.
#[test]
fn point_free_application_over_an_invariant_type_is_rejected() {
    assert_rejects(
        r#"type Printer a
    = Printer (a -> String)


makePrinter : Bool -> a -> Printer a
makePrinter _ _ =
    Printer (\_ -> "x")


printer : a -> Printer a
printer =
    makePrinter True


forInts : Printer Int
forInts =
    printer 5"#,
        "E2012",
    );
}

/// A user type that is contravariant in its parameter (via a function field).
#[test]
fn user_contravariant_type_caf_is_rejected() {
    assert_rejects(
        r#"type Pred a
    = Pred (a -> Bool)


always : Pred a
always =
    identity (Pred (\_ -> True))


check : Bool
check =
    case always of
        Pred f ->
            f 1"#,
        "E2012",
    );
}

/// A weak variable no use fixes stays legal: the value is only ever used
/// opaquely (the `appDef` shape: `App.run appDef` with an unset `key`).
#[test]
fn weak_variable_used_opaquely_is_accepted() {
    assert_accepts(
        r#"pending : Result Error (Sync.Ref (List a))
pending =
    Task.run (Sync.newRef [])


isReady : Bool
isReady =
    case pending of
        Ok _ ->
            True

        Err _ ->
            False


type Handlers k
    = Handlers (k -> Int)


handlers =
    identity (Handlers (\_ -> 1))


count : Int
count =
    case handlers of
        Handlers _ ->
            1"#,
    );
}

// ---- C-2: accepted twins ---------------------------------------------------

#[test]
fn concrete_ref_caf_is_accepted() {
    assert_accepts(
        r#"shared : Result Error (Sync.Ref (List Int))
shared =
    Task.run (Sync.newRef [])


useIt : Task Error ()
useIt =
    case shared of
        Ok r ->
            Sync.set [ 1 ] r

        Err e ->
            Task.fail e"#,
    );
}

#[test]
fn eta_expanded_point_free_def_is_accepted() {
    assert_accepts(
        r#"type Printer a
    = Printer (a -> String)


makePrinter : Bool -> a -> Printer a
makePrinter _ _ =
    Printer (\_ -> "x")


printer : a -> Printer a
printer x =
    makePrinter True x"#,
    );
}

#[test]
fn covariant_element_caf_is_accepted() {
    assert_accepts(
        r#"divider : Ui.Element msg
divider =
    Ui.el [] (Ui.text "-")"#,
    );
}

#[test]
fn covariant_cmd_caf_is_accepted() {
    assert_accepts(
        r#"nothing : Cmd msg
nothing =
    Cmd.batch []"#,
    );
}

#[test]
fn unannotated_covariant_caf_is_accepted() {
    assert_accepts(
        r#"nothing =
    Cmd.batch []


empties =
    List.map identity []"#,
    );
}

#[test]
fn user_covariant_type_caf_is_accepted() {
    assert_accepts(
        r#"type Tree a
    = Leaf
    | Node (Tree a) a (Tree a)


empty : Tree a
empty =
    identity Leaf"#,
    );
}

/// A syntactic value (a literal, a lambda, a constructor of values) is
/// generalised at any variance: it runs nothing.
#[test]
fn syntactic_value_caf_is_accepted_at_any_variance() {
    assert_accepts(
        r#"type Pred a
    = Pred (a -> Bool)


always : Pred a
always =
    Pred (\_ -> True)"#,
    );
}

// ---- D-ANY: `any` in a user annotation is a hole ---------------------------

/// The `any` form of C-2.
#[test]
fn any_ref_caf_left_open_is_rejected() {
    assert_rejects(
        r#"shared : Result Error (Sync.Ref (List any))
shared =
    Task.run (Sync.newRef [])


useIt : Task Error ()
useIt =
    case shared of
        Ok r ->
            Sync.set [ 1 ] r

        Err e ->
            Task.fail e"#,
        "E2012",
    );
}

/// `coerce : a -> any` was an unchecked cast: its result could be used at any
/// type. It now exports `a -> a`.
#[test]
fn any_result_is_no_longer_a_cast() {
    assert_rejects(
        r#"coerce : a -> any
coerce x =
    x


bad : Int
bad =
    String.length (coerce 5)"#,
        "E2001",
    );
}

/// `any` in an argument position is filled too: the body needs a record with
/// a `title : String` field.
#[test]
fn any_argument_is_filled_from_the_body() {
    assert_rejects(
        r#"title : any -> String
title m =
    m.title


bad : String
bad =
    title { title = 1 }"#,
        "E2001",
    );
}

#[test]
fn any_filled_correct_use_is_accepted() {
    assert_accepts(
        r#"coerce : a -> any
coerce x =
    x


good : Int
good =
    String.length (coerce "five")


title : any -> String
title m =
    m.title


good2 : String
good2 =
    title { title = "t", extra = 1 }"#,
    );
}

/// A homogeneous `List any` CAF (the real-app `routes : List any` shape) is
/// filled with its element type and stays legal.
#[test]
fn homogeneous_list_any_caf_is_accepted() {
    assert_accepts(
        r#"type Route
    = Home
    | About


routes : List any
routes =
    List.map identity [ Home, About ]


count : Int
count =
    List.length routes"#,
    );
}

/// An open `any` hole in a covariant position stays polymorphic.
#[test]
fn open_covariant_any_hole_is_accepted() {
    assert_accepts(
        r#"none : List any
none =
    List.map identity []


a : List Int
a =
    1 :: none


b : List String
b =
    "x" :: none"#,
    );
}

/// The diagnostic names the variable, where it sits, and suggests a concrete
/// type first.
#[test]
fn diagnostic_names_the_variable_and_suggests_a_concrete_type() {
    let errs = errors(&program(
        r#"shared : Result Error (Sync.Ref (List a))
shared =
    Task.run (Sync.newRef [])


useIt : Task Error ()
useIt =
    case shared of
        Ok r ->
            Sync.set [ 1 ] r

        Err e ->
            Task.fail e"#,
    ));
    let e = errs
        .iter()
        .find(|e| e.starts_with("E2012"))
        .unwrap_or_else(|| panic!("no E2012: {errs:#?}"));
    assert!(e.contains("`a` (inside `Ref`)"), "{e}");
    assert!(e.contains("Since v0.27.0"), "{e}");
    assert!(
        e.contains(
            "Fix: give it a concrete type, for example `shared : Result Error (Ref (List Int))`"
        ),
        "{e}"
    );
    assert!(
        e.trim_end()
            .ends_with("See docs/migration/v0.27.md#value-restriction"),
        "{e}"
    );
}

/// An open `any` hole names the `any` migration anchor.
#[test]
fn diagnostic_for_an_open_any_hole_links_the_any_migration_note() {
    let errs = errors(&program(
        r#"shared : Result Error (Sync.Ref (List any))
shared =
    Task.run (Sync.newRef [])


useIt : Task Error ()
useIt =
    case shared of
        Ok r ->
            Sync.set [ 1 ] r

        Err e ->
            Task.fail e"#,
    ));
    let e = errs
        .iter()
        .find(|e| e.starts_with("E2012"))
        .unwrap_or_else(|| panic!("no E2012: {errs:#?}"));
    assert!(
        e.contains("an `any` in a signature is filled from the body"),
        "{e}"
    );
    assert!(
        e.trim_end()
            .ends_with("See docs/migration/v0.27.md#any-in-annotations"),
        "{e}"
    );
}

/// A clash against a filled `any` says what changed, shows the filled type
/// and links the migration note.
#[test]
fn diagnostic_for_a_filled_any_clash_links_the_any_migration_note() {
    for body in [
        "coerce : a -> any\ncoerce x =\n    x\n\n\nbad : Int\nbad =\n    String.length (coerce 5)",
        "coerce : a -> any\ncoerce x =\n    x\n\n\nbad : String\nbad =\n    coerce 5",
    ] {
        let errs = errors(&program(body));
        let e = errs
            .iter()
            .find(|e| e.starts_with("E2001"))
            .unwrap_or_else(|| panic!("no E2001: {errs:#?}"));
        assert!(e.contains("Since v0.27.0 each `any` in a signature"), "{e}");
        assert!(e.contains("`coerce : a -> a`"), "{e}");
        assert!(
            e.trim_end()
                .ends_with("See docs/migration/v0.27.md#any-in-annotations"),
            "{e}"
        );
    }
}

// ---- the expansive-sub-expression refinement -------------------------------

/// A record of lambdas whose only application (`Task.succeed ()`) does not
/// mention `m` stays polymorphic in `m`, even where `m` is contravariant (the
/// stdlib `Std.App.noDurableWiring` shape).
#[test]
fn record_whose_expansive_parts_do_not_mention_the_variable_is_accepted() {
    assert_accepts(
        r#"type alias Wiring m =
    { setup : Task Error ()
    , persist : m -> Task Error ()
    }


wiring : Wiring m
wiring =
    { setup = Task.succeed ()
    , persist = \_ -> Task.succeed ()
    }"#,
    );
}

/// The same record shape with state in an expansive field is rejected.
#[test]
fn record_whose_expansive_part_holds_the_variable_is_rejected() {
    assert_rejects(
        r#"type alias Holder a =
    { cell : Result Error (Sync.Ref (List a))
    , label : String
    }


holder : Holder a
holder =
    { cell = Task.run (Sync.newRef [])
    , label = "x"
    }


useIt : Task Error ()
useIt =
    case holder.cell of
        Ok r ->
            Sync.set [ 1 ] r

        Err e ->
            Task.fail e"#,
        "E2012",
    );
}

/// A route list that leaves `page` open (the API-route shape) joined with the
/// page routes: `Std.App.Route` is covariant, so `page` stays polymorphic.
#[test]
fn open_route_list_joined_with_page_routes_is_accepted() {
    assert_accepts(
        r#"noRoutes : List (App.Route page)
noRoutes =
    List.filter (\_ -> True) []


both : List (App.Route Int)
both =
    List.map identity [ App.route "/" 1 ] ++ noRoutes"#,
    );
}
