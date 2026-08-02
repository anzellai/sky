//! `xtask welltyped` — the WELL-TYPED differential fuzzer (the `WellTypedFuzzerSpec`
//! analog the self-host v1 gap named, and `fuzz_gate.rs`'s own docstring calls a
//! still-open gap).
//!
//! ## What it does that `xtask fuzz` does NOT
//! `xtask fuzz` MUTATES corpus inputs (mostly producing INVALID programs) and
//! asserts robustness (no panic) + determinism. It never generates a VALID program
//! and never diffs the Haskell oracle. This gate closes that hole: it
//!
//!   1. GENERATES well-typed Sky programs with a bounded, deterministic,
//!      TYPE-DIRECTED builder (build a typed term, pretty-print it → valid by
//!      construction), then
//!   2. runs BOTH the Rust compiler AND the Haskell oracle in check-only mode and
//!      asserts they AGREE on ACCEPT/REJECT for every program (modulo the ledgered
//!      `known-divergences.toml` entries), then
//!   3. asserts the generation itself is DETERMINISTIC (same seed → identical
//!      program text, run-to-run).
//!
//! A well-typed generated program SHOULD be ACCEPTED by both. A disagreement that
//! is not a ledgered divergence is a REAL Rust-vs-oracle parity / soundness bug and
//! the gate FAILS with the minimal program + both verdicts.
//!
//! ## Type-check-phase verdict (why not full `sky check`)
//! `sky check` = type-check + `go build`; `go build` on the oracle is ~14 s cold,
//! which is untenable for hundreds of programs. This gate needs the parse, resolve
//! and typecheck verdict only (the task's "check-only, no go build"), so it streams
//! each compiler's stdout and reads the verdict at the type-check boundary — the
//! Rust compiler prints `-- Generating Go` and the oracle prints `Types OK` once,
//! and only once, type-checking has SUCCEEDED (both strictly before codegen /
//! `go build`). The child is killed the instant that marker appears (or on exit
//! without it = REJECT), so a check costs ~type-check time, not ~build time.
//!
//! This deliberately isolates the TYPE SYSTEM parity. A phase that runs AFTER
//! type-check (the oracle's codegen validator, `go build`) is a DIFFERENT gate's
//! job (`build-run` / `divergences`); e.g. the oracle type-checks `abs (0 - 7)`
//! ("Types OK") but its codegen validator then rejects it (E4005: no
//! `rt.Basics_abs`), while Rust compiles it fully — a real but codegen-phase
//! divergence, out of scope for a TYPE-CHECK parity gate (and `abs` is simply not
//! generated).
//!
//! ## LOCAL / release-only (like `xtask divergences`)
//! It shells the Haskell oracle, which is NOT available in CI (see
//! `divergences_gate.rs`). With no oracle binary discoverable the gate SKIPS
//! (exit 0) with a clear message, exactly like the divergence ledger's oracle
//! side. Point it at explicit binaries with `SKY_RUST_BIN` / `SKY_ORACLE_BIN`.
//!
//! ## Bounded + deterministic
//! A FIXED base seed drives a splitmix64 stream; program `i` uses a seed derived
//! purely from `i`, so the whole set (and every verdict, a pure function of the
//! program text) reproduces byte-for-byte. `--count=N` (default 120) bounds the
//! run; each subprocess is timeout-bounded. The default finishes in a few minutes.

use std::io::BufRead;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::mpsc;
use std::time::{Duration, Instant};

// ---- tuning knobs (all bounded) ------------------------------------------

/// Fixed base seed — identical program set every run (so a found bug reproduces).
const BASE_SEED: u64 = 0x5759_5F57_5459_5046; // "SY_WTYPF"

/// Default number of generated programs. Override with `--count=N`. Each program
/// costs ~type-check time on BOTH compilers (oracle ~3 s, rust ~0.7 s), so 120
/// finishes in a few minutes. Bounded on purpose.
const DEFAULT_COUNT: usize = 120;

/// Per-subprocess wall-clock ceiling. Type-check alone is a few seconds even cold;
/// this only trips on a genuinely wedged child (then recorded, child killed).
const CHECK_TIMEOUT: Duration = Duration::from_secs(40);

/// Max recursion depth of a generated expression term. Small → bounded program
/// size + fast checks; still deep enough to nest let/case/if/list/record.
const MAX_DEPTH: u32 = 4;

// ---- deterministic PRNG (splitmix64) -------------------------------------

/// The same tiny reproducible PRNG `fuzz_gate` uses (no external dep), seeded by a
/// fixed constant so runs are identical.
struct SplitMix64(u64);
impl SplitMix64 {
    fn new(seed: u64) -> Self {
        SplitMix64(seed)
    }
    fn next_u64(&mut self) -> u64 {
        self.0 = self.0.wrapping_add(0x9E37_79B9_7F4A_7C15);
        let mut z = self.0;
        z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
        z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
        z ^ (z >> 31)
    }
    fn below(&mut self, n: usize) -> usize {
        if n == 0 {
            0
        } else {
            (self.next_u64() % n as u64) as usize
        }
    }
    fn chance(&mut self, num: usize, den: usize) -> bool {
        self.below(den) < num
    }
    fn pick<'a, T>(&mut self, xs: &'a [T]) -> &'a T {
        &xs[self.below(xs.len())]
    }
}

// ---- the typed AST the builder constructs --------------------------------

#[derive(Clone, PartialEq, Eq)]
enum Ty {
    Int,
    Float,
    Str,
    Bool,
    List(Box<Ty>),
    Maybe(Box<Ty>),
    Tuple(Vec<Ty>),
    Record(Vec<(String, Ty)>),
    Adt(String),
}

impl Ty {
    /// Render as a standalone type (annotation head position).
    fn render(&self) -> String {
        match self {
            Ty::Int => "Int".into(),
            Ty::Float => "Float".into(),
            Ty::Str => "String".into(),
            Ty::Bool => "Bool".into(),
            Ty::List(t) => format!("List {}", t.render_arg()),
            Ty::Maybe(t) => format!("Maybe {}", t.render_arg()),
            Ty::Tuple(ts) => {
                let inner: Vec<String> = ts.iter().map(|t| t.render()).collect();
                format!("( {} )", inner.join(", "))
            }
            Ty::Record(fs) => {
                let inner: Vec<String> =
                    fs.iter().map(|(n, t)| format!("{n} : {}", t.render())).collect();
                format!("{{ {} }}", inner.join(", "))
            }
            Ty::Adt(n) => n.clone(),
        }
    }
    /// Render as a type ARGUMENT (parenthesise multi-token constructor applications).
    fn render_arg(&self) -> String {
        match self {
            Ty::List(_) | Ty::Maybe(_) => format!("({})", self.render()),
            _ => self.render(),
        }
    }
}

/// A top-level ADT declaration + its constructors.
#[derive(Clone)]
struct Adt {
    name: String,
    ctors: Vec<(String, Vec<Ty>)>, // (ctor name, arg types)
}

/// A top-level helper function: `name : p0 -> p1 -> ret`.
#[derive(Clone)]
struct Helper {
    name: String,
    params: Vec<Ty>,
    ret: Ty,
}

// ---- the generator -------------------------------------------------------

struct Gen {
    rng: SplitMix64,
    fresh: usize,
    adts: Vec<Adt>,
    helpers: Vec<Helper>,
}

impl Gen {
    fn new(seed: u64) -> Self {
        Gen {
            rng: SplitMix64::new(seed),
            fresh: 0,
            adts: Vec::new(),
            helpers: Vec::new(),
        }
    }

    fn fresh_var(&mut self) -> String {
        let v = format!("v{}", self.fresh);
        self.fresh += 1;
        v
    }

    /// A data-CONSTRUCTOR argument type. Restricted to the syntax BOTH compilers
    /// parse in constructor-argument position: atomic scalars, an already-declared
    /// ADT name, and parenthesised `(List T)` / `(Maybe T)`. It deliberately does
    /// NOT emit a bare tuple `(Int, String)` or record `{ f : T }` type here —
    /// those are a REAL Rust-vs-oracle PARSE divergence this gate discovered (Rust
    /// accepts them Elm-style; the oracle rejects with E0001), reported separately.
    /// Narrowing to the common grammar lets the gate exercise deep TYPE-CHECK
    /// parity instead of re-hitting the same parse divergence every run.
    fn gen_ctor_arg_ty(&mut self) -> Ty {
        let scalar = [Ty::Int, Ty::Float, Ty::Str, Ty::Bool];
        match self.rng.below(6) {
            0 => Ty::List(Box::new(self.rng.pick(&scalar).clone())),
            1 => Ty::Maybe(Box::new(self.rng.pick(&scalar).clone())),
            2 if !self.adts.is_empty() => {
                let adts = self.adts.clone();
                Ty::Adt(self.rng.pick(&adts).name.clone())
            }
            _ => self.rng.pick(&scalar).clone(),
        }
    }

    /// Pick a bounded random type. `depth` limits container nesting.
    fn gen_ty(&mut self, depth: u32) -> Ty {
        // Weighted toward scalars; containers only when there's depth budget and
        // (for ADTs) a declared ADT to reference.
        let scalar = [Ty::Int, Ty::Float, Ty::Str, Ty::Bool];
        if depth == 0 || self.rng.chance(3, 5) {
            return self.rng.pick(&scalar).clone();
        }
        match self.rng.below(6) {
            0 => Ty::List(Box::new(self.gen_ty(depth - 1))),
            1 => Ty::Maybe(Box::new(self.gen_ty(depth - 1))),
            2 => {
                let n = 2 + self.rng.below(2); // 2 or 3
                Ty::Tuple((0..n).map(|_| self.gen_ty(depth - 1)).collect())
            }
            3 => {
                let n = 1 + self.rng.below(3); // 1..3 fields
                let mut fs = Vec::new();
                for i in 0..n {
                    fs.push((format!("f{i}"), self.gen_ty(depth - 1)));
                }
                Ty::Record(fs)
            }
            4 if !self.adts.is_empty() => {
                let adts = self.adts.clone();
                let a = self.rng.pick(&adts);
                Ty::Adt(a.name.clone())
            }
            _ => self.rng.pick(&scalar).clone(),
        }
    }

    /// Generate a whole `Main` module as source text.
    fn gen_program(&mut self) -> String {
        // 0..2 ADTs.
        let n_adts = self.rng.below(3);
        for i in 0..n_adts {
            let name = format!("Ty{i}");
            let n_ctors = 1 + self.rng.below(3); // 1..3 ctors
            let mut ctors = Vec::new();
            for c in 0..n_ctors {
                let cname = format!("{name}{}", (b'A' + c as u8) as char);
                let n_args = self.rng.below(3); // 0..2 args (mostly small)
                let args: Vec<Ty> = (0..n_args).map(|_| self.gen_ctor_arg_ty()).collect();
                ctors.push((cname, args));
            }
            self.adts.push(Adt { name, ctors });
        }

        // 0..3 helper functions.
        let n_helpers = self.rng.below(4);
        for i in 0..n_helpers {
            let name = format!("hlp{i}");
            let n_params = self.rng.below(3); // 0..2 params
            let params: Vec<Ty> = (0..n_params).map(|_| self.gen_ty(2)).collect();
            let ret = self.gen_ty(2);
            self.helpers.push(Helper {
                name,
                params,
                ret,
            });
        }

        let mut out = String::new();
        out.push_str("module Main exposing (main)\n\n");
        out.push_str("import Sky.Core.Prelude exposing (..)\n\n");

        // Emit ADT declarations.
        for a in self.adts.clone() {
            let mut decl = format!("type {} =", a.name);
            for (i, (cn, args)) in a.ctors.iter().enumerate() {
                let sep = if i == 0 { " " } else { " | " };
                let argstr: String = args.iter().map(|t| format!(" {}", t.render_arg())).collect();
                decl.push_str(&format!("{sep}{cn}{argstr}"));
            }
            out.push_str(&decl);
            out.push_str("\n\n");
        }

        // Emit helper definitions (annotated → type known by construction).
        for h in self.helpers.clone() {
            let sig_parts: Vec<String> =
                h.params.iter().chain(std::iter::once(&h.ret)).map(|t| t.render()).collect();
            out.push_str(&format!("{} : {}\n", h.name, sig_parts.join(" -> ")));
            let mut env: Vec<(String, Ty)> = Vec::new();
            let mut params_src = String::new();
            for (i, pty) in h.params.iter().enumerate() {
                let pn = format!("a{i}");
                params_src.push_str(&format!(" {pn}"));
                env.push((pn, pty.clone()));
            }
            let body = self.gen_body(&h.ret, &env);
            out.push_str(&format!("{}{} =\n    {}\n\n", h.name, params_src, indent_cont(&body)));
        }

        // main : a concrete scalar expression.
        let scalar = [Ty::Int, Ty::Float, Ty::Str, Ty::Bool];
        let mty = self.rng.pick(&scalar).clone();
        let body = self.gen_body(&mty, &[]);
        out.push_str(&format!("main =\n    {}\n", indent_cont(&body)));
        out
    }

    /// Type-directed expression generator: returns source for an expression of
    /// type `ty` valid in `env`. Guaranteed well-typed by construction.
    fn gen_expr(&mut self, ty: &Ty, depth: u32, env: &[(String, Ty)]) -> String {
        // Prefer an in-scope variable of exactly this type sometimes (variable use
        // coverage) — always available even at depth 0.
        let in_scope: Vec<&String> =
            env.iter().filter(|(_, t)| t == ty).map(|(n, _)| n).collect();
        if !in_scope.is_empty() && self.rng.chance(1, 3) {
            return in_scope[self.rng.below(in_scope.len())].clone();
        }

        // A helper call whose return type matches — composition coverage.
        if depth > 0 && self.rng.chance(1, 5) {
            let candidates: Vec<Helper> =
                self.helpers.iter().filter(|h| &h.ret == ty).cloned().collect();
            if !candidates.is_empty() {
                let h = candidates[self.rng.below(candidates.len())].clone();
                let args: Vec<String> = h
                    .params
                    .iter()
                    .map(|pt| paren_if_app(&self.gen_expr(pt, depth - 1, env)))
                    .collect();
                if args.is_empty() {
                    return h.name.clone();
                }
                return format!("{} {}", h.name, args.join(" "));
            }
        }

        // A generic single-line `if` wrapper occasionally, for any type.
        if depth > 1 && self.rng.chance(1, 8) {
            let c = self.gen_expr(&Ty::Bool, depth - 1, env);
            let a = self.gen_expr(ty, depth - 1, env);
            let b = self.gen_expr(ty, depth - 1, env);
            return format!("if {} then {} else {}", paren_if_app(&c), paren_if_app(&a), paren_if_app(&b));
        }
        // A generic single-line `let … in …` wrapper occasionally.
        if depth > 1 && self.rng.chance(1, 8) {
            return self.gen_let_inline(ty, depth, env);
        }

        match ty {
            Ty::Int => self.gen_int(depth, env),
            Ty::Float => self.gen_float(depth, env),
            Ty::Str => self.gen_str(depth, env),
            Ty::Bool => self.gen_bool(depth, env),
            Ty::List(t) => self.gen_list(t, depth, env),
            Ty::Maybe(t) => self.gen_maybe(t, depth, env),
            Ty::Tuple(ts) => {
                let parts: Vec<String> = ts
                    .iter()
                    .map(|t| paren_if_app(&self.gen_expr(t, depth.saturating_sub(1), env)))
                    .collect();
                format!("( {} )", parts.join(", "))
            }
            Ty::Record(fs) => self.gen_record(fs, depth, env),
            Ty::Adt(n) => self.gen_adt(n, depth, env),
        }
    }

    /// A SINGLE-LINE `let v = e in body` (unambiguous in any sub-expression
    /// position — multi-line lets are only emitted at statement position by
    /// `gen_body`, where the layout is clean).
    fn gen_let_inline(&mut self, ty: &Ty, depth: u32, env: &[(String, Ty)]) -> String {
        let bty = self.gen_ty(1);
        let v = self.fresh_var();
        let e = self.gen_expr(&bty, depth - 1, env);
        let mut env2 = env.to_vec();
        env2.push((v.clone(), bty));
        let body = self.gen_expr(ty, depth - 1, &env2);
        format!("let {v} = {} in {}", paren_if_app(&e), body)
    }

    /// A helper/main BODY (statement / tail position, its own clean layout): a
    /// single-line expression, a top-level multi-line `let`, or a top-level `case`.
    /// All sub-expressions it emits are single-line, so no nested-layout ambiguity
    /// can arise. Internal lines carry ABSOLUTE indentation (8 for let bindings,
    /// 4 for `in`/body/case) — the caller prefixes only the first line.
    fn gen_body(&mut self, ty: &Ty, env: &[(String, Ty)]) -> String {
        match self.rng.below(4) {
            0 => {
                // top-level multi-line let with single-line bindings + body
                let n = 1 + self.rng.below(2);
                let mut env2 = env.to_vec();
                let mut binds = String::new();
                for _ in 0..n {
                    let bty = self.gen_ty(1);
                    let v = self.fresh_var();
                    let e = self.gen_expr(&bty, MAX_DEPTH - 1, &env2);
                    binds.push_str(&format!("        {v} = {e}\n"));
                    env2.push((v, bty));
                }
                let body = self.gen_expr(ty, MAX_DEPTH - 1, &env2);
                format!("let\n{binds}    in\n    {body}")
            }
            1 => self.gen_case_body(ty, env),
            _ => self.gen_expr(ty, MAX_DEPTH, env),
        }
    }

    fn gen_int(&mut self, depth: u32, env: &[(String, Ty)]) -> String {
        if depth == 0 {
            return format!("{}", self.rng.below(1000));
        }
        match self.rng.below(9) {
            0 => format!("{}", self.rng.below(1000)),
            1 => {
                let op = *self.rng.pick(&["+", "-", "*"]);
                format!(
                    "({} {op} {})",
                    self.gen_int(depth - 1, env),
                    self.gen_int(depth - 1, env)
                )
            }
            2 => format!("(modBy {} {})", 1 + self.rng.below(9), self.gen_int(depth - 1, env)),
            3 => format!("(String.length {})", paren_if_app(&self.gen_str(depth - 1, env))),
            4 => {
                let et = self.gen_ty(1);
                format!("(List.length {})", paren_if_app(&self.gen_list(&et, depth - 1, env)))
            }
            5 => format!(
                "(Maybe.withDefault {} {})",
                self.gen_int(depth - 1, env),
                paren_if_app(&self.gen_maybe(&Ty::Int, depth - 1, env))
            ),
            6 => format!(
                "(Result.withDefault {} {})",
                self.gen_int(depth - 1, env),
                paren_if_app(&self.gen_result(&Ty::Int, depth - 1, env))
            ),
            7 => format!(
                "(Maybe.withDefault {} (Just {}))",
                self.gen_int(depth - 1, env),
                self.gen_int(depth - 1, env)
            ),
            _ => format!("{}", self.rng.below(1000)),
        }
    }

    fn gen_float(&mut self, depth: u32, env: &[(String, Ty)]) -> String {
        if depth == 0 {
            return format!("{}.{}", self.rng.below(100), self.rng.below(100));
        }
        match self.rng.below(4) {
            0 => format!("{}.{}", self.rng.below(100), self.rng.below(100)),
            1 => {
                let op = *self.rng.pick(&["+", "-", "*"]);
                format!(
                    "({} {op} {})",
                    self.gen_float(depth - 1, env),
                    self.gen_float(depth - 1, env)
                )
            }
            2 => format!(
                "(Maybe.withDefault {} {})",
                self.gen_float(depth - 1, env),
                paren_if_app(&self.gen_maybe(&Ty::Float, depth - 1, env))
            ),
            _ => format!("{}.{}", self.rng.below(100), self.rng.below(100)),
        }
    }

    fn gen_str(&mut self, depth: u32, env: &[(String, Ty)]) -> String {
        if depth == 0 {
            return string_lit(self.rng.below(1000));
        }
        match self.rng.below(7) {
            0 => string_lit(self.rng.below(1000)),
            1 => format!(
                "({} ++ {})",
                self.gen_str(depth - 1, env),
                self.gen_str(depth - 1, env)
            ),
            2 => format!("(String.fromInt {})", self.gen_int(depth - 1, env)),
            3 => {
                let f = *self.rng.pick(&["String.toUpper", "String.toLower", "String.reverse"]);
                format!("({f} {})", paren_if_app(&self.gen_str(depth - 1, env)))
            }
            4 => format!(
                "(Maybe.withDefault {} {})",
                self.gen_str(depth - 1, env),
                paren_if_app(&self.gen_maybe(&Ty::Str, depth - 1, env))
            ),
            5 => format!(
                "(Maybe.withDefault {} (Just {}))",
                self.gen_str(depth - 1, env),
                self.gen_str(depth - 1, env)
            ),
            _ => string_lit(self.rng.below(1000)),
        }
    }

    fn gen_bool(&mut self, depth: u32, env: &[(String, Ty)]) -> String {
        if depth == 0 {
            return (*self.rng.pick(&["True", "False"])).to_string();
        }
        match self.rng.below(7) {
            0 => (*self.rng.pick(&["True", "False"])).to_string(),
            1 => format!("(not {})", paren_if_app(&self.gen_bool(depth - 1, env))),
            2 => {
                let op = *self.rng.pick(&["&&", "||"]);
                format!(
                    "({} {op} {})",
                    self.gen_bool(depth - 1, env),
                    self.gen_bool(depth - 1, env)
                )
            }
            3 => format!(
                "({} == {})",
                self.gen_int(depth - 1, env),
                self.gen_int(depth - 1, env)
            ),
            4 => {
                let op = *self.rng.pick(&["<", ">"]);
                format!(
                    "({} {op} {})",
                    self.gen_int(depth - 1, env),
                    self.gen_int(depth - 1, env)
                )
            }
            5 => format!(
                "({} == {})",
                self.gen_str(depth - 1, env),
                self.gen_str(depth - 1, env)
            ),
            _ => (*self.rng.pick(&["True", "False"])).to_string(),
        }
    }

    fn gen_list(&mut self, elem: &Ty, depth: u32, env: &[(String, Ty)]) -> String {
        if depth == 0 {
            return "[]".to_string();
        }
        // List Int can use range.
        if *elem == Ty::Int && self.rng.chance(1, 5) {
            let a = self.rng.below(5);
            let b = a + self.rng.below(6);
            return format!("(List.range {a} {b})");
        }
        match self.rng.below(6) {
            0 => "[]".to_string(),
            1 => {
                let n = 1 + self.rng.below(3);
                let parts: Vec<String> =
                    (0..n).map(|_| self.gen_expr(elem, depth - 1, env)).collect();
                format!("[ {} ]", parts.join(", "))
            }
            2 => format!(
                "({} :: {})",
                self.gen_expr(elem, depth - 1, env),
                paren_if_app(&self.gen_list(elem, depth - 1, env))
            ),
            3 => format!("(List.reverse {})", paren_if_app(&self.gen_list(elem, depth - 1, env))),
            4 => format!(
                "(List.append {} {})",
                paren_if_app(&self.gen_list(elem, depth - 1, env)),
                paren_if_app(&self.gen_list(elem, depth - 1, env))
            ),
            _ => {
                // List.map (\p -> body:elem) (xs : List src)
                let src = self.gen_ty(1);
                let p = self.fresh_var();
                let mut env2 = env.to_vec();
                env2.push((p.clone(), src.clone()));
                let body = self.gen_expr(elem, depth - 1, &env2);
                format!(
                    "(List.map (\\{p} -> {}) {})",
                    body,
                    paren_if_app(&self.gen_list(&src, depth - 1, env))
                )
            }
        }
    }

    fn gen_maybe(&mut self, inner: &Ty, depth: u32, env: &[(String, Ty)]) -> String {
        if depth == 0 {
            return "Nothing".to_string();
        }
        match self.rng.below(4) {
            0 => "Nothing".to_string(),
            1 => format!("(Just {})", paren_if_app(&self.gen_expr(inner, depth - 1, env))),
            2 => format!("(List.head {})", paren_if_app(&self.gen_list(inner, depth - 1, env))),
            _ => {
                // Maybe.map (\p -> body:inner) (m : Maybe src)
                let src = self.gen_ty(1);
                let p = self.fresh_var();
                let mut env2 = env.to_vec();
                env2.push((p.clone(), src.clone()));
                let body = self.gen_expr(inner, depth - 1, &env2);
                format!(
                    "(Maybe.map (\\{p} -> {}) {})",
                    body,
                    paren_if_app(&self.gen_maybe(&src, depth - 1, env))
                )
            }
        }
    }

    fn gen_result(&mut self, ok: &Ty, depth: u32, env: &[(String, Ty)]) -> String {
        if depth == 0 {
            return format!("(Ok {})", paren_if_app(&self.gen_expr(ok, 0, env)));
        }
        if self.rng.chance(1, 3) {
            format!("(Err {})", self.gen_str(depth - 1, env))
        } else {
            format!("(Ok {})", paren_if_app(&self.gen_expr(ok, depth - 1, env)))
        }
    }

    fn gen_record(&mut self, fs: &[(String, Ty)], depth: u32, env: &[(String, Ty)]) -> String {
        let lit = |g: &mut Self, env: &[(String, Ty)]| -> String {
            let parts: Vec<String> = fs
                .iter()
                .map(|(n, t)| {
                    format!("{n} = {}", paren_if_app(&g.gen_expr(t, depth.saturating_sub(1), env)))
                })
                .collect();
            format!("{{ {} }}", parts.join(", "))
        };
        // Record update (#166 shape): let r = <lit> in { r | fi = <e> }
        if depth > 1 && !fs.is_empty() && self.rng.chance(1, 2) {
            let r = self.fresh_var();
            let base = lit(self, env);
            let (fname, fty) = self.rng.pick(fs).clone();
            let fval = self.gen_expr(&fty, depth - 1, env);
            return format!("let {r} = {base} in {{ {r} | {fname} = {fval} }}");
        }
        lit(self, env)
    }

    fn gen_adt(&mut self, name: &str, depth: u32, env: &[(String, Ty)]) -> String {
        let adt = self.adts.iter().find(|a| a.name == name).cloned();
        let Some(adt) = adt else {
            return "()".to_string(); // unreachable; ADTs only referenced when declared
        };
        let (cn, args) = self.rng.pick(&adt.ctors).clone();
        if args.is_empty() {
            return cn;
        }
        let argstr: Vec<String> = args
            .iter()
            .map(|t| paren_if_app(&self.gen_expr(t, depth.saturating_sub(1), env)))
            .collect();
        format!("({} {})", cn, argstr.join(" "))
    }

    /// A top-level (BODY-position) `case` on a Bool or a declared ADT, exhaustive
    /// by construction, yielding `ret`. Scrutinee + every arm body are SINGLE-LINE,
    /// so the only newlines are the arm separators; arms sit at column 8, under the
    /// `case` keyword (column 4 after the caller's prefix) — clean off-side layout.
    fn gen_case_body(&mut self, ret: &Ty, env: &[(String, Ty)]) -> String {
        let d = MAX_DEPTH - 1;
        // Bool case unless an ADT is declared and the coin picks it.
        if self.adts.is_empty() || self.rng.chance(1, 2) {
            let scrut = self.gen_bool(d, env);
            let t = self.gen_expr(ret, d, env);
            let f = self.gen_expr(ret, d, env);
            return format!(
                "case {} of\n        True -> {}\n        False -> {}",
                paren_if_app(&scrut),
                t,
                f
            );
        }
        let adt = self.rng.pick(&self.adts.clone()).clone();
        let scrut = self.gen_adt(&adt.name, d, env);
        let mut arms = String::new();
        for (cn, args) in &adt.ctors {
            let mut env2 = env.to_vec();
            let mut pat = cn.clone();
            for (i, at) in args.iter().enumerate() {
                let pv = format!("{}p{i}", lower_first(cn));
                pat.push_str(&format!(" {pv}"));
                env2.push((pv, at.clone()));
            }
            let arm = self.gen_expr(ret, d, &env2);
            arms.push_str(&format!("        {pat} -> {arm}\n"));
        }
        format!("case {} of\n{}", paren_if_app(&scrut), arms.trim_end())
    }
}

// ---- source-formatting helpers -------------------------------------------

fn string_lit(n: usize) -> String {
    format!("\"s{n}\"")
}

/// Parenthesise an expression if it is a bare application/operator that would
/// mis-parse as an argument. Cheap heuristic: wrap when it contains a space and
/// is not already fully bracketed.
fn paren_if_app(e: &str) -> String {
    let t = e.trim();
    if !t.contains(' ') {
        return t.to_string();
    }
    let bracketed = (t.starts_with('(') && t.ends_with(')'))
        || (t.starts_with('[') && t.ends_with(']'))
        || (t.starts_with('{') && t.ends_with('}'))
        || (t.starts_with('"') && t.ends_with('"'));
    if bracketed && balanced_outer(t) {
        t.to_string()
    } else {
        format!("({t})")
    }
}

/// True when the outer bracket at position 0 closes exactly at the last char (so
/// the whole string is one bracketed group, not e.g. `(a) + (b)`).
fn balanced_outer(t: &str) -> bool {
    let bytes = t.as_bytes();
    if bytes.is_empty() {
        return false;
    }
    let (open, close) = match bytes[0] {
        b'(' => (b'(', b')'),
        b'[' => (b'[', b']'),
        b'{' => (b'{', b'}'),
        b'"' => return t.len() >= 2 && bytes[t.len() - 1] == b'"' && !t[1..t.len() - 1].contains('"'),
        _ => return false,
    };
    let mut depth = 0i32;
    for (i, &b) in bytes.iter().enumerate() {
        if b == open {
            depth += 1;
        } else if b == close {
            depth -= 1;
            if depth == 0 {
                return i == bytes.len() - 1;
            }
        }
    }
    false
}

/// Re-indent a multi-line expression body so continuation lines sit under the
/// `main =` / helper-body column (4 spaces already emitted before the first line).
fn indent_cont(e: &str) -> String {
    let mut lines = e.lines();
    let first = lines.next().unwrap_or("").to_string();
    let rest: Vec<String> = lines.map(|l| l.to_string()).collect();
    if rest.is_empty() {
        first
    } else {
        format!("{first}\n{}", rest.join("\n"))
    }
}

fn lower_first(s: &str) -> String {
    let mut c = s.chars();
    match c.next() {
        Some(f) => format!("{}{}", f.to_ascii_lowercase(), c.as_str()),
        None => String::new(),
    }
}

// ---- the compiler-verdict subprocess -------------------------------------

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Verdict {
    Accept,
    Reject,
    Timeout,
}

impl Verdict {
    fn short(self) -> &'static str {
        match self {
            Verdict::Accept => "ACCEPT",
            Verdict::Reject => "REJECT",
            Verdict::Timeout => "TIMEOUT",
        }
    }
}

/// Run `<bin> check src/Main.sky` in `workdir`, streaming its output; return
/// ACCEPT the instant `accept_marker` is seen (type-check succeeded — killed
/// before codegen / go build), REJECT if the process exits without it, TIMEOUT on
/// the wall ceiling. Also returns up to ~40 captured lines (for reporting a
/// divergence's rejecting-side diagnostic).
fn run_check(bin: &Path, workdir: &Path, accept_marker: &str) -> (Verdict, String) {
    // Clean prior artifacts so neither compiler's cache/output confuses the other.
    let _ = std::fs::remove_dir_all(workdir.join("sky-out"));
    let _ = std::fs::remove_dir_all(workdir.join(".skycache"));

    let mut child = match Command::new(bin)
        .arg("check")
        .arg("src/Main.sky")
        .current_dir(workdir)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
    {
        Ok(c) => c,
        Err(e) => return (Verdict::Reject, format!("spawn error: {e}")),
    };

    // Merge stdout + stderr onto one channel of lines.
    let (tx, rx) = mpsc::channel::<String>();
    for stream in [child.stdout.take().map(Stream::Out), child.stderr.take().map(Stream::Err)]
        .into_iter()
        .flatten()
    {
        let tx = tx.clone();
        std::thread::spawn(move || match stream {
            Stream::Out(s) => pump(s, tx),
            Stream::Err(s) => pump(s, tx),
        });
    }
    drop(tx);

    let deadline = Instant::now() + CHECK_TIMEOUT;
    let mut captured: Vec<String> = Vec::new();
    let mut verdict = Verdict::Reject; // default: exited without marker
    loop {
        let remaining = deadline.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            verdict = Verdict::Timeout;
            break;
        }
        match rx.recv_timeout(remaining.min(Duration::from_millis(500))) {
            Ok(line) => {
                if captured.len() < 40 {
                    captured.push(line.clone());
                }
                if line.contains(accept_marker) {
                    verdict = Verdict::Accept;
                    break;
                }
            }
            Err(mpsc::RecvTimeoutError::Timeout) => {
                if Instant::now() >= deadline {
                    verdict = Verdict::Timeout;
                    break;
                }
            }
            Err(mpsc::RecvTimeoutError::Disconnected) => break, // process ended
        }
    }

    let _ = child.kill();
    let _ = child.wait();
    (verdict, captured.join("\n"))
}

enum Stream {
    Out(std::process::ChildStdout),
    Err(std::process::ChildStderr),
}

fn pump<R: std::io::Read>(r: R, tx: mpsc::Sender<String>) {
    let reader = std::io::BufReader::new(r);
    for line in reader.lines().map_while(Result::ok) {
        if tx.send(line).is_err() {
            break;
        }
    }
}

// ---- binary discovery ----------------------------------------------------

/// Discover the Rust `sky` binary: `SKY_RUST_BIN`, else `sky-out/sky`, else the
/// cargo release bin.
fn find_rust_bin(root: &Path) -> Option<PathBuf> {
    if let Ok(p) = std::env::var("SKY_RUST_BIN") {
        let p = PathBuf::from(p);
        if p.exists() {
            return Some(p);
        }
    }
    let candidates = [
        root.join("sky-out/sky"),
        PathBuf::from(format!(
            "{}/.cargo/bin/release/sky",
            std::env::var("HOME").unwrap_or_default()
        )),
    ];
    candidates.into_iter().find(|p| p.exists())
}

/// Discover the Haskell oracle binary: `SKY_ORACLE_BIN`, else glob the cabal
/// dist-newstyle build tree. Returns None (→ gate SKIPS) when not present.
fn find_oracle_bin(root: &Path) -> Option<PathBuf> {
    if let Ok(p) = std::env::var("SKY_ORACLE_BIN") {
        let p = PathBuf::from(p);
        if p.exists() {
            return Some(p);
        }
    }
    // dist-newstyle/build/<arch>/ghc-<ver>/sky-compiler-<ver>/x/sky/build/sky/sky
    let base = root.join("dist-newstyle/build");
    let mut found = None;
    for arch in read_dirs(&base) {
        for ghc in read_dirs(&arch) {
            for pkg in read_dirs(&ghc) {
                let cand = pkg.join("x/sky/build/sky/sky");
                if cand.exists() {
                    found = Some(cand);
                }
            }
        }
    }
    found
}

fn read_dirs(dir: &Path) -> Vec<PathBuf> {
    let mut v: Vec<PathBuf> = std::fs::read_dir(dir)
        .map(|rd| rd.filter_map(|e| e.ok().map(|e| e.path())).filter(|p| p.is_dir()).collect())
        .unwrap_or_default();
    v.sort();
    v
}

// ---- ledgered-divergence filter ------------------------------------------

/// The `known-divergences.toml` entries that this gate must ALLOW rather than fail
/// on. D001 (export enforcement on stdlib) is the only active entry, and this
/// generator never imports a stdlib-private name, so it cannot be triggered here —
/// but the filter is kept so a future ledgered TYPE-CHECK divergence is honoured
/// automatically. Returns the set of divergence ids read from the ledger.
fn ledger_ids(root: &Path) -> Vec<String> {
    let text = std::fs::read_to_string(root.join("known-divergences.toml")).unwrap_or_default();
    text.lines()
        .filter_map(|l| {
            let t = l.trim_start();
            let rest = t.strip_prefix("id")?.trim_start().strip_prefix('=')?.trim_start();
            let rest = rest.strip_prefix('"')?;
            let end = rest.find('"')?;
            Some(rest[..end].to_string())
        })
        .collect()
}

// ---- findings ------------------------------------------------------------

struct Divergence {
    index: usize,
    seed: u64,
    rust: Verdict,
    oracle: Verdict,
    program: String,
    rejecting_output: String,
}

// ---- the gate ------------------------------------------------------------

pub fn run(args: &[String], root: &Path) -> i32 {
    let count: usize = args
        .iter()
        .find_map(|a| a.strip_prefix("--count="))
        .and_then(|s| s.parse().ok())
        .unwrap_or(DEFAULT_COUNT);
    let verbose = args.iter().any(|a| a == "-v" || a == "--verbose");
    // `--emit-only` prints the generated programs (no compiler) — a fast way to
    // eyeball the generator + confirm determinism without the oracle.
    let emit_only = args.iter().any(|a| a == "--emit-only");

    println!("welltyped gate — well-typed differential fuzzer (Rust vs Haskell oracle)\n");
    println!("base-seed = {BASE_SEED:#018x} (fixed → reproducible)");
    println!("count     = {count}   max-depth = {MAX_DEPTH}\n");

    // ---- determinism (L4): the generator is a pure function of its seed. Build
    // every program TWICE and assert byte-identical text run-to-run. ----
    let programs: Vec<(u64, String)> = (0..count)
        .map(|i| {
            let seed = prog_seed(i);
            (seed, Gen::new(seed).gen_program())
        })
        .collect();
    let programs2: Vec<String> = (0..count).map(|i| Gen::new(prog_seed(i)).gen_program()).collect();
    let det_ok = programs.iter().map(|(_, p)| p).eq(programs2.iter());
    println!(
        "determinism: same seed → identical program text over {count} programs = {}",
        if det_ok { "OK" } else { "VIOLATED" }
    );

    if emit_only {
        for (i, (seed, prog)) in programs.iter().enumerate() {
            println!("\n===== program #{i} (seed {seed:#018x}) =====\n{prog}");
        }
        return if det_ok { 0 } else { 1 };
    }

    // ---- discover both compilers ----
    let Some(rust_bin) = find_rust_bin(root) else {
        eprintln!("welltyped: no Rust `sky` binary found (set SKY_RUST_BIN or build sky-out/sky)");
        return 1;
    };
    let oracle_bin = find_oracle_bin(root);
    println!("rust   = {}", rust_bin.display());
    match &oracle_bin {
        Some(p) => println!("oracle = {}\n", p.display()),
        None => {
            println!("oracle = <not found>\n");
            println!(
                "welltyped gate: SKIP — the Haskell oracle binary is not available (it is not \
                 built in CI; this gate is LOCAL / release-only, exactly like `xtask divergences`). \
                 Determinism of the generator was still verified above. Set SKY_ORACLE_BIN to run \
                 the differential."
            );
            return if det_ok { 0 } else { 1 };
        }
    }
    let oracle_bin = oracle_bin.unwrap();
    let allow = ledger_ids(root);
    if !allow.is_empty() {
        println!("ledgered divergences (allowed): {}\n", allow.join(", "));
    }

    // ---- a single scratch project we rewrite per program ----
    let work = std::env::temp_dir().join(format!("sky-welltyped-{}", std::process::id()));
    let _ = std::fs::create_dir_all(work.join("src"));
    let _ = std::fs::write(
        work.join("sky.toml"),
        "name = \"welltyped\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    );

    let start = Instant::now();
    let mut divergences: Vec<Divergence> = Vec::new();
    let mut agree = 0usize;
    let mut both_accept = 0usize;
    let mut both_reject = 0usize;

    for (i, (seed, prog)) in programs.iter().enumerate() {
        let _ = std::fs::write(work.join("src/Main.sky"), prog);

        let (rv, _ro) = run_check(&rust_bin, &work, "Generating Go");
        let (ov, oo) = run_check(&oracle_bin, &work, "Types OK");

        let same = rv == ov;
        if same {
            agree += 1;
            match rv {
                Verdict::Accept => both_accept += 1,
                Verdict::Reject => both_reject += 1,
                Verdict::Timeout => {}
            }
        } else {
            // Timeouts are infrastructure noise, not a parity claim — record but
            // do not treat a lone TIMEOUT-vs-verdict as a divergence bug.
            let is_timeout = rv == Verdict::Timeout || ov == Verdict::Timeout;
            let rejecting_output = if rv == Verdict::Reject { String::new() } else { oo.clone() };
            let div = Divergence {
                index: i,
                seed: *seed,
                rust: rv,
                oracle: ov,
                program: prog.clone(),
                rejecting_output,
            };
            if is_timeout {
                eprintln!("  · #{i}: TIMEOUT (rust={} oracle={}) — infra, not a parity bug", rv.short(), ov.short());
            } else {
                divergences.push(div);
            }
        }

        if verbose || (i + 1) % 20 == 0 {
            eprintln!(
                "  · {}/{} checked — agree {agree}, divergences {}",
                i + 1,
                count,
                divergences.len()
            );
        }
    }

    let _ = std::fs::remove_dir_all(&work);

    // ---- report ----
    let elapsed = start.elapsed();
    println!("\n{}", "-".repeat(72));
    println!(
        "checked {count} well-typed programs in {:.1}s",
        elapsed.as_secs_f64()
    );
    println!(
        "agreement: {agree}/{count}  (both-accept {both_accept}, both-reject {both_reject})"
    );
    println!("divergences (non-ledgered): {}", divergences.len());
    println!("{}", "-".repeat(72));

    if !divergences.is_empty() {
        println!("\nREAL PARITY DIVERGENCES ({}):", divergences.len());
        for d in &divergences {
            println!(
                "\n  #{} seed={:#018x}  rust={}  oracle={}",
                d.index,
                d.seed,
                d.rust.short(),
                d.oracle.short()
            );
            println!("  --- program ---");
            for l in d.program.lines() {
                println!("  | {l}");
            }
            if !d.rejecting_output.is_empty() {
                println!("  --- rejecting compiler output (head) ---");
                for l in d.rejecting_output.lines().take(20) {
                    println!("  > {l}");
                }
            }
        }
        println!(
            "\nWELLTYPED GATE: FAIL — {} well-typed program(s) on which the Rust compiler and the \
             Haskell oracle DISAGREE (not a ledgered divergence). Each is a real parity / soundness \
             finding: triage the minimal program above (do NOT paper over it in known-divergences.toml).",
            divergences.len()
        );
        return 1;
    }

    if !det_ok {
        println!("\nWELLTYPED GATE: FAIL — generator is non-deterministic (see determinism line).");
        return 1;
    }

    println!(
        "\nWELLTYPED GATE: PASS ({count} well-typed programs, Rust ⇄ oracle agree on every one, \
         {both_accept} accepted / {both_reject} rejected by both; generator deterministic)"
    );
    0
}

/// Per-program seed: a pure function of the index (so program `i` is identical
/// across runs). Mixed through splitmix64 so consecutive indices don't correlate.
fn prog_seed(i: usize) -> u64 {
    let mut m = SplitMix64::new(BASE_SEED ^ (i as u64).wrapping_mul(0x9E37_79B9_7F4A_7C15));
    m.next_u64()
}
