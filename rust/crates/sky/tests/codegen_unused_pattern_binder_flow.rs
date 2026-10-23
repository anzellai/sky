//! Regression: a destructured pattern binder that the body never reads became
//! an unused Go variable, so `sky check` type-checked the program and then
//! `go build` failed with `declared and not used: v_N`.
//!
//! Reported by a user app for
//! `List.filter (\( q, a ) -> not (String.isEmpty a)) pairs` (`q` unused).
//! Case arms and `let` destructures already emitted a `_ = v` discard for each
//! binder; lambda parameters and function parameters (top-level and local) did
//! not. The fixture covers tuple, nested tuple, record, constructor and cons
//! patterns in lambdas, case arms, `let` destructures and function parameters.
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

fn stage_fixture() -> PathBuf {
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/codegen-unused-pattern-binders");
    let dir = std::env::temp_dir().join(format!(
        "sky-codegen-unused-binders-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    copy_dir(&fixture, &dir);
    dir
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

#[test]
fn unused_pattern_binders_go_build_and_run() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();
    // `sky run` does `sky check` + `go build` + run. Before the fix `go build`
    // failed with `declared and not used` although `sky check` type-checked.
    let out = Command::new(SKY)
        .args(["run", "src/Main.sky"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky run");
    let mut stdout = String::from_utf8_lossy(&out.stdout).into_owned();
    stdout.push_str(&String::from_utf8_lossy(&out.stderr));

    assert!(
        out.status.success(),
        "unused destructured pattern binders should go-build and run \
         (regression: `declared and not used`); got:\n{stdout}"
    );
    assert!(
        stdout.contains("2,1,3,4,5,6,7,8,9,10,11,12,14,15"),
        "expected `2,1,3,4,5,6,7,8,9,10,11,12,14,15`; got:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
