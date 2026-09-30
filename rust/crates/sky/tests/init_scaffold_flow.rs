//! A fresh `sky init` project passes Sky's own tools with zero findings
//! (F-13), and its `.dockerignore` keeps local keys and secrets out of an
//! image (A-7).
//!
//! Before v0.27.0 the scaffold's `src/Main.sky` was not `sky fmt`-clean (so
//! `sky verify` failed its fmt phase on a new project), every check printed
//! "3 runtime settings moved into typed app config" about the scaffold's own
//! `sky.toml`, `sky doctor` exited 1 on "SKY_AUTH_TOKEN_SECRET is unset" for an
//! app that does not use `Std.Auth`, and the commented `[auth]` block suggested
//! the Sky.Live session cookie name.

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

fn scratch() -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-init-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

fn run(dir: &Path, args: &[&str]) -> (i32, String, String) {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .env_remove("SKY_AUTH_TOKEN_SECRET")
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    (
        out.status.code().unwrap_or(-1),
        String::from_utf8_lossy(&out.stdout).into_owned(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

fn scaffold(flag: Option<&str>) -> (PathBuf, PathBuf) {
    let base = scratch();
    let mut args = vec!["init", "fresh"];
    if let Some(f) = flag {
        args.push(f);
    }
    let (code, out, err) = run(&base, &args);
    assert_eq!(code, 0, "sky init:\n{out}{err}");
    (base.clone(), base.join("fresh"))
}

#[test]
fn a_fresh_project_is_fmt_clean_and_ignores_local_state_in_images() {
    for flag in [None, Some("--production")] {
        let (base, dir) = scaffold(flag);
        let (code, out, err) = run(&dir, &["fmt", "--check", "src/Main.sky"]);
        assert_eq!(
            code, 0,
            "{flag:?}: the scaffold is sky fmt-clean:\n{out}{err}"
        );
        let toml = std::fs::read_to_string(dir.join("sky.toml")).unwrap();
        assert!(
            !toml.contains("\"sky_sid\""),
            "{flag:?}: the [auth] hint must not suggest the Sky.Live cookie name:\n{toml}"
        );
        let ignore = std::fs::read_to_string(dir.join(".dockerignore"))
            .expect("sky init writes a .dockerignore");
        for want in [".skydata/", ".env", "sky-out/"] {
            assert!(
                ignore.lines().any(|l| l.trim() == want),
                "{flag:?}: .dockerignore lists {want}:\n{ignore}"
            );
        }
        let _ = std::fs::remove_dir_all(&base);
    }
}

#[test]
fn a_fresh_project_checks_with_zero_findings_and_a_clean_doctor() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let (base, dir) = scaffold(None);
    let (code, out, err) = run(&dir, &["check", "--format", "json", "src/Main.sky"]);
    assert_eq!(code, 0, "sky check:\n{out}{err}");
    let diagnostics: Vec<&str> = out
        .lines()
        .filter(|l| l.contains("\"kind\":\"diagnostic\""))
        .collect();
    assert!(
        diagnostics.is_empty(),
        "a fresh project has no findings at all (not even an info):\n{}",
        diagnostics.join("\n")
    );
    let (code, out, err) = run(&dir, &["doctor"]);
    assert_eq!(code, 0, "sky doctor on a fresh project:\n{out}{err}");
    assert!(
        !format!("{out}{err}").contains("SKY_AUTH_TOKEN_SECRET"),
        "no auth-secret finding for an app without Std.Auth:\n{out}{err}"
    );
    let _ = std::fs::remove_dir_all(&base);
}
