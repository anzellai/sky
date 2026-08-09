//! Gate-manifest test — the class-level guard behind the exit-code fix in
//! `main.rs`.
//!
//! Making an unknown subcommand exit 2 turns a typo'd gate name into a LOUD CI
//! failure instead of a silently-green no-op. That closes the instance. This
//! test closes the CLASS on the other side: it walks every file under
//! `.github/workflows/` and `scripts/`, extracts the gate name from every
//! `xtask` invocation it finds, and asserts each one is dispatched by
//! [`crate::GATES`]. A rename in `main.rs` that leaves CI behind (or a typo in
//! CI that `main.rs` never dispatches) fails `cargo test -p xtask` — before the
//! push, not after a green-but-dead CI run.
//!
//! The extractor is deliberately strict: an `xtask` invocation whose gate name
//! is a shell variable is resolved through the enclosing `for VAR in ...` list,
//! and an *unresolvable* reference is a hard failure rather than a silent skip
//! — a blind spot in the extractor is exactly the failure mode this test
//! exists to prevent.

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

/// Gate names that MUST be found by the extractor. A refactor that breaks the
/// extractor (or moves the gate suite out of these trees) would otherwise leave
/// the test asserting nothing at all, forever green and worthless.
const MUST_FIND: &[&str] = &["build-run", "reject", "coerce-floor", "repro", "roundtrip"];

/// Lower bound on total extracted references. Deliberately far below the real
/// count (42 at the time of writing) so ordinary CI edits do not trip it, but
/// non-zero so a broken extractor cannot pass vacuously.
const MIN_REFS: usize = 20;

/// One `xtask <gate>` reference found in a CI workflow or a script.
#[derive(Debug)]
struct GateRef {
    file: String,
    line_no: usize,
    gate: String,
}

#[test]
fn every_ci_gate_name_is_dispatched_by_xtask() {
    let root = crate::repo_root();
    let workflows = root.join(".github/workflows");
    let scripts = root.join("scripts");
    assert!(
        workflows.is_dir(),
        "expected {} to exist — repo_root() resolved to {}",
        workflows.display(),
        root.display()
    );
    assert!(
        scripts.is_dir(),
        "expected {} to exist — repo_root() resolved to {}",
        scripts.display(),
        root.display()
    );

    let mut files = Vec::new();
    collect_files(&workflows, &mut files);
    collect_files(&scripts, &mut files);
    files.sort();

    let mut refs: Vec<GateRef> = Vec::new();
    let mut unresolved: Vec<String> = Vec::new();
    for file in &files {
        let Ok(text) = std::fs::read_to_string(file) else {
            continue; // non-UTF-8 (binary) file — no shell/YAML to scan
        };
        let rel = file
            .strip_prefix(&root)
            .unwrap_or(file)
            .to_string_lossy()
            .into_owned();
        let lines: Vec<&str> = text.lines().collect();
        for (idx, line) in lines.iter().enumerate() {
            for raw in gate_tokens(line) {
                match resolve(&raw, &lines, idx) {
                    Resolved::Names(names) => {
                        for gate in names {
                            refs.push(GateRef {
                                file: rel.clone(),
                                line_no: idx + 1,
                                gate,
                            });
                        }
                    }
                    Resolved::Unresolvable => unresolved.push(format!(
                        "{}:{}: cannot resolve xtask gate name `{}` — extend the extractor \
                         in gate_manifest_test.rs (an unread invocation is an unguarded one)",
                        rel,
                        idx + 1,
                        raw
                    )),
                }
            }
        }
    }

    assert!(
        unresolved.is_empty(),
        "xtask gate references the manifest test could not read:\n  {}",
        unresolved.join("\n  ")
    );

    // Visible under `--nocapture`: the extractor's reach, so a shrinking count
    // is noticeable during review as well as at the MIN_REFS floor.
    let mut per_file: BTreeSet<String> = BTreeSet::new();
    for r in &refs {
        per_file.insert(r.file.clone());
    }
    println!(
        "gate-manifest: {} xtask gate references across {} file(s): {:?}",
        refs.len(),
        per_file.len(),
        per_file
    );

    // Anti-vacuity: a matcher that finds nothing asserts nothing.
    assert!(
        refs.len() >= MIN_REFS,
        "gate-manifest extractor found only {} xtask references across {} files (expected \
         >= {}). The extractor is broken or the gate suite moved — a vacuous manifest test \
         is the exact failure class this test exists to prevent.",
        refs.len(),
        files.len(),
        MIN_REFS
    );
    let found: BTreeSet<&str> = refs.iter().map(|r| r.gate.as_str()).collect();
    for expect in MUST_FIND {
        assert!(
            found.contains(expect),
            "gate-manifest extractor never found a reference to `{expect}` — it is invoked \
             from CI, so the extractor (not CI) is what changed. Found: {found:?}"
        );
    }

    // The actual manifest assertion.
    let known: BTreeSet<&str> = crate::GATES.iter().map(|(name, _)| *name).collect();
    let bad: Vec<&GateRef> = refs
        .iter()
        .filter(|r| !known.contains(r.gate.as_str()))
        .collect();
    assert!(
        bad.is_empty(),
        "CI/scripts invoke xtask gate names that xtask does not dispatch (they would exit 2 \
         and fail the build — or, before the exit-code fix, silently pass while running \
         nothing):\n  {}\nxtask dispatches: {:?}",
        bad.iter()
            .map(|r| format!("{}:{}: `{}`", r.file, r.line_no, r.gate))
            .collect::<Vec<_>>()
            .join("\n  "),
        known
    );
}

/// Recursively collect every regular file under `dir`.
fn collect_files(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    for entry in rd.flatten() {
        let path = entry.path();
        if path.is_dir() {
            collect_files(&path, out);
        } else if path.is_file() {
            out.push(path);
        }
    }
}

/// Extract the raw gate-name token of every xtask invocation on `line`.
///
/// The invocation shapes that actually occur in this repo (verified against
/// `.github/workflows/rust-ci.yml`, `scripts/test-ci.sh`,
/// `scripts/test-local.sh`, `scripts/preflight-tag.sh`):
///
/// ```text
/// cargo run -q -p xtask -- roundtrip
/// cargo run -q -p xtask -- build-run --shape live --run
/// cargo run --release -q -p xtask -- "$g"
/// ```
///
/// Plus the not-yet-used-but-plausible direct-binary form
/// (`target/release/xtask roundtrip`), handled so that switching to it does not
/// silently disarm this test.
///
/// Returns the token verbatim (quotes stripped); variable references are
/// resolved later by [`resolve`].
fn gate_tokens(line: &str) -> Vec<String> {
    // A commented-out invocation does not run, so it is not a gate reference.
    if line.trim_start().starts_with('#') {
        return Vec::new();
    }
    let toks: Vec<&str> = line.split_whitespace().collect();
    let mut out = Vec::new();
    for (i, tok) in toks.iter().enumerate() {
        let bare = unquote(tok);
        let is_cargo_form = bare == "xtask" && i > 0 && unquote(toks[i - 1]) == "-p";
        let is_binary_form = !is_cargo_form
            && (bare == "xtask" || bare.rsplit('/').next() == Some("xtask"))
            && bare.contains('/');
        if is_cargo_form {
            // `-p xtask [flags...] -- <gate>`: skip cargo's own flags, then take
            // the token right after the `--` separator.
            let mut j = i + 1;
            while j < toks.len() && unquote(toks[j]) != "--" && unquote(toks[j]).starts_with('-') {
                j += 1;
            }
            if j < toks.len() && unquote(toks[j]) == "--" {
                if let Some(gate) = toks.get(j + 1) {
                    out.push(unquote(gate).to_string());
                }
            }
        } else if is_binary_form {
            // `path/to/xtask <gate> [flags...]`
            if let Some(gate) = toks.get(i + 1) {
                let gate = unquote(gate);
                if !gate.starts_with('-') {
                    out.push(gate.to_string());
                }
            }
        }
    }
    out
}

enum Resolved {
    Names(Vec<String>),
    Unresolvable,
}

/// Resolve a raw gate token to concrete gate names.
///
/// A literal resolves to itself. A shell variable (`"$g"`) resolves through the
/// nearest enclosing `for VAR in <names>` list — the shape used by
/// `scripts/test-ci.sh` and `scripts/preflight-tag.sh`, where the whole gate
/// suite is one loop. Anything else is [`Resolved::Unresolvable`]: the test
/// fails rather than quietly ignoring an invocation it cannot read.
fn resolve(raw: &str, lines: &[&str], idx: usize) -> Resolved {
    let Some(var) = var_name(raw) else {
        return Resolved::Names(vec![raw.to_string()]);
    };
    // Search the current line first (single-line `for ...; do ...; done`), then
    // upwards for the enclosing loop header.
    for line in lines[..=idx].iter().rev() {
        if let Some(items) = for_loop_items(line, &var) {
            if items.iter().any(|i| var_name(i).is_some()) {
                return Resolved::Unresolvable; // nested indirection
            }
            return Resolved::Names(items);
        }
    }
    Resolved::Unresolvable
}

/// `$g` / `${g}` / `"$g"` → `g`. Anything else → `None`.
fn var_name(raw: &str) -> Option<String> {
    let s = unquote(raw);
    let rest = s.strip_prefix('$')?;
    let rest = rest
        .strip_prefix('{')
        .and_then(|r| r.strip_suffix('}'))
        .unwrap_or(rest);
    Some(rest.to_string())
}

/// If `line` contains `for <var> in a b c;` (or `... do`), return `[a, b, c]`.
fn for_loop_items(line: &str, var: &str) -> Option<Vec<String>> {
    let toks: Vec<&str> = line.split_whitespace().collect();
    for i in 0..toks.len() {
        if toks[i] != "for" || toks.get(i + 1) != Some(&var) || toks.get(i + 2) != Some(&"in") {
            continue;
        }
        let mut items = Vec::new();
        for tok in &toks[i + 3..] {
            let t = tok.trim_end_matches(';');
            if t.is_empty() || t == "do" {
                break;
            }
            items.push(unquote(t).to_string());
            if tok.ends_with(';') {
                break;
            }
        }
        return Some(items);
    }
    None
}

fn unquote(tok: &str) -> &str {
    tok.trim_matches(|c| c == '"' || c == '\'')
}
