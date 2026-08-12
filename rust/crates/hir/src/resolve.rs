//! The resolver (doc 05 §4-§12). Builds a module environment once (builtins →
//! imports → local decls) then walks each top-level body with a lexical scope
//! stack. Every reference becomes a `Res`; an unresolvable name becomes
//! `Res::Error` + a diagnostic and the walk continues (L7).

use crate::cst;
use crate::db::{ImportSource, SkyDb};
use crate::exports::ModuleExports;
use crate::hir::{Body, CaseBranch, Expr, ExprId, LocalDef, PatId, Pattern, TopDef, Type, TypeId};
use crate::ids::{CtorRef, DefKind, LocalId, Res, TypeRes};
use crate::kernel::{
    kernel_functions, BUILTIN_CTORS, BUILTIN_TYPES, BUILTIN_VARS, KERNEL_IMPLICIT_TYPES,
    PRELUDE_QUALIFIERS,
};
use base::{DefId, FileId, ModuleId, Name, Span};
use diagnostics::Diagnostic;
use indexmap::IndexMap;
use std::collections::{HashMap, HashSet};
use syntax::ast::{self, AstNode};
use syntax::SyntaxKind;

/// Prelude-exposed type + constructor names a user-defined ADT/alias may NOT
/// shadow (oracle: Canonicalise audit §3.2). A user
/// `type Result a = Just a | Nothing` (or any type/ctor reusing one of these
/// names) is a hard canonicalise-time rejection — it silently shadows the
/// Prelude entry and produces confusing downstream errors. The canonical
/// stdlib modules that legitimately DEFINE these (`Sky.Core.Maybe`, …) are
/// never gated here: this diagnostic is surfaced through `check_modules`, which
/// only checks app-code modules (`check_ids`), never the trusted stdlib.
const PRELUDE_RESERVED: &[&str] = &[
    "Int", "Float", "Bool", "String", "Char", "List", "Maybe", "Result", "Task", "Error", "True",
    "False", "Just", "Nothing", "Ok", "Err",
];

/// Synthetic module id for builtin defs (Prelude types/ctors). Never collides
/// with a real module (real ids are small indices).
const BUILTIN_MOD: ModuleId = ModuleId(u32::MAX);

/// What kind of reference an unresolved name was, for the gate report.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum RefKind {
    Value,
    Ctor,
    Type,
}

/// A class-(a) unresolved name: should have resolved from Sky / stdlib / Prelude
/// / kernel, but didn't. A genuine resolver gap.
#[derive(Clone, Debug)]
pub struct ClassA {
    pub qualifier: Option<String>,
    pub name: String,
    pub kind: RefKind,
    pub reason: String,
}

/// A class-(b) reference into a Go FFI package — expected until the FFI surface
/// (doc 09 / M3) lands.
#[derive(Clone, Debug)]
pub struct ClassB {
    pub package: String,
    pub qualifier: Option<String>,
    pub name: String,
    pub kind: RefKind,
}

/// An LSP reference occurrence: an identifier span and what it resolved to,
/// tagged with the enclosing top-level def (so a `Res::Local` hover can look up
/// its type in the owning body's inference table). Populated additively for the
/// tooling layer (doc 10 §"resolve_at"); ignored by lowering + typecheck.
#[derive(Clone, Debug)]
pub struct RefOcc {
    pub span: Span,
    pub res: Res,
    pub owner: DefId,
}

/// A `<receiver>.field` occurrence — the field-name span, the receiver expr id
/// within `owner`'s body, and the field name. Hover/goto resolve the receiver's
/// inferred record type to find the field's type + declaration (doc 10).
#[derive(Clone, Debug)]
pub struct FieldOcc {
    pub span: Span,
    pub receiver: ExprId,
    pub field: Name,
    pub owner: DefId,
}

/// A type-name reference occurrence → its type constructor `DefId` (goto/hover
/// on a type name in an annotation or ctor-arg position).
#[derive(Clone, Debug)]
pub struct TypeOcc {
    pub span: Span,
    pub con: DefId,
    pub name: Name,
}

/// A local binder definition site (lambda param / let binding / case-pattern
/// binder) — the goto-def target for a `Res::Local` use.
#[derive(Clone, Debug)]
pub struct BinderDef {
    pub owner: DefId,
    pub local: LocalId,
    pub span: Span,
}

/// A record-alias field declaration — the goto-def target for a field access.
#[derive(Clone, Debug)]
pub struct FieldDecl {
    pub field: Name,
    pub siblings: Vec<Name>,
    pub span: Span,
}

/// The lexical class of an in-scope UNQUALIFIED name (for `scope_names`, the
/// LSP unqualified-completion index). Value covers ordinary bindings + kernel
/// functions; Ctor covers data constructors; Type covers type constructors +
/// aliases. Additive tooling data — ignored by lowering + typecheck.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ScopeNameKind {
    Value,
    Ctor,
    Type,
}

/// The result of resolving one module (doc 05 §1).
///
/// `Clone`: the salsa build path memoises resolution as the `#[salsa::tracked]`
/// `skydb::resolve_query` and hands each caller an owned `Rc<ResolveResult>`
/// (cloned out of the memo, exactly like `module_exports`); the clone is what
/// lets the tracked query's `&ResolveResult` cross the `SkyDb::resolve` seam.
/// `Debug`: used by the incremental-correctness harness' projection.
#[derive(Default, Clone, Debug)]
pub struct ResolveResult {
    pub bodies: IndexMap<DefId, Body>,
    pub top_defs: Vec<TopDef>,
    pub diagnostics: Vec<Diagnostic>,
    pub class_a: Vec<ClassA>,
    pub class_b: Vec<ClassB>,
    // ---- LSP index (doc 10) — additive; empty of consequence to build/check ----
    /// Value/ctor/kernel reference occurrences, span-keyed (resolve_at).
    pub ref_occs: Vec<RefOcc>,
    /// `receiver.field` occurrences.
    pub field_occs: Vec<FieldOcc>,
    /// Type-name reference occurrences.
    pub type_occs: Vec<TypeOcc>,
    /// Local binder definition sites.
    pub binders: Vec<BinderDef>,
    /// Declaration id → its name-token span (value / type-con / alias / ctor).
    pub def_spans: Vec<(DefId, Span)>,
    /// Record-alias field declarations.
    pub field_decls: Vec<FieldDecl>,
    /// Import qualifier → source module (for qualified completion `M.`).
    pub qualifiers: HashMap<String, ImportSource>,
    /// Every in-scope UNQUALIFIED name (name + lexical class), snapshotted after
    /// environment building: builtins + Prelude, `exposing`-imported names, and
    /// this module's own top-level defs. Powers unqualified completion (bare `pr`
    /// offers `println`); lexical locals are NOT here (they live in `binders`).
    pub scope_names: Vec<(String, ScopeNameKind)>,
    /// Type-name resolution for THIS module: every in-scope type-reference name,
    /// both bare (`"Model"`) and qualified (`"Q.Model"`, keyed by the qualifier
    /// as written — import alias or auto-qualifier), mapped to its resolved
    /// constructor. Lets a downstream crate map a type reference to its DEFINING
    /// module (via `def_loc(con).module`) instead of re-deriving identity from
    /// syntax by bare name — the fix for same-named aliases in different modules
    /// being conflated (issue #164). Foreign (Go FFI) qualifier members are
    /// omitted (they are not Sky type constructors).
    pub type_refs: HashMap<String, TypeRes>,
}

/// Resolve a module. Never panics; partial results + diagnostics (L7).
pub fn resolve(db: &dyn SkyDb, module: ModuleId) -> ResolveResult {
    let mut r = Resolver::new(db, module);
    r.build_env();
    r.walk_module();
    r.result
}

// ---------------------------------------------------------------------------
// Unqualified-name precedence (doc 05 §6b) — the ambiguity rule
//
// Before this existed, every binding of an unqualified name went into one flat
// `vars` / `ctors` map with a plain `.insert()`, so a name bound by two imports
// silently resolved to whichever import came LAST in the file. Two byte-identical
// programs differing only in the ORDER of two import lines computed different
// values, with no diagnostic either way (`corpus/repro/ambiguous-exposing-all/`).
// Reordering imports is something a formatter, a merge, or an added import does
// routinely, so "last wins" turns a difference that must not matter into a
// different program — the #164 family.
//
// The fix is a PRECEDENCE LATTICE plus a use-site ambiguity error. Every binding
// records which layer it entered scope through; a name bound in several layers
// resolves to the highest one, deterministically and independently of import
// order. Only when the WINNING layer holds two DIFFERENT definitions is the name
// ambiguous — and that is reported at the USE SITE, never at the import.
// ---------------------------------------------------------------------------

/// Precedence layer of an unqualified binding. Higher wins outright, with no
/// diagnostic; a tie inside the winning layer is the ambiguity error.
///
/// The layering is what makes the rule non-breaking. A naive "bound twice =
/// error" rule rejects working programs today: `import Sky.Core.Prelude exposing
/// (..)` together with `import Sky.Core.Math exposing (..)` already binds `abs` /
/// `min` / `max` / `sqrt` twice, and real examples do exactly that. Prelude sits
/// in [`Ambient`](BindLayer::Ambient), so an explicit import shadows it silently
/// — which is both what happens today and what Elm does with its implicit
/// `Basics`.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug)]
enum BindLayer {
    /// Unconditional builtins (`BUILTIN_VARS` / `BUILTIN_CTORS`) and the
    /// autoloaded Prelude ([`AMBIENT_IMPORTS`]). The user did not choose these:
    /// `sky init`'s templates emit `import Sky.Core.Prelude exposing (..)`
    /// unconditionally and AGENTS.md documents the module as autoloaded, so its
    /// presence is not an authorial claim on the name and must never make an
    /// explicit import ambiguous.
    Ambient = 0,
    /// `import M exposing (..)` — a BULK claim on whatever `M` happens to export
    /// today. Two of these claiming one name is the defect this rule closes.
    Open = 1,
    /// `import M exposing (name)` — the author named this binding specifically.
    /// A more precise claim than `(..)`, so it wins over one; two explicit lists
    /// naming the same thing are equally deliberate and stay ambiguous.
    Explicit = 2,
    /// Defined in this module. Always wins — a local shadowing an import is
    /// long-standing legal Sky (doc 05 C7) and stays legal, silently.
    Local = 3,
}

/// Imports whose bindings enter at [`BindLayer::Ambient`] — see the enum.
///
/// Both spellings of the prelude are listed because `kernel::KERNEL_MODULES`
/// maps them to the SAME pseudo-module (`Basics`). Keying ambient-ness on the
/// path alone would otherwise make `import Sky.Core.Basics exposing (..)` +
/// `import Sky.Core.Math exposing (..)` ambiguous on `abs` while the
/// `Sky.Core.Prelude` spelling of the identical program compiled — the layer a
/// binding lands in must not depend on which alias of one module was written.
const AMBIENT_IMPORTS: &[&str] = &["Sky.Core.Prelude", "Sky.Core.Basics"];

/// One binding of one unqualified name, with the provenance an ambiguity error
/// needs: which layer it came in through, which module it came from, and the
/// qualifier that would disambiguate it at a use site.
#[derive(Clone)]
struct Origin {
    layer: BindLayer,
    /// The import path as written (`Std.Html.Attributes`) — what the error names.
    module: String,
    /// The qualifier a use site can write to select THIS binding (`Attr`, `Html`
    /// …). `None` when the import binds no qualifier (auto-qualifier suppressed
    /// by explicit-alias-wins, doc 05 §6), in which case the error tells the user
    /// to add an alias instead of offering a qualified form that would not
    /// compile.
    qualifier: Option<String>,
    /// Identity of the thing bound. Two origins with EQUAL keys are the same
    /// definition reached by two routes (a re-export, or a module imported twice)
    /// and are NOT ambiguous — only genuinely different definitions are.
    key: String,
    /// What `vars` / `ctors` should map the name to if this origin wins.
    res: Res,
}

/// One binding of one unqualified TYPE name.
///
/// Separate from [`Origin`] because a type binding carries a [`TypeRes`] rather
/// than a [`Res`], and because its identity needs a third state that values do
/// not have — see [`TypeKey`].
#[derive(Clone)]
struct TypeOrigin {
    layer: BindLayer,
    module: String,
    qualifier: Option<String>,
    key: TypeKey,
    res: TypeRes,
}

/// Identity of a type binding, for the "same type reached twice" test.
///
/// The extra state versus [`res_key`] is [`Opaque`](TypeKey::Opaque), and it is
/// the whole reason the type namespace was excluded from `[E1012]` when the
/// value lattice landed. Several type paths SYNTHESISE a `DefId` for a name the
/// target module does not really export, so a `DefId` comparison can report two
/// identities for one conceptual type and reject a working program — the #164
/// failure mode. Rather than compare fabricated identities, a binding whose
/// identity was fabricated says so, and an ambiguity verdict involving one is
/// never reached.
///
/// That is deliberately conservative: it trades false REJECTIONS (which break
/// working programs and get reverted) for false NEGATIVES (which leave today's
/// behaviour exactly as it is). The lenient sites are individually narrowed
/// first — see `kernel_implicit_type_def` and `chase_reexported_type` — so
/// `Opaque` is the residue, not the rule.
#[derive(Clone, PartialEq, Eq)]
enum TypeKey {
    /// A real, comparable identity: this name was found in the target module's
    /// published exports, declared locally, or is a builtin / kernel-implicit
    /// type with ONE program-wide `DefId`.
    Id(String),
    /// The identity was fabricated because nothing authoritative was available
    /// (a Go FFI type, or a re-export that could not be chased to its
    /// declaration). Never compares equal to anything, including itself, so it
    /// can neither create nor join an ambiguity.
    Opaque,
}

/// Identity of a resolution, for the "same definition reached twice" test.
fn res_key(r: &Res) -> String {
    match r {
        Res::Def(d) => format!("def:{}", d.0),
        Res::Kernel { module, func } => format!("kernel:{}.{}", module.as_str(), func.as_str()),
        Res::Foreign { package, name } => format!("ffi:{}.{}", package.as_str(), name.as_str()),
        Res::Ctor(c) => format!("ctor:{}", c.def.0),
        Res::Local(l) => format!("local:{}", l.0),
        Res::Error => "error".to_string(),
    }
}

/// The import currently being processed, so the `bind_*` helpers can stamp
/// provenance without every one of them taking four more parameters.
#[derive(Clone)]
struct ImportCtx {
    module: String,
    qualifier: Option<String>,
    layer: BindLayer,
}

/// Outcome of applying the precedence lattice to one name's origins.
enum Settled {
    /// One definition wins its layer outright — bind it, say nothing.
    Winner(Res),
    /// The winning layer holds several DIFFERENT definitions.
    Ambiguous(Vec<Origin>),
}

/// Apply the lattice to one name. `None` when the name was bound once (the
/// overwhelming majority — nothing to settle, and nothing to re-insert).
fn settle_one(origins: &[Origin]) -> Option<Settled> {
    if origins.len() < 2 {
        return None;
    }
    let top = origins.iter().map(|o| o.layer).max()?;
    // Distinct DEFINITIONS in the winning layer. The same definition reached
    // twice — a re-export, a module imported under two forms, `exposing (..)`
    // plus `exposing (name)` on one module — is one binding, not a conflict.
    let mut distinct: Vec<&Origin> = Vec::new();
    for o in origins.iter().filter(|o| o.layer == top) {
        if !distinct.iter().any(|d| d.key == o.key) {
            distinct.push(o);
        }
    }
    match distinct.len() {
        0 => None,
        1 => Some(Settled::Winner(distinct[0].res.clone())),
        _ => Some(Settled::Ambiguous(
            distinct.into_iter().cloned().collect(),
        )),
    }
}

/// Outcome of applying the precedence lattice to one TYPE name's origins.
enum SettledType {
    Winner(TypeRes),
    Ambiguous(Vec<TypeOrigin>),
}

/// [`settle_one`] for the type namespace. Same lattice, one extra rule: an
/// [`Opaque`](TypeKey::Opaque) identity cannot be told apart from anything, so a
/// winning layer containing one is never called ambiguous — it resolves to the
/// last binding, exactly as it did before this rule existed.
fn settle_one_type(origins: &[TypeOrigin]) -> Option<SettledType> {
    if origins.len() < 2 {
        return None;
    }
    let top = origins.iter().map(|o| o.layer).max()?;
    let winning: Vec<&TypeOrigin> = origins.iter().filter(|o| o.layer == top).collect();
    // Any fabricated identity in the winning layer disqualifies the whole
    // verdict. Reporting on it would be guessing that two names we could not
    // resolve denote different types, which is precisely the guess that
    // manufactures false rejections.
    if winning.iter().any(|o| o.key == TypeKey::Opaque) {
        return winning.last().map(|o| SettledType::Winner(o.res));
    }
    let mut distinct: Vec<&TypeOrigin> = Vec::new();
    for o in &winning {
        if !distinct.iter().any(|d| d.key == o.key) {
            distinct.push(o);
        }
    }
    match distinct.len() {
        0 => None,
        1 => Some(SettledType::Winner(distinct[0].res)),
        _ => Some(SettledType::Ambiguous(
            distinct.into_iter().cloned().collect(),
        )),
    }
}

fn join_and(items: &[String]) -> String {
    join_with(items, "and")
}

fn join_or(items: &[String]) -> String {
    join_with(items, "or")
}

fn join_with(items: &[String], conj: &str) -> String {
    match items {
        [] => String::new(),
        [a] => a.clone(),
        [a, b] => format!("{a} {conj} {b}"),
        _ => {
            let (last, head) = items.split_last().unwrap();
            format!("{}, {conj} {last}", head.join(", "))
        }
    }
}

struct Resolver<'a> {
    db: &'a dyn SkyDb,
    module: ModuleId,
    file: FileId,

    // module-level namespaces
    vars: IndexMap<String, Res>,
    ctors: IndexMap<String, CtorRef>,
    types: IndexMap<String, TypeRes>,
    qual_vars: HashMap<String, HashMap<String, Res>>,
    qual_ctors: HashMap<String, HashMap<String, CtorRef>>,
    qual_types: HashMap<String, HashMap<String, TypeResEntry>>,
    import_aliases: HashMap<String, ImportSource>,
    /// Kernel pseudo-modules imported with `exposing (..)` — a lenient fallback
    /// for bare names we can't enumerate (no static kernel-function table).
    kernel_open: Vec<String>,
    /// A Go FFI package imported with `exposing (..)` — bare unresolved names
    /// are attributed to it (class-b), not a resolver gap.
    foreign_open: Option<String>,

    /// Provenance for every unqualified binding, parallel to `vars` / `ctors`
    /// (doc 05 §6b). Written by `bind_var` / `bind_ctor`, consumed once by
    /// `settle_precedence` at the end of `build_env`.
    var_origins: HashMap<String, Vec<Origin>>,
    ctor_origins: HashMap<String, Vec<Origin>>,
    /// Provenance for every unqualified TYPE binding, parallel to `types`.
    type_origins: HashMap<String, Vec<TypeOrigin>>,
    /// Names whose winning layer holds two or more DIFFERENT definitions. The
    /// binding in `vars` / `ctors` is left alone (so resolution stays total);
    /// the error is raised when a use site actually reads the name.
    ambiguous_vars: HashMap<String, Vec<Origin>>,
    ambiguous_ctors: HashMap<String, Vec<Origin>>,
    ambiguous_types: HashMap<String, Vec<TypeOrigin>>,
    /// The import being processed, for provenance stamping. `None` outside
    /// `process_import` (builtins and local decls stamp their layer directly).
    cur_import: Option<ImportCtx>,
    /// `(name, span-start)` pairs already reported ambiguous, so one reference
    /// visited twice (or a name read in a re-walked body) reports once.
    reported_ambiguous: HashSet<(String, u32)>,

    // lexical scope
    scopes: Vec<HashMap<String, LocalId>>,
    next_local: u32,
    /// When set, `bind_local` reuses an existing same-name binding in the
    /// current scope instead of allocating a fresh id. Used when resolving a
    /// destructure pattern whose binder names were already bound by the `let`
    /// pre-pass — so the pattern's `Var` ids match the ids the body references.
    reuse_binders: bool,
    /// Non-zero suppresses diagnostics/class tracking (interpolation interior,
    /// which the oracle never rejects — doc 03 §1.6).
    quiet: u32,

    /// The DefId of the top-level def whose body is currently being walked —
    /// tags LSP occurrences so a local hover can find the owning body's types.
    current_owner: Option<DefId>,

    body: Body,
    result: ResolveResult,
}

#[derive(Clone)]
enum TypeResEntry {
    /// A real (dep/local) type constructor.
    Res(TypeRes),
    /// A foreign (Go FFI) type qualifier member.
    Foreign(String),
}

impl<'a> Resolver<'a> {
    fn new(db: &'a dyn SkyDb, module: ModuleId) -> Self {
        Resolver {
            db,
            module,
            file: FileId(module.index()),
            vars: IndexMap::new(),
            ctors: IndexMap::new(),
            types: IndexMap::new(),
            qual_vars: HashMap::new(),
            qual_ctors: HashMap::new(),
            qual_types: HashMap::new(),
            import_aliases: HashMap::new(),
            kernel_open: Vec::new(),
            foreign_open: None,
            var_origins: HashMap::new(),
            ctor_origins: HashMap::new(),
            type_origins: HashMap::new(),
            ambiguous_vars: HashMap::new(),
            ambiguous_ctors: HashMap::new(),
            ambiguous_types: HashMap::new(),
            cur_import: None,
            reported_ambiguous: HashSet::new(),
            scopes: Vec::new(),
            next_local: 0,
            reuse_binders: false,
            quiet: 0,
            current_owner: None,
            body: Body::default(),
            result: ResolveResult::default(),
        }
    }

    // ---- LSP occurrence recording (doc 10) — additive, no build/check effect -

    #[inline]
    fn span_of(&self, range: syntax::TextRange) -> Span {
        Span::new(self.file, u32::from(range.start()), u32::from(range.end()))
    }

    fn owner(&self) -> DefId {
        self.current_owner.unwrap_or(DefId(u32::MAX))
    }

    fn record_ref(&mut self, range: syntax::TextRange, res: Res) {
        if self.quiet > 0 {
            return;
        }
        let span = self.span_of(range);
        let owner = self.owner();
        self.result.ref_occs.push(RefOcc { span, res, owner });
    }

    fn record_binder(&mut self, range: syntax::TextRange, local: LocalId) {
        if self.quiet > 0 {
            return;
        }
        let span = self.span_of(range);
        let owner = self.owner();
        self.result.binders.push(BinderDef { owner, local, span });
        // A binder is also a hoverable use-site of the local it introduces.
        self.result.ref_occs.push(RefOcc {
            span,
            res: Res::Local(local),
            owner,
        });
    }

    fn record_type_occ(&mut self, range: syntax::TextRange, name: &str) {
        if self.quiet > 0 {
            return;
        }
        if let Some(tr) = self.types.get(name).copied() {
            self.result.type_occs.push(TypeOcc {
                span: self.span_of(range),
                con: tr.con,
                name: Name::new(name),
            });
        }
    }

    fn def(&self, module: ModuleId, name: &str, kind: DefKind) -> DefId {
        self.db.intern_def(module, &Name::new(name), kind)
    }

    // ---- unqualified binding + precedence (doc 05 §6b) -------------------

    /// Bind an unqualified VALUE name, recording which layer it entered through.
    ///
    /// The `vars.insert` is kept verbatim so that, for every name bound exactly
    /// once, this is byte-for-byte the old behaviour. `settle_precedence` only
    /// revisits names with more than one origin.
    fn bind_var(&mut self, name: String, res: Res, layer: BindLayer) {
        let origin = self.origin_of(layer, res_key(&res), res.clone());
        self.var_origins.entry(name.clone()).or_default().push(origin);
        self.vars.insert(name, res);
    }

    /// Stamp the current provenance (import in flight, or this module) onto a
    /// binding at `layer`.
    fn origin_of(&self, layer: BindLayer, key: String, res: Res) -> Origin {
        match &self.cur_import {
            Some(c) => Origin {
                layer,
                module: c.module.clone(),
                qualifier: c.qualifier.clone(),
                key,
                res,
            },
            None => Origin {
                layer,
                module: self.self_module_name(),
                qualifier: None,
                key,
                res,
            },
        }
    }

    /// Bind an unqualified VALUE name at the layer of the import being processed.
    fn bind_var_imported(&mut self, name: String, res: Res) {
        let layer = self
            .cur_import
            .as_ref()
            .map(|c| c.layer)
            .unwrap_or(BindLayer::Open);
        self.bind_var(name, res, layer);
    }

    /// Bind an unqualified CONSTRUCTOR name. Same contract as [`bind_var`].
    fn bind_ctor(&mut self, name: String, ctor: CtorRef, layer: BindLayer) {
        let origin = self.origin_of(
            layer,
            format!("ctor:{}", ctor.def.0),
            Res::Ctor(ctor.clone()),
        );
        self.ctor_origins
            .entry(name.clone())
            .or_default()
            .push(origin);
        self.ctors.insert(name, ctor);
    }

    fn bind_ctor_imported(&mut self, name: String, ctor: CtorRef) {
        let layer = self
            .cur_import
            .as_ref()
            .map(|c| c.layer)
            .unwrap_or(BindLayer::Open);
        self.bind_ctor(name, ctor, layer);
    }

    /// Bind an unqualified TYPE name, recording which layer it entered through
    /// and how trustworthy its identity is.
    ///
    /// The `types.insert` is kept verbatim, so for every name bound exactly once
    /// this is byte-for-byte the old behaviour; `settle_precedence` only revisits
    /// names with more than one origin.
    fn bind_type(&mut self, name: String, tr: TypeRes, key: TypeKey, layer: BindLayer) {
        let (module, qualifier) = match &self.cur_import {
            Some(c) => (c.module.clone(), c.qualifier.clone()),
            None => (self.self_module_name(), None),
        };
        self.type_origins
            .entry(name.clone())
            .or_default()
            .push(TypeOrigin {
                layer,
                module,
                qualifier,
                key,
                res: tr,
            });
        self.types.insert(name, tr);
    }

    /// Bind an unqualified TYPE name at the layer of the import in flight.
    fn bind_type_imported(&mut self, name: String, tr: TypeRes, key: TypeKey) {
        let layer = self
            .cur_import
            .as_ref()
            .map(|c| c.layer)
            .unwrap_or(BindLayer::Open);
        self.bind_type(name, tr, key, layer);
    }

    /// The ONE `DefId` for a kernel-implicit type name.
    ///
    /// `Decoder` / `Value` / `Attribute` / … have no `type` declaration in any
    /// `.sky` source (`KERNEL_IMPLICIT_TYPES`), so `import M exposing (Decoder)`
    /// on a kernel pseudo-module had nothing authoritative to point at and minted
    /// `self.def(self.module, name)` — a FRESH `DefId` per importing module, and
    /// a different one per kernel module within a single import list. Two
    /// references to one conceptual `Decoder` therefore carried two identities,
    /// which is the specific fact that made a `DefId`-keyed ambiguity rule unsafe.
    ///
    /// Interning into `BUILTIN_MOD` instead gives the name ONE program-wide
    /// identity, the same way `BUILTIN_TYPES` already works — a kernel-implicit
    /// type is a property of the language, not of whoever imported it. The
    /// sentinel is the existing one, so every downstream guard that already knows
    /// `module_name(ModuleId(u32::MAX))` is not a real module keeps covering it
    /// (`ty::sig::rewrite_alias_refs` gates on `TypeAlias`; `lower`'s
    /// `pinned_union_go` gates on `index() == u32::MAX`).
    fn kernel_implicit_type_def(&self, name: &str) -> DefId {
        self.def(BUILTIN_MOD, name, DefKind::TypeCon)
    }

    /// Chase a type name that a module `exposing (…)`s but does not DECLARE.
    ///
    /// `exports::compute_exports` is computed purely from a module's own parse
    /// and never recurses (a deliberate salsa-shape choice), so a module that
    /// re-exposes an imported type publishes nothing for it and
    /// `exports.type_(name)` misses. The old code minted
    /// `self.def(exports.module, name)` — an identity belonging to the
    /// RE-EXPORTER, so the same type reached directly and through the re-export
    /// carried two identities.
    ///
    /// This reads the re-exporter's own import list (parse only — no `resolve`
    /// recursion, so no query cycle between mutually-importing modules) to find
    /// which module it got the name from, and returns THAT module's real export.
    /// Bounded by `visited` so a re-export cycle terminates.
    /// Breadth-first, and it considers EVERY import that could have supplied the
    /// name rather than the first one. A depth-first walk down the first
    /// candidate would miss the declaration whenever an unrelated
    /// `exposing (..)` import happened to be written above the real source —
    /// a miss is only a false negative here, never a false rejection, but it is
    /// avoidable and the search space is tiny.
    fn chase_reexported_type(&self, from: ModuleId, name: &str) -> Option<(DefId, u16)> {
        let mut visited: HashSet<u32> = HashSet::new();
        let mut frontier: Vec<ModuleId> = vec![from];
        visited.insert(from.index());
        // A re-export chain deeper than this is not a real program shape; the
        // bound is belt-and-braces on top of `visited`.
        for _ in 0..8 {
            let mut next: Vec<ModuleId> = Vec::new();
            for m in frontier.drain(..) {
                let tree = self.db.module_parse(m).tree();
                for imp in tree.imports() {
                    let Some(path) = imp.name().map(|n| n.text()) else {
                        continue;
                    };
                    let Some(clause) = imp.exposing().map(|e| cst::read_exposing(e.syntax()))
                    else {
                        continue;
                    };
                    let names_it = clause.all
                        || clause.items.iter().any(
                            |it| matches!(it, cst::ExposedItem::Type { name: n, .. } if n == name),
                        );
                    if !names_it {
                        continue;
                    }
                    // A kernel or Go-FFI origin is not a Sky declaration site;
                    // the caller falls back to its own handling for those.
                    let ImportSource::Dep(d) = self.db.classify_import(&path) else {
                        continue;
                    };
                    if let Some(found) = self.db.module_exports(d).type_(name) {
                        return Some(found);
                    }
                    if visited.insert(d.index()) {
                        next.push(d);
                    }
                }
            }
            if next.is_empty() {
                return None;
            }
            frontier = next;
        }
        None
    }

    fn self_module_name(&self) -> String {
        self.db.module_name(self.module).to_string()
    }

    /// Resolve every multiply-bound unqualified name by LAYER rather than by
    /// import order, and record the ones that stay genuinely ambiguous.
    ///
    /// Called once, after builtins → imports → local decls have all bound. This
    /// is what makes resolution order-independent: before it, `vars` holds
    /// whatever the last `.insert()` put there; after it, every multiply-bound
    /// name holds its highest-layer binding regardless of where the imports sat
    /// in the file. A name whose top layer holds two different definitions is
    /// left as-is in `vars` (resolution must stay total) and recorded in
    /// `ambiguous_vars`, so the error fires only if a use site reads it.
    fn settle_precedence(&mut self) {
        let mut wins: Vec<(String, Res)> = Vec::new();
        let mut ambiguous: Vec<(String, Vec<Origin>)> = Vec::new();
        for (name, origins) in &self.var_origins {
            if let Some(outcome) = settle_one(origins) {
                match outcome {
                    Settled::Winner(res) => wins.push((name.clone(), res)),
                    Settled::Ambiguous(cands) => ambiguous.push((name.clone(), cands)),
                }
            }
        }
        for (name, res) in wins {
            // Measured before landing (`SKY_AUDIT_PRECEDENCE` instrumentation,
            // since removed): across all 56 examples and 6 real apps —
            // skydeploy's control-plane, sky-lang.org, darraghstudio, sendcrafts,
            // rfcflow, sky-urlshortener — the layer winner picked here was
            // IDENTICAL to the old last-import-wins value in every case. The
            // lattice therefore changes no working program's meaning; it only
            // makes the choice independent of import order and rejects the ties.
            //
            // `IndexMap::insert` on an existing key replaces in place, so the
            // iteration order `snapshot_scope_names` publishes is unchanged.
            self.vars.insert(name, res);
        }
        self.ambiguous_vars = ambiguous.into_iter().collect();

        let mut cwins: Vec<(String, CtorRef)> = Vec::new();
        let mut cambig: Vec<(String, Vec<Origin>)> = Vec::new();
        for (name, origins) in &self.ctor_origins {
            if let Some(outcome) = settle_one(origins) {
                match outcome {
                    Settled::Winner(Res::Ctor(c)) => cwins.push((name.clone(), c)),
                    Settled::Winner(_) => {}
                    Settled::Ambiguous(cands) => cambig.push((name.clone(), cands)),
                }
            }
        }
        for (name, c) in cwins {
            self.ctors.insert(name, c);
        }
        self.ambiguous_ctors = cambig.into_iter().collect();

        // Types. Same lattice, same use-site reporting; the difference is that a
        // fabricated identity abstains instead of voting (see `TypeKey`).
        let mut twins: Vec<(String, TypeRes)> = Vec::new();
        let mut tambig: Vec<(String, Vec<TypeOrigin>)> = Vec::new();
        for (name, origins) in &self.type_origins {
            match settle_one_type(origins) {
                Some(SettledType::Winner(tr)) => twins.push((name.clone(), tr)),
                Some(SettledType::Ambiguous(cands)) => tambig.push((name.clone(), cands)),
                None => {}
            }
        }
        for (name, tr) in twins {
            self.types.insert(name, tr);
        }
        self.ambiguous_types = tambig.into_iter().collect();
    }

    /// Report an ambiguous unqualified reference at the site that reads it.
    ///
    /// At the USE site, not the import — importing two modules that both expose
    /// a name you never mention is legal and extremely common (every Sky.Live
    /// page does `import Std.Html exposing (..)` alongside `import
    /// Std.Html.Attributes exposing (..)`, and those two overlap). Elm draws the
    /// line in the same place. Reporting at the import would reject programs
    /// whose meaning is not order-dependent at all.
    fn report_ambiguous(&mut self, name: &str, cands: &[Origin], span: Option<Span>, what: &str) {
        let sources: Vec<(String, Option<String>)> = cands
            .iter()
            .map(|o| (o.module.clone(), o.qualifier.clone()))
            .collect();
        self.report_ambiguous_sources(name, &sources, span, what);
    }

    /// Same diagnostic for the TYPE namespace. A type reference is ambiguous for
    /// exactly the reason a value one is — `hir`'s `types` map and `lower`'s
    /// `nominal` map are both last-writer-wins on the bare name, so which
    /// declaration an annotation means is a function of import ORDER, and the two
    /// orders select two different Go types.
    fn report_ambiguous_type(&mut self, name: &str, cands: &[TypeOrigin], span: Option<Span>) {
        let sources: Vec<(String, Option<String>)> = cands
            .iter()
            .map(|o| (o.module.clone(), o.qualifier.clone()))
            .collect();
        self.report_ambiguous_sources(name, &sources, span, "type");
    }

    fn report_ambiguous_sources(
        &mut self,
        name: &str,
        cands: &[(String, Option<String>)],
        span: Option<Span>,
        what: &str,
    ) {
        if self.quiet > 0 {
            return;
        }
        if let Some(sp) = span {
            if !self.reported_ambiguous.insert((name.to_string(), sp.range.0)) {
                return;
            }
        } else if !self
            .reported_ambiguous
            .insert((name.to_string(), u32::MAX))
        {
            return;
        }
        let mods: Vec<String> = cands.iter().map(|(m, _)| format!("`{m}`")).collect();
        let quals: Vec<String> = cands
            .iter()
            .filter_map(|(_, q)| q.as_ref().map(|q| format!("`{q}.{name}`")))
            .collect();
        let fix = if quals.len() == cands.len() && !quals.is_empty() {
            format!("Qualify the reference — write {} — or narrow one import's `exposing (…)` list so only one of them binds `{name}`.", join_or(&quals))
        } else {
            format!(
                "Qualify the reference (give the imports aliases with `import M as X` if they do \
                 not already have one), or narrow one import's `exposing (…)` list so only one of \
                 them binds `{name}`."
            )
        };
        let mut diag = Diagnostic::error(
            "E1012",
            format!(
                "Ambiguous {what} `{name}` — it is brought into scope by {}, and this reference \
                 does not say which one it means. Sky rejects this instead of picking one, \
                 because the winner would otherwise depend on the ORDER of your import lines: \
                 swapping two imports would silently change what this program computes. {fix}",
                join_and(&mods)
            ),
        );
        if let Some(sp) = span {
            diag = diag.with_label(sp, format!("bound by {} imports", cands.len()));
        }
        self.result.diagnostics.push(diag);
    }

    // ---- environment building (doc 05 §5, §10) --------------------------

    fn build_env(&mut self) {
        // 1. unconditional builtins
        for (name, m, f) in BUILTIN_VARS {
            self.bind_var(
                (*name).to_string(),
                Res::Kernel {
                    module: Name::new(m),
                    func: Name::new(f),
                },
                BindLayer::Ambient,
            );
        }
        for (name, arity) in BUILTIN_TYPES {
            let con = self.def(BUILTIN_MOD, name, DefKind::TypeCon);
            // AMBIENT, for the same reason the Prelude's values are: the user did
            // not choose `List` / `Result` / `Error`, so their presence is not an
            // authorial claim on the name and must never make an explicit import
            // of a same-named type ambiguous.
            self.bind_type(
                (*name).to_string(),
                TypeRes { con, arity: *arity },
                TypeKey::Id(format!("def:{}", con.0)),
                BindLayer::Ambient,
            );
        }
        for (cn, ty, index, arity) in BUILTIN_CTORS {
            let type_ = self.def(BUILTIN_MOD, ty, DefKind::TypeCon);
            let d = self.def(BUILTIN_MOD, cn, DefKind::Ctor);
            self.bind_ctor(
                (*cn).to_string(),
                CtorRef {
                    def: d,
                    type_,
                    index: *index,
                    arity: *arity,
                },
                BindLayer::Ambient,
            );
        }
        for (qual, funcs) in PRELUDE_QUALIFIERS {
            let m = self.qual_vars.entry((*qual).to_string()).or_default();
            for f in *funcs {
                m.insert(
                    (*f).to_string(),
                    Res::Kernel {
                        module: Name::new(qual),
                        func: Name::new(f),
                    },
                );
            }
        }

        // 2. imports
        let tree = self.db.module_parse(self.module).tree();
        let imports: Vec<ast::Import> = tree.imports().collect();
        let claims = explicit_alias_claims(&imports);
        for imp in &imports {
            self.process_import(imp, &claims);
        }

        // 3. local declarations (after imports → local shadows import, C7)
        self.register_locals(&tree);
        // 4. settle every multiply-bound unqualified name by LAYER instead of by
        //    import order, and record what stays ambiguous (doc 05 §6b). Must run
        //    after ALL three binding phases and before `snapshot_scope_names`.
        self.settle_precedence();
        // LSP: publish import qualifiers so `M.` completion can enumerate the
        // target module's exports.
        self.result.qualifiers = self.import_aliases.clone();
        // #164: publish this module's type-name resolution so a downstream crate
        // can key type aliases by their DEFINING module (`def_loc`) rather than
        // by bare name. Bare names (own decls + `exposing`-imports) come from
        // `types`; qualified refs (import aliases + auto-qualifiers) from
        // `qual_types`, flattened to `"Qual.Name"` keys. Foreign qualifier
        // members are dropped — they name Go FFI types, not Sky constructors.
        {
            let mut trefs: HashMap<String, TypeRes> =
                HashMap::with_capacity(self.types.len() + self.qual_types.len());
            for (n, tr) in &self.types {
                trefs.insert(n.clone(), *tr);
            }
            for (q, inner) in &self.qual_types {
                for (n, entry) in inner {
                    if let TypeResEntry::Res(tr) = entry {
                        trefs.insert(format!("{q}.{n}"), *tr);
                    }
                }
            }
            self.result.type_refs = trefs;
        }
        // LSP: publish the in-scope unqualified names so bare-identifier
        // completion can offer Prelude + `exposing`-imported names, not just
        // locals + this module's top defs + qualifiers.
        self.snapshot_scope_names();
    }

    /// Snapshot the module-level unqualified namespaces (`vars` / `ctors` /
    /// `types`) into `result.scope_names` for unqualified completion. Called
    /// once, after `build_env` fully populates them (builtins → imports →
    /// local decls); lexical locals never enter these maps, so this is a stable
    /// module-scope set. Read-only over the maps — no effect on resolution.
    fn snapshot_scope_names(&mut self) {
        let mut out: Vec<(String, ScopeNameKind)> = Vec::new();
        for k in self.vars.keys() {
            out.push((k.clone(), ScopeNameKind::Value));
        }
        for k in self.ctors.keys() {
            out.push((k.clone(), ScopeNameKind::Ctor));
        }
        for k in self.types.keys() {
            out.push((k.clone(), ScopeNameKind::Type));
        }
        self.result.scope_names = out;
    }

    fn process_import(&mut self, imp: &ast::Import, claims: &HashMap<String, String>) {
        let Some(path) = imp.name().map(|n| n.text()) else {
            return;
        };
        let alias = imp.alias().map(|t| t.text().to_string());
        let source = self.db.classify_import(&path);
        let qual = effective_qualifier(claims, &path, &alias);

        // ---- unknown Sky module (the `ImportSource::Foreign` fallback hole) ----
        //
        // `classify_import` resolves parsed dep > kernel pseudo > **Foreign**, and
        // that last arm is a total fallback: ANY unrecognised import path becomes a
        // Go-FFI package reference, which resolves leniently to `nil`. For a real
        // Go package that leniency is the documented class-(b) contract. For a path
        // in a RESERVED Sky namespace it is a soundness hole, because no Go-FFI
        // package can ever live under `Std.` / `Sky.`.
        //
        // Measured, not reasoned: on this branch `import Std.NoSuchModule as Nope`
        // followed by `Nope.answer` printed "Names resolved", "Types OK", emitted
        // Go, passed `go build`, and panicked at run time with
        // `rt.AsInt: expected numeric value, got <nil>`. `sky check ≡ sky build`
        // and "no runtime panic from well-typed Sky" were both false for it.
        //
        // The *call* path already rejected this (lower.rs's "unknown Sky module"
        // error), which is exactly why the hole was invisible: the one shape anyone
        // had tried was the one that was covered. Three shapes reached the same
        // `Foreign` fallback with no check at all — a bare value reference, a type
        // reference (`Nope.Thing`), and an import that is never used. Anchoring the
        // check at the IMPORT closes all four with one rule, before any of them can
        // pick a different downstream path, and reports the import line the user
        // must actually fix rather than a use site far below it.
        if let ImportSource::Foreign(pkg) = &source {
            if crate::kernel::is_reserved_sky_namespace(pkg) && self.quiet == 0 {
                let span = imp
                    .name()
                    .map(|n| self.span_of(n.syntax().text_range()));
                let mut diag = Diagnostic::error(
                    "E1001",
                    format!(
                        "unknown Sky module `{pkg}` — no such module is in this \
                         compilation. Check the spelling of the import: Sky stdlib \
                         modules live under `Std.*` and `Sky.Core.*` / `Sky.Http.*` \
                         (e.g. `Sky.Core.List`, `Std.Db`). This is not a Go-FFI \
                         package; `sky install` cannot fetch it."
                    ),
                );
                if let Some(sp) = span {
                    diag = diag.with_label(sp, "no such module");
                }
                self.result.diagnostics.push(diag);
                self.result.class_a.push(ClassA {
                    qualifier: None,
                    name: pkg.clone(),
                    kind: RefKind::Value,
                    reason: "unknown Sky module".to_string(),
                });
            }
        }

        // ---- qualifier binding ----
        if let Some(q) = &qual {
            self.import_aliases.insert(q.clone(), source.clone());
            match &source {
                ImportSource::Dep(dep) => {
                    let exports = self.db.module_exports(*dep);
                    self.bind_qual_from_exports(q, &exports);
                }
                ImportSource::Kernel(_) | ImportSource::Foreign(_) => {}
            }
        }

        // ---- exposing binding (still happens even if the qualifier was
        // suppressed by explicit-alias-wins — C1) ----
        let clause = imp.exposing().map(|e| cst::read_exposing(e.syntax()));
        // Anchor a "not exposed" error at the `exposing (...)` clause (falling
        // back to the module name), so [E1011] renders a line + caret + excerpt.
        let exposing_span = imp
            .exposing()
            .map(|e| self.span_of(e.syntax().text_range()))
            .or_else(|| imp.name().map(|n| self.span_of(n.syntax().text_range())));

        // Provenance for everything this import is about to bind (doc 05 §6b).
        // The layer is a property of the IMPORT, not of the individual name:
        // `Sky.Core.Prelude` is ambient however it is written, `exposing (..)` is
        // a bulk claim, and a named `exposing (…)` list is a specific one.
        self.cur_import = Some(ImportCtx {
            module: path.clone(),
            qualifier: qual.clone(),
            layer: if AMBIENT_IMPORTS.contains(&path.as_str()) {
                BindLayer::Ambient
            } else if clause.as_ref().map(|c| c.all).unwrap_or(false) {
                BindLayer::Open
            } else {
                BindLayer::Explicit
            },
        });

        match (&source, clause) {
            (ImportSource::Dep(dep), Some(c)) => {
                let exports = self.db.module_exports(*dep);
                self.bind_exposing_dep(&c, &exports, &path, exposing_span);
            }
            (ImportSource::Kernel(pseudo), Some(c)) => {
                if c.all {
                    // `import M exposing (..)` on a kernel module binds exactly the
                    // module's known kernel functions (oracle: kernelVarsFor +
                    // addExposed, Module.hs:700-703) — NOT "anything". This closes
                    // the soundness hole where a bare undefined name (e.g. under the
                    // ubiquitous `import Sky.Core.Prelude exposing (..)` → `Basics`)
                    // silently resolved to a bogus `rt.<Mod>_<name>` kernel ref and
                    // only failed at `go build`. An unknown bare name now falls
                    // through `resolve_var` to `Res::Error` + `[E1001]`.
                    match kernel_functions(pseudo) {
                        Some(funcs) => {
                            let funcs: Vec<&'static str> = funcs.to_vec();
                            for f in funcs {
                                self.bind_var_imported(
                                    f.to_string(),
                                    Res::Kernel {
                                        module: Name::new(pseudo),
                                        func: Name::new(f),
                                    },
                                );
                            }
                        }
                        // No static enumeration for this pseudo — keep the lenient
                        // fallback (defensive; no such pseudo is imported
                        // `exposing (..)` in the corpus, so this never fires there).
                        None => self.kernel_open.push(pseudo.clone()),
                    }
                } else {
                    self.bind_exposing_kernel(pseudo, &c);
                }
            }
            (ImportSource::Foreign(pkg), Some(c)) => {
                if c.all {
                    self.foreign_open = Some(pkg.clone());
                } else {
                    self.bind_exposing_foreign(pkg, &c);
                }
            }
            (_, None) => {}
        }
        self.cur_import = None;
    }

    fn bind_qual_from_exports(&mut self, q: &str, exports: &ModuleExports) {
        let v = self.qual_vars.entry(q.to_string()).or_default();
        for (name, def) in &exports.values {
            v.insert(name.as_str().to_string(), Res::Def(*def));
        }
        let c = self.qual_ctors.entry(q.to_string()).or_default();
        for u in &exports.unions {
            for ct in &u.ctors {
                c.insert(ct.name.as_str().to_string(), to_ctor_ref(ct));
            }
        }
        let t = self.qual_types.entry(q.to_string()).or_default();
        for u in &exports.unions {
            t.insert(
                u.name.as_str().to_string(),
                TypeResEntry::Res(TypeRes {
                    con: u.def,
                    arity: u.arity,
                }),
            );
        }
        for a in &exports.aliases {
            t.insert(
                a.name.as_str().to_string(),
                TypeResEntry::Res(TypeRes {
                    con: a.def,
                    arity: a.arity,
                }),
            );
        }
    }

    fn bind_exposing_dep(
        &mut self,
        c: &cst::ExposingClause,
        exports: &ModuleExports,
        module_name: &str,
        span: Option<Span>,
    ) {
        if c.all {
            let values: Vec<(String, DefId)> = exports
                .values
                .iter()
                .map(|(n, d)| (n.as_str().to_string(), *d))
                .collect();
            for (name, def) in values {
                self.bind_var_imported(name, Res::Def(def));
            }
            for u in &exports.unions.clone() {
                self.bind_type_imported(
                    u.name.as_str().to_string(),
                    TypeRes {
                        con: u.def,
                        arity: u.arity,
                    },
                    TypeKey::Id(format!("def:{}", u.def.0)),
                );
                for ct in &u.ctors {
                    self.bind_ctor_imported(ct.name.as_str().to_string(), to_ctor_ref(ct));
                }
            }
            for a in &exports.aliases.clone() {
                self.bind_type_imported(
                    a.name.as_str().to_string(),
                    TypeRes {
                        con: a.def,
                        arity: a.arity,
                    },
                    TypeKey::Id(format!("def:{}", a.def.0)),
                );
            }
            return;
        }
        for it in &c.items {
            match it {
                cst::ExposedItem::Value(v) => {
                    // Elm semantics: a value the source module does not expose
                    // cannot be imported. `module_exports` already publishes
                    // every explicitly-exposed name (including re-exports), so a
                    // miss here means `v` is private (or undeclared) in
                    // `module_name`. Report [E1011]; still bind a recovery def so
                    // the rest of resolution doesn't cascade into a flood of
                    // "undefined name" errors for `v`'s use sites.
                    let res = match exports.value(v) {
                        Some(def) => Res::Def(def),
                        None => {
                            let mut diag = Diagnostic::error(
                                "E1011",
                                format!("module `{module_name}` does not expose `{v}`"),
                            );
                            if let Some(sp) = span {
                                diag = diag.with_label(sp, "not exposed by the module");
                            }
                            self.result.diagnostics.push(diag);
                            Res::Def(self.def(exports.module, v, DefKind::Value))
                        }
                    };
                    self.bind_var_imported(v.clone(), res);
                }
                cst::ExposedItem::Type { name, ctors } => {
                    // Identity, in descending order of authority:
                    //   1. the module really exports it            → its DefId
                    //   2. it re-exports it from somewhere         → chase, real DefId
                    //   3. it is a kernel-implicit language type   → the ONE builtin DefId
                    //   4. nothing authoritative                   → fabricate, and say so
                    // Only (4) is lenient now. It keeps the recovery binding the
                    // old code produced — resolution must stay total — but marks
                    // the identity `Opaque` so it can never be compared against
                    // another binding and reported ambiguous.
                    let (tr, key) = if let Some((def, arity)) = exports.type_(name) {
                        (
                            TypeRes { con: def, arity },
                            TypeKey::Id(format!("def:{}", def.0)),
                        )
                    } else if let Some((def, arity)) = self.chase_reexported_type(exports.module, name)
                    {
                        (
                            TypeRes { con: def, arity },
                            TypeKey::Id(format!("def:{}", def.0)),
                        )
                    } else if KERNEL_IMPLICIT_TYPES.contains(&name.as_str()) {
                        let con = self.kernel_implicit_type_def(name);
                        (
                            TypeRes { con, arity: 0 },
                            TypeKey::Id(format!("kernel-implicit:{name}")),
                        )
                    } else {
                        let con = self.def(exports.module, name, DefKind::TypeCon);
                        (TypeRes { con, arity: 0 }, TypeKey::Opaque)
                    };
                    self.bind_type_imported(name.clone(), tr, key);
                    // record-alias constructor value
                    if let Some(def) = exports.value(name) {
                        self.bind_var_imported(name.clone(), Res::Def(def));
                    }
                    match ctors {
                        cst::CtorExposure::None => {}
                        // Collected before binding because `bind_ctor_imported`
                        // takes `&mut self` while `exports` is borrowed here.
                        cst::CtorExposure::All | cst::CtorExposure::Some(_) => {
                            let wanted = match ctors {
                                cst::CtorExposure::Some(list) => Some(list),
                                _ => None,
                            };
                            let picked: Vec<(String, CtorRef)> = exports
                                .unions
                                .iter()
                                .find(|u| u.name.as_str() == name)
                                .map(|u| {
                                    u.ctors
                                        .iter()
                                        .filter(|ct| {
                                            wanted.is_none_or(|l| {
                                                l.iter().any(|x| x == ct.name.as_str())
                                            })
                                        })
                                        .map(|ct| (ct.name.as_str().to_string(), to_ctor_ref(ct)))
                                        .collect()
                                })
                                .unwrap_or_default();
                            for (n, c) in picked {
                                self.bind_ctor_imported(n, c);
                            }
                        }
                    }
                }
                cst::ExposedItem::Operator => {}
            }
        }
    }

    fn bind_exposing_kernel(&mut self, pseudo: &str, c: &cst::ExposingClause) {
        for it in &c.items {
            match it {
                cst::ExposedItem::Value(v) => {
                    self.bind_var_imported(
                        v.clone(),
                        Res::Kernel {
                            module: Name::new(pseudo),
                            func: Name::new(v),
                        },
                    );
                }
                cst::ExposedItem::Type { name, .. } => {
                    // A type exposed by a KERNEL pseudo-module has no `.sky`
                    // declaration to point at. It used to mint a fresh DefId in
                    // the IMPORTING module, so `Decoder` meant a different
                    // identity in every module that imported it — and two kernel
                    // modules in one import list produced two `Decoder`s. One
                    // program-wide identity instead (see
                    // `kernel_implicit_type_def`).
                    let con = self.kernel_implicit_type_def(name);
                    self.bind_type_imported(
                        name.clone(),
                        TypeRes { con, arity: 0 },
                        TypeKey::Id(format!("kernel-implicit:{name}")),
                    );
                }
                cst::ExposedItem::Operator => {}
            }
        }
    }

    fn bind_exposing_foreign(&mut self, pkg: &str, c: &cst::ExposingClause) {
        for it in &c.items {
            match it {
                cst::ExposedItem::Value(v) => {
                    self.bind_var_imported(
                        v.clone(),
                        Res::Foreign {
                            package: Name::new(pkg),
                            name: Name::new(v),
                        },
                    );
                }
                cst::ExposedItem::Type { name, .. } => {
                    self.qual_types
                        .entry(name.clone())
                        .or_default()
                        .insert(name.clone(), TypeResEntry::Foreign(pkg.to_string()));
                    // also make the bare type resolvable leniently as foreign.
                    // A Go FFI type's identity is its (package, name) pair, which
                    // is not a Sky `DefId` and cannot be compared against one, so
                    // the binding is `Opaque`: a bare name that might be a Go type
                    // never joins an ambiguity verdict. Documented false negative
                    // — the alternative is guessing that a Go `Client` and a Sky
                    // `Client` are different, which is how #164 broke a real app.
                    let con = self.def(self.module, name, DefKind::TypeCon);
                    self.bind_type_imported(
                        name.clone(),
                        TypeRes { con, arity: 0 },
                        TypeKey::Opaque,
                    );
                }
                cst::ExposedItem::Operator => {}
            }
        }
    }

    fn register_locals(&mut self, tree: &ast::SourceFile) {
        // Top-level value names already registered in THIS module. A second
        // `foo = …` for the same `foo` is a redefinition the oracle rejects (at
        // `go build`: "x redeclared in this block"); Sky has no multi-clause
        // function definitions, so a repeat is always an error. Reject at
        // check-time (`sky check ≡ sky build`). A `foo : T` annotation is a
        // separate `Decl::TypeAnno` (never a `Decl::Value`), so it never
        // trips this.
        let mut seen_values: HashSet<String> = HashSet::new();
        for decl in tree.decls() {
            match decl {
                ast::Decl::Value(v) => {
                    if let Some(n) = v.name() {
                        if n.kind() == SyntaxKind::LowerIdent {
                            let nm = n.text().to_string();
                            if !seen_values.insert(nm.clone()) {
                                self.result.diagnostics.push(
                                    Diagnostic::error(
                                        "E1002",
                                        format!(
                                            "`{nm}` is defined more than once at the top level"
                                        ),
                                    )
                                    .with_label(self.span_of(n.text_range()), "redefined here"),
                                );
                            }
                            let d = self.def(self.module, n.text(), DefKind::Value);
                            self.bind_var(n.text().to_string(), Res::Def(d), BindLayer::Local);
                            // LSP: a value's goto-def target is its name span. A
                            // `foo : T` annotation + `foo = …` value share one
                            // DefId; keep the first span seen (the annotation).
                            let span = self.span_of(n.text_range());
                            if !self.result.def_spans.iter().any(|(id, _)| *id == d) {
                                self.result.def_spans.push((d, span));
                            }
                        }
                    }
                }
                ast::Decl::Union(u) => {
                    let Some(tn) = u.name().map(|t| t.text().to_string()) else {
                        continue;
                    };
                    if PRELUDE_RESERVED.contains(&tn.as_str()) {
                        self.result.diagnostics.push(Diagnostic::error(
                            "E1004",
                            format!(
                                "Type `{tn}` shadows a Prelude-exposed name — pick a different name"
                            ),
                        ));
                    }
                    let arity = cst::decl_type_vars(u.syntax()).len() as u16;
                    let type_ = self.def(self.module, &tn, DefKind::TypeCon);
                    self.bind_type(
                        tn.clone(),
                        TypeRes { con: type_, arity },
                        TypeKey::Id(format!("def:{}", type_.0)),
                        BindLayer::Local,
                    );
                    if let Some(t) = u.name() {
                        self.result
                            .def_spans
                            .push((type_, self.span_of(t.text_range())));
                    }
                    for (i, var) in u.variants().iter().enumerate() {
                        let Some(cn) = var.name().map(|t| t.text().to_string()) else {
                            continue;
                        };
                        if PRELUDE_RESERVED.contains(&cn.as_str()) {
                            self.result.diagnostics.push(Diagnostic::error(
                                "E1004",
                                format!(
                                    "Constructor `{cn}` shadows a Prelude-exposed name — pick a different name"
                                ),
                            ));
                        }
                        let cargs = cst::child_types(var.syntax()).len() as u16;
                        let d = self.def(self.module, &cn, DefKind::Ctor);
                        if let Some(t) = var.name() {
                            self.result
                                .def_spans
                                .push((d, self.span_of(t.text_range())));
                        }
                        self.bind_ctor(
                            cn,
                            CtorRef {
                                def: d,
                                type_,
                                index: i as u16,
                                arity: cargs,
                            },
                            BindLayer::Local,
                        );
                    }
                }
                ast::Decl::Alias(a) => {
                    let Some(an) = a.name().map(|t| t.text().to_string()) else {
                        continue;
                    };
                    if PRELUDE_RESERVED.contains(&an.as_str()) {
                        self.result.diagnostics.push(Diagnostic::error(
                            "E1004",
                            format!(
                                "Type `{an}` shadows a Prelude-exposed name — pick a different name"
                            ),
                        ));
                    }
                    let arity = cst::decl_type_vars(a.syntax()).len() as u16;
                    let is_record = a
                        .ty()
                        .map(|t| matches!(t, ast::Type::Record(_)))
                        .unwrap_or(false);
                    let con = self.def(self.module, &an, DefKind::TypeAlias);
                    self.bind_type(
                        an.clone(),
                        TypeRes { con, arity },
                        TypeKey::Id(format!("def:{}", con.0)),
                        BindLayer::Local,
                    );
                    if let Some(t) = a.name() {
                        self.result
                            .def_spans
                            .push((con, self.span_of(t.text_range())));
                    }
                    // LSP: record each record-alias field's declaration span so a
                    // field access can goto-def into the alias body.
                    if is_record {
                        if let Some(ast::Type::Record(r)) = a.ty() {
                            let field_names: Vec<Name> = r
                                .syntax()
                                .children()
                                .filter(|c| c.kind() == SyntaxKind::TypeRecordField)
                                .filter_map(|f| cst::first_lower(&f).map(|n| Name::new(&n)))
                                .collect();
                            for f in r
                                .syntax()
                                .children()
                                .filter(|c| c.kind() == SyntaxKind::TypeRecordField)
                            {
                                if let Some(fname) = cst::first_lower(&f) {
                                    self.result.field_decls.push(FieldDecl {
                                        field: Name::new(&fname),
                                        siblings: field_names.clone(),
                                        span: self.span_of(f.text_range()),
                                    });
                                }
                            }
                        }
                        let d = self.def(self.module, &an, DefKind::Value);
                        self.bind_var(an, Res::Def(d), BindLayer::Local);
                    }
                }
                ast::Decl::TypeAnno(_) | ast::Decl::Foreign(_) => {}
            }
        }
    }

    // ---- walking (doc 05 §4) --------------------------------------------

    fn walk_module(&mut self) {
        let tree = self.db.module_parse(self.module).tree();
        for decl in tree.decls() {
            match decl {
                ast::Decl::Value(v) => self.walk_value(&v),
                ast::Decl::TypeAnno(a) => {
                    if let Some(t) = a.ty() {
                        self.body = Body::default();
                        let tid = self.resolve_type(&t);
                        self.body.anno = Some(tid);
                        self.finish_body(decl_name(&ast::Decl::TypeAnno(a.clone())));
                    }
                }
                ast::Decl::Union(u) => {
                    self.body = Body::default();
                    for var in u.variants() {
                        for at in cst::child_types(var.syntax()) {
                            let _ = self.resolve_type(&at);
                        }
                    }
                    self.finish_body(u.name().map(|t| t.text().to_string()));
                }
                ast::Decl::Alias(a) => {
                    self.body = Body::default();
                    if let Some(t) = a.ty() {
                        let tid = self.resolve_type(&t);
                        self.body.anno = Some(tid);
                    }
                    self.finish_body(a.name().map(|t| t.text().to_string()));
                }
                ast::Decl::Foreign(_) => {}
            }
        }
    }

    fn walk_value(&mut self, v: &ast::ValueDecl) {
        self.body = Body::default();
        self.scopes.clear();
        self.next_local = 0;
        // LSP: tag occurrences in this body with the value's DefId (same intern
        // key register_locals used) so a local hover finds the body's types.
        self.current_owner = v
            .name()
            .filter(|n| n.kind() == SyntaxKind::LowerIdent)
            .map(|n| self.def(self.module, n.text(), DefKind::Value));
        self.push_scope();
        let mut params = Vec::new();
        if let Some(pl) = v.params() {
            self.check_linear_group(pl.params());
            for p in pl.params() {
                params.push(self.resolve_pattern(&p));
            }
        }
        self.body.params = params;
        if let Some(b) = v.body() {
            let root = self.resolve_expr(&b);
            self.body.root = Some(root);
        }
        self.pop_scope();
        let name = v.name().map(|t| t.text().to_string());
        self.finish_body(name);
    }

    fn finish_body(&mut self, name: Option<String>) {
        let body = std::mem::take(&mut self.body);
        if let Some(n) = name {
            // a top-level value/type def key; kind picked coarsely for the map.
            let d = self.def(self.module, &n, DefKind::Value);
            self.result.top_defs.push(TopDef {
                def: d,
                name: Name::new(&n),
                span: Span::new(self.file, 0, 0),
            });
            self.result.bodies.insert(d, body);
        }
    }

    // ---- scopes ---------------------------------------------------------

    fn push_scope(&mut self) {
        self.scopes.push(HashMap::new());
    }
    fn pop_scope(&mut self) {
        self.scopes.pop();
    }
    fn fresh_local(&mut self) -> LocalId {
        let id = LocalId(self.next_local);
        self.next_local += 1;
        id
    }
    fn bind_local(&mut self, name: &str) -> LocalId {
        if self.reuse_binders {
            if let Some(scope) = self.scopes.last() {
                if let Some(&id) = scope.get(name) {
                    return id;
                }
            }
        }
        let id = self.fresh_local();
        if let Some(scope) = self.scopes.last_mut() {
            scope.insert(name.to_string(), id);
        }
        id
    }
    fn lookup_local(&self, name: &str) -> Option<LocalId> {
        for scope in self.scopes.iter().rev() {
            if let Some(&id) = scope.get(name) {
                return Some(id);
            }
        }
        None
    }

    // ---- expression resolution ------------------------------------------

    /// Thin span-recording wrapper: resolve the expression, then stamp the
    /// returned `ExprId` with its CST source range in `body.expr_spans`. Kept
    /// separate from `resolve_expr_inner` so every recursive sub-expression
    /// (the inner dispatches back through this wrapper) is stamped too. The
    /// side-table is read only by the type-checker for diagnostic positions;
    /// the lowerer ignores it, so this is codegen-neutral.
    fn resolve_expr(&mut self, e: &ast::Expr) -> ExprId {
        let r = e.syntax().text_range();
        let id = self.resolve_expr_inner(e);
        self.body.expr_spans.insert(id, self.span_of(r));
        id
    }

    fn resolve_expr_inner(&mut self, e: &ast::Expr) -> ExprId {
        match e {
            ast::Expr::Literal(l) => {
                let hir = if let Some(int) = l.int_literal() {
                    // An integer literal too large for the target Go `int`/`int64`
                    // (e.g. a 29-digit literal) is truncated silently by the
                    // Haskell oracle and lowers here to a node that panics at
                    // runtime as a classified `TypeMismatch`. Reject it at CHECK
                    // time (`sky check ≡ sky build` → "if it compiles it works")
                    // instead of shipping a program that "compiles" but crashes.
                    // The `quiet` gate mirrors interpolation-interior handling —
                    // the oracle never rejects there (doc 03 §1.6).
                    match int {
                        ast::IntLiteral::InRange(v) => Expr::Int(v),
                        ast::IntLiteral::OutOfRange { text, range } => {
                            if self.quiet == 0 {
                                let span = self.span_of(range);
                                self.result.diagnostics.push(
                                    Diagnostic::error(
                                        "E1005",
                                        format!(
                                            "Integer literal `{text}` is out of range \
                                             for `Int` (valid range is {} to {})",
                                            i64::MIN,
                                            i64::MAX
                                        ),
                                    )
                                    .with_label(span, "value does not fit in a 64-bit Int"),
                                );
                            }
                            Expr::Error
                        }
                    }
                } else if let Some(f) = l.as_float() {
                    Expr::Float(f)
                } else if let Some(b) = l.as_bool() {
                    Expr::Bool(b)
                } else if let Some(s) = l.as_string() {
                    // char token is single-quoted → distinct type (`Char`).
                    if l.is_char() {
                        Expr::Chr(s.into_boxed_str())
                    } else {
                        Expr::Str(s.into_boxed_str())
                    }
                } else {
                    Expr::Error
                };
                self.body.expr(hir)
            }
            ast::Expr::Multiline(m) => {
                // Reconstruct the string value from `StringChunk` tokens (raw
                // bytes, incl. the `"""` delimiters + literal text) interleaved
                // with `Interpolation` nodes (`{{expr}}` — stringified + `++`).
                // Pre-fix bug: this emitted `Expr::Str("")`, so every multiline
                // string (SQL `CREATE TABLE …`, HTML templates) lowered to empty.
                enum Seg {
                    Lit(String),
                    Ex(ExprId),
                }
                let mut segs: Vec<Seg> = Vec::new();
                let mut cur = String::new();
                self.quiet += 1;
                for ch in m.syntax().children_with_tokens() {
                    if let Some(t) = ch.as_token() {
                        if t.kind() == SyntaxKind::StringChunk {
                            cur.push_str(t.text());
                        }
                    } else if let Some(nd) = ch.as_node() {
                        if nd.kind() == SyntaxKind::Interpolation {
                            if !cur.is_empty() {
                                segs.push(Seg::Lit(std::mem::take(&mut cur)));
                            }
                            let mut eid = None;
                            for ie in cst::child_exprs(nd) {
                                eid = Some(self.resolve_expr(&ie));
                            }
                            if let Some(id) = eid {
                                segs.push(Seg::Ex(id));
                            }
                        }
                    }
                }
                self.quiet -= 1;
                if !cur.is_empty() {
                    segs.push(Seg::Lit(cur));
                }
                // Strip the `"""` delimiters off the first/last literal segments
                // and decode `\{{` → `{{` (the only multiline escape, doc 04 §9).
                let lit_idxs: Vec<usize> = segs
                    .iter()
                    .enumerate()
                    .filter(|(_, s)| matches!(s, Seg::Lit(_)))
                    .map(|(i, _)| i)
                    .collect();
                if let Some(&first) = lit_idxs.first() {
                    if let Seg::Lit(s) = &mut segs[first] {
                        if let Some(rest) = s.strip_prefix("\"\"\"") {
                            *s = rest.to_string();
                        }
                    }
                }
                if let Some(&last) = lit_idxs.last() {
                    if let Seg::Lit(s) = &mut segs[last] {
                        if let Some(rest) = s.strip_suffix("\"\"\"") {
                            *s = rest.to_string();
                        }
                    }
                }
                for s in &mut segs {
                    if let Seg::Lit(t) = s {
                        *t = decode_multiline_escapes(t);
                    }
                }
                // Build the value: pure-literal → `Expr::Str`; else fold with
                // `++`. An interpolation `{{expr}}` splices the expr's value
                // directly — Sky does NOT auto-`toString` (the docs' examples use
                // `{{String.fromInt n}}`), so the expr is expected to be a
                // `String` and concatenates as-is; a non-String is a genuine type
                // error, same as the oracle.
                let build_seg = |this: &mut Self, s: Seg| -> ExprId {
                    match s {
                        Seg::Lit(t) => this.body.expr(Expr::Str(t.into_boxed_str())),
                        Seg::Ex(id) => id,
                    }
                };
                if segs.is_empty() {
                    self.body.expr(Expr::Str(String::new().into_boxed_str()))
                } else {
                    let mut it = segs.into_iter();
                    let first = it.next().unwrap();
                    let mut acc = build_seg(self, first);
                    for s in it {
                        let rhs = build_seg(self, s);
                        let res = op_kernel("++");
                        acc = self.body.expr(Expr::Binop {
                            op: Name::new("++"),
                            res,
                            lhs: acc,
                            rhs,
                        });
                    }
                    acc
                }
            }
            ast::Expr::Ref(r) => {
                let name = r.name().map(|t| t.text().to_string()).unwrap_or_default();
                let span = r.name().map(|t| self.span_of(t.text_range()));
                let res = self.resolve_var(&name, span);
                if let Some(t) = r.name() {
                    self.record_ref(t.text_range(), res.clone());
                }
                self.body.expr(Expr::Var(res))
            }
            ast::Expr::QualRef(q) => {
                let (qual, name) = cst::dotted_parts(q.syntax());
                let span = Some(self.span_of(q.syntax().text_range()));
                let res = self.resolve_qual_var(&qual, &name, span);
                self.record_ref(q.syntax().text_range(), res.clone());
                self.body.expr(Expr::Var(res))
            }
            ast::Expr::Accessor(a) => {
                let f = cst::first_lower(a.syntax()).unwrap_or_default();
                self.body.expr(Expr::Accessor(Name::new(&f)))
            }
            ast::Expr::FieldAccess(fa) => {
                let base = cst::child_exprs(fa.syntax());
                let base_id = match base.first() {
                    Some(b) => self.resolve_expr(b),
                    None => self.body.expr(Expr::Error),
                };
                let field = cst::first_lower(fa.syntax()).unwrap_or_default();
                // LSP: record the field-name span → (receiver expr, field). The
                // last LowerIdent token of the field-access node is the field.
                if self.quiet == 0 {
                    if let Some(tok) = fa
                        .syntax()
                        .children_with_tokens()
                        .filter_map(|e| e.into_token())
                        .filter(|t| t.kind() == SyntaxKind::LowerIdent)
                        .last()
                    {
                        let owner = self.owner();
                        self.result.field_occs.push(FieldOcc {
                            span: self.span_of(tok.text_range()),
                            receiver: base_id,
                            field: Name::new(&field),
                            owner,
                        });
                    }
                }
                self.body.expr(Expr::Access(base_id, Name::new(&field)))
            }
            ast::Expr::List(l) => {
                let ids = l.elements().map(|e| self.resolve_expr(&e)).collect();
                self.body.expr(Expr::List(ids))
            }
            ast::Expr::Tuple(t) => {
                let ids = cst::child_exprs(t.syntax())
                    .iter()
                    .map(|e| self.resolve_expr(e))
                    .collect();
                self.body.expr(Expr::Tuple(ids))
            }
            ast::Expr::Unit(_) => self.body.expr(Expr::Unit),
            ast::Expr::Record(rec) => {
                let mut fields = Vec::new();
                for f in rec.fields() {
                    let name = f.name().map(|t| t.text().to_string()).unwrap_or_default();
                    let val = match f.value() {
                        Some(v) => self.resolve_expr(&v),
                        None => self.body.expr(Expr::Error),
                    };
                    fields.push((Name::new(&name), val));
                }
                self.body.expr(Expr::Record(fields))
            }
            ast::Expr::RecordUpdate(ru) => {
                let base_name = cst::first_lower(ru.syntax()).unwrap_or_default();
                let res =
                    self.resolve_var(&base_name, Some(self.span_of(ru.syntax().text_range())));
                if let Some(tok) = ru
                    .syntax()
                    .children_with_tokens()
                    .filter_map(|e| e.into_token())
                    .find(|t| t.kind() == SyntaxKind::LowerIdent)
                {
                    self.record_ref(tok.text_range(), res.clone());
                }
                let base_id = self.body.expr(Expr::Var(res));
                let mut fields = Vec::new();
                for f in ru.fields() {
                    let name = f.name().map(|t| t.text().to_string()).unwrap_or_default();
                    let val = match f.value() {
                        Some(v) => self.resolve_expr(&v),
                        None => self.body.expr(Expr::Error),
                    };
                    fields.push((Name::new(&name), val));
                }
                self.body.expr(Expr::Update {
                    base: base_id,
                    fields,
                })
            }
            ast::Expr::Paren(p) => match cst::child_exprs(p.syntax()).first() {
                Some(inner) => self.resolve_expr(inner),
                None => self.body.expr(Expr::Error),
            },
            ast::Expr::Negate(n) => {
                let inner = match cst::child_exprs(n.syntax()).first() {
                    Some(e) => self.resolve_expr(e),
                    None => self.body.expr(Expr::Error),
                };
                self.body.expr(Expr::Negate(inner))
            }
            ast::Expr::Bin(b) => {
                let lhs = match b.lhs() {
                    Some(e) => self.resolve_expr(&e),
                    None => self.body.expr(Expr::Error),
                };
                let rhs = match b.rhs() {
                    Some(e) => self.resolve_expr(&e),
                    None => self.body.expr(Expr::Error),
                };
                let op = b.op().map(|t| t.text().to_string()).unwrap_or_default();
                let res = op_kernel(&op);
                self.body.expr(Expr::Binop {
                    op: Name::new(&op),
                    res,
                    lhs,
                    rhs,
                })
            }
            ast::Expr::Call(c) => {
                let parts = c.parts();
                let (callee, args) = match parts.split_first() {
                    Some((f, a)) => (
                        self.resolve_expr(f),
                        a.iter().map(|e| self.resolve_expr(e)).collect(),
                    ),
                    None => (self.body.expr(Expr::Error), Vec::new()),
                };
                self.body.expr(Expr::Call(callee, args))
            }
            ast::Expr::Lambda(l) => {
                self.push_scope();
                let mut params = Vec::new();
                if let Some(pl) = l.params() {
                    self.check_linear_group(pl.params());
                    for p in pl.params() {
                        params.push(self.resolve_pattern(&p));
                    }
                }
                let body = match l.body() {
                    Some(b) => self.resolve_expr(&b),
                    None => self.body.expr(Expr::Error),
                };
                self.pop_scope();
                self.body.expr(Expr::Lambda { params, body })
            }
            ast::Expr::If(i) => {
                let parts = i.parts();
                let mut ids: Vec<ExprId> = parts.iter().map(|e| self.resolve_expr(e)).collect();
                // [cond, then, else] — fold to a 1-arm If; nested else-if is a
                // nested IfExpr already handled recursively.
                let els = ids.pop().unwrap_or_else(|| self.body.expr(Expr::Error));
                let arms = match (ids.first(), ids.get(1)) {
                    (Some(&c), Some(&t)) => vec![(c, t)],
                    _ => Vec::new(),
                };
                self.body.expr(Expr::If { arms, els })
            }
            ast::Expr::Let(l) => self.resolve_let(l),
            ast::Expr::Case(c) => self.resolve_case(c),
        }
    }

    fn resolve_let(&mut self, l: &ast::LetExpr) -> ExprId {
        self.push_scope();
        let bindings: Vec<ast::LetBinding> = l.bindings().collect();
        let destruct_nodes: Vec<syntax::SyntaxNode> = l
            .syntax()
            .children()
            .filter(|c| c.kind() == SyntaxKind::DestructureBinding)
            .collect();

        // pre-pass: bind all group binder names (forward references, C6). The
        // real LocalId is captured here (aligned to `bindings`) so `binders`
        // carries the actual binding id — body references resolve to it.
        let mut binder_ids: Vec<Option<LocalId>> = Vec::with_capacity(bindings.len());
        for b in &bindings {
            match b.name() {
                Some(n) => {
                    let id = self.bind_local(n.text());
                    self.record_binder(n.text_range(), id);
                    binder_ids.push(Some(id));
                }
                None => binder_ids.push(None),
            }
        }
        for d in &destruct_nodes {
            if let Some(p) = cst::child_pats(d).first() {
                self.bind_pattern_names(p);
            }
        }

        // resolve pass — walk let children in SOURCE order so a destructure
        // (`(gMin, gMax) = …`) whose binders a later binding captures inside a
        // closure is emitted before that binding (Go rejects a closure that
        // captures a not-yet-declared var — examples 26/37).
        let mut defs = Vec::new();
        let mut binding_ix = 0usize;
        for child in l.syntax().children() {
            match child.kind() {
                SyntaxKind::LetBinding => {
                    let Some(b) = ast::LetBinding::cast(child) else {
                        continue;
                    };
                    let i = binding_ix;
                    binding_ix += 1;
                    let binders = match (b.name(), binder_ids.get(i).copied().flatten()) {
                        (Some(n), Some(id)) => vec![(Name::new(n.text()), id)],
                        _ => Vec::new(),
                    };
                    let params_node = b
                        .syntax()
                        .children()
                        .find(|c| c.kind() == SyntaxKind::ParamList);
                    let has_body = b.body().is_some();
                    if let Some(pl) = params_node {
                        self.push_scope();
                        let mut params = Vec::new();
                        let plist = ast::ParamList::cast(pl)
                            .map(|pl| pl.params().collect::<Vec<_>>())
                            .unwrap_or_default();
                        self.check_linear_group(plist.iter().cloned());
                        for p in plist {
                            params.push(self.resolve_pattern(&p));
                        }
                        let body = match b.body() {
                            Some(e) => self.resolve_expr(&e),
                            None => self.body.expr(Expr::Error),
                        };
                        self.pop_scope();
                        defs.push(LocalDef {
                            binders,
                            pat: None,
                            params,
                            body,
                        });
                    } else if has_body {
                        let body = self.resolve_expr(&b.body().unwrap());
                        defs.push(LocalDef {
                            binders,
                            pat: None,
                            params: Vec::new(),
                            body,
                        });
                    } else {
                        // annotation-only binding: resolve its type
                        if let Some(t) = b.syntax().children().find_map(ast::Type::cast) {
                            let _ = self.resolve_type(&t);
                        }
                    }
                }
                SyntaxKind::DestructureBinding => {
                    // The pre-pass already bound this destructure's binder names
                    // for forward references; reuse those ids so the pattern's
                    // `Var` ids match what sibling bindings + body point to.
                    self.reuse_binders = true;
                    let pat = cst::child_pats(&child)
                        .first()
                        .map(|p| self.resolve_pattern(p));
                    self.reuse_binders = false;
                    let val = cst::child_exprs(&child)
                        .first()
                        .map(|e| self.resolve_expr(e))
                        .unwrap_or_else(|| self.body.expr(Expr::Error));
                    defs.push(LocalDef {
                        binders: Vec::new(),
                        pat,
                        params: Vec::new(),
                        body: val,
                    });
                }
                _ => {}
            }
        }

        let body = match l.body() {
            Some(b) => self.resolve_expr(&b),
            None => self.body.expr(Expr::Error),
        };
        self.pop_scope();
        self.body.expr(Expr::Let { defs, body })
    }

    fn resolve_case(&mut self, c: &ast::CaseExpr) -> ExprId {
        let subject = match c.subject() {
            Some(s) => self.resolve_expr(&s),
            None => self.body.expr(Expr::Error),
        };
        let mut branches = Vec::new();
        for arm in c.arms() {
            self.push_scope();
            let pat = match arm.pattern() {
                Some(p) => {
                    self.check_linear_group(std::iter::once(p.clone()));
                    self.resolve_pattern(&p)
                }
                None => self.body.pat(Pattern::Error),
            };
            let body = match arm.body() {
                Some(b) => self.resolve_expr(&b),
                None => self.body.expr(Expr::Error),
            };
            self.pop_scope();
            branches.push(CaseBranch { pat, body });
        }
        self.body.expr(Expr::Case { subject, branches })
    }

    // ---- pattern resolution ---------------------------------------------

    /// Linearity gate for a set of patterns bound TOGETHER in one scope
    /// (a function/lambda parameter list, or a single `case`-arm / `let`
    /// destructure pattern). Sky patterns are linear: the same variable may not
    /// be bound twice in one group — `f x x = …` and `case p of (a, a) -> …`
    /// both fail the oracle (at `go build`: "x redeclared" / "no new variables
    /// on left side of :="). We reject at check-time instead — `sky check ≡ sky
    /// build`, and rejecting BEFORE emit keeps a program that Go would refuse
    /// from ever reaching codegen. Only INTRA-group duplicates are flagged;
    /// shadowing across nested scopes (a lambda re-using an outer name) is legal
    /// and untouched. `_` is not a binder and never collides.
    fn check_linear_group(&mut self, pats: impl Iterator<Item = ast::Pattern>) {
        if self.quiet > 0 {
            return;
        }
        let mut acc: Vec<String> = Vec::new();
        for p in pats {
            collect_pattern_binders(&p, &mut acc);
        }
        let mut seen: HashSet<String> = HashSet::new();
        let mut reported: HashSet<String> = HashSet::new();
        for name in &acc {
            if !seen.insert(name.clone()) && reported.insert(name.clone()) {
                self.result.diagnostics.push(Diagnostic::error(
                    "E1003",
                    format!("Variable `{name}` is bound more than once in the same pattern"),
                ));
            }
        }
    }

    /// Register the variable binders of a pattern (used by the `let` pre-pass;
    /// does not resolve ctor heads).
    fn bind_pattern_names(&mut self, p: &ast::Pattern) {
        match p {
            ast::Pattern::Var(v) => {
                if let Some(n) = cst::first_lower(v.syntax()) {
                    self.bind_local(&n);
                }
            }
            ast::Pattern::Alias(a) => {
                let kids = cst::child_pats(a.syntax());
                if let Some(inner) = kids.first() {
                    self.bind_pattern_names(inner);
                }
                if let Some(n) = cst::lower_idents(a.syntax()).last() {
                    self.bind_local(n);
                }
            }
            ast::Pattern::Ctor(c) => {
                for a in cst::child_pats(c.syntax()) {
                    self.bind_pattern_names(&a);
                }
            }
            ast::Pattern::CtorQual(c) => {
                for a in cst::child_pats(c.syntax()) {
                    self.bind_pattern_names(&a);
                }
            }
            ast::Pattern::Tuple(t) => {
                for a in cst::child_pats(t.syntax()) {
                    self.bind_pattern_names(&a);
                }
            }
            ast::Pattern::List(t) => {
                for a in cst::child_pats(t.syntax()) {
                    self.bind_pattern_names(&a);
                }
            }
            ast::Pattern::Cons(t) => {
                for a in cst::child_pats(t.syntax()) {
                    self.bind_pattern_names(&a);
                }
            }
            ast::Pattern::Paren(t) => {
                for a in cst::child_pats(t.syntax()) {
                    self.bind_pattern_names(&a);
                }
            }
            ast::Pattern::Record(r) => {
                for f in cst::lower_idents(r.syntax()) {
                    self.bind_local(&f);
                }
            }
            _ => {}
        }
    }

    /// Thin span-recording wrapper for patterns — mirrors `resolve_expr`.
    fn resolve_pattern(&mut self, p: &ast::Pattern) -> PatId {
        let r = p.syntax().text_range();
        let id = self.resolve_pattern_inner(p);
        self.body.pat_spans.insert(id, self.span_of(r));
        id
    }

    fn resolve_pattern_inner(&mut self, p: &ast::Pattern) -> PatId {
        match p {
            ast::Pattern::Wildcard(_) => self.body.pat(Pattern::Anything),
            ast::Pattern::Var(v) => {
                let n = cst::first_lower(v.syntax()).unwrap_or_default();
                let id = self.bind_local(&n);
                // Record the LowerIdent TOKEN range, not the node range (which
                // includes leading whitespace) — else rename/goto edit the space
                // and corrupt source (`pick maybeVal` → `pickmv`).
                let span = cst::first_lower_tok(v.syntax())
                    .map(|t| t.text_range())
                    .unwrap_or_else(|| v.syntax().text_range());
                self.record_binder(span, id);
                self.body.pat(Pattern::Var(id))
            }
            ast::Pattern::Unit(_) => self.body.pat(Pattern::Unit),
            ast::Pattern::Int(i) => {
                let val = i
                    .syntax()
                    .text()
                    .to_string()
                    .trim()
                    .parse::<i64>()
                    .unwrap_or(0);
                self.body.pat(Pattern::Int(val))
            }
            ast::Pattern::Float(fp) => {
                // Float literal patterns are unsupported — float equality is
                // unreliable, so the oracle rejects them at canonicalisation
                // ("Float patterns not supported"). Emit a clean [E1006] here so
                // accept/reject parity holds AND the program never reaches
                // codegen: a silently recovered-as-wildcard float pattern
                // otherwise HM-checks and then emits go-build-broken Go (an
                // unused `_subj` / non-exhaustive lowering) — a check≢build hole.
                // Still recover as `Anything` to suppress cascade diagnostics.
                let span = self.span_of(cst::sig_range(fp.syntax()));
                self.result.diagnostics.push(
                    Diagnostic::error(
                        "E1006",
                        "Float patterns are not supported — float equality is \
                         unreliable. Match on an `Int` (e.g. via `round`/`floor`) \
                         or use an `if` guard instead.",
                    )
                    .with_label(span, "float literal pattern"),
                );
                self.body.pat(Pattern::Anything)
            }
            ast::Pattern::Str(s) => {
                // Pre-fix bug: this emitted an EMPTY string, so every string
                // `case` pattern compiled to `subj == ""` — `case cmd of "add"
                // -> …` matched only the empty command.
                self.body.pat(Pattern::Str(s.value().into_boxed_str()))
            }
            ast::Pattern::Char(c) => self.body.pat(Pattern::Chr(c.value().into_boxed_str())),
            ast::Pattern::Bool(b) => {
                let val = cst::first_token_is_true(b.syntax());
                self.body.pat(Pattern::Bool(val))
            }
            ast::Pattern::Negate(n) => {
                // A negative-literal pattern (`case n of -1 -> …`). The prior stub
                // hardcoded `Int(0)`, so EVERY negative pattern lowered to
                // `_subj == 0` — a silent miscompile (`-1`/`-5` never matched, and
                // `0` wrongly matched them). Parse the negated literal from the
                // node text; a negated FLOAT is still a float pattern and routes to
                // the same unsupported-[E1006] path as a plain float literal.
                let text: String = n
                    .syntax()
                    .text()
                    .to_string()
                    .chars()
                    .filter(|c| !c.is_whitespace())
                    .collect();
                if text.contains('.') || text.contains('e') || text.contains('E') {
                    let span = self.span_of(cst::sig_range(n.syntax()));
                    self.result.diagnostics.push(
                        Diagnostic::error(
                            "E1006",
                            "Float patterns are not supported — float equality is \
                             unreliable. Match on an `Int` (e.g. via `round`/`floor`) \
                             or use an `if` guard instead.",
                        )
                        .with_label(span, "float literal pattern"),
                    );
                    self.body.pat(Pattern::Anything)
                } else if let Ok(val) = text.parse::<i64>() {
                    self.body.pat(Pattern::Int(val))
                } else {
                    // `-<non-literal>` is not a valid pattern; recover as wildcard.
                    self.body.pat(Pattern::Anything)
                }
            }
            ast::Pattern::Record(r) => {
                let mut binders = Vec::new();
                for f in cst::lower_idents(r.syntax()) {
                    let id = self.bind_local(&f);
                    binders.push((Name::new(&f), id));
                }
                self.body.pat(Pattern::Record(binders))
            }
            ast::Pattern::Alias(a) => {
                let kids = cst::child_pats(a.syntax());
                let inner = match kids.first() {
                    Some(p) => self.resolve_pattern(p),
                    None => self.body.pat(Pattern::Anything),
                };
                let alias = cst::lower_idents(a.syntax())
                    .last()
                    .cloned()
                    .unwrap_or_default();
                let id = self.bind_local(&alias);
                self.body.pat(Pattern::Alias(inner, id))
            }
            ast::Pattern::Tuple(t) => {
                let ids = cst::child_pats(t.syntax())
                    .iter()
                    .map(|p| self.resolve_pattern(p))
                    .collect();
                self.body.pat(Pattern::Tuple(ids))
            }
            ast::Pattern::List(t) => {
                let ids = cst::child_pats(t.syntax())
                    .iter()
                    .map(|p| self.resolve_pattern(p))
                    .collect();
                self.body.pat(Pattern::List(ids))
            }
            ast::Pattern::Cons(t) => {
                let kids = cst::child_pats(t.syntax());
                let head = kids
                    .first()
                    .map(|p| self.resolve_pattern(p))
                    .unwrap_or_else(|| self.body.pat(Pattern::Anything));
                let tail = kids
                    .get(1)
                    .map(|p| self.resolve_pattern(p))
                    .unwrap_or_else(|| self.body.pat(Pattern::Anything));
                self.body.pat(Pattern::Cons(head, tail))
            }
            ast::Pattern::Paren(t) => match cst::child_pats(t.syntax()).first() {
                Some(p) => self.resolve_pattern(p),
                None => self.body.pat(Pattern::Unit),
            },
            ast::Pattern::Ctor(c) => {
                let name = cst::first_upper(c.syntax()).unwrap_or_default();
                let args = cst::child_pats(c.syntax())
                    .iter()
                    .map(|p| self.resolve_pattern(p))
                    .collect();
                // Unqualified unknown ctor pattern head: Elm degrades silently
                // (doc 05 §12) — no diagnostic.
                //
                // An AMBIGUOUS one is a different matter: a pattern head that
                // could be either module's constructor selects a different branch
                // depending on import order, so it is reported here for the same
                // reason the expression position is (doc 05 §6b).
                if let Some(cands) = self.ambiguous_ctors.get(&name).cloned() {
                    let sp = cst::first_upper_tok(c.syntax()).map(|t| self.span_of(t.text_range()));
                    self.report_ambiguous(&name, &cands, sp, "constructor");
                }
                let ctor = self.ctors.get(&name).cloned();
                // Record the ctor NAME token as a use-site so hover / goto-def /
                // find-references / rename / semantic-tokens all treat a pattern
                // constructor exactly like an expression one — without this,
                // rename silently skips the pattern occurrence → uncompilable file.
                if let (Some(cr), Some(tok)) = (&ctor, cst::first_upper_tok(c.syntax())) {
                    self.record_ref(tok.text_range(), Res::Ctor(cr.clone()));
                }
                self.body.pat(Pattern::Ctor {
                    ctor,
                    name: Name::new(&name),
                    args,
                })
            }
            ast::Pattern::CtorQual(c) => {
                let (qual, name) = cst::dotted_parts(c.syntax());
                let args = cst::child_pats(c.syntax())
                    .iter()
                    .map(|p| self.resolve_pattern(p))
                    .collect();
                let ctor = self.resolve_qual_ctor(&qual, &name);
                if let (Some(cr), Some(tok)) = (&ctor, cst::last_upper_tok(c.syntax())) {
                    self.record_ref(tok.text_range(), Res::Ctor(cr.clone()));
                }
                self.body.pat(Pattern::Ctor {
                    ctor,
                    name: Name::new(&name),
                    args,
                })
            }
        }
    }

    // ---- type resolution ------------------------------------------------

    fn resolve_type(&mut self, t: &ast::Type) -> TypeId {
        match t {
            ast::Type::Var(v) => {
                let n = cst::first_lower(v.syntax()).unwrap_or_default();
                self.body.ty(Type::Var(Name::new(&n)))
            }
            ast::Type::Con(c) => {
                let name = cst::first_upper(c.syntax()).unwrap_or_default();
                let range = type_con_range(c.syntax());
                self.record_type_occ(range, &name);
                self.type_con_at(&name, Vec::new(), Some(self.span_of(range)))
            }
            ast::Type::Qual(q) => {
                let (qual, name) = cst::dotted_parts(q.syntax());
                self.type_qual(&qual, &name, Vec::new())
            }
            ast::Type::App(app) => {
                let parts = cst::child_types(app.syntax());
                let Some((head, rest)) = parts.split_first() else {
                    return self.body.ty(Type::Error);
                };
                let args: Vec<TypeId> = rest.iter().map(|a| self.resolve_type(a)).collect();
                match head {
                    ast::Type::Con(c) => {
                        let name = cst::first_upper(c.syntax()).unwrap_or_default();
                        let range = type_con_range(c.syntax());
                        self.record_type_occ(range, &name);
                        self.type_con_at(&name, args, Some(self.span_of(range)))
                    }
                    ast::Type::Qual(q) => {
                        let (qual, name) = cst::dotted_parts(q.syntax());
                        self.type_qual(&qual, &name, args)
                    }
                    ast::Type::Var(v) => {
                        let n = cst::first_lower(v.syntax()).unwrap_or_default();
                        self.body.ty(Type::Con {
                            con: None,
                            name: Name::new(&n),
                            args,
                        })
                    }
                    other => self.resolve_type(other),
                }
            }
            ast::Type::Fun(f) => {
                let kids = cst::child_types(f.syntax());
                let from = kids
                    .first()
                    .map(|t| self.resolve_type(t))
                    .unwrap_or_else(|| self.body.ty(Type::Error));
                let to = kids
                    .get(1)
                    .map(|t| self.resolve_type(t))
                    .unwrap_or_else(|| self.body.ty(Type::Error));
                self.body.ty(Type::Lambda(from, to))
            }
            ast::Type::Tuple(t) => {
                let ids = cst::child_types(t.syntax())
                    .iter()
                    .map(|x| self.resolve_type(x))
                    .collect();
                self.body.ty(Type::Tuple(ids))
            }
            ast::Type::Unit(_) => self.body.ty(Type::Unit),
            ast::Type::Paren(p) => match cst::child_types(p.syntax()).first() {
                Some(inner) => self.resolve_type(inner),
                None => self.body.ty(Type::Unit),
            },
            ast::Type::Record(r) => {
                let mut fields = Vec::new();
                for field in r
                    .syntax()
                    .children()
                    .filter(|c| c.kind() == SyntaxKind::TypeRecordField)
                {
                    let fname = cst::first_lower(&field).unwrap_or_default();
                    let fty = cst::child_types(&field)
                        .first()
                        .map(|t| self.resolve_type(t))
                        .unwrap_or_else(|| self.body.ty(Type::Error));
                    fields.push((Name::new(&fname), fty));
                }
                let row = r
                    .syntax()
                    .children()
                    .find(|c| c.kind() == SyntaxKind::RowVar)
                    .and_then(|rv| cst::first_lower(&rv))
                    .map(|n| Name::new(&n));
                self.body.ty(Type::Record(fields, row))
            }
        }
    }

    fn type_con_at(&mut self, name: &str, args: Vec<TypeId>, span: Option<Span>) -> TypeId {
        // Reported HERE — at the annotation that fails to say which module it
        // means — never at the import, for the same reason values are: importing
        // two modules that both export a type name you never write is legal and
        // common. The binding in `types` is still used so resolution stays total;
        // `project::build` halts on the diagnostic before lowering runs.
        if let Some(cands) = self.ambiguous_types.get(name).cloned() {
            self.report_ambiguous_type(name, &cands, span);
        }
        if let Some(tr) = self.types.get(name) {
            return self.body.ty(Type::Con {
                con: Some(*tr),
                name: Name::new(name),
                args,
            });
        }
        if KERNEL_IMPLICIT_TYPES.contains(&name) {
            return self.body.ty(Type::Con {
                con: None,
                name: Name::new(name),
                args,
            });
        }
        // An unqualified type constructor not in scope is NOT a name-resolver
        // gap: the Sky canonicaliser leaves it nominal and the type checker
        // resolves it against the kernel type registry (`Dict`, `Set`, …) and
        // the cross-module type-home map (doc 05 §8, §12 — types are not in the
        // recovery table). Resolve leniently, no diagnostic; the oracle agrees.
        // (A bare uppercase type is a kernel/local type, never an FFI type —
        // those only ever appear qualified, so no foreign attribution here.)
        self.body.ty(Type::Con {
            con: None,
            name: Name::new(name),
            args,
        })
    }

    fn type_qual(&mut self, qual: &str, name: &str, args: Vec<TypeId>) -> TypeId {
        let qentry = self.qual_types.get(qual).and_then(|m| m.get(name)).cloned();
        if let Some(entry) = qentry {
            {
                match entry {
                    TypeResEntry::Res(tr) => {
                        return self.body.ty(Type::Con {
                            con: Some(tr),
                            name: Name::new(name),
                            args,
                        })
                    }
                    TypeResEntry::Foreign(pkg) => {
                        self.track_class_b(
                            pkg.clone(),
                            Some(qual.to_string()),
                            name,
                            RefKind::Type,
                        );
                        return self.body.ty(Type::Foreign {
                            package: Name::new(&pkg),
                            name: Name::new(name),
                            args,
                        });
                    }
                }
            }
        }
        // kernel-qualified type (e.g. Json decoders' types) — lenient kernel
        if self.db.kernel_pseudo(qual).is_some() {
            return self.body.ty(Type::Con {
                con: None,
                name: Name::new(name),
                args,
            });
        }
        match self.import_aliases.get(qual).cloned() {
            Some(ImportSource::Foreign(pkg)) => {
                self.track_class_b(pkg.clone(), Some(qual.to_string()), name, RefKind::Type);
                self.body.ty(Type::Foreign {
                    package: Name::new(&pkg),
                    name: Name::new(name),
                    args,
                })
            }
            Some(ImportSource::Kernel(_)) => self.body.ty(Type::Con {
                con: None,
                name: Name::new(name),
                args,
            }),
            Some(ImportSource::Dep(dep)) => {
                let exports = self.db.module_exports(dep);
                let con = exports
                    .type_(name)
                    .map(|(def, arity)| TypeRes { con: def, arity });
                self.body.ty(Type::Con {
                    con,
                    name: Name::new(name),
                    args,
                })
            }
            // Unknown type qualifier is not a name-resolver gap either (types
            // are resolved by the type checker, doc 05 §12). Lenient nominal.
            None => self.body.ty(Type::Con {
                con: None,
                name: Name::new(name),
                args,
            }),
        }
    }

    // ---- name resolution primitives (doc 05 §4, §9) ---------------------

    fn resolve_var(&mut self, name: &str, span: Option<Span>) -> Res {
        if let Some(id) = self.lookup_local(name) {
            return Res::Local(id);
        }
        // Ambiguity is reported HERE — at the reference that fails to say which
        // module it means — not at the import (doc 05 §6b). The binding recorded
        // in `ctors` / `vars` is still returned so the rest of resolution stays
        // total; the build halts on the diagnostic before lowering ever runs.
        if let Some(cands) = self.ambiguous_ctors.get(name).cloned() {
            self.report_ambiguous(name, &cands, span, "constructor");
        }
        if let Some(c) = self.ctors.get(name) {
            return Res::Ctor(c.clone());
        }
        if let Some(cands) = self.ambiguous_vars.get(name).cloned() {
            self.report_ambiguous(name, &cands, span, "name");
        }
        if let Some(r) = self.vars.get(name).cloned() {
            if let Res::Foreign { package, .. } = &r {
                self.track_class_b(package.as_str().to_string(), None, name, RefKind::Value);
            }
            return r;
        }
        // lenient kernel-exposing-all fallback
        if let Some(pseudo) = self.kernel_open.first() {
            return Res::Kernel {
                module: Name::new(pseudo),
                func: Name::new(name),
            };
        }
        if let Some(pkg) = self.foreign_open.clone() {
            self.track_class_b(pkg.clone(), None, name, RefKind::Value);
            return Res::Foreign {
                package: Name::new(&pkg),
                name: Name::new(name),
            };
        }
        self.track_class_a(None, name, RefKind::Value, "undefined name", span);
        Res::Error
    }

    fn resolve_qual_var(&mut self, qual: &str, name: &str, span: Option<Span>) -> Res {
        if let Some(c) = self.qual_ctors.get(qual).and_then(|m| m.get(name)) {
            return Res::Ctor(c.clone());
        }
        if let Some(r) = self.qual_vars.get(qual).and_then(|m| m.get(name)) {
            return r.clone();
        }
        // Explicit Go-FFI alias in THIS module wins over the ambient kernel
        // pseudo-module (CLAUDE.md "explicit alias wins"): `import
        // Github.Com.Google.Uuid as Uuid` makes `Uuid.newString` a Foreign FFI
        // ref even though `Uuid` is ALSO a kernel pseudo (`Uuid.v4`/`v7`). Only
        // the Foreign source is short-circuited here — Dep / Kernel imports keep
        // their existing precedence (kernel_pseudo fallback below), so this
        // cannot regress a stdlib-module qualifier that happens to shadow a
        // kernel pseudo.
        if let Some(ImportSource::Foreign(pkg)) = self.import_aliases.get(qual).cloned() {
            self.track_class_b(pkg.clone(), Some(qual.to_string()), name, RefKind::Value);
            return Res::Foreign {
                package: Name::new(&pkg),
                name: Name::new(name),
            };
        }
        if let Some(pseudo) = self.db.kernel_pseudo(qual) {
            return Res::Kernel {
                module: Name::new(pseudo),
                func: Name::new(name),
            };
        }
        match self.import_aliases.get(qual).cloned() {
            Some(ImportSource::Kernel(pseudo)) => Res::Kernel {
                module: Name::new(&pseudo),
                func: Name::new(name),
            },
            Some(ImportSource::Foreign(pkg)) => {
                self.track_class_b(pkg.clone(), Some(qual.to_string()), name, RefKind::Value);
                Res::Foreign {
                    package: Name::new(&pkg),
                    name: Name::new(name),
                }
            }
            Some(ImportSource::Dep(dep)) => {
                let exports = self.db.module_exports(dep);
                if let Some(def) = exports.value(name) {
                    Res::Def(def)
                } else if let Some((u, ct)) = exports.ctor(name) {
                    let _ = u;
                    Res::Ctor(to_ctor_ref(ct))
                } else {
                    self.track_class_a(
                        Some(qual.to_string()),
                        name,
                        RefKind::Value,
                        "name not exported by module",
                        span,
                    );
                    Res::Error
                }
            }
            None => {
                let hint = self.did_you_mean(qual);
                self.track_class_a(
                    Some(qual.to_string()),
                    name,
                    RefKind::Value,
                    &format!(
                        "unknown qualifier{}",
                        hint.map(|h| format!(" (did you mean `{h}`?)"))
                            .unwrap_or_default()
                    ),
                    span,
                );
                Res::Error
            }
        }
    }

    fn resolve_qual_ctor(&mut self, qual: &str, name: &str) -> Option<CtorRef> {
        if let Some(c) = self.qual_ctors.get(qual).and_then(|m| m.get(name)) {
            return Some(c.clone());
        }
        // qualified ctor via a Dep whose ctor wasn't bound under the qualifier
        if let Some(ImportSource::Dep(dep)) = self.import_aliases.get(qual).cloned() {
            let exports = self.db.module_exports(dep);
            if let Some((_, ct)) = exports.ctor(name) {
                return Some(to_ctor_ref(ct));
            }
        }
        if let Some(ImportSource::Foreign(pkg)) = self.import_aliases.get(qual).cloned() {
            self.track_class_b(pkg, Some(qual.to_string()), name, RefKind::Ctor);
            return None;
        }
        if self.db.kernel_pseudo(qual).is_some() {
            return None; // kernel ctor, resolved leniently (no CtorRef needed)
        }
        self.track_class_a(
            Some(qual.to_string()),
            name,
            RefKind::Ctor,
            "unknown qualified constructor",
            None,
        );
        None
    }

    // ---- diagnostics + tracking -----------------------------------------

    fn track_class_a(
        &mut self,
        qualifier: Option<String>,
        name: &str,
        kind: RefKind,
        reason: &str,
        span: Option<Span>,
    ) {
        if self.quiet > 0 {
            return;
        }
        let full = match &qualifier {
            Some(q) => format!("{q}.{name}"),
            None => name.to_string(),
        };
        // Attach the reference's source span so the renderer shows the line +
        // caret + excerpt (the E2001 path already does). `reason` stays on the
        // structured `class_a` entry — it was a redundant parenthetical in the
        // user-facing message.
        let mut diag = Diagnostic::error("E1001", format!("Undefined name: {full}"));
        if let Some(sp) = span {
            diag = diag.with_label(sp, "not defined");
        }
        self.result.diagnostics.push(diag);
        self.result.class_a.push(ClassA {
            qualifier,
            name: name.to_string(),
            kind,
            reason: reason.to_string(),
        });
    }

    fn track_class_b(
        &mut self,
        package: String,
        qualifier: Option<String>,
        name: &str,
        kind: RefKind,
    ) {
        if self.quiet > 0 {
            return;
        }
        self.result.class_b.push(ClassB {
            package,
            qualifier,
            name: name.to_string(),
            kind,
        });
    }

    fn did_you_mean(&self, qual: &str) -> Option<String> {
        let mut best: Option<(usize, String)> = None;
        let candidates = self
            .import_aliases
            .keys()
            .cloned()
            .chain(self.qual_vars.keys().cloned())
            .chain(self.qual_ctors.keys().cloned());
        for cand in candidates {
            let d = levenshtein(qual, &cand);
            if d > 2 {
                continue;
            }
            // Deterministic selection (L4): the candidate sets are `HashMap`s, so
            // their key-iteration order varies run-to-run. A strict `d < *bd`
            // tie-break lets whichever tied candidate is visited FIRST win — i.e.
            // the suggestion depended on hash-iteration order (the fuzzer caught
            // `Std.Cmd` suggesting `Sub` on one run and `Set` on the next). Break
            // ties by the lexicographically smallest name so the suggestion is a
            // pure function of the input, independent of iteration order.
            let better = match &best {
                None => true,
                Some((bd, bc)) => d < *bd || (d == *bd && cand < *bc),
            };
            if better {
                best = Some((d, cand));
            }
        }
        best.map(|(_, c)| c)
    }
}

// ---- free helpers --------------------------------------------------------

/// Collect the variable-binder names introduced by a pattern (recursing through
/// tuples / lists / cons / ctor args / parens / aliases / records). `_`
/// (wildcard) and literal patterns introduce no binder. Used by the linearity
/// gate; mirrors the binder set `bind_pattern_names` registers.
fn collect_pattern_binders(p: &ast::Pattern, acc: &mut Vec<String>) {
    match p {
        ast::Pattern::Var(v) => {
            if let Some(n) = cst::first_lower(v.syntax()) {
                acc.push(n);
            }
        }
        ast::Pattern::Alias(a) => {
            for k in cst::child_pats(a.syntax()) {
                collect_pattern_binders(&k, acc);
            }
            if let Some(n) = cst::lower_idents(a.syntax()).last() {
                acc.push(n.clone());
            }
        }
        ast::Pattern::Record(r) => {
            for f in cst::lower_idents(r.syntax()) {
                acc.push(f);
            }
        }
        ast::Pattern::Tuple(t) => {
            for k in cst::child_pats(t.syntax()) {
                collect_pattern_binders(&k, acc);
            }
        }
        ast::Pattern::List(t) => {
            for k in cst::child_pats(t.syntax()) {
                collect_pattern_binders(&k, acc);
            }
        }
        ast::Pattern::Cons(t) => {
            for k in cst::child_pats(t.syntax()) {
                collect_pattern_binders(&k, acc);
            }
        }
        ast::Pattern::Paren(t) => {
            for k in cst::child_pats(t.syntax()) {
                collect_pattern_binders(&k, acc);
            }
        }
        ast::Pattern::Ctor(c) => {
            for k in cst::child_pats(c.syntax()) {
                collect_pattern_binders(&k, acc);
            }
        }
        ast::Pattern::CtorQual(c) => {
            for k in cst::child_pats(c.syntax()) {
                collect_pattern_binders(&k, acc);
            }
        }
        _ => {}
    }
}

fn to_ctor_ref(ct: &crate::exports::ExportedCtor) -> CtorRef {
    CtorRef {
        def: ct.def,
        type_: ct.type_,
        index: ct.index,
        arity: ct.arity,
    }
}

/// The source range of a `TypeCon`'s NAME — the `UpperIdent` token, not the
/// enclosing node.
///
/// A `TypeCon` node's `text_range()` starts at the node's first token, which
/// includes the leading whitespace trivia the parser attached to it. Using the
/// node range makes `Shape` in `pick : Shape` span `" Shape"`, one column too
/// far left, which the editor then renders as a squiggle sitting under the
/// space — visible on `[E1012]`'s type-namespace arm, and on every hover /
/// goto-def / rename hit-range recorded by `record_type_occ` (hovering the
/// space before a type name resolved the type). Falls back to the node range
/// only when the node carries no `UpperIdent` at all, which the callers'
/// `first_upper` already treats as a malformed-parse case.
fn type_con_range(n: &syntax::SyntaxNode) -> syntax::TextRange {
    cst::first_upper_tok(n).map_or_else(|| n.text_range(), |t| t.text_range())
}

fn decl_name(d: &ast::Decl) -> Option<String> {
    match d {
        ast::Decl::TypeAnno(a) => a.name().map(|t| t.text().to_string()),
        _ => None,
    }
}

/// Decode the escapes of a multiline (triple-quoted) string literal segment
/// (doc 04 §9): `\\` collapses to a single backslash and `\{{` yields a literal
/// `{{`; every OTHER `\X` is preserved verbatim (so regex `\d+` and paths
/// `\test` survive). Left-to-right so `\\{{` is `\` + a live `{{`, not `\` +
/// escaped braces. (A plain `.replace("\\{{", "{{")` missed the `\\` collapse.)
fn decode_multiline_escapes(s: &str) -> String {
    let cs: Vec<char> = s.chars().collect();
    let mut out = String::with_capacity(s.len());
    let mut i = 0;
    while i < cs.len() {
        if cs[i] == '\\' && i + 1 < cs.len() && cs[i + 1] == '\\' {
            out.push('\\');
            i += 2;
        } else if cs[i] == '\\' && i + 2 < cs.len() && cs[i + 1] == '{' && cs[i + 2] == '{' {
            out.push('{');
            out.push('{');
            i += 3;
        } else {
            out.push(cs[i]);
            i += 1;
        }
    }
    out
}

/// The kernel reference an operator desugars to (doc 03 §5.2). Always resolves.
fn op_kernel(op: &str) -> Res {
    let func = match op {
        "+" => "add",
        "-" => "sub",
        "*" => "mul",
        "/" => "fdiv",
        "//" => "idiv",
        "%" => "mod",
        "^" => "pow",
        "++" => "append",
        "::" => "cons",
        "==" => "eq",
        "/=" => "neq",
        "<" => "lt",
        ">" => "gt",
        "<=" => "le",
        ">=" => "ge",
        "&&" => "and",
        "||" => "or",
        "|>" => "apR",
        "<|" => "apL",
        ">>" => "composeL",
        "<<" => "composeR",
        _ => "identity",
    };
    let module = if op == "::" { "List" } else { "Basics" };
    Res::Kernel {
        module: Name::new(module),
        func: Name::new(func),
    }
}

/// Explicit `import M as X` claims: alias `X` → canonical path `M`
/// (Module.hs:950). Used by `effective_qualifier`.
fn explicit_alias_claims(imports: &[ast::Import]) -> HashMap<String, String> {
    let mut claims = HashMap::new();
    for imp in imports {
        if let (Some(alias), Some(path)) = (imp.alias(), imp.name()) {
            claims.insert(alias.text().to_string(), path.text());
        }
    }
    claims
}

/// `effectiveQualifier` (Module.hs:976, doc 05 §6): explicit alias always binds;
/// a bare import's last segment is suppressed iff a *different* module's explicit
/// alias already claims it.
fn effective_qualifier(
    claims: &HashMap<String, String>,
    path: &str,
    alias: &Option<String>,
) -> Option<String> {
    if let Some(a) = alias {
        return Some(a.clone());
    }
    let last = path.rsplit('.').next().unwrap_or(path).to_string();
    match claims.get(&last) {
        Some(claimed) if claimed != path => None, // suppressed; explicit wins
        _ => Some(last),
    }
}

fn levenshtein(a: &str, b: &str) -> usize {
    let a: Vec<char> = a.chars().collect();
    let b: Vec<char> = b.chars().collect();
    let mut prev: Vec<usize> = (0..=b.len()).collect();
    let mut cur = vec![0; b.len() + 1];
    for (i, &ca) in a.iter().enumerate() {
        cur[0] = i + 1;
        for (j, &cb) in b.iter().enumerate() {
            let cost = usize::from(ca != cb);
            cur[j + 1] = (prev[j + 1] + 1).min(cur[j] + 1).min(prev[j] + cost);
        }
        std::mem::swap(&mut prev, &mut cur);
    }
    prev[b.len()]
}

#[cfg(test)]
mod tests {
    use super::decode_multiline_escapes;

    #[test]
    fn multiline_escapes_collapse_backslash_keep_others() {
        // `\\` collapses; `\{{` yields literal `{{`; every other `\X` verbatim.
        assert_eq!(decode_multiline_escapes(r"a\\b"), r"a\b");
        assert_eq!(
            decode_multiline_escapes(r"regex \d+ path\test"),
            r"regex \d+ path\test"
        );
        assert_eq!(decode_multiline_escapes(r"\{{literal}}"), "{{literal}}");
        // `\\{{` is `\` + a live `{{` (left-to-right), not `\` + escaped braces.
        assert_eq!(decode_multiline_escapes(r"\\{{"), r"\{{");
        // trailing single backslash preserved
        assert_eq!(decode_multiline_escapes(r"end\"), r"end\");
        assert_eq!(decode_multiline_escapes("plain"), "plain");
    }
}
