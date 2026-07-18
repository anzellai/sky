//! Orchestration: typecheck a set of modules against the loaded world (doc 06).
//! Produces the type-error diagnostics + per-def type table (the "per-region
//! type map output (name → inferred type)" the lowerer will consume).

use crate::exhaustive;
use crate::infer::Infer;
use crate::sig::World;
use crate::{Scheme, Ty};
use base::{DefId, ModuleId, Span};
use diagnostics::{Code, Diagnostic, Severity};
use hir::{Body, ExprId, LocalId, SourceDb};
use std::collections::HashMap;

/// The kind of type-error emitted (all currently map to a unify clash).
#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub enum TypeErrorKind {
    Mismatch,
}

/// A def's inferred (or declared) type — the entry in the per-def type table.
#[derive(Clone, Debug)]
pub struct DefType {
    pub module: String,
    pub name: String,
    pub ty: Ty,
    /// True when the type came from the def's own annotation, false when it was
    /// inferred from the body.
    pub declared: bool,
}

/// The result of typechecking a group of modules.
#[derive(Default)]
pub struct CheckOutput {
    /// Type-error diagnostics (severity Error). The M3 accept-parity count.
    pub type_errors: usize,
    /// Exhaustiveness warnings (E3001) — NOT counted as type errors.
    pub exhaustiveness_warnings: usize,
    /// Name-resolution errors (unresolved names — `[E1001]`-class) from
    /// `hir::resolve` over the checked modules. Surfaced additively so the
    /// accept/reject gate can treat an unresolved reference as a rejection
    /// (the oracle rejects at canonicalisation) WITHOUT changing the M3
    /// accept-parity `type_errors` count.
    pub name_errors: usize,
    pub diagnostics: Vec<Diagnostic>,
    pub def_types: Vec<DefType>,
}

/// The per-body type table the lowerer (doc 07 §2) consumes: the def's result
/// type, plus a per-expression and per-local type map keyed by arena id.
#[derive(Default)]
pub struct BodyTypes {
    pub result: Option<Ty>,
    pub exprs: HashMap<ExprId, Ty>,
    pub locals: HashMap<LocalId, Ty>,
}

/// A reusable typed view of a whole program (stdlib + deps + entry). Built once;
/// `body_types` re-infers a single def against the shared world. Lets the `lower`
/// crate ask for per-expression types without rebuilding the world per def.
pub struct Typer<'a> {
    world: World,
    db: &'a SourceDb,
}

impl<'a> Typer<'a> {
    pub fn new(db: &'a SourceDb) -> Self {
        Typer {
            world: World::build(db),
            db,
        }
    }

    /// Infer `body`, returning the per-expression + per-local type table.
    /// `def` is the body's own DefId so a recursive self-reference stays
    /// monomorphic (does not instantiate the def's own pass-3 scheme).
    pub fn body_types(&self, def: DefId, body: &Body) -> BodyTypes {
        let mut infer = Infer::new(&self.world, self.db)
            .with_self_def(Some(def))
            .with_inferred(true);
        let (result, exprs, locals) = infer.infer_def_typed(body);
        BodyTypes {
            result,
            exprs,
            locals,
        }
    }

    /// Like [`Typer::body_types`], but additionally seeds param/local types from
    /// the def's declared signature (the annotation). Used by the tooling layer
    /// (hover / completion) so a param reflects `f : Int -> Int` rather than the
    /// body-only-inferred `number`. Kept SEPARATE from `body_types` so the
    /// lowerer's typed table — and therefore codegen — is byte-for-byte
    /// unchanged (the LSP is additive).
    pub fn body_types_annotated(&self, def: DefId, body: &Body) -> BodyTypes {
        let mut infer = Infer::new(&self.world, self.db)
            .with_self_def(Some(def))
            .with_inferred(true)
            .with_expected(self.world.value_sigs.get(&def).cloned());
        let (result, exprs, locals) = infer.infer_def_typed(body);
        BodyTypes {
            result,
            exprs,
            locals,
        }
    }

    /// The declared/derived scheme for a top-level value def, if known.
    pub fn value_sig(&self, def: DefId) -> Option<&Scheme> {
        self.world.value_sigs.get(&def)
    }

    /// The scheme for a constructor by its own DefId (union member).
    pub fn ctor_sig_by_def(&self, def: DefId) -> Option<&Scheme> {
        self.world.ctors_by_def.get(&def)
    }

    /// A kernel function's scheme, keyed as `Res::Kernel { module, func }` is
    /// (pseudo-module, func) — the tooling layer's hover on a stdlib call.
    pub fn kernel_sig(&self, module: &str, func: &str) -> Option<&Scheme> {
        self.world
            .kernel_sigs
            .get(&(module.to_string(), func.to_string()))
    }

    /// The pass-3 inferred scheme for an unannotated stdlib combinator, if any.
    pub fn inferred_sig(&self, def: DefId) -> Option<&Scheme> {
        self.world.inferred_sigs.get(&def)
    }
}

/// Typecheck `to_check` module ids against the world built from every module in
/// `db` (stdlib + deps + entry). Never panics; partial results + diagnostics (L7).
pub fn check_modules(db: &SourceDb, to_check: &[ModuleId]) -> CheckOutput {
    let world = World::build(db);
    let mut out = CheckOutput::default();

    for &mid in to_check {
        let mname = db.module_name(mid).to_string();
        let resolved = hir::resolve(db, mid);
        // Surface unresolved-name diagnostics (additive — see `name_errors`).
        for d in &resolved.diagnostics {
            if d.severity == Severity::Error {
                out.name_errors += 1;
                out.diagnostics.push(d.clone());
            }
        }
        let names: HashMap<base::DefId, String> = resolved
            .top_defs
            .iter()
            .map(|td| (td.def, td.name.as_str().to_string()))
            .collect();

        for (def, body) in &resolved.bodies {
            let dname = names.get(def).cloned().unwrap_or_default();
            let mut infer = Infer::new(&world, db).with_self_def(Some(*def));
            // Annotation gate (M3 residual): a def with a DECLARED signature is
            // checked against it (params seeded + body result unified with the
            // declared type), so a body that contradicts its own annotation is
            // rejected. Unannotated defs keep the lenient body-only inference
            // that preserves accept-parity. Scoped to `to_check` (never the
            // trusted stdlib) so only app-code annotations are tightened.
            let inferred = match world.value_sigs.get(def) {
                Some(scheme) => {
                    let scheme = scheme.clone();
                    infer.infer_def_against(body, &scheme);
                    None
                }
                None => infer.infer_def(body),
            };
            for err in &infer.errors {
                out.type_errors += 1;
                out.diagnostics.push(Diagnostic {
                    severity: Severity::Error,
                    code: Code("E2001".to_string()),
                    message: format!("[{dname}] Type mismatch — {}", err.message),
                    labels: Vec::new(),
                    suggestion: None,
                });
            }

            // exhaustiveness (warnings, not type errors)
            let warns = exhaustive::check_body(body, &world);
            out.exhaustiveness_warnings += warns.len();
            out.diagnostics.extend(warns);

            // per-def type entry: declared annotation wins, else inferred body.
            let name = names.get(def).cloned().unwrap_or_default();
            if name.is_empty() {
                continue;
            }
            let (ty, declared) = match world.value_sigs.get(def) {
                Some(s) => (s.ty.clone(), true),
                None => (inferred.unwrap_or(Ty::Error), false),
            };
            out.def_types.push(DefType {
                module: mname.clone(),
                name,
                ty,
                declared,
            });
        }
    }
    // stable order for the type table (L4)
    out.def_types
        .sort_by(|a, b| (a.module.as_str(), a.name.as_str()).cmp(&(b.module.as_str(), b.name.as_str())));
    let _ = Span::new(base::FileId(0), 0, 0); // (spans unavailable in HIR bodies — see report)
    out
}
