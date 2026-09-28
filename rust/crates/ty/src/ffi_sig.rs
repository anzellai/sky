//! Go-FFI signature parsing: the pinned `skyType` string of a `sky add` binding
//! (`sky-ffi/<pkg>.kernel.json`) → the type [`Scheme`] inference instantiates at
//! a `Res::Foreign` reference.
//!
//! # What is enforced, and what is not
//!
//! Every Go-FFI call returns `Result Error a` (docs/ffi/boundary-philosophy.md).
//! This module makes the checker hold a program to that:
//!
//! * **Strict:** the `Result Error` wrapper, the arity (the top-level arrow
//!   count must equal the inspector's recorded `arity`), the primitives
//!   (`String`, `Int`, `Float`, `Bool`, `()`), `List`, `Maybe`, tuples, and the
//!   arrow structure of a callback parameter.
//! * **Wildcard `any`** (the checker's per-occurrence wildcard) wherever the
//!   pinned string does not describe the runtime value truthfully:
//!   - Go-opaque types — `Name@pkg`, a bare unknown name, a lower-case
//!     (unexported or named-parameter) name. Payload soundness for these needs
//!     nominal opaque types plus an implements axiom; that is a later tier.
//!   - a callback's RESULT position — the `reflect.MakeFunc` adapter accepts a
//!     Sky closure whatever it returns (`lower.rs`, `ffi_call`);
//!   - a zero-parameter callback `( -> R)` — Sky has no spelling for it;
//!   - a function-typed value anywhere else (a payload, a list element): the
//!     runtime hands back a raw Go func;
//!   - `Bytes` — the typed wrapper returns a raw `[]byte` (`gen_bindings.rs`);
//!   - `Dict` — the generator renders EVERY Go map as `Dict String V`, whatever
//!     its key type (`ffi::gen::go_type_to_sky`), so the key is not trustworthy;
//!   - `error` — the generator maps it to `String`, which the runtime value is
//!     not;
//!   - a 2-tuple payload ending in `Bool` — older surfaces render the comma-ok
//!     result `(T, bool)` as `(T, Bool)` while the typed wrapper returns
//!     `Maybe T`, and `(T, bool, error)` renders the same way;
//!   - Go array residue (`[32]byte`, `[]string`) and `complex*`.
//! * **Go residue normalised:** `int*` / `uint*` / `byte` / `rune` / `uintptr` /
//!   `untyped int` → `Int`, `float*` → `Float`, `string` → `String`, `bool` →
//!   `Bool`.
//! * A single capital letter is a type variable (`ptr : T -> Result Error T`).
//! * The error type is emitted QUALIFIED (`Sky.Core.Error.Error`), so a user's
//!   own `type Error` never unifies with it (`nominal::same`).
//!
//! A string that cannot be parsed — or whose arrow count disagrees with the
//! recorded arity, or which lacks the `Result Error` wrapper — gets the
//! arity-only scheme `any -> … -> Result Error any`: the wrapper stays enforced
//! even when nothing else can be read. A parameter or payload that fails to
//! parse on its own (the inspector emits a few unbalanced strings such as
//! `( -> ReadCloser, error))`) becomes `any` while the rest of the signature
//! stays typed. None of this is a user warning: a user cannot fix inspector
//! output. [`census`] counts the fallbacks for `-v` / docs.
//!
//! Parsing is LAZY: inference parses one string when a `Res::Foreign` reference
//! to it is first instantiated (`Infer`'s per-run memo), never the whole
//! surface.

use crate::{Scheme, Ty};
use base::Name;

/// The qualified name of the error type an FFI result carries.
pub const FFI_ERROR_TYPE: &str = "Sky.Core.Error.Error";

/// How a pinned signature became a scheme.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FfiSchemeKind {
    /// The `skyType` parsed; its arity matched and it carries the wrapper.
    Parsed,
    /// The `skyType` was empty (the inspector omitted it).
    FallbackMissing,
    /// The `skyType` could not be read as a whole (tokens, arity, or wrapper).
    FallbackUnparseable,
}

/// The scheme for one pinned Go-FFI function, plus how it was obtained.
#[derive(Clone, Debug)]
pub struct FfiScheme {
    pub scheme: Scheme,
    pub kind: FfiSchemeKind,
}

/// The scheme inference instantiates for a Go-FFI function with this pinned
/// `sky_type` and inspector `arity`.
pub fn scheme_for(sky_type: &str, arity: usize) -> FfiScheme {
    if sky_type.trim().is_empty() {
        return FfiScheme {
            scheme: fallback(arity),
            kind: FfiSchemeKind::FallbackMissing,
        };
    }
    match parse(sky_type, arity) {
        Some(scheme) => FfiScheme {
            scheme,
            kind: FfiSchemeKind::Parsed,
        },
        None => FfiScheme {
            scheme: fallback(arity),
            kind: FfiSchemeKind::FallbackUnparseable,
        },
    }
}

/// The arity-only scheme: `any -> … -> Result Error any` with `arity`
/// parameters. The `Result` wrapper is still enforced.
pub fn fallback(arity: usize) -> Scheme {
    let mut ty = result_of(wild());
    for _ in 0..arity {
        ty = Ty::Fun(Box::new(wild()), Box::new(ty));
    }
    Scheme::mono(ty)
}

/// The number of top-level parameters a `skyType` string spells, or `None` when
/// its parentheses do not balance. Used where no inspector arity is at hand
/// (the reject corpus's `-- ffi:` directive).
pub fn spelled_arity(sky_type: &str) -> Option<usize> {
    let toks = tokenise(sky_type)?;
    let segs = split_top(&toks);
    if segs.iter().any(|s| s.garbled) {
        return None;
    }
    Some(segs.len().saturating_sub(1))
}

/// Census of a whole surface: `(parsed, missing, unparseable)`. For `-v` / docs
/// reporting only — inference never parses a whole surface.
pub fn census<'a>(sigs: impl Iterator<Item = (&'a str, usize)>) -> (usize, usize, usize) {
    let (mut ok, mut missing, mut bad) = (0, 0, 0);
    for (s, arity) in sigs {
        match scheme_for(s, arity).kind {
            FfiSchemeKind::Parsed => ok += 1,
            FfiSchemeKind::FallbackMissing => missing += 1,
            FfiSchemeKind::FallbackUnparseable => bad += 1,
        }
    }
    (ok, missing, bad)
}

/// Build a Go-FFI surface from `-- ffi:` header directives in test sources
/// (the reject corpus and the checker's own tests), so a program can be checked
/// against a pinned binding without a `sky-ffi/` directory:
///
/// ```text
/// -- ffi: Pkg read : String -> Int -> Int -> Result Error (String, Int)
/// -- ffi: Pkg noType/2 :
/// ```
///
/// `Package name : skyType`, one per line. The arity is the spelled top-level
/// parameter count, or an explicit `/N` suffix on the name (needed when the
/// skyType is empty or deliberately malformed).
pub fn surface_from_directives(src: &str) -> hir::FfiSurface {
    let mut out = hir::FfiSurface::new();
    for line in src.lines() {
        let Some(rest) = line.trim_start().strip_prefix("-- ffi:") else {
            continue;
        };
        let Some((head, sky)) = rest.split_once(':') else {
            continue;
        };
        let mut words = head.split_whitespace();
        let (Some(pkg), Some(name)) = (words.next(), words.next()) else {
            continue;
        };
        let sky = sky.trim();
        let (name, arity) = match name.split_once('/') {
            Some((n, a)) => (n, a.parse().unwrap_or(0)),
            None => (name, spelled_arity(sky).unwrap_or(0)),
        };
        out.insert_fn(
            pkg,
            name,
            hir::FfiFnSig {
                arity,
                sky_type: std::sync::Arc::from(sky),
            },
        );
    }
    out
}

/// [`surface_from_directives`] over every module of a case, merged — the
/// shape the reject corpus and the shared-world differential share, so both of
/// the differential's paths check a case against the same pinned bindings.
/// `None` when no module carries a directive.
pub fn surface_from_parses(
    modules: &[(String, syntax::Parse)],
) -> Option<std::sync::Arc<hir::FfiSurface>> {
    let mut out = hir::FfiSurface::new();
    for (_, parse) in modules {
        for (p, n, sig) in surface_from_directives(&parse.reprint()).iter() {
            out.insert_fn(p, n, sig.clone());
        }
    }
    (!out.is_empty()).then(|| std::sync::Arc::new(out))
}

/// Parse a full `skyType` against its recorded arity. `None` means "use the
/// arity-only fallback".
pub fn parse(sky_type: &str, arity: usize) -> Option<Scheme> {
    let toks = tokenise(sky_type)?;
    let segs = split_top(&toks);
    // The final segment is the result; everything before it is a parameter.
    let (last, params) = segs.split_last()?;
    if params.len() != arity {
        return None;
    }
    // The wrapper: the result segment must read `Result Error <payload>`.
    if last.garbled {
        // An unbalanced result segment still carries a readable wrapper when
        // it starts `Result Error`; only the payload is lost.
        if !starts_with_wrapper(&last.toks) {
            return None;
        }
    } else if !starts_with_wrapper(&last.toks) {
        return None;
    }
    let mut tv = TyVars::default();
    let payload_toks = &last.toks[2..];
    let payload = if last.garbled || payload_toks.is_empty() {
        wild()
    } else {
        match parse_full(payload_toks) {
            Some(raw) => conv(&raw, Ctx::Payload, &mut tv),
            None => wild(),
        }
    };
    let mut ty = result_of(payload);
    for seg in params.iter().rev() {
        let p = if seg.garbled {
            wild()
        } else {
            match parse_full(&seg.toks) {
                Some(raw) => conv(&raw, Ctx::Param, &mut tv),
                None => wild(),
            }
        };
        ty = Ty::Fun(Box::new(p), Box::new(ty));
    }
    Some(Scheme { vars: tv.names, ty })
}

fn starts_with_wrapper(toks: &[Tok]) -> bool {
    matches!(toks, [Tok::Ident(r), Tok::Ident(e), ..] if r == "Result" && e == "Error")
}

fn wild() -> Ty {
    Ty::var("any")
}

fn result_of(payload: Ty) -> Ty {
    Ty::app("Result", vec![Ty::app(FFI_ERROR_TYPE, vec![]), payload])
}

// ---- tokens ------------------------------------------------------------------

#[derive(Clone, Debug, PartialEq, Eq)]
enum Tok {
    Arrow,
    LParen,
    RParen,
    Comma,
    LBrack,
    RBrack,
    Ident(String),
}

/// `None` on a character no skyType legitimately contains.
fn tokenise(s: &str) -> Option<Vec<Tok>> {
    let chars: Vec<char> = s.chars().collect();
    let mut out = Vec::new();
    let mut i = 0;
    while i < chars.len() {
        let c = chars[i];
        match c {
            ' ' | '\t' | '\n' | '\r' => i += 1,
            '(' => {
                out.push(Tok::LParen);
                i += 1;
            }
            ')' => {
                out.push(Tok::RParen);
                i += 1;
            }
            ',' => {
                out.push(Tok::Comma);
                i += 1;
            }
            '[' => {
                out.push(Tok::LBrack);
                i += 1;
            }
            ']' => {
                out.push(Tok::RBrack);
                i += 1;
            }
            '-' if chars.get(i + 1) == Some(&'>') => {
                out.push(Tok::Arrow);
                i += 2;
            }
            c if is_ident_char(c) => {
                let start = i;
                while i < chars.len() {
                    let d = chars[i];
                    // `-` belongs to an identifier (a Go module path such as
                    // `github.com/go-chi/chi`) unless it opens an arrow.
                    if d == '-' && chars.get(i + 1) != Some(&'>') {
                        i += 1;
                        continue;
                    }
                    if !is_ident_char(d) {
                        break;
                    }
                    i += 1;
                }
                out.push(Tok::Ident(chars[start..i].iter().collect()));
            }
            _ => return None,
        }
    }
    Some(out)
}

fn is_ident_char(c: char) -> bool {
    c.is_alphanumeric() || matches!(c, '_' | '.' | '/' | '@' | '*')
}

/// One top-level (depth-0) arrow segment. `garbled` marks a stray closing
/// bracket at depth 0 or a bracket left open at the end — the inspector emits a
/// few such strings; the segment then reads as `any`.
struct Seg {
    toks: Vec<Tok>,
    garbled: bool,
}

fn split_top(toks: &[Tok]) -> Vec<Seg> {
    let mut segs = Vec::new();
    let mut cur = Seg {
        toks: Vec::new(),
        garbled: false,
    };
    let mut depth: usize = 0;
    for t in toks {
        match t {
            Tok::LParen | Tok::LBrack => {
                depth += 1;
                cur.toks.push(t.clone());
            }
            Tok::RParen | Tok::RBrack => {
                if depth == 0 {
                    cur.garbled = true;
                } else {
                    depth -= 1;
                }
                cur.toks.push(t.clone());
            }
            Tok::Arrow if depth == 0 => {
                segs.push(std::mem::replace(
                    &mut cur,
                    Seg {
                        toks: Vec::new(),
                        garbled: false,
                    },
                ));
            }
            _ => cur.toks.push(t.clone()),
        }
    }
    if depth != 0 {
        cur.garbled = true;
    }
    segs.push(cur);
    segs
}

// ---- the raw type grammar -----------------------------------------------------

#[derive(Clone, Debug)]
enum Raw {
    /// `Head arg…` (a bare name has no args).
    Name(String, Vec<Raw>),
    Fun(Box<Raw>, Box<Raw>),
    /// `( -> R)` — a zero-parameter Go func.
    ZeroFun,
    Tuple(Vec<Raw>),
    Unit,
    /// `[N]T` / `[]T` Go residue.
    GoArray,
}

/// Parse a whole token slice as one type; `None` unless every token is used.
fn parse_full(toks: &[Tok]) -> Option<Raw> {
    let mut p = P { toks, pos: 0 };
    let t = p.ty()?;
    (p.pos == toks.len()).then_some(t)
}

struct P<'a> {
    toks: &'a [Tok],
    pos: usize,
}

impl P<'_> {
    fn peek(&self) -> Option<&Tok> {
        self.toks.get(self.pos)
    }
    fn eat(&mut self, t: &Tok) -> bool {
        if self.peek() == Some(t) {
            self.pos += 1;
            true
        } else {
            false
        }
    }
    fn ty(&mut self) -> Option<Raw> {
        let lhs = self.app()?;
        if self.eat(&Tok::Arrow) {
            let rhs = self.ty()?;
            return Some(Raw::Fun(Box::new(lhs), Box::new(rhs)));
        }
        Some(lhs)
    }
    fn app(&mut self) -> Option<Raw> {
        if let Some(Tok::Ident(h)) = self.peek().cloned() {
            self.pos += 1;
            let mut args = Vec::new();
            while matches!(
                self.peek(),
                Some(Tok::Ident(_)) | Some(Tok::LParen) | Some(Tok::LBrack)
            ) {
                args.push(self.atom()?);
            }
            return Some(Raw::Name(h, args));
        }
        self.atom()
    }
    fn atom(&mut self) -> Option<Raw> {
        match self.peek().cloned()? {
            Tok::Ident(h) => {
                self.pos += 1;
                Some(Raw::Name(h, Vec::new()))
            }
            Tok::LParen => {
                self.pos += 1;
                if self.eat(&Tok::RParen) {
                    return Some(Raw::Unit);
                }
                if self.eat(&Tok::Arrow) {
                    let _r = self.ty()?;
                    return self.eat(&Tok::RParen).then_some(Raw::ZeroFun);
                }
                let first = self.ty()?;
                if self.eat(&Tok::RParen) {
                    return Some(first);
                }
                let mut items = vec![first];
                while self.eat(&Tok::Comma) {
                    items.push(self.ty()?);
                }
                self.eat(&Tok::RParen).then_some(Raw::Tuple(items))
            }
            Tok::LBrack => {
                self.pos += 1;
                if let Some(Tok::Ident(_)) = self.peek() {
                    self.pos += 1;
                }
                if !self.eat(&Tok::RBrack) {
                    return None;
                }
                let _elem = self.atom()?;
                Some(Raw::GoArray)
            }
            _ => None,
        }
    }
}

// ---- raw → Ty -------------------------------------------------------------------

/// Where a type sits in the signature — decides which positions are
/// wildcards.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Ctx {
    /// A top-level parameter: a function here is a CALLBACK.
    Param,
    /// The `Result Error` payload itself.
    Payload,
    /// Inside a callback's parameter list, or inside a container.
    Inner,
}

#[derive(Default)]
struct TyVars {
    names: Vec<Name>,
}

impl TyVars {
    fn var(&mut self, letter: &str) -> Ty {
        let n = format!("ffi{}", letter.to_lowercase());
        let name = Name::new(&n);
        if !self.names.contains(&name) {
            self.names.push(name.clone());
        }
        Ty::Var(name)
    }
}

fn conv(raw: &Raw, ctx: Ctx, tv: &mut TyVars) -> Ty {
    match raw {
        Raw::Unit => Ty::Unit,
        Raw::GoArray | Raw::ZeroFun => wild(),
        Raw::Fun(..) => {
            if ctx != Ctx::Param {
                return wild();
            }
            // A callback: its parameters are typed, its RESULT is a wildcard
            // (the MakeFunc adapter accepts a closure returning anything).
            let mut params = Vec::new();
            let mut cur = raw;
            while let Raw::Fun(a, b) = cur {
                params.push(conv(a, Ctx::Inner, tv));
                cur = b;
            }
            let mut ty = wild();
            for p in params.into_iter().rev() {
                ty = Ty::Fun(Box::new(p), Box::new(ty));
            }
            ty
        }
        Raw::Tuple(items) => {
            if !(2..=3).contains(&items.len()) {
                return wild();
            }
            if ctx == Ctx::Payload && items.len() == 2 && is_bool(&items[1]) && !is_bool(&items[0])
            {
                // Legacy comma-ok rendering: `(T, Bool)` may be a `Maybe T`.
                return wild();
            }
            Ty::Tuple(items.iter().map(|x| conv(x, Ctx::Inner, tv)).collect())
        }
        Raw::Name(head, args) => conv_name(head, args, tv),
    }
}

fn is_bool(r: &Raw) -> bool {
    matches!(r, Raw::Name(h, a) if a.is_empty() && (h == "Bool" || h == "bool"))
}

fn conv_name(head: &str, args: &[Raw], tv: &mut TyVars) -> Ty {
    match (head, args) {
        ("List", [x]) => Ty::app("List", vec![conv(x, Ctx::Inner, tv)]),
        ("Maybe", [x]) => Ty::app("Maybe", vec![conv(x, Ctx::Inner, tv)]),
        ("untyped", [Raw::Name(k, a)]) if a.is_empty() => match k.as_str() {
            "int" | "rune" => Ty::app("Int", vec![]),
            "float" => Ty::app("Float", vec![]),
            "string" => Ty::app("String", vec![]),
            "bool" => Ty::app("Bool", vec![]),
            _ => wild(),
        },
        (h, []) => match primitive(h) {
            Some(p) => Ty::app(p, vec![]),
            None if is_type_var(h) => tv.var(h),
            None => wild(),
        },
        // Any other application — `Dict …`, `Result …` nested, an opaque
        // generic, a named callback parameter (`network string`) — is a
        // wildcard.
        _ => wild(),
    }
}

/// The Sky primitive a pinned name denotes, normalising Go residue.
fn primitive(h: &str) -> Option<&'static str> {
    Some(match h {
        "String" | "string" => "String",
        "Int" | "int" | "int8" | "int16" | "int32" | "int64" | "uint" | "uint8" | "uint16"
        | "uint32" | "uint64" | "uintptr" | "byte" | "rune" => "Int",
        "Float" | "float32" | "float64" => "Float",
        "Bool" | "bool" => "Bool",
        _ => return None,
    })
}

fn is_type_var(h: &str) -> bool {
    let mut cs = h.chars();
    matches!((cs.next(), cs.next()), (Some(c), None) if c.is_ascii_uppercase())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wrapper_primitives_and_tuples_are_strict() {
        let t = scheme_for("String -> Int -> Int -> Result Error (String, Int)", 3);
        assert_eq!(t.kind, FfiSchemeKind::Parsed);
        let want = Ty::Fun(
            Box::new(Ty::app("String", vec![])),
            Box::new(Ty::Fun(
                Box::new(Ty::app("Int", vec![])),
                Box::new(Ty::Fun(
                    Box::new(Ty::app("Int", vec![])),
                    Box::new(Ty::app(
                        "Result",
                        vec![
                            Ty::app(FFI_ERROR_TYPE, vec![]),
                            Ty::Tuple(vec![Ty::app("String", vec![]), Ty::app("Int", vec![])]),
                        ],
                    )),
                )),
            )),
        );
        assert_eq!(t.scheme.ty, want);
    }

    #[test]
    fn error_type_is_qualified() {
        let s = scheme_for("() -> Result Error String", 1).scheme;
        let Ty::Fun(_, r) = &s.ty else { panic!() };
        let Ty::App(n, args) = r.as_ref() else {
            panic!()
        };
        assert_eq!(n.as_str(), "Result");
        assert_eq!(args[0], Ty::app("Sky.Core.Error.Error", vec![]));
    }

    #[test]
    fn go_residue_normalises() {
        for (go, sky) in [
            ("int", "Int"),
            ("int64", "Int"),
            ("uint16", "Int"),
            ("byte", "Int"),
            ("rune", "Int"),
            ("uintptr", "Int"),
            ("float32", "Float"),
            ("float64", "Float"),
            ("string", "String"),
            ("bool", "Bool"),
            ("(untyped int)", "Int"),
            ("(untyped string)", "String"),
            ("(untyped float)", "Float"),
        ] {
            let s = scheme_for(&format!("{go} -> Result Error {go}"), 1).scheme;
            assert_eq!(
                s.ty,
                Ty::Fun(
                    Box::new(Ty::app(sky, vec![])),
                    Box::new(result_of(Ty::app(sky, vec![])))
                ),
                "{go}"
            );
        }
    }

    #[test]
    fn wildcards() {
        for (src, arity) in [
            ("UUID@github.com/google/uuid -> Result Error ()", 1),
            ("Request -> Result Error ()", 1),
            ("network -> Result Error ()", 1),
            ("Bytes -> Result Error ()", 1),
            ("any -> Result Error ()", 1),
            ("error -> Result Error ()", 1),
            ("Dict String String -> Result Error ()", 1),
            ("complex128 -> Result Error ()", 1),
            ("( -> ()) -> Result Error ()", 1),
        ] {
            let s = scheme_for(src, arity);
            assert_eq!(s.kind, FfiSchemeKind::Parsed, "{src}");
            let Ty::Fun(p, _) = &s.scheme.ty else {
                panic!("{src}")
            };
            assert_eq!(**p, wild(), "{src}: param must be the wildcard");
        }
        // payload wildcards
        for src in [
            "() -> Result Error Bytes",
            "() -> Result Error (Dict String String)",
            "() -> Result Error (Request -> Request -> String)",
            "() -> Result Error [32]byte",
            "() -> Result Error (String, Bool)",
            "() -> Result Error Route@github.com/gorilla/mux",
        ] {
            let s = scheme_for(src, 1).scheme;
            let Ty::Fun(_, r) = &s.ty else { panic!() };
            assert_eq!(**r, result_of(wild()), "{src}");
        }
    }

    #[test]
    fn callback_params_typed_result_wild() {
        let s = scheme_for(
            "Router@github.com/gorilla/mux -> String -> (ResponseWriter -> String -> ()) -> Result Error ()",
            3,
        )
        .scheme;
        let Ty::Fun(_, r) = &s.ty else { panic!() };
        let Ty::Fun(_, r) = r.as_ref() else { panic!() };
        let Ty::Fun(cb, _) = r.as_ref() else { panic!() };
        assert_eq!(
            **cb,
            Ty::Fun(
                Box::new(wild()),
                Box::new(Ty::Fun(
                    Box::new(Ty::app("String", vec![])),
                    Box::new(wild())
                ))
            )
        );
    }

    #[test]
    fn list_maybe_and_type_vars() {
        let s = scheme_for("List String -> Result Error (Maybe Int)", 1).scheme;
        assert_eq!(
            s.ty,
            Ty::Fun(
                Box::new(Ty::app("List", vec![Ty::app("String", vec![])])),
                Box::new(result_of(Ty::app("Maybe", vec![Ty::app("Int", vec![])])))
            )
        );
        let s = scheme_for("T -> Result Error T", 1).scheme;
        assert_eq!(s.vars.len(), 1);
        assert_eq!(
            s.ty,
            Ty::Fun(
                Box::new(Ty::Var(s.vars[0].clone())),
                Box::new(result_of(Ty::Var(s.vars[0].clone())))
            )
        );
    }

    #[test]
    fn unbalanced_segments_degrade_locally() {
        // A stray `)` in the payload keeps the params typed.
        let s = scheme_for("URL@net/url -> Result Error (Request -> URL, error))", 1);
        assert_eq!(s.kind, FfiSchemeKind::Parsed);
        assert_eq!(
            s.scheme.ty,
            Ty::Fun(Box::new(wild()), Box::new(result_of(wild())))
        );
        // A garbled PARAMETER becomes any; the others stay typed.
        let s = scheme_for("( -> ReadCloser, error)) -> String -> Result Error Int", 2);
        assert_eq!(s.kind, FfiSchemeKind::Parsed);
        assert_eq!(
            s.scheme.ty,
            Ty::Fun(
                Box::new(wild()),
                Box::new(Ty::Fun(
                    Box::new(Ty::app("String", vec![])),
                    Box::new(result_of(Ty::app("Int", vec![])))
                ))
            )
        );
    }

    #[test]
    fn fallback_keeps_the_result() {
        for (src, arity, kind) in [
            ("", 2, FfiSchemeKind::FallbackMissing),
            (
                "String -> Result Error Int",
                2,
                FfiSchemeKind::FallbackUnparseable,
            ),
            ("String -> Int", 1, FfiSchemeKind::FallbackUnparseable),
            (
                "String -> Result Error Int ;",
                1,
                FfiSchemeKind::FallbackUnparseable,
            ),
        ] {
            let s = scheme_for(src, arity);
            assert_eq!(s.kind, kind, "{src:?}");
            let mut want = result_of(wild());
            for _ in 0..arity {
                want = Ty::Fun(Box::new(wild()), Box::new(want));
            }
            assert_eq!(s.scheme.ty, want, "{src:?}");
        }
    }

    #[test]
    fn spelled_arity_counts_top_level_arrows() {
        assert_eq!(spelled_arity("String -> Int -> Result Error ()"), Some(2));
        assert_eq!(spelled_arity("(A -> B) -> Result Error ()"), Some(1));
        assert_eq!(spelled_arity("() -> Result Error ()"), Some(1));
        assert_eq!(spelled_arity("( -> X, error)) -> Result Error ()"), None);
    }
}
