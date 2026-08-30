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

pub use db::{ImportSource, SkyDb, SourceDb};
pub use exports::{compute_exports, ExportedAlias, ExportedCtor, ExportedUnion, ModuleExports};
pub use hir::{Body, CaseBranch, Expr, ExprId, LocalDef, PatId, Pattern, TopDef, Type, TypeId};
pub use ids::{CtorRef, DefKind, DefLoc, DefTable, LocalId, Res, TypeRes};
pub use kernel::{
    is_reserved_sky_namespace, kernel_functions, KERNEL_FUNCTIONS, KERNEL_IMPLICIT_TYPES,
    KERNEL_MODULES, PRELUDE_PROTECTED, PRELUDE_QUALIFIERS,
};
pub use resolve::{
    resolve, BinderDef, ClassA, ClassB, FieldDecl, FieldOcc, RefKind, RefOcc, ResolveResult,
    ScopeNameKind, TypeOcc,
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
    fn interpolation_call_resolves_through_grammar() {
        // Regression: a rich interpolation body (`{{String.fromInt n}}`) must
        // resolve to a real `Call(Var(Kernel …), [Var …])` in the HIR — the
        // pre-fix hand-rolled classifier wrapped the whole body in one
        // `QualRefExpr` that resolved to `Res::Error` and silently lowered to
        // `nil` (a miscompile). No class-a errors, one Call node present.
        let src = "module Main exposing (main)\n\
                   import Sky.Core.Prelude exposing (..)\n\
                   import Std.Log exposing (println)\n\n\
                   n = 42\n\n\
                   render =\n    \"\"\"A={{String.fromInt n}}\"\"\"\n\n\
                   main =\n    println render\n";
        let db = db_with(&[("Main", src)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(r.class_a.is_empty(), "class-a: {:?}", r.class_a);
        let has_kernel_call = r.bodies.values().any(|body| {
            body.exprs.iter().any(|(_, e)| {
                if let Expr::Call(callee, args) = e {
                    matches!(&body.exprs[*callee], Expr::Var(Res::Kernel { module, func })
                        if module.as_str() == "String" && func.as_str() == "fromInt")
                        && args.len() == 1
                } else {
                    false
                }
            })
        });
        assert!(
            has_kernel_call,
            "expected a Call to Kernel String.fromInt from the interpolation body"
        );
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

    // ---- ambiguous unqualified names (doc 05 §6b) ------------------------
    //
    // Every rejection below is paired with an ACCEPTED twin that differs only in
    // the defect. A rejection assertion on its own passes just as well against a
    // compiler that rejects everything; the twin is what makes the pair
    // falsifiable (Family R convention, `xtask/src/corpus/reject_matrix.rs`).

    /// The two modules the ambiguity cases import — same name, same type, two
    /// different definitions.
    fn ambig_deps() -> [(&'static str, &'static str); 2] {
        [
            (
                "Ambig.Alpha",
                "module Ambig.Alpha exposing (..)\nlabel = \"ALPHA\"\n",
            ),
            (
                "Ambig.Beta",
                "module Ambig.Beta exposing (..)\nlabel = \"BETA\"\n",
            ),
        ]
    }

    fn ambiguity_codes(r: &resolve::ResolveResult) -> Vec<String> {
        r.diagnostics
            .iter()
            .filter(|d| d.code.0 == "E1012")
            .map(|d| d.message.clone())
            .collect()
    }

    #[test]
    fn ambiguous_unqualified_name_is_rejected() {
        // THE defect: two `exposing (..)` imports binding one name, referenced
        // bare. Before the precedence lattice this compiled clean and printed
        // whichever module was imported LAST
        // (`corpus/repro/ambiguous-exposing-all/`).
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Beta exposing (..)\n\n\
                    main =\n    println label\n";
        let [a, b] = ambig_deps();
        let db = db_with(&[a, b, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        let msgs = ambiguity_codes(&r);
        assert_eq!(msgs.len(), 1, "expected one [E1012], got {:?}", r.diagnostics);
        // The message must name BOTH modules and offer both qualified forms —
        // an ambiguity error that does not say what the alternatives are leaves
        // the user to guess which import to change.
        assert!(msgs[0].contains("Ambig.Alpha"), "{}", msgs[0]);
        assert!(msgs[0].contains("Ambig.Beta"), "{}", msgs[0]);
        assert!(msgs[0].contains("Alpha.label"), "{}", msgs[0]);
        assert!(msgs[0].contains("Beta.label"), "{}", msgs[0]);
    }

    #[test]
    fn ambiguous_unqualified_name_is_rejected_either_import_order() {
        // The pair IS the defect: the same program with the two import lines
        // swapped used to print the other module's answer. Both orders must now
        // reject, or the rule has merely moved the order-dependence.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Beta exposing (..)\n\
                    import Ambig.Alpha exposing (..)\n\n\
                    main =\n    println label\n";
        let [a, b] = ambig_deps();
        let db = db_with(&[a, b, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert_eq!(ambiguity_codes(&r).len(), 1, "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_unambiguous_name_is_accepted() {
        // The ACCEPTED twin: byte-identical but for `Ambig.Beta` no longer
        // exposing `label`, so exactly one binding is in scope. A twin failure
        // means the graph shape broke, not that ambiguity was detected.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Beta exposing (..)\n\n\
                    main =\n    println label\n";
        let db = db_with(&[
            (
                "Ambig.Alpha",
                "module Ambig.Alpha exposing (..)\nlabel = \"ALPHA\"\n",
            ),
            (
                "Ambig.Beta",
                "module Ambig.Beta exposing (other)\nother = \"BETA\"\n",
            ),
            ("Main", main),
        ]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
        assert!(r.class_a.is_empty(), "class-a: {:?}", r.class_a);
    }

    #[test]
    fn twin_ambiguous_name_never_referenced_is_accepted() {
        // Reported at the USE SITE, not the import. Importing two modules that
        // both expose a name you never mention is legal and extremely common —
        // every Sky.Live page does `import Std.Html exposing (..)` alongside
        // `import Std.Html.Attributes exposing (..)`. Reporting at the import
        // would reject programs whose meaning is not order-dependent at all.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Beta exposing (..)\n\n\
                    main =\n    println \"neither\"\n";
        let [a, b] = ambig_deps();
        let db = db_with(&[a, b, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_qualified_reference_is_accepted() {
        // The fix the diagnostic tells the user to apply must actually work.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Beta exposing (..)\n\n\
                    main =\n    println Alpha.label\n";
        let [a, b] = ambig_deps();
        let db = db_with(&[a, b, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
        assert!(r.class_a.is_empty(), "class-a: {:?}", r.class_a);
    }

    #[test]
    fn twin_local_definition_shadows_both_imports() {
        // A locally-defined name shadowing an import is long-standing legal Sky
        // (doc 05 C7) and stays legal, silently: `Local` is the top layer, so it
        // wins outright and the two imported bindings never compete.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Beta exposing (..)\n\n\
                    label = \"MINE\"\n\n\
                    main =\n    println label\n";
        let [a, b] = ambig_deps();
        let db = db_with(&[a, b, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_explicit_exposing_list_beats_exposing_all() {
        // `exposing (label)` names THIS binding specifically; `exposing (..)` is
        // a bulk claim on whatever the module happens to export. The specific
        // claim wins, in either import order — which is the whole point: the
        // answer must not depend on where the lines sit.
        for main in [
            "module Main exposing (main)\n\
             import Std.Log exposing (println)\n\
             import Ambig.Alpha exposing (label)\n\
             import Ambig.Beta exposing (..)\n\n\
             main =\n    println label\n",
            "module Main exposing (main)\n\
             import Std.Log exposing (println)\n\
             import Ambig.Beta exposing (..)\n\
             import Ambig.Alpha exposing (label)\n\n\
             main =\n    println label\n",
        ] {
            let [a, b] = ambig_deps();
            let db = db_with(&[a, b, ("Main", main)]);
            let m = db.module_by_name("Main").unwrap();
            let r = resolve(&db, m);
            assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
        }
    }

    #[test]
    fn two_explicit_exposing_lists_are_still_ambiguous() {
        // Both claims are equally deliberate and equally specific, so neither
        // outranks the other. This is the case the `Explicit > Open` refinement
        // must NOT swallow.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (label)\n\
                    import Ambig.Beta exposing (label)\n\n\
                    main =\n    println label\n";
        let [a, b] = ambig_deps();
        let db = db_with(&[a, b, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert_eq!(ambiguity_codes(&r).len(), 1, "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_prelude_is_ambient_and_never_makes_an_import_ambiguous() {
        // The case that blocked this fix for months. `Sky.Core.Prelude exposing
        // (..)` and `Sky.Core.Math exposing (..)` both bind `abs`/`min`/`max`/
        // `sqrt`, and real examples import exactly that pair. Prelude is
        // autoloaded — `sky init`'s templates emit the line unconditionally — so
        // it sits in the AMBIENT layer and an explicit import shadows it with no
        // diagnostic. A naive "bound twice = error" rule rejects this program.
        let src = "module Main exposing (main)\n\
                   import Sky.Core.Prelude exposing (..)\n\
                   import Sky.Core.Math exposing (..)\n\
                   import Std.Log exposing (println)\n\n\
                   main =\n    println (String.fromInt (abs 3))\n";
        let db = db_with(&[("Main", src)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_both_prelude_spellings_are_ambient() {
        // `Sky.Core.Prelude` and `Sky.Core.Basics` are the SAME kernel pseudo
        // (`KERNEL_MODULES` maps both to `Basics`). The layer a binding lands in
        // must not depend on which alias of one module the user wrote, or the
        // `Basics` spelling of this program would be ambiguous on `abs` while
        // the `Prelude` spelling compiled.
        for prelude in ["Sky.Core.Prelude", "Sky.Core.Basics"] {
            let src = format!(
                "module Main exposing (main)\n\
                 import {prelude} exposing (..)\n\
                 import Sky.Core.Math exposing (..)\n\
                 import Std.Log exposing (println)\n\n\
                 main =\n    println (String.fromInt (abs 3))\n"
            );
            let db = db_with(&[("Main", &src)]);
            let m = db.module_by_name("Main").unwrap();
            let r = resolve(&db, m);
            assert!(
                ambiguity_codes(&r).is_empty(),
                "{prelude}: {:?}",
                r.diagnostics
            );
        }
    }

    #[test]
    fn twin_same_definition_reached_twice_is_not_ambiguous() {
        // One module imported under two forms binds ONE definition by two
        // routes. Keying ambiguity on the definition's identity rather than on
        // "was it bound more than once" is what keeps re-exports and
        // belt-and-braces import styles compiling.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Alpha exposing (label)\n\n\
                    main =\n    println label\n";
        let [a, _] = ambig_deps();
        let db = db_with(&[a, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    #[test]
    fn ambiguous_constructor_is_rejected_in_expression_and_pattern() {
        // A constructor picks a VARIANT, so an ambiguous ctor selects a
        // different branch depending on import order — the same defect one
        // namespace over, in both expression and pattern position.
        let alpha = "module Ambig.Alpha exposing (Flag(..))\ntype Flag = On | Off\n";
        let beta = "module Ambig.Beta exposing (Flag(..))\ntype Flag = On | Off\n";
        let expr = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Beta exposing (..)\n\n\
                    main =\n    println (toString On)\n";
        let db = db_with(&[("Ambig.Alpha", alpha), ("Ambig.Beta", beta), ("Main", expr)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert_eq!(
            ambiguity_codes(&r).len(),
            1,
            "expression position: {:?}",
            r.diagnostics
        );

        let pat = "module Main exposing (main)\n\
                   import Std.Log exposing (println)\n\
                   import Ambig.Alpha exposing (..)\n\
                   import Ambig.Beta exposing (..)\n\n\
                   pick f =\n    case f of\n        On -> 1\n        _ -> 2\n\n\
                   main =\n    println \"x\"\n";
        let db = db_with(&[("Ambig.Alpha", alpha), ("Ambig.Beta", beta), ("Main", pat)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert_eq!(
            ambiguity_codes(&r).len(),
            1,
            "pattern position: {:?}",
            r.diagnostics
        );
    }

    #[test]
    fn twin_one_constructor_source_is_accepted() {
        let alpha = "module Ambig.Alpha exposing (Flag(..))\ntype Flag = On | Off\n";
        let beta = "module Ambig.Beta exposing (other)\nother = 1\n";
        let src = "module Main exposing (main)\n\
                   import Std.Log exposing (println)\n\
                   import Ambig.Alpha exposing (..)\n\
                   import Ambig.Beta exposing (..)\n\n\
                   main =\n    println (toString On)\n";
        let db = db_with(&[("Ambig.Alpha", alpha), ("Ambig.Beta", beta), ("Main", src)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    // ---- ambiguous TYPE names (doc 05 §6b, type namespace) ------------------
    //
    // The value/constructor lattice above deliberately excluded the TYPE
    // namespace, because several type paths synthesise a `DefId` leniently when
    // a module does not really export the name, so two modules could yield two
    // `DefId`s for ONE conceptual type and keying on that would manufacture
    // false rejections (the #164 failure mode).
    //
    // Every rejection below is paired with an ACCEPTED twin. The twins are the
    // load-bearing half: they are the concrete programs the lenient synthesis
    // would have broken, so they are written FIRST and they must stay green.

    /// Two modules declaring DIFFERENT unions under one name. Constructor names
    /// are disjoint, so nothing here is caught by the existing ctor rule — the
    /// TYPE name is the only ambiguous thing.
    fn ambig_type_deps() -> [(&'static str, &'static str); 2] {
        [
            (
                "Ambig.Alpha",
                "module Ambig.Alpha exposing (..)\ntype Shape = Circle Int\n",
            ),
            (
                "Ambig.Beta",
                "module Ambig.Beta exposing (..)\ntype Shape = Square Int\n",
            ),
        ]
    }

    #[test]
    fn ambiguous_type_name_is_rejected() {
        // THE defect, one namespace over. `Shape` is bound by two `exposing (..)`
        // imports and written in an annotation. `hir::resolve`'s `types` map is a
        // plain last-`insert()`-wins `IndexMap`, so which union the annotation
        // means is a function of import ORDER — and `lower`'s `nominal` map is
        // last-writer-wins on the bare name too, so the two orders select two
        // DIFFERENT Go types.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Beta exposing (..)\n\n\
                    tag : Shape -> Int\n\
                    tag _s =\n    1\n\n\
                    main =\n    println \"x\"\n";
        let [a, b] = ambig_type_deps();
        let db = db_with(&[a, b, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        let msgs = ambiguity_codes(&r);
        assert_eq!(msgs.len(), 1, "expected one [E1012], got {:?}", r.diagnostics);
        assert!(msgs[0].contains("Ambig.Alpha"), "{}", msgs[0]);
        assert!(msgs[0].contains("Ambig.Beta"), "{}", msgs[0]);
        assert!(msgs[0].contains("type"), "{}", msgs[0]);
    }

    #[test]
    fn ambiguous_type_name_is_rejected_either_import_order() {
        // The pair IS the defect: swapping the two import lines must not change
        // which union the annotation means, so BOTH orders reject or the rule has
        // merely moved the order-dependence somewhere else.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Beta exposing (..)\n\
                    import Ambig.Alpha exposing (..)\n\n\
                    tag : Shape -> Int\n\
                    tag _s =\n    1\n\n\
                    main =\n    println \"x\"\n";
        let [a, b] = ambig_type_deps();
        let db = db_with(&[a, b, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert_eq!(ambiguity_codes(&r).len(), 1, "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_unambiguous_type_name_is_accepted() {
        // The ACCEPTED twin: identical but for `Ambig.Beta` declaring no `Shape`,
        // so exactly one binding is in scope. A twin failure means the graph shape
        // broke, not that ambiguity was detected.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Beta exposing (..)\n\n\
                    tag : Shape -> Int\n\
                    tag _s =\n    1\n\n\
                    main =\n    println \"x\"\n";
        let db = db_with(&[
            (
                "Ambig.Alpha",
                "module Ambig.Alpha exposing (..)\ntype Shape = Circle Int\n",
            ),
            (
                "Ambig.Beta",
                "module Ambig.Beta exposing (..)\ntype Other = Square Int\n",
            ),
            ("Main", main),
        ]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_ambiguous_type_never_referenced_is_accepted() {
        // Use-site reporting, same as values. Importing two modules that both
        // export a type name you never write is legal and extremely common.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Beta exposing (..)\n\n\
                    main =\n    println \"x\"\n";
        let [a, b] = ambig_type_deps();
        let db = db_with(&[a, b, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_qualified_type_reference_is_accepted() {
        // The fix the diagnostic tells the user to apply has to actually work.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Beta exposing (..)\n\n\
                    tag : Alpha.Shape -> Int\n\
                    tag _s =\n    1\n\n\
                    main =\n    println \"x\"\n";
        let [a, b] = ambig_type_deps();
        let db = db_with(&[a, b, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_local_type_declaration_shadows_both_imports() {
        // Local wins outright — the same C7 rule the value lattice applies.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Beta exposing (..)\n\n\
                    type Shape = Mine\n\n\
                    tag : Shape -> Int\n\
                    tag _s =\n    1\n\n\
                    main =\n    println \"x\"\n";
        let [a, b] = ambig_type_deps();
        let db = db_with(&[a, b, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    // ---- G2: the false-positive guards the lenient synthesis was protecting --

    #[test]
    fn twin_kernel_implicit_type_from_two_modules_is_accepted() {
        // Two kernel pseudo-modules both exposing `Decoder`. NOTE: this case is
        // accepted with or without the `BUILTIN_MOD` identity fix, because the
        // old code keyed both on `(self.module, "Decoder")` and so already
        // agreed WITHIN one module. It is kept as a shape guard, not as the
        // proof of the fix — `twin_kernel_implicit_type_via_reexport_is_accepted`
        // below is the discriminating one.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Sky.Core.Json.Decode exposing (Decoder)\n\
                    import Sky.Core.Json.Encode exposing (Decoder)\n\n\
                    dec : Decoder -> Int\n\
                    dec _d =\n    1\n\n\
                    main =\n    println \"x\"\n";
        let db = db_with(&[("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_kernel_implicit_type_via_reexport_is_accepted() {
        // THE blocker this item was gated on, in the shape that actually bites.
        //
        // `Decoder` is KERNEL-IMPLICIT — it has no `type` declaration in any
        // `.sky` source (`KERNEL_IMPLICIT_TYPES`). A Sky module that re-exposes
        // it (`Codec.Wrap` below — the `Std.Codec` shape) publishes no export for
        // it either, because `exports.rs` computes exports from a module's own
        // parse. So the two routes to ONE conceptual `Decoder` used to mint two
        // unrelated `DefId`s:
        //
        //   via the re-exporter → `def(Codec.Wrap, "Decoder")`
        //   via the kernel      → `def(Main,       "Decoder")`   (the IMPORTER!)
        //
        // A `DefId`-keyed ambiguity rule reports those as two types and rejects
        // this program. That is the false rejection this whole item was blocked
        // on, and it is why the leniency had to be narrowed BEFORE the rule was
        // extended: the re-export chase fails here (the chain ends at a kernel
        // module, not a Sky declaration), so both routes fall through to the
        // kernel-implicit branch and agree on ONE identity.
        let wrap = "module Codec.Wrap exposing (Decoder)\n\
                    import Sky.Core.Json.Decode exposing (Decoder)\n";
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Sky.Core.Json.Decode exposing (Decoder)\n\
                    import Codec.Wrap exposing (Decoder)\n\n\
                    dec : Decoder -> Int\n\
                    dec _d =\n    1\n\n\
                    main =\n    println \"x\"\n";
        let db = db_with(&[("Codec.Wrap", wrap), ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_type_reexported_and_imported_directly_is_accepted() {
        // A type reached by two routes is ONE type. `Ambig.Wrap` re-exposes
        // `Ambig.Alpha`'s `Shape`; importing both must not be ambiguous.
        // `exports.rs` computes exports from a module's OWN parse and never
        // recurses, so `Wrap` publishes no `Shape` of its own — the re-export has
        // to be chased to `Alpha` or the two routes carry two identities.
        let wrap = "module Ambig.Wrap exposing (Shape)\n\
                    import Ambig.Alpha exposing (Shape)\n";
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (Shape)\n\
                    import Ambig.Wrap exposing (Shape)\n\n\
                    tag : Shape -> Int\n\
                    tag _s =\n    1\n\n\
                    main =\n    println \"x\"\n";
        let [a, _] = ambig_type_deps();
        let db = db_with(&[a, ("Ambig.Wrap", wrap), ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_reexport_chase_is_not_defeated_by_import_order() {
        // The chase must consider EVERY import that could have supplied the
        // name, not the first one. Here `Ambig.Wrap` imports an unrelated module
        // `exposing (..)` ABOVE the import it actually re-exports `Shape` from.
        // A depth-first walk down the first candidate finds nothing, falls back
        // to `Opaque` — and the ambiguity is then missed rather than
        // manufactured, so this test guards a false NEGATIVE, not a rejection.
        let noise = "module Ambig.Noise exposing (..)\nnoise = 1\n";
        let wrap = "module Ambig.Wrap exposing (Shape)\n\
                    import Ambig.Noise exposing (..)\n\
                    import Ambig.Alpha exposing (Shape)\n";
        // `Ambig.Beta` declares its OWN `Shape`, so this reference IS ambiguous —
        // between Alpha (reached through Wrap) and Beta. If the chase failed,
        // Wrap's binding would be `Opaque` and nothing would be reported.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Wrap exposing (Shape)\n\
                    import Ambig.Beta exposing (Shape)\n\n\
                    tag : Shape -> Int\n\
                    tag _s =\n    1\n\n\
                    main =\n    println \"x\"\n";
        let [a, b] = ambig_type_deps();
        let db = db_with(&[
            a,
            b,
            ("Ambig.Noise", noise),
            ("Ambig.Wrap", wrap),
            ("Main", main),
        ]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        let msgs = ambiguity_codes(&r);
        assert_eq!(msgs.len(), 1, "expected one [E1012], got {:?}", r.diagnostics);
    }

    #[test]
    fn kernel_implicit_type_versus_a_real_declaration_is_ambiguous() {
        // The kernel-implicit identity earning its keep, in the direction that
        // REJECTS. `Codec.Wrap` re-exposes the kernel `Decoder`; `Db.Phantom`
        // declares a `Decoder` of its very own. Those are two different types,
        // and mixing them is not hypothetical — `lower::goty` carries a
        // hand-written band-aid for exactly this pair ("`Decoder` is declared in
        // multiple modules … so a flat nominal lookup would coerce a real
        // decoder to an unrelated module's phantom enum and panic at runtime").
        //
        // Resolving the re-export to the ONE kernel-implicit identity is what
        // makes this reportable: without it, the re-export falls through to
        // `TypeKey::Opaque`, the rule abstains, and the mix goes unreported.
        let wrap = "module Codec.Wrap exposing (Decoder)\n\
                    import Sky.Core.Json.Decode exposing (Decoder)\n";
        let phantom = "module Db.Phantom exposing (..)\ntype Decoder = Phantom\n";
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Codec.Wrap exposing (Decoder)\n\
                    import Db.Phantom exposing (Decoder)\n\n\
                    dec : Decoder -> Int\n\
                    dec _d =\n    1\n\n\
                    main =\n    println \"x\"\n";
        let db = db_with(&[("Codec.Wrap", wrap), ("Db.Phantom", phantom), ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert_eq!(
            ambiguity_codes(&r).len(),
            1,
            "expected one [E1012], got {:?}",
            r.diagnostics
        );
    }

    #[test]
    fn twin_unresolvable_type_identity_abstains_instead_of_rejecting() {
        // The `TypeKey::Opaque` safeguard, as an executable argument.
        //
        // `Ambig.Wrap` re-exposes `Widget`, and it got `Widget` from a KERNEL
        // pseudo-module — which is not a Sky declaration site, so the re-export
        // chase ends with nothing, and `Widget` is not one of the
        // `KERNEL_IMPLICIT_TYPES` either. There is no authoritative identity to
        // be had: the compiler genuinely cannot tell whether `Wrap.Widget` is
        // `Ambig.Gamma.Widget` or something else.
        //
        // The rule ABSTAINS. Guessing "two identities, therefore two types"
        // rejects a program that may well be correct, and a rule that rejects
        // working programs gets reverted (#164) and closes nothing. Guessing the
        // other way — silently picking the last import — is today's behaviour and
        // is exactly what this program keeps.
        //
        // This is the test that fails if `TypeKey::Opaque` is ever made
        // comparable, which is the over-rejection failure mode in one line.
        let wrap = "module Ambig.Wrap exposing (Widget)\n\
                    import Sky.Core.Json.Decode exposing (Widget)\n";
        let gamma = "module Ambig.Gamma exposing (..)\ntype Widget = Knob Int\n";
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Wrap exposing (Widget)\n\
                    import Ambig.Gamma exposing (Widget)\n\n\
                    tag : Widget -> Int\n\
                    tag _w =\n    1\n\n\
                    main =\n    println \"x\"\n";
        let db = db_with(&[("Ambig.Wrap", wrap), ("Ambig.Gamma", gamma), ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(
            ambiguity_codes(&r).is_empty(),
            "an unresolvable identity must abstain, not reject: {:?}",
            r.diagnostics
        );
    }

    #[test]
    fn twin_same_type_reached_twice_is_not_ambiguous() {
        // One module imported under two forms binds ONE type by two routes.
        let main = "module Main exposing (main)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Alpha exposing (Shape)\n\n\
                    tag : Shape -> Int\n\
                    tag _s =\n    1\n\n\
                    main =\n    println \"x\"\n";
        let [a, _] = ambig_type_deps();
        let db = db_with(&[a, ("Main", main)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_builtin_type_is_ambient_and_never_ambiguous() {
        // `Error` is a BUILTIN type (auto-imported from `Sky.Core.Error`, C19)
        // AND a kernel-implicit name. A module that also imports an `Error` from
        // somewhere explicit must keep compiling: the builtin sits in the ambient
        // layer, exactly as the Prelude does for values.
        let src = "module Main exposing (main)\n\
                   import Sky.Core.Prelude exposing (..)\n\
                   import Sky.Core.Error exposing (Error)\n\
                   import Std.Log exposing (println)\n\n\
                   f : Result Error Int -> Int\n\
                   f _r =\n    1\n\n\
                   main =\n    println \"x\"\n";
        let db = db_with(&[("Main", src)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
    }

    #[test]
    fn twin_explicit_type_exposing_beats_exposing_all() {
        // The `Explicit > Open` refinement, in the type namespace, in both
        // import orders.
        for main in [
            "module Main exposing (main)\n\
             import Std.Log exposing (println)\n\
             import Ambig.Alpha exposing (Shape)\n\
             import Ambig.Beta exposing (..)\n\n\
             tag : Shape -> Int\n\
             tag _s =\n    1\n\n\
             main =\n    println \"x\"\n",
            "module Main exposing (main)\n\
             import Std.Log exposing (println)\n\
             import Ambig.Beta exposing (..)\n\
             import Ambig.Alpha exposing (Shape)\n\n\
             tag : Shape -> Int\n\
             tag _s =\n    1\n\n\
             main =\n    println \"x\"\n",
        ] {
            let [a, b] = ambig_type_deps();
            let db = db_with(&[a, b, ("Main", main)]);
            let m = db.module_by_name("Main").unwrap();
            let r = resolve(&db, m);
            assert!(ambiguity_codes(&r).is_empty(), "{:?}", r.diagnostics);
        }
    }

    #[test]
    fn resolve_records_expr_span() {
        // Phase 1 gate: the resolver's span side-table stamps every expression
        // with its CST byte range. The root of `foo = 1 + 2` is the `1 + 2`
        // node, so its recorded span must slice back to exactly "1 + 2".
        let src = "foo = 1 + 2\n";
        let db = db_with(&[("Main", src)]);
        let m = db.module_by_name("Main").unwrap();
        let r = resolve(&db, m);
        let body = r.bodies.values().next().expect("one def body for `foo`");
        let root = body.root.expect("foo has a root expression");
        let span = body.expr_span(root).expect("root expr has a recorded span");
        let (start, end) = (span.range.0 as usize, span.range.1 as usize);
        // The wrapper stores `e.syntax().text_range()` — the full CST node range,
        // which attaches the leading trivia after `=`, so it slices to " 1 + 2".
        assert_eq!(
            &src[start..end],
            " 1 + 2",
            "span {span:?} sliced wrong text"
        );
        assert_eq!(src[start..end].trim(), "1 + 2");
        // "foo =" is 5 bytes → node range [5, 11) (leading space is node trivia).
        assert_eq!((start, end), (5, 11), "unexpected byte range {span:?}");
    }
}
