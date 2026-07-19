//! Type-directed lowering (doc 07 §2): resolved HIR + the `ty` per-expression
//! type table → the typed Go-IR. Whole-program: DCE from `main`, kernel
//! dispatch, ADT/record type-decl emission. Scoped to the M4 CLI-family subset;
//! server/TUI/webview backends are reported as out of scope.

use crate::goty::{sky_ty_to_go, sky_ty_to_go_in, Nominal, NominalKind, TypeEnv};
use crate::ir::*;
use crate::kernel::{alias_go_name, kernel_go_name};
use base::{DefId, ModuleId, Name};
use hir::{Body, CaseBranch, Expr, ExprId, ImportSource, LocalId, Pattern, PatId, Res, SkyDb};
use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};
use ty::{BodyTypes, Ty, Typer};

pub struct LowerOutput {
    pub items: Vec<GoItem>,
    pub warnings: Vec<String>,
    /// Hard lowering errors — conditions that would emit Go the toolchain
    /// rejects (e.g. a call to a Go-FFI function that has no callable wrapper).
    /// The build driver aborts before `go build` when this is non-empty, so a
    /// program that would break `go build` is rejected at check time instead
    /// (upholds the `sky check ≡ sky build` invariant).
    pub errors: Vec<String>,
    /// True when `main` was found + lowered; false → nothing to build.
    pub entry_ok: bool,
    /// Sky module paths of the Go-FFI packages actually *called* by the emitted
    /// program (doc 09) — the build driver materialises only these bindings into
    /// `sky-out/rt/`, so an added-but-unused package never inflates the build.
    pub ffi_used: BTreeSet<String>,
}

/// The pinned FFI surface, projected to exactly what lowering needs: for each
/// imported Go package (keyed by its Sky module path) the kernel prefix + the
/// set of wrapper symbols defined for it. Built by `project` from the loaded
/// `ffi::FfiRegistry`; kept free of `serde`/`ffi` so `lower` stays decoupled.
#[derive(Default, Clone)]
pub struct FfiTable {
    pub mods: BTreeMap<String, FfiModInfo>,
}

#[derive(Clone)]
pub struct FfiModInfo {
    /// The Go symbol prefix (`Go_Uuid`).
    pub kernel_name: String,
    /// The wrapper's defined `Go_*` symbols (`Go_Uuid_newStringT`, …).
    pub go_symbols: BTreeSet<String>,
    /// The wrapper's declared `FfiT_<sym>_P<i>` typed-slot aliases (only
    /// non-primitive params get one). A call-site coercion to a slot alias is
    /// emitted only when the target name is present here.
    pub ffi_slots: BTreeSet<String>,
    /// Per-wrapper-symbol ordered Go param type strings (`Go_Stripe_…T →
    /// ["int64", "*pkg.X"]`), parsed from the wrapper signatures — the
    /// authoritative REAL Go param types (`int64` where the skyType only says
    /// `Int`). Used to coerce/convert a primitive arg to the wrapper's exact Go
    /// type. Non-primitive params still route through their `FfiT_…_P<i>` slot.
    pub wrapper_params: BTreeMap<String, Vec<String>>,
}

impl FfiTable {
    /// The Go call symbol for `module.func`, preferring the typed `T` wrapper
    /// (`Go_Uuid_newStringT`) over the untyped fallback (`Go_Uuid_future`).
    /// `None` when the package isn't in the surface or defines no such symbol.
    /// Returns `(symbol, typed)`. `typed` is `true` for the `…T` wrapper (typed
    /// params, unit params elided from its Go signature) and `false` for the
    /// untyped fallback `Go_<Pkg>_<fn>(_ any)` (all params `any`, INCLUDING the
    /// unit — so unit call-args must NOT be elided for it).
    pub fn call_symbol(&self, module: &str, func: &str) -> Option<(String, bool)> {
        let m = self.mods.get(module)?;
        let base = format!("{}_{}", m.kernel_name, func);
        let typed = format!("{base}T");
        if m.go_symbols.contains(&typed) {
            Some((typed, true))
        } else if m.go_symbols.contains(&base) {
            Some((base, false))
        } else {
            None
        }
    }

    /// Whether any imported package declares the typed-slot alias `name`
    /// (`FfiT_Go_Mux_routerHandleFunc_P0`). Slot names embed the full Go symbol,
    /// so they are globally unique — a flat scan is unambiguous.
    pub fn has_ffi_slot(&self, name: &str) -> bool {
        self.mods.values().any(|m| m.ffi_slots.contains(name))
    }

    /// The ordered Go param type strings for a wrapper `symbol` (`Go_…T`).
    /// Empty when the surface carries no parsed wrapper-param info.
    pub fn wrapper_params(&self, symbol: &str) -> Vec<String> {
        self.mods
            .values()
            .find_map(|m| m.wrapper_params.get(symbol))
            .cloned()
            .unwrap_or_default()
    }
}

struct DefEntry {
    module_name: String,
    name: String,
    body: Body,
    types: BodyTypes,
    /// The declared/derived scheme type (peeled for param types), if any.
    sig: Option<Ty>,
}

/// A collected type declaration (union or record alias) awaiting emission.
struct TypeDecl {
    name: String,
    go_name: String,
    kind: TypeDeclKind,
}

enum TypeDeclKind {
    /// iota enum: nullary variants in declaration order.
    Iota(Vec<String>),
    /// sealed ADT: variants with their arg Sky types (declaration order).
    Adt(Vec<(String, Vec<Ty>)>),
    /// record alias: (field name, field Sky type) in declaration order.
    Record(Vec<(String, Ty)>),
}

/// Build-time configuration (from `sky.toml`) that drives the emitted `init()`
/// defaults — the runtime reads these via `rt.SkyDefault` (`SKY_*` fallbacks).
#[derive(Default, Clone)]
pub struct LowerConfig {
    /// `port` (default 8000 when `None`).
    pub port: Option<String>,
    /// Extra `rt.SetSkyDefault(suffix, value)` pairs — e.g. `[("DB_DRIVER",
    /// "sqlite"), ("DB_PATH", "todos.db")]` from `[database]`. Emitted after the
    /// fixed defaults so a config value wins.
    pub extra_defaults: Vec<(String, String)>,
    /// The pinned Go-FFI surface (doc 09) for this project — empty when the
    /// project imports no Go packages.
    pub ffi: FfiTable,
}

pub fn lower_program(db: &dyn SkyDb, entry: ModuleId) -> LowerOutput {
    lower_program_cfg(db, entry, &LowerConfig::default())
}

pub fn lower_program_cfg(db: &dyn SkyDb, entry: ModuleId, cfg: &LowerConfig) -> LowerOutput {
    let typer = Typer::new(db);
    let mut warnings = Vec::new();
    let mut errors: Vec<String> = Vec::new();

    // ---- collect all value defs (bodies) across modules ----
    // Name-resolution (class-a) errors are surfaced as hard errors so `sky check`
    // rejects a program with an undefined bare/qualified name BEFORE `go build`,
    // upholding `sky check ≡ sky build` (CLAUDE.md §8). Without this the resolver
    // degrades the reference to `Res::Error`, lowering silently drops it, and the
    // defect only surfaces as a `go build` failure (or, worse, passes). Deduped by
    // (qualifier, name) so a name used N times reports once (oracle: dedupeByNameTop,
    // Module.hs:1599). Class-b (Go-FFI) refs are NOT surfaced here — they resolve
    // once the FFI surface lands (doc 09).
    let mut seen_name_errors: HashSet<(Option<String>, String)> = HashSet::new();
    let mut defs: BTreeMap<DefId, DefEntry> = BTreeMap::new();
    for m in db.module_ids() {
        let mname = db.module_name(m).to_string();
        let resolved = db.resolve(m);
        for ca in &resolved.class_a {
            if seen_name_errors.insert((ca.qualifier.clone(), ca.name.clone())) {
                let full = match &ca.qualifier {
                    Some(q) => format!("{q}.{}", ca.name),
                    None => ca.name.clone(),
                };
                errors.push(format!(
                    "[E1001] Undefined name: {full} (in module {mname}) — {}",
                    ca.reason
                ));
            }
        }
        for td in &resolved.top_defs {
            if let Some(body) = resolved.bodies.get(&td.def) {
                let types = typer.body_types(td.def, body);
                let sig = typer.value_sig(td.def).map(|s| s.ty.clone());
                defs.insert(
                    td.def,
                    DefEntry {
                        module_name: mname.clone(),
                        name: td.name.as_str().to_string(),
                        body: body.clone(),
                        types,
                        sig,
                    },
                );
            }
        }
    }

    // ---- collect type declarations + the nominal name map ----
    let (nominal, nominal_by_module, type_decls) = collect_types(db);

    // record field-set → `_R` alias index (structural → nominal resolution).
    let mut record_fieldsets: HashMap<Vec<String>, String> = HashMap::new();
    for d in &type_decls {
        if let TypeDeclKind::Record(fields) = &d.kind {
            let mut names: Vec<String> = fields.iter().map(|(n, _)| n.clone()).collect();
            names.sort();
            record_fieldsets.entry(names).or_insert_with(|| d.go_name.clone());
        }
    }
    // ---- detect the app's single TEA Model ----
    // A TEA `init` returns `( Model, Cmd msg )` and CONSTRUCTS the Model as a full
    // record literal → inference gives a closed record that EXACTLY matches a
    // declared record alias. That alias is the Model; unannotated view/update/
    // helpers infer subsets of it, resolved to the nominal `_R` in `sky_ty_to_go`.
    //
    // `defs` is a `BTreeMap<DefId, _>` whose iteration order is DefId-valued;
    // since `DefId`s became content-keyed salsa-interned ids (resolve-stage port),
    // that order is no longer the insertion order. Scan candidates in a STABLE
    // `(module, name)` order so which def is picked — hence emitted Go — never
    // depends on the raw `DefId` value (the `repro` byte-stability guarantee). In
    // a well-formed single-`init` app exactly one def matches, so the order only
    // hardens the pathological multi-match case.
    let model: Option<(Vec<String>, String)> = {
        let mut candidates: Vec<&DefEntry> = defs.values().collect();
        candidates.sort_by(|a, b| {
            (a.module_name.as_str(), a.name.as_str())
                .cmp(&(b.module_name.as_str(), b.name.as_str()))
        });
        candidates.into_iter().find_map(|e| {
            let Ty::Tuple(xs) = e.types.result.as_ref()? else {
                return None;
            };
            if xs.len() != 2 {
                return None;
            }
            let Ty::Record(fields, _) = &xs[0] else {
                return None;
            };
            if !matches!(&xs[1], Ty::App(cn, _) if cn.as_str() == "Cmd") {
                return None;
            }
            let mut names: Vec<String> =
                fields.iter().map(|(n, _)| n.as_str().to_string()).collect();
            names.sort();
            record_fieldsets.get(&names).map(|go| (names.clone(), go.clone()))
        })
    };
    let env = TypeEnv {
        nominal,
        nominal_by_module,
        record_fieldsets,
        model,
    };

    // Type names declared in MORE THAN ONE module (`Msg`/`Model`/`Page` in a
    // multi-module app). A qualified type reference (`Counter.Msg`) drops its
    // qualifier at `ast_type_to_ty`, so an ambiguous name can't be resolved to
    // the right Go union — its `sky_ty_to_go` result is unreliable.
    let ambiguous_names: HashSet<String> = {
        let mut per_name: HashMap<String, HashSet<String>> = HashMap::new();
        for (module, name) in env.nominal_by_module.keys() {
            per_name.entry(name.clone()).or_default().insert(module.clone());
        }
        per_name
            .into_iter()
            .filter(|(_, mods)| mods.len() > 1)
            .map(|(n, _)| n)
            .collect()
    };
    // ADT unions emitted as sealed interfaces: an app-module ADT (see
    // `should_seal_prefix`) whose every variant field type resolves
    // UNAMBIGUOUSLY (so the typed variant struct fields + ctor params are the
    // real Go types). Ambiguous-cross-module-field unions stay on the `rt.SkyADT`
    // bag — a correctness floor, not a soundness one (both paths co-live).
    let sealed_unions: HashSet<String> = type_decls
        .iter()
        .filter_map(|d| match &d.kind {
            TypeDeclKind::Adt(variants)
                if should_seal_prefix(&d.go_name)
                    && variants
                        .iter()
                        .all(|(_, args)| args.iter().all(|t| !ty_refs_ambiguous(t, &ambiguous_names))) =>
            {
                Some(d.go_name.clone())
            }
            _ => None,
        })
        .collect();

    // record `_R` go-name → declared field (Go-name, Sky type) list, for typing
    // record-literal field values to the struct's declared field types.
    let mut record_fields: HashMap<String, Vec<(String, Ty)>> = HashMap::new();
    for d in &type_decls {
        if let TypeDeclKind::Record(fields) = &d.kind {
            record_fields.insert(
                d.go_name.clone(),
                fields.iter().map(|(n, t)| (capitalize(n), t.clone())).collect(),
            );
        }
    }

    // ctor name → (owning Go type name, kind) reverse index + declaration-order tag.
    let mut ctor_owner: HashMap<String, (String, NominalKind)> = HashMap::new();
    let mut ctor_tag: HashMap<String, usize> = HashMap::new();
    // ctor name → value-argument count (for eta-expanding a bare constructor
    // reference used as a function value — `onInput UpdateDraft` where
    // `UpdateDraft : String -> Msg`).
    let mut ctor_arity: HashMap<String, usize> = HashMap::new();
    // (owning Go type, ctor name) → (kind, declaration tag). Keyed by the OWNING
    // union so a ctor name shared across two unions (`AlignLeft` lives in both
    // `Std.Ui.HAlign` and `Std.Css.TextAlign`) resolves correctly when the
    // subject/expected type pins the union — the bare-name `ctor_owner` map
    // collides and picks whichever union interned last.
    let mut ctor_in_union: HashMap<(String, String), (NominalKind, usize)> = HashMap::new();
    // (owning Go type, ctor name) → value-argument count. Parallel to
    // `ctor_in_union`. The bare-name `ctor_arity` map collides when a ctor name
    // is shared across two nominals — most sharply when a record alias's
    // positional-ctor name equals a nullary ADT ctor (`type Tab = Overview | …`
    // + `type alias Overview = { …12 fields… }`, example 25): whichever interns
    // last overwrites, so a value-position `Overview` (the Tab ctor, arity 0)
    // wrongly reads the record's arity 12 and eta-expands into a 12-param
    // closure that then fails `rt.Coerce[Tab]` at runtime. Pin resolves the
    // arity against the SAME nominal the owner is pinned to.
    let mut ctor_arity_in_union: HashMap<(String, String), usize> = HashMap::new();
    // (owning Go type, ctor name) → the variant struct's payload Go-types (V0..Vn),
    // for sealed-ADT emission: typed constructors, typed `.V{i}` pattern reads, and
    // the wire JSON factory. Computed with the SAME `sky_ty_to_go(t, &env)` mapping
    // `emit_type_decl` uses for the struct fields, so the read type matches the
    // declared field type exactly (no coercion at the read site).
    let mut ctor_field_gotys: HashMap<(String, String), Vec<GoTy>> = HashMap::new();
    for d in &type_decls {
        match &d.kind {
            TypeDeclKind::Iota(vs) => {
                for (i, v) in vs.iter().enumerate() {
                    ctor_owner.insert(v.clone(), (d.go_name.clone(), NominalKind::Iota));
                    ctor_arity.insert(v.clone(), 0);
                    ctor_in_union
                        .insert((d.go_name.clone(), v.clone()), (NominalKind::Iota, i));
                    ctor_arity_in_union.insert((d.go_name.clone(), v.clone()), 0);
                }
            }
            TypeDeclKind::Adt(vs) => {
                for (i, (cn, args)) in vs.iter().enumerate() {
                    ctor_owner.insert(cn.clone(), (d.go_name.clone(), NominalKind::Adt));
                    ctor_tag.insert(cn.clone(), i);
                    ctor_arity.insert(cn.clone(), args.len());
                    ctor_in_union
                        .insert((d.go_name.clone(), cn.clone()), (NominalKind::Adt, i));
                    ctor_arity_in_union.insert((d.go_name.clone(), cn.clone()), args.len());
                    ctor_field_gotys.insert(
                        (d.go_name.clone(), cn.clone()),
                        args.iter().map(|t| sky_ty_to_go(t, &env)).collect(),
                    );
                }
            }
            TypeDeclKind::Record(fields) => {
                ctor_owner.insert(d.name.clone(), (d.go_name.clone(), NominalKind::Record));
                ctor_arity.insert(d.name.clone(), fields.len());
                ctor_in_union
                    .insert((d.go_name.clone(), d.name.clone()), (NominalKind::Record, 0));
                ctor_arity_in_union.insert((d.go_name.clone(), d.name.clone()), fields.len());
            }
        }
    }

    // ---- per-def inferred parameter types (for arg→param coercion) ----
    // Independent per-def inference gives a callee's params a precise type
    // (e.g. `Result Error String`) that a caller's argument (`Result any String`)
    // must be coerced to at the call site (the cross-def unification the global
    // solver would do — recovered here as an explicit boundary coercion).
    let mut def_param_tys: HashMap<DefId, Vec<Ty>> = HashMap::new();
    for (d, e) in &defs {
        // Prefer the DECLARED signature's param types (nominal, e.g.
        // `RetryPolicy e`) over the body-INFERRED local types. Independent
        // per-def inference of a record param that a body only field-updates
        // (`{ p | shouldRetry = … }`) produces a *subset* structural record
        // (`{ shouldRetry }`), which `sky_ty_to_go` renders as an anonymous
        // `struct{ ShouldRetry … }`. The caller then passes the full nominal
        // `RetryPolicy_R` and the boundary-coercion forces a
        // `rt.Coerce[struct{…}]` between a named struct and a different
        // anonymous struct — which `go build` rejects. Using the annotated
        // param type keeps both sides on the nominal `_R` type, so `from == to`
        // and the coercion elides (doc 07 §3 subset-record case).
        let sig_ptys = e.sig.as_ref().map(peel_params);
        let mut ptys = Vec::new();
        for (i, p) in e.body.params.iter().enumerate() {
            let inferred = || match &e.body.pats[*p] {
                Pattern::Var(lid) => {
                    e.types.locals.get(lid).cloned().unwrap_or(Ty::Var(Name::new("any")))
                }
                _ => Ty::Var(Name::new("any")),
            };
            // Take the sig param type when it is concrete enough to be useful
            // (not a bare type variable — a rigid `msg`/`a` gives no more than
            // the inferred type and would erase to `any` anyway).
            let t = match sig_ptys.as_ref().and_then(|ps| ps.get(i)) {
                Some(st) if !matches!(st, Ty::Var(_)) => st.clone(),
                _ => inferred(),
            };
            ptys.push(t);
        }
        def_param_tys.insert(*d, ptys);
    }
    // per-def result type — the callee's Go return type as seen by a caller (for
    // arg→param coercion of the return, partial-application closure return, and
    // Task-returning detection). MUST agree with the type `lower_def` actually
    // emits as the func's Go return: `lower_def` prefers the DECLARED sig result
    // (`sig_result_after`) when concrete over the body-inferred one. A body that
    // only field-updates its record param (`{ post | upvoters = … }`) infers a
    // *subset* open record (`{ Downvoters, Id, Upvoters | row }` → anon
    // `struct{…}`), but the func returns the annotated nominal (`State_Post_R`).
    // If `def_result_tys` reported the subset, a partial-app closure would be
    // typed `func(_p any) struct{…}` while its body returns the nominal — which
    // `go build` rejects (19-skyforum toggle map). Mirroring the sig-first rule
    // keeps both sides on the nominal `_R`. Bare-var / unannotated results fall
    // back to the body-inferred type, unchanged.
    let mut def_result_tys: HashMap<DefId, Ty> = HashMap::new();
    for (d, e) in &defs {
        let sig_ret = e
            .sig
            .as_ref()
            .map(|s| sig_result_after(s, e.body.params.len()));
        let t = match sig_ret {
            Some(st) if !matches!(st, Ty::Var(_)) => Some(st),
            _ => e.types.result.clone(),
        };
        if let Some(t) = t {
            def_result_tys.insert(*d, t);
        }
    }

    // ---- kernel-alias map (defs whose body is `Ffi.kernel "X"`) ----
    let mut kernel_alias: HashMap<DefId, String> = HashMap::new();
    for (d, e) in &defs {
        if let Some(raw) = detect_kernel_alias(&e.body) {
            kernel_alias.insert(*d, raw);
        }
    }

    // ---- find `main` in the entry module ----
    let main_def = defs.iter().find_map(|(d, e)| {
        if e.name == "main" && module_prefix(&e.module_name) == module_prefix(db.module_name(entry))
        {
            Some(*d)
        } else {
            None
        }
    });
    let Some(main_def) = main_def else {
        return LowerOutput {
            items: Vec::new(),
            warnings: vec!["no `main` in entry module".into()],
            errors: Vec::new(),
            entry_ok: false,
            ffi_used: BTreeSet::new(),
        };
    };

    // ---- DCE: lower reachable defs, discovering refs as we go ----
    let mut seen: HashSet<DefId> = HashSet::new();
    let mut work: Vec<DefId> = vec![main_def];
    let mut funcs: Vec<GoItem> = Vec::new();
    let mut used_go_types: HashSet<String> = HashSet::new();
    let mut ffi_used: BTreeSet<String> = BTreeSet::new();

    while let Some(d) = work.pop() {
        if !seen.insert(d) {
            continue;
        }
        let Some(e) = defs.get(&d) else {
            continue; // an aliased/foreign def with no body — skip
        };
        if kernel_alias.contains_key(&d) && d != main_def {
            // kernel-alias def called directly is inlined at the call site; only
            // a value-reference would need a wrapper (tracked when it happens).
            continue;
        }
        let mut cx = Ctx {
            db,
            defs: &defs,
            kernel_alias: &kernel_alias,
            env: &env,
            record_fields: &record_fields,
            ctor_owner: &ctor_owner,
            ctor_tag: &ctor_tag,
            ctor_arity: &ctor_arity,
            ctor_in_union: &ctor_in_union,
            ctor_arity_in_union: &ctor_arity_in_union,
            ctor_field_gotys: &ctor_field_gotys,
            sealed_unions: &sealed_unions,
            def_param_tys: &def_param_tys,
            def_result_tys: &def_result_tys,
            body: &e.body,
            types: &e.types,
            local_names: HashMap::new(),
            local_tys: HashMap::new(),
            local_counter: 0,
            discovered: Vec::new(),
            used_types: HashSet::new(),
            warnings: Vec::new(),
            errors: Vec::new(),
            closure_elem: None,
            ffi: &cfg.ffi,
            ffi_used: BTreeSet::new(),
            cur_module: e.module_name.clone(),
        };
        let item = cx.lower_def(&e.name, &e.module_name, e.sig.as_ref(), d == main_def);
        for r in cx.discovered {
            if !seen.contains(&r) {
                work.push(r);
            }
        }
        for t in cx.used_types {
            used_go_types.insert(t);
        }
        for m in cx.ffi_used {
            ffi_used.insert(m);
        }
        warnings.extend(cx.warnings);
        errors.extend(cx.errors);
        funcs.push(item);
    }

    // ---- type-decl reachability: BFS over Go type names used in emitted code ----
    let type_by_go: HashMap<String, &TypeDecl> =
        type_decls.iter().map(|t| (t.go_name.clone(), t)).collect();
    let mut type_items: Vec<(String, Vec<GoItem>)> = Vec::new();
    let mut seen_types: HashSet<String> = HashSet::new();
    let mut tqueue: Vec<String> = used_go_types.into_iter().collect();
    tqueue.sort();
    while let Some(gn) = tqueue.pop() {
        if !seen_types.insert(gn.clone()) {
            continue;
        }
        // record type names carry the `_R` suffix in Go usage.
        let decl = type_by_go.get(&gn).or_else(|| type_by_go.get(gn.trim_end_matches("_R")));
        if let Some(decl) = decl {
            let (items, more) = emit_type_decl(decl, &env, &sealed_unions);
            for m in more {
                if !seen_types.contains(&m) {
                    tqueue.push(m);
                }
            }
            type_items.push((decl.go_name.clone(), items));
        }
    }
    // deterministic: sort type groups by module then name (declaration proxy).
    type_items.sort_by(|a, b| a.0.cmp(&b.0));

    // ---- assemble the module ----
    let mut items = Vec::new();
    items.push(prologue_init(cfg));
    for (_, group) in type_items {
        items.extend(group);
    }
    items.extend(funcs);

    LowerOutput {
        items,
        warnings,
        errors,
        entry_ok: true,
        ffi_used,
    }
}

/// The bootstrap `init()` every emitted program opens with (doc 08 §3).
/// Config-driven defaults from `sky.toml` (`[database]` path/driver, `port`) are
/// appended so `Db.connect ()` etc. resolve the same `SKY_*` fallbacks the
/// oracle sets.
fn prologue_init(cfg: &LowerConfig) -> GoItem {
    let call = |name: &str, args: &[&str]| {
        GoStmt::Expr(GoExpr::new(
            GoExprKind::Call(
                Box::new(GoExpr::new(GoExprKind::Ident(name.into()), GoTy::Any)),
                args.iter()
                    .map(|a| GoExpr::new(GoExprKind::StrLit((*a).into()), GoTy::Bare(Prim::Str)))
                    .collect(),
            ),
            GoTy::Unit,
        ))
    };
    let port = cfg.port.clone().unwrap_or_else(|| "8000".to_string());
    let mut stmts = vec![
        call("rt.SetPortDefault", &[&port]),
        call("rt.SetSkyDefault", &["LIVE_TTL", "1800"]),
        call("rt.SetSkyDefault", &["AUTH_TOKEN_TTL", "86400"]),
        call("rt.SetSkyDefault", &["AUTH_COOKIE", "sky_auth"]),
        call("rt.SetSkyDefault", &["AUTH_DRIVER", "jwt"]),
    ];
    for (suffix, value) in &cfg.extra_defaults {
        stmts.push(call("rt.SetSkyDefault", &[suffix, value]));
    }
    GoItem::Init(stmts)
}

// ---- module / name mangling (doc 08 §5) --------------------------------

fn module_prefix(module: &str) -> String {
    module.replace('.', "_")
}

fn top_go_name(module: &str, name: &str) -> String {
    format!("{}_{}", module_prefix(module), reserved_rewrite(name))
}

const RESERVED: &[&str] = &[
    "init", "string", "error", "any", "bool", "byte", "rune", "int", "int8", "int16", "int32",
    "int64", "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64", "true", "false",
    "nil", "iota", "len", "cap", "make", "new", "append", "copy", "delete", "panic", "recover",
    "print", "println", "close", "min", "max", "complex", "real", "imag", "clear", "for", "func",
    "type", "range", "return", "if", "else", "switch", "case", "default", "var", "const", "map",
    "struct", "interface", "chan", "go", "select", "package", "import", "goto", "break", "continue",
    "fallthrough", "defer",
];

fn reserved_rewrite(name: &str) -> String {
    if RESERVED.contains(&name) {
        format!("{name}_")
    } else {
        name.to_string()
    }
}

// ---- kernel-alias detection --------------------------------------------

/// Detect a `name = Ffi.kernel "Raw"` body — the resolved shape is
/// `Call(Var(Res::Kernel{func:"kernel"}), [Str raw])`.
fn detect_kernel_alias(body: &Body) -> Option<String> {
    let root = body.root?;
    if let Expr::Call(callee, args) = &body.exprs[root] {
        if args.len() == 1 {
            if let Expr::Var(Res::Kernel { func, .. }) = &body.exprs[*callee] {
                if func.as_str() == "kernel" {
                    if let Expr::Str(raw) = &body.exprs[args[0]] {
                        return Some(raw.to_string());
                    }
                }
            }
        }
    }
    None
}

// ---- type declaration collection ---------------------------------------

/// Build a `qualifier → declaring-module-name` map for module `m`'s imports.
/// The qualifier is the explicit `as`-alias if present, else the import path's
/// final segment (the default qualifier). Only parsed-module (`Dep`) imports are
/// mapped — kernel / FFI qualifiers are resolved elsewhere in `sky_ty_to_go`.
fn import_module_map(db: &dyn SkyDb, m: base::ModuleId) -> HashMap<String, String> {
    let mut map = HashMap::new();
    let tree = db.module_parse(m).tree();
    for imp in tree.imports() {
        let Some(path) = imp.name().map(|n| n.text()) else {
            continue;
        };
        if let ImportSource::Dep(dep) = db.classify_import(&path) {
            let modname = db.module_name(dep).to_string();
            let qual = imp
                .alias()
                .map(|t| t.text().to_string())
                .unwrap_or_else(|| path.rsplit('.').next().unwrap_or(&path).to_string());
            map.insert(qual, modname);
        }
    }
    map
}

/// Rewrite a qualified `Ty::App` name's qualifier (`Counter.Msg`, or an alias
/// `C.Msg`) to the full declaring module name (`Counter.Msg`) via `map`, keyed
/// by the type's final segment. Bare names and unmapped qualifiers pass through
/// unchanged.
fn requalify_ty(t: &Ty, map: &HashMap<String, String>) -> Ty {
    match t {
        Ty::App(name, args) => {
            let args: Vec<Ty> = args.iter().map(|a| requalify_ty(a, map)).collect();
            let n = name.as_str();
            let new_name = match n.rsplit_once('.') {
                Some((qual, tail)) => match map.get(qual) {
                    Some(modname) => format!("{modname}.{tail}"),
                    None => n.to_string(),
                },
                None => n.to_string(),
            };
            Ty::App(base::Name::new(&new_name), args)
        }
        Ty::Fun(a, b) => Ty::Fun(
            Box::new(requalify_ty(a, map)),
            Box::new(requalify_ty(b, map)),
        ),
        Ty::Tuple(xs) => Ty::Tuple(xs.iter().map(|x| requalify_ty(x, map)).collect()),
        Ty::Record(fs, ext) => Ty::Record(
            fs.iter()
                .map(|(n, x)| (n.clone(), requalify_ty(x, map)))
                .collect(),
            ext.clone(),
        ),
        other => other.clone(),
    }
}

#[allow(clippy::type_complexity)]
fn collect_types(
    db: &dyn SkyDb,
) -> (
    HashMap<String, Nominal>,
    HashMap<(String, String), Nominal>,
    Vec<TypeDecl>,
) {
    use syntax::ast::{self, AstNode};
    let mut nominal: HashMap<String, Nominal> = HashMap::new();
    let mut nominal_by_module: HashMap<(String, String), Nominal> = HashMap::new();
    let mut decls: Vec<TypeDecl> = Vec::new();
    // Transparent-alias expander: a record field annotated `List Point` (where
    // `type alias Point = (Float, Float)`) must expand `Point` to the tuple so the
    // struct-field decl agrees with the pre-expanded function signatures. Without
    // this the field erases to `[]any` while the param renders `[]rt.T2[…]` — the
    // same Sky type, two incompatible Go types (26/37).
    let world = ty::World::build(db);
    for m in db.module_ids() {
        let mname = db.module_name(m).to_string();
        let prefix = module_prefix(&mname);
        // Per-module qualifier → declaring-module-name map, so a variant field
        // written `Counter.Msg` (or an aliased `import X as C` → `C.Msg`) can be
        // requalified to the FULL declaring module name that `nominal_by_module`
        // is keyed by. Bare (unqualified) references are untouched.
        let requal = import_module_map(db, m);
        // register a nominal under both the flat map (last-writer) and the
        // module-scoped map (never collides across modules).
        macro_rules! reg {
            ($tname:expr, $nom:expr) => {{
                let nom: Nominal = $nom;
                nominal_by_module.insert((mname.clone(), $tname.clone()), nom.clone());
                nominal.insert($tname.clone(), nom);
            }};
        }
        let tree = db.module_parse(m).tree();
        for decl in tree.decls() {
            match &decl {
                ast::Decl::Union(u) => {
                    let Some(tname) = u.name().map(|t| t.text().to_string()) else {
                        continue;
                    };
                    let mut variants: Vec<(String, Vec<Ty>)> = Vec::new();
                    let mut all_nullary = true;
                    for var in u.variants() {
                        let Some(cn) = var.name().map(|t| t.text().to_string()) else {
                            continue;
                        };
                        // Qualifier-preserving extraction + requalify to the full
                        // declaring module name, so a cross-module variant field
                        // (`CounterMsg Counter.Msg`) resolves to `Counter_Msg` in
                        // `sky_ty_to_go`, distinct from a same-module `Msg`.
                        let arg_tys: Vec<Ty> = ty::variant_arg_types_qualified(var.syntax())
                            .iter()
                            .map(|t| requalify_ty(t, &requal))
                            .collect();
                        if !arg_tys.is_empty() {
                            all_nullary = false;
                        }
                        variants.push((cn, arg_tys));
                    }
                    let go_name = format!("{prefix}_{tname}");
                    let kind = if all_nullary {
                        // Phantom opaque-handle detection: a single-variant
                        // iota enum whose sole constructor is `<Name>_OPAQUE`
                        // (stdlib convention for `Route`/`Server`/`Cookie`).
                        // Its runtime value is a kernel struct handle, so it
                        // resolves to `any` in `sky_ty_to_go` — never the `int`
                        // the placeholder decl aliases.
                        let opaque = variants.len() == 1
                            && variants[0].0.ends_with("_OPAQUE");
                        reg!(
                            tname,
                            Nominal {
                                go_name: go_name.clone(),
                                kind: NominalKind::Iota,
                                opaque,
                            }
                        );
                        TypeDeclKind::Iota(variants.into_iter().map(|(n, _)| n).collect())
                    } else {
                        reg!(
                            tname,
                            Nominal {
                                go_name: go_name.clone(),
                                kind: NominalKind::Adt,
                                opaque: false,
                            }
                        );
                        TypeDeclKind::Adt(variants)
                    };
                    decls.push(TypeDecl {
                        name: tname,
                        go_name,
                        kind,
                    });
                }
                ast::Decl::Alias(a) => {
                    if let (Some(tname), Some(ast::Type::Record(_))) =
                        (a.name().map(|t| t.text().to_string()), a.ty())
                    {
                        let fields: Vec<(String, Ty)> = ty::record_alias_fields(a.syntax())
                            .into_iter()
                            .map(|(n, t)| (n, world.expand_ty(&t)))
                            .collect();
                        let go_name = format!("{prefix}_{tname}_R");
                        reg!(
                            tname,
                            Nominal {
                                go_name: go_name.clone(),
                                kind: NominalKind::Record,
                                opaque: false,
                            }
                        );
                        decls.push(TypeDecl {
                            name: tname,
                            go_name,
                            kind: TypeDeclKind::Record(fields),
                        });
                    }
                }
                _ => {}
            }
        }
    }
    (nominal, nominal_by_module, decls)
}

/// Emit a type declaration's Go items; return the further Go type names its
/// fields/variants reference (for BFS reachability).
fn emit_type_decl(
    decl: &TypeDecl,
    env: &TypeEnv,
    sealed_unions: &HashSet<String>,
) -> (Vec<GoItem>, Vec<String>) {
    let mut more: Vec<String> = Vec::new();
    let mut collect = |t: &Ty| {
        let gt = sky_ty_to_go(t, env);
        collect_named(&gt, &mut more);
        gt
    };
    match &decl.kind {
        TypeDeclKind::Iota(variants) => (
            vec![GoItem::Type(
                decl.go_name.clone(),
                GoTypeDef::IotaEnum(variants.clone()),
            )],
            more,
        ),
        TypeDeclKind::Record(fields) => {
            let go_fields: Vec<(String, GoTy)> = fields
                .iter()
                .map(|(n, t)| (capitalize(n), collect(t)))
                .collect();
            let mut items = vec![GoItem::Type(
                decl.go_name.clone(),
                GoTypeDef::Struct(go_fields.clone()),
            )];
            // gob registration
            items.push(GoItem::Raw(format!(
                "func init() {{ rt.RegisterGobType({}{{}}) }}",
                decl.go_name
            )));
            // positional constructor `Prefix_Name(p0, …) Prefix_Name_R`
            let ctor_name = decl.go_name.trim_end_matches("_R").to_string();
            let params: Vec<String> = go_fields
                .iter()
                .enumerate()
                .map(|(i, (_, t))| format!("p{i} {}", render_goty(t)))
                .collect();
            let assigns: Vec<String> = go_fields
                .iter()
                .enumerate()
                .map(|(i, (f, _))| format!("{f}: p{i}"))
                .collect();
            items.push(GoItem::Raw(format!(
                "func {ctor_name}({}) {} {{ return {}{{{}}} }}",
                params.join(", "),
                decl.go_name,
                decl.go_name,
                assigns.join(", ")
            )));
            (items, more)
        }
        TypeDeclKind::Adt(variants) if sealed_unions.contains(&decl.go_name) => {
            // Sealed-interface emission: `type Name interface {…}` + one typed
            // `Name_<Ctor>_V` struct per variant, typed constructors returning the
            // interface, and an init() block registering the tag, the gob type
            // (unconditionally — the session-store value-walker misses absent
            // variants), and the wire JSON factory.
            let mut variant_defs: Vec<(String, usize, Vec<GoTy>)> = Vec::new();
            let mut items: Vec<GoItem> = Vec::new();
            let mut reg = String::from("func init() { ");
            for (i, (cn, args)) in variants.iter().enumerate() {
                let ftys: Vec<GoTy> = args.iter().map(&mut collect).collect();
                let vstruct = format!("{}_{}_V", decl.go_name, cn);
                variant_defs.push((cn.clone(), i, ftys.clone()));
                // typed constructor → returns the sealed interface (struct auto-boxes).
                let params: Vec<String> = ftys
                    .iter()
                    .enumerate()
                    .map(|(j, t)| format!("v{j} {}", render_goty(t)))
                    .collect();
                let assigns: Vec<String> =
                    (0..args.len()).map(|j| format!("V{j}: v{j}")).collect();
                items.push(GoItem::Raw(format!(
                    "func {}_{}({}) {} {{ return {}{{{}}} }}",
                    decl.go_name,
                    cn,
                    params.join(", "),
                    decl.go_name,
                    vstruct,
                    assigns.join(", ")
                )));
                reg.push_str(&format!("rt.RegisterAdtTag(\"{cn}\", {i}); "));
                reg.push_str(&format!(
                    "rt.RegisterMsgVariant(\"{}\", \"{cn}\", {i}, {}); ",
                    decl.go_name,
                    args.len()
                ));
                reg.push_str(&format!("rt.GobRegister({vstruct}{{}}); "));
                // wire JSON factory: decode each raw arg into its typed field.
                let mut fbody = String::new();
                for (j, t) in ftys.iter().enumerate() {
                    fbody.push_str(&format!(
                        "var v{j} {}; if len(raw) >= {} {{ _ = rt.JsonUnmarshal(raw[{j}], &v{j}) }}; ",
                        render_goty(t),
                        j + 1
                    ));
                }
                let fassigns: Vec<String> =
                    (0..args.len()).map(|j| format!("V{j}: v{j}")).collect();
                reg.push_str(&format!(
                    "rt.RegisterAdtVariant(\"{cn}\", func(raw []rt.JsonRawMessage) any {{ {fbody}return {vstruct}{{{}}} }}); ",
                    fassigns.join(", ")
                ));
            }
            reg.push('}');
            items.insert(
                0,
                GoItem::Type(decl.go_name.clone(), GoTypeDef::SealedIface(variant_defs)),
            );
            items.push(GoItem::Raw(reg));
            (items, more)
        }
        TypeDeclKind::Adt(variants) => {
            let mut items = vec![GoItem::Type(decl.go_name.clone(), GoTypeDef::AdtAlias)];
            let mut reg = String::from("func init() { ");
            for (i, (cn, args)) in variants.iter().enumerate() {
                // ADT payloads are stored in the untyped `Fields []any` bag, so
                // the constructor params are `any` — call sites pass already-
                // widened args (a typed param would force each caller to narrow
                // to the exact variant arg type, e.g. `Claims []T2` fed a
                // `rt.Concat` result of type `any`). `collect(t)` still runs for
                // its reachability side effect on the arg types.
                let params: Vec<String> = args
                    .iter()
                    .enumerate()
                    .map(|(j, t)| {
                        let _ = collect(t);
                        format!("v{j} any")
                    })
                    .collect();
                let fields: Vec<String> = (0..args.len()).map(|j| format!("v{j}")).collect();
                // constructor
                items.push(GoItem::Raw(format!(
                    "func {}_{}({}) {} {{ return {}{{Tag: {i}, SkyName: \"{cn}\", Fields: []any{{{}}}}} }}",
                    decl.go_name,
                    cn,
                    params.join(", "),
                    decl.go_name,
                    decl.go_name,
                    fields.join(", ")
                )));
                reg.push_str(&format!("rt.RegisterAdtTag(\"{cn}\", {i}); "));
                reg.push_str(&format!(
                    "rt.RegisterMsgVariant(\"{}\", \"{cn}\", {i}, {}); ",
                    decl.go_name,
                    args.len()
                ));
            }
            reg.push('}');
            items.push(GoItem::Raw(reg));
            (items, more)
        }
    }
}

fn collect_named(t: &GoTy, out: &mut Vec<String>) {
    match t {
        GoTy::Named(n, args) => {
            if !n.starts_with("rt.") {
                out.push(n.clone());
            }
            for a in args {
                collect_named(a, out);
            }
        }
        GoTy::Slice(e) => collect_named(e, out),
        GoTy::Map(k, v) => {
            collect_named(k, out);
            collect_named(v, out);
        }
        GoTy::Func(ps, r) => {
            for p in ps {
                collect_named(p, out);
            }
            collect_named(r, out);
        }
        GoTy::Tuple(xs) => {
            for x in xs {
                collect_named(x, out);
            }
        }
        GoTy::Struct(fs) => {
            for (_, ft) in fs {
                collect_named(ft, out);
            }
        }
        _ => {}
    }
}

fn capitalize(s: &str) -> String {
    let mut cs = s.chars();
    match cs.next() {
        Some(c) => c.to_uppercase().collect::<String>() + cs.as_str(),
        None => String::new(),
    }
}

/// Whether an ADT union (by its Go name) is a candidate for sealed-interface
/// emission — an app-module (non-stdlib) ADT.
///
/// STDLIB ADTs are kept on the bag: the runtime itself constructs many of them
/// directly as `rt.SkyADT` values (`Sky.Core.Error`, `Std.Money`, `Std.Ui`
/// attributes, `Std.Db` SqlField, retry policies, …) and `rt.SkyADT` does NOT
/// implement the `SkyVariant` interface — flipping those to interfaces would make
/// rt-produced values fail the user-side variant type-assert. USER (app-module)
/// ADTs are only ever constructed by the emitted typed constructor and consumed
/// by emitted pattern matches + the sealed-aware runtime paths (session-store gob
/// via `GobRegister`, wire dispatch via `RegisterAdtVariant`, `HtmlToVNode` /
/// msg-logging via `unwrapADTShape`/`SkyVariant`), so they migrate cleanly.
///
/// The final sealing decision (see `sealed_unions`) further requires every
/// variant field type to resolve unambiguously — a qualified cross-module type
/// reference (`Counter.Msg`) drops its qualifier at `ast_type_to_ty`, so an
/// ambiguous name can't be pinned to the right Go type.
fn should_seal_prefix(go_name: &str) -> bool {
    !(go_name.starts_with("Sky_Core_")
        || go_name.starts_with("Std_")
        || go_name.starts_with("Sky_Http_"))
}

/// Whether a Sky type references any nominal name that is declared in more than
/// one module (`ambiguous`) — structurally, at any depth. Such a reference can't
/// be resolved to a single Go type after the qualifier is dropped, so a union
/// carrying it as a variant field is NOT sealed.
fn ty_refs_ambiguous(t: &Ty, ambiguous: &HashSet<String>) -> bool {
    match t {
        Ty::App(n, args) => {
            ambiguous.contains(n.as_str())
                || args.iter().any(|a| ty_refs_ambiguous(a, ambiguous))
        }
        Ty::Fun(a, b) => ty_refs_ambiguous(a, ambiguous) || ty_refs_ambiguous(b, ambiguous),
        Ty::Tuple(xs) => xs.iter().any(|x| ty_refs_ambiguous(x, ambiguous)),
        Ty::Record(fs, _) => fs.iter().any(|(_, x)| ty_refs_ambiguous(x, ambiguous)),
        _ => false,
    }
}

// ---- per-def lowering context ------------------------------------------

struct Ctx<'a> {
    db: &'a dyn SkyDb,
    defs: &'a BTreeMap<DefId, DefEntry>,
    kernel_alias: &'a HashMap<DefId, String>,
    env: &'a TypeEnv,
    record_fields: &'a HashMap<String, Vec<(String, Ty)>>,
    ctor_owner: &'a HashMap<String, (String, NominalKind)>,
    ctor_tag: &'a HashMap<String, usize>,
    ctor_arity: &'a HashMap<String, usize>,
    ctor_in_union: &'a HashMap<(String, String), (NominalKind, usize)>,
    ctor_arity_in_union: &'a HashMap<(String, String), usize>,
    /// (owning Go type, ctor) → variant payload Go-types. Present for ADT ctors.
    ctor_field_gotys: &'a HashMap<(String, String), Vec<GoTy>>,
    /// Go names of ADT unions emitted as sealed interfaces (typed variant dispatch).
    sealed_unions: &'a HashSet<String>,
    def_param_tys: &'a HashMap<DefId, Vec<Ty>>,
    def_result_tys: &'a HashMap<DefId, Ty>,
    body: &'a Body,
    types: &'a BodyTypes,
    local_names: HashMap<LocalId, String>,
    /// The *declared* Go type of each bound local (params from the sig,
    /// let-bindings from the RHS). A local reference uses this rather than the
    /// caller's "expected" slot type, so e.g. a `RetryPolicy_R` param stays
    /// nominal instead of collapsing to a body-inferred subset record.
    local_tys: HashMap<LocalId, GoTy>,
    local_counter: u32,
    discovered: Vec<DefId>,
    used_types: HashSet<String>,
    warnings: Vec<String>,
    /// Hard lowering errors (see [`LowerOutput::errors`]).
    errors: Vec<String>,
    /// The pinned FFI surface (read-only) + the modules actually called here.
    ffi: &'a FfiTable,
    ffi_used: BTreeSet<String>,
    /// The module the def currently being lowered belongs to — disambiguates a
    /// nominal type name (`Msg`/`Model`) declared in more than one module.
    cur_module: String,
    /// The Go element type of the list a HIGHER-ORDER combinator closure is being
    /// applied to (`List.map (\x -> …) xs` → element type of `xs`), set while the
    /// closure arg is lowered. A closure param whose body-inferred type collapsed to
    /// an anonymous subset `struct{…}` is pinned to this instead — the honest
    /// runtime element (a nominal `_R`, or `any` for an erased list). `None`
    /// outside a combinator-closure arg. See `lower_lambda` + `lower_call`.
    closure_elem: Option<GoTy>,
}

impl<'a> Ctx<'a> {
    fn goty(&mut self, t: &Ty) -> GoTy {
        let m = self.cur_module.clone();
        self.goty_in(t, &m)
    }

    /// Like `goty`, but resolves nominal names in `module`'s scope. Used to lower
    /// a CALLEE's declared param/result types (which live in the callee's module,
    /// not the caller's) so a cross-module `Msg`/`Model` resolves to the callee's
    /// own type, and the arg coercion targets the right nominal.
    fn goty_in(&mut self, t: &Ty, module: &str) -> GoTy {
        let gt = sky_ty_to_go_in(t, self.env, Some(module));
        let mut names = Vec::new();
        collect_named(&gt, &mut names);
        for n in names {
            self.used_types.insert(n);
        }
        gt
    }

    fn expr_ty(&mut self, e: ExprId) -> GoTy {
        match self.types.exprs.get(&e).cloned() {
            Some(t) => self.goty(&t),
            None => GoTy::Any,
        }
    }

    /// The recorded Sky type of an expression node, resolving a `Var(Local)`
    /// through the per-local type table when the expr node itself carries no
    /// recorded type (a bare local reference often does not).
    fn sky_ty_of(&self, e: ExprId) -> Option<Ty> {
        if let Some(t) = self.types.exprs.get(&e) {
            return Some(t.clone());
        }
        if let Expr::Var(Res::Local(l)) = &self.body.exprs[e] {
            return self.types.locals.get(l).cloned();
        }
        None
    }

    /// A `Dict.toList` on a `Dict Int v` / `Dict Float v` must lower to the
    /// typed-key kernel entry point (`rt.Dict_toListIntKey` /
    /// `rt.Dict_toListFloatKey`) — the underlying runtime map is `map[string]V`,
    /// so the default `rt.Dict_toList` leaks stringified keys and any downstream
    /// `rt.AsInt` on a key panics with TypeMismatch (Limitation #10). The key
    /// type is read from the argument's HM-inferred `Dict k v` shape at the call
    /// site (oracle: `rt.Dict_toListIntKey(byCounts)` vs
    /// `rt.Dict_toList(rt.AsMapAny(totals))`).
    fn dict_tolist_specialised(&self, base: &str, args: &[ExprId]) -> Option<&'static str> {
        if base != "rt.Dict_toList" || args.len() != 1 {
            return None;
        }
        match self.sky_ty_of(args[0])? {
            Ty::App(dict, dargs) if dict.as_str() == "Dict" && dargs.len() == 2 => {
                match &dargs[0] {
                    Ty::App(k, ka) if ka.is_empty() && k.as_str() == "Int" => {
                        Some("rt.Dict_toListIntKey")
                    }
                    Ty::App(k, ka) if ka.is_empty() && k.as_str() == "Float" => {
                        Some("rt.Dict_toListFloatKey")
                    }
                    _ => None,
                }
            }
            _ => None,
        }
    }

    /// Is `e` a Task-typed expression? Checks the recorded type first, then —
    /// for a call to an unannotated def whose result the caller inferred as a
    /// flex var — the callee's own inferred result type.
    fn expr_is_task(&self, e: ExprId) -> bool {
        if let Some(Ty::App(n, _)) = self.types.exprs.get(&e) {
            if n.as_str() == "Task" {
                return true;
            }
        }
        if let Expr::Call(callee, _) = &self.body.exprs[e] {
            if let Expr::Var(Res::Def(d)) = &self.body.exprs[*callee] {
                if let Some(Ty::App(n, _)) = self.def_result_tys.get(d) {
                    return n.as_str() == "Task";
                }
            }
        }
        false
    }

    fn local_ty(&mut self, l: LocalId) -> GoTy {
        match self.types.locals.get(&l).cloned() {
            Some(t) => self.goty(&t),
            None => GoTy::Any,
        }
    }

    fn lower_def(
        &mut self,
        name: &str,
        module: &str,
        sig: Option<&Ty>,
        is_main: bool,
    ) -> GoItem {
        // bind params
        let param_pats: Vec<PatId> = self.body.params.clone();
        let mut params = Vec::new();
        let mut param_destructure: Vec<GoStmt> = Vec::new();
        let sig_params = sig.map(peel_params).unwrap_or_default();
        for (i, p) in param_pats.iter().enumerate() {
            let (pname, pty, binds) = self.bind_param(*p, sig_params.get(i));
            params.push(GoParam { name: pname, ty: pty });
            param_destructure.extend(binds);
        }

        if is_main {
            return self.lower_main(name, module);
        }

        let go_name = top_go_name(module, name);
        // Prefer the DECLARED signature's return type over the body-inferred
        // result. `debugShow : a -> String` whose body is `errorToString v`
        // infers `any` under independent per-def inference (errorToString's
        // cross-module sig isn't threaded), so the Go func would return `any`
        // and a caller's `"…" + debugShow x` would fail `go build`. The sig
        // says `String`; the body is coerced to it. Peel exactly as many arrows
        // as the def has value params so a partially-applied sig
        // (`f : A -> B -> C` with `f x = \y -> …`) still returns the right
        // (function) type. Only used when concrete (a bare type variable adds
        // nothing over the inferred type and erases to `any` anyway).
        let sig_ret = sig.map(|s| sig_result_after(s, param_pats.len()));
        // A CONCRETE declared/inferred return type — used both as the body's
        // expected slot and the emitted Go return type. `None` when neither the
        // sig nor per-def inference pinned it to something better than `any`.
        let declared_ret: Option<GoTy> = match sig_ret {
            Some(t) if !matches!(t, Ty::Var(_)) => Some(self.goty(&t)),
            _ => match &self.types.result {
                Some(t) => {
                    let gt = self.goty(t);
                    (gt != GoTy::Any).then_some(gt)
                }
                None => Some(GoTy::Unit),
            },
        };
        let root = self.body.root;
        let mut body = param_destructure;
        // When the return type is otherwise `any`, recover it from the body's own
        // lowered actual type. An unannotated def whose body is e.g. a `case`
        // yielding `rt.SkyResult[…]` must emit that concrete return type — a bare
        // `any` breaks a caller that case-analyses the result (`_subj.Tag` on
        // `any`, doc 07 §2). A concrete return is strictly more precise: it stays
        // assignable to any caller slot that expected `any`.
        let ret_ty = match root {
            Some(r) => {
                let expected = declared_ret.clone().unwrap_or(GoTy::Any);
                let e = self.lower_expr(r, &expected);
                let bt = e.ty.clone();
                body.push(GoStmt::Return(Some(e)));
                declared_ret.unwrap_or(bt)
            }
            None => declared_ret.unwrap_or(GoTy::Unit),
        };
        GoItem::Func(GoFuncDecl {
            name: go_name,
            type_params: Vec::new(),
            params,
            ret: ret_ty,
            body,
            doc: None,
        })
    }

    /// Bind a function parameter. Returns the Go param name, its Go type, and
    /// any destructuring statements to prepend to the body (for constructor /
    /// tuple / record patterns like `amount (Money d _) = d`).
    fn bind_param(&mut self, p: PatId, sig_ty: Option<&Ty>) -> (String, GoTy, Vec<GoStmt>) {
        match &self.body.pats[p] {
            Pattern::Var(id) => {
                let id = *id;
                let name = self.fresh_local_named(id, None);
                let ty = match sig_ty {
                    Some(t) => self.goty(t),
                    None => self.local_ty(id),
                };
                self.local_tys.insert(id, ty.clone());
                (name, ty, vec![])
            }
            Pattern::Anything | Pattern::Unit => {
                let ty = sig_ty.map(|t| self.goty(t)).unwrap_or(GoTy::Any);
                ("_".to_string(), ty, vec![])
            }
            _ => {
                // Destructured param (`amount (Money d _) = …`): bind the value
                // to a temp, then reuse the pattern-match binder to emit the
                // inner variable bindings at the top of the body. The test
                // condition is discarded — a function param is an irrefutable
                // (single-constructor / tuple / record) binding.
                let n = self.fresh_temp();
                let ty = sig_ty.map(|t| self.goty(t)).unwrap_or(GoTy::Any);
                let subj = GoExpr::new(GoExprKind::Ident(n.clone()), ty.clone());
                let (_cond, binds) = self.pattern_test(&subj, &ty, p);
                (n, ty, binds)
            }
        }
    }

    fn fresh_temp(&mut self) -> String {
        let n = format!("_t{}", self.local_counter);
        self.local_counter += 1;
        n
    }

    fn fresh_local_named(&mut self, id: LocalId, hint: Option<&str>) -> String {
        let base = hint.unwrap_or("v");
        // Uniquify by the (body-unique) LocalId so shadowing / same-named binders
        // across arms don't collide in a single Go block (Go forbids redeclare).
        let name = if base == "_" {
            "_".to_string()
        } else {
            format!("{}_{}", reserved_rewrite(base), id.0)
        };
        self.local_names.insert(id, name.clone());
        name
    }

    fn lower_main(&mut self, _name: &str, _module: &str) -> GoItem {
        let mut stmts = vec![GoStmt::Expr(GoExpr::new(
            GoExprKind::Ident("defer rt.LogPanicAndExit()".into()),
            GoTy::Unit,
        ))];
        let root = self.body.root;
        if let Some(r) = root {
            self.lower_main_body(r, &mut stmts);
        }
        GoItem::Func(GoFuncDecl {
            name: "main".into(),
            type_params: Vec::new(),
            params: Vec::new(),
            ret: GoTy::Unit,
            body: stmts,
            doc: Some("SKY-ORIGIN: entry".into()),
        })
    }

    /// Lower `main`'s body: a `let … in e` becomes stmts + a forced final; a bare
    /// expr is force-run (the runtime auto-forces a Task-typed entry, doc 08 §3).
    fn lower_main_body(&mut self, e: ExprId, out: &mut Vec<GoStmt>) {
        if let Expr::Let { defs, body } = &self.body.exprs[e] {
            let defs = defs.clone();
            let body = *body;
            for d in &defs {
                self.lower_let_def(d, out);
            }
            self.lower_main_body(body, out);
        } else {
            let ty = self.types.exprs.get(&e).cloned();
            let is_task = self.expr_is_task(e);
            // A Unit `in ()` has already run its effects via the let-discards; a
            // non-Unit entry is force-run (runtime auto-forces a Task-typed main).
            let is_unit_only = matches!(&ty, Some(Ty::Unit)) && !is_task;
            let lowered = self.lower_expr(e, &GoTy::Any);
            if is_unit_only {
                out.push(GoStmt::Discard(lowered));
            } else {
                out.push(GoStmt::Discard(any_task_run(lowered)));
            }
        }
    }

    fn lower_let_def(&mut self, d: &hir::LocalDef, out: &mut Vec<GoStmt>) {
        let is_task = self.expr_is_task(d.body);
        // A destructuring let (`(gMin, gMax) = heatmapRange grid`,
        // `{ x, y } = …`, `(Money d _) = …`): bind the RHS to a temp, then reuse
        // `pattern_test` to emit each inner binder assignment. Without this the
        // whole def collapsed to a `_ = rhs` discard and every destructured
        // binder referenced later stayed an undefined `v_N` (examples 26/37).
        if d.params.is_empty() {
            if let Some(pat) = d.pat {
                // Only genuine destructuring patterns route here. A `Var` is an
                // ordinary `name = expr` (handled below); `Anything`/`Unit` are
                // `let _ = TaskExpr` discards that MUST keep the auto-force
                // (`AnyTaskRun`) path so their side effects fire (doc 08 §3).
                if matches!(
                    &self.body.pats[pat],
                    Pattern::Tuple(_)
                        | Pattern::Record(_)
                        | Pattern::Ctor { .. }
                        | Pattern::List(_)
                        | Pattern::Cons(_, _)
                        | Pattern::Alias(_, _)
                ) {
                    let ty = self.expr_ty(d.body);
                    let lowered = self.lower_expr(d.body, &ty);
                    let tmp = self.fresh_temp();
                    let subj = GoExpr::new(GoExprKind::Ident(tmp.clone()), lowered.ty.clone());
                    out.push(GoStmt::Short(tmp.clone(), lowered));
                    let (_cond, binds) = self.pattern_test(&subj, &subj.ty, pat);
                    out.push(GoStmt::Expr(GoExpr::new(
                        GoExprKind::Ident(format!("_ = {tmp}")),
                        GoTy::Unit,
                    )));
                    out.extend(binds);
                    for (_bn, lid) in &d.binders {
                        if let Some(name) = self.local_names.get(lid).cloned() {
                            if name != "_" {
                                out.push(GoStmt::Expr(GoExpr::new(
                                    GoExprKind::Ident(format!("_ = {name}")),
                                    GoTy::Unit,
                                )));
                            }
                        }
                    }
                    return;
                }
            }
        }
        if d.binders.is_empty() {
            // discard `_ = expr` — auto-force a task side effect.
            let lowered = self.lower_expr(d.body, &GoTy::Any);
            if is_task {
                out.push(GoStmt::Discard(any_task_run(lowered)));
            } else {
                out.push(GoStmt::Discard(lowered));
            }
        } else {
            // `name = expr` binding. The binding's type is the RHS expr's type
            // (let-binders aren't in the per-local table); lower + `:=`.
            let (bn, lid) = d.binders[0].clone();
            let gname = self.fresh_local_named(lid, Some(bn.as_str()));
            // A PARAMETERISED let-binding (`selectRecent db = Db.query db …`) is
            // a local function — emit a Go closure. Inlining the body instead
            // (the previous behaviour) leaves each param referenced as an
            // undefined `v_N` ident (`undefined: v_2`, examples 18/36/37).
            let lowered = if d.params.is_empty() {
                let ty = self.expr_ty(d.body);
                self.lower_expr(d.body, &ty)
            } else {
                self.lower_local_fn(&d.params, d.body)
            };
            self.local_tys.insert(lid, lowered.ty.clone());
            out.push(GoStmt::Short(gname.clone(), lowered));
            // `_ = name` so a let binding the body ignores does not trip Go's
            // "declared and not used" (matches the oracle's per-binding discard).
            if gname != "_" {
                out.push(GoStmt::Expr(GoExpr::new(
                    GoExprKind::Ident(format!("_ = {gname}")),
                    GoTy::Unit,
                )));
            }
        }
    }

    /// Lower a parameterised local binding (`f x y = body`) into a Go closure
    /// `func(x, y) R { <destructure>; return body }`. Mirrors `lower_def`'s
    /// param binding; used for `let`-bound helper functions.
    fn lower_local_fn(&mut self, params: &[PatId], body: ExprId) -> GoExpr {
        let mut gparams: Vec<GoParam> = Vec::new();
        let mut destructure: Vec<GoStmt> = Vec::new();
        let mut ptys: Vec<GoTy> = Vec::new();
        for p in params {
            let (pname, pty, binds) = self.bind_param(*p, None);
            ptys.push(pty.clone());
            gparams.push(GoParam { name: pname, ty: pty });
            destructure.extend(binds);
        }
        let ret_ty = self.expr_ty(body);
        let b = self.lower_expr(body, &ret_ty);
        let mut stmts = destructure;
        stmts.push(GoStmt::Return(Some(b)));
        let fn_ty = GoTy::Func(ptys, Box::new(ret_ty.clone()));
        GoExpr::new(GoExprKind::FuncLit(gparams, ret_ty, stmts), fn_ty)
    }

    // ---- expression lowering -------------------------------------------

    fn lower_expr(&mut self, e: ExprId, expected: &GoTy) -> GoExpr {
        let mut actual = self.expr_ty(e);
        // Transparent control-flow (`if` / `case` / `let … in body`) has no value
        // of its own — its arms/body flow DIRECTLY into the slot the whole
        // expression occupies. When the caller's `expected` slot is more specific
        // than this node's own (frequently type-erased) inferred type, thread the
        // expected type down so each arm targets IT (doc 07 §2: lower children
        // with their expected `GoTy`). This is what the oracle does — it types the
        // emitted IIFE by the slot, not by an erased `List a` inference — and it
        // makes a concrete arm value (`pawnTable() : []int`) land in a matching
        // `func() []int` slot instead of a spurious `func() []any` (16-skychess).
        // Guard on `expected != Any`: `Any` means "no constraint", so keep the
        // node's own concrete inference (a concrete arm flowing into an `any` slot
        // needs no per-arm widening).
        if *expected != GoTy::Any
            && actual != *expected
            && matches!(
                &self.body.exprs[e],
                Expr::If { .. } | Expr::Case { .. } | Expr::Let { .. }
            )
        {
            actual = expected.clone();
        }
        let node = self.lower_expr_inner(e, &actual);
        self.coerce_if_needed(node, expected)
    }

    fn coerce_if_needed(&mut self, x: GoExpr, expected: &GoTy) -> GoExpr {
        // `expected == Any` is an implicit upcast in Go (concrete → any); skip.
        // But `any → concrete` is a REAL narrowing (Go never auto-narrows), so we
        // must NOT skip when the SOURCE is `any` (the 02/07 mismatch class).
        if &x.ty == expected || *expected == GoTy::Any {
            return x;
        }
        let reason = if x.ty == GoTy::Any {
            CoerceReason::FfiReturn
        } else {
            CoerceReason::PrimitiveJoin
        };
        GoExpr::new(
            GoExprKind::Coerce {
                inner: Box::new(x.clone()),
                from: x.ty.clone(),
                to: expected.clone(),
                reason,
            },
            expected.clone(),
        )
    }

    fn lower_expr_inner(&mut self, e: ExprId, actual: &GoTy) -> GoExpr {
        match &self.body.exprs[e] {
            Expr::Int(n) => GoExpr::new(GoExprKind::IntLit(*n), actual.clone()),
            Expr::Float(f) => GoExpr::new(GoExprKind::FloatLit(*f), GoTy::Bare(Prim::Float)),
            Expr::Str(s) => GoExpr::new(GoExprKind::StrLit(s.to_string()), GoTy::Bare(Prim::Str)),
            Expr::Bool(b) => GoExpr::new(GoExprKind::BoolLit(*b), GoTy::Bare(Prim::Bool)),
            Expr::Chr(s) => GoExpr::new(GoExprKind::StrLit(s.to_string()), GoTy::Bare(Prim::Str)),
            Expr::Unit => GoExpr::new(
                GoExprKind::Ident("struct{}{}".into()),
                GoTy::Unit,
            ),
            Expr::Var(res) => self.lower_var(res.clone(), actual),
            Expr::Call(callee, args) => self.lower_call(*callee, args, actual),
            Expr::Binop { op, lhs, rhs, .. } => self.lower_binop(op.as_str(), *lhs, *rhs, actual),
            Expr::Negate(inner) => {
                let x = self.lower_expr(*inner, actual);
                GoExpr::new(
                    GoExprKind::Binary(
                        GoBin::Sub,
                        Box::new(GoExpr::new(GoExprKind::IntLit(0), x.ty.clone())),
                        Box::new(x.clone()),
                    ),
                    x.ty,
                )
            }
            Expr::If { arms, els } => self.lower_if(arms, *els, actual),
            Expr::Let { defs, body } => self.lower_let_expr(defs, *body, actual),
            Expr::Access(base, field) => {
                let b = self.lower_expr(*base, &GoTy::Any);
                let cap = capitalize(field.as_str());
                // Field access on an `any`-typed base — a lambda param pinned to a
                // list element the combinator erased to `any` (`\r -> r.tx.account`
                // over a `[]any` `Dict.get` result), or a raw untyped kernel value.
                // Go's `.Field` selector is invalid on `any`, so route through the
                // runtime's reflective `rt.Field`. Typed `Any` so the use slot
                // narrows it (matches the oracle's `rt.Field(r, "AmountCents")`).
                if b.ty == GoTy::Any {
                    return GoExpr::new(
                        GoExprKind::Call(
                            Box::new(GoExpr::new(GoExprKind::Ident("rt.Field".into()), GoTy::Any)),
                            vec![
                                b,
                                GoExpr::new(GoExprKind::StrLit(cap), GoTy::Bare(Prim::Str)),
                            ],
                        ),
                        GoTy::Any,
                    );
                }
                // A func-typed struct field (`OnChange func(bool) any`) accessed
                // in callee position must carry its REAL func type, not the
                // expected slot type (`actual`, frequently `any` for a callee) —
                // `lower_call` reads the callee's param types off this node to
                // coerce `any`-returning kernel args (`rt.Basics_not`) into the
                // concrete param slot (`bool`). Non-func fields keep the existing
                // `actual` typing (zero baseline change).
                let field_ty = match &b.ty {
                    GoTy::Struct(fs) => fs
                        .iter()
                        .find(|(n, _)| n.as_str() == cap)
                        .map(|(_, t)| t.clone())
                        .filter(|t| matches!(t, GoTy::Func(_, _))),
                    _ => None,
                }
                .unwrap_or_else(|| actual.clone());
                GoExpr::new(
                    GoExprKind::Selector(Box::new(b), cap),
                    field_ty,
                )
            }
            Expr::Record(fields) => self.lower_record(fields, actual),
            Expr::Update { base, fields } => self.lower_update(*base, fields, actual),
            Expr::Tuple(elems) => self.lower_tuple(elems, actual),
            Expr::List(elems) => self.lower_list(elems, actual),
            Expr::Lambda { params, body } => self.lower_lambda(params, *body, actual),
            Expr::Case { subject, branches } => self.lower_case(*subject, branches, actual),
            Expr::Accessor(field) => {
                // `.field` as a function value → func(x any) any { return x.Field }
                let f = capitalize(field.as_str());
                GoExpr::new(
                    GoExprKind::Ident(format!("func(_r any) any {{ return rt.Field(_r, \"{f}\") }}")),
                    GoTy::Any,
                )
            }
            Expr::Error => {
                self.warnings.push("lowered an Expr::Error recovery node".into());
                GoExpr::new(GoExprKind::Nil, GoTy::Any)
            }
        }
    }

    fn lower_var(&mut self, res: Res, actual: &GoTy) -> GoExpr {
        match res {
            Res::Local(id) => {
                let name = self
                    .local_names
                    .get(&id)
                    .cloned()
                    // Fallback must match `fresh_local_named`'s default-hint
                    // format (`v_<id>`), NOT `v<id>` — else a reference lowered
                    // before its binder registered (`undefined: v4`) diverges
                    // from the eventual `v_4` declaration.
                    .unwrap_or_else(|| format!("v_{}", id.0));
                // Use the local's DECLARED Go type, not the caller's expected
                // slot type — the outer `lower_expr` coerces to `expected`. A
                // param declared `RetryPolicy_R` must not report itself as a
                // body-inferred subset record.
                let ty = self.local_tys.get(&id).cloned().unwrap_or_else(|| actual.clone());
                GoExpr::new(GoExprKind::Ident(name), ty)
            }
            Res::Def(d) => {
                // A bare record-alias constructor used as a value/first-class
                // function (`Result.map3 Profile …`, `List.map Piece …`): eta
                // to a closure over its fields, same as a `Res::Ctor` value.
                if self.record_ctor_name(d).is_some() {
                    return self.lower_ctor_value(d, actual, None);
                }
                if let Some(raw) = self.kernel_alias.get(&d) {
                    // value-reference to a kernel-alias: use the runtime symbol.
                    // A NULLARY kernel value (`Dict.empty = Ffi.kernel "Dict_empty"`,
                    // `Cmd.none`) whose runtime symbol is a zero-arg func must be
                    // CALLED here too — the alias def carries no params, so a bare
                    // `rt.Dict_empty` is a `func() any` and a typed slot panics.
                    let go = alias_go_name(raw);
                    let nullary = raw
                        .split_once('_')
                        .is_some_and(|(m, f)| crate::kernel::is_nullary_kernel_value(m, f));
                    if nullary {
                        self.nullary_kernel_value(&go, actual)
                    } else {
                        GoExpr::new(GoExprKind::Ident(go), actual.clone())
                    }
                } else if let Some(e) = self.defs.get(&d) {
                    self.discovered.push(d);
                    let go = top_go_name(&e.module_name, &e.name);
                    if e.body.params.is_empty() {
                        // A zero-param top-level binding is emitted as a zero-arg
                        // Go thunk `func M_x() T` (every top-level def becomes a
                        // func, doc 08 §3). Referenced in a *value* slot it must
                        // be CALLED to yield the value — a bare `M_x` is a
                        // `func() T`, not a `T` (Limitation #7 value-slot class,
                        // e.g. `Sky_Test_runMain(tests)`, `Cmd.perform someTask`).
                        // A def WITH params is a genuine function value (HOF
                        // callback) and stays bare.
                        let fn_ty = GoTy::Func(vec![], Box::new(actual.clone()));
                        GoExpr::new(
                            GoExprKind::Call(
                                Box::new(GoExpr::new(GoExprKind::Ident(go), fn_ty)),
                                vec![],
                            ),
                            actual.clone(),
                        )
                    } else {
                        GoExpr::new(GoExprKind::Ident(go), actual.clone())
                    }
                } else {
                    GoExpr::new(GoExprKind::Ident("nil".into()), GoTy::Any)
                }
            }
            Res::Kernel { module, func } => {
                let go = kernel_go_name(module.as_str(), func.as_str());
                if crate::kernel::is_nullary_kernel_value(module.as_str(), func.as_str()) {
                    self.nullary_kernel_value(&go, actual)
                } else {
                    GoExpr::new(GoExprKind::Ident(go), actual.clone())
                }
            }
            Res::Ctor(cr) => {
                let pin = self.pinned_union_go(cr.type_);
                self.lower_ctor_value(cr.def, actual, pin)
            }
            Res::Foreign { package, name } => {
                self.warnings
                    .push(format!("foreign ref {}.{}", package.as_str(), name.as_str()));
                GoExpr::new(GoExprKind::Ident("nil".into()), GoTy::Any)
            }
            Res::Error => GoExpr::new(GoExprKind::Nil, GoTy::Any),
        }
    }

    /// Emit a nullary kernel *value* (`Dict.empty`, `Cmd.none`, `Math.pi`): its
    /// runtime symbol is a zero-arg func, so CALL it (a bare `func() T` in a value
    /// slot panics). The non-`T` symbols return Go `any` (`func Dict_empty() any`).
    /// A CONCRETE-key map slot (`map[string]V` — `Dict.empty : Dict String ()`
    /// passed to a `map[string]struct{}` param) needs the `any` value narrowed via
    /// `rt.AsMapT` (matching the oracle) or `go build` rejects `any` in that slot.
    /// Every other slot keeps the historical bare-call typed-`actual` form: an
    /// any-key `map[interface{}]interface{}` (`Dict any any`, a widened `foldl`
    /// accumulator) has NO sound coercion from the runtime's `map[string]any`, and
    /// those contexts widen to `any` anyway — coercing there panics (CoerceFailure).
    fn nullary_kernel_value(&mut self, go: &str, actual: &GoTy) -> GoExpr {
        let concrete_key_map = matches!(actual, GoTy::Map(k, _) if **k != GoTy::Any);
        if concrete_key_map {
            let fn_ty = GoTy::Func(vec![], Box::new(GoTy::Any));
            let call = GoExpr::new(
                GoExprKind::Call(
                    Box::new(GoExpr::new(GoExprKind::Ident(go.to_string()), fn_ty)),
                    vec![],
                ),
                GoTy::Any,
            );
            return self.coerce_if_needed(call, actual);
        }
        let fn_ty = GoTy::Func(vec![], Box::new(actual.clone()));
        GoExpr::new(
            GoExprKind::Call(
                Box::new(GoExpr::new(GoExprKind::Ident(go.to_string()), fn_ty)),
                vec![],
            ),
            actual.clone(),
        )
    }

    fn lower_ctor_value(&mut self, def: DefId, actual: &GoTy, pin: Option<String>) -> GoExpr {
        // A bare constructor reference used as a VALUE. For a nullary ctor this
        // is just the constructed value. For a ctor of arity ≥ 1 used as a
        // function value (`onInput UpdateDraft`, `onClick (Select room)` after
        // spine flattening leaves a partial), Sky curries but Go does not — so
        // eta-expand into a closure that applies the ctor to its params.
        let loc = self.db.def_loc(def);
        let cname = loc.map(|l| l.name.as_str().to_string()).unwrap_or_default();
        let arity = self.ctor_arity_pinned(&cname, pin.as_deref());
        if arity == 0 {
            let (_, expr) = self.ctor_call(def, &[], actual, pin);
            return expr;
        }
        // ADT/iota constructors take the untyped `Fields []any` bag — their Go
        // ctor func is `func(any…) T`. Eta-expanding with a CONCRETE param type
        // (adopted from the expected func shape) is unsound: when the closure is
        // later `rt.Coerce`d to a different concrete func type and reflect-called,
        // the arg's real type won't match the declared param (`int` reaching a
        // `rt.SkyADT` param — the cross-module `Msg`-wrapper case). Force `any`
        // params for those; record ctors keep their typed field params.
        let adt_like = matches!(
            self.ctor_owner.get(&cname).map(|(_, k)| *k),
            Some(NominalKind::Adt) | Some(NominalKind::Iota)
        );
        // Match the expected function shape when known; else `any` params.
        let (param_tys, ret): (Vec<GoTy>, GoTy) = match actual {
            GoTy::Func(ps, r) if ps.len() == arity => {
                let ps = if adt_like { vec![GoTy::Any; arity] } else { ps.clone() };
                (ps, (**r).clone())
            }
            _ => (vec![GoTy::Any; arity], GoTy::Any),
        };
        let mut gparams: Vec<GoParam> = Vec::new();
        let mut arg_exprs: Vec<GoExpr> = Vec::new();
        for pty in param_tys.iter().take(arity) {
            let pname = format!("_p{}", self.local_counter);
            self.local_counter += 1;
            gparams.push(GoParam { name: pname.clone(), ty: pty.clone() });
            // ADT/iota ctor params are the untyped `Fields []any` bag; record
            // ctor params are typed. `ctor_emit` widens/coerces per kind, so
            // hand it the raw typed ident and let it decide.
            arg_exprs.push(GoExpr::new(GoExprKind::Ident(pname), pty.clone()));
        }
        let ctor_val = self.ctor_emit(&cname, arg_exprs, &ret, pin.as_deref());
        let body = self.coerce_if_needed(ctor_val, &ret);
        let fn_ty = GoTy::Func(param_tys, Box::new(ret.clone()));
        GoExpr::new(
            GoExprKind::FuncLit(gparams, ret, vec![GoStmt::Return(Some(body))]),
            fn_ty,
        )
    }

    /// Lower a constructor application (0+ args). Handles builtin Ok/Err/Just/
    /// Nothing/True/False and user ADT/iota/record constructors.
    fn ctor_call(
        &mut self,
        def: DefId,
        args: &[ExprId],
        actual: &GoTy,
        pin: Option<String>,
    ) -> (bool, GoExpr) {
        let loc = self.db.def_loc(def);
        let cname = loc.map(|l| l.name.as_str().to_string()).unwrap_or_default();
        // Partial application of a multi-arg constructor (`JobDone jid` where
        // `JobDone : Int -> Result … -> Msg`, `Piece kind` for a record ctor) is
        // a function VALUE — eta-expand the missing params into a closure. Go has
        // no currying, so a direct under-applied call is "not enough arguments in
        // call to <Ctor>" (examples 16/18). Builtin container ctors keep their
        // fixed arity handling in `ctor_emit`.
        let arity = self.ctor_arity_pinned(&cname, pin.as_deref());
        let builtin = matches!(cname.as_str(), "Ok" | "Err" | "Just" | "Nothing" | "True" | "False");
        if !builtin && args.len() < arity {
            return (true, self.ctor_partial(&cname, args, arity, actual, pin));
        }
        // For a sealed-ADT ctor (typed params), lower each arg with its declared
        // field Go-type as the expected slot so the value lands typed and the
        // `coerce_if_needed` in `ctor_emit` elides — zero construction coerces in
        // the common (already-typed) case. Bag / builtin ctors keep `any`.
        let field_tys = self.sealed_ctor_field_gotys(&cname, pin.as_deref());
        let lowered_args: Vec<GoExpr> = match &field_tys {
            Some(ftys) => args
                .iter()
                .enumerate()
                .map(|(i, a)| self.lower_expr(*a, ftys.get(i).unwrap_or(&GoTy::Any)))
                .collect(),
            None => args.iter().map(|a| self.lower_expr(*a, &GoTy::Any)).collect(),
        };
        let expr = self.ctor_emit(&cname, lowered_args, actual, pin.as_deref());
        (true, expr)
    }

    /// The variant field Go-types for a ctor IFF it belongs to a sealed ADT union
    /// (else `None`, so bag/builtin ctors keep `any`-typed argument lowering).
    /// Disambiguated by the pinned owning union, mirroring `ctor_arity_pinned`.
    fn sealed_ctor_field_gotys(&self, cname: &str, pin: Option<&str>) -> Option<Vec<GoTy>> {
        let (go_type, kind) = self.ctor_union_go_pinned(cname, pin)?;
        if kind != NominalKind::Adt || !self.sealed_unions.contains(&go_type) {
            return None;
        }
        self.ctor_field_gotys
            .get(&(go_type, cname.to_string()))
            .cloned()
    }

    /// Eta-expand a partially-applied constructor into a Go closure that applies
    /// the ctor to the supplied args plus fresh params for the missing ones.
    fn ctor_partial(
        &mut self,
        cname: &str,
        given: &[ExprId],
        arity: usize,
        actual: &GoTy,
        pin: Option<String>,
    ) -> GoExpr {
        let mut arg_exprs: Vec<GoExpr> =
            given.iter().map(|a| self.lower_expr(*a, &GoTy::Any)).collect();
        let n_rest = arity - given.len();
        let (rest_tys, ret): (Vec<GoTy>, GoTy) = match actual {
            GoTy::Func(ps, r) if ps.len() == n_rest => (ps.clone(), (**r).clone()),
            _ => (vec![GoTy::Any; n_rest], GoTy::Any),
        };
        let mut gparams: Vec<GoParam> = Vec::new();
        for pty in &rest_tys {
            let pname = format!("_p{}", self.local_counter);
            self.local_counter += 1;
            gparams.push(GoParam { name: pname.clone(), ty: pty.clone() });
            arg_exprs.push(GoExpr::new(GoExprKind::Ident(pname), pty.clone()));
        }
        let ctor_val = self.ctor_emit(cname, arg_exprs, &ret, pin.as_deref());
        let body = self.coerce_if_needed(ctor_val, &ret);
        let fn_ty = GoTy::Func(rest_tys, Box::new(ret.clone()));
        GoExpr::new(
            GoExprKind::FuncLit(gparams, ret, vec![GoStmt::Return(Some(body))]),
            fn_ty,
        )
    }

    /// Emit a constructor call from already-lowered argument expressions.
    fn ctor_emit(
        &mut self,
        cname: &str,
        lowered_args: Vec<GoExpr>,
        actual: &GoTy,
        pin: Option<&str>,
    ) -> GoExpr {
        let cname = cname.to_string();
        // type args for the generic container constructors (Go can't infer the
        // unused type param, e.g. `E` in `Ok`).
        let (res_ea, maybe_a) = match actual {
            GoTy::Named(n, ts) if n == "rt.SkyResult" && ts.len() == 2 => {
                (format!("[{}, {}]", render_goty(&ts[0]), render_goty(&ts[1])), String::new())
            }
            GoTy::Named(n, ts) if n == "rt.SkyMaybe" && ts.len() == 1 => {
                (String::new(), format!("[{}]", render_goty(&ts[0])))
            }
            _ => (String::new(), String::new()),
        };
        let expr = match cname.as_str() {
            "Ok" => call_rt(&format!("rt.Ok{res_ea}"), lowered_args, actual.clone()),
            "Err" => call_rt(&format!("rt.Err{res_ea}"), lowered_args, actual.clone()),
            "Just" => call_rt(&format!("rt.Just{maybe_a}"), lowered_args, actual.clone()),
            "Nothing" => {
                let a = match actual {
                    GoTy::Named(n, ts) if n == "rt.SkyMaybe" && ts.len() == 1 => render_goty(&ts[0]),
                    _ => "any".to_string(),
                };
                GoExpr::new(GoExprKind::Ident(format!("rt.Nothing[{a}]()")), actual.clone())
            }
            "True" => GoExpr::new(GoExprKind::BoolLit(true), GoTy::Bare(Prim::Bool)),
            "False" => GoExpr::new(GoExprKind::BoolLit(false), GoTy::Bare(Prim::Bool)),
            _ => {
                // user ctor: find its union go name via nominal + ctor union.
                if let Some((go_type, kind)) = self.ctor_union_go_pinned(&cname, pin) {
                    match kind {
                        NominalKind::Iota => {
                            self.used_types.insert(go_type.clone());
                            GoExpr::new(
                                GoExprKind::Ident(format!("{go_type}_{cname}")),
                                GoTy::Named(go_type, vec![]),
                            )
                        }
                        NominalKind::Adt => {
                            self.used_types.insert(go_type.clone());
                            // A sealed-ADT ctor takes TYPED params (the variant
                            // struct's field types); coerce each arg to its field
                            // type. This elides when the arg is already typed (the
                            // direct-call path lowers args with the field type as
                            // the expected slot); it inserts the necessary narrowing
                            // only for `any`-typed args (the eta-expanded closure
                            // path forces `any` params). Bag ADTs keep `any` params
                            // and pass args unchanged.
                            let args_out = if self.sealed_unions.contains(&go_type) {
                                let ftys = self
                                    .ctor_field_gotys
                                    .get(&(go_type.clone(), cname.clone()))
                                    .cloned()
                                    .unwrap_or_default();
                                lowered_args
                                    .into_iter()
                                    .enumerate()
                                    .map(|(i, a)| match ftys.get(i) {
                                        Some(t) => self.coerce_if_needed(a, t),
                                        None => a,
                                    })
                                    .collect()
                            } else {
                                lowered_args
                            };
                            call_rt(&format!("{go_type}_{cname}"), args_out, GoTy::Named(go_type, vec![]))
                        }
                        NominalKind::Record => {
                            let ctor = go_type.trim_end_matches("_R").to_string();
                            self.used_types.insert(go_type.clone());
                            // Coerce each arg to the record's declared field Go
                            // type (fields are in ctor-param order). A partial
                            // application (`\p1 p2 … -> Overview p1 p2 …`) supplies
                            // `any` rest-params that must narrow to the typed field
                            // — else `Overview(_p1 any, …)` fails against a
                            // `func(string, …)` signature (example 25).
                            let ftys: Vec<GoTy> = self
                                .record_fields
                                .get(&go_type)
                                .map(|fs| {
                                    fs.iter().map(|(_, t)| sky_ty_to_go_in(t, self.env, Some(&self.cur_module))).collect()
                                })
                                .unwrap_or_default();
                            let coerced: Vec<GoExpr> = lowered_args
                                .into_iter()
                                .enumerate()
                                .map(|(i, a)| match ftys.get(i) {
                                    Some(t) => self.coerce_if_needed(a, t),
                                    None => a,
                                })
                                .collect();
                            call_rt(&ctor, coerced, GoTy::Named(go_type, vec![]))
                        }
                    }
                } else {
                    self.warnings.push(format!("unknown ctor {cname}"));
                    GoExpr::new(GoExprKind::Nil, GoTy::Any)
                }
            }
        };
        expr
    }

    fn ctor_union_go(&self, cname: &str) -> Option<(String, NominalKind)> {
        self.ctor_owner.get(cname).cloned()
    }

    /// The owning-union Go name for a ctor, disambiguated by the resolved
    /// `CtorRef.type_` DefId (`pin`) when the bare name collides across two
    /// unions (`AlignLeft` lives in both `Std.Ui.HAlign` and
    /// `Std.Css.TextAlign`). The bare-name `ctor_owner` map picks whichever
    /// union interned last, so a value-position ctor reference in a module
    /// that only imports the OTHER union emits the wrong nominal (and, for an
    /// iota enum, a bare `int` that then fails `rt.Coerce`). Mirrors the
    /// pattern-path disambiguation via `_subj_ty`. Falls back to `ctor_owner`.
    fn ctor_union_go_pinned(
        &self,
        cname: &str,
        pin: Option<&str>,
    ) -> Option<(String, NominalKind)> {
        if let Some(gt) = pin {
            if let Some((k, _)) = self.ctor_in_union.get(&(gt.to_string(), cname.to_string())) {
                return Some((gt.to_string(), *k));
            }
        }
        self.ctor_union_go(cname)
    }

    /// The value-argument count for a ctor, disambiguated by the pinned owning
    /// union (same discipline as `ctor_union_go_pinned`). The bare-name
    /// `ctor_arity` map collides when a record alias's positional-ctor name
    /// equals a nullary ADT ctor (example 25 `Overview`); pin picks the arity
    /// belonging to the SAME nominal the value reference resolved to. Falls back
    /// to the bare map when no pin is available (record-alias ctors resolved as
    /// `Res::Def` carry no union pin).
    fn ctor_arity_pinned(&self, cname: &str, pin: Option<&str>) -> usize {
        if let Some(gt) = pin {
            if let Some(a) = self.ctor_arity_in_union.get(&(gt.to_string(), cname.to_string())) {
                return *a;
            }
        }
        self.ctor_arity.get(cname).copied().unwrap_or(0)
    }

    /// Build the owning-union Go name (`Std_Css_TextAlign`) from a union's
    /// `DefId` (the resolved `CtorRef.type_`). Matches `module_prefix + "_" +
    /// type-name`, the same shape `reg!` interns during type-decl collection.
    fn pinned_union_go(&self, type_: DefId) -> Option<String> {
        let loc = self.db.def_loc(type_)?;
        // Builtin ctors (Ok/Err/Just/Nothing/…) intern their type under the
        // synthetic `BUILTIN_MOD` (`ModuleId(u32::MAX)`), which is not a real
        // module — `module_name` would index out of bounds. They never collide
        // and are handled by name in `ctor_emit`, so no pin is needed.
        if loc.module.index() == u32::MAX {
            return None;
        }
        let mname = self.db.module_name(loc.module).to_string();
        Some(format!("{}_{}", module_prefix(&mname), loc.name.as_str()))
    }

    // ---- call / operator / control-flow lowering -----------------------

    /// If `d` names a record-alias auto-constructor (`type alias Piece = { … }`
    /// gives `Piece : Kind -> Colour -> Piece`), return its name. These resolve
    /// to `Res::Def` (a bodyless synthesized def), NOT `Res::Ctor`, so the ctor
    /// call/value paths must recognise them by name — otherwise `lower_var`
    /// emits a zero-arg thunk `Piece()` and the args apply to its result
    /// (`Piece()(kind, colour)` → "not enough arguments", example 16).
    fn record_ctor_name(&self, d: DefId) -> Option<String> {
        let loc = self.db.def_loc(d);
        let name = loc.map(|l| l.name.as_str().to_string())?;
        match self.ctor_owner.get(&name) {
            Some((_, NominalKind::Record)) => Some(name),
            _ => None,
        }
    }

    fn lower_call(&mut self, callee: ExprId, args: &[ExprId], actual: &GoTy) -> GoExpr {
        // constructor application?
        if let Expr::Var(Res::Ctor(cr)) = &self.body.exprs[callee] {
            let cr = cr.clone();
            let pin = self.pinned_union_go(cr.type_);
            let (_, e) = self.ctor_call(cr.def, args, actual, pin);
            return e;
        }
        // record-alias auto-constructor applied (resolves as `Res::Def`, see
        // `record_ctor_name`): route through the arity-aware ctor path.
        if let Expr::Var(Res::Def(d)) = &self.body.exprs[callee] {
            let d = *d;
            if self.record_ctor_name(d).is_some() {
                let (_, e) = self.ctor_call(d, args, actual, None);
                return e;
            }
        }
        // kernel-alias direct call → uniform widen-args / coerce-return.
        if let Expr::Var(Res::Def(d)) = &self.body.exprs[callee] {
            if let Some(raw) = self.kernel_alias.get(d) {
                let go = alias_go_name(raw);
                return self.kernel_call(&go, args, actual);
            }
        }
        // kernel direct call
        if let Expr::Var(Res::Kernel { module, func }) = &self.body.exprs[callee] {
            let go = kernel_go_name(module.as_str(), func.as_str());
            return self.kernel_call(&go, args, actual);
        }
        // Go-FFI direct call (doc 09): `Uuid.newString ()` → the typed wrapper
        // `rt.Go_Uuid_newStringT(…)`. The wrapper drops Sky's `()` unit params
        // (its Go signature takes zero args), so unit call-args are elided. The
        // per-usage HM inference already gave the call its `SkyResult[…]` shape,
        // so the enclosing `case`/coercion narrows the `any` return exactly as
        // for a kernel call.
        if let Expr::Var(Res::Foreign { package, name }) = &self.body.exprs[callee] {
            if let Some((sym, typed)) = self.ffi.call_symbol(package.as_str(), name.as_str()) {
                self.ffi_used.insert(package.as_str().to_string());
                let wparams = self.ffi.wrapper_params(&sym);
                return self.ffi_call(&format!("rt.{sym}"), args, actual, &wparams, typed);
            }
            // A Go-FFI call whose package has no wrapper symbol for `name`: the
            // function either does not exist in the pinned surface, or is
            // inexpressible from Sky (e.g. it takes a Go `error` parameter, whose
            // wrapper is deliberately not emitted — see `ffi::gen::has_error_param`).
            // Falling through would lower the callee to `nil` and emit `nil(args)`,
            // which `go build` rejects (`cannot call nil`). Reject at check time
            // instead so `sky check ≡ sky build` holds.
            self.errors.push(format!(
                "no such Go-FFI function `{}.{}` — it is not exported by the pinned \
                 FFI surface, or it takes a value that cannot be produced from Sky \
                 (such as a Go `error` parameter). It cannot be called from Sky.",
                package.as_str(),
                name.as_str()
            ));
            return GoExpr::new(GoExprKind::Ident("nil".into()), actual.clone());
        }
        // general call: lower callee + args, coercing each arg to the callee's
        // inferred param type (cross-def boundary coercion). The call's Go type is
        // the callee's DECLARED Go return type (which is `any` for a generic Sky
        // def) — the outer `coerce_if_needed` then narrows it to the slot type.
        let (param_gtys, ret_goty, go_arity): (Option<Vec<GoTy>>, GoTy, Option<usize>) =
            if let Expr::Var(Res::Def(d)) = &self.body.exprs[callee] {
                let d = *d;
                // The callee's param/result types are declared in ITS module — a
                // nominal `Msg`/`Model` there is the callee's, not the caller's.
                let callee_mod = self
                    .defs
                    .get(&d)
                    .map(|e| e.module_name.clone())
                    .unwrap_or_else(|| self.cur_module.clone());
                let ps = self.def_param_tys.get(&d).cloned().map(|ptys| {
                    ptys.iter().map(|t| self.goty_in(t, &callee_mod)).collect::<Vec<_>>()
                });
                let ret = self
                    .def_result_tys
                    .get(&d)
                    .cloned()
                    .map(|t| self.goty_in(&t, &callee_mod))
                    .unwrap_or_else(|| actual.clone());
                // The emitted Go function's arity is the def's value-param count.
                let arity = self.defs.get(&d).map(|e| e.body.params.len());
                (ps, ret, arity)
            } else {
                (None, actual.clone(), None)
            };
        // Higher-order list-combinator closure: `List.map (\x -> …) xs`,
        // `List.foldl (\x acc -> …) z xs`. When arg 0 is a lambda and the LAST arg
        // is list-shaped, the closure's element param IS the list's element type.
        // Pin it (via `closure_elem`) so a body-inferred subset `struct{…}` param
        // resolves to the honest runtime element. The element is read from the list
        // arg's OWN recorded Go type, so `func(elem …) …` and the list arg agree by
        // construction — when that element is already the subset struct
        // (18-job-queue's `[]struct{…}` field) the pin is a no-op (byte-identical).
        // Only when the list's Go type is a concrete `Slice` do we know its element
        // (`[]any` → `any`, `[]State_Post_R` → the nominal). A bare `Any` means
        // inference didn't pin the list — leave the closure param untouched (its
        // body-inferred struct is the baseline, and pinning to `any` there would
        // strip the fields a record update needs, 18-job-queue).
        let combinator_elem: Option<GoTy> =
            if args.len() >= 2 && matches!(&self.body.exprs[args[0]], Expr::Lambda { .. }) {
                match self.expr_ty(*args.last().unwrap()) {
                    GoTy::Slice(e) => Some(*e),
                    _ => None,
                }
            } else {
                None
            };
        let c = self.lower_expr(callee, &GoTy::Any);
        // A non-`Def` callee (a typed Go func *value* — e.g. the checkbox cfg's
        // `OnChange func(bool) any` struct field) carries its param types in its
        // own lowered Go func type. Derive the arg-coercion targets from it so an
        // `any`-returning kernel arg (`rt.Basics_not`) narrows to the concrete
        // param slot (`bool`) via `rt.AsBool` — Go rejects a bare `any` in a
        // typed param position ("need type assertion", example 37/38).
        let param_gtys = param_gtys.or_else(|| match &c.ty {
            GoTy::Func(ps, _) if ps.iter().any(|p| *p != GoTy::Any) => Some(ps.clone()),
            _ => None,
        });
        let largs: Vec<GoExpr> = args
            .iter()
            .enumerate()
            .map(|(i, a)| {
                let expected = param_gtys
                    .as_ref()
                    .and_then(|ps| ps.get(i))
                    .cloned()
                    .unwrap_or(GoTy::Any);
                if i == 0 && combinator_elem.is_some() {
                    let saved = self.closure_elem.take();
                    self.closure_elem = combinator_elem.clone();
                    let e = self.lower_expr(*a, &expected);
                    self.closure_elem = saved;
                    e
                } else {
                    self.lower_expr(*a, &expected)
                }
            })
            .collect();
        // Partial application: a def of arity N called with M < N args must
        // yield a closure over the remaining params (Sky curries; Go does not).
        // `Result.andThen (validateTime now)` → `func(_p0 any) R { return
        // validateTime(now, rt.AsString(_p0)) }`.
        if let Some(arity) = go_arity {
            if largs.len() < arity {
                return self.make_partial(c, largs, &param_gtys, &ret_goty, arity);
            }
        }
        GoExpr::new(GoExprKind::Call(Box::new(c), largs), ret_goty)
    }

    /// Build a closure for a partially-applied top-level function. The closure
    /// takes the remaining params as `any` (so the runtime's reflection-based
    /// HOF dispatch can invoke it), coercing each to the callee's real Go param
    /// type inside the wrapped call.
    fn make_partial(
        &mut self,
        callee: GoExpr,
        given: Vec<GoExpr>,
        param_gtys: &Option<Vec<GoTy>>,
        ret: &GoTy,
        arity: usize,
    ) -> GoExpr {
        let n_given = given.len();
        let n_rest = arity - n_given;
        let mut rest_params = Vec::new();
        let mut call_args = given;
        for i in 0..n_rest {
            let pty = param_gtys
                .as_ref()
                .and_then(|ps| ps.get(n_given + i))
                .cloned()
                .unwrap_or(GoTy::Any);
            let pname = format!("_p{}", self.local_counter);
            self.local_counter += 1;
            rest_params.push(GoParam { name: pname.clone(), ty: GoTy::Any });
            let arg_ref = GoExpr::new(GoExprKind::Ident(pname), GoTy::Any);
            call_args.push(self.coerce_if_needed(arg_ref, &pty));
        }
        let body_call =
            GoExpr::new(GoExprKind::Call(Box::new(callee), call_args), ret.clone());
        let fn_ty = GoTy::Func(vec![GoTy::Any; n_rest], Box::new(ret.clone()));
        GoExpr::new(
            GoExprKind::FuncLit(rest_params, ret.clone(), vec![GoStmt::Return(Some(body_call))]),
            fn_ty,
        )
    }

    /// `func_e applied to value_e`, flattening a partial-application spine on
    /// `func_e` so the emitted call carries the full argument list.
    fn lower_pipe(&mut self, func_e: ExprId, value_e: ExprId, actual: &GoTy) -> GoExpr {
        if let Expr::Call(g, gargs) = &self.body.exprs[func_e] {
            let g = *g;
            let mut full = gargs.clone();
            full.push(value_e);
            return self.lower_call(g, &full, actual);
        }
        // `value_e |> func_e` with a bare function reference is the single-arg
        // application `func_e(value_e)`. Route through `lower_call` so the piped
        // value coerces to the callee's DECLARED param type — a `List.map`-produced
        // `[]any` flowing into `uniqueKeepingFirst : List String -> …` must narrow
        // to `[]string` (35-composite-generics). The prior `Call(f,[a])` lowered the
        // arg with expected `Any`, skipping that boundary coercion. `lower_call`
        // dispatches Res::Def/Kernel/Ctor/FFI identically to a direct call.
        if matches!(&self.body.exprs[func_e], Expr::Var(_)) {
            return self.lower_call(func_e, &[value_e], actual);
        }
        let f = self.lower_expr(func_e, &GoTy::Any);
        let a = self.lower_expr(value_e, &GoTy::Any);
        GoExpr::new(GoExprKind::Call(Box::new(f), vec![a]), actual.clone())
    }

    /// The uniform kernel-call rule (doc 07 §6 FFI-return): every runtime kernel
    /// is `any`-based, so widen each arg to `any`, call `go(…)` (result `any`),
    /// then coerce the `any` result to the call's typed slot.
    /// Lower a call to a Go-FFI wrapper. Like `kernel_call`, but Sky's `()` unit
    /// call-args are dropped — the typed wrapper's Go signature has no parameter
    /// for a Sky `Unit` (a `() -> R` binding lowers to a zero-arg Go func). Any
    /// non-unit args are widened to `any` and passed through; the `any` return is
    /// coerced to the call's typed slot (FFI-return coercion).
    /// Coerce/convert an arg to a Go-FFI wrapper's REAL primitive param type.
    /// `want` is the wrapper's Go type string for this slot (`"int64"`,
    /// `"string"`, …). Handles two shapes Go rejects on a bare value:
    ///   * `any → string/int/bool/float64/[]byte`: assert via `rt.As*`.
    ///   * numeric widen (`Int`→`int64`/`int32`/`uint…`, `Float`→`float32`):
    ///     a Go conversion `int64(x)` (asserting `any→int` first when needed) —
    ///     `rt.Coerce[int64]` would be a type ASSERTION and panic (`int` is not
    ///     `int64`). A matching or unrecognised type passes verbatim.
    fn coerce_ffi_prim_arg(&mut self, e: GoExpr, want: Option<&str>) -> GoExpr {
        let Some(want) = want else {
            return e;
        };
        let is_any = e.ty == GoTy::Any;
        match want {
            "string" => self.coerce_if_needed(e, &GoTy::Bare(Prim::Str)),
            "bool" => self.coerce_if_needed(e, &GoTy::Bare(Prim::Bool)),
            "int" => self.coerce_if_needed(e, &GoTy::Bare(Prim::Int)),
            "float64" => self.coerce_if_needed(e, &GoTy::Bare(Prim::Float)),
            "[]byte" => self.coerce_if_needed(e, &GoTy::Bare(Prim::Bytes)),
            // Go integer widths Sky's `Int` (Go `int`) must be CONVERTED to.
            "int64" | "int32" | "int16" | "int8" | "uint" | "uint64" | "uint32"
            | "uint16" | "uint8" | "byte" | "rune" | "uintptr" => {
                let base = if is_any {
                    self.coerce_if_needed(e, &GoTy::Bare(Prim::Int))
                } else {
                    e
                };
                self.go_convert(want, base)
            }
            "float32" => {
                let base = if is_any {
                    self.coerce_if_needed(e, &GoTy::Bare(Prim::Float))
                } else {
                    e
                };
                self.go_convert(want, base)
            }
            // A non-primitive / `any` wrapper param takes the value verbatim (Go
            // widens any concrete type to `any` implicitly).
            _ => e,
        }
    }

    /// Wrap `e` in a Go numeric conversion `T(e)` (e.g. `int64(x)`).
    fn go_convert(&self, go_ty: &str, e: GoExpr) -> GoExpr {
        GoExpr::new(
            GoExprKind::Call(
                Box::new(GoExpr::new(GoExprKind::Ident(go_ty.into()), GoTy::Any)),
                vec![e],
            ),
            GoTy::Named(go_ty.into(), vec![]),
        )
    }

    fn ffi_call(
        &mut self,
        go: &str,
        args: &[ExprId],
        actual: &GoTy,
        wrapper_params: &[String],
        typed: bool,
    ) -> GoExpr {
        // A Go-FFI wrapper (`rt.Go_<Pkg>_<fn>T`) has TYPED params, not `any`:
        // each arg must narrow to the wrapper's per-param slot alias
        // `rt.FfiT_<base>_P<i>` (`base` = the wrapper name minus the `rt.` prefix
        // and the trailing `T`). `rt.Coerce` handles the narrowing — including the
        // reflect.MakeFunc adapter for a Sky closure flowing into a Go `func(...)`
        // slot. Widening to `any` (as a kernel call does) fails `go build` because
        // Go won't pass `any` into a `*mux.Router` / `string` / `func(...)` param.
        let base = go
            .strip_prefix("rt.")
            .unwrap_or(go)
            .strip_suffix('T')
            .unwrap_or(go);
        let mut largs: Vec<GoExpr> = Vec::new();
        let mut pi = 0usize;
        for a in args {
            // A TYPED wrapper drops Sky's `()` unit params (zero-arg Go
            // signature) — elide the arg. The UNTYPED fallback keeps `(_ any)`,
            // so its unit param must be passed as the Go unit literal
            // `struct{}{}` (matching how a Sky helper's unit-param call emits).
            if matches!(&self.body.exprs[*a], Expr::Unit) {
                if typed {
                    continue;
                }
                largs.push(GoExpr::new(GoExprKind::Ident("struct{}{}".into()), GoTy::Any));
                pi += 1;
                continue;
            }
            let e = self.lower_expr(*a, &GoTy::Any);
            let slot_name = format!("FfiT_{base}_P{pi}");
            if self.ffi.has_ffi_slot(&slot_name) {
                // Non-primitive Go param: narrow to its typed slot alias.
                let slot = GoTy::Named(format!("rt.{slot_name}"), vec![]);
                let from = e.ty.clone();
                largs.push(GoExpr::new(
                    GoExprKind::Coerce {
                        inner: Box::new(e),
                        from,
                        to: slot.clone(),
                        reason: CoerceReason::FfiReturn,
                    },
                    slot,
                ));
            } else {
                // Primitive Go param (no typed-slot alias). Coerce/convert the
                // arg to the wrapper's REAL Go param type (from its parsed
                // signature) — `rt.AsString` for `any → string`, a numeric
                // conversion `int64(x)` where Sky's `Int` (Go `int`) must widen
                // to the wrapper's `int64`, etc. A param type we don't recognise
                // (or an already-matching one) passes the value verbatim.
                let want = wrapper_params.get(pi).map(String::as_str);
                largs.push(self.coerce_ffi_prim_arg(e, want));
            }
            pi += 1;
        }
        let call = GoExpr::new(
            GoExprKind::Call(
                Box::new(GoExpr::new(GoExprKind::Ident(go.into()), GoTy::Any)),
                largs,
            ),
            GoTy::Any,
        );
        if *actual == GoTy::Any {
            call
        } else {
            GoExpr::new(
                GoExprKind::Coerce {
                    inner: Box::new(call),
                    from: GoTy::Any,
                    to: actual.clone(),
                    reason: CoerceReason::FfiReturn,
                },
                actual.clone(),
            )
        }
    }

    fn kernel_call(&mut self, go: &str, args: &[ExprId], actual: &GoTy) -> GoExpr {
        // `Dict.toList` on a typed-key Dict routes to the typed-key entry point
        // (`rt.Dict_toListIntKey` / `…FloatKey`) so keys re-parse to their Sky
        // type instead of leaking the runtime `map[string]V` string keys.
        let go = self.dict_tolist_specialised(go, args).unwrap_or(go);
        let largs: Vec<GoExpr> = args
            .iter()
            .map(|a| {
                let e = self.lower_expr(*a, &GoTy::Any);
                widen(e)
            })
            .collect();
        let call = GoExpr::new(
            GoExprKind::Call(
                Box::new(GoExpr::new(GoExprKind::Ident(go.into()), GoTy::Any)),
                largs,
            ),
            GoTy::Any,
        );
        if *actual == GoTy::Any {
            call
        } else {
            GoExpr::new(
                GoExprKind::Coerce {
                    inner: Box::new(call),
                    from: GoTy::Any,
                    to: actual.clone(),
                    reason: CoerceReason::FfiReturn,
                },
                actual.clone(),
            )
        }
    }

    fn lower_binop(&mut self, op: &str, lhs: ExprId, rhs: ExprId, actual: &GoTy) -> GoExpr {
        // string/appendable ++ → rt.Concat; :: → rt.List_cons; pipes desugar.
        match op {
            "++" => {
                let l = self.lower_expr(lhs, &GoTy::Any);
                let r = self.lower_expr(rhs, &GoTy::Any);
                // string concat is the dominant case in the CLI corpus.
                if actual == &GoTy::Bare(Prim::Str) || l.ty == GoTy::Bare(Prim::Str) {
                    // Go's `+` needs both operands statically `string`. An operand
                    // that stayed `any` (an untyped kernel return like
                    // `Db.getField`, whose Sky sig is loose) must narrow via
                    // `rt.AsString` at the concat boundary — otherwise `go build`
                    // rejects `string + any`.
                    return GoExpr::new(
                        GoExprKind::Binary(
                            GoBin::Add,
                            Box::new(coerce_to_str(l)),
                            Box::new(coerce_to_str(r)),
                        ),
                        GoTy::Bare(Prim::Str),
                    );
                }
                // `rt.Concat` returns Go `any` (list `++`); type the node `Any`
                // so the enclosing slot narrows via `rt.AsListT` rather than
                // feeding `any` into a `[]T` slot.
                call_rt("rt.Concat", vec![widen(l), widen(r)], GoTy::Any)
            }
            "::" => {
                let elem = actual.elem_ty();
                let l = self.lower_expr(lhs, &elem);
                let r = self.lower_expr(rhs, actual);
                // x :: xs  →  rt.List_cons(x, xs). `rt.List_cons` returns Go
                // `any`, so the node's type is `Any`; the enclosing slot
                // (`lower_expr`) coerces to the expected `[]T` via `rt.AsListT`.
                // Typing it `[]T` here would suppress that coercion and feed an
                // `any` value into a `[]T` slot (`go build` rejects it).
                call_rt("rt.List_cons", vec![widen(l), widen(r)], GoTy::Any)
            }
            "|>" => {
                // a |> f  ==  f a. Flatten when `f` is a partial application
                // (`Result.withDefault "x"`) so kernels/Sky funcs (uncurried in
                // Go) receive the full arg list: `f(partial…, a)`.
                self.lower_pipe(rhs, lhs, actual)
            }
            "<|" => {
                // f <| a  ==  f a. Flatten `f`'s spine similarly.
                self.lower_pipe(lhs, rhs, actual)
            }
            _ => {
                let l = self.lower_expr(lhs, &GoTy::Any);
                let r = self.lower_expr(rhs, &GoTy::Any);
                if let Some(b) = go_binop(op) {
                    // Both operands statically a MATCHING primitive → Go's native
                    // operator (`count + 1`, `n < 10`, `s == "q"`). Otherwise an
                    // operand is `any` (a kernel / `rt.IntDiv` return, a case
                    // binder that stayed flex): Go rejects `int + any` / `any >
                    // any`, so route through the runtime's any-based helper
                    // (`rt.Add`/`rt.Lte`/…) — result `any`, narrowed at the use
                    // slot. Matches the oracle (`rt.Sub(7, rt.IntDiv(sq, 8))`).
                    let both_prim = matches!(l.ty, GoTy::Bare(_))
                        && matches!(r.ty, GoTy::Bare(_))
                        && l.ty == r.ty;
                    // Float `+`/`-`/`*` must NOT emit Go's native operator: on
                    // arm64 Go contracts `a*b + c` into a single-rounding FMA,
                    // producing a last-ulp-different result from the oracle,
                    // which routes float arithmetic through `rt.Add`/`rt.Mul`
                    // (each returns a rounded float64 boxed in `any`, so the
                    // intermediate product is rounded before the add — no FMA
                    // fusion). Route float arithmetic through the same rt
                    // helpers so results are byte-identical. Int arithmetic is
                    // exact (native == rt), and float comparisons yield bool
                    // (no fusion), so both stay native.
                    let float_arith =
                        l.ty == GoTy::Bare(Prim::Float) && matches!(op, "+" | "-" | "*");
                    if both_prim && !float_arith {
                        let ty = if is_cmp(op) { GoTy::Bare(Prim::Bool) } else { l.ty.clone() };
                        return GoExpr::new(GoExprKind::Binary(b, Box::new(l), Box::new(r)), ty);
                    }
                    if op == "&&" || op == "||" {
                        // Logical ops need bool operands; coerce, keep the Go op.
                        let lb = self.coerce_if_needed(l, &GoTy::Bare(Prim::Bool));
                        let rb = self.coerce_if_needed(r, &GoTy::Bare(Prim::Bool));
                        return GoExpr::new(
                            GoExprKind::Binary(b, Box::new(lb), Box::new(rb)),
                            GoTy::Bare(Prim::Bool),
                        );
                    }
                    let helper = match op {
                        "+" => "rt.Add",
                        "-" => "rt.Sub",
                        "*" => "rt.Mul",
                        "==" => "rt.Eq",
                        "/=" => "rt.NotEq",
                        "<" => "rt.Lt",
                        ">" => "rt.Gt",
                        "<=" => "rt.Lte",
                        ">=" => "rt.Gte",
                        _ => "rt.Add",
                    };
                    call_rt(helper, vec![widen(l), widen(r)], GoTy::Any)
                } else {
                    let rtname = match op {
                        "//" => "rt.IntDiv",
                        "%" => "rt.Rem",
                        "/" => "rt.Div",
                        "^" => "rt.Pow",
                        ">>" | "<<" => "rt.Compose",
                        _ => "rt.Add",
                    };
                    // These runtime helpers all return Go `any` (they type-switch
                    // on their `any` operands). Typing the node as its TRUE Go
                    // static type (`any`) rather than the Sky-inferred `actual`
                    // lets `coerce_if_needed` narrow it (`rt.CoerceInt(...)`) at
                    // the use site — matching the oracle. Typing it `actual`
                    // (e.g. `int`) is a lie: Go's `:=` infers `any` and the value
                    // then fails to satisfy an `int` slot.
                    call_rt(rtname, vec![widen(l), widen(r)], GoTy::Any)
                }
            }
        }
    }

    fn lower_if(&mut self, arms: &[(ExprId, ExprId)], els: ExprId, actual: &GoTy) -> GoExpr {
        // build a typed IIFE with nested ifs (works as an expression).
        let arms = arms.to_vec();
        let mut stmts: Vec<GoStmt> = Vec::new();
        self.build_if_chain(&arms, els, actual, &mut stmts);
        GoExpr::new(GoExprKind::Block(stmts), actual.clone())
    }

    fn build_if_chain(
        &mut self,
        arms: &[(ExprId, ExprId)],
        els: ExprId,
        actual: &GoTy,
        out: &mut Vec<GoStmt>,
    ) {
        if let Some(((cond, then), rest)) = arms.split_first() {
            let c = self.lower_expr(*cond, &GoTy::Bare(Prim::Bool));
            let t = self.lower_expr(*then, actual);
            let mut els_stmts: Vec<GoStmt> = Vec::new();
            self.build_if_chain(rest, els, actual, &mut els_stmts);
            out.push(GoStmt::If(c, vec![GoStmt::Return(Some(t))], els_stmts));
        } else {
            let e = self.lower_expr(els, actual);
            out.push(GoStmt::Return(Some(e)));
        }
    }

    fn lower_let_expr(&mut self, defs: &[hir::LocalDef], body: ExprId, actual: &GoTy) -> GoExpr {
        let defs = defs.to_vec();
        // Pre-register every binder's Go name BEFORE lowering any body, so a
        // forward reference (`let a = b + 1; b = 5 in a` — Sky allows these)
        // resolves to the SAME name the binder will emit, instead of the
        // `undefined: v_<id>` fallback.
        for d in &defs {
            // Pre-register EVERY binder (a tuple/record destructure introduces
            // several), so a forward reference to any of them resolves to the
            // name its binding will emit.
            for (bn, lid) in &d.binders {
                let _ = self.fresh_local_named(*lid, Some(bn.as_str()));
            }
        }
        let mut stmts: Vec<GoStmt> = Vec::new();
        for d in &defs {
            self.lower_let_def(d, &mut stmts);
        }
        let b = self.lower_expr(body, actual);
        stmts.push(GoStmt::Return(Some(b)));
        GoExpr::new(GoExprKind::Block(stmts), actual.clone())
    }

    fn lower_record(&mut self, fields: &[(Name, ExprId)], actual: &GoTy) -> GoExpr {
        // resolve the struct name from `actual` (Named(...)); order fields by the
        // Go struct's field order via capitalisation match.
        //
        // When `actual` is NOT a nominal `_R` record (e.g. an anonymous record
        // flowing into an `any`-typed kernel slot — the TEA cfg record passed to
        // `rt.Cli_program`/`Tui_app`/`Live_app`/`Webview_app`), there is no
        // declared struct type to key the composite literal against. The oracle
        // emits an *anonymous* Go struct with every field typed `any`
        // (`struct{ Init any; Update any; … }{Init: …, …}`); the runtime
        // reflect-dispatches those fields. Reproduce that exactly.
        let nominal = matches!(actual, GoTy::Named(_, _));
        if let GoTy::Named(n, _) = actual {
            self.used_types.insert(n.clone());
        }
        // declared field types for this record `_R`, if known.
        let field_tys: HashMap<String, Ty> = match actual {
            GoTy::Named(n, _) => self
                .record_fields
                .get(n)
                .map(|fs| fs.iter().map(|(n, t)| (n.clone(), t.clone())).collect())
                .unwrap_or_default(),
            _ => HashMap::new(),
        };
        // A concrete anonymous-struct slot: `actual` is a `GoTy::Struct` none of
        // whose fields is a function or `any`. This is the JSON-decoder record
        // whose enclosing lambda return type is `struct{ Done bool; Id int; Title
        // string }` (06-json) — the field values must lower to their CONCRETE Go
        // field types and the literal must render those same concrete types, so
        // the rendered `struct{…}{…}` matches the claimed slot type (otherwise an
        // all-`any` literal is claimed to be a `struct{…bool…}` and `go build`
        // rejects it). Records with function fields (the TEA cfg passed to
        // Live_app/Tui_app) DELIBERATELY stay all-`any` — the runtime
        // reflect-dispatches them and the oracle emits all-`any` there.
        let concrete_struct: Option<Vec<(String, GoTy)>> = match actual {
            GoTy::Struct(fts)
                if !fts.is_empty()
                    && fts
                        .iter()
                        .all(|(_, t)| !matches!(t, GoTy::Func(_, _) | GoTy::Any)) =>
            {
                Some(
                    fts.iter()
                        .map(|(n, t)| (n.as_str().to_string(), t.clone()))
                        .collect(),
                )
            }
            _ => None,
        };
        let fs: Vec<(String, GoExpr)> = fields
            .iter()
            .map(|(n, v)| {
                let cap = capitalize(n.as_str());
                let expected = if let Some(cs) = &concrete_struct {
                    cs.iter()
                        .find(|(fn_, _)| *fn_ == cap)
                        .map(|(_, t)| t.clone())
                        .unwrap_or(GoTy::Any)
                } else {
                    field_tys.get(&cap).map(|t| self.goty(t)).unwrap_or(GoTy::Any)
                };
                let lowered = self.lower_expr(*v, &expected);
                (cap, lowered)
            })
            .collect();
        let go_name = if nominal {
            match actual {
                GoTy::Named(n, _) => n.clone(),
                _ => unreachable!(),
            }
        } else if concrete_struct.is_some() {
            // Render the concrete struct type so the literal's Go type equals the
            // claimed `actual` (keyed-literal order is field-name independent).
            render_goty(actual)
        } else {
            // Anonymous struct type with every field `any`, field names sorted
            // (L4 deterministic emission). Keyed composite-literal order is
            // independent of the type decl's field order, so sorting is safe.
            let mut names: Vec<String> = fs.iter().map(|(c, _)| c.clone()).collect();
            names.sort();
            let decls = names
                .iter()
                .map(|c| format!("{c} any"))
                .collect::<Vec<_>>()
                .join("; ");
            format!("struct{{ {decls} }}")
        };
        GoExpr::new(GoExprKind::StructLit(go_name, fs), actual.clone())
    }

    fn lower_update(&mut self, base: ExprId, fields: &[(Name, ExprId)], actual: &GoTy) -> GoExpr {
        // { base | f = v } → func() T { _u := base; _u.F = v; return _u }()
        // A record update yields EXACTLY the base record's Go type (`_u := base`
        // copies it whole), NOT the update expression's body-inferred type
        // (which is a *subset* record when only some fields are read/written).
        // Take the base's own lowered type as the block/`_u` type; the outer
        // `lower_expr` coerces the block to the caller's `expected` if needed.
        let b = self.lower_expr(base, &GoTy::Any);
        let uty = b.ty.clone();
        // Declared field types of the base record `_R` — so each updated field's
        // value lowers with its Go field type as `expected` (a kernel `any`
        // return like `rt.Basics_not(...)` is then coerced to the field's `bool`,
        // not fed raw into a typed struct field — `go build` rejects the latter).
        let field_tys: HashMap<String, Ty> = match &uty {
            GoTy::Named(n, _) => self
                .record_fields
                .get(n)
                .map(|fs| fs.iter().map(|(fn_, t)| (fn_.clone(), t.clone())).collect())
                .unwrap_or_default(),
            _ => HashMap::new(),
        };
        let mut stmts = vec![GoStmt::Short("_u".into(), b)];
        let uref = GoExpr::new(GoExprKind::Ident("_u".into()), uty.clone());
        for (n, v) in fields {
            let cap = capitalize(n.as_str());
            let expected = field_tys.get(&cap).map(|t| self.goty(t)).unwrap_or(GoTy::Any);
            let lowered = self.lower_expr(*v, &expected);
            stmts.push(GoStmt::AssignField(uref.clone(), cap, lowered));
        }
        stmts.push(GoStmt::Return(Some(uref)));
        // Return the block typed as the base record; the caller's `lower_expr`
        // reconciles against the true `expected` slot type. (The `actual`
        // argument is this node's *body-inferred* subset type — deliberately
        // not used as the result type.)
        let _ = actual;
        GoExpr::new(GoExprKind::Block(stmts), uty)
    }

    fn lower_tuple(&mut self, elems: &[ExprId], actual: &GoTy) -> GoExpr {
        let tys: Vec<GoTy> = match actual {
            GoTy::Tuple(ts) => ts.clone(),
            _ => vec![GoTy::Any; elems.len()],
        };
        let args: Vec<GoExpr> = elems
            .iter()
            .enumerate()
            .map(|(i, e)| self.lower_expr(*e, tys.get(i).unwrap_or(&GoTy::Any)))
            .collect();
        let ctor = match elems.len() {
            2 => "rt.MkT2",
            3 => "rt.MkT3",
            _ => "rt.MkTupleN",
        };
        // rt.T2 literal is a struct; use the named struct literal form.
        let go = format!("rt.T{}", elems.len());
        if elems.len() == 2 || elems.len() == 3 {
            // `rt.T2`/`rt.T3` struct fields are V0, V1, … (see runtime rt.go
            // `type T2[A, B any] struct { V0 A; V1 B }`).
            let fields: Vec<(String, GoExpr)> = args
                .into_iter()
                .enumerate()
                .map(|(i, a)| (format!("V{i}"), a))
                .collect();
            let tname = GoTy::Tuple(tys);
            return GoExpr::new(
                GoExprKind::StructLit(format!("{go}{}", tuple_type_args(&tname)), fields),
                actual.clone(),
            );
        }
        call_rt(ctor, args, actual.clone())
    }

    fn lower_list(&mut self, elems: &[ExprId], actual: &GoTy) -> GoExpr {
        let elem = actual.elem_ty();
        let args: Vec<GoExpr> = elems.iter().map(|e| self.lower_expr(*e, &elem)).collect();
        GoExpr::new(GoExprKind::SliceLit(elem, args), actual.clone())
    }

    fn lower_lambda(&mut self, params: &[PatId], body: ExprId, actual: &GoTy) -> GoExpr {
        let params = params.to_vec();
        let (pt, rt) = match actual {
            GoTy::Func(ps, r) => (ps.clone(), (**r).clone()),
            _ => (vec![GoTy::Any; params.len()], GoTy::Any),
        };
        let mut gparams = Vec::new();
        let mut destructure: Vec<GoStmt> = Vec::new();
        let mut elem_pinned = false;
        for (i, p) in params.iter().enumerate() {
            let mut ty = pt.get(i).cloned().unwrap_or(GoTy::Any);
            // A closure param whose body-inferred Go type collapsed to an ANONYMOUS
            // subset `struct{…}` (only the fields the closure reads: `\r ->
            // r.tx.account`) never matches the runtime value a LIST COMBINATOR
            // passes — the real element is the list's own Go type. `closure_elem`
            // (set at the combinator call site) carries it; pin the FIRST such subset
            // param to it (the element position for map/filter/foldl `\elem …` and
            // indexedMap `\idx elem …` — idx is `int`, not a struct, so the element
            // is still the first struct). An erased `any` element → field access
            // routes through `rt.Field` (see `Expr::Access`); a nominal `_R` element
            // → typed field access + record update stay valid. When the list element
            // IS that same anonymous struct (18-job-queue: `[]struct{…}` field), the
            // pin is a no-op — byte-identical to before. Non-combinator lambdas keep
            // their struct param (no `closure_elem`) — no regression.
            if !elem_pinned && matches!(ty, GoTy::Struct(_)) {
                if let Some(elem) = self.closure_elem.clone() {
                    ty = elem;
                    elem_pinned = true;
                }
            }
            let name = match &self.body.pats[*p] {
                Pattern::Var(id) => {
                    let n = self.fresh_local_named(*id, None);
                    self.local_tys.insert(*id, ty.clone());
                    n
                }
                Pattern::Anything | Pattern::Unit => "_".to_string(),
                _ => {
                    // Destructured lambda param (`\(_, result) -> …`): bind a
                    // temp, then emit the inner bindings at the body head — the
                    // same shape as `bind_param` for top-level defs.
                    let n = self.fresh_temp();
                    let subj = GoExpr::new(GoExprKind::Ident(n.clone()), ty.clone());
                    let (_cond, binds) = self.pattern_test(&subj, &ty, *p);
                    destructure.extend(binds);
                    n
                }
            };
            gparams.push(GoParam { name, ty });
        }
        // The element pin is consumed by THIS lambda's params only; a nested lambda
        // in the body is a different closure (its own combinator call sets its own
        // `closure_elem`). Clear so the body doesn't inherit this one.
        self.closure_elem = None;
        let b = self.lower_expr(body, &rt);
        let mut stmts = destructure;
        stmts.push(GoStmt::Return(Some(b)));
        GoExpr::new(
            GoExprKind::FuncLit(gparams, rt, stmts),
            actual.clone(),
        )
    }

    fn lower_case(&mut self, subject: ExprId, branches: &[CaseBranch], actual: &GoTy) -> GoExpr {
        let branches = branches.to_vec();
        let subj = self.lower_expr(subject, &GoTy::Any);
        // `_subj := subj` binds `_subj` to EXACTLY the lowered subject's Go type,
        // so that is the authoritative type for payload extraction — it can be
        // more concrete than the caller's recorded inference (a callee whose
        // recovered return is `SkyResult[E, []map]` while the call site inferred
        // `SkyResult[E, []any]`). Trusting the recorded type there mislabels the
        // `Ok rows` binder `[]any` and `return rows` skips its `[]map → []any`
        // coercion (example 17). Fall back to the recorded type only when the
        // lowered subject is itself `any`.
        let mut subj_ty = if subj.ty != GoTy::Any {
            subj.ty.clone()
        } else {
            self.expr_ty(subject)
        };
        // When the subject's Go type is `any` (a record field / FFI value the
        // per-def inference couldn't pin) but the branches match a USER ADT /
        // iota constructor, `_subj.Tag` / `_subj == Iota_Const` fail to compile
        // (`any` has no `.Tag`; `any == int` is a type mismatch). Coerce the
        // subject to the nominal the patterns imply so tag/equality tests type.
        let subj = if subj.ty == GoTy::Any && subj_ty != GoTy::Any {
            // The emitted subject VALUE is `any` (e.g. a call to a Sky helper
            // whose Go return erased to `any` because its body forwards a raw
            // FFI value) but caller-side inference pinned a concrete
            // `subj_ty` (`rt.SkyResult[…]` / a nominal ADT). `_subj.Tag` /
            // field reads are generated against `subj_ty`, so the bound `_subj`
            // must actually BE that type — coerce the `any` value up to it.
            // Common in FFI-heavy code where the callee's return stayed `any`
            // but the call expression's HM type is a Result/Maybe (doc 09).
            self.coerce_if_needed(subj, &subj_ty)
        } else if subj_ty == GoTy::Any {
            if let Some(nom) = self.pattern_nominal(&branches) {
                subj_ty = nom.clone();
                self.coerce_if_needed(subj, &nom)
            } else if let Some(cont) = self.pattern_container(&branches) {
                // Builtin container patterns (Ok/Err, Just/Nothing) on an
                // `any`-typed subject — e.g. a Sky function whose recovered
                // return stayed `any`, or an FFI value flowing into a `case`.
                // `_subj.Tag` / `.OkValue` need a concrete `rt.SkyResult` /
                // `rt.SkyMaybe`; coerce to the element-erased shape so the tag
                // + payload reads type-check. Element types stay `any`; each
                // arm's binder re-coerces to its own inferred type (2538).
                subj_ty = cont.clone();
                self.coerce_if_needed(subj, &cont)
            } else {
                subj
            }
        } else {
            subj
        };
        let mut stmts = vec![GoStmt::Short("_subj".into(), subj)];
        let subj_ref = GoExpr::new(GoExprKind::Ident("_subj".into()), subj_ty.clone());
        for br in &branches {
            self.lower_case_branch(&subj_ref, &subj_ty, br, actual, &mut stmts);
        }
        // fallthrough guard (exhaustiveness should prevent reaching here).
        stmts.push(GoStmt::Expr(GoExpr::new(
            GoExprKind::Ident("panic(rt.Unreachable(\"case\"))".into()),
            GoTy::Unit,
        )));
        GoExpr::new(GoExprKind::Block(stmts), actual.clone())
    }

    /// The nominal Go type implied by a case's branch patterns — the owning ADT
    /// / iota type of the first USER constructor pattern found. Builtin
    /// container patterns (Ok/Err/Just/Nothing/True/False) are skipped; they
    /// route through `rt.SkyResult` / `rt.SkyMaybe` / `bool`, not a bare nominal.
    fn pattern_nominal(&self, branches: &[CaseBranch]) -> Option<GoTy> {
        for br in branches {
            if let Pattern::Ctor { name, .. } = &self.body.pats[br.pat] {
                let cname = name.as_str();
                if matches!(cname, "Ok" | "Err" | "Just" | "Nothing" | "True" | "False") {
                    continue;
                }
                if let Some((go_type, _kind)) = self.ctor_owner.get(cname) {
                    return Some(GoTy::Named(go_type.clone(), vec![]));
                }
            }
        }
        None
    }

    /// The builtin container Go type implied by a case's branch patterns when
    /// the subject is `any`: `rt.SkyResult[any, any]` for Ok/Err, `rt.SkyMaybe
    /// [any]` for Just/Nothing. Element types are erased to `any` — the exact
    /// element inference is unavailable when the subject itself lowered to
    /// `any`, and each arm's binder re-coerces to its own recorded type. Returns
    /// `None` when no container constructor appears (a user ADT / literal case,
    /// handled by `pattern_nominal`).
    fn pattern_container(&self, branches: &[CaseBranch]) -> Option<GoTy> {
        for br in branches {
            if let Pattern::Ctor { name, .. } = &self.body.pats[br.pat] {
                match name.as_str() {
                    "Ok" | "Err" => {
                        return Some(GoTy::Named(
                            "rt.SkyResult".into(),
                            vec![GoTy::Any, GoTy::Any],
                        ));
                    }
                    "Just" | "Nothing" => {
                        return Some(GoTy::Named("rt.SkyMaybe".into(), vec![GoTy::Any]));
                    }
                    _ => {}
                }
            }
        }
        None
    }

    fn lower_case_branch(
        &mut self,
        subj: &GoExpr,
        subj_ty: &GoTy,
        br: &CaseBranch,
        actual: &GoTy,
        out: &mut Vec<GoStmt>,
    ) {
        // Sealed-ADT top-level ctor pattern → idiomatic comma-ok type-switch case:
        //   `if _vN, _okN := _subj.(Union_Ctor_V); _okN { <typed .V{i} binds>; body }`
        // Typed dispatch — the variant struct binds once, field reads are direct
        // typed `_vN.V{i}` (no `rt.Coerce` on the payload).
        if let Pattern::Ctor { name, args, .. } = &self.body.pats[br.pat] {
            let cname = name.as_str();
            if !is_builtin_ctor(cname) {
                if let Some(union) = self.sealed_adt_union(cname, subj_ty) {
                    let args = args.clone();
                    let vstruct_ty = GoTy::Named(format!("{union}_{cname}_V"), vec![]);
                    let binder_name = format!("_v{}", self.local_counter);
                    self.local_counter += 1;
                    let ok = format!("_ok{}", self.local_counter);
                    self.local_counter += 1;
                    let struct_val =
                        GoExpr::new(GoExprKind::Ident(binder_name.clone()), vstruct_ty.clone());
                    let (subcond, binds) =
                        self.adt_variant_binds(&struct_val, &union, cname, &args);
                    let binder = if binds.is_empty() && subcond.is_none() {
                        "_".to_string()
                    } else {
                        binder_name
                    };
                    let discards = discard_binds(&binds);
                    let mut then_inner: Vec<GoStmt> = binds;
                    then_inner.extend(discards);
                    let body = self.lower_expr(br.body, actual);
                    then_inner.push(GoStmt::Return(Some(body)));
                    let then = match subcond {
                        Some(c) => vec![GoStmt::If(c, then_inner, vec![])],
                        None => then_inner,
                    };
                    out.push(GoStmt::IfTypeAssert {
                        binder,
                        ok,
                        subj: subj.clone(),
                        ty: vstruct_ty,
                        then,
                    });
                    return;
                }
            }
        }
        let (cond, binds) = self.pattern_test(subj, subj_ty, br.pat);
        // Discard every pattern-bound name (`_ = v`) so an arm that ignores its
        // binding (`Just _ -> …` bound as `v := _subj.JustValue`) does not trip
        // Go's "declared and not used". `_ = v` is always legal — harmless when
        // the var IS used. Mirrors the oracle's per-binding discard.
        let discards = discard_binds(&binds);
        let mut then: Vec<GoStmt> = binds;
        then.extend(discards);
        let body = self.lower_expr(br.body, actual);
        then.push(GoStmt::Return(Some(body)));
        match cond {
            Some(c) => out.push(GoStmt::If(c, then, vec![])),
            None => out.extend(then),
        }
    }

    /// Build a boolean test for a pattern against `subj`, plus the binding stmts
    /// that run when it matches. `None` cond = irrefutable (wildcard/var).
    fn pattern_test(
        &mut self,
        subj: &GoExpr,
        subj_ty: &GoTy,
        p: PatId,
    ) -> (Option<GoExpr>, Vec<GoStmt>) {
        match &self.body.pats[p] {
            Pattern::Anything | Pattern::Unit => (None, vec![]),
            Pattern::Var(id) => {
                let name = self.fresh_local_named(*id, None);
                // When the payload is extracted as `any` (a container of
                // `SkyResult[any,any]`/`SkyMaybe[any]` whose element inference
                // stayed flex) but the binder's OWN inferred type is concrete
                // (`Ok profile` used as `profile.Name` → `profile : Profile`),
                // coerce the extracted value to the binder's type so field /
                // ctor access type-checks. Narrowing-only: guarded on the
                // subject being `any`, so typed containers keep their exact
                // element type (and the coercion elides via `from == to`).
                let recorded = self.types.locals.get(id).cloned().map(|t| self.goty(&t));
                let bound = match recorded {
                    Some(rt) if subj.ty == GoTy::Any && rt != GoTy::Any => {
                        self.coerce_if_needed(subj.clone(), &rt)
                    }
                    _ => subj.clone(),
                };
                // Register the bound var's REAL Go type, so a later reference
                // coerces to its use-slot rather than trusting the slot type.
                // Critical for cons tails bound from `rt.SkyTailSlice` (`[]any`)
                // that flow into a `[]T` param.
                self.local_tys.insert(*id, bound.ty.clone());
                (None, vec![GoStmt::Short(name, bound)])
            }
            Pattern::Int(n) => (
                Some(GoExpr::new(
                    GoExprKind::Binary(
                        GoBin::Eq,
                        Box::new(subj.clone()),
                        Box::new(GoExpr::new(GoExprKind::IntLit(*n), GoTy::Bare(Prim::Int))),
                    ),
                    GoTy::Bare(Prim::Bool),
                )),
                vec![],
            ),
            Pattern::Str(s) => (
                Some(GoExpr::new(
                    GoExprKind::Binary(
                        GoBin::Eq,
                        Box::new(subj.clone()),
                        Box::new(GoExpr::new(
                            GoExprKind::StrLit(s.to_string()),
                            GoTy::Bare(Prim::Str),
                        )),
                    ),
                    GoTy::Bare(Prim::Bool),
                )),
                vec![],
            ),
            Pattern::Bool(b) => (
                Some(GoExpr::new(
                    GoExprKind::Binary(
                        GoBin::Eq,
                        Box::new(subj.clone()),
                        Box::new(GoExpr::new(GoExprKind::BoolLit(*b), GoTy::Bare(Prim::Bool))),
                    ),
                    GoTy::Bare(Prim::Bool),
                )),
                vec![],
            ),
            Pattern::Ctor { ctor, name, args } => {
                self.ctor_pattern(subj, subj_ty, ctor.as_ref().map(|c| c.def), name.as_str(), args)
            }
            Pattern::Alias(inner, id) => {
                let inner = *inner;
                let name = self.fresh_local_named(*id, None);
                let (c, mut binds) = self.pattern_test(subj, subj_ty, inner);
                binds.insert(0, GoStmt::Short(name, subj.clone()));
                (c, binds)
            }
            Pattern::Cons(h, t) => {
                let (h, t) = (*h, *t);
                let elem = subj_ty.elem_ty();
                // guard: at least one element.
                let cond = GoExpr::new(
                    GoExprKind::Binary(
                        GoBin::Ge,
                        Box::new(call_rt("rt.SkyLen", vec![subj.clone()], GoTy::Bare(Prim::Int))),
                        Box::new(int_lit(1)),
                    ),
                    GoTy::Bare(Prim::Bool),
                );
                let head_raw =
                    call_rt("rt.SkyElem", vec![subj.clone(), int_lit(0)], GoTy::Any);
                let head = self.coerce_if_needed(head_raw, &elem);
                // `rt.SkyTailSlice` returns `[]any`; type it as such so a use
                // in a `[]T` slot narrows via `rt.AsListT`.
                let tail = call_rt(
                    "rt.SkyTailSlice",
                    vec![subj.clone()],
                    GoTy::Slice(Box::new(GoTy::Any)),
                );
                let (ch, mut binds) = self.pattern_test(&head, &elem, h);
                let (ct, tb) = self.pattern_test(&tail, subj_ty, t);
                binds.extend(tb);
                (and_opt(Some(cond), and_opt(ch, ct)), binds)
            }
            Pattern::List(pats) => {
                let pats = pats.clone();
                let elem = subj_ty.elem_ty();
                // guard: exact length match.
                let mut cond = Some(GoExpr::new(
                    GoExprKind::Binary(
                        GoBin::Eq,
                        Box::new(call_rt("rt.SkyLen", vec![subj.clone()], GoTy::Bare(Prim::Int))),
                        Box::new(int_lit(pats.len() as i64)),
                    ),
                    GoTy::Bare(Prim::Bool),
                ));
                let mut binds = Vec::new();
                for (i, sp) in pats.iter().enumerate() {
                    let raw =
                        call_rt("rt.SkyElem", vec![subj.clone(), int_lit(i as i64)], GoTy::Any);
                    let el = self.coerce_if_needed(raw, &elem);
                    let (c, b) = self.pattern_test(&el, &elem, *sp);
                    cond = and_opt(cond, c);
                    binds.extend(b);
                }
                (cond, binds)
            }
            Pattern::Tuple(pats) => {
                let pats = pats.clone();
                let elem_tys: Vec<GoTy> = match subj_ty {
                    GoTy::Tuple(ts) => ts.clone(),
                    _ => vec![GoTy::Any; pats.len()],
                };
                let mut cond = None;
                let mut binds = Vec::new();
                for (i, sp) in pats.iter().enumerate() {
                    let ety = elem_tys.get(i).cloned().unwrap_or(GoTy::Any);
                    // `.V{i}` is `any` on the runtime `rt.T2[any,any]` — coerce
                    // to the element's concrete type so a bound var carries its
                    // real type (e.g. an ADT for a nested `case`).
                    let raw = GoExpr::new(
                        GoExprKind::Selector(Box::new(subj.clone()), format!("V{i}")),
                        GoTy::Any,
                    );
                    let field = self.coerce_if_needed(raw, &ety);
                    let (c, b) = self.pattern_test(&field, &ety, *sp);
                    cond = and_opt(cond, c);
                    binds.extend(b);
                }
                (cond, binds)
            }
            Pattern::Record(fields) => {
                let fields = fields.clone();
                // field Go-types from the nominal `_R` struct when known.
                let field_tys: HashMap<String, GoTy> = match subj_ty {
                    GoTy::Named(gn, _) => self
                        .record_fields
                        .get(gn)
                        .map(|fs| {
                            fs.iter()
                                .map(|(cap, t)| (cap.clone(), sky_ty_to_go_in(t, self.env, Some(&self.cur_module))))
                                .collect()
                        })
                        .unwrap_or_default(),
                    _ => HashMap::new(),
                };
                let mut binds = Vec::new();
                for (fname, lid) in &fields {
                    let cap = capitalize(fname.as_str());
                    let fty = field_tys.get(&cap).cloned().unwrap_or(GoTy::Any);
                    let name = self.fresh_local_named(*lid, Some(fname.as_str()));
                    self.local_tys.insert(*lid, fty.clone());
                    binds.push(GoStmt::Short(
                        name,
                        GoExpr::new(
                            GoExprKind::Selector(Box::new(subj.clone()), cap),
                            fty,
                        ),
                    ));
                }
                (None, binds)
            }
            Pattern::Chr(s) => (
                Some(GoExpr::new(
                    GoExprKind::Binary(
                        GoBin::Eq,
                        Box::new(subj.clone()),
                        Box::new(GoExpr::new(
                            GoExprKind::StrLit(s.to_string()),
                            GoTy::Bare(Prim::Str),
                        )),
                    ),
                    GoTy::Bare(Prim::Bool),
                )),
                vec![],
            ),
            Pattern::Float(_) | Pattern::Error => {
                self.warnings
                    .push("unsupported pattern in case — treated as wildcard".into());
                (None, vec![])
            }
        }
    }

    fn ctor_pattern(
        &mut self,
        subj: &GoExpr,
        _subj_ty: &GoTy,
        cdef: Option<DefId>,
        cname: &str,
        args: &[PatId],
    ) -> (Option<GoExpr>, Vec<GoStmt>) {
        let _ = cdef;
        let args = args.to_vec();
        // Payload types from the container's Go type (rt.SkyResult[E,A] etc.).
        let (a_ty, e_ty) = match _subj_ty {
            GoTy::Named(n, ts) if n == "rt.SkyResult" && ts.len() == 2 => {
                (ts[1].clone(), ts[0].clone())
            }
            GoTy::Named(n, ts) if n == "rt.SkyMaybe" && ts.len() == 1 => (ts[0].clone(), GoTy::Any),
            _ => (GoTy::Any, GoTy::Any),
        };
        match cname {
            "Ok" | "Just" => {
                let field = if cname == "Ok" { "OkValue" } else { "JustValue" };
                let (subc, binds) = self.bind_field_pat(subj, &args, field, &a_ty);
                (and_opt(Some(tag_eq(subj, 0)), subc), binds)
            }
            "Err" => {
                let (subc, binds) = self.bind_field_pat(subj, &args, "ErrValue", &e_ty);
                (and_opt(Some(tag_eq(subj, 1)), subc), binds)
            }
            "Nothing" => (Some(tag_eq(subj, 1)), vec![]),
            "True" => (Some(subj.clone()), vec![]),
            "False" => (
                Some(GoExpr::new(
                    GoExprKind::Binary(
                        GoBin::Eq,
                        Box::new(subj.clone()),
                        Box::new(GoExpr::new(GoExprKind::BoolLit(false), GoTy::Bare(Prim::Bool))),
                    ),
                    GoTy::Bare(Prim::Bool),
                )),
                vec![],
            ),
            _ => {
                // Disambiguate the owning union by the subject's nominal type
                // when known (`_subj_ty = Named(gt,_)`) — the bare-name
                // `ctor_owner` map collides for a ctor name shared across two
                // unions (`AlignLeft`). Fall back to the bare lookup otherwise.
                let owner: Option<(String, NominalKind)> = match _subj_ty {
                    GoTy::Named(gt, _) => self
                        .ctor_in_union
                        .get(&(gt.clone(), cname.to_string()))
                        .map(|(k, _)| (gt.clone(), *k))
                        .or_else(|| self.ctor_owner.get(cname).cloned()),
                    _ => self.ctor_owner.get(cname).cloned(),
                };
                if let Some((go, NominalKind::Iota)) = &owner {
                    let cond = GoExpr::new(
                        GoExprKind::Binary(
                            GoBin::Eq,
                            Box::new(subj.clone()),
                            Box::new(GoExpr::new(
                                GoExprKind::Ident(format!("{go}_{cname}")),
                                GoTy::Any,
                            )),
                        ),
                        GoTy::Bare(Prim::Bool),
                    );
                    return (Some(cond), vec![]);
                }
                // Sealed-ADT NESTED ctor pattern (`SendMessage (Inner x)` reached
                // via recursion). Tag test via `rt.EnumTagIs` (works whether `subj`
                // is the sealed interface or `any`; NOT a coerce) short-circuits the
                // typed `subj.(Union_Ctor_V).V{i}` field reads — so those reads are
                // only evaluated when the tag matched.
                if let Some((go, NominalKind::Adt)) = &owner {
                    if self.sealed_unions.contains(go) {
                        let tag = match _subj_ty {
                            GoTy::Named(gt, _) => self
                                .ctor_in_union
                                .get(&(gt.clone(), cname.to_string()))
                                .map(|(_, t)| *t)
                                .or_else(|| self.ctor_tag.get(cname).copied())
                                .unwrap_or(0),
                            _ => self.ctor_tag.get(cname).copied().unwrap_or(0),
                        };
                        let tag_cond = call_rt(
                            "rt.EnumTagIs",
                            vec![subj.clone(), int_lit(tag as i64)],
                            GoTy::Bare(Prim::Bool),
                        );
                        let vstruct_ty = GoTy::Named(format!("{go}_{cname}_V"), vec![]);
                        let struct_val = GoExpr::new(
                            GoExprKind::TypeAssert(Box::new(subj.clone()), vstruct_ty.clone()),
                            vstruct_ty,
                        );
                        let (subcond, binds) = self.adt_variant_binds(&struct_val, go, cname, &args);
                        return (and_opt(Some(tag_cond), subcond), binds);
                    }
                }
                // sealed ADT: match by declaration-order tag; bind Fields[i].
                // Prefer the union-scoped tag when the subject pins the union.
                let tag = match _subj_ty {
                    GoTy::Named(gt, _) => self
                        .ctor_in_union
                        .get(&(gt.clone(), cname.to_string()))
                        .map(|(_, t)| *t)
                        .or_else(|| self.ctor_tag.get(cname).copied())
                        .unwrap_or(0),
                    _ => self.ctor_tag.get(cname).copied().unwrap_or(0),
                };
                let mut cond = Some(tag_eq(subj, tag));
                let mut binds = Vec::new();
                for (i, a) in args.iter().enumerate() {
                    // Field i's target type: a bound var carries its own type; a
                    // nested pattern narrows through `any`.
                    let vty = match &self.body.pats[*a] {
                        Pattern::Var(id) => self.local_ty(*id),
                        // A nested ctor pattern (`SendMessage (Ok result)`)
                        // needs its payload field coerced from `any` to the
                        // sub-pattern's own ADT type, else the recursive
                        // `_subj.Fields[i].Tag` reads `.Tag` off `any` (27/28).
                        p @ (Pattern::Ctor { .. }
                        | Pattern::Tuple(_)
                        | Pattern::Record(_)) => {
                            self.pattern_nominal_ty(p).unwrap_or(GoTy::Any)
                        }
                        _ => GoTy::Any,
                    };
                    let field = GoExpr::new(
                        GoExprKind::Index(
                            Box::new(GoExpr::new(
                                GoExprKind::Selector(Box::new(subj.clone()), "Fields".into()),
                                GoTy::Slice(Box::new(GoTy::Any)),
                            )),
                            Box::new(GoExpr::new(
                                GoExprKind::IntLit(i as i64),
                                GoTy::Bare(Prim::Int),
                            )),
                        ),
                        GoTy::Any,
                    );
                    // Fields[i] is `any`; narrow to the sub-pattern's type.
                    let elem = if vty == GoTy::Any {
                        field
                    } else {
                        GoExpr::new(
                            GoExprKind::Coerce {
                                inner: Box::new(field),
                                from: GoTy::Any,
                                to: vty.clone(),
                                reason: CoerceReason::GenericErase,
                            },
                            vty.clone(),
                        )
                    };
                    // Recurse so nested patterns (`Wrap (Just x)`, literal payload
                    // matches) contribute their own guard + bindings, not just a
                    // bare Var.
                    let (c, b) = self.pattern_test(&elem, &vty, *a);
                    cond = and_opt(cond, c);
                    binds.extend(b);
                }
                (cond, binds)
            }
        }
    }

    /// Resolve the owning union of `cname` (disambiguated by `subj_ty`'s pinned
    /// nominal, as elsewhere) and return its Go name IFF it is a sealed-interface
    /// ADT union — else `None`. Used to route a ctor pattern to typed variant
    /// dispatch instead of the `rt.SkyADT` bag.
    fn sealed_adt_union(&self, cname: &str, subj_ty: &GoTy) -> Option<String> {
        let owner = match subj_ty {
            GoTy::Named(gt, _) => self
                .ctor_in_union
                .get(&(gt.clone(), cname.to_string()))
                .map(|(k, _)| (gt.clone(), *k))
                .or_else(|| self.ctor_owner.get(cname).cloned()),
            _ => self.ctor_owner.get(cname).cloned(),
        };
        match owner {
            Some((go, NominalKind::Adt)) if self.sealed_unions.contains(&go) => Some(go),
            _ => None,
        }
    }

    /// Given `struct_val` — a Go expression of a sealed-ADT variant struct type
    /// (`Union_Ctor_V`) — build the field bindings + nested sub-condition for its
    /// argument patterns. Each field reads directly as the declared typed
    /// `struct_val.V{i}` (no `rt.Coerce`); nested sub-patterns recurse.
    fn adt_variant_binds(
        &mut self,
        struct_val: &GoExpr,
        union: &str,
        ctor: &str,
        args: &[PatId],
    ) -> (Option<GoExpr>, Vec<GoStmt>) {
        let ftys = self
            .ctor_field_gotys
            .get(&(union.to_string(), ctor.to_string()))
            .cloned()
            .unwrap_or_default();
        let mut cond: Option<GoExpr> = None;
        let mut binds: Vec<GoStmt> = Vec::new();
        for (i, a) in args.iter().enumerate() {
            let fty = ftys.get(i).cloned().unwrap_or(GoTy::Any);
            let field = GoExpr::new(
                GoExprKind::Selector(Box::new(struct_val.clone()), format!("V{i}")),
                fty.clone(),
            );
            let (c, b) = self.pattern_test(&field, &fty, *a);
            cond = and_opt(cond, c);
            binds.extend(b);
        }
        (cond, binds)
    }

    /// The nominal Go type a (sub-)pattern matches against, derived structurally
    /// from its ctor head — so a payload extracted as `any` can be narrowed
    /// before the recursive `pattern_test` reads `.Tag` / `.V{i}` off it.
    fn pattern_nominal_ty(&self, p: &Pattern) -> Option<GoTy> {
        match p {
            Pattern::Ctor { name, .. } => match name.as_str() {
                "Ok" | "Err" => Some(GoTy::Named(
                    "rt.SkyResult".into(),
                    vec![GoTy::Any, GoTy::Any],
                )),
                "Just" | "Nothing" => {
                    Some(GoTy::Named("rt.SkyMaybe".into(), vec![GoTy::Any]))
                }
                "True" | "False" => Some(GoTy::Bare(Prim::Bool)),
                other => self
                    .ctor_owner
                    .get(other)
                    .map(|(go, _)| GoTy::Named(go.clone(), Vec::new())),
            },
            Pattern::Tuple(pats) => {
                let elems = pats
                    .iter()
                    .map(|sp| {
                        self.pattern_nominal_ty(&self.body.pats[*sp])
                            .unwrap_or(GoTy::Any)
                    })
                    .collect();
                Some(GoTy::Tuple(elems))
            }
            _ => None,
        }
    }

    /// Extract a container payload field (`OkValue`/`JustValue`/`ErrValue`) and
    /// match the sub-pattern against it, recursing so nested patterns
    /// (`Ok (Just user)`, `Err (Custom msg)`) contribute their own guard +
    /// bindings rather than being silently dropped.
    fn bind_field_pat(
        &mut self,
        subj: &GoExpr,
        args: &[PatId],
        field: &str,
        ty: &GoTy,
    ) -> (Option<GoExpr>, Vec<GoStmt>) {
        if let Some(a) = args.first() {
            // The payload's declared type. When it is `any` (element-erased
            // container, `rt.SkyResult[E, any]`) but the sub-pattern is itself a
            // container / ADT (`Ok (Just x)`, `Ok (Custom e)`), narrow the
            // extracted `.OkValue` to the sub-pattern's nominal so the recursive
            // `pattern_test` reads `.Tag` off a concrete `rt.SkyMaybe` / ADT
            // rather than off `any`. Mirrors the sealed-ADT `Fields[i]` arm.
            let sub_ty = if *ty == GoTy::Any {
                self.pattern_nominal_ty(&self.body.pats[*a]).unwrap_or(GoTy::Any)
            } else {
                ty.clone()
            };
            let raw = GoExpr::new(
                GoExprKind::Selector(Box::new(subj.clone()), field.into()),
                GoTy::Any,
            );
            let field_expr = if sub_ty == GoTy::Any {
                GoExpr::new(GoExprKind::Selector(Box::new(subj.clone()), field.into()), ty.clone())
            } else {
                GoExpr::new(
                    GoExprKind::Coerce {
                        inner: Box::new(raw),
                        from: GoTy::Any,
                        to: sub_ty.clone(),
                        reason: CoerceReason::GenericErase,
                    },
                    sub_ty.clone(),
                )
            };
            return self.pattern_test(&field_expr, &sub_ty, *a);
        }
        (None, vec![])
    }
}

fn int_lit(n: i64) -> GoExpr {
    GoExpr::new(GoExprKind::IntLit(n), GoTy::Bare(Prim::Int))
}

/// For every `name := …` short-binding in `binds`, emit a `_ = name` discard
/// statement, so a pattern arm that ignores its binding does not trip Go's
/// "declared and not used". `_ = name` is always legal Go.
fn discard_binds(binds: &[GoStmt]) -> Vec<GoStmt> {
    binds
        .iter()
        .filter_map(|s| match s {
            GoStmt::Short(name, _) if name != "_" => Some(GoStmt::Expr(GoExpr::new(
                GoExprKind::Ident(format!("_ = {name}")),
                GoTy::Unit,
            ))),
            _ => None,
        })
        .collect()
}

/// Combine two optional pattern-guard conditions with `&&` (identity: `None`).
fn and_opt(a: Option<GoExpr>, b: Option<GoExpr>) -> Option<GoExpr> {
    match (a, b) {
        (None, x) | (x, None) => x,
        (Some(a), Some(b)) => Some(GoExpr::new(
            GoExprKind::Binary(GoBin::And, Box::new(a), Box::new(b)),
            GoTy::Bare(Prim::Bool),
        )),
    }
}

/// The builtin container / bool constructors, which route through
/// `rt.SkyResult` / `rt.SkyMaybe` / `bool` rather than a user nominal — never
/// sealed-ADT variant dispatch.
fn is_builtin_ctor(cname: &str) -> bool {
    matches!(cname, "Ok" | "Err" | "Just" | "Nothing" | "True" | "False")
}

fn tag_eq(subj: &GoExpr, tag: usize) -> GoExpr {
    GoExpr::new(
        GoExprKind::Binary(
            GoBin::Eq,
            Box::new(GoExpr::new(
                GoExprKind::Selector(Box::new(subj.clone()), "Tag".into()),
                GoTy::Bare(Prim::Int),
            )),
            Box::new(GoExpr::new(
                GoExprKind::IntLit(tag as i64),
                GoTy::Bare(Prim::Int),
            )),
        ),
        GoTy::Bare(Prim::Bool),
    )
}

// ---- free helpers -------------------------------------------------------

fn peel_params(sig: &Ty) -> Vec<Ty> {
    let mut out = Vec::new();
    let mut cur = sig;
    while let Ty::Fun(a, b) = cur {
        out.push((**a).clone());
        cur = b;
    }
    out
}

/// The return type of a signature after peeling exactly `n` leading arrows
/// (the def's value-param count). A sig with fewer arrows than params stops
/// early and returns whatever remains (defensive).
fn sig_result_after(sig: &Ty, n: usize) -> Ty {
    let mut cur = sig;
    for _ in 0..n {
        match cur {
            Ty::Fun(_, b) => cur = b,
            _ => break,
        }
    }
    cur.clone()
}

/// `rt.AnyTaskRun(expr)` — force a Task at an entry boundary (doc 08 §3).
fn any_task_run(expr: GoExpr) -> GoExpr {
    GoExpr::new(
        GoExprKind::Call(
            Box::new(GoExpr::new(GoExprKind::Ident("rt.AnyTaskRun".into()), GoTy::Any)),
            vec![expr],
        ),
        GoTy::Any,
    )
}

fn call_rt(name: &str, args: Vec<GoExpr>, ty: GoTy) -> GoExpr {
    GoExpr::new(
        GoExprKind::Call(
            Box::new(GoExpr::new(GoExprKind::Ident(name.into()), GoTy::Any)),
            args,
        ),
        ty,
    )
}

fn widen(e: GoExpr) -> GoExpr {
    if e.ty == GoTy::Any {
        e
    } else {
        GoExpr::new(GoExprKind::Widen(Box::new(e)), GoTy::Any)
    }
}

/// Narrow an operand of a Go string `+` to `string`. A statically-`string`
/// operand passes through unchanged; anything else (an `any`-typed kernel
/// return, a flex case binder) routes through `rt.AsString` so `go build`
/// accepts `string + string` rather than rejecting `string + any`.
fn coerce_to_str(e: GoExpr) -> GoExpr {
    if e.ty == GoTy::Bare(Prim::Str) {
        return e;
    }
    let from = e.ty.clone();
    GoExpr::new(
        GoExprKind::Coerce {
            inner: Box::new(e),
            from,
            to: GoTy::Bare(Prim::Str),
            reason: CoerceReason::FfiReturn,
        },
        GoTy::Bare(Prim::Str),
    )
}

fn go_binop(op: &str) -> Option<GoBin> {
    Some(match op {
        "+" => GoBin::Add,
        "-" => GoBin::Sub,
        "*" => GoBin::Mul,
        "==" => GoBin::Eq,
        "/=" => GoBin::Ne,
        "<" => GoBin::Lt,
        ">" => GoBin::Gt,
        "<=" => GoBin::Le,
        ">=" => GoBin::Ge,
        "&&" => GoBin::And,
        "||" => GoBin::Or,
        _ => return None,
    })
}

fn is_cmp(op: &str) -> bool {
    matches!(op, "==" | "/=" | "<" | ">" | "<=" | ">=")
}

fn tuple_type_args(t: &GoTy) -> String {
    if let GoTy::Tuple(xs) = t {
        // Erased `any` element types — the tuple literal is `rt.T2[any,any]{…}`
        // (runtime `SkyTuple2`); concrete element values widen in.
        let parts: Vec<String> = xs.iter().map(|_| "any".to_string()).collect();
        format!("[{}]", parts.join(", "))
    } else {
        String::new()
    }
}

/// A local Go-*type* renderer for the Raw ADT/record constructor strings. Types
/// only — expressions are always proper IR nodes (keeps `codegen` a leaf, no
/// crate cycle). Mirrors `codegen::render_ty`.
fn render_goty(t: &GoTy) -> String {
    match t {
        GoTy::Bare(p) => p.go_name().to_string(),
        GoTy::Unit => "struct{}".to_string(),
        GoTy::Any => "any".to_string(),
        GoTy::Named(n, args) if args.is_empty() => n.clone(),
        GoTy::Named(n, args) => {
            let a: Vec<String> = args.iter().map(render_goty).collect();
            format!("{n}[{}]", a.join(", "))
        }
        GoTy::Slice(t) => format!("[]{}", render_goty(t)),
        GoTy::Map(k, v) => format!("map[{}]{}", render_goty(k), render_goty(v)),
        GoTy::Func(ps, r) => {
            let a: Vec<String> = ps.iter().map(render_goty).collect();
            format!("func({}) {}", a.join(", "), render_goty(r))
        }
        // Tuples render as the runtime `SkyTuple2/3` shape `rt.TN[any, …]` —
        // element types erased so reflection paths (`T2[any,any]`) match. Type
        // info survives on the GoTy for pattern-bind coercion only.
        GoTy::Tuple(xs) => match xs.len() {
            2 | 3 => format!(
                "rt.T{}[{}]",
                xs.len(),
                xs.iter().map(|_| "any").collect::<Vec<_>>().join(", ")
            ),
            _ => "rt.SkyTupleN".to_string(),
        },
        GoTy::TyVar(n) => n.clone(),
        GoTy::Struct(fs) => {
            let parts: Vec<String> = fs
                .iter()
                .map(|(n, t)| format!("{} {}", n.as_str(), render_goty(t)))
                .collect();
            format!("struct{{ {} }}", parts.join("; "))
        }
    }
}

#[cfg(test)]
mod qualified_type_tests {
    use super::*;
    use crate::goty::{sky_ty_to_go, TypeEnv};
    use crate::ir::GoTy;
    use hir::SourceDb;

    fn parse_mod(src: &str) -> syntax::Parse {
        syntax::parse(src, base::FileId(0))
    }

    /// Regression: a union variant field typed with a QUALIFIED cross-module type
    /// (`A.Msg`) must resolve to that module's Go type (`A_Msg`), distinct from a
    /// same-module local `Msg` (`B_Msg`). Before qualifier preservation, `A.Msg`
    /// collapsed to bare `Msg` at `ast_type_to_ty` and resolved (wrongly) to
    /// `B_Msg` — so a cross-module-field union could not be sealed with the
    /// correct field type (the `rt.Coerce: expected <nil>, got int` panic class in
    /// `10-live-component`).
    #[test]
    fn qualified_variant_field_resolves_to_declaring_module() {
        let mut db = SourceDb::new();
        db.add_module(
            "A",
            parse_mod("module A exposing (..)\n\ntype Msg = AOne | ATwo Int\n"),
        );
        db.add_module(
            "B",
            parse_mod("module B exposing (..)\n\nimport A\n\ntype Msg = BLocal | Wrap A.Msg\n"),
        );

        let (nominal, nominal_by_module, decls) = collect_types(&db);
        let env = TypeEnv {
            nominal,
            nominal_by_module,
            ..Default::default()
        };

        // B declares its OWN `Msg` (→ `B_Msg`) whose `Wrap` variant carries `A.Msg`.
        let b_msg = decls
            .iter()
            .find(|d| d.go_name == "B_Msg")
            .expect("B_Msg decl present");
        let wrap_args = match &b_msg.kind {
            TypeDeclKind::Adt(vs) => vs
                .iter()
                .find(|(cn, _)| cn == "Wrap")
                .map(|(_, args)| args.clone())
                .expect("Wrap variant present"),
            _ => panic!("B_Msg should be a (sealed-eligible) ADT"),
        };
        assert_eq!(wrap_args.len(), 1, "Wrap carries exactly one field");

        // The qualifier is preserved (requalified to the full declaring module).
        assert_eq!(
            wrap_args[0],
            Ty::app("A.Msg", vec![]),
            "the Wrap field Ty keeps its `A.` qualifier"
        );

        // …and it maps to A's Go type, NOT B's same-named `Msg`.
        assert_eq!(
            sky_ty_to_go(&wrap_args[0], &env),
            GoTy::Named("A_Msg".to_string(), vec![]),
            "A.Msg must resolve to A_Msg (declaring module), not B_Msg"
        );

        // Sanity: a BARE local `Msg` reference inside B still resolves to `B_Msg`
        // (same-module names are byte-identical to pre-fix behaviour).
        assert_eq!(
            sky_ty_to_go_in(&Ty::app("Msg", vec![]), &env, Some("B")),
            GoTy::Named("B_Msg".to_string(), vec![]),
            "a same-module bare `Msg` still resolves to B_Msg"
        );

        // And both distinct Go types actually exist as separate decls.
        assert!(decls.iter().any(|d| d.go_name == "A_Msg"));
        assert!(decls.iter().any(|d| d.go_name == "B_Msg"));
    }

    /// An `import X as C` alias must requalify to the declaring module, so `C.Msg`
    /// (alias) still resolves to `A_Msg`.
    #[test]
    fn aliased_import_qualifier_requalifies_to_module() {
        let mut db = SourceDb::new();
        db.add_module(
            "A",
            parse_mod("module A exposing (..)\n\ntype Msg = AOne | ATwo Int\n"),
        );
        db.add_module(
            "B",
            parse_mod("module B exposing (..)\n\nimport A as C\n\ntype Msg = BLocal | Wrap C.Msg\n"),
        );

        let (nominal, nominal_by_module, decls) = collect_types(&db);
        let env = TypeEnv {
            nominal,
            nominal_by_module,
            ..Default::default()
        };

        let b_msg = decls.iter().find(|d| d.go_name == "B_Msg").unwrap();
        let wrap_args = match &b_msg.kind {
            TypeDeclKind::Adt(vs) => vs
                .iter()
                .find(|(cn, _)| cn == "Wrap")
                .map(|(_, args)| args.clone())
                .unwrap(),
            _ => panic!("B_Msg should be an ADT"),
        };
        // requalified from alias `C` to the full module name `A`.
        assert_eq!(wrap_args[0], Ty::app("A.Msg", vec![]));
        assert_eq!(
            sky_ty_to_go(&wrap_args[0], &env),
            GoTy::Named("A_Msg".to_string(), vec![])
        );
    }
}
