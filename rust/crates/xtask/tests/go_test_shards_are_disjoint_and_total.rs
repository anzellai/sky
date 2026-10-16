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
    shard_weighted(list, k, n, None)
}

fn shard_weighted(list: &str, k: u32, n: u32, weights: Option<&std::path::Path>) -> Vec<String> {
    let mut cmd = Command::new("bash");
    if let Some(w) = weights {
        cmd.env("GO_TEST_DURATIONS", w);
    }
    let mut child = cmd
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

/// With measured durations the shards are still disjoint and total, the long
/// tests are spread across shards, and a test the durations file does not
/// know (a new or renamed test) is still assigned to exactly one shard.
#[test]
fn weighted_shards_balance_and_stay_disjoint_and_total() {
    let dir = std::env::temp_dir().join(format!("sky-go-shard-test-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let weights = dir.join("w.tsv");
    std::fs::write(
        &weights,
        "p\tTestSlowA\t300\np\tTestSlowB\t290\np\tTestSlowC\t10\np\tTestGone\t500\n",
    )
    .unwrap();
    let mut list = String::new();
    for t in [
        "TestSlowA",
        "TestSlowB",
        "TestSlowC",
        "TestNew1",
        "TestNew2",
        "TestNew3",
    ] {
        list.push_str(&format!("p\t{t}\n"));
    }
    let all: BTreeSet<String> = list.lines().map(str::to_string).collect();
    for n in 1..=4u32 {
        let mut seen = Vec::new();
        for k in 1..=n {
            seen.extend(shard_weighted(&list, k, n, Some(&weights)));
        }
        let uniq: BTreeSet<String> = seen.iter().cloned().collect();
        assert_eq!(uniq.len(), seen.len(), "N={n}: a pair runs twice: {seen:?}");
        assert_eq!(uniq, all, "N={n}: the shards do not cover the list");
    }
    let one = shard_weighted(&list, 1, 2, Some(&weights));
    let a = one.iter().any(|l| l.ends_with("TestSlowA"));
    let b = one.iter().any(|l| l.ends_with("TestSlowB"));
    assert!(a != b, "the two long tests landed in one shard: {one:?}");
    let _ = std::fs::remove_dir_all(&dir);
}
