//! Regression for a codegen erasure bug (the `Std.Ai.Tool` follow-up).
//!
//! A NAMED `let`-binding lambda whose param destructures a single-constructor
//! union — `let one (W w) = w.label in List.map one xs` — where the union lives
//! in a NON-sealed-prefix module (`Std.Widget` -> Go prefix `Std_`, which
//! `should_seal_prefix` excludes). `lower_local_fn` passed `sig_ty = None` to
//! `bind_param`, so the param erased to Go `any`, and `pattern_test` then emitted
//! `.Fields` on `any` for the non-sealed ADT: `sky check` passed but `go build`
//! failed (`_t0.Fields undefined (type any has no field or method Fields)`).
//!
//! Fixed in `lower.rs` `bind_param` by reconstructing the ctor's nominal Go type
//! (`pattern_nominal_ty`) when the signature gives no concrete type. Origin R6
//! (doc 14 §3); closeable (both shapes known at emit time), coerce-floor
//! unchanged. This is the minimal form of the bug that forced the `Std.Ai.Tool`
//! accessor workaround (now removed). Needs a `go` toolchain.
//!
//! The union MUST stay in a `Std.*` module: a `Main`-module union is sealed (a
//! typed variant struct) and would compile even unfixed — a false green.

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
    let fixture =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/codegen-ctor-destructure");
    let dir = std::env::temp_dir().join(format!(
        "sky-codegen-ctor-{}-{}",
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
fn named_let_lambda_ctor_destructure_go_builds_and_runs() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();
    // `sky run` does `sky check` + `go build` + run. Before the fix `go build`
    // failed here even though `sky check` passed.
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
        "a named let-lambda ctor destructure in a non-sealed module should go-build and run \
         (regression: `_t0.Fields undefined`); got:\n{stdout}"
    );
    assert!(
        stdout.contains("a,b"),
        "expected the joined labels `a,b`; got:\n{stdout}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
