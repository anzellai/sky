#![forbid(unsafe_code)]
//! `hir` — desugared, name-resolved high-level IR: imports, scopes, `DefId`
//! resolution, module items (doc 02, doc 05).
//!
//! M2: real name resolution. `resolve(db, module)` builds a module environment
//! (builtins → imports → local decls) and walks each top-level body, turning
//! every reference into a [`Res`] (or a first-class [`Res::Error`] + diagnostic,
//! L7). Cross-module visibility is demand-driven via [`SourceDb::module_exports`]
//! — no 5-round fixpoint (doc 05 §8, L2). No globals: the environment is a value
//! threaded down the walk (L1).

mod cst;
mod db;
mod exports;
mod hir;
mod ids;
mod kernel;
mod resolve;

pub use db::{ImportSource, SourceDb};
pub use exports::{ExportedAlias, ExportedCtor, ExportedUnion, ModuleExports};
pub use hir::{
    Body, CaseBranch, Expr, ExprId, LocalDef, Pattern, PatId, TopDef, Type, TypeId,
};
pub use ids::{CtorRef, DefKind, DefLoc, DefTable, LocalId, Res, TypeRes};
pub use kernel::{KERNEL_IMPLICIT_TYPES, KERNEL_MODULES, PRELUDE_PROTECTED};
pub use resolve::{
    resolve, BinderDef, ClassA, ClassB, FieldDecl, FieldOcc, RefKind, RefOcc, ResolveResult,
    TypeOcc,
};

#[cfg(test)]
mod tests {
    use super::*;
    use base::FileId;

    fn db_with(modules: &[(&str, &str)]) -> SourceDb {
        let mut db = SourceDb::new();
        for (name, src) in modules {
            let parse = syntax::parse(src, FileId(0));
            db.add_module(name, parse);
        }
        db
    }

    #[test]
    fn resolves_prelude_and_kernel() {
        let src = "module Main exposing (main)\n\
                   import Sky.Core.Prelude exposing (..)\n\
                   import Std.Log exposing (println)\n\n\
                   main =\n    println (String.fromInt 42)\n";
        let db = db_with(&[("Main", src)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(r.class_a.is_empty(), "class-a: {:?}", r.class_a);
    }

    #[test]
    fn let_forward_reference() {
        let src = "x =\n    let\n        a = b\n        b = 5\n    in\n    a\n";
        let db = db_with(&[("Main", src)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(r.class_a.is_empty(), "class-a: {:?}", r.class_a);
    }

    #[test]
    fn unknown_qualifier_is_class_a() {
        let src = "x =\n    NotARealModule.foo 1\n";
        let db = db_with(&[("Main", src)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert_eq!(r.class_a.len(), 1);
        assert_eq!(r.class_a[0].qualifier.as_deref(), Some("NotARealModule"));
    }

    #[test]
    fn cross_module_ctor_and_value() {
        let dep = "module Lib exposing (Color(..), pick)\n\
                   type Color = Red | Green\n\
                   pick = Red\n";
        let main = "module Main exposing (main)\n\
                    import Lib exposing (Color(..), pick)\n\n\
                    main =\n    case pick of\n        Red -> 1\n        Green -> 2\n";
        let db = db_with(&[("Lib", dep), ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(r.class_a.is_empty(), "class-a: {:?}", r.class_a);
    }

    #[test]
    fn unknown_bare_name_under_prelude_open_is_rejected() {
        // Regression for the `kernel_open` soundness hole: `import Sky.Core.Prelude
        // exposing (..)` opens the `Basics` kernel pseudo-module. A bare undefined
        // name (`mysteryValue`) MUST resolve to `Res::Error` + a class-(a)
        // `[E1001] Undefined name` diagnostic at resolve time — NOT a lenient
        // `rt.Basics_mysteryValue` kernel ref that only fails at `go build`.
        let src = "module Main exposing (main)\n\
                   import Sky.Core.Prelude exposing (..)\n\
                   import Std.Log exposing (println)\n\n\
                   main =\n    println mysteryValue\n";
        let db = db_with(&[("Main", src)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert_eq!(r.class_a.len(), 1, "class-a: {:?}", r.class_a);
        assert_eq!(r.class_a[0].name, "mysteryValue");
        assert_eq!(r.class_a[0].qualifier, None);
        assert!(
            r.diagnostics
                .iter()
                .any(|d| d.code.0 == "E1001" && d.message.contains("mysteryValue")),
            "expected [E1001] Undefined name for mysteryValue, got {:?}",
            r.diagnostics
        );
    }

    #[test]
    fn correct_prelude_program_same_shape_still_resolves() {
        // The sibling positive: the SAME program shape with a real `Basics`
        // export (`identity`) and enumerated kernel functions (`compare`, added
        // by the ported `KERNEL_FUNCTIONS` table) resolves clean — the fix binds
        // exactly the module's known functions, so nothing legitimate regresses.
        let src = "module Main exposing (main)\n\
                   import Sky.Core.Prelude exposing (..)\n\
                   import Std.Log exposing (println)\n\n\
                   main =\n    println (String.fromInt (compare 1 2))\n";
        let db = db_with(&[("Main", src)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(r.class_a.is_empty(), "class-a: {:?}", r.class_a);
    }

    #[test]
    fn explicit_alias_wins() {
        // `Db.x` → Std.Db (kernel); bare `conn` → Lib.Db.
        let libdb = "module Lib.Db exposing (conn)\nconn = 0\n";
        let main = "module Main exposing (main)\n\
                    import Std.Db as Db\n\
                    import Lib.Db exposing (conn)\n\n\
                    main =\n    Db.exec conn\n";
        let db = db_with(&[("Lib.Db", libdb), ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(r.class_a.is_empty(), "class-a: {:?}", r.class_a);
    }
}
