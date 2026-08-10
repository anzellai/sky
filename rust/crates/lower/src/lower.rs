//! Type-directed lowering (doc 07 §2): resolved HIR + the `ty` per-expression
//! type table → the typed Go-IR. Whole-program: DCE from `main`, kernel
//! dispatch, ADT/record type-decl emission. Scoped to the M4 CLI-family subset;
//! server/TUI/webview backends are reported as out of scope.

use crate::goty::{
    sky_ty_to_go, sky_ty_to_go_in, sky_ty_to_go_params, Nominal, NominalKind, TypeEnv,
};
use crate::ir::*;
use crate::kernel::{alias_go_name, kernel_go_name};
use base::{DefId, ModuleId, Name};
use hir::{Body, CaseBranch, Expr, ExprId, ImportSource, LocalId, PatId, Pattern, Res, SkyDb};
use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};
use ty::{BodyTypes, Ty, TyDb, Typer};

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
    /// True when ANY module in the program (entry + deps) imports `Std.Live.*`
    /// or `Sky.Http.Server.*` — i.e. the app is a Sky.Live or Sky.Http.Server
    /// app whose runtime auto-mounts `/_sky/console`. Drives BOTH the blank
    /// `_ "sky-app/rt/console_app"` import in codegen AND the `rt/console_app`
    /// materialisation in the build driver, so the inline dev console links
    /// (mirrors the Haskell oracle's `consoleNeededFromImports`, Compile.hs).
    pub console_needed: bool,
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

    /// Whether a generated FFI surface for `module` is loaded at all (i.e. the
    /// package was `sky install`ed). Distinguishes "no surface present → run
    /// `sky install`" from "surface present but this specific symbol is missing
    /// / inexpressible" — two failures that need different developer actions.
    pub fn has_package(&self, module: &str) -> bool {
        self.mods.contains_key(module)
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
    /// The `[env] prefix` from sky.toml — re-namespaces every runtime `SKY_*`
    /// read. Emitted as a leading `rt.SetEnvPrefix(...)` in `init()` (before the
    /// defaults, so they seed under the custom prefix). `None` keeps `SKY`.
    pub env_prefix: Option<String>,
    /// Extra `rt.SetSkyDefault(suffix, value)` pairs — e.g. `[("DB_PATH",
    /// "todos.db")]` from `[database]`. Emitted after the fixed defaults so a
    /// config value wins. Every suffix here must be one the runtime actually
    /// READS; a default nothing reads is a documented contract that does not
    /// exist (see `db_driver` below).
    pub extra_defaults: Vec<(String, String)>,
    /// The `[database] driver` value as DECLARED in sky.toml, if any.
    ///
    /// Deliberately NOT an entry in `extra_defaults`. It used to be emitted as
    /// `DB_DRIVER` → `SKY_DB_DRIVER`, and **nothing in `runtime-go` has ever
    /// read it**: the driver comes from the DSN's shape (`rt.detectDriver`,
    /// `runtime-go/rt/db_auth.go`), which is what all ~15 downstream dialect
    /// branches key off. So `driver = "postgres"` beside a `./app.db` path
    /// silently opened SQLite while two docs advertised the key as the selector.
    ///
    /// It is kept here as a declared EXPECTATION, checked against the DSN at
    /// build time (`db_driver_conflict`) so a contradiction is reported instead
    /// of silently ignored. The DSN stays the single source of truth.
    pub db_driver: Option<String>,
    /// The `[database] path` / `url` DSN as declared in sky.toml, for the
    /// consistency check above.
    pub db_dsn: Option<String>,
    /// The pinned Go-FFI surface (doc 09) for this project — empty when the
    /// project imports no Go packages.
    pub ffi: FfiTable,
    /// Runtime kernel arities: `rt.<Name>` (keyed WITHOUT the `rt.` prefix) → the
    /// number of `any` parameters that Go symbol takes. Populated by the build
    /// driver from `abi_guard::runtime_arities`. This is the authoritative arity
    /// for deciding whether a kernel application is partial (eta-expand into a
    /// closure) or full (direct call) — the curried HM type over-counts for
    /// function-returning kernels (`Handler`-returning middleware). Empty in the
    /// default config (e.g. unit tests / the eager `lower_program`), which
    /// disables the partial-kernel path — safe, since a full application still
    /// lowers to the correct direct call.
    pub kernel_arity: BTreeMap<String, usize>,
    /// Runtime kernel symbols (keyed WITHOUT the `rt.` prefix) whose Go func is
    /// declared VARIADIC (`func Http_request(arg any, rest ...any)`). Populated
    /// from `abi_guard::runtime_variadic_kernels`. For these — and ONLY these —
    /// the Go-source param scan in `kernel_arity` is NOT the true currying arity
    /// (a variadic tail is zero-or-more; a fully-variadic `func(args ...any)`
    /// scans as 1 regardless of the Sky arg count). A kernel ALIAS backed by a
    /// variadic symbol takes its arity from the declared Sky signature instead.
    /// Non-variadic symbols keep the Go scan, which is authoritative and — for a
    /// `Handler`-returning alias like `withCors` — the ONLY correct source (the
    /// curried sig over-counts because its result type is itself a function).
    pub variadic_kernels: std::collections::BTreeSet<String>,
}

pub fn lower_program(db: &dyn TyDb, entry: ModuleId) -> LowerOutput {
    lower_program_cfg(db, entry, &LowerConfig::default())
}

pub fn lower_program_cfg(db: &dyn TyDb, entry: ModuleId, cfg: &LowerConfig) -> LowerOutput {
    let typer = Typer::new(db);
    let mut warnings = Vec::new();
    let mut errors: Vec<String> = Vec::new();
    // Whole-program console detection: does any module REACHABLE from the entry
    // import `Std.Live.*` or `Sky.Http.Server.*`? Those are the only surfaces
    // whose runtime reaches `MountEmbeddedConsole`, so their binaries link the
    // inline console via the `_ "sky-app/rt/console_app"` blank import codegen
    // emits + the driver materialises. Computed once here; flows through
    // `LowerOutput.console_needed` to codegen + `materialise_rt`. Mirrors the
    // oracle's `consoleNeededFromImports` OR-fold over `moduleOrder` (the
    // dep-reachable graph) — NOT over every interned module, which would falsely
    // fire for every program (the stdlib's own `Std.Live` source imports
    // `Sky.Http.Server`, and the whole stdlib is interned regardless of use).
    let console_needed = program_needs_console(db.as_sky_db(), entry);

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
                let types = typer.body_types(m, td.def, body);
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
    let mut record_fieldsets: HashMap<Vec<String>, Vec<String>> = HashMap::new();
    // `_R` go-name → ordered type-param vars + field templates (for
    // instantiating a parametric alias resolved via the structural path).
    let mut record_params: HashMap<String, Vec<Name>> = HashMap::new();
    let mut record_templates: HashMap<String, Vec<(String, Ty)>> = HashMap::new();
    for d in &type_decls {
        if let TypeDeclKind::Record(fields) = &d.kind {
            let mut names: Vec<String> = fields.iter().map(|(n, _)| n.clone()).collect();
            names.sort();
            // Keep EVERY alias sharing this field-name set (not just the first) so
            // the structural resolver can disambiguate by field TYPE. Two records
            // with identical field names but different field types collide here.
            let cands = record_fieldsets.entry(names).or_default();
            if !cands.contains(&d.go_name) {
                cands.push(d.go_name.clone());
            }
            record_params.insert(d.go_name.clone(), record_type_params(fields));
            record_templates.insert(d.go_name.clone(), fields.clone());
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
            record_fieldsets
                .get(&names)
                .and_then(|cands| cands.first())
                .map(|go| (names.clone(), go.clone()))
        })
    };
    let env = TypeEnv {
        nominal,
        nominal_by_module,
        record_fieldsets,
        record_params,
        record_templates,
        model,
    };

    // Type names declared in MORE THAN ONE module (`Msg`/`Model`/`Page` in a
    // multi-module app). A qualified type reference (`Counter.Msg`) drops its
    // qualifier at `ast_type_to_ty`, so an ambiguous name can't be resolved to
    // the right Go union — its `sky_ty_to_go` result is unreliable.
    // determinism (L4): the ONLY HashMap iteration in the lowering pipeline, and
    // it is order-INDEPENDENT — it counts, per type name, how many modules declare
    // it, then keeps names with count > 1. Both the `per_name` build and the
    // `filter/map/collect` produce set-valued results whose CONTENTS don't depend
    // on `nominal_by_module`'s (randomized) key order. `ambiguous_names` is then
    // used lookup-only (`ty_refs_ambiguous` → `.contains`), so no order reaches
    // emitted Go / diagnostics.
    let ambiguous_names: HashSet<String> = {
        let mut per_name: HashMap<String, HashSet<String>> = HashMap::new();
        for (module, name) in env.nominal_by_module.keys() {
            per_name
                .entry(name.clone())
                .or_default()
                .insert(module.clone());
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
                    && variants.iter().all(|(_, args)| {
                        args.iter().all(|t| !ty_refs_ambiguous(t, &ambiguous_names))
                    }) =>
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
                fields
                    .iter()
                    .map(|(n, t)| (capitalize(n), t.clone()))
                    .collect(),
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
                    ctor_in_union.insert((d.go_name.clone(), v.clone()), (NominalKind::Iota, i));
                    ctor_arity_in_union.insert((d.go_name.clone(), v.clone()), 0);
                }
            }
            TypeDeclKind::Adt(vs) => {
                for (i, (cn, args)) in vs.iter().enumerate() {
                    ctor_owner.insert(cn.clone(), (d.go_name.clone(), NominalKind::Adt));
                    ctor_tag.insert(cn.clone(), i);
                    ctor_arity.insert(cn.clone(), args.len());
                    ctor_in_union.insert((d.go_name.clone(), cn.clone()), (NominalKind::Adt, i));
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
                ctor_in_union.insert(
                    (d.go_name.clone(), d.name.clone()),
                    (NominalKind::Record, 0),
                );
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
        // Row-polymorphic params (row var shared param↔result) must present to
        // callers as `any` — matching the `any` Go signature `lower_def` emits —
        // so the caller widens its argument instead of coercing it DOWN to a
        // closed struct (which would drop the row-carried fields).
        let (rp_params, _rp_result) = row_poly_flags(&e.body, &e.types);
        let mut ptys = Vec::new();
        for (i, p) in e.body.params.iter().enumerate() {
            let inferred = || match &e.body.pats[*p] {
                Pattern::Var(lid) => e
                    .types
                    .locals
                    .get(lid)
                    .cloned()
                    .unwrap_or(Ty::Var(Name::new("any"))),
                _ => Ty::Var(Name::new("any")),
            };
            // Take the sig param type when it is concrete enough to be useful
            // (not a bare type variable — a rigid `msg`/`a` gives no more than
            // the inferred type and would erase to `any` anyway).
            let t = match sig_ptys.as_ref().and_then(|ps| ps.get(i)) {
                Some(st) if !matches!(st, Ty::Var(_)) => st.clone(),
                // Row-poly + no concrete annotation → `any`.
                _ if *rp_params.get(i).unwrap_or(&false) => Ty::Var(Name::new("any")),
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
        let (_rp_params, rp_result) = row_poly_flags(&e.body, &e.types);
        let t = match sig_ret {
            Some(st) if !matches!(st, Ty::Var(_)) => Some(st),
            // Row-poly result + no concrete annotation → `any` (matches the `any`
            // Go return `lower_def` emits + the reflective `rt.RecordUpdate` body).
            _ if rp_result => Some(Ty::Var(Name::new("any"))),
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
    // ---- freeze-unsafe effect reachability (memoised-CAF stale-read lint) ----
    let def_effect = compute_def_effect(&defs, &kernel_alias);
    // ---- kernel-alias arity override for VARIADIC runtime symbols ----
    // A bare kernel alias (`request = Ffi.kernel "Http_request"`) carries its
    // arity entirely in the declared signature — the body has no value params.
    // For MOST aliases the Go-source param scan (`LowerConfig.kernel_arity`) is
    // the authoritative currying arity AND the only correct one: a middleware
    // alias like `withCors : List String -> Handler -> Handler` returns a
    // `Handler` (itself `Request -> Task …`), so its curried sig arrow-count (3,
    // after the result alias unfolds) OVER-counts the runtime symbol's real 2
    // params — the Go scan reads 2 and is right.
    //
    // The scan is wrong ONLY for VARIADIC Go funcs, which don't encode a fixed
    // arity: `func Http_request(arg any, rest ...any)` scans as 2 (so a full
    // 1-arg call under-counts and mis-eta-expands into a partial closure the
    // Task machinery mishandles), and a fully-variadic `func(args ...any)`
    // scans as 1 regardless of how many Sky args it takes (JsonEnc.list / a
    // partial Db.open never eta-expand). For those — and only those — the
    // declared Sky signature's arrow-count is the true arity (every variadic
    // kernel has a non-function result, so the sig doesn't over-count). The call
    // arm falls back to the Go scan for every non-variadic alias.
    let mut kernel_alias_arity: HashMap<DefId, usize> = HashMap::new();
    for (d, e) in &defs {
        if let Some(raw) = kernel_alias.get(d) {
            let sym = alias_go_name(raw);
            let sym = sym.strip_prefix("rt.").unwrap_or(&sym);
            if cfg.variadic_kernels.contains(sym) {
                if let Some(sig) = &e.sig {
                    kernel_alias_arity.insert(*d, peel_params(sig).len());
                }
            }
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
            console_needed,
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
            db: db.as_sky_db(),
            defs: &defs,
            kernel_alias: &kernel_alias,
            kernel_alias_arity: &kernel_alias_arity,
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
            def_effect: &def_effect,
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
            kernel_arity: &cfg.kernel_arity,
            cur_module: e.module_name.clone(),
            cur_def: d,
            tco: None,
        };
        let def_items = cx.lower_def(&e.name, &e.module_name, e.sig.as_ref(), d == main_def);
        // determinism (L4): `discovered` is a Vec (ordered), and the two set
        // drains below are order-INDEPENDENT — they only union into another
        // set/BTreeSet, so the resulting membership is identical regardless of the
        // (randomized) HashSet iteration order. `used_go_types` is sorted before it
        // drives emission (see `tqueue.sort()`); `ffi_used` is a BTreeSet.
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
        funcs.extend(def_items);
    }

    // ---- type-decl reachability: BFS over Go type names used in emitted code ----
    // determinism (L4): `type_by_go` and `seen_types` are lookup-only (iteration
    // order never reaches output). `used_go_types` (a HashSet) IS drained into a
    // Vec here, but `tqueue.sort()` immediately makes the drive order
    // deterministic; `type_items` is likewise sorted below before emission.
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
        let decl = type_by_go
            .get(&gn)
            .or_else(|| type_by_go.get(gn.trim_end_matches("_R")));
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
        console_needed,
    }
}

/// True when any module REACHABLE from `entry` (entry + its transitive
/// Sky-source `Dep` imports) imports `Std.Live.*` or `Sky.Http.Server.*` — the
/// two surfaces whose runtime auto-mounts `/_sky/console`.
///
/// Walks the import graph from `entry`, descending only through `Dep` edges
/// (real Sky-source modules — exactly the oracle's `moduleOrder` set), and
/// short-circuits the moment a triggering import path is seen. Scanning the
/// whole interned module set instead would falsely fire for EVERY program: the
/// entire Layer-3 stdlib is interned regardless of use, and `Std.Live`'s own
/// source imports `Sky.Http.Server`.
///
/// Segment-prefix match on the dotted import path mirrors the oracle's
/// `importTriggersConsole` (`("Std":"Live":_)` / `("Sky":"Http":"Server":_)`,
/// Compile.hs): `Std.Live`, `Std.Live.Head`, `Sky.Http.Server`,
/// `Sky.Http.Server.WebSocket` all match; `Std.LiveX` / `Sky.Http.ServerX`
/// (a different final segment) do not.
fn program_needs_console(db: &dyn SkyDb, entry: base::ModuleId) -> bool {
    // Building the bundled inline console itself: suppress the blank
    // `_ "sky-app/rt/console_app"` self-import. The emitted package is
    // transformed to `package console_app` (see
    // scripts/regenerate-console.sh), so a console_app that imported
    // `sky-app/rt/console_app` would import itself — `go build` rejects
    // the cycle. Mirrors the oracle's `globalIsInlineConsoleBuild` gate
    // (Compile.hs). The console still needs Std.Live, so the import
    // scan below WOULD return true; this env override is what lets the
    // console be a Live app without self-importing.
    if std::env::var("SKY_BUILD_IS_INLINE_CONSOLE").as_deref() == Ok("1") {
        return false;
    }
    let mut seen: HashSet<base::ModuleId> = HashSet::new();
    let mut stack: Vec<base::ModuleId> = vec![entry];
    while let Some(m) = stack.pop() {
        if !seen.insert(m) {
            continue;
        }
        for imp in db.module_parse(m).tree().imports() {
            let Some(path) = imp.name().map(|n| n.text()) else {
                continue;
            };
            if path == "Std.Live"
                || path.starts_with("Std.Live.")
                || path == "Sky.Http.Server"
                || path.starts_with("Sky.Http.Server.")
            {
                return true;
            }
            if let ImportSource::Dep(dep) = db.classify_import(&path) {
                stack.push(dep);
            }
        }
    }
    false
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
    let mut stmts = Vec::new();
    // `[env] prefix` FIRST — it changes what env name every subsequent default
    // seeds under, so it must run before SetPortDefault / SetSkyDefault.
    if let Some(prefix) = &cfg.env_prefix {
        stmts.push(call("rt.SetEnvPrefix", &[prefix]));
    }
    stmts.push(call("rt.SetPortDefault", &[&port]));
    // sky.toml-derived values FIRST so they win: `SetSkyDefault` is set-if-unset,
    // so the first call for a suffix wins and the fixed fallbacks below become
    // no-ops when sky.toml already provided the key. (Emitting the fixed defaults
    // first silently clobbered `[live] ttl` / `[auth] *` from sky.toml.)
    for (suffix, value) in &cfg.extra_defaults {
        stmts.push(call("rt.SetSkyDefault", &[suffix, value]));
    }
    stmts.push(call("rt.SetSkyDefault", &["LIVE_TTL", "1800"]));
    stmts.push(call("rt.SetSkyDefault", &["AUTH_TOKEN_TTL", "86400"]));
    stmts.push(call("rt.SetSkyDefault", &["AUTH_COOKIE", "sky_auth"]));
    stmts.push(call("rt.SetSkyDefault", &["AUTH_DRIVER", "jwt"]));
    GoItem::Init(stmts)
}

// ---- module / name mangling (doc 08 §5) --------------------------------

fn module_prefix(module: &str) -> String {
    module.replace('.', "_")
}

fn top_go_name(module: &str, name: &str) -> String {
    format!("{}_{}", module_prefix(module), reserved_rewrite(name))
}

/// A kernel that returns a DIFFERENT value on each run (entropy / clock). If a
/// memoised top-level CAF FORCES one of these to a plain value it freezes to a
/// single result — the post-CAF-memoisation footgun (colliding UUIDs, frozen
/// clock). `module` is the canonical kernel alias (`Uuid` / `Random` / `Time`
/// / `Crypto`). Deterministic seeded variants (`seededInt`, …) are excluded —
/// they are pure given their seed, so memoising them is sound.
fn is_fresh_value_kernel(module: &str, func: &str) -> bool {
    // Match on the last path segment so both the canonical alias (`Uuid`) and a
    // fully-qualified form (`Sky.Core.Uuid`) resolve identically.
    let m = module.rsplit('.').next().unwrap_or(module);
    matches!(
        (m, func),
        ("Uuid", "v4")
            | ("Uuid", "v7")
            | ("Random", "int")
            | ("Random", "float")
            | ("Random", "range")
            | ("Random", "choice")
            | ("Random", "shuffle")
            | ("Random", "weighted")
            | ("Time", "now")
            | ("Time", "unixMillis")
            | ("Crypto", "randomBytes")
            | ("Crypto", "randomToken")
    )
}

/// Parse a kernel-alias symbol (`"Uuid_v4"`, `"Time_unixMillis"`) into
/// `(module, func)` iff it names a fresh-value kernel. `split_once('_')` keeps
/// multi-word funcs intact (`"Crypto_randomToken"` → `("Crypto",
/// "randomToken")`).
fn fresh_value_symbol_parts(sym: &str) -> Option<(String, String)> {
    let (m, f) = sym.split_once('_')?;
    is_fresh_value_kernel(m, f).then(|| (m.to_string(), f.to_string()))
}

/// Freeze-unsafe effect kinds a memoised CAF can force. Both go stale/frozen
/// when cached: `Fresh` = clock/entropy (Uuid / Random / Time / Crypto),
/// `StoreRead` = a mutable-DB read whose result changes as rows are written.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum EffectKind {
    Fresh,
    StoreRead,
}

/// A DB READ kernel. Freezing its result in a memoised CAF makes the value go
/// STALE after later writes (the production incident: a product created after
/// the first render never appeared — the read was cached forever). Deliberately
/// EXCLUDED:
///   * `Db.connect` / `Db.open` — HANDLE kernels; memoising a pool handle is the
///     blessed "one shared connection" contract.
///   * `Db.exec` / `Db.execRaw` — WRITES / DDL, not reads. A memoised write CAF
///     runs ONCE, which is exactly what a boot-time `initDb`/schema-create wants
///     — flagging it would be a false positive (skyvote's `initDb`, darragh's
///     `ensureSchema`). The stale-value footgun is specifically about READS.
fn is_store_read_kernel(module: &str, func: &str) -> bool {
    let m = module.rsplit('.').next().unwrap_or(module);
    m == "Db"
        && matches!(
            func,
            "query"
                | "queryObjects"
                | "queryDecode"
                | "getById"
                | "getByIdDecode"
                | "findOneByField"
                | "findManyByField"
                | "findByConditions"
                | "unsafeFindWhere"
        )
}

/// Classify a kernel `(module, func)` into a freeze-unsafe `EffectKind`.
fn kernel_effect(module: &str, func: &str) -> Option<EffectKind> {
    if is_fresh_value_kernel(module, func) {
        Some(EffectKind::Fresh)
    } else if is_store_read_kernel(module, func) {
        Some(EffectKind::StoreRead)
    } else {
        None
    }
}

/// Classify a kernel-alias symbol (`"Time_now"`, `"Db_query"`).
fn effect_symbol_parts(sym: &str) -> Option<EffectKind> {
    let (m, f) = sym.split_once('_')?;
    kernel_effect(m, f)
}

/// True when a memoised CAF's frozen RESULT is ordinary DATA that can go STALE
/// after later writes (a `List`, record, scalar, ADT, tuple, …) — the ONLY case
/// the stale-read lint should fire on. False for results whose "freezing" is the
/// intended contract or that hold nothing stale-able:
///   * `Fun` — a returned lambda is forced by the CALLER, not at memo time.
///   * `Unit` and `Result _ () / Maybe _ () / Task _ ()` — completion SIGNALS
///     (a run-once boot action / migration), no value to go stale.
///   * resource HANDLES (`Db` / `Pool` / `Conn` / `Connection` / `Client` /
///     `Cache`) and table/config DESCRIPTORS (`Store`) — memoising one shared
///     handle/config is the blessed contract. Crucially, any effectful lambda
///     such a value CARRIES (e.g. `Store.defaultWith (\_ -> nowMs ())`, a
///     per-insert default the runtime calls LATER) is STORED, not forced now —
///     so the reachable clock/DB kernel is not a memo-time read.
/// `Result`/`Maybe`/`Task` are TRANSPARENT: stale-ability follows their payload
/// (`Result Error (List Post)` is stale data; `Result Error ()` is not).
fn is_stale_data_result(ty: &Ty) -> bool {
    match ty {
        Ty::Unit | Ty::Fun(_, _) => false,
        Ty::App(n, args) => {
            if is_config_or_handle_result(ty) {
                return false;
            }
            let base = n.as_str().rsplit('.').next().unwrap_or(n.as_str());
            match base {
                // Transparent wrappers: the payload (last type arg) decides.
                "Result" | "Maybe" | "Task" => args.last().is_some_and(is_stale_data_result),
                _ => true,
            }
        }
        // Var / Record / Tuple / Error → treat as data (may be stale).
        _ => true,
    }
}

/// A long-lived config/handle DESCRIPTOR result — a resource handle (`Db` /
/// `Pool` / `Conn` / `Connection` / `Client` / `Cache`) or a table/config value
/// (`Store`). Its carried lambdas (a `Store.defaultWith (\_ -> nowMs ())`
/// per-insert default) are STORED for later, NOT forced at memo time, so its
/// effect must not propagate to a caller. NARROWER than `is_stale_data_result`'s
/// suppression set: it deliberately does NOT include `Unit` / `Result _ ()` /
/// functions, which are legitimate propagation CONDUITS in a read chain
/// (`loadPosts → Store.toList → Db_queryObjects`) and must stay transparent.
fn is_config_or_handle_result(ty: &Ty) -> bool {
    let name = match ty {
        Ty::App(n, _) => n.as_str(),
        _ => return false,
    };
    let base = name.rsplit('.').next().unwrap_or(name);
    matches!(
        base,
        "Db" | "Pool" | "Conn" | "Connection" | "Client" | "Cache" | "Store"
    )
}

/// Merge two effect candidates. Either suffices to fire the lint; StoreRead is
/// preferred deterministically so the message names the DB read when both a
/// read and a fresh-value effect are reachable.
fn merge_effect(a: Option<EffectKind>, b: Option<EffectKind>) -> Option<EffectKind> {
    match (a, b) {
        (Some(EffectKind::StoreRead), _) | (_, Some(EffectKind::StoreRead)) => {
            Some(EffectKind::StoreRead)
        }
        (Some(EffectKind::Fresh), _) | (_, Some(EffectKind::Fresh)) => Some(EffectKind::Fresh),
        _ => None,
    }
}

/// Whole-program pre-pass: per def, the freeze-unsafe effect kernel (fresh-value
/// clock/entropy OR mutable-store read) syntactically REACHABLE from its body —
/// transitively through called defs AND into lambda bodies (the expr arena holds
/// every sub-expression, so a single `.iter()` sees inside lambdas). This is how
/// the memoised-CAF lint catches a LAUNDERED effect the existing direct-kernel
/// scan misses: `listActive = withConnList (\c -> Store.query …)` (read hidden
/// in the lambda arg) and `errRef = … (Data.nowMs ())` (clock hidden one hop
/// through the user wrapper `nowMs`). Over-approximates "forced when evaluated";
/// the lint gates on a DATA (non-handle, non-function) result type, so a lambda
/// merely CONSUMED to produce that data is exactly the intended frozen reading.
fn compute_def_effect(
    defs: &BTreeMap<DefId, DefEntry>,
    kernel_alias: &HashMap<DefId, String>,
) -> HashMap<DefId, EffectKind> {
    // Per-def direct kernel effect + outgoing def references (call-graph edges,
    // including references that appear inside lambda bodies).
    let mut direct: HashMap<DefId, Option<EffectKind>> = HashMap::new();
    let mut edges: HashMap<DefId, Vec<DefId>> = HashMap::new();
    for (d, e) in defs {
        let mut eff: Option<EffectKind> = None;
        let mut es: Vec<DefId> = Vec::new();
        let note_def = |d2: &DefId, eff: &mut Option<EffectKind>, es: &mut Vec<DefId>| {
            if let Some(sym) = kernel_alias.get(d2) {
                *eff = merge_effect(*eff, effect_symbol_parts(sym));
            }
            es.push(*d2);
        };
        for (_id, expr) in e.body.exprs.iter() {
            match expr {
                Expr::Var(Res::Kernel { module, func }) => {
                    eff = merge_effect(eff, kernel_effect(module.as_str(), func.as_str()));
                }
                Expr::Var(Res::Def(d2)) => note_def(d2, &mut eff, &mut es),
                Expr::Binop { res, .. } => match res {
                    Res::Kernel { module, func } => {
                        eff = merge_effect(eff, kernel_effect(module.as_str(), func.as_str()));
                    }
                    Res::Def(d2) => note_def(d2, &mut eff, &mut es),
                    _ => {}
                },
                _ => {}
            }
        }
        direct.insert(*d, eff);
        edges.insert(*d, es);
    }

    // Fixpoint: effect[d] = merge(direct[d], effect[d'] over edges d → d').
    let mut effect: HashMap<DefId, EffectKind> = HashMap::new();
    for (d, e) in &direct {
        if let Some(k) = e {
            effect.insert(*d, *k);
        }
    }
    let mut changed = true;
    while changed {
        changed = false;
        for (d, es) in &edges {
            let mut cur = effect
                .get(d)
                .copied()
                .or_else(|| direct.get(d).copied().flatten());
            for d2 in es {
                if let Some(k2) = effect.get(d2).copied() {
                    cur = merge_effect(cur, Some(k2));
                }
            }
            if let Some(k) = cur {
                if effect.get(d) != Some(&k) {
                    effect.insert(*d, k);
                    changed = true;
                }
            }
        }
    }
    effect
}

const RESERVED: &[&str] = &[
    "init",
    "string",
    "error",
    "any",
    "bool",
    "byte",
    "rune",
    "int",
    "int8",
    "int16",
    "int32",
    "int64",
    "uint",
    "uint8",
    "uint16",
    "uint32",
    "uint64",
    "float32",
    "float64",
    "true",
    "false",
    "nil",
    "iota",
    "len",
    "cap",
    "make",
    "new",
    "append",
    "copy",
    "delete",
    "panic",
    "recover",
    "print",
    "println",
    "close",
    "min",
    "max",
    "complex",
    "real",
    "imag",
    "clear",
    "for",
    "func",
    "type",
    "range",
    "return",
    "if",
    "else",
    "switch",
    "case",
    "default",
    "var",
    "const",
    "map",
    "struct",
    "interface",
    "chan",
    "go",
    "select",
    "package",
    "import",
    "goto",
    "break",
    "continue",
    "fallthrough",
    "defer",
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
    db: &dyn TyDb,
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
    // same Sky type, two incompatible Go types (26/37). Routed through the
    // memoised `type_world` query so it shares the build's single world assembly.
    let world = db.type_world();
    for m in db.module_ids() {
        let mname = db.module_name(m).to_string();
        let prefix = module_prefix(&mname);
        // Per-module qualifier → declaring-module-name map, so a variant field
        // written `Counter.Msg` (or an aliased `import X as C` → `C.Msg`) can be
        // requalified to the FULL declaring module name that `nominal_by_module`
        // is keyed by. Bare (unqualified) references are untouched.
        let requal = import_module_map(db.as_sky_db(), m);
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
                        let opaque = variants.len() == 1 && variants[0].0.ends_with("_OPAQUE");
                        reg!(
                            tname,
                            Nominal {
                                go_name: go_name.clone(),
                                kind: NominalKind::Iota,
                                opaque,
                                type_arity: 0,
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
                                type_arity: 0,
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
                        // The alias's DISTINCT non-`"any"` type-param vars, in
                        // first-appearance order across the field types → the Go
                        // generic arity (`Cfg_R[T1, …]`). `"any"` is the
                        // per-occurrence wildcard floor, never a real param.
                        let type_arity = record_type_params(&fields).len();
                        reg!(
                            tname,
                            Nominal {
                                go_name: go_name.clone(),
                                kind: NominalKind::Record,
                                opaque: false,
                                type_arity,
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

/// Arity of a BUILTIN constructor (`Just`, `Ok`, `Err`, `Nothing`, …). These are
/// interned under the synthetic `BUILTIN_MOD`, not the per-module `ctor_arity`
/// map, so a bare VALUE reference to one (`JsonDec.map Just dec` — the ctor as a
/// first-class function) fell through `ctor_arity_pinned`'s map lookup to the old
/// `unwrap_or(0)` default and emitted a zero-arg call `rt.Just()` → "not enough
/// arguments in call to rt.Just". The arity-1 builtins must eta-expand into a
/// closure of the right arity instead. `Nothing` / `True` / `False` are genuinely
/// nullary and correctly stay 0 (their value path emits `rt.Nothing[T]()`).
fn builtin_ctor_arity(cname: &str) -> usize {
    match cname {
        "Just" | "Ok" | "Err" => 1,
        _ => 0,
    }
}

/// The DISTINCT non-`"any"` type-param vars of a record alias's field types, in
/// first-appearance order (the Go generic param order `T1, T2, …`). `"any"` is
/// the per-occurrence wildcard floor (`ty::is_polymorphic`), never a real
/// generic parameter — a `{ onPress : msg, blob : any }` alias has arity 1.
fn record_type_params(fields: &[(String, Ty)]) -> Vec<Name> {
    let mut out: Vec<Name> = Vec::new();
    for (_, t) in fields {
        for v in t.free_vars() {
            if v.as_str() != "any" && !out.contains(&v) {
                out.push(v);
            }
        }
    }
    out
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
            // Type-param vars → generic Go params `T1, T2, …` (Phase 2 typed-Go
            // ceiling). arity 0 keeps the exact non-generic emission (byte-
            // identical to the M4 baseline for every concrete record).
            let param_vars = record_type_params(fields);
            let param_map: HashMap<Name, GoTy> = param_vars
                .iter()
                .enumerate()
                .map(|(i, v)| (v.clone(), GoTy::TyVar(format!("T{}", i + 1))))
                .collect();
            // Field Go types: a type-param field renders as its `Ti`; every
            // other field renders normally (and drives reachability via
            // `collect`). Sharing `sky_ty_to_go_params` with the use sites keeps
            // the decl and every instantiation in exact agreement.
            let go_fields: Vec<(String, GoTy)> = fields
                .iter()
                .map(|(n, t)| {
                    let gt = sky_ty_to_go_params(t, env, None, &param_map);
                    collect_named(&gt, &mut more);
                    (capitalize(n), gt)
                })
                .collect();

            if param_vars.is_empty() {
                // ---- non-generic (baseline, byte-identical) ----
                let mut items = vec![GoItem::Type(
                    decl.go_name.clone(),
                    GoTypeDef::Struct(go_fields.clone()),
                )];
                items.push(GoItem::Raw(format!(
                    "func init() {{ rt.RegisterGobType({}{{}}) }}",
                    decl.go_name
                )));
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
                return (items, more);
            }

            // ---- generic (`Cfg_R[T1 any, …]`) ----
            // `<T1 any, T2 any, …>` clause + `[T1, T2, …]` arg list + the
            // gob-registrable `[any, …]` concrete instantiation (gob can't
            // register a generic type; the wire boundary re-enters as `any` and
            // is `rt.Coerce`d — the established §8.2 pattern).
            let clause: String = (0..param_vars.len())
                .map(|i| format!("T{} any", i + 1))
                .collect::<Vec<_>>()
                .join(", ");
            let arglist: String = (0..param_vars.len())
                .map(|i| format!("T{}", i + 1))
                .collect::<Vec<_>>()
                .join(", ");
            let anylist: String = std::iter::repeat("any")
                .take(param_vars.len())
                .collect::<Vec<_>>()
                .join(", ");
            let struct_fields: String = go_fields
                .iter()
                .map(|(f, t)| format!("{f} {}", render_goty(t)))
                .collect::<Vec<_>>()
                .join("; ");
            let mut items = vec![GoItem::Raw(format!(
                "type {}[{clause}] struct {{ {struct_fields} }}",
                decl.go_name
            ))];
            items.push(GoItem::Raw(format!(
                "func init() {{ rt.RegisterGobType({}[{anylist}]{{}}) }}",
                decl.go_name
            )));
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
                "func {ctor_name}[{clause}]({}) {}[{arglist}] {{ return {}[{arglist}]{{{}}} }}",
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
                let assigns: Vec<String> = (0..args.len()).map(|j| format!("V{j}: v{j}")).collect();
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

/// Lower a Sky `Char` literal / pattern (`'a'`) to a Go rune value `rune(<cp>)`.
/// `Char` maps to Go `rune` (`Prim::Rune`), so a char must NOT lower to a string
/// literal — a case subject `c : Char` is a `rune`, and `rune == "a"` fails
/// `go build` (mismatched types). Emitting `rune(<codepoint>)` also boxes as
/// `int32` in an `any` slot, matching the runtime's `firstRune` / `String_toList`
/// rune representation. `s` is the DECODED char (escapes already resolved), so
/// the first `char`'s Unicode scalar is the codepoint.
fn rune_lit(s: &str) -> GoExpr {
    let cp = s.chars().next().map(|c| c as i64).unwrap_or(0);
    GoExpr::new(
        GoExprKind::Call(
            Box::new(GoExpr::new(
                GoExprKind::Ident("rune".into()),
                GoTy::Bare(Prim::Rune),
            )),
            vec![GoExpr::new(GoExprKind::IntLit(cp), GoTy::Bare(Prim::Int))],
        ),
        GoTy::Bare(Prim::Rune),
    )
}

/// Whether a Sky type references any nominal name that is declared in more than
/// one module (`ambiguous`) — structurally, at any depth. Such a reference can't
/// be resolved to a single Go type after the qualifier is dropped, so a union
/// carrying it as a variant field is NOT sealed.
fn ty_refs_ambiguous(t: &Ty, ambiguous: &HashSet<String>) -> bool {
    match t {
        Ty::App(n, args) => {
            ambiguous.contains(n.as_str()) || args.iter().any(|a| ty_refs_ambiguous(a, ambiguous))
        }
        Ty::Fun(a, b) => ty_refs_ambiguous(a, ambiguous) || ty_refs_ambiguous(b, ambiguous),
        Ty::Tuple(xs) => xs.iter().any(|x| ty_refs_ambiguous(x, ambiguous)),
        Ty::Record(fs, _) => fs.iter().any(|(_, x)| ty_refs_ambiguous(x, ambiguous)),
        _ => false,
    }
}

// ---- per-def lowering context ------------------------------------------

// determinism (L4): every `HashMap`/`HashSet` field below (both the shared `&'a`
// lookup tables and the per-def scratch maps `local_names` / `local_tys` /
// `used_types`) is consulted lookup-only — `.get()` / `.contains` / `.insert`.
// None is ITERATED into emitted Go, GoIR order, or diagnostics: emission order is
// driven by `defs` (a `BTreeMap`), the `discovered` Vec, and the sorted
// `tqueue` / `type_items`. `used_types` is drained only via a set-union that a
// later `.sort()` re-orders. So `HashMap` is sound here despite randomized order.
struct Ctx<'a> {
    db: &'a dyn SkyDb,
    defs: &'a BTreeMap<DefId, DefEntry>,
    kernel_alias: &'a HashMap<DefId, String>,
    /// kernel-alias def → its DECLARED Sky signature arrow-count, present ONLY
    /// for aliases backed by a VARIADIC runtime symbol (see
    /// `LowerConfig.variadic_kernels`). For those the Go-source param scan
    /// mis-counts the currying arity; the sig is the authority. Non-variadic
    /// aliases are absent here and fall back to the Go scan in the call arm —
    /// which is authoritative and, for a `Handler`-returning alias like
    /// `withCors`, the only correct source (the curried sig over-counts).
    kernel_alias_arity: &'a HashMap<DefId, usize>,
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
    /// Whole-program: the freeze-unsafe effect (fresh-value / mutable-store read)
    /// reachable from each def's body. The memoised-CAF lint uses it to catch a
    /// LAUNDERED effect the direct-kernel scan misses.
    def_effect: &'a HashMap<DefId, EffectKind>,
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
    /// Runtime kernel arities (`rt.<Name>` sans prefix → param count) — the
    /// authoritative arity for the partial-kernel eta-expansion decision.
    kernel_arity: &'a BTreeMap<String, usize>,
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
    /// The `DefId` of the def currently being lowered — identifies self-calls
    /// for the tail-call optimiser.
    cur_def: DefId,
    /// Set while lowering a tail-recursive def body in the TCO statement path.
    /// `Some` iff the enclosing def qualified for TCO (see `is_tail_recursive`);
    /// a saturated self-call reached in tail position under this context becomes
    /// a param-reassignment + `continue` instead of a recursive Go call.
    tco: Option<TcoCtx>,
}

/// The tail-call-optimisation context for one def. Present only while the def's
/// body is lowered through the statement-path tail-walk (`lower_tail_stmts`).
#[derive(Clone)]
struct TcoCtx {
    /// The def whose saturated tail self-calls become `continue` jumps.
    def: DefId,
    /// Value-param count — a call must be saturated (`args.len() == arity`) to
    /// be a tail jump; anything else is a value capture / partial application
    /// and stays a normal call.
    arity: usize,
    /// The emitted Go signature params `(name, go-type)`, in order. The jump
    /// reassigns each from the corresponding call arg (coerced to the param's
    /// Go type via the same path `lower_call` uses). A `_` name is an unused
    /// param — its arg is dropped (Go forbids assigning to `_`).
    params: Vec<(String, GoTy)>,
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

    /// A `Dict.toList` / `Dict.keys` on a `Dict Int v` / `Dict Float v` must lower
    /// to the typed-key kernel entry point (`rt.Dict_toListIntKey` /
    /// `rt.Dict_keysIntKey` / `…FloatKey`) — the underlying runtime map is
    /// `map[string]V`, so the default `rt.Dict_toList` / `rt.Dict_keys` leaks
    /// stringified keys and any downstream `rt.AsInt` on a key yields 0
    /// (`Dict.keys` on an annotated `Dict Int v` came back `[0, 0, …]`). The key
    /// type is read from the argument's HM-inferred `Dict k v` shape at the call
    /// site (oracle: `rt.Dict_toListIntKey(byCounts)` vs
    /// `rt.Dict_toList(rt.AsMapAny(totals))`).
    fn dict_tolist_specialised(&self, base: &str, args: &[ExprId]) -> Option<&'static str> {
        if args.len() != 1 {
            return None;
        }
        // (default-kernel, IntKey-variant, FloatKey-variant) per key-producing op.
        let (int_variant, float_variant) = match base {
            "rt.Dict_toList" => ("rt.Dict_toListIntKey", "rt.Dict_toListFloatKey"),
            "rt.Dict_keys" => ("rt.Dict_keysIntKey", "rt.Dict_keysFloatKey"),
            _ => return None,
        };
        match self.sky_ty_of(args[0])? {
            Ty::App(dict, dargs) if dict.as_str() == "Dict" && dargs.len() == 2 => {
                match &dargs[0] {
                    Ty::App(k, ka) if ka.is_empty() && k.as_str() == "Int" => Some(int_variant),
                    Ty::App(k, ka) if ka.is_empty() && k.as_str() == "Float" => Some(float_variant),
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

    fn lower_def(&mut self, name: &str, module: &str, sig: Option<&Ty>, is_main: bool) -> Vec<GoItem> {
        // bind params
        let param_pats: Vec<PatId> = self.body.params.clone();
        let mut params = Vec::new();
        let mut param_destructure: Vec<GoStmt> = Vec::new();
        let sig_params = sig.map(peel_params).unwrap_or_default();

        // Row-polymorphism detection (row-poly RESULT access class) — see
        // `row_poly_flags`. A row var shared between a param record and the
        // result record marks BOTH positions for `any` erasure so the emitted Go
        // signature (`bump(r any) any`) + reflective `rt.Field`/`rt.RecordUpdate`
        // body preserve every row-carried field. The SAME flags drive the
        // caller-facing `def_param_tys`/`def_result_tys` tables, so both sides of
        // every call agree.
        let (rp_params, rp_result) = row_poly_flags(&self.body, &self.types);

        for (i, p) in param_pats.iter().enumerate() {
            let (pname, mut pty, binds) = self.bind_param(*p, sig_params.get(i));
            // Erase a genuinely row-polymorphic param to `any`, and re-register
            // the local so the body's reads (`r.age` → `rt.Field`) and updates
            // (`{ r | … }` → `rt.RecordUpdate`) take the reflective any-typed
            // path. Skipped when the slot has a concrete DECLARED sig type (an
            // explicit annotation is authoritative).
            if sig_params.get(i).is_none() && *rp_params.get(i).unwrap_or(&false) {
                pty = GoTy::Any;
                if let Pattern::Var(id) = &self.body.pats[*p] {
                    self.local_tys.insert(*id, GoTy::Any);
                }
            }
            params.push(GoParam {
                name: pname,
                ty: pty,
            });
            param_destructure.extend(binds);
        }
        // Whether the RESULT is row-polymorphic (erase the return type to `any`).
        let result_row_poly = sig.is_none() && rp_result;

        if is_main {
            return vec![self.lower_main(name, module)];
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
        let declared_ret: Option<GoTy> = if result_row_poly {
            // Row-polymorphic result: emit `any`, matching the reflective
            // `rt.RecordUpdate`/`rt.Field` body path (see the param loop above).
            Some(GoTy::Any)
        } else {
            match sig_ret {
                Some(t) if !matches!(t, Ty::Var(_)) => Some(self.goty(&t)),
                _ => match &self.types.result {
                    Some(t) => {
                        let gt = self.goty(t);
                        (gt != GoTy::Any).then_some(gt)
                    }
                    None => Some(GoTy::Unit),
                },
            }
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
                // Tail-call optimisation (Limitation #8 in the oracle): a def
                // whose ONLY self-references are saturated calls in tail
                // position lowers to a `for {}` loop where each tail self-call
                // becomes param-reassignment + `continue` — constant Go stack
                // regardless of recursion depth. Gated on simple params
                // (`body.is_empty()` ⇒ no destructure to re-run per iteration).
                let arity = param_pats.len();
                if body.is_empty() && self.is_tail_recursive(r, self.cur_def, arity) {
                    let ret = declared_ret.clone().unwrap_or_else(|| self.expr_ty(r));
                    self.tco = Some(TcoCtx {
                        def: self.cur_def,
                        arity,
                        params: params
                            .iter()
                            .map(|p| (p.name.clone(), p.ty.clone()))
                            .collect(),
                    });
                    let mut loop_body: Vec<GoStmt> = Vec::new();
                    self.lower_tail_stmts(r, &ret, &mut loop_body);
                    self.tco = None;
                    body.push(GoStmt::Loop(loop_body));
                    ret
                } else {
                    let e = self.lower_expr(r, &expected);
                    let bt = e.ty.clone();
                    body.push(GoStmt::Return(Some(e)));
                    declared_ret.unwrap_or(bt)
                }
            }
            None => declared_ret.unwrap_or(GoTy::Unit),
        };
        // ── CAF memoisation ────────────────────────────────────────────────
        // A zero-parameter top-level binding is a VALUE, not a function — a
        // single thing evaluated once and shared (Elm/Haskell CAF semantics,
        // the "memoised handle" contract). Emit it through a lazy once-cell so
        // every `Foo_bar()` call returns the SAME value instead of re-running
        // the body (which reopened a DB pool / re-read env / recomputed per
        // reference). Gate:
        //   * no value params (it's a CAF, not a function);
        //   * not `main` (handled above);
        //   * has a body (`root`);
        //   * NOT self-referential — a self-ref compute would re-enter the
        //     cell's sync.Once and deadlock, so those stay plain functions
        //     (also the right behaviour for a `Err _ -> conn` retry loop).
        // type_params is always empty here (top-level defs erase polymorphism
        // to `any`), so a memoised cell holds one representation safely.
        let self_referential = root.is_some_and(|r| self.body_references_def(r, self.cur_def));
        if params.is_empty() && root.is_some() && !self_referential {
            // Footgun lint: a memoised CAF that FORCES a fresh-value effect
            // (Uuid.v4 / Random.* / Time.now / Crypto.random*) freezes it to a
            // single value. `expr_is_task` skips a bare `x = Uuid.v4` (a
            // re-runnable Task value — not forced, no footgun); it fires only
            // when the effect was run to a plain value (`Task.run Uuid.v4 |>
            // …`). Warn, don't error — a single shared value is occasionally
            // intended.
            if let Some(r) = root {
                if !self.expr_is_task(r) {
                    if let Some((m, f)) = self.find_fresh_value_kernel(r) {
                        let short = m.rsplit('.').next().unwrap_or(&m).to_string();
                        self.warnings.push(format!(
                            "top-level `{name}` runs `{short}.{f}` and is memoised to a SINGLE \
                             value (evaluated once, then cached). If you want a fresh value per \
                             use, make it a function: `{name} () = …` and call `{name} ()`. \
                             Ignore this if one shared value is intended."
                        ));
                    } else if let Some(kind) = self.def_effect.get(&self.cur_def).copied() {
                        // Laundered effect: the fresh-value / DB read is hidden
                        // behind a helper (`listActive = withConnList (\c ->
                        // Store.query …)`, `errRef = … (nowMs ())`), so the
                        // direct-kernel scan above (no lambda descent, only a
                        // DIRECT `Ffi.kernel` alias) misses it. Suppress the
                        // blessed memoised-HANDLE contract (`db = Task.run
                        // (Db.connect ())` → result type `Db`) and a function-
                        // typed result (a returned lambda isn't forced at memo
                        // time — the effect only fires when the caller runs it).
                        // Fire only when the frozen RESULT is stale-able data.
                        // A handle/`Store`/config, a `Result _ ()` completion
                        // signal, a `Unit`, or a function result is never a
                        // stale-read footgun (its effectful lambdas, if any, are
                        // stored deferred config, not forced at memo time).
                        // Unknown result type → suppress (conservative).
                        let is_data = self
                            .def_result_tys
                            .get(&self.cur_def)
                            .is_some_and(is_stale_data_result);
                        if is_data {
                            let what = match kind {
                                EffectKind::Fresh => "a clock/entropy read",
                                EffectKind::StoreRead => "a database read",
                            };
                            self.warnings.push(format!(
                                "top-level `{name}` is memoised to a SINGLE value (evaluated \
                                 once, then cached) but forcing it performs {what} through a \
                                 helper — so the result is frozen for the whole process and \
                                 won't reflect later writes. For a fresh read per use make it a \
                                 function: `{name} () = …` and call `{name} ()`. Ignore this if \
                                 one shared snapshot is intended."
                            ));
                        }
                    }
                }
            }
            let caf_var = format!("{go_name}__caf");
            let cell_ty = GoTy::Named("rt.LazyCaf".to_string(), vec![ret_ty.clone()]);
            // compute := func() T { <original body> }
            let compute = GoExpr::new(
                GoExprKind::FuncLit(Vec::new(), ret_ty.clone(), body),
                GoTy::Func(Vec::new(), Box::new(ret_ty.clone())),
            );
            // {caf_var}.Get(compute)
            let cell_ident = GoExpr::new(GoExprKind::Ident(caf_var.clone()), cell_ty.clone());
            let get_sel = GoExpr::new(
                GoExprKind::Selector(Box::new(cell_ident), "Get".to_string()),
                GoTy::Any,
            );
            let get_call = GoExpr::new(
                GoExprKind::Call(Box::new(get_sel), vec![compute]),
                ret_ty.clone(),
            );
            let accessor = GoFuncDecl {
                name: go_name.clone(),
                type_params: Vec::new(),
                params: Vec::new(),
                ret: ret_ty,
                body: vec![GoStmt::Return(Some(get_call))],
                doc: None,
            };
            return vec![GoItem::Var(caf_var, cell_ty, None), GoItem::Func(accessor)];
        }

        vec![GoItem::Func(GoFuncDecl {
            name: go_name,
            type_params: Vec::new(),
            params,
            ret: ret_ty,
            body,
            doc: None,
        })]
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
                // An unannotated destructured param (a tuple pattern on a
                // `let`-bound local fn) arrives with Go type `any` when the fn is
                // passed through a HOF that erases the callback to
                // `func(any,any)any` (foldl/foldr). `pattern_test` self-heals an
                // `any` tuple subject reflectively (via `rt.TupleField`, see the
                // `Pattern::Tuple` arm) — #170 — so no subject coercion is needed
                // here; passing the raw `any` subject is correct.
                let subj = GoExpr::new(GoExprKind::Ident(n.clone()), ty.clone());
                let (_cond, binds) = self.pattern_test(&subj, &ty, p);
                (n, ty, binds)
            }
        }
    }

    /// Reconstruct a destructured param pattern's concrete Go type from its
    /// binder locals' inferred types — so an `any`-typed tuple param can be
    /// coerced to `rt.T{n}[…]` before `pattern_test` reads its `.V{i}` fields
    /// (#170). A tuple's element types come from each sub-pattern's binder local
    /// (`( trues, falses )` where both binders infer `List a` → `rt.T2[[]any,
    /// []any]`). Returns `None` for shapes this can't reconstruct (records,
    /// nested ADTs — their `any` subject is left as-is), so the caller keeps the
    /// pre-#170 behaviour there.
    fn pattern_binder_goty(&mut self, p: PatId) -> Option<GoTy> {
        match self.body.pats[p].clone() {
            Pattern::Var(lid) => Some(self.local_ty(lid)),
            Pattern::Tuple(pats) => {
                let elems: Vec<GoTy> = pats
                    .iter()
                    .map(|sp| self.pattern_binder_goty(*sp).unwrap_or(GoTy::Any))
                    .collect();
                Some(GoTy::Tuple(elems))
            }
            _ => None,
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
            // Pre-register every binder's Go name so a forward reference resolves
            // to the name its binding will emit (matches `lower_let_expr`).
            for d in &defs {
                for (bn, lid) in &d.binders {
                    let _ = self.fresh_local_named(*lid, Some(bn.as_str()));
                }
            }
            // Dependency-order the defs (Go declare-before-use vs Sky's out-of-order
            // forward references — `let a = b + 1; b = 5`).
            for &i in &self.order_let_defs(&defs) {
                self.lower_let_def(&defs[i], out);
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
                entry_task_run(lowered, out);
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
                    // Discard EVERY binder `pattern_test` introduced, not just the
                    // let-def's top-level `d.binders`. A tuple / nested destructure
                    // binds intermediate `v_N` temps (`(a, b, c, d) = quad` →
                    // `v_0..v_3`) that are ABSENT from `d.binders`; when the body
                    // ignores one (`… use a, b …`), the unused `v_N` tripped Go's
                    // "declared and not used" and `go build` failed (the oracle
                    // compiles it). Walk the emitted `Short` binds directly so the
                    // discard set matches exactly what was bound. `_ = x` is legal
                    // Go even when `x` is later used (matches the oracle's
                    // per-binding discard).
                    let discard_names: Vec<String> = binds
                        .iter()
                        .filter_map(|s| match s {
                            GoStmt::Short(n, _) if n != "_" => Some(n.clone()),
                            _ => None,
                        })
                        .collect();
                    out.extend(binds);
                    for name in discard_names {
                        out.push(GoStmt::Expr(GoExpr::new(
                            GoExprKind::Ident(format!("_ = {name}")),
                            GoTy::Unit,
                        )));
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
            let is_fn = !d.params.is_empty();
            let lowered = if !is_fn {
                let ty = self.expr_ty(d.body);
                self.lower_expr(d.body, &ty)
            } else {
                self.lower_local_fn(&d.params, d.body)
            };
            self.local_tys.insert(lid, lowered.ty.clone());
            // A SELF-RECURSIVE local function references its own name inside the
            // closure body. Go's `name := func(){ … name … }` leaves `name`
            // undefined in its own initializer, so declare then assign — `var name
            // T; name = func…` (issue #162). Only local FUNCTIONS can be
            // self-recursive here (a value `x = … x …` is ill-typed and never
            // reaches codegen), so gate on `is_fn` to keep every ordinary binding
            // on the `:=` path byte-identical.
            let recursive = is_fn && {
                let mut refs: HashSet<LocalId> = HashSet::new();
                self.collect_local_refs(d.body, &mut refs);
                refs.contains(&lid)
            };
            if recursive {
                out.push(GoStmt::VarDecl(gname.clone(), lowered.ty.clone()));
                out.push(GoStmt::Assign(gname.clone(), lowered));
            } else {
                out.push(GoStmt::Short(gname.clone(), lowered));
            }
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
        // Row-polymorphism detection for the LOCAL fn — the same rule `lower_def`
        // applies to a top-level def's params, but scoped to THIS closure's
        // params + its body result. A param whose inferred type is an OPEN record
        // sharing its row-var with the result (or another param) must lower to
        // `any` so the body's record reads/updates take the reflective
        // `rt.Field`/`rt.RecordUpdate` path. A subset closed struct would DROP the
        // row-carried fields (#171: `addValues acc = { acc | value = … }` erased
        // `acc` to `struct{Value any}` and lost `name`, so a foldl accumulator
        // came back with a zeroed `name`). `lower_def` runs this for top-level
        // defs; local fns went through `bind_param` with no row-poly awareness.
        let (rp_params, rp_result) = self.local_fn_row_poly(params, body);

        let mut gparams: Vec<GoParam> = Vec::new();
        let mut destructure: Vec<GoStmt> = Vec::new();
        let mut ptys: Vec<GoTy> = Vec::new();
        for (i, p) in params.iter().enumerate() {
            let (pname, mut pty, binds) = self.bind_param(*p, None);
            if *rp_params.get(i).unwrap_or(&false) {
                pty = GoTy::Any;
                if let Pattern::Var(id) = &self.body.pats[*p] {
                    self.local_tys.insert(*id, GoTy::Any);
                }
            }
            ptys.push(pty.clone());
            gparams.push(GoParam {
                name: pname,
                ty: pty,
            });
            destructure.extend(binds);
        }
        // Row-poly result → `any` (matches the reflective `rt.RecordUpdate` body),
        // mirroring `lower_def`'s `result_row_poly` handling.
        let ret_ty = if rp_result {
            GoTy::Any
        } else {
            self.expr_ty(body)
        };
        let b = self.lower_expr(body, &ret_ty);
        let mut stmts = destructure;
        stmts.push(GoStmt::Return(Some(b)));
        let fn_ty = GoTy::Func(ptys, Box::new(ret_ty.clone()));
        GoExpr::new(GoExprKind::FuncLit(gparams, ret_ty, stmts), fn_ty)
    }

    /// Row-polymorphism flags for a LOCAL fn — `(per-param, result)`. Mirrors the
    /// free `row_poly_flags` (which reads the enclosing def's `body.params` +
    /// `types.result`), but over THIS closure's `params` and its body-expr result
    /// type (`types.exprs[body]`). A position is row-poly when its inferred type
    /// is an OPEN record whose extension-var name is SHARED (count ≥ 2) across the
    /// param/result positions — the row var flows through, as in
    /// `\acc -> { acc | value = … }`.
    fn local_fn_row_poly(&self, params: &[PatId], body: ExprId) -> (Vec<bool>, bool) {
        use std::collections::HashMap as Hm;
        let param_tys: Vec<Option<Ty>> = params
            .iter()
            .map(|p| match &self.body.pats[*p] {
                Pattern::Var(id) => self.types.locals.get(id).cloned(),
                _ => None,
            })
            .collect();
        let result_ty = self.types.exprs.get(&body).cloned();
        let mut counts: Hm<Name, u32> = Hm::new();
        for t in param_tys
            .iter()
            .map(|t| t.as_ref())
            .chain(std::iter::once(result_ty.as_ref()))
        {
            if let Some(name) = record_ext_name(t) {
                *counts.entry(name.clone()).or_insert(0) += 1;
            }
        }
        let is_rp =
            |t: Option<&Ty>| record_ext_name(t).is_some_and(|n| counts.get(n).copied().unwrap_or(0) >= 2);
        let pflags = param_tys.iter().map(|t| is_rp(t.as_ref())).collect();
        let rflag = is_rp(result_ty.as_ref());
        (pflags, rflag)
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
        // Same principle, applied to a RECORD LITERAL: `{ key = k, value = v }`
        // has no identity beyond its fields, so the slot it flows into is the
        // authoritative statement of which record it is. The literal's own
        // inferred type is frequently under-determined — the lowerer's typed
        // table is deliberately NOT annotation-seeded (`Typer::body_types`), so a
        // constructor's param-valued fields read back as unsolved vars — and
        // `sky_ty_to_go` then cannot pick a nominal for it. Taking the slot type
        // is both more accurate and strictly more typed: the literal is built AS
        // the declared struct (`Main_Kv_R{Key: k, Value: v}`) instead of being
        // built anonymously and narrowed back with `rt.Coerce`.
        //
        // Guarded to the case where the slot is a nominal record carrying EXACTLY
        // this literal's field names, so it can only ever replace a coercion into
        // that same nominal — never re-target the literal at an unrelated shape.
        if *expected != GoTy::Any && actual != *expected {
            if let (Expr::Record(fields), GoTy::Named(n, _)) = (&self.body.exprs[e], expected) {
                if let Some(decl) = self.record_fields.get(n.as_str()) {
                    // `record_fields` is keyed by the GO field name (capitalised);
                    // the literal carries Sky names.
                    let mut lit: Vec<String> =
                        fields.iter().map(|(n, _)| capitalize(n.as_str())).collect();
                    let mut dec: Vec<String> = decl.iter().map(|(n, _)| n.clone()).collect();
                    lit.sort_unstable();
                    dec.sort_unstable();
                    if lit == dec {
                        actual = expected.clone();
                    }
                }
            }
        }
        // `expected` is threaded alongside `actual` because a KERNEL referenced as
        // a value is emitted as its raw `any`-based runtime symbol: whether that
        // needs a bridge depends on the SLOT it lands in, not on the node's own
        // inferred type. See `kernel_value_eta` / `nullary_kernel_value`.
        let node = self.lower_expr_inner(e, &actual, expected);
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

    fn lower_expr_inner(&mut self, e: ExprId, actual: &GoTy, expected: &GoTy) -> GoExpr {
        match &self.body.exprs[e] {
            Expr::Int(n) => GoExpr::new(GoExprKind::IntLit(*n), actual.clone()),
            Expr::Float(f) => GoExpr::new(GoExprKind::FloatLit(*f), GoTy::Bare(Prim::Float)),
            Expr::Str(s) => GoExpr::new(GoExprKind::StrLit(s.to_string()), GoTy::Bare(Prim::Str)),
            Expr::Bool(b) => GoExpr::new(GoExprKind::BoolLit(*b), GoTy::Bare(Prim::Bool)),
            Expr::Chr(s) => rune_lit(s),
            Expr::Unit => GoExpr::new(GoExprKind::Ident("struct{}{}".into()), GoTy::Unit),
            Expr::Var(res) => self.lower_var(res.clone(), actual, expected),
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
                // A record ALIAS is NOT monomorphised: a generic field
                // (`type alias Payload a = { value : a, ... }`) erases to a Go
                // `any` field (`Value any`) regardless of the instantiation. When
                // that `any` field is consumed at a CONCRETE slot — directly
                // (`p.value` with `p : Payload Int`) OR after being carried in an
                // ADT variant and re-bound in a case arm (`Filled rec -> rec.value`,
                // where the variant field `V0 Main_Payload_R` binds `rec`
                // nominally) — Go rejects the uncoerced `any` in the concrete
                // context (`cannot use v.Value (any) as int`). Narrow it via
                // `rt.Coerce`, mirroring the container-payload narrowing in
                // `bind_field_pat` / the cons-tail reads. Only fires when the
                // field's DECLARED Go type is `any` and the use slot is concrete —
                // concrete-typed fields keep byte-identical emission.
                let declared_field_ty: Option<GoTy> = match &b.ty {
                    GoTy::Struct(fs) => fs
                        .iter()
                        .find(|(n, _)| n.as_str() == cap)
                        .map(|(_, t)| t.clone()),
                    GoTy::Named(n, _) => {
                        let sky = self
                            .record_fields
                            .get(n)
                            .and_then(|fs| fs.iter().find(|(fn_, _)| fn_.as_str() == cap))
                            .map(|(_, t)| t.clone());
                        sky.map(|t| self.goty(&t))
                    }
                    _ => None,
                };
                if declared_field_ty.as_ref() == Some(&GoTy::Any) && *actual != GoTy::Any {
                    let sel = GoExpr::new(GoExprKind::Selector(Box::new(b), cap), GoTy::Any);
                    return self.coerce_if_needed(sel, actual);
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
                GoExpr::new(GoExprKind::Selector(Box::new(b), cap), field_ty)
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
                    GoExprKind::Ident(format!(
                        "func(_r any) any {{ return rt.Field(_r, \"{f}\") }}"
                    )),
                    // Type it as the `func(any) any` it actually IS, not bare `any`.
                    // A consumer that applies it (`… |> .field`, `List.map .field
                    // xs`) then sees the closure's `any` RESULT and narrows it to
                    // the concrete slot via `coerce_if_needed`; typing it `any`
                    // made the application result inherit the slot type and skip
                    // the narrowing, so `rt.Field`'s `any` reached e.g. a `return
                    // int` unconverted (issue #161).
                    GoTy::Func(vec![GoTy::Any], Box::new(GoTy::Any)),
                )
            }
            Expr::Error => {
                self.warnings
                    .push("lowered an Expr::Error recovery node".into());
                GoExpr::new(GoExprKind::Nil, GoTy::Any)
            }
        }
    }

    fn lower_var(&mut self, res: Res, actual: &GoTy, expected: &GoTy) -> GoExpr {
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
                let ty = self
                    .local_tys
                    .get(&id)
                    .cloned()
                    .unwrap_or_else(|| actual.clone());
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
                        self.nullary_kernel_value(&go, actual, expected)
                    } else if let Some(eta) = self.kernel_value_eta(&go, expected) {
                        // Point-free alias of an arity-≥1 kernel
                        // (`joinStr = String.append`) landing in a concretely-typed
                        // func slot — eta-expand rather than emit the raw
                        // `func(any…) any` symbol. See `kernel_value_eta`.
                        eta
                    } else if let Some(n) = crate::kernel::generic_kernel_value_tyargs(&go) {
                        // A GENERIC runtime kernel used as a first-class value
                        // (`JsonEnc.list identity args`): Go requires the type
                        // args explicit — `any(rt.Basics_identity)` is rejected
                        // ("cannot use generic function … without instantiation").
                        // Instantiate with `any` (Sky's polymorphism erases to
                        // `any` here). A directly-called reference never reaches
                        // this arm — Go infers the args at the call site.
                        let inst = format!("{go}[{}]", ["any"].repeat(n).join(", "));
                        GoExpr::new(GoExprKind::Ident(inst), actual.clone())
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
                        //
                        // When the def's ACTUAL Go return type is `any` — e.g. a
                        // row-polymorphic value `ada = bump {name, age}` whose
                        // callee `bump` returns `any` — but the CALLER's local
                        // inference typed this reference as a concrete struct
                        // (`{name}` from a later `ada.name`), the two disagree and
                        // `go build` rejects `Main_ada().Name` (the func returns
                        // `any`, which has no fields). Trust the def's real return
                        // type: emit the call as `any` so field reads route
                        // through `rt.Field` (see `Expr::Access`). Only overrides
                        // toward `any` (never away from a concrete type), so every
                        // ordinary zero-param call is byte-identical.
                        // NOTE: `actual` (the caller's region inference) can
                        // disagree with the func's REAL emitted return, and NOT only
                        // by full erasure to `any`: an unannotated def whose body
                        // infers `[]any` in isolation but is pinned to a concrete
                        // `[]map[string]string` by THIS consumer leaves the func
                        // emitting `[]any` while `actual` says the concrete slice
                        // (13-skyshop `listAllProducts` into a `Model.products`
                        // update). Typing the call by `actual` there skips the
                        // coercion (types look equal) and `go build` rejects the
                        // `[]any` value in the concrete field. Type the call by the
                        // func's real return (`def_result_tys` invariant: equals what
                        // `lower_def` emits) so the outer `coerce_if_needed` bridges
                        // it to the slot (`rt.AsListT[map[string]string](…)`). When
                        // `def_ret` and `actual` agree (the common case) this is
                        // byte-identical; `any` field reads still route through
                        // `rt.Field` because `def_ret == any → call_ty == any`.
                        let def_ret = self.def_result_tys.get(&d).cloned();
                        let call_ty = match def_ret {
                            Some(t) => self.goty(&t),
                            None => actual.clone(),
                        };
                        let fn_ty = GoTy::Func(vec![], Box::new(call_ty.clone()));
                        GoExpr::new(
                            GoExprKind::Call(
                                Box::new(GoExpr::new(GoExprKind::Ident(go), fn_ty)),
                                vec![],
                            ),
                            call_ty,
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
                    self.nullary_kernel_value(&go, actual, expected)
                } else if let Some(eta) = self.kernel_value_eta(&go, expected) {
                    // Same point-free class as the kernel-alias arm above, for a
                    // DIRECT kernel reference. See `kernel_value_eta`.
                    eta
                } else if let Some(n) = crate::kernel::generic_kernel_value_tyargs(&go) {
                    // A GENERIC runtime kernel used as a first-class value
                    // (`JsonEnc.list identity args`): Go requires the type args
                    // explicit — `any(rt.Basics_identity)` is rejected ("cannot
                    // use generic function … without instantiation"). Instantiate
                    // with `any` (Sky's polymorphism erases to `any` here). A
                    // directly-called reference never reaches this arm — Go infers
                    // the args at the call site.
                    let inst = format!("{go}[{}]", ["any"].repeat(n).join(", "));
                    GoExpr::new(GoExprKind::Ident(inst), actual.clone())
                } else {
                    GoExpr::new(GoExprKind::Ident(go), actual.clone())
                }
            }
            Res::Ctor(cr) => {
                let pin = self.pinned_union_go(cr.type_);
                self.lower_ctor_value(cr.def, actual, pin)
            }
            Res::Foreign { package, name } => {
                self.warnings.push(format!(
                    "foreign ref {}.{}",
                    package.as_str(),
                    name.as_str()
                ));
                GoExpr::new(GoExprKind::Ident("nil".into()), GoTy::Any)
            }
            Res::Error => GoExpr::new(GoExprKind::Nil, GoTy::Any),
        }
    }

    /// Emit a nullary kernel *value* (`Dict.empty`, `Cmd.none`, `Uuid.v7`,
    /// `Math.pi`): its runtime symbol is a zero-arg func, so CALL it (a bare
    /// `func() T` in a value slot panics). The symbol returns Go `any`
    /// (`func Dict_empty() any`, `func Uuid_v7() any`).
    ///
    /// Historically the call node was typed `actual` — a LIE about the symbol.
    /// The lie is harmless while the value is only widened (`any(rt.Dict_empty())`
    /// is valid Go whatever the node claims), which is why it survived; it becomes
    /// a defect the moment the value lands in a CONCRETELY-typed slot, because the
    /// node then claims to already be that type, `coerce_if_needed` sees
    /// `x.ty == expected` and inserts nothing, and raw `any` reaches the typed Go
    /// slot: `return rt.Uuid_v7()` in a `rt.SkyTask[…]` return, which `go build`
    /// rejects ("need type assertion").
    ///
    /// So the narrowing is driven by the SLOT (`expected`), not by the node's own
    /// inferred type: type the call `Any` (the truth) and coerce, but only when
    /// the slot actually demands a concrete type. An `any` slot keeps the
    /// historical bare-call form, so every emission that was already correct is
    /// byte-identical — the `coerce-floor` ratchet is what caught the first,
    /// over-eager version of this fix widening 13 examples.
    ///
    /// The one slot that must NOT be coerced even when concrete is an ANY-KEY map
    /// (`map[interface{}]interface{}` — a `Dict any any` from a widened `foldl`
    /// accumulator). A Sky `Dict k v` is `map[string]V` at runtime, and
    /// `rt.AsMapT` REBUILDS a `map[string]V`, which is a different (invariant) Go
    /// map type, so there is no sound narrowing available.
    fn nullary_kernel_value(&mut self, go: &str, actual: &GoTy, expected: &GoTy) -> GoExpr {
        let any_key_map = matches!(actual, GoTy::Map(k, _) if **k == GoTy::Any);
        // A concrete-key map slot narrowed via `rt.AsMapT` even when the node was
        // only being widened — preserved verbatim so its emission does not move.
        let concrete_key_map = matches!(actual, GoTy::Map(k, _) if **k != GoTy::Any);
        if !any_key_map && (concrete_key_map || *expected != GoTy::Any) {
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

    /// A kernel referenced as a VALUE (never applied) landing in a slot that
    /// demands a CONCRETELY-typed Go function — the point-free alias class
    /// (`tickle = String.toUpper`, `joinStr = String.append`).
    ///
    /// Every runtime kernel is `any`-based (`func String_append(a, b any) any` —
    /// see the [`crate::kernel`] module doc), so the bare symbol is a
    /// `func(any…) any` and `go build` rejects it in a `func(string, string) string`
    /// slot. Emitting it bare while *typing the node `actual`* was the defect: the
    /// node claimed to be the slot's func type, so `coerce_if_needed` inserted
    /// nothing and the raw symbol reached the typed slot verbatim.
    ///
    /// Bridge it with the SAME eta-expansion a partial application already uses —
    /// [`Self::kernel_partial`] with zero given args — producing a closure whose
    /// params carry the slot's concrete types, widening each into the `any`-based
    /// call and coercing the `any` result back to the slot's return type.
    ///
    /// The decision is driven by the SLOT (`expected`), never by the reference's
    /// own inferred type. A kernel passed as a HOF callback
    /// (`List.map String.toUpper xs`) has a perfectly concrete inferred type
    /// (`func(string) string`) yet is only ever widened — `any(rt.String_toUpper)`
    /// is valid Go — so eta-expanding it would add a runtime coercion to output
    /// that was already correct. The first version of this fix keyed on the
    /// inferred type and did exactly that; `coerce-floor` caught it widening the
    /// runtime-coercion floor across 13 examples.
    ///
    /// Returns `None` (leaving the historical bare emission byte-identical) unless
    /// the SLOT is a func of exactly the kernel's RUNTIME arity whose shape the raw
    /// symbol cannot already satisfy:
    ///   * a non-func slot — including `any`, i.e. "only widened" — needs no bridge;
    ///   * an all-`any` `func(any…) any` slot IS the symbol's own shape;
    ///   * an arity mismatch means the slot is not this symbol's call shape
    ///     (a function-returning kernel whose curried Sky type over-counts), and
    ///     eta-expanding to the wrong arity would emit a bad call.
    fn kernel_value_eta(&mut self, go: &str, expected: &GoTy) -> Option<GoExpr> {
        let GoTy::Func(params, ret) = expected else {
            return None;
        };
        let arity = self.kernel_runtime_arity(go)?;
        if arity == 0 || params.len() != arity {
            return None;
        }
        if params.iter().all(|p| *p == GoTy::Any) && **ret == GoTy::Any {
            return None;
        }
        Some(self.kernel_partial(go, &[], arity, expected))
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
                let ps = if adt_like {
                    vec![GoTy::Any; arity]
                } else {
                    ps.clone()
                };
                (ps, (**r).clone())
            }
            _ => (vec![GoTy::Any; arity], GoTy::Any),
        };
        let mut gparams: Vec<GoParam> = Vec::new();
        let mut arg_exprs: Vec<GoExpr> = Vec::new();
        for pty in param_tys.iter().take(arity) {
            let pname = format!("_p{}", self.local_counter);
            self.local_counter += 1;
            gparams.push(GoParam {
                name: pname.clone(),
                ty: pty.clone(),
            });
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
        let builtin = matches!(
            cname.as_str(),
            "Ok" | "Err" | "Just" | "Nothing" | "True" | "False"
        );
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
            None => args
                .iter()
                .map(|a| self.lower_expr(*a, &GoTy::Any))
                .collect(),
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
        let mut arg_exprs: Vec<GoExpr> = given
            .iter()
            .map(|a| self.lower_expr(*a, &GoTy::Any))
            .collect();
        let n_rest = arity - given.len();
        let (rest_tys, ret): (Vec<GoTy>, GoTy) = match actual {
            GoTy::Func(ps, r) if ps.len() == n_rest => (ps.clone(), (**r).clone()),
            _ => (vec![GoTy::Any; n_rest], GoTy::Any),
        };
        let mut gparams: Vec<GoParam> = Vec::new();
        for pty in &rest_tys {
            let pname = format!("_p{}", self.local_counter);
            self.local_counter += 1;
            gparams.push(GoParam {
                name: pname.clone(),
                ty: pty.clone(),
            });
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
            GoTy::Named(n, ts) if n == "rt.SkyResult" && ts.len() == 2 => (
                format!("[{}, {}]", render_goty(&ts[0]), render_goty(&ts[1])),
                String::new(),
            ),
            GoTy::Named(n, ts) if n == "rt.SkyMaybe" && ts.len() == 1 => {
                (String::new(), format!("[{}]", render_goty(&ts[0])))
            }
            _ => (String::new(), String::new()),
        };
        // `Ok`/`Just` wrap their argument, so the payload type param is the
        // argument's type. When the EXPECTED payload is an anonymous struct — a
        // record row narrowed by field-access inference at this site (e.g. a
        // function that reads only some fields of a record param but returns the
        // whole record) — while the argument lowered to the full named record,
        // the expected type is the narrowed one. Trust the argument's fuller
        // named type so we emit `rt.Ok[E, User_R](v)`, not
        // `rt.Ok[E, struct{…subset…}](v)` (which fails `go build`).
        let payload_from_arg = |expected: &GoTy| -> GoTy {
            match (expected, lowered_args.first().map(|a| &a.ty)) {
                (GoTy::Struct(_), Some(GoTy::Named(n, targs))) => {
                    GoTy::Named(n.clone(), targs.clone())
                }
                _ => expected.clone(),
            }
        };
        let ok_actual = match actual {
            GoTy::Named(n, ts) if n == "rt.SkyResult" && ts.len() == 2 => {
                GoTy::Named(n.clone(), vec![ts[0].clone(), payload_from_arg(&ts[1])])
            }
            _ => actual.clone(),
        };
        let ok_ea = match &ok_actual {
            GoTy::Named(n, ts) if n == "rt.SkyResult" && ts.len() == 2 => {
                format!("[{}, {}]", render_goty(&ts[0]), render_goty(&ts[1]))
            }
            _ => res_ea.clone(),
        };
        let just_actual = match actual {
            GoTy::Named(n, ts) if n == "rt.SkyMaybe" && ts.len() == 1 => {
                GoTy::Named(n.clone(), vec![payload_from_arg(&ts[0])])
            }
            _ => actual.clone(),
        };
        let just_a = match &just_actual {
            GoTy::Named(n, ts) if n == "rt.SkyMaybe" && ts.len() == 1 => {
                format!("[{}]", render_goty(&ts[0]))
            }
            _ => maybe_a.clone(),
        };
        let expr = match cname.as_str() {
            "Ok" => call_rt(&format!("rt.Ok{ok_ea}"), lowered_args, ok_actual),
            "Err" => call_rt(&format!("rt.Err{res_ea}"), lowered_args, actual.clone()),
            "Just" => call_rt(&format!("rt.Just{just_a}"), lowered_args, just_actual),
            "Nothing" => {
                let a = match actual {
                    GoTy::Named(n, ts) if n == "rt.SkyMaybe" && ts.len() == 1 => {
                        render_goty(&ts[0])
                    }
                    _ => "any".to_string(),
                };
                GoExpr::new(
                    GoExprKind::Ident(format!("rt.Nothing[{a}]()")),
                    actual.clone(),
                )
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
                            call_rt(
                                &format!("{go_type}_{cname}"),
                                args_out,
                                GoTy::Named(go_type, vec![]),
                            )
                        }
                        NominalKind::Record => {
                            let ctor = go_type.trim_end_matches("_R").to_string();
                            self.used_types.insert(go_type.clone());
                            // For a GENERIC record ctor (`Cfg_R[T1,…]`) called at
                            // a generic slot (`Cfg_R[Msg]`), map each type-param
                            // field to its concrete instantiation so the arg
                            // coerces to `Msg` (not the erased `any`) and Go's
                            // call-site inference pins `Cfg(...)` to `Cfg_R[Msg]`.
                            // The result GoTy is then the honest `Cfg_R[Msg]`.
                            let generic_subst: HashMap<Name, GoTy> = match actual {
                                GoTy::Named(n, args) if *n == go_type && !args.is_empty() => self
                                    .env
                                    .record_params
                                    .get(&go_type)
                                    .map(|params| {
                                        params.iter().cloned().zip(args.iter().cloned()).collect()
                                    })
                                    .unwrap_or_default(),
                                _ => HashMap::new(),
                            };
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
                                    fs.iter()
                                        .map(|(_, t)| {
                                            sky_ty_to_go_params(
                                                t,
                                                self.env,
                                                Some(&self.cur_module),
                                                &generic_subst,
                                            )
                                        })
                                        .collect()
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
                            let res_ty = if generic_subst.is_empty() {
                                GoTy::Named(go_type.clone(), vec![])
                            } else {
                                actual.clone()
                            };
                            call_rt(&ctor, coerced, res_ty)
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
            if let Some(a) = self
                .ctor_arity_in_union
                .get(&(gt.to_string(), cname.to_string()))
            {
                return *a;
            }
        }
        self.ctor_arity
            .get(cname)
            .copied()
            .unwrap_or_else(|| builtin_ctor_arity(cname))
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

    /// Owning-union Go name + kind for a ctor PATTERN. Prefers the pattern's
    /// resolved `CtorRef.type_` DefId (module-correct) via `pinned_union_go` —
    /// this is what closes the cross-module same-named-ctor collision (finding
    /// C3): `case alphaVal of Alpha.Leaf s -> …` where a sibling module also
    /// declares `type Prim = Leaf … | …` used to assert against
    /// `Beta_Prim_Leaf_V` because the bare-name `ctor_owner` map is
    /// last-writer-wins. The resolved ctor knows its own union, so honour it.
    /// Falls back to the subject's pinned nominal, then the bare-name map
    /// (unresolved ctor / no `CtorRef` — the pre-existing behaviour).
    fn ctor_union_owner(
        &self,
        ctor_ty: Option<DefId>,
        cname: &str,
        subj_ty: &GoTy,
    ) -> Option<(String, NominalKind)> {
        if let Some(t) = ctor_ty {
            if let Some(go) = self.pinned_union_go(t) {
                // Kind is authoritative from the union-scoped map (go is
                // module-correct); the bare map is a same-kind safety net.
                let kind = self
                    .ctor_in_union
                    .get(&(go.clone(), cname.to_string()))
                    .map(|(k, _)| *k)
                    .or_else(|| self.ctor_owner.get(cname).map(|(_, k)| *k))
                    .unwrap_or(NominalKind::Adt);
                return Some((go, kind));
            }
        }
        match subj_ty {
            GoTy::Named(gt, _) => self
                .ctor_in_union
                .get(&(gt.clone(), cname.to_string()))
                .map(|(k, _)| (gt.clone(), *k))
                .or_else(|| self.ctor_owner.get(cname).cloned()),
            _ => self.ctor_owner.get(cname).cloned(),
        }
    }

    /// Declaration-order tag for `cname` in a resolved union `go` (falls back to
    /// the bare-name `ctor_tag` map when the union-scoped entry is absent).
    fn union_ctor_tag(&self, go: &str, cname: &str) -> usize {
        self.ctor_in_union
            .get(&(go.to_string(), cname.to_string()))
            .map(|(_, t)| *t)
            .or_else(|| self.ctor_tag.get(cname).copied())
            .unwrap_or(0)
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
                // A VARIADIC-backed alias takes its arity from the declared Sky
                // sig (`kernel_alias_arity`), correcting the Go-source scan's
                // mis-count. Every other alias falls back to the Go scan, which
                // is authoritative (and the only correct source for a
                // `Handler`-returning alias whose curried sig over-counts).
                let arity = self
                    .kernel_alias_arity
                    .get(d)
                    .copied()
                    .or_else(|| self.kernel_runtime_arity(&go));
                if let Some(arity) = arity {
                    if arity > args.len() {
                        return self.kernel_partial(&go, args, arity, actual);
                    }
                }
                return self.kernel_call(&go, args, actual);
            }
        }
        // kernel direct call
        if let Expr::Var(Res::Kernel { module, func }) = &self.body.exprs[callee] {
            let go = kernel_go_name(module.as_str(), func.as_str());
            // Partial application: Sky curries but the Go runtime symbol does not,
            // so an under-applied kernel must eta-expand into a closure instead of
            // emitting an under-applied call. The arity comes from the runtime
            // param count (authoritative), NOT the curried HM type (which
            // over-counts for function-returning kernels).
            if let Some(arity) = self.kernel_runtime_arity(&go) {
                if arity > args.len() {
                    return self.kernel_partial(&go, args, arity, actual);
                }
            }
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
            // A Go-FFI call with no wrapper symbol for `name`. Two distinct
            // causes need two distinct developer actions:
            //   1. NO surface loaded for the package → it was never `sky
            //      install`ed (the `sky-ffi/` surface is a gitignored build
            //      artifact, absent on a fresh clone). Point the dev at `sky
            //      install` — blaming the symbol here is actively misleading.
            //   2. Surface IS present but this symbol is genuinely absent /
            //      inexpressible (e.g. it takes a Go `error` parameter, whose
            //      wrapper is deliberately not emitted — see
            //      `ffi::gen::has_error_param`).
            // Falling through would lower the callee to `nil` → `nil(args)`,
            // which `go build` rejects; reject at check time so `sky check ≡ sky
            // build` holds.
            let pkg = package.as_str();
            let fun = name.as_str();
            // A real Go-FFI package is an import path (`github.com/…`) or a Go
            // std package (`fmt`, `strings`) — NEVER a Sky-namespaced
            // `Std.*` / `Sky.*` qualifier. So a Sky-namespaced name reaching
            // this Foreign fallthrough is an unknown/misspelled STDLIB module
            // (`Std.Lst` for `Std.List`), not a missing Go module. Pointing the
            // dev at `sky install` there is actively misleading — it can never
            // fetch a Sky stdlib module.
            let sky_namespaced = pkg.starts_with("Std.") || pkg.starts_with("Sky.");
            let msg = if sky_namespaced {
                format!(
                    "unknown Sky module `{pkg}`, so `{pkg}.{fun}` cannot be resolved. \
                     Check the spelling of the import — Sky stdlib modules live under \
                     `Std.*` and `Sky.Core.*` / `Sky.Http.*` (e.g. `Sky.Core.List`, \
                     `Std.Db`). This is not a Go-FFI package; `sky install` won't fetch it."
                )
            } else if self.ffi.has_package(pkg) {
                format!(
                    "no such Go-FFI function `{pkg}.{fun}` — the FFI surface for `{pkg}` \
                     is present but exports no such function, or it takes a value that \
                     cannot be produced from Sky (such as a Go `error` parameter). It \
                     cannot be called from Sky."
                )
            } else {
                format!(
                    "`{pkg}` has no generated FFI surface, so `{pkg}.{fun}` cannot be \
                     resolved. Run `sky install` to fetch and inspect its Go module — \
                     the `sky-ffi/` surface is a generated build artifact (regenerated \
                     from your `sky.toml` dependencies), not committed to the repo."
                )
            };
            self.errors.push(msg);
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
                    ptys.iter()
                        .map(|t| self.goty_in(t, &callee_mod))
                        .collect::<Vec<_>>()
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
                // Invariant: guarded by `args.len() >= 2`, so `last()` is `Some`.
                let last = args.last().unwrap_or_else(|| {
                    base::bug!("combinator arg list emptied under len>=2 guard")
                });
                // Prefer the source's DECLARED Go type over its use-expr's
                // solved type. When the source list is a function PARAMETER
                // (`panics lines = List.map … lines`), the use-expr's solved
                // type erases to `any` on the lowering path — so `expr_ty`
                // would miss the element and leave the closure param an
                // un-pinned subset struct (the record-update-over-param panic,
                // DarraghStudio bug #2 param variant). The param's `local_ty`
                // carries the annotated `[]Rec`, which pins the element
                // correctly. A CAF source already reports a concrete `Slice`
                // via `expr_ty`, so this only ADDS coverage for the param case.
                let src_ty = match &self.body.exprs[*last] {
                    Expr::Var(Res::Local(id)) => self
                        .local_tys
                        .get(id)
                        .cloned()
                        .filter(|t| matches!(t, GoTy::Slice(_)))
                        .unwrap_or_else(|| self.expr_ty(*last)),
                    _ => self.expr_ty(*last),
                };
                match src_ty {
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
            if largs.len() > arity {
                return self.over_apply(c, largs, arity, &ret_goty);
            }
        } else if let GoTy::Func(ps, cod) = c.ty.clone() {
            // Under-application of a func VALUE (not a top-level Def, so `go_arity`
            // is None) — e.g. a nested-curried return applied STEPWISE:
            // `((makeAdder 1) 2) 3`, where `makeAdder 1` is a `func(int,int) int`
            // value and `… 2` applies it to ONE arg. Go can't partially apply a
            // fixed-arity func, so wrap the remaining params in a closure (the same
            // shape `make_partial` builds for Defs). Over-application (largs >= arity)
            // and exact calls fall through to the plain call below. (audit #7b
            // stepwise companion to the `lower_lambda` spine-collapse.)
            let arity = ps.len();
            if !largs.is_empty() && largs.len() < arity {
                let cod = *cod;
                return self.make_partial(c, largs, &Some(ps), &cod, arity);
            }
        }
        GoExpr::new(GoExprKind::Call(Box::new(c), largs), ret_goty)
    }

    /// Over-application (audit #7): a def of arity N called with M > N args.
    /// Sky curries and the body returns a function, but Go's direct call is
    /// fixed-arity — `mul3 : Int -> Int -> (Int -> Int); mul3 a b = \c -> …`
    /// called `mul3 2 3 5` would emit `Main_mul3(2, 3, 5)`, which Go rejects
    /// ("too many arguments in call to Main_mul3", a 2-param func). Emit the
    /// N-ary direct call, then apply each remaining arg to the returned closure
    /// in turn (`Main_mul3(2, 3)(5)`), coercing each extra arg to the closure's
    /// Go param type and threading the codomain as the running result type.
    fn over_apply(
        &mut self,
        callee: GoExpr,
        largs: Vec<GoExpr>,
        arity: usize,
        ret: &GoTy,
    ) -> GoExpr {
        let mut it = largs.into_iter();
        let base: Vec<GoExpr> = it.by_ref().take(arity).collect();
        // For an arity-0 function-valued def (`greet = String.append "hi "`),
        // `callee` is ALREADY the forced CAF (`Main_greet()`) that RETURNS the
        // function — wrapping it in an empty `Call` would double-force it to
        // `Main_greet()()`, which `go build` rejects. Start from `callee`
        // directly and apply the args to it.
        let mut call = if arity == 0 {
            callee
        } else {
            GoExpr::new(GoExprKind::Call(Box::new(callee), base), ret.clone())
        };
        let mut cur = ret.clone();
        // Apply the remaining args by BATCHING each round to the running func's
        // arity — a curried return whose lambda body was spine-collapsed to a flat
        // `func(int,int) int` (see `lower_lambda`, audit #7b) must be called
        // `f(a,b)(c,d)`, not one-at-a-time `f(a,b)(c)(d)` which under-applies. A
        // single-codomain func (`func(int) int`, the mul3 case) has arity 1 → one
        // arg per round → identical to the prior behaviour.
        let rest: Vec<GoExpr> = it.collect();
        let mut idx = 0;
        while idx < rest.len() {
            match cur.clone() {
                GoTy::Func(ps, cod) => {
                    let cod = *cod;
                    let n = ps.len().max(1);
                    let end = (idx + n).min(rest.len());
                    let batch: Vec<GoExpr> = rest[idx..end]
                        .iter()
                        .enumerate()
                        .map(|(k, e)| {
                            self.coerce_if_needed(e.clone(), ps.get(k).unwrap_or(&GoTy::Any))
                        })
                        .collect();
                    call = GoExpr::new(GoExprKind::Call(Box::new(call), batch), cod.clone());
                    cur = cod;
                    idx += n;
                }
                _ => {
                    let ea = self.coerce_if_needed(rest[idx].clone(), &GoTy::Any);
                    call = GoExpr::new(GoExprKind::Call(Box::new(call), vec![ea]), GoTy::Any);
                    cur = GoTy::Any;
                    idx += 1;
                }
            }
        }
        call
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
            rest_params.push(GoParam {
                name: pname.clone(),
                ty: GoTy::Any,
            });
            let arg_ref = GoExpr::new(GoExprKind::Ident(pname), GoTy::Any);
            call_args.push(self.coerce_if_needed(arg_ref, &pty));
        }
        let body_call = GoExpr::new(GoExprKind::Call(Box::new(callee), call_args), ret.clone());
        let fn_ty = GoTy::Func(vec![GoTy::Any; n_rest], Box::new(ret.clone()));
        GoExpr::new(
            GoExprKind::FuncLit(
                rest_params,
                ret.clone(),
                vec![GoStmt::Return(Some(body_call))],
            ),
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
        // Thread the function value's declared PARAM type as the piped value's
        // expected type, so an erased `[]any`/`any` from an upstream polymorphic
        // pipeline stage narrows to the consumer's concrete param — e.g. a lambda
        // `\rest -> …` typed `func([]Step) …` at the end of `… |> dropWhile p |>
        // (\rest -> …)` (issue #163). Without it the value lowered with expected
        // `Any`, so `coerce_if_needed` skipped the boundary and go build hit
        // `[]any` vs `[]Step`. The return slot is likewise the function's own
        // result type. `coerce_if_needed` elides when the types already match, so
        // a concrete-into-concrete pipe stays byte-identical.
        let (param_ty, ret_ty) = match &f.ty {
            GoTy::Func(ps, r) => (
                ps.first().cloned().unwrap_or_else(|| GoTy::Any),
                (**r).clone(),
            ),
            _ => (GoTy::Any, actual.clone()),
        };
        let a = self.lower_expr(value_e, &param_ty);
        let call = GoExpr::new(GoExprKind::Call(Box::new(f), vec![a]), ret_ty);
        self.coerce_if_needed(call, actual)
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
            "int64" | "int32" | "int16" | "int8" | "uint" | "uint64" | "uint32" | "uint16"
            | "uint8" | "byte" | "rune" | "uintptr" => {
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
                largs.push(GoExpr::new(
                    GoExprKind::Ident("struct{}{}".into()),
                    GoTy::Any,
                ));
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

    /// The runtime parameter count of kernel symbol `go` (`rt.String_append`), or
    /// `None` when it isn't a known runtime symbol (defensive — then no
    /// eta-expansion, so a full application still lowers correctly). This is the
    /// authoritative kernel arity: `rt.Middleware_withCors(origins, handler)` is 2,
    /// even though `withCors : List String -> Handler -> Handler` has 3 curried
    /// arrows once `Handler = Request -> Task` unfolds.
    fn kernel_runtime_arity(&self, go: &str) -> Option<usize> {
        let sym = go.strip_prefix("rt.").unwrap_or(go);
        self.kernel_arity.get(sym).copied()
    }

    /// Eta-expand a partially-applied kernel into a Go closure. A kernel runtime
    /// symbol (`rt.String_append(a, b any) any`) has no currying, so a direct
    /// under-applied call (`String.append "hi "` — 1 arg to a 2-arg kernel) emits
    /// `rt.String_append("hi ")`, which `go build` rejects ("not enough
    /// arguments"). Instead emit `func(_p any) any { return rt.String_append("hi ", _p) }`.
    /// Mirrors [`ctor_partial`]; kernel params are `any`, so the closure widens
    /// each fresh param into the call and coerces the `any` result to `ret`.
    /// `arity` is the runtime param count (from [`kernel_runtime_arity`]), so a
    /// FULL application never reaches here — only genuine under-applications.
    fn kernel_partial(
        &mut self,
        go: &str,
        given: &[ExprId],
        arity: usize,
        actual: &GoTy,
    ) -> GoExpr {
        let mut arg_exprs: Vec<GoExpr> = given
            .iter()
            .map(|a| {
                let e = self.lower_expr(*a, &GoTy::Any);
                widen(e)
            })
            .collect();
        let n_rest = arity - given.len();
        let (rest_tys, ret): (Vec<GoTy>, GoTy) = match actual {
            GoTy::Func(ps, r) if ps.len() == n_rest => (ps.clone(), (**r).clone()),
            _ => (vec![GoTy::Any; n_rest], GoTy::Any),
        };
        let mut gparams: Vec<GoParam> = Vec::new();
        for pty in &rest_tys {
            let pname = format!("_p{}", self.local_counter);
            self.local_counter += 1;
            gparams.push(GoParam {
                name: pname.clone(),
                ty: pty.clone(),
            });
            // The kernel symbol takes `any` params — widen the typed closure param.
            arg_exprs.push(widen(GoExpr::new(GoExprKind::Ident(pname), pty.clone())));
        }
        let call = GoExpr::new(
            GoExprKind::Call(
                Box::new(GoExpr::new(GoExprKind::Ident(go.into()), GoTy::Any)),
                arg_exprs,
            ),
            GoTy::Any,
        );
        let body = self.coerce_if_needed(call, &ret);
        let fn_ty = GoTy::Func(rest_tys, Box::new(ret.clone()));
        GoExpr::new(
            GoExprKind::FuncLit(gparams, ret, vec![GoStmt::Return(Some(body))]),
            fn_ty,
        )
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
            ">>" | "<<" => {
                // Function composition. `f >> g` = ComposeL(f, g) (apply f then
                // g); `f << g` = ComposeR(f, g) (apply g then f) — matching the
                // oracle (`rt.ComposeL(inc, dbl)` / `rt.ComposeR(inc, dbl)`). The
                // generic `rt.ComposeL[A,B,C]`/`ComposeR` infer their type params
                // from the `func` arguments, so the operands must be lowered at
                // their OWN function types, NOT widened to `any` (Go cannot infer
                // `func(A) B` from an `any` value). The result is the composed
                // function `func(A) C`, so type the node `actual`.
                let lty = self.expr_ty(lhs);
                let rty = self.expr_ty(rhs);
                let l = self.lower_expr(lhs, &lty);
                let r = self.lower_expr(rhs, &rty);
                let helper = if op == ">>" {
                    "rt.ComposeL"
                } else {
                    "rt.ComposeR"
                };
                call_rt(helper, vec![l, r], actual.clone())
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
                        let ty = if is_cmp(op) {
                            GoTy::Bare(Prim::Bool)
                        } else {
                            l.ty.clone()
                        };
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

    // ---- tail-call optimisation (Limitation #8) ---------------------------

    /// True iff `root` references `def` (via a `Res::Def` call) at least once,
    /// and EVERY such reference is a saturated call in tail position. Mirrors
    /// the oracle `Sky.Build.TailCallOpt.isTailRecursive`:
    /// `countTailSelfCalls > 0 && countNonTailSelfCalls == 0`.
    fn is_tail_recursive(&self, root: ExprId, def: DefId, arity: usize) -> bool {
        let tail = self.count_tail_self(root, def, arity, true);
        let non_tail = self.count_nontail_self(root, def, arity, true);
        tail > 0 && non_tail == 0
    }

    /// `callee` resolves to the top-level def `def` (a self-reference).
    fn is_self_call(&self, callee: ExprId, def: DefId) -> bool {
        matches!(&self.body.exprs[callee], Expr::Var(Res::Def(d)) if *d == def)
    }

    /// Any reference to top-level `def` ANYWHERE in the subtree rooted at `e`
    /// (not just tail / call position). Used to gate CAF memoisation: a
    /// self-referential zero-arg binding (`conn = case … Err _ -> conn`) must
    /// NOT be memoised, because its compute closure would re-enter the cell's
    /// `sync.Once.Do` and deadlock — it stays a plain re-evaluating function
    /// (which is also the correct behaviour for such a retry loop). Walks via
    /// `expr_children`, the canonical complete child enumerator, so a new AST
    /// node that adds a child there is covered here too.
    fn body_references_def(&self, e: ExprId, def: DefId) -> bool {
        if matches!(&self.body.exprs[e], Expr::Var(Res::Def(d)) if *d == def) {
            return true;
        }
        self.expr_children(e)
            .into_iter()
            .any(|c| self.body_references_def(c, def))
    }

    /// True when the subtree rooted at `e` contains a record UPDATE whose base
    /// is the local `pid` (`{ pid | f = v }`). Used to widen a subset-struct
    /// param to `any` so its update goes reflective — an anonymous struct can't
    /// carry a full-record update without dropping fields. Does NOT descend into
    /// nested lambdas (a different param binds `pid` there).
    fn param_is_updated(&self, e: ExprId, pid: LocalId) -> bool {
        if let Expr::Update { base, .. } = &self.body.exprs[e] {
            if matches!(&self.body.exprs[*base], Expr::Var(Res::Local(l)) if *l == pid) {
                return true;
            }
        }
        if matches!(&self.body.exprs[e], Expr::Lambda { .. }) {
            return false;
        }
        self.expr_children(e)
            .into_iter()
            .any(|c| self.param_is_updated(c, pid))
    }

    /// The first "fresh-value" kernel (`Uuid.v4` / entropy `Random.*` /
    /// `Time.now` / `Crypto.random*`) reached in the subtree rooted at `e`,
    /// WITHOUT descending into nested lambda bodies (a fresh-value call inside
    /// a lambda runs per-invocation, not at memoisation time). Used to warn
    /// when a memoised top-level CAF forces one of these to a single frozen
    /// value — the footgun that silently gave colliding UUIDs / a frozen clock
    /// after CAF memoisation landed.
    fn find_fresh_value_kernel(&self, e: ExprId) -> Option<(String, String)> {
        match &self.body.exprs[e] {
            // Direct kernel reference (rare at user sites).
            Expr::Var(Res::Kernel { module, func })
                if is_fresh_value_kernel(module.as_str(), func.as_str()) =>
            {
                return Some((module.as_str().to_string(), func.as_str().to_string()));
            }
            // The usual case: `Uuid.v4` is a `Res::Def` to the stdlib alias
            // whose body is `Ffi.kernel "Uuid_v4"` — resolve it to the symbol.
            Expr::Var(Res::Def(d)) => {
                if let Some(sym) = self.kernel_alias.get(d) {
                    if let Some((m, f)) = fresh_value_symbol_parts(sym) {
                        return Some((m, f));
                    }
                }
            }
            _ => {}
        }
        // A fresh-value call inside a lambda fires per-call; skip that subtree.
        if matches!(&self.body.exprs[e], Expr::Lambda { .. }) {
            return None;
        }
        for c in self.expr_children(e) {
            if let Some(hit) = self.find_fresh_value_kernel(c) {
                return Some(hit);
            }
        }
        None
    }

    /// Every child expression, for a NON-tail (`inTail = false`) walk. Includes
    /// let-def RHS bodies (a self-call there is non-tail) and `if` conditions.
    /// New AST variants MUST get an arm here — a missing child → a missed
    /// self-call → a misclassified (and thus miscompiled) TCO candidate.
    /// Field names read on the local `param` (`param.field`) anywhere in the
    /// subtree rooted at `e`. Used by `lower_lambda`'s SOUND WIDENING: when a
    /// closure param's Go type is a lossy SUBSET struct (an open record row whose
    /// unresolved tail dropped a field), and the body reads a field the struct
    /// omits, the param is widened to `any` so the access routes through
    /// `rt.Field` instead of emitting `v.Field` on a struct that lacks it.
    fn fields_read_on_local(&self, e: ExprId, param: LocalId, out: &mut HashSet<String>) {
        if let Expr::Access(base, field) = &self.body.exprs[e] {
            if let Expr::Var(Res::Local(l)) = &self.body.exprs[*base] {
                if *l == param {
                    out.insert(field.as_str().to_string());
                }
            }
        }
        for c in self.expr_children(e) {
            self.fields_read_on_local(c, param, out);
        }
    }

    fn expr_children(&self, e: ExprId) -> Vec<ExprId> {
        match &self.body.exprs[e] {
            Expr::Int(_)
            | Expr::Float(_)
            | Expr::Str(_)
            | Expr::Chr(_)
            | Expr::Bool(_)
            | Expr::Unit
            | Expr::Var(_)
            | Expr::Accessor(_)
            | Expr::Error => vec![],
            Expr::List(es) | Expr::Tuple(es) => es.clone(),
            Expr::Record(fs) => fs.iter().map(|(_, v)| *v).collect(),
            Expr::Update { base, fields } => {
                let mut v = vec![*base];
                v.extend(fields.iter().map(|(_, x)| *x));
                v
            }
            Expr::Negate(x) => vec![*x],
            Expr::Access(x, _) => vec![*x],
            Expr::Lambda { body, .. } => vec![*body],
            Expr::Call(c, args) => {
                let mut v = vec![*c];
                v.extend(args.iter().copied());
                v
            }
            Expr::Binop { lhs, rhs, .. } => vec![*lhs, *rhs],
            Expr::If { arms, els } => {
                let mut v = Vec::new();
                for (c, b) in arms {
                    v.push(*c);
                    v.push(*b);
                }
                v.push(*els);
                v
            }
            Expr::Let { defs, body } => {
                let mut v: Vec<ExprId> = defs.iter().map(|d| d.body).collect();
                v.push(*body);
                v
            }
            Expr::Case { subject, branches } => {
                let mut v = vec![*subject];
                v.extend(branches.iter().map(|b| b.body));
                v
            }
        }
    }

    /// Count self-references in TAIL position. Tail-position propagators
    /// (`if`/`case`/`let` bodies) recurse in tail; every other node breaks tail
    /// context and its children are walked NON-tail (arg positions never count).
    fn count_tail_self(&self, e: ExprId, def: DefId, arity: usize, in_tail: bool) -> usize {
        match &self.body.exprs[e] {
            Expr::If { arms, els } if in_tail => {
                arms.iter()
                    .map(|(_, b)| self.count_tail_self(*b, def, arity, true))
                    .sum::<usize>()
                    + self.count_tail_self(*els, def, arity, true)
            }
            Expr::Let { body, .. } if in_tail => self.count_tail_self(*body, def, arity, true),
            Expr::Case { branches, .. } if in_tail => branches
                .iter()
                .map(|b| self.count_tail_self(b.body, def, arity, true))
                .sum(),
            Expr::Call(callee, args)
                if in_tail && self.is_self_call(*callee, def) && args.len() == arity =>
            {
                1 + args
                    .iter()
                    .map(|a| self.count_tail_self(*a, def, arity, false))
                    .sum::<usize>()
            }
            _ => self
                .expr_children(e)
                .iter()
                .map(|c| self.count_tail_self(*c, def, arity, false))
                .sum(),
        }
    }

    /// Count self-references in NON-tail position. Any self-call that is not a
    /// saturated tail call (wrong arity, or outside tail position) counts —
    /// a non-zero result disqualifies TCO.
    fn count_nontail_self(&self, e: ExprId, def: DefId, arity: usize, in_tail: bool) -> usize {
        match &self.body.exprs[e] {
            Expr::If { arms, els } if in_tail => {
                arms.iter()
                    .map(|(_, b)| self.count_nontail_self(*b, def, arity, true))
                    .sum::<usize>()
                    + self.count_nontail_self(*els, def, arity, true)
            }
            Expr::Let { body, .. } if in_tail => self.count_nontail_self(*body, def, arity, true),
            Expr::Case { branches, .. } if in_tail => branches
                .iter()
                .map(|b| self.count_nontail_self(b.body, def, arity, true))
                .sum(),
            // Saturated tail call: don't count, but walk its args non-tail.
            Expr::Call(callee, args)
                if in_tail && self.is_self_call(*callee, def) && args.len() == arity =>
            {
                args.iter()
                    .map(|a| self.count_nontail_self(*a, def, arity, false))
                    .sum()
            }
            // Any other self-call (non-tail position, or wrong arity): count it.
            Expr::Call(callee, args) if self.is_self_call(*callee, def) => {
                1 + args
                    .iter()
                    .map(|a| self.count_nontail_self(*a, def, arity, false))
                    .sum::<usize>()
            }
            _ => self
                .expr_children(e)
                .iter()
                .map(|c| self.count_nontail_self(*c, def, arity, false))
                .sum(),
        }
    }

    /// Lower `e` at a TAIL position of a TCO'd def, pushing statements into
    /// `out` (a `for {}` loop block). Control-flow propagators recurse in
    /// statement form (so `continue` stays inside the loop, never inside a
    /// scoping IIFE); a saturated self-call becomes a `continue` jump; every
    /// other leaf is a `return`.
    fn lower_tail_stmts(&mut self, e: ExprId, ret: &GoTy, out: &mut Vec<GoStmt>) {
        match self.body.exprs[e].clone() {
            Expr::If { arms, els } => self.build_tail_if_chain(&arms, els, ret, out),
            Expr::Let { defs, body } => {
                // Pre-register binders (forward refs). Def RHSs are lowered
                // normally — a self-call in a let-RHS stays a recursive call
                // (correct; only tail-position calls become jumps).
                for d in &defs {
                    for (bn, lid) in &d.binders {
                        let _ = self.fresh_local_named(*lid, Some(bn.as_str()));
                    }
                }
                for d in &defs {
                    self.lower_let_def(d, out);
                }
                self.lower_tail_stmts(body, ret, out);
            }
            Expr::Case {
                subject, branches, ..
            } => {
                self.emit_case(subject, &branches, ret, true, out);
            }
            _ => {
                if let Some(js) = self.try_tail_jump(e) {
                    out.extend(js);
                } else {
                    let g = self.lower_expr(e, ret);
                    out.push(GoStmt::Return(Some(g)));
                }
            }
        }
    }

    /// The tail-position `if`-chain of a TCO'd def: like `build_if_chain`, but
    /// each `then`/`else` branch is walked by `lower_tail_stmts` instead of
    /// terminating in a plain `return`.
    fn build_tail_if_chain(
        &mut self,
        arms: &[(ExprId, ExprId)],
        els: ExprId,
        ret: &GoTy,
        out: &mut Vec<GoStmt>,
    ) {
        if let Some(((cond, then), rest)) = arms.split_first() {
            let c = self.lower_expr(*cond, &GoTy::Bare(Prim::Bool));
            let mut then_stmts: Vec<GoStmt> = Vec::new();
            self.lower_tail_stmts(*then, ret, &mut then_stmts);
            let mut els_stmts: Vec<GoStmt> = Vec::new();
            self.build_tail_if_chain(rest, els, ret, &mut els_stmts);
            out.push(GoStmt::If(c, then_stmts, els_stmts));
        } else {
            self.lower_tail_stmts(els, ret, out);
        }
    }

    /// If `e` is a saturated self-call under the active TCO context, emit the
    /// tail jump: compute each new param value into a temporary (clobber-safe —
    /// a later arg may read an earlier param), assign every param from its
    /// temporary, then `continue`. Args are coerced to the param's Go type via
    /// the same `lower_expr(expected)` path `lower_call` uses. Returns `None`
    /// when `e` is not a tail jump (the caller then emits a normal `return`).
    fn try_tail_jump(&mut self, e: ExprId) -> Option<Vec<GoStmt>> {
        let tco = self.tco.clone()?;
        let (callee, args) = match &self.body.exprs[e] {
            Expr::Call(c, a) => (*c, a.clone()),
            _ => return None,
        };
        if !self.is_self_call(callee, tco.def) || args.len() != tco.arity {
            return None;
        }
        let mut stmts: Vec<GoStmt> = Vec::new();
        let mut assigns: Vec<GoStmt> = Vec::new();
        for (i, a) in args.iter().enumerate() {
            let (pname, pty) = tco.params[i].clone();
            if pname == "_" {
                // Unused param — its arg is dead and Go forbids assigning `_`.
                continue;
            }
            let val = self.lower_expr(*a, &pty);
            let tmp = self.fresh_temp();
            stmts.push(GoStmt::Short(tmp.clone(), val));
            assigns.push(GoStmt::Assign(
                pname,
                GoExpr::new(GoExprKind::Ident(tmp), pty),
            ));
        }
        stmts.extend(assigns);
        stmts.push(GoStmt::Continue);
        Some(stmts)
    }

    /// Collect every `Res::Local` referenced anywhere in expression `e`. Used to
    /// dependency-order `let` bindings: Sky allows an out-of-source-order forward
    /// reference (`let a = b + 1; b = 5 in a`), but Go requires declare-before-use,
    /// so the emitter must place `b` ahead of `a`. A missed variant only drops a
    /// dependency edge (degrading to source order — the prior behaviour), never a
    /// crash, but every `Expr` variant is covered here.
    fn collect_local_refs(&self, e: ExprId, out: &mut HashSet<LocalId>) {
        match &self.body.exprs[e] {
            Expr::Var(Res::Local(l)) => {
                out.insert(*l);
            }
            Expr::Var(_)
            | Expr::Int(_)
            | Expr::Float(_)
            | Expr::Str(_)
            | Expr::Chr(_)
            | Expr::Bool(_)
            | Expr::Unit
            | Expr::Accessor(_)
            | Expr::Error => {}
            Expr::List(es) | Expr::Tuple(es) => {
                for &x in es {
                    self.collect_local_refs(x, out);
                }
            }
            Expr::Record(fs) => {
                for (_, x) in fs {
                    self.collect_local_refs(*x, out);
                }
            }
            Expr::Update { base, fields } => {
                self.collect_local_refs(*base, out);
                for (_, x) in fields {
                    self.collect_local_refs(*x, out);
                }
            }
            Expr::Negate(x) | Expr::Access(x, _) => self.collect_local_refs(*x, out),
            Expr::Lambda { body, .. } => self.collect_local_refs(*body, out),
            Expr::Call(f, args) => {
                self.collect_local_refs(*f, out);
                for &x in args {
                    self.collect_local_refs(x, out);
                }
            }
            Expr::Binop { lhs, rhs, .. } => {
                self.collect_local_refs(*lhs, out);
                self.collect_local_refs(*rhs, out);
            }
            Expr::If { arms, els } => {
                for (c, t) in arms {
                    self.collect_local_refs(*c, out);
                    self.collect_local_refs(*t, out);
                }
                self.collect_local_refs(*els, out);
            }
            Expr::Let { defs, body } => {
                for d in defs {
                    self.collect_local_refs(d.body, out);
                }
                self.collect_local_refs(*body, out);
            }
            Expr::Case { subject, branches } => {
                self.collect_local_refs(*subject, out);
                for b in branches {
                    self.collect_local_refs(b.body, out);
                }
            }
        }
    }

    /// Return the emission order of a `let` group's defs so each def is emitted
    /// AFTER every sibling whose binder it references (Kahn topo-sort). A cycle
    /// (mutual value recursion — ill-typed; or mutually-recursive local functions,
    /// whose names are pre-registered so closures still resolve) leaves the
    /// remaining defs in source order.
    fn order_let_defs(&self, defs: &[hir::LocalDef]) -> Vec<usize> {
        let n = defs.len();
        if n <= 1 {
            return (0..n).collect();
        }
        // binder LocalId -> the def index that introduces it.
        let mut owner: HashMap<LocalId, usize> = HashMap::new();
        for (i, d) in defs.iter().enumerate() {
            for (_, lid) in &d.binders {
                owner.insert(*lid, i);
            }
        }
        // deps[i] = sibling def indices that def i references (must precede it).
        let mut deps: Vec<HashSet<usize>> = vec![HashSet::new(); n];
        for (i, d) in defs.iter().enumerate() {
            let mut refs = HashSet::new();
            self.collect_local_refs(d.body, &mut refs);
            for r in refs {
                if let Some(&j) = owner.get(&r) {
                    if j != i {
                        deps[i].insert(j);
                    }
                }
            }
        }
        // Kahn, stable: each pass emits every not-yet-emitted def whose deps are
        // all satisfied, scanning in source order so independent defs keep their
        // original order.
        let mut emitted = vec![false; n];
        let mut order = Vec::with_capacity(n);
        loop {
            let mut progressed = false;
            for i in 0..n {
                if !emitted[i] && deps[i].iter().all(|&j| emitted[j]) {
                    emitted[i] = true;
                    order.push(i);
                    progressed = true;
                }
            }
            if !progressed {
                break;
            }
        }
        // Any defs left in a cycle: append in source order (prior behaviour).
        for i in 0..n {
            if !emitted[i] {
                order.push(i);
            }
        }
        order
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
        // Dependency-order the defs so a forward reference emits its target first
        // (Go requires declare-before-use; names are pre-registered above so a
        // genuine cycle still resolves by name).
        for &i in &self.order_let_defs(&defs) {
            self.lower_let_def(&defs[i], &mut stmts);
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
        // A GENERIC nominal record (`Cfg_R[Msg]`) whose fields include a
        // FUNCTION type is a TEA cfg destined for kernel reflect-dispatch
        // (`Webview.app { init, update, view, … }`). Its concrete generic func
        // fields (`Init func(struct{}) rt.T2[Model, any]`) don't match the raw
        // Go function VALUES supplied for them (`Main_init_ : func(any) …`), and
        // no coerce bridges a bare function reference (it claims the slot type).
        // The non-generic TEA cfg path already emits these as an all-`any`
        // anonymous struct that the runtime reflect-dispatches — keep that form
        // here too. Tightly scoped to the newly-generic func-field case so every
        // other record (incl. non-generic nominal func-field records) is
        // byte-identical.
        let generic_func_cfg = matches!(actual, GoTy::Named(n, args)
            if !args.is_empty()
                && self
                    .record_fields
                    .get(n)
                    .map(|fs| fs.iter().any(|(_, t)| matches!(t, Ty::Fun(..))))
                    .unwrap_or(false));
        let nominal = matches!(actual, GoTy::Named(_, _)) && !generic_func_cfg;
        if nominal {
            if let GoTy::Named(n, _) = actual {
                self.used_types.insert(n.clone());
            }
        }
        // For a GENERIC record slot (`Cfg_R[Msg]`), map each type-param var to
        // its concrete instantiation so a type-param field (`onPress : msg`)
        // gets its expected Go type (`Msg`) — NOT the erased `any` — matching
        // the struct's concrete field type `T1 = Msg` (else `go build` rejects
        // an `any` value assigned into a `T1` field).
        let generic_subst: HashMap<Name, GoTy> = match actual {
            GoTy::Named(n, args) if nominal && !args.is_empty() => self
                .env
                .record_params
                .get(n)
                .map(|params| {
                    params
                        .iter()
                        .cloned()
                        .zip(args.iter().cloned())
                        .collect::<HashMap<Name, GoTy>>()
                })
                .unwrap_or_default(),
            _ => HashMap::new(),
        };
        // declared field types for this record `_R`, if known.
        let field_tys: HashMap<String, Ty> = match actual {
            GoTy::Named(n, _) if nominal => self
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
        // Also covers the MIXED case: a record with some CONCRETE and some `any`
        // fields (a polymorphic record-returning helper, `wrap v = { val = v, tag =
        // "w" }` → `{ val : a, tag : String }` → `struct{ Tag string; Val any }`).
        // The declared return renders the concrete fields concretely, so the
        // literal must too — rendering it all-`any` (`struct{ Tag any; Val any }`)
        // mismatches the declared `Tag string` and `go build` rejects the return.
        // Condition: NO function field (those TEA-cfg records reflect-dispatch and
        // stay all-`any`) AND at least one non-`any` field (an all-`any` record
        // keeps the sorted all-`any` fallback below — byte-identical). `any`-typed
        // fields render per-field as `any`, matching the slot.
        let concrete_struct: Option<Vec<(String, GoTy)>> = match actual {
            GoTy::Struct(fts)
                if !fts.is_empty()
                    && fts.iter().all(|(_, t)| !matches!(t, GoTy::Func(_, _)))
                    && fts.iter().any(|(_, t)| !matches!(t, GoTy::Any)) =>
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
                } else if !generic_subst.is_empty() {
                    // generic slot: resolve type-param fields to their concrete
                    // instantiation (shares `sky_ty_to_go_params` with the decl).
                    field_tys
                        .get(&cap)
                        .map(|t| {
                            sky_ty_to_go_params(t, self.env, Some(&self.cur_module), &generic_subst)
                        })
                        .unwrap_or(GoTy::Any)
                } else {
                    field_tys
                        .get(&cap)
                        .map(|t| self.goty(t))
                        .unwrap_or(GoTy::Any)
                };
                let lowered = self.lower_expr(*v, &expected);
                (cap, lowered)
            })
            .collect();
        let mut all_any_fallback = false;
        let go_name = if nominal {
            match actual {
                // A generic instantiation (`Cfg_R[Msg]`) must render its type
                // args in the composite literal head, not the bare `Cfg_R`.
                GoTy::Named(_, args) if !args.is_empty() => render_goty(actual),
                GoTy::Named(n, _) => n.clone(),
                // Invariant: `nominal` is set iff `actual` is a `GoTy::Named`
                // (established where `nominal`/`actual` are computed above); a
                // non-Named here means that invariant was broken upstream.
                other => base::bug!("nominal record literal with non-Named actual GoTy: {other:?}"),
            }
        } else if concrete_struct.is_some() {
            // Render the concrete struct type so the literal's Go type equals the
            // claimed `actual` (keyed-literal order is field-name independent).
            render_goty(actual)
        } else {
            // Anonymous struct type with every field `any`, field names sorted
            // (L4 deterministic emission). Keyed composite-literal order is
            // independent of the type decl's field order, so sorting is safe.
            all_any_fallback = true;
            let mut names: Vec<String> = fs.iter().map(|(c, _)| c.clone()).collect();
            names.sort();
            let decls = names
                .iter()
                .map(|c| format!("{c} any"))
                .collect::<Vec<_>>()
                .join("; ");
            format!("struct{{ {decls} }}")
        };
        // Honest type of the emitted literal — the type its RENDERED Go form
        // genuinely has, so the OUTER `lower_expr` `coerce_if_needed(node,
        // expected)` (the ONLY caller of `lower_expr_inner`, line 1613) can bridge
        // it to the destination slot correctly.
        //
        // For the nominal (`_R`) / `concrete_struct` branches the rendered literal
        // IS `actual` (fields rendered concretely / keyed against the `_R` decl),
        // so `actual.clone()` is honest.
        //
        // For the all-`any` fallback the literal is `struct{…any…}`. Claiming the
        // CONCRETE `actual` here (this happens when `actual` is a `GoTy::Struct`
        // with a func field — the `concrete_struct` guard rejects func fields
        // because Sky function values are runtime `func(any)any` and can't be
        // assigned to a concretely-typed Go func field) was the check≢build hole:
        // the outer `coerce_if_needed` saw `ty == expected` and inserted nothing,
        // so the raw all-`any` literal reached a concrete param and `go build`
        // rejected it (`struct{Flag any;…}` vs `struct{Flag bool;…}`). Claiming
        // the HONEST all-`any` struct type instead lets the outer machinery decide:
        //   * destination is a CONCRETE struct (`apply : {…} -> …` param) → the
        //     outer coerce narrows via `rt.Coerce[<concrete struct>](struct{…any…}{…})`
        //     — the runtime narrows field-by-field, incl. func fields (the oracle's
        //     `apply(rt.Coerce[Anon_R_…](struct{…any…}{…}))`).
        //   * destination is `any` (the TEA cfg record passed to
        //     `Live_app`/`Tui_app`/`Webview_app` — `kernel_call` lowers the arg with
        //     `expected == Any` then `widen`s) → the outer coerce no-ops
        //     (`expected == Any`) and `widen` wraps `any(struct{…any…}{…})`. Since
        //     `widen` wraps whenever `ty != Any` (true for BOTH the old concrete
        //     claim and this all-`any` claim), the emitted bytes are IDENTICAL to
        //     before this fix — the TEA cfg path is preserved byte-for-byte.
        // NOTE: we DON'T call `coerce_if_needed` here — doing so against the
        // record's OWN `actual` would force a coerce even when the true destination
        // is `any` (over-coercing every TEA cfg). The outer `coerce_if_needed`
        // knows the real destination slot; defer to it.
        let honest_ty = if all_any_fallback {
            match actual {
                GoTy::Struct(fts) if fts.iter().any(|(_, t)| !matches!(t, GoTy::Any)) => {
                    let mut names: Vec<String> = fs.iter().map(|(c, _)| c.clone()).collect();
                    names.sort();
                    GoTy::Struct(
                        names
                            .into_iter()
                            .map(|n| (Name::new(&n), GoTy::Any))
                            .collect(),
                    )
                }
                // A generic func-cfg (`AppCfg_R[Model, Msg]`) emitted as the
                // all-`any` anonymous struct: claim that anon struct (NOT the
                // generic nominal) so a concrete destination slot narrows via
                // `rt.Coerce` instead of asserting the raw anon struct IS the
                // generic nominal (the check≢build hole). A destination `any`
                // slot (the kernel TEA-cfg arg) widens either way — byte-
                // identical there.
                GoTy::Named(_, args) if !args.is_empty() && generic_func_cfg => {
                    let mut names: Vec<String> = fs.iter().map(|(c, _)| c.clone()).collect();
                    names.sort();
                    GoTy::Struct(
                        names
                            .into_iter()
                            .map(|n| (Name::new(&n), GoTy::Any))
                            .collect(),
                    )
                }
                _ => actual.clone(),
            }
        } else {
            actual.clone()
        };
        GoExpr::new(GoExprKind::StructLit(go_name, fs), honest_ty)
    }

    fn lower_update(&mut self, base: ExprId, fields: &[(Name, ExprId)], actual: &GoTy) -> GoExpr {
        // { base | f = v } → func() T { _u := base; _u.F = v; return _u }()
        // A record update yields EXACTLY the base record's Go type (`_u := base`
        // copies it whole), NOT the update expression's body-inferred type
        // (which is a *subset* record when only some fields are read/written).
        // Take the base's own lowered type as the block/`_u` type; the outer
        // `lower_expr` coerces the block to the caller's `expected` if needed.
        let base_sky_ty = self.sky_ty_of(base);
        let b = self.lower_expr(base, &GoTy::Any);
        let uty = b.ty.clone();
        // A ROW-POLYMORPHIC base (`bump r = { r | age = … }`, base lowers to
        // `any`) has no static Go struct to `_u.Field = v` into. Route the whole
        // update through the runtime's reflective `rt.RecordUpdate(base,
        // map[string]any{"Field": v})`, exactly as the oracle does. The updated
        // values lower with `any` as their expected type (no declared field
        // types are known) and widen. `rt.RecordUpdate` copies the base
        // (map- or struct-backed) and overwrites the named fields reflectively.
        if uty == GoTy::Any {
            let pairs: Vec<(String, GoExpr)> = fields
                .iter()
                .map(|(n, v)| {
                    let cap = capitalize(n.as_str());
                    let lowered = widen(self.lower_expr(*v, &GoTy::Any));
                    (format!("\"{cap}\""), lowered)
                })
                .collect();
            let map_lit = GoExpr::new(
                GoExprKind::StructLit("map[string]any".to_string(), pairs),
                GoTy::Any,
            );
            return GoExpr::new(
                GoExprKind::Call(
                    Box::new(GoExpr::new(
                        GoExprKind::Ident("rt.RecordUpdate".into()),
                        GoTy::Any,
                    )),
                    vec![widen(b), map_lit],
                ),
                GoTy::Any,
            );
        }
        // Declared field types of the base record `_R` — so each updated field's
        // value lowers with its Go field type as `expected` (a kernel `any`
        // return like `rt.Basics_not(...)` is then coerced to the field's `bool`,
        // not fed raw into a typed struct field — `go build` rejects the latter).
        let mut field_tys: HashMap<String, Ty> = match &uty {
            GoTy::Named(n, _) => self
                .record_fields
                .get(n)
                .map(|fs| fs.iter().map(|(fn_, t)| (fn_.clone(), t.clone())).collect())
                .unwrap_or_default(),
            _ => HashMap::new(),
        };
        // Fallback: an anonymous record wrapped in a custom type
        // (`type Query a = Query { … }`) is never a top-level Record decl, so it is
        // absent from `record_fields` → its field types are unknown → a typed-slice
        // field would take a raw `any` (from `x :: xs` / `xs ++ ys`) with NO
        // coercion, and `go build` rejects assigning `any` to a `[]T` field (a
        // check≢build hole). Read the declared field types straight from the base's
        // inferred Sky record type so the update coerces each field exactly like a
        // record literal does.
        if field_tys.is_empty() {
            if let Some(Ty::Record(fs, _)) = base_sky_ty {
                field_tys = fs
                    .iter()
                    .map(|(n, t)| (capitalize(n.as_str()), t.clone()))
                    .collect();
            }
        }
        let mut stmts = vec![GoStmt::Short("_u".into(), b)];
        let uref = GoExpr::new(GoExprKind::Ident("_u".into()), uty.clone());
        for (n, v) in fields {
            let cap = capitalize(n.as_str());
            let expected = field_tys
                .get(&cap)
                .map(|t| self.goty(t))
                .unwrap_or(GoTy::Any);
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
        // Runtime has typed tuple structs `rt.T2`..`rt.T9` (fields V0..Vn); arity
        // ≥10 uses the slice-backed `rt.SkyTupleN{ Vs []any }`. (Previously arity
        // ≥4 emitted an undefined `rt.MkTupleN` — a check≢build hole; even the
        // oracle mis-coerced a 4-tuple to `rt.T3` and panicked at runtime, so this
        // is compatible-AND-better.)
        if elems.len() <= 9 {
            // `rt.T{n}` struct fields are V0, V1, … (runtime rt.go
            // `type T2[A, B any] struct { V0 A; V1 B }`).
            let fields: Vec<(String, GoExpr)> = args
                .into_iter()
                .enumerate()
                .map(|(i, a)| (format!("V{i}"), a))
                .collect();
            let tname = GoTy::Tuple(tys);
            return GoExpr::new(
                GoExprKind::StructLit(
                    format!("rt.T{}{}", elems.len(), tuple_type_args(&tname)),
                    fields,
                ),
                actual.clone(),
            );
        }
        // arity ≥10 → `rt.SkyTupleN{ Vs: []any{…} }` (slice-backed, heterogeneous).
        let vs = GoExpr::new(
            GoExprKind::SliceLit(GoTy::Any, args.into_iter().map(widen).collect()),
            GoTy::Any,
        );
        GoExpr::new(
            GoExprKind::StructLit("rt.SkyTupleN".to_string(), vec![("Vs".to_string(), vs)]),
            actual.clone(),
        )
    }

    fn lower_list(&mut self, elems: &[ExprId], actual: &GoTy) -> GoExpr {
        let elem = actual.elem_ty();
        let args: Vec<GoExpr> = elems.iter().map(|e| self.lower_expr(*e, &elem)).collect();
        GoExpr::new(GoExprKind::SliceLit(elem, args), actual.clone())
    }

    fn lower_lambda(&mut self, params: &[PatId], body: ExprId, actual: &GoTy) -> GoExpr {
        let mut params = params.to_vec();
        let (pt, rt) = match actual {
            GoTy::Func(ps, r) => (ps.clone(), (**r).clone()),
            _ => (vec![GoTy::Any; params.len()], GoTy::Any),
        };
        // `sky_ty_to_go` flattens the ENTIRE curried arrow spine into one N-ary Go
        // func (`Int -> (Int -> Int)` → `func(int,int) int`), but a source
        // `\c -> \d -> body` is a CHAIN of arity-1 lambda NODES. Binding only
        // `params` against the flat `pt` would drop `pt[1..]`, pass the final
        // codomain as the inner body's expected type, and emit a `func(int) int`
        // value under a `func(int,int) int` declaration → `go build` arity error
        // (plus a spurious `rt.AsInt` on the inner closure). Absorb the nested
        // lambda spine so the emitted closure is a single flat `func(c, d) body`,
        // matching the declared type and the flat func-value ABI (`rt.skyCallOne`
        // curries a flat N-ary func, so both flat and curried application sites stay
        // valid). Fires ONLY when the type carries more params than the lambda binds
        // AND the body is a nested lambda — i.e. currently-broken shapes;
        // arity-matched callbacks (`List.map (\x -> …)`) have `pt.len() ==
        // params.len()` so the loop never runs → byte-identical. (audit #7b)
        let mut body = body;
        while params.len() < pt.len() {
            if let Expr::Lambda {
                params: inner,
                body: ib,
            } = &self.body.exprs[body]
            {
                let inner = inner.clone();
                let ib = *ib;
                params.extend(inner);
                body = ib;
            } else {
                break; // residual: lambda bottoms out in a func VALUE — ETA-EXPAND below
            }
        }
        // F5 eta-expansion. The spine loop above absorbs a NESTED-lambda chain so
        // the emitted closure's param count matches the flat `pt`. But a lambda can
        // also bottom out in a func VALUE that supplies the remaining arrows —
        // `makeF a = \_ -> add1` where `add1 : Int -> Int` (body is NOT a lambda
        // node). The loop breaks with `params.len() < pt.len()`; without repair the
        // closure would emit `func(_ any) int { return rt.AsInt(add1) }` (fewer
        // params than the declared `func(any,int) int`, and the func-value body
        // forced through the final codomain `int` via a spurious `rt.AsInt`) — a
        // runtime TypeMismatch panic (or `go build` arity error) the oracle never
        // produces. Eta-expand: synthesise the missing params and APPLY the body
        // (a func value of the remaining arrow type) to them, so the emitted closure
        // is `func(_ any, _eta0 int) int { return add1(_eta0) }` — arity matches the
        // declared flat func type and the func-value body is called, not coerced to a
        // scalar. Empty when the loop already matched arity (nested-lambda chains,
        // `examples/41-nested-curry`) → byte-identical.
        let eta_extra: Vec<GoTy> = if params.len() < pt.len() {
            pt[params.len()..].to_vec()
        } else {
            Vec::new()
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
            // SOUND WIDENING: if the param's Go type is a struct that OMITS a field
            // the body actually reads on this param, widen to `any` (the access then
            // routes through `rt.Field`). This fires when an OPEN record row
            // (`{a, b | ρ}`, ρ unresolved) lowered to a lossy SUBSET struct — the
            // dropped tail held the read field. Example: a `List.find` predicate
            // `\e -> e.primary` over `List {email, verified | ρ}` where ρ should
            // carry `primary`; `goty` emitted `struct{Email; Verified}` and the body
            // reads `.Primary`. Emitting a field read on a struct that lacks the
            // field is a GUARANTEED `go build` failure, so widening here can never
            // regress a program that currently compiles.
            let widen_param = if let (Pattern::Var(pid), GoTy::Struct(sfields)) =
                (&self.body.pats[*p], &ty)
            {
                let pid = *pid;
                let present: HashSet<String> =
                    sfields.iter().map(|(n, _)| n.as_str().to_string()).collect();
                let mut read: HashSet<String> = HashSet::new();
                self.fields_read_on_local(body, pid, &mut read);
                let capf = |s: &str| -> String {
                    let mut ch = s.chars();
                    match ch.next() {
                        Some(c) => c.to_ascii_uppercase().to_string() + ch.as_str(),
                        None => String::new(),
                    }
                };
                // Widen when the body reads a field the subset struct lacks, OR
                // when the param is the base of a record-UPDATE. An anonymous
                // subset `struct{…}` cannot soundly carry `{ r | f = v }`: the
                // update yields the FULL record, but a struct-typed slot drops
                // the un-updated fields (physically), so a later consumer that
                // reads them hits `reflect: struct{F} as struct{G}`. Widening to
                // `any` routes the update through the reflective rt.RecordUpdate
                // path, which preserves every field. The ROOT fix for the
                // record-update-narrowing panic class (DarraghStudio bug #2) —
                // covers map-chains ([]any element), foldl accumulators, and any
                // source whose concrete element type isn't recoverable. A param
                // pinned to a concrete Named record is NOT a `struct{…}` here, so
                // it keeps the fast typed path.
                read.iter().any(|f| !present.contains(&capf(f)))
                    // Update-base widening ONLY when the param was NOT pinned to
                    // the source list's real element (`!elem_pinned`). A pinned
                    // subset struct IS the honest runtime element (a genuinely
                    // `[]struct{…}` list — 18-job-queue), so its update is
                    // already sound and staying typed avoids needless reflective
                    // coercions. An UN-pinned subset struct is the update-row
                    // narrowing artifact over an erased ([]any) element whose
                    // true value is a full record — THAT is the unsound case to
                    // widen (map-chains, caseD).
                    || (!elem_pinned && self.param_is_updated(body, pid))
            } else {
                false
            };
            if widen_param {
                ty = GoTy::Any;
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
        // Record-update-over-param narrowing fix (DarraghStudio bug #2).
        // For `\r -> { r | f = v }` mapped over a typed list, the row-poly
        // solver (on the erased-`List.map` lowering path) leaves the update's
        // row OPEN and it reads back as the SUBSET of updated fields — so the
        // expected return `rt` is an anonymous `struct{ F }` that has PHYSICALLY
        // DROPPED the un-updated fields. A later `List.map` reading a different
        // field then invokes its closure on that subset struct → `reflect:
        // Call using struct{F} as struct{G}`. But the param `r` is pinned to
        // the list's element type (a concrete record), and updating a record
        // yields the SAME record — so the real result type is that full record.
        // Emit it: the lambda is boxed to `any` at the erased call site (no
        // caller signature to violate), and a downstream narrow consumer can
        // narrow FROM the full record, which the subset can't reconstruct.
        let rt = if matches!(rt, GoTy::Struct(_)) {
            match &self.body.exprs[body] {
                Expr::Update { base, .. } => match &self.body.exprs[*base] {
                    // The update yields its base param's type. A concrete Named
                    // record → the full typed record. A param WIDENED to `any`
                    // (subset-struct base — see widen_param) → `any`, so the
                    // reflective rt.RecordUpdate result isn't re-narrowed to the
                    // dropping struct. Either way, never the narrow struct.
                    Expr::Var(Res::Local(pid)) => match self.local_tys.get(pid) {
                        Some(full @ GoTy::Named(_, _)) => full.clone(),
                        Some(GoTy::Any) => GoTy::Any,
                        _ => rt,
                    },
                    _ => rt,
                },
                _ => rt,
            }
        } else {
            rt
        };
        let b = if eta_extra.is_empty() {
            self.lower_expr(body, &rt)
        } else {
            // The body is a func VALUE of the remaining arrow type `func(eta_extra) rt`.
            // Lower it against that concrete func type (so a bare def reference like
            // `add1` emits a callable `func(int) int` rather than being narrowed to a
            // scalar), then apply it to freshly-synthesised extra params. `pt.len() ==
            // gparams already-built params + eta_extra`, so appending concrete-typed
            // extra params keeps the emitted signature equal to the declared `actual`
            // func type.
            let body_fn_ty = GoTy::Func(eta_extra.clone(), Box::new(rt.clone()));
            let bexpr = self.lower_expr(body, &body_fn_ty);
            let bexpr = self.coerce_if_needed(bexpr, &body_fn_ty);
            let mut extra_args = Vec::new();
            for pty in &eta_extra {
                let pname = format!("_eta{}", self.local_counter);
                self.local_counter += 1;
                gparams.push(GoParam {
                    name: pname.clone(),
                    ty: pty.clone(),
                });
                extra_args.push(GoExpr::new(GoExprKind::Ident(pname), pty.clone()));
            }
            GoExpr::new(GoExprKind::Call(Box::new(bexpr), extra_args), rt.clone())
        };
        let mut stmts = destructure;
        stmts.push(GoStmt::Return(Some(b)));
        GoExpr::new(GoExprKind::FuncLit(gparams, rt, stmts), actual.clone())
    }

    fn lower_case(&mut self, subject: ExprId, branches: &[CaseBranch], actual: &GoTy) -> GoExpr {
        let mut stmts: Vec<GoStmt> = Vec::new();
        self.emit_case(subject, branches, actual, false, &mut stmts);
        GoExpr::new(GoExprKind::Block(stmts), actual.clone())
    }

    /// Lower a `case` as statements pushed into `out`. `tail` = the case sits in
    /// the tail position of a TCO'd def: each branch body is walked by
    /// `lower_tail_stmts` (so a tail self-call becomes `continue`, and nested
    /// control-flow stays in statement form), and the subject binder gets a
    /// FRESH name so a sibling/nested case in the same flat loop scope does not
    /// redeclare `_subj`. In non-tail mode this is byte-identical to the prior
    /// `lower_case` (fixed `_subj` binder, branch bodies end in `return`).
    fn emit_case(
        &mut self,
        subject: ExprId,
        branches: &[CaseBranch],
        actual: &GoTy,
        tail: bool,
        out: &mut Vec<GoStmt>,
    ) {
        let branches = branches.to_vec();
        // In tail mode the case is emitted directly into the loop block (not a
        // scoping IIFE), so `_subj` would collide with a sibling / enclosing
        // case. Give each tail case its own subject binder.
        let subj_name = if tail {
            let n = format!("_subj{}", self.local_counter);
            self.local_counter += 1;
            n
        } else {
            "_subj".to_string()
        };
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
        // Emit the branch bodies into a temp buffer first, so we can tell
        // whether they actually reference the subject binder before deciding how
        // to bind it.
        let subj_ref = GoExpr::new(GoExprKind::Ident(subj_name.clone()), subj_ty.clone());
        let mut body: Vec<GoStmt> = Vec::new();
        for br in &branches {
            self.lower_case_branch(&subj_ref, &subj_ty, br, actual, tail, &mut body);
        }
        // fallthrough guard (exhaustiveness should prevent reaching here).
        body.push(GoStmt::Expr(GoExpr::new(
            GoExprKind::Ident("panic(rt.Unreachable(\"case\"))".into()),
            GoTy::Unit,
        )));
        // Bind the subject with `:=` only when an arm actually reads it. A `case`
        // whose arms are ALL wildcards / unit (`case n of _ -> …`) never touches
        // `_subj`, and Go rejects an unused `:=` local — so valid Sky would emit
        // un-buildable Go (a `sky check ≢ go build` hole). Any other pattern —
        // a literal (`_subj == lit`), constructor (`_subj.Tag`), binder
        // (`x := _subj`), or destructure — reads it, so a mixed case still binds
        // as before and the corpus emission is byte-identical. When unread, fall
        // back to `_ = subj`: still evaluates the subject (side effects
        // preserved), declares nothing.
        let subj_read = !branches
            .iter()
            .all(|br| matches!(self.body.pats[br.pat], Pattern::Anything | Pattern::Unit));
        if subj_read {
            out.push(GoStmt::Short(subj_name, subj));
        } else {
            out.push(GoStmt::Discard(subj));
        }
        out.extend(body);
    }

    /// The nominal Go type implied by a case's branch patterns — the owning ADT
    /// / iota type of the first USER constructor pattern found. Builtin
    /// container patterns (Ok/Err/Just/Nothing/True/False) are skipped; they
    /// route through `rt.SkyResult` / `rt.SkyMaybe` / `bool`, not a bare nominal.
    fn pattern_nominal(&self, branches: &[CaseBranch]) -> Option<GoTy> {
        for br in branches {
            if let Pattern::Ctor { ctor, name, .. } = &self.body.pats[br.pat] {
                let cname = name.as_str();
                if matches!(cname, "Ok" | "Err" | "Just" | "Nothing" | "True" | "False") {
                    continue;
                }
                // Prefer the resolved ctor's own union (module-correct) over the
                // last-writer bare-name map — C3 cross-module collision fix.
                let ctor_ty = ctor.as_ref().map(|c| c.type_);
                if let Some((go_type, _kind)) = self.ctor_union_owner(ctor_ty, cname, &GoTy::Any) {
                    return Some(GoTy::Named(go_type, vec![]));
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
        tail: bool,
        out: &mut Vec<GoStmt>,
    ) {
        // Sealed-ADT top-level ctor pattern → idiomatic comma-ok type-switch case:
        //   `if _vN, _okN := _subj.(Union_Ctor_V); _okN { <typed .V{i} binds>; body }`
        // Typed dispatch — the variant struct binds once, field reads are direct
        // typed `_vN.V{i}` (no `rt.Coerce` on the payload).
        if let Pattern::Ctor { ctor, name, args } = &self.body.pats[br.pat] {
            let cname = name.as_str();
            let ctor_ty = ctor.as_ref().map(|c| c.type_);
            if !is_builtin_ctor(cname) {
                if let Some(union) = self.sealed_adt_union(ctor_ty, cname, subj_ty) {
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
                    if tail {
                        self.lower_tail_stmts(br.body, actual, &mut then_inner);
                    } else {
                        let body = self.lower_expr(br.body, actual);
                        then_inner.push(GoStmt::Return(Some(body)));
                    }
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
        if tail {
            self.lower_tail_stmts(br.body, actual, &mut then);
        } else {
            let body = self.lower_expr(br.body, actual);
            then.push(GoStmt::Return(Some(body)));
        }
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
            Pattern::Ctor { ctor, name, args } => self.ctor_pattern(
                subj,
                subj_ty,
                ctor.as_ref().map(|c| c.type_),
                name.as_str(),
                args,
            ),
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
                        Box::new(call_rt(
                            "rt.SkyLen",
                            vec![subj.clone()],
                            GoTy::Bare(Prim::Int),
                        )),
                        Box::new(int_lit(1)),
                    ),
                    GoTy::Bare(Prim::Bool),
                );
                let head_raw = call_rt("rt.SkyElem", vec![subj.clone(), int_lit(0)], GoTy::Any);
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
                        Box::new(call_rt(
                            "rt.SkyLen",
                            vec![subj.clone()],
                            GoTy::Bare(Prim::Int),
                        )),
                        Box::new(int_lit(pats.len() as i64)),
                    ),
                    GoTy::Bare(Prim::Bool),
                ));
                let mut binds = Vec::new();
                for (i, sp) in pats.iter().enumerate() {
                    let raw = call_rt(
                        "rt.SkyElem",
                        vec![subj.clone(), int_lit(i as i64)],
                        GoTy::Any,
                    );
                    let el = self.coerce_if_needed(raw, &elem);
                    let (c, b) = self.pattern_test(&el, &elem, *sp);
                    cond = and_opt(cond, c);
                    binds.extend(b);
                }
                (cond, binds)
            }
            Pattern::Tuple(pats) => {
                let pats = pats.clone();
                let mut cond = None;
                let mut binds = Vec::new();
                // A CONCRETE tuple subject (`rt.T2[float64, int]` / the slice-backed
                // `rt.SkyTupleN`) reads elements by direct field access. An `any`
                // subject — a HOF-erased callback param (foldr's `func(any,any)any`),
                // a let/case-bound erased value, `fst`/`snd` of an erased pair — has
                // NO `.V{i}` field (`_t0.V0 undefined`, #170). Reading it reflectively
                // via `rt.TupleField` (returns `any`, shape-erased across every tuple
                // instantiation) is the robust route the sibling `Cons`/`List` arms
                // and `fst`/`snd` already take; coercing the whole subject to a
                // reconstructed generic instantiation is fragile (Go generics are
                // invariant). Element types then come from each binder's own inferred
                // type, not the erased subject.
                match subj_ty {
                    GoTy::Tuple(ts) => {
                        let elem_tys = ts.clone();
                        let slice_backed = pats.len() >= 10;
                        for (i, sp) in pats.iter().enumerate() {
                            let ety = elem_tys.get(i).cloned().unwrap_or(GoTy::Any);
                            // Arity 2..9 read `.V{i}` on `rt.T{n}`; arity ≥10 read
                            // `.Vs[i]` on the slice-backed `rt.SkyTupleN`. On a TYPED
                            // tuple the field is already concrete, so
                            // `coerce_if_needed(ety, ety)` elides the redundant narrow.
                            let raw = if slice_backed {
                                let vs = GoExpr::new(
                                    GoExprKind::Selector(Box::new(subj.clone()), "Vs".to_string()),
                                    GoTy::Any,
                                );
                                GoExpr::new(
                                    GoExprKind::Index(
                                        Box::new(vs),
                                        Box::new(GoExpr::new(
                                            GoExprKind::IntLit(i as i64),
                                            GoTy::Bare(Prim::Int),
                                        )),
                                    ),
                                    GoTy::Any,
                                )
                            } else {
                                GoExpr::new(
                                    GoExprKind::Selector(
                                        Box::new(subj.clone()),
                                        format!("V{i}"),
                                    ),
                                    ety.clone(),
                                )
                            };
                            let field = self.coerce_if_needed(raw, &ety);
                            let (c, b) = self.pattern_test(&field, &ety, *sp);
                            cond = and_opt(cond, c);
                            binds.extend(b);
                        }
                    }
                    _ => {
                        // `any` subject → reflective per-element read + coerce to the
                        // binder's own inferred Go type (so a bound var carries its
                        // real type for a nested `case` / downstream typed use).
                        for (i, sp) in pats.iter().enumerate() {
                            let ety = self.pattern_binder_goty(*sp).unwrap_or(GoTy::Any);
                            let raw = call_rt(
                                "rt.TupleField",
                                vec![subj.clone(), int_lit(i as i64)],
                                GoTy::Any,
                            );
                            let field = self.coerce_if_needed(raw, &ety);
                            let (c, b) = self.pattern_test(&field, &ety, *sp);
                            cond = and_opt(cond, c);
                            binds.extend(b);
                        }
                    }
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
                                .map(|(cap, t)| {
                                    (
                                        cap.clone(),
                                        sky_ty_to_go_in(t, self.env, Some(&self.cur_module)),
                                    )
                                })
                                .collect()
                        })
                        .unwrap_or_default(),
                    _ => HashMap::new(),
                };
                // An `any` subject (a generic ADT payload extracted as `any`:
                // `Full { x, y }` where `Box a`'s payload erased) has no `.Cap`
                // field — `_v.X undefined`. Read fields reflectively via `rt.Field`
                // (the same route `Expr::Access` takes on an `any` record) and
                // coerce to the binder's own inferred type. Mirrors the reflective
                // Tuple/Cons/List arms. A concrete Named / Struct subject keeps
                // direct field access (fast path, byte-identical).
                let reflective = matches!(subj_ty, GoTy::Any);
                let mut binds = Vec::new();
                for (fname, lid) in &fields {
                    let cap = capitalize(fname.as_str());
                    let fty = if reflective {
                        self.local_ty(*lid)
                    } else {
                        field_tys.get(&cap).cloned().unwrap_or(GoTy::Any)
                    };
                    let name = self.fresh_local_named(*lid, Some(fname.as_str()));
                    self.local_tys.insert(*lid, fty.clone());
                    let read = if reflective {
                        let cap_lit =
                            GoExpr::new(GoExprKind::StrLit(cap), GoTy::Bare(Prim::Str));
                        let raw = call_rt("rt.Field", vec![subj.clone(), cap_lit], GoTy::Any);
                        self.coerce_if_needed(raw, &fty)
                    } else {
                        GoExpr::new(GoExprKind::Selector(Box::new(subj.clone()), cap), fty.clone())
                    };
                    binds.push(GoStmt::Short(name, read));
                }
                (None, binds)
            }
            Pattern::Chr(s) => (
                Some(GoExpr::new(
                    GoExprKind::Binary(GoBin::Eq, Box::new(subj.clone()), Box::new(rune_lit(s))),
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
        ctor_ty: Option<DefId>,
        cname: &str,
        args: &[PatId],
    ) -> (Option<GoExpr>, Vec<GoStmt>) {
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
                let field = if cname == "Ok" {
                    "OkValue"
                } else {
                    "JustValue"
                };
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
                        Box::new(GoExpr::new(
                            GoExprKind::BoolLit(false),
                            GoTy::Bare(Prim::Bool),
                        )),
                    ),
                    GoTy::Bare(Prim::Bool),
                )),
                vec![],
            ),
            _ => {
                // Disambiguate the owning union: prefer the resolved ctor's own
                // union (module-correct — closes the cross-module same-named-ctor
                // collision C3), then the subject's pinned nominal, then the
                // bare-name `ctor_owner` map (which collides for a ctor name
                // shared across two unions — `AlignLeft`, or `Leaf` in two
                // modules' `type Prim`).
                let owner: Option<(String, NominalKind)> =
                    self.ctor_union_owner(ctor_ty, cname, _subj_ty);
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
                        let tag = self.union_ctor_tag(go, cname);
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
                        let (subcond, binds) =
                            self.adt_variant_binds(&struct_val, go, cname, &args);
                        return (and_opt(Some(tag_cond), subcond), binds);
                    }
                }
                // Non-sealed ADT bag: match by declaration-order tag; bind
                // Fields[i]. Prefer the resolved-union tag, then the
                // subject-pinned tag, then the bare-name map.
                let tag = match &owner {
                    Some((go, _)) => self.union_ctor_tag(go, cname),
                    None => match _subj_ty {
                        GoTy::Named(gt, _) => self
                            .ctor_in_union
                            .get(&(gt.clone(), cname.to_string()))
                            .map(|(_, t)| *t)
                            .or_else(|| self.ctor_tag.get(cname).copied())
                            .unwrap_or(0),
                        _ => self.ctor_tag.get(cname).copied().unwrap_or(0),
                    },
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
                        p @ (Pattern::Ctor { .. } | Pattern::Tuple(_) | Pattern::Record(_)) => {
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
    fn sealed_adt_union(
        &self,
        ctor_ty: Option<DefId>,
        cname: &str,
        subj_ty: &GoTy,
    ) -> Option<String> {
        match self.ctor_union_owner(ctor_ty, cname, subj_ty) {
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
            // #172: when the field is `any` (an element-erased ADT payload —
            // e.g. `Loaded a` where `a` erased to `any`) but the sub-pattern is
            // itself a container / ADT / tuple (`Loaded (Just x)`,
            // `Wrap (Ok v)`), narrow the extracted `.V{i}` to the sub-pattern's
            // nominal so the recursive `pattern_test` reads `.Tag` / `.V{j}` off
            // a concrete struct rather than off `any` (which emits
            // `_v.V0.Tag undefined` in Go). Mirrors `bind_field_pat`.
            let sub_ty = if fty == GoTy::Any {
                self.pattern_nominal_ty(&self.body.pats[*a])
                    .unwrap_or(GoTy::Any)
            } else {
                fty.clone()
            };
            let field = if sub_ty == GoTy::Any {
                GoExpr::new(
                    GoExprKind::Selector(Box::new(struct_val.clone()), format!("V{i}")),
                    fty.clone(),
                )
            } else {
                let raw = GoExpr::new(
                    GoExprKind::Selector(Box::new(struct_val.clone()), format!("V{i}")),
                    GoTy::Any,
                );
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
            let (c, b) = self.pattern_test(&field, &sub_ty, *a);
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
            Pattern::Ctor { ctor, name, .. } => match name.as_str() {
                "Ok" | "Err" => Some(GoTy::Named(
                    "rt.SkyResult".into(),
                    vec![GoTy::Any, GoTy::Any],
                )),
                "Just" | "Nothing" => Some(GoTy::Named("rt.SkyMaybe".into(), vec![GoTy::Any])),
                "True" | "False" => Some(GoTy::Bare(Prim::Bool)),
                // Prefer the resolved ctor's own union (module-correct — C3)
                // over the last-writer bare-name map.
                other => self
                    .ctor_union_owner(ctor.as_ref().map(|c| c.type_), other, &GoTy::Any)
                    .map(|(go, _)| GoTy::Named(go, Vec::new())),
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
                self.pattern_nominal_ty(&self.body.pats[*a])
                    .unwrap_or(GoTy::Any)
            } else {
                ty.clone()
            };
            let raw = GoExpr::new(
                GoExprKind::Selector(Box::new(subj.clone()), field.into()),
                GoTy::Any,
            );
            let field_expr = if sub_ty == GoTy::Any {
                GoExpr::new(
                    GoExprKind::Selector(Box::new(subj.clone()), field.into()),
                    ty.clone(),
                )
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

/// The top-level record extension-variable NAME of an inferred type, if it is an
/// open record. Read-back names row vars by union-find id, so a name that recurs
/// across positions is literally the SAME row var.
fn record_ext_name(ty: Option<&Ty>) -> Option<&Name> {
    match ty {
        Some(Ty::Record(_, Some(name))) => Some(name),
        _ => None,
    }
}

/// Row-polymorphism flags for a def: `(per-param, result)`. A position is
/// row-polymorphic when its inferred type is an OPEN record whose extension-var
/// name is SHARED with another param/result position (count ≥ 2) — i.e. the row
/// variable flows between the parameter and the result, as in
/// `bump r = { r | age = r.age + 1 }` : `{age|ρ} -> {age|ρ}`. Those positions
/// must lower to `any` (reflective `rt.Field`/`rt.RecordUpdate`) instead of a
/// closed Go struct, otherwise a caller's wider record has its extra fields
/// coerced away (the row-poly result-access bug). Single-occurrence open rows
/// (subset-of-nominal params, locally-consumed records) are NOT row-poly and
/// keep their concrete struct — baseline-identical.
fn row_poly_flags(body: &Body, types: &BodyTypes) -> (Vec<bool>, bool) {
    use std::collections::HashMap as Hm;
    let param_tys: Vec<Option<Ty>> = body
        .params
        .iter()
        .map(|p| match &body.pats[*p] {
            Pattern::Var(id) => types.locals.get(id).cloned(),
            _ => None,
        })
        .collect();
    let mut counts: Hm<Name, u32> = Hm::new();
    for t in param_tys
        .iter()
        .map(|t| t.as_ref())
        .chain(std::iter::once(types.result.as_ref()))
    {
        if let Some(name) = record_ext_name(t) {
            *counts.entry(name.clone()).or_insert(0) += 1;
        }
    }
    let is_rp = |t: Option<&Ty>| {
        record_ext_name(t).is_some_and(|n| counts.get(n).copied().unwrap_or(0) >= 2)
    };
    let pflags = param_tys.iter().map(|t| is_rp(t.as_ref())).collect();
    let rflag = is_rp(types.result.as_ref());
    (pflags, rflag)
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

/// The PROCESS ENTRY's task force. Runs the entry Task and honours its result:
/// an `Err` is reported and the process exits non-zero.
///
/// This used to be a bare `_ = rt.AnyTaskRun(<main>)`, which threw the entry's
/// `Result` into the blank identifier. A `main : Task Error ()` that FAILED
/// therefore printed nothing about the failure and exited 0 — so every gate
/// keyed on exit status was blind to app-level failure. That is how a golden
/// file came to hold one byte encoding a dead `Db.connect` and stayed green.
///
/// Kept separate from [`any_task_run`] deliberately. `rt.AnyTaskRun` is shared
/// with user-level `Task.run` / `Task.perform` and with every `let _ = <task>`
/// discard, where "run it and ignore the result" is the CORRECT semantics —
/// teaching the shared helper to exit the process would break them. Only the
/// process entry has an exit code to report through, so only the process entry
/// gets this wrapper.
///
/// Emits:
/// ```go
/// if _skyEntry := rt.AnyTaskRun(<main>); rt.ResultTag(_skyEntry) == 1 {
///     _ = rt.Log_error(rt.Debug_toString(rt.ResultErr(_skyEntry)))
///     _ = rt.System_exit(1)
/// }
/// ```
/// `ResultTag` returns -1 for a non-`SkyResult` and 0 for `Ok`, so only a real
/// `Err` (tag 1) trips it — a succeeding entry is untouched and still exits 0.
fn entry_task_run(expr: GoExpr, out: &mut Vec<GoStmt>) {
    const ENTRY: &str = "_skyEntry";
    out.push(GoStmt::Short(ENTRY.to_string(), any_task_run(expr)));
    let entry_ref = || GoExpr::new(GoExprKind::Ident(ENTRY.to_string()), GoTy::Any);
    let cond = GoExpr::new(
        GoExprKind::Binary(
            GoBin::Eq,
            Box::new(call_rt(
                "rt.ResultTag",
                vec![entry_ref()],
                GoTy::Bare(Prim::Int),
            )),
            Box::new(GoExpr::new(
                GoExprKind::IntLit(1),
                GoTy::Bare(Prim::Int),
            )),
        ),
        GoTy::Bare(Prim::Bool),
    );
    // `rt.Log_error` follows the Task-everywhere doctrine and returns a LAZY
    // thunk — discarding it would emit nothing at all, which is most of the
    // defect. Force it through the ordinary auto-force path.
    let report = any_task_run(call_rt(
        "rt.Log_error",
        // `Basics_errorToStringT` routes a Sky `Error` through `renderSkyError`,
        // so the entry reports "Unexpected: deliberate entry failure" rather than
        // a raw Go struct dump; it falls back to `%v` for any other error type.
        vec![call_rt(
            "rt.Basics_errorToStringT",
            vec![call_rt("rt.ResultErr", vec![entry_ref()], GoTy::Any)],
            GoTy::Bare(Prim::Str),
        )],
        GoTy::Any,
    ));
    let exit = call_rt(
        "rt.System_exit",
        vec![GoExpr::new(GoExprKind::IntLit(1), GoTy::Bare(Prim::Int))],
        GoTy::Any,
    );
    out.push(GoStmt::If(
        cond,
        vec![GoStmt::Discard(report), GoStmt::Discard(exit)],
        Vec::new(),
    ));
}

/// `rt.AnyTaskRun(expr)` — force a Task at an entry boundary (doc 08 §3).
fn any_task_run(expr: GoExpr) -> GoExpr {
    GoExpr::new(
        GoExprKind::Call(
            Box::new(GoExpr::new(
                GoExprKind::Ident("rt.AnyTaskRun".into()),
                GoTy::Any,
            )),
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
        // Typed-tuple codegen: each element renders
        // to its concrete Go type, so a `(String, Int)` literal emits
        // `rt.T2[string, int]{…}`. A floor/type-var element is `GoTy::Any`,
        // which renders to `"any"` — so a partially-typed tuple keeps that
        // position `any` (e.g. `rt.T2[any, int]`). Phase 0 hardened the
        // runtime reflection sites (fst/snd/Dict.fromList) so these typed
        // instantiations flow soundly.
        let parts: Vec<String> = xs.iter().map(render_goty).collect();
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
        // Tuples render as the runtime `rt.TN[…]` generic instantiation with
        // each element's concrete Go type (typed-tuple codegen). A
        // `GoTy::Any` element renders to `"any"`, so a floor/type-var position
        // stays `any` (partial typing, e.g. `rt.T2[any, int]`). Phase 0
        // hardened the runtime reflection sites to accept these typed shapes.
        GoTy::Tuple(xs) => match xs.len() {
            // Typed structs `rt.T2`..`rt.T9`; arity ≥10 → slice-backed
            // `rt.SkyTupleN` (must match codegen::render_tuple_ty + lower_tuple).
            2..=9 => format!(
                "rt.T{}[{}]",
                xs.len(),
                xs.iter().map(render_goty).collect::<Vec<_>>().join(", ")
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
            parse_mod(
                "module B exposing (..)\n\nimport A as C\n\ntype Msg = BLocal | Wrap C.Msg\n",
            ),
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

#[cfg(test)]
mod memoised_effect_lint_tests {
    //! Unit coverage for the memoised-CAF stale-read lint's decision core (the
    //! classifiers where correctness + false-positive risk live). The end-to-end
    //! firing is exercised by the CLI fixtures / example sweep; here we pin the
    //! kernel/type classification so a future edit can't silently start warning
    //! on the blessed handle pattern or stop warning on a DB read.
    use super::*;

    #[test]
    fn store_read_kernels_classify_but_connect_does_not() {
        // Reads that go stale when frozen.
        assert!(is_store_read_kernel("Db", "query"));
        assert!(is_store_read_kernel("Db", "queryObjects"));
        assert!(is_store_read_kernel("Db", "getById"));
        assert!(is_store_read_kernel("Db", "findManyByField"));
        assert!(is_store_read_kernel("Std.Db", "getById")); // fully-qualified alias
        // The blessed memoised-HANDLE kernels must NOT be reads.
        assert!(!is_store_read_kernel("Db", "connect"));
        assert!(!is_store_read_kernel("Db", "open"));
        assert!(!is_store_read_kernel("Db", "close"));
        // WRITES / DDL are run-once-intended, NOT stale reads (boot initDb).
        assert!(!is_store_read_kernel("Db", "exec"));
        assert!(!is_store_read_kernel("Db", "execRaw"));
        // Env / unrelated kernels are not store reads.
        assert!(!is_store_read_kernel("System", "getenv"));
    }

    #[test]
    fn kernel_effect_covers_fresh_and_read_not_env() {
        assert_eq!(kernel_effect("Time", "now"), Some(EffectKind::Fresh));
        assert_eq!(kernel_effect("Uuid", "v4"), Some(EffectKind::Fresh));
        assert_eq!(kernel_effect("Db", "query"), Some(EffectKind::StoreRead));
        // System.getenv is a read-once-intended pattern (apiKey) — never flagged.
        assert_eq!(kernel_effect("System", "getenv"), None);
        assert_eq!(kernel_effect("Db", "connect"), None);
        // Writes are run-once-intended, not stale reads.
        assert_eq!(kernel_effect("Db", "exec"), None);
    }

    #[test]
    fn stale_data_result_gates_correctly() {
        // Stale-able DATA → fire-eligible.
        assert!(is_stale_data_result(&Ty::app(
            "List",
            vec![Ty::app("Product", vec![])]
        )));
        assert!(is_stale_data_result(&Ty::app("String", vec![])));
        assert!(is_stale_data_result(&Ty::app("Int", vec![])));
        // Result/Task wrapping DATA is transparent → still fire-eligible.
        assert!(is_stale_data_result(&Ty::app(
            "Result",
            vec![Ty::app("Error", vec![]), Ty::app("List", vec![Ty::app("Post", vec![])])]
        )));

        // Handles / config descriptors → suppressed.
        assert!(!is_stale_data_result(&Ty::app("Db", vec![])));
        assert!(!is_stale_data_result(&Ty::app("Pool", vec![])));
        assert!(!is_stale_data_result(&Ty::app("Std.Db.Db", vec![]))); // qualified
        // FP1: `products : Store Product` — a table/config descriptor.
        assert!(!is_stale_data_result(&Ty::app(
            "Store",
            vec![Ty::app("Product", vec![])]
        )));
        // FP2: `ensureMigrations : Result Error ()` — a completion signal.
        assert!(!is_stale_data_result(&Ty::app(
            "Result",
            vec![Ty::app("Error", vec![]), Ty::Unit]
        )));
        // `db : Result Error Db` — Result wrapping a handle → suppressed.
        assert!(!is_stale_data_result(&Ty::app(
            "Result",
            vec![Ty::app("Error", vec![]), Ty::app("Db", vec![])]
        )));
        // Unit + function results → suppressed.
        assert!(!is_stale_data_result(&Ty::Unit));
        assert!(!is_stale_data_result(&Ty::Fun(
            Box::new(Ty::Unit),
            Box::new(Ty::app("Int", vec![]))
        )));
    }

    #[test]
    fn merge_prefers_store_read_then_fresh() {
        use EffectKind::*;
        assert_eq!(merge_effect(Some(Fresh), Some(StoreRead)), Some(StoreRead));
        assert_eq!(merge_effect(Some(StoreRead), Some(Fresh)), Some(StoreRead));
        assert_eq!(merge_effect(Some(Fresh), None), Some(Fresh));
        assert_eq!(merge_effect(None, None), None);
    }
}

#[cfg(test)]
mod tco_tests {
    //! Regression: a tail-recursive user function lowers to a `for {}` loop
    //! (param-reassignment + `continue`), NOT a recursive Go self-call — the
    //! Limitation #8 auto-TCO pass. Ported from the oracle
    //! `Sky.Build.TailCallOpt`.
    use super::*;
    use hir::SourceDb;

    fn parse_mod(src: &str) -> syntax::Parse {
        syntax::parse(src, base::FileId(0))
    }

    /// Recursively test whether any `GoStmt` in `body` (or nested) matches `f`.
    fn any_stmt(body: &[GoStmt], f: &dyn Fn(&GoStmt) -> bool) -> bool {
        body.iter().any(|s| {
            f(s) || match s {
                GoStmt::Loop(b) => any_stmt(b, f),
                GoStmt::If(_, t, e) => any_stmt(t, f) || any_stmt(e, f),
                GoStmt::IfTypeAssert { then, .. } => any_stmt(then, f),
                _ => false,
            }
        })
    }

    #[test]
    fn tail_recursive_def_emits_loop_not_recursion() {
        let mut db = SourceDb::new();
        let src = "module Main exposing (main)\n\
                   \n\
                   countDown : Int -> Int -> Int\n\
                   countDown n acc =\n\
                   \x20   if n <= 0 then acc else countDown (n - 1) (acc + 1)\n\
                   \n\
                   main : Int\n\
                   main = countDown 5 0\n";
        let mid = db.add_module("Main", parse_mod(src));
        let out = lower_program(&db, mid);

        let f = out
            .items
            .iter()
            .find_map(|it| match it {
                GoItem::Func(fd) if fd.name == "Main_countDown" => Some(fd),
                _ => None,
            })
            .expect("Main_countDown must be lowered (reachable from main)");

        // (1) The body is a single forever-loop — the TCO wrap.
        assert!(
            matches!(f.body.as_slice(), [GoStmt::Loop(_)]),
            "tail-recursive body must be wrapped in a single `for {{}}` loop, got: {:?}",
            f.body
        );

        // (2) A tail self-call became a `continue` jump.
        assert!(
            any_stmt(&f.body, &|s| matches!(s, GoStmt::Continue)),
            "the tail self-call must lower to `continue`"
        );

        // (3) A param is reassigned before the jump (loop state update).
        assert!(
            any_stmt(&f.body, &|s| matches!(s, GoStmt::Assign(_, _))),
            "the tail jump must reassign a parameter"
        );

        // (4) No recursive Go self-call survives anywhere in the emitted func —
        // the whole point of TCO. `Main_countDown` must not appear in the body's
        // debug rendering (params are `v_*`; the name field is separate).
        let body_dbg = format!("{:?}", f.body);
        assert!(
            !body_dbg.contains("Main_countDown"),
            "TCO'd body must contain NO recursive call to Main_countDown; body: {body_dbg}"
        );
    }
}
