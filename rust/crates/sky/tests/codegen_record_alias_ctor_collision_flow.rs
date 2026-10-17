//! Regression: two modules each declare `type alias Reply` (a record alias with
//! a different field set), and each uses its own `Reply` constructor.
//!
//! Before the fix, both type-checked and `sky check` passed, but every use of
//! the constructor in one module built the OTHER module's record. A record-alias
//! constructor resolves to `Res::Def` (a bodyless synthesised def), and the
//! lowering found its Go constructor through the bare-name `ctor_owner` /
//! `ctor_arity` maps, which are last-writer-wins. So `Decode.map2 Reply …` in
//! module `A` lowered to `B_Reply` (a 3-argument curried func), and at run time
//! `A.parse` panicked:
//!
//! ```text
//! CoerceFailure … rt.Coerce: expected main.A_Reply_R, got func(interface {}) interface {}
//! ```
//!
//! The fix (`lower/src/lower.rs`, `record_ctor_pin`) pins the constructor to the
//! Go record of the module that DECLARES it and passes that pin to every ctor
//! path (applied, value, partial application, the eta-expanded closure).
//!
//! The fixture covers every way the constructor reaches the lowering: applied in
//! full, partially applied through a `let`, as a value to `Decode.mapN` and to
//! `List.map`, in a CAF, in an unannotated helper, through `Codec.auto`, through
//! a record update, and from `Main` through `import A exposing (Reply)`. It runs
//! under `sky build` + run AND under `sky test` (the path the defect was found
//! on). Needs a `go` toolchain.

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
        .join("tests/fixtures/record-alias-ctor-collision");
    let dir = std::env::temp_dir().join(format!(
        "sky-alias-ctor-collision-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    copy_dir(&fixture, &dir);
    dir
}

fn run(dir: &Path, args: &[&str]) -> (bool, String) {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    let mut log = String::from_utf8_lossy(&out.stdout).into_owned();
    log.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), log)
}

/// Every line is `<field read>/<field read>` of the record the line's module
/// built. An `A` line reads `answer` and `relevant`, a `B` line reads `text` and
/// `notes`, so a record built by the wrong module's constructor cannot print it.
const EXPECTED: &str = "yes/rel\n\
t/n\n\
m/rel\n\
m/\n\
p/irr\n\
p/p\n\
c/rel\n\
c/\n\
caf/rel\n\
caf/\n\
u!/rel\n\
u!/\n\
x/rel;y/rel\n\
x/;y/\n\
lo/irr\n\
lo/l\n\
main/rel\n\
main/\n";

#[test]
fn same_named_record_alias_constructors_build_their_own_module_record() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();

    let (ok, log) = run(&dir, &["build", "src/Main.sky"]);
    assert!(ok, "the fixture must build:\n{log}");
    let main_go = std::fs::read_to_string(dir.join("sky-out/main.go")).unwrap_or_default();
    // A's decoder must call A's constructor. Before the fix it called `B_Reply`.
    let a_decoder = main_go
        .lines()
        .find(|l| l.contains("rt.JsonDec_map2("))
        .unwrap_or_default();
    assert!(
        a_decoder.contains("A_Reply(") && !a_decoder.contains("B_Reply("),
        "module A's `Decode.map2 Reply` must build A's record:\n{a_decoder}"
    );

    let out = Command::new(dir.join("sky-out/app"))
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run the built app");
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        out.status.success() && !stderr.contains("CoerceFailure"),
        "the app must run without a panic:\nstdout:\n{stdout}\nstderr:\n{stderr}"
    );
    assert_eq!(stdout, EXPECTED, "stderr:\n{stderr}");

    // The `sky test` path compiles the project modules with the test module as
    // the entry. Both decoders must build their own record there too.
    let (ok, log) = run(&dir, &["test", "tests/BothTest.sky"]);
    assert!(
        ok && log.contains("2 passed, 0 failed"),
        "`sky test` must pass both decoders:\n{log}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
