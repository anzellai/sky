//! `xtask denominators` — THE denominator contract
//! (docs/ci-test-architecture-v2.md §5).
//!
//! # The problem this exists to solve
//!
//! "100 % coverage" is a claim about a fraction, and a fraction is meaningless
//! if the denominator can shrink without anyone noticing. Before this gate,
//! every number the design quoted was a hand-count in a markdown file, and five
//! separate code paths could shrink the real denominator while exiting 0. The
//! two consequences were both observed in-tree: the design documents disagreed
//! with each other (1,744 vs 1,762 stdlib entries; 72 vs 124 syntax kinds) and
//! *both* disagreed with the compiler.
//!
//! # The contract
//!
//! 1. **ONE script produces EVERY denominator.** No document, ledger or verdict
//!    may quote a number this gate did not produce. Run it; if the number is not
//!    in its output, it is not a denominator, it is an opinion.
//! 2. **The output is checked in** at `docs/coverage/denominators.json`.
//! 3. **A DECREASE is a gate failure** unless it is accounted for in
//!    `docs/coverage/removals.toml`. An INCREASE is always allowed (the surface
//!    grew; go cover it) and updates the checked-in file.
//! 4. **Filtered and unfiltered stdlib counts are reported SEPARATELY** and
//!    never averaged, because the 6 `exposing (..)` modules contribute "every
//!    top-level declaration" while the other 81 contribute a curated public API.
//!    Averaging two different kinds of number produces a third kind: fiction.
//!
//! # Where the numbers come from
//!
//! * **stdlib** — from `api/symbols.json`, produced by calling
//!   `project::render_doc_site_export` (the EXACT function `sky doc --export`
//!   calls, doc.rs's `render_doc_site_mode`) into a temp dir, then counting the
//!   emitted manifest. Reusing the code path rather than shelling out to a
//!   prebuilt `sky` binary means the gate cannot silently measure a stale
//!   binary, and needs no build ordering. As a cross-check, the same counts are
//!   recomputed per-module through `project::stdlib_denominators` (which also
//!   yields the unfiltered numbers); the two must agree exactly or the gate
//!   fails. That disagreement would mean the manifest writer and the symbol
//!   extractor had drifted apart.
//! * **language** — `syntax::SyntaxKind::KINDS` (macro-generated, therefore
//!   total) classified by `syntax::kind_class`. The gate re-runs
//!   `kind_class::assert_total()` before reporting: a language denominator
//!   computed from an incomplete classification table is already a shrunk one.
//! * **tests** — `Test.test` leaves (CASES) and `Test.{equal, notEqual, ok, err,
//!   expectErrorKind, isTrue, isFalse, fail}` calls (ASSERTIONS) over
//!   `tests/conformance/tests/` and `examples/*/tests/`. `Test.pass` is counted
//!   separately as VACUOUS and reported both ways, because the design doc's
//!   prose ("`pass` is not an assertion") and its table (which included it in
//!   the 776) disagreed.

use serde_json::{json, Value};
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

/// The assertion functions of `Sky.Test`. `pass` is deliberately NOT here.
const ASSERTION_FNS: &[&str] =
    &["equal", "notEqual", "ok", "err", "expectErrorKind", "isTrue", "isFalse", "fail"];

pub fn run(args: &[String], repo_root: &Path) -> i32 {
    let check_only = args.iter().any(|a| a == "--check");

    let computed = match compute(repo_root) {
        Ok(v) => v,
        Err(e) => {
            eprintln!("xtask denominators: FAILED to compute denominators\n{e}");
            return 1;
        }
    };

    let out_path = repo_root.join("docs/coverage/denominators.json");
    let removals_path = repo_root.join("docs/coverage/removals.toml");

    let removals = match parse_removals(&removals_path) {
        Ok(n) => n,
        Err(e) => {
            eprintln!("xtask denominators: {} is malformed\n{e}", removals_path.display());
            return 1;
        }
    };

    print_report(&computed, removals);

    let baseline: Option<Value> = std::fs::read_to_string(&out_path)
        .ok()
        .and_then(|s| serde_json::from_str(&s).ok());

    let mut current = computed.clone();
    current["removals_accounted"] = json!(removals);

    if let Some(base) = &baseline {
        if let Err(msg) = ratchet(base, &current, removals) {
            eprintln!("\n{msg}");
            eprintln!(
                "\nxtask denominators: FAIL — the denominator shrank without an accounting entry."
            );
            return 1;
        }
        if check_only && !same_metrics(base, &current) {
            eprintln!(
                "\nxtask denominators --check: FAIL — {} is STALE.\n\
                 The denominators grew (or the module lists changed) and the checked-in file was \n\
                 not updated. Run `xtask denominators` and commit the result.\n{}",
                out_path.display(),
                metric_diff(base, &current)
            );
            return 1;
        }
    } else if check_only {
        eprintln!(
            "\nxtask denominators --check: FAIL — {} does not exist. Run `xtask denominators`.",
            out_path.display()
        );
        return 1;
    }

    if check_only {
        println!("\nxtask denominators --check: PASS — checked-in denominators are current.");
        return 0;
    }

    if let Some(parent) = out_path.parent() {
        if let Err(e) = std::fs::create_dir_all(parent) {
            eprintln!("xtask denominators: cannot create {}: {e}", parent.display());
            return 1;
        }
    }
    let text = format!("{}\n", serde_json::to_string_pretty(&current).unwrap());
    if let Err(e) = std::fs::write(&out_path, text) {
        eprintln!("xtask denominators: cannot write {}: {e}", out_path.display());
        return 1;
    }
    println!("\nxtask denominators: wrote {}", out_path.display());
    0
}

// ---------------------------------------------------------------- computation

fn compute(repo_root: &Path) -> Result<Value, String> {
    // The language denominator is only meaningful if the classification table is
    // total — an unclassified kind means the real denominator is larger than the
    // one we are about to print.
    syntax::kind_class::assert_total()?;
    let kinds = syntax::kind_class::kind_count();
    let constructs = syntax::kind_class::construct_kinds().len();

    // --- stdlib, from the real `sky doc --export` code path -----------------
    let tmp = std::env::temp_dir().join(format!("sky-denominators-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&tmp);
    let proj = tmp.join("project");
    let out = tmp.join("site");
    std::fs::create_dir_all(&proj).map_err(|e| format!("temp dir: {e}"))?;
    std::fs::create_dir_all(&out).map_err(|e| format!("temp dir: {e}"))?;
    // `proj` is an empty directory: it has no `src/`, so the manifest contains
    // the stdlib and nothing else, exactly like `sky doc --export` from a
    // project-less cwd.
    let export = project::render_doc_site_export(repo_root, &proj, &out)
        .map_err(|e| format!("sky doc --export code path FAILED: {e}"));
    let manifest = export.and_then(|()| {
        std::fs::read_to_string(out.join("api").join("symbols.json"))
            .map_err(|e| format!("no api/symbols.json: {e}"))
    });
    let _ = std::fs::remove_dir_all(&tmp);
    let manifest: Value =
        serde_json::from_str(&manifest?).map_err(|e| format!("symbols.json is not JSON: {e}"))?;
    let entries = manifest["entries"].as_array().ok_or("symbols.json has no `entries` array")?;

    let mut m_modules = std::collections::BTreeSet::new();
    let mut m_types = 0usize;
    for e in entries {
        m_modules.insert(e["module"].as_str().unwrap_or_default().to_string());
        if e["sig"].as_str().unwrap_or_default().starts_with("type ") {
            m_types += 1;
        }
    }
    let m_entries = entries.len();
    let m_values = m_entries - m_types;

    // --- the same numbers, recomputed per module (and the unfiltered ones) ---
    let per_module = project::stdlib_denominators(repo_root)
        .map_err(|e| format!("stdlib_denominators FAILED: {e}"))?;

    let sum = |f: fn(&project::ModuleDenominator) -> usize| -> usize {
        per_module.iter().map(f).sum()
    };
    let (f_entries, f_values, f_types) =
        (sum(|m| m.filtered_entries), sum(|m| m.filtered_values), sum(|m| m.filtered_types));

    // Cross-check: the manifest writer and the symbol extractor must agree.
    if (m_entries, m_values, m_types, m_modules.len())
        != (f_entries, f_values, f_types, per_module.len())
    {
        return Err(format!(
            "DRIFT between api/symbols.json and stdlib_denominators:\n  \
             symbols.json: {m_entries} entries / {m_values} values / {m_types} types / {} modules\n  \
             per-module:   {f_entries} entries / {f_values} values / {f_types} types / {} modules",
            m_modules.len(),
            per_module.len()
        ));
    }

    let (u_entries, u_values, u_types) =
        (sum(|m| m.unfiltered_entries), sum(|m| m.unfiltered_values), sum(|m| m.unfiltered_types));

    let all_mods: Vec<&project::ModuleDenominator> =
        per_module.iter().filter(|m| m.exposes_all).collect();
    let all_names: Vec<String> = all_mods.iter().map(|m| m.module.clone()).collect();
    let all_entries: usize = all_mods.iter().map(|m| m.filtered_entries).sum();
    let explicit_mods = per_module.len() - all_mods.len();
    let explicit_entries = f_entries - all_entries;
    let explicit_unfiltered: usize =
        per_module.iter().filter(|m| !m.exposes_all).map(|m| m.unfiltered_entries).sum();

    // --- tests ---------------------------------------------------------------
    let conformance = count_tests(&[repo_root.join("tests/conformance/tests")]);
    let example_suites: Vec<PathBuf> = example_test_dirs(repo_root);
    let examples = count_tests(&example_suites);

    let mut examples_json = test_json(&examples);
    examples_json["suites"] = json!(example_suites.len());

    Ok(json!({
        "_README": "GENERATED by `xtask denominators` — do not hand-edit. \
                    No document, ledger or verdict may quote a denominator this file \
                    does not contain. A DECREASE in any number here fails the gate unless \
                    docs/coverage/removals.toml accounts for it; an INCREASE is always \
                    allowed and rewrites this file. See docs/ci-test-architecture-v2.md §5.",
        "stdlib": {
            "_source": "api/symbols.json, via project::render_doc_site_export \
                        (the `sky doc --export` code path)",
            "modules": per_module.len(),
            "entries": f_entries,
            "values": f_values,
            "types": f_types,
            "unfiltered": {
                "_note": "every top-level declaration, ignoring `exposing` lists. \
                          NEVER average these with the filtered counts — they answer a \
                          different question.",
                "entries": u_entries,
                "values": u_values,
                "types": u_types,
                "hidden_by_exposing": u_entries - f_entries
            },
            "exposing_all": {
                "_note": "modules whose header is `exposing (..)`: no public-API curation \
                          exists, so their filtered count IS their unfiltered count — every \
                          top-level declaration, including helpers never intended as API. \
                          Migrating these to explicit `exposing` lists is a tracked task.",
                "modules": all_mods.len(),
                "module_names": all_names,
                "entries": all_entries
            },
            "explicit_exposing": {
                "modules": explicit_mods,
                "entries": explicit_entries,
                "unfiltered_entries": explicit_unfiltered
            }
        },
        "language": {
            "_source": "syntax::SyntaxKind::KINDS classified by syntax::kind_class",
            "syntax_kinds": kinds,
            "constructs": constructs,
            "non_constructs": kinds - constructs
        },
        "tests": {
            "_note": "a CASE is one `Test.test` leaf. An ASSERTION is one call to \
                      Test.{equal,notEqual,ok,err,expectErrorKind,isTrue,isFalse,fail}. \
                      `Test.pass` is VACUOUS and excluded from `assertions`; \
                      `assertion_calls_incl_pass` is the looser reading, reported so the \
                      two are never confused.",
            "conformance": test_json(&conformance),
            "examples": examples_json
        }
    }))
}

#[derive(Default, Clone)]
struct TestCounts {
    cases: usize,
    vacuous_pass: usize,
    by_fn: BTreeMap<String, usize>,
}

impl TestCounts {
    fn assertions(&self) -> usize {
        self.by_fn.values().sum()
    }
}

fn test_json(t: &TestCounts) -> Value {
    json!({
        "cases": t.cases,
        "assertions": t.assertions(),
        "vacuous_pass": t.vacuous_pass,
        "assertion_calls_incl_pass": t.assertions() + t.vacuous_pass,
        "by_assertion_fn": t.by_fn.iter().map(|(k, v)| (k.clone(), json!(v))).collect::<serde_json::Map<_, _>>()
    })
}

fn example_test_dirs(repo_root: &Path) -> Vec<PathBuf> {
    let mut out = Vec::new();
    if let Ok(rd) = std::fs::read_dir(repo_root.join("examples")) {
        let mut dirs: Vec<PathBuf> = rd
            .filter_map(|e| e.ok())
            .map(|e| e.path().join("tests"))
            .filter(|p| p.is_dir())
            .collect();
        dirs.sort();
        out.append(&mut dirs);
    }
    out
}

fn count_tests(roots: &[PathBuf]) -> TestCounts {
    let mut t = TestCounts::default();
    for fname in ASSERTION_FNS {
        t.by_fn.insert((*fname).to_string(), 0);
    }
    let mut files = Vec::new();
    for r in roots {
        collect_sky(r, &mut files);
    }
    for f in files {
        let Ok(src) = std::fs::read_to_string(&f) else {
            continue;
        };
        t.cases += count_calls(&src, "test");
        t.vacuous_pass += count_calls(&src, "pass");
        for fname in ASSERTION_FNS {
            *t.by_fn.get_mut(*fname).unwrap() += count_calls(&src, fname);
        }
    }
    t
}

/// Count `Test.<name>` call sites, requiring a non-identifier char after the
/// name so `Test.test` does not also match a hypothetical `Test.testFoo`.
fn count_calls(src: &str, name: &str) -> usize {
    let needle = format!("Test.{name}");
    let bytes = src.as_bytes();
    let mut n = 0;
    let mut from = 0;
    while let Some(i) = src[from..].find(&needle) {
        let at = from + i;
        let after = at + needle.len();
        let boundary = bytes
            .get(after)
            .map(|c| !(c.is_ascii_alphanumeric() || *c == b'_'))
            .unwrap_or(true);
        if boundary {
            n += 1;
        }
        from = after;
    }
    n
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok()).map(|e| e.path()).collect();
    entries.sort();
    for p in entries {
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().map(|e| e == "sky").unwrap_or(false) {
            out.push(p);
        }
    }
}

// -------------------------------------------------------------------- ratchet

/// Flatten every NUMBER in the document to a dotted key. Keys starting with `_`
/// are documentation prose and are skipped; `removals_accounted` is bookkeeping,
/// not a denominator.
fn metrics(v: &Value) -> BTreeMap<String, i64> {
    fn walk(prefix: &str, v: &Value, out: &mut BTreeMap<String, i64>) {
        match v {
            Value::Number(n) => {
                if let Some(i) = n.as_i64() {
                    out.insert(prefix.to_string(), i);
                }
            }
            Value::Object(map) => {
                for (k, child) in map {
                    if k.starts_with('_') || (prefix.is_empty() && k == "removals_accounted") {
                        continue;
                    }
                    let key =
                        if prefix.is_empty() { k.clone() } else { format!("{prefix}.{k}") };
                    walk(&key, child, out);
                }
            }
            _ => {}
        }
    }
    let mut out = BTreeMap::new();
    walk("", v, &mut out);
    out
}

fn same_metrics(a: &Value, b: &Value) -> bool {
    metrics(a) == metrics(b) && a["stdlib"]["exposing_all"]["module_names"]
        == b["stdlib"]["exposing_all"]["module_names"]
}

fn metric_diff(base: &Value, cur: &Value) -> String {
    let (mb, mc) = (metrics(base), metrics(cur));
    let mut lines = Vec::new();
    for (k, c) in &mc {
        match mb.get(k) {
            Some(b) if b != c => lines.push(format!("  {k}: {b} -> {c}")),
            None => lines.push(format!("  {k}: (new) -> {c}")),
            _ => {}
        }
    }
    for k in mb.keys() {
        if !mc.contains_key(k) {
            lines.push(format!("  {k}: (metric disappeared)"));
        }
    }
    if lines.is_empty() {
        "  (no numeric change; a module list changed)".to_string()
    } else {
        lines.join("\n")
    }
}

/// THE RATCHET. Every metric may grow freely. A metric may only SHRINK if
/// `removals.toml` gained at least as many accounted entries as the largest
/// single-metric decrease — removing N symbols means writing down N removals,
/// each with a reason, an owner and a commit.
/// Metrics the FALL-IS-A-SHRINK rule must NOT be applied to, and why.
///
/// Both exclusions were forced by the rule producing a wrong verdict on a real
/// change, and both would have trained people to write junk accounting entries —
/// which is how a ratchet stops being read.
///
/// * **`vacuous_pass`** counts unconditional `Test.pass` calls. v2 §4.1 lists
///   the 18 of them as a DEFECT: a test case that cannot fail. So a fall is the
///   improvement the design asks for, and charging a `[[removal]]` entry for it
///   is the ratchet paying people to leave vacuous tests in place. It is instead
///   ratcheted the other way — see [`vacuity_ratchet`].
/// * **`by_assertion_fn.*`** is the per-function breakdown (`equal`, `fail`,
///   `isTrue`, …). It is diagnostic, not a denominator. Rewriting one
///   `Test.fail` as a `Test.equal` moves two of these while the total
///   `assertions` RISES; charging that as a shrink makes every honest refactor
///   owe paperwork. The aggregate `assertions` and `cases` counts are still
///   fully ratcheted, which is where the real claim lives.
fn is_diagnostic_metric(key: &str) -> bool {
    key.ends_with(".vacuous_pass") || key.contains(".by_assertion_fn.")
}

/// FAIL-ON-INCREASE for vacuity — the inverse ratchet.
///
/// A NEW unconditional `Test.pass` is a new test case that cannot fail, which is
/// the exact class the per-item falsifiability model exists to kill. Falling is
/// always fine and rewrites the baseline.
fn vacuity_ratchet(base: &Value, cur: &Value) -> Result<(), String> {
    let (mb, mc) = (metrics(base), metrics(cur));
    let mut risen: Vec<String> = Vec::new();
    for (k, c) in &mc {
        if !k.ends_with(".vacuous_pass") {
            continue;
        }
        let b = mb.get(k).copied().unwrap_or(0);
        if *c > b {
            risen.push(format!("  {k}: {b} -> {c}  (+{})", c - b));
        }
    }
    if risen.is_empty() {
        return Ok(());
    }
    Err(format!(
        "VACUITY ROSE — {} metric(s) gained unconditional `Test.pass` calls:\n{}\n\n\
         `Test.pass` is not an assertion; a case built on it cannot fail, and it is \
         counted as VACUOUS everywhere else in this design. Write the assertion the \
         case was meant to make, or delete the case. Do not add a removals entry — \
         this ratchet runs the other way and no accounting will satisfy it.",
        risen.len(),
        risen.join("\n")
    ))
}

fn ratchet(base: &Value, cur: &Value, removals_now: usize) -> Result<(), String> {
    vacuity_ratchet(base, cur)?;

    let (mb, mc) = (metrics(base), metrics(cur));
    let mut decreases: Vec<(String, i64, i64)> = Vec::new();
    for (k, b) in &mb {
        if is_diagnostic_metric(k) {
            continue;
        }
        if let Some(c) = mc.get(k) {
            if c < b {
                decreases.push((k.clone(), *b, *c));
            }
        } else {
            decreases.push((k.clone(), *b, 0));
        }
    }
    if decreases.is_empty() {
        return Ok(());
    }
    let removals_before = base["removals_accounted"].as_i64().unwrap_or(0).max(0) as usize;
    let newly_accounted = removals_now.saturating_sub(removals_before);
    let worst = decreases.iter().map(|(_, b, c)| b - c).max().unwrap_or(0) as usize;
    if newly_accounted >= worst {
        return Ok(());
    }
    let listed: Vec<String> =
        decreases.iter().map(|(k, b, c)| format!("  {k}: {b} -> {c}  (-{})", b - c)).collect();
    Err(format!(
        "DENOMINATOR SHRANK — {} metric(s) decreased:\n{}\n\n\
         Largest single-metric decrease: {worst}. Newly accounted removals: {newly_accounted} \
         (removals.toml has {removals_now}; the baseline recorded {removals_before}).\n\
         Add {} more entry(ies) to docs/coverage/removals.toml — each needs symbol, reason, \
         owner and commit — or restore the surface. A denominator that can fall silently \
         makes every coverage percentage above it unfalsifiable.",
        decreases.len(),
        listed.join("\n"),
        worst - newly_accounted
    ))
}

/// Count the `[[removal]]` entries, validating that each carries all four
/// required fields. A removals file whose entries are blank would let the
/// ratchet be defeated by adding empty stanzas.
fn parse_removals(path: &Path) -> Result<usize, String> {
    let Ok(src) = std::fs::read_to_string(path) else {
        return Ok(0);
    };
    let required = ["symbol", "reason", "owner", "commit"];
    let mut count = 0usize;
    let mut fields: Vec<String> = Vec::new();
    let mut problems: Vec<String> = Vec::new();
    let close = |fields: &[String], idx: usize, problems: &mut Vec<String>| {
        for r in required {
            if !fields.iter().any(|f| f == r) {
                problems.push(format!("[[removal]] #{idx} is missing `{r}`"));
            }
        }
    };
    for line in src.lines() {
        let line = line.trim();
        if line.starts_with('#') || line.is_empty() {
            continue;
        }
        if line.starts_with("[[removal]]") {
            if count > 0 {
                close(&fields, count, &mut problems);
            }
            count += 1;
            fields.clear();
            continue;
        }
        if let Some((k, v)) = line.split_once('=') {
            if count > 0 && !v.trim().trim_matches('"').trim().is_empty() {
                fields.push(k.trim().to_string());
            }
        }
    }
    if count > 0 {
        close(&fields, count, &mut problems);
    }
    if problems.is_empty() {
        Ok(count)
    } else {
        Err(problems.join("\n"))
    }
}

// --------------------------------------------------------------------- report

fn print_report(v: &Value, removals: usize) {
    let g = |p: &[&str]| -> i64 {
        let mut cur = v;
        for k in p {
            cur = &cur[*k];
        }
        cur.as_i64().unwrap_or(-1)
    };
    println!("xtask denominators — docs/ci-test-architecture-v2.md §5");
    println!("======================================================");
    println!("\nSTDLIB API  (source: api/symbols.json via the `sky doc --export` code path)");
    println!("  modules ............................. {}", g(&["stdlib", "modules"]));
    println!("  entries ............................. {}", g(&["stdlib", "entries"]));
    println!("    values ............................ {}", g(&["stdlib", "values"]));
    println!("    types ............................. {}", g(&["stdlib", "types"]));
    println!("\n  FILTERED vs UNFILTERED (reported separately; never averaged)");
    println!(
        "  unfiltered entries (all top-level decls)  {}",
        g(&["stdlib", "unfiltered", "entries"])
    );
    println!(
        "  hidden by `exposing` lists ............... {}",
        g(&["stdlib", "unfiltered", "hidden_by_exposing"])
    );
    let all_e = g(&["stdlib", "exposing_all", "entries"]);
    let total_e = g(&["stdlib", "entries"]);
    println!("\n  `exposing (..)` modules ({} of {}) — UNFILTERED BY CONSTRUCTION:",
        g(&["stdlib", "exposing_all", "modules"]), g(&["stdlib", "modules"]));
    if let Some(names) = v["stdlib"]["exposing_all"]["module_names"].as_array() {
        for n in names {
            println!("      {}", n.as_str().unwrap_or("?"));
        }
    }
    println!(
        "    their entries ..................... {all_e}  ({:.1}% of the {total_e} total)",
        (all_e as f64) * 100.0 / (total_e.max(1) as f64)
    );
    println!(
        "  explicit-`exposing` modules ({}) entries  {}",
        g(&["stdlib", "explicit_exposing", "modules"]),
        g(&["stdlib", "explicit_exposing", "entries"])
    );
    println!("\nLANGUAGE  (source: syntax::SyntaxKind::KINDS + syntax::kind_class)");
    println!("  syntax kinds ........................ {}", g(&["language", "syntax_kinds"]));
    println!("  classified CONSTRUCT ................ {}", g(&["language", "constructs"]));
    println!("  classified NON-construct ............ {}", g(&["language", "non_constructs"]));
    for (label, key) in [("CONFORMANCE", "conformance"), ("EXAMPLE SUITES", "examples")] {
        println!("\n{label}  (tests)");
        if key == "examples" {
            println!("  suites .............................. {}", g(&["tests", key, "suites"]));
        }
        println!("  cases (Test.test) ................... {}", g(&["tests", key, "cases"]));
        println!("  assertions .......................... {}", g(&["tests", key, "assertions"]));
        println!(
            "  vacuous Test.pass ................... {}",
            g(&["tests", key, "vacuous_pass"])
        );
        if let Some(map) = v["tests"][key]["by_assertion_fn"].as_object() {
            let mut pairs: Vec<(&String, i64)> =
                map.iter().map(|(k, v)| (k, v.as_i64().unwrap_or(0))).collect();
            pairs.sort_by(|a, b| b.1.cmp(&a.1));
            let shown: Vec<String> =
                pairs.iter().filter(|(_, n)| *n > 0).map(|(k, n)| format!("{k} {n}")).collect();
            println!("    {}", shown.join(" · "));
        }
    }
    println!("\nremovals.toml accounted entries ....... {removals}");
}

#[cfg(test)]
mod tests {
    use super::*;

    fn doc(entries: i64, removals: i64) -> Value {
        json!({
            "_README": "prose is not a metric",
            "removals_accounted": removals,
            "stdlib": { "_source": "prose", "entries": entries, "modules": 87 }
        })
    }

    #[test]
    fn prose_and_bookkeeping_are_not_metrics() {
        let m = metrics(&doc(10, 3));
        assert_eq!(m.keys().collect::<Vec<_>>(), vec!["stdlib.entries", "stdlib.modules"]);
    }

    #[test]
    fn increase_is_always_allowed() {
        assert!(ratchet(&doc(1746, 0), &doc(1800, 0), 0).is_ok());
    }

    #[test]
    fn unchanged_is_allowed() {
        assert!(ratchet(&doc(1746, 0), &doc(1746, 0), 0).is_ok());
    }

    /// THE RATCHET'S RED. A decrease with no new removals entry must fail.
    #[test]
    fn decrease_without_removal_entry_fails() {
        let err = ratchet(&doc(1746, 0), &doc(1745, 0), 0).unwrap_err();
        assert!(err.contains("DENOMINATOR SHRANK"), "{err}");
        assert!(err.contains("stdlib.entries: 1746 -> 1745"), "{err}");
    }

    #[test]
    fn decrease_is_allowed_once_accounted_one_for_one() {
        // Three symbols removed needs three NEW removals entries, not one.
        assert!(ratchet(&doc(1746, 0), &doc(1743, 0), 1).is_err());
        assert!(ratchet(&doc(1746, 0), &doc(1743, 0), 3).is_ok());
        // Entries already spent on the previous decrease do not pay again.
        assert!(ratchet(&doc(1746, 3), &doc(1745, 3), 3).is_err());
        assert!(ratchet(&doc(1746, 3), &doc(1745, 3), 4).is_ok());
    }

    fn tests_doc(vacuous: i64, fail_calls: i64, assertions: i64) -> Value {
        json!({
            "removals_accounted": 0,
            "tests": { "examples": {
                "vacuous_pass": vacuous,
                "assertions": assertions,
                "by_assertion_fn": { "fail": fail_calls, "equal": 100 }
            }}
        })
    }

    /// REMOVING a vacuous `Test.pass` must not owe paperwork.
    ///
    /// This is the case that exposed the defect: converting two unconditional
    /// `Test.pass` calls into real assertions moved `vacuous_pass` 11 -> 9 and
    /// the ratchet demanded two `[[removal]]` entries for it — charging a fee
    /// for doing exactly what v2 §4.1 asks.
    #[test]
    fn falling_vacuity_is_an_improvement_not_a_shrink() {
        assert!(ratchet(&tests_doc(11, 32, 84), &tests_doc(9, 32, 148), 0).is_ok());
    }

    /// ...and the inverse ratchet is real: NEW vacuity fails, and no amount of
    /// accounting buys it off.
    #[test]
    fn rising_vacuity_fails_and_cannot_be_accounted_away() {
        let err = ratchet(&tests_doc(9, 32, 148), &tests_doc(10, 32, 148), 0).unwrap_err();
        assert!(err.contains("VACUITY ROSE"), "{err}");
        assert!(ratchet(&tests_doc(9, 32, 148), &tests_doc(10, 32, 148), 99).is_err());
    }

    /// The per-function breakdown is diagnostic. Rewriting a `Test.fail` as a
    /// `Test.equal` moves it while the aggregate RISES; that is a refactor, not
    /// a coverage removal.
    #[test]
    fn per_assertion_fn_breakdown_is_not_ratcheted() {
        assert!(ratchet(&tests_doc(9, 32, 148), &tests_doc(9, 31, 149), 0).is_ok());
    }

    /// But the AGGREGATE assertion count still is — that is where the claim lives.
    #[test]
    fn the_aggregate_assertion_count_is_still_ratcheted() {
        let err = ratchet(&tests_doc(9, 32, 148), &tests_doc(9, 32, 147), 0).unwrap_err();
        assert!(err.contains("DENOMINATOR SHRANK"), "{err}");
        assert!(err.contains("tests.examples.assertions: 148 -> 147"), "{err}");
    }

    /// A metric that vanishes entirely is a decrease to zero, not a free pass.
    #[test]
    fn disappearing_metric_is_a_decrease() {
        let base = doc(1746, 0);
        let cur = json!({ "removals_accounted": 0, "stdlib": { "modules": 87 } });
        assert!(ratchet(&base, &cur, 0).is_err());
    }

    #[test]
    fn removals_stanza_must_carry_all_four_fields() {
        let dir = std::env::temp_dir().join(format!("sky-denom-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let p = dir.join("removals.toml");

        std::fs::write(&p, "# comment only\n").unwrap();
        assert_eq!(parse_removals(&p).unwrap(), 0);

        std::fs::write(
            &p,
            "[[removal]]\nsymbol = \"Std.A.b\"\nreason = \"r\"\nowner = \"o\"\ncommit = \"c\"\n",
        )
        .unwrap();
        assert_eq!(parse_removals(&p).unwrap(), 1);

        // An empty stanza would otherwise buy a free decrease.
        std::fs::write(&p, "[[removal]]\nsymbol = \"Std.A.b\"\n").unwrap();
        let err = parse_removals(&p).unwrap_err();
        assert!(err.contains("missing `reason`"), "{err}");

        // A blank value is the same defeat by another spelling.
        std::fs::write(
            &p,
            "[[removal]]\nsymbol = \"Std.A.b\"\nreason = \"\"\nowner = \"o\"\ncommit = \"c\"\n",
        )
        .unwrap();
        assert!(parse_removals(&p).unwrap_err().contains("missing `reason`"));

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn call_counting_respects_identifier_boundaries() {
        assert_eq!(count_calls("Test.test x\nTest.testHelper y\n", "test"), 1);
        assert_eq!(count_calls("Test.equal a b |> Test.equal c d", "equal"), 2);
        assert_eq!(count_calls("Test.pass", "pass"), 1);
    }

    /// The classification table must be total before any language denominator
    /// is reported — the gate re-checks it rather than trusting the syntax
    /// crate's own test to have run.
    #[test]
    fn language_denominator_requires_a_total_classification() {
        assert!(syntax::kind_class::assert_total().is_ok());
    }
}
