//! The one-time "Sky upgraded X -> Y" notice (v0.27.0 migration addendum).
//!
//! The first run of a `sky` version on a machine prints the notice once, on
//! stderr when stderr is a terminal, with the migration guide's link, and records the version in the
//! user cache dir; the next run prints nothing. A `--format json` run carries
//! it as one NDJSON `notice` record instead, and `--version` never shows it.
//! Each case uses its own `XDG_CACHE_HOME`, so the machine's real record is
//! never read or written.

use std::path::{Path, PathBuf};
use std::process::Command;

const SKY: &str = env!("CARGO_BIN_EXE_sky");
const GUIDE: &str = "docs/migration/v0.27.md";

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-notice-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"notice\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\nimport Std.Log exposing (println)\n\n\nmain =\n    println \"hi\"\n",
    )
    .unwrap();
    dir
}

/// `sky <args>` in `dir` with its own cache root. Returns (stdout, stderr).
fn run(dir: &Path, cache: &Path, args: &[&str]) -> (String, String) {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .env("XDG_CACHE_HOME", cache)
        .env("HOME", cache)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    (
        String::from_utf8_lossy(&out.stdout).into_owned(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

/// `sky <args>` in `dir` under a pseudo-terminal (`script`), so stderr is a
/// terminal. Returns the terminal's output (stdout and stderr together).
fn run_tty(dir: &Path, cache: &Path, args: &[&str]) -> String {
    let mut cmd = Command::new("script");
    if cfg!(target_os = "macos") {
        cmd.arg("-q").arg("/dev/null").arg(SKY).args(args);
    } else {
        let line = std::iter::once(SKY)
            .chain(args.iter().copied())
            .map(|a| format!("'{a}'"))
            .collect::<Vec<_>>()
            .join(" ");
        cmd.args(["-qec", &line, "/dev/null"]);
    }
    let out = cmd
        .current_dir(dir)
        .env("XDG_CACHE_HOME", cache)
        .env("HOME", cache)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn script (util-linux or BSD) for a pseudo-terminal");
    String::from_utf8_lossy(&out.stdout).into_owned()
}

#[test]
fn the_first_run_of_a_version_prints_the_notice_once() {
    let dir = scratch("text");
    let cache = dir.join("cache");
    // `--version` never shows it and records nothing.
    let out = run_tty(&dir, &cache, &["--version"]);
    assert!(!out.contains("Sky upgraded"), "{out}");
    assert!(!cache.join("sky/last-version").exists());
    // The first real command on a terminal shows it once.
    let out = run_tty(&dir, &cache, &["fmt", "--check", "src/Main.sky"]);
    assert!(
        out.contains("Sky upgraded") && out.contains(GUIDE),
        "the first run prints the notice with the guide link:\n{out}"
    );
    assert!(cache.join("sky/last-version").is_file());
    // The second run prints nothing.
    let out = run_tty(&dir, &cache, &["fmt", "--check", "src/Main.sky"]);
    assert!(!out.contains("Sky upgraded"), "only once:\n{out}");
    // A recorded older version names both ends.
    std::fs::write(cache.join("sky/last-version"), "sky v0.26.1\n").unwrap();
    let out = run_tty(&dir, &cache, &["fmt", "--check", "src/Main.sky"]);
    assert!(out.contains("Sky upgraded sky v0.26.1 -> "), "{out}");
    let _ = std::fs::remove_dir_all(&dir);
}

/// The TTY rule: with stderr not a terminal (a script, CI, a pipe) and no
/// `--format json`, the notice is neither printed nor recorded, so the first
/// run a person sees still shows it.
#[test]
fn a_run_with_no_terminal_neither_prints_nor_records_the_notice() {
    let dir = scratch("notty");
    let cache = dir.join("cache");
    let (out, err) = run(&dir, &cache, &["fmt", "--check", "src/Main.sky"]);
    assert!(
        !err.contains("Sky upgraded") && !out.contains("Sky upgraded"),
        "{err}"
    );
    assert!(
        !cache.join("sky/last-version").exists(),
        "a run that showed nothing must not use up the notice"
    );
    let out = run_tty(&dir, &cache, &["fmt", "--check", "src/Main.sky"]);
    assert!(
        out.contains("Sky upgraded"),
        "the next terminal run shows it:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_json_run_carries_the_notice_as_one_record() {
    let dir = scratch("json");
    let cache = dir.join("cache");
    let (out, err) = run(
        &dir,
        &cache,
        &["fmt", "--check", "--format", "json", "src/Main.sky"],
    );
    assert!(
        !err.contains("Sky upgraded"),
        "not as text in json mode:\n{err}"
    );
    let notices: Vec<serde_json::Value> = out
        .lines()
        .map(|l| serde_json::from_str::<serde_json::Value>(l).expect("NDJSON"))
        .filter(|v| v["kind"] == "notice")
        .collect();
    assert_eq!(notices.len(), 1, "one notice record:\n{out}");
    assert_eq!(notices[0]["schema"], 1);
    assert!(notices[0]["guide"].as_str().unwrap().contains(GUIDE));
    assert!(notices[0]["silent"].is_array());
    let last: serde_json::Value = serde_json::from_str(out.lines().last().unwrap()).unwrap();
    assert_eq!(last["kind"], "summary", "the summary is still last:\n{out}");
    // The second json run carries none.
    let (out, _) = run(
        &dir,
        &cache,
        &["fmt", "--check", "--format", "json", "src/Main.sky"],
    );
    assert!(!out.contains("\"notice\""), "{out}");
    let _ = std::fs::remove_dir_all(&dir);
}
