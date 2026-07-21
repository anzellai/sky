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
use std::collections::BTreeSet;
use std::path::Path;
use std::sync::OnceLock;

/// Every exported top-level identifier the Go runtime (`runtime-go/rt`) defines,
/// scanned once per process from disk (the exact tree `go build` compiles).
pub fn runtime_exports(repo_root: &Path) -> &'static BTreeSet<String> {
    static CACHE: OnceLock<BTreeSet<String>> = OnceLock::new();
    CACHE.get_or_init(|| scan_runtime_exports(&repo_root.join("runtime-go").join("rt")))
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
        .map(|name| {
            Diagnostic::error(
                "E4005",
                format!(
                    "Codegen emitted a reference to `rt.{name}`, but the Sky runtime \
                     does not export it. This is a Sky compiler bug — please report \
                     with the offending source. (Fix the kernel table in \
                     `rust/crates/lower/src/kernel.rs` or add `rt.{name}` to \
                     `runtime-go/rt/`.)"
                ),
            )
        })
        .collect()
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
}
