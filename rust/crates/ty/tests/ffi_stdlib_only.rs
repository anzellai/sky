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
