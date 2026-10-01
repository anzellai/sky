//! `[E1011]`: `Sky.Ffi` is stdlib-only, `Ffi.kernel` included.
//!
//! `Ffi.kernel "Sym"` binds a runtime kernel and trusts the def's annotation
//! without comparing it to the kernel's real signature. In app code that was a
//! hole: `probe : String -> Int = Ffi.kernel "Crypto_sha256"` passed `sky check`
//! and panicked at run time with a TypeMismatch. The checker now rejects every
//! `Sky.Ffi` member in a checked module unless the build grants it
//! (`hir::FfiTrust`):
//!
//! * no module is exempt by its NAME — a project module declared
//!   `module Sky.Evil` is app code (it used to pass, because the scan skipped
//!   the reserved `Sky.*` / `Std.*` namespace);
//! * a module in `FfiTrust::modules` (the build puts the compiler's own
//!   bundled-app source there, matched by content) gets full `Sky.Ffi`;
//! * a kernel prefix in `FfiTrust::kernel_prefixes` (a Sky.Spa-generated
//!   project: `Spa_`) opens `Ffi.kernel "<prefix>…"` and nothing else.

use hir::{FfiSurface, FfiTrust, SourceDb};
use std::path::PathBuf;
use std::sync::OnceLock;

fn stdlib() -> &'static Vec<(String, syntax::Parse)> {
    static S: OnceLock<Vec<(String, syntax::Parse)>> = OnceLock::new();
    S.get_or_init(|| {
        let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        while !dir.join("sky-stdlib").is_dir() {
            assert!(dir.pop(), "no sky-stdlib ancestor");
        }
        ty::reject_corpus::load_stdlib(&dir)
    })
}

/// Check `modules` (`(name, source)`, all checked) under `trust`; return every
/// error-severity diagnostic as `(code, message, suggestion)`.
fn check(modules: &[(&str, &str)], trust: FfiTrust) -> Vec<(String, String, String)> {
    let mut db = SourceDb::new();
    for (n, parse) in stdlib() {
        db.add_module(n, parse.clone());
    }
    let mut surface = FfiSurface::new();
    surface.set_trust(trust);
    db.set_ffi_surface(std::sync::Arc::new(surface));
    let ids: Vec<_> = modules
        .iter()
        .map(|(n, src)| db.add_module(n, syntax::parse(src, base::FileId(0))))
        .collect();
    ty::check_modules(&db, &ids)
        .diagnostics
        .iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .map(|d| {
            (
                d.code.0.clone(),
                d.message.clone(),
                d.suggestion.clone().unwrap_or_default(),
            )
        })
        .collect()
}

fn codes(diags: &[(String, String, String)]) -> Vec<String> {
    diags.iter().map(|(c, _, _)| c.clone()).collect()
}

/// The Judge's reproduction, verbatim.
const PROBE: &str = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Ffi as Ffi
import Std.Log exposing (println)


probe : String -> Int
probe =
    Ffi.kernel \"Crypto_sha256\"


main =
    println (String.fromInt (probe \"abc\" + 1))
";

#[test]
fn app_code_ffi_kernel_is_rejected_with_e1011() {
    let d = check(&[("Main", PROBE)], FfiTrust::default());
    assert_eq!(codes(&d), vec!["E1011".to_string()], "{d:?}");
    let (_, msg, hint) = &d[0];
    assert!(
        msg.contains("`Ffi.kernel` is not exposed to application code"),
        "{msg}"
    );
    // The hint names the typed stdlib function that wraps this kernel.
    assert!(hint.contains("`Crypto.sha256`"), "{hint}");
    // And ends with the migration guide's anchor.
    assert!(
        hint.ends_with("see docs/migration/v0.27.md#sky-ffi-is-stdlib-only"),
        "{hint}"
    );
}

#[test]
fn the_hint_names_no_stdlib_function_for_a_kernel_the_stdlib_does_not_wrap() {
    let src = PROBE.replace("Crypto_sha256", "Hub_readOverview");
    let d = check(&[("Main", &src)], FfiTrust::default());
    assert_eq!(codes(&d), vec!["E1011".to_string()], "{d:?}");
    assert!(!d[0].2.contains("that is"), "{}", d[0].2);
}

#[test]
fn an_unapplied_or_non_literal_ffi_kernel_is_rejected() {
    let src = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Ffi as Ffi
import Std.Log exposing (println)


bind : String -> a
bind name =
    Ffi.kernel name


main =
    println (bind \"String_toUpper\" \"x\")
";
    // Even under a `Spa_` grant: the symbol is not a literal, so it is unknown.
    let trust = FfiTrust {
        kernel_prefixes: ["Spa_".to_string()].into_iter().collect(),
        ..FfiTrust::default()
    };
    let d = check(&[("Main", src)], trust);
    assert!(codes(&d).contains(&"E1011".to_string()), "{d:?}");
}

#[test]
fn a_module_in_the_reserved_namespace_is_not_exempt_by_its_name() {
    let evil = "\
module Sky.Evil.Coerce exposing (probe)

import Sky.Core.Prelude exposing (..)
import Sky.Ffi as Ffi


probe : String -> Int
probe =
    Ffi.callPure \"Crypto_sha256\"
";
    let evil_kernel = evil
        .replace("Sky.Evil.Coerce", "Std.Evil.Kernel")
        .replace("Ffi.callPure", "Ffi.kernel");
    let main = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Evil.Coerce as A
import Std.Evil.Kernel as B
import Std.Log exposing (println)


main =
    println (String.fromInt (A.probe \"abc\" + B.probe \"abc\"))
";
    let d = check(
        &[
            ("Sky.Evil.Coerce", evil),
            ("Std.Evil.Kernel", &evil_kernel),
            ("Main", main),
        ],
        FfiTrust::default(),
    );
    assert_eq!(
        codes(&d),
        vec!["E1011".to_string(), "E1011".to_string()],
        "{d:?}"
    );
}

#[test]
fn a_trusted_module_keeps_full_sky_ffi() {
    // The build grants this to the compiler's own bundled-app source.
    let lib = "\
module DocCatalog exposing (loadCatalog)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Sky.Ffi as Ffi


loadCatalog : String -> Result Error Int
loadCatalog =
    Ffi.kernel \"Doc_loadCatalog\"
";
    let main = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Result as Result
import Sky.Core.String as String
import DocCatalog
import Std.Log exposing (println)


main =
    println (String.fromInt (Result.withDefault 0 (DocCatalog.loadCatalog \"x\")))
";
    let trust = FfiTrust {
        modules: ["DocCatalog".to_string()].into_iter().collect(),
        ..FfiTrust::default()
    };
    let d = check(&[("DocCatalog", lib), ("Main", main)], trust);
    assert!(d.is_empty(), "{d:?}");
    // The grant is per module: the same binding in `Main` is still rejected.
    let untrusted = check(&[("DocCatalog", lib), ("Main", main)], FfiTrust::default());
    assert_eq!(
        codes(&untrusted),
        vec!["E1011".to_string()],
        "{untrusted:?}"
    );
}

#[test]
fn a_kernel_prefix_grant_opens_only_that_prefix_and_only_ffi_kernel() {
    let src = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Ffi as Ffi
import Std.Log exposing (println)


spaWasmNameBuilt : String -> String -> String
spaWasmNameBuilt =
    Ffi.kernel \"Spa_ssrWasmNameBuilt\"


main =
    println (spaWasmNameBuilt \"\" \"dist\")
";
    let spa = || FfiTrust {
        kernel_prefixes: ["Spa_".to_string()].into_iter().collect(),
        ..FfiTrust::default()
    };
    assert!(check(&[("Main", src)], spa()).is_empty());
    // Another prefix under the same grant is still app code.
    let other = src.replace("Spa_ssrWasmNameBuilt", "Crypto_sha256");
    assert_eq!(codes(&check(&[("Main", &other)], spa())), vec!["E1011"]);
    // The prefix grant never opens `Ffi.callPure`.
    let call = src.replace("Ffi.kernel", "Ffi.callPure");
    assert_eq!(codes(&check(&[("Main", &call)], spa())), vec!["E1011"]);
}

#[test]
fn stdlib_use_of_ffi_kernel_is_accepted() {
    // The stdlib binds kernels with `Ffi.kernel` throughout; importing and
    // calling one of those typed functions is ordinary app code.
    let src = "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Crypto as Crypto
import Std.Log exposing (println)


main =
    println (Crypto.sha256 \"abc\")
";
    assert!(check(&[("Main", src)], FfiTrust::default()).is_empty());
}

// ---- `Sky.Ffi` plumbing is reachable ONLY as a `Sky.Ffi` member ----------
//
// The `[E1011]` scan used to match only `Res::Kernel { module: "Ffi", .. }`,
// while lowering bound `func == "kernel"` from ANY kernel pseudo-module. A
// pseudo with no static member list (`Webview`, `Live`, `Tui`, `Cli`, `Jobs`)
// resolved an unknown member leniently, so `Webview.kernel "Crypto_sha256"`
// passed `sky check` with no `Webview` import and the binary panicked with a
// TypeMismatch. Every route below must now be refused with `[E1011]`.

/// A module whose `probe : String -> Int` is bound by `body`, with `imports`.
fn route(imports: &str, body: &str) -> String {
    format!(
        "\
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Std.Log exposing (println)
{imports}


probe : String -> Int
probe =
{body}


main =
    println (String.fromInt (probe \"abc\" + 1))
"
    )
}

fn assert_e1011(imports: &str, body: &str) {
    let src = route(imports, body);
    let d = check(&[("Main", &src)], FfiTrust::default());
    assert!(
        codes(&d).contains(&"E1011".to_string()),
        "expected [E1011] for\n{src}\ngot {d:?}"
    );
}

#[test]
fn the_judges_case_webview_kernel_is_rejected_with_e1011() {
    let src = route("", "    Webview.kernel \"Crypto_sha256\"");
    let d = check(&[("Main", &src)], FfiTrust::default());
    assert_eq!(codes(&d), vec!["E1011".to_string()], "{d:?}");
    assert!(
        d[0].1.contains("`Webview.kernel`") && d[0].1.contains("`Sky.Ffi`"),
        "{}",
        d[0].1
    );
    assert!(
        d[0].2
            .ends_with("see docs/migration/v0.27.md#sky-ffi-is-stdlib-only"),
        "{}",
        d[0].2
    );
}

#[test]
fn ffi_plumbing_through_any_other_kernel_qualifier_is_rejected() {
    for q in [
        "Webview", "Live", "Tui", "Cli", "Jobs", "Crypto", "Fmt", "Basics",
    ] {
        for m in ["kernel", "call", "callPure", "callTask"] {
            assert_e1011("", &format!("    {q}.{m} \"Crypto_sha256\""));
        }
    }
}

#[test]
fn ffi_plumbing_through_an_aliased_or_opened_kernel_import_is_rejected() {
    assert_e1011("import Webview as W", "    W.kernel \"Crypto_sha256\"");
    // `Std.Webview` is a Sky module: `kernel` is simply not one of its exports.
    let src = route("import Std.Webview as W", "    W.kernel \"Crypto_sha256\"");
    let d = check(&[("Main", &src)], FfiTrust::default());
    assert!(codes(&d).contains(&"E1001".to_string()), "{d:?}");
    assert_e1011(
        "import Webview exposing (..)",
        "    kernel \"Crypto_sha256\"",
    );
    assert_e1011(
        "import Webview exposing (kernel)",
        "    kernel \"Crypto_sha256\"",
    );
    assert_e1011(
        "import Fmt exposing (callPure)",
        "    callPure \"Crypto_sha256\"",
    );
    assert_e1011(
        "import Webview exposing (..)\nimport Sky.Ffi exposing (..)",
        "    kernel \"Crypto_sha256\"",
    );
}

#[test]
fn every_sky_ffi_route_to_a_kernel_is_rejected() {
    assert_e1011(
        "import Sky.Ffi exposing (..)",
        "    kernel \"Crypto_sha256\"",
    );
    assert_e1011(
        "import Sky.Ffi exposing (kernel)",
        "    kernel \"Crypto_sha256\"",
    );
    assert_e1011("import Sky.Ffi as F", "    F.kernel \"Crypto_sha256\"");
    // No import at all: `Ffi` is an ambient kernel qualifier.
    assert_e1011("", "    Ffi.kernel \"Crypto_sha256\"");
    // A let-bound alias, a record field, a partial application and a value
    // passed to a function: the reference itself is refused, applied or not.
    assert_e1011(
        "",
        "    let\n        k =\n            Ffi.kernel\n    in\n    k \"Crypto_sha256\"",
    );
    assert_e1011(
        "",
        "    let\n        r =\n            { k = Ffi.kernel }\n    in\n    r.k \"Crypto_sha256\"",
    );
    assert_e1011("", "    identity Ffi.kernel \"Crypto_sha256\"");
    assert_e1011("", "    (\\f -> f \"Crypto_sha256\") Ffi.kernel");
    assert_e1011("", "    Ffi.callPure \"Crypto_sha256\"");
    assert_e1011(
        "",
        "    Ffi.callTask \"Crypto_sha256\" |> (\\_ -> String.length)",
    );
}

#[test]
fn a_trusted_module_still_cannot_reach_plumbing_through_another_qualifier() {
    // `FfiTrust::modules` opens `Sky.Ffi`; it does not make `Webview.kernel` a
    // member of `Webview`.
    let src = route("", "    Webview.kernel \"Crypto_sha256\"");
    let trust = FfiTrust {
        modules: ["Main".to_string()].into_iter().collect(),
        ..FfiTrust::default()
    };
    let d = check(&[("Main", &src)], trust);
    assert!(codes(&d).contains(&"E1011".to_string()), "{d:?}");
}

#[test]
fn an_unknown_member_of_an_unlisted_kernel_module_is_refused_at_check() {
    // `Live` / `Webview` / `Tui` / `Cli` / `Jobs` have no static member list.
    // Unimported, an unknown member resolved to a signature-less kernel
    // reference: a fresh type, lowered to `rt.<Mod>_<member>`.
    for q in ["Webview", "Live", "Tui", "Cli", "Jobs"] {
        let src = route("", &format!("    {q}.notAMember"));
        let d = check(&[("Main", &src)], FfiTrust::default());
        assert!(codes(&d).contains(&"E1001".to_string()), "{q}: {d:?}");
    }
    // A real member keeps its stdlib signature, so a wrong annotation is a
    // type error rather than a run-time panic.
    let src = route("", "    Live.address");
    let d = check(&[("Main", &src)], FfiTrust::default());
    assert_eq!(codes(&d), vec!["E2001".to_string()], "{d:?}");
}
