//! ABI-symbol guard (port of the oracle's `Sky.Build.Validator.validateEmittedGo`).
//!
//! Before `go build`, validate that every `rt.<Ident>` the emitted `main.go`
//! references actually exists in the Go runtime (`runtime-go/rt/*.go`). A miss
//! means codegen emitted a symbol the runtime does not export — surfaced today
//! only as a confusing `go build: undefined: rt.X`. The guard turns it into a
//! clean `[E4005]` compiler diagnostic (matching the oracle), aborting before
//! `go build`. This upholds "if it type-checks it builds": a `rt.X` hole is a
//! compiler bug, reported as one, not a raw Go error.
//!
//! Structural safety: the guard's fire-set is a subset of `{ rt.X : go build
//! emits "undefined: rt.X" }`, so it can never turn a currently-building program
//! red — it is a pure UX upgrade on already-broken input.

use diagnostics::Diagnostic;
use std::collections::{BTreeMap, BTreeSet};
use std::path::Path;
use std::sync::OnceLock;

/// Every exported top-level identifier the Go runtime (`runtime-go/rt`) defines,
/// scanned once per process from disk (the exact tree `go build` compiles).
pub fn runtime_exports(repo_root: &Path) -> &'static BTreeSet<String> {
    static CACHE: OnceLock<BTreeSet<String>> = OnceLock::new();
    CACHE.get_or_init(|| scan_runtime_exports(&repo_root.join("runtime-go").join("rt")))
}

/// The parameter count of every top-level `func Name(params) …` in the runtime,
/// scanned once per process — the AUTHORITATIVE arity of each `rt.<Name>` kernel
/// symbol (the emitted call passes exactly this many `any` args). The lowerer
/// uses it to decide whether a kernel application is partial (eta-expand) or full
/// (direct call). This is the only sound arity source: a kernel's curried HM type
/// over-counts when its result is a function alias (`Handler = Request -> Task`),
/// so `withCors : List String -> Handler -> Handler` (runtime arity 2) would look
/// like arity 3 and a full 2-arg call would be mis-eta-expanded.
pub fn runtime_arities(repo_root: &Path) -> &'static BTreeMap<String, usize> {
    static CACHE: OnceLock<BTreeMap<String, usize>> = OnceLock::new();
    CACHE.get_or_init(|| scan_runtime_arities(&repo_root.join("runtime-go").join("rt")))
}

fn scan_runtime_arities(rt_dir: &Path) -> BTreeMap<String, usize> {
    let mut out = BTreeMap::new();
    scan_arity_dir(rt_dir, &mut out);
    out
}

fn scan_arity_dir(dir: &Path, out: &mut BTreeMap<String, usize>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<_> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for p in entries {
        if p.is_dir() {
            scan_arity_dir(p.as_path(), out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("go") {
            let name = p.file_name().and_then(|n| n.to_str()).unwrap_or("");
            if name.ends_with("_test.go") {
                continue;
            }
            if let Ok(src) = std::fs::read_to_string(&p) {
                for line in src.lines() {
                    if let Some((n, arity)) = extract_func_arity(line) {
                        // A name may appear once (Go forbids redeclaration); first
                        // wins if a duplicate ever slips through.
                        out.entry(n).or_insert(arity);
                    }
                }
            }
        }
    }
}

/// For a top-level `func Name(params) …` line, return `(Name, params_inner)` —
/// the function name and the raw text between its outermost parens. Methods
/// (`func (r *T) M`) and non-func decls yield `None`; a generic `[T any]` list
/// is skipped before the value params. Only column-0 `func ` declarations count.
fn func_head(line: &str) -> Option<(String, &str)> {
    let rest = line.strip_prefix("func ")?;
    // Method receiver (`func (r *T) …`) → the char after `func ` is `(`.
    let name: String = rest.chars().take_while(|c| is_ident_char(*c)).collect();
    if name.is_empty() {
        return None;
    }
    let after = &rest[name.len()..];
    // Skip an optional generic type-param list `[T any]` before the value params.
    let after = after.trim_start();
    let after = if let Some(stripped) = after.strip_prefix('[') {
        // find the matching `]` at depth 0
        let mut depth = 1usize;
        let mut idx = 0;
        for (i, c) in stripped.char_indices() {
            match c {
                '[' => depth += 1,
                ']' => {
                    depth -= 1;
                    if depth == 0 {
                        idx = i + 1;
                        break;
                    }
                }
                _ => {}
            }
        }
        stripped[idx..].trim_start()
    } else {
        after
    };
    let params_inner = param_list_inner(after)?;
    Some((name, params_inner))
}

/// For a top-level `func Name(params) …` line, return `(Name, param_count)`.
fn extract_func_arity(line: &str) -> Option<(String, usize)> {
    let (name, params_inner) = func_head(line)?;
    Some((name, count_params(params_inner)))
}

/// The names of runtime kernel funcs declared with a trailing Go VARIADIC
/// parameter (`func Http_request(arg any, rest ...any)`). For these the Go-
/// source param scan is NOT the true currying arity (a variadic tail is
/// zero-or-more, and a fully-variadic `func(args ...any)` scans as 1 regardless
/// of the Sky arg count); the lowerer overrides them with the declared Sky
/// signature's arrow-count. Cached once per process, keyed WITHOUT `rt.`.
pub fn runtime_variadic_kernels(repo_root: &Path) -> &'static BTreeSet<String> {
    static CACHE: OnceLock<BTreeSet<String>> = OnceLock::new();
    CACHE.get_or_init(|| {
        let mut out = BTreeSet::new();
        scan_variadic_dir(&repo_root.join("runtime-go").join("rt"), &mut out);
        out
    })
}

fn scan_variadic_dir(dir: &Path, out: &mut BTreeSet<String>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<_> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for p in entries {
        if p.is_dir() {
            scan_variadic_dir(p.as_path(), out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("go") {
            let name = p.file_name().and_then(|n| n.to_str()).unwrap_or("");
            if name.ends_with("_test.go") {
                continue;
            }
            if let Ok(src) = std::fs::read_to_string(&p) {
                for line in src.lines() {
                    if let Some((n, params)) = func_head(line) {
                        // `...` only appears in a Go param list as a variadic tail
                        // marker (`rest ...any`) — never in a well-formed non-
                        // variadic param type.
                        if params.contains("...") {
                            out.insert(n);
                        }
                    }
                }
            }
        }
    }
}

/// The text between the outermost `(` and its matching `)` in `s` (the value
/// parameter list), or `None` if there is no `(` at the head.
fn param_list_inner(s: &str) -> Option<&str> {
    let s = s.strip_prefix('(')?;
    let mut depth = 1usize;
    for (i, c) in s.char_indices() {
        match c {
            '(' | '[' | '{' => depth += 1,
            ')' | ']' | '}' => {
                depth -= 1;
                if depth == 0 {
                    return Some(&s[..i]);
                }
            }
            _ => {}
        }
    }
    None
}

/// Count Go parameters in a parameter-list body. Splits on top-level commas
/// (depth 0), then within each group counts leading identifiers that precede a
/// type — so `a, b int` is 2, `a any, b any` is 2, `m map[string]int` is 1,
/// `f func(int, int) int` is 1. An empty list is 0.
fn count_params(inner: &str) -> usize {
    let inner = inner.trim();
    if inner.is_empty() {
        return 0;
    }
    // Split on depth-0 commas.
    let mut groups: Vec<&str> = Vec::new();
    let mut depth = 0i32;
    let mut start = 0;
    for (i, c) in inner.char_indices() {
        match c {
            '(' | '[' | '{' => depth += 1,
            ')' | ']' | '}' => depth -= 1,
            ',' if depth == 0 => {
                groups.push(&inner[start..i]);
                start = i + 1;
            }
            _ => {}
        }
    }
    groups.push(&inner[start..]);
    // Param count == group count: Go's shared-type form (`a, b int`) already
    // splits on commas into `a` and `b int`, one parameter each.
    groups.iter().filter(|g| !g.trim().is_empty()).count()
}

fn scan_runtime_exports(rt_dir: &Path) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    scan_dir(rt_dir, &mut out);
    out
}

fn scan_dir(dir: &Path, out: &mut BTreeSet<String>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<_> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for p in entries {
        if p.is_dir() {
            scan_dir(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("go") {
            let name = p.file_name().and_then(|n| n.to_str()).unwrap_or("");
            if name.ends_with("_test.go") {
                continue;
            }
            if let Ok(src) = std::fs::read_to_string(&p) {
                for line in src.lines() {
                    if let Some(n) = extract_top_level_name(line) {
                        out.insert(n);
                    }
                }
            }
        }
    }
}

/// A top-level Go declaration's name: `func Name(` / `func Name[` / `type Name` /
/// `var Name` / `const Name`. Methods (`func (r *T) M`) yield no name (the char
/// after `func ` is `(`, not an ident) and are correctly skipped. Only column-0
/// declarations count (a leading space rules out indented / nested forms).
fn extract_top_level_name(line: &str) -> Option<String> {
    let rest = ["func ", "type ", "var ", "const "]
        .iter()
        .find_map(|kw| line.strip_prefix(kw))?;
    let name: String = rest.chars().take_while(|c| is_ident_char(*c)).collect();
    if name.is_empty() {
        None
    } else {
        Some(name)
    }
}

fn is_ident_char(c: char) -> bool {
    c.is_ascii_alphanumeric() || c == '_'
}

/// Every distinct `rt.<Ident>` referenced in the emitted Go, at a token boundary,
/// skipping string literals, `//` line comments, `/* … */` block comments, and
/// field-access chains (`rt.a.b` — the `b` is not an `rt` symbol). Port of the
/// oracle's `extractRtRefs`, extended with the `/* */` skip because Rust codegen
/// emits inline block comments (`/* FFI return */ rt.AsString(...)`) with a real
/// ref on the same line after the comment — the oracle's line-comment rule would
/// wrongly drop it.
pub fn extract_rt_refs(src: &str) -> Vec<String> {
    let bytes: Vec<char> = src.chars().collect();
    let n = bytes.len();
    let mut i = 0;
    let mut boundary = true; // start-of-string is a token boundary
    let mut out: Vec<String> = Vec::new();
    while i < n {
        let c = bytes[i];
        // String literal — skip to the closing quote (honouring `\"`).
        if c == '"' {
            i += 1;
            while i < n {
                if bytes[i] == '\\' {
                    i += 2;
                    continue;
                }
                if bytes[i] == '"' {
                    i += 1;
                    break;
                }
                i += 1;
            }
            boundary = true;
            continue;
        }
        // Line comment — skip to end of line.
        if c == '/' && i + 1 < n && bytes[i + 1] == '/' {
            while i < n && bytes[i] != '\n' {
                i += 1;
            }
            boundary = true;
            continue;
        }
        // Block comment — skip to `*/` (bounded; refs after it on the same line
        // must survive).
        if c == '/' && i + 1 < n && bytes[i + 1] == '*' {
            i += 2;
            while i < n {
                if bytes[i] == '*' && i + 1 < n && bytes[i + 1] == '/' {
                    i += 2;
                    break;
                }
                i += 1;
            }
            boundary = true;
            continue;
        }
        // `rt.<Ident>` at a token boundary.
        if boundary && c == 'r' && i + 2 < n && bytes[i + 1] == 't' && bytes[i + 2] == '.' {
            let start = i + 3;
            let mut j = start;
            while j < n && is_ident_char(bytes[j]) {
                j += 1;
            }
            if j > start {
                // Exclude field access `rt.a.b` — the ident is followed by `.`.
                let field_access = j < n && bytes[j] == '.';
                if !field_access {
                    out.push(bytes[start..j].iter().collect());
                }
                // Resume scanning after the consumed ident.
                i = j;
                boundary = j < n && !is_ident_char(bytes[j]) && bytes[j] != '.';
                continue;
            }
        }
        boundary = !is_ident_char(c) && c != '.';
        i += 1;
    }
    out
}

/// A `rt.X` reference is FFI-generated (lives in a separate `<pkg>_bindings.go`,
/// not `runtime-go/rt`) — excluded from the runtime-export check.
fn is_ffi_generated(name: &str) -> bool {
    name.starts_with("Go_") || name.starts_with("FfiT_")
}

/// Validate the emitted Go's `rt.*` references against the runtime's exports.
/// Returns one `[E4005]` diagnostic per distinct undefined symbol.
pub fn check_abi_symbols(source: &str, exports: &BTreeSet<String>) -> Vec<Diagnostic> {
    let mut missing: BTreeSet<String> = BTreeSet::new();
    for name in extract_rt_refs(source) {
        if is_ffi_generated(&name) || exports.contains(&name) {
            continue;
        }
        missing.insert(name);
    }
    missing
        .into_iter()
        .map(|name| Diagnostic::error("E4005", abi_message(&name, exports)))
        .collect()
}

/// The `[E4005]` message for a missing `rt.<name>`. A missing symbol whose
/// `<Module>_` prefix has sibling exports (`rt.<Module>_*`) is a real kernel
/// module the user typo'd a MEMBER of (`String.lenght`) — surface that as a
/// name error with a did-you-mean, not a "compiler bug — please report" (which
/// blames the compiler for a user typo). A missing symbol with NO siblings is a
/// genuine codegen hole and keeps the report-a-bug framing.
fn abi_message(name: &str, exports: &BTreeSet<String>) -> String {
    if let Some((module, member)) = name.rsplit_once('_') {
        let prefix = format!("{module}_");
        let siblings: Vec<&str> = exports
            .iter()
            .filter_map(|e| e.strip_prefix(&prefix))
            .filter(|m| !m.is_empty() && !m.contains('_'))
            .collect();
        if !siblings.is_empty() {
            // `String_` → the Sky qualifier `String`; `Json_Decode_` → `Json.Decode`.
            let sky_qual = module.replace('_', ".");
            let hint = closest(member, &siblings)
                .map(|c| format!(" — did you mean `{sky_qual}.{c}`?"))
                .unwrap_or_default();
            return format!(
                "`{sky_qual}` has no member `{member}`{hint} (the Sky runtime exports \
                 no `rt.{name}`)."
            );
        }
    }
    format!(
        "Codegen emitted a reference to `rt.{name}`, but the Sky runtime does not \
         export it. This is a Sky compiler bug — please report with the offending \
         source. (Fix the kernel table in `rust/crates/lower/src/kernel.rs` or add \
         `rt.{name}` to `runtime-go/rt/`.)"
    )
}

/// The candidate closest to `target` by Levenshtein distance, within a small
/// threshold (so an unrelated name yields no misleading suggestion).
fn closest<'a>(target: &str, candidates: &[&'a str]) -> Option<&'a str> {
    let max = (target.len() / 2).max(2);
    candidates
        .iter()
        .map(|c| (levenshtein(target, c), *c))
        .filter(|(d, _)| *d <= max)
        .min_by_key(|(d, _)| *d)
        .map(|(_, c)| c)
}

/// Standard Levenshtein edit distance (two-row DP).
fn levenshtein(a: &str, b: &str) -> usize {
    let (a, b): (Vec<char>, Vec<char>) = (a.chars().collect(), b.chars().collect());
    let mut prev: Vec<usize> = (0..=b.len()).collect();
    let mut cur = vec![0usize; b.len() + 1];
    for (i, ca) in a.iter().enumerate() {
        cur[0] = i + 1;
        for (j, cb) in b.iter().enumerate() {
            let cost = if ca == cb { 0 } else { 1 };
            cur[j + 1] = (prev[j + 1] + 1).min(cur[j] + 1).min(prev[j] + cost);
        }
        std::mem::swap(&mut prev, &mut cur);
    }
    prev[b.len()]
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn extracts_refs_skipping_comments_and_strings() {
        let src = r#"
            x := rt.Add(1, 2)
            y := /* FFI return */ rt.AsString(z)
            // rt.NotAReference here
            s := "rt.AlsoNotAReference"
            f := rt.T2[any, any]{V0: rt.Coerce[int](a)}
            chain := rt.SkyTask.Foo
        "#;
        let refs = extract_rt_refs(src);
        assert!(refs.contains(&"Add".to_string()), "got {refs:?}");
        assert!(refs.contains(&"AsString".to_string()), "got {refs:?}");
        assert!(refs.contains(&"T2".to_string()));
        assert!(refs.contains(&"Coerce".to_string()));
        // comment / string / field-access must NOT be refs
        assert!(!refs.contains(&"NotAReference".to_string()));
        assert!(!refs.contains(&"AlsoNotAReference".to_string()));
        assert!(!refs.contains(&"SkyTask".to_string()) || !refs.contains(&"Foo".to_string()));
        assert!(
            !refs.contains(&"Foo".to_string()),
            "field access leaked: {refs:?}"
        );
    }

    #[test]
    fn top_level_names_skip_methods() {
        assert_eq!(
            extract_top_level_name("func Add(a, b any) any {"),
            Some("Add".into())
        );
        assert_eq!(
            extract_top_level_name("func Coerce[T any](x any) T {"),
            Some("Coerce".into())
        );
        assert_eq!(
            extract_top_level_name("type SkyTupleN struct{ Vs []any }"),
            Some("SkyTupleN".into())
        );
        assert_eq!(
            extract_top_level_name("var _defaultBroker = newBroker()"),
            Some("_defaultBroker".into())
        );
        // method — no top-level name
        assert_eq!(extract_top_level_name("func (r *Reader) Read() {"), None);
        // indented — not top-level
        assert_eq!(extract_top_level_name("    func inner() {"), None);
    }

    #[test]
    fn flags_undefined_symbol() {
        let mut exports = BTreeSet::new();
        exports.insert("Add".to_string());
        let diags = check_abi_symbols("x := rt.Pow(2, 3) + rt.Add(1, 2)", &exports);
        assert_eq!(diags.len(), 1);
        assert!(diags[0].message.contains("rt.Pow"));
        assert_eq!(diags[0].code.0, "E4005");
    }

    #[test]
    fn kernel_member_typo_gets_did_you_mean_not_report_a_bug() {
        // `rt.String_length` exists → `String` is a real kernel module, so a
        // typo'd `rt.String_lenght` is a member typo, not a compiler bug.
        let mut exports = BTreeSet::new();
        exports.insert("String_length".to_string());
        exports.insert("String_reverse".to_string());
        let diags = check_abi_symbols("x := rt.String_lenght(s)", &exports);
        assert_eq!(diags.len(), 1);
        let m = &diags[0].message;
        assert!(m.contains("has no member `lenght`"), "got: {m}");
        assert!(m.contains("did you mean `String.length`"), "got: {m}");
        assert!(
            !m.contains("compiler bug"),
            "typo must not blame the compiler: {m}"
        );
    }

    #[test]
    fn func_arity_counts_params_robustly() {
        // all-any dispatch kernels
        assert_eq!(
            extract_func_arity("func String_append(a any, b any) any {"),
            Some(("String_append".into(), 2))
        );
        assert_eq!(
            extract_func_arity("func Middleware_withCors(origins any, handler any) any {"),
            Some(("Middleware_withCors".into(), 2))
        );
        assert_eq!(
            extract_func_arity("func Char_toUpper(c any) any { return x }"),
            Some(("Char_toUpper".into(), 1))
        );
        // zero-arg
        assert_eq!(
            extract_func_arity("func Dict_empty() any {"),
            Some(("Dict_empty".into(), 0))
        );
        // Go shared-type form: `lo, hi, n int` is 3 params
        assert_eq!(
            extract_func_arity("func Basics_clampT(lo, hi, n int) int {"),
            Some(("Basics_clampT".into(), 3))
        );
        // func-typed param has an inner comma at depth>0 → still ONE param
        assert_eq!(
            extract_func_arity("func Apply(f func(int, int) int, x any) any {"),
            Some(("Apply".into(), 2))
        );
        // generic type-param list is skipped before the value params
        assert_eq!(
            extract_func_arity("func Coerce[T any](v any) T {"),
            Some(("Coerce".into(), 1))
        );
        // methods + non-func decls yield nothing
        assert_eq!(
            extract_func_arity("func (r *Reader) Read(p any) int {"),
            None
        );
        assert_eq!(extract_func_arity("type Foo struct {"), None);
    }

    #[test]
    fn func_head_detects_variadic_kernels() {
        // A trailing `...T` marks a variadic runtime symbol — the one case the
        // param scan mis-counts the currying arity, so the lowerer must override
        // it with the declared Sky sig (anzellai/sky#155).
        let is_variadic = |line: &str| {
            func_head(line)
                .map(|(_, params)| params.contains("..."))
                .unwrap_or(false)
        };
        // leading-fixed + variadic tail (`Http.request` shape) — scans as 2 but is
        // Sky-arity 1.
        assert!(is_variadic("func Http_request(firstArg any, rest ...any) any {"));
        // fully-variadic (`JsonEnc.list` shape) — scans as 1 but is Sky-arity 2.
        assert!(is_variadic("func JsonEnc_list(args ...any) any {"));
        assert!(is_variadic("func Db_open(args ...any) any {"));
        // NON-variadic middleware whose result type is itself a function
        // (`withCors : … -> Handler -> Handler`): the Go scan (2) is authoritative
        // and MUST NOT be overridden, else the curried sig over-counts to 3.
        assert!(!is_variadic(
            "func Middleware_withCors(origins any, handler any) any {"
        ));
        assert!(!is_variadic("func String_append(a any, b any) any {"));
        assert!(!is_variadic("func Dict_empty() any {"));
    }

    #[test]
    fn genuine_codegen_hole_keeps_report_a_bug() {
        // No `rt.Widget_*` sibling exports → a real codegen hole, keep the
        // report-a-bug framing.
        let exports = BTreeSet::new();
        let diags = check_abi_symbols("x := rt.Widget_render(w)", &exports);
        assert_eq!(diags.len(), 1);
        assert!(
            diags[0].message.contains("compiler bug"),
            "got: {}",
            diags[0].message
        );
    }
}
