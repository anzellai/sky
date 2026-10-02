//! Regression for a field-dropping record lowering (v0.27.1, found in a real
//! Sky.Spa app).
//!
//! A function that reads only `req.body` and hands `req` to an UNANNOTATED
//! helper is lowered with `req : { body : String | r }`: on the lowering path
//! an unannotated callee is a fresh type variable, so the row stays open. When
//! the program also declares `type alias Note = { body : String }`, `goty`
//! resolved that open row to `Note` by its field-name set alone. The `Request`
//! passed in was then converted to `Note` and back to `Request` at the Go
//! boundary, and every field except `body` was lost: the cookie, the path, the
//! headers. In the Sky.Spa split this made every RPC after sign-in run signed
//! out.
//!
//! Fixed in `lower/src/goty.rs`: an OPEN row resolves to a nominal record by
//! its field set only when no other nominal record could carry it with more
//! fields. Here `Request` collects `body`, so the row is ambiguous and lowers
//! to the reflective path, which keeps the whole value. Needs a `go` toolchain.

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
        .join("tests/fixtures/open-row-alias-keeps-fields");
    let dir = std::env::temp_dir().join(format!(
        "sky-open-row-alias-{}-{}",
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
fn open_row_matching_an_alias_keeps_the_wider_value() {
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
    assert!(out.status.success(), "the fixture must build:\n{log}");

    let main_go = std::fs::read_to_string(dir.join("sky-out/main.go")).expect("emitted main.go");
    assert!(
        !main_go.contains("func Main_handle(v_0 Main_Note_R)"),
        "`handle`'s open-row parameter must not be lowered as the unrelated alias `Note`"
    );

    let run = Command::new(dir.join("sky-out/app"))
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run the built app");
    let stdout = String::from_utf8_lossy(&run.stdout).into_owned();
    let stderr = String::from_utf8_lossy(&run.stderr).into_owned();
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        run.status.success(),
        "the app must exit 0:\nstdout:\n{stdout}\nstderr:\n{stderr}"
    );
    assert!(
        stdout.contains("note | payload abc123 /rpc/save"),
        "the cookie and the path must survive the call through `handle` \
         (before the fix: the Request crossed the Go boundary as `Note`):\n\
         stdout:\n{stdout}\nstderr:\n{stderr}"
    );
}
