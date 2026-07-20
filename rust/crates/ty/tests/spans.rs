//! Diagnostic-span regression (Phase 2 / Phase 3 gate). Proves E2001 (type
//! mismatch) and E3001 (non-exhaustive `case`) now carry a REAL source span —
//! the label anchors at the offending expression's line:col, never the old
//! `0:0` placeholder. Byte→line:col conversion here demonstrates the offset
//! maps to the source line the user actually wrote.

use hir::SourceDb;

/// 1-based (line, col) of a byte offset in `src` — the same arithmetic a
/// renderer uses to turn a `Span` into a caret position.
fn line_col(src: &str, byte: usize) -> (usize, usize) {
    let mut line = 1usize;
    let mut col = 1usize;
    for (i, ch) in src.char_indices() {
        if i >= byte {
            break;
        }
        if ch == '\n' {
            line += 1;
            col = 1;
        } else {
            col += 1;
        }
    }
    (line, col)
}

fn check_single(name: &str, src: &str) -> ty::CheckOutput {
    let mut db = SourceDb::new();
    let parse = syntax::parse(src, base::FileId(0));
    let mid = db.add_module(name, parse);
    ty::check_modules(&db, &[mid])
}

#[test]
fn e2001_carries_operand_span() {
    // `1 + "x"` — a String flows into the numeric `+`, a genuine unify clash.
    let src = "module Main exposing (foo)\n\nfoo = 1 + \"x\"\n";
    let out = check_single("Main", src);

    let e2001: Vec<_> = out
        .diagnostics
        .iter()
        .filter(|d| d.code.0 == "E2001")
        .collect();
    assert_eq!(
        out.type_errors, 1,
        "expected exactly one type error, diagnostics = {:?}",
        out.diagnostics
    );
    assert_eq!(e2001.len(), 1, "expected exactly one E2001");

    let d = e2001[0];
    assert_eq!(d.labels.len(), 1, "E2001 must carry one label, got {:?}", d.labels);
    let span = d.labels[0].span;
    let (start, end) = (span.range.0 as usize, span.range.1 as usize);

    // NOT the old 0:0 placeholder.
    assert_ne!((start, end), (0, 0), "span still at 0:0 — spans not wired");

    // The span sits on line 3 (`foo = 1 + "x"`), the line the user wrote.
    let (line, _col) = line_col(src, start);
    assert_eq!(line, 3, "span byte {start} maps to line {line}, expected 3");

    // And it slices back to the offending binop expression.
    assert_eq!(src[start..end].trim(), "1 + \"x\"", "span sliced {:?}", &src[start..end]);
}

#[test]
fn e3001_carries_subject_span() {
    // A non-exhaustive `case` over a user ADT — only `Red` is covered.
    let src = "module Main exposing (f)\n\
               \n\
               type Color = Red | Green\n\
               \n\
               f c =\n\
               \x20   case c of\n\
               \x20       Red -> 1\n";
    let out = check_single("Main", src);

    let e3001: Vec<_> = out
        .diagnostics
        .iter()
        .filter(|d| d.code.0 == "E3001")
        .collect();
    assert!(
        out.exhaustiveness_warnings >= 1,
        "expected a non-exhaustive warning, diagnostics = {:?}",
        out.diagnostics
    );
    assert_eq!(e3001.len(), 1, "expected exactly one E3001, got {:?}", e3001);

    let d = e3001[0];
    assert_eq!(d.labels.len(), 1, "E3001 must carry one label, got {:?}", d.labels);
    let span = d.labels[0].span;
    let (start, end) = (span.range.0 as usize, span.range.1 as usize);

    assert_ne!((start, end), (0, 0), "E3001 span still at 0:0 — not wired");

    // The subject is the `c` on the `case c of` line (line 6).
    let (line, _col) = line_col(src, start);
    assert_eq!(line, 6, "subject span byte {start} maps to line {line}, expected 6");
    assert_eq!(src[start..end].trim(), "c", "subject span sliced {:?}", &src[start..end]);
}
