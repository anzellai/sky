//! Orchestration: typecheck a set of modules against the loaded world (doc 06).
//! Produces the type-error diagnostics + per-def type table (the "per-region
//! type map output (name → inferred type)" the lowerer will consume).

use crate::exhaustive;
use crate::infer::Infer;
use crate::sig::World;
use crate::Ty;
use base::{ModuleId, Span};
use diagnostics::{Code, Diagnostic, Severity};
use hir::SourceDb;
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
    pub diagnostics: Vec<Diagnostic>,
    pub def_types: Vec<DefType>,
}

/// Typecheck `to_check` module ids against the world built from every module in
/// `db` (stdlib + deps + entry). Never panics; partial results + diagnostics (L7).
pub fn check_modules(db: &SourceDb, to_check: &[ModuleId]) -> CheckOutput {
    let world = World::build(db);
    let mut out = CheckOutput::default();

    for &mid in to_check {
        let mname = db.module_name(mid).to_string();
        let resolved = hir::resolve(db, mid);
        let names: HashMap<base::DefId, String> = resolved
            .top_defs
            .iter()
            .map(|td| (td.def, td.name.as_str().to_string()))
            .collect();

        for (def, body) in &resolved.bodies {
            let dname = names.get(def).cloned().unwrap_or_default();
            let mut infer = Infer::new(&world, db);
            let inferred = infer.infer_def(body);
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
