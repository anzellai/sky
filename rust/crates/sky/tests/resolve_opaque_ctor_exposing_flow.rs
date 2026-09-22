//! Regression for a confusing diagnostic (2026-09-22, found in jokeraces-bot).
//!
//! `import Std.Ai.Provider exposing (Provider(..))` asks for the constructors of
//! `Provider`, but `Std.Ai.Provider` exposes `Provider` OPAQUELY (its constructors
//! are private to the module). The resolver therefore bound no constructor, so the
//! bare `Custom _` pattern in the importer fell through to an unrelated same-named
//! constructor in scope — `Std.Ui`'s `Custom Int Int` — and type-checking then
//! reported a baffling `Int -> Breakpoint vs Provider` mismatch that named neither
//! the real cause nor the offending import.
//!
//! The fix reports the CAUSE at the import with its own code, `E1013` ("OPAQUE
//! TYPE"), and `build.rs` hoists it AHEAD of the consequential type error (exactly
//! as it already hoists the `E1012` ambiguity cause). The user now sees the opaque
//! import named at its own line, not the downstream type clash.
//!
//! This fails at `sky check` (name resolution + the cause hoist), so it needs no Go
//! toolchain.

use std::path::{Path, PathBuf};
use std::process::Command;

const SKY: &str = env!("CARGO_BIN_EXE_sky");

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
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/opaque-ctor-exposing");
    let dir = std::env::temp_dir().join(format!(
        "sky-opaque-ctor-{}-{}",
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
fn opaque_ctor_exposing_reports_e1013_not_the_masked_type_error() {
    let dir = stage_fixture();
    let out = Command::new(SKY)
        .args(["check", "src/Main.sky"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky check");
    let mut log = String::from_utf8_lossy(&out.stdout).into_owned();
    log.push_str(&String::from_utf8_lossy(&out.stderr));

    // The build must FAIL — asking for private constructors is an error.
    assert!(
        !out.status.success(),
        "`exposing (T(..))` on an opaquely-exposed type must be rejected; got a \
         success:\n{log}"
    );
    // It must name the CAUSE: the opaque import, with its own code and title.
    assert!(
        log.contains("E1013") && log.contains("OPAQUE TYPE"),
        "expected the opaque-constructor cause [E1013] OPAQUE TYPE; got:\n{log}"
    );
    assert!(
        log.contains("exposes the type `Provider` but not its constructors"),
        "expected the E1013 message naming Provider's private constructors; got:\n{log}"
    );
    // It must NOT surface the downstream, masked type error — that clash is the
    // consequence of binding `Std.Ui`'s `Custom` and would mislead the user.
    assert!(
        !log.contains("Breakpoint") && !log.contains("E2001"),
        "the consequential type error must be hoisted OUT in favour of the E1013 \
         cause; got the masked clash instead:\n{log}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}
