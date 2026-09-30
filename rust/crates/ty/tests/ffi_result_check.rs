//! The checker enforces the `Result Error a` return of a Go-FFI binding.
//!
//! Before this change every `Res::Foreign` reference inferred to a fresh
//! flexible type variable, so `probe : Int` / `probe = Pkg.read "x" 0 0`
//! type-checked, the lowering narrowed a `SkyResult` with `rt.AsInt`, and the
//! program crashed at run time. The pinned `skyType` now reaches EVERY inference
//! run through the type database (`SkyDb::ffi_fn`), so the direct misuse, the
//! same misuse behind an unannotated wrapper, and the let-bound misuse are all
//! rejected, while the legitimate shapes a real FFI program uses stay accepted.
//!
//! Each program pins its bindings with `-- ffi: Pkg name : skyType` directives
//! (`ty::ffi_sig::surface_from_directives`), the same mechanism the reject
//! corpus uses, and is checked by `ty::reject_corpus::evaluate_modules` — the
//! reject criterion itself, so accept and reject here mean what `sky check`
//! means.

use std::path::PathBuf;
use std::sync::OnceLock;
use ty::reject_corpus::{evaluate_modules, load_stdlib, Verdict};

fn stdlib() -> &'static Vec<(String, syntax::Parse)> {
    static S: OnceLock<Vec<(String, syntax::Parse)>> = OnceLock::new();
    S.get_or_init(|| {
        let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        while !dir.join("sky-stdlib").is_dir() {
            assert!(dir.pop(), "no sky-stdlib ancestor");
        }
        load_stdlib(&dir)
    })
}

const HEADER: &str = "\
-- ffi: Pkg read : String -> Int -> Int -> Result Error (String, Int)
-- ffi: Pkg repeat : String -> Int -> Result Error String
-- ffi: Pkg encodedLen : Int -> Result Error Int
-- ffi: Pkg newRouter : () -> Result Error go@Pkg.Router
-- ffi: Pkg handleFunc : go@Pkg.Router -> String -> (go@Pkg.ResponseWriter -> go@Pkg.Request -> ()) -> Result Error go@Pkg.Route
-- ffi: Pkg onTick : go@Pkg.Router -> (Int -> Int) -> Result Error ()
-- ffi: Pkg onDone : go@Pkg.Router -> ( -> ()) -> Result Error ()
-- ffi: Pkg join : List String -> String -> Result Error String
-- ffi: Pkg lookupEnv : String -> Result Error (Maybe String)
-- ffi: Pkg ptrStr : Bool -> Result Error (Maybe String)
-- ffi: Pkg intMap : () -> Result Error (Dict Int String)
-- ffi: Pkg apply : (Int -> String) -> Result Error String
-- ffi: Pkg writeTo : goi@Io.Writer -> String -> Result Error ()
-- ffi: Pkg describeValue : any -> Result Error String
-- ffi: Pkg noType/2 :
-- ffi: Github.Com.Google.Uuid newString : () -> Result Error String
-- ffi: Pkg defaultClient : () -> Result Error go@Pkg.Client
-- ffi: Pkg setTimeoutMs : Int -> go@Pkg.Client -> Result Error go@Pkg.Client
-- ffi: Pkg describe : go@Pkg.Client -> Result Error String
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Sky.Core.Result as Result
import Sky.Core.String as String
import Std.Log exposing (println)
import Pkg
";

fn check(body: &str) -> Verdict {
    let src = format!("{HEADER}\n{body}");
    evaluate_modules("case", &[(String::new(), src)], stdlib())
}

fn assert_accepts(label: &str, body: &str) {
    let v = check(body);
    assert!(
        !v.rejected(),
        "{label}: must be ACCEPTED, got {} ({:?})",
        v.first_msg,
        v.observed_codes
    );
}

fn assert_rejects(label: &str, body: &str, code: &str) {
    let v = check(body);
    assert!(v.rejected(), "{label}: must be REJECTED, was accepted");
    assert!(
        v.observed_codes.iter().any(|c| c == code),
        "{label}: expected [{code}], observed {:?} ({})",
        v.observed_codes,
        v.first_msg
    );
}

// ---- rejects --------------------------------------------------------------------

#[test]
fn direct_misuse_of_an_ffi_result_is_rejected() {
    assert_rejects(
        "probe : Int = Pkg.read …",
        "probe : Int\nprobe =\n    Pkg.read \"x\" 0 0\n\n\nmain =\n    println (String.fromInt probe)\n",
        "E2001",
    );
}

#[test]
fn misuse_behind_an_unannotated_wrapper_is_rejected() {
    assert_rejects(
        "readIt wrapper",
        "readIt s =\n    Pkg.read s 0 0\n\n\nprobe : Int\nprobe =\n    readIt \"x\"\n\n\nmain =\n    println (String.fromInt probe)\n",
        "E2001",
    );
}

#[test]
fn let_bound_result_used_as_its_payload_is_rejected() {
    assert_rejects(
        "let n = Pkg.encodedLen 3",
        "main =\n    let\n        n =\n            Pkg.encodedLen 3\n    in\n    println (String.fromInt n)\n",
        "E2001",
    );
}

#[test]
fn payload_primitive_is_enforced() {
    assert_rejects(
        "String payload used as Int",
        "main =\n    case Pkg.repeat \"a\" 1 of\n        Ok n ->\n            println (String.fromInt (n + 1))\n\n        Err _ ->\n            println \"err\"\n",
        "E2001",
    );
}

#[test]
fn a_missing_sky_type_still_enforces_the_result() {
    assert_rejects(
        "noType arity fallback",
        "probe : Int\nprobe =\n    Pkg.noType 1 2\n\n\nmain =\n    println (String.fromInt probe)\n",
        "E2001",
    );
}

#[test]
fn over_application_is_rejected() {
    let v = check("main =\n    println (Result.withDefault \"\" (Pkg.repeat \"a\" 1 2))\n");
    assert!(v.rejected(), "over-applied FFI call must be rejected");
}

#[test]
fn sky_ffi_call_is_not_an_app_escape_hatch() {
    assert_rejects(
        "Ffi.callPure bypass",
        "import Sky.Ffi as Ffi\n\n\nprobe : Int\nprobe =\n    Ffi.callPure \"Go_Pkg_read\" []\n\n\nmain =\n    println (String.fromInt probe)\n",
        "E1011",
    );
    assert_rejects(
        "Ffi.call bypass",
        "import Sky.Ffi as Ffi\n\n\nprobe : Int\nprobe =\n    Ffi.call \"Go_Pkg_read\" []\n\n\nmain =\n    println (String.fromInt probe)\n",
        "E1011",
    );
    assert_rejects(
        "Ffi.callTask bypass",
        "import Sky.Ffi as Ffi\n\n\nmain =\n    let\n        _ =\n            Ffi.callTask \"Go_Pkg_read\" []\n    in\n    println \"x\"\n",
        "E1011",
    );
}

// ---- accepts --------------------------------------------------------------------

#[test]
fn result_pipelines_are_accepted() {
    assert_accepts(
        "case / withDefault / andThen / map",
        "main =\n    let\n        a =\n            case Pkg.read \"x\" 0 0 of\n                Ok ( s, n ) ->\n                    s ++ String.fromInt n\n\n                Err _ ->\n                    \"err\"\n\n        b =\n            Result.withDefault \"\" (Pkg.repeat \"ab\" 2)\n\n        c =\n            Pkg.repeat \"a\" 1\n                |> Result.andThen (\\s -> Pkg.repeat s 2)\n                |> Result.withDefault \"\"\n\n        d =\n            Pkg.encodedLen 3\n                |> Result.map (\\n -> n + 1)\n                |> Result.withDefault 0\n    in\n    println (a ++ b ++ c ++ String.fromInt d)\n",
    );
}

#[test]
fn partial_application_and_ffi_values_are_accepted() {
    assert_accepts(
        "partial application + List.map",
        "main =\n    let\n        rep =\n            Pkg.repeat \"ab\"\n\n        xs =\n            List.map rep [ 1, 2 ]\n\n        ys =\n            List.map Pkg.encodedLen [ 1, 2, 3 ]\n\n        zs =\n            Ok 4 |> Result.andThen Pkg.encodedLen\n    in\n    println (String.fromInt (List.length xs + List.length ys + Result.withDefault 0 zs))\n",
    );
}

#[test]
fn callbacks_and_opaque_values_are_accepted() {
    assert_accepts(
        "callbacks returning values, opaque in a Value slot",
        "handler w r =\n    ()\n\n\nkeep : Pkg.Router -> Pkg.Router\nkeep v =\n    v\n\n\nmain =\n    case Pkg.newRouter () of\n        Ok router ->\n            let\n                _ =\n                    Pkg.handleFunc (keep router) \"/\" handler\n\n                _ =\n                    Pkg.onTick router (\\n -> n + 1)\n\n                _ =\n                    Pkg.onDone router (\\_ -> ())\n            in\n            println \"ok\"\n\n        Err _ ->\n            println \"err\"\n",
    );
}

#[test]
fn variadic_list_and_comma_ok_are_accepted() {
    assert_accepts(
        "List arg, Maybe payload",
        "main =\n    let\n        j =\n            Pkg.join [ \"a\", \"b\" ] \",\" |> Result.withDefault \"\"\n\n        m =\n            case Pkg.lookupEnv \"HOME\" of\n                Ok (Just v) ->\n                    v\n\n                Ok Nothing ->\n                    \"\"\n\n                Err _ ->\n                    \"\"\n    in\n    println (j ++ m)\n",
    );
}

#[test]
fn aliased_and_exposing_imports_are_typed_too() {
    let src = "\
-- ffi: Github.Com.Google.Uuid newString : () -> Result Error String
-- ffi: Pkg repeat : String -> Int -> Result Error String
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Result as Result
import Std.Log exposing (println)
import Github.Com.Google.Uuid as Uuid
import Pkg exposing (..)


main =
    let
        a =
            case Uuid.newString () of
                Ok s ->
                    s

                Err _ ->
                    \"x\"

        b =
            Result.withDefault \"\" (repeat \"a\" 2)
    in
    println (a ++ b)
";
    let v = evaluate_modules("alias", &[(String::new(), src.to_string())], stdlib());
    assert!(!v.rejected(), "aliased FFI import: {}", v.first_msg);
    // …and the alias does not hide the Result.
    let bad = src.replace(
        "case Uuid.newString () of\n                Ok s ->\n                    s\n\n                Err _ ->\n                    \"x\"",
        "Uuid.newString ()",
    );
    let v = evaluate_modules("alias-bad", &[(String::new(), bad)], stdlib());
    assert!(
        v.observed_codes.iter().any(|c| c == "E2001"),
        "an aliased FFI Result used as String must be rejected: {:?}",
        v.observed_codes
    );
}

#[test]
fn a_package_without_a_loaded_surface_stays_lenient() {
    // No `-- ffi:` directive for `Other`: the surface is absent, lowering
    // reports the missing wrapper (`sky install`), and the checker must not
    // invent an error of its own.
    let src = "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Std.Log exposing (println)\nimport Other\n\n\nmain =\n    println (Other.thing 1)\n";
    let v = evaluate_modules("nosurface", &[(String::new(), src.to_string())], stdlib());
    assert!(!v.rejected(), "{}", v.first_msg);
}

/// The FFI error type is `Sky.Core.Error.Error`, QUALIFIED. A dependency that
/// declares its own `type Error` (app code cannot — `[E1004]` protects the
/// Prelude name — but a `.skydeps` module is not checked) must not satisfy it:
/// a bare `Error` would unify with any same-named type (`nominal::same`).
#[test]
fn a_foreign_error_is_not_a_same_named_user_type() {
    let lib = "module Lib exposing (Error(..), describe)\n\n\ntype Error\n    = Oops\n\n\ndescribe : Error -> String\ndescribe e =\n    case e of\n        Oops ->\n            \"oops\"\n";
    let main = |handler: &str| {
        format!(
            "-- ffi: Pkg repeat : String -> Int -> Result Error String\n\
             module Main exposing (main)\n\n\
             import Sky.Core.Prelude exposing (..)\n\
             import Std.Log exposing (println)\n\
             import Lib\n\
             import Pkg\n\n\n\
             main =\n    case Pkg.repeat \"a\" 1 of\n        Ok s ->\n            println s\n\n        \
             Err e ->\n            println ({handler} e)\n"
        )
    };
    let run = |handler: &str| -> (usize, Vec<String>) {
        let mut db = hir::SourceDb::new();
        for (n, p) in stdlib() {
            db.add_module(n, p.clone());
        }
        let src = main(handler);
        db.set_ffi_surface(std::sync::Arc::new(ty::ffi_sig::surface_from_directives(
            &src,
        )));
        db.add_module("Lib", syntax::parse(lib, base::FileId(0)));
        let m = db.add_module("Main", syntax::parse(&src, base::FileId(0)));
        let out = ty::check_modules(&db, &[m]);
        let msgs = out
            .diagnostics
            .iter()
            .filter(|d| d.severity == diagnostics::Severity::Error)
            .map(|d| format!("[{}] {}", d.code.0, d.message))
            .collect();
        (out.type_errors + out.name_errors, msgs)
    };
    // The twin: the stdlib's own renderer accepts the FFI error.
    let (n, msgs) = run("errorToString");
    assert_eq!(n, 0, "the FFI error IS a Sky.Core.Error.Error: {msgs:?}");
    // The dependency's same-named `Error` does not.
    let (n, msgs) = run("Lib.describe");
    assert!(
        n > 0 && msgs.iter().any(|m| m.contains("[E2001]")),
        "a dependency's own `Error` must not unify with the FFI error: {msgs:?}"
    );
}

/// The `[E2001]` for an ignored FFI Result carries the migration hint.
#[test]
fn the_type_error_carries_the_ffi_result_hint() {
    let src = format!(
        "{HEADER}\nprobe : Int\nprobe =\n    Pkg.read \"x\" 0 0\n\n\nmain =\n    println (String.fromInt probe)\n"
    );
    let mut db = hir::SourceDb::new();
    for (n, p) in stdlib() {
        db.add_module(n, p.clone());
    }
    db.set_ffi_surface(std::sync::Arc::new(ty::ffi_sig::surface_from_directives(
        &src,
    )));
    let m = db.add_module("Main", syntax::parse(&src, base::FileId(0)));
    let out = ty::check_modules(&db, &[m]);
    let d = out
        .diagnostics
        .iter()
        .find(|d| d.code.0 == "E2001")
        .expect("an E2001");
    let hint = d.suggestion.clone().unwrap_or_default();
    assert!(
        hint.contains("Result Error a") && hint.contains("Result.withDefault"),
        "hint: {hint:?}; message: {}",
        d.message
    );
}

// ---- a Go FFI type has its own identity (v0.27.0) ------------------------------
//
// A type named through an FFI package (`Pkg.Client`) used to fall back to its
// bare name, where the app's own `type alias Client` captured it: an app record
// passed where the Go value belongs type-checked, and the Go value then
// panicked at the record's narrowing.

const OWN_CLIENT: &str = "type alias Client =\n    { name : String }\n\n\n";

#[test]
fn an_app_record_is_not_a_go_type_of_the_same_name() {
    assert_rejects(
        "useIt { name = \"x\" } where Pkg.Client is expected",
        &format!(
            "{OWN_CLIENT}useIt : Pkg.Client -> String\nuseIt _ =\n    \"ok\"\n\n\nmain =\n    println (useIt {{ name = \"x\" }})\n"
        ),
        "E2001",
    );
}

#[test]
fn a_go_value_flows_between_ffi_calls_and_an_annotated_helper() {
    assert_accepts(
        "the Go client passed from one FFI call to another",
        &format!(
            "{OWN_CLIENT}useIt : Pkg.Client -> Result Error String\nuseIt c =\n    Pkg.setTimeoutMs 50 c |> Result.andThen Pkg.describe\n\n\nmine : Client\nmine =\n    {{ name = \"x\" }}\n\n\nmain =\n    case Pkg.defaultClient () |> Result.andThen useIt of\n        Ok s ->\n            println (s ++ mine.name)\n\n        Err _ ->\n            println \"err\"\n"
        ),
    );
}

// ---- surface format 3 (v0.27.0) ------------------------------------------------

/// C-6: an opaque Go value is its own type, never an Int.
#[test]
fn an_opaque_go_value_is_not_an_int() {
    assert_rejects(
        "opaque used as Int",
        "asInt : Int\nasInt =\n    case Pkg.newRouter () of\n        Ok r ->\n            r\n\n        Err _ ->\n            0\n\n\nmain =\n    println (String.fromInt asInt)\n",
        "E2001",
    );
}

/// C-12: the callback's result is typed from the pin.
#[test]
fn a_callback_of_the_wrong_result_type_is_rejected() {
    assert_rejects(
        "apply (\\n -> n * 2) where func(int) string is wanted",
        "main =\n    println (Result.withDefault \"\" (Pkg.apply (\\n -> n * 2)))\n",
        "E2001",
    );
    assert_accepts(
        "apply (\\n -> String.fromInt n)",
        "main =\n    println (Result.withDefault \"\" (Pkg.apply (\\n -> String.fromInt n)))\n",
    );
}

/// C-4 / C-5: a pointer is a Maybe; a map keeps its key type.
#[test]
fn pointers_and_map_keys_are_typed() {
    assert_rejects(
        "a *string read as String",
        "s : String\ns =\n    case Pkg.ptrStr True of\n        Ok v ->\n            v\n\n        Err _ ->\n            \"\"\n\n\nmain =\n    println s\n",
        "E2001",
    );
    assert_accepts(
        "Maybe String and Dict Int",
        "main =\n    let\n        a =\n            case Pkg.ptrStr True of\n                Ok (Just v) ->\n                    v\n\n                _ ->\n                    \"\"\n\n        n =\n            Pkg.intMap () |> Result.map (\\d -> List.length [ d ]) |> Result.withDefault 0\n    in\n    println (a ++ String.fromInt n)\n",
    );
}

/// [E2013]: a Sky value is never a Go interface; an empty interface accepts
/// anything.
#[test]
fn a_sky_value_for_a_go_interface_is_rejected() {
    let v = check("main =\n    println (Result.withDefault \"\" (Pkg.writeTo \"x\" \"y\" |> Result.map (\\_ -> \"ok\")))\n");
    assert!(v.rejected(), "a String is not an io.Writer");
    assert!(
        v.observed_codes.iter().any(|c| c == "E2013"),
        "{:?} {}",
        v.observed_codes,
        v.first_msg
    );
    let src = format!(
        "{HEADER}\nmain =\n    println (Result.withDefault \"\" (Pkg.writeTo \"x\" \"y\" |> Result.map (\\_ -> \"ok\")))\n"
    );
    let mut db = hir::SourceDb::new();
    for (n, p) in stdlib() {
        db.add_module(n, p.clone());
    }
    db.set_ffi_surface(std::sync::Arc::new(ty::ffi_sig::surface_from_directives(
        &src,
    )));
    let m = db.add_module("Main", syntax::parse(&src, base::FileId(0)));
    let out = ty::check_modules(&db, &[m]);
    let d = out
        .diagnostics
        .iter()
        .find(|d| d.code.0 == "E2013")
        .expect("an E2013");
    assert!(
        d.message.contains("v0.27.0")
            && d.message.contains("Fix:")
            && d.message
                .ends_with("see docs/migration/v0.27.md#ffi-go-interface-params"),
        "{}",
        d.message
    );
    assert_accepts(
        "a Go value for a Go interface, a String for an empty one",
        "main =\n    let\n        a =\n            Pkg.newRouter () |> Result.andThen (\\r -> Pkg.writeTo r \"x\") |> Result.map (\\_ -> \"\") |> Result.withDefault \"\"\n\n        b =\n            Pkg.describeValue \"s\" |> Result.withDefault \"\"\n    in\n    println (a ++ b)\n",
    );
}

/// Every format-3 type error names the v0.27.0 change, the fix, and its
/// migration anchor.
#[test]
fn format3_type_errors_carry_the_migration_hint() {
    let hint_of = |body: &str| -> String {
        let src = format!("{HEADER}\n{body}");
        let mut db = hir::SourceDb::new();
        for (n, p) in stdlib() {
            db.add_module(n, p.clone());
        }
        db.set_ffi_surface(std::sync::Arc::new(ty::ffi_sig::surface_from_directives(
            &src,
        )));
        let m = db.add_module("Main", syntax::parse(&src, base::FileId(0)));
        let out = ty::check_modules(&db, &[m]);
        let d = out
            .diagnostics
            .iter()
            .find(|d| d.code.0 == "E2001")
            .unwrap_or_else(|| panic!("an E2001 for:\n{body}"));
        d.suggestion.clone().unwrap_or_default()
    };
    for (body, anchor) in [
        (
            "asInt : Int\nasInt =\n    case Pkg.newRouter () of\n        Ok r ->\n            r\n\n        Err _ ->\n            0\n\n\nmain =\n    println (String.fromInt asInt)\n",
            "ffi-opaque-go-types",
        ),
        (
            "main =\n    println (Result.withDefault \"\" (Pkg.apply (\\n -> n * 2)))\n",
            "ffi-callback-result",
        ),
        (
            "s : String\ns =\n    case Pkg.ptrStr True of\n        Ok v ->\n            v\n\n        Err _ ->\n            \"\"\n\n\nmain =\n    println s\n",
            "ffi-pointer-is-maybe",
        ),
        (
            "d : Result Error (Dict String String)\nd =\n    Pkg.intMap ()\n\n\nmain =\n    println \"x\"\n",
            "ffi-map-keys",
        ),
    ] {
        let hint = hint_of(body);
        assert!(
            hint.contains("since v0.27.0")
                && hint.contains("Fix:")
                && hint.ends_with(&format!("see docs/migration/v0.27.md#{anchor}")),
            "{anchor}: {hint:?}"
        );
    }
}
