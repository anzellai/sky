//! Every `go build` `sky` runs is made by `project::go_cmd` — one builder, one
//! set of flags.
//!
//! The Go build cache keys a package on its flags. Before the builder, the
//! native build, the wasm build and the cache warm-up each made their own
//! `Command::new("go")` with their own `GOFLAGS`, and none passed `-trimpath`:
//! the warm-up's objects were never a project build's cache hits, and every new
//! project directory recompiled the runtime. A new call site that makes its own
//! `go build` would bring that back for its path, so this test fails on one.

use std::path::{Path, PathBuf};

/// The builder itself.
const BUILDER: &str = "project/src/go_cmd.rs";

/// `go build` sites that are not an app build, each with its reason. A new
/// entry needs one.
const ALLOWED: &[(&str, &str)] = &[(
    "ffi/src/inspect.rs",
    "builds the sky-ffi-inspect helper tool once per Sky version into its own \
     tools dir; the `ffi` crate sits below `project` and cannot use its builder",
)];

fn crates_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .to_path_buf()
}

fn rs_files(dir: &Path, out: &mut Vec<PathBuf>) {
    for e in std::fs::read_dir(dir).unwrap().flatten() {
        let p = e.path();
        if p.is_dir() {
            rs_files(&p, out);
        } else if p.extension().is_some_and(|x| x == "rs") {
            out.push(p);
        }
    }
}

/// The `go` commands in `src` that run `build`: each `Command::new("go")` to
/// the end of its statement, plus the statements right after it that go on
/// configuring the same `let mut <var>` (`cmd.arg("build")`), skipping
/// comments. Whitespace-insensitive.
fn go_build_sites(src: &str) -> Vec<usize> {
    let squash = |t: &str| -> String { t.chars().filter(|c| !c.is_whitespace()).collect() };
    let mut sites = Vec::new();
    let mut from = 0;
    while let Some(i) = src[from..].find("Command::new(\"go\")") {
        let at = from + i;
        let line_start = src[..at].rfind('\n').map_or(0, |j| j + 1);
        let var = src[line_start..at]
            .trim()
            .strip_prefix("let mut ")
            .and_then(|r| r.split('=').next())
            .map(|v| v.trim().to_string())
            .filter(|v| !v.is_empty());
        let mut end = src[at..].find(';').map_or(src.len(), |j| at + j);
        let mut stmt = squash(&src[at..end]);
        if let Some(v) = var {
            let prefix = format!("{v}.");
            loop {
                let rest = &src[(end + 1).min(src.len())..];
                let mut skipped = 0;
                for l in rest.split_inclusive('\n') {
                    if l.trim().is_empty() || l.trim_start().starts_with("//") {
                        skipped += l.len();
                    } else {
                        break;
                    }
                }
                let next = &rest[skipped..];
                if !next.trim_start().starts_with(&prefix) {
                    break;
                }
                let s0 = end + 1 + skipped;
                end = src[s0..].find(';').map_or(src.len(), |j| s0 + j);
                stmt.push_str(&squash(&src[s0..end]));
            }
        }
        if stmt.contains("\"build\"") {
            sites.push(src[..at].matches('\n').count() + 1);
        }
        from = at + 1;
    }
    sites
}

#[test]
fn only_the_builder_makes_a_go_build_command() {
    let root = crates_dir();
    let mut files = Vec::new();
    for krate in ["project", "sky", "ffi"] {
        rs_files(&root.join(krate).join("src"), &mut files);
    }
    let mut bad = Vec::new();
    for f in &files {
        let rel = f
            .strip_prefix(&root)
            .unwrap()
            .to_string_lossy()
            .replace('\\', "/");
        if rel == BUILDER || ALLOWED.iter().any(|(p, _)| *p == rel) {
            continue;
        }
        let src = std::fs::read_to_string(f).unwrap();
        for line in go_build_sites(&src) {
            bad.push(format!("{rel}:{line}"));
        }
    }
    assert!(
        bad.is_empty(),
        "these make their own `go build` command; use project::go_cmd::GoBuild \
         so the build gets -trimpath, Sky's GOFLAGS and its build cache:\n  {}",
        bad.join("\n  ")
    );
    // The builder is real: it does make the command.
    let builder = std::fs::read_to_string(root.join(BUILDER)).unwrap();
    assert!(builder.contains("Command::new(\"go\")"));
}

#[test]
fn the_scan_finds_a_go_build_however_it_is_written() {
    let one_line = "let c = Command::new(\"go\").args([\"build\", \"-o\"]).arg(x);";
    let split = "let mut c = Command::new(\"go\")\n    .arg(\n        \"build\",\n    );";
    let other = "let c = Command::new(\"go\").args([\"env\", \"GOROOT\"]);";
    // The shape the native build had: the command bound, then configured.
    let bound = "let mut cmd = Command::new(\"go\");\n    // a comment\n    cmd.arg(\"build\").args(jobs);\n    cmd.arg(\"-o\");";
    assert_eq!(go_build_sites(bound), vec![1]);
    assert_eq!(go_build_sites(one_line), vec![1]);
    assert_eq!(go_build_sites(split), vec![1]);
    assert!(go_build_sites(other).is_empty());
}
