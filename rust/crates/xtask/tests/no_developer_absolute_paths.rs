//! No gate may be wired to one developer's filesystem.
//!
//! `scripts/verify-live-resilience.mjs` resolved the compiler it builds its
//! fixture with as
//!
//! ```text
//! process.env.SKY_SKY_BIN || '/Users/anzel/.cargo/bin/release/sky'
//! ```
//!
//! — one machine's `CARGO_TARGET_DIR`, spelled out. On that machine the gate
//! ran. Everywhere else it fell through to `sky` on PATH, which a CI runner does
//! not have, so the two `resilience-*` checks threw inside `ensureFixtureBuilt`
//! before a single assertion executed. It read green for as long as it was only
//! ever run in the one place it worked, and died on its first nightly run.
//!
//! The failure mode is what makes this worth a gate rather than a code review
//! note: a path like that does not fail where it is written. It fails on every
//! OTHER machine, which for a release-only script means "the first time anyone
//! else looks", and by then the gate has been quietly credited for years of
//! coverage it never provided.
//!
//! `scripts/lib/cargo-target.sh` already teaches the same lesson about
//! `rust/target/release/sky`: a build step that succeeds while shipping
//! something other than what you compiled. This test covers the sibling case —
//! a step that runs the right thing on one host and nothing at all on the rest.
//!
//! Scope: executable lines of `scripts/**` and `.github/workflows/**`. Comments
//! are exempt, because the fixes above are documented BY quoting the path they
//! removed, and a gate that forbids naming the bug forbids explaining it.

use std::fs;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut d = Path::new(env!("CARGO_MANIFEST_DIR")).to_path_buf();
    while !d.join("examples").is_dir() {
        d = d.parent().expect("repo root not found").to_path_buf();
    }
    d
}

/// Files a gate can execute: shell, node, python, workflows.
fn is_scanned(p: &Path) -> bool {
    matches!(
        p.extension().and_then(|e| e.to_str()),
        Some("sh" | "bash" | "mjs" | "cjs" | "js" | "ts" | "py" | "yml" | "yaml" | "lua")
    )
}

fn walk(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(entries) = fs::read_dir(dir) else { return };
    for e in entries.flatten() {
        let p = e.path();
        if p.is_dir() {
            // `node_modules` is vendored, not ours to police.
            if p.file_name().and_then(|n| n.to_str()) == Some("node_modules") {
                continue;
            }
            walk(&p, out);
        } else if is_scanned(&p) {
            out.push(p);
        }
    }
}

/// Is this line a comment (and therefore allowed to quote a bad path)?
fn is_comment(line: &str) -> bool {
    let t = line.trim_start();
    t.starts_with('#') || t.starts_with("//") || t.starts_with("--") || t.starts_with('*')
}

/// A home directory belonging to a specific person, as opposed to a path that
/// happens to start with the same prefix. `/Users/` and `/home/` are only
/// user-specific once a NAME follows; `$HOME`, `~`, `${{ github.workspace }}`
/// and `/home/runner` are all portable and stay legal.
fn developer_home(line: &str) -> Option<String> {
    for prefix in ["/Users/", "/home/"] {
        let mut from = 0usize;
        while let Some(idx) = line[from..].find(prefix) {
            let start = from + idx;
            let rest = &line[start + prefix.len()..];
            let user: String = rest
                .chars()
                .take_while(|c| c.is_alphanumeric() || *c == '_' || *c == '-' || *c == '.')
                .collect();
            // `/home/runner` is GitHub's own runner home — every workflow's
            // `github.workspace` expands to it. It is not a developer's box.
            if !user.is_empty() && user != "runner" {
                return Some(format!("{prefix}{user}"));
            }
            from = start + prefix.len();
        }
    }
    None
}

#[test]
fn no_gate_script_hardcodes_a_developer_home_directory() {
    let root = repo_root();
    let mut files = Vec::new();
    walk(&root.join("scripts"), &mut files);
    walk(&root.join(".github").join("workflows"), &mut files);
    assert!(
        files.len() > 20,
        "found only {} scannable files — the walk is looking in the wrong place",
        files.len()
    );

    let mut offences = Vec::new();
    for f in &files {
        let Ok(text) = fs::read_to_string(f) else { continue };
        for (i, line) in text.lines().enumerate() {
            if is_comment(line) {
                continue;
            }
            if let Some(home) = developer_home(line) {
                offences.push(format!(
                    "{}:{}: hardcodes `{}`\n      {}",
                    f.strip_prefix(&root).unwrap_or(f).display(),
                    i + 1,
                    home,
                    line.trim()
                ));
            }
        }
    }

    assert!(
        offences.is_empty(),
        "{} script line(s) resolve a path inside one developer's home directory. \
         A gate wired to a single machine reports green there and does not run \
         anywhere else — which is how the nightly browser tier's two resilience \
         checks spent their life unexecuted. Resolve from the script's own \
         location (`__dirname`/`$(dirname \"$0\")`) or from an env override:\n  {}",
        offences.len(),
        offences.join("\n  ")
    );
}

#[test]
fn the_scan_can_actually_fail() {
    // Mutation witness: the predicate must reject the exact line that was in
    // `verify-live-resilience.mjs`, and must not reject the portable forms that
    // replaced it — including GitHub's own `/home/runner` workspace.
    assert_eq!(
        developer_home("        || '/Users/anzel/.cargo/bin/release/sky';").as_deref(),
        Some("/Users/anzel"),
    );
    assert_eq!(
        developer_home("const ROOT = \"/home/jdoe/works/sky\";").as_deref(),
        Some("/home/jdoe"),
    );
    assert_eq!(developer_home("  GOCACHE: /home/runner/work/sky/sky/.gocache"), None);
    assert_eq!(developer_home("path.join(repoRoot, 'sky-out', 'sky')"), None);
    assert_eq!(developer_home("SKY=\"$ROOT/sky-out/sky\""), None);
    assert_eq!(developer_home("cd \"$HOME/.cargo\""), None);
    // A comment quoting the removed path stays legal — the fixes document
    // themselves that way.
    assert!(is_comment("// used to be '/Users/anzel/.cargo/bin/release/sky'"));
    assert!(is_comment("# /Users/anzel/.cargo/bin"));
}
