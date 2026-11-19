//! Coverage points (`sky test --coverage`).
//!
//! A coverage build records one point per top-level Sky function and one per
//! `case` arm. Each lowered point puts a marker statement into the Go-IR: a
//! [`GoStmt::Comment`](crate::ir::GoStmt::Comment) whose text starts with
//! [`MARK_PREFIX`]. Codegen renders a marker as NOTHING and returns the
//! `main.go` line and column where it stood (`codegen::emit_program_marked`),
//! so a coverage build emits the same Go bytes as an ordinary build. The build
//! driver joins those positions with the Go coverage profile
//! (`go build -cover`): a point is hit when the innermost profile block that
//! holds its position ran.
//!
//! Why markers and not `//line file.sky:N` directives: `go build -cover`
//! ignores `//line` (the profile keeps the real `main.go` positions), and the
//! build driver maps `go build` errors back by `main.go:LINE:COL`, which a
//! `//line` directive would break. An ordinary build records nothing and its
//! Go-IR holds no marker, so its output is byte-identical to a build without
//! this feature.

/// The text a marker comment starts with. The control character cannot occur
/// in a comment the lowerer writes for any other reason.
pub const MARK_PREFIX: &str = "\u{1}skycov:";

/// What a coverage point counts.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum CovKind {
    /// A top-level Sky definition. Hit when its Go body ran.
    Function,
    /// One `case` arm. Hit when the arm's body ran.
    Arm,
}

/// One coverage point of the program.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct CovPoint {
    /// The marker id (its text after [`MARK_PREFIX`]), unique in the program.
    /// `None` for a top-level definition the program never lowered (no code
    /// path reaches it, so it can never run): it is reported as not hit.
    pub mark: Option<usize>,
    pub kind: CovKind,
    /// The dotted Sky module name.
    pub module: String,
    /// The top-level definition the point belongs to (an arm of a `case`
    /// inside a `let`-bound helper belongs to the enclosing definition).
    pub function: String,
    /// The definition's name, or the arm's pattern.
    pub span: Option<base::Span>,
}

/// The marker comment text for id `id`.
pub fn mark_text(id: usize) -> String {
    format!("{MARK_PREFIX}{id}")
}

/// The marker id a comment carries, if it is a marker.
pub fn mark_id(comment: &str) -> Option<usize> {
    comment.strip_prefix(MARK_PREFIX)?.parse().ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_marker_round_trips_and_an_ordinary_comment_is_not_one() {
        assert_eq!(mark_id(&mark_text(42)), Some(42));
        assert_eq!(mark_id("SKY-ORIGIN: entry"), None);
        assert_eq!(mark_id("skycov:3"), None);
    }
}
