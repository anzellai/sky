//! Regression for a codegen record-lowering bug (2026-09-23, found while fixing
//! the Sky.Spa `App.withRequest` RPC path).
//!
//! A top-level binding whose VALUE is a row-polymorphic lambda —
//! `onReq = \_ model -> ( { model | server = "s" }, 0 )` — is generalised, so
//! its record parameter keeps an OPEN row `{ ρ | server }`. `goty` resolves an
//! open row by field NAME, and an unrelated nominal record with exactly those
//! fields (`Other = { server }` here; in the auto-split a generated one-field
//! `TouchResp` record) captured it. The lambda was typed
//! `func(any, Other_R) rt.T2[Other_R, int]`, every caller coerced its Model DOWN
//! to `Other_R`, and the Model came back with every other field reset to zero —
//! a silent wrong answer (`count` 5 → 0). The same function written with
//! parameters (`onReqDef _ model = …`) or bound by `let` was already correct.
//!
//! Fixed in `lower/src/lower.rs` (`caf_lambda_row_poly_ty`): a zero-parameter
//! def whose value is a lambda with a row var shared between a parameter and
//! the (possibly tuple-wrapped) result presents the ERASED function type
//! (`func(any, any) rt.T2[any, int]`), and its root lambda is lowered against
//! it, so the update goes through the reflective `rt.RecordUpdate` and keeps
//! every field. The same collision hit an immediately-APPLIED lambda
//! (`onReqApplied r m = (\_ model -> ( { model | server = "u" }, 0 )) r m`, the
//! shape the split's `spaOnRequest_ req_ model_` takes): `row_poly_positions`
//! now flags a row var flowing from a param into a TUPLE-wrapped result, and a
//! position whose Go type merely COLLIDES with a non-Model nominal takes the
//! reflective path (`lower_lambda`, `lower_def`); positions that resolve to the
//! Model or already erase to `any` are unchanged. Needs a `go` toolchain.

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
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/codegen-rowpoly-lambda-caf");
    let dir = std::env::temp_dir().join(format!(
        "sky-rowpoly-caf-{}-{}",
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
fn rowpoly_lambda_caf_keeps_every_record_field() {
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
    assert!(out.status.success(), "sky build failed:\n{log}");

    let run = Command::new(dir.join("sky-out/app"))
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run the built app");
    let stdout = String::from_utf8_lossy(&run.stdout).into_owned();
    assert!(
        run.status.success(),
        "app failed: {stdout}{}",
        String::from_utf8_lossy(&run.stderr)
    );
    // CAF lambda, top-level function, let-bound lambda, immediately-applied
    // lambda: every one keeps `count`.
    assert_eq!(
        stdout.trim(),
        "5 6 7 8 osstu",
        "a row-polymorphic lambda CAF must not narrow its record to a same-named \
         nominal (regression: `count` reset to 0 → \"0 6 7 0 osstu\")"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
