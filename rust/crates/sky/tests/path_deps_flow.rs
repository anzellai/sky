//! `sky add ./dir` — local path dependencies, through the real binary.
//!
//! A Go module (has `go.mod`) becomes `"<module>" = { path = "…" }` under
//! `["go.dependencies"]`, wired into the generated `go.mod` with `require` +
//! `replace` on EVERY build (the build rewrites `go.mod` from the runtime's
//! copy, so a one-off edit would be lost). A Sky package becomes
//! `"<name>" = { path = "…" }` under `[dependencies]` and is loaded from the
//! directory. A relative path is relative to the project root, never the
//! working directory.

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

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-pathdep-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

fn run(dir: &Path, prog: &str, args: &[&str]) -> (bool, String) {
    let out = Command::new(prog)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), s)
}

fn write(p: &Path, text: &str) {
    std::fs::create_dir_all(p.parent().unwrap()).unwrap();
    std::fs::write(p, text).unwrap();
}

/// `<base>/greet` (Go module `example.com/greet`) + `<base>/app` (Sky project).
fn go_fixture(tag: &str) -> (PathBuf, PathBuf, PathBuf) {
    let base = scratch(tag);
    let greet = base.join("greet");
    write(
        &greet.join("go.mod"),
        "module example.com/greet\n\ngo 1.22\n",
    );
    write(
        &greet.join("greet.go"),
        "package greet\n\n// Hello greets.\nfunc Hello(name string) string {\n\treturn \"hello \" + name\n}\n",
    );
    let app = base.join("app");
    write(
        &app.join("sky.toml"),
        "name = \"pathdep\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    );
    (base, greet, app)
}

const MAIN_HELLO: &str = "module Main exposing (main)\n\n\
import Example.Com.Greet as Greet\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.Result as Result\n\
import Std.Log exposing (println)\n\n\n\
main =\n    println (Greet.hello \"sky\" |> Result.withDefault \"err\")\n";

#[test]
fn go_module_path_dependency_add_build_edit_install_remove() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let (base, greet, app) = go_fixture("go");
    write(&app.join("src/Main.sky"), MAIN_HELLO);

    let (ok, log) = run(&app, SKY, &["add", "../greet"]);
    assert!(ok, "sky add ../greet:\n{log}");
    let toml = std::fs::read_to_string(app.join("sky.toml")).unwrap();
    assert!(
        toml.contains("[\"go.dependencies\"]\n\"example.com/greet\" = { path = \"../greet\" }"),
        "recorded relative to the project root:\n{toml}"
    );
    assert!(app.join("sky-ffi/greet.kernel.json").is_file(), "{log}");

    // Build + call it (an FFI binding returns `Result Error a`).
    let (ok, log) = run(&app, SKY, &["build", "src/Main.sky"]);
    assert!(ok, "build:\n{log}");
    let go_mod = std::fs::read_to_string(app.join("sky-out/go.mod")).unwrap();
    assert!(
        go_mod.contains("example.com/greet v0.0.0")
            && go_mod.contains(&format!(
                "example.com/greet => {}",
                greet.canonicalize().unwrap().display()
            )),
        "require + replace re-applied by the build:\n{go_mod}"
    );
    let bin = app.join("sky-out/app");
    let (ok, out) = run(&app, bin.to_str().unwrap(), &[]);
    assert!(ok && out.contains("hello sky"), "{out}");

    // A BODY edit is picked up by the next build, with no drift warning.
    write(
        &greet.join("greet.go"),
        "package greet\n\n// Hello greets.\nfunc Hello(name string) string {\n\treturn \"howdy \" + name\n}\n",
    );
    let (ok, log) = run(&app, SKY, &["build", "src/Main.sky"]);
    assert!(ok, "rebuild:\n{log}");
    assert!(
        !log.contains("changed its exported Go API"),
        "a body edit is not API drift:\n{log}"
    );
    let (_, out) = run(&app, bin.to_str().unwrap(), &[]);
    assert!(
        out.contains("howdy sky"),
        "the edited dependency is built: {out}"
    );

    // A NEW exported function: the build warns until `sky install` refreshes
    // the surface, after which the function is callable.
    write(
        &greet.join("extra.go"),
        "package greet\n\n// Shout shouts.\nfunc Shout(s string) string { return s + \"!\" }\n",
    );
    let (ok, log) = run(&app, SKY, &["build", "src/Main.sky"]);
    assert!(ok, "{log}");
    assert!(
        log.contains("changed its exported Go API") && log.contains("sky install"),
        "API drift is reported:\n{log}"
    );
    let (ok, log) = run(&app, SKY, &["install"]);
    assert!(ok, "sky install:\n{log}");
    assert!(log.contains("local Go module"), "{log}");
    write(
        &app.join("src/Main.sky"),
        &MAIN_HELLO.replace(
            "Greet.hello \"sky\" |> Result.withDefault \"err\"",
            "Greet.shout \"hey\" |> Result.withDefault \"err\"",
        ),
    );
    let (ok, log) = run(&app, SKY, &["build", "src/Main.sky"]);
    assert!(ok, "{log}");
    assert!(!log.contains("changed its exported Go API"), "{log}");
    let (_, out) = run(&app, bin.to_str().unwrap(), &[]);
    assert!(out.contains("hey!"), "{out}");

    // `sky update` leaves a path dependency alone, and says so.
    let (_, log) = run(&app, SKY, &["update"]);
    assert!(log.contains("local path dependency"), "{log}");

    // Remove by the path the user typed.
    let (ok, log) = run(&app, SKY, &["remove", "../greet"]);
    assert!(ok, "sky remove:\n{log}");
    let toml = std::fs::read_to_string(app.join("sky.toml")).unwrap();
    assert!(!toml.contains("example.com/greet"), "{toml}");
    assert!(!app.join("sky-ffi/greet.kernel.json").exists());
    let go_mod = std::fs::read_to_string(app.join("sky-out/go.mod")).unwrap();
    assert!(!go_mod.contains("example.com/greet"), "{go_mod}");
    let _ = std::fs::remove_dir_all(&base);
}

#[test]
fn sky_package_path_dependency_resolves_against_the_project_root() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let base = scratch("sky");
    let widgets = base.join("widgets");
    write(
        &widgets.join("sky.toml"),
        "name = \"widgets\"\nversion = \"0.1.0\"\n\n[lib]\nexposing = [\"Widgets.Banner\"]\n",
    );
    write(
        &widgets.join("src/Widgets/Banner.sky"),
        "module Widgets.Banner exposing (banner)\n\n\
import Sky.Core.Prelude exposing (..)\n\n\n\
banner : String -> String\n\
banner s =\n    \"** \" ++ s ++ \" **\"\n",
    );
    let app = base.join("app");
    write(
        &app.join("sky.toml"),
        "name = \"skypath\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    );
    write(
        &app.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.Log exposing (println)\n\
import Widgets.Banner exposing (banner)\n\n\n\
main =\n    println (banner \"sky\")\n",
    );
    let (ok, log) = run(&app, SKY, &["add", "../widgets"]);
    assert!(ok, "{log}");
    let toml = std::fs::read_to_string(app.join("sky.toml")).unwrap();
    assert!(
        toml.contains("[dependencies]\n\"widgets\" = { path = \"../widgets\" }"),
        "{toml}"
    );
    // Built from the PARENT directory: the relative path still resolves
    // against the project root, not the working directory.
    let (ok, log) = run(&base, SKY, &["build", "app/src/Main.sky"]);
    assert!(ok, "build from another cwd:\n{log}");
    let (ok, out) = run(&app, app.join("sky-out/app").to_str().unwrap(), &[]);
    assert!(ok && out.contains("** sky **"), "{out}");

    // `sky install` has nothing to fetch for it, and says so.
    let (ok, log) = run(&app, SKY, &["install"]);
    assert!(ok && log.contains("nothing to fetch"), "{log}");

    // Removed by its name.
    let (ok, log) = run(&app, SKY, &["remove", "widgets"]);
    assert!(ok, "{log}");
    assert!(!std::fs::read_to_string(app.join("sky.toml"))
        .unwrap()
        .contains("widgets"));
    let _ = std::fs::remove_dir_all(&base);
}

#[test]
fn a_missing_path_is_an_error_everywhere() {
    let base = scratch("missing");
    let app = base.join("app");
    write(
        &app.join("sky.toml"),
        "name = \"missing\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    );
    write(
        &app.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.Log exposing (println)\n\n\n\
main =\n    println \"hi\"\n",
    );
    // `sky add` of a directory that does not exist.
    let (ok, log) = run(&app, SKY, &["add", "./nope"]);
    assert!(!ok, "{log}");
    assert!(log.contains("does not exist"), "{log}");
    assert!(!std::fs::read_to_string(app.join("sky.toml"))
        .unwrap()
        .contains("nope"));

    // A directory that is neither a Go module nor a Sky package.
    std::fs::create_dir_all(base.join("empty")).unwrap();
    let (ok, log) = run(&app, SKY, &["add", "../empty"]);
    assert!(!ok && log.contains("neither a Go module"), "{log}");

    // A declared path dependency whose directory has gone: the build stops
    // and names it (no Go toolchain needed: it stops before `go build`).
    let mut toml = std::fs::read_to_string(app.join("sky.toml")).unwrap();
    toml.push_str("\n[dependencies]\n\"gone\" = { path = \"../gone\" }\n");
    std::fs::write(app.join("sky.toml"), &toml).unwrap();
    let (ok, log) = run(&app, SKY, &["check", "src/Main.sky"]);
    assert!(!ok, "{log}");
    assert!(
        log.contains("\"gone\"") && log.contains("does not exist"),
        "{log}"
    );

    // `sky doctor` warns about it, and about a path outside the project.
    std::fs::create_dir_all(base.join("outside/src")).unwrap();
    write(
        &base.join("outside/src/Outside.sky"),
        "module Outside exposing (x)\n\nimport Sky.Core.Prelude exposing (..)\n\n\nx : Int\nx =\n    1\n",
    );
    toml.push_str("\"outside\" = { path = \"../outside\" }\n");
    std::fs::write(app.join("sky.toml"), &toml).unwrap();
    let (_, log) = run(&app, SKY, &["doctor"]);
    assert!(
        log.contains("path dependency \"gone\"") && log.contains("does not exist"),
        "{log}"
    );
    assert!(
        log.contains("path dependency \"outside\"") && log.contains("is outside"),
        "{log}"
    );
    let _ = std::fs::remove_dir_all(&base);
}
