//! Regression for a codegen record-lowering bug (2026-09-20, found in FacePlan).
//!
//! An inline lambda that reads a NESTED record field over a list —
//! `List.all (\r -> r.clinic.billingOverride == "comped") rows` where
//! `rows : List ClinicRow` and `ClinicRow = { clinic : Clinic, … }` — infers for
//! `r` the SUBSET row `{ clinic | ρ }` (only the accessed field is known). The
//! app is a TEA app whose `Model` ALSO has a `clinic` field, typed
//! `Maybe Clinic`. `goty`'s subset→nominal Model resolver matched the row to the
//! Model on the field NAME `clinic` alone, so `r.clinic` rendered as
//! `Maybe Clinic` and `r.clinic.billingOverride` as a STATIC access on a
//! `rt.SkyMaybe`: `sky check` passed but `go build` failed with
//! `v_0.Clinic.BillingOverride undefined (type rt.SkyMaybe[Main_Clinic_R] …)`.
//!
//! Fixed in `lower/src/goty.rs` (`model_subset_resolves`): a Model NAME-subset
//! only resolves to the nominal Model when the field set is unambiguously the
//! Model's — no OTHER record nominal (here `ClinicRow`) collects all the same
//! field names, and no field TYPE contradicts the Model. An ambiguous subset
//! falls through to the safe reflective (`any`) path. A view/update helper's
//! genuine Model subset (`{ activity, rows, … }`, fields no other nominal
//! collects) still resolves to the Model, so the coercion-eliding case the
//! resolver exists for is preserved.
//!
//! The Model + a DISTINCT record sharing the `clinic` field name are both
//! required: without the shared name there is no ambiguity, and without the TEA
//! Model there is no subset→Model resolver to mis-fire — either shape compiles
//! even unfixed (a false green). Needs a `go` toolchain.

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
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/model-subset-record-lambda");
    let dir = std::env::temp_dir().join(format!(
        "sky-model-subset-{}-{}",
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
fn model_subset_record_lambda_go_builds() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();
    // `sky build` type-checks AND `go build`s the emitted Go. Before the fix the
    // Go build failed (`v_0.Clinic.BillingOverride undefined … rt.SkyMaybe[…]`)
    // even though `sky check`'s type phase passed.
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
        "a nested-record-field lambda over a list, in an app whose Model shares the \
         outer field name, should go-build (regression: `.BillingOverride undefined \
         (type rt.SkyMaybe[…])`); got:\n{log}"
    );
    assert!(
        !log.contains("SkyMaybe") && !log.contains("undefined"),
        "unexpected SkyMaybe/undefined in the build output:\n{log}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
