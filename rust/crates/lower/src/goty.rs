//! `sky_ty_to_go` — the single Sky→Go type map (doc 07 §3). Total, returns
//! `GoTy`, never a string. Nominal user types (ADTs / record aliases) resolve
//! through the `TypeEnv` the lowerer builds from the program's type declarations.

use crate::ir::{GoTy, Prim};
use std::collections::HashMap;
use ty::Ty;

/// How a nominal type name renders in Go, plus its kind (for value lowering).
#[derive(Clone, Debug)]
pub struct Nominal {
    /// The Go type name as used in a type position, e.g. `Main_Model_R`,
    /// `Main_Msg`, `Sky_Core_Error_Error`.
    pub go_name: String,
    pub kind: NominalKind,
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
    /// sorted field-name list → the record alias's Go `_R` type name.
    pub record_fieldsets: HashMap<Vec<String>, String>,
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
    match t {
        Ty::App(name, args) => app_to_go(name.as_str(), args, env, cur_mod),
        Ty::Fun(a, b) => {
            // Collapse the curried spine into an N-ary Go func.
            let mut params = vec![sky_ty_to_go_in(a, env, cur_mod)];
            let mut ret = b.as_ref();
            while let Ty::Fun(x, y) = ret {
                params.push(sky_ty_to_go_in(x, env, cur_mod));
                ret = y;
            }
            GoTy::Func(params, Box::new(sky_ty_to_go_in(ret, env, cur_mod)))
        }
        // Tuple element types are kept for TYPING (pattern binds) AND for
        // typed-tuple codegen (v0.17 typed-Go ceiling): `render_goty` / codegen
        // `render_ty` now render each element to its concrete Go type, so a
        // `(String, Int)` tuple emits `rt.T2[string, int]`. A `GoTy::Any`
        // element (floor / type-var, e.g. Ty::Var below) stays `any` — partial
        // typing like `rt.T2[Model_R, any]`. The runtime reflection paths
        // (`Basics_fst/snd`, `Dict_fromList`, `Dict_fromListT/TA`) were hardened
        // (route through `AsTuple2`/`AsTuple3`) so these distinct nominal
        // instantiations flow soundly instead of panicking on the `.(SkyTuple2)`
        // assertion.
        Ty::Tuple(xs) => GoTy::Tuple(xs.iter().map(|x| sky_ty_to_go_in(x, env, cur_mod)).collect()),
        Ty::Unit => GoTy::Unit,
        // A remaining rigid/flex var → generic erase to `any` (doc 07 §6 class 8).
        Ty::Var(_) => GoTy::Any,
        Ty::Record(fields, ext) => {
            // resolve to a nominal `_R` alias when the field-name set matches one.
            let mut names: Vec<String> =
                fields.iter().map(|(n, _)| n.as_str().to_string()).collect();
            names.sort();
            if let Some(go_name) = env.record_fieldsets.get(&names) {
                return GoTy::Named(go_name.clone(), vec![]);
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
            let _ = ext;
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
                .map(|(n, ft)| (base::Name::new(&cap(n.as_str())), sky_ty_to_go_in(ft, env, cur_mod)))
                .collect();
            go_fields.sort_by(|a, b| a.0.as_str().cmp(b.0.as_str()));
            GoTy::Struct(go_fields)
        }
        Ty::Error => GoTy::Any,
    }
}

fn app_to_go(name: &str, args: &[Ty], env: &TypeEnv, cur_mod: Option<&str>) -> GoTy {
    let go = |t: &Ty| sky_ty_to_go_in(t, env, cur_mod);
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
                .and_then(|m| env.nominal_by_module.get(&(m.to_string(), bare.to_string())))
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
                // User nominal types are emitted NON-generic in the M4 runtime
                // representation: sealed ADT → `rt.SkyADT` bag (`Fields []any`),
                // iota enum → `int`, record alias → a plain (non-parametric)
                // struct whose type-var fields are erased to `any`. So a
                // parametric application like `ShouldRetry msg` / `Event msg`
                // renders as the bare Go name — appending `[msg]` would
                // reference a non-generic type with a type argument and fail
                // `go build`. The type parameters are erased; ADT payloads flow
                // as `any` (the generic-erase floor, doc 07 §6 class 8). The
                // arg types' own reachability is driven by the variant/field
                // use sites in `emit_type_decl`, so dropping them here is safe.
                let _ = go; // args intentionally erased
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
