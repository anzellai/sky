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
    // Everything written to stderr during the run is kept, so a failure that
    // produced no diagnostic is explained by its real message rather than a
    // pointer to stderr (F-15).
    let capture = StderrCapture::start();
    let code = run_with(out, command, f, &|| capture.as_ref().map(|c| c.text()));
    drop(capture);
    code
}

/// The body of [`run`], writing the stream to `out`. `stderr_text` returns
/// what the run printed on stderr so far, when that was captured. A panic in
/// `f` still ends the stream with an error diagnostic and the summary: the
/// stream promises a summary line last whatever happens (F-16).
pub fn run_with(
    out: Box<dyn Write>,
    command: &'static str,
    f: impl FnOnce() -> ExitCode,
    stderr_text: &dyn Fn() -> Option<String>,
) -> ExitCode {
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
    // The one-time upgrade notice is a record of the stream, never stderr text
    // mixed into a json run (`version_notice`).
    if let Some(n) = crate::version_notice::take_pending() {
        emit_line(&crate::version_notice::json_line(&n), None);
    }
    let code = match std::panic::catch_unwind(std::panic::AssertUnwindSafe(f)) {
        Ok(code) => code,
        Err(payload) => {
            let what = payload
                .downcast_ref::<&str>()
                .map(|s| s.to_string())
                .or_else(|| payload.downcast_ref::<String>().cloned())
                .unwrap_or_else(|| "a panic with no message".to_string());
            diagnostic(&Reported::plain(
                Severity::Error,
                project::diagnostics::Origin::Sky,
                format!("internal compiler error in sky {command}: {what}. Please report it."),
            ));
            ExitCode::from(101)
        }
    };
    let _ = std::io::stdout().flush();
    let ok = code == ExitCode::SUCCESS;
    if !ok
        && SINK.with(|s| {
            s.borrow()
                .as_ref()
                .is_some_and(|k| k.errors == 0 && k.failed_tests == 0)
        })
    {
        let _ = std::io::stderr().flush();
        let message = stderr_text()
            .map(|t| last_lines(&t, 20))
            .filter(|t| !t.is_empty())
            .map(|t| format!("sky {command} failed: {t}"))
            .unwrap_or_else(|| {
                format!("sky {command} failed; the human-readable report is on stderr")
            });
        diagnostic(&Reported::plain(
            Severity::Error,
            project::diagnostics::Origin::Sky,
            message,
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

/// The last `n` non-blank lines of `text`, joined by newlines, with any
/// terminal colour codes removed.
fn last_lines(text: &str, n: usize) -> String {
    let plain = strip_ansi(text);
    let lines: Vec<&str> = plain
        .lines()
        .map(str::trim_end)
        .filter(|l| !l.trim().is_empty())
        .collect();
    let start = lines.len().saturating_sub(n);
    lines[start..].join("\n")
}

fn strip_ansi(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut chars = s.chars().peekable();
    while let Some(c) = chars.next() {
        if c == '\u{1b}' && chars.peek() == Some(&'[') {
            chars.next();
            for d in chars.by_ref() {
                if d.is_ascii_alphabetic() {
                    break;
                }
            }
        } else {
            out.push(c);
        }
    }
    out
}

/// A copy of everything written to file descriptors 1 and 2 while a json
/// command runs (both point at stderr then). The bytes still reach the real
/// stderr through a pump thread; the copy is what [`run`] reads when a
/// failure produced no diagnostic. `None` when the pipe cannot be set up (the
/// run then falls back to pointing at stderr) and on non-unix hosts.
struct StderrCapture {
    #[cfg(unix)]
    saved: std::os::fd::OwnedFd,
    buf: std::sync::Arc<std::sync::Mutex<Vec<u8>>>,
    #[cfg(unix)]
    done: std::sync::mpsc::Receiver<()>,
}

impl StderrCapture {
    #[cfg(unix)]
    fn start() -> Option<Self> {
        use std::io::Read;
        use std::os::fd::{AsFd, AsRawFd};
        let _ = std::io::stderr().flush();
        let saved = std::io::stderr().as_fd().try_clone_to_owned().ok()?;
        let (r, w) = nix::unistd::pipe().ok()?;
        nix::unistd::dup2(w.as_raw_fd(), 2).ok()?;
        if nix::unistd::dup2(w.as_raw_fd(), 1).is_err() {
            let _ = nix::unistd::dup2(saved.as_raw_fd(), 2);
            return None;
        }
        drop(w);
        let buf = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
        let copy = buf.clone();
        let mut real = std::fs::File::from(saved.try_clone().ok()?);
        let (tx, done) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let mut reader = std::fs::File::from(r);
            let mut chunk = [0u8; 8192];
            while let Ok(n) = reader.read(&mut chunk) {
                if n == 0 {
                    break;
                }
                let _ = real.write_all(&chunk[..n]);
                if let Ok(mut b) = copy.lock() {
                    // Keep the tail only: a long build log is not the error.
                    b.extend_from_slice(&chunk[..n]);
                    let over = b.len().saturating_sub(64 * 1024);
                    b.drain(..over);
                }
            }
            let _ = tx.send(());
        });
        Some(StderrCapture { saved, buf, done })
    }

    #[cfg(not(unix))]
    fn start() -> Option<Self> {
        None
    }

    /// What the run has printed so far.
    fn text(&self) -> String {
        let _ = std::io::stderr().flush();
        // Give the pump a moment to copy what was just written.
        std::thread::sleep(std::time::Duration::from_millis(20));
        self.buf
            .lock()
            .map(|b| String::from_utf8_lossy(&b).into_owned())
            .unwrap_or_default()
    }
}

#[cfg(unix)]
impl Drop for StderrCapture {
    fn drop(&mut self) {
        use std::os::fd::AsRawFd;
        let _ = std::io::stderr().flush();
        // Point 1 and 2 back at the real stderr; the pipe's last writer closes
        // and the pump ends (bounded: a child still holding the pipe must not
        // hang sky's exit).
        let _ = nix::unistd::dup2(self.saved.as_raw_fd(), 2);
        let _ = nix::unistd::dup2(self.saved.as_raw_fd(), 1);
        let _ = self
            .done
            .recv_timeout(std::time::Duration::from_millis(500));
    }
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

/// Re-encode a relayed `diagnostic` line with a new `file` and extra keys
/// appended, keeping the wire key order (a parsed `Value` sorts its keys).
pub fn rewrite_diagnostic(v: &Value, file: Option<String>, extra: &[(&str, String)]) -> String {
    let range_of = |r: &Value| -> String {
        if !r.is_object() {
            return "null".into();
        }
        let pos = |p: &Value| {
            obj(&[
                ("line", p["line"].to_string()),
                ("character", p["character"].to_string()),
            ])
        };
        obj(&[("start", pos(&r["start"])), ("end", pos(&r["end"]))])
    };
    let mut f: Vec<(&str, String)> = vec![
        ("kind", enc("diagnostic")),
        ("schema", SCHEMA.to_string()),
        (
            "file",
            file.as_deref().map(enc).unwrap_or_else(|| "null".into()),
        ),
        ("range", range_of(&v["range"])),
        ("severity", v["severity"].to_string()),
        ("code", v["code"].to_string()),
        ("message", v["message"].to_string()),
        ("source", v["source"].to_string()),
    ];
    if let Some(rel) = v["relatedInformation"].as_array() {
        let items: Vec<String> = rel
            .iter()
            .map(|r| {
                obj(&[
                    ("file", r["file"].to_string()),
                    ("range", range_of(&r["range"])),
                    ("message", r["message"].to_string()),
                ])
            })
            .collect();
        f.push(("relatedInformation", format!("[{}]", items.join(","))));
    }
    f.extend(extra.iter().cloned());
    obj(&f)
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

    /// A relayed line re-encodes in wire order (a parsed `Value` sorts its keys,
    /// `character` before `line`), with the new `file` and the extra keys last.
    #[test]
    fn rewrite_diagnostic_keeps_the_wire_order() {
        let child = r#"{"kind":"diagnostic","schema":1,"file":"src/A.sky","range":{"start":{"line":2,"character":3},"end":{"line":2,"character":5}},"severity":"warning","code":null,"message":"m","source":"sky"}"#;
        let v: Value = serde_json::from_str(child).unwrap();
        let out = rewrite_diagnostic(&v, Some("src/B.sky".into()), &[("half", enc("backend"))]);
        assert_eq!(
            out,
            r#"{"kind":"diagnostic","schema":1,"file":"src/B.sky","range":{"start":{"line":2,"character":3},"end":{"line":2,"character":5}},"severity":"warning","code":null,"message":"m","source":"sky","half":"backend"}"#
        );
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

#[cfg(test)]
mod run_tests {
    use super::*;
    use std::sync::{Arc, Mutex};

    struct Shared(Arc<Mutex<Vec<u8>>>);
    impl Write for Shared {
        fn write(&mut self, b: &[u8]) -> std::io::Result<usize> {
            self.0.lock().unwrap().extend_from_slice(b);
            Ok(b.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }

    fn lines_of(buf: &Arc<Mutex<Vec<u8>>>) -> Vec<Value> {
        String::from_utf8(buf.lock().unwrap().clone())
            .unwrap()
            .lines()
            .map(|l| serde_json::from_str(l).expect("NDJSON"))
            .collect()
    }

    /// F-16: a panic inside a json command still ends the stream with an
    /// error diagnostic and a summary line with `ok: false`.
    #[test]
    fn a_panic_still_ends_the_stream_with_a_summary() {
        let buf = Arc::new(Mutex::new(Vec::new()));
        let code = run_with(
            Box::new(Shared(buf.clone())),
            "check",
            || panic!("boom"),
            &|| None,
        );
        assert_ne!(code, ExitCode::SUCCESS);
        let lines = lines_of(&buf);
        let last = lines.last().expect("a summary line");
        assert_eq!(last["kind"], "summary");
        assert_eq!(last["ok"], false);
        assert_eq!(last["errors"], 1);
        assert!(lines[0]["message"]
            .as_str()
            .unwrap()
            .contains("internal compiler error"));
        assert!(lines[0]["message"].as_str().unwrap().contains("boom"));
    }

    /// F-15: a failure with no diagnostic carries what the command printed
    /// on stderr, not only a pointer to it.
    #[test]
    fn a_failure_without_a_diagnostic_carries_the_real_message() {
        let buf = Arc::new(Mutex::new(Vec::new()));
        let code = run_with(
            Box::new(Shared(buf.clone())),
            "check",
            || ExitCode::FAILURE,
            &|| Some("progress\n\u{1b}[31msky: no such file: src/Nope.sky\u{1b}[0m\n".to_string()),
        );
        assert_eq!(code, ExitCode::FAILURE);
        let lines = lines_of(&buf);
        let msg = lines[0]["message"].as_str().unwrap();
        assert!(msg.contains("no such file: src/Nope.sky"), "{msg}");
        assert!(!msg.contains('\u{1b}'), "no colour codes: {msg}");
        assert_eq!(lines.last().unwrap()["ok"], false);
    }
}
