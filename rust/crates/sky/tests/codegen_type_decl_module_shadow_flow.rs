//! Regression for a codegen type-resolution bug (2026-09-22, found in jokeraces-bot).
//!
//! A project type whose BARE name matches a stdlib type broke the Go build while
//! `sky check`'s type phase passed — an "if it compiles it works" break. A project
//! `type Turn` (module `Core.Turn`) shadowed `Std.Ai.Provider`'s own `Turn` in the
//! EMITTED struct for `Provider.CustomImpl`: the field rendered as
//! `func([]Core_Turn_Turn, …)` while the function literal lowered inside Provider's
//! body correctly had `func([]Std_Ai_Provider_Turn, …)`, so `go build` rejected the
//! struct literal in `Std_Ai_Provider_custom`:
//!
//! ```text
//! cannot use func([]Std_Ai_Provider_Turn, …) … as func([]Core_Turn_Turn, …) value
//! in struct literal
//! ```
//!
//! Cause: `emit_type_decl` (`lower/src/lower.rs`) lowered a declaration's field and
//! constructor-payload types with `cur_mod = None`, so a bare name resolved through
//! the flat, last-writer-wins `nominal` map — landing on whichever module's `Turn`
//! registered last. Function bodies already lower with the declaring module set, so
//! they resolve through `nominal_by_module`. The fix routes declarations the same
//! way (a `module` on `TypeDecl`, passed as `cur_mod` at both emit sites).
//!
//! The provider must stay reachable from `main`, or DCE drops the struct literal and
//! the mismatch never surfaces — hence the `Provider.chat brain []` in `main`. Needs
//! a `go` toolchain.

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
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/type-decl-module-shadow");
    let dir = std::env::temp_dir().join(format!(
        "sky-typedecl-shadow-{}-{}",
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
fn project_type_shadowing_a_stdlib_type_go_builds() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();
    // `sky build` type-checks AND `go build`s the emitted Go. Before the fix the
    // Go build failed on the `Provider.CustomImpl` struct literal because the
    // field type resolved `Turn` to the PROJECT's `Core_Turn_Turn` instead of the
    // stdlib's `Std_Ai_Provider_Turn`.
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky build");
    let mut log = String::from_utf8_lossy(&out.stdout).into_owned();
    log.push_str(&String::from_utf8_lossy(&out.stderr));

    assert!(
        out.status.success(),
        "a project type whose bare name matches a stdlib type must go-build \
         (regression: the emitted struct field named the project type instead of \
         the stdlib one); got:\n{log}"
    );
    assert!(
        !log.contains("in struct literal") && !log.contains("Core_Turn_Turn"),
        "unexpected struct-literal type mismatch in the build output:\n{log}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
