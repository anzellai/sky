//! Every pinned historical repro under `corpus/repro/` is BUILT and RUN.
//!
//! `corpus/repro/coordinates.toml` lists hand-written reproductions of shipped
//! defects, each with the stdout its author chose (or `reject`). The Layer-1
//! generator expands their coordinates into neighbourhoods, but until this test
//! nothing executed the repro files themselves: a repro could stop matching its
//! `expect_stdout` and every gate stayed green. This test is that gate.
//!
//! It includes the v0.27.0 round-3 downstream reproductions (`as` over a nested
//! constructor pattern, a Task-returning parameter in a `Task.andThen` chain,
//! let-polymorphism, `Module.value.field`). Needs a `go` toolchain.
//!
//! A single-file entry is staged as `src/Main.sky`. A directory entry is a
//! project (`src/…`), or a set of projects (one per sub-directory holding a
//! `src/`) that must ALL behave as declared.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("corpus/repro/coordinates.toml").is_file() {
            return dir;
        }
        assert!(dir.pop(), "could not locate the repo root");
    }
}

/// One `[[repro]]` entry — only the keys this test reads.
#[derive(Debug, Default)]
struct Repro {
    file: String,
    expect: String,
    expect_stdout: String,
}

/// Unquote a TOML basic string value (`"…"`), decoding `\n`, `\"`, `\\`.
fn unquote(v: &str) -> String {
    let v = v.trim();
    let inner = v
        .strip_prefix('"')
        .and_then(|s| s.strip_suffix('"'))
        .unwrap_or_else(|| panic!("expected a quoted TOML string, got {v:?}"));
    let mut out = String::new();
    let mut chars = inner.chars();
    while let Some(c) = chars.next() {
        if c == '\\' {
            match chars.next() {
                Some('n') => out.push('\n'),
                Some('"') => out.push('"'),
                Some('\\') => out.push('\\'),
                other => panic!("unsupported escape \\{other:?} in {v:?}"),
            }
        } else {
            out.push(c);
        }
    }
    out
}

/// Parse `coordinates.toml`. Each `[[repro]]` table must name each key at most
/// once — a duplicate key is invalid TOML, and a hand parser that silently took
/// the last one would test the wrong repro.
fn parse(src: &str) -> Vec<Repro> {
    let mut out: Vec<Repro> = Vec::new();
    let mut seen: Vec<String> = Vec::new();
    for line in src.lines() {
        let t = line.trim();
        if t.is_empty() || t.starts_with('#') {
            continue;
        }
        if t == "[[repro]]" {
            out.push(Repro::default());
            seen.clear();
            continue;
        }
        let Some((k, v)) = t.split_once('=') else {
            panic!("unparseable line in coordinates.toml: {line:?}");
        };
        let k = k.trim().to_string();
        let cur = out
            .last_mut()
            .unwrap_or_else(|| panic!("key {k:?} before the first [[repro]]"));
        assert!(
            !seen.contains(&k),
            "duplicate key {k:?} in the [[repro]] for {:?}",
            cur.file
        );
        seen.push(k.clone());
        match k.as_str() {
            "file" => cur.file = unquote(v),
            "expect" => cur.expect = unquote(v),
            "expect_stdout" => cur.expect_stdout = unquote(v),
            _ => {}
        }
    }
    out
}

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

fn scratch(tag: &str) -> PathBuf {
    std::env::temp_dir().join(format!(
        "sky-pinned-repro-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}

/// The projects an entry stands for, each staged into its own scratch dir.
fn stage(root: &Path, r: &Repro) -> Vec<PathBuf> {
    let src = root.join("corpus/repro").join(&r.file);
    let toml = "name = \"repro\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\nbin = \"app\"\n";
    let mut dirs = Vec::new();
    let mut add = |project_src: &Path, tag: &str| {
        let dir = scratch(tag);
        copy_dir(project_src, &dir.join("src"));
        std::fs::write(dir.join("sky.toml"), toml).unwrap();
        dirs.push(dir);
    };
    if src.is_file() {
        let dir = scratch("file");
        std::fs::create_dir_all(dir.join("src")).unwrap();
        std::fs::copy(&src, dir.join("src/Main.sky")).unwrap();
        std::fs::write(dir.join("sky.toml"), toml).unwrap();
        dirs.push(dir);
    } else if src.join("src").is_dir() {
        add(&src.join("src"), "dir");
    } else {
        let mut subs: Vec<PathBuf> = std::fs::read_dir(&src)
            .unwrap_or_else(|e| panic!("repro {:?} is missing: {e}", r.file))
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.join("src").is_dir())
            .collect();
        subs.sort();
        for s in subs {
            add(&s.join("src"), "sub");
        }
    }
    assert!(!dirs.is_empty(), "repro {:?} stages no project", r.file);
    dirs
}

#[test]
fn coordinates_toml_is_well_formed_and_every_file_exists() {
    let root = repo_root();
    let src = std::fs::read_to_string(root.join("corpus/repro/coordinates.toml")).unwrap();
    let repros = parse(&src);
    assert!(!repros.is_empty(), "coordinates.toml lists no repro");
    for r in &repros {
        assert!(!r.file.is_empty(), "a [[repro]] has no `file`");
        assert!(
            root.join("corpus/repro").join(&r.file).exists(),
            "repro file {:?} does not exist",
            r.file
        );
        assert!(
            r.expect == "accept" || r.expect == "reject",
            "repro {:?}: expect must be accept or reject, got {:?}",
            r.file,
            r.expect
        );
    }
}

#[test]
fn every_pinned_repro_builds_and_prints_its_expected_stdout() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let root = repo_root();
    let src = std::fs::read_to_string(root.join("corpus/repro/coordinates.toml")).unwrap();
    let mut failures = Vec::new();
    for r in parse(&src) {
        for dir in stage(&root, &r) {
            let build = Command::new(SKY)
                .args(["build", "src/Main.sky"])
                .current_dir(&dir)
                .stdin(std::process::Stdio::null())
                .output()
                .expect("spawn sky build");
            let log = format!(
                "{}{}",
                String::from_utf8_lossy(&build.stdout),
                String::from_utf8_lossy(&build.stderr)
            );
            match r.expect.as_str() {
                "reject" => {
                    if build.status.success() {
                        failures.push(format!("{}: expected a rejection, it built", r.file));
                    }
                }
                _ => {
                    if !build.status.success() {
                        failures.push(format!("{}: expected to build; got:\n{log}", r.file));
                    } else {
                        let run = Command::new(dir.join("sky-out/app"))
                            .current_dir(&dir)
                            .stdin(std::process::Stdio::null())
                            .output()
                            .expect("run the repro binary");
                        let got = String::from_utf8_lossy(&run.stdout);
                        let got = got.trim_end_matches('\n');
                        if !run.status.success() || got != r.expect_stdout {
                            failures.push(format!(
                                "{}: expected stdout {:?}, got {got:?} (exit {:?}, stderr {})",
                                r.file,
                                r.expect_stdout,
                                run.status.code(),
                                String::from_utf8_lossy(&run.stderr)
                            ));
                        }
                    }
                }
            }
            let _ = std::fs::remove_dir_all(&dir);
        }
    }
    assert!(
        failures.is_empty(),
        "{} pinned repro(s) did not behave as declared:\n{}",
        failures.len(),
        failures.join("\n")
    );
}
