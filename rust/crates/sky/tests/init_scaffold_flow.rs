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

/// A fresh directory per call. The tests of this file run in parallel in one
/// process, and two calls in the same clock tick got the same name: one test
/// then read the `.gitattributes` another had written. A counter keeps every
/// name distinct.
fn scratch() -> PathBuf {
    static NEXT: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
    let n = NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
    let dir = std::env::temp_dir().join(format!(
        "sky-init-{}-{n}-{}",
        (u64::from(std::process::id()) << 32) | {
            static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        },
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
        for want in [".skydata/", ".sky/", ".env", "sky-out/"] {
            assert!(
                ignore.lines().any(|l| l.trim() == want),
                "{flag:?}: .dockerignore lists {want}:\n{ignore}"
            );
        }
        // `.sky/` is per-machine AI state (the local registry store, frozen
        // datasets, the `sky ai` pipeline build cache): never committed.
        let git =
            std::fs::read_to_string(dir.join(".gitignore")).expect("sky init writes a .gitignore");
        for want in [".sky/", ".skydata/", ".env", "sky-out/"] {
            assert!(
                git.lines().any(|l| l.trim() == want),
                "{flag:?}: .gitignore lists {want}:\n{git}"
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

/// The `.gitattributes` every new project gets (v0.27.12): GitHub Linguist has
/// no Sky grammar, and Sky syntax is Elm, so `.sky` / `.skyi` files highlight
/// as Elm. Every template variant writes it; an existing file is kept.
const GITATTRIBUTES: &str =
    "# GitHub Linguist has no Sky grammar yet. Sky syntax is Elm, so highlight it as Elm.\n\
*.sky linguist-language=Elm\n\
*.skyi linguist-language=Elm\n";

#[test]
fn a_fresh_project_highlights_sky_as_elm_on_github() {
    for flag in [None, Some("--production")] {
        let (base, dir) = scaffold(flag);
        let attrs = std::fs::read_to_string(dir.join(".gitattributes"))
            .unwrap_or_else(|e| panic!("{flag:?}: sky init writes a .gitattributes: {e}"));
        assert_eq!(
            attrs, GITATTRIBUTES,
            "{flag:?}: the exact .gitattributes lines"
        );
        let _ = std::fs::remove_dir_all(&base);
    }
}

#[test]
fn sky_init_keeps_an_existing_gitattributes() {
    let base = scratch();
    let dir = base.join("fresh");
    std::fs::create_dir_all(&dir).unwrap();
    let mine = "*.png binary\n";
    std::fs::write(dir.join(".gitattributes"), mine).unwrap();
    let (code, out, err) = run(&base, &["init", "fresh"]);
    assert_eq!(code, 0, "sky init:\n{out}{err}");
    assert_eq!(
        std::fs::read_to_string(dir.join(".gitattributes")).unwrap(),
        mine,
        "sky init must not overwrite a .gitattributes the project already has"
    );
    let _ = std::fs::remove_dir_all(&base);
}
