//! **`[E2009]` — the un-derivable-codec-element check.**
//!
//! # Why this exists at CHECK time
//!
//! `Std.Codec.auto` (and its `autoCamel` / `autoBase` / `autoWith` siblings)
//! derives a codec by RUNTIME reflection over a witness record VALUE's Go type
//! (`runtime-go/rt/codec_auto.go`). Reflection reads a `List` field's element
//! type from the FIRST element of the witness list — so when the witness list is
//! EMPTY and the element type is not pinned def-locally (no `: Codec T`
//! annotation on the binding, no named record type constraining it), the element
//! type is a genuinely-free type variable. It lowers to Go `[]any`, and at decode
//! time `codec_auto.go` panics:
//!
//! > `Codec.auto: cannot decode kind interface`
//!
//! from a program that had passed `sky check`. A panic out of well-typed Sky is
//! exactly what the language promises not to do ("if it compiles, it works",
//! AGENTS.md), so the type checker refuses the program instead, with source
//! context and the two workarounds that actually fix it.
//!
//! # THE DISCRIMINATOR — only a FREE (unsolved) element fires
//!
//! The whole design is one distinction, and getting it wrong in either direction
//! is a defect:
//!
//! * A witness whose collection element solves to a CONCRETE type def-locally is
//!   FINE. `tmplCodec : Codec Tmpl = Codec.auto { items = [] }` pins the element
//!   to `Tmpl`'s field type through the annotation; a non-empty witness list
//!   (`items = [ x ]`) pins it through the element value. In both the recorded
//!   witness type carries `List Item` — an `App`, not a bare var — and this check
//!   stays SILENT. Firing here would reject working programs, which is worse than
//!   the panic this replaces.
//! * A witness whose collection element is a genuinely-FREE inference variable at
//!   final read-back (an unannotated binding with an empty witness list) is the
//!   bug. Its element reads back as an internal flex var (`t42`) — `read_back`
//!   maps `Content::Flex` to `Ty::Var("t{id}")` — or the wildcard `any`.
//!
//! # Why the internal-var filter, not "any `Ty::Var`"
//!
//! A genuinely-polymorphic codec helper — `listCodec : List a -> Codec (List a)`
//! — carries the RIGID quantifier `a` in its witness type (`read_back` maps
//! `Content::Rigid(n)` to `Ty::Var(n)`, keeping the user's name). Its caller
//! determines `a`, so the element is NOT undetermined; firing on it would break
//! every such helper. Rigid quantifier names are user-written (`a`, `msg`), never
//! the `t<digits>` / `r<digits>` shape inference assigns to a flex var, so the
//! internal-var filter ([`is_free_elem`]) fires on the free case and spares the
//! polymorphic one. This is the same fail-open instinct `dictkey` states: an
//! over-rejecting checker is worse than the panic it replaces.

use crate::Ty;
use base::Span;
use hir::{Body, Expr, ExprId, Res, SkyDb};
use std::collections::HashMap;

/// The Std.Codec `auto`-family functions whose LAST argument is a reflection
/// witness. `auto`/`autoCamel` take the witness as their only argument;
/// `autoBase : Bool -> a -> Codec a` and
/// `autoWith : List (String, Codec b) -> a -> Codec a` take it as the second.
fn witness_arg_index(func: &str) -> Option<usize> {
    match func {
        "auto" | "autoCamel" => Some(0),
        "autoBase" | "autoWith" => Some(1),
        _ => None,
    }
}

/// Is `n` an internal inference-variable name (`t42`, `r7`) — i.e. an UNSOLVED
/// flex var, not a user-written quantifier? A copy of `crate::is_internal_var`
/// (private there), kept local so this module is self-contained like `dictkey`.
fn is_internal_var(n: &str) -> bool {
    let mut chars = n.chars();
    matches!(chars.next(), Some('t') | Some('r'))
        && !n[1..].is_empty()
        && n[1..].chars().all(|c| c.is_ascii_digit())
}

/// A collection element that is not determined def-locally: an internal flex var
/// (`t42`) or the per-occurrence wildcard `any`. A concrete element (`App` /
/// record / tuple) or a user quantifier (`a`, a Rigid) is NOT free.
fn is_free_elem(t: &Ty) -> bool {
    matches!(t, Ty::Var(n) if is_internal_var(n.as_str()) || n.as_str() == "any")
}

/// If `t` is itself a collection whose element is undetermined, name the
/// container. `List a` / `Set a` / `Maybe a` key on the sole argument; `Dict k v`
/// keys on the VALUE (position 1) — the key can never decode to a var and its own
/// check is `[E2008]`.
fn collection_free_elem(t: &Ty) -> Option<&'static str> {
    let Ty::App(name, args) = t else {
        return None;
    };
    match crate::nominal::base(name.as_str()) {
        "List" if args.len() == 1 && is_free_elem(&args[0]) => Some("List"),
        "Set" if args.len() == 1 && is_free_elem(&args[0]) => Some("Set"),
        "Maybe" if args.len() == 1 && is_free_elem(&args[0]) => Some("Maybe"),
        "Dict" if args.len() == 2 && is_free_elem(&args[1]) => Some("Dict"),
        _ => None,
    }
}

/// The first undetermined-element collection reachable inside `t`, in
/// depth-first order — nested inside the witness record's fields, tuples, other
/// collections or function positions, because an element that cannot decode
/// cannot decode wherever it is written.
pub fn first_undeterminable(t: &Ty) -> Option<&'static str> {
    if let Some(kind) = collection_free_elem(t) {
        return Some(kind);
    }
    match t {
        Ty::App(_, args) | Ty::Tuple(args) => args.iter().find_map(first_undeterminable),
        Ty::Record(fields, _) => fields.iter().find_map(|(_, ft)| first_undeterminable(ft)),
        Ty::Fun(a, b) => first_undeterminable(a).or_else(|| first_undeterminable(b)),
        Ty::Var(_) | Ty::Unit | Ty::Error => None,
    }
}

/// One offending `Codec.auto`-family call.
#[derive(Clone, Debug)]
pub struct CodecElemFinding {
    pub def_name: String,
    pub span: Option<Span>,
    /// The container kind whose element is undetermined (`List` / `Set` / …).
    pub container: &'static str,
}

/// Accumulated `[E2009]` findings for a module, deduplicated per offending call
/// (mirrors `dictkey::DictKeyScan.found` — one defect, one diagnostic).
#[derive(Default)]
pub struct CodecElemScan {
    pub found: Vec<CodecElemFinding>,
}

impl CodecElemScan {
    fn add(&mut self, def_name: &str, span: Option<Span>, container: &'static str) {
        if self
            .found
            .iter()
            .any(|f| f.def_name == def_name && f.span == span)
        {
            return;
        }
        self.found.push(CodecElemFinding {
            def_name: def_name.to_string(),
            span,
            container,
        });
    }
}

/// Resolve a call's callee to `(module, func)` when it is a Std.Codec
/// `auto`-family reference — a `Res::Def` pointing at the stdlib def, or (defence
/// in depth) a `Res::Kernel`. Everything else returns `None`.
fn codec_family(callee: &Expr, sky: &dyn SkyDb) -> Option<(String, String)> {
    match callee {
        Expr::Var(Res::Def(def)) => {
            let loc = sky.def_loc(*def)?;
            Some((
                sky.module_name(loc.module).to_string(),
                loc.name.as_str().to_string(),
            ))
        }
        Expr::Var(Res::Kernel { module, func }) => {
            Some((module.as_str().to_string(), func.as_str().to_string()))
        }
        _ => None,
    }
}

/// Scan one body for `Codec.auto`-family calls whose witness has an
/// undetermined-element collection, recording each into `out`.
///
/// `expr_ty` is the def's recorded per-expression type map
/// (`Infer::recorded_expr_types`), read back AFTER inference completes — so a var
/// solved by any constraint in the def is already concrete, and only a
/// genuinely-free element remains an internal flex var.
pub fn scan_body(
    body: &Body,
    expr_ty: &HashMap<ExprId, Ty>,
    sky: &dyn SkyDb,
    def_name: &str,
    out: &mut CodecElemScan,
) {
    for (eid, expr) in body.exprs.iter() {
        let Expr::Call(callee, args) = expr else {
            continue;
        };
        let Some((module, func)) = codec_family(&body.exprs[*callee], sky) else {
            continue;
        };
        if crate::nominal::base(&module) != "Codec" {
            continue;
        }
        let Some(widx) = witness_arg_index(&func) else {
            continue;
        };
        let Some(&witness) = args.get(widx) else {
            continue;
        };
        let Some(ty) = expr_ty.get(&witness) else {
            continue;
        };
        if let Some(kind) = first_undeterminable(ty) {
            // Prefer the witness's own span (the record literal the user edits);
            // fall back to the whole call.
            let span = body.expr_span(witness).or_else(|| body.expr_span(eid));
            out.add(def_name, span, kind);
        }
    }
}

/// The `[E2009]` diagnostic body for one offending call. Names the container, the
/// runtime failure it prevents, and why an empty witness list defeats reflection.
pub fn message(container: &str) -> String {
    format!(
        "`Codec.auto` cannot derive an element codec for this `{container}` \
         field: its element type is not determined here. The witness list is \
         empty, so reflection has no element value to read the type from, and the \
         element erases to `any` — at decode time the generated code panics with \
         `Codec.auto: cannot decode kind interface`, out of a program that passed \
         `sky check`."
    )
}

/// The `Try: …` line — the workarounds that actually resolve the element.
pub fn suggestion() -> String {
    "annotate the codec binding with its concrete type so the element resolves \
     (e.g. `myCodec : Codec MyRecord`), give the witness a non-empty list, or set \
     the field's codec explicitly with `Codec.list <elementCodec>`."
        .to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use base::Name;

    fn list(elem: Ty) -> Ty {
        Ty::app("List", vec![elem])
    }
    fn flex() -> Ty {
        Ty::var("t42")
    }
    fn item() -> Ty {
        Ty::Record(vec![(Name::new("qty"), Ty::app("Int", vec![]))], None)
    }

    #[test]
    fn free_list_element_fires() {
        assert_eq!(first_undeterminable(&list(flex())), Some("List"));
        assert_eq!(first_undeterminable(&list(Ty::var("any"))), Some("List"));
    }

    #[test]
    fn concrete_list_element_is_silent() {
        assert_eq!(first_undeterminable(&list(item())), None);
        assert_eq!(first_undeterminable(&list(Ty::app("Int", vec![]))), None);
    }

    /// THE trap: a polymorphic helper carries a RIGID user quantifier, not a
    /// flex var. Firing on it would break every generic codec helper.
    #[test]
    fn rigid_quantifier_element_is_silent() {
        assert_eq!(first_undeterminable(&list(Ty::var("a"))), None);
        assert_eq!(first_undeterminable(&list(Ty::var("msg"))), None);
    }

    #[test]
    fn set_maybe_and_dict_value_fire() {
        assert_eq!(first_undeterminable(&Ty::app("Set", vec![flex()])), Some("Set"));
        assert_eq!(
            first_undeterminable(&Ty::app("Maybe", vec![flex()])),
            Some("Maybe")
        );
        assert_eq!(
            first_undeterminable(&Ty::app("Dict", vec![Ty::app("String", vec![]), flex()])),
            Some("Dict")
        );
        // A free Dict KEY is `[E2008]`'s business, not ours.
        assert_eq!(
            first_undeterminable(&Ty::app("Dict", vec![flex(), Ty::app("String", vec![])])),
            None
        );
    }

    /// A free element nested in a record field / tuple is still found — the
    /// witness the bug fires on is a record with an empty-list field.
    #[test]
    fn nested_free_element_is_found() {
        let witness = Ty::Record(
            vec![
                (Name::new("id"), Ty::app("String", vec![])),
                (Name::new("items"), list(flex())),
            ],
            None,
        );
        assert_eq!(first_undeterminable(&witness), Some("List"));
        let in_tuple = Ty::Tuple(vec![Ty::app("Int", vec![]), list(flex())]);
        assert_eq!(first_undeterminable(&in_tuple), Some("List"));
        // Module-qualified `List` name still keys on the bare tail.
        let qualified = Ty::app("Sky.Core.List.List", vec![flex()]);
        assert_eq!(first_undeterminable(&qualified), Some("List"));
    }

    #[test]
    fn witness_arg_index_matches_the_signatures() {
        assert_eq!(witness_arg_index("auto"), Some(0));
        assert_eq!(witness_arg_index("autoCamel"), Some(0));
        assert_eq!(witness_arg_index("autoBase"), Some(1));
        assert_eq!(witness_arg_index("autoWith"), Some(1));
        assert_eq!(witness_arg_index("toJson"), None);
    }

    #[test]
    fn message_names_the_container_and_the_panic() {
        let m = message("List");
        assert!(m.contains("`List`"), "{m}");
        assert!(m.contains("cannot decode kind interface"), "{m}");
    }
}
