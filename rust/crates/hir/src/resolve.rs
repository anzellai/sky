//! The resolver (doc 05 §4-§12). Builds a module environment once (builtins →
//! imports → local decls) then walks each top-level body with a lexical scope
//! stack. Every reference becomes a `Res`; an unresolvable name becomes
//! `Res::Error` + a diagnostic and the walk continues (L7).

use crate::cst;
use crate::db::{ImportSource, SourceDb};
use crate::exports::ModuleExports;
use crate::hir::{Body, CaseBranch, Expr, ExprId, LocalDef, Pattern, PatId, TopDef, Type, TypeId};
use crate::ids::{CtorRef, DefKind, LocalId, Res, TypeRes};
use crate::kernel::{
    BUILTIN_CTORS, BUILTIN_TYPES, BUILTIN_VARS, KERNEL_IMPLICIT_TYPES, PRELUDE_QUALIFIERS,
};
use base::{DefId, FileId, ModuleId, Name, Span};
use diagnostics::Diagnostic;
use indexmap::IndexMap;
use std::collections::HashMap;
use syntax::ast::{self, AstNode};
use syntax::SyntaxKind;

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

/// The result of resolving one module (doc 05 §1).
#[derive(Default)]
pub struct ResolveResult {
    pub bodies: IndexMap<DefId, Body>,
    pub top_defs: Vec<TopDef>,
    pub diagnostics: Vec<Diagnostic>,
    pub class_a: Vec<ClassA>,
    pub class_b: Vec<ClassB>,
    pub occurrences: Vec<(Span, Res)>,
}

/// Resolve a module. Never panics; partial results + diagnostics (L7).
pub fn resolve(db: &SourceDb, module: ModuleId) -> ResolveResult {
    let mut r = Resolver::new(db, module);
    r.build_env();
    r.walk_module();
    r.result
}

struct Resolver<'a> {
    db: &'a SourceDb,
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
    fn new(db: &'a SourceDb, module: ModuleId) -> Self {
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
            scopes: Vec::new(),
            next_local: 0,
            reuse_binders: false,
            quiet: 0,
            body: Body::default(),
            result: ResolveResult::default(),
        }
    }

    fn def(&self, module: ModuleId, name: &str, kind: DefKind) -> DefId {
        self.db
            .defs()
            .borrow_mut()
            .intern(module, &Name::new(name), kind)
    }

    // ---- environment building (doc 05 §5, §10) --------------------------

    fn build_env(&mut self) {
        // 1. unconditional builtins
        for (name, m, f) in BUILTIN_VARS {
            self.vars.insert(
                (*name).to_string(),
                Res::Kernel {
                    module: Name::new(m),
                    func: Name::new(f),
                },
            );
        }
        for (name, arity) in BUILTIN_TYPES {
            let con = self.def(BUILTIN_MOD, name, DefKind::TypeCon);
            self.types
                .insert((*name).to_string(), TypeRes { con, arity: *arity });
        }
        for (cn, ty, index, arity) in BUILTIN_CTORS {
            let type_ = self.def(BUILTIN_MOD, ty, DefKind::TypeCon);
            let d = self.def(BUILTIN_MOD, cn, DefKind::Ctor);
            self.ctors.insert(
                (*cn).to_string(),
                CtorRef {
                    def: d,
                    type_,
                    index: *index,
                    arity: *arity,
                },
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
    }

    fn process_import(&mut self, imp: &ast::Import, claims: &HashMap<String, String>) {
        let Some(path) = imp.name().map(|n| n.text()) else {
            return;
        };
        let alias = imp.alias().map(|t| t.text().to_string());
        let source = self.db.classify_import(&path);
        let qual = effective_qualifier(claims, &path, &alias);

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
        let clause = imp
            .exposing()
            .map(|e| cst::read_exposing(e.syntax()));
        match (&source, clause) {
            (ImportSource::Dep(dep), Some(c)) => {
                let exports = self.db.module_exports(*dep);
                self.bind_exposing_dep(&c, &exports);
            }
            (ImportSource::Kernel(pseudo), Some(c)) => {
                if c.all {
                    self.kernel_open.push(pseudo.clone());
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

    fn bind_exposing_dep(&mut self, c: &cst::ExposingClause, exports: &ModuleExports) {
        if c.all {
            for (name, def) in &exports.values {
                self.vars.insert(name.as_str().to_string(), Res::Def(*def));
            }
            for u in &exports.unions {
                self.types.insert(
                    u.name.as_str().to_string(),
                    TypeRes {
                        con: u.def,
                        arity: u.arity,
                    },
                );
                for ct in &u.ctors {
                    self.ctors
                        .insert(ct.name.as_str().to_string(), to_ctor_ref(ct));
                }
            }
            for a in &exports.aliases {
                self.types.insert(
                    a.name.as_str().to_string(),
                    TypeRes {
                        con: a.def,
                        arity: a.arity,
                    },
                );
            }
            return;
        }
        for it in &c.items {
            match it {
                cst::ExposedItem::Value(v) => {
                    let res = exports
                        .value(v)
                        .map(Res::Def)
                        .unwrap_or_else(|| Res::Def(self.def(exports.module, v, DefKind::Value)));
                    self.vars.insert(v.clone(), res);
                }
                cst::ExposedItem::Type { name, ctors } => {
                    if let Some((def, arity)) = exports.type_(name) {
                        self.types.insert(name.clone(), TypeRes { con: def, arity });
                    } else {
                        // lenient: kernel-implicit or re-exported type
                        let con = self.def(exports.module, name, DefKind::TypeCon);
                        self.types.insert(name.clone(), TypeRes { con, arity: 0 });
                    }
                    // record-alias constructor value
                    if let Some(def) = exports.value(name) {
                        self.vars.insert(name.clone(), Res::Def(def));
                    }
                    match ctors {
                        cst::CtorExposure::None => {}
                        cst::CtorExposure::All => {
                            if let Some(u) = exports.unions.iter().find(|u| u.name.as_str() == name)
                            {
                                for ct in &u.ctors {
                                    self.ctors
                                        .insert(ct.name.as_str().to_string(), to_ctor_ref(ct));
                                }
                            }
                        }
                        cst::CtorExposure::Some(list) => {
                            if let Some(u) = exports.unions.iter().find(|u| u.name.as_str() == name)
                            {
                                for ct in &u.ctors {
                                    if list.iter().any(|x| x == ct.name.as_str()) {
                                        self.ctors
                                            .insert(ct.name.as_str().to_string(), to_ctor_ref(ct));
                                    }
                                }
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
                    self.vars.insert(
                        v.clone(),
                        Res::Kernel {
                            module: Name::new(pseudo),
                            func: Name::new(v),
                        },
                    );
                }
                cst::ExposedItem::Type { name, .. } => {
                    let con = self.def(self.module, name, DefKind::TypeCon);
                    self.types.insert(name.clone(), TypeRes { con, arity: 0 });
                }
                cst::ExposedItem::Operator => {}
            }
        }
    }

    fn bind_exposing_foreign(&mut self, pkg: &str, c: &cst::ExposingClause) {
        for it in &c.items {
            match it {
                cst::ExposedItem::Value(v) => {
                    self.vars.insert(
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
                    // also make the bare type resolvable leniently as foreign
                    let con = self.def(self.module, name, DefKind::TypeCon);
                    self.types.insert(name.clone(), TypeRes { con, arity: 0 });
                }
                cst::ExposedItem::Operator => {}
            }
        }
    }

    fn register_locals(&mut self, tree: &ast::SourceFile) {
        for decl in tree.decls() {
            match decl {
                ast::Decl::Value(v) => {
                    if let Some(n) = v.name() {
                        if n.kind() == SyntaxKind::LowerIdent {
                            let d = self.def(self.module, n.text(), DefKind::Value);
                            self.vars.insert(n.text().to_string(), Res::Def(d));
                        }
                    }
                }
                ast::Decl::Union(u) => {
                    let Some(tn) = u.name().map(|t| t.text().to_string()) else {
                        continue;
                    };
                    let arity = cst::decl_type_vars(u.syntax()).len() as u16;
                    let type_ = self.def(self.module, &tn, DefKind::TypeCon);
                    self.types
                        .insert(tn.clone(), TypeRes { con: type_, arity });
                    for (i, var) in u.variants().iter().enumerate() {
                        let Some(cn) = var.name().map(|t| t.text().to_string()) else {
                            continue;
                        };
                        let cargs = cst::child_types(var.syntax()).len() as u16;
                        let d = self.def(self.module, &cn, DefKind::Ctor);
                        self.ctors.insert(
                            cn,
                            CtorRef {
                                def: d,
                                type_,
                                index: i as u16,
                                arity: cargs,
                            },
                        );
                    }
                }
                ast::Decl::Alias(a) => {
                    let Some(an) = a.name().map(|t| t.text().to_string()) else {
                        continue;
                    };
                    let arity = cst::decl_type_vars(a.syntax()).len() as u16;
                    let is_record = a
                        .ty()
                        .map(|t| matches!(t, ast::Type::Record(_)))
                        .unwrap_or(false);
                    let con = self.def(self.module, &an, DefKind::TypeAlias);
                    self.types
                        .insert(an.clone(), TypeRes { con, arity });
                    if is_record {
                        let d = self.def(self.module, &an, DefKind::Value);
                        self.vars.insert(an, Res::Def(d));
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
        self.push_scope();
        let mut params = Vec::new();
        if let Some(pl) = v.params() {
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

    fn resolve_expr(&mut self, e: &ast::Expr) -> ExprId {
        match e {
            ast::Expr::Literal(l) => {
                let hir = if let Some(i) = l.as_int() {
                    Expr::Int(i)
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
                        *t = t.replace("\\{{", "{{");
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
                let name = r
                    .name()
                    .map(|t| t.text().to_string())
                    .unwrap_or_default();
                let res = self.resolve_var(&name);
                self.body.expr(Expr::Var(res))
            }
            ast::Expr::QualRef(q) => {
                let (qual, name) = cst::dotted_parts(q.syntax());
                let res = self.resolve_qual_var(&qual, &name);
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
                let res = self.resolve_var(&base_name);
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
                let mut ids: Vec<ExprId> =
                    parts.iter().map(|e| self.resolve_expr(e)).collect();
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
                Some(n) => binder_ids.push(Some(self.bind_local(n.text()))),
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
                    let Some(b) = ast::LetBinding::cast(child) else { continue };
                    let i = binding_ix;
                    binding_ix += 1;
                    let binders = match (b.name(), binder_ids.get(i).copied().flatten()) {
                        (Some(n), Some(id)) => vec![(Name::new(n.text()), id)],
                        _ => Vec::new(),
                    };
                    let params_node =
                        b.syntax().children().find(|c| c.kind() == SyntaxKind::ParamList);
                    let has_body = b.body().is_some();
                    if let Some(pl) = params_node {
                        self.push_scope();
                        let mut params = Vec::new();
                        for p in ast::ParamList::cast(pl)
                            .map(|pl| pl.params().collect::<Vec<_>>())
                            .unwrap_or_default()
                        {
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
                    let pat = cst::child_pats(&child).first().map(|p| self.resolve_pattern(p));
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
                Some(p) => self.resolve_pattern(&p),
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

    fn resolve_pattern(&mut self, p: &ast::Pattern) -> PatId {
        match p {
            ast::Pattern::Wildcard(_) => self.body.pat(Pattern::Anything),
            ast::Pattern::Var(v) => {
                let n = cst::first_lower(v.syntax()).unwrap_or_default();
                let id = self.bind_local(&n);
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
            ast::Pattern::Float(_) => {
                // Float patterns are rejected upstream; recover as wildcard.
                self.body.pat(Pattern::Anything)
            }
            ast::Pattern::Str(s) => {
                // Pre-fix bug: this emitted an EMPTY string, so every string
                // `case` pattern compiled to `subj == ""` — `case cmd of "add"
                // -> …` matched only the empty command.
                self.body.pat(Pattern::Str(s.value().into_boxed_str()))
            }
            ast::Pattern::Char(c) => {
                self.body.pat(Pattern::Chr(c.value().into_boxed_str()))
            }
            ast::Pattern::Bool(b) => {
                let val = cst::first_token_is_true(b.syntax());
                self.body.pat(Pattern::Bool(val))
            }
            ast::Pattern::Negate(_) => self.body.pat(Pattern::Int(0)),
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
                let ctor = self.ctors.get(&name).cloned();
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
                self.type_con(&name, Vec::new())
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
                        self.type_con(&name, args)
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

    fn type_con(&mut self, name: &str, args: Vec<TypeId>) -> TypeId {
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
                        self.track_class_b(pkg.clone(), Some(qual.to_string()), name, RefKind::Type);
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
                let con = exports.type_(name).map(|(def, arity)| TypeRes { con: def, arity });
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

    fn resolve_var(&mut self, name: &str) -> Res {
        if let Some(id) = self.lookup_local(name) {
            return Res::Local(id);
        }
        if let Some(c) = self.ctors.get(name) {
            return Res::Ctor(c.clone());
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
        self.track_class_a(None, name, RefKind::Value, "undefined name");
        Res::Error
    }

    fn resolve_qual_var(&mut self, qual: &str, name: &str) -> Res {
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
                        hint.map(|h| format!(" (did you mean `{h}`?)")).unwrap_or_default()
                    ),
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
        );
        None
    }

    // ---- diagnostics + tracking -----------------------------------------

    fn track_class_a(&mut self, qualifier: Option<String>, name: &str, kind: RefKind, reason: &str) {
        if self.quiet > 0 {
            return;
        }
        let full = match &qualifier {
            Some(q) => format!("{q}.{name}"),
            None => name.to_string(),
        };
        self.result.diagnostics.push(Diagnostic::error(
            "E1001",
            format!("Undefined name: {full} ({reason})"),
        ));
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
            if d <= 2 && best.as_ref().map(|(bd, _)| d < *bd).unwrap_or(true) {
                best = Some((d, cand));
            }
        }
        best.map(|(_, c)| c)
    }
}

// ---- free helpers --------------------------------------------------------

fn to_ctor_ref(ct: &crate::exports::ExportedCtor) -> CtorRef {
    CtorRef {
        def: ct.def,
        type_: ct.type_,
        index: ct.index,
        arity: ct.arity,
    }
}

fn decl_name(d: &ast::Decl) -> Option<String> {
    match d {
        ast::Decl::TypeAnno(a) => a.name().map(|t| t.text().to_string()),
        _ => None,
    }
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
