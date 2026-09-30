//! `sky check` in a library package (a `[lib]` table, no `entry`, no `Main`)
//! checks every module and exits 0 when they type-check and build, 1 when one
//! does not. It used to exit 1 with "no entry main" after the types passed, so
//! a library could not use `sky check` as a gate.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn library(tag: &str, body: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-libcheck-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src/Geo")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"geo\"\nversion = \"0.1.0\"\n\n[lib]\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Geo/Shape.sky"),
        format!("module Geo.Shape exposing (area)\n\n\narea : Int -> Int\narea x =\n    {body}\n"),
    )
    .unwrap();
    dir
}

fn check(dir: &Path, args: &[&str]) -> (i32, String) {
    let out = Command::new(SKY)
        .arg("check")
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.code().unwrap_or(-1), s)
}

#[test]
fn a_library_checks_green_and_red() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let good = library("good", "x * x");
    let (code, out) = check(&good, &[]);
    assert_eq!(code, 0, "a clean library passes `sky check`:\n{out}");
    let (code, out) = check(&good, &["src/Geo/Shape.sky"]);
    assert_eq!(code, 0, "naming a module passes too:\n{out}");
    let bad = library("bad", "\"not an int\"");
    let (code, out) = check(&bad, &[]);
    assert_eq!(code, 1, "a type error fails:\n{out}");
    assert!(
        out.contains("Shape.sky"),
        "the error names the module:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&good);
    let _ = std::fs::remove_dir_all(&bad);
}
