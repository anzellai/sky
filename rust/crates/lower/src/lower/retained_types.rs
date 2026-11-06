//! Retained immutable lowering types; numeric and row decisions stay explicit.
use super::*;
use std::ops::Deref;
use std::sync::Arc;

#[derive(Clone)]
pub(super) enum SignatureTy {
    World { owner: Arc<ty::World>, def: DefId },
    Shared(Arc<Ty>),
}
impl Deref for SignatureTy {
    type Target = Ty;
    fn deref(&self) -> &Ty {
        match self {
            Self::World { owner, def } => &owner.value_sigs[def].ty,
            Self::Shared(ty) => ty,
        }
    }
}
pub(super) enum RetainedTy<'a> {
    Borrowed(&'a Ty),
    Shared(Arc<Ty>),
}
// Cloning a selected cache value copies only its immutable owner/reference.
impl Clone for RetainedTy<'_> {
    fn clone(&self) -> Self {
        match self {
            Self::Borrowed(t) => Self::Borrowed(*t),
            Self::Shared(t) => Self::Shared(Arc::clone(t)),
        }
    }
}
impl Deref for RetainedTy<'_> {
    type Target = Ty;
    fn deref(&self) -> &Ty {
        match self {
            Self::Borrowed(ty) => ty,
            Self::Shared(ty) => ty,
        }
    }
}
impl PartialEq for RetainedTy<'_> {
    fn eq(&self, other: &Self) -> bool {
        **self == **other
    }
}
impl Eq for RetainedTy<'_> {}
pub(super) type ParamTypes<'a> = HashMap<DefId, Vec<RetainedTy<'a>>>;
pub(super) type ResultTypes<'a> = HashMap<DefId, RetainedTy<'a>>;

pub(super) fn signature(
    owner: &Arc<ty::World>,
    def: DefId,
    shape: &ty::numeric::NumericShape,
) -> Option<SignatureTy> {
    owner.value_sigs.get(&def).map(|s| {
        if matches!(shape, ty::numeric::NumericShape::None) {
            SignatureTy::World {
                owner: Arc::clone(owner),
                def,
            }
        } else {
            SignatureTy::Shared(Arc::new(shape.apply(&s.ty)))
        }
    })
}

fn owned(ty: Ty) -> RetainedTy<'static> {
    RetainedTy::Shared(Arc::new(ty))
}

fn apply_numeric<'a>(
    shape: Option<&ty::numeric::NumericShape>,
    ty: RetainedTy<'a>,
) -> RetainedTy<'a> {
    match shape {
        None | Some(ty::numeric::NumericShape::None) => ty,
        Some(shape) => owned(shape.apply(&ty)),
    }
}

fn signature_parameters(sig: &Ty) -> Vec<&Ty> {
    let mut out = Vec::new();
    let mut current = sig;
    while let Ty::Fun(a, b) = current {
        out.push(&**a);
        current = b;
    }
    out
}

fn signature_result(sig: &Ty, count: usize) -> &Ty {
    let mut current = sig;
    for _ in 0..count {
        match current {
            Ty::Fun(_, b) => current = b,
            _ => break,
        }
    }
    current
}

#[cfg(test)]
fn owned_signature_parameters(sig: &Ty) -> Vec<Ty> {
    let mut out = Vec::new();
    let mut current = sig;
    while let Ty::Fun(a, b) = current {
        out.push((**a).clone());
        current = b;
    }
    out
}

pub(super) fn build_caches<'a>(
    defs: &'a BTreeMap<DefId, DefEntry>,
    env: &TypeEnv,
    record_fields: &HashMap<String, Vec<(String, Ty)>>,
) -> (ParamTypes<'a>, ResultTypes<'a>) {
    let mut def_param_tys: ParamTypes<'_> = HashMap::new();
    for (d, e) in defs {
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
        let sig_ptys = e.sig.as_deref().map(signature_parameters);
        // Row-polymorphic params (row var shared param↔result) must present to
        // callers as `any` — matching the `any` Go signature `lower_def` emits —
        // so the caller widens its argument instead of coercing it DOWN to a
        // closed struct (which would drop the row-carried fields).
        let collides = |t: &Ty| match &sky_ty_to_go(t, &env) {
            GoTy::Named(n, _) => goty_names_non_model_record(n, &record_fields, &env),
            _ => false,
        };
        let (rp_params, _rp_result) = row_poly_flags(&e.body, &e.types, &collides);
        let mut ptys = Vec::new();
        for (i, p) in e.body.params.iter().enumerate() {
            let inferred = || match &e.body.pats[*p] {
                Pattern::Var(lid) => e.types.local(*lid).unwrap_or(Ty::Var(Name::new("any"))),
                _ => Ty::Var(Name::new("any")),
            };
            // Take the sig param type when it is concrete enough to be useful
            // (not a bare type variable — a rigid `msg`/`a` gives no more than
            // the inferred type and would erase to `any` anyway).
            let t = match sig_ptys.as_ref().and_then(|ps| ps.get(i)) {
                Some(st) if !matches!(st, Ty::Var(_)) => RetainedTy::Borrowed(*st),
                // Row-poly + no concrete annotation → `any`.
                _ if *rp_params.get(i).unwrap_or(&false) => owned(Ty::Var(Name::new("any"))),
                _ => owned(inferred()),
            };
            // Keep the SECOND numeric application, including after a rewritten
            // signature; exact None is the only borrowed identity case.
            ptys.push(apply_numeric(e.numeric.signature.parameter(i), t));
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
    let mut def_result_tys: ResultTypes<'_> = HashMap::new();
    for (d, e) in defs {
        let sig_ret = e
            .sig
            .as_deref()
            .map(|s| signature_result(s, e.body.params.len()));
        let collides = |t: &Ty| match &sky_ty_to_go(t, &env) {
            GoTy::Named(n, _) => goty_names_non_model_record(n, &record_fields, &env),
            _ => false,
        };
        let (_rp_params, rp_result) = row_poly_flags(&e.body, &e.types, &collides);
        let t = match sig_ret {
            Some(st) if !matches!(st, Ty::Var(_)) => Some(RetainedTy::Borrowed(st)),
            // Row-poly result + no concrete annotation → `any` (matches the `any`
            // Go return `lower_def` emits + the reflective `rt.RecordUpdate` body).
            _ if rp_result => Some(owned(Ty::Var(Name::new("any")))),
            // A row-polymorphic lambda CAF presents its erased function type —
            // the same one `lower_def` emits (see `caf_lambda_row_poly_ty`).
            None => caf_lambda_row_poly_ty(&e.body, &e.types)
                .map(owned)
                .or_else(|| e.types.result.clone().map(owned)),
            _ => e.types.result.clone().map(owned),
        };
        if let Some(t) = t {
            let t = apply_numeric(e.numeric.signature.result_after(e.body.params.len()), t);
            def_result_tys.insert(*d, t);
        }
    }
    // A zero-parameter ALIAS of another def (`alias2 = (add2)`) whose own
    // per-def inference could not see the target's type (an unannotated target
    // reads back as a bare var) takes the TARGET's result type. `lower_def`
    // already emits the alias with the target's Go func type (recovered from the
    // lowered body), so a caller that read the bare var saw `any` and emitted a
    // curried call on it (`Main_alias2()(3)(4)`) that `go build` rejects while
    // `sky check` passed. Iterated so an alias of an alias resolves too.
    for _ in 0..8 {
        let mut changed = false;
        for (d, e) in defs {
            if !e.body.params.is_empty() || e.sig.is_some() {
                continue;
            }
            let Some(root) = e.body.root else { continue };
            let Expr::Var(Res::Def(target)) = &e.body.exprs[root] else {
                continue;
            };
            let own_unresolved = match def_result_tys.get(d).map(|t| &**t) {
                None => true,
                Some(Ty::Var(_)) | Some(Ty::Error) => true,
                Some(_) => false,
            };
            if !own_unresolved || target == d {
                continue;
            }
            if let Some(tt) = def_result_tys.get(target).cloned() {
                if !matches!(&*tt, Ty::Var(_) | Ty::Error) && def_result_tys.get(d) != Some(&tt) {
                    def_result_tys.insert(*d, tt);
                    changed = true;
                }
            }
        }
        if !changed {
            break;
        }
    }

    (def_param_tys, def_result_tys)
}

#[cfg(test)]
fn legacy_caches(
    defs: &BTreeMap<DefId, DefEntry>,
    env: &TypeEnv,
    record_fields: &HashMap<String, Vec<(String, Ty)>>,
) -> (HashMap<DefId, Vec<Ty>>, HashMap<DefId, Ty>) {
    let mut def_param_tys: HashMap<DefId, Vec<Ty>> = HashMap::new();
    for (d, e) in defs {
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
        let sig_ptys = e.sig.as_deref().map(owned_signature_parameters);
        // Row-polymorphic params (row var shared param↔result) must present to
        // callers as `any` — matching the `any` Go signature `lower_def` emits —
        // so the caller widens its argument instead of coercing it DOWN to a
        // closed struct (which would drop the row-carried fields).
        let collides = |t: &Ty| match &sky_ty_to_go(t, &env) {
            GoTy::Named(n, _) => goty_names_non_model_record(n, &record_fields, &env),
            _ => false,
        };
        let (rp_params, _rp_result) = row_poly_flags(&e.body, &e.types, &collides);
        let mut ptys = Vec::new();
        for (i, p) in e.body.params.iter().enumerate() {
            let inferred = || match &e.body.pats[*p] {
                Pattern::Var(lid) => e.types.local(*lid).unwrap_or(Ty::Var(Name::new("any"))),
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
            let t = e
                .numeric
                .signature
                .parameter(i)
                .map_or_else(|| t.clone(), |shape| shape.apply(&t));
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
    for (d, e) in defs {
        let sig_ret = e
            .sig
            .as_deref()
            .map(|s| sig_result_after(s, e.body.params.len()));
        let collides = |t: &Ty| match &sky_ty_to_go(t, &env) {
            GoTy::Named(n, _) => goty_names_non_model_record(n, &record_fields, &env),
            _ => false,
        };
        let (_rp_params, rp_result) = row_poly_flags(&e.body, &e.types, &collides);
        let t = match sig_ret {
            Some(st) if !matches!(st, Ty::Var(_)) => Some(st),
            // Row-poly result + no concrete annotation → `any` (matches the `any`
            // Go return `lower_def` emits + the reflective `rt.RecordUpdate` body).
            _ if rp_result => Some(Ty::Var(Name::new("any"))),
            // A row-polymorphic lambda CAF presents its erased function type —
            // the same one `lower_def` emits (see `caf_lambda_row_poly_ty`).
            None => caf_lambda_row_poly_ty(&e.body, &e.types).or_else(|| e.types.result.clone()),
            _ => e.types.result.clone(),
        };
        if let Some(t) = t {
            let t = e
                .numeric
                .signature
                .result_after(e.body.params.len())
                .map_or_else(|| t.clone(), |shape| shape.apply(&t));
            def_result_tys.insert(*d, t);
        }
    }
    // A zero-parameter ALIAS of another def (`alias2 = (add2)`) whose own
    // per-def inference could not see the target's type (an unannotated target
    // reads back as a bare var) takes the TARGET's result type. `lower_def`
    // already emits the alias with the target's Go func type (recovered from the
    // lowered body), so a caller that read the bare var saw `any` and emitted a
    // curried call on it (`Main_alias2()(3)(4)`) that `go build` rejects while
    // `sky check` passed. Iterated so an alias of an alias resolves too.
    for _ in 0..8 {
        let mut changed = false;
        for (d, e) in defs {
            if !e.body.params.is_empty() || e.sig.is_some() {
                continue;
            }
            let Some(root) = e.body.root else { continue };
            let Expr::Var(Res::Def(target)) = &e.body.exprs[root] else {
                continue;
            };
            let own_unresolved = match def_result_tys.get(d) {
                None => true,
                Some(Ty::Var(_)) | Some(Ty::Error) => true,
                Some(_) => false,
            };
            if !own_unresolved || target == d {
                continue;
            }
            if let Some(tt) = def_result_tys.get(target).cloned() {
                if !matches!(tt, Ty::Var(_) | Ty::Error) && def_result_tys.get(d) != Some(&tt) {
                    def_result_tys.insert(*d, tt);
                    changed = true;
                }
            }
        }
        if !changed {
            break;
        }
    }

    (def_param_tys, def_result_tys)
}

#[cfg(test)]
#[derive(Debug)]
pub(super) struct Snapshot {
    name: String,
    signature_identity: bool,
    signature_param_identity: Vec<bool>,
    signature_param_leaf_identity: Vec<bool>,
    cache_param_identity: Vec<bool>,
    cache_param_leaf_identity: Vec<bool>,
    cache_result_identity: bool,
    parameter_owned: Vec<bool>,
    result_owned: bool,
    numeric_rewrite: bool,
    signature_present: bool,
    source_parameter_widths: Vec<usize>,
    parameters: Vec<Ty>,
    result: Option<Ty>,
    initially_unresolved: bool,
    alias_target: Option<String>,
    alias_identity: bool,
}

#[cfg(test)]
fn refs(sig: &Ty) -> Vec<&Ty> {
    let mut out = Vec::new();
    let mut current = sig;
    while let Ty::Fun(a, b) = current {
        out.push(&**a);
        current = b;
    }
    out
}
#[cfg(test)]
fn after(sig: &Ty, n: usize) -> &Ty {
    let mut current = sig;
    for _ in 0..n {
        if let Ty::Fun(_, b) = current {
            current = b;
        } else {
            break;
        }
    }
    current
}
#[cfg(test)]
fn first_leaf(ty: &Ty) -> &Ty {
    match ty {
        Ty::Record(fields, _) if !fields.is_empty() => first_leaf(&fields[0].1),
        Ty::Fun(a, _) => first_leaf(a),
        Ty::Tuple(xs) | Ty::App(_, xs) if !xs.is_empty() => first_leaf(&xs[0]),
        _ => ty,
    }
}

/// Test-only, direct callback inspection of the ACTUAL production cache path.
/// No TLS, pointer mutation, private app fixture or alternate lowerer is used.
#[cfg(test)]
pub(super) fn inspect(
    typer: &Typer<'_>,
    defs: &BTreeMap<DefId, DefEntry>,
    params: &ParamTypes<'_>,
    results: &ResultTypes<'_>,
    env: &TypeEnv,
    fields: &HashMap<String, Vec<(String, Ty)>>,
) -> Vec<Snapshot> {
    let (old_params, old_results) = legacy_caches(defs, env, fields);
    assert_eq!(params.len(), old_params.len());
    assert_eq!(results.len(), old_results.len());
    let mut snapshots = Vec::new();
    for (d, e) in defs {
        let actual_params = &params[d];
        let expected_params = &old_params[d];
        assert_eq!(
            actual_params.len(),
            expected_params.len(),
            "parameter count {}",
            e.name
        );
        for (actual, expected) in actual_params.iter().zip(expected_params) {
            assert_eq!(&**actual, expected, "parameter value {}", e.name);
        }
        assert_eq!(
            results.get(d).map(|t| &**t),
            old_results.get(d),
            "result value {}",
            e.name
        );
        let world = typer.value_sig(*d).map(|s| &s.ty);
        let expected_signature = world.map(|t| e.numeric.signature.apply(t));
        assert_eq!(
            e.sig.as_deref(),
            expected_signature.as_ref(),
            "signature value {}",
            e.name
        );
        let source_params = world.map(refs).unwrap_or_default();
        let sig_params = e.sig.as_deref().map(refs).unwrap_or_default();
        let sig_result = e.sig.as_deref().map(|s| after(s, e.body.params.len()));
        let alias = match e.body.root.map(|r| &e.body.exprs[r]) {
            Some(Expr::Var(Res::Def(target))) => Some(*target),
            _ => None,
        };
        snapshots.push(Snapshot {
            name:e.name.clone(),
            signature_identity:matches!((e.sig.as_deref(),world),(Some(a),Some(b)) if std::ptr::eq(a,b)),
            signature_param_identity:sig_params.iter().zip(&source_params).map(|(a,b)|std::ptr::eq(*a,*b)).collect(),
            signature_param_leaf_identity:sig_params.iter().zip(&source_params).map(|(a,b)|std::ptr::eq(first_leaf(a),first_leaf(b))).collect(),
            cache_param_identity:actual_params.iter().zip(&sig_params).map(|(a,b)|std::ptr::eq(&**a,*b)).collect(),
            cache_param_leaf_identity:actual_params.iter().zip(&sig_params).map(|(a,b)|std::ptr::eq(first_leaf(a),first_leaf(b))).collect(),
            cache_result_identity:matches!((results.get(d),sig_result),(Some(a),Some(b)) if std::ptr::eq(&**a,b)),
            parameter_owned:actual_params.iter().map(|t|matches!(t,RetainedTy::Shared(_))).collect(),
            result_owned:results.get(d).is_some_and(|t|matches!(t,RetainedTy::Shared(_))),
            numeric_rewrite:!matches!(e.numeric.signature,ty::numeric::NumericShape::None),
            signature_present:e.sig.is_some(),
            source_parameter_widths:source_params.iter().map(|t|match t {Ty::Record(xs,None)=>xs.len(),_=>0}).collect(),
            parameters:actual_params.iter().map(|t|(**t).clone()).collect(),
            result:results.get(d).map(|t|(**t).clone()),
            initially_unresolved:matches!(&e.types.result,None|Some(Ty::Var(_))|Some(Ty::Error)),
            alias_target:alias.and_then(|t|defs.get(&t).map(|e|e.name.clone())),
            alias_identity:alias.is_some_and(|t|matches!((results.get(d),results.get(&t)),(Some(a),Some(b)) if std::ptr::eq(&**a,&**b))),
        });
    }
    snapshots
}

#[cfg(test)]
mod tests {
    use super::*;
    use base::FileId;
    use hir::SourceDb;

    const WIDTH: usize = 96;
    fn source() -> String {
        let fields = (0..WIDTH)
            .map(|i| format!("f{i} : Int"))
            .collect::<Vec<_>>()
            .join(", ");
        let values = (0..WIDTH)
            .map(|i| format!("f{i} = {i}"))
            .collect::<Vec<_>>()
            .join(", ");
        format!("module Main exposing (..)\ntype alias Model = {{ {fields} }}\nretainModel : Model -> Model\nretainModel m = m\nblank : Model\nblank = {{ {values} }}\nunannotatedModel = {{ {values} }}\nmodelAlias = unannotatedModel\naliasAgain = modelAlias\nnumericKeep : number -> number\nnumericKeep x = x + 1\ninferredPlus x = x + 1\nrowModify r = {{ r | f0 = 1 }}\nrowAnnotated : {{ r | f0 : Int }} -> {{ r | f0 : Int }}\nrowAnnotated r = {{ r | f0 = 2 }}\nrowCaf = \\r -> {{ r | f0 = 3 }}\nmain = {{ model = retainModel aliasAgain, blank = blank, number = numericKeep 1, inferred = inferredPlus 2, rowFn = rowModify, taggedFn = rowAnnotated, cafFn = rowCaf }}\n")
    }
    fn capture() -> Vec<Snapshot> {
        let src = source();
        let parsed = syntax::parse(&src, FileId(0));
        assert!(
            parsed.errors().is_empty() && parsed.error_node_count() == 0,
            "public fixture parses"
        );
        let mut db = SourceDb::new();
        let module = db.add_module("Main", parsed);
        let checked = ty::check_modules(&db, &[module]);
        assert_eq!(
            (checked.type_errors, checked.name_errors),
            (0, 0),
            "fixture must really typecheck: {:?}",
            checked.diagnostics
        );
        let mut observations = None;
        let mut observer = |rows| observations = Some(rows);
        let out = super::super::lower_program_impl(
            &db,
            module,
            &LowerConfig::default(),
            Some(&mut observer),
        );
        assert!(
            out.entry_ok && out.errors.is_empty(),
            "fixture really lowers: {:?}",
            out.errors
        );
        observations.expect("actual production cache path inspected")
    }
    fn named<'a>(rows: &'a [Snapshot], name: &str) -> &'a Snapshot {
        rows.iter().find(|r| r.name == name).expect(name)
    }
    #[test]
    fn wide_model_signature_subtrees_and_caches_share_authoritative_allocations() {
        let rows = capture();
        let keep = named(&rows, "retainModel");
        assert_eq!(
            keep.source_parameter_widths,
            [WIDTH],
            "wide Model premise must hold"
        );
        assert!(!keep.numeric_rewrite);
        assert_eq!(keep.parameters.len(), 1);
        assert!(matches!(&keep.parameters[0],Ty::Record(fs,None) if fs.len()==WIDTH));
        assert!(matches!(&keep.result,Some(Ty::Record(fs,None)) if fs.len()==WIDTH));
        assert!(
            keep.signature_identity,
            "None numeric shape copied authoritative signature"
        );
        assert_eq!(keep.signature_param_identity, [true]);
        assert_eq!(keep.signature_param_leaf_identity, [true]);
        assert_eq!(keep.cache_param_identity, [true]);
        assert_eq!(keep.cache_param_leaf_identity, [true]);
        assert!(keep.cache_result_identity);
    }
    #[test]
    fn alias_fixpoint_shares_selected_result_without_borrowing_mutable_map_entries() {
        let rows = capture();
        let origin = named(&rows, "unannotatedModel");
        assert!(
            !origin.signature_present && origin.result_owned,
            "alias source must be owned inferred storage"
        );
        for (name, target) in [
            ("modelAlias", "unannotatedModel"),
            ("aliasAgain", "modelAlias"),
        ] {
            let row = named(&rows, name);
            assert!(!row.signature_present, "unannotated alias premise");
            assert!(
                row.initially_unresolved,
                "alias must actually need the existing fixpoint"
            );
            assert_eq!(row.alias_target.as_deref(), Some(target));
            assert!(matches!(&row.result,Some(Ty::Record(fs,None)) if fs.len()==WIDTH));
            assert!(
                row.alias_identity,
                "alias cloned a complete selected result"
            );
        }
    }
    #[test]
    fn numeric_inferred_row_and_caf_decisions_match_complete_owned_oracle() {
        let rows = capture();
        let numeric = named(&rows, "numericKeep");
        assert!(numeric.numeric_rewrite && numeric.signature_present);
        assert!(!numeric.signature_identity);
        assert_eq!(numeric.parameter_owned, [true]);
        assert!(numeric.result_owned);
        assert!(matches!(&numeric.parameters[0],Ty::Var(n) if n.as_str()=="__sky_numeric"));
        assert!(matches!(&numeric.result,Some(Ty::Var(n)) if n.as_str()=="__sky_numeric"));
        let inferred = named(&rows, "inferredPlus");
        assert!(!inferred.signature_present);
        assert_eq!(inferred.parameter_owned, [true]);
        assert!(inferred.result_owned);
        let row = named(&rows, "rowModify");
        assert!(!row.signature_present);
        assert!(matches!(&row.parameters[0],Ty::Var(n) if n.as_str()=="any"));
        assert!(matches!(&row.result,Some(Ty::Var(n)) if n.as_str()=="any"));
        assert!(row.result_owned && row.parameter_owned == [true]);
        let annotated = named(&rows, "rowAnnotated");
        assert!(annotated.signature_present);
        assert!(matches!(&annotated.parameters[0], Ty::Record(_, Some(_))));
        assert!(matches!(&annotated.result, Some(Ty::Record(_, Some(_)))));
        let caf = named(&rows, "rowCaf");
        assert!(!caf.signature_present && caf.result_owned);
        assert!(
            matches!(&caf.result,Some(Ty::Fun(a,b)) if matches!(&**a,Ty::Var(n) if n.as_str()=="any") && matches!(&**b,Ty::Var(n) if n.as_str()=="any"))
        );
    }

    #[test]
    fn alias_progress_equality_ignores_ownership_variants_and_distinct_owners() {
        let value = Ty::Record(
            vec![(
                Name::new("nested"),
                Ty::Tuple(vec![Ty::app("Int", vec![]), Ty::app("String", vec![])]),
            )],
            None,
        );
        let borrowed = RetainedTy::Borrowed(&value);
        let first = RetainedTy::Shared(Arc::new(value.clone()));
        let second = RetainedTy::Shared(Arc::new(value.clone()));
        assert!(!std::ptr::eq(&*borrowed, &*first));
        assert!(!std::ptr::eq(&*first, &*second));
        assert!(
            borrowed == first && first == second && second == borrowed,
            "alias progress must use structural Ty equality across owners"
        );
        let mut selected = HashMap::new();
        selected.insert(DefId(0), borrowed);
        let target = second;
        assert!(
            selected.get(&DefId(0)) == Some(&target),
            "equal target must not appear to make fixpoint progress"
        );
        let different = RetainedTy::Shared(Arc::new(Ty::var("different")));
        assert!(selected.get(&DefId(0)) != Some(&different));
    }
}
