//! A CAF accessor holds no function literal.
//!
//! A zero-parameter top-level binding (a CAF) is memoised through
//! `rt.LazyCaf`. The accessor is a one-line function, so the Go inliner copies
//! it into every caller. When the accessor held the whole initialiser as a
//! closure literal (`return X__caf.Get(func() T { … })`), each inlined copy
//! also copied that closure and every closure nested inside it, and the Go
//! back end compiled the whole tree once per call site. A generated Sky.Spa
//! backend at 200 Model fields compiled 100,681 functions, 82,216 of them
//! copies of one codec initialiser (62 s and 6.1 GB in `go build`).
//!
//! The initialiser is now its own named function (`X__cafInit`), and the
//! accessor passes it by name: `return X__caf.Get(X__cafInit)`. Go inlines
//! only direct calls, so the initialiser is compiled exactly once.

use base::FileId;
use codegen::emit_program;
use hir::SourceDb;

/// The text of the top-level Go function `name` in `src`, from its `func`
/// line to the closing `}` at column 0.
fn go_func<'a>(src: &'a str, name: &str) -> Option<&'a str> {
    let head = format!("\nfunc {name}(");
    let start = src.find(&head)? + 1;
    let rest = &src[start..];
    let end = rest.find("\n}\n")? + 3;
    Some(&rest[..end])
}

#[test]
fn caf_accessor_holds_no_func_literal() {
    let main = "module Main exposing (main)\n\n\
                double : Int -> Int\ndouble x =\n    x * 2\n\n\
                table : Int\ntable =\n    (\\x -> double x + 1) 20\n\n\
                main =\n    table\n";
    let mut db = SourceDb::new();
    let mid = db.add_module("Main", syntax::parse(main, FileId(0)));
    let out = lower::lower_program(&db, mid);
    assert!(out.errors.is_empty(), "lowering errors: {:?}", out.errors);
    let go = emit_program(&out.items, false);

    let accessor = go_func(&go, "Main_table")
        .unwrap_or_else(|| panic!("no accessor `Main_table` in emitted Go:\n{go}"));
    assert!(
        accessor.contains("Main_table__caf.Get("),
        "the accessor reads the memo cell:\n{accessor}"
    );
    assert!(
        !accessor.contains("func("),
        "a CAF accessor must not hold a function literal (the Go inliner copies it \
         into every caller):\n{accessor}"
    );
    assert!(
        accessor.contains("Main_table__caf.Get(Main_table__cafInit)"),
        "the accessor passes the named initialiser:\n{accessor}"
    );
    let init = go_func(&go, "Main_table__cafInit")
        .unwrap_or_else(|| panic!("no initialiser `Main_table__cafInit`:\n{go}"));
    assert!(
        init.contains("Main_double("),
        "the initialiser holds the original body:\n{init}"
    );
    // Exactly one definition of each.
    assert_eq!(go.matches("\nfunc Main_table__cafInit(").count(), 1);
    assert_eq!(go.matches("\nfunc Main_table(").count(), 1);
}
