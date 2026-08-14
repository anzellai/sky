//! The Stage-1 FREEZE — `comparer.go` and `keys.go` are immutable after Stage 1.
//!
//! # Why these two files, and why a hash rather than a review note
//!
//! `docs/bluedb/P1-STAGE2-PLAN.md` ranks it risk **#1**, above every deadlock
//! and every fail-open:
//!
//! > **Someone fixes N1 in `skydbSplit`.** Irreversible under a frozen comparer
//! > name. Guard: `git diff --exit-code` on `comparer.go` and `keys.go`.
//!
//! N1 — `pebbleReader.Iterate`'s bounds ending in an arbitrary user byte, which
//! `skydbSplit` then mis-reads as a suffix length — has an obvious fix in
//! `skydbSplit` and that fix is a **one-way door**. `comparerName` is
//! `"skydb.mvcc.v1"`, and Pebble writes it into SSTable metadata: changing
//! `Split` changes on-disk ordering, breaks the leading-byte-stripping invariant
//! `base.CheckComparer` enforces, and requires bumping to `skydb.mvcc.v2` plus a
//! **full store rewrite** of every deployed database. The bug was therefore
//! fixed in the CALLER (`reader.go`), and the plan required a standing guard
//! that these two files never move again.
//!
//! That guard did not exist. The property was true only by evidence — someone
//! looking at `git diff` — which is exactly the class of "true today, unenforced
//! tomorrow" this branch exists to eliminate. The plan's own suggestion (a
//! pre-commit `git diff --exit-code`) is not durable either: it lives outside
//! the repository, runs on one machine, and is silent when skipped.
//!
//! # The mechanism
//!
//! The Stage-1 content hash of each file, pinned as a constant, compared against
//! the file on disk by `cargo test`. It is exact (a hash, not a heuristic), it
//! travels with the repository, and it fails in CI.
//!
//! # If you are here because this test went red
//!
//! You changed `comparer.go` or `keys.go`. That is a **format break**, not a
//! bug fix. Doing it deliberately requires, in one change:
//!
//! 1. `comparerName` bumped to `skydb.mvcc.v2` (a store written by v1 must not
//!    open under v2 semantics — Pebble's `CheckComparer` refuses the mismatch,
//!    which is the only thing standing between an ordering change and silent
//!    corruption);
//! 2. a full store rewrite / migration path for every deployed database;
//! 3. this pin updated in the same commit, with the reason recorded.
//!
//! If what you actually wanted was to fix a bounds bug, fix it in the caller —
//! that is where N1's fix lives, and `audit_test.go`'s
//! `TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes` is the regression that
//! keeps it honest.

/// Repo-relative path + the sha256 of its content as of the Stage-1 commit.
///
/// Recorded at `bb34a667` ("feat(bluedb-v2): P1 Stage 1 — the irreversible key
/// format, alone and proven"), verified identical at HEAD when this pin was
/// written:
///
/// ```text
/// git show bb34a667:runtime-go/bluedb/comparer.go | shasum -a 256
/// git show bb34a667:runtime-go/bluedb/keys.go     | shasum -a 256
/// ```
// Read by the test below; xtask is a binary, so `pub` does not exempt it.
#[allow(dead_code)]
pub const FROZEN_AFTER_STAGE_1: &[(&str, &str)] = &[
    (
        "runtime-go/bluedb/comparer.go",
        "a7bed6f0669d61d76e1199d6214a5cd117e8bee30904209cb27507c168bc6c49",
    ),
    (
        "runtime-go/bluedb/keys.go",
        "19283c4fc856f4e585b49e17af881ce1212beed1c27dafc1c2c51e79fcafedcf",
    ),
];

/// The commit the pin was taken at, quoted into the failure so the reader can
/// diff against it without hunting for it.
#[allow(dead_code)]
pub const STAGE_1_COMMIT: &str = "bb34a667";

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bluedb_gates::sha256;

    fn repo() -> std::path::PathBuf {
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../..")
            .canonicalize()
            .expect("repo root")
    }

    /// Risk #1 of `P1-STAGE2-PLAN.md`, as a mechanism rather than a note.
    #[test]
    fn the_frozen_key_format_files_have_not_moved_since_stage_1() {
        for (rel, want) in FROZEN_AFTER_STAGE_1 {
            let bytes = std::fs::read(repo().join(rel))
                .unwrap_or_else(|e| panic!("{rel} is missing or unreadable: {e}"));
            let got = sha256::hex(&bytes);
            assert_eq!(
                &got.as_str(),
                want,
                "\n{rel} has CHANGED since Stage 1 ({STAGE_1_COMMIT}).\n\n\
                 These two files define the on-disk key format under the frozen \
                 comparerName \"skydb.mvcc.v1\", which Pebble writes into SSTable metadata. \
                 Changing them changes on-disk ordering, so it is a FORMAT BREAK requiring \
                 skydb.mvcc.v2 plus a full store rewrite of every deployed database — never a \
                 bug fix.\n\n\
                 If you were fixing a bounds bug: the fix belongs in the CALLER (reader.go), \
                 which is where N1's fix lives.\n\
                 If you meant it: bump comparerName, ship the migration, and update this pin in \
                 the same commit.\n\n\
                 Diff it with:  git diff {STAGE_1_COMMIT} -- {rel}\n"
            );
        }
    }

    /// The pin must name files that exist, and it must not be empty — an empty
    /// table would make the test above vacuously green, which is the failure
    /// mode this whole harness is built against.
    #[test]
    fn the_freeze_list_is_non_empty_and_names_real_files() {
        assert!(
            !FROZEN_AFTER_STAGE_1.is_empty(),
            "an empty freeze list asserts nothing"
        );
        for (rel, want) in FROZEN_AFTER_STAGE_1 {
            assert!(repo().join(rel).is_file(), "{rel} does not exist");
            assert_eq!(want.len(), 64, "{rel}: {want} is not a sha256 hex digest");
        }
    }
}
