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
//! A corpus file may declare the diagnostic code(s) its defect is about, in the
//! form `[E1234]`, on a header line carrying one of TWO markers. Multiple codes
//! on one line are all declared (e.g. `oracle: reject [E2001] + [E2007] arity
//! gate`).
//!
//! ## PRECEDENCE — `-- rust:` WINS over `-- oracle:`
//!
//! 1. `-- rust: reject [CODE…]` — the RUST expectation. When present it WINS,
//!    and the `-- oracle:` line on the same file is NOT consulted for codes.
//! 2. `-- oracle: reject [CODE…]` — the ORACLE expectation, used as the Rust
//!    expectation ON THE DOCUMENTED ASSUMPTION that the two agree. This is the
//!    common case and it is a fallback, not a synonym.
//! 3. Neither — no declared code; the file asserts rejection only.
//!
//! **Why this precedence exists — do NOT "simplify" it back to one header.**
//! `-- oracle:` documents what the HASKELL ORACLE does; this gate observes what
//! RUST emits. They are two different claims, and asserting the first against
//! the second is a category error. It bit us concretely: `arity_call_value_
//! with_unit.sky` and `regress_cli_over_application.sky` declare the oracle's
//! generic `[E2001]` unify clash, while Rust emits the dedicated, STRICTLY MORE
//! SPECIFIC arity diagnostic `[E2007]`. Rust is better there, and a gate must
//! not punish a diagnostic improvement. Collapsing the two markers back into one
//! would silently re-assert the oracle's taxonomy against Rust and re-open that
//! exact failure.
//!
//! The two headers COEXISTING is the record of a legitimate divergence: the
//! oracle expectation stays true and stays the differential's reference, while
//! the `-- rust:` line states what this gate checks. Never delete an `--
//! oracle:` line to "resolve" a mismatch.
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
//! [`EXPECTED_FILES_WITHOUT_DECLARED_CODE`]). The three-way census
//! (rust-declared / oracle-derived / undeclared) is asserted exactly, so the
//! split cannot drift unnoticed either.

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
pub const EXPECTED_CORPUS_FILES: usize = 75;

/// The EXACT number of corpus files tagged `-- gate: known-leniency` — programs
/// the ORACLE rejects that the Rust checker deliberately accepts for a
/// documented accept-parity reason. Reported, but not part of the hard gate.
/// Ratchets DOWNWARD: closing a leniency updates this constant and
/// [`EXPECTED_HARD_GATE_FILES`] in the same commit.
pub const EXPECTED_KNOWN_LENIENCY_FILES: usize = 0;

/// The EXACT number of corpus programs the Rust checker MUST reject. Replaces
/// the stale `hard >= 13` floor: a floor is satisfied by deleting files.
pub const EXPECTED_HARD_GATE_FILES: usize = EXPECTED_CORPUS_FILES - EXPECTED_KNOWN_LENIENCY_FILES;

/// The EXACT number of corpus files whose expectation comes from a RUST-specific
/// `-- rust: reject [CODE…]` header — the files where Rust's diagnostic
/// legitimately differs from the oracle's (see the precedence rule in the module
/// docstring). Ratchets: adding a `-- rust:` line to a file moves it out of
/// [`EXPECTED_FILES_WITH_ORACLE_CODE`] or
/// [`EXPECTED_FILES_WITHOUT_DECLARED_CODE`] and updates BOTH constants in the
/// same commit.
pub const EXPECTED_FILES_WITH_RUST_CODE: usize = 29;

/// The EXACT number of corpus files whose expectation is DERIVED from the
/// `-- oracle: reject [CODE…]` header, on the assumption that Rust and the
/// oracle agree on the code. A file that turns out to disagree gains a
/// `-- rust:` line (never an edited `-- oracle:` line) and moves to
/// [`EXPECTED_FILES_WITH_RUST_CODE`].
pub const EXPECTED_FILES_WITH_ORACLE_CODE: usize = 46;

/// Total files that declare a code either way. Kept for the report line.
pub const EXPECTED_FILES_WITH_DECLARED_CODE: usize =
    EXPECTED_FILES_WITH_RUST_CODE + EXPECTED_FILES_WITH_ORACLE_CODE;

/// The EXACT number of corpus files that declare NO diagnostic code. These
/// still assert rejection, but the rejection is unpinned — any diagnostic
/// satisfies them. Both faces print them by name. Ratchets DOWNWARD.
pub const EXPECTED_FILES_WITHOUT_DECLARED_CODE: usize = 0;

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

/// Where a file's expected code(s) came from. See the PRECEDENCE section of the
/// module docstring — `Rust` WINS over `Oracle`.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum CodeSource {
    /// From a `-- rust: reject [CODE…]` header — Rust's own expectation, stated
    /// because it differs from the oracle's.
    Rust,
    /// Derived from `-- oracle: reject [CODE…]`, on the documented assumption
    /// that Rust and the oracle agree on the code.
    Oracle,
    /// No code declared; the file asserts rejection only.
    None,
}

impl CodeSource {
    pub fn label(self) -> &'static str {
        match self {
            CodeSource::Rust => "rust",
            CodeSource::Oracle => "oracle",
            CodeSource::None => "unpinned",
        }
    }
}

/// The codes THIS GATE expects Rust to emit, plus where they came from.
///
/// Precedence: a `-- rust: reject [CODE…]` header WINS outright; otherwise
/// `-- oracle: reject [CODE…]` is used as the Rust expectation; otherwise none.
/// Only the text AFTER the marker on that line is scanned, so a code mentioned
/// in unrelated prose earlier on the line is not mistaken for a declaration.
/// See the module docstring for WHY the precedence exists and for the AT-LEAST
/// satisfaction rule.
pub fn declared_codes(src: &str) -> (Vec<String>, CodeSource) {
    let rust = codes_after(src, "rust: reject");
    if !rust.is_empty() {
        return (rust, CodeSource::Rust);
    }
    let oracle = codes_after(src, "oracle: reject");
    if !oracle.is_empty() {
        return (oracle, CodeSource::Oracle);
    }
    (Vec::new(), CodeSource::None)
}

/// Every `[E1234]` code appearing after `marker` on any line, in source order,
/// deduplicated.
fn codes_after(src: &str, marker: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for line in src.lines() {
        let Some((_, tail)) = line.split_once(marker) else {
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
    /// Codes THIS GATE expects Rust to emit (see [`declared_codes`]).
    pub declared_codes: Vec<String>,
    /// Which header the expectation came from — `-- rust:` beats `-- oracle:`.
    pub code_source: CodeSource,
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
    let (declared, code_source) = declared_codes(&src);

    let v = evaluate_modules(&name, &[(String::new(), src)], stdlib);
    Verdict {
        known_leniency,
        declared_codes: declared,
        code_source,
        ..v
    }
}

/// Apply THE declared criterion to modules held **in memory**.
///
/// The generated reject matrix (`xtask corpus --reject`, v2 §3.1 family R) has
/// no files on disk: its programs are a pure function of the generator and its
/// axis space. It must nonetheless decide "rejected" by exactly the rule the
/// checked-in corpus uses, so this is the shared core and [`evaluate`] is a thin
/// file-reading wrapper over it. A private copy in `xtask` is precisely the
/// divergence v2 §1.5 catalogues — the two reject faces once disagreed on the
/// parse criterion and on discovery, and neither knew.
///
/// `modules` is `(dotted module name, source)`. An EMPTY name means "take the
/// name from the module header, defaulting to `Main`" (what a single corpus file
/// does). Every listed module is added to the db AND checked, so a defect in a
/// helper module counts exactly as it would in a real build — which is the whole
/// point of varying the import shape.
///
/// The returned verdict carries **no** declared codes and `known_leniency =
/// false`: those come from a file header, and a generated case declares its
/// expectation in the generator instead. The caller overlays them.
pub fn evaluate_modules(
    label: &str,
    modules: &[(String, String)],
    stdlib: &[(String, syntax::Parse)],
) -> Verdict {
    let mut db = SourceDb::new();
    for (n, parse) in stdlib {
        db.add_module(n, parse.clone());
    }

    let mut parse_errors = 0usize;
    let mut observed: Vec<String> = Vec::new();
    let mut parse_first: Option<String> = None;
    let mut ids = Vec::new();

    for (name, src) in modules {
        let parse = syntax::parse(src, base::FileId(0));
        // Criterion clause 1 — mirrors `crates/project/src/build.rs:194`. Read
        // BEFORE the parse is moved into the db.
        parse_errors += parse.errors().len().max(parse.error_node_count().min(1));
        observed.extend(parse.errors().iter().map(|d| d.code.0.clone()));
        if parse_first.is_none() {
            parse_first = parse.errors().first().map(fmt_diag);
        }
        let mname = if name.is_empty() {
            parse
                .tree()
                .module_header()
                .and_then(|h| h.name())
                .map(|n| n.text())
                .filter(|s| !s.is_empty())
                .unwrap_or_else(|| "Main".to_string())
        } else {
            name.clone()
        };
        ids.push(db.add_module(&mname, parse));
    }

    let out = crate::check_modules(&db, &ids);

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
        name: label.to_string(),
        type_errors: out.type_errors,
        name_errors: out.name_errors,
        exhaustiveness: out.exhaustiveness_warnings,
        parse_errors,
        known_leniency: false,
        declared_codes: Vec::new(),
        code_source: CodeSource::None,
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
pub fn code_census(rows: &[Verdict]) -> (usize, usize, Vec<&str>) {
    let rust = rows
        .iter()
        .filter(|r| r.code_source == CodeSource::Rust)
        .count();
    let oracle = rows
        .iter()
        .filter(|r| r.code_source == CodeSource::Oracle)
        .count();
    let none: Vec<&str> = rows
        .iter()
        .filter(|r| r.code_source == CodeSource::None)
        .map(|r| r.name.as_str())
        .collect();
    (rust, oracle, none)
}

/// Check the census against the ratchet constants. `Err` is the actionable
/// message both faces surface verbatim.
pub fn check_code_census(rows: &[Verdict]) -> Result<(), String> {
    let (rust, oracle, none) = code_census(rows);
    if rust != EXPECTED_FILES_WITH_RUST_CODE
        || oracle != EXPECTED_FILES_WITH_ORACLE_CODE
        || none.len() != EXPECTED_FILES_WITHOUT_DECLARED_CODE
    {
        return Err(format!(
            "reject corpus code-declaration census changed: expected EXACTLY \
             {EXPECTED_FILES_WITH_RUST_CODE} file(s) with a rust-specific `-- rust: reject` \
             code, {EXPECTED_FILES_WITH_ORACLE_CODE} deriving the code from `-- oracle: \
             reject`, and {EXPECTED_FILES_WITHOUT_DECLARED_CODE} declaring none; found \
             {rust} / {oracle} / {}. Update ty::reject_corpus::EXPECTED_FILES_WITH_RUST_CODE \
             / EXPECTED_FILES_WITH_ORACLE_CODE / EXPECTED_FILES_WITHOUT_DECLARED_CODE in the \
             SAME commit. Undeclared: {}",
            none.len(),
            none.join(", ")
        ));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn declared_codes_parses_single_multi_and_none() {
        assert_eq!(
            declared_codes("-- oracle: reject [E2001]"),
            (vec!["E2001".to_string()], CodeSource::Oracle)
        );
        assert_eq!(
            declared_codes("-- oracle: reject [E2001] + [E2007] arity gate"),
            (
                vec!["E2001".to_string(), "E2007".to_string()],
                CodeSource::Oracle
            )
        );
        assert_eq!(
            declared_codes("-- oracle: reject — exit 1, `[E0001] PARSE ERROR"),
            (vec!["E0001".to_string()], CodeSource::Oracle)
        );
        assert_eq!(
            declared_codes("-- oracle: reject. no code here").1,
            CodeSource::None
        );
        // A code BEFORE the marker is prose, not a declaration.
        assert_eq!(
            declared_codes("-- [E2001] was the old code; oracle: reject").1,
            CodeSource::None
        );
        // Not a diagnostic code shape.
        assert_eq!(
            declared_codes("-- oracle: reject [nope] [E] [E12a]").1,
            CodeSource::None
        );
    }

    #[test]
    fn rust_header_wins_over_oracle_header() {
        // The whole point of the precedence rule: `-- oracle:` records what the
        // HASKELL ORACLE does; this gate observes what RUST emits. When they
        // differ, the rust line is the expectation and the oracle line STAYS.
        let src = "-- rust: reject [E2007] dedicated arity diagnostic\n\
                   -- oracle: reject [E2001] generic unify clash\n";
        assert_eq!(
            declared_codes(src),
            (vec!["E2007".to_string()], CodeSource::Rust)
        );
        // No rust line → the oracle expectation is used as the rust one.
        assert_eq!(
            declared_codes("-- oracle: reject [E2001] generic unify clash\n"),
            (vec!["E2001".to_string()], CodeSource::Oracle)
        );
        // A rust line without a code does NOT suppress the oracle fallback.
        assert_eq!(
            declared_codes("-- rust: reject at check time\n-- oracle: reject [E2001]\n"),
            (vec!["E2001".to_string()], CodeSource::Oracle)
        );
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
            code_source: CodeSource::Oracle,
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
