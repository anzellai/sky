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
    }
    assert!(
        bad.is_empty(),
        "generated output is tracked in git:\n  {}\nAdd it to .gitignore and \
         `git rm --cached` it.",
        bad.join("\n  ")
    );
}
