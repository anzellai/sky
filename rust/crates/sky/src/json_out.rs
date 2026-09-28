//! `--format json` — machine-readable output for `sky check`, `sky build`,
//! `sky test` and `sky fmt --check`.
//!
//! The stream is NDJSON on stdout: one JSON object per line, every line
//! carrying `"schema": 1` and a `"kind"`:
//!
//! * `diagnostic` — LSP-shaped: `file` (relative to the project root, `/`
//!   separators, or `null`), `range` (0-based `line` / `character`, UTF-16, or
//!   `null`), `severity` (`error` / `warning` / `info`), `code` (`E2001`, or
//!   `null`), `message`, `source` (`sky` / `go`), and `relatedInformation`
//!   when the diagnostic has secondary locations.
//! * `test` — one per `Sky.Test` case (`sky test`).
//! * `summary` — exactly one, always the LAST line.
//!
//! Stdout carries ONLY that stream. While a json command runs, file
//! descriptor 1 is pointed at stderr, and the stream is written to a private
//! duplicate of the original stdout. So a progress line printed by any code
//! path, including a child process that inherits stdout, lands on stderr and
//! can never corrupt the stream. (On a non-unix host the redirect is skipped;
//! the json commands print no progress there.)
//!
//! The diagnostics come from the SAME `diagnostics::Reported` values the text
//! mode renders (`project::BuildReport::diagnostics`), so the two modes report
//! the same number of errors.

use project::diagnostics::{Reported, Severity};
use serde_json::Value;
use std::cell::RefCell;
use std::io::Write;
use std::process::ExitCode;
use std::time::Instant;

/// The schema version on every line. Bumped only on a breaking change to a
/// field's meaning or type; new fields do not bump it.
pub const SCHEMA: u64 = 1;

struct Sink {
    out: Box<dyn Write>,
    command: &'static str,
    started: Instant,
    root: Option<String>,
    errors: usize,
    warnings: usize,
    /// A failing `test` line was written: the failure is already explained.
    failed_tests: usize,
    extra: Vec<(String, String)>,
}

thread_local! {
    static SINK: RefCell<Option<Sink>> = const { RefCell::new(None) };
}

/// Whether a `--format json` command is running on this thread.
pub fn active() -> bool {
    SINK.with(|s| s.borrow().is_some())
}

/// Strip `--format <v>` / `--format=<v>` from `args`. `Ok(true)` for json,
/// `Ok(false)` for text or no flag; `Err` names an unknown value.
pub fn take_format(args: &[String]) -> Result<(Vec<String>, bool), String> {
    let mut rest = Vec::with_capacity(args.len());
    let mut json = false;
    let mut it = args.iter();
    while let Some(a) = it.next() {
        let v = if a == "--format" {
            match it.next() {
                Some(v) => v.clone(),
                None => return Err("--format needs a value: text or json".into()),
            }
        } else if let Some(v) = a.strip_prefix("--format=") {
            v.to_string()
        } else {
            rest.push(a.clone());
            continue;
        };
        json = match v.as_str() {
            "json" => true,
            "text" => false,
            other => return Err(format!("--format {other}: expected `text` or `json`")),
        };
    }
    Ok((rest, json))
}

/// Run `f` as a json command: redirect stdout, collect lines, and always end
/// with the summary line, whatever path `f` returns through. A failing exit
/// with no error diagnostic gets one, pointing at stderr, so a consumer never
/// sees `ok: false` with nothing to show for it.
pub fn run(command: &'static str, f: impl FnOnce() -> ExitCode) -> ExitCode {
    let _ = std::io::stdout().flush();
    let out = redirect_stdout();
    SINK.with(|s| {
        *s.borrow_mut() = Some(Sink {
            out,
            command,
            started: Instant::now(),
            root: None,
            errors: 0,
            warnings: 0,
            failed_tests: 0,
            extra: Vec::new(),
        })
    });
    let code = f();
    let _ = std::io::stdout().flush();
    let ok = code == ExitCode::SUCCESS;
    if !ok
        && SINK.with(|s| {
            s.borrow()
                .as_ref()
                .is_some_and(|k| k.errors == 0 && k.failed_tests == 0)
        })
    {
        diagnostic(&Reported::plain(
            Severity::Error,
            project::diagnostics::Origin::Sky,
            format!("sky {command} failed; the human-readable report is on stderr"),
        ));
    }
    SINK.with(|s| {
        if let Some(mut k) = s.borrow_mut().take() {
            let mut fields: Vec<(String, String)> = vec![
                ("kind".into(), enc("summary")),
                ("schema".into(), SCHEMA.to_string()),
                ("command".into(), enc(k.command)),
                ("ok".into(), ok.to_string()),
                ("errors".into(), k.errors.to_string()),
                ("warnings".into(), k.warnings.to_string()),
                (
                    "durationMs".into(),
                    (k.started.elapsed().as_millis() as u64).to_string(),
                ),
                (
                    "root".into(),
                    k.root.as_deref().map(enc).unwrap_or_else(|| "null".into()),
                ),
            ];
            fields.extend(std::mem::take(&mut k.extra));
            let _ = writeln!(k.out, "{}", obj(&fields));
            let _ = k.out.flush();
        }
    });
    code
}

/// Point fd 1 at stderr and return a writer on a private duplicate of the
/// original stdout (close-on-exec, so a child never inherits it).
#[cfg(unix)]
fn redirect_stdout() -> Box<dyn Write> {
    use std::os::fd::AsFd;
    let Ok(saved) = std::io::stdout().as_fd().try_clone_to_owned() else {
        return Box::new(std::io::stdout());
    };
    if nix::unistd::dup2(2, 1).is_err() {
        return Box::new(std::io::stdout());
    }
    Box::new(std::io::LineWriter::new(std::fs::File::from(saved)))
}

#[cfg(not(unix))]
fn redirect_stdout() -> Box<dyn Write> {
    Box::new(std::io::stdout())
}

/// Record the project root the `file` paths are relative to.
pub fn set_root(root: &std::path::Path) {
    SINK.with(|s| {
        if let Some(k) = s.borrow_mut().as_mut() {
            k.root = Some(root.to_string_lossy().to_string());
        }
    });
}

/// Add a field to the summary line (`sky test` adds its case counts).
pub fn summary_field(key: &str, v: Value) {
    SINK.with(|s| {
        if let Some(k) = s.borrow_mut().as_mut() {
            k.extra.push((key.to_string(), v.to_string()));
        }
    });
}

/// JSON-encode a string.
pub fn enc(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_else(|_| "\"\"".into())
}

/// An object from already-encoded values, in THIS order. `serde_json`'s map
/// sorts keys (or keeps insertion order, depending on a crate feature another
/// workspace member may switch on), so the wire order is fixed here instead.
pub fn obj<K: AsRef<str>>(fields: &[(K, String)]) -> String {
    let body: Vec<String> = fields
        .iter()
        .map(|(k, v)| format!("{}:{v}", enc(k.as_ref())))
        .collect();
    format!("{{{}}}", body.join(","))
}

fn range_json(r: &Option<project::diagnostics::TextRange>) -> String {
    match r {
        Some(r) => obj(&[
            (
                "start",
                obj(&[
                    ("line", r.start.line.to_string()),
                    ("character", r.start.character.to_string()),
                ]),
            ),
            (
                "end",
                obj(&[
                    ("line", r.end.line.to_string()),
                    ("character", r.end.character.to_string()),
                ]),
            ),
        ]),
        None => "null".into(),
    }
}

fn opt(s: &Option<String>) -> String {
    s.as_deref().map(enc).unwrap_or_else(|| "null".into())
}

/// The wire line for one diagnostic.
pub fn diagnostic_line(d: &Reported) -> String {
    let mut f: Vec<(&str, String)> = vec![
        ("kind", enc("diagnostic")),
        ("schema", SCHEMA.to_string()),
        ("file", opt(&d.file)),
        ("range", range_json(&d.range)),
        ("severity", enc(d.severity.as_str())),
        ("code", opt(&d.code)),
        ("message", enc(&d.message)),
        ("source", enc(d.origin.as_str())),
    ];
    if !d.related.is_empty() {
        let rel: Vec<String> = d
            .related
            .iter()
            .map(|r| {
                obj(&[
                    ("file", opt(&r.file)),
                    ("range", range_json(&r.range)),
                    ("message", enc(&r.message)),
                ])
            })
            .collect();
        f.push(("relatedInformation", format!("[{}]", rel.join(","))));
    }
    obj(&f)
}

/// Emit one diagnostic line (no-op outside a json command).
pub fn diagnostic(d: &Reported) {
    emit_line(&diagnostic_line(d), Some(d.severity.as_str()));
}

/// Emit an already-encoded line. `severity` is `Some` for a diagnostic line,
/// which the summary counts.
pub fn emit_line(line: &str, severity: Option<&str>) {
    SINK.with(|s| {
        if let Some(k) = s.borrow_mut().as_mut() {
            match severity {
                Some("error") => k.errors += 1,
                Some("warning") => k.warnings += 1,
                _ => {}
            }
            let _ = writeln!(k.out, "{line}");
        }
    });
}

/// Emit one `test` line; a failing case counts as the explanation of a
/// failing exit, so no generic error diagnostic is added for it.
pub fn test_line(line: &str, failed: bool) {
    SINK.with(|s| {
        if let Some(k) = s.borrow_mut().as_mut() {
            if failed {
                k.failed_tests += 1;
            }
            let _ = writeln!(k.out, "{line}");
        }
    });
}

/// Relay a child `sky … --format json` stream: forward its diagnostic lines
/// verbatim, unless `replace` returns a line to write instead (it sees the
/// parsed line), and drop its summary (the parent writes its own). Lines that
/// are not JSON are human text that leaked onto the child's stdout; they go to
/// stderr.
pub fn relay(child_stdout: &[u8], mut replace: impl FnMut(&Value) -> Option<String>) {
    for line in String::from_utf8_lossy(child_stdout).lines() {
        match serde_json::from_str::<Value>(line) {
            Ok(v) if v.get("kind").and_then(Value::as_str) == Some("summary") => {}
            Ok(v) => {
                let sev = v
                    .get("severity")
                    .and_then(Value::as_str)
                    .map(str::to_string);
                match replace(&v) {
                    Some(new) => emit_line(&new, sev.as_deref()),
                    None => emit_line(line, sev.as_deref()),
                }
            }
            Err(_) => eprintln!("{line}"),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use project::diagnostics::{LineCol, Origin, TextRange};

    #[test]
    fn take_format_strips_both_spellings() {
        let a: Vec<String> = ["check", "--format", "json", "src/Main.sky"]
            .iter()
            .map(|s| s.to_string())
            .collect();
        let (rest, json) = take_format(&a).unwrap();
        assert!(json);
        assert_eq!(rest, vec!["check", "src/Main.sky"]);
        let b: Vec<String> = ["--format=text"].iter().map(|s| s.to_string()).collect();
        assert_eq!(take_format(&b).unwrap(), (vec![], false));
        let c: Vec<String> = ["--format", "xml"].iter().map(|s| s.to_string()).collect();
        assert!(take_format(&c).is_err());
        let d: Vec<String> = ["--format"].iter().map(|s| s.to_string()).collect();
        assert!(take_format(&d).is_err());
    }

    /// The golden for one diagnostic line: the exact bytes a consumer parses.
    #[test]
    fn diagnostic_line_golden() {
        let mut d = Reported::plain(
            Severity::Error,
            Origin::Sky,
            "[main] type mismatch: `String` vs `Int`",
        );
        d.code = Some("E2001".into());
        d.file = Some("src/Main.sky".into());
        d.range = Some(TextRange {
            start: LineCol {
                line: 6,
                character: 4,
            },
            end: LineCol {
                line: 6,
                character: 10,
            },
        });
        let line = diagnostic_line(&d);
        assert_eq!(
            line,
            r#"{"kind":"diagnostic","schema":1,"file":"src/Main.sky","range":{"start":{"line":6,"character":4},"end":{"line":6,"character":10}},"severity":"error","code":"E2001","message":"[main] type mismatch: `String` vs `Int`","source":"sky"}"#
        );
    }
}
