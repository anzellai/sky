//! The Std.Ai.Train docs state the trainers as they ship: Mlx on the native
//! `sky-trainer`, Fake in the runtime, Python for Unsloth only; and the v0.28
//! migration guide is measured from the newest release before the v0.28 line.
//!
//! # Why this exists
//!
//! The training guide opened with "The trainer is Python" after the native
//! trainer replaced the Python driver for Mlx and the Fake trainer moved into
//! the runtime, and the testing guide still sent a tester to `sky ai provision
//! --train fake` for a managed CPython that is no longer installed. The
//! migration guide said it covered changes since v0.27.8 while v0.27.17 was
//! the newest release. Each sentence is the first thing a user reads, and each
//! was wrong without anything noticing.

use std::path::PathBuf;

fn read(rel: &str) -> String {
    let root = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));
    std::fs::read_to_string(root.join(rel)).unwrap_or_else(|e| panic!("read {rel}: {e}"))
}

/// The newest released version before the `line` (`"v0.28"`): the first
/// `## vX.Y.Z` heading of CHANGELOG.md that is not marked unreleased and is
/// not on that line. The v0.28 migration guide is measured from it, so it stays
/// right both while v0.28.0 is unreleased and once its heading is dated.
/// `(major, minor)` of `v0.28` or `v0.28.1`.
fn minor_line(v: &str) -> Option<(u32, u32)> {
    let mut parts = v.trim_start_matches('v').split('.');
    Some((parts.next()?.parse().ok()?, parts.next()?.parse().ok()?))
}

/// The newest released version on a line older than `line`. A newer line
/// above it (a later minor release) does not count: it is not "before".
fn newest_release_before(changelog: &str, line: &str) -> String {
    let want = minor_line(line).expect("a line such as v0.28");
    changelog
        .lines()
        .filter(|l| l.starts_with("## v") && !l.contains("(unreleased)"))
        .filter_map(|l| l.trim_start_matches("## ").split_whitespace().next())
        .find(|v| minor_line(v).is_some_and(|m| m < want))
        .map(str::to_string)
        .expect("a released version in CHANGELOG.md")
}

#[test]
fn the_training_docs_state_the_trainers_as_they_ship() {
    let train = read("docs/ai/train.md");
    let testing = read("docs/ai/testing-guide.md");
    let changelog = read("CHANGELOG.md");
    let migration = read("docs/migration/v0.28.md");
    let mut p = Vec::new();
    for (doc, text) in [
        ("docs/ai/train.md", &train),
        ("docs/ai/testing-guide.md", &testing),
    ] {
        for stale in [
            "The trainer is Python",
            "sky ai provision --train fake`",
            "MLX-LM",
            "managed CPython",
            "mlx-darwin-arm64",
        ] {
            if text.contains(stale) {
                p.push(format!("{doc} still says `{stale}`"));
            }
        }
    }
    for need in [
        "sky-trainer",
        "--sky-ai-fake-trainer",
        "SKY_AI_TRAINER_BIN",
        "ai-trainer-bundles.yml",
    ] {
        if !train.contains(need) {
            p.push(format!("docs/ai/train.md does not name `{need}`"));
        }
    }
    // The v0.28.0 section wherever it sits: a later patch heading comes first.
    let v028 = changelog
        .split("\n## v")
        .skip(1)
        .find(|s| s.starts_with("0.28.0 "))
        .unwrap_or("");
    if !v028.contains("sky-trainer") || !v028.contains("--sky-ai-fake-trainer") {
        p.push("CHANGELOG.md v0.28.0 does not describe the native trainer and the runtime's Fake trainer".into());
    }
    if v028.contains("MLX-LM as a library") {
        p.push("CHANGELOG.md v0.28.0 still says Mlx trains with MLX-LM".into());
    }
    let release = newest_release_before(&changelog, "v0.28");
    let baseline = format!("worked on {release}.");
    if !migration.contains(&baseline) {
        p.push(format!(
            "docs/migration/v0.28.md is not measured from the newest release: want `{baseline}`"
        ));
    }
    for need in ["SKY_AI_TRAINER_BIN", "toolchains/mlx", "--train mlx"] {
        if !migration.contains(need) {
            p.push(format!("docs/migration/v0.28.md does not name `{need}`"));
        }
    }
    assert!(p.is_empty(), "{}", p.join("\n"));
}

#[test]
fn the_newest_release_skips_a_later_line() {
    let c = "# Changelog\n\n## v0.29.0 — w (2026-10-29)\n\n## v0.28.1 — x (2026-10-28)\n\n## v0.28.0 — y (2026-10-28)\n\n## v0.27.20 — z (2026-10-27)\n";
    assert_eq!(newest_release_before(c, "v0.28"), "v0.27.20");
}

#[test]
fn the_newest_release_skips_an_unreleased_heading() {
    let c = "# Changelog\n\n## v0.28.0 — x (unreleased)\n\n## v0.27.17 — y (2026-10-26)\n\n## v0.27.16 — z\n";
    assert_eq!(newest_release_before(c, "v0.28"), "v0.27.17");
}

#[test]
fn the_baseline_skips_the_line_once_it_is_released() {
    let c = "# Changelog\n\n## v0.28.0 — x (2026-10-28)\n\n## v0.27.20 - y (2026-10-27)\n\n## v0.27.19 - z\n";
    assert_eq!(newest_release_before(c, "v0.28"), "v0.27.20");
}
