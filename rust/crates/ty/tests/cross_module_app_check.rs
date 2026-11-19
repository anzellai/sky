//! F1c reject-lock: cross-module misuse of an UNANNOTATED app def.
//!
//! The single-module reject corpus (`tests/reject.rs`) cannot express a
//! cross-module fixture — it loads stdlib + exactly one target file. This test
//! closes that: it builds a 2-module `SourceDb` where module `A` defines an
//! unannotated `xs = ["a", "b"]` (a `List String`) and module `B` uses it
//! cross-module. It asserts:
//!
//!   * MISUSE (`bad = xs + 1`) — `xs` used as a Number — is now REJECTED (the
//!     F1c `app_check_sigs` channel pins `xs : List String`, so `+` clashes).
//!     Before the fix `xs` resolved to a fresh flex cross-module and the misuse
//!     was silently ACCEPTED (go build caught it downstream).
//!   * CONTROL (`ok = List.length xs`) — a VALID cross-module use — still
//!     ACCEPTS (zero type errors). Proves the tightening is not over-eager.

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

/// Build stdlib + module A (`State`) + module B (`Main`, source given), then
/// return the number of type errors reported for B.
fn type_errors_for_b(root: &Path, b_src: &str) -> usize {
    let a_src = "module State exposing (xs)\n\nxs = [ \"a\", \"b\" ]\n";
    type_errors_for_main(root, ("State", a_src), b_src).0
}

/// Build stdlib + the module `a` (name, source) + module `Main` (source
/// given), then return the number of type errors reported for `Main` and
/// their rendered text.
fn type_errors_for_main(root: &Path, a: (&str, &str), b_src: &str) -> (usize, String) {
    let stdlib = load_stdlib(root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");

    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    db.add_module(a.0, syntax::parse(a.1, base::FileId(0)));
    let b_mid = db.add_module("Main", syntax::parse(b_src, base::FileId(0)));

    let out = ty::check_modules(&db, &[b_mid]);
    let text = out
        .diagnostics
        .iter()
        .map(|d| d.message.clone())
        .collect::<Vec<_>>()
        .join("\n");
    (out.type_errors, text)
}

#[test]
fn cross_module_unannotated_misuse_is_rejected() {
    let root = repo_root();
    // `xs : List String` used as a Number — must be REJECTED.
    let b_src = "module Main exposing (bad)\n\
                 import State exposing (xs)\n\n\
                 bad = xs + 1\n";
    let errs = type_errors_for_b(&root, b_src);
    assert!(
        errs > 0,
        "SOUNDNESS HOLE — cross-module misuse `xs + 1` (xs : List String) was ACCEPTED"
    );
}

#[test]
fn cross_module_valid_use_still_accepts() {
    let root = repo_root();
    // A VALID cross-module use of the same def — must still ACCEPT.
    let b_src = "module Main exposing (ok)\n\
                 import State exposing (xs)\n\
                 import Sky.Core.List as List\n\n\
                 ok = List.length xs\n";
    let errs = type_errors_for_b(&root, b_src);
    assert_eq!(
        errs, 0,
        "OVER-EAGER — a valid cross-module use `List.length xs` was REJECTED ({errs} errors)"
    );
}

/// A routing helper from another module, hidden in a user custom type
/// (v0.29.14, D7). `Routing.router` sets `String` pages inside a `Router`
/// value, whose type holds no `App` node. The model's `page` field is a
/// `Route`, so the served app is refused with `[E2001]`.
const ROUTING: &str = "module Routing exposing (Router(..), router, runRouter)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.App as App\n\n\
type Router f s p m msg k\n    = Router (App.App f s p m msg k -> App.App f s p m msg k)\n\n\n\
router =\n    Router (\\a -> App.withRoutes [ App.route \"/\" PAGE ] a)\n\n\n\
runRouter (Router f) a =\n    f a\n";

const ROUTED_MAIN: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.App as App\n\
import Std.Ui as Ui exposing (Element)\n\
import Routing exposing (router, runRouter)\n\n\
type Route\n    = Dashboard\n    | Missing\n\n\
type alias Model =\n    { page : Route, title : String }\n\n\
type Msg\n    = Inc\n\n\n\
init : () -> ( Model, Cmd Msg )\n\
init () =\n    ( { page = Dashboard, title = \"t\" }, Cmd.none )\n\n\n\
update : Msg -> Model -> ( Model, Cmd Msg )\n\
update msg model =\n    ( model, Cmd.none )\n\n\n\
view : Model -> Element Msg\n\
view model =\n    Ui.text model.title\n\n\n\
app =\n    App.app { init = init, update = update, view = view, subscriptions = \\_ -> Sub.none }\
\n        |> runRouter router\
\n        |> App.withNotFound MISSING\n\n\n\
main =\n    App.run app\n";

#[test]
fn cross_module_routing_helper_in_a_custom_type_is_checked_at_the_served_app() {
    let root = repo_root();
    let routing = ROUTING.replace("PAGE", "\"home\"");
    let main = ROUTED_MAIN.replace("MISSING", "\"missing\"");
    let (errs, text) = type_errors_for_main(&root, ("Routing", &routing), &main);
    assert!(
        errs > 0 && text.contains("model's `page` field"),
        "SOUNDNESS HOLE — a cross-module routing helper in a custom type gave `String` pages \
         to a model whose `page` field is a `Route`, and the app was ACCEPTED ({errs} errors):\n{text}"
    );
}

#[test]
fn cross_module_routing_helper_in_a_custom_type_with_the_model_page_type_accepts() {
    let root = repo_root();
    // The helper's pages must name the main module's `Route`, so the control
    // gives the helper a generic page: the caller passes the routes.
    let good = "module Routing exposing (Router(..), router, runRouter)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.App as App\n\n\
type Router f s p m msg k\n    = Router (App.App f s p m msg k -> App.App f s p m msg k)\n\n\n\
router routes =\n    Router (\\a -> App.withRoutes routes a)\n\n\n\
runRouter (Router f) a =\n    f a\n";
    let main = ROUTED_MAIN
        .replace(
            "runRouter router\n",
            "runRouter (router [ App.route \"/\" Dashboard ])\n",
        )
        .replace("MISSING", "Missing");
    let (errs, text) = type_errors_for_main(&root, ("Routing", good), &main);
    assert_eq!(
        errs, 0,
        "OVER-EAGER — a cross-module routing helper with the model's page type was REJECTED:\n{text}"
    );
}
