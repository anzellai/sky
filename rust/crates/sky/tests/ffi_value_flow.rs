//! Regression: a Go-FFI function used as a VALUE (v0.25.19).
//!
//! `List.map Hex.encodedLen xs` and `r |> Result.andThen Hex.encodedLen` pass
//! the FFI function itself, not a call of it. Lowering only handled the CALL
//! shape; a bare reference fell through to `nil` with a `foreign ref` warning.
//! `sky build` succeeded and the program panicked (`NilDereference`) the first
//! time the value was applied. `examples/13-skyshop` shipped four such sites
//! (`Result.andThen Stripe.addressLine1`, …) on its Stripe shipping-address
//! path. Found while making lowering refuse to emit `nil` for any unresolved
//! reference.
//!
//! The value now lowers to a curried closure over the FFI wrapper, with each
//! argument narrowed exactly as the call form narrows it. This drives a real
//! Go-stdlib binding (`encoding/hex`, no network needed) end to end: install,
//! build, run, and check the output.

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

fn scratch_project() -> PathBuf {
    let uniq = format!(
        "sky-ffi-value-{}-{}",
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
        "name = \"ffi-value\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [\"go.dependencies\"]\n\"encoding/hex\" = \"latest\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\n\
         import Encoding.Hex as Hex\n\
         import Sky.Core.List as List\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.Result as Result\n\
         import Sky.Core.String as String\n\
         import Std.Log exposing (println)\n\n\n\
         main =\n    \
         let\n        \
         lens =\n            \
         List.map Hex.encodedLen [ 1, 2, 3 ]\n\n        \
         chained =\n            \
         Ok 4 |> Result.andThen Hex.encodedLen\n    \
         in\n    \
         println\n        \
         (String.join \",\"\n            \
         (List.map (\\r -> String.fromInt (Result.withDefault 0 r)) (lens ++ [ chained ]))\n        \
         )\n",
    )
    .unwrap();
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

#[test]
fn ffi_function_used_as_a_value_is_applied_not_nil() {
    if !required(Need::Go, go_on_path()) {
        return;
    }
    let dir = scratch_project();

    let (ok, log) = run(&dir, SKY, &["install"]);
    assert!(ok, "sky install (encoding/hex) failed:\n{log}");

    let (ok, log) = run(&dir, SKY, &["build", "src/Main.sky"]);
    assert!(ok, "sky build failed:\n{log}");
    assert!(
        !log.contains("foreign ref"),
        "the FFI value must lower to its wrapper, not fall through to `nil`:\n{log}"
    );

    let app = dir.join("sky-out").join("app");
    let (ok, out) = run(&dir, app.to_str().unwrap(), &[]);
    assert!(ok, "the app must run without a panic:\n{out}");
    // hex.EncodedLen(n) = 2n.
    assert!(
        out.contains("2,4,6,8"),
        "List.map and Result.andThen over the FFI value must apply it; got:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
