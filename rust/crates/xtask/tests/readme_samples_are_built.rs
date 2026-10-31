//! Every Sky sample in `README.md` is a copy of code that CI builds.
//!
//! CLAUDE.md §0.6 rule 1: a README sample must be a copy of a file that CI
//! builds (an `examples/` member, which the example sweep builds, or a full
//! `module Main` program in a live doc, which `scripts/doc-examples.sh`
//! type-checks and `go build`s), so that a README sample cannot rot.
//!
//! This test enforces the "copy" half. Each ```elm fence in `README.md` must
//! appear verbatim (whole, or as a contiguous excerpt) in one of:
//!
//! * a full-module ```elm fence of `docs/showcase.md` that `doc-examples.sh`
//!   checks (it starts with `module Main` and carries no `doc-example: skip`);
//! * a `.sky` source file under `examples/*/src/`.
//!
//! Before v0.29.0 the README carried hand-written samples that nothing
//! compiled, and they drifted from the stdlib (a stale banner, a contradicting
//! Go version, an API that had moved).

use std::path::{Path, PathBuf};

// The xtask crate is a binary, so the exclusion list is included by path: the
// same file the gates read, never a copy of it.
#[path = "../src/ci_excluded.rs"]
#[allow(dead_code)]
mod ci_excluded;

fn repo() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

fn read(rel: &str) -> String {
    let p = repo().join(rel);
    std::fs::read_to_string(&p).unwrap_or_else(|e| panic!("read {}: {e}", p.display()))
}

/// The bodies of every ```elm / ```sky fence in `text`, without the fences.
fn elm_fences(text: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut cur: Option<String> = None;
    for line in text.lines() {
        match cur.as_mut() {
            None => {
                let t = line.trim_end();
                if t.starts_with("```elm") || t.starts_with("```sky") {
                    cur = Some(String::new());
                }
            }
            Some(buf) => {
                if line.trim_end() == "```" {
                    out.push(std::mem::take(buf));
                    cur = None;
                } else {
                    buf.push_str(line);
                    buf.push('\n');
                }
            }
        }
    }
    out
}

fn sky_sources(dir: &Path, out: &mut Vec<(PathBuf, String)>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    for e in rd.flatten() {
        let p = e.path();
        if p.is_dir() {
            sky_sources(&p, out);
        } else if p.extension().and_then(|s| s.to_str()) == Some("sky") {
            if let Ok(text) = std::fs::read_to_string(&p) {
                out.push((p, text));
            }
        }
    }
}

/// The built code a README sample may copy from.
fn built_corpus() -> Vec<(String, String)> {
    let mut corpus = Vec::new();

    let showcase = read("docs/showcase.md");
    let programs: Vec<String> = elm_fences(&showcase)
        .into_iter()
        .filter(|b| b.starts_with("module Main") && !b.contains("doc-example: skip"))
        .collect();
    assert!(
        programs.len() >= 2,
        "docs/showcase.md must hold the full programs the README copies from \
         (found {} checked `module Main` fence(s))",
        programs.len()
    );
    for (i, b) in programs.into_iter().enumerate() {
        corpus.push((format!("docs/showcase.md program {}", i + 1), b));
    }

    let mut files = Vec::new();
    if let Ok(rd) = std::fs::read_dir(repo().join("examples")) {
        for e in rd.flatten() {
            // USER RULE (anzellai/sky#299): examples/76-ai-shell-command is
            // excluded from CI in general. CI does not build it, so a README
            // sample may not copy from it.
            let name = e.file_name().to_string_lossy().into_owned();
            if ci_excluded::is_ci_excluded(&name) {
                continue;
            }
            sky_sources(&e.path().join("src"), &mut files);
        }
    }
    for (p, text) in files {
        corpus.push((p.display().to_string(), text));
    }
    corpus
}

#[test]
fn every_readme_sample_is_a_copy_of_built_code() {
    let readme = read("README.md");
    let samples = elm_fences(&readme);
    assert!(
        samples.len() >= 3,
        "README.md has {} Sky sample(s); the README's \"Show me\" section is \
         expected to carry at least three. If the parse found none, the fence \
         syntax drifted and this test would verify nothing.",
        samples.len()
    );

    let corpus = built_corpus();
    let mut orphans = Vec::new();
    for (i, s) in samples.iter().enumerate() {
        let body = s.trim_end_matches('\n');
        if body.trim().is_empty() {
            orphans.push(format!("  sample {}: empty", i + 1));
            continue;
        }
        if !corpus.iter().any(|(_, text)| text.contains(body)) {
            let first = body.lines().next().unwrap_or("");
            orphans.push(format!("  sample {} (starts `{first}`)", i + 1));
        }
    }
    assert!(
        orphans.is_empty(),
        "README.md sample(s) that are not a verbatim copy of built code:\n{}\n\n\
         Copy each sample from a full `module Main` program in \
         docs/showcase.md (doc-examples.sh checks it) or from an \
         examples/*/src file (the example sweep builds it), and keep the copy \
         exact. Edit the source first, run `sky fmt` on it, then paste.",
        orphans.join("\n")
    );
}

#[test]
fn a_sample_that_is_not_built_code_is_caught() {
    // The falsifier: a sample with one changed token must not match.
    let corpus = built_corpus();
    let readme = read("README.md");
    let sample = elm_fences(&readme)
        .into_iter()
        .next()
        .expect("README.md has a Sky sample");
    let mutated = format!("{}-- drift\n", sample);
    assert!(
        !corpus
            .iter()
            .any(|(_, text)| text.contains(mutated.trim_end())),
        "a mutated sample still matched the corpus, so the main test cannot fail"
    );
}
