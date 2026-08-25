//! `test-sky` (`cargo test -p sky`) is sharded across sibling CI jobs by
//! `scripts/ci/sky-test-shard.sh`. That partition is SHELL, so this test is its
//! safety net: run the script for every shard of N, collect the `--test`
//! binaries, and assert they union to exactly the `rust/crates/sky/tests/*.rs`
//! set with no binary in two shards. A dropped binary is a false-green — its
//! tests run in NO job across the fan-out, so a regression there ships unseen.

use std::path::PathBuf;
use std::process::Command;

fn repo_root() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

fn shard(root: &PathBuf, i: usize, n: usize) -> String {
    let out = Command::new("bash")
        .arg(root.join("scripts/ci/sky-test-shard.sh"))
        .arg(i.to_string())
        .arg(n.to_string())
        .output()
        .expect("run sky-test-shard.sh");
    assert!(
        out.status.success(),
        "sky-test-shard.sh {i} {n} failed: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8_lossy(&out.stdout).into_owned()
}

/// The `--test <name>` binaries in a shard's flag string.
fn test_binaries(flags: &str) -> Vec<String> {
    let toks: Vec<&str> = flags.split_whitespace().collect();
    let mut names = Vec::new();
    let mut k = 0;
    while k < toks.len() {
        if toks[k] == "--test" && k + 1 < toks.len() {
            names.push(toks[k + 1].to_string());
            k += 2;
        } else {
            k += 1;
        }
    }
    names
}

#[test]
fn sky_test_shards_are_total_and_disjoint() {
    let root = repo_root();

    let mut expect: Vec<String> = std::fs::read_dir(root.join("rust/crates/sky/tests"))
        .expect("read rust/crates/sky/tests")
        .filter_map(|e| e.ok())
        .filter_map(|e| {
            let p = e.path();
            if p.extension().and_then(|x| x.to_str()) == Some("rs") {
                p.file_stem().and_then(|s| s.to_str()).map(String::from)
            } else {
                None
            }
        })
        .collect();
    expect.sort();
    assert!(
        !expect.is_empty(),
        "no sky integration tests found — the glob is wrong, not the repo"
    );

    // For the CI shard count (2) and a couple of others, the union of shards must
    // be the whole set and disjoint. Robust to future shard-count changes.
    for n in [2usize, 3] {
        let mut union: Vec<String> = Vec::new();
        for i in 0..n {
            union.extend(test_binaries(&shard(&root, i, n)));
        }
        let before = union.len();
        union.sort();
        union.dedup();
        assert_eq!(
            union.len(),
            before,
            "n={n}: a test binary appears in more than one shard (not disjoint)"
        );
        assert_eq!(
            union, expect,
            "n={n}: shards do not union to the sky/tests set — a binary is dropped or duplicated"
        );
    }

    // The crate lib tests run exactly once, in shard 0.
    assert!(
        shard(&root, 0, 2).contains("--lib"),
        "shard 0 must carry --lib (the crate unit tests)"
    );
    assert!(
        !shard(&root, 1, 2).contains("--lib"),
        "only shard 0 runs --lib; a second --lib would run the unit tests twice"
    );
}
