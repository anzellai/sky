//! `diagnostics` — the `Diagnostic` value type + one Elm-style renderer shared
//! by the CLI and the LSP (doc 02, law L7: errors are values, diagnostics are
//! data — never exceptions for control flow).
//!
//! The value shape (code, severity, message, labels, suggested fix) is the
//! stable data; `render_cli` turns it into the Elm-style terminal block and the
//! LSP maps it into its JSON shape. Byte↔line:col conversion lives here as
//! `line_starts`/`position_at` so the CLI renderer and the LSP position mapper
//! share ONE implementation (the LSP re-uses these; it keeps no private copy).
//!
//! `#![forbid(unsafe_code)]` is intentionally omitted here because the renderer
//! may pull in a dependency; the *frontend* purity crates keep the forbid
//! (doc 02).

use base::{FileId, Span};
use std::collections::HashMap;

/// Severity of a diagnostic.
#[derive(Copy, Clone, PartialEq, Eq, Hash, Debug)]
pub enum Severity {
    Error,
    Warning,
    Info,
}

/// A stable diagnostic code, e.g. `E1001`, `E2007` (mirrors stage-0 codes).
#[derive(Clone, PartialEq, Eq, Hash, Debug)]
pub struct Code(pub String);

/// A labelled span within a diagnostic (the primary or a secondary highlight).
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Label {
    pub span: Span,
    pub message: String,
}

/// A structured diagnostic — a value, not an exception (L7). Every query
/// returns `(result, Vec<Diagnostic>)` so errors never short-circuit a build.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Diagnostic {
    pub severity: Severity,
    pub code: Code,
    pub message: String,
    pub labels: Vec<Label>,
    /// Optional machine-applicable fix-it text (doc 07 / L7).
    pub suggestion: Option<String>,
}

impl Diagnostic {
    pub fn error(code: &str, message: impl Into<String>) -> Self {
        Diagnostic {
            severity: Severity::Error,
            code: Code(code.to_string()),
            message: message.into(),
            labels: Vec::new(),
            suggestion: None,
        }
    }

    pub fn with_label(mut self, span: Span, message: impl Into<String>) -> Self {
        self.labels.push(Label {
            span,
            message: message.into(),
        });
        self
    }
}

// ---- source access ----------------------------------------------------

/// Read access to source text (and, optionally, a display path) keyed by
/// `FileId`, so the renderer can excerpt the offending line for a label — even a
/// secondary label pointing into a *different* file than the primary.
///
/// The required `text` method is enough for the common single-file diagnostic;
/// `path` is defaulted to `None` (the header then shows a bare `line:col`), and
/// a provider that knows the file's name/path overrides it for a full
/// `path:line:col` header.
pub trait SourceProvider {
    fn text(&self, file: FileId) -> Option<&str>;
    fn path(&self, _file: FileId) -> Option<&str> {
        None
    }
}

/// Blanket impl: a `FileId → text` map is the simplest provider (path unknown).
impl SourceProvider for &HashMap<FileId, String> {
    fn text(&self, file: FileId) -> Option<&str> {
        self.get(&file).map(String::as_str)
    }
}

// ---- byte ↔ line:col (shared with the LSP) ----------------------------

/// Byte offsets of the start of each line (index 0 = line 0). `O(n)` over the
/// text; callers that convert many offsets should hoist it.
pub fn line_starts(text: &str) -> Vec<usize> {
    let mut v = vec![0usize];
    for (i, b) in text.bytes().enumerate() {
        if b == b'\n' {
            v.push(i + 1);
        }
    }
    v
}

/// Byte offset → `(0-based line, 0-based UTF-16 column)`. The column counts
/// UTF-16 code units to match the LSP `Position` contract; the CLI renderer adds
/// 1 to each for its 1-based `line:col` header. Offsets past the end clamp to the
/// end of the text.
pub fn position_at(text: &str, offset: u32) -> (u32, u32) {
    let starts = line_starts(text);
    let off = (offset as usize).min(text.len());
    let line = match starts.binary_search(&off) {
        Ok(i) => i,
        Err(i) => i.saturating_sub(1),
    };
    let col: usize = text[starts[line]..off].chars().map(|c| c.len_utf16()).sum();
    (line as u32, col as u32)
}

/// A 0-based `(line, UTF-16 character)` position: the LSP `Position` contract,
/// kept here as plain data so the LSP and the CLI's `--format json` output map
/// a span through ONE function ([`span_range`]).
#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub struct LineCol {
    pub line: u32,
    pub character: u32,
}

/// A half-open `[start, end)` range of [`LineCol`]s (the LSP `Range` shape).
#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub struct TextRange {
    pub start: LineCol,
    pub end: LineCol,
}

/// Byte span → 0-based LSP-shaped range. The LSP's `span_to_range` and the
/// CLI's JSON diagnostics both call this, so an editor and a CI tool that read
/// the same diagnostic see the same range.
pub fn span_range(text: &str, span: Span) -> TextRange {
    let (sl, sc) = position_at(text, span.range.0);
    let (el, ec) = position_at(text, span.range.1);
    TextRange {
        start: LineCol {
            line: sl,
            character: sc,
        },
        end: LineCol {
            line: el,
            character: ec,
        },
    }
}

impl Severity {
    /// The lower-case name used on the wire (`--format json`): `error`,
    /// `warning`, `info`.
    pub fn as_str(self) -> &'static str {
        match self {
            Severity::Error => "error",
            Severity::Warning => "warning",
            Severity::Info => "info",
        }
    }
}

/// Which tool produced a [`Reported`] diagnostic: the Sky compiler, or `go
/// build` over the emitted Go.
#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub enum Origin {
    Sky,
    Go,
}

impl Origin {
    pub fn as_str(self) -> &'static str {
        match self {
            Origin::Sky => "sky",
            Origin::Go => "go",
        }
    }
}

/// A secondary location of a [`Reported`] diagnostic (LSP `relatedInformation`).
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct RelatedLocation {
    pub file: Option<String>,
    pub range: Option<TextRange>,
    pub message: String,
}

/// A diagnostic RESOLVED against its sources: the file is a display path and
/// the span is a line/character range, so it can leave the process. This is the
/// one value both CLI output modes read. The human mode prints `rendered`; the
/// `--format json` mode prints the other fields. Both come from the same
/// [`Diagnostic`] in [`Reported::from_diagnostic`], so the two modes cannot
/// disagree on how many diagnostics there are or where they point.
///
/// `file` and `range` are `None` for a diagnostic with no source span: a
/// lowering warning, a config warning, a driver failure such as "no entry
/// module".
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Reported {
    pub severity: Severity,
    pub code: Option<String>,
    pub message: String,
    pub file: Option<String>,
    pub range: Option<TextRange>,
    pub origin: Origin,
    pub related: Vec<RelatedLocation>,
    /// The human rendering (the Elm-style block for a spanned Sky diagnostic,
    /// the bare message otherwise).
    pub rendered: String,
}

impl Reported {
    /// Resolve a structured Sky diagnostic. The primary label gives `file` and
    /// `range`; the other labels become `related`.
    pub fn from_diagnostic(d: &Diagnostic, sources: &dyn SourceProvider) -> Self {
        let locate = |span: Span| {
            let file = sources.path(span.file).map(str::to_string);
            let range = sources.text(span.file).map(|t| span_range(t, span));
            (file, range)
        };
        let (file, range) = d
            .labels
            .first()
            .map(|l| locate(l.span))
            .unwrap_or((None, None));
        let related = d
            .labels
            .iter()
            .skip(1)
            .map(|l| {
                let (file, range) = locate(l.span);
                RelatedLocation {
                    file,
                    range,
                    message: l.message.clone(),
                }
            })
            .collect();
        Reported {
            severity: d.severity,
            code: Some(d.code.0.clone()),
            message: d.message.clone(),
            file,
            range,
            origin: Origin::Sky,
            related,
            rendered: d.render_cli(sources),
        }
    }

    /// A diagnostic with no source span (a note the driver or the lowering
    /// produced as text). `rendered` is the message itself.
    pub fn plain(severity: Severity, origin: Origin, message: impl Into<String>) -> Self {
        let message = message.into();
        Reported {
            severity,
            code: None,
            rendered: message.clone(),
            message,
            file: None,
            range: None,
            origin,
            related: Vec::new(),
        }
    }
}

// ---- Elm-style CLI renderer -------------------------------------------

/// The width the header rules pad to (the `-- TITLE ----- path:line:col` line).
const HEADER_WIDTH: usize = 76;

/// Code → human title shown in the `-- TITLE …` header rule.
fn code_title(code: &str) -> &'static str {
    match code {
        "E0001" => "PARSE ERROR",
        "E1001" => "NAMING ERROR",
        "E1002" => "DUPLICATE DEFINITION",
        "E1003" => "DUPLICATE PATTERN VARIABLE",
        "E1004" => "SHADOWED NAME",
        "E1005" => "INTEGER OVERFLOW",
        "E1006" => "UNSUPPORTED PATTERN",
        "E1010" => "IMPORT CYCLE",
        "E1011" => "NOT EXPOSED",
        "E1012" => "AMBIGUOUS NAME",
        "E1013" => "OPAQUE TYPE",
        "E1014" => "UNKNOWN OPERATOR",
        "E1015" => "UNKNOWN EXPORT",
        "E1016" => "RECURSIVE TYPE ALIAS",
        "E2001" => "TYPE ERROR",
        "E2007" => "ARITY MISMATCH",
        // E2001-E2007 are allocated by the Haskell oracle
        // (`legacy-haskell-compiler/src/Sky/Reporting/Diagnostic.hs:183-206`);
        // E2008 is the first free code in the type-error band.
        "E2008" => "UNSUPPORTED DICT KEY",
        "E2009" => "UN-DERIVABLE CODEC ELEMENT",
        "E2010" => "FORM SUBMIT HANDLER",
        "E2011" => "PUB/SUB PAYLOAD MISMATCH",
        "E2012" => "VALUE RESTRICTION",
        "E3001" => "MISSING PATTERNS",
        "E4005" => "CODEGEN ERROR",
        _ => "ERROR",
    }
}

/// `-- TITLE -------------------------------- path:line:col`, padded to
/// [`HEADER_WIDTH`]. When there is no location, the rule fills the whole width.
fn header_line(title: &str, loc: Option<&str>, code: &str) -> String {
    let prefix = format!("-- {title} ");
    // Tail carries the location (if known) and always the error code, matching
    // the oracle's `-- PARSE ERROR ---- src/Main.sky:5:1 [E0001]`.
    let tail_str = match loc {
        Some(loc) => format!("{loc} [{code}]"),
        None => format!("[{code}]"),
    };
    let tail = tail_str.len() + 1; // one space before the tail
    let dashes = HEADER_WIDTH.saturating_sub(prefix.len() + tail).max(1);
    format!("{prefix}{} {tail_str}", "-".repeat(dashes))
}

/// How many source lines of preceding context to show above the caret line.
const CONTEXT_LINES: usize = 2;

/// The source excerpt + caret for one label span, with a small window of
/// preceding context lines (matching the Haskell oracle), e.g.
///
/// ```text
///    5 | main : Int
///    6 | main =
///    7 |     "not an int"
///      |     ^
/// ```
///
/// The caret run sits under `[range.0, range.1)` clamped to the label's first
/// line. Leading whitespace inside the span is trimmed so the caret is tight
/// (spans may carry leading trivia). Line numbers are right-aligned in a gutter
/// whose width is the widest line number shown, followed by ` | `. Returns
/// `None` when the file text is unavailable, so the caller falls back to a
/// text-only block.
fn excerpt(text: &str, span: Span) -> Option<String> {
    let starts = line_starts(text);
    let (line0, _) = position_at(text, span.range.0);
    let caret_idx = line0 as usize;
    let line_start = *starts.get(caret_idx)?;
    let line_end = line_bounds_end(&starts, text, caret_idx);

    // Byte offsets of the span within the caret line, clamped to the line.
    let mut sb = (span.range.0 as usize).clamp(line_start, line_end);
    let eb = (span.range.1 as usize).clamp(sb, line_end);
    // Trim leading whitespace inside the span for a tight caret.
    while sb < eb && text[sb..].chars().next().is_some_and(char::is_whitespace) {
        sb += text[sb..].chars().next().unwrap().len_utf8();
    }

    // Display columns = char counts (tabs render as one cell here — a pragmatic
    // choice matching the oracle's caret alignment on space-indented source).
    let col_start = text[line_start..sb].chars().count();
    let caret_len = text[sb..eb].chars().count().max(1);

    // Gutter width = width of the largest (1-based) line number shown.
    let gutter_w = (caret_idx + 1).to_string().len();
    let first_idx = caret_idx.saturating_sub(CONTEXT_LINES);

    let mut out = String::new();
    for li in first_idx..=caret_idx {
        let ls = starts[li];
        let le = line_bounds_end(&starts, text, li);
        let ltext = &text[ls..le];
        out.push_str(&format!("{:>gutter_w$} | {ltext}\n", li + 1));
    }
    // The caret line: blank gutter, ` | `, then spaces to the caret column.
    out.push_str(&format!(
        "{} | {}{}",
        " ".repeat(gutter_w),
        " ".repeat(col_start),
        "^".repeat(caret_len)
    ));
    Some(out)
}

/// End byte of line `idx` (0-based), with the trailing `\n` dropped. Falls back
/// to the text length for the final line.
fn line_bounds_end(starts: &[usize], text: &str, idx: usize) -> usize {
    starts
        .get(idx + 1)
        .map(|&s| s.saturating_sub(1))
        .unwrap_or(text.len())
}

impl Diagnostic {
    /// Render this diagnostic as an Elm-style terminal block:
    ///
    /// ```text
    /// -- TYPE MISMATCH ------------------------------------- src/Main.sky:7:14
    ///
    /// 7|     println (String.fromInt (1 + "x"))
    ///                                      ^^^
    /// [Main] Type mismatch — expected Int, found String
    /// ```
    ///
    /// followed by any secondary `labels[1..]` blocks and a `Try: <suggestion>`
    /// line. `sources` supplies the offending source line(s) (and, optionally,
    /// the display path used in the header).
    pub fn render_cli(&self, sources: &dyn SourceProvider) -> String {
        let title = code_title(&self.code.0);
        let primary = self.labels.first();

        // Header location: `path:line:col` (or bare `line:col` when the provider
        // has no path). 1-based line + column.
        let loc = primary.and_then(|l| {
            let text = sources.text(l.span.file)?;
            let (line0, col0) = position_at(text, l.span.range.0);
            let where_ = match sources.path(l.span.file) {
                Some(p) => format!("{p}:{}:{}", line0 + 1, col0 + 1),
                None => format!("{}:{}", line0 + 1, col0 + 1),
            };
            Some(where_)
        });

        let mut out = String::new();
        out.push_str(&header_line(title, loc.as_deref(), &self.code.0));
        out.push('\n');

        // Primary excerpt (if we can read the file).
        if let Some(l) = primary {
            if let Some(text) = sources.text(l.span.file) {
                if let Some(block) = excerpt(text, l.span) {
                    out.push('\n');
                    out.push_str(&block);
                    out.push('\n');
                }
            }
        }

        // The message.
        out.push('\n');
        out.push_str(&self.message);
        out.push('\n');

        // Secondary labels as related blocks.
        for l in self.labels.iter().skip(1) {
            out.push('\n');
            out.push_str(&l.message);
            out.push('\n');
            if let Some(text) = sources.text(l.span.file) {
                if let Some(block) = excerpt(text, l.span) {
                    out.push('\n');
                    out.push_str(&block);
                    out.push('\n');
                }
            }
        }

        // Suggested fix.
        if let Some(s) = &self.suggestion {
            out.push('\n');
            out.push_str(&format!("Try: {s}"));
            out.push('\n');
        }

        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use base::FileId;

    #[test]
    fn builds_a_structured_diagnostic() {
        let d = Diagnostic::error("E1001", "two imports bind the qualifier")
            .with_label(Span::new(FileId(0), 0, 5), "here");
        assert_eq!(d.severity, Severity::Error);
        assert_eq!(d.code.0, "E1001");
        assert_eq!(d.labels.len(), 1);
    }

    /// `Reported::from_diagnostic` is the one resolution both CLI output modes
    /// read: its `rendered` IS `render_cli`, and its range is `span_range`.
    #[test]
    fn reported_carries_the_rendering_and_the_lsp_range() {
        let src = "module Main exposing (main)\nmain = 1 + \"x\"\n";
        let start = src.find("\"x\"").unwrap() as u32;
        let d = Diagnostic::error("E2001", "mismatch")
            .with_label(Span::new(FileId(0), start, start + 3), "here")
            .with_label(Span::new(FileId(0), 0, 6), "declared here");
        let sources = NamedSource {
            path: "src/Main.sky".to_string(),
            text: src.to_string(),
        };
        let r = Reported::from_diagnostic(&d, &sources);
        assert_eq!(r.rendered, d.render_cli(&sources));
        assert_eq!(r.file.as_deref(), Some("src/Main.sky"));
        assert_eq!(r.code.as_deref(), Some("E2001"));
        assert_eq!(r.origin, Origin::Sky);
        let range = r.range.unwrap();
        assert_eq!((range.start.line, range.start.character), (1, 11));
        assert_eq!((range.end.line, range.end.character), (1, 14));
        assert_eq!(r.related.len(), 1);
        assert_eq!(r.related[0].message, "declared here");
        assert_eq!(
            span_range(src, Span::new(FileId(0), start, start + 3)),
            range
        );
    }

    #[test]
    fn position_at_maps_offsets() {
        let text = "abc\ndef\n";
        assert_eq!(position_at(text, 0), (0, 0));
        assert_eq!(position_at(text, 2), (0, 2));
        assert_eq!(position_at(text, 4), (1, 0));
        assert_eq!(position_at(text, 6), (1, 2));
    }

    /// A provider that carries both text and a display path (the CLI's shape).
    struct NamedSource {
        path: String,
        text: String,
    }
    impl SourceProvider for NamedSource {
        fn text(&self, _file: FileId) -> Option<&str> {
            Some(&self.text)
        }
        fn path(&self, _file: FileId) -> Option<&str> {
            Some(&self.path)
        }
    }

    #[test]
    fn render_cli_produces_the_elm_block() {
        // A known source; the span covers `"x"` on line 2 (0-based line 1).
        //   line 0: `module Main exposing (main)`
        //   line 1: `main = 1 + "x"`
        let src = "module Main exposing (main)\nmain = 1 + \"x\"\n";
        let start = src.find("\"x\"").unwrap() as u32;
        let end = start + 3;
        let d = Diagnostic {
            severity: Severity::Error,
            code: Code("E2001".to_string()),
            message: "[Main] Type mismatch — expected Int, found String".to_string(),
            labels: vec![Label {
                span: Span::new(FileId(0), start, end),
                message: "this expression".to_string(),
            }],
            suggestion: None,
        };
        let sources = NamedSource {
            path: "src/Main.sky".to_string(),
            text: src.to_string(),
        };
        let rendered = d.render_cli(&sources);

        // `"x"` starts at 0-based column 11 → 1-based col 12, on line 2. The
        // window shows the one preceding line (line 1) plus the caret line, with
        // a right-aligned `N | ` gutter and a `  | ^^^` caret row.
        let expected = "\
-- TYPE ERROR ------------------------------------ src/Main.sky:2:12 [E2001]

1 | module Main exposing (main)
2 | main = 1 + \"x\"
  |            ^^^

[Main] Type mismatch — expected Int, found String
";
        assert_eq!(
            rendered, expected,
            "\n--- got ---\n{rendered}\n--- want ---\n{expected}"
        );
    }

    /// The excerpt shows up to two PRECEDING context lines above the caret line
    /// (the oracle's window), so an annotated-binding mismatch whose caret sits
    /// on the RHS line still displays the `name : T` / `name =` lines above it.
    #[test]
    fn render_cli_shows_context_window() {
        // line 1: `main : Int`     (0-based 0)
        // line 2: `main =`         (0-based 1)
        // line 3: `    "nope"`     (0-based 2) — the caret line
        let src = "main : Int\nmain =\n    \"nope\"\n";
        let start = src.find("\"nope\"").unwrap() as u32;
        let end = start + 6;
        let d = Diagnostic {
            severity: Severity::Error,
            code: Code("E2001".to_string()),
            message: "[main] type mismatch: `String` vs `Int`".to_string(),
            labels: vec![Label {
                span: Span::new(FileId(0), start, end),
                message: "this expression".into(),
            }],
            suggestion: None,
        };
        let sources = NamedSource {
            path: "src/Main.sky".to_string(),
            text: src.to_string(),
        };
        let rendered = d.render_cli(&sources);

        // Header carries the RHS location (line 3, col 5) + the path.
        assert!(
            rendered.contains("src/Main.sky:3:5 [E2001]"),
            "header should point at the RHS line:col with a path: {rendered}"
        );
        // Both preceding context lines present, with a `N | ` gutter.
        assert!(
            rendered.contains("1 | main : Int"),
            "ctx line 1: {rendered}"
        );
        assert!(rendered.contains("2 | main ="), "ctx line 2: {rendered}");
        assert!(
            rendered.contains("3 |     \"nope\""),
            "caret line: {rendered}"
        );
        // The caret sits under the opening quote (col 5), on a blank-gutter row.
        assert!(rendered.contains("\n  |     ^"), "caret row: {rendered}");
    }

    #[test]
    fn render_cli_renders_suggestion_and_secondary_label() {
        let src = "a = 1\nb = 2\n";
        let d = Diagnostic {
            severity: Severity::Error,
            code: Code("E2001".to_string()),
            message: "primary message".to_string(),
            labels: vec![
                Label {
                    span: Span::new(FileId(0), 0, 1),
                    message: "primary here".to_string(),
                },
                Label {
                    span: Span::new(FileId(0), 6, 7),
                    message: "also relevant here".to_string(),
                },
            ],
            suggestion: Some("rename `b`".to_string()),
        };
        let map: HashMap<FileId, String> = [(FileId(0), src.to_string())].into_iter().collect();
        let rendered = d.render_cli(&(&map));

        // No path in the map → bare `line:col` header.
        assert!(rendered.contains("-- TYPE ERROR "), "header: {rendered}");
        assert!(
            rendered.contains(" 1:1"),
            "bare line:col header: {rendered}"
        );
        assert!(rendered.contains("primary message"));
        assert!(rendered.contains("also relevant here"));
        assert!(rendered.contains("Try: rename `b`"));
        assert!(rendered.contains("1 | a = 1"));
        assert!(rendered.contains("2 | b = 2"));
    }
}
