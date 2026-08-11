//! **`[E2008]` — the unsupported-`Dict`-key check.**
//!
//! # Why this exists at CHECK time
//!
//! A Sky `Dict k v` is a Go `map[string]V`: every key is stringified on the way
//! in (`runtime-go/rt/rt.go`) and decoded back on the way out by the operations
//! that let the key escape — `toList` / `keys` / `values` / `foldl` / `map`. The
//! decode is only defined for five key types, and for a COMPOSITE key it can
//! never be defined, for a decisive reason:
//!
//! > `fmt.Sprintf("%v", key)` is **not injective** on composites. The tuples
//! > `( "a b", "c" )` and `( "a", "b c" )` both render `{a b c}`. Two distinct
//! > keys collide in the map, so no decoder — however clever — can recover the
//! > original key, and one of the two entries is silently lost.
//!
//! Until now that surfaced as a RUNTIME panic (`rt.UnsupportedDictKey`) from a
//! program that passed `sky check`. A panic out of well-typed Sky violates the
//! project's central promise ("if it compiles, it works", AGENTS.md), so the
//! type checker refuses the program instead, with source context.
//!
//! # THE VALID SET — verified empirically, do not narrow it
//!
//! `String`, `Int`, `Float`, `Char`, `Bool`. All five decode AND order
//! correctly across `toList` / `keys` / `values` / `foldl` / `map` on current
//! `main` (a `Dict Int v` visits 9 before 10; `Float` keys sort 1.5 before 2.5;
//! `Bool` keys visit `False` then `True`). Narrowing the set to a "nicer" subset
//! (e.g. dropping `Float`/`Bool`) would REJECT PROGRAMS THAT WORK TODAY, which
//! is a worse outcome than the panic this replaces. Reject only what genuinely
//! cannot work.
//!
//! # THE FAIL-OPEN RULE — an over-rejecting checker is worse than the panic
//!
//! [`classify`] has THREE outcomes, not two, and the third is load-bearing:
//! [`KeyVerdict::Unknown`] means "the key type is not yet pinned to anything
//! concrete", and it is SILENT. Everything the checker is unsure about lands
//! there:
//!
//! * **A key-polymorphic signature.** `keysOf : Dict k v -> List k` is ordinary,
//!   valid, widely-used Sky — every generic dictionary helper in every codebase
//!   has this shape. Its `k` is a `Ty::Var` (an annotation quantifier, or a
//!   skolemised rigid on the annotation-gate path), so it is `Unknown` and this
//!   check stays silent. Firing here would break every such helper; that single
//!   false positive would be worse than the runtime panic. Since #174 the shape
//!   does not merely survive, it WORKS: the encoded key carries its own kind tag
//!   (`rt.encodeDictKey`), so iteration inside a helper whose `k` was erased
//!   decodes correctly. Rejecting it would now be doubly wrong.
//! * **`Dict.empty` with nothing inferred yet.** Its key is an unresolved
//!   flexible var — `Ty::Var("t42")` after read-back — hence `Unknown`.
//! * **A `comparable` / `number` super-typed var.** `comparable` reads back as
//!   `Ty::Var`, but an unresolved `Number` super reads back as the LOWERCASE
//!   nominal `Ty::App("number", [])` (`infer.rs::read_back_seen`). That is an
//!   inference artefact, not a user type — hence the lowercase-initial guard in
//!   [`classify`], without which `Dict.insert n v d` on an un-defaulted numeric
//!   key would be rejected outright.
//! * **`Ty::Error`** — the L7 recovery sentinel. Never cascade off it.
//! * **An unknown / FFI-opaque name** the world could not resolve. The `ty`
//!   leniency contract already turns those into fresh flexible vars.
//!
//! # Aliases
//!
//! No alias handling lives here, deliberately. `sig::World` expands aliases
//! TRANSPARENTLY when it builds the type (`sig.rs` §"pass 1b" + `World::expand`),
//! so by the time a `Ty` reaches this module `type alias Pair = ( Int, Int )` is
//! already `Ty::Tuple([Int, Int])` and `type alias UserId = String` is already
//! `Ty::App("String", [])`. Re-implementing expansion here would be a second
//! source of truth and would reopen the #164 class of same-named-alias bugs. The
//! consequence is that `Dict Pair v` is rejected (correct — it is a tuple) and
//! `Dict UserId v` is accepted (correct — it is a `String`), with no bypass.
//!
//! # Where a key type becomes concrete
//!
//! A bare type variable that INSTANTIATES to a composite at a call site is
//! caught at that call site, not in the polymorphic helper: the caller's own
//! expression carries the concrete `Dict ( Int, Int ) v` in its inferred type,
//! and `check.rs` scans every inferred expression type, not only annotations.
//! That is the honest place — it is where the composite key actually exists, and
//! it is the span the user can act on.

use crate::Ty;

/// The five key types that survive the stringify → decode round trip.
///
/// **Verified**, not assumed: each was exercised through `toList` / `keys` /
/// `values` / `foldl` / `map` on `main`. Changing this list changes what the
/// language accepts — do it in a commit that says why, and only alongside the
/// matching runtime support in `runtime-go/rt/rt.go`.
pub const SUPPORTED_DICT_KEYS: [&str; 5] = ["String", "Int", "Float", "Char", "Bool"];

/// A human list for the diagnostic body: ``String`, `Int`, `Float`, `Char` or
/// `Bool``.
pub fn supported_list() -> String {
    let quoted: Vec<String> = SUPPORTED_DICT_KEYS
        .iter()
        .map(|k| format!("`{k}`"))
        .collect();
    let (last, head) = quoted.split_last().expect("non-empty");
    format!("{} or {last}", head.join(", "))
}

/// The verdict on ONE key type. The three-way split is the whole design: see
/// the FAIL-OPEN RULE in the module docs for why `Unknown` may never be folded
/// into `Unsupported`.
#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub enum KeyVerdict {
    /// One of [`SUPPORTED_DICT_KEYS`]. Round-trips.
    Supported,
    /// Not pinned to a concrete type (type variable, super-typed var, error
    /// sentinel). **Silent** — the checker does not know enough to reject.
    Unknown,
    /// Concretely a type that cannot round-trip. This is the only verdict that
    /// produces a diagnostic.
    Unsupported,
}

/// Classify a `Dict`'s KEY type.
pub fn classify(key: &Ty) -> KeyVerdict {
    match key {
        // A quantifier, a skolemised rigid, an unresolved flexible var
        // (`t42`), `comparable` / `appendable` — all "not known yet".
        Ty::Var(_) => KeyVerdict::Unknown,
        // L7 recovery sentinel: never cascade a second error off a first one.
        Ty::Error => KeyVerdict::Unknown,
        Ty::App(name, args) => {
            let n = name.as_str();
            // An unresolved `Number` super reads back as the LOWERCASE nominal
            // `number` (infer.rs::read_back_seen). Sky type CONSTRUCTORS are
            // uppercase by grammar, so a lowercase-initial `App` is always an
            // inference artefact, never a user type. Fail open.
            if n.chars().next().is_some_and(|c| c.is_lowercase()) {
                return KeyVerdict::Unknown;
            }
            if args.is_empty() && SUPPORTED_DICT_KEYS.contains(&n) {
                KeyVerdict::Supported
            } else {
                // A saturated nominal: a union (`Dict Color v`), a parameterised
                // type (`Dict (Maybe Int) v`, `Dict (List Int) v`), an FFI
                // opaque. None of them decode.
                KeyVerdict::Unsupported
            }
        }
        // Composites, in the exact sense that defeats `%v`: no injective
        // stringification exists.
        Ty::Tuple(_) | Ty::Record(_, _) | Ty::Unit | Ty::Fun(_, _) => KeyVerdict::Unsupported,
    }
}

/// The nominal name a `Dict` type carries in [`Ty::App`]. `sig::World` folds the
/// home module into the name for accept-parity, and the kernel type table
/// (`hir/src/kernel.rs`) registers `Sky.Core.Dict.Dict` under the BARE name
/// `Dict`, so this is what an annotation and an inferred type both produce.
const DICT: &str = "Dict";

/// Collect every UNSUPPORTED `Dict` key type reachable inside `t`, in
/// first-seen order, deduplicated by rendering.
///
/// Structural: a `Dict` nested inside a record field, a tuple slot, a function
/// argument or another `Dict`'s VALUE is found just the same, because a key that
/// cannot decode cannot decode wherever it is written.
pub fn unsupported_keys(t: &Ty) -> Vec<Ty> {
    let mut out: Vec<Ty> = Vec::new();
    walk(t, &mut out);
    out
}

fn walk(t: &Ty, out: &mut Vec<Ty>) {
    if let Ty::App(name, args) = t {
        if name.as_str() == DICT && args.len() == 2 && classify(&args[0]) == KeyVerdict::Unsupported
        {
            let key = args[0].clone();
            if !out.iter().any(|k| k.render() == key.render()) {
                out.push(key);
            }
            // Fall through: the VALUE type may hold another offending Dict, and
            // the key itself may be `Dict (Dict (Int,Int) v) w`.
        }
    }
    match t {
        Ty::Var(_) | Ty::Unit | Ty::Error => {}
        Ty::Fun(a, b) => {
            walk(a, out);
            walk(b, out);
        }
        Ty::App(_, args) | Ty::Tuple(args) => {
            for a in args {
                walk(a, out);
            }
        }
        Ty::Record(fields, _) => {
            for (_, ft) in fields {
                walk(ft, out);
            }
        }
    }
}

/// The `[E2008]` diagnostic body for one offending key type.
///
/// Names the offending type, states what IS supported, gives the reason the
/// composite case is irreparable (non-injective stringification, with the
/// concrete collision), and hands over to [`suggestion`] for the workaround.
pub fn message(key: &Ty) -> String {
    format!(
        "`{}` cannot be used as a `Dict` key. A Sky `Dict k v` is a Go \
         `map[string]v`: the key is stringified on the way in, and only {} \
         decode back to the key you declared. A composite key does not \
         stringify injectively — the tuples ( \"a b\", \"c\" ) and \
         ( \"a\", \"b c\" ) both render as `{{a b c}}`, so two distinct keys \
         collide in the map and no decoder can recover the original.",
        key.render_pretty(),
        supported_list(),
    )
}

/// The `Try: …` line — the two workarounds that actually work.
pub fn suggestion() -> String {
    "encode the key as a `String` yourself (e.g. `String.fromInt x ++ \":\" ++ \
     String.fromInt y`) and keep the structured form in the VALUE, or hold the \
     entries as a `List ( k, v )` of pairs if you need composite keys and do \
     not need dictionary lookup."
        .to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use base::Name;

    fn app(n: &str) -> Ty {
        Ty::app(n, vec![])
    }
    fn dict(k: Ty, v: Ty) -> Ty {
        Ty::app("Dict", vec![k, v])
    }

    #[test]
    fn the_five_supported_keys_are_supported() {
        for k in SUPPORTED_DICT_KEYS {
            assert_eq!(
                classify(&app(k)),
                KeyVerdict::Supported,
                "{k} must stay supported — it round-trips on main today, and \
                 rejecting it would break working programs"
            );
            assert!(unsupported_keys(&dict(app(k), app("String"))).is_empty());
        }
    }

    /// THE trap. A key-polymorphic signature is ordinary Sky; firing on it
    /// would break every generic dictionary helper in every codebase.
    #[test]
    fn key_polymorphic_signatures_are_silent() {
        // keysOf : Dict k v -> List k
        let t = Ty::Fun(
            Box::new(dict(Ty::var("k"), Ty::var("v"))),
            Box::new(Ty::app("List", vec![Ty::var("k")])),
        );
        assert!(unsupported_keys(&t).is_empty());
        // An unresolved flexible var, as `Dict.empty` produces.
        assert!(unsupported_keys(&dict(Ty::var("t42"), Ty::var("t43"))).is_empty());
        // A skolemised rigid on the annotation-gate path reads back the same way.
        assert_eq!(
            classify(&Ty::Var(Name::new("comparable"))),
            KeyVerdict::Unknown
        );
    }

    /// An un-defaulted numeric super reads back as the LOWERCASE nominal
    /// `number`. Treating it as a user type would reject `Dict.insert n v d`.
    #[test]
    fn lowercase_nominal_inference_artefacts_are_silent() {
        assert_eq!(classify(&app("number")), KeyVerdict::Unknown);
        assert!(unsupported_keys(&dict(app("number"), app("String"))).is_empty());
    }

    #[test]
    fn error_sentinel_never_cascades() {
        assert_eq!(classify(&Ty::Error), KeyVerdict::Unknown);
        assert!(unsupported_keys(&dict(Ty::Error, app("String"))).is_empty());
    }

    #[test]
    fn composites_and_nominals_are_rejected() {
        let cases = vec![
            Ty::Tuple(vec![app("Int"), app("Int")]),
            Ty::App(Name::new("List"), vec![app("Int")]),
            Ty::Record(vec![(Name::new("x"), app("Int"))], None),
            Ty::Unit,
            Ty::App(Name::new("Color"), vec![]),
            Ty::App(Name::new("Maybe"), vec![app("Int")]),
            Ty::Fun(Box::new(app("Int")), Box::new(app("Int"))),
        ];
        for k in cases {
            assert_eq!(
                classify(&k),
                KeyVerdict::Unsupported,
                "{} must be rejected",
                k.render()
            );
            let found = unsupported_keys(&dict(k.clone(), app("String")));
            assert_eq!(found.len(), 1, "{} should be reported once", k.render());
        }
    }

    /// A `Dict` buried in a record field / tuple / arrow is still found — the
    /// key cannot decode wherever it is written.
    #[test]
    fn nested_dicts_are_found() {
        let bad = dict(Ty::Tuple(vec![app("Int"), app("Int")]), app("String"));
        let inside_record = Ty::Record(vec![(Name::new("index"), bad.clone())], None);
        assert_eq!(unsupported_keys(&inside_record).len(), 1);
        let inside_arrow = Ty::Fun(Box::new(app("Int")), Box::new(bad.clone()));
        assert_eq!(unsupported_keys(&inside_arrow).len(), 1);
        let inside_tuple = Ty::Tuple(vec![app("Int"), bad.clone()]);
        assert_eq!(unsupported_keys(&inside_tuple).len(), 1);
        // Two DIFFERENT offending keys → two findings; the same one twice → one.
        let two = Ty::Tuple(vec![bad.clone(), dict(app("Color"), app("Int"))]);
        assert_eq!(unsupported_keys(&two).len(), 2);
        let same_twice = Ty::Tuple(vec![bad.clone(), bad.clone()]);
        assert_eq!(unsupported_keys(&same_twice).len(), 1);
    }

    /// A `Dict` whose VALUE is an offending `Dict` is reported.
    #[test]
    fn offending_dict_in_value_position_is_found() {
        let inner = dict(Ty::Tuple(vec![app("Int"), app("Int")]), app("String"));
        let outer = dict(app("String"), inner);
        assert_eq!(unsupported_keys(&outer).len(), 1);
    }

    /// `Dict` at the wrong arity is not our business (the arity/unify gates own
    /// it); we must not index out of bounds on it either.
    #[test]
    fn wrong_arity_dict_is_ignored() {
        assert!(unsupported_keys(&Ty::app("Dict", vec![Ty::Unit])).is_empty());
        assert!(unsupported_keys(&Ty::app("Dict", vec![])).is_empty());
    }

    #[test]
    fn message_names_the_type_and_the_supported_set() {
        let m = message(&Ty::Tuple(vec![app("Int"), app("Int")]));
        assert!(m.contains("( Int, Int )"), "{m}");
        for k in SUPPORTED_DICT_KEYS {
            assert!(m.contains(&format!("`{k}`")), "{m} must mention {k}");
        }
    }
}
