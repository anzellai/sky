//! A Sky.Spa split refuses a program that uses `Std.Ui.Terminal`, naming the
//! reason and the target that works.
//!
//! The terminal's output loop sends `Cmd.toIsland` from the branch that reads
//! the PTY, which the split makes a server branch, and Sky.Spa does not deliver
//! a widget command from a server branch. Without the refusal the split built
//! a page whose terminal never printed anything.

use project::spa_split;
use std::path::PathBuf;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(
            dir.pop(),
            "could not locate repo root (no sky-stdlib ancestor)"
        );
    }
}

#[test]
fn a_terminal_app_is_refused_by_the_spa_split() {
    let out = std::env::temp_dir().join(format!("sky-spa-terminal-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&out);
    let err = spa_split::generate(
        &repo_root(),
        &repo_root().join("rust/crates/sky/tests/fixtures/ui-terminal"),
        None,
        &out,
        None,
        None,
    )
    .err()
    .expect("a Std.Ui.Terminal app must not split for Sky.Spa");
    assert!(
        err.contains("Std.Ui.Terminal is not available on a Sky.Spa target")
            && err.contains("--target web"),
        "the refusal must name the module and the target that works; got: {err}"
    );
    let _ = std::fs::remove_dir_all(&out);
}

#[test]
fn a_canvas_app_still_splits() {
    let out = std::env::temp_dir().join(format!("sky-spa-canvas-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&out);
    spa_split::generate(
        &repo_root(),
        &repo_root().join("rust/crates/sky/tests/fixtures/ui-canvas"),
        None,
        &out,
        None,
        None,
    )
    .unwrap_or_else(|e| panic!("a Std.Ui.Canvas app must split for Sky.Spa: {e}"));
    let _ = std::fs::remove_dir_all(&out);
}
