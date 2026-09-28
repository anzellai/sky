//! v0.27.0: `sky check` enforces the `Result Error a` return of a Go-FFI
//! binding, end to end through the real CLI.
//!
//! Before this change every Go-FFI reference type-checked as a free type
//! variable. `probe : Int` / `probe = Hex.encodedLen 3` passed `sky check`, the
//! lowering narrowed the wrapper's `SkyResult` with `rt.AsInt`, and the program
//! crashed at run time. The binding's pinned `skyType` now types the call.
//!
//! Two legs:
//! 1. The tracked `extdeps` uuid surface (the LSP fixture — no Go needed, the
//!    check halts before `go build`): the probe is rejected with `[E2001]` and
//!    the FFI hint.
//! 2. Real Go-stdlib bindings (`encoding/hex`, `strings`; `sky install` needs no
//!    network for the standard library): the probe is rejected, and a corrected
//!    program — Result pipelines, partial application, an FFI function passed as
//!    a value, and a real `Err` from Go — builds and runs.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn go_on_path() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-ffi-result-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    dir
}

fn run(dir: &Path, prog: &str, args: &[&str]) -> (bool, String) {
    let out = Command::new(prog)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), s)
}

fn assert_ffi_result_rejection(log: &str) {
    assert!(
        log.contains("[E2001]"),
        "the ignored FFI Result must be a type error:\n{log}"
    );
    assert!(
        log.contains("Result Error a"),
        "the diagnostic must carry the FFI Result hint:\n{log}"
    );
}

#[test]
fn pinned_uuid_surface_rejects_an_ignored_result() {
    let dir = scratch("uuid");
    let fx = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../sky-lsp/tests/fixtures/extdeps");
    std::fs::create_dir_all(dir.join("sky-ffi/go")).unwrap();
    std::fs::copy(
        fx.join("uuid.kernel.json"),
        dir.join("sky-ffi/uuid.kernel.json"),
    )
    .unwrap();
    std::fs::copy(
        fx.join("uuid_bindings.go"),
        dir.join("sky-ffi/go/uuid_bindings.go"),
    )
    .unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"ffi-result-uuid\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [\"go.dependencies\"]\n\"github.com/google/uuid\" = \"latest\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
         import Github.Com.Google.Uuid as Uuid\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\n\
         probe : String\n\
         probe =\n    Uuid.newString ()\n\n\n\
         main =\n    println probe\n",
    )
    .unwrap();
    let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
    assert!(!ok, "sky check must reject the ignored FFI Result:\n{log}");
    assert_ffi_result_rejection(&log);
    let _ = std::fs::remove_dir_all(&dir);
}

const GOOD: &str = "module Main exposing (main)

import Encoding.Hex as Hex
import Sky.Core.List as List
import Sky.Core.Prelude exposing (..)
import Sky.Core.Result as Result
import Sky.Core.String as String
import Std.Log exposing (println)
import Strings


twice =
    Strings.repeat \"ab\"


main =
    let
        reps =
            List.map twice [ 1, 2 ]

        lens =
            List.map Hex.encodedLen [ 1, 2 ]

        bad =
            case Hex.decodeString \"zz\" of
                Ok _ ->
                    \"unexpected-ok\"

                Err e ->
                    \"err:\" ++ String.left 3 (errorToString e)

        good =
            Hex.encodedLen 5
                |> Result.map (\\n -> n * 10)
                |> Result.withDefault 0
    in
    println
        (String.join \",\" (List.map (Result.withDefault \"?\") reps)
            ++ \"|\"
            ++ String.join \",\" (List.map (\\r -> String.fromInt (Result.withDefault 0 r)) lens)
            ++ \"|\"
            ++ bad
            ++ \"|\"
            ++ String.fromInt good
        )
";

#[test]
fn go_stdlib_bindings_enforce_the_result_and_run() {
    if !required(Need::Go, go_on_path()) {
        return;
    }
    let dir = scratch("std");
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"ffi-result-std\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [\"go.dependencies\"]\n\"encoding/hex\" = \"latest\"\n\"strings\" = \"latest\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), GOOD).unwrap();
    let (ok, log) = run(&dir, SKY, &["install"]);
    assert!(ok, "sky install (encoding/hex, strings) failed:\n{log}");

    // The probe: an FFI Result used as its bare payload.
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
         import Encoding.Hex as Hex\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Std.Log exposing (println)\n\n\n\
         probe : Int\n\
         probe =\n    Hex.encodedLen 3\n\n\n\
         main =\n    println (String.fromInt probe)\n",
    )
    .unwrap();
    let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
    assert!(!ok, "sky check must reject the ignored FFI Result:\n{log}");
    assert_ffi_result_rejection(&log);

    // The corrected program builds and runs.
    std::fs::write(dir.join("src/Main.sky"), GOOD).unwrap();
    let (ok, log) = run(&dir, SKY, &["build", "src/Main.sky"]);
    assert!(ok, "sky build failed:\n{log}");
    let app = dir.join("sky-out").join("app");
    let (ok, out) = run(&dir, app.to_str().unwrap(), &[]);
    assert!(ok, "the app must run without a panic:\n{out}");
    // repeat "ab" 1/2; EncodedLen 1/2 = 2/4; a real Go error; EncodedLen 5 = 10, times 10.
    assert!(
        out.contains("ab,abab|2,4|err:") && out.contains("|100"),
        "partial application, FFI values, an FFI Err and a pipeline must all work; got:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// `Ffi.kernel` is stdlib-only (`[E1011]`), end to end through the real CLI.
/// The reproduction below type-checked and then panicked at run time with a
/// TypeMismatch: the checker trusted the annotation `String -> Int` and never
/// compared it to `Crypto_sha256`'s real `String -> String`. So did the same
/// binding in a project module named into the reserved `Sky.*` namespace.
/// No Go toolchain needed: the check halts before `go build`.
#[test]
fn app_code_ffi_kernel_is_rejected_by_sky_check() {
    let probe = "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Sky.Ffi as Ffi\n\
         import Std.Log exposing (println)\n\n\n\
         probe : String -> Int\n\
         probe =\n    Ffi.kernel \"Crypto_sha256\"\n\n\n\
         main =\n    println (String.fromInt (probe \"abc\" + 1))\n";
    let dir = scratch("kernel");
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"ffi-kernel\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), probe).unwrap();
    let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
    assert!(!ok, "sky check must reject app-code Ffi.kernel:\n{log}");
    assert!(log.contains("[E1011]"), "expected [E1011]:\n{log}");
    assert!(
        log.contains("`Crypto.sha256`"),
        "the hint must name the typed stdlib function:\n{log}"
    );

    // The same binding in a module declared into the reserved namespace.
    std::fs::create_dir_all(dir.join("src/Sky/Evil")).unwrap();
    std::fs::write(
        dir.join("src/Sky/Evil/Coerce.sky"),
        "module Sky.Evil.Coerce exposing (probe)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Ffi as Ffi\n\n\n\
         probe : String -> Int\n\
         probe =\n    Ffi.kernel \"Crypto_sha256\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Sky.Evil.Coerce exposing (probe)\n\
         import Std.Log exposing (println)\n\n\n\
         main =\n    println (String.fromInt (probe \"abc\" + 1))\n",
    )
    .unwrap();
    let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
    assert!(!ok, "a reserved-namespace module is app code:\n{log}");
    assert!(
        log.contains("[E1011]") && log.contains("Coerce.sky"),
        "expected [E1011] in Sky/Evil/Coerce.sky:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
