//! `Std.Ai.Device` in the Sky.Spa auto-split.
//!
//! A server arm whose task reaches a server effect AND the device slot runs
//! on the server as a whole: there `Std.Ai.Device` answers as the slot does
//! in Sky.Live. Before v0.28.0 such a follow-up was read as a `Std.Native`
//! client residual, and the split failed ("server branch `Score` returns a
//! Std.Native client effect, but its module does not import Std.Native").
//! A client arm that asks the device alone stays in the client.
//!
//! Fixture: `crates/sky/tests/fixtures/spa-ai-device-mixed`.

use project::{spa_partition, spa_split};
use std::path::PathBuf;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(dir.pop(), "no sky-stdlib ancestor");
    }
}

fn fixture_dir() -> PathBuf {
    repo_root().join("rust/crates/sky/tests/fixtures/spa-ai-device-mixed")
}

#[test]
fn a_server_task_that_asks_the_device_runs_on_the_server() {
    let r = spa_partition::analyze(&repo_root(), &fixture_dir(), None)
        .unwrap_or_else(|e| panic!("analyze failed: {e}"));
    let check = r
        .branches
        .iter()
        .find(|b| b.msg == "Check")
        .expect("no Check branch");
    assert!(check.io.is_some(), "`Check` logs: it is a server branch");
    for f in r.follow_up.iter().filter(|f| f.branch == "Check") {
        assert!(
            !f.native,
            "`Check`'s task runs on the server; it is not a client residual: {f:?}"
        );
    }
    let prepare = r
        .branches
        .iter()
        .find(|b| b.msg == "Prepare")
        .expect("no Prepare branch");
    assert!(
        prepare.io.is_none(),
        "`Prepare` asks the device alone: it stays in the client"
    );
}

#[test]
fn the_split_generates_both_halves() {
    let out = std::env::temp_dir().join(format!("sky-spa-ai-device-mixed-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&out);
    spa_split::generate(&repo_root(), &fixture_dir(), None, &out, None, None)
        .unwrap_or_else(|e| panic!("generate failed: {e}"));
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    assert!(
        front.contains("Device.prepare"),
        "the client runs the device prepare itself"
    );
    let _ = std::fs::remove_dir_all(&out);
}
