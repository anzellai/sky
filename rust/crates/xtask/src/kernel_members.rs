//! `kernel-members` — the drift gate that keeps the kernel MEMBER tables a
//! faithful, complete image of the runtime, so the resolve-time reject of an
//! unknown qualified kernel member (`List.sum`) can NEVER false-reject a real
//! one.
//!
//! # Why this exists
//!
//! `resolve.rs::resolve_qual_var` rejects a qualified `M.f` whose `f` is not in
//! `hir::kernel::KERNEL_FUNCTIONS[M]`. For that reject to have ZERO false
//! positives, `KERNEL_FUNCTIONS[M]` MUST be a **superset** of every real
//! callable member of `M`. A member `f` is REAL iff the exact codegen
//! indirection `lower::kernel::kernel_go_name(M, f)` names a symbol the Go
//! runtime actually exports (`project::abi_guard::runtime_exports`). This gate
//! computes `RealMembers(M)` from that definition and asserts the tables match
//! it — so a runtime symbol that becomes user-facing forces a table update
//! BEFORE the reject can turn it red.
//!
//! # What it asserts, per kernel pseudo-module `M`
//!
//! 1. If `KERNEL_FUNCTIONS` has an entry for `M`:
//!    `set(KERNEL_FUNCTIONS[M]) == RealMembers(M)` — exact. A phantom (a name
//!    with no runtime symbol, e.g. `List.parallelMap`) or a missing real member
//!    (e.g. `List.sortWith`) fails.
//! 2. `PRELUDE_QUALIFIERS[M] ⊆ RealMembers(M)` — an auto-available qualifier
//!    that names a phantom fails.
//! 3. For a `.sky`-backed `M`: `exposing(M) ⊆ RealMembers(M)`, and every
//!    `Ffi.kernel "Sym"` binding in the `.sky` names a `Sym ∈ runtime_exports`.
//! 4. COMPLETENESS (the zero-false-positive safety): for every module with a
//!    `KERNEL_FUNCTIONS` entry, scan the runtime for `rt.<M>_<base>` symbols
//!    whose `<base>` is a plausible USER member (not a typed-variant or a
//!    known internal backing symbol) and FAIL on any such base not in
//!    `RealMembers(M)` — that is a runtime-only member the reject would
//!    false-reject, and it must be classified (added as a member, or listed as
//!    internal) rather than left to surface as a false red at check time.

use hir::{KERNEL_FUNCTIONS, KERNEL_MODULES, PRELUDE_QUALIFIERS};
use std::collections::{BTreeMap, BTreeSet};
use std::path::Path;

/// Typed-variant / erased-shape suffixes appended to a base runtime kernel name.
/// Users never call these directly (they call the base name, and the lowerer
/// picks the typed variant), so a runtime `rt.<M>_<base><suffix>` whose stripped
/// `<base>` is already a member is INTERNAL, not a distinct member. Longest
/// first so the strip is greedy.
const TYPED_SUFFIXES: &[&str] = &[
    "ElemFirstT",
    // Dict typed-key dispatch variants (`keysIntKey`, `foldlBoolKey`, …): the
    // lowerer picks these by the key's Go shape; a user calls the base name.
    "BoolKey",
    "IntKey",
    "CharKey",
    "FloatKey",
    "StringKey",
    "AnyTA",
    "AnyT",
    "TA",
    "Any",
    "T",
];

/// Runtime `rt.<M>_<base>` symbols that are NOT user-facing members even though
/// their `<base>` survives the typed-variant strip: operator backings, internal
/// helpers, and representation shims. Keyed by pseudo-module. A base listed here
/// is excluded from the completeness scan (it is deliberately not a member and
/// the reject SHOULD refuse `M.<base>`). Anything NOT listed here and NOT a
/// member fails the scan, forcing a conscious classification.
fn internal_runtime_bases(module: &str) -> &'static [&'static str] {
    match module {
        // Arithmetic/comparison operator backings — users write `+`, `-`, `==`,
        // never `Basics.add`. `toString`/`compare`/etc. that ARE members stay in
        // the tables; only the operator kernels are internal here.
        "Basics" => &[
            "add", "sub", "mul", "fdiv", "idiv", "pow", "eq", "ord", "neq", "lt", "gt", "lte",
            "gte", "and", "or", "append", "cons", "apL", "apR", "composeL", "composeR",
        ],
        // `Dict_map2T` is the typed variant of a `map2` the surface never
        // exposes (no bare `rt.Dict_map2`); it is internal dispatch.
        "Dict" => &["map2"],
        // `rt.Process_loadEnv` is the backing for `System.loadEnv`; `Process`'s
        // only member is `run`. A bare `Process.loadEnv` is not intended.
        "Process" => &["loadEnv"],
        _ => &[],
    }
}

/// The runtime symbol for `(M, f)` with the `rt.` prefix stripped, matching the
/// bare names in `runtime_exports`.
fn rt_symbol(module: &str, func: &str) -> String {
    let full = lower::kernel::kernel_go_name(module, func);
    full.strip_prefix("rt.").unwrap_or(&full).to_string()
}

/// Sky import path (e.g. `Sky.Core.List`) → `.sky` file under `sky-stdlib/`.
fn sky_path_for(import_path: &str, repo_root: &Path) -> std::path::PathBuf {
    let rel = import_path.replace('.', "/");
    repo_root.join("sky-stdlib").join(format!("{rel}.sky"))
}

/// The dotted import path that backs a pseudo-module, if any (`List` →
/// `Sky.Core.List`). Prefers a `Sky.Core.*` / `Std.*` path (the canonical home)
/// over a bare alias.
fn import_path_for(pseudo: &str) -> Option<&'static str> {
    KERNEL_MODULES
        .iter()
        .filter(|(k, v)| *v == pseudo && k.contains('.'))
        .map(|(k, _)| *k)
        .min_by_key(|k| (!k.starts_with("Sky.Core."), !k.starts_with("Std."), k.len()))
}

struct SkyModule {
    exposing: Vec<String>,
    kernel_syms: Vec<String>,
}

/// Strip `--` line comments from a `.sky` source so a doc-comment that mentions
/// the `Ffi.kernel "List_<name>"` PATTERN is not mistaken for a real binding.
/// (Good enough for stdlib scanning; a `--` inside a string literal is rare here
/// and never inside an `exposing`/`Ffi.kernel` token we key on.)
fn strip_line_comments(src: &str) -> String {
    src.lines()
        .map(|line| match line.find("--") {
            Some(i) => &line[..i],
            None => line,
        })
        .collect::<Vec<_>>()
        .join("\n")
}

/// Extract the `exposing ( … )` names and every `Ffi.kernel "Sym"` string from a
/// `.sky` source. `exposing (..)` (open) yields an empty `exposing` list (there
/// is nothing to check against — the module re-exports whatever it defines).
fn parse_sky(raw: &str) -> SkyModule {
    let src = &strip_line_comments(raw);
    let mut exposing = Vec::new();
    // exposing block: from the first `exposing` after `module` to its matching
    // `)`. Handle `exposing (..)` as open.
    if let Some(mod_idx) = src.find("module ") {
        if let Some(exp_rel) = src[mod_idx..].find("exposing") {
            let after = &src[mod_idx + exp_rel + "exposing".len()..];
            if let Some(open) = after.find('(') {
                // find matching close paren
                let mut depth = 0i32;
                let mut end = None;
                for (i, c) in after[open..].char_indices() {
                    match c {
                        '(' => depth += 1,
                        ')' => {
                            depth -= 1;
                            if depth == 0 {
                                end = Some(open + i);
                                break;
                            }
                        }
                        _ => {}
                    }
                }
                if let Some(end) = end {
                    let inner = &after[open + 1..end];
                    if inner.trim() != ".." {
                        for tok in inner.split(',') {
                            let name = tok.trim().trim_matches(|c: char| c == '(' || c == ')');
                            // drop `Type(..)` ctor-exposing suffix and operators
                            let name = name.split('(').next().unwrap_or(name).trim();
                            if !name.is_empty()
                                && name
                                    .chars()
                                    .next()
                                    .map(|c| c.is_ascii_lowercase())
                                    .unwrap_or(false)
                            {
                                exposing.push(name.to_string());
                            }
                        }
                    }
                }
            }
        }
    }
    // Ffi.kernel "Sym"
    let mut kernel_syms = Vec::new();
    let needle = "Ffi.kernel";
    let mut idx = 0;
    while let Some(rel) = src[idx..].find(needle) {
        let start = idx + rel + needle.len();
        if let Some(q1) = src[start..].find('"') {
            let s = start + q1 + 1;
            if let Some(q2) = src[s..].find('"') {
                kernel_syms.push(src[s..s + q2].to_string());
                idx = s + q2 + 1;
                continue;
            }
        }
        idx = start;
    }
    SkyModule {
        exposing,
        kernel_syms,
    }
}

/// The set of member `f` names a module contributes to the KERNEL_TABLE (the
/// lowerer's `(M, f) → rt.symbol` map).
fn kernel_table_members(module: &str) -> BTreeSet<String> {
    lower::kernel::kernel_table_entries()
        .iter()
        .filter(|(m, _, _)| *m == module)
        .map(|(_, f, _)| f.to_string())
        .collect()
}

/// Strip a trailing typed-variant suffix if one is present; returns the base and
/// whether a strip happened.
fn strip_typed(base: &str) -> Option<String> {
    for suf in TYPED_SUFFIXES {
        if base.len() > suf.len() && base.ends_with(suf) {
            return Some(base[..base.len() - suf.len()].to_string());
        }
    }
    None
}

pub struct Report {
    pub passed: bool,
    pub assertions: u64,
    pub detail: String,
}

// `check_body` is the harness entry point. The gate is registered in the harness
// (`registry::GATES`) once its assertions are GREEN — i.e. after the P2 table
// sync lands — because the harness treats a red gate as FAIL/UNKNOWN. Until then
// it runs as the standalone `xtask kernel-members` discovery subcommand.
#[allow(dead_code)]

pub fn check_body(repo_root: &Path) -> (bool, u64, String) {
    let r = compute(repo_root);
    (r.passed, r.assertions, r.detail)
}

fn compute(repo_root: &Path) -> Report {
    let exports = project::abi_guard::runtime_exports(repo_root);
    let is_real = |m: &str, f: &str| exports.contains(&rt_symbol(m, f));

    // The universe of pseudo-modules to check.
    let mut pseudos: BTreeSet<&str> = BTreeSet::new();
    for (m, _) in KERNEL_FUNCTIONS {
        pseudos.insert(m);
    }
    for (m, _) in PRELUDE_QUALIFIERS {
        pseudos.insert(m);
    }
    for (m, _, _) in lower::kernel::kernel_table_entries() {
        pseudos.insert(m);
    }

    let kf_map: BTreeMap<&str, &[&str]> = KERNEL_FUNCTIONS.iter().map(|(m, f)| (*m, *f)).collect();
    let pq_map: BTreeMap<&str, &[&str]> =
        PRELUDE_QUALIFIERS.iter().map(|(m, f)| (*m, *f)).collect();

    let mut assertions: u64 = 0;
    let mut failures: Vec<String> = Vec::new();

    for &m in &pseudos {
        // ---- candidate member names --------------------------------------
        let mut candidates: BTreeSet<String> = BTreeSet::new();
        if let Some(fs) = kf_map.get(m) {
            candidates.extend(fs.iter().map(|s| s.to_string()));
        }
        if let Some(fs) = pq_map.get(m) {
            candidates.extend(fs.iter().map(|s| s.to_string()));
        }
        candidates.extend(kernel_table_members(m));

        // .sky backing
        let sky = import_path_for(m)
            .map(|p| sky_path_for(p, repo_root))
            .filter(|p| p.exists())
            .and_then(|p| std::fs::read_to_string(&p).ok().map(|s| parse_sky(&s)));
        if let Some(sm) = &sky {
            candidates.extend(sm.exposing.iter().cloned());
        }

        // ---- RealMembers = candidates that map to a real runtime symbol ---
        let real: BTreeSet<String> = candidates
            .iter()
            .filter(|f| is_real(m, f))
            .cloned()
            .collect();

        // ---- assertion 1: KERNEL_FUNCTIONS[M] == RealMembers(M) -----------
        if let Some(fs) = kf_map.get(m) {
            assertions += 1;
            let kf: BTreeSet<String> = fs.iter().map(|s| s.to_string()).collect();
            if kf != real {
                let missing: Vec<_> = real.difference(&kf).cloned().collect();
                let extra: Vec<_> = kf.difference(&real).cloned().collect();
                let mut msg = format!("[1] KERNEL_FUNCTIONS[{m}] != RealMembers:");
                if !missing.is_empty() {
                    msg += &format!("\n      ADD (real, missing): {}", missing.join(", "));
                }
                if !extra.is_empty() {
                    // phantoms: in table but no runtime symbol
                    let phantoms: Vec<_> = extra
                        .iter()
                        .filter(|f| !candidates.contains(*f) || !is_real(m, f))
                        .cloned()
                        .collect();
                    msg += &format!("\n      REMOVE (phantom, no runtime symbol): {}", extra.join(", "));
                    let _ = phantoms;
                }
                failures.push(msg);
            }
        }

        // ---- assertion 2: PRELUDE_QUALIFIERS[M] ⊆ RealMembers(M) ----------
        if let Some(fs) = pq_map.get(m) {
            assertions += 1;
            let phantoms: Vec<_> = fs.iter().filter(|f| !is_real(m, f)).cloned().collect();
            if !phantoms.is_empty() {
                failures.push(format!(
                    "[2] PRELUDE_QUALIFIERS[{m}] names phantom(s) with no runtime symbol: {}",
                    phantoms.join(", ")
                ));
            }
        }

        // ---- assertion 3: every `Ffi.kernel "Sym"` binding names a real symbol
        // (a `.sky` may ALSO define pure-Sky Defs with no runtime symbol —
        // `Maybe.isJust`, `Http.withUrl` — which resolve `Res::Def` via
        // `qual_vars` and are deliberately NOT checked here).
        if let Some(sm) = &sky {
            assertions += 1;
            // A `Ffi.kernel "M_f"` alias lowers through `alias_go_name`, which
            // consults the KERNEL_TABLE — e.g. `Task_perform` re-maps to
            // `rt.AnyTaskRun`, NOT `rt.Task_perform`. Resolve through that
            // indirection before checking the runtime, or a legitimately
            // remapped alias reads as a phantom.
            let bad_syms: Vec<_> = sm
                .kernel_syms
                .iter()
                .filter(|s| {
                    let sym = lower::kernel::alias_go_name(s);
                    let bare = sym.strip_prefix("rt.").unwrap_or(&sym);
                    !exports.contains(bare)
                })
                .cloned()
                .collect();
            if !bad_syms.is_empty() {
                failures.push(format!(
                    "[3b] {m}.sky Ffi.kernel names symbol(s) absent from runtime: {}",
                    bad_syms.join(", ")
                ));
            }
        }

        // ---- assertion 4: COMPLETENESS scan (only for reject-validated M) --
        if kf_map.contains_key(m) {
            assertions += 1;
            let internal: BTreeSet<&str> = internal_runtime_bases(m).iter().copied().collect();
            let prefix = format!("{m}_");
            let mut runtime_only: Vec<String> = Vec::new();
            for sym in exports.iter() {
                let Some(base) = sym.strip_prefix(&prefix) else {
                    continue;
                };
                // skip typed variants whose stripped base is a member/candidate
                // or itself an internal backing symbol
                if let Some(stripped) = strip_typed(base) {
                    if candidates.contains(&stripped)
                        || real.contains(&stripped)
                        || internal.contains(stripped.as_str())
                    {
                        continue;
                    }
                }
                if real.contains(base) || candidates.contains(base) {
                    continue;
                }
                if internal.contains(base) {
                    continue;
                }
                runtime_only.push(base.to_string());
            }
            if !runtime_only.is_empty() {
                runtime_only.sort();
                runtime_only.dedup();
                failures.push(format!(
                    "[4] {m}: runtime-only base name(s) not classified (add as member or list internal): {}",
                    runtime_only.join(", ")
                ));
            }
        }
    }

    let passed = failures.is_empty();
    let detail = if passed {
        format!(
            "kernel-members: OK — {} pseudo-modules, {assertions} assertions, tables are a faithful superset of the runtime",
            pseudos.len()
        )
    } else {
        format!(
            "kernel-members: {} drift finding(s):\n  {}",
            failures.len(),
            failures.join("\n  ")
        )
    };
    Report {
        passed,
        assertions,
        detail,
    }
}

pub fn run(args: &[String], repo_root: &Path) -> i32 {
    let _check = args.iter().any(|a| a == "--check");
    let r = compute(repo_root);
    println!("{}", r.detail);
    println!("(assertions: {})", r.assertions);
    if r.passed {
        0
    } else {
        1
    }
}
