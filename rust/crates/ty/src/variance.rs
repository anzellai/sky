//! Variance of type constructors, for the relaxed value restriction (C-2).
//!
//! # Why this module exists
//!
//! A top-level definition with no parameters is a CAF: its body runs once and
//! every use shares the one value. When that body is an application (an
//! *expansive* expression, see `infer::is_syntactic_value`), the value can hold
//! mutable state — a `Std.Sync.Ref`, a `Std.Cache.Cache`, a `Std.Db` table — so
//! it must not be given a polymorphic type. Otherwise one use stores an `Int`
//! and another reads it back as a `String` (the classic ML polymorphic-ref
//! hole, repro `C/t6` of the v0.27.0 audit).
//!
//! The *relaxed* value restriction (Garrigue 2004) still generalises a type
//! variable that occurs only in COVARIANT positions: a value of type
//! `Cmd msg` or `List (Html msg)` can never be written into at a type chosen by
//! a later use, so sharing it at many types is sound. That keeps
//! `divider : Element msg; divider = Ui.el [] Ui.none` and `none = Cmd.batch []`
//! legal while it rejects `shared : Result Error (Ref (List a))`.
//!
//! # The variance of a type constructor
//!
//! * **Stdlib types default to INVARIANT.** Many stdlib types are phantom-typed
//!   over a hidden handle (`type Ref a = Ref__Internal Int`,
//!   `type Cache k v = Cache Int`, `Table a = Table_OPAQUE`), so computing their
//!   variance from the representation would call them covariant, which is
//!   false. Only the explicit [`COVARIANT_STDLIB`] allowlist is covariant, each
//!   entry checked by hand against its representation and its API.
//! * **User unions get their variance by fixpoint** over their constructor
//!   argument types (mutual recursion included), starting from "unused".
//!   A user union can only reach mutable state through a stdlib type, which is
//!   invariant unless allowlisted, so the fixpoint is sound.
//! * **Structural types** follow the usual rules: a function argument flips the
//!   polarity, a function result, a record field, a record row variable and a
//!   tuple element keep it.
//! * Anything else (a Go FFI type, a kernel-implicit type not on the list, an
//!   unresolved name) is INVARIANT.
//!
//! Aliases are already expanded when a type reaches this module.

use crate::sig::World;
use crate::Ty;
use base::{DefId, Name};
use hir::SkyDb;
use std::collections::HashMap;

/// The variance of a type parameter (or of a variable's occurrences in a
/// type). Ordered as a lattice: `Bi` (does not occur) is the bottom, `Inv` the
/// top, `Co` and `Contra` are incomparable.
#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub enum Variance {
    /// Does not occur (a phantom parameter).
    Bi,
    Co,
    Contra,
    Inv,
}

impl Variance {
    fn join(self, o: Variance) -> Variance {
        use Variance::*;
        match (self, o) {
            (Bi, x) | (x, Bi) => x,
            (a, b) if a == b => a,
            _ => Inv,
        }
    }

    fn flip(self) -> Variance {
        match self {
            Variance::Co => Variance::Contra,
            Variance::Contra => Variance::Co,
            v => v,
        }
    }

    /// The polarity of a position `inner` nested inside a position `outer`.
    fn compose(outer: Variance, inner: Variance) -> Variance {
        match inner {
            Variance::Bi => Variance::Bi,
            Variance::Inv => Variance::Inv,
            Variance::Co => outer,
            Variance::Contra => outer.flip(),
        }
    }

    /// A variable with this variance may be generalised under the relaxed value
    /// restriction.
    pub fn generalisable(self) -> bool {
        matches!(self, Variance::Bi | Variance::Co)
    }
}

/// Stdlib and builtin type constructors that are COVARIANT in every parameter.
///
/// Each entry was checked against its representation and its API (grill
/// review, `GRILL-compiler.md` §1):
///
/// * `List`, `Maybe`, `Result`, `Dict`, `Set`: immutable data.
/// * `Task`, `Cmd`, `Sub`: descriptions of effects; the parameter is only ever
///   produced.
/// * `Decoder` (`Sky.Core.Json.Decode`, kernel-implicit) and
///   `Std.Config.Decoder`: a decoder only produces its value.
/// * `Std.Html.Html`, `Std.Html.Attributes.Attribute`,
///   `Std.Html.Attributes.Event`: `msg` is reached only through `String -> msg`
///   style handlers, which are covariant in `msg`.
/// * `Std.Ui.Element`, `Std.Ui.Attribute`: `msg` reaches the representation
///   only through `AttrEvent any` / `Raw any`, so it is phantom there.
/// * `Std.Ui.Canvas.Shape`, `Std.Ui.Canvas.Attr`: the same shape as `Std.Ui`.
/// * `Std.App.Route`: plain data, `RouteStatic String page`,
///   `RouteParam String (String -> page)` and `RouteApi String (Request -> …)`;
///   `page` occurs only covariantly. (Found by the S3a measurement: an API
///   route list leaves `page` open and is joined with the page routes.)
///
/// Everything else in the stdlib is INVARIANT, on purpose: `Codec`, `Cache`,
/// `Table`, `Job`, `Store`, `WorkflowDef`, `AppConfig`, the `Std.Sync` types,
/// and every future type until it is reviewed and listed here.
pub const COVARIANT_STDLIB: &[&str] = &[
    "List",
    "Maybe",
    "Result",
    "Dict",
    "Set",
    "Task",
    "Cmd",
    "Sub",
    "Decoder",
    "Std.Config.Decoder",
    "Std.Html.Html",
    "Std.Html.Attributes.Attribute",
    "Std.Html.Attributes.Event",
    "Std.Ui.Element",
    "Std.Ui.Attribute",
    "Std.Ui.Canvas.Shape",
    "Std.Ui.Canvas.Attr",
    "Std.App.Route",
    // Kernel-implicit BARE spellings (`hir::KERNEL_IMPLICIT_TYPES`): a user
    // annotation `App.Route page` or `Attribute msg` can resolve to the bare
    // name. Every stdlib type with that name and a parameter is listed above
    // (`Std.App.Route`; `Std.Ui.Attribute`, `Std.Html.Attributes.Attribute`),
    // so the bare name is covariant too. `Store`, `Handler`, `Session`, … stay
    // invariant.
    "Route",
    "Attribute",
];

/// Is `module` part of the stdlib (a kernel pseudo-module, `Sky.*` or `Std.*`)?
pub fn is_stdlib_module(module: &str) -> bool {
    module.starts_with("Sky.")
        || module.starts_with("Std.")
        || hir::KERNEL_MODULES
            .iter()
            .any(|(path, pseudo)| *path == module || *pseudo == module)
}

/// The variance of every parameter of every type constructor the checker
/// knows, built from the world's constructor schemes.
pub struct VarianceTable {
    params: HashMap<String, Vec<Variance>>,
}

impl VarianceTable {
    /// Build the table: allowlisted names are covariant, stdlib unions are
    /// invariant, user unions are computed by fixpoint.
    pub fn build(world: &World, db: &dyn SkyDb) -> VarianceTable {
        // Group constructor schemes by the union they build. A ctor scheme is
        // `arg1 -> … -> T p1 … pn`, with `p_i` the union's parameter names.
        struct Union {
            params: Vec<Name>,
            args: Vec<Ty>,
            stdlib: bool,
        }
        let mut unions: HashMap<String, Union> = HashMap::new();
        let mut ctor_defs: Vec<(&DefId, &crate::Scheme)> = world.ctors_by_def.iter().collect();
        ctor_defs.sort_by_key(|(d, _)| **d);
        for (def, scheme) in ctor_defs {
            let mut args = Vec::new();
            let mut cur = &scheme.ty;
            while let Ty::Fun(a, b) = cur {
                args.push((**a).clone());
                cur = b;
            }
            let Ty::App(name, targs) = cur else { continue };
            let params: Vec<Name> = targs
                .iter()
                .filter_map(|t| match t {
                    Ty::Var(n) => Some(n.clone()),
                    _ => None,
                })
                .collect();
            if params.len() != targs.len() {
                continue;
            }
            let stdlib = db
                .def_loc(*def)
                .map(|l| is_stdlib_module(db.module_name(l.module)))
                .unwrap_or(true);
            let entry = unions.entry(name.as_str().to_string()).or_insert(Union {
                params: params.clone(),
                args: Vec::new(),
                stdlib,
            });
            entry.stdlib |= stdlib;
            // Rename this ctor's parameter names onto the union's first-seen
            // names, so every ctor contributes against one parameter list.
            let rename: HashMap<Name, Name> = params
                .iter()
                .cloned()
                .zip(entry.params.iter().cloned())
                .collect();
            for a in args {
                entry.args.push(rename_vars(&a, &rename));
            }
        }

        let mut table = VarianceTable {
            params: HashMap::new(),
        };
        // Seed: allowlisted and stdlib unions are fixed; user unions start at Bi.
        let mut user: Vec<String> = Vec::new();
        for (name, u) in &unions {
            let v = if COVARIANT_STDLIB.contains(&name.as_str()) {
                Variance::Co
            } else if u.stdlib {
                Variance::Inv
            } else {
                user.push(name.clone());
                Variance::Bi
            };
            table.params.insert(name.clone(), vec![v; u.params.len()]);
        }
        user.sort();
        // Fixpoint over the user unions. The lattice has height 3 per
        // parameter, so this terminates quickly.
        loop {
            let mut changed = false;
            for name in &user {
                let u = &unions[name];
                let mut next = Vec::with_capacity(u.params.len());
                for p in &u.params {
                    let mut v = Variance::Bi;
                    for a in &u.args {
                        v = v.join(table.occurrence(p, a, Variance::Co));
                    }
                    next.push(v);
                }
                if table.params.get(name) != Some(&next) {
                    table.params.insert(name.clone(), next);
                    changed = true;
                }
            }
            if !changed {
                break;
            }
        }
        table
    }

    /// The variance of parameter `i` of the type constructor `name` applied to
    /// `arity` arguments.
    fn param(&self, name: &str, i: usize, arity: usize) -> Variance {
        if COVARIANT_STDLIB.contains(&name) {
            return Variance::Co;
        }
        match self.params.get(name) {
            Some(vs) if vs.len() == arity => vs[i],
            _ => Variance::Inv,
        }
    }

    /// The combined variance of every occurrence of the variable `v` in `t`,
    /// where `t` itself sits at polarity `pol`.
    pub fn occurrence(&self, v: &Name, t: &Ty, pol: Variance) -> Variance {
        match t {
            Ty::Var(n) => {
                if n == v {
                    pol
                } else {
                    Variance::Bi
                }
            }
            Ty::Fun(a, b) => self
                .occurrence(v, a, pol.flip())
                .join(self.occurrence(v, b, pol)),
            Ty::App(name, args) => {
                let mut out = Variance::Bi;
                for (i, a) in args.iter().enumerate() {
                    let inner = self.param(name.as_str(), i, args.len());
                    let pos = Variance::compose(pol, inner);
                    if pos == Variance::Bi {
                        continue;
                    }
                    let occ = self.occurrence(v, a, Variance::Co);
                    if occ == Variance::Bi {
                        continue;
                    }
                    out = out.join(Variance::compose(pos, occ));
                }
                out
            }
            Ty::Record(fields, ext) => {
                let mut out = match ext {
                    Some(e) if e == v => pol,
                    _ => Variance::Bi,
                };
                for (_, ft) in fields {
                    out = out.join(self.occurrence(v, ft, pol));
                }
                out
            }
            Ty::Tuple(xs) => xs
                .iter()
                .fold(Variance::Bi, |acc, x| acc.join(self.occurrence(v, x, pol))),
            Ty::Unit | Ty::Error => Variance::Bi,
        }
    }

    /// Why `v` is not generalisable in `t`: the first position that makes it
    /// contravariant or invariant, in words for a diagnostic.
    pub fn reason(&self, v: &Name, t: &Ty, pol: Variance) -> Option<String> {
        match t {
            Ty::Var(_) | Ty::Unit | Ty::Error => None,
            Ty::Fun(a, b) => {
                if self.occurrence(v, a, pol.flip()) != Variance::Bi
                    && !self.occurrence(v, a, pol.flip()).generalisable()
                {
                    return self
                        .reason(v, a, pol.flip())
                        .or_else(|| Some("in a function argument".to_string()));
                }
                self.reason(v, b, pol)
            }
            Ty::App(name, args) => {
                for (i, a) in args.iter().enumerate() {
                    let inner = self.param(name.as_str(), i, args.len());
                    let pos = Variance::compose(pol, inner);
                    let occ = self.occurrence(v, a, Variance::Co);
                    if pos == Variance::Bi || occ == Variance::Bi {
                        continue;
                    }
                    if Variance::compose(pos, occ).generalisable() {
                        continue;
                    }
                    let shown = crate::nominal::strip(name.as_str());
                    return match inner {
                        Variance::Inv => Some(format!("inside `{shown}`")),
                        _ => self
                            .reason(v, a, pos)
                            .or_else(|| Some(format!("inside `{shown}`"))),
                    };
                }
                None
            }
            Ty::Record(fields, _) => fields.iter().find_map(|(_, ft)| self.reason(v, ft, pol)),
            Ty::Tuple(xs) => xs.iter().find_map(|x| self.reason(v, x, pol)),
        }
    }
}

pub(crate) fn rename_vars(t: &Ty, m: &HashMap<Name, Name>) -> Ty {
    match t {
        Ty::Var(n) => Ty::Var(m.get(n).cloned().unwrap_or_else(|| n.clone())),
        Ty::Fun(a, b) => Ty::Fun(Box::new(rename_vars(a, m)), Box::new(rename_vars(b, m))),
        Ty::App(n, args) => Ty::App(n.clone(), args.iter().map(|a| rename_vars(a, m)).collect()),
        Ty::Record(fs, ext) => Ty::Record(
            fs.iter()
                .map(|(n, t)| (n.clone(), rename_vars(t, m)))
                .collect(),
            ext.as_ref()
                .map(|e| m.get(e).cloned().unwrap_or_else(|| e.clone())),
        ),
        Ty::Tuple(xs) => Ty::Tuple(xs.iter().map(|x| rename_vars(x, m)).collect()),
        Ty::Unit => Ty::Unit,
        Ty::Error => Ty::Error,
    }
}

/// One type variable a CAF may not be generalised over.
pub struct Violation {
    /// The variable as the diagnostic shows it.
    pub var: String,
    /// Where it occurs, in words (`inside `Ref``, `in a function argument`).
    pub reason: String,
    /// The variable was minted by inference (an `any` hole or an unannotated
    /// def), not written by the user.
    pub from_inference: bool,
    /// The variable as it is spelt in the checked type (before display
    /// renaming): what pass 6b substitutes.
    pub raw: String,
}

/// The type variables of `t` that the relaxed value restriction forbids a CAF
/// with an expansive body from generalising: every free variable whose
/// occurrences are not all covariant and which, when `reached` is given, the
/// type of some expansive sub-expression mentions. Also returns `t` with inference
/// variables renamed for display (`t42` → `a`), and the display names of the
/// violating variables use the same renaming.
pub fn violations(
    world: &World,
    db: &dyn SkyDb,
    t: &Ty,
    reached: Option<&std::collections::HashSet<String>>,
) -> (Ty, Vec<Violation>) {
    let free = t.free_vars();
    if free.iter().all(|n| n.as_str() == "any") {
        return (t.clone(), Vec::new());
    }
    let table = VarianceTable::build(world, db);
    let (shown, names) = prettify(t);
    let mut out = Vec::new();
    for v in free {
        if v.as_str() == "any" {
            continue;
        }
        // A variable no expansive sub-expression mentions is generalisable
        // at any variance (see `Infer::check_value_restriction`).
        if reached.is_some_and(|r| !r.contains(v.as_str())) {
            continue;
        }
        let var = table.occurrence(&v, t, Variance::Co);
        if var.generalisable() {
            continue;
        }
        let reason = table
            .reason(&v, t, Variance::Co)
            .unwrap_or_else(|| "in a position that is not covariant".to_string());
        let display = names
            .get(v.as_str())
            .cloned()
            .unwrap_or_else(|| v.as_str().to_string());
        out.push(Violation {
            var: display,
            reason,
            from_inference: is_internal_var(v.as_str()),
            raw: v.as_str().to_string(),
        });
    }
    (shown, out)
}

/// `t` with each variable named in `vars` replaced by `with` — the concrete
/// type a diagnostic suggests (`Result Error (Ref (List Int))`).
pub fn concretise(t: &Ty, vars: &[String], with: &Ty) -> Ty {
    let hit = |n: &Name| vars.iter().any(|v| v == n.as_str());
    match t {
        Ty::Var(n) if hit(n) => with.clone(),
        Ty::Fun(a, b) => Ty::Fun(
            Box::new(concretise(a, vars, with)),
            Box::new(concretise(b, vars, with)),
        ),
        Ty::App(n, args) => Ty::App(
            n.clone(),
            args.iter().map(|a| concretise(a, vars, with)).collect(),
        ),
        Ty::Tuple(xs) => Ty::Tuple(xs.iter().map(|x| concretise(x, vars, with)).collect()),
        Ty::Record(fs, ext) => Ty::Record(
            fs.iter()
                .map(|(n, t)| (n.clone(), concretise(t, vars, with)))
                .collect(),
            // A row variable has no concrete spelling; closing it is the fix.
            ext.as_ref().filter(|e| !hit(e)).cloned(),
        ),
        other => other.clone(),
    }
}

/// Is `n` a variable the unifier minted (`t42`, `r7`), rather than one the
/// user wrote? Mirrors `Ty::render_pretty`.
fn is_internal_var(n: &str) -> bool {
    let mut chars = n.chars();
    (matches!(chars.next(), Some('t') | Some('r'))
        && !n[1..].is_empty()
        && n[1..].chars().all(|c| c.is_ascii_digit()))
        || crate::unify::SuperType::minted_label(n).is_some()
}

/// Rename the unifier's variables to `a`, `b`, … (skipping names the user
/// wrote), returning the renamed type and the mapping.
fn prettify(t: &Ty) -> (Ty, HashMap<String, String>) {
    let free = t.free_vars();
    let kept: std::collections::HashSet<String> = free
        .iter()
        .filter(|n| !is_internal_var(n.as_str()))
        .map(|n| n.as_str().to_string())
        .collect();
    let mut map: HashMap<String, String> = HashMap::new();
    let mut counter = 0usize;
    for n in &free {
        if !is_internal_var(n.as_str()) {
            continue;
        }
        let clean = loop {
            let letter = (b'a' + (counter % 26) as u8) as char;
            let suffix = counter / 26;
            counter += 1;
            let cand = if suffix == 0 {
                letter.to_string()
            } else {
                format!("{letter}{suffix}")
            };
            if !kept.contains(&cand) {
                break cand;
            }
        };
        map.insert(n.as_str().to_string(), clean);
    }
    let names: HashMap<Name, Name> = map
        .iter()
        .map(|(k, v)| (Name::new(k), Name::new(v)))
        .collect();
    (rename_vars(t, &names), map)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lattice() {
        use Variance::*;
        assert_eq!(Co.join(Contra), Inv);
        assert_eq!(Bi.join(Contra), Contra);
        assert_eq!(Co.join(Co), Co);
        assert_eq!(Variance::compose(Contra, Contra), Co);
        assert_eq!(Variance::compose(Co, Inv), Inv);
        assert_eq!(Variance::compose(Contra, Bi), Bi);
    }

    #[test]
    fn structural_occurrence() {
        let t = VarianceTable {
            params: HashMap::new(),
        };
        let a = Name::new("a");
        // a -> a : invariant overall
        let f = Ty::Fun(Box::new(Ty::var("a")), Box::new(Ty::var("a")));
        assert_eq!(t.occurrence(&a, &f, Variance::Co), Variance::Inv);
        // (a -> Int) -> Int : a is covariant (double flip)
        let g = Ty::Fun(
            Box::new(Ty::Fun(
                Box::new(Ty::var("a")),
                Box::new(Ty::app("Int", vec![])),
            )),
            Box::new(Ty::app("Int", vec![])),
        );
        assert_eq!(t.occurrence(&a, &g, Variance::Co), Variance::Co);
        // List (Maybe a): covariant through the allowlist
        let l = Ty::app("List", vec![Ty::app("Maybe", vec![Ty::var("a")])]);
        assert_eq!(t.occurrence(&a, &l, Variance::Co), Variance::Co);
        // An unknown constructor is invariant.
        let r = Ty::app("Std.Sync.Ref", vec![Ty::var("a")]);
        assert_eq!(t.occurrence(&a, &r, Variance::Co), Variance::Inv);
        // A record row variable keeps the polarity.
        let rec = Ty::Record(vec![], Some(Name::new("a")));
        assert_eq!(t.occurrence(&a, &rec, Variance::Co), Variance::Co);
    }
}

/// The name prefix of a WEAK variable's opaque type (C-2). Pass 6b replaces a
/// variable the value restriction does not generalise, in the scheme callers
/// see, by `Ty::App("<WEAK_PREFIX><def id>_<var>")`: a nominal type with no
/// constructors that unifies only with itself. No dots, so the printer shows
/// it whole and [`crate::infer`] can find it in a mismatch message.
pub const WEAK_PREFIX: &str = "SkyWeak_";

/// A weak variable, for the `[E2012]` message at a use that fixes it.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct WeakVar {
    /// The CAF that owns it.
    pub def: String,
    /// The variable as the diagnostic shows it.
    pub var: String,
    /// Where it occurs (`inside `Ref``).
    pub reason: String,
    /// The fix sentence(s), ending with the migration link.
    pub fix: String,
}

/// The fix sentences shared by the definition-site and use-site `[E2012]`
/// messages: a concrete type first, then the alternative, then the link.
pub struct VrInfo {
    pub fix: String,
}

/// Build the [`VrInfo`] for the CAF `name` whose (display-renamed) type is
/// `shown` and whose checked type is `ty`.
pub fn vr_info(
    name: &str,
    shown: &Ty,
    ty: &Ty,
    viols: &[Violation],
    annotated_any: bool,
) -> VrInfo {
    let bad: Vec<String> = viols.iter().map(|v| v.var.clone()).collect();
    let concrete = concretise(shown, &bad, &Ty::app("Int", vec![])).render();
    let from_any = annotated_any && viols.iter().any(|v| v.from_inference);
    let (why_any, anchor) = if from_any {
        (
            " In v0.27.0 an `any` in a signature is filled from the body, and this body \
             leaves it open, so write the type instead of `any`.",
            "any-in-annotations",
        )
    } else {
        ("", "value-restriction")
    };
    let other = if matches!(ty, Ty::Fun(..)) {
        format!(
            " Or give `{name}` its parameter, so the body is a function and not a value \
             computed once: `{name} x = … x`."
        )
    } else {
        " Or, if every use really needs its own value, make it a function of `()`; each \
         call then builds a new value."
            .to_string()
    };
    VrInfo {
        fix: format!(
            "{why_any} Fix: give it a concrete type, for example `{name} : {concrete}`.{other} \
             See docs/migration/v0.27.md#{anchor}"
        ),
    }
}

/// `t` with each violating variable replaced by its weak opaque type, and the
/// [`WeakVar`] records for them. `def_key` makes the names unique per def.
pub fn weaken(
    t: &Ty,
    def_key: &str,
    def_name: &str,
    viols: &[Violation],
    info: &VrInfo,
) -> (Ty, Vec<(String, WeakVar)>) {
    let mut out = t.clone();
    let mut weak = Vec::new();
    for v in viols {
        let wname = format!("{WEAK_PREFIX}{def_key}_{}", v.raw);
        out = concretise(&out, std::slice::from_ref(&v.raw), &Ty::app(&wname, vec![]));
        weak.push((
            wname,
            WeakVar {
                def: def_name.to_string(),
                var: v.var.clone(),
                reason: v.reason.clone(),
                fix: info.fix.clone(),
            },
        ));
    }
    (out, weak)
}
