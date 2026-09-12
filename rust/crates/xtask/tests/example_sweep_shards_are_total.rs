//! `scripts/example-sweep.sh` shard partition is TOTAL and DISJOINT.
//!
//! The release gate shards the full example sweep across a `strategy.matrix` of
//! N runners (release.yml, `gate-sweep`). Each shard runs the examples whose
//! stable index mod TOTAL == INDEX. If the partition dropped an example, a
//! regression in it would ship unseen; if two shards ran the same example, a
//! runner would waste time and a port race could mask a failure. Either defeats
//! the point of §0.2.1 (the FULL sweep gates the release).
//!
//! This drives the REAL script in its `--list` mode (which enumerates the
//! sharded example names and builds nothing — it needs no compiler), so it
//! tests the script's actual behaviour, not a re-implementation of the maths.
//! For each N it asserts: the union of shards 0..N equals the unsharded full
//! set, and no name appears in more than one shard.

use std::collections::BTreeSet;
use std::path::PathBuf;
use std::process::Command;

fn repo_root() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
        .canonicalize()
        .expect("canonicalize repo root")
}

/// Run `example-sweep.sh --list` (optionally sharded) and return the names.
fn list(shard: Option<(u32, u32)>) -> Vec<String> {
    let mut cmd = Command::new("bash");
    cmd.arg("scripts/example-sweep.sh")
        .arg("--list")
        .current_dir(repo_root());
    if let Some((i, t)) = shard {
        cmd.env("SWEEP_SHARD_INDEX", i.to_string())
            .env("SWEEP_SHARD_TOTAL", t.to_string());
    }
    let out = cmd.output().expect("run example-sweep.sh --list (bash on PATH?)");
    assert!(
        out.status.success(),
        "example-sweep.sh --list {:?} exited {:?}:\n{}",
        shard,
        out.status.code(),
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8_lossy(&out.stdout)
        .lines()
        .map(str::trim)
        .filter(|l| !l.is_empty())
        .map(str::to_string)
        .collect()
}

#[test]
fn the_full_list_has_a_real_corpus() {
    // Anti-vacuity: a broken --list that printed nothing would make every
    // union check below pass over the empty set.
    let full = list(None);
    assert!(
        full.len() > 5,
        "example-sweep.sh --list returned only {} names — the script or the \
         parse is broken, not the repo",
        full.len()
    );
    // No duplicates in the base table either.
    let uniq: BTreeSet<&String> = full.iter().collect();
    assert_eq!(
        uniq.len(),
        full.len(),
        "the unsharded example list has duplicate entries: {full:?}"
    );
}

#[test]
fn shards_are_disjoint_and_total_for_each_n() {
    let full: BTreeSet<String> = list(None).into_iter().collect();

    // Test a range of shard counts, including the N=5 the release gate uses and
    // edge cases (N=1 must equal the full set; N larger than the corpus must
    // leave some shards empty yet still union to the whole set).
    for total in [1u32, 2, 3, 5, 7, 13, (full.len() as u32) + 3] {
        let mut union: Vec<String> = Vec::new();
        for index in 0..total {
            union.extend(list(Some((index, total))));
        }

        // Disjoint: no name appears in two shards.
        let union_set: BTreeSet<String> = union.iter().cloned().collect();
        assert_eq!(
            union_set.len(),
            union.len(),
            "N={total}: shards overlap — a name appears in more than one shard.\n\
             union (with dups): {union:?}"
        );

        // Total: the union is exactly the full set.
        assert_eq!(
            union_set, full,
            "N={total}: the union of shards is NOT the full example set.\n\
             missing from shards: {:?}\n\
             extra in shards: {:?}",
            full.difference(&union_set).collect::<Vec<_>>(),
            union_set.difference(&full).collect::<Vec<_>>()
        );
    }
}
