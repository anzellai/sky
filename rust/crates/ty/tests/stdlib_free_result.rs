//! CAST gate: a trusted stdlib signature must not be an unchecked cast.
//!
//! A stdlib function bound to a runtime kernel (`name = Ffi.kernel "Sym"`) is
//! trusted: the checker takes its annotation as the truth and never sees the Go
//! body. If the RESULT of such a signature has a type variable that no
//! parameter mentions, every caller picks that variable freely, so the
//! signature casts whatever the kernel returns to whatever the caller wants.
//! `Auth.verifyToken : Secret -> String -> Result Error a` was one: the claims
//! map came back typed as a `Dict String String`, a record, or an `Int`, as the
//! call site pleased, and the mismatch surfaced (if at all) as a run-time
//! narrowing panic far from the call.
//!
//! `any` in a result is the same hole for EVERY stdlib signature, kernel-bound
//! or Sky-bodied: each `any` occurrence is a fresh variable at every use
//! (`ty/src/lib.rs`, `Ty::Any`), so a result `any` is unlinked to the body.
//!
//! A Sky-bodied stdlib function with a free result variable is NOT flagged: its
//! body is type-checked against the annotation, so a free result variable there
//! is a genuine polymorphic value (`Html.text : String -> Html msg`).
//!
//! Each allowed exception is listed in [`ALLOWED`] with the reason it is sound.
//! The test fails on an unlisted hit and on a listed entry that no longer hits.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use syntax::ast::{self, AstNode};
use syntax::{SyntaxKind, SyntaxNode};

/// `Module.name` -> why the free / `any` result is sound. "Phantom" means the
/// result type never carries a value of that variable to the caller, so no cast
/// can happen through it.
const ALLOWED: &[(&str, &str)] = &[
    (
        "Sky.Core.Dict.empty",
        "phantom: the empty Dict holds no key or value",
    ),
    (
        "Sky.Core.Set.empty",
        "phantom: the empty Set holds no element",
    ),
    (
        "Sky.Core.Json.Decode.fail",
        "phantom: a decoder that always fails never yields an `a`",
    ),
    (
        "Std.Config.fail",
        "phantom: a decoder that always fails never yields an `a`",
    ),
    (
        "Std.Db.Decode.fail",
        "phantom: a decoder that always fails never yields an `a`",
    ),
    (
        "Sky.Core.System.exit",
        "bottom: the process exits, the call never returns",
    ),
    (
        "Sky.Core.Task.fail",
        "phantom: the task always fails, it never succeeds with an `a`",
    ),
    (
        "Sky.Core.Task.forever",
        "phantom: the loop ends only with an error, it never succeeds with a `b`",
    ),
    (
        "Sky.Core.Task.lazy",
        "phantom: the task always succeeds (the trampoline wraps the thunk in Ok), so it never fails with an `e`; a panic stays a classified panic",
    ),
    (
        "Sky.Core.Task.succeed",
        "phantom: the task always succeeds, it never fails with an `e`",
    ),
    (
        "Std.Cmd.none",
        "phantom: a Cmd/Sub that only performs an effect never delivers a `msg`",
    ),
    (
        "Std.Cmd.publish",
        "phantom: a Cmd/Sub that only performs an effect never delivers a `msg`; subscribers decode the payload with their own typed Sub",
    ),
    (
        "Std.Cmd.publishNoEcho",
        "phantom: a Cmd/Sub that only performs an effect never delivers a `msg`; subscribers decode the payload with their own typed Sub",
    ),
    (
        "Std.Cmd.toIsland",
        "phantom: a Cmd/Sub that only performs an effect never delivers a `msg`",
    ),
    (
        "Std.Nav.pushUrl",
        "phantom: a Cmd/Sub that only performs an effect never delivers a `msg`; the route change reaches `update` through the app's own typed routing",
    ),
    (
        "Std.Nav.replaceUrl",
        "phantom: a Cmd/Sub that only performs an effect never delivers a `msg`; the route change reaches `update` through the app's own typed routing",
    ),
    (
        "Std.Spa.reportError",
        "phantom: a Cmd/Sub that only performs an effect never delivers a `msg`",
    ),
    (
        "Std.Spa.reportRpcFailure",
        "phantom: a Cmd/Sub that only performs an effect never delivers a `msg`",
    ),
    (
        "Std.Sub.none",
        "phantom: the empty subscription delivers no `msg`",
    ),
    (
        "Std.Sync.newQueue",
        "phantom: a new queue is empty; `push`/`pop` fix `a` through the queue handle",
    ),
    (
        "Sky.Core.WebSocket.subscribeWebSocketRaw",
        "internal: not exported; only the typed Sub wrappers (onOpen/onMessage/onClose/onError) call it, and their signatures tie `msg` to the caller's function",
    ),
    (
        "Std.Cache.getRaw",
        "internal: not exported; `Cache.get : Cache k v -> k -> Task Error (Maybe v)` calls it and fixes `v` through the Cache handle",
    ),
    (
        "Std.Ui.ariaForDescription",
        "internal: a Std.Ui render helper, not exported to apps",
    ),
    (
        "Std.Ui.collectHtmlAttrs",
        "internal: a Std.Ui render helper, not exported to apps",
    ),
    (
        "Std.Ui.kernelAttr",
        "internal: a Std.Ui render helper, not exported to apps",
    ),
    (
        "Std.Ui.renderElement",
        "internal: a Std.Ui render helper, not exported to apps",
    ),
    (
        "Std.Ui.renderElementIn",
        "internal: a Std.Ui render helper, not exported to apps",
    ),
    (
        "Std.Ui.renderNearby",
        "internal: a Std.Ui render helper, not exported to apps",
    ),
    (
        "Std.Ui.renderNodeAs",
        "internal: a Std.Ui render helper, not exported to apps",
    ),
    (
        "Std.Ui.renderText",
        "internal: a Std.Ui render helper, not exported to apps",
    ),
    (
        "Std.Ui.toAttrAttribute",
        "internal: a Std.Ui render helper, not exported to apps",
    ),
];

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

fn types_under(n: &SyntaxNode) -> Vec<SyntaxNode> {
    n.children()
        .filter(|c| ast::Type::can_cast(c.kind()))
        .collect()
}

/// Every type variable name under `n` (`any` included).
fn vars(n: &SyntaxNode, out: &mut Vec<String>) {
    if n.kind() == SyntaxKind::TypeVar {
        let t = n.text().to_string();
        out.push(t.trim().to_string());
        return;
    }
    for c in n.children() {
        vars(&c, out);
    }
}

/// Split a signature into its parameter types and its final result type.
fn params_and_result(ty: &SyntaxNode) -> (Vec<SyntaxNode>, SyntaxNode) {
    let mut params = Vec::new();
    let mut cur = ty.clone();
    loop {
        // `(a -> b)` as a whole signature is still an arrow.
        if cur.kind() == SyntaxKind::TypeParen {
            if let Some(inner) = types_under(&cur).into_iter().next() {
                if inner.kind() == SyntaxKind::TypeFun {
                    cur = inner;
                    continue;
                }
            }
        }
        if cur.kind() != SyntaxKind::TypeFun {
            return (params, cur);
        }
        let parts = types_under(&cur);
        let [param, result] = parts.as_slice() else {
            return (params, cur);
        };
        params.push(param.clone());
        cur = result.clone();
    }
}

/// `Module.name` -> the violation, for every stdlib signature that breaks the
/// rule.
fn violations() -> BTreeMap<String, String> {
    let mut files = Vec::new();
    collect_sky(&repo_root().join("sky-stdlib"), &mut files);
    assert!(files.len() > 50, "stdlib not found");
    let mut out = BTreeMap::new();
    let db = load_stdlib_db();
    for path in files {
        let src = std::fs::read_to_string(&path).unwrap();
        let parse = syntax::parse(&src, base::FileId(0));
        let tree = parse.tree();
        let module = tree
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .unwrap_or_default();
        // What an app can reach: the module's published values.
        let exported: Vec<String> = db
            .module_by_name(&module)
            .map(|m| {
                db.module_exports(m)
                    .values
                    .iter()
                    .map(|(n, _)| n.as_str().to_string())
                    .collect()
            })
            .unwrap_or_default();
        let mut kernel_bound: Vec<String> = Vec::new();
        let mut annos: Vec<(String, SyntaxNode)> = Vec::new();
        for d in tree.decls() {
            match d {
                ast::Decl::Value(v) => {
                    let (Some(n), Some(b)) = (v.name(), v.body()) else {
                        continue;
                    };
                    let body = b.syntax().text().to_string();
                    let body = body.trim_start();
                    // `Ffi.kernel "Sym"` / `Ffi.callPure …` under any qualifier.
                    let head = body.split_whitespace().next().unwrap_or("");
                    if head.ends_with(".kernel")
                        || head.ends_with(".callPure")
                        || head.ends_with(".call")
                        || head.ends_with(".callTask")
                    {
                        kernel_bound.push(n.text().to_string());
                    }
                }
                ast::Decl::TypeAnno(a) => {
                    if let (Some(n), Some(t)) = (a.name(), a.ty()) {
                        annos.push((n.text().to_string(), t.syntax().clone()));
                    }
                }
                _ => {}
            }
        }
        for (name, ty) in annos {
            let (params, result) = params_and_result(&ty);
            let mut pv = Vec::new();
            for p in &params {
                vars(p, &mut pv);
            }
            let mut rv = Vec::new();
            vars(&result, &mut rv);
            let key = format!("{module}.{name}");
            let reach = if exported.contains(&name) {
                "exported"
            } else {
                "internal"
            };
            let sig = ty
                .text()
                .to_string()
                .split_whitespace()
                .collect::<Vec<_>>()
                .join(" ");
            if rv.iter().any(|v| v == "any") {
                out.insert(
                    key,
                    format!("[{reach}] `any` in the result: {name} : {sig}"),
                );
                continue;
            }
            if kernel_bound.contains(&name) {
                let mut free: Vec<&String> = rv.iter().filter(|v| !pv.contains(v)).collect();
                free.sort();
                free.dedup();
                if !free.is_empty() {
                    out.insert(
                        key,
                        format!(
                            "[{reach}] kernel result variable(s) {free:?} not in any parameter: \
                             {name} : {sig}"
                        ),
                    );
                }
            }
        }
    }
    out
}

#[test]
fn no_trusted_stdlib_signature_casts_its_result() {
    let found = violations();
    let allowed: BTreeMap<&str, &str> = ALLOWED.iter().copied().collect();
    let unlisted: Vec<String> = found
        .iter()
        .filter(|(k, _)| !allowed.contains_key(k.as_str()))
        .map(|(k, v)| format!("  {k}: {v}"))
        .collect();
    let stale: Vec<&&str> = allowed
        .keys()
        .filter(|k| !found.contains_key(**k))
        .collect();
    // An "internal" reason is only true while the helper stays unexported.
    let wrongly_internal: Vec<&String> = found
        .iter()
        .filter(|(k, v)| {
            v.starts_with("[exported]")
                && allowed
                    .get(k.as_str())
                    .is_some_and(|r| r.starts_with("internal"))
        })
        .map(|(k, _)| k)
        .collect();
    assert!(
        wrongly_internal.is_empty(),
        "these are allowed as internal helpers but are now exported: {wrongly_internal:?}"
    );
    assert!(
        unlisted.is_empty() && stale.is_empty(),
        "trusted stdlib signatures that cast their result.\n\
         Give the result a concrete type (or a variable a parameter fixes), or, if it is \
         genuinely sound, add it to ALLOWED with the reason:\n{}\n\
         ALLOWED entries that no longer apply (remove them):\n  {stale:?}",
        unlisted.join("\n")
    );
}

#[test]
fn the_gate_sees_the_known_shapes() {
    // Falsifiability: the scan must classify these real signatures the way the
    // rule says, or an empty ALLOWED list would pass vacuously.
    let found = violations();
    // A kernel with a phantom-only result stays visible to the scan (it is
    // allowed with a reason, not invisible).
    assert!(found.contains_key("Sky.Core.System.exit"), "{found:?}");
    // A Sky-bodied stdlib function with a free result is checked by the
    // type checker, so it is not a hit.
    assert!(!found.contains_key("Std.Html.text"), "{found:?}");
}

// ---- Auth.verifyToken : Secret -> String -> Result Error Json.Value ---------

fn load_stdlib_db() -> hir::SourceDb {
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
    db
}

fn check_main(main: &str) -> Vec<diagnostics::Diagnostic> {
    let mut db = load_stdlib_db();
    let mid = db.add_module("Main", syntax::parse(main, base::FileId(0)));
    ty::check_modules(&db, &[mid])
        .diagnostics
        .into_iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .collect()
}

const AUTH_HEAD: &str = "module Main exposing (main)\n\
    import Sky.Core.Prelude exposing (..)\n\
    import Sky.Core.Dict as Dict exposing (Dict)\n\
    import Sky.Core.Json.Decode as Decode\n\
    import Sky.Core.Json.Encode exposing (Value)\n\
    import Sky.Core.Secret exposing (Secret)\n\
    import Std.Auth as Auth\n\
    import Std.Log exposing (println)\n\n";

#[test]
fn verify_token_claims_can_no_longer_be_cast_at_the_call_site() {
    // The old cast: the caller chose the claims type. Now a type error that
    // says what changed, with the decoder form and the migration anchor.
    let main = format!(
        "{AUTH_HEAD}verify : Secret -> String -> Result Error (Dict String String)\n\
         verify s t =\n    Auth.verifyToken s t\n\n\
         main =\n    println \"x\"\n"
    );
    let errs = check_main(&main);
    let e = errs
        .iter()
        .find(|d| d.code.0 == "E2001")
        .unwrap_or_else(|| panic!("expected [E2001], got {errs:?}"));
    let hint = e.suggestion.clone().unwrap_or_default();
    assert!(hint.contains("Decode.decodeValue"), "{hint}");
    assert!(
        hint.ends_with("See docs/migration/v0.27.md#auth-verifytoken-json"),
        "{hint}"
    );
}

#[test]
fn verify_token_claims_read_through_a_decoder_check_clean() {
    // The accepted twin: the claims are a Value, read with a decoder.
    let main = format!(
        "{AUTH_HEAD}verify : Secret -> String -> Result Error Value\n\
         verify s t =\n    Auth.verifyToken s t\n\n\
         subject : Secret -> String -> Result Error String\n\
         subject s t =\n    Auth.verifyToken s t\n        |> Result.andThen (Decode.decodeValue (Decode.field \"sub\" Decode.string))\n\n\
         main =\n    println \"x\"\n"
    );
    let errs = check_main(&main);
    assert!(errs.is_empty(), "{errs:?}");
}

// ---- Server.withCookie : String -> String -> String -> Response -> Response --

const SERVER_HEAD: &str = "module Main exposing (main)\n\
    import Sky.Core.Prelude exposing (..)\n\
    import Sky.Http.Server as Server exposing (Response)\n\
    import Std.Log exposing (println)\n\n";

#[test]
fn with_cookie_old_cookie_shape_points_at_add_cookie() {
    // The v0.26.1 pre-built-Cookie shape, which the `any` signature admitted.
    let main = format!(
        "{SERVER_HEAD}page : Response\npage =\n    Server.html \"hi\"\n        |> Server.withCookie (Server.cookie \"k\" \"v\")\n\n\
         main =\n    println \"x\"\n"
    );
    let errs = check_main(&main);
    let e = errs
        .iter()
        .find(|d| d.code.0 == "E2001")
        .unwrap_or_else(|| panic!("expected [E2001], got {errs:?}"));
    let hint = e.suggestion.clone().unwrap_or_default();
    assert!(hint.contains("Server.addCookie"), "{hint}");
    assert!(
        hint.ends_with("See docs/migration/v0.27.md#server-withcookie-typed"),
        "{hint}"
    );
}

#[test]
fn with_cookie_typed_forms_check_clean() {
    let main = format!(
        "{SERVER_HEAD}page : Response\npage =\n    Server.html \"hi\"\n        |> Server.withCookie \"k\" \"v\" \"Path=/; HttpOnly\"\n        |> Server.addCookie (Server.cookie \"k2\" \"v2\")\n\n\
         main =\n    println \"x\"\n"
    );
    let errs = check_main(&main);
    assert!(errs.is_empty(), "{errs:?}");
}
