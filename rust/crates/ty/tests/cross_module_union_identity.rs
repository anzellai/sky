//! Cross-module NOMINAL IDENTITY for unions — the soundness lock.
//!
//! # The hole this closes
//!
//! Two same-named unions in two modules were ONE type to the checker.
//! `ty::sig::rewrite_alias_refs` module-qualified `DefKind::TypeAlias` references
//! only (the #164 fix), so every union collapsed to its bare final segment, and
//! `unify.rs` compares `Ty::App` names as plain strings. `App("Shape")` from
//! `Conflate.Alpha` therefore unified with `App("Shape")` from `Conflate.Beta`,
//! program-wide — while `lower` emitted two DISTINCT Go interfaces for them and
//! bridged the gap with `rt.Coerce`. Both Go interfaces have the same method set
//! (`SkyVariantTag`/`SkyVariantName`), so the assertion SUCCEEDED and handed the
//! callee a value none of its `case` arms matched:
//!
//! ```text
//! panic: sky.Unreachable(case): sky: codegen reached an arm the
//!        exhaustiveness checker said was impossible
//! ```
//!
//! `sky check` clean, `go build` clean, runtime panic — a direct violation of
//! "no runtime panic from well-typed Sky code". Pinned at
//! `corpus/repro/cross-module-union-conflation/`.
//!
//! # Why this file and not the reject corpus
//!
//! `tests/reject.rs` loads stdlib plus EXACTLY ONE target file, so it cannot
//! express a two-module fixture. Same reason `cross_module_app_check.rs` exists.
//! The reject-corpus census constants are therefore untouched by this test.
//!
//! # What is asserted
//!
//! One REJECT (the repro) and the accepted twins that make the tightening
//! falsifiable in the other direction. Every twin below is a program that
//! compiled BEFORE this change and must still compile AFTER it — they are the
//! #164 blast radius, stated as tests:
//!
//! * same-named unions each used CORRECTLY in their own module,
//! * `Main.Msg` WRAPPING `Counter.Msg` (`examples/10-live-component`),
//! * a union and an ALIAS sharing a name across modules,
//! * one union reached by two paths (direct import + re-export),
//! * the same union referenced QUALIFIED in one place and BARE in another,
//! * a kernel-implicit type (`Decoder`) reached from two modules — the
//!   collision the `goty.rs` `("Decoder", _) => GoTy::Any` band-aid works around,
//! * an alias-of-a-union crossing a module boundary.

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

/// Build stdlib + the given `(module name, source)` list, check the LAST module,
/// and return its type-error count.
fn type_errors(root: &Path, mods: &[(&str, String)]) -> usize {
    let stdlib = load_stdlib(root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");

    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    let mut last = None;
    for (name, src) in mods {
        last = Some(db.add_module(name, syntax::parse(src, base::FileId(0))));
    }
    let target = last.expect("at least one module");
    ty::check_modules(&db, &[target]).type_errors
}

/// `Conflate.Alpha` — `Shape = Circle Int`, disjoint constructors from Beta's.
fn alpha() -> (&'static str, String) {
    (
        "Conflate.Alpha",
        "module Conflate.Alpha exposing (..)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         \n\
         type Shape = Circle Int\n\
         \n\
         alphaTag : Shape -> String\n\
         alphaTag s =\n\
         \x20   case s of\n\
         \x20       Circle _n -> \"alpha-circle\"\n"
            .to_string(),
    )
}

/// `Conflate.Beta` — `Shape = Square Int`.
fn beta() -> (&'static str, String) {
    (
        "Conflate.Beta",
        "module Conflate.Beta exposing (..)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         \n\
         type Shape = Square Int\n\
         \n\
         betaTag : Shape -> String\n\
         betaTag s =\n\
         \x20   case s of\n\
         \x20       Square _n -> \"beta-square\"\n"
            .to_string(),
    )
}

// ---------------------------------------------------------------------------
// THE REJECT — the pinned repro.
// ---------------------------------------------------------------------------

#[test]
fn cross_module_same_named_unions_do_not_unify() {
    let root = repo_root();
    // `B.betaTag` wants `Conflate.Beta.Shape`; it is handed a
    // `Conflate.Alpha.Shape`. Every reference is FULLY QUALIFIED, so no
    // use-site ambiguity rule ([E1012] in any namespace) can reach this.
    let main = (
        "Main",
        "module Main exposing (main)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         import Conflate.Alpha as A\n\
         import Conflate.Beta as B\n\
         \n\
         main =\n\
         \x20   println (B.betaTag (A.Circle 3))\n"
            .to_string(),
    );
    let errs = type_errors(&root, &[alpha(), beta(), main]);
    assert!(
        errs > 0,
        "SOUNDNESS HOLE — `Conflate.Alpha.Shape` was accepted where \
         `Conflate.Beta.Shape` is required. `sky check` passes, `go build` \
         passes, and the program panics with sky.Unreachable(case) at runtime. \
         See corpus/repro/cross-module-union-conflation/."
    );
}

// ---------------------------------------------------------------------------
// ACCEPTED TWINS — the #164 blast radius. Each compiled before the change and
// must still compile after it, or the fix is over-eager.
// ---------------------------------------------------------------------------

#[test]
fn twin_same_named_unions_used_correctly_are_accepted() {
    let root = repo_root();
    // Both `Shape`s exist, each is used with ITS OWN constructor. Nothing here
    // is wrong, and rejecting it would outlaw same-named types across modules.
    let main = (
        "Main",
        "module Main exposing (main)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         import Conflate.Alpha as A\n\
         import Conflate.Beta as B\n\
         \n\
         main =\n\
         \x20   println (A.alphaTag (A.Circle 3) ++ B.betaTag (B.Square 4))\n"
            .to_string(),
    );
    let errs = type_errors(&root, &[alpha(), beta(), main]);
    assert_eq!(
        errs, 0,
        "OVER-EAGER — same-named unions each used CORRECTLY in their own module \
         were rejected ({errs} errors). Same-named types across modules are \
         idiomatic Sky."
    );
}

#[test]
fn twin_msg_wrapping_across_modules_is_accepted() {
    let root = repo_root();
    // `examples/10-live-component`: BOTH modules declare `Msg`, and `Main.Msg`
    // has a variant that WRAPS `Counter.Msg`. If the two `Msg`es were still one
    // type this would be an infinite/cyclic type; if the fix over-qualified the
    // wrapped reference it would fail to resolve. It has to be exactly right.
    let counter = (
        "Counter",
        "module Counter exposing (..)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         \n\
         type Msg = Bump | Reset\n\
         \n\
         step : Msg -> Int -> Int\n\
         step m n =\n\
         \x20   case m of\n\
         \x20       Bump -> n + 1\n\
         \x20       Reset -> 0\n"
            .to_string(),
    );
    let main = (
        "Main",
        "module Main exposing (main)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         import Counter\n\
         \n\
         type Msg = Wrapped Counter.Msg | Noop\n\
         \n\
         apply : Msg -> Int -> Int\n\
         apply m n =\n\
         \x20   case m of\n\
         \x20       Wrapped cm -> Counter.step cm n\n\
         \x20       Noop -> n\n\
         \n\
         main =\n\
         \x20   println (String.fromInt (apply (Wrapped Counter.Bump) 1))\n"
            .to_string(),
    );
    let errs = type_errors(&root, &[counter, main]);
    assert_eq!(
        errs, 0,
        "OVER-EAGER — `Main.Msg` wrapping `Counter.Msg` was rejected \
         ({errs} errors). This is examples/10-live-component."
    );
}

#[test]
fn twin_union_and_alias_sharing_a_name_are_accepted() {
    let root = repo_root();
    // A UNION `Handle` in one module and a type ALIAS `Handle` in another.
    // Aliases were already module-qualified (#164); unions now are too. The two
    // namespaces must not collide with each other.
    let uni = (
        "Uni",
        "module Uni exposing (..)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         \n\
         type Handle = Opened Int\n\
         \n\
         idOf : Handle -> Int\n\
         idOf h =\n\
         \x20   case h of\n\
         \x20       Opened n -> n\n"
            .to_string(),
    );
    let ali = (
        "Ali",
        "module Ali exposing (..)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         \n\
         type alias Handle = { tag : String }\n\
         \n\
         tagOf : Handle -> String\n\
         tagOf h =\n\
         \x20   h.tag\n"
            .to_string(),
    );
    let main = (
        "Main",
        "module Main exposing (main)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         import Uni\n\
         import Ali\n\
         \n\
         main =\n\
         \x20   println (String.fromInt (Uni.idOf (Uni.Opened 2)) ++ Ali.tagOf { tag = \"t\" })\n"
            .to_string(),
    );
    let errs = type_errors(&root, &[uni, ali, main]);
    assert_eq!(
        errs, 0,
        "OVER-EAGER — a union and an alias sharing the name `Handle` across \
         modules were rejected ({errs} errors)."
    );
}

#[test]
fn twin_one_union_reached_by_two_paths_is_accepted() {
    let root = repo_root();
    // ONE union, reached two ways: imported directly from its declaring module
    // AND through a re-exporting module. These must be the SAME type — this is
    // the case a naive DefId comparison gets wrong (the #164 failure mode, and
    // the reason `TypeKey::Opaque` exists in hir::resolve).
    let decl = (
        "Decl",
        "module Decl exposing (..)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         \n\
         type Status = Live | Dead\n"
            .to_string(),
    );
    let reexp = (
        "Reexp",
        "module Reexp exposing (..)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         import Decl exposing (..)\n\
         \n\
         isLive : Status -> Bool\n\
         isLive s =\n\
         \x20   case s of\n\
         \x20       Live -> True\n\
         \x20       Dead -> False\n"
            .to_string(),
    );
    let main = (
        "Main",
        "module Main exposing (main)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         import Decl\n\
         import Reexp\n\
         \n\
         main =\n\
         \x20   println (if Reexp.isLive Decl.Live then \"y\" else \"n\")\n"
            .to_string(),
    );
    let errs = type_errors(&root, &[decl, reexp, main]);
    assert_eq!(
        errs, 0,
        "OVER-EAGER — one union reached through its declaring module AND a \
         re-exporter was split into two types ({errs} errors)."
    );
}

#[test]
fn twin_qualified_and_bare_reference_to_one_union_are_accepted() {
    let root = repo_root();
    // The SAME union referred to QUALIFIED (`Decl.Status`) in one signature and
    // BARE (via `exposing`) in another. Qualified and bare references must land
    // on one identity, or every `exposing (..)` program breaks.
    let decl = (
        "Decl",
        "module Decl exposing (..)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         \n\
         type Status = Live | Dead\n"
            .to_string(),
    );
    let main = (
        "Main",
        "module Main exposing (main)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         import Decl exposing (..)\n\
         \n\
         viaBare : Status -> Int\n\
         viaBare s =\n\
         \x20   case s of\n\
         \x20       Live -> 1\n\
         \x20       Dead -> 0\n\
         \n\
         viaQual : Decl.Status -> Int\n\
         viaQual s =\n\
         \x20   viaBare s\n\
         \n\
         main =\n\
         \x20   println (String.fromInt (viaQual Live))\n"
            .to_string(),
    );
    let errs = type_errors(&root, &[decl, main]);
    assert_eq!(
        errs, 0,
        "OVER-EAGER — a qualified and a bare reference to ONE union were split \
         into two types ({errs} errors)."
    );
}

#[test]
fn twin_kernel_implicit_type_across_modules_is_accepted() {
    let root = repo_root();
    // `Decoder` is kernel-implicit and reachable from several stdlib modules
    // with no `type` declaration anywhere — the exact collision the `goty.rs`
    // `("Decoder", _) => GoTy::Any` band-aid works around. It must NOT get
    // module-qualified, or a decoder built in one module stops matching a
    // decoder consumed in another.
    let mk = (
        "Mk",
        "module Mk exposing (..)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Json.Decode as Decode\n\
         \n\
         intDec : Decode.Decoder Int\n\
         intDec =\n\
         \x20   Decode.int\n"
            .to_string(),
    );
    let main = (
        "Main",
        "module Main exposing (main)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         import Std.Json.Decode as Decode\n\
         import Mk\n\
         \n\
         run : Decode.Decoder Int -> String -> String\n\
         run d s =\n\
         \x20   case Decode.decodeString d s of\n\
         \x20       Ok n -> String.fromInt n\n\
         \x20       Err _e -> \"err\"\n\
         \n\
         main =\n\
         \x20   println (run Mk.intDec \"1\")\n"
            .to_string(),
    );
    let errs = type_errors(&root, &[mk, main]);
    assert_eq!(
        errs, 0,
        "OVER-EAGER — a kernel-implicit `Decoder` crossing a module boundary \
         was rejected ({errs} errors)."
    );
}

#[test]
fn twin_alias_of_a_union_across_modules_is_accepted() {
    let root = repo_root();
    // An ALIAS whose right-hand side is a union declared in a THIRD module.
    // Alias expansion has to produce the union's qualified identity, not a bare
    // name that then fails to meet the union's own declaration.
    let decl = (
        "Decl",
        "module Decl exposing (..)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         \n\
         type Status = Live | Dead\n"
            .to_string(),
    );
    let ali = (
        "Ali",
        "module Ali exposing (..)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         import Decl\n\
         \n\
         type alias State = Decl.Status\n"
            .to_string(),
    );
    let main = (
        "Main",
        "module Main exposing (main)\n\
         \n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         import Decl\n\
         import Ali\n\
         \n\
         useState : Ali.State -> Int\n\
         useState s =\n\
         \x20   case s of\n\
         \x20       Decl.Live -> 1\n\
         \x20       Decl.Dead -> 0\n\
         \n\
         main =\n\
         \x20   println (String.fromInt (useState Decl.Live))\n"
            .to_string(),
    );
    let errs = type_errors(&root, &[decl, ali, main]);
    assert_eq!(
        errs, 0,
        "OVER-EAGER — an alias of a cross-module union was rejected \
         ({errs} errors)."
    );
}
