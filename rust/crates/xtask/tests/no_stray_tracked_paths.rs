//! Repo hygiene: what is COMMITTED, not what is on disk.
//!
//! Two things reached the published repository that should never have:
//!
//! 1. A tracked **symlink** named `node_modules` pointing at
//!    `/Users/anzel/works/playground/sky/node_modules` — one developer's
//!    absolute path. `.gitignore` had `node_modules/`, and a trailing slash
//!    matches a DIRECTORY, so a symlink of the same name walked straight past
//!    it. On any other clone that link dangles.
//!
//! 2. `sky-out-test/`, a root directory named like build output, carrying a
//!    near-duplicate of a file that lives properly in `test-files/`. Present
//!    since v0.8.1 and referenced by nothing.
//!
//! `no_developer_absolute_paths.rs` already forbids the same class INSIDE
//! script bodies. It could not see either of these, because neither is a
//! script: one is a symlink target, the other a path. This test asks git what
//! is tracked, which is the only question that decides what ships.

use std::process::Command;

fn tracked(args: &[&str]) -> String {
    let root = concat!(env!("CARGO_MANIFEST_DIR"), "/../../..");
    let out = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(args)
        .output()
        .expect("git");
    String::from_utf8_lossy(&out.stdout).into_owned()
}

/// A tracked symlink pointing outside the repo is broken for everyone else.
///
/// Relative, in-repo symlinks are legitimate and stay allowed; the failure mode
/// is an ABSOLUTE target, which can only be right on the machine that made it.
#[test]
fn no_tracked_symlink_points_at_an_absolute_path() {
    let mut bad = Vec::new();
    for line in tracked(&["ls-files", "-s"]).lines() {
        // `<mode> <sha> <stage>\t<path>`; 120000 is a symlink. The INDEX, not
        // HEAD: this must fail on what you are about to commit, not only on
        // what already shipped.
        let Some((meta, path)) = line.split_once('\t') else {
            continue;
        };
        if !meta.starts_with("120000") {
            continue;
        }
        let target = tracked(&["cat-file", "-p", meta.split_whitespace().nth(1).unwrap_or("")]);
        let target = target.trim();
        if target.starts_with('/') || target.contains(":\\") {
            bad.push(format!("{path} -> {target}"));
        }
    }
    assert!(
        bad.is_empty(),
        "tracked symlink(s) point at an absolute path — these dangle on every \
         other clone:\n  {}\nIf a worktree needs `node_modules`, create the link \
         locally; do not commit it.",
        bad.join("\n  ")
    );
}

/// Build-output directories must not be tracked.
///
/// The names below are what this project's tooling generates. A tracked copy is
/// either a stale scratch dir or a real artefact someone committed by accident,
/// and both mislead a reader about what is source.
#[test]
fn no_tracked_build_output_directories() {
    const GENERATED: &[&str] = &[
        "node_modules",
        "sky-out",
        "sky-out-test",
        ".skycache",
        ".skydeps",
        "target",
        "_site",
        "dist",
    ];
    let listing = tracked(&["ls-files"]);
    let mut bad = Vec::new();
    for path in listing.lines() {
        let first = path.split('/').next().unwrap_or("");
        if GENERATED.contains(&first) {
            bad.push(path.to_string());
        }
        // A generated dir nested anywhere, not only at the root.
        if path
            .split('/')
            .any(|seg| GENERATED.contains(&seg) && seg != "dist")
        {
            if !bad.iter().any(|b| b == path) {
                bad.push(path.to_string());
            }
        }
        if is_cargo_artifact(path) && !bad.iter().any(|b| b == path) {
            bad.push(path.to_string());
        }
    }
    assert!(
        bad.is_empty(),
        "generated output is tracked in git:\n  {}\nAdd it to .gitignore and \
         `git rm --cached` it.",
        bad.join("\n  ")
    );
}

/// Is this path inside a cargo target directory under ANY name?
///
/// The exact-name list above matches the segment `target` and missed
/// `isolated-target`, which is how 1928 build files reached `main` in
/// `2aac9db5`: a `git add -A` in a worktree whose cargo target dir sat at the
/// repo root under a non-standard name. `CARGO_TARGET_DIR` accepts any path, so
/// "the directory is called target" was never a safe assumption — and agents
/// and parallel worktrees routinely use a distinct name precisely to avoid
/// clobbering each other.
///
/// Two independent signals, either of which is conclusive:
///   * a path segment that IS `target` or ends in `-target`; or
///   * a `.fingerprint/` segment, which only cargo writes.
///
/// The second is the safety net for a target dir named something else entirely
/// (`build-out/`, `cargo-tmp/`): the name can be anything, but the contents
/// still look exactly like cargo's.
fn is_cargo_artifact(path: &str) -> bool {
    path.split('/').any(|seg| {
        seg == "target" || seg.ends_with("-target") || seg == ".fingerprint"
    })
}

/// The classifier is asserted directly, so this file proves it can fail without
/// needing a polluted index to test against — the index is (now) clean, and a
/// scan over a clean index passes whether or not the rule works.
#[test]
fn the_cargo_artifact_classifier_catches_what_actually_leaked() {
    // The real leaked paths, verbatim from `git ls-tree` at 2aac9db5.
    for leaked in [
        "isolated-target/debug/xtask",
        "isolated-target/debug/.fingerprint/base-08c4f1cdbd8f2468/lib-base",
        "isolated-target/release/build/ffi-1368cf38e80fb13a/out/embedded-assets/sky-stdlib/Std/Ui.sky",
        "target/debug/deps/libsyntax.rlib",
        "some/nested/build-target/debug/foo",
        "weirdly-named-dir/debug/.fingerprint/x/lib-y",
    ] {
        assert!(
            is_cargo_artifact(leaked),
            "`{leaked}` is cargo build output and must be flagged — this is the \
             exact shape that reached main"
        );
    }

    // Real source paths that merely mention the word must NOT be flagged, or
    // the rule fires on the repo's own code and gets weakened back out again.
    for ok in [
        "rust/crates/xtask/src/harness/registry.rs",
        "scripts/lib/cargo-target.sh",
        "docs/rust-rewrite/13-change-verification-and-edge-cases.md",
        "rust/crates/codegen/src/target_shape.rs",
        "sky-stdlib/Std/Ui.sky",
    ] {
        assert!(
            !is_cargo_artifact(ok),
            "`{ok}` is a real tracked source path and must NOT be flagged — an \
             over-eager rule here is how this check gets deleted later"
        );
    }
}
