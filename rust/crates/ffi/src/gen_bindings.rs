//! Go wrapper (`<slug>_bindings.go`) generator — the Rust port of
//! `src/Sky/Build/FfiGen.hs`'s `emitGoFile` + `emitTypedWrapper` +
//! `emitTypedVariant` + `emitTypedCall` + `rewriteType` + alias-table machinery
//! (FfiGen.hs:600-1781).
//!
//! Consumes an [`inspect::PackageInfo`] and produces the `package rt` Go source
//! that surfaces each Go function as a typed `Go_<Kernel>_<fn>T` wrapper
//! returning `SkyResult[any, T]` with `SkyFfiRecoverT` panic capture.
//!
//! This is a faithful, byte-for-byte port: the committed golden file
//! `tests/fixtures/uuid.expected_bindings.go` is the spec and the test at the
//! bottom drives this emitter to a byte-identical match against it.

use crate::inspect::{Function, PackageInfo};
use std::collections::{BTreeMap, BTreeSet};

// ---------------------------------------------------------------------------
// Small character predicates. Go type strings are ASCII, so the Haskell
// Data.Char `isLower`/`isUpper`/`isAlphaNum` collapse to the ASCII variants.
// ---------------------------------------------------------------------------

fn is_lower(c: char) -> bool {
    c.is_ascii_lowercase()
}
fn is_upper(c: char) -> bool {
    c.is_ascii_uppercase()
}
fn is_alnum(c: char) -> bool {
    c.is_ascii_alphanumeric()
}
/// `isSegChar` — path segment char (FfiGen.hs:716).
fn is_seg_char(c: char) -> bool {
    is_alnum(c) || c == '-' || c == '_'
}
/// `isNameChar` — identifier char after a `.` (FfiGen.hs:717).
fn is_name_char(c: char) -> bool {
    is_alnum(c) || c == '_'
}
/// Type-term delimiter (FfiGen.hs:691 / 750).
fn is_boundary(c: char) -> bool {
    matches!(c, ' ' | '\t' | '\n' | '*' | '[' | ']' | '(' | ')' | ',' | '<' | '>')
}

/// `unlines` — join with `\n` AND append a trailing `\n` (Haskell semantics).
/// Each element gets its own trailing newline; multi-line elements (whole
/// wrapper entries) therefore gain a blank-line separator, exactly as in the
/// Haskell emitter.
fn unlines(lines: &[String]) -> String {
    let mut out = String::new();
    for l in lines {
        out.push_str(l);
        out.push('\n');
    }
    out
}

fn join(sep: &str, items: &[String]) -> String {
    items.join(sep)
}

/// `lowerFirst` (FfiGen.hs:377).
fn lower_first(s: &str) -> String {
    let mut chars = s.chars();
    match chars.next() {
        Some(c) => c.to_lowercase().chain(chars).collect(),
        None => String::new(),
    }
}

/// `quote` (FfiGen.hs:1923) — Go/Haskell string literal with `"`/`\` escaped.
fn quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            _ => out.push(c),
        }
    }
    out.push('"');
    out
}

/// `isBareParam` (FfiGen.hs:1307) — a single uppercase letter.
fn is_bare_param_hs(t: &str) -> bool {
    let mut chars = t.chars();
    match (chars.next(), chars.next()) {
        (Some(c), None) => c.is_ascii_uppercase(),
        _ => false,
    }
}

/// `isStarBareParam` (FfiGen.hs:1313) — `*` followed by a bare param.
fn is_star_bare_param_hs(t: &str) -> bool {
    t.strip_prefix('*').is_some_and(is_bare_param_hs)
}

/// `isPointerType` (FfiGen.hs:1655).
fn is_pointer_type(t: &str) -> bool {
    t.starts_with('*')
}

// ---------------------------------------------------------------------------
// Package discovery + alias resolution (FfiGen.hs:600-665).
// ---------------------------------------------------------------------------

/// A table: Go package path → alias used in the emitted wrapper. The requested
/// package itself is bound to `pkg`. Mirrors `FfiGen.buildAliasTable`.
pub(crate) fn build_alias_table(info: &PackageInfo) -> BTreeMap<String, String> {
    let self_path = info.pkg.clone();
    let all_paths = discover_package_paths(info);
    let others: Vec<String> = all_paths.into_iter().filter(|p| *p != self_path).collect();

    let mut table: BTreeMap<String, String> = BTreeMap::new();
    table.insert(self_path.clone(), "pkg".to_string());
    let mut used: BTreeSet<String> = BTreeSet::new();
    used.insert("pkg".to_string());
    used.insert("fmt".to_string());

    for path in others {
        let base = path_to_alias(&path);
        let final_alias = unique_alias(&used, &base, 0);
        table.insert(path, final_alias.clone());
        used.insert(final_alias);
    }
    table
}

fn unique_alias(used: &BTreeSet<String>, base: &str, n: u32) -> String {
    let candidate = if n == 0 {
        base.to_string()
    } else {
        format!("{base}_{n}")
    };
    if used.contains(&candidate) {
        unique_alias(used, base, n + 1)
    } else {
        candidate
    }
}

/// `pathToAlias` (FfiGen.hs:627) — last path segment sanitised to a valid Go
/// identifier, folding a trailing version segment onto the preceding one.
fn path_to_alias(path: &str) -> String {
    let last_seg: String = path.rsplit('/').next().unwrap_or("").to_string();
    let alias = if is_version_segment(&last_seg) {
        // rest = path with the trailing "/lastSeg" removed.
        let cut = last_seg.chars().count() + 1;
        let plen = path.chars().count();
        let rest: String = path.chars().take(plen.saturating_sub(cut)).collect();
        let prev_seg: String = rest.rsplit('/').next().unwrap_or("").to_string();
        if !prev_seg.is_empty() && !is_version_segment(&prev_seg) {
            sanitise(&prev_seg)
        } else {
            sanitise(&last_seg)
        }
    } else {
        sanitise(&last_seg)
    };
    let head_ok = alias
        .chars()
        .next()
        .map(|c| is_lower(c) || c == '_')
        .unwrap_or(false);
    if alias.is_empty() || !head_ok {
        format!("p_{alias}")
    } else {
        alias
    }
}

fn sanitise(s: &str) -> String {
    s.chars().map(|c| if is_alnum(c) { c } else { '_' }).collect()
}

/// `isVersionSegment` (FfiGen.hs:645) — `v` followed by digits (possibly none).
fn is_version_segment(s: &str) -> bool {
    match s.strip_prefix('v') {
        Some(rest) => rest.chars().all(|c| c.is_ascii_digit()),
        None => false,
    }
}

/// `discoverPackagePaths` (FfiGen.hs:652) — every Go package referenced in any
/// function signature (raw types), including the package itself, `internal/`
/// and `vendor/` subtrees skipped, first-occurrence order preserved.
fn discover_package_paths(info: &PackageInfo) -> Vec<String> {
    let self_path = info.pkg.clone();
    let mut paths: Vec<String> = Vec::new();
    for fn_ in &info.functions {
        for p in &fn_.params {
            paths.extend(extract_package_paths(&p.ty));
        }
        for r in &fn_.results {
            paths.extend(extract_package_paths(&r.ty));
        }
    }
    let ok = |p: &str| !has_seg("internal", p) && !has_seg("vendor", p);
    let mut chained: Vec<String> = Vec::new();
    chained.push(self_path);
    for p in paths.into_iter().filter(|p| ok(p)) {
        chained.push(p);
    }
    // nub — preserve first-occurrence order, dedup.
    let mut seen: BTreeSet<String> = BTreeSet::new();
    let mut out: Vec<String> = Vec::new();
    for p in chained {
        if seen.insert(p.clone()) {
            out.push(p);
        }
    }
    out
}

fn has_seg(seg: &str, p: &str) -> bool {
    p.split('/').any(|x| x == seg)
}

const KNOWN_BARE_PKGS: &[&str] = &[
    "time", "io", "os", "fmt", "sync", "errors", "bytes", "strings", "strconv", "unicode", "math",
    "sort", "regexp", "reflect", "encoding", "bufio", "log", "context", "hash", "crypto", "net",
    "mime", "path",
];

/// `extractPackagePaths` (FfiGen.hs:674) — every package path in a Go type
/// string. State machine over chars: only a lowercase char at a type boundary
/// can begin a fresh package path.
fn extract_package_paths(s: &str) -> Vec<String> {
    let chars: Vec<char> = s.chars().collect();
    let n = chars.len();
    let mut out: Vec<String> = Vec::new();
    let mut at_boundary = true;
    let mut i = 0;
    while i < n {
        let c = chars[i];
        if at_boundary && is_lower(c) {
            match scan_path_discover(&chars, i) {
                Some((path, more)) => {
                    out.push(path);
                    at_boundary = true;
                    i = more;
                }
                None => {
                    at_boundary = is_boundary(c);
                    i += 1;
                }
            }
        } else {
            at_boundary = is_boundary(c);
            i += 1;
        }
    }
    out
}

/// `scanPath`/`walk` for discovery (FfiGen.hs:696): returns `(path, index after
/// the TypeName)` — applies the `hasPathSep || isKnownBarePkg` acceptance gate.
fn scan_path_discover(chars: &[char], start: usize) -> Option<(String, usize)> {
    let n = chars.len();
    let mut acc = String::new();
    let mut j = start;
    loop {
        if j >= n {
            return None;
        }
        let c = chars[j];
        if is_seg_char(c) {
            acc.push(c);
            j += 1;
        } else if c == '/' {
            acc.push('/');
            j += 1;
        } else if c == '.' {
            if j + 1 < n {
                let nch = chars[j + 1];
                if is_upper(nch) {
                    // Consume the TypeName, then decide.
                    let mut k = j + 1;
                    while k < n && is_name_char(chars[k]) {
                        k += 1;
                    }
                    if !acc.is_empty() && (acc.contains('/') || is_known_bare_pkg(&acc)) {
                        return Some((acc, k));
                    }
                    return None;
                } else if is_lower(nch) || is_alnum(nch) {
                    acc.push('.');
                    j += 1;
                } else {
                    return None;
                }
            } else {
                return None;
            }
        } else {
            return None;
        }
    }
}

fn is_known_bare_pkg(p: &str) -> bool {
    KNOWN_BARE_PKGS.contains(&p)
}

// ---------------------------------------------------------------------------
// Type rewriting (FfiGen.hs:736).
// ---------------------------------------------------------------------------

/// `rewriteType` (FfiGen.hs:736) — rewrite each `<pkg-path>.<Name>` to
/// `<alias>.<Name>` via the alias table, preserving `*`/`[]`/`map[K]V` wrappers.
fn rewrite_type(table: &BTreeMap<String, String>, s: &str) -> String {
    let chars: Vec<char> = s.chars().collect();
    let n = chars.len();
    let mut out = String::new();
    let mut at_boundary = true;
    let mut i = 0;
    while i < n {
        let c = chars[i];
        if at_boundary && is_lower(c) {
            match scan_path_rewrite(&chars, i) {
                Some((path, name, more)) => match table.get(&path) {
                    Some(alias) => {
                        out.push_str(alias);
                        out.push('.');
                        out.push_str(&name);
                        at_boundary = true;
                        i = more;
                    }
                    None => {
                        out.push(c);
                        at_boundary = is_boundary(c);
                        i += 1;
                    }
                },
                None => {
                    out.push(c);
                    at_boundary = is_boundary(c);
                    i += 1;
                }
            }
        } else {
            out.push(c);
            at_boundary = is_boundary(c);
            i += 1;
        }
    }
    out
}

/// `scanPath`/`walk` for rewriting (FfiGen.hs:752): returns `(path, name, index
/// after the TypeName)`. No `hasPathSep` gate — just a non-empty accumulator.
fn scan_path_rewrite(chars: &[char], start: usize) -> Option<(String, String, usize)> {
    let n = chars.len();
    let mut acc = String::new();
    let mut j = start;
    loop {
        if j >= n {
            return None;
        }
        let c = chars[j];
        if is_seg_char(c) {
            acc.push(c);
            j += 1;
        } else if c == '/' {
            acc.push('/');
            j += 1;
        } else if c == '.' {
            if j + 1 < n {
                let nch = chars[j + 1];
                if is_upper(nch) {
                    let mut k = j + 1;
                    let mut name = String::new();
                    while k < n && is_name_char(chars[k]) {
                        name.push(chars[k]);
                        k += 1;
                    }
                    if !acc.is_empty() {
                        return Some((acc, name, k));
                    }
                    return None;
                } else if is_lower(nch) || is_alnum(nch) {
                    acc.push('.');
                    j += 1;
                } else {
                    return None;
                }
            } else {
                return None;
            }
        } else {
            return None;
        }
    }
}

// ---------------------------------------------------------------------------
// Argument coercion (FfiGen.hs:1371) — typed `arg` params.
// ---------------------------------------------------------------------------

/// `typedArgCast` (FfiGen.hs:1371).
fn typed_arg_cast(i: usize, t: &str) -> String {
    let p = format!("arg{i}");
    match t {
        "string" => format!("fmt.Sprintf(\"%v\", {p})"),
        "int" => format!("AsInt({p})"),
        "int8" => format!("int8(AsInt({p}))"),
        "int16" => format!("int16(AsInt({p}))"),
        "int32" => format!("int32(AsInt({p}))"),
        "int64" => format!("int64(AsInt({p}))"),
        "uint" => format!("uint(AsInt({p}))"),
        "uint8" => format!("uint8(AsInt({p}))"),
        "uint16" => format!("uint16(AsInt({p}))"),
        "uint32" => format!("uint32(AsInt({p}))"),
        "uint64" => format!("uint64(AsInt({p}))"),
        "float64" => format!("AsFloat({p})"),
        "float32" => format!("float32(AsFloat({p}))"),
        "bool" => format!("AsBool({p})"),
        "byte" => format!("byte(AsInt({p}))"),
        "rune" => format!("rune(AsInt({p}))"),
        "[]byte" => format!("SkyFfiArg_bytes({p})"),
        "error" => format!("{p}.(error)"),
        _ => format!("{p}.({t})"),
    }
}

/// `packResults` (FfiGen.hs:1396).
fn pack_results(vs: &[String]) -> String {
    match vs {
        [] => "struct{}{}".to_string(),
        [v] => v.clone(),
        _ => format!("[]any{{{}}}", join(", ", vs)),
    }
}

/// `emitTypedCall` (FfiGen.hs:1320) — body of the any/any DirectCall wrapper.
fn emit_typed_call(fn_: &Function, params: &[(String, String)], results: &[(String, String)]) -> String {
    let name = &fn_.name;
    let method_n = &fn_.method_name;
    let n_params = params.len();
    let arg_exprs: Vec<String> = params
        .iter()
        .enumerate()
        .map(|(i, (_, t))| {
            let cast = typed_arg_cast(i, t);
            if fn_.variadic && i == n_params - 1 {
                format!("{cast}...")
            } else {
                cast
            }
        })
        .collect();
    let call = if method_n.is_empty() {
        format!("pkg.{name}({})", join(", ", &arg_exprs))
    } else {
        let recv_cast = match params.first() {
            Some((_, rt)) => typed_arg_cast(0, rt),
            None => "arg0".to_string(),
        };
        let method_args: Vec<String> = arg_exprs.iter().skip(1).cloned().collect();
        format!("{recv_cast}.{method_n}({})", join(", ", &method_args))
    };
    match results {
        [] => format!("\t{call}\n\tout = Ok[any, any](struct{{}}{{}})"),
        [(_, t)] => {
            if t == "error" {
                unlines(&[
                    format!("\terr := {call}"),
                    "\tif err != nil { out = Err[any, any](ErrFfi(err.Error())); return }".to_string(),
                    "\tout = Ok[any, any](struct{}{})".to_string(),
                ])
            } else {
                format!("\tout = Ok[any, any]({call})")
            }
        }
        _ => {
            let last_ty = &results[results.len() - 1].1;
            let others = &results[..results.len() - 1];
            let bind_vars: Vec<String> = (0..others.len()).map(|i| format!("r{i}")).collect();
            let mut all_vars = bind_vars.clone();
            if last_ty == "error" {
                all_vars.push("err".to_string());
            } else {
                all_vars.push(format!("r{}", bind_vars.len()));
            }
            let assign_line = format!("\t{} := {call}", join(", ", &all_vars));
            if last_ty == "error" {
                unlines(&[
                    assign_line,
                    "\tif err != nil { out = Err[any, any](ErrFfi(err.Error())); return }".to_string(),
                    format!("\tout = Ok[any, any]({})", pack_results(&bind_vars)),
                ])
            } else {
                unlines(&[
                    assign_line,
                    format!("\tout = Ok[any, any]([]any{{{}}})", join(", ", &all_vars)),
                ])
            }
        }
    }
}

// ---------------------------------------------------------------------------
// FfiT_* aliases + type-expressibility checks (FfiGen.hs:1546-1763).
// ---------------------------------------------------------------------------

/// `emitFfiTAliases` (FfiGen.hs:1546).
fn emit_ffi_t_aliases(any_name: &str, params: &[(String, String)], ok_type: &str) -> Vec<String> {
    let mut lines: Vec<String> = Vec::new();
    for (i, (_, t)) in params.iter().enumerate() {
        if needs_alias(t) {
            lines.push(format!("type FfiT_{any_name}_P{i} = {t}"));
        }
    }
    if needs_alias(ok_type) {
        lines.push(format!("type FfiT_{any_name}_R = {ok_type}"));
    }
    lines
}

/// `needsAlias` (FfiGen.hs:1564).
fn needs_alias(t: &str) -> bool {
    let bare = strip_leading_decor(t);
    bare.contains('.')
}

fn strip_leading_decor(t: &str) -> &str {
    t.trim_start_matches(['*', '[', ']', ' '])
}

/// `allPackagesKnown` (FfiGen.hs:1576).
fn all_packages_known(known: &BTreeSet<String>, t0: &str) -> bool {
    let t = strip_leading_decor(t0);
    extract_pkg_prefixes(t)
        .iter()
        .all(|p| p == "pkg" || p == "fmt" || known.contains(p))
}

/// `extractPkgPrefixes` (FfiGen.hs:1588) — every `<ident>.` package prefix.
fn extract_pkg_prefixes(s: &str) -> Vec<String> {
    let chars: Vec<char> = s.chars().collect();
    let n = chars.len();
    let mut acc: Vec<String> = Vec::new();
    let mut i = 0;
    while i < n {
        // span identChar
        let start = i;
        while i < n && (is_alnum(chars[i]) || chars[i] == '_') {
            i += 1;
        }
        let tok: String = chars[start..i].iter().collect();
        if tok.is_empty() {
            // skip one char
            if i < n {
                i += 1;
            }
            // (loop continues; matches `go cs acc`)
        } else if i < n && chars[i] == '.' && tok.chars().next().is_some_and(is_lower) {
            acc.push(tok);
            i += 1; // consume the '.'
        }
        // else: leave i at the non-ident char; next iteration's empty-tok
        //       branch advances past it (matches `go rest acc`).
    }
    acc
}

/// `isSimpleTypedType` (FfiGen.hs:1663).
fn is_simple_typed_type(t0: &str) -> bool {
    if t0 == "interface{}" {
        return true;
    }
    if let Some(rest) = t0.strip_prefix("[]") {
        return is_simple_typed_type(rest);
    }
    if let Some(rest) = t0.strip_prefix("func(") {
        if let Some((arg_types, ret_type)) = split_func(rest) {
            return arg_types.iter().all(|a| is_simple_typed_type(a))
                && (ret_type.is_empty() || is_simple_typed_type(&ret_type));
        }
        // splitFunc returned Nothing → fall through to the else branch.
    }
    if let Some(rest) = t0.strip_prefix("map[") {
        if let Some((k, v)) = split_map(rest) {
            return is_simple_typed_type(&k) && is_simple_typed_type(&v);
        }
    }
    // else branch.
    let t = t0.trim_start_matches('*');
    let t2 = t.strip_prefix("[]").unwrap_or(t);
    !t2.is_empty()
        && !t2.contains("func(")
        && !t2.contains("chan ")
        && !t2.contains("<-chan")
        && !t2.contains("chan<-")
        && !t2.contains("map[")
        && !t2.contains("...")
        && !t2.contains('[')
        && !is_bare_param_hs(t2)
        && t2.chars().all(is_type_char)
}

fn is_type_char(c: char) -> bool {
    is_alnum(c) || c == '.' || c == '_' || c == '*' || c == '/'
}

/// Split the body of `func(...)` (called AFTER the opening `(`). Returns
/// `(arg-types, return-type)`, matching `splitFunc` (FfiGen.hs:1713).
fn split_func(s: &str) -> Option<(Vec<String>, String)> {
    let chars: Vec<char> = s.chars().collect();
    let (inside, rest) = split_func_args(&chars);
    // rest must start with ')'
    let rest_chars: Vec<char> = rest.chars().collect();
    if rest_chars.first() == Some(&')') {
        let ret_raw: String = rest_chars[1..].iter().collect();
        let arg_types = if inside.is_empty() {
            Vec::new()
        } else {
            split_func_commas(&inside)
                .into_iter()
                .map(|piece| drop_param_name(&piece))
                .collect()
        };
        let ret = ret_raw.trim_start_matches(' ').to_string();
        Some((arg_types, ret))
    } else {
        None
    }
}

/// `splitFuncArgs` (FfiGen.hs:1739) — the balanced-paren scan up to the
/// matching top-level `)`. Returns `(inside, rest-from-')')`.
fn split_func_args(chars: &[char]) -> (String, String) {
    let mut acc = String::new();
    let mut d: i32 = 0;
    let mut i = 0;
    let n = chars.len();
    while i < n {
        let c = chars[i];
        if d == 0 && c == ')' {
            let rest: String = chars[i..].iter().collect();
            return (acc, rest);
        } else if c == '(' {
            d += 1;
            acc.push(c);
        } else if c == ')' {
            d -= 1;
            acc.push(c);
        } else {
            acc.push(c);
        }
        i += 1;
    }
    (acc, String::new())
}

/// `dropParamName` (FfiGen.hs:1728).
fn drop_param_name(piece: &str) -> String {
    let trimmed = piece.trim_start_matches(' ');
    match trimmed.find(' ') {
        Some(idx) => {
            let lhs = &trimmed[..idx];
            let rhs = &trimmed[idx + 1..];
            let is_plain_ident = !lhs.is_empty() && lhs.chars().all(|c| is_alnum(c) || c == '_');
            if !rhs.is_empty() && is_plain_ident {
                rhs.trim_start_matches(' ').to_string()
            } else {
                trimmed.to_string()
            }
        }
        None => trimmed.to_string(),
    }
}

/// `splitFuncCommas` (FfiGen.hs:1746) — split on top-level `,`, trimming each.
fn split_func_commas(inside: &str) -> Vec<String> {
    let chars: Vec<char> = inside.chars().collect();
    let mut pieces: Vec<String> = vec![String::new()];
    let mut d: i32 = 0;
    for &c in &chars {
        if d == 0 && c == ',' {
            pieces.push(String::new());
        } else if c == '(' || c == '[' {
            d += 1;
            pieces.last_mut().unwrap().push(c);
        } else if c == ')' || c == ']' {
            d -= 1;
            pieces.last_mut().unwrap().push(c);
        } else {
            pieces.last_mut().unwrap().push(c);
        }
    }
    pieces
        .into_iter()
        .map(|p| p.trim_matches(' ').to_string())
        .collect()
}

/// Split `<key>]<value>` for the content inside `map[...` (FfiGen.hs:1703).
fn split_map(s: &str) -> Option<(String, String)> {
    let chars: Vec<char> = s.chars().collect();
    let (k, rest) = split_at_closing_bracket(&chars);
    let rest_chars: Vec<char> = rest.chars().collect();
    if rest_chars.first() == Some(&']') {
        let v: String = rest_chars[1..].iter().collect();
        Some((k, v))
    } else {
        None
    }
}

fn split_at_closing_bracket(chars: &[char]) -> (String, String) {
    let mut acc = String::new();
    let mut d: i32 = 0;
    let mut i = 0;
    let n = chars.len();
    while i < n {
        let c = chars[i];
        if d == 0 && c == ']' {
            let rest: String = chars[i..].iter().collect();
            return (acc, rest);
        } else if c == '[' {
            d += 1;
            acc.push(c);
        } else if c == ']' {
            d -= 1;
            acc.push(c);
        } else {
            acc.push(c);
        }
        i += 1;
    }
    (acc, String::new())
}

// ---------------------------------------------------------------------------
// Wrapper classification (FfiGen.hs:1256).
// ---------------------------------------------------------------------------

enum WrapperClass {
    DirectCall,
    ReflectTopLevel,
    ReflectGeneric,
    ReflectMethod(String),
}

/// `wrapperClass` (FfiGen.hs:1256).
fn wrapper_class(fn_: &Function, rparams: &[(String, String)], rresults: &[(String, String)]) -> WrapperClass {
    let all_types: Vec<&String> = rparams.iter().map(|(_, t)| t).chain(rresults.iter().map(|(_, t)| t)).collect();
    let has_generic = all_types.iter().any(|t| crate::gen::is_generic_type(t))
        || all_types.iter().any(|t| has_generic_marker(t));
    let has_internal = all_types.iter().any(|t| touches_internal(t));

    if !fn_.method_name.is_empty() && (has_generic || has_internal) {
        WrapperClass::ReflectMethod(fn_.method_name.clone())
    } else if has_generic {
        WrapperClass::ReflectGeneric
    } else if has_internal {
        WrapperClass::ReflectTopLevel
    } else {
        WrapperClass::DirectCall
    }
}

fn has_generic_marker(t: &str) -> bool {
    t.contains("[T ")
        || t.contains("[T]")
        || t.contains("[T,")
        || t.contains("[K ")
        || t.contains("[V ")
        || t.contains("[]T")
        || t == "T"
        || t.ends_with("*T")
}

fn touches_internal(t: &str) -> bool {
    t.contains("/internal.") || t.contains("/internal/") || t.contains("/vendor.") || t.contains("/vendor/")
}

// ---------------------------------------------------------------------------
// Identity-pointer helper (FfiGen.hs:1292).
// ---------------------------------------------------------------------------

fn emit_identity_pointer_typed(wrapper_name: &str) -> String {
    unlines(&[
        "// Generic identity-pointer helper via reflect.".to_string(),
        format!("func {wrapper_name}(arg0 any) (out any) {{"),
        "\tdefer SkyFfiRecover(&out)()".to_string(),
        "\trv := reflectValueOfAny(arg0)".to_string(),
        "\tpv := reflectNewOf(rv.Type())".to_string(),
        "\tpv.Elem().Set(rv)".to_string(),
        "\tout = pv.Interface()".to_string(),
        "\treturn".to_string(),
        "}".to_string(),
    ])
}

// ---------------------------------------------------------------------------
// Typed-variant emission (FfiGen.hs:1414).
// ---------------------------------------------------------------------------

/// Classify a result list for typed emission (FfiGen.hs:1613). Returns
/// `(okGoType, isEffectful)`; the Haskell `pickExpr` is always `id`, so we drop
/// it and use the call expression directly at the single-result site.
fn classify_typed_result(results: &[(String, String)]) -> Option<(String, bool)> {
    let ts: Vec<&str> = results.iter().map(|(_, t)| t.as_str()).collect();
    match ts.as_slice() {
        [] => Some(("struct{}".to_string(), false)),
        ["error"] => Some(("struct{}".to_string(), true)),
        [t] if *t != "error" => Some((t.to_string(), false)),
        [t, "error"] if *t != "error" => Some((t.to_string(), true)),
        [t, "bool"] if *t != "error" && *t != "bool" => Some((format!("SkyMaybe[{t}]"), false)),
        [t1, t2] if *t1 != "error" && *t2 != "error" => Some(("SkyTuple2".to_string(), false)),
        [t1, t2, "error"] if *t1 != "error" && *t2 != "error" => Some(("SkyTuple2".to_string(), true)),
        [t1, t2, t3] if *t1 != "error" && *t2 != "error" && *t3 != "error" => {
            Some(("SkyTuple3".to_string(), false))
        }
        [t1, t2, t3, "error"] if *t1 != "error" && *t2 != "error" && *t3 != "error" => {
            Some(("SkyTuple3".to_string(), true))
        }
        _ => None,
    }
}

fn pack_tuple(xs: &[String]) -> String {
    match xs {
        [a] => a.clone(),
        [a, b] => format!("SkyTuple2{{V0: any({a}), V1: any({b})}}"),
        [a, b, c] => format!("SkyTuple3{{V0: any({a}), V1: any({b}), V2: any({c})}}"),
        _ => {
            let inner: Vec<String> = xs.iter().map(|x| format!("any({x})")).collect();
            format!("SkyTupleN{{Vs: []any{{{}}}}}", join(", ", &inner))
        }
    }
}

/// `emitTypedVariant` (FfiGen.hs:1414) — the strongly-typed `...T` wrapper.
#[allow(clippy::too_many_lines)]
fn emit_typed_variant(
    known_aliases: &BTreeSet<String>,
    any_name: &str,
    fn_: &Function,
    params: &[(String, String)],
    results: &[(String, String)],
) -> Option<String> {
    let is_method = !fn_.method_name.is_empty();
    let type_is_safe = |t: &str| is_simple_typed_type(t) && all_packages_known(known_aliases, t);

    if fn_.is_field || fn_.is_field_set || fn_.is_pkg_var {
        return None;
    }
    if params.iter().any(|(_, t)| !type_is_safe(t)) {
        return None;
    }
    if results.iter().any(|(_, t)| !type_is_safe(t)) {
        return None;
    }
    if is_method && params.is_empty() {
        return None;
    }

    let (ok_type, is_effectful) = classify_typed_result(results)?;

    let typed_name = format!("{any_name}T");
    let go_fn_name = &fn_.name;
    let method_n = &fn_.method_name;

    // Param declarations. Variadic last param becomes `[]X` unless already `[]`.
    let param_type_for = |t: &str, is_last: bool| -> String {
        if fn_.variadic && is_last {
            if t.starts_with("[]") {
                t.to_string()
            } else {
                format!("[]{t}")
            }
        } else {
            t.to_string()
        }
    };
    let n_params = params.len();
    let param_decls: Vec<String> = params
        .iter()
        .enumerate()
        .map(|(i, (_, t))| format!("arg{i} {}", param_type_for(t, i == n_params - 1)))
        .collect();
    let param_decls = join(", ", &param_decls);

    let spread_if_variadic = |i: usize| -> String {
        if fn_.variadic && i == n_params - 1 {
            format!("arg{i}...")
        } else {
            format!("arg{i}")
        }
    };
    let arg_refs: Vec<String> = (0..n_params).map(spread_if_variadic).collect();
    let arg_refs = join(", ", &arg_refs);
    let call = if is_method {
        let call_args: Vec<String> = (1..n_params).map(spread_if_variadic).collect();
        format!("arg0.{method_n}({})", join(", ", &call_args))
    } else {
        format!("pkg.{go_fn_name}({arg_refs})")
    };

    let recover_line = "\tdefer SkyFfiRecoverT(&out)()".to_string();

    let nil_recv_check = if is_method && !params.is_empty() && is_pointer_type(&params[0].1) {
        Some(format!(
            "\tif arg0 == nil {{ out = Err[any,{ok_type}](ErrFfi(\"nil receiver: {}.{method_n}\")); return }}",
            fn_.recv_type
        ))
    } else {
        None
    };

    let non_error_count = results.iter().filter(|(_, t)| t != "error").count();
    let r_names: Vec<String> = (0..non_error_count).map(|i| format!("r{i}")).collect();

    let body_lines: Vec<String> = if is_effectful {
        if results.len() == 1 {
            // single `error`
            vec![
                format!("\terr := {call}"),
                format!("\tif err != nil {{ out = Err[any,{ok_type}](ErrFfi(err.Error())); return }}"),
                format!("\tout = Ok[any,{ok_type}](struct{{}}{{}})"),
            ]
        } else {
            // (T, ..., error)
            let mut lhs_vars = r_names.clone();
            lhs_vars.push("err".to_string());
            vec![
                format!("\t{} := {call}", join(", ", &lhs_vars)),
                format!("\tif err != nil {{ out = Err[any,{ok_type}](ErrFfi(err.Error())); return }}"),
                format!("\tout = Ok[any,{ok_type}]({})", pack_tuple(&r_names)),
            ]
        }
    } else {
        let result_ts: Vec<&str> = results.iter().map(|(_, t)| t.as_str()).collect();
        match result_ts.as_slice() {
            [] => vec![
                format!("\t{call}"),
                format!("\tout = Ok[any,{ok_type}](struct{{}}{{}})"),
            ],
            [_] => vec![format!("\tout = Ok[any,{ok_type}]({call})")],
            // (T, bool) comma-ok → CommaOkToMaybe
            [t, "bool"] if *t != "bool" => vec![
                format!("\tr0, r1 := {call}"),
                format!("\tout = Ok[any,{ok_type}](CommaOkToMaybe(r0, r1))"),
            ],
            // (T, ...) without error
            _ => vec![
                format!("\t{} := {call}", join(", ", &r_names)),
                format!("\tout = Ok[any,{ok_type}]({})", pack_tuple(&r_names)),
            ],
        }
    };

    let alias_lines = emit_ffi_t_aliases(any_name, params, &ok_type);

    let mut lines: Vec<String> = Vec::new();
    lines.extend(alias_lines);
    lines.push(format!("// [{}] typed wrapper for {any_name} (P7 adaptor target)", fn_.effect));
    lines.push(format!("func {typed_name}({param_decls}) (out SkyResult[any, {ok_type}]) {{"));
    lines.push(recover_line);
    if let Some(nrc) = nil_recv_check {
        lines.push(nrc);
    }
    lines.extend(body_lines);
    lines.push("\treturn".to_string());
    lines.push("}".to_string());

    Some(unlines(&lines))
}

// ---------------------------------------------------------------------------
// Per-function wrapper (FfiGen.hs:967).
// ---------------------------------------------------------------------------

fn emit_typed_wrapper(kernel_name: &str, aliases: &BTreeMap<String, String>, fn_: &Function) -> String {
    let go_fn_name = &fn_.name;
    let sky_name = lower_first(go_fn_name);
    let wrapper_name = format!("{kernel_name}_{sky_name}");
    let n_args = fn_.params.len().max(1);

    let rparams: Vec<(String, String)> = fn_
        .params
        .iter()
        .map(|p| (p.name.clone(), rewrite_type(aliases, &p.ty)))
        .collect();
    let rresults: Vec<(String, String)> = fn_
        .results
        .iter()
        .map(|p| (p.name.clone(), rewrite_type(aliases, &p.ty)))
        .collect();

    let has_generic =
        rparams.iter().any(|(_, t)| crate::gen::is_generic_type(t)) || rresults.iter().any(|(_, t)| crate::gen::is_generic_type(t));
    let is_identity_pointer = has_generic
        && rparams.len() == 1
        && rresults.len() == 1
        && is_bare_param_hs(&rparams[0].1)
        && is_star_bare_param_hs(&rresults[0].1);

    let known_aliases: BTreeSet<String> = aliases.values().cloned().collect();

    // Guards (checked before the class match, mirroring the Haskell case).
    if is_identity_pointer {
        return emit_identity_pointer_typed(&wrapper_name);
    }

    if fn_.is_field {
        let field_name = &fn_.method_name;
        let receiver_type = rparams.first().map(|(_, t)| t.clone()).unwrap_or_default();
        let field_type = rresults.first().map(|(_, t)| t.clone()).unwrap_or_default();
        let receiver_ok = is_simple_typed_type(&receiver_type)
            && all_packages_known(&known_aliases, &receiver_type)
            && !receiver_type.is_empty();
        let field_expressible = is_simple_typed_type(&field_type)
            && all_packages_known(&known_aliases, &field_type)
            && !field_type.is_empty();
        let ok_type = if field_expressible {
            field_type.clone()
        } else {
            "any".to_string()
        };
        let mut typed_alias: Vec<String> = Vec::new();
        if needs_alias(&receiver_type) {
            typed_alias.push(format!("type FfiT_{wrapper_name}_P0 = {receiver_type}"));
        }
        if field_expressible && needs_alias(&field_type) {
            typed_alias.push(format!("type FfiT_{wrapper_name}_R = {field_type}"));
        }
        let typed_decl = format!(
            "func {wrapper_name}T(arg0 {receiver_type}) SkyResult[any, {ok_type}] {{ return Ok[any, {ok_type}](arg0.{field_name}) }}\n"
        );
        let any_decl = format!(
            "func {wrapper_name}(arg0 any) any {{ return SkyFfiFieldGet(arg0, {}) }}\n",
            quote(field_name)
        );
        return if receiver_ok {
            format!("{}{typed_decl}{any_decl}", unlines(&typed_alias))
        } else {
            any_decl
        };
    }

    if fn_.is_field_set {
        let field_name = &fn_.method_name;
        let raw_value_type = rparams.first().map(|(_, t)| t.clone()).unwrap_or_default();
        let receiver_type = rparams.get(1).map(|(_, t)| t.clone()).unwrap_or_default();
        let (sky_side_value, assign_expr) = match raw_value_type.strip_prefix('*') {
            Some(inner) => (
                inner.to_string(),
                format!("func() *{inner} {{ v := value; return &v }}()"),
            ),
            None => (raw_value_type.clone(), "value".to_string()),
        };
        let params_ok = is_simple_typed_type(&raw_value_type)
            && all_packages_known(&known_aliases, &raw_value_type)
            && is_simple_typed_type(&receiver_type)
            && all_packages_known(&known_aliases, &receiver_type)
            && !raw_value_type.is_empty()
            && !receiver_type.is_empty();
        let mut typed_alias_set: Vec<String> = Vec::new();
        if needs_alias(&sky_side_value) {
            typed_alias_set.push(format!("type FfiT_{wrapper_name}_P0 = {sky_side_value}"));
        }
        if needs_alias(&receiver_type) {
            typed_alias_set.push(format!("type FfiT_{wrapper_name}_P1 = {receiver_type}"));
        }
        let typed_decl_set = format!(
            "func {wrapper_name}T(value {sky_side_value}, recv {receiver_type}) SkyResult[any, {receiver_type}] {{ recv.{field_name} = {assign_expr}; return Ok[any, {receiver_type}](recv) }}\n"
        );
        let any_decl_set = format!(
            "func {wrapper_name}(value any, recv any) any {{ return SkyFfiFieldSet(value, recv, {}) }}\n",
            quote(field_name)
        );
        return if params_ok {
            format!("{}{typed_decl_set}{any_decl_set}", unlines(&typed_alias_set))
        } else {
            any_decl_set
        };
    }

    if fn_.is_pkg_var {
        let recv = &fn_.recv_type;
        let method = &fn_.method_name;
        return if !recv.is_empty() && method.is_empty() {
            // Zero-value struct constructor.
            format!("func {wrapper_name}(_ any) any {{ return Ok[any, any](new(pkg.{recv})) }}\n")
        } else if recv.is_empty() && !method.is_empty() {
            // Setter for a pkg-level var.
            format!(
                "func {wrapper_name}(value any) any {{ reflect.ValueOf(&pkg.{method}).Elem().Set(reflect.ValueOf(value).Convert(reflect.TypeOf(pkg.{method}))); return Ok[any, any](struct{{}}{{}}) }}\n"
            )
        } else {
            // Plain pkg-level var/const read.
            format!("func {wrapper_name}(_ any) any {{ return Ok[any, any](pkg.{go_fn_name}) }}\n")
        };
    }

    let effectful = rresults.iter().any(|(_, t)| t == "error");
    let has_err = if effectful { "true" } else { "false" };

    match wrapper_class(fn_, &rparams, &rresults) {
        WrapperClass::DirectCall => {
            match emit_typed_variant(&known_aliases, &wrapper_name, fn_, &rparams, &rresults) {
                Some(s) => s,
                None => {
                    let param_list: Vec<String> = (0..n_args).map(|i| format!("arg{i} any")).collect();
                    let param_list = join(", ", &param_list);
                    let unit_sink = if fn_.params.is_empty() {
                        "\t_ = arg0\n".to_string()
                    } else {
                        String::new()
                    };
                    let body = format!("{unit_sink}{}", emit_typed_call(fn_, &rparams, &rresults));
                    unlines(&[
                        format!("// [{}] {kernel_name}.{sky_name} → pkg.{go_fn_name}", fn_.effect),
                        format!("func {wrapper_name}({param_list}) (out any) {{"),
                        "\tdefer SkyFfiRecover(&out)()".to_string(),
                        body,
                        "\treturn".to_string(),
                        "}".to_string(),
                    ])
                }
            }
        }
        WrapperClass::ReflectTopLevel => {
            let target = format!("reflect.ValueOf(pkg.{go_fn_name})");
            reflect_call(kernel_name, &sky_name, &wrapper_name, n_args, &fn_.effect, has_err, &target)
        }
        WrapperClass::ReflectGeneric => {
            let underscore_param_list: Vec<String> = (0..n_args).map(|_| "_ any".to_string()).collect();
            let underscore_param_list = join(", ", &underscore_param_list);
            unlines(&[
                format!("// [{}] {kernel_name}.{sky_name} — generic with unknown constraint; stubbed as Err", fn_.effect),
                format!("func {wrapper_name}({underscore_param_list}) (out any) {{"),
                format!(
                    "\tout = Err[any, any]({})",
                    quote(&format!("generic function {go_fn_name} requires hand-written instantiation"))
                ),
                "\treturn".to_string(),
                "}".to_string(),
            ])
        }
        WrapperClass::ReflectMethod(method_name) => {
            let reflect_param_list: Vec<String> = (0..n_args).map(|i| format!("arg{i} any")).collect();
            let reflect_param_list = join(", ", &reflect_param_list);
            let reflect_method_args: Vec<String> = (1..n_args).map(|i| format!("arg{i}")).collect();
            let reflect_method_args_list = format!("[]any{{{}}}", join(", ", &reflect_method_args));
            unlines(&[
                format!(
                    "// [{}] {kernel_name}.{sky_name} → {}.{method_name} (receiver-reflect)",
                    fn_.effect, fn_.recv_type
                ),
                format!("func {wrapper_name}({reflect_param_list}) (out any) {{"),
                "\tdefer SkyFfiRecover(&out)()".to_string(),
                "\trecv := reflect.ValueOf(arg0)".to_string(),
                format!("\tm := recv.MethodByName({})", quote(&method_name)),
                "\tif !m.IsValid() {".to_string(),
                format!(
                    "\t\tout = Err[any, any]({})",
                    quote(&format!("{method_name}: no such method on receiver"))
                ),
                "\t\treturn".to_string(),
                "\t}".to_string(),
                format!("\tout = SkyFfiReflectCall(m, {has_err}, {reflect_method_args_list})"),
                "\treturn".to_string(),
                "}".to_string(),
            ])
        }
    }
}

/// The `reflectCall` closure (FfiGen.hs:1024).
fn reflect_call(
    kernel_name: &str,
    sky_name: &str,
    wrapper_name: &str,
    n_args: usize,
    effect: &str,
    has_err: &str,
    target: &str,
) -> String {
    let reflect_param_list: Vec<String> = (0..n_args).map(|i| format!("arg{i} any")).collect();
    let reflect_param_list = join(", ", &reflect_param_list);
    let reflect_args: Vec<String> = (0..n_args).map(|i| format!("arg{i}")).collect();
    let reflect_args_list = format!("[]any{{{}}}", join(", ", &reflect_args));
    unlines(&[
        format!("// [{effect}] {kernel_name}.{sky_name} → {target} (via SkyFfiReflectCall)"),
        format!("func {wrapper_name}({reflect_param_list}) (out any) {{"),
        "\tdefer SkyFfiRecover(&out)()".to_string(),
        format!("\tout = SkyFfiReflectCall({target}, {has_err}, {reflect_args_list})"),
        "\treturn".to_string(),
        "}".to_string(),
    ])
}

// ---------------------------------------------------------------------------
// Deduplication (FfiGen.hs:956).
// ---------------------------------------------------------------------------

/// `dedupByFirst` (FfiGen.hs:956) — drop FnInfos whose `lowerFirst` name is
/// already produced by an earlier entry.
fn dedup_by_first(fns: &[Function]) -> Vec<&Function> {
    let mut seen: BTreeSet<String> = BTreeSet::new();
    let mut out: Vec<&Function> = Vec::new();
    for fn_ in fns {
        let key = lower_first(&fn_.name);
        if seen.insert(key) {
            out.push(fn_);
        }
    }
    out
}

// ---------------------------------------------------------------------------
// Import block (FfiGen.hs:924).
// ---------------------------------------------------------------------------

/// `buildImportLinesFiltered` (FfiGen.hs:925).
fn build_import_lines_filtered(
    info: &PackageInfo,
    aliases: &BTreeMap<String, String>,
    any_emitted: bool,
    used: &BTreeSet<String>,
) -> Vec<String> {
    let self_path = &info.pkg;
    // Map.toList is already sorted by key (path); BTreeMap iterates sorted.
    let pkg_line = if any_emitted {
        format!("\tpkg {}", quote(self_path))
    } else {
        format!(
            "\t_ {}  // all bindings skipped; blank import retains go.mod dep",
            quote(self_path)
        )
    };
    let mut lines: Vec<String> = vec![pkg_line, "\t\"fmt\"".to_string()];
    for (path, alias) in aliases {
        if path == self_path {
            continue;
        }
        if used.contains(alias) {
            lines.push(format!("\t{alias} {}", quote(path)));
        } else {
            lines.push(format!(
                "\t_ {}  // aliased {alias}; unused in emitted wrappers",
                quote(path)
            ));
        }
    }
    lines
}

// ---------------------------------------------------------------------------
// Top-level file emitter (FfiGen.hs:845).
// ---------------------------------------------------------------------------

/// Emit the `<slug>_bindings.go` source. Mirrors `FfiGen.emitGoFile`.
pub(crate) fn emit_go_file(kernel_name: &str, info: &PackageInfo) -> String {
    let aliases = build_alias_table(info);
    let seen_names = dedup_by_first(&info.functions);
    let entries: Vec<String> = seen_names
        .iter()
        // A function taking a Go `error` PARAMETER has no callable wrapper: the
        // Sky surface maps `error` → `String`, but the wrapper would declare
        // `argN error`, so any call emits Go passing a `string` where an `error`
        // is required (`go build` rejects it). Such a function is inexpressible
        // from Sky (see `gen::has_error_param`); emit no wrapper so its Go symbol
        // never enters `go_symbols` and a call is rejected cleanly at lower time
        // (undefined FFI function) rather than breaking `go build`.
        .filter(|fn_| !crate::gen::has_error_param(fn_))
        .map(|fn_| emit_typed_wrapper(kernel_name, &aliases, fn_))
        .collect();
    let any_emitted = entries.iter().any(|e| !is_skipped_entry(e));

    let emitted_blob: String = entries.concat();
    let mut used_aliases: BTreeSet<String> = BTreeSet::new();
    used_aliases.insert("pkg".to_string());
    used_aliases.insert("fmt".to_string());
    for alias in aliases.values() {
        if emitted_blob.contains(&format!("{alias}.")) {
            used_aliases.insert(alias.clone());
        }
    }
    let uses_reflect = emitted_blob.contains("reflect.ValueOf");
    let reflect_in_aliases = aliases.contains_key("reflect");

    let mut import_lines = build_import_lines_filtered(info, &aliases, any_emitted, &used_aliases);
    if uses_reflect && !reflect_in_aliases {
        import_lines.push("\t\"reflect\"".to_string());
    }

    let module_name = crate::gen::pkg_to_module_name(&info.pkg);

    let mut lines: Vec<String> = Vec::new();
    lines.push(format!(
        "// Code generated by sky-ffi-inspect from {}. DO NOT EDIT.",
        info.pkg
    ));
    lines.push(format!("// Re-run `sky add {}` to regenerate.", info.pkg));
    lines.push("//".to_string());
    lines.push("// Wrapper functions are in `package rt` with names <Kernel>_<lowerFn>.".to_string());
    lines.push(format!(
        "// Sky source resolves `import {module_name} as X` and calls `X.<lowerFn>` — the canonicaliser routes it via"
    ));
    lines.push("// the FFI registry to these typed Go functions. Every wrapper wraps".to_string());
    lines.push("// panics in Err[any, any] via SkyFfiRecover.".to_string());
    lines.push(String::new());
    lines.push("package rt".to_string());
    lines.push(String::new());
    lines.push("import (".to_string());
    lines.extend(import_lines);
    lines.push(")".to_string());
    lines.push(String::new());
    lines.extend(entries);
    lines.push(String::new());
    lines.push("// Pin fmt against \"imported and not used\" across partial files.".to_string());
    lines.push("var _ = fmt.Sprintf".to_string());

    unlines(&lines)
}

/// `isSkippedEntry` (FfiGen.hs:1764).
fn is_skipped_entry(s: &str) -> bool {
    !s.contains("func ")
}

// ---------------------------------------------------------------------------
// Golden test — byte-compare against the committed `uuid.expected_bindings.go`.
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::Path;

    fn fixture(name: &str) -> String {
        let p = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("tests")
            .join("fixtures")
            .join(name);
        std::fs::read_to_string(p).unwrap()
    }

    /// Report the first ~40 differing lines, unified-diff style.
    fn first_diffs(got: &str, want: &str) -> String {
        let g: Vec<&str> = got.lines().collect();
        let w: Vec<&str> = want.lines().collect();
        let mut report = String::new();
        let mut shown = 0;
        let max = g.len().max(w.len());
        for i in 0..max {
            let gl = g.get(i).copied();
            let wl = w.get(i).copied();
            if gl != wl {
                report.push_str(&format!("@@ line {}\n", i + 1));
                report.push_str(&format!("- want: {:?}\n", wl.unwrap_or("<EOF>")));
                report.push_str(&format!("+ got:  {:?}\n", gl.unwrap_or("<EOF>")));
                shown += 1;
                if shown >= 40 {
                    report.push_str("... (truncated)\n");
                    break;
                }
            }
        }
        if got.len() != want.len() {
            report.push_str(&format!(
                "byte length differs: got {} vs want {}\n",
                got.len(),
                want.len()
            ));
        }
        report
    }

    #[test]
    fn uuid_bindings_byte_identical() {
        // NOTE: raw parsed order (no normalize) — the committed golden file is
        // in inspector order, and normalize would reorder the wrappers.
        let info = crate::inspect::parse_one(&fixture("uuid.inspector.json")).unwrap();
        let got = emit_go_file("Go_Uuid", &info);
        let want = fixture("uuid.expected_bindings.go");
        if got != want {
            panic!(
                "emit_go_file output differs from committed golden file:\n{}",
                first_diffs(&got, &want)
            );
        }
    }

    /// Byte-identity for a committed package. `inspector` is already normalised
    /// (functions name-sorted), so `parse_one` alone is faithful — no re-sort.
    fn assert_golden_matches(inspector: &str, golden: &str) {
        let info = crate::inspect::parse_one(&fixture(inspector)).unwrap();
        let kernel_name = crate::gen::kernel_name_from_pkg(&info.pkg);
        let got = emit_go_file(&kernel_name, &info);
        let want = fixture(golden);
        assert!(!want.is_empty(), "committed golden {golden} is empty");
        if got != want {
            panic!(
                "emit_go_file({kernel_name}) differs from committed {golden}:\n{}",
                first_diffs(&got, &want)
            );
        }
    }

    // gorilla/mux — methods (Router/Route receiver wrappers), structs, the
    // Router type. Locks arity / Sky-type / receiver mapping for a real
    // method-heavy package (was previously uncovered — only uuid was golden).
    #[test]
    fn mux_bindings_byte_identical() {
        assert_golden_matches("mux.inspector.json", "mux.expected_bindings.go");
    }

    // net/http — interfaces (Handler/ResponseWriter/…) + handler funcs. Locks
    // the interface-mapping + large-surface path.
    #[test]
    fn net_http_bindings_byte_identical() {
        assert_golden_matches("net_http.inspector.json", "net_http.expected_bindings.go");
    }

    // Part 2 — determinism: two emissions from the SAME committed inspector must
    // be byte-identical. Guards against a future HashMap-iteration-order leak in
    // the generator (alias table / used-import set are the risk sites).
    #[test]
    fn emit_go_file_is_deterministic() {
        let info = crate::inspect::parse_one(&fixture("mux.inspector.json")).unwrap();
        let kernel_name = crate::gen::kernel_name_from_pkg(&info.pkg);
        let a = emit_go_file(&kernel_name, &info);
        let b = emit_go_file(&kernel_name, &info);
        assert_eq!(a, b, "emit_go_file must be deterministic run-to-run");
    }
}
