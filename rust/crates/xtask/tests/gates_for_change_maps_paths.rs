//! `scripts/gates-for-change.sh` maps changed paths to the narrowest gates.
//!
//! It is the local half of the gate policy (CLAUDE.md §0.2): run the gates a
//! change can break, and leave the full suite to the release workflow. A map
//! that silently dropped a path would let a change reach that suite untested,
//! so these tests drive the real script (`--dry-run`) in a scratch repository
//! and assert the plan for the paths the policy names — and that a path no
//! rule claims is reported, not ignored.

use std::path::{Path, PathBuf};
use std::process::Command;

fn repo() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

fn git(dir: &Path, args: &[&str]) {
    let ok = Command::new("git")
        .arg("-C")
        .arg(dir)
        .args(args)
        .output()
        .expect("git runs")
        .status
        .success();
    assert!(ok, "git {args:?}");
}

fn write(dir: &Path, rel: &str, body: &str) {
    let p = dir.join(rel);
    std::fs::create_dir_all(p.parent().unwrap()).unwrap();
    std::fs::write(p, body).unwrap();
}

/// A scratch repo holding the script, with `changes` made after the base.
fn plan_for(tag: &str, changes: &[&str]) -> String {
    let dir = std::env::temp_dir().join(format!("sky-gfc-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("scripts/lib")).unwrap();
    for rel in ["scripts/gates-for-change.sh", "scripts/lib/with-timeout.sh"] {
        std::fs::copy(repo().join(rel), dir.join(rel)).unwrap();
    }
    git(&dir, &["init", "-q", "-b", "main"]);
    git(&dir, &["config", "user.email", "t@example.invalid"]);
    git(&dir, &["config", "user.name", "t"]);
    git(&dir, &["add", "-A"]);
    git(&dir, &["commit", "-q", "-m", "base"]);
    for c in changes {
        write(&dir, c, "changed\n");
    }
    let shell = if Path::new("/bin/bash").exists() {
        "/bin/bash"
    } else {
        "bash"
    };
    let out = Command::new(shell)
        .arg(dir.join("scripts/gates-for-change.sh"))
        .args(["--dry-run", "--base", "main"])
        .output()
        .expect("the script runs");
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let _ = std::fs::remove_dir_all(&dir);
    assert!(out.status.success(), "dry-run failed:\n{text}");
    text
}

#[test]
fn the_go_runtime_maps_to_go_test_and_the_web_e2e() {
    let p = plan_for("rt", &["runtime-go/rt/live.go"]);
    for want in [
        "go test -p 1",
        "scripts/build.sh",
        "scripts/verify-all-web.sh",
        "scripts/live-client-e2e.sh",
        "gofmt",
    ] {
        assert!(p.contains(want), "missing `{want}`:\n{p}");
    }
}

#[test]
fn the_spa_split_maps_to_project_tests_and_the_spa_e2e() {
    let p = plan_for("spa", &["rust/crates/project/src/spa_split.rs"]);
    for want in [
        "cargo test -p project",
        "scripts/spa-rpc-consistency-e2e.sh",
        "scripts/spa-restore-e2e.sh",
        "spa-diff-fuzz",
        "cargo fmt",
    ] {
        assert!(p.contains(want), "missing `{want}`:\n{p}");
    }
    assert!(
        !p.contains("scripts/verify-all-web.sh"),
        "a split change does not need the whole browser tier:\n{p}"
    );
}

#[test]
fn the_type_checker_maps_to_the_compiler_gates() {
    let p = plan_for("ty", &["rust/crates/ty/src/infer.rs"]);
    for want in [
        "cargo test -p ty",
        "coerce-floor",
        "--tier t2",
        "scripts/example-sweep.sh",
        "build-run --all",
    ] {
        assert!(p.contains(want), "missing `{want}`:\n{p}");
    }
}

#[test]
fn docs_map_to_the_doc_examples() {
    let p = plan_for("docs", &["docs/skyui/overview.md"]);
    assert!(p.contains("scripts/doc-examples.sh"), "{p}");
    assert!(
        !p.contains("scripts/build.sh"),
        "a doc edit needs no compiler:\n{p}"
    );
}

#[test]
fn a_changed_example_is_built_and_run_by_name() {
    let p = plan_for("ex", &["examples/19-skyforum/src/Main.sky"]);
    assert!(p.contains("build-run --only=19-skyforum --run"), "{p}");
    assert!(p.contains("roundtrip"), "{p}");
}

#[test]
fn an_unclaimed_path_is_reported_not_ignored() {
    let p = plan_for("unmapped", &["flake.nix"]);
    assert!(p.contains("UNMAPPED") && p.contains("flake.nix"), "{p}");
}

#[test]
fn every_change_ends_with_the_incremental_falsifier_run() {
    let p = plan_for("falsify", &["apps/ledger/src/Repo.sky"]);
    assert!(p.contains("--only apps-ledger"), "{p}");
    let last = p
        .lines()
        .filter(|l| l.trim_start().starts_with('$'))
        .last()
        .unwrap_or_default();
    assert!(
        last.contains("--verify-falsifiers") && !last.contains("--all"),
        "the last step re-proves only what changed: {last}"
    );
}
