//! Regression for a masked diagnostic (v0.27.0, found while fixing the Sky.Spa
//! split's dependency types).
//!
//! `import Geo.Shape` binds the qualifier `Shape`, so an annotation that names
//! the type by the full module path, `p0 : Geo.Shape.Point`, does not resolve.
//! The resolver reported `[E1001] Undefined name: Geo.Shape.Point`, but the
//! build printed the type error that followed from it instead: the checker
//! still needs a type there, the unresolved name fell back to a bare
//! `Point`, and the type world expanded that to the stdlib's
//! `Std.Ui.Canvas.Point` (`{ x : Float, y : Float }`). The user saw
//! `[p0] type mismatch: Int vs Float` and never the undefined name.
//!
//! `build.rs` now reports an unresolved TYPE reference ahead of the type gate,
//! as it does the other name-resolution causes. Fails at `sky check`, so it
//! needs no Go toolchain.

use std::path::PathBuf;
use std::process::Command;

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn scratch() -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-unresolved-type-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src/Geo")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"unresolved-type\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Geo/Shape.sky"),
        "module Geo.Shape exposing (Point, shift)\n\n\ntype alias Point =\n    { x : Int\n    , y : Int\n    }\n\n\nshift : Int -> Point -> Point\nshift d p =\n    { p | x = p.x + d }\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\nimport Geo.Shape\nimport Sky.Core.Prelude exposing (..)\nimport Std.Log exposing (println)\n\n\np0 : Geo.Shape.Point\np0 =\n    { x = 3, y = 4 }\n\n\nmain =\n    println (String.fromInt p0.x)\n",
    )
    .unwrap();
    dir
}

#[test]
fn an_unresolved_type_reference_is_reported_not_the_type_error_it_causes() {
    let dir = scratch();
    let out = Command::new(SKY)
        .args(["check", "src/Main.sky"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky check");
    let mut log = String::from_utf8_lossy(&out.stdout).into_owned();
    log.push_str(&String::from_utf8_lossy(&out.stderr));
    assert!(!out.status.success(), "must be rejected:\n{log}");
    assert!(
        log.contains("[E1001]") && log.contains("Undefined name: Geo.Shape.Point"),
        "the undefined type name is the reported cause:\n{log}"
    );
    assert!(
        log.contains("src/Main.sky:8:"),
        "reported at the annotation:\n{log}"
    );
    assert!(
        !log.contains("[E2001]") && !log.contains("Float"),
        "the consequential type error must not be shown:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
