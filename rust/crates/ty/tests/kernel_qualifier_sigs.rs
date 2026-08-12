//! Reject-lock for the kernel-qualifier members that gained a Sky signature.
//!
//! A kernel-qualifier reference (`String.left`, `Time.parse`, `Task.map2` — no
//! import needed, `hir::KERNEL_MODULES` resolves them) is typed by `ty::sig`
//! pass 2 ONLY if some `.sky` module mapped to that pseudo declares `name :
//! Type`. Without a declaration `Infer::infer_res` returns a bare flex var, the
//! `[E2007]` arity gate self-disables on `Ty::Var`, and the call is unchecked at
//! ANY arity — the `Path.join "a" "b"` defect, which passed `sky check` and then
//! handed the user a raw `go build` error.
//!
//! `lower::Ctx::reject_over_application` catches the arity half one stage later,
//! but from LOWERING: no source span, and nothing at all about argument or
//! result TYPES. So these tests assert at the TYPE layer, where the diagnostic
//! carries a span, and each member gets BOTH halves:
//!
//!   * MISUSE (wrong arity, or a wrong argument/result type) is REJECTED, and
//!   * a VALID use still ACCEPTS — the accept-parity guard, because narrowing a
//!     member that was previously callable at any arity is exactly how a
//!     checker change breaks a real app (#164).
//!
//! Deleting any one of the signatures in `sky-stdlib/` turns its `rejected_*`
//! case green-to-red here; that is the mutation these tests are proved by.

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
        if p.components().any(|c| {
            matches!(
                c.as_os_str().to_str(),
                Some("sky-out") | Some(".skycache") | Some(".skydeps")
            )
        }) {
            continue;
        }
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

fn type_errors_for_main(main_src: &str) -> usize {
    let root = repo_root();
    let mut files = Vec::new();
    collect_sky(&root.join("sky-stdlib"), &mut files);
    assert!(!files.is_empty(), "stdlib failed to load");
    let mut db = SourceDb::new();
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
        db.add_module(&name, parse);
    }
    let mid = db.add_module("Main", syntax::parse(main_src, base::FileId(0)));
    ty::check_modules(&db, &[mid]).type_errors
}

/// Deliberately imports NOTHING but the prelude: every member under test is
/// reached through the kernel-QUALIFIER surface, which is the surface that had
/// no types.
const PRELUDE: &str = "module Main exposing (main)\n\
    import Sky.Core.Prelude exposing (..)\n\
    import Std.Log exposing (println)\n";

fn src(body: &str) -> String {
    format!("{PRELUDE}main =\n    {body}\n")
}

#[track_caller]
fn assert_rejected(what: &str, body: &str) {
    let errs = type_errors_for_main(&src(body));
    assert!(
        errs > 0,
        "UNCHECKED HOLE ({what}) — `{body}` was ACCEPTED with 0 type errors. \
         A kernel-qualifier member with no Sky signature infers a flex var, \
         which absorbs any number of arrows and any argument type."
    );
}

#[track_caller]
fn assert_accepted(what: &str, body: &str) {
    let errs = type_errors_for_main(&src(body));
    assert_eq!(
        errs, 0,
        "OVER-EAGER ({what}) — the valid use `{body}` was REJECTED with {errs} \
         type error(s). Every member here was callable at any arity before it \
         was typed; narrowing one past its real uses is the #164 failure mode."
    );
}

// ---- String: count-first slicing ----

#[test]
fn rejected_string_left_over_applied() {
    assert_rejected("String.left", "println (String.left 3 \"abcdef\" \"extra\")");
}

#[test]
fn rejected_string_left_argument_order_swapped() {
    // `String.left "abcdef" 3` is the order users guess; the runtime is
    // `rt.String_left(n, s)`, so it must clash.
    assert_rejected("String.left order", "println (String.left \"abcdef\" 3)");
}

#[test]
fn rejected_string_graphemes_result_is_not_a_string() {
    // `graphemes` returns a COUNT despite the plural name. A `String` result
    // would be the misreading the signature exists to prevent.
    assert_rejected(
        "String.graphemes",
        "println (String.join \",\" [ String.graphemes \"abc\" ])",
    );
}

#[test]
fn rejected_string_isvalid_result_is_not_a_string() {
    assert_rejected("String.isValid", "println (String.isValid \"ok\")");
}

#[test]
fn rejected_string_tobytes_element_is_not_a_string() {
    assert_rejected(
        "String.toBytes",
        "println (String.join \",\" (String.toBytes \"hey\"))",
    );
}

#[test]
fn accepted_string_members_valid_uses() {
    assert_accepted("String.left", "println (String.left 3 \"abcdef\")");
    assert_accepted("String.right", "println (String.right 2 \"abcdef\")");
    assert_accepted("String.truncate", "println (String.truncate 3 \"abcdef\")");
    assert_accepted("String.ellipsize", "println (String.ellipsize 3 \"abcdef\")");
    assert_accepted("String.htmlEscape", "println (String.htmlEscape \"<b>\")");
    assert_accepted("String.slugify", "println (String.slugify \"A B\")");
    assert_accepted("String.normalize", "println (String.normalize \"a\")");
    assert_accepted("String.normalizeNFD", "println (String.normalizeNFD \"a\")");
    assert_accepted(
        "String.graphemes",
        "println (String.fromInt (String.graphemes \"abc\"))",
    );
    assert_accepted(
        "String.isValid",
        "println (Basics.toString (String.isValid \"ok\"))",
    );
    assert_accepted(
        "String.fromBytes/toBytes",
        "println (String.fromBytes (String.toBytes \"hey\"))",
    );
    assert_accepted(
        "String.toChar",
        "println (String.fromChar (String.toChar \"abc\"))",
    );
}

// ---- Time: pure, fallible, Result-not-Task ----

#[test]
fn rejected_time_parse_over_applied() {
    assert_rejected(
        "Time.parse",
        "println (Basics.toString (Time.parse \"2006-01-02\" \"2025-01-01\" \"extra\"))",
    );
}

#[test]
fn rejected_time_parseiso_result_payload_is_not_a_string() {
    // The payload is Unix millis (`Int`), not the formatted string.
    assert_rejected(
        "Time.parseISO8601",
        "println (Result.withDefault \"\" (Time.parseISO8601 \"2025-01-01T00:00:00Z\"))",
    );
}

#[test]
fn accepted_time_parsers_valid_uses() {
    assert_accepted(
        "Time.parse",
        "println (String.fromInt (Result.withDefault 0 (Time.parse \"2006-01-02\" \"2025-01-01\")))",
    );
    assert_accepted(
        "Time.parseISO8601",
        "println (String.fromInt (Result.withDefault 0 (Time.parseISO8601 \"2025-01-01T00:00:00Z\")))",
    );
}

// ---- JsonDec.map5 ----

#[test]
fn rejected_jsondec_map5_over_applied() {
    assert_rejected(
        "JsonDec.map5",
        "println (Basics.toString (JsonDec.map5 (\\a b c d e -> a) \
         JsonDec.string JsonDec.string JsonDec.string JsonDec.string \
         JsonDec.string JsonDec.string))",
    );
}

#[test]
fn accepted_jsondec_map5_valid_use() {
    assert_accepted(
        "JsonDec.map5",
        "println (Result.withDefault \"\" (JsonDec.decodeString \
         (JsonDec.map5 (\\a b c d e -> a ++ b ++ c ++ d ++ e) \
          (JsonDec.field \"a\" JsonDec.string) (JsonDec.field \"b\" JsonDec.string) \
          (JsonDec.field \"c\" JsonDec.string) (JsonDec.field \"d\" JsonDec.string) \
          (JsonDec.field \"e\" JsonDec.string)) \"{}\"))",
    );
}

// ---- Server: request accessors ----

#[test]
fn rejected_server_formvalue_is_not_a_maybe() {
    // `rt.Server_formValue` returns a bare string (`""` when absent), so a
    // `Maybe`-shaped use must clash. `docs/skyauth/overview.md` carried exactly
    // this mistake and compiled, because the member had no signature.
    assert_rejected(
        "Server.formValue",
        "println (Maybe.withDefault \"\" (Server.formValue \"email\" \
         (Server.text \"x\")))",
    );
}

#[test]
fn rejected_server_method_over_applied() {
    assert_rejected(
        "Server.method",
        "println (Server.method (Server.text \"x\") \"extra\")",
    );
}

// ---- Task applicatives ----

#[test]
fn rejected_task_map2_over_applied() {
    assert_rejected(
        "Task.map2",
        "println (Basics.toString (Task.run (Task.map2 (\\a b -> a) \
         (Task.succeed 1) (Task.succeed 2) (Task.succeed 3))))",
    );
}

#[test]
fn rejected_task_andmap_argument_order_swapped() {
    // `andMap` is VALUE first, FUNCTION second (matching `Maybe.andMap ma mfn`
    // and `Result.andMap ra rfn`). The swapped order is the natural applicative
    // reading, it used to type-check, and it produces the wrong answer.
    assert_rejected(
        "Task.andMap order",
        "println (String.fromInt (Result.withDefault 0 (Task.run \
         (Task.andMap (Task.succeed (\\n -> n + 1)) (Task.succeed 7)))))",
    );
}

#[test]
fn accepted_task_applicatives_valid_uses() {
    assert_accepted(
        "Task.map2",
        "println (String.fromInt (Result.withDefault 0 (Task.run \
         (Task.map2 (\\a b -> a + b) (Task.succeed 1) (Task.succeed 2)))))",
    );
    assert_accepted(
        "Task.map3",
        "println (Result.withDefault \"\" (Task.run \
         (Task.map3 (\\a b c -> a ++ b ++ c) (Task.succeed \"x\") \
          (Task.succeed \"y\") (Task.succeed \"z\"))))",
    );
    assert_accepted(
        "Task.andMap",
        "println (String.fromInt (Result.withDefault 0 (Task.run \
         (Task.andMap (Task.succeed 7) (Task.succeed (\\n -> n + 1))))))",
    );
}

// ---- Db.unsafeFindWhere ----
//
// This was `Db.findWhere` until that name was UN-declared. Audit P1-3 renamed
// it to `unsafeFindWhere`, and giving `findWhere` a Sky signature required
// exposing it from `Std/Db.sky` — putting a raw `WHERE`-concatenating function
// back in the public API under a name that does not warn. The signature went;
// the arity check moves to the name the audit chose to keep.
//
// `findWhere` itself is not left unguarded: it is untyped, so an over-applied
// call is caught by `lower::reject_over_application` rather than by the type
// layer — a plainer diagnostic with no span, but still `sky check`, never a raw
// `go build` error.

#[test]
fn rejected_db_unsafe_findwhere_over_applied() {
    assert_rejected(
        "Db.unsafeFindWhere",
        "println (Basics.toString (Db.unsafeFindWhere (Db.connect ()) \"post\" \
         \"id = ?\" [ 1 ] \"extra\"))",
    );
}
