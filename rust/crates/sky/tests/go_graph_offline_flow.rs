//! After `sky install`, `sky build` needs no network.
//!
//! The build's go.mod is the runtime's requirements plus the project's Go
//! dependencies, and minimal version selection over the two can RAISE a module
//! the runtime itself imports. Here a local Go dependency requires
//! `github.com/google/uuid v1.99.0` (a version served only by this test's own
//! file proxy) while `runtime-go/go.mod` pins v1.6.0, so the build compiles the
//! runtime against v1.99.0. Before the fix `sky install` resolved the
//! dependency without the runtime's packages present: it never downloaded the
//! raised uuid (or any module only the runtime imports), and an offline build
//! failed with `reading …/uuid/@v/v1.99.0.zip: no such file or directory`. That
//! is the nightly bench failure on examples/13-skyshop, where a newer
//! google.golang.org/api raised go.opentelemetry.io/proto/otlp.
//!
//! The install runs against an empty module cache of its own; the build then
//! runs with `GOPROXY=off`, so anything the install did not fetch fails it.

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
        "sky-gograph-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&dir).unwrap();
    dir.canonicalize().unwrap()
}

fn write(p: &Path, text: &str) {
    std::fs::create_dir_all(p.parent().unwrap()).unwrap();
    std::fs::write(p, text).unwrap();
}

fn run(dir: &Path, prog: &str, args: &[&str], env: &[(&str, String)]) -> (bool, String) {
    let mut c = Command::new(prog);
    c.args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null());
    for (k, v) in env {
        c.env(k, v);
    }
    let out = c.output().expect("spawn");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), s)
}

/// The developer's own module cache: the source of the real uuid v1.6.0 and a
/// local mirror of the runtime's modules.
fn real_modcache() -> String {
    let out = Command::new("go")
        .args(["env", "GOMODCACHE"])
        .output()
        .expect("go env");
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

const MKZIP: &str = r#"package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
)

func main() {
	src, prefix, out := os.Args[1], os.Args[2], os.Args[3]
	f, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	w := zip.NewWriter(f)
	err = filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		h, err := w.Create(prefix + "/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		r, err := os.Open(p)
		if err != nil {
			return err
		}
		defer r.Close()
		_, err = io.Copy(h, r)
		return err
	})
	if err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	if err := f.Close(); err != nil {
		panic(err)
	}
}
"#;

/// A file proxy serving `github.com/google/uuid v1.99.0`: the source of the
/// real v1.6.0 under a version no public proxy has.
fn fake_uuid_proxy(base: &Path) -> PathBuf {
    let tools = base.join("tools");
    let (ok, log) = run(
        &tools_dir(&tools),
        "go",
        &["mod", "download", "-json", "github.com/google/uuid@v1.6.0"],
        &[("GOFLAGS", String::new())],
    );
    assert!(ok, "go mod download uuid@v1.6.0:\n{log}");
    let dir = log
        .lines()
        .find_map(|l| {
            l.trim()
                .strip_prefix("\"Dir\": \"")
                .and_then(|r| r.strip_suffix("\","))
        })
        .unwrap_or_else(|| panic!("no Dir in:\n{log}"))
        .to_string();
    let proxy = base.join("proxy");
    let at_v = proxy.join("github.com/google/uuid/@v");
    write(&at_v.join("list"), "v1.99.0\n");
    write(
        &at_v.join("v1.99.0.info"),
        "{\"Version\":\"v1.99.0\",\"Time\":\"2026-01-01T00:00:00Z\"}\n",
    );
    write(
        &at_v.join("v1.99.0.mod"),
        &std::fs::read_to_string(Path::new(&dir).join("go.mod")).unwrap(),
    );
    write(&tools.join("mkzip.go"), MKZIP);
    let zip = at_v.join("v1.99.0.zip");
    let (ok, log) = run(
        &tools,
        "go",
        &[
            "run",
            "mkzip.go",
            &dir,
            "github.com/google/uuid@v1.99.0",
            zip.to_str().unwrap(),
        ],
        &[("GOFLAGS", String::new())],
    );
    assert!(ok, "go run mkzip.go:\n{log}");
    proxy
}

fn tools_dir(p: &Path) -> PathBuf {
    std::fs::create_dir_all(p).unwrap();
    p.to_path_buf()
}

#[test]
fn a_build_after_install_needs_no_network_when_a_dependency_raises_a_runtime_module() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let base = scratch("offline");
    let proxy = fake_uuid_proxy(&base);
    let modcache = base.join("modcache");
    std::fs::create_dir_all(&modcache).unwrap();

    // The local Go dependency: it imports nothing, but its go.mod requires
    // the newer uuid, which raises the version the runtime is built with.
    let raiser = base.join("raiser");
    write(
        &raiser.join("go.mod"),
        "module example.com/raiser\n\ngo 1.22\n\nrequire github.com/google/uuid v1.99.0\n",
    );
    write(
        &raiser.join("raiser.go"),
        "package raiser\n\n// Hello greets.\nfunc Hello(name string) string {\n\treturn \"hi \" + name\n}\n",
    );
    let app = base.join("app");
    write(
        &app.join("sky.toml"),
        "name = \"gograph\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    );
    write(
        &app.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
         import Example.Com.Raiser as Raiser\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.Result as Result\n\
         import Std.Log exposing (println)\n\n\n\
         main =\n    println (Raiser.hello \"sky\" |> Result.withDefault \"err\")\n",
    );

    let common = |goproxy: String| -> Vec<(&'static str, String)> {
        vec![
            ("GOMODCACHE", modcache.to_string_lossy().into_owned()),
            ("GOPROXY", goproxy),
            ("GOSUMDB", "off".to_string()),
            ("GOTOOLCHAIN", "local".to_string()),
            ("GOFLAGS", "-mod=mod".to_string()),
        ]
    };
    // "Online": this test's proxy first, then the developer's module cache,
    // then the public proxy for anything neither holds.
    let online = common(format!(
        "file://{},file://{}/cache/download,https://proxy.golang.org",
        proxy.display(),
        real_modcache()
    ));
    let offline = common("off".to_string());

    let (ok, log) = run(&app, SKY, &["add", "../raiser"], &online);
    assert!(ok, "sky add ../raiser:\n{log}");
    let (ok, log) = run(&app, SKY, &["install"], &online);
    assert!(ok, "sky install:\n{log}");

    let (ok, log) = run(&app, SKY, &["build", "src/Main.sky"], &offline);
    let go_mod = std::fs::read_to_string(app.join("sky-out/go.mod")).unwrap_or_default();
    let modcache_s = modcache.to_string_lossy().into_owned();
    let cleanup = || {
        let _ = Command::new("go")
            .args(["clean", "-modcache"])
            .env("GOMODCACHE", &modcache_s)
            .output();
        let _ = std::fs::remove_dir_all(&base);
    };
    if !ok {
        cleanup();
        panic!("an offline build after `sky install` must succeed:\n{log}");
    }
    assert!(
        go_mod.contains("github.com/google/uuid v1.99.0"),
        "the build uses the raised version:\n{go_mod}"
    );
    let bin = app.join("sky-out/app");
    let (ok, out) = run(&app, bin.to_str().unwrap(), &[], &[]);
    cleanup();
    assert!(ok && out.contains("hi sky"), "{out}");
}
