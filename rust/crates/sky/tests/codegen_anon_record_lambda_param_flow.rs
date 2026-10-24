//! Regression for a codegen soundness break (v0.27.14): `sky check` passed and
//! `go build` failed.
//!
//! `List.foldl (\it acc -> { entries = it.label :: acc.entries, … }) { entries
//! = [], … } items`, where `items : List Item` is a list of NAMED records and the
//! accumulator is an ANONYMOUS record. A list combinator closure pins the param
//! that stands at the list element to the list's own Go element type
//! (`closure_elem`), so a subset `struct{…}` param reads the real runtime value.
//! The pin went to the FIRST `struct{…}` param, whatever its position. `it` is
//! the nominal `Main_Item_R`, not a `struct{…}`, so the pin skipped it and
//! narrowed `acc` to `Main_Item_R`: `v_3.Entries undefined (type Main_Item_R has
//! no field or method Entries)`. An accumulator-first user fold (`\acc it -> …`)
//! and a nested anonymous accumulator failed the same way.
//!
//! Fixed in `lower/src/lower.rs` (`closure_elem_positions`): the element
//! position comes from the solved Sky types, so only a param of the list's
//! element type takes the pin. The fixture also covers the neighbours that
//! already built (a `List.map` into an anonymous record, a let-bound step, an
//! anonymous element with a named accumulator, both anonymous, the element and
//! the accumulator of one named type, `List.indexedMap`), so a fix that breaks
//! one of them goes red here. Needs a `go` toolchain.

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
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/anon-record-lambda-param");
    let dir = std::env::temp_dir().join(format!(
        "sky-anon-record-lambda-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    copy_dir(&fixture, &dir);
    dir
}

/// Each line reads fields of the record its function built. A record narrowed
/// to the wrong type cannot build, and a pin to the wrong param reads the wrong
/// fields.
const EXPECTED: &str = "collect B+A 2\n\
accFirst B+A 2\n\
mapped A2+B2\n\
letBound 4 /b+/a\n\
nested B+A 2 /b\n\
anonElem y+x 3\n\
bothAnon 3\n\
sameType /a/b B\n\
indexed 0A+1B\n";

#[test]
fn anonymous_record_lambda_params_build_and_run() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();

    // `sky build` type-checks AND `go build`s the emitted Go. Before the fix the
    // Go build failed on `acc`'s fields (`… undefined (type Main_Item_R …)`).
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
        "an anonymous-record lambda param must go-build (regression: \
         `v.Entries undefined (type Main_Item_R …)`); got:\n{log}"
    );
    assert!(
        !log.contains("undefined"),
        "unexpected `undefined` in the build output:\n{log}"
    );

    let run = Command::new(dir.join("sky-out/app"))
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run the built app");
    let stdout = String::from_utf8_lossy(&run.stdout);
    let stderr = String::from_utf8_lossy(&run.stderr);
    assert!(
        run.status.success(),
        "the app must run without a panic:\nstdout:\n{stdout}\nstderr:\n{stderr}"
    );
    assert_eq!(stdout, EXPECTED, "stderr:\n{stderr}");

    let _ = std::fs::remove_dir_all(&dir);
}
