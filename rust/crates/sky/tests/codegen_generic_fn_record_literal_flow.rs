//! Regression for a codegen soundness break (v0.29.6): `sky check` passed and
//! `go build` failed.
//!
//! A record literal of FUNCTIONS that builds a parametric record alias
//! (`Wiring model = { restore : String -> Maybe model, persist : model ->
//! String }`), wrapped in `Just` inside a record update (`{ cfg | wiring = Just
//! { restore = r, persist = p } }`), was built as the all-`any` anonymous struct
//! that `lower_record` keeps for a TEA config read by reflection
//! (`generic_func_cfg`), and `Just` lowered its argument at `any`: `cannot use
//! struct{Persist any; Restore any}{…} as Main_Wiring_R[any] value in argument
//! to rt.Just[Main_Wiring_R[any]]`. `Ok` and `Err` failed the same way.
//!
//! Fixed in `lower/src/lower.rs` (`builtin_container_fn_field_gotys`,
//! `slot_typed_generic_fn_record`, `lower_record`): the literal argument of a
//! builtin container whose element is that nominal is built AS the nominal
//! (doc 14 lever §5.2), and `ctor_emit` narrows any other all-`any` record that
//! reaches a function-field nominal payload (R1). `lower/src/goty.rs` recovers
//! an unbound alias parameter as `any` (`instantiate_structural`) and resolves
//! two modules' identical aliases to one nominal (`select_record_candidate`).
//!
//! The fixture covers the neighbours: `Just` / `Ok` / `Err` / a plain field in
//! one record update, an unannotated update, the same literals in polymorphic
//! defs (annotated and not), `Just (Just …)`, a lambda, a `let`, an `if`, a user
//! ADT, a tuple, a list literal, a cons, `Dict.fromList`, `Maybe.map`, a nested
//! alias, a concrete `Wiring Int`, a non-parameterised alias of functions, and a
//! cross-module alias that has an identical twin in `Main`. Every line calls
//! both functions read back from the slot, so a dropped or swapped field cannot
//! print `41/7`. Needs a `go` toolchain.

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
        .join("tests/fixtures/generic-fn-record-literal-payload");
    let dir = std::env::temp_dir().join(format!(
        "sky-generic-fn-record-literal-{}-{}",
        (u64::from(std::process::id()) << 32) | {
            static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        },
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    copy_dir(&fixture, &dir);
    dir
}

const EXPECTED: &str = "updateJust 41/7\n\
updateOk 41/7\n\
updateErr 41/7\n\
updatePlain 41/7\n\
updateBare 41/7\n\
untouched none,0/none\n\
just 41/7\n\
justBare 41/7\n\
ok 41/7\n\
err 41/7\n\
nested 41/7\n\
lambda 41/7\n\
let 41/7\n\
if 41/7\n\
box 41/7\n\
tuple 41/7\n\
list 41/7\n\
cons 41/7\n\
dict 41/7\n\
mapped 41/7\n\
outer 41/7\n\
concrete 41/7\n\
fixed 41\n\
crossModule 41/7\n\
crossLocal 41/7\n";

#[test]
fn generic_fn_record_literal_payloads_build_and_run() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();

    // `sky build` type-checks AND `go build`s the emitted Go. Before the fix the
    // Go build failed on `rt.Just[Main_Wiring_R[any]](struct{ Persist any; … })`.
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
        "a record literal of functions in a container payload must go-build \
         (regression: `cannot use struct{{Persist any; Restore any}}{{…}} as \
         Main_Wiring_R[any] value`); got:\n{log}"
    );
    assert!(
        !log.contains("cannot use"),
        "unexpected `cannot use` in the build output:\n{log}"
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
