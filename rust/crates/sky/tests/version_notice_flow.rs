//! The one-time "Sky upgraded X -> Y" notice (v0.27.0 migration addendum).
//!
//! The first run of a `sky` version on a machine prints the notice once, on
//! stderr, with the migration guide's link, and records the version in the
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

#[test]
fn the_first_run_of_a_version_prints_the_notice_once() {
    let dir = scratch("text");
    let cache = dir.join("cache");
    // `--version` never shows it and records nothing.
    let (_, err) = run(&dir, &cache, &["--version"]);
    assert!(!err.contains("Sky upgraded"), "{err}");
    assert!(!cache.join("sky/last-version").exists());
    // The first real command shows it once, on stderr.
    let (out, err) = run(&dir, &cache, &["fmt", "--check", "src/Main.sky"]);
    assert!(
        err.contains("Sky upgraded") && err.contains(GUIDE),
        "the first run prints the notice with the guide link:\n{err}"
    );
    assert!(!out.contains("Sky upgraded"), "never on stdout:\n{out}");
    assert!(cache.join("sky/last-version").is_file());
    // The second run prints nothing.
    let (_, err) = run(&dir, &cache, &["fmt", "--check", "src/Main.sky"]);
    assert!(!err.contains("Sky upgraded"), "only once:\n{err}");
    // A recorded older version names both ends.
    std::fs::write(cache.join("sky/last-version"), "sky v0.26.1\n").unwrap();
    let (_, err) = run(&dir, &cache, &["fmt", "--check", "src/Main.sky"]);
    assert!(err.contains("Sky upgraded sky v0.26.1 -> "), "{err}");
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
