//! Coverage-guided `sky fuzz`, end to end.
//!
//! The fixture's `update` panics (a classified `DivisionByZero`) only after
//! `Open`, `Turn`, `Fire` in that order, behind nested `case` arms
//! (`case model.stage of` over `case msg of`), among 20 Msgs. Any other Msg
//! goes back to the start. With the same seed and Msg budget, the unguided net
//! misses it and the guided net, which keeps the Msg prefixes that reach a new
//! arm and extends them, finds it and prints the shrunk steps. Both runs are
//! deterministic (one seed drives every choice). `sky fuzz --coverage` records
//! a run that `sky coverage report` reads. Needs a `go` toolchain.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// The budget and seed the two runs share. Chosen so the unguided run misses
/// the panic; the guided run finds it well within the budget.
const ITERS: &str = "1000";
const SEED: &str = "1";

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn stage(tag: &str) -> PathBuf {
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/fuzz-guided");
    let dir = std::env::temp_dir().join(format!(
        "sky-fuzz-guided-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::copy(fixture.join("sky.toml"), dir.join("sky.toml")).unwrap();
    std::fs::copy(fixture.join("src/Main.sky"), dir.join("src/Main.sky")).unwrap();
    dir
}

fn sky(dir: &Path, args: &[&str]) -> (i32, String) {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .env_remove("DATABASE_URL")
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.code().unwrap_or(-1), s)
}

#[test]
fn the_guided_net_finds_an_order_the_unguided_net_misses() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage("order");
    let fuzz = ["fuzz", "src/Main.sky", "--iters", ITERS, "--seed", SEED];

    let mut unguided = fuzz.to_vec();
    unguided.push("--unguided");
    let (code, out) = sky(&dir, &unguided);
    assert_eq!(code, 0, "the unguided run must miss the panic:\n{out}");
    assert!(out.contains("unguided, 0 kept"), "{out}");
    assert!(out.contains("no unclassified panic"), "{out}");

    for _ in 0..2 {
        // Twice: the same seed gives the same finding.
        let (code, out) = sky(&dir, &fuzz);
        assert_ne!(code, 0, "the guided run must find the panic:\n{out}");
        assert!(
            out.contains("FAIL") && out.contains("DivisionByZero"),
            "the panic must be reported as before:\n{out}"
        );
        assert!(
            out.contains("panicking sequence (3 Msg(s) from init): Open, Turn, Fire"),
            "the shrunk steps:\n{out}"
        );
        assert!(out.contains("case arms reached 9/9"), "{out}");
    }
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn fuzz_coverage_records_a_run_the_report_reads() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage("cov");
    // A short unguided run reaches the stage-0 arms only.
    let (code, out) = sky(
        &dir,
        &[
            "fuzz",
            "src/Main.sky",
            "--iters",
            "5",
            "--seed",
            "3",
            "--unguided",
            "--coverage",
        ],
    );
    assert_eq!(code, 0, "{out}");
    assert!(out.contains("sky coverage report"), "{out}");
    let (code, json) = sky(&dir, &["coverage", "report", "--format", "json"]);
    assert_eq!(code, 0, "{json}");
    let v: serde_json::Value = serde_json::from_str(&json).expect("report is JSON");
    assert_eq!(v["runs"], 1);
    let modules = v["modules"].as_array().unwrap();
    assert_eq!(modules.len(), 1, "{json}");
    let functions = modules[0]["functions"].as_array().unwrap();
    // The harness's own definitions (genMsg, the fuzz driver) are left out.
    let names: Vec<&str> = functions
        .iter()
        .map(|f| f["name"].as_str().unwrap())
        .collect();
    assert!(names.contains(&"update"), "{names:?}");
    assert!(
        !names
            .iter()
            .any(|n| n.starts_with("genMsg") || n.starts_with("spa")),
        "{names:?}"
    );
    let update = functions.iter().find(|f| f["name"] == "update").unwrap();
    assert_eq!(update["hit"], true);
    let arms = update["arms"].as_array().unwrap();
    assert_eq!(arms.len(), 9, "{json}");
    let hit = arms.iter().filter(|a| a["hit"] == true).count();
    assert!(
        hit >= 2 && hit < 9,
        "a short run reaches some arms, not all: {json}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
