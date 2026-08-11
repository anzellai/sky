//! The memoised-CAF stale-read lint (`lower/src/lower.rs`, "is memoised to a
//! SINGLE value") — pinned in BOTH directions.
//!
//! The lint exists for a real production incident: a top-level
//! `posts = Task.run (Store.all db posts)` freezes the row set for the life of
//! the process, so a row written later never appears. That true positive must
//! survive every change here.
//!
//! It also USED to fire on the documented Sky.Live idiom:
//!
//! ```elm
//! apiRoutes =
//!     [ Live.api "GET /admin/login" GhAuth.handleLogin
//!     , Live.api "GET /healthz" handleHealthz
//!     ]
//! ```
//!
//! Nothing in that list is stale-able — it is a table of route REGISTRATIONS,
//! built once and handed to `Live.app`; each handler does its reads per request.
//! `compute_def_effect` nonetheless treated EVERY `Res::Def` reference as an
//! effect edge, so each handler's per-request `Db.query` propagated into the
//! table, whose `List any` result reads as stale-able data → warning. A lint
//! that cries wolf on the documented idiom trains users to ignore it, taking
//! the true positive down with it.
//!
//! The rule now pinned (`def_reference_is_forced`): an effect propagates only
//! through a reference that evaluating the body FORCES — an application (a
//! `Call` callee, the function side of `|>` / `<|`, an operand of `>>` / `<<`)
//! or a mention of a zero-arity def whose value is not itself a function. A
//! handler passed as a VALUE is a computation stored for the callee to run
//! later; it reads nothing now.
//!
//! Every test below drives the real pipeline (parse → resolve → infer → lower)
//! over the real stdlib and asserts on the emitted warning list.

use project::emit_example_warnings;
use std::path::PathBuf;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(dir.pop(), "could not locate repo root (no sky-stdlib ancestor)");
    }
}

/// Materialise a throwaway single-module project and return its dir.
fn scratch(tag: &str, main: &str) -> PathBuf {
    let uniq = format!(
        "sky-caflint-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"caf-lint\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main).unwrap();
    dir
}

/// Lower `main` and return the warnings naming the memoised-CAF stale-read lint.
fn stale_read_warnings(tag: &str, main: &str) -> Vec<String> {
    let repo = repo_root();
    let project = scratch(tag, main);
    let warnings =
        emit_example_warnings(&repo, &project).unwrap_or_else(|e| panic!("emit failed: {e}"));
    let _ = std::fs::remove_dir_all(&project);
    warnings
        .into_iter()
        .filter(|w| w.contains("is memoised to a SINGLE value"))
        .collect()
}

fn warns_about(warnings: &[String], name: &str) -> bool {
    warnings.iter().any(|w| w.contains(&format!("`{name}`")))
}

/// Shared prelude: a store, a blessed pool CAF, and the helpers the shapes
/// under test are built from. `Store.all` reaches `Db_queryObjects`, so every
/// def that FORCES it carries `EffectKind::StoreRead`.
const PRELUDE: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Sky.Core.Result as Result
import Sky.Core.String as String
import Sky.Core.System as System
import Sky.Core.Task as Task
import Sky.Core.Error as Error exposing (Error)
import Std.Codec as Codec
import Std.Db as Db exposing (Db)
import Std.Db.Store as Store exposing (Store)
import Std.Log exposing (println)


type alias Todo =
    { id : Int
    , title : String
    }


todos : Store Todo
todos =
    Store.fromCodec "todos" (Codec.auto { id = 0, title = "" })
        |> Store.serial "id"


db : Db
db =
    case Task.run (Db.connect ()) of

        Ok h ->
            h

        Err e ->
            let
                _ = println (Error.toString e)
            in
                System.exit 1
"#;

fn program(body: &str, main_body: &str) -> String {
    format!("{PRELUDE}\n{body}\n\nmain =\n{main_body}\n")
}

// =========================================================================
// TRUE POSITIVES — the footgun the lint exists for. These MUST keep warning.
// =========================================================================

/// The original incident shape: a row set read at module level and frozen for
/// the process. `Store.all` is APPLIED, so its read is forced right here.
#[test]
fn frozen_row_set_warns() {
    let w = stale_read_warnings(
        "frozen",
        &program(
            r#"
frozenTodos : List Todo
frozenTodos =
    case Task.run (Store.all db todos) of

        Ok xs ->
            xs

        Err _ ->
            []
"#,
            "    println (String.fromInt (List.length frozenTodos))",
        ),
    );
    assert!(
        warns_about(&w, "frozenTodos"),
        "a memoised `Task.run (Store.all …)` row set is the incident the lint \
         exists for — it must warn. Got: {w:?}"
    );
}

/// The same read LAUNDERED one hop behind a user helper — the shape the
/// transitive `def_effect` path was added for. The helper is applied, and the
/// read also sits in a lambda the helper runs.
#[test]
fn laundered_read_behind_a_helper_warns() {
    let w = stale_read_warnings(
        "laundered",
        &program(
            r#"
withTodoList : (Db -> Task Error (List Todo)) -> List Todo
withTodoList f =
    case Task.run (f db) of

        Ok xs ->
            xs

        Err _ ->
            []


listActive : List Todo
listActive =
    withTodoList (\c -> Store.all c todos)
"#,
            "    println (String.fromInt (List.length listActive))",
        ),
    );
    assert!(
        warns_about(&w, "listActive"),
        "a read laundered behind a helper is still a frozen read — it must \
         warn. Got: {w:?}"
    );
}

/// The read forced through a PIPELINE. `|>` stays a `Binop` all the way to
/// emission, so its function side (`Task.run`) is a bare `Var` — the
/// forced-reference rule must count it as an application anyway.
#[test]
fn read_forced_through_a_pipeline_warns() {
    let w = stale_read_warnings(
        "piped",
        &program(
            r#"
pipedTodos : List Todo
pipedTodos =
    Store.all db todos
        |> Task.run
        |> Result.withDefault []
"#,
            "    println (String.fromInt (List.length pipedTodos))",
        ),
    );
    assert!(
        warns_about(&w, "pipedTodos"),
        "`Store.all db todos |> Task.run` forces the read exactly like the \
         `Call` form — it must warn. Got: {w:?}"
    );
}

// =========================================================================
// FALSE POSITIVES — correct code the lint used to flag. These MUST be silent.
// =========================================================================

/// The sky-lang.org shape: a top-level table of route registrations that
/// REFERENCES handlers which read per request. Nothing is read while the table
/// is built, so nothing about it is frozen.
#[test]
fn route_table_of_handler_references_is_silent() {
    let w = stale_read_warnings(
        "routes",
        &program(
            r#"
handleOne : Int -> String
handleOne n =
    case Task.run (Store.all db todos) of

        Ok xs ->
            String.fromInt (List.length xs + n)

        Err _ ->
            "err"


reg : String -> (Int -> String) -> ( String, Int -> String )
reg name f =
    ( name, f )


routeTable : List ( String, Int -> String )
routeTable =
    [ reg "one" handleOne ]
"#,
            "    println (String.fromInt (List.length routeTable))",
        ),
    );
    assert!(
        !warns_about(&w, "routeTable"),
        "a table of handler REFERENCES reads nothing when built — the handler \
         runs per request. Warning here is the false positive found on \
         sky-lang.org's `apiRoutes`. Got: {w:?}"
    );
}

/// Same, with a POINT-FREE handler: zero-arity, but its value is a FUNCTION,
/// so forcing it builds a closure rather than performing the read.
#[test]
fn point_free_handler_reference_is_silent() {
    let w = stale_read_warnings(
        "pointfree",
        &program(
            r#"
wrap : Store Todo -> Int -> String
wrap store n =
    case Task.run (Store.all db store) of

        Ok xs ->
            String.fromInt (List.length xs + n)

        Err _ ->
            "err"


handlePointFree : Int -> String
handlePointFree =
    wrap todos


reg : String -> (Int -> String) -> ( String, Int -> String )
reg name f =
    ( name, f )


routeTable : List ( String, Int -> String )
routeTable =
    [ reg "pf" handlePointFree ]
"#,
            "    println (String.fromInt (List.length routeTable) ++ handlePointFree 1)",
        ),
    );
    assert!(
        !warns_about(&w, "routeTable"),
        "a point-free handler's value is a closure; forcing it reads nothing. \
         Got: {w:?}"
    );
    assert!(
        !warns_about(&w, "handlePointFree"),
        "a memoised FUNCTION is not a frozen read. Got: {w:?}"
    );
}

/// The blessed shared-pool CAF itself (`db = case Task.run (Db.connect ()) of
/// …`) — the idiom AGENTS.md documents. `Db.connect` is a HANDLE kernel, not a
/// read, and one shared pool is the point. Asserted inside a program that DOES
/// warn (about `frozenTodos`), so the silence is about `db`, not about the lint
/// being off or the defs being dead-code-eliminated.
#[test]
fn shared_pool_handle_is_silent() {
    let w = stale_read_warnings(
        "pool",
        &program(
            r#"
frozenTodos : List Todo
frozenTodos =
    case Task.run (Store.all db todos) of

        Ok xs ->
            xs

        Err _ ->
            []
"#,
            "    println (String.fromInt (List.length frozenTodos))",
        ),
    );
    assert!(
        warns_about(&w, "frozenTodos"),
        "sanity: this program must still warn about the frozen read, or the \
         `db` assertion below is vacuous. Got: {w:?}"
    );
    assert!(
        !warns_about(&w, "db") && !warns_about(&w, "todos"),
        "memoising the connection pool (and the `Store` descriptor) is the \
         documented contract. Got: {w:?}"
    );
}

/// Guard against the cheap way to "fix" the false positive — muting the lint.
/// One program, both shapes: the frozen read warns, the route table does not.
#[test]
fn one_program_discriminates_both_shapes() {
    let w = stale_read_warnings(
        "both",
        &program(
            r#"
frozenTodos : List Todo
frozenTodos =
    case Task.run (Store.all db todos) of

        Ok xs ->
            xs

        Err _ ->
            []


handleOne : Int -> String
handleOne n =
    case Task.run (Store.all db todos) of

        Ok xs ->
            String.fromInt (List.length xs + n)

        Err _ ->
            "err"


reg : String -> (Int -> String) -> ( String, Int -> String )
reg name f =
    ( name, f )


routeTable : List ( String, Int -> String )
routeTable =
    [ reg "one" handleOne ]
"#,
            "    println (String.fromInt (List.length frozenTodos + List.length routeTable))",
        ),
    );
    assert!(
        warns_about(&w, "frozenTodos") && !warns_about(&w, "routeTable"),
        "the lint must DISCRIMINATE: frozen read warns, registration table \
         does not. Got: {w:?}"
    );
}
