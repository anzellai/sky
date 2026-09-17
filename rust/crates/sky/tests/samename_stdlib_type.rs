//! Regression: a PROJECT type alias whose name shares its tail with a stdlib
//! type in the compile set must NOT be hijacked by that stdlib type in the
//! emitted Go.
//!
//! Found building a real app: a project `type alias Message = { author, body }`
//! used in a model field `messages : List Message`. Every `Std.App` build pulls
//! `Std.Ai.Provider` (whose `Message = { role, content }`) into the compile set
//! transitively, and the record-alias field expansion consulted a BARE,
//! first-writer-wins alias table (stdlib loads first). So `messages` silently
//! lowered to `[]Std_Ai_Provider_Message_R` — the stdlib record's shape — while
//! the view/update kept `Main_Message_R`, an inconsistency that renders wrong
//! JSON (the field keys become `role`/`content`) and mislabels the struct.
//!
//! The fix expands record-alias field types in the DECLARING module's view
//! (`World::expand_ty_in_module`), so a module's own type shadows a same-named
//! stdlib alias. This locks it: the emitted Model field must be `Main_Message_R`,
//! AND the build must genuinely contain `Std_Ai_Provider_Message_R` (proving the
//! collision condition is present, so the test can never pass vacuously).
//!
//! D1 (type-reference resolution) of
//! `docs/rust-rewrite/13-change-verification-and-edge-cases.md`.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

static BUILD_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/samename-stdlib-type")
}

fn scratch() -> PathBuf {
    std::env::temp_dir().join(format!(
        "sky-samename-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}

fn copy_tree(src: &Path, dst: &Path) {
    std::fs::create_dir_all(dst).unwrap();
    for entry in std::fs::read_dir(src).unwrap() {
        let entry = entry.unwrap();
        let n = entry.file_name();
        if matches!(
            n.to_string_lossy().as_ref(),
            ".skyapp" | "sky-out" | ".skycache" | ".skydeps" | "dist"
        ) {
            continue;
        }
        let from = entry.path();
        let to = dst.join(&n);
        if from.is_dir() {
            copy_tree(&from, &to);
        } else {
            std::fs::copy(&from, &to).unwrap();
        }
    }
}

fn find_main_go(root: &Path) -> Option<String> {
    for entry in std::fs::read_dir(root).ok()? {
        let p = entry.ok()?.path();
        if p.is_dir() {
            if let Some(s) = find_main_go(&p) {
                return Some(s);
            }
        } else if p.file_name().and_then(|n| n.to_str()) == Some("main.go") {
            if let Ok(s) = std::fs::read_to_string(&p) {
                // The app's own module, not the embedded console app.
                if s.contains("Main_Model_R") {
                    return Some(s);
                }
            }
        }
    }
    None
}

#[test]
fn project_type_alias_shadows_a_same_named_stdlib_type_in_emitted_go() {
    let _lock = BUILD_LOCK.lock().unwrap_or_else(|p| p.into_inner());

    // The emitted Go is what carries the defect, so this needs the Go toolchain.
    if !required(Need::Go, have_go()) {
        return;
    }

    let proj = scratch();
    let _ = std::fs::remove_dir_all(&proj);
    copy_tree(&fixture_dir(), &proj);

    let output = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&proj)
        .output()
        .expect("run sky build on the samename-stdlib-type fixture");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(output.status.success(), "fixture must build:\n{log}");

    let go = find_main_go(&proj.join(".skyapp")).unwrap_or_default();
    assert!(!go.is_empty(), "emitted main.go with Main_Model_R must exist:\n{log}");

    // The collision condition must be present, or the test is vacuous: the stdlib
    // `Std_Ai_Provider_Message_R` must be in the compile set.
    assert!(
        go.contains("Std_Ai_Provider_Message_R"),
        "the collision condition is absent (Std.Ai not in the compile set); the \
         test would be vacuous — the fixture no longer exercises the bug"
    );

    // Isolate the `Main_Model_R` struct definition — a stdlib type
    // (`Std_Ai_Agent_Input_R`) legitimately carries its own
    // `Messages []Std_Ai_Provider_Message_R` field, so the check must be scoped
    // to the app's Model struct, not any `Messages []` field in the file.
    let model_line = go
        .lines()
        .find(|l| l.contains("type Main_Model_R struct"))
        .unwrap_or_else(|| panic!("emitted Go must define `type Main_Model_R struct`:\n(not found)"))
        .to_string();

    // The Model's `messages` field must use the PROJECT's own `Main_Message_R`,
    // never the stdlib `Std_Ai_Provider_Message_R`.
    assert!(
        model_line.contains("Messages []Main_Message_R"),
        "the project's own `Message` must win: the Model field must be \
         `[]Main_Message_R`, not the stdlib `[]Std_Ai_Provider_Message_R`:\n{model_line}"
    );
    assert!(
        !model_line.contains("Std_Ai_Provider_Message_R"),
        "the Model field must NOT be hijacked by the same-tailed stdlib alias:\n{model_line}"
    );

    let _ = std::fs::remove_dir_all(&proj);
}
