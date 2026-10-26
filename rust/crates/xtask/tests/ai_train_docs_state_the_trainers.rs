//! The Std.Ai.Train docs state the trainers as they ship: Mlx on the native
//! `sky-trainer`, Fake in the runtime, Python for Unsloth only; and the v0.28
//! migration guide is measured from the newest released version.
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

/// The newest released version: the first `## vX.Y.Z` heading of
/// CHANGELOG.md that is not marked unreleased.
fn newest_release(changelog: &str) -> String {
    changelog
        .lines()
        .filter(|l| l.starts_with("## v") && !l.contains("(unreleased)"))
        .find_map(|l| l.trim_start_matches("## ").split_whitespace().next())
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
    let v028 = changelog
        .split("\n## v")
        .nth(1)
        .filter(|s| s.starts_with("0.28.0"))
        .unwrap_or("");
    if !v028.contains("sky-trainer") || !v028.contains("--sky-ai-fake-trainer") {
        p.push("CHANGELOG.md v0.28.0 does not describe the native trainer and the runtime's Fake trainer".into());
    }
    if v028.contains("MLX-LM as a library") {
        p.push("CHANGELOG.md v0.28.0 still says Mlx trains with MLX-LM".into());
    }
    let release = newest_release(&changelog);
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
fn the_newest_release_skips_an_unreleased_heading() {
    let c = "# Changelog\n\n## v0.28.0 — x (unreleased)\n\n## v0.27.17 — y (2026-10-26)\n\n## v0.27.16 — z\n";
    assert_eq!(newest_release(c), "v0.27.17");
}
