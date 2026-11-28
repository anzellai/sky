//! Resolve-time reject of an UNKNOWN qualified kernel member, and the positive
//! counterpart: a real member (`List.sortWith`) resolves — ambiently AND via
//! `exposing` — with a PRECISE type from its new `.sky` Def.
//!
//! Before this change, a qualified `Mod.fn` for ANY `fn` minted an unvalidated
//! `Res::Kernel` (a `fresh_flex` type, `rt.Mod_fn` lowering). If `rt.Mod_fn` did
//! not exist, the program passed `sky check` and failed `go build` with
//! `undefined: rt.Mod_fn` — caught only by the codegen ABI guard at `[E4005]`.
//! `hir::resolve::resolve_qual_var` now validates the member against
//! `hir::kernel::KERNEL_FUNCTIONS[Mod]` (a proven superset of the runtime, per
//! the `xtask kernel-members` gate) and emits `[E1001]` at NAME RESOLUTION.

use hir::SourceDb;
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
        let skip = p.components().any(|c| {
            matches!(
                c.as_os_str().to_str(),
                Some("sky-out") | Some(".skycache") | Some(".skydeps")
            )
        });
        if skip {
            continue;
        }
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

fn load_stdlib(root: &Path) -> Vec<(String, syntax::Parse)> {
    let mut files = Vec::new();
    collect_sky(&root.join("sky-stdlib"), &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .filter(|s| !s.is_empty())
            .unwrap_or_default();
        out.push((name, parse));
    }
    out
}

/// Build stdlib + a single `Main` and return every error-severity diagnostic
/// code it produces.
fn error_codes(main_src: &str) -> Vec<String> {
    let root = repo_root();
    let stdlib = load_stdlib(&root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");
    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    let mid = db.add_module("Main", syntax::parse(main_src, base::FileId(0)));
    ty::check_modules(&db, &[mid])
        .diagnostics
        .iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .map(|d| d.code.0.clone())
        .collect()
}

fn type_errors(main_src: &str) -> usize {
    let root = repo_root();
    let stdlib = load_stdlib(&root);
    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    let mid = db.add_module("Main", syntax::parse(main_src, base::FileId(0)));
    ty::check_modules(&db, &[mid]).type_errors
}

const HEAD: &str = "module Main exposing (main)\n\
    import Sky.Core.Prelude exposing (..)\n\
    import Sky.Core.String as String\n\
    import Std.Log exposing (println)\n";

fn ambient(body: &str) -> String {
    format!("{HEAD}main =\n    {body}\n")
}

// ─────────────────────────────────────────────────────────────────────────────
// The reject — an unknown / phantom qualified kernel member is [E1001], not a
// downstream go-build failure.
// ─────────────────────────────────────────────────────────────────────────────

#[test]
fn unknown_member_list_sum_rejected_e1001() {
    let codes = error_codes(&ambient("println (String.fromInt (List.sum [ 1, 2, 3 ]))"));
    assert!(
        codes.iter().any(|c| c == "E1001"),
        "`List.sum` must be rejected at resolution with [E1001]; got {codes:?}"
    );
}

#[test]
fn unknown_member_basics_remainderby_rejected_e1001() {
    let codes = error_codes(&ambient(
        "println (String.fromInt (Basics.remainderBy 2 7))",
    ));
    assert!(
        codes.iter().any(|c| c == "E1001"),
        "`Basics.remainderBy` must be rejected with [E1001]; got {codes:?}"
    );
}

#[test]
fn phantom_member_list_parallelmap_rejected_e1001() {
    let codes = error_codes(&ambient(
        "println (String.fromInt (List.length (List.parallelMap (\\x -> x) [ 1, 2, 3 ])))",
    ));
    assert!(
        codes.iter().any(|c| c == "E1001"),
        "phantom `List.parallelMap` must be rejected with [E1001]; got {codes:?}"
    );
}

#[test]
fn phantom_member_io_readbytes_rejected_e1001() {
    let codes = error_codes(&ambient("println (Io.readBytes 10)"));
    assert!(
        codes.iter().any(|c| c == "E1001"),
        "phantom `Io.readBytes` must be rejected with [E1001]; got {codes:?}"
    );
}

// ─────────────────────────────────────────────────────────────────────────────
// The positive counterpart — `List.sortWith` is a REAL member: it must NOT be
// rejected (ambient), and via `exposing` it resolves to the `.sky` Def with a
// PRECISE type `(a -> a -> Int) -> List a -> List a`.
// ─────────────────────────────────────────────────────────────────────────────

#[test]
fn sortwith_ambient_qualified_accepts() {
    // Ambient (no import) → Res::Kernel, lenient — but MUST NOT be rejected.
    let codes = error_codes(&ambient(
        "println (String.fromInt (List.length (List.sortWith (\\a b -> a - b) [ 3, 1, 2 ])))",
    ));
    assert!(
        !codes.iter().any(|c| c == "E1001"),
        "`List.sortWith` is a real member and must NOT be rejected; got {codes:?}"
    );
}

#[test]
fn sortwith_via_exposing_accepts_and_is_precisely_typed() {
    // `exposing (sortWith)` → the `.sky` Def, precisely typed. A well-typed use
    // must ACCEPT …
    let ok = "module Main exposing (main)\n\
        import Sky.Core.Prelude exposing (..)\n\
        import Sky.Core.String as String\n\
        import Sky.Core.List exposing (sortWith)\n\
        import Std.Log exposing (println)\n\
        main =\n\
        \x20   println (String.fromInt (List.length (sortWith (\\a b -> a - b) [ 3, 1, 2 ])))\n";
    assert_eq!(
        type_errors(ok),
        0,
        "well-typed `sortWith` via exposing must be accepted"
    );

    // … and a comparator that returns a String (not Int) must be REJECTED —
    // proving the `.sky` signature's `-> Int` codomain is load-bearing (a
    // fresh_flex kernel ref would have accepted this).
    let bad = "module Main exposing (main)\n\
        import Sky.Core.Prelude exposing (..)\n\
        import Sky.Core.String as String\n\
        import Sky.Core.List exposing (sortWith)\n\
        import Std.Log exposing (println)\n\
        main =\n\
        \x20   println (String.fromInt (List.length (sortWith (\\a b -> \"x\") [ 3, 1, 2 ])))\n";
    assert!(
        type_errors(bad) > 0,
        "a String-returning comparator must be rejected by sortWith's precise `.sky` type"
    );
}

// ─────────────────────────────────────────────────────────────────────────────
// A pure-Sky member of a bare kernel qualifier's home (`Task.bestEffort`,
// `Task.unless`, `Task.toResult`, `Maybe.isJust`) type-checks with NO import,
// with the precise type of its `.sky` definition. Before the fix each was
// `[E1001] Undefined name` (`hir::implicit_qualifier_homes`).
// ─────────────────────────────────────────────────────────────────────────────

const NO_IMPORT_HEAD: &str = "module Main exposing (main)\n\
    import Std.Log exposing (println)\n";

#[test]
fn pure_sky_members_of_an_implicit_qualifier_check_with_no_import() {
    let src = format!(
        "{NO_IMPORT_HEAD}\n\
         step : Int -> Task Error (Task.Step Int Int)\n\
         step n =\n    if n >= 3 then\n        Task.succeed (Task.Done n)\n\n    else\n        Task.succeed (Task.Loop (n + 1))\n\n\
         main =\n    Task.run\n        (Task.bestEffort (println \"a\")\n            \
         |> Task.andThen (\\_ -> Task.unless (Maybe.isJust (Just 1)) (println \"b\"))\n            \
         |> Task.andThen (\\_ -> Task.toResult (Task.loop step 0))\n            \
         |> Task.andThen (\\_ -> println \"c\")\n        )\n"
    );
    let codes = error_codes(&src);
    assert!(
        codes.is_empty(),
        "no-import Task helpers must check; got {codes:?}"
    );
}

#[test]
fn pure_sky_member_with_no_import_keeps_its_precise_type() {
    // `Task.unless : Bool -> Task e () -> Task e ()`: a String condition is a
    // type error, so the member resolved to the typed `.sky` definition, not a
    // fresh flexible variable.
    let src =
        format!("{NO_IMPORT_HEAD}\nmain =\n    Task.run (Task.unless \"yes\" (println \"b\"))\n");
    assert!(
        type_errors(&src) > 0,
        "`Task.unless \"yes\"` must be a type error"
    );
}

// ─────────────────────────────────────────────────────────────────────────────
// Type names under a bare kernel qualifier with NO import. A kernel-backed type
// (no `.sky` declaration) and a type of the home still check; a type the home
// does not have is `[E1001]` (`hir` `Resolver::implicit_home_type`). Before,
// `Time.Parts` checked and named `Std.Time`'s record, and `Time.NoSuchType`
// checked as an unknown nominal type.
// ─────────────────────────────────────────────────────────────────────────────

#[test]
fn kernel_backed_and_home_types_check_with_no_import() {
    let src = format!(
        "{NO_IMPORT_HEAD}\n\
         count : Dict.Dict String Int -> Set.Set Int -> Maybe.Maybe Int -> Int\n\
         count d s m =\n    Dict.size d + Set.size s + Maybe.withDefault 0 m\n\n\
         later : Int -> Task.Task Error Int\n\
         later n =\n    Task.succeed n\n\n\
         cmd : Process.Command\n\
         cmd =\n    Process.command \"ls\"\n\n\
         main =\n    Task.run (later (count Dict.empty Set.empty (Just 1)) |> Task.andThen (\\_ -> println \"ok\"))\n"
    );
    let codes = error_codes(&src);
    assert!(
        codes.is_empty(),
        "kernel-backed and home types must check with no import; got {codes:?}"
    );
}

#[test]
fn a_type_the_implicit_home_lacks_is_e1001() {
    for ty in ["Time.Parts", "Time.NoSuchType"] {
        let src = format!(
            "{NO_IMPORT_HEAD}\nf : {ty} -> Int\nf p =\n    0\n\nmain =\n    println (String.fromInt (f 1))\n"
        );
        let codes = error_codes(&src);
        assert!(
            codes.iter().any(|c| c == "E1001"),
            "`{ty}` with no import must be E1001; got {codes:?}"
        );
    }
}

/// The Judge repro gives ONE diagnostic: the `[E1001]` with the import hint.
/// The type checker used to read the rejected `Time.Parts` as `Std.Time`'s
/// record by its bare name and added a second, wrong `[E2001]` (the call's
/// record "is missing field(s): abbreviation, …").
#[test]
fn an_unknown_qualified_type_gives_one_diagnostic() {
    for (import, ty) in [("", "Time.Parts")] {
        let src = format!(
            "{NO_IMPORT_HEAD}{import}\nf : {ty} -> Int\nf p =\n    p.weekday\n\n\
             main =\n    println (String.fromInt (f {{ year = 1 }}))\n"
        );
        let root = repo_root();
        let mut db = SourceDb::new();
        for (n, parse) in &load_stdlib(&root) {
            db.add_module(n, parse.clone());
        }
        let mid = db.add_module("Main", syntax::parse(&src, base::FileId(0)));
        let errs: Vec<(String, String)> = ty::check_modules(&db, &[mid])
            .diagnostics
            .iter()
            .filter(|d| d.severity == diagnostics::Severity::Error)
            .map(|d| (d.code.0.clone(), d.message.clone()))
            .collect();
        assert_eq!(
            errs.len(),
            1,
            "`{ty}`: exactly one error expected: {errs:?}"
        );
        assert_eq!(errs[0].0, "E1001", "`{ty}`: {errs:?}");
        if import.is_empty() {
            assert!(
                errs[0]
                    .1
                    .contains("`Parts` is in `Std.Time`: add `import Std.Time as Time`"),
                "{errs:?}"
            );
        }
    }
}

/// Errors (code, message) of a `Main` checked with the stdlib and `extra`
/// project modules.
fn errors_with(extra: &[(&str, &str)], main_src: &str) -> Vec<(String, String)> {
    let root = repo_root();
    let mut db = SourceDb::new();
    for (n, parse) in &load_stdlib(&root) {
        db.add_module(n, parse.clone());
    }
    let mut ids = Vec::new();
    for (n, src) in extra {
        ids.push(db.add_module(n, syntax::parse(src, base::FileId(0))));
    }
    ids.push(db.add_module("Main", syntax::parse(main_src, base::FileId(0))));
    ty::check_modules(&db, &ids)
        .diagnostics
        .iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .map(|d| (d.code.0.clone(), d.message.clone()))
        .collect()
}

/// The Judge repro: `Lib.Db` exports only `Conn`, and `Main` names five
/// kernel-implicit types through its alias. Each is ONE `[E1001]`, with no
/// second error from a type picked by its bare name.
#[test]
fn kernel_implicit_types_under_a_user_alias_are_e1001_with_no_cascade() {
    let lib = "module Lib.Db exposing (Conn)\n\ntype alias Conn =\n    { n : Int }\n";
    let main = format!(
        "{NO_IMPORT_HEAD}import Lib.Db as L\n\n\
         g : L.Error -> L.Db -> L.Session -> L.Cmd Int -> Int\n\
         g e d s c =\n    0\n\n\
         f : L.Request -> Int\nf r =\n    r.method\n\n\
         main =\n    println \"ok\"\n"
    );
    let errs = errors_with(&[("Lib.Db", lib)], &main);
    assert_eq!(errs.len(), 5, "{errs:?}");
    for name in ["Error", "Db", "Session", "Cmd", "Request"] {
        assert!(
            errs.iter().any(|(c, m)| c == "E1001"
                && m.contains(&format!("L.{name}"))
                && m.contains(&format!("does not export a type `{name}`"))),
            "`L.{name}`: {errs:?}"
        );
    }
}

/// A qualified kernel type never names a same-named local type by its bare
/// name: with a local `type alias Value = { n : Int }`, `JsonEnc.Value` is the
/// JSON value, so reading the field `n` from it is a type error.
#[test]
fn a_qualified_kernel_type_is_not_captured_by_a_local_type() {
    let main = format!(
        "{NO_IMPORT_HEAD}\ntype alias Value =\n    {{ n : Int }}\n\n\
         f : JsonEnc.Value -> Int\nf v =\n    v.n\n\n\
         main =\n    println (String.fromInt 1)\n"
    );
    let errs = errors_with(&[], &main);
    assert!(
        errs.iter().any(|(c, _)| c == "E2001"),
        "`JsonEnc.Value` must not be the local record: {errs:?}"
    );
}

/// The accepted unexported stdlib type (`Ui.Html`, a warning) is `Std.Html`'s
/// `Html`, not a same-named local type.
#[test]
fn an_unexported_stdlib_type_is_its_one_home_not_a_local_type() {
    let main = format!(
        "{NO_IMPORT_HEAD}import Std.Ui as Ui\n\ntype alias Html a =\n    {{ n : a }}\n\n\
         f : Ui.Html Int -> Int\nf h =\n    h.n\n\n\
         main =\n    println (String.fromInt 1)\n"
    );
    let errs = errors_with(&[], &main);
    assert!(
        errs.iter().any(|(c, _)| c == "E2001"),
        "`Ui.Html` must be Std.Html's Html: {errs:?}"
    );
}
