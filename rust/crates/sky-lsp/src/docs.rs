//! Pure text helpers behind hover's documentation: the `-- |` / `{-| -}` doc
//! comment above a declaration, a declaration's source text as a hover code
//! block, and the definitions of the compiler-builtin types that have no Sky
//! source (`Maybe`, `Result`, `List`, …).

/// The doc comment directly above the declaration starting at byte `start` of
/// `text`, markers stripped. Blank lines between the comment and the declaration
/// are allowed. A line-comment block counts only from its LAST `-- |` line (so a
/// section banner `-- ─── … ───` above a doc is not part of it); a block comment
/// counts only when it opens with `{-|`. Plain comments are not documentation —
/// the same convention `sky doc` reads.
pub(crate) fn doc_comment_before(text: &str, start: usize) -> Option<String> {
    let before = text.get(..start)?;
    let mut lines: Vec<&str> = before.lines().collect();
    // `lines()` drops a trailing empty segment; the declaration's own line
    // prefix (indentation, or nothing) is not a comment line either way.
    if !before.ends_with('\n') {
        lines.pop();
    }
    while lines.last().is_some_and(|l| l.trim().is_empty()) {
        lines.pop();
    }
    let last = lines.last()?.trim();
    if last.ends_with("-}") {
        // Block comment: walk up to its opener.
        let end = lines.len();
        let mut i = end;
        while i > 0 {
            i -= 1;
            if lines[i].trim_start().starts_with("{-") {
                break;
            }
        }
        let first = lines[i].trim_start();
        let body = first.strip_prefix("{-|")?;
        let mut out: Vec<String> = Vec::new();
        let mut all: Vec<&str> = vec![body];
        all.extend(lines[i + 1..end].iter().copied());
        for l in all {
            let l = l.trim_end();
            let l = l.strip_suffix("-}").unwrap_or(l);
            out.push(l.to_string());
        }
        return finish(out);
    }
    if !last.starts_with("--") {
        return None;
    }
    let mut i = lines.len();
    while i > 0 && lines[i - 1].trim_start().starts_with("--") {
        i -= 1;
    }
    let block = &lines[i..];
    let doc_start = block.iter().rposition(|l| {
        let t = l.trim_start();
        t.starts_with("-- |") || t.starts_with("--|")
    })?;
    let mut out = Vec::new();
    for (k, l) in block[doc_start..].iter().enumerate() {
        let t = l.trim_start();
        let stripped = if k == 0 {
            t.strip_prefix("-- |")
                .or_else(|| t.strip_prefix("--|"))
                .unwrap_or(t)
        } else {
            t.strip_prefix("--").unwrap_or(t)
        };
        out.push(stripped.strip_prefix(' ').unwrap_or(stripped).to_string());
    }
    finish(out)
}

/// Dedent + trim a collected doc body; `None` when nothing is left.
fn finish(lines: Vec<String>) -> Option<String> {
    let lines: Vec<String> = dedent(&lines);
    let s = lines.join("\n").trim().to_string();
    (!s.is_empty()).then_some(s)
}

fn dedent(lines: &[String]) -> Vec<String> {
    let indent = lines
        .iter()
        .skip(1)
        .filter(|l| !l.trim().is_empty())
        .map(|l| l.len() - l.trim_start().len())
        .min()
        .unwrap_or(0);
    lines
        .iter()
        .enumerate()
        .map(|(i, l)| {
            if i == 0 {
                l.trim().to_string()
            } else {
                l.get(indent.min(l.len() - l.trim_start().len())..)
                    .unwrap_or(l)
                    .trim_end()
                    .to_string()
            }
        })
        .collect()
}

/// A declaration's source slice as hover shows it: trailing whitespace trimmed
/// per line, blank lines collapsed, and an over-long record alias kept whole
/// (the definition IS what the user asked to see).
pub(crate) fn decl_source(slice: &str) -> String {
    let mut out: Vec<&str> = Vec::new();
    for l in slice.lines() {
        let l = l.trim_end();
        if l.is_empty() && out.last().is_some_and(|p| p.is_empty()) {
            continue;
        }
        out.push(l);
    }
    while out.last().is_some_and(|l| l.is_empty()) {
        out.pop();
    }
    out.join("\n")
}

/// The definition + doc of a compiler-builtin type that has no Sky source
/// declaration (hir's `BUILTIN_TYPES`). Constructors mirror `BUILTIN_CTORS`.
pub(crate) fn builtin_type(name: &str) -> Option<(&'static str, &'static str)> {
    Some(match name {
        "Int" => ("type Int", "A whole number (a Go `int`)."),
        "Float" => ("type Float", "A floating-point number (a Go `float64`)."),
        "String" => ("type String", "A UTF-8 text value."),
        "Char" => ("type Char", "A single Unicode character."),
        "Bool" => (
            "type Bool\n    = True\n    | False",
            "A boolean: `True` or `False`.",
        ),
        "List" => (
            "type List a",
            "An immutable list of values of the same type.",
        ),
        "Maybe" => (
            "type Maybe a\n    = Just a\n    | Nothing",
            "An optional value: `Just` a value, or `Nothing`.",
        ),
        "Result" => (
            "type Result error value\n    = Ok value\n    | Err error",
            "The result of a computation that may fail: `Ok` a value, or `Err` an error.",
        ),
        "Task" => (
            "type Task error value",
            "A description of an effect that, when run, either fails with `error` or \
             succeeds with `value`.",
        ),
        _ => return None,
    })
}

/// The builtin union a builtin constructor belongs to (hir's `BUILTIN_CTORS`).
pub(crate) fn builtin_ctor_parent(ctor: &str) -> Option<&'static str> {
    Some(match ctor {
        "Just" | "Nothing" => "Maybe",
        "Ok" | "Err" => "Result",
        "True" | "False" => "Bool",
        _ => return None,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn doc(src: &str, needle: &str) -> Option<String> {
        doc_comment_before(src, src.find(needle).unwrap())
    }

    #[test]
    fn line_doc_after_banner() {
        let src = "x = 1\n\n-- ─── banner ───\n-- | `map f xs` — apply.\n--   more\nmap : Int\n";
        assert_eq!(
            doc(src, "map :").as_deref(),
            Some("`map f xs` — apply.\nmore")
        );
    }

    #[test]
    fn blank_line_between_doc_and_decl() {
        let src = "-- | A user.\n\ntype alias User = {}\n";
        assert_eq!(doc(src, "type alias").as_deref(), Some("A user."));
    }

    #[test]
    fn plain_comment_is_not_doc() {
        let src = "-- helper\nf = 1\n";
        assert_eq!(doc(src, "f ="), None);
    }

    #[test]
    fn block_doc() {
        let src = "{-| Adds one.\n\n    Example.\n-}\ninc : Int -> Int\n";
        assert_eq!(doc(src, "inc :").as_deref(), Some("Adds one.\n\nExample."));
    }

    #[test]
    fn doc_does_not_leak_across_decls() {
        let src = "-- | A.\na = 1\nb = 2\n";
        assert_eq!(doc(src, "b ="), None);
    }
}
