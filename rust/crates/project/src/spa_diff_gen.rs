//! `spa_diff_gen` — the type-directed VALUE-GENERATOR emitter for the Sky.Spa
//! differential split fuzzer (design: `docs/design/auto-testing.md`, pillar 1).
//!
//! ## What this module is (phase 1)
//! It EMITS Sky source: deterministic generators `gen<T> : Seed -> (T, Seed)`
//! derived from the compiler's own `ty::Ty`, plus a `genModel` for the app's
//! Model record and a `genMsg`/`genMsg_<Ctor>` for its Msg constructors. It runs
//! NOTHING and reads NO app source — it turns a type into Sky text a later phase
//! compiles into the fuzzer harness. It is the `ty::Ty`-retargeted sibling of
//! `xtask::welltyped_gate`'s type-directed builder: same STRUCTURE (per-type
//! emitters + a seeded SplitMix-style PRNG), but it emits generator FUNCTIONS
//! that thread a `Seed` and build values with the app's REAL constructors
//! (type-checked), rather than pretty-printing random literal expressions.
//!
//! ## Why generators, not literals
//! The differential fuzzer (phase 2) drives the app's real `update` two ways —
//! directly and through the Sky.Spa split — over the SAME random reachable
//! `(Model, Msg)`. To be reachable and well-typed the values must be built by
//! the app's own types, in-process, from a reproducible seed. So the harness
//! calls these generators at Sky runtime; here we only write them.
//!
//! ## Soundness discipline
//! * **Never guess a shape.** A field / arg whose `ty` the typer could not
//!   recover (`None`), or whose type resolves to something we cannot inhabit
//!   (an opaque FFI type, a bare type variable, `Error`, a generic user type),
//!   is SKIPPED with a loud note — never approximated. If a Model field is
//!   unrecoverable the whole `genModel` is withheld (a partial Model would be a
//!   guess).
//! * **Termination is by construction.** Every composite generator carries a
//!   depth budget (`spaDec`-decremented). A recursive ADT past the cap builds
//!   its simplest inhabiting constructor; a recursive type with NO finite
//!   inhabitant is refused with a note. This is the "depth-cap recursive ADTs"
//!   the design names.
//! * **Deterministic.** A fixed LCG step (`spaSeedNext`) threads `Seed`
//!   left-to-right, so a seed reproduces a value byte-for-byte — the property
//!   the differential run relies on.

use crate::spa_partition::ModelFieldTy;
use std::collections::{BTreeSet, HashMap, HashSet};

/// A user type the generator can build a value of, as resolved from the typed
/// HIR. Phase 2 backs [`TypeResolver`] with `hir::exports::ExportedUnion` +
/// the typer; the unit tests back it with a synthetic map.
#[derive(Clone, Debug)]
pub enum TypeDef {
    /// A record type (a `type alias N = { … }`): ordered `(field, type)`.
    Record(Vec<(String, ty::Ty)>),
    /// A union type (`type N = A | B x | …`): ordered ctors, each with its arg
    /// types (from the typer — `ExportedCtor` carries arity, not arg types).
    Union(Vec<(String, Vec<ty::Ty>)>),
}

/// Resolve a nominal type NAME (tail segment, e.g. `Todo`) to its definition, or
/// `None` when it is opaque / not a user record-or-union the generator can build.
pub trait TypeResolver {
    fn resolve(&self, name: &str) -> Option<TypeDef>;
}

// The depth budget a top-level generator starts with lives Sky-side as the
// emitted `spaGenCap` constant (see `PRELUDE`): small, so value size is bounded,
// and `spaDec` decrements it on every recursive descent.

/// The tail segment of a possibly home-folded nominal name (`Sky.Core.Basics.Int`
/// → `Int`).
fn tail(name: &str) -> &str {
    name.rsplit('.').next().unwrap_or(name)
}

fn is_scalar_tail(t: &str) -> bool {
    matches!(t, "Int" | "Float" | "String" | "Bool")
}

/// The scalar generator name for a scalar tail (`Int` → `spaGenInt`).
fn scalar_gen(t: &str) -> Option<&'static str> {
    match t {
        "Int" => Some("spaGenInt"),
        "Float" => Some("spaGenFloat"),
        "String" => Some("spaGenString"),
        "Bool" => Some("spaGenBool"),
        _ => None,
    }
}

/// A Sky-identifier-safe fragment for a type, used to name its generator
/// (`List Int` → `List_Int`, `Maybe Todo` → `Maybe_Todo`).
fn mangle(t: &ty::Ty) -> String {
    match t {
        ty::Ty::App(name, args) => {
            let tl = tail(name.as_str());
            if args.is_empty() {
                tl.to_string()
            } else {
                let inner: Vec<String> = args.iter().map(mangle).collect();
                format!("{tl}_{}", inner.join("_"))
            }
        }
        ty::Ty::Tuple(xs) => {
            let inner: Vec<String> = xs.iter().map(mangle).collect();
            format!("Tup{}_{}", xs.len(), inner.join("_"))
        }
        ty::Ty::Record(fs, _) => {
            let inner: Vec<String> =
                fs.iter().map(|(n, t)| format!("{}_{}", n.as_str(), mangle(t))).collect();
            format!("Rec_{}", inner.join("_"))
        }
        ty::Ty::Unit => "Unit".to_string(),
        ty::Ty::Var(n) => format!("Var_{}", n.as_str()),
        ty::Ty::Fun(_, _) => "Fun".to_string(),
        ty::Ty::Error => "Error".to_string(),
    }
}

/// Render a type as Sky surface syntax for a generator's annotation
/// (`List Int`, `Maybe Todo`, `Todo`). `None` for a shape we cannot spell.
fn sky_ty(t: &ty::Ty) -> Option<String> {
    match t {
        ty::Ty::App(name, args) => {
            let tl = tail(name.as_str());
            if args.is_empty() {
                Some(tl.to_string())
            } else {
                let inner: Vec<String> =
                    args.iter().map(sky_ty_arg).collect::<Option<_>>()?;
                Some(format!("{tl} {}", inner.join(" ")))
            }
        }
        ty::Ty::Tuple(xs) => {
            let inner: Vec<String> = xs.iter().map(sky_ty).collect::<Option<_>>()?;
            Some(format!("( {} )", inner.join(", ")))
        }
        ty::Ty::Record(fs, _) => {
            let inner: Vec<String> = fs
                .iter()
                .map(|(n, t)| sky_ty(t).map(|s| format!("{} : {s}", n.as_str())))
                .collect::<Option<_>>()?;
            Some(format!("{{ {} }}", inner.join(", ")))
        }
        ty::Ty::Unit => Some("()".to_string()),
        ty::Ty::Var(_) | ty::Ty::Fun(_, _) | ty::Ty::Error => None,
    }
}

/// Parenthesise a rendered type in ARGUMENT position (`List Int` → `(List Int)`).
fn sky_ty_arg(t: &ty::Ty) -> Option<String> {
    let s = sky_ty(t)?;
    let multi = matches!(t, ty::Ty::App(_, a) if !a.is_empty());
    if multi {
        Some(format!("({s})"))
    } else {
        Some(s)
    }
}

/// The output of a generation run.
#[derive(Debug, Default)]
pub struct GenOutput {
    /// The complete emitted Sky source: the `Seed` prelude + every generator +
    /// `genModel` + `genMsg`.
    pub source: String,
    /// Loud skips — a field / arg / type the generator refused to spell.
    pub notes: Vec<String>,
    /// Whether `genModel` was emitted (false when a Model field was unrecoverable).
    pub emitted_model: bool,
    /// The Msg ctor names for which a `genMsg_<Ctor>` was emitted.
    pub emitted_msgs: Vec<String>,
}

/// The generator emitter. Accumulates per-type generator defs (deduped) plus the
/// `genModel` / `genMsg` entry points, then renders them with the shared prelude.
pub struct GenModule<'a> {
    resolver: &'a dyn TypeResolver,
    /// Mangles whose generator def is already buffered (dedup).
    emitted: BTreeSet<String>,
    /// Generator defs, in dependency order (a type is pushed after its deps).
    defs: Vec<String>,
    notes: Vec<String>,
    msg_type: Option<String>,
    emitted_msgs: Vec<String>,
    emitted_model: bool,
    /// Memo of nominal names proven to have a finite value (monotone true-only).
    finite_true: HashMap<String, bool>,
}

impl<'a> GenModule<'a> {
    pub fn new(resolver: &'a dyn TypeResolver) -> Self {
        GenModule {
            resolver,
            emitted: BTreeSet::new(),
            defs: Vec::new(),
            notes: Vec::new(),
            msg_type: None,
            emitted_msgs: Vec::new(),
            emitted_model: false,
            finite_true: HashMap::new(),
        }
    }

    // ---- finiteness (termination guard) ----------------------------------

    /// Does a value of `ty` have a finite inhabitant the generator can build?
    /// A least-fixpoint over the resolver's type graph: monotone in the set of
    /// nominal names proven finite, so a recursive union with a nullary ctor
    /// converges to `true` and a base-case-free product converges to `false`.
    fn finite(&mut self, ty: &ty::Ty) -> bool {
        loop {
            let before = self.finite_true.len();
            let mut visiting: HashSet<String> = HashSet::new();
            let r = self.finite_rec(ty, &mut visiting);
            // Re-run while the pass discovered new finite nominal names (they may
            // unlock a `true` for `ty` on the next pass); stop once stable.
            if self.finite_true.len() == before {
                return r;
            }
        }
    }

    fn finite_rec(&mut self, ty: &ty::Ty, visiting: &mut HashSet<String>) -> bool {
        match ty {
            ty::Ty::App(name, args) => {
                let tl = tail(name.as_str());
                match (tl, args.len()) {
                    (s, 0) if is_scalar_tail(s) => true,
                    // A List / Maybe always has a finite value ([] / Nothing),
                    // even when the element type is not itself generatable.
                    ("List", 1) | ("Maybe", 1) => true,
                    ("Result", 2) => {
                        // Ok a OR Err e — at least one side must be buildable.
                        let a_ok = self.finite_rec(&args[1], visiting);
                        let e_ok = self.finite_rec(&args[0], visiting);
                        a_ok || e_ok
                    }
                    (_, 0) => self.finite_nominal(tl, visiting),
                    // A generic user type applied to args (e.g. `Store Todo`) is
                    // out of scope for the value generator.
                    _ => false,
                }
            }
            ty::Ty::Tuple(xs) => xs.iter().all(|t| {
                // clone-free borrow dance: evaluate each independently.
                self.finite_rec(t, visiting)
            }),
            ty::Ty::Record(fs, _) => fs.clone().iter().all(|(_, t)| self.finite_rec(t, visiting)),
            ty::Ty::Unit => true,
            ty::Ty::Var(_) | ty::Ty::Fun(_, _) | ty::Ty::Error => false,
        }
    }

    fn finite_nominal(&mut self, name: &str, visiting: &mut HashSet<String>) -> bool {
        if self.finite_true.get(name).copied().unwrap_or(false) {
            return true;
        }
        if visiting.contains(name) {
            // Cycle within this pass — conservatively not-yet-finite; a later
            // pass resolves it once a base case is proven.
            return false;
        }
        let Some(def) = self.resolver.resolve(name) else {
            return false; // opaque / unknown
        };
        visiting.insert(name.to_string());
        let res = match def {
            TypeDef::Record(fs) => fs.iter().all(|(_, t)| self.finite_rec(t, visiting)),
            // A union is finite iff SOME ctor's args are all finite (a base case).
            TypeDef::Union(ctors) => ctors
                .iter()
                .any(|(_, args)| args.iter().all(|t| self.finite_rec(t, visiting))),
        };
        visiting.remove(name);
        if res {
            self.finite_true.insert(name.to_string(), true);
        }
        res
    }

    // ---- generator emission ----------------------------------------------

    /// Ensure a generator for `ty` exists (emitting it + its deps), returning the
    /// Sky EXPRESSION that generates a value of `ty` from a seed variable — a
    /// `Seed -> (T, Seed)` function name. `Err(note)` when `ty` is not buildable.
    fn ensure(&mut self, ty: &ty::Ty) -> Result<String, String> {
        // Scalars: the prelude carries them; no per-type def.
        if let ty::Ty::App(name, args) = ty {
            let tl = tail(name.as_str());
            if args.is_empty() {
                if let Some(s) = scalar_gen(tl) {
                    return Ok(s.to_string());
                }
            }
        }
        if let ty::Ty::Unit = ty {
            return Ok("spaGenUnit".to_string());
        }
        if !self.finite(ty) {
            return Err(format!(
                "type `{}` has no finite value the generator can build (opaque, a bare type variable, a base-case-free recursive type, or an unresolved shape) — skipped, not guessed",
                sky_ty(ty).unwrap_or_else(|| mangle(ty))
            ));
        }
        let m = mangle(ty);
        let wrapper = format!("spaGen_{m}");
        if self.emitted.contains(&m) {
            return Ok(wrapper);
        }
        // Reserve the name BEFORE recursing so a recursive type refers to its own
        // (still-being-built) worker without re-entering emission.
        self.emitted.insert(m.clone());
        match self.emit_worker(ty, &m) {
            Ok(()) => Ok(wrapper),
            Err(e) => {
                self.emitted.remove(&m);
                Err(e)
            }
        }
    }

    /// Emit `spaGenD_<m> : Int -> Seed -> (T, Seed)` (depth worker) and
    /// `spaGen_<m> : Seed -> (T, Seed)` (public wrapper).
    fn emit_worker(&mut self, ty: &ty::Ty, m: &str) -> Result<(), String> {
        let sky = sky_ty(ty)
            .ok_or_else(|| format!("cannot render Sky type for `{m}`"))?;
        let worker = format!("spaGenD_{m}");
        let body = self.worker_body(ty)?; // uses `d` and `s0`; returns `( value, sN )`
        let mut def = String::new();
        def.push_str(&format!("{worker} : Int -> Seed -> ( {sky}, Seed )\n"));
        def.push_str(&format!("{worker} d s0 =\n{body}\n\n"));
        def.push_str(&format!("spaGen_{m} : Seed -> ( {sky}, Seed )\n"));
        def.push_str(&format!("spaGen_{m} s =\n    spaGenD_{m} spaGenCap s\n\n\n"));
        self.defs.push(def);
        Ok(())
    }

    /// The worker BODY for `ty`, at 4-space indent, using bound `d` and `s0`.
    fn worker_body(&mut self, ty: &ty::Ty) -> Result<String, String> {
        match ty {
            ty::Ty::App(name, args) => {
                let tl = tail(name.as_str());
                match (tl, args.len()) {
                    ("List", 1) => self.list_body(&args[0]),
                    ("Maybe", 1) => self.maybe_body(&args[0]),
                    ("Result", 2) => self.result_body(&args[0], &args[1]),
                    (_, 0) => {
                        // A nominal user record / union.
                        match self.resolver.resolve(tl) {
                            Some(TypeDef::Record(fs)) => self.record_body(&fs),
                            Some(TypeDef::Union(ctors)) => self.union_body(&ctors),
                            None => Err(format!("nominal type `{tl}` did not resolve")),
                        }
                    }
                    _ => Err(format!("unsupported nominal type `{tl}`")),
                }
            }
            ty::Ty::Tuple(xs) => self.tuple_body(xs),
            ty::Ty::Record(fs, _) => {
                let fs: Vec<(String, ty::Ty)> =
                    fs.iter().map(|(n, t)| (n.as_str().to_string(), t.clone())).collect();
                self.record_body(&fs)
            }
            _ => Err(format!("no worker body for `{}`", mangle(ty))),
        }
    }

    /// A generator CALL producing `( <val>, <next_seed> )` from `seed`, at child
    /// depth `spaDec d`. Emits the child type's generator as a side effect.
    fn child_call(&mut self, ty: &ty::Ty, seed: &str) -> Result<String, String> {
        // Scalars + Unit: seed-only, depth irrelevant.
        if let ty::Ty::App(name, args) = ty {
            let tl = tail(name.as_str());
            if args.is_empty() {
                if let Some(s) = scalar_gen(tl) {
                    return Ok(format!("{s} {seed}"));
                }
            }
        }
        if let ty::Ty::Unit = ty {
            return Ok(format!("spaGenUnit {seed}"));
        }
        // Composite: thread the decremented depth into its worker.
        self.ensure(ty)?; // ensure the def exists (dedup by mangle)
        Ok(format!("spaGenD_{} (spaDec d) {seed}", mangle(ty)))
    }

    /// Bind a SEQUENCE of child generators threading the seed, returning the bind
    /// lines (`( v0, s1 ) = …`) + the value var names + the final seed var. The
    /// seed starts at `start_seed`; value vars are `<prefix>0, <prefix>1, …`.
    fn seq(
        &mut self,
        tys: &[ty::Ty],
        prefix: &str,
        start_seed: &str,
    ) -> Result<(Vec<String>, Vec<String>, String), String> {
        let mut binds = Vec::new();
        let mut vals = Vec::new();
        let mut seed = start_seed.to_string();
        for (i, t) in tys.iter().enumerate() {
            let val = format!("{prefix}{i}");
            let next = format!("{prefix}s{}", i + 1);
            let call = self.child_call(t, &seed)?;
            binds.push(format!("( {val}, {next} ) = {call}"));
            vals.push(val);
            seed = next;
        }
        Ok((binds, vals, seed))
    }

    fn list_body(&mut self, elem: &ty::Ty) -> Result<String, String> {
        if !self.finite(elem) {
            // Element not buildable → the list is always empty (a valid value,
            // not a guess), noted so a reader knows coverage is degenerate.
            self.notes.push(format!(
                "list element `{}` not buildable — generator emits `[]` only",
                sky_ty(elem).unwrap_or_else(|| mangle(elem))
            ));
            return Ok("    ( [], s0 )".to_string());
        }
        let one = self.child_call(elem, "s1")?;
        let two_a = self.child_call(elem, "s1")?;
        let two_b = self.child_call(elem, "s2")?;
        Ok(format!(
            "    if d <= 0 then\n\
             \x20       ( [], s0 )\n\
             \x20   else\n\
             \x20       let\n\
             \x20           ( pick, s1 ) = spaGenInt s0\n\
             \x20       in\n\
             \x20       case modBy 3 pick of\n\
             \x20           0 ->\n\
             \x20               ( [], s1 )\n\n\
             \x20           1 ->\n\
             \x20               let ( x0, s2 ) = {one} in ( [ x0 ], s2 )\n\n\
             \x20           _ ->\n\
             \x20               let ( x0, s2 ) = {two_a} in let ( x1, s3 ) = {two_b} in ( [ x0, x1 ], s3 )"
        ))
    }

    fn maybe_body(&mut self, inner: &ty::Ty) -> Result<String, String> {
        if !self.finite(inner) {
            self.notes.push(format!(
                "Maybe inner `{}` not buildable — generator emits `Nothing` only",
                sky_ty(inner).unwrap_or_else(|| mangle(inner))
            ));
            return Ok("    ( Nothing, s0 )".to_string());
        }
        let just = self.child_call(inner, "s1")?;
        Ok(format!(
            "    if d <= 0 then\n\
             \x20       ( Nothing, s0 )\n\
             \x20   else\n\
             \x20       let\n\
             \x20           ( pick, s1 ) = spaGenInt s0\n\
             \x20       in\n\
             \x20       if modBy 2 pick == 0 then\n\
             \x20           ( Nothing, s1 )\n\n\
             \x20       else\n\
             \x20           let ( x, s2 ) = {just} in ( Just x, s2 )"
        ))
    }

    fn result_body(&mut self, err: &ty::Ty, ok: &ty::Ty) -> Result<String, String> {
        let ok_ok = self.finite(ok);
        let err_ok = self.finite(err);
        if ok_ok && err_ok {
            let ok_call = self.child_call(ok, "s1")?;
            let err_call = self.child_call(err, "s1")?;
            Ok(format!(
                "    let\n\
                 \x20       ( pick, s1 ) = spaGenInt s0\n\
                 \x20   in\n\
                 \x20   if modBy 2 pick == 0 then\n\
                 \x20       let ( a, s2 ) = {ok_call} in ( Ok a, s2 )\n\n\
                 \x20   else\n\
                 \x20       let ( e, s2 ) = {err_call} in ( Err e, s2 )"
            ))
        } else if ok_ok {
            let ok_call = self.child_call(ok, "s0")?;
            Ok(format!("    let ( a, s1 ) = {ok_call} in ( Ok a, s1 )"))
        } else if err_ok {
            let err_call = self.child_call(err, "s0")?;
            Ok(format!("    let ( e, s1 ) = {err_call} in ( Err e, s1 )"))
        } else {
            Err("Result has neither a buildable Ok nor Err side".to_string())
        }
    }

    fn tuple_body(&mut self, xs: &[ty::Ty]) -> Result<String, String> {
        let (binds, vals, last) = self.seq(xs, "t", "s0")?;
        Ok(self.let_result(&binds, &format!("( {} )", vals.join(", ")), &last))
    }

    fn record_body(&mut self, fs: &[(String, ty::Ty)]) -> Result<String, String> {
        let tys: Vec<ty::Ty> = fs.iter().map(|(_, t)| t.clone()).collect();
        let (binds, vals, last) = self.seq(&tys, "f", "s0")?;
        let sets: Vec<String> = fs
            .iter()
            .zip(&vals)
            .map(|((n, _), v)| format!("{n} = {v}"))
            .collect();
        let value = format!("{{ {} }}", sets.join(", "));
        Ok(self.let_result(&binds, &value, &last))
    }

    fn union_body(&mut self, ctors: &[(String, Vec<ty::Ty>)]) -> Result<String, String> {
        // The base ctor for the depth-0 / cap branch: all-args-finite, fewest args.
        let mut base: Option<&(String, Vec<ty::Ty>)> = None;
        for c in ctors {
            if c.1.iter().all(|t| self.finite(t)) {
                match base {
                    Some(b) if b.1.len() <= c.1.len() => {}
                    _ => base = Some(c),
                }
            }
        }
        let base = base
            .ok_or_else(|| "union has no all-finite-args ctor (no base case)".to_string())?
            .clone();
        let base_expr = self.ctor_expr(&base.0, &base.1, "s0")?;

        // The full case over all ctors (depth > 0).
        let mut arms = String::new();
        let k = ctors.len();
        for (i, (cn, args)) in ctors.iter().enumerate() {
            let sel = if i + 1 == k {
                "_".to_string()
            } else {
                i.to_string()
            };
            let arm = self.ctor_expr(cn, args, "s1")?;
            arms.push_str(&format!("            {sel} ->\n                {arm}\n\n"));
        }
        let arms = arms.trim_end();
        Ok(format!(
            "    if d <= 0 then\n\
             \x20       {base_expr}\n\
             \x20   else\n\
             \x20       let\n\
             \x20           ( pick, s1 ) = spaGenInt s0\n\
             \x20       in\n\
             \x20       case modBy {k} pick of\n\
             {arms}"
        ))
    }

    /// A single ctor application expression producing `( <Ctor> args…, sN )` from
    /// `start_seed`.
    fn ctor_expr(
        &mut self,
        ctor: &str,
        args: &[ty::Ty],
        start_seed: &str,
    ) -> Result<String, String> {
        if args.is_empty() {
            return Ok(format!("( {ctor}, {start_seed} )"));
        }
        let (binds, vals, last) = self.seq(args, "c", start_seed)?;
        // Chained inline lets keep the arm a single logical expression.
        let mut expr = format!("( {} {}, {last} )", ctor, vals.join(" "));
        for b in binds.iter().rev() {
            expr = format!("let {b} in {expr}");
        }
        Ok(expr)
    }

    /// Wrap a sequence of single-line binds around a result value at 4-space
    /// body indent. Empty binds → just `( <value>, <seed> )`.
    fn let_result(&self, binds: &[String], value: &str, last_seed: &str) -> String {
        if binds.is_empty() {
            return format!("    ( {value}, {last_seed} )");
        }
        let mut out = String::from("    let\n");
        for b in binds {
            out.push_str(&format!("        {b}\n"));
        }
        out.push_str("    in\n");
        out.push_str(&format!("    ( {value}, {last_seed} )"));
        out
    }

    // ---- public entry points ---------------------------------------------

    /// Emit `genModel : Seed -> (<model_type>, Seed)` from the Model's fields.
    /// Returns `false` (and appends a note) when any field's type is unrecoverable
    /// — a partial Model would be a guessed shape, so none is emitted.
    pub fn emit_model(&mut self, model_type: &str, fields: &[ModelFieldTy]) -> bool {
        let mut binds = Vec::new();
        let mut sets = Vec::new();
        let mut seed = "s0".to_string();
        for (i, f) in fields.iter().enumerate() {
            let Some(ty) = &f.ty else {
                self.notes.push(format!(
                    "Model field `{}` has no recoverable type (`ty` = None) — genModel withheld",
                    f.name
                ));
                return false;
            };
            let next = format!("s{}", i + 1);
            match self.child_call_top(ty, &seed) {
                Ok(call) => {
                    binds.push(format!("( {}, {next} ) = {call}", f.name));
                    sets.push(format!("{0} = {0}", f.name));
                    seed = next;
                }
                Err(e) => {
                    self.notes
                        .push(format!("Model field `{}`: {e} — genModel withheld", f.name));
                    return false;
                }
            }
        }
        let value = format!("{{ {} }}", sets.join(", "));
        let body = self.let_result(&binds, &value, &seed);
        let mut def = String::new();
        def.push_str(&format!("genModel : Seed -> ( {model_type}, Seed )\n"));
        def.push_str(&format!("genModel s0 =\n{body}\n\n\n"));
        self.defs.push(def);
        self.emitted_model = true;
        true
    }

    /// Emit `genMsg_<Ctor> : Seed -> (<msg_type>, Seed)` for one Msg constructor,
    /// its args typed by `arg_tys` (from the branch's `msg_arg_tys`). Returns
    /// `false` (and notes) when an arg type is unrecoverable.
    pub fn emit_msg(&mut self, msg_type: &str, ctor: &str, arg_tys: &[ModelFieldTy]) -> bool {
        self.msg_type = Some(msg_type.to_string());
        let mut arg_ty_list: Vec<ty::Ty> = Vec::new();
        for a in arg_tys {
            let Some(ty) = &a.ty else {
                self.notes.push(format!(
                    "Msg `{ctor}` arg `{}` has no recoverable type — genMsg_{ctor} skipped",
                    a.name
                ));
                return false;
            };
            arg_ty_list.push(ty.clone());
        }
        // Build the ctor application via the top-depth children.
        let mut binds = Vec::new();
        let mut vals = Vec::new();
        let mut seed = "s0".to_string();
        for (i, t) in arg_ty_list.iter().enumerate() {
            let val = format!("a{i}");
            let next = format!("s{}", i + 1);
            match self.child_call_top(t, &seed) {
                Ok(call) => {
                    binds.push(format!("( {val}, {next} ) = {call}"));
                    vals.push(val);
                    seed = next;
                }
                Err(e) => {
                    self.notes
                        .push(format!("Msg `{ctor}` arg {i}: {e} — genMsg_{ctor} skipped"));
                    return false;
                }
            }
        }
        let value = if vals.is_empty() {
            ctor.to_string()
        } else {
            format!("{ctor} {}", vals.join(" "))
        };
        let body = self.let_result(&binds, &value, &seed);
        let mut def = String::new();
        def.push_str(&format!("genMsg_{ctor} : Seed -> ( {msg_type}, Seed )\n"));
        def.push_str(&format!("genMsg_{ctor} s0 =\n{body}\n\n\n"));
        self.defs.push(def);
        self.emitted_msgs.push(ctor.to_string());
        true
    }

    /// A top-level child call at the full depth cap (used by `genModel`/`genMsg`).
    fn child_call_top(&mut self, ty: &ty::Ty, seed: &str) -> Result<String, String> {
        if let ty::Ty::App(name, args) = ty {
            let tl = tail(name.as_str());
            if args.is_empty() {
                if let Some(s) = scalar_gen(tl) {
                    return Ok(format!("{s} {seed}"));
                }
            }
        }
        if let ty::Ty::Unit = ty {
            return Ok(format!("spaGenUnit {seed}"));
        }
        self.ensure(ty)?;
        Ok(format!("spaGenD_{} spaGenCap {seed}", mangle(ty)))
    }

    /// Emit a `genMsg : Seed -> (<msg_type>, Seed)` dispatcher over every ctor a
    /// `genMsg_<Ctor>` was emitted for. No-op when none were emitted.
    pub fn emit_msg_dispatch(&mut self) {
        if self.emitted_msgs.is_empty() {
            return;
        }
        let msg_type = match &self.msg_type {
            Some(m) => m.clone(),
            None => return,
        };
        let k = self.emitted_msgs.len();
        let mut arms = String::new();
        for (i, c) in self.emitted_msgs.iter().enumerate() {
            let sel = if i + 1 == k {
                "_".to_string()
            } else {
                i.to_string()
            };
            arms.push_str(&format!("        {sel} ->\n            genMsg_{c} s1\n\n"));
        }
        let arms = arms.trim_end();
        let mut def = String::new();
        def.push_str(&format!("genMsg : Seed -> ( {msg_type}, Seed )\n"));
        def.push_str("genMsg s0 =\n");
        if k == 1 {
            def.push_str(&format!("    genMsg_{} s0\n\n\n", self.emitted_msgs[0]));
        } else {
            def.push_str("    let\n        ( pick, s1 ) = spaGenInt s0\n    in\n");
            def.push_str(&format!("    case modBy {k} pick of\n{arms}\n\n\n"));
        }
        self.defs.push(def);
    }

    /// Render the complete Sky source: the shared prelude followed by every
    /// buffered generator def, in dependency order.
    pub fn finish(self) -> GenOutput {
        let mut source = String::new();
        source.push_str(PRELUDE);
        for d in &self.defs {
            source.push_str(d);
        }
        GenOutput {
            source,
            notes: self.notes,
            emitted_model: self.emitted_model,
            emitted_msgs: self.emitted_msgs,
        }
    }
}

/// The `Seed` machinery + scalar generators every emitted module shares. A small
/// deterministic LCG (Numerical-Recipes constants), kept inside a 31-bit modulus
/// so `s * mult` stays well within `Int` (int64) — reproducible, which is all the
/// differential run needs (it compares two legs on the SAME seed, not a
/// distribution).
const PRELUDE: &str = "\
-- ===================================================================\n\
-- spa_diff_gen: seeded value generators (generated; do not edit)\n\
-- ===================================================================\n\
\n\
type alias Seed =\n\
\x20   Int\n\
\n\
spaGenCap : Int\n\
spaGenCap =\n\
\x20   4\n\
\n\
spaDec : Int -> Int\n\
spaDec d =\n\
\x20   if d <= 0 then\n\
\x20       0\n\
\n\
\x20   else\n\
\x20       d - 1\n\
\n\
spaSeedNext : Seed -> Seed\n\
spaSeedNext s =\n\
\x20   modBy 2147483647 ((s * 1103515245) + 12345)\n\
\n\
spaGenInt : Seed -> ( Int, Seed )\n\
spaGenInt s =\n\
\x20   let\n\
\x20       s2 = spaSeedNext s\n\
\x20   in\n\
\x20   ( modBy 1000 s2, s2 )\n\
\n\
spaGenBool : Seed -> ( Bool, Seed )\n\
spaGenBool s =\n\
\x20   let\n\
\x20       s2 = spaSeedNext s\n\
\x20   in\n\
\x20   ( modBy 2 s2 == 0, s2 )\n\
\n\
spaGenFloat : Seed -> ( Float, Seed )\n\
spaGenFloat s =\n\
\x20   let\n\
\x20       s2 = spaSeedNext s\n\
\x20   in\n\
\x20   case modBy 4 s2 of\n\
\x20       0 ->\n\
\x20           ( 0.0, s2 )\n\
\n\
\x20       1 ->\n\
\x20           ( 1.5, s2 )\n\
\n\
\x20       2 ->\n\
\x20           ( 3.14, s2 )\n\
\n\
\x20       _ ->\n\
\x20           ( 42.0, s2 )\n\
\n\
spaGenString : Seed -> ( String, Seed )\n\
spaGenString s =\n\
\x20   let\n\
\x20       s2 = spaSeedNext s\n\
\x20   in\n\
\x20   ( \"s\" ++ String.fromInt (modBy 1000 s2), s2 )\n\
\n\
spaGenUnit : Seed -> ( (), Seed )\n\
spaGenUnit s =\n\
\x20   ( (), s )\n\
\n\
\n";

#[cfg(test)]
mod tests {
    use super::*;

    fn app(name: &str) -> ty::Ty {
        ty::Ty::app(name, vec![])
    }
    fn field(name: &str, ty: Option<ty::Ty>) -> ModelFieldTy {
        ModelFieldTy {
            name: name.to_string(),
            ty_name: String::new(),
            codec: None,
            ty,
        }
    }

    /// A synthetic resolver over a fixed name → def map.
    struct Env(HashMap<String, TypeDef>);
    impl TypeResolver for Env {
        fn resolve(&self, name: &str) -> Option<TypeDef> {
            self.0.get(name).cloned()
        }
    }
    fn env(pairs: Vec<(&str, TypeDef)>) -> Env {
        Env(pairs.into_iter().map(|(k, v)| (k.to_string(), v)).collect())
    }

    // Rough structural well-formedness: balanced brackets + every emitted worker
    // annotated, so a syntax-shape regression is caught without a compiler.
    fn well_formed(src: &str) {
        let mut paren = 0i32;
        let mut brack = 0i32;
        let mut brace = 0i32;
        let mut in_str = false;
        let mut prev = ' ';
        for c in src.chars() {
            if in_str {
                if c == '"' && prev != '\\' {
                    in_str = false;
                }
                prev = c;
                continue;
            }
            match c {
                '"' => in_str = true,
                '(' => paren += 1,
                ')' => paren -= 1,
                '[' => brack += 1,
                ']' => brack -= 1,
                '{' => brace += 1,
                '}' => brace -= 1,
                _ => {}
            }
            assert!(paren >= 0 && brack >= 0 && brace >= 0, "unbalanced close in:\n{src}");
            prev = c;
        }
        assert_eq!(paren, 0, "unbalanced parens in:\n{src}");
        assert_eq!(brack, 0, "unbalanced brackets in:\n{src}");
        assert_eq!(brace, 0, "unbalanced braces in:\n{src}");
    }

    #[test]
    fn scalar_int_field_model() {
        let e = env(vec![]);
        let mut g = GenModule::new(&e);
        assert!(g.emit_model("Model", &[field("count", Some(app("Int")))]));
        let out = g.finish();
        assert!(out.emitted_model);
        assert!(out.notes.is_empty(), "unexpected notes: {:?}", out.notes);
        assert!(out.source.contains("genModel : Seed -> ( Model, Seed )"));
        assert!(out.source.contains("( count, s1 ) = spaGenInt s0"));
        assert!(out.source.contains("( { count = count }, s1 )"));
        well_formed(&out.source);
    }

    #[test]
    fn record_list_and_maybe_fields() {
        // Model = { todos : List Todo, note : Maybe String, done : Bool }
        // Todo  = { id : Int, label : String }
        let e = env(vec![(
            "Todo",
            TypeDef::Record(vec![
                ("id".to_string(), app("Int")),
                ("label".to_string(), app("String")),
            ]),
        )]);
        let mut g = GenModule::new(&e);
        let list_todo = ty::Ty::app("List", vec![app("Todo")]);
        let maybe_str = ty::Ty::app("Maybe", vec![app("String")]);
        let ok = g.emit_model(
            "Model",
            &[
                field("todos", Some(list_todo)),
                field("note", Some(maybe_str)),
                field("done", Some(app("Bool"))),
            ],
        );
        assert!(ok);
        let out = g.finish();
        assert!(out.emitted_model);
        assert!(out.notes.is_empty(), "unexpected notes: {:?}", out.notes);
        // The Todo record generator, the List and Maybe workers, and genModel.
        assert!(out.source.contains("spaGenD_Todo : Int -> Seed -> ( Todo, Seed )"));
        assert!(out.source.contains("spaGenD_List_Todo : Int -> Seed -> ( List Todo, Seed )"));
        assert!(out.source.contains("spaGenD_Maybe_String : Int -> Seed -> ( Maybe String, Seed )"));
        // The Todo record is built field-by-field: `{ id = f0, label = f1 }`.
        assert!(out.source.contains("{ id = f0, label = f1 }"), "todo record body:\n{}", out.source);
        well_formed(&out.source);
    }

    #[test]
    fn two_variant_adt_with_typed_arg_as_msg() {
        // Msg = SetCount Int | Reset
        let e = env(vec![(
            "Msg",
            TypeDef::Union(vec![
                ("SetCount".to_string(), vec![app("Int")]),
                ("Reset".to_string(), vec![]),
            ]),
        )]);
        let mut g = GenModule::new(&e);
        assert!(g.emit_msg("Msg", "SetCount", &[field("n", Some(app("Int")))]));
        assert!(g.emit_msg("Msg", "Reset", &[]));
        g.emit_msg_dispatch();
        let out = g.finish();
        assert_eq!(out.emitted_msgs, vec!["SetCount".to_string(), "Reset".to_string()]);
        assert!(out.source.contains("genMsg_SetCount : Seed -> ( Msg, Seed )"));
        assert!(out.source.contains("( SetCount a0, s1 )"));
        assert!(out.source.contains("genMsg_Reset s0 =\n    ( Reset, s0 )"));
        assert!(out.source.contains("genMsg : Seed -> ( Msg, Seed )"));
        assert!(out.source.contains("case modBy 2 pick of"));
        well_formed(&out.source);
    }

    #[test]
    fn union_value_generator_over_typed_variants() {
        // Shape = Circle Float | Rect Int Int  (a 2-variant ADT with typed args)
        let e = env(vec![(
            "Shape",
            TypeDef::Union(vec![
                ("Circle".to_string(), vec![app("Float")]),
                ("Rect".to_string(), vec![app("Int"), app("Int")]),
            ]),
        )]);
        let mut g = GenModule::new(&e);
        assert!(g.emit_model("Model", &[field("shape", Some(app("Shape")))]));
        let out = g.finish();
        assert!(out.emitted_model);
        assert!(out.source.contains("spaGenD_Shape : Int -> Seed -> ( Shape, Seed )"));
        assert!(out.source.contains("( Circle c0, "));
        assert!(out.source.contains("( Rect c0 c1, "));
        well_formed(&out.source);
    }

    #[test]
    fn recursive_adt_is_depth_capped_not_refused() {
        // Tree = Leaf | Node Tree Tree  — a base case exists, so it is finite and
        // must be emitted (bounded by the depth worker), not refused.
        let e = env(vec![(
            "Tree",
            TypeDef::Union(vec![
                ("Leaf".to_string(), vec![]),
                ("Node".to_string(), vec![app("Tree"), app("Tree")]),
            ]),
        )]);
        let mut g = GenModule::new(&e);
        assert!(g.emit_msg("Msg", "SetTree", &[field("t", Some(app("Tree")))]));
        let out = g.finish();
        assert_eq!(out.emitted_msgs, vec!["SetTree".to_string()]);
        assert!(out.source.contains("spaGenD_Tree : Int -> Seed -> ( Tree, Seed )"));
        // The recursive arm threads the decremented depth into the same worker.
        assert!(out.source.contains("spaGenD_Tree (spaDec d)"));
        // The depth-0 branch builds the nullary base ctor.
        assert!(out.source.contains("if d <= 0 then\n        ( Leaf, s0 )"));
        well_formed(&out.source);
    }

    #[test]
    fn base_case_free_recursive_type_is_refused_with_note() {
        // Bad = More Bad  — no nullary / base ctor, so no finite value exists.
        let e = env(vec![(
            "Bad",
            TypeDef::Union(vec![("More".to_string(), vec![app("Bad")])]),
        )]);
        let mut g = GenModule::new(&e);
        let ok = g.emit_model("Model", &[field("x", Some(app("Bad")))]);
        assert!(!ok, "a base-case-free recursive type must be refused");
        let out = g.finish();
        assert!(!out.emitted_model);
        assert!(out.notes.iter().any(|n| n.contains("Bad") && n.contains("no finite value")));
    }

    #[test]
    fn unrecoverable_field_type_withholds_model_with_note() {
        let e = env(vec![]);
        let mut g = GenModule::new(&e);
        let ok = g.emit_model(
            "Model",
            &[field("ok", Some(app("Int"))), field("mystery", None)],
        );
        assert!(!ok);
        let out = g.finish();
        assert!(!out.emitted_model);
        assert!(out.notes.iter().any(|n| n.contains("mystery") && n.contains("None")));
    }

    #[test]
    fn opaque_named_type_is_skipped_not_guessed() {
        // `Secret` does not resolve → no finite value → skipped loudly.
        let e = env(vec![]);
        let mut g = GenModule::new(&e);
        let ok = g.emit_msg("Msg", "SetSecret", &[field("s", Some(app("Secret")))]);
        assert!(!ok);
        let out = g.finish();
        assert!(out.emitted_msgs.is_empty());
        assert!(out.notes.iter().any(|n| n.contains("SetSecret")));
    }

    #[test]
    fn deterministic_emission() {
        let e = env(vec![(
            "Todo",
            TypeDef::Record(vec![
                ("id".to_string(), app("Int")),
                ("label".to_string(), app("String")),
            ]),
        )]);
        let build = || {
            let mut g = GenModule::new(&e);
            g.emit_model("Model", &[field("todos", Some(ty::Ty::app("List", vec![app("Todo")])))]);
            g.emit_msg("Msg", "Add", &[field("label", Some(app("String")))]);
            g.emit_msg_dispatch();
            g.finish().source
        };
        assert_eq!(build(), build(), "emission must be a pure function of its input");
    }
}
