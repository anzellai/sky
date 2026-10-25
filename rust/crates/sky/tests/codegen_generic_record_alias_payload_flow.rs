//! Regression for a codegen soundness break (v0.27.16): `sky check` passed and
//! `go build` failed.
//!
//! A polymorphic def that returns a parametric record alias (`action : String
//! -> msg -> Action msg`) erases its type variables in its Go signature (doc 14
//! R12), so a call to it has the Go type `Lib_Action_R[any]`. `Just (Lib.action
//! "Edit" Noop)` monomorphises the constructor from the checked type,
//! `rt.Just[Lib_Action_R[Main_Msg]]`, and lowers its argument at `any`, so the
//! narrowing every other slot gets never ran: `cannot use Lib_action(...)
//! (value of struct type Lib_Action_R[any]) as Lib_Action_R[Main_Msg] value in
//! argument to rt.Just[Lib_Action_R[Main_Msg]]`. `Ok` and `Err` failed the same
//! way. The list form `[ Lib.action "Edit" Noop ]` always built.
//!
//! Fixed in `lower/src/lower.rs` (`ctor_emit`, `erased_instance_of`): a builtin
//! container payload whose Go type is the payload's own generic type at an
//! erased instantiation is narrowed to the payload, as a list element is. The
//! fixture also covers the neighbours: a user ADT with a type parameter, a
//! tuple, a record field, a function argument, `Maybe.map` and `Result.map`
//! results, let-bound values with and without an annotation, a same-module
//! alias, an alias with two type parameters and a nested `Maybe (List …)`. Each
//! line reads the label AND the message back, so a value narrowed to the wrong
//! type cannot print the expected line. Needs a `go` toolchain.

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
        .join("tests/fixtures/generic-record-alias-payload");
    let dir = std::env::temp_dir().join(format!(
        "sky-generic-alias-payload-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    copy_dir(&fixture, &dir);
    dir
}

const EXPECTED: &str = "pick Edit\n\
list Edit\n\
just Open:Save\n\
ok ok Go:Save\n\
err err Stop:Noop\n\
slot Fill:Save\n\
tuple Pair:Noop/2\n\
field Primary:Save Extra:Noop\n\
arg Arg:Save\n\
maybeMap Mapped:Save\n\
resultMap ok Mapped:Noop\n\
letAnnotated Annotated:Save\n\
letBare Unannotated:Noop\n\
sameModule Local:Save\n\
twoParams Two:Noop\n\
nested N1:Save,N2:Noop\n";

#[test]
fn generic_record_alias_payloads_build_and_run() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();

    // `sky build` type-checks AND `go build`s the emitted Go. Before the fix the
    // Go build failed on `rt.Just[Lib_Action_R[Main_Msg]](Lib_action(…))`.
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
        "a generic record alias in a container payload must go-build (regression: \
         `cannot use Lib_action(…) (value of struct type Lib_Action_R[any]) as \
         Lib_Action_R[Main_Msg] value`); got:\n{log}"
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
