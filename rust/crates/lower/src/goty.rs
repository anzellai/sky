//! `sky_ty_to_go` — the single Sky→Go type map (doc 07 §3). Total, returns
//! `GoTy`, never a string. Nominal user types (ADTs / record aliases) resolve
//! through the `TypeEnv` the lowerer builds from the program's type declarations.

use crate::ir::{GoTy, Prim};
use base::Name;
use std::collections::HashMap;
use ty::Ty;

/// How a nominal type name renders in Go, plus its kind (for value lowering).
#[derive(Clone, Debug)]
pub struct Nominal {
    /// The Go type name as used in a type position, e.g. `Main_Model_R`,
    /// `Main_Msg`, `Sky_Core_Error_Error`.
    pub go_name: String,
    pub kind: NominalKind,
    /// Number of Go generic type parameters (`Cfg_R[T1, …]`). 0 = non-generic
    /// (the M4 baseline for every ADT / iota + record aliases with no type
    /// vars). A parametric record alias (`type alias Cfg msg = { … }`) records
    /// its DISTINCT non-`"any"` field-type vars here (in first-appearance
    /// order) so the App use site (`Cfg Msg`) propagates `Cfg_R[Msg]` instead
    /// of erasing to the non-generic `Cfg_R`. Only `NominalKind::Record` is
    /// ever > 0 in this milestone.
    pub type_arity: usize,
    /// A phantom opaque-handle type: a single-variant iota enum whose sole
    /// constructor follows the stdlib `<Name>_OPAQUE` convention (`Route`,
    /// `Server`, `Cookie`). The Go type decl renders as `type X = int` (a
    /// placeholder), but the *runtime value* is a kernel struct handle
    /// (`rt.SkyRoute`, …) produced by an FFI kernel — never an `int`. So at a
    /// value/type position these resolve to `any`, exactly like the other
    /// kernel-opaque handles (`Decoder`, `Value`, `Cmd`, `Sub`). Coercing the
    /// handle to the `int` alias would panic (`rt.Coerce: expected int, got
    /// rt.SkyRoute`).
    pub opaque: bool,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum NominalKind {
    Record,
    Adt,
    Iota,
}

/// The type environment the Sky→Go map consults: nominal type names + a field-set
/// index that resolves a *structural* record (what inference produces after
/// transparent alias expansion, `ty::sig::World::expand`) back to its named `_R`
/// alias — so records emit + assign as nominal Go types (the positional-ABI
/// contract, doc 08 §_fieldIndex).
///
/// determinism (L4): every `HashMap` field below is consulted lookup-only
/// (`.get()` / `.contains_key()`); their iteration order never reaches emitted Go
/// or diagnostics, so `HashMap` is sound here despite its randomized order.
#[derive(Default)]
pub struct TypeEnv {
    pub nominal: HashMap<String, Nominal>,
    /// Module-scoped nominal lookup: `(module_name, type_name) → Nominal`. Two
    /// modules can each declare a `Msg`/`Model` ADT; the flat `nominal` map
    /// collapses them (last writer wins), so a type name resolved inside module
    /// M must prefer M's own declaration. Falls back to `nominal` when M declares
    /// no such name. Only collisions differ from the flat map — a name declared
    /// in one module resolves identically either way.
    pub nominal_by_module: HashMap<(String, String), Nominal>,
    /// sorted field-name list → the record aliases' Go `_R` type names. Usually
    /// one, but two aliases can share a field-NAME set while differing in field
    /// TYPES (a user's `EnvForm {key,value:String}` vs `Std.Analytics.EventProp
    /// {key,value:PropValue}`); both are kept so the structural resolver can pick
    /// the one whose field TYPES actually match (see `select_record_candidate`),
    /// instead of the first-registered arbitrarily winning.
    pub record_fieldsets: HashMap<Vec<String>, Vec<String>>,
    /// `_R` Go name → the alias's DISTINCT non-`"any"` type-param vars, in
    /// first-appearance order across the field types. Empty for a
    /// non-parametric record alias. Used to (a) instantiate `Cfg_R[Msg]` at a
    /// structurally-resolved use site by recovering each param's concrete type
    /// from the matched record's fields, and (b) render the generic decl.
    pub record_params: HashMap<String, Vec<Name>>,
    /// `_R` Go name → the alias's field templates (Sky field name → declared
    /// Sky type, which still carries the type-param `Ty::Var`s). Used to
    /// recover concrete type args for a structurally-resolved parametric alias.
    pub record_templates: HashMap<String, Vec<(String, Ty)>>,
    /// The app's single TEA Model, when detected: `(sorted full field-set, `_R`
    /// Go name)`. An unannotated `view`/`update`/`subscriptions` or a Model-taking
    /// helper infers a *subset* row over the Model (only the fields it touches);
    /// the runtime value is always the full nominal record, so a record whose
    /// field-set is a SUBSET of the Model resolves to the nominal `_R` here. One
    /// Model per app makes this unambiguous (doc 07 §3 subset-record case, the
    /// TEA disambiguator).
    pub model: Option<(Vec<String>, String)>,
}

/// Map a Sky type to its structural Go type. `cur_mod` is the module the type is
/// being lowered in (when known) — it disambiguates a nominal name declared in
/// more than one module (`Counter.Msg` vs `Main.Msg`).
pub fn sky_ty_to_go(t: &Ty, env: &TypeEnv) -> GoTy {
    sky_ty_to_go_in(t, env, None)
}

pub fn sky_ty_to_go_in(t: &Ty, env: &TypeEnv, cur_mod: Option<&str>) -> GoTy {
    let empty: HashMap<Name, GoTy> = HashMap::new();
    go_ty(t, env, cur_mod, &empty)
}

/// Map a Sky type to Go, resolving the given type-param `Ty::Var`s to a
/// caller-supplied target `GoTy` (a Go generic `GoTy::TyVar("T1")` when emitting
/// a parametric alias's decl; the concrete instantiation `GoTy::Named("Msg")`
/// when typing a record literal at a `Cfg_R[Msg]` slot). Vars NOT in the map
/// still erase to `any` (the generic-erase floor). Sharing this single core with
/// the plain map guarantees the decl form and every use-site form agree exactly.
pub fn sky_ty_to_go_params(
    t: &Ty,
    env: &TypeEnv,
    cur_mod: Option<&str>,
    params: &HashMap<Name, GoTy>,
) -> GoTy {
    go_ty(t, env, cur_mod, params)
}

fn go_ty(t: &Ty, env: &TypeEnv, cur_mod: Option<&str>, params: &HashMap<Name, GoTy>) -> GoTy {
    match t {
        Ty::App(name, args) => app_to_go(name.as_str(), args, env, cur_mod, params),
        Ty::Fun(a, b) => {
            // Collapse the curried spine into an N-ary Go func.
            let mut ps = vec![go_ty(a, env, cur_mod, params)];
            let mut ret = b.as_ref();
            while let Ty::Fun(x, y) = ret {
                ps.push(go_ty(x, env, cur_mod, params));
                ret = y;
            }
            GoTy::Func(ps, Box::new(go_ty(ret, env, cur_mod, params)))
        }
        // Tuple element types are kept for TYPING (pattern binds) AND for
        // typed-tuple codegen: `render_goty` / codegen
        // `render_ty` now render each element to its concrete Go type, so a
        // `(String, Int)` tuple emits `rt.T2[string, int]`. A `GoTy::Any`
        // element (floor / type-var, e.g. Ty::Var below) stays `any` — partial
        // typing like `rt.T2[Model_R, any]`. The runtime reflection paths
        // (`Basics_fst/snd`, `Dict_fromList`, `Dict_fromListT/TA`) were hardened
        // (route through `AsTuple2`/`AsTuple3`) so these distinct nominal
        // instantiations flow soundly instead of panicking on the `.(SkyTuple2)`
        // assertion.
        Ty::Tuple(xs) => GoTy::Tuple(xs.iter().map(|x| go_ty(x, env, cur_mod, params)).collect()),
        Ty::Unit => GoTy::Unit,
        // A type-param var resolves to its caller-supplied target (a Go generic
        // `T1` for a parametric alias's decl, or the concrete instantiation at a
        // use site); a var NOT in the map → generic erase to `any` (doc 07 §6
        // class 8).
        Ty::Var(n) => params.get(n).cloned().unwrap_or(GoTy::Any),
        Ty::Record(fields, ext) => {
            // resolve to a nominal `_R` alias when the field-name set matches one.
            let mut names: Vec<String> =
                fields.iter().map(|(n, _)| n.as_str().to_string()).collect();
            names.sort();
            if let Some(candidates) = env.record_fieldsets.get(&names) {
                // Among aliases sharing this field-NAME set, pick the one whose
                // field TYPES match this record (a user `EnvForm {…value:String}`
                // must not resolve to `Std.Analytics.EventProp {…value:PropValue}`).
                if let Some(go_name) =
                    select_record_candidate(candidates, fields, env, cur_mod, params)
                {
                    // A parametric alias resolved STRUCTURALLY (a record literal /
                    // unannotated value whose inferred closed record matches the
                    // alias's field set): the Go type is generic, so a bare
                    // `Cfg_R` (no type args) would not compile. Recover each type
                    // arg by matching the alias's field templates against this
                    // record's concrete field types. On full recovery emit the
                    // instantiation `Cfg_R[Msg]`; if any arg can't be recovered,
                    // fall through to the anonymous-struct form (safe — never a
                    // dangling generic reference).
                    match instantiate_structural(go_name, fields, env, cur_mod, params) {
                        Some(gt) => return gt,
                        None => {
                            if env
                                .record_params
                                .get(go_name)
                                .map(|p| p.is_empty())
                                .unwrap_or(true)
                            {
                                return GoTy::Named(go_name.clone(), vec![]);
                            }
                            // parametric but unrecoverable → fall through to anon struct
                        }
                    }
                }
            }
            // subset→nominal: an anonymous record whose fields are all present in
            // the app's single TEA Model resolves to the nominal Model `_R`. This
            // is what the runtime actually passes — unannotated `view model` /
            // `viewHistory model` infer a subset row; both sides must land on the
            // nominal `_R` so the boundary coercion elides instead of asserting one
            // subset struct against a different one (the `rt.Coerce` render panic).
            if let Some((model_fields, model_go)) = &env.model {
                if !names.is_empty()
                    && names.len() < model_fields.len()
                    && names.iter().all(|n| model_fields.binary_search(n).is_ok())
                {
                    return GoTy::Named(model_go.clone(), vec![]);
                }
            }
            // NOTE on OPEN records (`ext = Some(ρ)`): most open rows are NOT
            // row-polymorphic in a way that needs erasure — they are subset rows
            // over a nominal (resolved above via `record_fieldsets` / the Model
            // subset check) or a locally-consumed record whose extra fields are
            // simply dropped at the coercion boundary. Blanket-erasing every open
            // record to `any` here regresses those (multi-nominal composite apps
            // whose subset rows match no single Model). The ONE case that needs
            // `any` — a genuinely row-polymorphic function whose row var flows
            // from a PARAMETER into the RESULT (`bump r = { r | age = … }`) — is
            // detected structurally in `lower_def` (via the shared row-var name
            // across param+result) and erased there, keeping this map total +
            // baseline-identical for every other record.
            // #173 facet #3: a genuinely OPEN record (`ext = Some(ρ)`, an
            // unresolved row var) that reached here matched no nominal / Model
            // above, so it is a SUBSET view of a fuller record — the runtime value
            // carries fields beyond the ones named here. Lowering it to a CLOSED
            // anon struct physically DROPS those fields when the full value is
            // coerced in (`Dict k (List Record)` where the record is
            // field-accessed → `name` lost; also every foldl/foldr accumulator or
            // container that stores such a record and reads it back). Box as `any`
            // instead — field access routes through the reflective `rt.Field` path
            // (the documented irreducible floor: sound, recovers safely), and the
            // FULL runtime value is preserved. This trades static typedness for the
            // guarantee that no Sky program can ever silently lose record fields
            // ("if it compiles it works" — the coerce-floor widening this causes is
            // re-blessed as a justified correctness cost). A CLOSED record
            // (`ext = None`) keeps its precise anon struct — unchanged; the
            // row-poly param→result case is already erased in `lower_def`.
            if ext.is_some() {
                return GoTy::Any;
            }
            // else: anonymous Go struct. Field names Go-exported (capitalised).
            // Go anonymous-struct field ORDER is part of the type's identity, so
            // every `Ty::Record` for the same field-set MUST lower to the SAME
            // field order — otherwise two structurally-identical records (e.g. an
            // annotation's decl-order fields vs a checker-normalised result type)
            // render as distinct Go types and `go build` rejects the assignment
            // (`struct{Name;Age;Active}` vs `struct{Active;Age;Name}`). Sort by the
            // Go field name — the single canonical order shared with
            // `lower_record`'s all-`any` fallback + record-literal keyed
            // construction (keyed-literal order is field-name independent) + the
            // oracle's observed output. Matches the `_fieldIndex` non-regression
            // rule (field enumeration sorted before any order-dependent emission).
            let mut go_fields: Vec<(base::Name, GoTy)> = fields
                .iter()
                .map(|(n, ft)| {
                    (
                        base::Name::new(&cap(n.as_str())),
                        go_ty(ft, env, cur_mod, params),
                    )
                })
                .collect();
            go_fields.sort_by(|a, b| a.0.as_str().cmp(b.0.as_str()));
            GoTy::Struct(go_fields)
        }
        Ty::Error => GoTy::Any,
    }
}

/// Recover the concrete Go type args for a parametric record alias resolved
/// structurally. For each declared type-param var, find (by unification against
/// the alias's field templates) the concrete Sky type this record binds it to,
/// then lower that to Go. Returns `None` if the alias has no params (caller
/// handles the non-generic case) or if any param stays unbound.
/// Among the record aliases that share a field-NAME set, choose the one whose
/// declared field TYPES are compatible with the concrete record `fields`. Two
/// aliases with the same field names but different field types (e.g. a user's
/// `EnvForm {key, value : String}` and `Std.Analytics.EventProp {key, value :
/// PropValue}`, which is pulled in transitively by `Std.Live`) would otherwise
/// collide, and the first-registered would win — mis-typing the other's params
/// and emitting a `go build` failure (`sky check` passes but the Go doesn't).
///
/// A `Ty::Var` template slot is a WILDCARD (parametric alias — the concrete arg
/// is recovered afterwards by `instantiate_structural`), so this never regresses
/// the `Cfg msg` structural path. When there is a single candidate (the common
/// case) or none matches, returns the first — preserving prior behaviour exactly.
fn select_record_candidate<'a>(
    candidates: &'a [String],
    fields: &[(Name, Ty)],
    env: &TypeEnv,
    cur_mod: Option<&str>,
    params: &HashMap<Name, GoTy>,
) -> Option<&'a String> {
    if candidates.len() <= 1 {
        return candidates.first();
    }
    let concrete: HashMap<&str, &Ty> = fields.iter().map(|(n, t)| (n.as_str(), t)).collect();
    let compatible = |go_name: &str| -> bool {
        let Some(templates) = env.record_templates.get(go_name) else {
            return false;
        };
        templates.iter().all(|(fname, tmpl)| match concrete.get(fname.as_str()) {
            None => false,
            // parametric slot → wildcard (resolved later by instantiate_structural)
            Some(_) if matches!(tmpl, Ty::Var(_)) => true,
            Some(ct) => {
                go_ty(tmpl, env, cur_mod, params) == go_ty(ct, env, cur_mod, params)
            }
        })
    };
    candidates
        .iter()
        .find(|c| compatible(c))
        .or_else(|| candidates.first())
}

fn instantiate_structural(
    go_name: &str,
    rec_fields: &[(Name, Ty)],
    env: &TypeEnv,
    cur_mod: Option<&str>,
    params: &HashMap<Name, GoTy>,
) -> Option<GoTy> {
    let param_vars = env.record_params.get(go_name)?;
    if param_vars.is_empty() {
        return None;
    }
    let templates = env.record_templates.get(go_name)?;
    // field name → concrete inferred type
    let concrete: HashMap<&str, &Ty> = rec_fields.iter().map(|(n, t)| (n.as_str(), t)).collect();
    let mut bound: HashMap<Name, Ty> = HashMap::new();
    for (fname, tmpl) in templates {
        if let Some(ct) = concrete.get(fname.as_str()) {
            unify_param(tmpl, ct, param_vars, &mut bound);
        }
    }
    let mut args = Vec::with_capacity(param_vars.len());
    for pv in param_vars {
        let bt = bound.get(pv)?; // unbound → give up (None)
        args.push(go_ty(bt, env, cur_mod, params));
    }
    Some(GoTy::Named(go_name.to_string(), args))
}

/// Structural unification of an alias field TEMPLATE against the concrete
/// inferred field type, binding any template `Ty::Var` that is one of the
/// alias's type params. First binding wins (deterministic).
fn unify_param(tmpl: &Ty, concrete: &Ty, param_vars: &[Name], bound: &mut HashMap<Name, Ty>) {
    match (tmpl, concrete) {
        (Ty::Var(v), c) if param_vars.contains(v) => {
            bound.entry(v.clone()).or_insert_with(|| c.clone());
        }
        (Ty::App(_, ta), Ty::App(_, ca)) => {
            for (a, b) in ta.iter().zip(ca.iter()) {
                unify_param(a, b, param_vars, bound);
            }
        }
        (Ty::Fun(a1, b1), Ty::Fun(a2, b2)) => {
            unify_param(a1, a2, param_vars, bound);
            unify_param(b1, b2, param_vars, bound);
        }
        (Ty::Tuple(xs), Ty::Tuple(ys)) => {
            for (a, b) in xs.iter().zip(ys.iter()) {
                unify_param(a, b, param_vars, bound);
            }
        }
        (Ty::Record(fs1, _), Ty::Record(fs2, _)) => {
            let m: HashMap<&str, &Ty> = fs2.iter().map(|(n, t)| (n.as_str(), t)).collect();
            for (n, t) in fs1 {
                if let Some(c) = m.get(n.as_str()) {
                    unify_param(t, c, param_vars, bound);
                }
            }
        }
        _ => {}
    }
}

fn app_to_go(
    name: &str,
    args: &[Ty],
    env: &TypeEnv,
    cur_mod: Option<&str>,
    params: &HashMap<Name, GoTy>,
) -> GoTy {
    let go = |t: &Ty| go_ty(t, env, cur_mod, params);
    // A qualified reference (`Counter.Msg`) carries its declaring module in the
    // name — resolve it to THAT module's nominal directly, bypassing the
    // `cur_mod` disambiguation (which would wrongly pick a same-module `Msg`).
    // Falls through to the bare final segment when the qualified name isn't a
    // known user nominal (kernel/FFI qualified types like `Json.Value`), so those
    // keep their existing bare-name resolution below.
    let bare = if let Some((qual, tail)) = name.rsplit_once('.') {
        if let Some(n) = env
            .nominal_by_module
            .get(&(qual.to_string(), tail.to_string()))
        {
            if n.opaque {
                return GoTy::Any;
            }
            // Parametric record alias referenced qualified (`Counter.Cfg Msg`):
            // propagate the type args (see the bare-name path below).
            if n.kind == NominalKind::Record && n.type_arity > 0 && n.type_arity == args.len() {
                return GoTy::Named(n.go_name.clone(), args.iter().map(&go).collect());
            }
            return GoTy::Named(n.go_name.clone(), vec![]);
        }
        tail
    } else {
        name
    };
    match (bare, args.len()) {
        ("Int", 0) => GoTy::Bare(Prim::Int),
        ("Float", 0) => GoTy::Bare(Prim::Float),
        ("String", 0) => GoTy::Bare(Prim::Str),
        ("Bool", 0) => GoTy::Bare(Prim::Bool),
        ("Char", 0) => GoTy::Bare(Prim::Rune),
        ("Bytes", 0) => GoTy::Bare(Prim::Bytes),
        // numeric-flex default → int (Sky's `number` defaults to Int).
        ("number", 0) => GoTy::Bare(Prim::Int),
        ("comparable" | "appendable" | "compappend", 0) => GoTy::Any,
        ("List", 1) => GoTy::Slice(Box::new(go(&args[0]))),
        ("Maybe", 1) => GoTy::Named("rt.SkyMaybe".into(), vec![go(&args[0])]),
        ("Result", 2) => GoTy::Named("rt.SkyResult".into(), vec![go(&args[0]), go(&args[1])]),
        ("Task", 2) => GoTy::Named("rt.SkyTask".into(), vec![go(&args[0]), go(&args[1])]),
        // A Sky `Dict k v` is ALWAYS stored as `map[string]V` at runtime — keys are
        // stringified (`rt.Dict_empty() any` returns `map[string]any`; an `Int`-keyed
        // dict stores key `"0"`). The oracle renders every dict key as `string`
        // (`bucketsByIndex : Dict Int … → map[string][]…`); narrowing goes through
        // `rt.AsMapT[V]` which returns `map[string]V`. Rendering the key as `goty(k)`
        // (e.g. `map[int]V`) never matches that runtime shape and panics under
        // `rt.Coerce` — so pin the Go key type to `string`.
        ("Dict", 2) => GoTy::Map(Box::new(GoTy::Bare(Prim::Str)), Box::new(go(&args[1]))),
        ("Cmd", _) => GoTy::Any,
        ("Sub", _) => GoTy::Any,
        // Kernel-opaque handle types carry a runtime handle (a `JsonDecoder` /
        // JSON `Value`), not a modelled Go struct — map to `any`. `Decoder` is
        // declared in multiple modules (`Sky.Core.Json.Decode` uses a
        // kernel-implicit one; `Std.Config`/`Std.Db.Decode` each phantom-define
        // `type Decoder a = Decoder`), so a flat nominal lookup would coerce a
        // real decoder to an unrelated module's phantom enum (`= int`) and
        // panic at runtime. `Value` is the same story for JSON encoders.
        ("Decoder", _) => GoTy::Any,
        ("Value", _) => GoTy::Any,
        _ => {
            // Prefer the current module's own declaration of `name` when it has
            // one (disambiguates a `Msg`/`Model` declared in several modules);
            // fall back to the flat map otherwise.
            let nominal = cur_mod
                .and_then(|m| {
                    env.nominal_by_module
                        .get(&(m.to_string(), bare.to_string()))
                })
                .or_else(|| env.nominal.get(bare));
            if let Some(n) = nominal {
                // Phantom opaque-handle types (`Route`/`Server`/`Cookie`):
                // the runtime value is a kernel struct handle, not the `int`
                // the placeholder decl aliases. Resolve to `any` so lists of
                // them emit `[]any` and FFI-return elements aren't coerced to
                // `int` (which panics at runtime).
                if n.opaque {
                    return GoTy::Any;
                }
                // A PARAMETRIC record alias (`type alias Cfg msg = { … }`) is
                // emitted as a Go generic struct `Cfg_R[T1, …]` (Phase 2 of the
                // typed-Go ceiling), so an application `Cfg Msg` must propagate
                // its type args → `Cfg_R[Msg]`. The decl + ctor + every use site
                // share `sky_ty_to_go_params`, so the instantiations agree. A
                // floor arg (FFI-return / type-var) lowers to `any` here, giving
                // the correct partial `Cfg_R[any]`. Guard on an exact arity
                // match; a mismatch (shouldn't happen for a well-typed program)
                // falls through to the erased bare name.
                if n.kind == NominalKind::Record && n.type_arity > 0 && n.type_arity == args.len() {
                    return GoTy::Named(n.go_name.clone(), args.iter().map(&go).collect());
                }
                // Other user nominal types stay NON-generic in the M4 runtime
                // representation: sealed ADT → `rt.SkyADT` bag (`Fields []any`),
                // iota enum → `int`, non-parametric record → a plain struct. A
                // parametric ADT application (`ShouldRetry msg` / `Event msg`)
                // renders as the bare Go name — its payloads flow as `any` (the
                // generic-erase floor, doc 07 §6 class 8); the arg types'
                // reachability is driven by the variant use sites in
                // `emit_type_decl`, so dropping them here is safe.
                let _ = go; // ADT/non-parametric args intentionally erased
                GoTy::Named(n.go_name.clone(), vec![])
            } else {
                // Unknown nominal (FFI type, unmodelled) → erase to any.
                GoTy::Any
            }
        }
    }
}

fn cap(s: &str) -> String {
    let mut cs = s.chars();
    match cs.next() {
        Some(c) => c.to_uppercase().collect::<String>() + cs.as_str(),
        None => String::new(),
    }
}
