//! `xtask fmt` — the `sky fmt` comment-loss safety gate (v1 blocker close).
//!
//! `sky fmt` re-lays-out code through an opinionated pretty-printer, then a
//! per-file safety net (`fmt::format_source` → `is_safe`) accepts that output
//! ONLY when it preserves every comment verbatim, preserves the significant
//! token multiset, re-parses cleanly, and is idempotent — otherwise the whole
//! file falls back to the lossless CST reprint. That net is the guarantee that
//! `sky fmt` never silently drops a comment.
//!
//! This gate LOCKS that guarantee across the whole corpus. For every `.sky`
//! file under `sky-stdlib/` + `examples/` (both comment-heavy) PLUS a dedicated
//! comment-torture fixture, it formats the file and asserts:
//!
//!   1. the comment multiset is byte-identical before and after (no comment
//!      dropped, added, or mutated — own-line AND trailing/inline), and
//!   2. formatting is idempotent (`format(format(x)) == format(x)`).
//!
//! The torture fixture (`crates/xtask/fmt-fixtures/comment-torture.sky`)
//! deliberately includes the shapes the opinionated printer would drop on its
//! own (notably trailing/inline comments) — so if a future change removes or
//! breaks the safety net, the opinionated output loses those comments and THIS
//! gate fails, instead of the loss reaching users. It is the CI teeth behind
//! the fmt crate's per-file unit tests.

use base::FileId;
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use syntax::{parse, SyntaxKind};

pub fn run(_args: &[String], repo_root: &Path) -> i32 {
    let mut files = Vec::new();
    for root in ["sky-stdlib", "examples"] {
        collect_sky(&repo_root.join(root), &mut files);
    }
    // The comment-torture fixture — sharp teeth for the trailing-comment class
    // that the opinionated printer would otherwise drop.
    collect_sky(
        &repo_root.join("rust/crates/xtask/fmt-fixtures"),
        &mut files,
    );
    files.sort();
    files.dedup();

    if files.is_empty() {
        eprintln!("FMT GATE: no .sky files found");
        return 1;
    }

    let mut dropped: Vec<CommentLoss> = Vec::new();
    let mut non_idempotent: Vec<String> = Vec::new();

    for f in &files {
        let Ok(src) = std::fs::read_to_string(f) else {
            continue;
        };
        let out = fmt::format_source(&src);

        // (1) comment preservation — the load-bearing property.
        let before = comment_multiset(&src);
        let after = comment_multiset(&out);
        if before != after {
            dropped.push(CommentLoss {
                file: rel(repo_root, f),
                diff: comment_diff(&before, &after),
            });
        }

        // (2) idempotency — a second pass must be a byte no-op.
        if fmt::format_source(&out) != out {
            non_idempotent.push(rel(repo_root, f));
        }
    }

    if dropped.is_empty() && non_idempotent.is_empty() {
        println!(
            "FMT GATE: PASS  (comment multiset + idempotency preserved across {} .sky file(s))",
            files.len()
        );
        return 0;
    }

    println!("FMT GATE: FAIL");
    if !dropped.is_empty() {
        println!(
            "\n  {} file(s) where `sky fmt` changed the comment multiset \
             (the safety net must fall back to lossless — a comment was dropped/added/mutated):",
            dropped.len()
        );
        for d in &dropped {
            println!("    {}", d.file);
            for line in &d.diff {
                println!("      {line}");
            }
        }
    }
    if !non_idempotent.is_empty() {
        println!(
            "\n  {} file(s) where `sky fmt` is NOT idempotent (format(format(x)) != format(x)):",
            non_idempotent.len()
        );
        for f in &non_idempotent {
            println!("    {f}");
        }
    }
    println!(
        "\n`sky fmt` MUST NOT lose a comment. The per-file safety net in \
         crates/fmt/src/lib.rs (is_safe) guarantees it by falling back to the \
         lossless reprint; a failure here means that net regressed."
    );
    1
}

/// The roots whose tracked `.sky` files must be `sky fmt`-clean.
///
/// `tests/` and `corpus/` are out of scope on purpose: test fixtures and corpus
/// cases pin exact source shapes (a mis-indented comment, a hand-broken layout)
/// that a reformat would erase.
pub const FMT_CLEAN_ROOTS: &[&str] =
    &["sky-stdlib", "sky-bundled", "apps", "examples", "templates"];

/// The `fmt-clean` gate: every TRACKED `.sky` file under [`FMT_CLEAN_ROOTS`] is
/// already `sky fmt`-clean (`fmt::is_formatted`, the check `sky fmt --check`
/// and `sky verify` run). One assertion per file.
///
/// Membership is `git ls-files`, not a directory walk, so a local `sky-out/`,
/// `.split/` or `.skydeps/` tree never changes the count.
///
/// Before this gate, nothing checked it: `sky verify` covers only the example
/// projects with a `tests/` suite, and the `fmt` gate checks that formatting
/// keeps comments and is idempotent, not that a file is already formatted. So
/// a printer fix that took a file off the lossless fallback (or a hand edit)
/// could leave a tracked file that `sky fmt` rewrites, and nothing went red.
pub fn check_clean(repo_root: &Path) -> (bool, u64, String) {
    let mut args = vec!["ls-files".to_string(), "-z".to_string(), "--".to_string()];
    for r in FMT_CLEAN_ROOTS {
        args.push(format!("{r}/*.sky"));
    }
    let out = match std::process::Command::new("git")
        .args(&args)
        .current_dir(repo_root)
        .output()
    {
        Ok(o) if o.status.success() => o,
        Ok(o) => {
            return (
                false,
                0,
                format!(
                    "git ls-files failed: {}",
                    String::from_utf8_lossy(&o.stderr).trim()
                ),
            )
        }
        Err(e) => return (false, 0, format!("git ls-files could not run: {e}")),
    };
    let listing = String::from_utf8_lossy(&out.stdout).to_string();
    let mut files: Vec<&str> = listing
        .split('\0')
        .filter(|f| !f.is_empty())
        // USER RULE (anzellai/sky#299): examples/76-ai-shell-command is excluded
        // from CI in general; skip every `crate::ci_excluded` example by name.
        .filter(|f| !crate::ci_excluded::is_ci_excluded_rel(f))
        .collect();
    files.sort_unstable();
    let mut assertions = 0u64;
    let mut unclean = Vec::new();
    for f in &files {
        assertions += 1;
        match std::fs::read_to_string(repo_root.join(f)) {
            Ok(src) if fmt::is_formatted(&src) => {}
            Ok(_) => unclean.push(f.to_string()),
            Err(e) => unclean.push(format!("{f} (unreadable: {e})")),
        }
    }
    if unclean.is_empty() {
        (
            true,
            assertions,
            format!("{assertions} tracked .sky file(s) are `sky fmt`-clean"),
        )
    } else {
        (
            false,
            assertions,
            format!(
                "{} of {assertions} tracked .sky file(s) need `sky fmt`:\n  {}",
                unclean.len(),
                unclean.join("\n  ")
            ),
        )
    }
}

struct CommentLoss {
    file: String,
    diff: Vec<String>,
}

/// Multiset of comment token TEXT (own-line `--`/`{- -}` AND trailing/inline),
/// keyed by exact text so a mutated comment counts as one dropped + one added.
/// Mirrors `fmt::comment_multiset` (private) so the gate needs no fmt internals.
fn comment_multiset(src: &str) -> BTreeMap<String, usize> {
    let parsed = parse(src, FileId(0));
    let mut m = BTreeMap::new();
    for e in parsed.syntax().descendants_with_tokens() {
        if let Some(t) = e.into_token() {
            if matches!(t.kind(), SyntaxKind::LineComment | SyntaxKind::BlockComment) {
                *m.entry(t.text().to_string()).or_insert(0) += 1;
            }
        }
    }
    m
}

/// Human-readable +/- lines for the comments that differ between two multisets.
fn comment_diff(before: &BTreeMap<String, usize>, after: &BTreeMap<String, usize>) -> Vec<String> {
    let mut out = Vec::new();
    for (text, &n) in before {
        let a = after.get(text).copied().unwrap_or(0);
        if a < n {
            out.push(format!("- {}× LOST: {}", n - a, one_line(text)));
        }
    }
    for (text, &n) in after {
        let b = before.get(text).copied().unwrap_or(0);
        if b < n {
            out.push(format!("+ {}× ADDED: {}", n - b, one_line(text)));
        }
    }
    out
}

fn one_line(s: &str) -> String {
    let t = s.replace('\n', "\\n");
    if t.len() > 80 {
        format!("{}…", &t[..80])
    } else {
        t
    }
}

fn rel(root: &Path, path: &Path) -> String {
    path.strip_prefix(root)
        .unwrap_or(path)
        .display()
        .to_string()
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for path in entries {
        if path.is_dir() {
            let skip = matches!(
                path.file_name().and_then(|s| s.to_str()),
                Some("sky-out") | Some(".skycache") | Some(".skydeps")
            );
            // USER RULE (anzellai/sky#299): examples/76-ai-shell-command is excluded
            // from CI in general; skip every `crate::ci_excluded` example by name.
            if !skip && !crate::ci_excluded::is_ci_excluded_path(&path) {
                collect_sky(&path, out);
            }
        } else if path.extension().and_then(|s| s.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // The gate's teeth: comment_multiset + comment_diff MUST detect a dropped
    // comment. If this ever passes silently, the corpus gate is toothless.
    #[test]
    fn detects_dropped_comment() {
        let with = "module M exposing (x)\n\n-- keep me\nx = 1  -- and me\n";
        let without = "module M exposing (x)\n\nx = 1\n"; // both comments gone
        let a = comment_multiset(with);
        let b = comment_multiset(without);
        assert_ne!(a, b, "dropping comments must change the multiset");
        let diff = comment_diff(&a, &b);
        assert_eq!(
            diff.len(),
            2,
            "both dropped comments must be reported: {diff:?}"
        );
        assert!(
            diff.iter().all(|l| l.starts_with("- ")),
            "losses render as `-`: {diff:?}"
        );
    }

    #[test]
    fn detects_mutated_comment() {
        let orig = "-- alpha\nx = 1\n";
        let mutated = "-- ALPHA\nx = 1\n"; // same count, different text
        let a = comment_multiset(orig);
        let b = comment_multiset(mutated);
        assert_ne!(a, b, "a mutated comment must change the multiset");
    }

    #[test]
    fn identical_comments_match() {
        let src = "-- a\n-- b\nx = 1  -- c\n";
        assert_eq!(comment_multiset(src), comment_multiset(src));
        assert!(comment_diff(&comment_multiset(src), &comment_multiset(src)).is_empty());
    }
}
