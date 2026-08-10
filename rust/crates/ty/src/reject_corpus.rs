//! The **single declaration** of the reject-corpus criterion (v2 §1.5).
//!
//! The reject corpus (`rust/crates/ty/tests/reject/corpus/*.sky`) is checked by
//! TWO faces:
//!
//!   * `cargo test -p ty --test reject` — the `cargo nextest` face, and
//!   * `cargo run -p xtask -- reject`   — the CLI gate face (also driven by the
//!     gate harness, `xtask/src/harness/bodies.rs::reject`).
//!
//! Historically each face carried its OWN copy of "what counts as rejected",
//! "what counts as the corpus", and "how many files must there be". They
//! drifted: the parse-error criterion diverged on severity, discovery diverged
//! on recursion, and both floors (`>= 13`) had gone stale against a corpus of
//! 63. This module exists so that divergence is structurally impossible: both
//! faces call [`evaluate`], [`corpus_files`], and read [`EXPECTED_CORPUS_FILES`]
//! from here, and neither face is permitted a private copy.
//!
//! # THE DECLARED CRITERION
//!
//! A corpus program is **REJECTED** iff at least one of the following holds
//! after parsing it and running [`crate::check_modules`] over it with the
//! stdlib loaded:
//!
//! 1. **Parse.** The parse produced at least one diagnostic *of any severity*,
//!    OR `Parse::error_node_count() > 0`. This mirrors the driver's parse gate
//!    verbatim (`crates/project/src/build.rs:194` — `!parse.errors().is_empty()
//!    || parse.error_node_count() > 0`), which is the behaviour a user actually
//!    experiences from `sky check`. Mirroring the driver is deliberate: if the
//!    driver's parse gate ever changes, the criterion must change with it, and
//!    copying the driver's exact test is what keeps that true.
//!
//!    *Severity note.* `crates/syntax/src/parser.rs` has exactly one diagnostic
//!    construction site (`Parser::error`) and it is unconditionally
//!    `Diagnostic::error`, so "any severity" and "severity == Error" are the
//!    same set today. The distinction is latent, not observable — it arms the
//!    moment anyone adds a parser warning, at which point the driver and this
//!    criterion still agree because they run the same test.
//!
//! 2. **Type.** `CheckOutput::type_errors > 0` (the `[E2001]` / `[E2007]`
//!    unify-clash + arity class).
//!
//! 3. **Name.** `CheckOutput::name_errors > 0` (unresolved name — the `[E1001]`
//!    class — and import cycles, `[E1010]`).
//!
//! 4. **Exhaustiveness.** `CheckOutput::exhaustiveness_warnings > 0`. Sky treats
//!    a non-exhaustive `case` as a hard rejection (self-host R1-D3), stronger
//!    than GHC-as-configured, even though the diagnostic itself carries
//!    `Severity::Warning`. **An exhaustiveness warning COUNTS as rejection.**
//!
//! Diagnostic *text* parity with the Haskell oracle is NOT required (the rewrite
//! may improve prose). Diagnostic *code* parity IS required wherever the corpus
//! file declares a code — see [`declared_codes`].
//!
//! # THE DECLARED CODE RULE
//!
//! A corpus file may declare the diagnostic code(s) its defect is about, on any
//! line mentioning `oracle: reject`, in the form `[E1234]`. Multiple codes on
//! one line are all declared (e.g. `oracle: reject [E2001] + [E2007] arity
//! gate`).
//!
//! **Rule: AT LEAST.** A file with declared codes is satisfied iff the observed
//! code set is a SUPERSET of the declared code set — every declared code must be
//! observed; extra observed codes are permitted.
//!
//! Why at-least and not exact-set:
//!
//!   * One defect legitimately cascades into more than one diagnostic, and the
//!     corpus header records the defect the file is *about*, not an exhaustive
//!     transcript of the checker's output. An exact-set rule would turn a
//!     diagnostic *improvement* (a new secondary code) red, which is not a
//!     soundness regression and would push authors toward weakening headers.
//!   * At-least is nonetheless strictly stronger than the boolean it replaces,
//!     and closes exactly the hole that motivated it: a file that means to
//!     exercise `[E3001]` but is in fact rejected only by a stray `[E0001]`
//!     parse error now FAILS, because `E3001` is not in the observed set.
//!
//! **Observed codes** are the codes of the diagnostics that CONTRIBUTE to the
//! rejection verdict above — every parse diagnostic, plus every check
//! diagnostic that is either `Severity::Error` or carries the exhaustiveness
//! code `E3001`. A `Severity::Warning` diagnostic that plays no part in the
//! verdict cannot satisfy a declared code.
//!
//! Files declaring NO code still assert rejection, and are reported by name by
//! both faces so the gap is visible rather than silent (see
//! [`EXPECTED_FILES_WITHOUT_DECLARED_CODE`]).

use hir::SourceDb;
use std::path::{Path, PathBuf};

/// The corpus directory, relative to the repo root.
pub const CORPUS_REL_DIR: &str = "rust/crates/ty/tests/reject/corpus";

/// The EXACT number of `.sky` files in the reject corpus.
///
/// This is an exact count, not a floor. A floor (the previous `>= 13` against a
/// real corpus of 63) lets 50 files be deleted with every gate still green.
///
/// **Adding or removing a corpus file is a deliberate act: update this constant
/// in the SAME commit.** Both faces fail with an actionable message naming the
/// expected and actual counts, so the ratchet cannot be satisfied by accident.
pub const EXPECTED_CORPUS_FILES: usize = 63;

/// The EXACT number of corpus files tagged `-- gate: known-leniency` — programs
/// the ORACLE rejects that the Rust checker deliberately accepts for a
/// documented accept-parity reason. Reported, but not part of the hard gate.
/// Ratchets DOWNWARD: closing a leniency updates this constant and
/// [`EXPECTED_HARD_GATE_FILES`] in the same commit.
pub const EXPECTED_KNOWN_LENIENCY_FILES: usize = 0;

/// The EXACT number of corpus programs the Rust checker MUST reject. Replaces
/// the stale `hard >= 13` floor: a floor is satisfied by deleting files.
pub const EXPECTED_HARD_GATE_FILES: usize = EXPECTED_CORPUS_FILES - EXPECTED_KNOWN_LENIENCY_FILES;

/// The EXACT number of corpus files that declare at least one diagnostic code
/// in their header (see [`declared_codes`]). Ratchets upward: a new corpus file
/// should declare its code, and moving one of the
/// [`EXPECTED_FILES_WITHOUT_DECLARED_CODE`] files into this set is a welcome
/// change that updates BOTH constants in the same commit.
pub const EXPECTED_FILES_WITH_DECLARED_CODE: usize = 47;

/// The EXACT number of corpus files that declare NO diagnostic code. These
/// still assert rejection, but the rejection is unpinned — any diagnostic
/// satisfies them. Both faces print them by name. Ratchets DOWNWARD.
pub const EXPECTED_FILES_WITHOUT_DECLARED_CODE: usize = 16;

/// Generated / cache trees that are never part of any corpus.
fn is_generated(path: &Path) -> bool {
    path.components().any(|c| {
        matches!(
            c.as_os_str().to_str(),
            Some("sky-out") | Some(".skycache") | Some(".skydeps")
        )
    })
}

/// RECURSIVE `.sky` discovery, sorted, skipping generated trees.
///
/// Both faces use this for the corpus AND for the stdlib. A flat `read_dir` (the
/// old `xtask reject` corpus discovery) silently ignores a `.sky` file placed in
/// a subdirectory — the file is committed, the author believes it is gated, and
/// one of the two faces never sees it.
pub fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for path in entries {
        if is_generated(&path) {
            continue;
        }
        if path.is_dir() {
            collect_sky(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}

/// The corpus directory under `root`.
pub fn corpus_dir(root: &Path) -> PathBuf {
    root.join(CORPUS_REL_DIR)
}

/// Every corpus file, discovered RECURSIVELY, with the exact-count ratchet
/// enforced. `Err` carries the actionable message both faces surface verbatim.
pub fn corpus_files(root: &Path) -> Result<Vec<PathBuf>, String> {
    let dir = corpus_dir(root);
    if !dir.is_dir() {
        return Err(format!("reject: cannot read corpus dir {}", dir.display()));
    }
    let mut files = Vec::new();
    collect_sky(&dir, &mut files);
    if files.len() != EXPECTED_CORPUS_FILES {
        return Err(format!(
            "reject corpus size changed: expected EXACTLY {EXPECTED_CORPUS_FILES} \
             .sky files under {}, found {}. Adding or removing a corpus file is a \
             deliberate act — update ty::reject_corpus::EXPECTED_CORPUS_FILES (and \
             EXPECTED_FILES_WITH_DECLARED_CODE / \
             EXPECTED_FILES_WITHOUT_DECLARED_CODE) in the SAME commit.",
            dir.display(),
            files.len()
        ));
    }
    Ok(files)
}

/// Derive a module name: the header if present, else the path below
/// `root_marker`.
fn module_name(parse: &syntax::Parse, path: &Path, root_marker: &str) -> String {
    if let Some(n) = parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
    {
        if !n.is_empty() {
            return n;
        }
    }
    let comps: Vec<&str> = path.iter().filter_map(|c| c.to_str()).collect();
    let start = comps
        .iter()
        .rposition(|c| *c == root_marker)
        .map(|i| i + 1)
        .unwrap_or(0);
    let mut segs: Vec<String> = comps[start..].iter().map(|s| s.to_string()).collect();
    if let Some(last) = segs.last_mut() {
        *last = last.trim_end_matches(".sky").to_string();
    }
    segs.join(".")
}

/// Parse every `.sky` file under `dir` (recursively) into `(module name, parse)`.
pub fn load_dir(dir: &Path, root_marker: &str) -> Vec<(String, syntax::Parse)> {
    let mut files = Vec::new();
    collect_sky(dir, &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let name = module_name(&parse, &path, root_marker);
        out.push((name, parse));
    }
    out
}

/// The stdlib world both faces check the corpus against.
pub fn load_stdlib(root: &Path) -> Vec<(String, syntax::Parse)> {
    load_dir(&root.join("sky-stdlib"), "sky-stdlib")
}

/// Every `[E1234]` code declared on a line mentioning `oracle: reject`, in
/// source order, deduplicated.
///
/// Only the text AFTER `oracle: reject` on that line is scanned, so a code
/// mentioned in unrelated prose earlier on the line is not mistaken for a
/// declaration. See the module docstring for the AT-LEAST satisfaction rule.
pub fn declared_codes(src: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for line in src.lines() {
        let Some((_, tail)) = line.split_once("oracle: reject") else {
            continue;
        };
        for code in scan_codes(tail) {
            if !out.contains(&code) {
                out.push(code);
            }
        }
    }
    out
}

/// Extract every `[E<digits>]` token from `text`.
fn scan_codes(text: &str) -> Vec<String> {
    let bytes = text.as_bytes();
    let mut out = Vec::new();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'[' {
            let start = i + 1;
            if let Some(end_rel) = text[start..].find(']') {
                let inner = &text[start..start + end_rel];
                let mut chars = inner.chars();
                if chars.next() == Some('E')
                    && !inner[1..].is_empty()
                    && inner[1..].chars().all(|c| c.is_ascii_digit())
                {
                    out.push(inner.to_string());
                }
                i = start + end_rel + 1;
                continue;
            }
        }
        i += 1;
    }
    out
}

/// The per-file verdict — the ONE shape both faces report from.
pub struct Verdict {
    pub name: String,
    pub type_errors: usize,
    pub name_errors: usize,
    pub exhaustiveness: usize,
    /// Parse diagnostics (`[E0001]` class), floored to 1 when the tree carries
    /// ERROR nodes but no diagnostic. See criterion clause 1.
    pub parse_errors: usize,
    /// A file tagged `-- gate: known-leniency` in its first 3 lines is a program
    /// the ORACLE rejects but the Rust checker deliberately accepts for a
    /// documented accept-parity reason (see the file's header). Tracked +
    /// reported, but NOT counted against the hard reject gate.
    pub known_leniency: bool,
    /// Codes the file's header declares (see [`declared_codes`]).
    pub declared_codes: Vec<String>,
    /// Codes actually observed among the verdict-contributing diagnostics,
    /// deduplicated, sorted.
    pub observed_codes: Vec<String>,
    pub first_msg: String,
}

impl Verdict {
    /// THE criterion. See the module docstring.
    pub fn rejected(&self) -> bool {
        self.parse_errors > 0
            || self.type_errors > 0
            || self.name_errors > 0
            || self.exhaustiveness > 0
    }

    /// Declared codes that were NOT observed — empty means the file is
    /// satisfied under the AT-LEAST rule. A file declaring no code is always
    /// satisfied (it asserts rejection only).
    pub fn missing_codes(&self) -> Vec<String> {
        self.declared_codes
            .iter()
            .filter(|c| !self.observed_codes.contains(c))
            .cloned()
            .collect()
    }

    /// Which signal caught it (for the report).
    pub fn signal(&self) -> &'static str {
        if self.parse_errors > 0 {
            "parse"
        } else if self.type_errors > 0 {
            "type"
        } else if self.name_errors > 0 {
            "name"
        } else if self.exhaustiveness > 0 {
            "exhaustive"
        } else {
            "-"
        }
    }
}

/// Run ONE corpus file through parse + check and apply the declared criterion.
///
/// This is the only place either face is allowed to decide "rejected".
pub fn evaluate(file: &Path, stdlib: &[(String, syntax::Parse)]) -> Verdict {
    let name = file
        .file_name()
        .and_then(|s| s.to_str())
        .unwrap_or("?")
        .to_string();

    let src = std::fs::read_to_string(file).unwrap_or_default();
    let known_leniency = src
        .lines()
        .take(3)
        .any(|l| l.contains("gate: known-leniency"));
    let declared = declared_codes(&src);

    let mut db = SourceDb::new();
    for (n, parse) in stdlib {
        db.add_module(n, parse.clone());
    }

    let parse = syntax::parse(&src, base::FileId(0));
    // Criterion clause 1 — mirrors `crates/project/src/build.rs:194`. Read
    // BEFORE the parse is moved into the db.
    let parse_errors = parse.errors().len().max(parse.error_node_count().min(1));
    let mut observed: Vec<String> = parse.errors().iter().map(|d| d.code.0.clone()).collect();
    let parse_first: Option<String> = parse.errors().first().map(fmt_diag);

    let mname = parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "Main".to_string());
    let mid = db.add_module(&mname, parse);
    let out = crate::check_modules(&db, &[mid]);

    // Observed codes = the codes of the diagnostics that CONTRIBUTE to the
    // verdict: errors, plus the exhaustiveness warnings the criterion promotes
    // to rejections.
    observed.extend(
        out.diagnostics
            .iter()
            .filter(|d| d.severity == diagnostics::Severity::Error || d.code.0 == "E3001")
            .map(|d| d.code.0.clone()),
    );
    observed.sort();
    observed.dedup();

    let first_msg = parse_first
        .or_else(|| {
            out.diagnostics
                .iter()
                .find(|d| d.severity == diagnostics::Severity::Error || d.code.0 == "E3001")
                .map(fmt_diag)
        })
        .unwrap_or_default();

    Verdict {
        name,
        type_errors: out.type_errors,
        name_errors: out.name_errors,
        exhaustiveness: out.exhaustiveness_warnings,
        parse_errors,
        known_leniency,
        declared_codes: declared,
        observed_codes: observed,
        first_msg,
    }
}

fn fmt_diag(d: &diagnostics::Diagnostic) -> String {
    let m = d.message.replace('\n', " ");
    let m: String = m.chars().take(70).collect();
    format!("[{}] {}", d.code.0, m)
}

/// Scan the whole corpus. Both faces call this and then apply their own
/// reporting; neither re-derives discovery or the criterion.
pub fn scan(root: &Path) -> Result<Vec<Verdict>, String> {
    let stdlib = load_stdlib(root);
    if stdlib.is_empty() {
        return Err(format!(
            "reject: no stdlib modules under {}/sky-stdlib",
            root.display()
        ));
    }
    let files = corpus_files(root)?;
    Ok(files.iter().map(|f| evaluate(f, &stdlib)).collect())
}

/// The declared-code census, asserted exactly by both faces so the numbers
/// ratchet. Returns `(with_code, without_code_names)`.
pub fn code_census(rows: &[Verdict]) -> (usize, Vec<&str>) {
    let with = rows.iter().filter(|r| !r.declared_codes.is_empty()).count();
    let without: Vec<&str> = rows
        .iter()
        .filter(|r| r.declared_codes.is_empty())
        .map(|r| r.name.as_str())
        .collect();
    (with, without)
}

/// Check the census against the ratchet constants. `Err` is the actionable
/// message both faces surface verbatim.
pub fn check_code_census(rows: &[Verdict]) -> Result<(), String> {
    let (with, without) = code_census(rows);
    if with != EXPECTED_FILES_WITH_DECLARED_CODE
        || without.len() != EXPECTED_FILES_WITHOUT_DECLARED_CODE
    {
        return Err(format!(
            "reject corpus code-declaration census changed: expected EXACTLY \
             {EXPECTED_FILES_WITH_DECLARED_CODE} file(s) declaring a diagnostic code and \
             {EXPECTED_FILES_WITHOUT_DECLARED_CODE} declaring none, found {with} / {}. \
             Update ty::reject_corpus::EXPECTED_FILES_WITH_DECLARED_CODE / \
             EXPECTED_FILES_WITHOUT_DECLARED_CODE in the SAME commit. \
             Undeclared: {}",
            without.len(),
            without.join(", ")
        ));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn declared_codes_parses_single_multi_and_none() {
        assert_eq!(declared_codes("-- oracle: reject [E2001]"), vec!["E2001"]);
        assert_eq!(
            declared_codes("-- oracle: reject [E2001] + [E2007] arity gate"),
            vec!["E2001", "E2007"]
        );
        assert_eq!(
            declared_codes("-- oracle: reject — exit 1, `[E0001] PARSE ERROR"),
            vec!["E0001"]
        );
        assert!(declared_codes("-- oracle: reject. no code here").is_empty());
        // A code BEFORE the marker is prose, not a declaration.
        assert!(declared_codes("-- [E2001] was the old code; oracle: reject").is_empty());
        // Not a diagnostic code shape.
        assert!(declared_codes("-- oracle: reject [nope] [E] [E12a]").is_empty());
    }

    #[test]
    fn missing_codes_is_at_least_not_exact() {
        let v = Verdict {
            name: "x".into(),
            type_errors: 1,
            name_errors: 0,
            exhaustiveness: 0,
            parse_errors: 0,
            known_leniency: false,
            declared_codes: vec!["E2001".into()],
            observed_codes: vec!["E2001".into(), "E2007".into()],
            first_msg: String::new(),
        };
        assert!(v.rejected());
        assert!(
            v.missing_codes().is_empty(),
            "extra observed codes are fine"
        );

        let v = Verdict {
            declared_codes: vec!["E3001".into()],
            observed_codes: vec!["E0001".into()],
            ..v
        };
        assert_eq!(v.missing_codes(), vec!["E3001".to_string()]);
    }
}
