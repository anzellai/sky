//! Coverage for `Durable.saveSnapshot` / `loadSnapshot` — the pure-Sky model
//! snapshot substrate for durable TEA replay, fully offline.
//!
//! The `durable-snapshot` fixture serialises a model with `Std.Codec`, persists
//! it, restores it from the database (as a restart would), continues, and proves
//! write-if-newer (a stale lower-seq snapshot is ignored). It prints:
//!
//!     snapshot restored=3 final=5 stale_ignored=5
//!
//! which proves a model round-trips through the snapshot table and a stale write
//! cannot regress the state. Needs a `go` toolchain.

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
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/durable-snapshot");
    let dir = std::env::temp_dir().join(format!(
        "sky-durable-snap-{}-{}",
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
fn model_snapshot_round_trips_and_write_if_newer_holds() {
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
        "durable-snapshot fixture exited non-zero:\n{stdout}"
    );
    assert!(
        stdout.contains("snapshot restored=3 final=5 stale_ignored=5"),
        "a model should restore from the snapshot (3), continue (5), and a stale write \
         should be ignored (5); got:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
