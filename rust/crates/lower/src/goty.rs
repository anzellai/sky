//! `sky_ty_to_go` — the single Sky→Go type map (doc 07 §3). Total, returns
//! `GoTy`, never a string. Nominal user types (ADTs / record aliases) resolve
//! through the `TypeEnv` the lowerer builds from the program's type declarations.

use crate::ir::{GoTy, Prim};
use base::Name;
use hir::KERNEL_IMPLICIT_TYPES;
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
/// the `Cfg msg` structural path.
///
/// # Why a *guess* is not allowed here
///
/// This used to end in `.or_else(|| candidates.first())`, and short-circuited to
/// `candidates.first()` whenever there was exactly one candidate — both without
/// any type check. Either path could hand back a nominal the record's own field
/// types CONTRADICT, and the lowerer then emitted a coercion into that nominal's
/// field types. That is the `{ key, value }` defect:
///
/// ```elm
/// type alias Kv = { key : String, value : String }
/// mk : String -> String -> Kv
/// mk k v = { key = k, value = v }        -- compiles; panics at runtime
/// ```
///
/// `Std.Analytics.EventProp = { key : String, value : PropValue }` is in every
/// compilation (no import needed), so it shares the `[key, value]` name key. The
/// lowerer's typed table is deliberately NOT seeded from annotations
/// (`Typer::body_types` vs `body_types_annotated` — codegen byte-stability), so
/// a constructor's param-valued fields read back as unsolved `Ty::Var`s. Neither
/// candidate then compared equal, the fallback picked the stdlib one by
/// registration order, and `value` was coerced into an ADT slot:
/// `rt.Coerce: expected rt.SkyADT, got string`.
///
/// The contract now: **resolve only to a nominal the field types actually
/// support, and only when they single one out.** Three tiers per candidate —
///
/// * `Mismatch` — a field's concrete type definitely differs from the template's.
///   The candidate is not viable at all; it can never be the right nominal.
/// * `Unknown` — the field's concrete type carries no information (it erases to
///   `any`, or is an unresolved `number`/`comparable` super). It neither
///   confirms nor refutes.
/// * otherwise the field matches (or the template slot is a parametric wildcard).
///
/// A candidate with any `Mismatch` is dropped. Among the survivors, one with no
/// `Unknown` at all is a determined match and wins (ties there are Go-shape
/// identical, so the first is taken — exactly the old `find` behaviour). If only
/// `Unknown`-bearing survivors remain, a LONE one still resolves — that is the
/// ordinary structural→nominal path every example depends on — but two or more
/// are genuinely indistinguishable, and `None` is returned so the record keeps
/// its structural form instead of being guessed onto one of them.
/// Does `t` contain anything the checker has not pinned down — a free type var,
/// an unresolved super (`number` / `comparable` / …), or an error node — at ANY
/// depth?
///
/// A type carrying one of these cannot refute a candidate alias: `Maybe t0`
/// against a declared `Maybe String` is under-determined, not contradictory. A
/// var that the caller HAS bound (a parametric alias instantiation) is resolved
/// and does not count.
fn has_unresolved(t: &Ty, params: &HashMap<Name, GoTy>) -> bool {
    match t {
        Ty::Var(n) => !params.contains_key(n),
        Ty::Error => true,
        Ty::App(n, args) => {
            matches!(
                n.as_str(),
                "number" | "comparable" | "appendable" | "compappend"
            ) || args.iter().any(|a| has_unresolved(a, params))
        }
        Ty::Fun(a, b) => has_unresolved(a, params) || has_unresolved(b, params),
        Ty::Tuple(xs) => xs.iter().any(|x| has_unresolved(x, params)),
        // An OPEN row is itself missing information about the rest of the record.
        Ty::Record(fs, ext) => {
            ext.is_some() || fs.iter().any(|(_, ft)| has_unresolved(ft, params))
        }
        Ty::Unit => false,
    }
}

fn select_record_candidate<'a>(
    candidates: &'a [String],
    fields: &[(Name, Ty)],
    env: &TypeEnv,
    cur_mod: Option<&str>,
    params: &HashMap<Name, GoTy>,
) -> Option<&'a String> {
    let concrete: HashMap<&str, &Ty> = fields.iter().map(|(n, t)| (n.as_str(), t)).collect();

    // Does this concrete field type carry no usable information — anywhere?
    //
    // The check must be RECURSIVE, not just top-level. `Codec.auto { note =
    // Nothing }` infers the field as `Maybe t0` where the alias declares
    // `Maybe String`; `items = []` infers `List t0` against `List Int`. Those
    // lower to different Go types, but they do not CONTRADICT the template —
    // they are simply not resolved yet. Treating them as refutations refused the
    // alias, erased the record, and `Codec.auto` then saw a bare `interface{}`.
    let uninformative = |ct: &Ty| -> bool {
        has_unresolved(ct, params) || go_ty(ct, env, cur_mod, params) == GoTy::Any
    };

    // (viable, unknown_count) — `None` when the candidate is refuted outright.
    let fit = |go_name: &str| -> Option<usize> {
        // No registered template → nothing to compare against, so nothing can be
        // refuted either. Count it as wholly undetermined rather than dropping
        // it: a lone such candidate still resolves (the pre-existing behaviour),
        // while two of them are correctly reported as indistinguishable.
        let Some(templates) = env.record_templates.get(go_name) else {
            return Some(1);
        };
        let mut unknown = 0usize;
        for (fname, tmpl) in templates {
            let Some(ct) = concrete.get(fname.as_str()) else {
                return None; // template field absent from the record → not this alias
            };
            // parametric slot → wildcard (resolved later by instantiate_structural)
            if matches!(tmpl, Ty::Var(_)) {
                continue;
            }
            let tg = go_ty(tmpl, env, cur_mod, params);
            // an `any`-typed template slot cannot refute anything
            if tg == GoTy::Any {
                continue;
            }
            if uninformative(ct) {
                unknown += 1;
                continue;
            }
            if go_ty(ct, env, cur_mod, params) != tg {
                return None; // definite contradiction
            }
        }
        Some(unknown)
    };

    let viable: Vec<(&String, usize)> = candidates
        .iter()
        .filter_map(|c| fit(c).map(|u| (c, u)))
        .collect();

    // A determined match — every field confirmed — wins outright.
    if let Some((c, _)) = viable.iter().find(|(_, u)| *u == 0) {
        return Some(c);
    }
    // Otherwise only partially-determined survivors remain. One is the ordinary
    // structural→nominal case; several are indistinguishable and must not be
    // guessed between.
    match viable.as_slice() {
        [(c, _)] => Some(c),
        _ => None,
    }
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
    // KERNEL-OPAQUE HANDLES WIN OVER EVERY NOMINAL LOOKUP, qualified or not.
    //
    // These names denote a runtime handle (a `JsonDecoder`, a JSON `Value`, a
    // command queue), not a modelled Go struct, so their Go type is `any` NO
    // MATTER which module's declaration a name resolves to. That has to be
    // decided BEFORE the qualified fast path below, because `Std.Config` really
    // does declare `type Decoder a = Decoder` (`sky-stdlib/Std/Config.sky:54`)
    // — a phantom whose Go form is a single-variant iota enum, `= int`.
    //
    // Once the checker started module-qualifying unions, references to that
    // phantom arrived as `Std.Config.Decoder`, hit `nominal_by_module` here, and
    // resolved to `Std_Config_Decoder` — so a real kernel decoder handle was
    // narrowed with `rt.Coerce[Std_Config_Decoder]`, i.e. coerced to an `int`.
    // `apps/relay` (which imports `Std.Config` for its typed decoders) caught it
    // as a +15 widening on the `coerce-floor` gate. The bare-name arms further
    // down had always covered this; hoisting them closes the qualified hole too.
    if matches!(
        ty::nominal::base(name),
        "Decoder" | "Value" | "Cmd" | "Sub"
    ) {
        return GoTy::Any;
    }
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
        // `Cmd` / `Sub` / `Decoder` / `Value` are handled by the kernel-opaque
        // pre-check at the top of this function, which fires for the qualified
        // form as well. Kept unreachable-but-listed would be a lie; they are
        // simply gone from this table. See that pre-check for the reasoning.
        _ => {
            // Prefer the current module's own declaration of `name` when it has
            // one (disambiguates a `Msg`/`Model` declared in several modules);
            // fall back to the flat map otherwise.
            //
            // EXCEPT a bare KERNEL-IMPLICIT type name (`Route`, `Session`,
            // `Response`, `Handler`, … — hir::KERNEL_IMPLICIT_TYPES). Such a name,
            // arriving here bare, is a FOREIGN kernel handle, never the CURRENT
            // module's same-named LOCAL type (a local reference is rewritten to
            // its qualified key upstream and takes the qualified fast-path). The
            // cur_mod preference would wrongly capture that local, binding a
            // kernel handle (e.g. `rt.liveRoute`) to the local nominal and
            // mis-narrowing it at runtime — the cross-module collision class. For
            // a kernel-implicit name use ONLY the flat map: it resolves a DECLARED
            // same-named type correctly (`Sky.Core.Error.Error`) and finds nothing
            // for a truly-undeclared one, which erases to `any` below — exactly
            // right for an opaque kernel handle.
            let nominal = if KERNEL_IMPLICIT_TYPES.contains(&bare) {
                env.nominal.get(bare)
            } else {
                cur_mod
                    .and_then(|m| {
                        env.nominal_by_module
                            .get(&(m.to_string(), bare.to_string()))
                    })
                    .or_else(|| env.nominal.get(bare))
            };
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
