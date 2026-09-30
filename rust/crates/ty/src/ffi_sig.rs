//! Go-FFI signature parsing: the pinned `skyType` string of a `sky add` binding
//! (`sky-ffi/<pkg>.kernel.json`) → the type [`Scheme`] inference instantiates at
//! a `Res::Foreign` reference.
//!
//! # What is enforced (surface format 3)
//!
//! Every Go-FFI call returns `Result Error a` (docs/ffi/boundary-philosophy.md),
//! and every position of the signature is typed. There is no unchecked
//! wildcard:
//!
//! * the `Result Error` wrapper, the arity (the top-level arrow count must
//!   equal the inspector's recorded `arity`), the primitives, `List`, `Maybe`,
//!   `Dict` with its real key type (a Go `map[int]V` is `Dict Int V`, C-5),
//!   tuples, and `Bytes` (a Sky String);
//! * a Go pointer to a non-opaque type is `Maybe` (C-4);
//! * an opaque Go type is the NOMINAL `go@<Sky module>.<Name>`
//!   ([`crate::nominal::go_type`], the key a `Pkg.Name` annotation gets), so an
//!   opaque value is never usable as an `Int` or as another Go type (C-6);
//!   unnamed opaque shapes are `go@Go.GoFunc`, `go@Go.GoAny`, `go@Go.GoMap`, …;
//! * a callback's parameters AND result are typed from the pin, so a closure
//!   returning the wrong type is a type error, not a `go build` failure
//!   (C-12); a zero-parameter Go callback is `() -> r`;
//! * a type variable exists only where the surface spells one (`$T`, a
//!   format-3 generic); a bare capital letter is never one.
//!
//! Two parameter positions accept any Sky value, both CHECKED:
//!
//! * an empty Go interface (`any`, `driver.Value`): Go itself accepts every
//!   value there;
//! * a non-empty Go interface (`goi@…`, `io.Writer`): [`crate::ffi_iface`]
//!   rejects a Sky-native argument after solving, and the wrapper asserts the
//!   Go value inside its guard, so a value that does not implement the
//!   interface is an `Err`, never a crash.
//!
//! Integer widths and signedness are checked by the wrapper
//! (`runtime-go/rt/ffi_convert.go`): an out-of-range value is an `Err` (C-7).
//!
//! A string that cannot be parsed — or whose arrow count disagrees with the
//! recorded arity, or which lacks the `Result Error` wrapper — gets the
//! arity-only scheme `any -> … -> Result Error go@Go.GoUnknown`: the result is
//! opaque, and the wrapper (reflective, `SkyFfiReflectCall3`) converts every
//! argument inside its guard. [`census`] counts the fallbacks; [`wildcard_census`]
//! counts the unchecked positions.
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
    let mut ty = result_of(opaque("Unknown"));
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
        opaque("Unknown")
    } else {
        match parse_full(payload_toks) {
            Some(raw) => conv(&raw, Dir::Out, false, &mut tv),
            None => opaque("Unknown"),
        }
    };
    let mut ty = result_of(payload);
    for seg in params.iter().rev() {
        let p = if seg.garbled {
            wild()
        } else {
            match parse_full(&seg.toks) {
                Some(raw) => conv(&raw, Dir::In, true, &mut tv),
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
    c.is_alphanumeric() || matches!(c, '_' | '.' | '/' | '@' | '*' | '$')
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
    /// `( -> R)` — a zero-parameter Go func (a format-2 spelling).
    ZeroFun(Box<Raw>),
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
                    let r = self.ty()?;
                    return self.eat(&Tok::RParen).then_some(Raw::ZeroFun(Box::new(r)));
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

/// Which way a value crosses the boundary. `In`: Sky hands it to Go (a
/// parameter, a callback's result). `Out`: Go hands it to Sky (the payload, a
/// callback's argument). Only an `In` interface is checked at the call
/// (a wildcard here, see [`iface_params`]); an `Out` one is an opaque nominal.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Dir {
    In,
    Out,
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

/// An opaque Go type the surface names by kind (`go@Go.GoFunc`, …).
fn opaque(kind: &str) -> Ty {
    Ty::app(
        &format!("{}Go.Go{kind}", crate::nominal::GO_TYPE_PREFIX),
        vec![],
    )
}

/// `top`: a top-level parameter, the only position a func is a callback.
fn conv(raw: &Raw, dir: Dir, top: bool, tv: &mut TyVars) -> Ty {
    match raw {
        Raw::Unit => Ty::Unit,
        Raw::GoArray => opaque("Unknown"),
        Raw::ZeroFun(r) => {
            if dir == Dir::In && top {
                Ty::Fun(Box::new(Ty::Unit), Box::new(conv(r, Dir::In, false, tv)))
            } else {
                opaque("Func")
            }
        }
        Raw::Fun(..) => {
            if !(dir == Dir::In && top) {
                return opaque("Func");
            }
            // A callback: Go hands its arguments to Sky (`Out`), and Sky
            // hands its result back to Go (`In`) — typed from the pin, so a
            // closure returning the wrong type is a type error (C-12).
            let mut params = Vec::new();
            let mut cur = raw;
            while let Raw::Fun(a, b) = cur {
                params.push(conv(a, Dir::Out, false, tv));
                cur = b;
            }
            let mut ty = conv(cur, Dir::In, false, tv);
            for p in params.into_iter().rev() {
                ty = Ty::Fun(Box::new(p), Box::new(ty));
            }
            ty
        }
        Raw::Tuple(items) => {
            if !(2..=3).contains(&items.len()) {
                return opaque("Tuple");
            }
            Ty::Tuple(items.iter().map(|x| conv(x, dir, false, tv)).collect())
        }
        Raw::Name(head, args) => conv_name(head, args, dir, tv),
    }
}

fn conv_name(head: &str, args: &[Raw], dir: Dir, tv: &mut TyVars) -> Ty {
    match (head, args) {
        ("List", [x]) => Ty::app("List", vec![conv(x, dir, false, tv)]),
        ("Maybe", [x]) => Ty::app("Maybe", vec![conv(x, dir, false, tv)]),
        ("Dict", [k, v]) => Ty::app(
            "Dict",
            vec![conv(k, dir, false, tv), conv(v, dir, false, tv)],
        ),
        // A callback's `Result Error a` result (`func(…) error`).
        ("Result", [Raw::Name(e, ea), x]) if e == "Error" && ea.is_empty() => {
            result_of(conv(x, dir, false, tv))
        }
        ("untyped", [Raw::Name(k, a)]) if a.is_empty() => match k.as_str() {
            "int" | "rune" => Ty::app("Int", vec![]),
            "float" => Ty::app("Float", vec![]),
            "string" => Ty::app("String", vec![]),
            "bool" => Ty::app("Bool", vec![]),
            _ => opaque("Unknown"),
        },
        (h, []) => conv_atom(h, dir, tv),
        _ => opaque("Unknown"),
    }
}

fn conv_atom(h: &str, dir: Dir, tv: &mut TyVars) -> Ty {
    if let Some(p) = primitive(h) {
        return Ty::app(p, vec![]);
    }
    let go = crate::nominal::GO_TYPE_PREFIX;
    // A non-empty interface: a parameter accepts any Go value here, and the
    // call is checked after solving (`crate::ffi_iface`) and again at run
    // time inside the wrapper's guard. A value Go returns is its nominal.
    if let Some(key) = h.strip_prefix("goi@") {
        return match dir {
            Dir::In => wild(),
            Dir::Out => Ty::app(&format!("{go}{key}"), vec![]),
        };
    }
    if h.starts_with(go) {
        return Ty::app(h, vec![]);
    }
    if let Some(v) = h.strip_prefix('$') {
        return tv.var(v);
    }
    match h {
        // An empty interface: a parameter accepts anything (Go does); a value
        // Go returns is an opaque Go value.
        "any" => match dir {
            Dir::In => wild(),
            Dir::Out => opaque("Any"),
        },
        "error" => opaque("Error"),
        _ => {
            // A format-2 opaque marker `Name@importPath`: its nominal key.
            if let Some((name, path)) = h.split_once('@') {
                if !name.is_empty() {
                    return Ty::app(
                        &format!("{go}{}.{name}", module_of_import_path(path)),
                        vec![],
                    );
                }
            }
            // A bare name the surface did not qualify (a format-2 string):
            // opaque, never a wildcard and never a type variable.
            opaque("Unknown")
        }
    }
}

/// The Sky module path `sky add` binds a Go import path to
/// (`github.com/stripe/stripe-go/v84` → `Github.Com.Stripe.StripeGo.V84`).
/// The same transform as `ffi::gen::pkg_to_module_name`, which a format-3
/// surface has already applied; only a format-2 marker needs it here.
fn module_of_import_path(path: &str) -> String {
    let mut segs = Vec::new();
    for slash in path.split('/') {
        for dot in slash.split('.') {
            let mut s = String::new();
            let mut up = false;
            for c in dot.chars() {
                if c == '-' {
                    up = true;
                } else if up {
                    s.extend(c.to_uppercase());
                    up = false;
                } else if c.is_alphanumeric() {
                    s.push(c);
                } else {
                    s.push('_');
                }
            }
            if !s.is_empty() {
                let mut cs = s.chars();
                let first = cs.next().unwrap();
                segs.push(first.to_uppercase().chain(cs).collect::<String>());
            }
        }
    }
    segs.join(".")
}

/// The Sky primitive a pinned name denotes, normalising Go residue.
fn primitive(h: &str) -> Option<&'static str> {
    Some(match h {
        // `Bytes` is a Sky String whose bytes need not be UTF-8
        // (`Sky.Core.Bytes`); the wrapper converts `[]byte` / `[N]byte`.
        "String" | "string" | "Bytes" => "String",
        "Int" | "int" | "int8" | "int16" | "int32" | "int64" | "uint" | "uint8" | "uint16"
        | "uint32" | "uint64" | "uintptr" | "byte" | "rune" => "Int",
        "Float" | "float32" | "float64" => "Float",
        "Bool" | "bool" => "Bool",
        _ => return None,
    })
}

/// Does a pinned signature take a callback (a parenthesised function
/// parameter)?
pub fn has_callback_param(sky_type: &str, arity: usize) -> bool {
    let Some(toks) = tokenise(sky_type) else {
        return false;
    };
    let segs = split_top(&toks);
    let Some((_, params)) = segs.split_last() else {
        return false;
    };
    params.len() == arity
        && params
            .iter()
            .any(|s| !s.garbled && s.toks.contains(&Tok::Arrow))
}

/// Which top-level parameters of a pinned signature are non-empty Go
/// interfaces (`goi@…`): the call sites [`crate::ffi_iface`] checks after
/// solving. `None` when the string does not parse.
pub fn iface_params(sky_type: &str, arity: usize) -> Option<Vec<Option<String>>> {
    let toks = tokenise(sky_type)?;
    let segs = split_top(&toks);
    let (_, params) = segs.split_last()?;
    if params.len() != arity {
        return None;
    }
    Some(
        params
            .iter()
            .map(|seg| match seg.toks.as_slice() {
                [Tok::Ident(h)] => h.strip_prefix("goi@").map(str::to_string),
                _ => None,
            })
            .collect(),
    )
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

    fn p1(src: &str) -> Ty {
        let s = scheme_for(src, 1);
        assert_eq!(s.kind, FfiSchemeKind::Parsed, "{src}");
        let Ty::Fun(p, _) = &s.scheme.ty else {
            panic!("{src}")
        };
        (**p).clone()
    }

    fn r0(src: &str) -> Ty {
        let s = scheme_for(src, 1);
        let Ty::Fun(_, r) = &s.scheme.ty else {
            panic!("{src}")
        };
        let Ty::App(_, args) = r.as_ref() else {
            panic!("{src}")
        };
        args[1].clone()
    }

    fn go(key: &str) -> Ty {
        Ty::app(&format!("go@{key}"), vec![])
    }

    /// C-6: an opaque Go type is a nominal, never a wildcard: the parameter
    /// and the result carry the same key a `Pkg.Name` annotation gets.
    #[test]
    fn opaque_go_types_are_nominal() {
        let key = "Github.Com.Google.Uuid.UUID";
        assert_eq!(p1(&format!("go@{key} -> Result Error ()")), go(key));
        assert_eq!(r0(&format!("() -> Result Error go@{key}")), go(key));
        assert_eq!(
            r0("() -> Result Error (List go@Net.Http.Cookie)"),
            Ty::app("List", vec![go("Net.Http.Cookie")])
        );
        assert_eq!(r0("() -> Result Error go@Go.GoFunc"), go("Go.GoFunc"));
        // A format-2 marker maps to the same key.
        assert_eq!(
            p1("UUID@github.com/google/uuid -> Result Error ()"),
            go(key)
        );
        // A bare name, a lower-case name, `error`, a Go array, `complex128`:
        // opaque, never a wildcard.
        for src in [
            "Request -> Result Error ()",
            "network -> Result Error ()",
            "complex128 -> Result Error ()",
            "[32]byte -> Result Error ()",
        ] {
            assert_eq!(p1(src), go("Go.GoUnknown"), "{src}");
        }
        assert_eq!(p1("error -> Result Error ()"), go("Go.GoError"));
        // A single capital letter is NOT a type variable; `$T` is.
        assert_eq!(p1("T -> Result Error ()"), go("Go.GoUnknown"));
        let s = scheme_for("$T -> Result Error (Maybe $T)", 1).scheme;
        assert_eq!(s.vars.len(), 1);
    }

    /// C-4 / C-5: pointers are Maybe, map keys keep their type.
    #[test]
    fn pointers_and_map_keys() {
        assert_eq!(
            r0("() -> Result Error (Maybe String)"),
            Ty::app("Maybe", vec![Ty::app("String", vec![])])
        );
        assert_eq!(
            p1("Dict Int String -> Result Error ()"),
            Ty::app(
                "Dict",
                vec![Ty::app("Int", vec![]), Ty::app("String", vec![])]
            )
        );
        assert_eq!(p1("Bytes -> Result Error ()"), Ty::app("String", vec![]));
        // A tuple ending in Bool is a tuple (comma-ok renders as Maybe).
        assert_eq!(
            r0("() -> Result Error (String, Bool)"),
            Ty::Tuple(vec![Ty::app("String", vec![]), Ty::app("Bool", vec![])])
        );
    }

    /// The two checked parameter positions: an empty interface accepts any
    /// value (Go does); a non-empty one is checked after solving. As a
    /// RESULT both are opaque.
    #[test]
    fn interfaces() {
        assert_eq!(p1("any -> Result Error ()"), wild());
        assert_eq!(p1("goi@Io.Writer -> Result Error ()"), wild());
        assert_eq!(r0("() -> Result Error go@Io.Writer"), go("Io.Writer"));
        assert_eq!(
            iface_params("goi@Io.Writer -> String -> Result Error ()", 2),
            Some(vec![Some("Io.Writer".to_string()), None])
        );
    }

    /// C-12: a callback's parameters and RESULT are typed from the pin; a
    /// zero-parameter callback is `() -> r`; a func anywhere else is opaque.
    #[test]
    fn callbacks_are_typed() {
        let s = scheme_for(
            "go@Github.Com.Gorilla.Mux.Router -> String -> (go@Net.Http.ResponseWriter -> go@Net.Http.Request -> ()) -> Result Error ()",
            3,
        )
        .scheme;
        let Ty::Fun(_, r) = &s.ty else { panic!() };
        let Ty::Fun(_, r) = r.as_ref() else { panic!() };
        let Ty::Fun(cb, _) = r.as_ref() else { panic!() };
        assert_eq!(
            **cb,
            Ty::Fun(
                Box::new(go("Net.Http.ResponseWriter")),
                Box::new(Ty::Fun(
                    Box::new(go("Net.Http.Request")),
                    Box::new(Ty::Unit)
                ))
            )
        );
        assert_eq!(
            p1("(Int -> String) -> Result Error String"),
            Ty::Fun(
                Box::new(Ty::app("Int", vec![])),
                Box::new(Ty::app("String", vec![]))
            )
        );
        assert_eq!(
            p1("(() -> ()) -> Result Error Int"),
            Ty::Fun(Box::new(Ty::Unit), Box::new(Ty::Unit))
        );
        assert_eq!(
            p1("( -> ()) -> Result Error Int"),
            Ty::Fun(Box::new(Ty::Unit), Box::new(Ty::Unit))
        );
        assert_eq!(
            p1("(Int -> Result Error ()) -> Result Error String"),
            Ty::Fun(
                Box::new(Ty::app("Int", vec![])),
                Box::new(result_of(Ty::Unit))
            )
        );
        assert_eq!(
            r0("() -> Result Error (Int -> Int)"),
            go("Go.GoFunc"),
            "a returned func is opaque"
        );
    }

    #[test]
    fn list_maybe() {
        let s = scheme_for("List String -> Result Error (Maybe Int)", 1).scheme;
        assert_eq!(
            s.ty,
            Ty::Fun(
                Box::new(Ty::app("List", vec![Ty::app("String", vec![])])),
                Box::new(result_of(Ty::app("Maybe", vec![Ty::app("Int", vec![])])))
            )
        );
    }

    #[test]
    fn unbalanced_segments_degrade_locally() {
        // A stray `)` in the payload: the result is opaque, the params typed.
        let s = scheme_for("URL@net/url -> Result Error (Request -> URL, error))", 1);
        assert_eq!(s.kind, FfiSchemeKind::Parsed);
        assert_eq!(
            s.scheme.ty,
            Ty::Fun(
                Box::new(go("Net.Url.URL")),
                Box::new(result_of(go("Go.GoUnknown")))
            )
        );
        // A garbled PARAMETER is checked by the reflective wrapper; the others
        // stay typed.
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
    fn fallback_keeps_the_result_opaque() {
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
            let mut want = result_of(go("Go.GoUnknown"));
            for _ in 0..arity {
                want = Ty::Fun(Box::new(wild()), Box::new(want));
            }
            assert_eq!(s.scheme.ty, want, "{src:?}");
        }
    }

    /// No format-3 signature leaves an unchecked result position.
    #[test]
    fn census_counts_only_parameter_wildcards() {
        let sigs = [
            ("go@Io.Reader -> Result Error (Maybe String)", 1),
            ("goi@Io.Writer -> Bytes -> Result Error ()", 2),
            ("any -> Result Error go@Go.GoAny", 1),
        ];
        assert_eq!(wildcard_census(sigs.into_iter()), (3, 2, 2));
    }

    #[test]
    fn spelled_arity_counts_top_level_arrows() {
        assert_eq!(spelled_arity("String -> Int -> Result Error ()"), Some(2));
        assert_eq!(spelled_arity("(A -> B) -> Result Error ()"), Some(1));
        assert_eq!(spelled_arity("() -> Result Error ()"), Some(1));
        assert_eq!(spelled_arity("( -> X, error)) -> Result Error ()"), None);
    }
}

/// The `ffi_sig` wildcard census of a surface: `(bindings, bindings with at
/// least one wildcard, wildcard occurrences)`. A wildcard is the checker's
/// per-occurrence `any`: a position the pinned signature leaves unchecked.
pub fn wildcard_census<'a>(sigs: impl Iterator<Item = (&'a str, usize)>) -> (usize, usize, usize) {
    fn count(t: &Ty) -> usize {
        match t {
            Ty::Var(n) if n.as_str() == "any" => 1,
            Ty::Var(_) | Ty::Unit | Ty::Error => 0,
            Ty::App(_, args) | Ty::Tuple(args) => args.iter().map(count).sum(),
            Ty::Fun(a, b) => count(a) + count(b),
            Ty::Record(fields, _) => fields.iter().map(|(_, t)| count(t)).sum(),
        }
    }
    let (mut n, mut with, mut occ) = (0, 0, 0);
    for (s, arity) in sigs {
        n += 1;
        let c = count(&scheme_for(s, arity).scheme.ty);
        if c > 0 {
            with += 1;
        }
        occ += c;
    }
    (n, with, occ)
}

#[cfg(test)]
mod census_tests {
    /// Prints the wildcard census of every `*.kernel.json` under the
    /// directories in `SKY_FFI_CENSUS_DIRS` (colon-separated). A measurement,
    /// not a gate: `cargo test -p ty ffi_wildcard_census -- --ignored --nocapture`.
    #[test]
    #[ignore]
    fn ffi_wildcard_census() {
        let dirs = std::env::var("SKY_FFI_CENSUS_DIRS").unwrap_or_default();
        for dir in dirs.split(':').filter(|d| !d.is_empty()) {
            let mut files: Vec<_> = std::fs::read_dir(dir)
                .unwrap()
                .filter_map(|e| e.ok().map(|e| e.path()))
                .filter(|p| p.to_string_lossy().ends_with("kernel.json"))
                .collect();
            files.sort();
            for f in files {
                let v: serde_json::Value =
                    serde_json::from_str(&std::fs::read_to_string(&f).unwrap()).unwrap();
                let fns = v["functions"].as_array().cloned().unwrap_or_default();
                let sigs: Vec<(String, usize)> = fns
                    .iter()
                    .map(|f| {
                        (
                            f["skyType"].as_str().unwrap_or("").to_string(),
                            f["arity"].as_u64().unwrap_or(0) as usize,
                        )
                    })
                    .collect();
                let (n, with, occ) =
                    super::wildcard_census(sigs.iter().map(|(s, a)| (s.as_str(), *a)));
                println!(
                    "CENSUS {} bindings={n} with_wildcard={with} wildcards={occ}",
                    f.display()
                );
            }
        }
    }
}
