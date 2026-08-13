//! The docs that state a CURRENT version must state the current one.
//!
//! `README.md` said "Status: **v0.19.x** release candidate" while the tree was
//! shipping v0.20.1, and `AGENTS.md` said "Current line: **v0.19.x**". Both were
//! written once and then never touched again by a release, because nothing
//! connected them to the release. A reader's first impression of the project —
//! and an AI agent's first fact about it, since `AGENTS.md` is the file every
//! tool reads — was a version line stale by a whole minor.
//!
//! This is the doc-rot equivalent of the gate-vacuity class this cycle is about:
//! a claim that nothing checks decays silently, and the decay is invisible
//! precisely because the claim still LOOKS authoritative.
//!
//! `CHANGELOG.md`'s newest `## vX.Y.Z` heading is the single source of truth —
//! it is already the release-notes source (`scripts/release-notes.sh` reads it,
//! and `release.yml` hard-gates on it), so tying these files to it means the act
//! of writing release notes is what keeps them true.
//!
//! Scope is deliberately narrow: only files that assert what the CURRENT version
//! IS. A historical statement ("v0.17 closed Limitation #8", "shipped in
//! v0.16.6", the licence note about v0.10.1 onwards) is a fact about the past
//! and must NOT be rewritten by a release — those are excluded by matching a
//! specific phrase rather than any version-looking string.

use std::path::PathBuf;

fn repo() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

fn read(rel: &str) -> String {
    let p = repo().join(rel);
    std::fs::read_to_string(&p).unwrap_or_else(|e| panic!("read {}: {e}", p.display()))
}

/// `(major, minor)` of the newest released version in the changelog.
///
/// The newest heading wins, so this tracks the release being prepared as soon as
/// its section is written — which is the same moment `release-notes.sh` starts
/// succeeding for that tag.
fn current_minor_line() -> (u32, u32) {
    let ch = read("CHANGELOG.md");
    for line in ch.lines() {
        let Some(rest) = line.strip_prefix("## v") else {
            continue;
        };
        let ver: String = rest
            .chars()
            .take_while(|c| c.is_ascii_digit() || *c == '.')
            .collect();
        let mut it = ver.split('.');
        let (Some(maj), Some(min)) = (it.next(), it.next()) else {
            continue;
        };
        if let (Ok(maj), Ok(min)) = (maj.parse(), min.parse()) {
            return (maj, min);
        }
    }
    panic!("no `## vX.Y.Z` heading in CHANGELOG.md — the parse is wrong, not the repo");
}

/// Every file that asserts the current line, and the phrase that does it.
/// Adding a doc that states a current version means adding it here.
const CLAIM_SITES: &[(&str, &str)] = &[
    ("README.md", "**Status: v"),
    ("AGENTS.md", "Current line: **v"),
];

#[test]
fn docs_that_state_a_current_version_state_the_current_one() {
    let (maj, min) = current_minor_line();
    let expected = format!("v{maj}.{min}.x");
    let mut stale = Vec::new();

    for (file, phrase) in CLAIM_SITES {
        let text = read(file);
        let Some(idx) = text.find(phrase) else {
            panic!(
                "{file} no longer contains `{phrase}`. Either the claim was \
                 removed (then delete its row from CLAIM_SITES) or it was \
                 reworded (then update the row) — silently losing the check is \
                 the failure this gate exists to prevent."
            );
        };
        // The version token immediately after the phrase.
        let tail = &text[idx + phrase.len() - 1..];
        let found: String = tail
            .chars()
            .skip(1) // the `v`
            .take_while(|c| c.is_ascii_digit() || *c == '.' || *c == 'x')
            .collect();
        let found = format!("v{found}");
        if !(found == expected || found == format!("v{maj}.{min}")) {
            stale.push(format!("  {file}: says `{found}`, current line is `{expected}`"));
        }
    }

    assert!(
        stale.is_empty(),
        "doc(s) state a stale current version:\n{}\n\n\
         CHANGELOG.md's newest heading is the source of truth. Update the \
         file(s) above when writing release notes — that is the moment these \
         claims become wrong, and the only moment someone is looking.\n\n\
         Historical mentions (\"v0.17 closed …\", \"shipped in v0.16.6\", the \
         licence note) are NOT covered here and must not be rewritten: they are \
         facts about the past.",
        stale.join("\n")
    );
}

/// The gate must not pass by matching nothing. If a claim site stops containing
/// a version at all, the parse above yields an empty string and the comparison
/// would fail — but only if a version was expected in the first place, so pin
/// that both sites really do carry one today.
#[test]
fn every_claim_site_actually_carries_a_version() {
    for (file, phrase) in CLAIM_SITES {
        let text = read(file);
        let idx = text
            .find(phrase)
            .unwrap_or_else(|| panic!("{file} lost its `{phrase}` claim"));
        let tail = &text[idx + phrase.len()..];
        let digits: String = tail.chars().take_while(|c| c.is_ascii_digit()).collect();
        assert!(
            !digits.is_empty(),
            "{file}'s `{phrase}` claim is not followed by a version number, so \
             the staleness check above compares nothing"
        );
    }
    assert!(
        CLAIM_SITES.len() >= 2,
        "CLAIM_SITES has shrunk below the two known files — a check that \
         inspects fewer sites passes more easily, which is how it dies"
    );
}
