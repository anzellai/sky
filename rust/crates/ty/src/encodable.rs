//! The public `Encodable` check (v0.27.0, B-1), for code outside the checker.
//!
//! The checker enforces `Encodable` as a type bound during inference
//! (`crate::obligations`). Tools that hold an already-solved [`Ty`] (the
//! Sky.Spa split deciding whether a model field can be embedded in the first
//! paint with `Codec.auto`) ask the same question here, with the SAME rules:
//! the nominal classification is shared (`crate::obligations::classify`), so
//! the two can never disagree about which types encode.
//!
//! A type is encodable when it holds no function, `Secret`, crypto key or
//! protocol state, runtime handle (`Process`, `Watcher`, `Sync.Ref`/`Mutex`/
//! `Queue`, `WebSocket`, `WebSocketServer`, `StreamId`, `StreamWriter`,
//! `Cache`), kernel type holding closures (`Task`, `Cmd`, `Sub`, `Html`, …) or
//! opaque stdlib type other than `Decimal`, anywhere inside: through `List`,
//! `Maybe`, `Result`, `Dict`, `Set`, tuples, records and the constructors of
//! custom types. A type variable and an unknown (Go) type are accepted.

use crate::obligations::{classify, Nominal};
use crate::{Scheme, Ty, TyDb};
use base::Name;
use std::collections::{HashMap, HashSet};

/// Why a type cannot be encoded by `Codec.auto`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Unencodable {
    /// The offending part of the type, rendered (`Secret`, `Int -> Int`).
    pub offending: String,
    /// What it is, in words (`a function`, `a runtime handle`).
    pub reason: String,
}

impl Unencodable {
    /// A one-paragraph diagnostic body, ending with the migration link.
    pub fn message(&self) -> String {
        crate::unify::encodable_failure(&format!("`{}` ({})", self.offending, self.reason))
    }
}

/// `None` when `ty` round-trips through `Codec.auto`; else the first offending
/// part. `db` supplies the nominal environment (the declarations world).
///
/// S4 (`project/src/spa_split.rs`, `codec_auto_unencodable`) calls it as
/// `ty::encodable::check(&field_ty, db)` with the field's resolved type.
pub fn check(ty: &Ty, db: &dyn TyDb) -> Option<Unencodable> {
    let world = db.type_world();
    let mut seen = HashSet::new();
    walk(ty, &world, db.as_sky_db(), &mut seen)
}

fn walk(
    t: &Ty,
    world: &crate::World,
    db: &dyn hir::SkyDb,
    seen: &mut HashSet<String>,
) -> Option<Unencodable> {
    let bad = |t: &Ty, reason: &str| {
        Some(Unencodable {
            offending: t.render(),
            reason: reason.to_string(),
        })
    };
    match t {
        Ty::Fun(..) => bad(t, "a function"),
        Ty::Var(_) | Ty::Unit | Ty::Error => None,
        Ty::Tuple(xs) => xs.iter().find_map(|x| walk(x, world, db, seen)),
        Ty::Record(fs, _) => fs.iter().find_map(|(_, x)| walk(x, world, db, seen)),
        Ty::App(n, args) => {
            let name = n.as_str();
            match (name, args.len()) {
                ("Int" | "Float" | "String" | "Char" | "Bool", 0) => return None,
                ("List" | "Maybe" | "Set", 1) | ("Result" | "Dict", 2) => {
                    return args.iter().find_map(|x| walk(x, world, db, seen));
                }
                _ => {}
            }
            if name.starts_with(crate::variance::WEAK_PREFIX) {
                return None;
            }
            match classify(world, db, name) {
                Nominal::Denied(kind) => bad(t, kind),
                Nominal::Closures => bad(t, "its values hold functions"),
                Nominal::Opaque { encodable: true } | Nominal::Unknown => None,
                Nominal::Opaque { encodable: false } => {
                    bad(t, "an opaque type the encoder cannot see inside")
                }
                Nominal::Union(ctors) => {
                    if !seen.insert(t.render()) {
                        return None;
                    }
                    ctors.iter().find_map(|(_, s)| {
                        payload(s, args)
                            .into_iter()
                            .find_map(|p| walk(&p, world, db, seen))
                    })
                }
            }
        }
    }
}

/// The payload types of one constructor, its type parameters replaced by
/// `args` (the constructor's result is `T p1 … pn`).
fn payload(scheme: &Scheme, args: &[Ty]) -> Vec<Ty> {
    let mut cur = &scheme.ty;
    let mut out = Vec::new();
    while let Ty::Fun(a, b) = cur {
        out.push((**a).clone());
        cur = b;
    }
    let mut map: HashMap<Name, Ty> = HashMap::new();
    if let Ty::App(_, params) = cur {
        for (p, a) in params.iter().zip(args) {
            if let Ty::Var(v) = p {
                map.insert(v.clone(), a.clone());
            }
        }
    }
    out.iter().map(|t| subst(t, &map)).collect()
}

fn subst(t: &Ty, map: &HashMap<Name, Ty>) -> Ty {
    match t {
        Ty::Var(v) => map.get(v).cloned().unwrap_or_else(|| t.clone()),
        Ty::Fun(a, b) => Ty::Fun(Box::new(subst(a, map)), Box::new(subst(b, map))),
        Ty::App(n, xs) => Ty::App(n.clone(), xs.iter().map(|x| subst(x, map)).collect()),
        Ty::Tuple(xs) => Ty::Tuple(xs.iter().map(|x| subst(x, map)).collect()),
        Ty::Record(fs, e) => Ty::Record(
            fs.iter().map(|(n, x)| (n.clone(), subst(x, map))).collect(),
            e.clone(),
        ),
        Ty::Unit | Ty::Error => t.clone(),
    }
}
