//! `xtask s8` — the CLAUDE.md §8 forbidden-pattern gate.
//!
//! Enforces the non-regression rules that public Sky surfaces must not use:
//!   * `Result String a` / `Task String a` — String as the ERROR type (the
//!     error slot is the FIRST type argument, so `Result String …` /
//!     `Task String …` is the violation; `Result Error String` — String as the
//!     VALUE — is fine).
//!   * `Std.IoError` / bare `IoError` — deleted pre-v1.
//!   * `RemoteData` — deleted pre-v1.
//!
//! This restores the enforcement the Haskell `sky verify` / cabal specs carried,
//! which the Rust toolchain had dropped. It is a pure source lint over
//! `sky-stdlib/` + `examples/` (comments stripped, generated dirs skipped) — no
//! IR / codegen / golden interaction, so it is parity-neutral.

use std::path::{Path, PathBuf};

/// A `(pattern-label, needle)` the scan rejects when it appears in code (not a
/// comment). Needles are matched on whitespace-normalised code so `Result   String`
/// and a line-wrapped `Result String` both trip.
const FORBIDDEN: &[(&str, &str)] = &[
    ("Result String (String-as-error)", "Result String"),
    ("Task String (String-as-error)", "Task String"),
    ("Std.IoError (deleted pre-v1)", "Std.IoError"),
    ("IoError (deleted pre-v1)", "IoError"),
    ("RemoteData (deleted pre-v1)", "RemoteData"),
];

struct Violation {
    file: PathBuf,
    line_no: usize,
    label: String,
    line: String,
}

pub fn run(_args: &[String], repo_root: &Path) -> i32 {
    let mut files = Vec::new();
    for root in ["sky-stdlib", "examples"] {
        collect_sky(&repo_root.join(root), &mut files);
    }
    files.sort();

    let mut violations = Vec::new();
    for f in &files {
        let Ok(text) = std::fs::read_to_string(f) else {
            continue;
        };
        for (i, raw) in text.lines().enumerate() {
            let code = strip_comment(raw);
            if code.trim().is_empty() {
                continue;
            }
            let norm = normalise_ws(code);
            for (label, needle) in FORBIDDEN {
                if norm.contains(needle) {
                    violations.push(Violation {
                        file: f.clone(),
                        line_no: i + 1,
                        label: (*label).to_string(),
                        line: raw.trim().to_string(),
                    });
                }
            }
        }
    }

    if violations.is_empty() {
        println!(
            "S8 GATE: PASS  (no forbidden public-surface pattern in {} .sky file(s))",
            files.len()
        );
        return 0;
    }

    println!("S8 GATE: FAIL — {} forbidden-pattern use(s):", violations.len());
    for v in &violations {
        let rel = v.file.strip_prefix(repo_root).unwrap_or(&v.file);
        println!("  {}:{}  [{}]", rel.display(), v.line_no, v.label);
        println!("    {}", v.line);
    }
    println!(
        "\nCLAUDE.md §8: use `Result Error a` / `Task Error a`; `Std.IoError` and \
         `RemoteData` were deleted pre-v1."
    );
    1
}

/// Strip a `--` line comment (outside a string literal) so a forbidden pattern
/// documented in prose (e.g. Sky.Core.Error's "no more `Result String a`") never
/// trips the gate. Conservative: a `--` inside a `"…"` string is kept, but a
/// double-quote count is only a heuristic — good enough for the annotation lines
/// this gate targets.
fn strip_comment(line: &str) -> &str {
    let bytes = line.as_bytes();
    let mut in_str = false;
    let mut i = 0;
    while i < bytes.len() {
        match bytes[i] {
            b'"' => in_str = !in_str,
            b'-' if !in_str && i + 1 < bytes.len() && bytes[i + 1] == b'-' => {
                return &line[..i];
            }
            _ => {}
        }
        i += 1;
    }
    line
}

/// Collapse runs of whitespace to a single space so a multi-space or wrapped
/// `Result String` still matches the single-space needle.
fn normalise_ws(s: &str) -> String {
    s.split_whitespace().collect::<Vec<_>>().join(" ")
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for path in entries {
        let skip = path.components().any(|c| {
            matches!(
                c.as_os_str().to_str(),
                Some("sky-out")
                    | Some("sky-out-rust")
                    | Some(".skycache")
                    | Some(".skydeps")
                    | Some(".sky-stdlib")
            )
        });
        if skip {
            continue;
        }
        if path.is_dir() {
            collect_sky(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn strips_line_comments() {
        assert_eq!(strip_comment("foo -- Result String bar"), "foo ");
        assert_eq!(strip_comment("x : Result String Int"), "x : Result String Int");
        // A `--` inside a string literal is not a comment.
        assert_eq!(strip_comment("s = \"a -- b\""), "s = \"a -- b\"");
    }

    #[test]
    fn ws_normalises() {
        assert!(normalise_ws("Result   String").contains("Result String"));
        assert!(normalise_ws("Task\tString").contains("Task String"));
    }
}
