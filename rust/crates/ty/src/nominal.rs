//! Nominal type identity — the ONE answer to "are these the same type?".
//!
//! # Why this module exists
//!
//! `Ty::App`'s name used to be a bare final segment, compared by `unify` as a
//! plain string. Two same-named unions in two modules were therefore ONE type to
//! the checker, while `lower` emitted two DISTINCT Go interfaces for them and
//! bridged the gap with `rt.Coerce`. Both Go interfaces carry the same method
//! set (`SkyVariantTag`/`SkyVariantName`, no unexported sealing method), so the
//! assertion SUCCEEDED and handed the callee a value none of its `case` arms
//! matched:
//!
//! ```text
//! panic: sky.Unreachable(case): sky: codegen reached an arm the
//!        exhaustiveness checker said was impossible
//! ```
//!
//! `sky check` clean, `go build` clean, runtime panic — a direct violation of
//! "no runtime panic from well-typed Sky code". Pinned at
//! `corpus/repro/cross-module-union-conflation/`.
//!
//! # The representation
//!
//! A nominal name is EITHER
//!
//! * **qualified** — `"Conflate.Alpha.Shape"`, meaning *the type named `Shape`
//!   declared in module `Conflate.Alpha`*. Produced only when the declaring
//!   module is known with certainty, and only for user-declared unions and
//!   aliases (see [`same`] for why that scoping is the safety property).
//! * **bare** — `"Shape"`, meaning *some type named `Shape`, declaring module
//!   unknown*. This is the pre-existing representation and remains the
//!   representation for builtins, kernel-implicit types, Go FFI types, and any
//!   reference whose declaration could not be resolved.
//!
//! A bare name never contains a `.`; a qualified one always does (module names
//! are dotted, so [`base`] takes the LAST segment). That is the whole encoding —
//! there is no new type, so nothing downstream had to change representation.
//!
//! # The rule, and why it cannot manufacture a false rejection
//!
//! [`same`] treats a BARE name as a wildcard over its base segment. Two names
//! are different ONLY when both are qualified, their bases agree, and their
//! qualifiers differ — i.e. only when the compiler resolved BOTH sides, with
//! certainty, to unions declared in two DIFFERENT modules.
//!
//! That is the entire new rejection surface. Everywhere resolution is
//! incomplete — an unimported type, a Go FFI type, an unchaseable re-export, a
//! kernel-implicit type — the name stays bare and behaves EXACTLY as it did
//! before this module existed. The discipline is deliberately the same one
//! `hir::resolve`'s [`TypeKey::Opaque`] already applies to the `[E1012]`
//! lattice: prefer a false NEGATIVE (leave today's behaviour alone) over a
//! false REJECTION (break a working program). #164 is the standing reminder of
//! what the other trade costs — a resolution fix that passed every corpus gate,
//! broke a real app, and was reverted.
//!
//! # What stays BARE, and the invariant that makes each safe
//!
//! * **Builtins and kernel-implicit types** (`Int`, `List`, `Dict`, `Task`,
//!   `Maybe`, `Result`, `Cmd`, `Sub`, `Decoder`, `Value`, …). They intern into
//!   the `BUILTIN_MOD` sentinel (`ModuleId(u32::MAX)`) rather than a real
//!   module, so [`crate::sig`]'s qualification guard skips them by construction.
//!   This is load-bearing far beyond this crate: `dictkey`'s `DICT = "Dict"`,
//!   `unify`'s `"List"` super-type rules, and `lower`'s `"Task"`/`"Cmd"`
//!   dispatch all match these names as bare strings.
//! * **`World::ctor_union` / `World::union_ctors`.** Read at exactly ONE site
//!   (`exhaustive.rs`) and only as a FALLBACK behind the `DefId`-keyed
//!   `union_members_by_def`, which is already module-correct. The fallback is
//!   reached only for builtin ctors (`True`/`False`/`Just`/`Nothing`/`Ok`/`Err`)
//!   seeded by `seed_builtin_ctors` under globally-unique bare names.
//! * **`World::aliases`** (the bare, last-writer-wins table) and the
//!   `union_names_in_module` `protect` set. Both are consulted by `expand` with
//!   BARE `App` names on the lowering path (`expand_ty` carries no module
//!   context), so their KEYS must stay bare. Their VALUES — alias bodies — now
//!   carry qualified union names, which is what `lower` wants: `goty::app_to_go`
//!   resolves a qualified name through `nominal_by_module` and falls back to the
//!   bare final segment on a miss, so it was already qualification-tolerant.
//!
//! # Rendering
//!
//! [`strip`] is applied by `Ty`'s printer, so every signature and every
//! diagnostic reads exactly as it did before. `unify` re-qualifies ONLY when the
//! two sides of a mismatch share a base — the one case where the stripped form
//! would print the useless `` type mismatch: `Shape` vs `Shape` ``.

/// The bare final segment of a nominal name: `"Conflate.Alpha.Shape"` → `"Shape"`,
/// `"Shape"` → `"Shape"`.
pub fn base(name: &str) -> &str {
    name.rsplit('.').next().unwrap_or(name)
}

/// Is this name module-qualified (as opposed to a bare final segment)?
pub fn is_qualified(name: &str) -> bool {
    name.contains('.')
}

/// Build the canonical qualified key for a type declared in `module`.
///
/// The ONE place this shape is formed for unions, and byte-identical to the
/// shape `sig`'s `alias_keys` / `alias_by_mod` already use for aliases, so the
/// two namespaces cannot drift apart.
pub fn qualify(module: &str, name: &str) -> String {
    format!("{module}.{name}")
}

/// **The** nominal identity test — are `a` and `b` the same type?
///
/// * Equal names → same (the overwhelmingly common case, and the only one that
///   existed before this module).
/// * Different bases → different (unchanged: `Customer` never met `Widget`).
/// * Same base, both qualified, different qualifier → **DIFFERENT**. This is the
///   entire new rejection surface, and it fires only when both sides resolved
///   with certainty to two different modules.
/// * Same base, either side bare → same. A bare name is "module unknown" and
///   must not manufacture a rejection out of a resolution gap.
pub fn same(a: &str, b: &str) -> bool {
    if a == b {
        return true;
    }
    if base(a) != base(b) {
        return false;
    }
    // Bases agree. Only two CONFIDENT, DIFFERENT qualifiers separate them.
    !(is_qualified(a) && is_qualified(b))
}

/// The form to print. Qualifiers are an internal identity device; a signature or
/// a diagnostic reads better — and stays byte-identical to every pre-existing
/// snapshot and oracle message — as the bare name.
pub fn strip(name: &str) -> &str {
    base(name)
}

/// Choose the representative when two names [`same`] each other but differ.
///
/// Keeps the QUALIFIED side, so a confident identity propagates through
/// inference variables instead of being erased by the first bare name it meets.
/// Without this a value that met a bare `Shape` early would stay bare and go on
/// unifying with every module's `Shape`.
pub fn most_specific<'a>(a: &'a str, b: &'a str) -> &'a str {
    if is_qualified(a) {
        a
    } else {
        b
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_repro_two_qualified_unions_are_different() {
        assert!(!same("Conflate.Alpha.Shape", "Conflate.Beta.Shape"));
    }

    #[test]
    fn identical_names_are_same() {
        assert!(same("Shape", "Shape"));
        assert!(same("Conflate.Alpha.Shape", "Conflate.Alpha.Shape"));
    }

    #[test]
    fn different_bases_are_different() {
        assert!(!same("Customer", "Widget"));
        assert!(!same("A.Customer", "A.Widget"));
    }

    #[test]
    fn a_bare_name_never_manufactures_a_rejection() {
        // The resolution-gap safety property: bare is "module unknown".
        assert!(same("Shape", "Conflate.Beta.Shape"));
        assert!(same("Conflate.Alpha.Shape", "Shape"));
    }

    #[test]
    fn base_and_qualify_round_trip() {
        assert_eq!(base("Conflate.Alpha.Shape"), "Shape");
        assert_eq!(base("Shape"), "Shape");
        // Module names are dotted; the LAST segment is the type name.
        assert_eq!(qualify("Conflate.Alpha", "Shape"), "Conflate.Alpha.Shape");
        assert_eq!(base(&qualify("Conflate.Alpha", "Shape")), "Shape");
    }

    #[test]
    fn most_specific_prefers_the_qualified_side() {
        assert_eq!(most_specific("Shape", "B.Shape"), "B.Shape");
        assert_eq!(most_specific("A.Shape", "Shape"), "A.Shape");
    }

    #[test]
    fn rendering_is_always_bare() {
        assert_eq!(strip("Conflate.Alpha.Shape"), "Shape");
        assert_eq!(strip("Int"), "Int");
    }
}
