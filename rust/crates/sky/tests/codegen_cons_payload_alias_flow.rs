//! Regression for two codegen shapes that passed `sky check` but failed
//! `go build` (a check ≡ build violation), both found while building the
//! Sky.Spa follow-up decoder (2026-09-23):
//!
//! * `Ok (m :: ms)` in a `Result Error (List Msg)` slot. `x :: xs` lowers to
//!   `rt.List_cons`, which returns `any`, and the `Ok`/`Err`/`Just` constructor
//!   passed that `any` straight into its concrete payload type param
//!   (`rt.Ok[E, []Main_Msg](rt.List_cons(…))` — "need type assertion"). Fixed in
//!   `lower/src/lower.rs` (`ctor_emit`): an `any` argument into a concrete
//!   payload is narrowed; every argument that already compiled is untouched.
//! * `alias2 3 4` where `alias2 = add2` aliases an unannotated function-valued
//!   binding. The alias's own inference read the target as a bare var, so the
//!   caller saw `any` and emitted a curried call on it (`Main_alias2()(3)(4)`).
//!   Fixed in `lower/src/lower.rs` (`def_result_tys`): a zero-parameter alias
//!   takes its target's result type, matching the Go func `lower_def` emits.
//!
//! Needs a `go` toolchain.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let dest = to.join(entry.file_name());
        if entry.file_type().unwrap().is_dir() {
            copy_dir(&entry.path(), &dest);
        } else {
            std::fs::copy(entry.path(), dest).unwrap();
        }
    }
}

fn stage_fixture() -> PathBuf {
    let fixture =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/codegen-cons-payload-alias");
    let dir = std::env::temp_dir().join(format!(
        "sky-cons-alias-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    copy_dir(&fixture, &dir);
    dir
}

#[test]
fn cons_payload_and_function_alias_go_build_and_run() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky build");
    let mut log = String::from_utf8_lossy(&out.stdout).into_owned();
    log.push_str(&String::from_utf8_lossy(&out.stderr));
    assert!(out.status.success(), "sky build failed:\n{log}");

    let run = Command::new(dir.join("sky-out/app"))
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run the built app");
    let stdout = String::from_utf8_lossy(&run.stdout).into_owned();
    assert!(
        run.status.success(),
        "app failed: {stdout}{}",
        String::from_utf8_lossy(&run.stderr)
    );
    assert_eq!(
        stdout.trim(),
        "3 7",
        "`Ok (m :: ms)` and a two-argument call through a function alias must build and run"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
