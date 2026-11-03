//! Strings accepted by Sky must also be valid Go literals, including U+FEFF.
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;

#[test]
fn every_unicode_scalar_survives_the_go_lexer() {
    if !live_gate::required(
        live_gate::Need::Go,
        Command::new("go").arg("version").output().is_ok(),
    ) {
        return;
    }
    let dir = std::env::temp_dir().join(format!("sky-go-scalars-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let text: String = (0..=0x10ffff).filter_map(char::from_u32).collect();
    let source = format!(
        "package main\nimport \"fmt\"\nfunc main() {{ fmt.Print(len([]rune({}))) }}\n",
        base::go_string_literal(&text)
    );
    std::fs::write(dir.join("main.go"), source).unwrap();
    let out = Command::new("go")
        .args(["run", "main.go"])
        .current_dir(&dir)
        .output()
        .unwrap();
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert_eq!(
        String::from_utf8(out.stdout).unwrap(),
        text.chars().count().to_string()
    );
}

#[test]
fn a_sky_string_with_a_bom_builds_and_runs() {
    if !live_gate::required(
        live_gate::Need::Go,
        Command::new("go").arg("version").output().is_ok(),
    ) {
        return;
    }
    let dir = std::env::temp_dir().join(format!("sky-bom-flow-{}", std::process::id()));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"bom-flow\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"),
        "module Main exposing (main)\nimport Std.Log exposing (println)\nmain = println \"before\\u{FEFF}after\"\n").unwrap();
    let out = Command::new(env!("CARGO_BIN_EXE_sky"))
        .args(["run", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .unwrap();
    let _ = std::fs::remove_dir_all(&dir);
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(String::from_utf8_lossy(&out.stdout).contains("before\u{feff}after"));
}
