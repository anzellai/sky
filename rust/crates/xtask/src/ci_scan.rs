//! What CI actually invokes — ONE extractor, used by every caller.
//!
//! Two things need to read the same fact: `gate_manifest_test` asserts that
//! every `xtask <gate>` reference in `.github/workflows/**` and `scripts/**`
//! is a subcommand `xtask` dispatches, and `coverage_ledger` needs the same
//! references to score a surface as CI-covered. Those must never be two
//! extractors. A second copy drifts, and the drift is invisible: the manifest
//! test would keep passing against one reading of CI while the ledger reported
//! coverage against another.
//!
//! The extractor is deliberately strict. An `xtask` invocation whose gate name
//! is a shell variable is resolved through the enclosing `for VAR in ...` list,
//! and an *unresolvable* reference is reported as such rather than skipped — a
//! blind spot in the extractor is exactly the failure mode both callers exist
//! to prevent.

use std::path::{Path, PathBuf};

/// One invocation found in a CI workflow or a script.
#[derive(Debug, Clone)]
pub struct GateRef {
    pub file: String,
    /// Read by `gate_manifest_test`'s failure message, which is a test-only
    /// build; the ledger deliberately omits it from its evidence strings so an
    /// unrelated CI edit cannot make the checked-in ledger go stale.
    #[allow(dead_code)]
    pub line_no: usize,
    /// The `xtask` subcommand, or the repo-relative script path.
    pub gate: String,
}

/// Recursively collect every regular file under `dir`.
pub fn collect_files(dir: &Path, out: &mut Vec<PathBuf>) {
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
/// silently disarm the callers.
///
/// Returns the token verbatim (quotes stripped); variable references are
/// resolved later by [`resolve`].
pub fn gate_tokens(line: &str) -> Vec<String> {
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

pub enum Resolved {
    Names(Vec<String>),
    Unresolvable,
}

/// Resolve a raw gate token to concrete gate names.
///
/// A literal resolves to itself. A shell variable (`"$g"`) resolves through the
/// nearest enclosing `for VAR in <names>` list — the shape used by
/// `scripts/test-ci.sh` and `scripts/preflight-tag.sh`, where the whole gate
/// suite is one loop. Anything else is [`Resolved::Unresolvable`]: the caller
/// fails rather than quietly ignoring an invocation it cannot read.
pub fn resolve(raw: &str, lines: &[&str], idx: usize) -> Resolved {
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
pub fn var_name(raw: &str) -> Option<String> {
    let s = unquote(raw);
    let rest = s.strip_prefix('$')?;
    let rest = rest
        .strip_prefix('{')
        .and_then(|r| r.strip_suffix('}'))
        .unwrap_or(rest);
    Some(rest.to_string())
}

/// If `line` contains `for <var> in a b c;` (or `... do`), return `[a, b, c]`.
pub fn for_loop_items(line: &str, var: &str) -> Option<Vec<String>> {
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

pub fn unquote(tok: &str) -> &str {
    tok.trim_matches(|c| c == '"' || c == '\'')
}

/// Scan `roots` (directories) for `xtask <gate>` invocations.
///
/// Returns the resolved references and the invocations that could NOT be
/// resolved. Callers must treat a non-empty `unresolved` as a failure: an
/// unread invocation is an unguarded one.
pub fn scan_xtask_refs(repo_root: &Path, roots: &[PathBuf]) -> (Vec<GateRef>, Vec<String>) {
    let mut files = Vec::new();
    for r in roots {
        if r.is_dir() {
            collect_files(r, &mut files);
        } else if r.is_file() {
            files.push(r.clone());
        }
    }
    files.sort();

    let mut refs: Vec<GateRef> = Vec::new();
    let mut unresolved: Vec<String> = Vec::new();
    for file in &files {
        let Ok(text) = std::fs::read_to_string(file) else {
            continue; // non-UTF-8 (binary) file — no shell/YAML to scan
        };
        let rel = rel_of(repo_root, file);
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
                         in ci_scan.rs (an unread invocation is an unguarded one)",
                        rel,
                        idx + 1,
                        raw
                    )),
                }
            }
        }
    }
    (refs, unresolved)
}

/// Scan `roots` for `scripts/<path>` invocations — the other half of what CI
/// runs. A commented line is not an invocation.
pub fn scan_script_refs(repo_root: &Path, roots: &[PathBuf]) -> Vec<GateRef> {
    let mut files = Vec::new();
    for r in roots {
        if r.is_dir() {
            collect_files(r, &mut files);
        } else if r.is_file() {
            files.push(r.clone());
        }
    }
    files.sort();

    let mut refs = Vec::new();
    for file in &files {
        let Ok(text) = std::fs::read_to_string(file) else {
            continue;
        };
        let rel = rel_of(repo_root, file);
        for (idx, line) in text.lines().enumerate() {
            if line.trim_start().starts_with('#') {
                continue;
            }
            for tok in line.split(|c: char| c.is_whitespace() || c == '"' || c == '\'') {
                let tok = tok.trim_end_matches([';', ')']);
                // CI invokes these from several working directories, so the
                // token is `scripts/x.sh`, `./scripts/x.sh` or `../scripts/x.sh`.
                // Anchoring on the prefix missed the `../` form, which silently
                // scored a CI-wired gate as absent — understating coverage,
                // which is the direction that manufactures false weakenings.
                let Some(at) = tok.find("scripts/") else {
                    continue;
                };
                let t = &tok[at..];
                if !(t.ends_with(".sh") || t.ends_with(".mjs") || t.ends_with(".cjs")) {
                    continue;
                }
                refs.push(GateRef {
                    file: rel.clone(),
                    line_no: idx + 1,
                    gate: t.to_string(),
                });
            }
        }
    }
    refs
}

/// Does an UNCOMMENTED line in any of `roots` contain `needle`?
///
/// The escape hatch for the handful of CI steps that are neither an `xtask`
/// subcommand nor a script — `go test ./rt/...` is a real gate and pretending
/// otherwise, because it does not fit the two tidy shapes, would understate
/// coverage.
pub fn mentions_command(roots: &[PathBuf], needle: &str) -> bool {
    let mut files = Vec::new();
    for r in roots {
        if r.is_dir() {
            collect_files(r, &mut files);
        } else if r.is_file() {
            files.push(r.clone());
        }
    }
    for file in &files {
        let Ok(text) = std::fs::read_to_string(file) else {
            continue;
        };
        for line in text.lines() {
            if line.trim_start().starts_with('#') {
                continue;
            }
            if line.contains(needle) {
                return true;
            }
        }
    }
    false
}

fn rel_of(repo_root: &Path, file: &Path) -> String {
    file.strip_prefix(repo_root)
        .unwrap_or(file)
        .to_string_lossy()
        .into_owned()
}
