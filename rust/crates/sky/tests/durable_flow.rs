//! End-to-end coverage for `Std.Durable` — durable step-journalled workflows.
//!
//! The `durable-checkout` fixture runs a workflow whose `charge` step inserts one
//! row into a side table, suspends at an `awaitSignal`, then resumes after the
//! signal. On resume the `charge` step is REPLAYED from the journal, so the side
//! table must still hold exactly one row. The fixture prints:
//!
//!     poll1 charges=1
//!     poll2 charges=1 status=done
//!
//! and this test asserts both. Two properties, both the point of the module:
//!   * exactly-once — `charges` stays 1 across the suspend/resume, so the step's
//!     effect did not re-run on replay.
//!   * durable completion — a run that suspended on a signal resumes and reaches
//!     `done` after the signal is delivered.
//!
//! Needs a `go` toolchain (it builds the emitted Go). When absent the live gate
//! FAILS naming what to install; `SKY_LIVE_TESTS=skip` is the one opt-out.

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

/// Copy the fixture (sky.toml + src/) into a fresh temp dir so the run starts
/// from an empty database and leaves no artefacts in the source tree.
fn stage_fixture() -> PathBuf {
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/durable-checkout");
    let dir = std::env::temp_dir().join(format!(
        "sky-durable-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::copy(fixture.join("sky.toml"), dir.join("sky.toml")).unwrap();
    copy_dir(&fixture.join("src"), &dir.join("src"));
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
fn durable_workflow_suspends_resumes_and_runs_each_step_once() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();
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
        "durable fixture exited non-zero:\n{stdout}"
    );
    // poll #1 ran `charge` once and suspended at the signal.
    assert!(
        stdout.contains("poll1 charges=1"),
        "the charge step should have run exactly once before the suspend:\n{stdout}"
    );
    // poll #2 resumed: `charge` REPLAYED from the journal (still one row), the
    // run finalised and reached `done`.
    assert!(
        stdout.contains("poll2 charges=1 status=done"),
        "on resume the charge step must NOT re-run (exactly-once), and the run \
         must complete; expected `poll2 charges=1 status=done`:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
