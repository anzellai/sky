//! `scripts/ci/go-test-shard.sh` splits a `go test` run by test, and its shards
//! are DISJOINT and TOTAL: for every N, shards 1..=N together select every
//! (package, test) pair of the list exactly once.
//!
//! The race run of the Go runtime tests (655-668 s in one job) is split across
//! two jobs in rust-ci and in the release gate. Each job lists the same tests
//! and takes its share by index; a partition that dropped or repeated a pair
//! would leave a test unrun, or run it twice, while every job reports green.
//! This drives the REAL script's `--partition` mode (pure; no Go toolchain) on
//! a list shaped like `go test -list` output after parsing: unsorted, with a
//! duplicate, and with the same test name in two packages.

use std::collections::BTreeSet;
use std::io::Write;
use std::path::PathBuf;
use std::process::{Command, Stdio};

fn repo_root() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
        .canonicalize()
        .expect("canonicalize repo root")
}

fn shard(list: &str, k: u32, n: u32) -> Vec<String> {
    let mut child = Command::new("bash")
        .arg("scripts/ci/go-test-shard.sh")
        .arg("--partition")
        .arg(format!("{k}/{n}"))
        .current_dir(repo_root())
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("run go-test-shard.sh (bash on PATH?)");
    child
        .stdin
        .take()
        .unwrap()
        .write_all(list.as_bytes())
        .unwrap();
    let out = child.wait_with_output().unwrap();
    assert!(
        out.status.success(),
        "--partition {k}/{n} exited {:?}: {}",
        out.status.code(),
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8_lossy(&out.stdout)
        .lines()
        .map(str::to_string)
        .collect()
}

#[test]
fn shards_are_disjoint_and_total_for_each_n() {
    let mut list = String::new();
    for p in ["sky-app/rt", "sky-app/rt/hub", "sky-app/rt/jobs"] {
        for t in [
            "TestZeta",
            "TestAlpha",
            "ExampleBeta",
            "FuzzGamma",
            "TestDelta",
        ] {
            list.push_str(&format!("{p}\t{t}\n"));
        }
    }
    list.push_str("sky-app/rt\tTestAlpha\n"); // a duplicate line
    let all: BTreeSet<String> = list.lines().map(str::to_string).collect();
    assert_eq!(all.len(), 15);
    for n in 1..=6u32 {
        let mut seen: Vec<String> = Vec::new();
        for k in 1..=n {
            let got = shard(&list, k, n);
            if n <= 15 {
                assert!(!got.is_empty(), "shard {k}/{n} is empty");
            }
            seen.extend(got);
        }
        let uniq: BTreeSet<String> = seen.iter().cloned().collect();
        assert_eq!(
            uniq.len(),
            seen.len(),
            "N={n}: a pair runs in two shards: {seen:?}"
        );
        assert_eq!(uniq, all, "N={n}: the shards do not cover the list");
    }
}

#[test]
fn an_out_of_range_shard_is_a_usage_error() {
    for spec in ["0/2", "3/2", "x/2", "2"] {
        let out = Command::new("bash")
            .arg("scripts/ci/go-test-shard.sh")
            .arg("--partition")
            .arg(spec)
            .current_dir(repo_root())
            .stdin(Stdio::null())
            .output()
            .unwrap();
        assert_eq!(out.status.code(), Some(2), "accepted shard `{spec}`");
    }
}
