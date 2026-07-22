//! Orchestration: typecheck a set of modules against the loaded world (doc 06).
//! Produces the type-error diagnostics + per-def type table (the "per-region
//! type map output (name → inferred type)" the lowerer will consume).

use crate::db::TyDb;
use crate::exhaustive;
use crate::infer::Infer;
use crate::sig::World;
use crate::{Scheme, Ty};
use base::{DefId, ModuleId};
use diagnostics::{Code, Diagnostic, Severity};
use hir::{Body, ExprId, LocalId};
use std::collections::HashMap;
use std::rc::Rc;

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
///
/// This is the value of the `infer(DefId)` tracked query (Stage D-2 — doc 01's
/// `infer(DefId) -> types, per-region type map`). It is deliberately `no_eq` as
/// a salsa output (recompute-on-demand is fine; the lowerer is not yet a query),
/// but derives `Clone` so the trait accessor can hand an owned copy back out of
/// the memo, mirroring `SkyDb::resolve`'s `Rc`-clone.
#[derive(Default, Clone)]
pub struct BodyTypes {
    pub result: Option<Ty>,
    /// The def's FULL inferred type — the arrow spine of the (read-back) param
    /// types folded over `result` (`p0 -> p1 -> … -> result`). For a nullary def
    /// this equals `result`. Populated for the tooling layer (hover / inlay) so an
    /// unannotated function presents `foo : Int -> Int -> Int`, not the body-root
    /// `result` alone. STRICTLY ADDITIVE + tooling-only: the lowerer reads
    /// `result`/`exprs`/`locals` and never this field, so codegen is unchanged.
    pub signature: Option<Ty>,
    pub exprs: HashMap<ExprId, Ty>,
    pub locals: HashMap<LocalId, Ty>,
}

/// A reusable typed view of a whole program (stdlib + deps + entry). The world
/// comes from `db.type_world()` — a single memoised assembly on the salsa backend
/// (so the build's several `Typer::new` sites share one world), or an eager build
/// on `SourceDb`. `body_types` routes through the `infer(DefId)` query, so a def's
/// typed table is memoised at per-def granularity on the salsa backend.
pub struct Typer<'a> {
    world: Rc<World>,
    db: &'a dyn TyDb,
}

impl<'a> Typer<'a> {
    pub fn new(db: &'a dyn TyDb) -> Self {
        Typer {
            world: db.type_world(),
            db,
        }
    }

    /// The per-expression + per-local type table for `def` (in `module`) — routed
    /// through the `infer(DefId)` query (`TyDb::body_types_of`), so it is memoised
    /// per def on the salsa backend. `module` is the def's own module (the caller
    /// has it from its `resolve` walk); it lets the query reach the body without a
    /// revision-unsafe interned-`DefKey` read. The `body` argument is retained for
    /// call-site compatibility with the lowerer (which has it in hand); the query
    /// refetches the identical body from `resolve`, so both agree. Recursive
    /// self-references stay monomorphic inside the query body (its `with_self_def`).
    pub fn body_types(&self, module: ModuleId, def: DefId, _body: &Body) -> BodyTypes {
        (*self.db.body_types_of(module, def)).clone()
    }

    /// Like [`Typer::body_types`], but additionally seeds param/local types from
    /// the def's declared signature (the annotation). Used by the tooling layer
    /// (hover / completion) so a param reflects `f : Int -> Int` rather than the
    /// body-only-inferred `number`. Kept SEPARATE from `body_types` so the
    /// lowerer's typed table — and therefore codegen — is byte-for-byte
    /// unchanged (the LSP is additive).
    pub fn body_types_annotated(&self, def: DefId, body: &Body) -> BodyTypes {
        let mut infer = Infer::new(&self.world, self.db.as_sky_db())
            .with_self_def(Some(def))
            .with_inferred(true)
            .with_expected(self.world.value_sigs.get(&def).cloned());
        let (result, signature, exprs, locals) = infer.infer_def_typed(body);
        BodyTypes {
            result,
            signature,
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

/// Advance a span's start byte past any leading whitespace (within the span),
/// so a diagnostic caret anchors under the first real character of the offending
/// expression rather than the trailing newline / indentation trivia the CST
/// carries at the head of a body-RHS region. The end byte is untouched. If the
/// span is entirely whitespace (never expected for a real expression) or already
/// tight, it is returned unchanged.
fn trim_leading_ws(src: &str, span: base::Span) -> base::Span {
    let start = span.range.0 as usize;
    let end = (span.range.1 as usize).min(src.len());
    if start >= end {
        return span;
    }
    let mut ns = start;
    for ch in src[start..end].chars() {
        if ch.is_whitespace() {
            ns += ch.len_utf8();
        } else {
            break;
        }
    }
    if ns == start || ns >= end {
        return span;
    }
    base::Span::new(span.file, ns as u32, span.range.1)
}

/// Typecheck `to_check` module ids against the world built from every module in
/// `db` (stdlib + deps + entry). Never panics; partial results + diagnostics (L7).
pub fn check_modules(db: &dyn TyDb, to_check: &[ModuleId]) -> CheckOutput {
    // The FULL world (passes 1-6): the accept/reject checker runs `!use_inferred`
    // inference, which consults the body-derived `app_check_sigs` /
    // `any_result_check_sigs` pins. `type_world` (declarations only) omits them so
    // it can backdate on the salsa backend — the checker takes `check_world`.
    let world = db.check_world();
    let sky = db.as_sky_db();
    let mut out = CheckOutput::default();

    // Elm-like import-cycle rejection. An app-module import cycle otherwise
    // leaves the cycle's unannotated defs at wildcard flex (the sig pass defers
    // cyclic SCCs), which defeats the `go build` backstop and lets cross-cycle
    // misuse slip through `sky check`. Reject each cycle once, attributed to a
    // member actually being checked. (The oracle types cycles; Sky deliberately
    // diverges — verified 0 cycles in the corpus + stdlib, so this rejects only
    // a future cycle a project might introduce.)
    let to_check_set: std::collections::HashSet<ModuleId> = to_check.iter().copied().collect();
    for group in crate::sig::app_import_cycle_groups(sky) {
        let Some(&anchor) = group.iter().find(|m| to_check_set.contains(m)) else {
            continue;
        };
        let mut names: Vec<String> = group
            .iter()
            .map(|m| sky.module_name(*m).to_string())
            .collect();
        names.sort();
        let labels = sky
            .resolve(anchor)
            .def_spans
            .first()
            .map(|(_, s)| {
                vec![diagnostics::Label {
                    span: *s,
                    message: "this module is part of an import cycle".into(),
                }]
            })
            .unwrap_or_default();
        out.name_errors += 1;
        out.diagnostics.push(Diagnostic {
            severity: Severity::Error,
            code: Code("E1010".to_string()),
            message: format!(
                "Import cycle: the modules {} import each other, forming a cycle. \
                 Sky rejects import cycles — break it by extracting the shared \
                 definitions into a separate module that both import.",
                names.join(" ↔ ")
            ),
            labels,
            suggestion: None,
        });
    }

    for &mid in to_check {
        let mname = sky.module_name(mid).to_string();
        // Module source text — used to tighten an E2001 label span to the first
        // non-whitespace byte of the offending expression's region (see
        // `trim_leading_ws`). A body-RHS span (e.g. the whole `main = <expr>`
        // binding) recorded by the resolver includes the leading newline +
        // indentation between `=` and the expression, so its start byte lands on
        // the `main =` line; trimming re-anchors the caret under the real
        // sub-expression (`"not an int"`), matching the Haskell oracle.
        let module_src = sky.module_parse(mid).syntax().text().to_string();
        let resolved = sky.resolve(mid);
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
            let mut infer = Infer::new(&world, sky).with_self_def(Some(*def));
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
                    // The `-- TYPE MISMATCH --` header already names the category;
                    // don't repeat it in the message (was `Type mismatch — type
                    // mismatch: …`). Keep the `[def]` context tag.
                    message: format!("[{dname}] {}", err.message),
                    labels: err
                        .span
                        .map(|s| {
                            vec![diagnostics::Label {
                                span: trim_leading_ws(&module_src, s),
                                message: "this expression".into(),
                            }]
                        })
                        .unwrap_or_default(),
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
    out.def_types.sort_by(|a, b| {
        (a.module.as_str(), a.name.as_str()).cmp(&(b.module.as_str(), b.name.as_str()))
    });
    out
}
