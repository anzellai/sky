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
            stale.push(format!(
                "  {file}: says `{found}`, current line is `{expected}`"
            ));
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

/// Sample output that shows the CURRENT version, written in the live docs as if
/// a reader had just run the command. The v0.27.0 docs sweep found
/// `"skyVersion":"v0.25.21"` in the `/_sky/build` sample and `from sky v0.11.1`
/// in the `sky upgrade-claude` sample: nothing tied them to a release, so they
/// read as the version a user gets today. Every occurrence of each phrase in a
/// live doc must name the current minor line.
const SAMPLE_OUTPUT_PHRASES: &[&str] = &[
    "\"skyVersion\":\"v",
    "from sky v",
    "the compiler's release version (`v",
];

/// Live docs: `docs/**` without the frozen `docs/history/` tree, plus the
/// top-level and template guides.
fn live_docs() -> Vec<PathBuf> {
    fn walk(dir: &std::path::Path, out: &mut Vec<PathBuf>) {
        let Ok(rd) = std::fs::read_dir(dir) else {
            return;
        };
        for e in rd.flatten() {
            let p = e.path();
            if p.is_dir() {
                if p.file_name().and_then(|s| s.to_str()) != Some("history") {
                    walk(&p, out);
                }
            } else if p.extension().and_then(|s| s.to_str()) == Some("md") {
                out.push(p);
            }
        }
    }
    let mut out = Vec::new();
    walk(&repo().join("docs"), &mut out);
    walk(&repo().join("templates"), &mut out);
    out.push(repo().join("README.md"));
    out.push(repo().join("AGENTS.md"));
    out
}

#[test]
fn sample_output_in_live_docs_shows_the_current_version() {
    let (maj, min) = current_minor_line();
    let mut stale = Vec::new();
    let mut seen = vec![0usize; SAMPLE_OUTPUT_PHRASES.len()];
    for path in live_docs() {
        let Ok(text) = std::fs::read_to_string(&path) else {
            continue;
        };
        for (i, phrase) in SAMPLE_OUTPUT_PHRASES.iter().enumerate() {
            for (idx, _) in text.match_indices(phrase) {
                seen[i] += 1;
                let ver: String = text[idx + phrase.len()..]
                    .chars()
                    .take_while(|c| c.is_ascii_digit() || *c == '.')
                    .collect();
                let mut it = ver.split('.');
                let ok = matches!(
                    (it.next().and_then(|s| s.parse::<u32>().ok()),
                     it.next().and_then(|s| s.parse::<u32>().ok())),
                    (Some(a), Some(b)) if a == maj && b == min
                );
                if !ok {
                    let line = text[..idx].lines().count().max(1);
                    stale.push(format!(
                        "  {}:{line}: `{phrase}{ver}`, current line is v{maj}.{min}.x",
                        path.strip_prefix(repo()).unwrap_or(&path).display()
                    ));
                }
            }
        }
    }
    for (i, phrase) in SAMPLE_OUTPUT_PHRASES.iter().enumerate() {
        assert!(
            seen[i] > 0,
            "no live doc contains `{phrase}` any more. Delete its row from \
             SAMPLE_OUTPUT_PHRASES, or update it if the sample was reworded."
        );
    }
    assert!(
        stale.is_empty(),
        "live-doc sample output shows an old version as the current one:\n{}\n\n\
         Update the sample to the current release (CHANGELOG.md's newest heading).",
        stale.join("\n")
    );
}

/// The full `X.Y.Z` of the newest `## vX.Y.Z` heading in CHANGELOG.md: the
/// first line matching `^## v([0-9]+\.[0-9]+\.[0-9]+)( |$)`.
fn newest_changelog_version() -> String {
    let ch = read("CHANGELOG.md");
    for line in ch.lines() {
        let Some(rest) = line.strip_prefix("## v") else {
            continue;
        };
        let ver: String = rest
            .chars()
            .take_while(|c| c.is_ascii_digit() || *c == '.')
            .collect();
        let after = &rest[ver.len()..];
        let parts: Vec<&str> = ver.split('.').collect();
        if parts.len() == 3
            && parts.iter().all(|p| !p.is_empty())
            && (after.is_empty() || after.starts_with(' '))
        {
            return ver;
        }
    }
    panic!("no `## vX.Y.Z` heading in CHANGELOG.md — the parse is wrong, not the repo");
}

/// `version` under `[workspace.package]` in rust/Cargo.toml.
fn workspace_version() -> String {
    let toml = read("rust/Cargo.toml");
    let mut in_section = false;
    for line in toml.lines() {
        let t = line.trim();
        if t.starts_with('[') {
            in_section = t == "[workspace.package]";
            continue;
        }
        if in_section {
            if let Some(v) = t
                .strip_prefix("version")
                .map(str::trim_start)
                .and_then(|r| r.strip_prefix('='))
            {
                return v.trim().trim_matches('"').to_string();
            }
        }
    }
    panic!("rust/Cargo.toml has no `[workspace.package] version`, the one version source");
}

/// The version has ONE source, `[workspace.package] version` in rust/Cargo.toml
/// (`sky --version`, every crate, default.nix and flake.nix take it from
/// there), and the release notes name the version they describe. A release
/// bumps both in the same commit; this fails on any commit where they differ,
/// so a binary can never report a version whose notes are missing, or the
/// reverse. release.yml also refuses a tag that differs from either.
#[test]
fn the_workspace_version_is_the_newest_changelog_heading() {
    let cargo = workspace_version();
    let changelog = newest_changelog_version();
    assert_eq!(
        cargo, changelog,
        "rust/Cargo.toml `[workspace.package] version` is `{cargo}`, but CHANGELOG.md's \
         newest heading is `## v{changelog}`. A release bumps the version and writes \
         the heading in the same commit."
    );
    // Every crate takes the workspace version: none states its own.
    let crates = repo().join("rust/crates");
    let mut own = Vec::new();
    for e in std::fs::read_dir(&crates)
        .expect("read rust/crates")
        .flatten()
    {
        let manifest = e.path().join("Cargo.toml");
        let Ok(text) = std::fs::read_to_string(&manifest) else {
            continue;
        };
        let pkg = text.split("\n[").next().unwrap_or("");
        if !pkg.lines().any(|l| l.trim() == "version.workspace = true") {
            own.push(manifest.display().to_string());
        }
    }
    assert!(
        own.is_empty(),
        "crate(s) without `version.workspace = true` in [package]: {own:?}"
    );
}

/// No Nix file states a version of its own: default.nix reads the workspace
/// version with `lib.importTOML`, and flake.nix takes it from default.nix. A
/// literal here is how the package said `0.18.0` through v0.27 (issue #216).
#[test]
fn no_nix_file_states_a_literal_version() {
    let mut found = Vec::new();
    for file in ["default.nix", "flake.nix"] {
        let text = read(file);
        for (n, line) in text.lines().enumerate() {
            let t = line.trim();
            if t.starts_with('#') {
                continue;
            }
            if let Some(rest) = t.strip_prefix("version") {
                let rest = rest.trim_start();
                if let Some(v) = rest.strip_prefix('=') {
                    let v = v.trim().trim_start_matches('"');
                    if v.starts_with(|c: char| c.is_ascii_digit()) {
                        found.push(format!("{file}:{}: {t}", n + 1));
                    }
                }
            }
        }
        assert!(
            text.contains("workspace.package.version") || file == "flake.nix",
            "{file} no longer reads `workspace.package.version` from rust/Cargo.toml"
        );
    }
    assert!(
        found.is_empty(),
        "a Nix file states a literal version; take it from rust/Cargo.toml:\n  {}",
        found.join("\n  ")
    );
}
