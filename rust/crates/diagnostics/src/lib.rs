//! `diagnostics` — the `Diagnostic` value type + one Elm-style renderer shared
//! by the CLI and the LSP (doc 02, law L7: errors are values, diagnostics are
//! data — never exceptions for control flow).
//!
//! M0 stub: the shape is here (structured value: code, severity, span, labels,
//! suggested fix); rendering via `annotate-snippets` is wired in a later
//! milestone. `#![forbid(unsafe_code)]` is intentionally omitted here because a
//! future renderer may pull in a dependency; the *frontend* purity crates keep
//! the forbid (doc 02).

use base::Span;

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
}
