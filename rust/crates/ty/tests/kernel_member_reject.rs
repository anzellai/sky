//! Resolve-time reject of an UNKNOWN qualified kernel member, and the positive
//! counterpart: a real member (`List.sortWith`) resolves — ambiently AND via
//! `exposing` — with a PRECISE type from its new `.sky` Def.
//!
//! Before this change, a qualified `Mod.fn` for ANY `fn` minted an unvalidated
//! `Res::Kernel` (a `fresh_flex` type, `rt.Mod_fn` lowering). If `rt.Mod_fn` did
//! not exist, the program passed `sky check` and failed `go build` with
//! `undefined: rt.Mod_fn` — caught only by the codegen ABI guard at `[E4005]`.
//! `hir::resolve::resolve_qual_var` now validates the member against
//! `hir::kernel::KERNEL_FUNCTIONS[Mod]` (a proven superset of the runtime, per
//! the `xtask kernel-members` gate) and emits `[E1001]` at NAME RESOLUTION.

use hir::SourceDb;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        if !dir.pop() {
            panic!("could not locate repo root (no sky-stdlib ancestor)");
        }
    }
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for p in entries {
        let skip = p.components().any(|c| {
            matches!(
                c.as_os_str().to_str(),
                Some("sky-out") | Some(".skycache") | Some(".skydeps")
            )
        });
        if skip {
            continue;
        }
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

fn load_stdlib(root: &Path) -> Vec<(String, syntax::Parse)> {
    let mut files = Vec::new();
    collect_sky(&root.join("sky-stdlib"), &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .filter(|s| !s.is_empty())
            .unwrap_or_default();
        out.push((name, parse));
    }
    out
}

/// Build stdlib + a single `Main` and return every error-severity diagnostic
/// code it produces.
fn error_codes(main_src: &str) -> Vec<String> {
    let root = repo_root();
    let stdlib = load_stdlib(&root);
    assert!(!stdlib.is_empty(), "stdlib failed to load");
    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    let mid = db.add_module("Main", syntax::parse(main_src, base::FileId(0)));
    ty::check_modules(&db, &[mid])
        .diagnostics
        .iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .map(|d| d.code.0.clone())
        .collect()
}

fn type_errors(main_src: &str) -> usize {
    let root = repo_root();
    let stdlib = load_stdlib(&root);
    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    let mid = db.add_module("Main", syntax::parse(main_src, base::FileId(0)));
    ty::check_modules(&db, &[mid]).type_errors
}

const HEAD: &str = "module Main exposing (main)\n\
    import Sky.Core.Prelude exposing (..)\n\
    import Sky.Core.String as String\n\
    import Std.Log exposing (println)\n";

fn ambient(body: &str) -> String {
    format!("{HEAD}main =\n    {body}\n")
}

// ─────────────────────────────────────────────────────────────────────────────
// The reject — an unknown / phantom qualified kernel member is [E1001], not a
// downstream go-build failure.
// ─────────────────────────────────────────────────────────────────────────────

#[test]
fn unknown_member_list_sum_rejected_e1001() {
    let codes = error_codes(&ambient("println (String.fromInt (List.sum [ 1, 2, 3 ]))"));
    assert!(
        codes.iter().any(|c| c == "E1001"),
        "`List.sum` must be rejected at resolution with [E1001]; got {codes:?}"
    );
}

#[test]
fn unknown_member_basics_remainderby_rejected_e1001() {
    let codes = error_codes(&ambient("println (String.fromInt (Basics.remainderBy 2 7))"));
    assert!(
        codes.iter().any(|c| c == "E1001"),
        "`Basics.remainderBy` must be rejected with [E1001]; got {codes:?}"
    );
}

#[test]
fn phantom_member_list_parallelmap_rejected_e1001() {
    let codes = error_codes(&ambient(
        "println (String.fromInt (List.length (List.parallelMap (\\x -> x) [ 1, 2, 3 ])))",
    ));
    assert!(
        codes.iter().any(|c| c == "E1001"),
        "phantom `List.parallelMap` must be rejected with [E1001]; got {codes:?}"
    );
}

#[test]
fn phantom_member_io_readbytes_rejected_e1001() {
    let codes = error_codes(&ambient("println (Io.readBytes 10)"));
    assert!(
        codes.iter().any(|c| c == "E1001"),
        "phantom `Io.readBytes` must be rejected with [E1001]; got {codes:?}"
    );
}

// ─────────────────────────────────────────────────────────────────────────────
// The positive counterpart — `List.sortWith` is a REAL member: it must NOT be
// rejected (ambient), and via `exposing` it resolves to the `.sky` Def with a
// PRECISE type `(a -> a -> Int) -> List a -> List a`.
// ─────────────────────────────────────────────────────────────────────────────

#[test]
fn sortwith_ambient_qualified_accepts() {
    // Ambient (no import) → Res::Kernel, lenient — but MUST NOT be rejected.
    let codes = error_codes(&ambient(
        "println (String.fromInt (List.length (List.sortWith (\\a b -> a - b) [ 3, 1, 2 ])))",
    ));
    assert!(
        !codes.iter().any(|c| c == "E1001"),
        "`List.sortWith` is a real member and must NOT be rejected; got {codes:?}"
    );
}

#[test]
fn sortwith_via_exposing_accepts_and_is_precisely_typed() {
    // `exposing (sortWith)` → the `.sky` Def, precisely typed. A well-typed use
    // must ACCEPT …
    let ok = "module Main exposing (main)\n\
        import Sky.Core.Prelude exposing (..)\n\
        import Sky.Core.String as String\n\
        import Sky.Core.List exposing (sortWith)\n\
        import Std.Log exposing (println)\n\
        main =\n\
        \x20   println (String.fromInt (List.length (sortWith (\\a b -> a - b) [ 3, 1, 2 ])))\n";
    assert_eq!(
        type_errors(ok),
        0,
        "well-typed `sortWith` via exposing must be accepted"
    );

    // … and a comparator that returns a String (not Int) must be REJECTED —
    // proving the `.sky` signature's `-> Int` codomain is load-bearing (a
    // fresh_flex kernel ref would have accepted this).
    let bad = "module Main exposing (main)\n\
        import Sky.Core.Prelude exposing (..)\n\
        import Sky.Core.String as String\n\
        import Sky.Core.List exposing (sortWith)\n\
        import Std.Log exposing (println)\n\
        main =\n\
        \x20   println (String.fromInt (List.length (sortWith (\\a b -> \"x\") [ 3, 1, 2 ])))\n";
    assert!(
        type_errors(bad) > 0,
        "a String-returning comparator must be rejected by sortWith's precise `.sky` type"
    );
}
