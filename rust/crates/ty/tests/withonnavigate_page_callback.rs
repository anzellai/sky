//! Regression lock: `Std.Live.withOnNavigate` callback is `(page -> msg)`.
//!
//! The runtime's `dispatchOnNavigate` (v0.16.7 #418) extracts `model.Page` and
//! reflect-calls the `onNavigate` callback with the **Page value**. For a long
//! time `sky-stdlib/Std/Live.sky` mistyped the parameter as `(String -> msg)`,
//! so a user callback lowered to `func(string)` and `reflect.Call` handed it a
//! Page — panicking at runtime (`reflect: Call using <Page>_V as type string`)
//! on EVERY navigation, because `onNavigate` fires on the initial mount too.
//! `sky check` + `go build` both passed; the failure was reflect-dynamic and
//! only surfaced when the app ran (it took down a production Sky.Live site).
//!
//! This guards the signature at the type-check layer, where it IS visible: a
//! callback that pattern-matches the user's `Page` union
//! (`\page -> case page of HomePage -> …`) type-checks ONLY when the parameter
//! is `page` (a free var unifying with the union). Under the old
//! `(String -> msg)` sig the `case page of HomePage ->` arm cannot unify
//! `String` with the `Page` union, so it would report type errors. Assert the
//! page-typed callback ACCEPTS (0 errors); a control with a genuinely
//! ill-typed callback body still REJECTS (proves the check isn't vacuous).

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

/// Build stdlib + a single `Main` module (source given), return the number of
/// type errors reported for it.
fn type_errors_for_main(root: &Path, main_src: &str) -> usize {
    let stdlib = load_stdlib(root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");

    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    let mid = db.add_module("Main", syntax::parse(main_src, base::FileId(0)));

    let out = ty::check_modules(&db, &[mid]);
    out.type_errors
}

/// A complete minimal Sky.Live app whose `withOnNavigate` callback
/// pattern-matches the `Page` union. `{CALLBACK}` is substituted per test.
fn app_src(callback: &str) -> String {
    format!(
        "module Main exposing (main)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Live as Live exposing (app, config, route)\n\
         import Std.Ui as Ui\n\
         import Std.Cmd as Cmd\n\
         import Std.Sub as Sub\n\
         \n\
         type Page\n    = HomePage\n    | AboutPage\n\
         \n\
         type Msg\n    = Noop\n    | NavHappened\n\
         \n\
         init _req =\n    ( {{ page = HomePage, hits = 0 }}, Cmd.none )\n\
         \n\
         update msg model =\n    case msg of\n        Noop ->\n            ( model, Cmd.none )\n\n        NavHappened ->\n            ( {{ model | hits = model.hits + 1 }}, Cmd.none )\n\
         \n\
         view model =\n    Ui.layout []\n        (Ui.column [] [ Ui.text (String.fromInt model.hits) ])\n\
         \n\
         subscriptions _model =\n    Sub.none\n\
         \n\
         main =\n    app\n        (config\n            {{ init = init\n            , update = update\n            , view = view\n            , subscriptions = subscriptions\n            , routes = [ route \"/\" HomePage, route \"/about\" AboutPage ]\n            , notFound = HomePage\n            }}\n            |> Live.withOnNavigate {callback})\n",
        callback = callback
    )
}

#[test]
fn onnavigate_page_typed_callback_accepts() {
    let root = repo_root();
    // Callback pattern-matches the Page union — only well-typed when the
    // withOnNavigate parameter is `(page -> msg)`, not `(String -> msg)`.
    let cb = "(\\page ->\n                case page of\n                    HomePage ->\n                        Noop\n\n                    AboutPage ->\n                        NavHappened)";
    let errs = type_errors_for_main(&root, &app_src(cb));
    assert_eq!(
        errs, 0,
        "REGRESSION — a Page-typed onNavigate callback was REJECTED ({errs} errors); \
         Std.Live.withOnNavigate must be `(page -> msg)`, not `(String -> msg)`"
    );
}

#[test]
fn onnavigate_ignored_arg_callback_accepts() {
    let root = repo_root();
    // The common form: ignore the page, fire one Msg on every navigation.
    let cb = "(\\_ -> NavHappened)";
    let errs = type_errors_for_main(&root, &app_src(cb));
    assert_eq!(
        errs, 0,
        "REGRESSION — `\\_ -> Msg` onNavigate callback was REJECTED ({errs} errors)"
    );
}

#[test]
fn onnavigate_ill_typed_callback_still_rejects() {
    let root = repo_root();
    // Control: the callback body must return a Msg. Returning a bare String
    // must REJECT — proves the acceptance tests above aren't vacuously green.
    let cb = "(\\_ -> \"not a msg\")";
    let errs = type_errors_for_main(&root, &app_src(cb));
    assert!(
        errs > 0,
        "VACUOUS — an onNavigate callback returning a String (not Msg) was ACCEPTED"
    );
}
