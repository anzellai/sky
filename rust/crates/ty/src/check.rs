//! Orchestration: typecheck a set of modules against the loaded world (doc 06).
//! Produces the type-error diagnostics + per-def type table (the "per-region
//! type map output (name → inferred type)" the lowerer will consume).

use crate::db::TyDb;
use crate::dictkey;
use crate::exhaustive;
use crate::infer::Infer;
use crate::sig::World;
use crate::{Scheme, Ty};
use base::{DefId, ModuleId, Span};
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
/// When a type error is an `AppConfig` vs `record` mismatch, the user almost
/// certainly wrote the pre-v0.19 row-open record form for `Live.app` / `Tui.app`
/// / `Cli.program`. Turn the cryptic `type mismatch: AppConfig _ _ vs record`
/// into an actionable migration hint (rendered as `Try: …`).
fn builder_cfg_migration_hint(message: &str) -> Option<String> {
    if message.contains("AppConfig") && message.contains("record") {
        Some(
            "the app config is a typed BUILDER since v0.19, not a record — wrap it: \
             `Live.app (Live.config { …required… } |> Live.withHead … )`. Optional \
             fields (head / guard / analytics / onKey / onLine / …) become `|> withX …`. \
             Same for Tui.app / Tui.program / Cli.program. \
             See docs/v0.19/migration-builder-cfg.md"
                .to_string(),
        )
    } else {
        None
    }
}

/// When a `String` is supplied where a `Secret` is now required (or vice
/// versa), the caller almost certainly wrote pre-Secret code passing a raw
/// String secret to `Auth.signToken` / `Auth.verifyToken` / `Jwt.hs256` /
/// `signSlidingToken`. Turn the bare `type mismatch: Secret vs String` into an
/// actionable migration hint (rendered as `Try: …`).
fn secret_migration_hint(message: &str) -> Option<String> {
    if message.contains("Secret") && message.contains("String") {
        Some(
            "secret-bearing arguments are the opaque `Secret` type now, not \
             `String` — a raw String secret can no longer leak into a log or a \
             response. Wrap it at the boundary: `Secret.fromEnv \"MY_SECRET\"` \
             (read from the environment), or `Secret.fromString someRuntimeString` \
             when you already hold the value. `import Sky.Core.Secret as Secret \
             exposing (Secret)`; the raw bytes come back only through \
             `Secret.reveal`. See docs/security/secret-migration.md"
                .to_string(),
        )
    } else {
        None
    }
}

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

/// One accumulated `[E2008]` finding: the offending key type, the def it is
/// attributed to, and the span it is best reported at.
struct DictKeyFinding {
    /// `Ty::render_pretty()` of the key — the dedup identity.
    rendered: String,
    key: Ty,
    /// The def whose name tags the message (`[grid] …`).
    def_name: String,
    span: Option<Span>,
    /// The span came from a WRITTEN type annotation rather than an inferred
    /// expression type.
    from_annotation: bool,
}

/// Accumulates the `[E2008]` findings for ONE MODULE and picks the span each is
/// best reported at.
///
/// **Deduplication is by the KEY TYPE's rendering, per module — not per def, and
/// not per occurrence.** This is a UX decision with teeth. A single
/// `Dict ( Int, Int ) v` field on a Sky.Live `Model` flows into the inferred
/// type of EVERY def that touches the model — `init`, `update`, `view`, every
/// helper — and each of those defs mentions it in most of its expressions.
/// Reporting per occurrence would bury the user under dozens of copies of one
/// message; reporting per def would still emit one per handler. There is exactly
/// ONE defect (the key type) and exactly ONE fix (change how the key is
/// encoded), so there is one diagnostic. A second, genuinely DIFFERENT offending
/// key type in the same module still gets its own.
///
/// **Span preference** (best caret first):
///   1. the def's own WRITTEN annotation type — the text the user must edit;
///   2. otherwise the EARLIEST-STARTING inferred-expression span carrying the
///      type — the first place in the file the offending dictionary appears,
///      which is where it gets built (`Dict.insert ( 1, 2 ) "a" Dict.empty`)
///      rather than some later read of it. Shortest span breaks a start tie, so
///      the caret lands on the callee rather than spanning the whole call, and
///      the choice is deterministic (L4).
#[derive(Default)]
struct DictKeyScan {
    found: Vec<DictKeyFinding>,
}

impl DictKeyScan {
    /// Fold one type — a declared signature, or one expression's inferred type —
    /// into the scan.
    fn add(&mut self, t: &Ty, def_name: &str, span: Option<Span>, from_annotation: bool) {
        for key in dictkey::unsupported_keys(t) {
            let rendered = key.render_pretty();
            match self.found.iter_mut().find(|f| f.rendered == rendered) {
                Some(slot) => {
                    if better_dict_key_span(span, from_annotation, slot.span, slot.from_annotation)
                    {
                        slot.span = span;
                        slot.from_annotation = from_annotation;
                        slot.def_name = def_name.to_string();
                    }
                }
                None => self.found.push(DictKeyFinding {
                    rendered,
                    key,
                    def_name: def_name.to_string(),
                    span,
                    from_annotation,
                }),
            }
        }
    }
}

/// Is `(cand, cand_anno)` a better anchor than the incumbent? See the span
/// preference documented on [`DictKeyScan`].
fn better_dict_key_span(
    cand: Option<Span>,
    cand_anno: bool,
    cur: Option<Span>,
    cur_anno: bool,
) -> bool {
    match (cand, cur) {
        (None, _) => false,
        (Some(_), None) => true,
        (Some(c), Some(k)) => {
            if cand_anno != cur_anno {
                return cand_anno;
            }
            let (cl, kl) = (c.range.1 - c.range.0, k.range.1 - k.range.0);
            (c.range.0, cl) < (k.range.0, kl)
        }
    }
}

/// `def name → span of the TYPE in its `name : Type` annotation`, read straight
/// off the CST.
///
/// `hir`'s `def_spans` records only a `Decl::Value`'s NAME token (it exists for
/// goto-definition), so an `[E2008]` anchored from it lands on `grid` in
/// `grid = …` — one line below the `grid : Dict Coord String` the user actually
/// has to edit. The annotation's type node is the honest caret for a defect that
/// IS the written type, and the CST already carries its range, so nothing in
/// `hir` or `ty::Ty` needs a new span channel to reach it.
///
/// `file` supplies the `FileId` (a `base::Span` carries one; a `text_range` does
/// not).
fn annotation_type_spans(parse: &syntax::Parse, file: base::FileId) -> HashMap<String, Span> {
    use syntax::ast::AstNode;
    let mut out = HashMap::new();
    for decl in parse.tree().decls() {
        let syntax::ast::Decl::TypeAnno(a) = decl else {
            continue;
        };
        let (Some(name), Some(ty)) = (a.name(), a.ty()) else {
            continue;
        };
        let r = ty.syntax().text_range();
        out.entry(name.text().to_string())
            .or_insert_with(|| Span::new(file, u32::from(r.start()), u32::from(r.end())));
    }
    out
}

/// Typecheck `to_check` module ids against the world built from every module in
/// `db` (stdlib + deps + entry). Never panics; partial results + diagnostics (L7).
pub fn check_modules(db: &dyn TyDb, to_check: &[ModuleId]) -> CheckOutput {
    // The FULL world (passes 1-6): the accept/reject checker runs `!use_inferred`
    // inference, which consults the body-derived `app_check_sigs` /
    // `any_result_check_sigs` pins. `type_world` (declarations only) omits them so
    // it can backdate on the salsa backend — the checker takes `check_world`.
    check_modules_with_world(db, db.check_world(), to_check)
}

/// [`check_modules`] against an ALREADY-ASSEMBLED world.
///
/// The one seam the shared-world corpus path (`crate::shared`) needs: it forks a
/// prebuilt stdlib world and folds in the case's modules, so it must hand the
/// checker that world instead of having the checker demand a fresh
/// `db.check_world()` — which is the whole 1.29 s/case this exists to remove.
/// Everything after the world is obtained is shared verbatim between the two
/// paths, so a divergence can only come from the world, never from the checking.
pub fn check_modules_with_world(
    db: &dyn TyDb,
    world: std::rc::Rc<crate::sig::World>,
    to_check: &[ModuleId],
) -> CheckOutput {
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

        // `[E2008]` state, accumulated across the WHOLE module (see
        // `DictKeyScan` for why the dedup is per module, not per def).
        let anno_spans = resolved
            .def_spans
            .first()
            .map(|(_, s)| annotation_type_spans(&sky.module_parse(mid), s.file))
            .unwrap_or_default();
        let mut dict_keys = DictKeyScan::default();

        for (def, body) in &resolved.bodies {
            let dname = names.get(def).cloned().unwrap_or_default();
            // `with_record_exprs` makes the solved per-expression types readable
            // after inference WITHOUT changing inference (see its doc comment).
            // The `[E2008]` scan below needs them to catch a composite key that
            // exists only in an INFERRED type — `Dict.insert ( 1, 2 ) "a"
            // Dict.empty` in an unannotated binding writes no `Dict` type
            // anywhere, so an annotation-only check would miss it entirely.
            let mut infer = Infer::new(&world, sky)
                .with_self_def(Some(*def))
                .with_record_exprs(true);
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
                    code: Code(err.code.to_string()),
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
                    suggestion: builder_cfg_migration_hint(&err.message)
                        .or_else(|| secret_migration_hint(&err.message)),
                });
            }

            // ---- [E2008] unsupported `Dict` key -------------------------
            //
            // A `Dict k v` is a Go `map[string]v`; only String/Int/Float/Char/
            // Bool decode back out. A COMPOSITE key can NEVER work — `%v` is not
            // injective on composites, so two distinct keys collide and one
            // entry is silently lost — and it used to surface as the runtime
            // panic `rt.Dict: unsupported key type` from a program `sky check`
            // had passed. A panic out of well-typed Sky is exactly what this
            // language promises not to do, so it is a type error instead.
            //
            // Both faces are scanned: the DECLARED annotation (best caret, and
            // the case a user can read off their own source) and every INFERRED
            // expression type (the unannotated case an annotation-only check
            // would miss). `dictkey::classify` is SILENT on anything not pinned
            // to a concrete type, so a key-polymorphic `Dict k v` — ordinary,
            // valid Sky — never fires. See `dictkey.rs` for that rule.
            if let Some(scheme) = world.value_sigs.get(def) {
                dict_keys.add(&scheme.ty, &dname, anno_spans.get(&dname).copied(), true);
            }
            for (e, ty) in infer.recorded_expr_types() {
                dict_keys.add(&ty, &dname, body.expr_span(e), false);
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

        // One `[E2008]` per distinct offending key type in this module.
        for f in &dict_keys.found {
            out.type_errors += 1;
            out.diagnostics.push(Diagnostic {
                severity: Severity::Error,
                code: Code("E2008".to_string()),
                message: format!("[{}] {}", f.def_name, dictkey::message(&f.key)),
                labels: f
                    .span
                    .map(|s| {
                        vec![diagnostics::Label {
                            span: trim_leading_ws(&module_src, s),
                            message: "this dictionary's key type".into(),
                        }]
                    })
                    .unwrap_or_default(),
                suggestion: Some(dictkey::suggestion()),
            });
        }
    }
    // stable order for the type table (L4)
    out.def_types.sort_by(|a, b| {
        (a.module.as_str(), a.name.as_str()).cmp(&(b.module.as_str(), b.name.as_str()))
    });
    out
}

#[cfg(test)]
mod migration_hint_tests {
    use super::{builder_cfg_migration_hint, secret_migration_hint};

    #[test]
    fn secret_hint_fires_on_secret_vs_string_mismatch() {
        // The exact message shape the checker renders for a raw-String secret
        // passed to Auth.signToken / Jwt.hs256 after the Secret migration.
        let h = secret_migration_hint("type mismatch: `Secret` vs `String`")
            .expect("Secret/String mismatch must produce a migration hint");
        assert!(h.contains("Secret.fromEnv"), "hint names the fix: {h}");
        assert!(h.contains("Secret.reveal"), "hint names the escape hatch: {h}");
        // order-independent: String-vs-Secret must also fire.
        assert!(secret_migration_hint("type mismatch: `String` vs `Secret`").is_some());
    }

    #[test]
    fn secret_hint_silent_on_unrelated_mismatch() {
        assert!(secret_migration_hint("type mismatch: `Int` vs `Bool`").is_none());
        // A String-only mismatch (no Secret in play) must not misfire.
        assert!(secret_migration_hint("type mismatch: `String` vs `Char`").is_none());
    }

    #[test]
    fn builder_and_secret_hints_are_distinct_and_dont_cross_fire() {
        assert!(builder_cfg_migration_hint("type mismatch: AppConfig _ _ vs record").is_some());
        assert!(secret_migration_hint("type mismatch: AppConfig _ _ vs record").is_none());
        assert!(builder_cfg_migration_hint("type mismatch: `Secret` vs `String`").is_none());
    }
}
