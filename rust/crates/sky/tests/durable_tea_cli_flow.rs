//! Behavioural coverage for zero-annotation durable TEA — `App.withDurableId` on
//! a `Std.App` Cli app, end to end, fully offline.
//!
//! The `durable-cli-counter` fixture is a plain counter with NO durable code in
//! its model / msg / update — durability comes only from the one
//! `App.withDurableId "t" db modelCodec` builder line. The runtime restores the
//! model on start and snapshots it after each update. This test builds the
//! binary once, then runs it twice against the SAME sqlite database:
//!
//!   - Run 1 feeds two lines, so `update` fires twice: the view prints
//!     `count: 0`, `count: 1`, `count: 2`, and count 2 is persisted.
//!   - Run 2 feeds one line against the same db + run id "t": the model is
//!     restored to 2 (the first view already prints `count: 2`, not `count: 0`)
//!     then bumped to `count: 3`.
//!
//! So the last count of run 1 (2) is the first count of run 2 — state survives
//! the process restart with zero annotation on the user's TEA. Needs a `go`
//! toolchain.

use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn stage_fixture() -> PathBuf {
    let fixture =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/durable-cli-counter");
    let dir = std::env::temp_dir().join(format!(
        "sky-durable-cli-{}-{}",
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

/// Run the built binary, feeding `lines` on stdin, and return combined output.
fn run_binary(app: &Path, cwd: &Path, lines: &str) -> String {
    use std::io::Write;
    let mut child = Command::new(app)
        .current_dir(cwd)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("spawn durable-cli-counter binary");
    child
        .stdin
        .take()
        .unwrap()
        .write_all(lines.as_bytes())
        .unwrap();
    let out = child.wait_with_output().expect("wait binary");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    s
}

#[test]
// T1 tier-budget: this does a full `go build` of the fixture plus two binary
// runs (~16s), enough to tip the per-commit `test-sky` shard over its 990s T1
// ceiling (the same reason `ai_memory_pg_flow` was moved off it at v0.25.0).
// Raising the ceiling is forbidden, so it runs NIGHTLY via `cargo test -p sky --
// --ignored` (nightly-sweep.yml). Per-commit durable coverage stays: the
// snapshot SUBSTRATE is proven behaviourally by `durable_snapshot_flow.rs` and
// the App wiring by the `durable_tea_app_check.rs` compile guard, both per-commit
// and both sharing the `durable_tea.go` path this test exercises end to end.
// Remove `#[ignore]` to re-arm per-commit only alongside a matching T1 budget cut.
#[ignore = "heavy go-build+run e2e leg; runs nightly (--ignored). Per-commit legs: durable_snapshot_flow.rs + durable_tea_app_check.rs"]
fn durable_cli_model_survives_a_restart_with_zero_annotation() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();

    // Build once. `sky build` on a terminal:cli target writes the binary under
    // .skyapp/terminal-cli/sky-out/app.
    let build = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&dir)
        .stdin(Stdio::null())
        .output()
        .expect("spawn sky build");
    let build_log = format!(
        "{}{}",
        String::from_utf8_lossy(&build.stdout),
        String::from_utf8_lossy(&build.stderr)
    );
    assert!(
        build.status.success(),
        "durable-cli-counter should build:\n{build_log}"
    );
    let app = dir.join(".skyapp/terminal-cli/sky-out/app");
    assert!(app.exists(), "expected built binary at {app:?}\n{build_log}");

    // Run 1 — two bumps. Fresh db.
    let run1 = run_binary(&app, &dir, "x\nx\n");
    assert!(
        run1.contains("count: 0") && run1.contains("count: 1") && run1.contains("count: 2"),
        "run 1 should count 0 -> 1 -> 2 and persist 2; got:\n{run1}"
    );

    // Run 2 — same db + run id. The model restores to 2, so the FIRST view of
    // run 2 already reads `count: 2` (not `count: 0`), then one bump -> 3.
    let run2 = run_binary(&app, &dir, "x\n");
    assert!(
        run2.contains("count: 2"),
        "run 2 should RESTORE the persisted model (first view count: 2, not count: 0); got:\n{run2}"
    );
    assert!(
        run2.contains("count: 3"),
        "run 2 should bump the restored model to count: 3; got:\n{run2}"
    );
    assert!(
        !run2.starts_with("count: 0"),
        "run 2 must not start from a fresh model (count: 0) — that would mean no restore; got:\n{run2}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
