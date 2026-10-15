//! What `sky` passes to `go build`, and what the Go build cache does with it.
//!
//! Before v0.27.9 no `go build` passed `-trimpath`. Go then records each
//! package's absolute source directory, and the directory is part of the
//! package's cache key: the same program built in a second directory (a new
//! checkout, a CI runner's workspace, each Sky.Spa leg, each desktop shell)
//! recompiled the whole runtime. Measured on examples/13-skyshop: 83 s in a
//! new directory against 5 s warm.
//!
//! These tests watch the real commands through a `go` shim on `PATH` that logs
//! each argv, and count the compile actions Go runs (`go build -x`).

use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::Mutex;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// One heavy build at a time (16 GB hosts).
static BUILD_LOCK: Mutex<()> = Mutex::new(());

fn real_go() -> Option<PathBuf> {
    let out = Command::new("sh")
        .args(["-c", "command -v go"])
        .output()
        .ok()?;
    let p = String::from_utf8_lossy(&out.stdout).trim().to_string();
    (out.status.success() && !p.is_empty()).then(|| PathBuf::from(p))
}

fn scratch(tag: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!(
        "sky-gocache-flow-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// A `go` that logs `CGO_ENABLED`, `GOOS` and its argv (one line per call) to
/// `<dir>/go.log`, then runs the real `go`. With `SHIM_FAIL_STATIC=1` a static
/// (`CGO_ENABLED=0`) native `go build` fails, as one whose FFI package needs
/// cgo does, so the cgo retry runs. With `SHIM_FAKE_RT=1` a `go build
/// ./rt/...` (the cache warm-up) is only logged. With `SHIM_X=1` every `go
/// build` also gets `-x`, its stderr appended to `<dir>/x.log`.
fn write_shim(dir: &Path, go: &Path) {
    use std::os::unix::fs::PermissionsExt;
    let log = dir.join("go.log");
    let xlog = dir.join("x.log");
    let script = format!(
        r#"#!/bin/sh
{{ printf 'CGO_ENABLED=%s GOOS=%s |' "$CGO_ENABLED" "$GOOS"; for a in "$@"; do printf ' %s' "$a"; done; echo; }} >> '{log}'
if [ "$1" = build ]; then
  if [ "$SHIM_FAIL_STATIC" = 1 ] && [ "$CGO_ENABLED" = 0 ] && [ -z "$GOOS" ]; then
    echo "shim: this build needs cgo" >&2; exit 1
  fi
  for a in "$@"; do [ "$a" = ./rt/... ] && [ "$SHIM_FAKE_RT" = 1 ] && exit 0; done
  if [ "$SHIM_X" = 1 ]; then
    shift
    '{go}' build -x "$@" 2>> '{xlog}'
    exit $?
  fi
fi
exec '{go}' "$@"
"#,
        log = log.display(),
        xlog = xlog.display(),
        go = go.display()
    );
    let p = dir.join("go");
    std::fs::write(&p, script).unwrap();
    std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o755)).unwrap();
}

fn path_with(shim: &Path) -> String {
    format!(
        "{}:{}",
        shim.display(),
        std::env::var("PATH").unwrap_or_default()
    )
}

/// The `go build` lines of the shim's log.
fn build_lines(shim: &Path) -> Vec<String> {
    std::fs::read_to_string(shim.join("go.log"))
        .unwrap_or_default()
        .lines()
        .filter(|l| l.contains("| build"))
        .map(String::from)
        .collect()
}

/// A small HTTP server project.
fn write_server(dir: &Path) {
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"trim\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.Task as Task\n\
         import Sky.Http.Server as Server\n\n\n\
         main =\n    Server.listen 8000\n        [ Server.get \"/\" (\\_ -> Task.succeed (Server.text \"ok\")) ]\n",
    )
    .unwrap();
}

/// A build identity that does not depend on the directory or file times, so a
/// copy in another directory emits byte-identical Go.
fn fixed_stamp(c: &mut Command) -> &mut Command {
    c.env(
        "SKY_BUILD_COMMIT",
        "0123456789abcdef0123456789abcdef01234567",
    )
    .env("SKY_BUILD_EPOCH", "1790000000")
    .env_remove("SKY_BUILD_STAMP_PINNED")
    .env_remove("SKY_GO_TRIMPATH")
    .env_remove("GOFLAGS")
}

fn sky_build(dir: &Path, shim: &Path, extra_env: &[(&str, &str)], args: &[&str]) -> String {
    let mut c = Command::new(SKY);
    c.arg("build").args(args).current_dir(dir);
    fixed_stamp(&mut c).env("PATH", path_with(shim));
    for (k, v) in extra_env {
        c.env(k, v);
    }
    let out = c.output().unwrap();
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(out.status.success(), "sky build failed:\n{text}");
    text
}

/// Every `go build` gets `-trimpath`: the static build, its cgo retry, and the
/// cache warm-up for native and wasm. `--no-trimpath` turns it off.
#[test]
fn every_go_build_trims_paths() {
    let Some(go) = real_go() else {
        assert!(!required(Need::Go, false));
        return;
    };
    let _g = BUILD_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let root = scratch("argv");
    let shim = root.join("shim");
    std::fs::create_dir_all(&shim).unwrap();
    write_shim(&shim, &go);
    let proj = root.join("app");
    write_server(&proj);

    // Static first; the shim refuses it, so the cgo retry runs.
    sky_build(
        &proj,
        &shim,
        &[("SHIM_FAIL_STATIC", "1")],
        &["src/Main.sky"],
    );
    let builds = build_lines(&shim);
    assert_eq!(
        builds.len(),
        2,
        "static + cgo retry:\n{}",
        builds.join("\n")
    );
    assert!(builds[0].starts_with("CGO_ENABLED=0 "), "{}", builds[0]);
    assert!(builds[1].starts_with("CGO_ENABLED=1 "), "{}", builds[1]);
    for b in &builds {
        assert!(b.contains(" -trimpath"), "no -trimpath: {b}");
    }

    // The cache warm-up, native and wasm (only logged: `SHIM_FAKE_RT`).
    std::fs::remove_file(shim.join("go.log")).unwrap();
    let mut c = Command::new(SKY);
    c.arg("__warm-go-cache").current_dir(&proj);
    fixed_stamp(&mut c)
        .env("PATH", path_with(&shim))
        .env("SHIM_FAKE_RT", "1");
    assert!(c.status().unwrap().success());
    let builds = build_lines(&shim);
    assert_eq!(builds.len(), 2, "native + wasm:\n{}", builds.join("\n"));
    assert!(builds.iter().any(|b| b.contains("GOOS=js")), "{builds:?}");
    for b in &builds {
        assert!(
            b.ends_with("| build -trimpath ./rt/..."),
            "the warm-up must use the project build's flags: {b}"
        );
    }

    // The opt-out.
    std::fs::remove_file(shim.join("go.log")).unwrap();
    sky_build(&proj, &shim, &[], &["--no-trimpath", "src/Main.sky"]);
    let builds = build_lines(&shim);
    assert_eq!(builds.len(), 1, "{builds:?}");
    assert!(!builds[0].contains("-trimpath"), "{}", builds[0]);
    let _ = std::fs::remove_dir_all(&root);
}

/// The same program in a new directory compiles nothing: every package,
/// `main` included, is a cache hit from the build in the first directory.
#[test]
fn a_copy_in_a_new_directory_compiles_nothing() {
    let Some(go) = real_go() else {
        assert!(!required(Need::Go, false));
        return;
    };
    let _g = BUILD_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let root = scratch("newdir");
    let shim = root.join("shim");
    std::fs::create_dir_all(&shim).unwrap();
    write_shim(&shim, &go);
    let example = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../examples/09-live-counter")
        .canonicalize()
        .unwrap();
    let a = root.join("a");
    let b = root.join("deeper").join("b");
    for d in [&a, &b] {
        std::fs::create_dir_all(d.join("src")).unwrap();
        std::fs::copy(example.join("sky.toml"), d.join("sky.toml")).unwrap();
        std::fs::copy(example.join("src/Main.sky"), d.join("src/Main.sky")).unwrap();
    }
    sky_build(&a, &shim, &[], &["src/Main.sky"]);
    let _ = std::fs::remove_file(shim.join("x.log"));
    sky_build(&b, &shim, &[("SHIM_X", "1")], &["src/Main.sky"]);
    let x = std::fs::read_to_string(shim.join("x.log")).unwrap();
    let compiles: Vec<&str> = x
        .lines()
        .filter(|l| l.contains("/compile ") && l.contains(" -p "))
        .collect();
    assert!(
        x.lines().any(|l| l.contains("/link ")),
        "the -x log must show the link (is SHIM_X wired?):\n{x}"
    );
    assert!(
        compiles.is_empty(),
        "{} package(s) recompiled in the new directory:\n{}",
        compiles.len(),
        compiles.join("\n")
    );
    let _ = std::fs::remove_dir_all(&root);
}
