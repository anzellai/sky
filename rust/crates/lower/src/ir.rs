//! The typed Go-IR (doc 07 §1). Every `GoExpr` carries the `GoTy` it lowered
//! from; codegen (doc 08) only renders. `GoName` is an already-mangled Go
//! identifier (module-prefixed / reserved-rewritten at construction time, doc 08
//! §5), so no emit path re-mangles.
//!
//! Deviation from doc 07 (reported): `GoName` wraps a `String` rather than an
//! interned `Name`. The mangling is a deterministic pure function of the Sky
//! name, so the determinism law (L4) holds — interning is a perf optimisation
//! deferred past the M4 "examples must run" gate.

use base::Name;

/// A primitive Go type (doc 07 §1 `Prim`).
#[derive(Clone, PartialEq, Eq, Hash, Debug)]
pub enum Prim {
    Int,
    Str,
    Bool,
    Float,
    Rune,
    Bytes,
}

impl Prim {
    pub fn go_name(&self) -> &'static str {
        match self {
            Prim::Int => "int",
            Prim::Str => "string",
            Prim::Bool => "bool",
            Prim::Float => "float64",
            Prim::Rune => "rune",
            Prim::Bytes => "[]byte",
        }
    }
}

/// Structural Go type — the ONLY representation of a Go type in the IR (doc 07
/// §1). No `Raw(String)` escape hatch.
#[derive(Eq, Debug)]
pub enum GoTy {
    Bare(Prim),
    Unit,
    Any,
    Func(Vec<GoTy>, Box<GoTy>),
    /// `Name[args…]` — nominal named type (`Main_Msg`, `rt.SkyList[T]`, …).
    Named(String, Vec<GoTy>),
    /// `[]T`.
    Slice(Box<GoTy>),
    /// `map[K]V`.
    Map(Box<GoTy>, Box<GoTy>),
    /// Anonymous struct in `_fieldIndex` order.
    Struct(Vec<(Name, GoTy)>),
    /// Parametric tuple `rt.T2[…]` / `rt.T3[…]`; arity ≥ 4 → `rt.SkyTupleN`.
    Tuple(Vec<GoTy>),
    /// A Go generic type parameter (`T1`, `E`, …) by its name.
    TyVar(String),
}

// A Go type is as deep as the Sky type it lowers: a curried constructor of a
// record of n fields is `func(f0) func(f1) … R`, n levels. Derived clone,
// equality, hashing and drop recurse once per level; each level goes through
// `base::ensure_stack` instead (see `ty::Ty`, whose derived clone overflowed a
// 2 MiB LSP worker on a 12,000-field codec). The impls follow the derived ones
// exactly (same fields, same order, the same variant index hashed as `isize`).
impl Clone for GoTy {
    fn clone(&self) -> Self {
        base::ensure_stack(|| match self {
            GoTy::Bare(p) => GoTy::Bare(p.clone()),
            GoTy::Unit => GoTy::Unit,
            GoTy::Any => GoTy::Any,
            GoTy::Func(ins, out) => GoTy::Func(ins.clone(), out.clone()),
            GoTy::Named(n, args) => GoTy::Named(n.clone(), args.clone()),
            GoTy::Slice(t) => GoTy::Slice(t.clone()),
            GoTy::Map(k, v) => GoTy::Map(k.clone(), v.clone()),
            GoTy::Struct(fs) => GoTy::Struct(fs.clone()),
            GoTy::Tuple(xs) => GoTy::Tuple(xs.clone()),
            GoTy::TyVar(n) => GoTy::TyVar(n.clone()),
        })
    }
}

impl PartialEq for GoTy {
    fn eq(&self, other: &Self) -> bool {
        base::ensure_stack(|| match (self, other) {
            (GoTy::Bare(a), GoTy::Bare(b)) => a == b,
            (GoTy::Unit, GoTy::Unit) | (GoTy::Any, GoTy::Any) => true,
            (GoTy::Func(i1, o1), GoTy::Func(i2, o2)) => i1 == i2 && o1 == o2,
            (GoTy::Named(n1, a1), GoTy::Named(n2, a2)) => n1 == n2 && a1 == a2,
            (GoTy::Slice(a), GoTy::Slice(b)) => a == b,
            (GoTy::Map(k1, v1), GoTy::Map(k2, v2)) => k1 == k2 && v1 == v2,
            (GoTy::Struct(a), GoTy::Struct(b)) => a == b,
            (GoTy::Tuple(a), GoTy::Tuple(b)) => a == b,
            (GoTy::TyVar(a), GoTy::TyVar(b)) => a == b,
            _ => false,
        })
    }
}

impl std::hash::Hash for GoTy {
    fn hash<H: std::hash::Hasher>(&self, state: &mut H) {
        base::ensure_stack(|| match self {
            GoTy::Bare(p) => {
                0isize.hash(state);
                p.hash(state);
            }
            GoTy::Unit => 1isize.hash(state),
            GoTy::Any => 2isize.hash(state),
            GoTy::Func(ins, out) => {
                3isize.hash(state);
                ins.hash(state);
                out.hash(state);
            }
            GoTy::Named(n, args) => {
                4isize.hash(state);
                n.hash(state);
                args.hash(state);
            }
            GoTy::Slice(t) => {
                5isize.hash(state);
                t.hash(state);
            }
            GoTy::Map(k, v) => {
                6isize.hash(state);
                k.hash(state);
                v.hash(state);
            }
            GoTy::Struct(fs) => {
                7isize.hash(state);
                fs.hash(state);
            }
            GoTy::Tuple(xs) => {
                8isize.hash(state);
                xs.hash(state);
            }
            GoTy::TyVar(n) => {
                9isize.hash(state);
                n.hash(state);
            }
        })
    }
}

impl Drop for GoTy {
    fn drop(&mut self) {
        // Move the children out and drop them inside the guard: the drop glue
        // that runs after this function then finds only leaves.
        match self {
            GoTy::Func(ins, out) => {
                let ins = std::mem::take(ins);
                let out = std::mem::replace(&mut **out, GoTy::Unit);
                base::ensure_stack(move || drop((ins, out)));
            }
            GoTy::Slice(t) => {
                let t = std::mem::replace(&mut **t, GoTy::Unit);
                base::ensure_stack(move || drop(t));
            }
            GoTy::Map(k, v) => {
                let k = std::mem::replace(&mut **k, GoTy::Unit);
                let v = std::mem::replace(&mut **v, GoTy::Unit);
                base::ensure_stack(move || drop((k, v)));
            }
            GoTy::Named(_, xs) | GoTy::Tuple(xs) if !xs.is_empty() => {
                let xs = std::mem::take(xs);
                base::ensure_stack(move || drop(xs));
            }
            GoTy::Struct(fs) if !fs.is_empty() => {
                let fs = std::mem::take(fs);
                base::ensure_stack(move || drop(fs));
            }
            _ => {}
        }
    }
}

/// `GoTy::Any` with a `'static` lifetime. `GoTy` has a `Drop` impl, so
/// `&GoTy::Any` is a temporary, not a promoted constant.
pub static GO_ANY: GoTy = GoTy::Any;

impl GoTy {
    /// Split `func(ps) r` into `(ps, r)`, or give the type back. `GoTy` has a
    /// `Drop` impl, so a pattern cannot move the parts out of it; this does.
    pub fn into_func(mut self) -> Result<(Vec<GoTy>, GoTy), GoTy> {
        match &mut self {
            GoTy::Func(ps, r) => Ok((std::mem::take(ps), std::mem::replace(&mut **r, GoTy::Unit))),
            _ => Err(self),
        }
    }

    /// The element of a slice type, or the type back (see [`GoTy::into_func`]).
    pub fn into_slice(mut self) -> Result<GoTy, GoTy> {
        match &mut self {
            GoTy::Slice(t) => Ok(std::mem::replace(&mut **t, GoTy::Unit)),
            _ => Err(self),
        }
    }

    /// The element type of a slice — structural (`[]T` → `T`). `Any` for a
    /// non-slice (defensive; the lowerer only calls this on known slices).
    pub fn elem_ty(&self) -> GoTy {
        match self {
            GoTy::Slice(t) => (**t).clone(),
            GoTy::Named(n, args) if n == "rt.SkyList" && args.len() == 1 => args[0].clone(),
            _ => GoTy::Any,
        }
    }
}

/// Why a coercion is justified (doc 07 §6). Rendered as the `/* … */` comment.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum CoerceReason {
    FfiReturn,
    WireDecode,
    TeaDispatch,
    PrimitiveJoin,
    GenericErase,
}

impl CoerceReason {
    pub fn comment(&self) -> &'static str {
        match self {
            CoerceReason::FfiReturn => "FFI return",
            CoerceReason::WireDecode => "wire decode",
            CoerceReason::TeaDispatch => "TEA dispatch",
            CoerceReason::PrimitiveJoin => "primitive join",
            CoerceReason::GenericErase => "generic erase",
        }
    }
}

/// A typed Go binary operator.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum GoBin {
    Add,
    Sub,
    Mul,
    Eq,
    Ne,
    Lt,
    Gt,
    Le,
    Ge,
    And,
    Or,
}

#[derive(PartialEq, Debug)]
pub struct GoExpr {
    pub kind: GoExprKind,
    pub ty: GoTy,
}

impl GoExpr {
    pub fn new(kind: GoExprKind, ty: GoTy) -> Self {
        GoExpr { kind, ty }
    }

    /// Take the node's kind, leaving `Nil` in its place. `GoExpr` has a `Drop`
    /// impl, so a kind cannot be moved out of it by a pattern; this is the way
    /// to consume one.
    pub fn take_kind(&mut self) -> GoExprKind {
        std::mem::replace(&mut self.kind, GoExprKind::Nil)
    }

    /// Consume the node into its kind and type (see [`GoExpr::take_kind`]).
    pub fn into_parts(mut self) -> (GoExprKind, GoTy) {
        let kind = self.take_kind();
        let ty = std::mem::replace(&mut self.ty, GoTy::Unit);
        (kind, ty)
    }
}

// A lowered expression is as deep as the Sky expression it comes from: a
// left-nested `|>` pipeline or a generated record codec gives one `Call` level
// per step. The derived clone and drop glue recurse once per level, which
// overflowed a 2 MiB thread on a 20,000-step pipeline when the program was
// dropped. Each level goes through `base::ensure_stack` instead, so depth is
// bounded by memory, as in the walkers that build and read the tree.
impl Clone for GoExpr {
    fn clone(&self) -> Self {
        base::ensure_stack(|| GoExpr {
            kind: self.kind.clone(),
            ty: self.ty.clone(),
        })
    }
}

impl Drop for GoExpr {
    fn drop(&mut self) {
        let kind = self.take_kind();
        base::ensure_stack(move || drop(kind));
    }
}

#[derive(Clone, PartialEq, Debug)]
pub enum GoExprKind {
    Ident(String),
    IntLit(i64),
    FloatLit(f64),
    StrLit(String),
    BoolLit(bool),
    Nil,
    /// `f(args…)`.
    Call(Box<GoExpr>, Vec<GoExpr>),
    /// `f[T…](args…)`.
    GenericCall(String, Vec<GoTy>, Vec<GoExpr>),
    /// `x.field`.
    Selector(Box<GoExpr>, String),
    /// `x.(T)` — a Go type assertion. NOT a coerce: it is typed dispatch on a
    /// sealed-interface value (a sealed-ADT variant downcast), always guarded by
    /// a preceding tag/ok check so it never panics in practice. Emitted for
    /// direct typed `.V{i}` reads on sealed-ADT variant structs.
    TypeAssert(Box<GoExpr>, GoTy),
    /// `x[i]`.
    Index(Box<GoExpr>, Box<GoExpr>),
    /// `[]T{…}`.
    SliceLit(GoTy, Vec<GoExpr>),
    /// `Name{Field: v, …}` — struct literal by named fields.
    StructLit(String, Vec<(String, GoExpr)>),
    /// `func(params) ret { body }`.
    FuncLit(Vec<GoParam>, GoTy, Vec<GoStmt>),
    Binary(GoBin, Box<GoExpr>, Box<GoExpr>),
    /// A typed IIFE — `func() ret { stmts }()`. `ty` is the return type.
    Block(Vec<GoStmt>),
    /// The only narrowing node (doc 07 §6).
    Coerce {
        inner: Box<GoExpr>,
        from: GoTy,
        to: GoTy,
        reason: CoerceReason,
    },
    /// `any(x)` — widen to `any` (the trivial upcast, never a runtime op).
    Widen(Box<GoExpr>),
}

#[derive(Clone, PartialEq, Debug)]
pub struct GoParam {
    pub name: String,
    pub ty: GoTy,
}

#[derive(Clone, PartialEq, Debug)]
pub enum GoStmt {
    Expr(GoExpr),
    /// `name := expr`.
    Short(String, GoExpr),
    /// `_ = expr` — discard (auto-forced task side effects, doc: `let _ = …`).
    Discard(GoExpr),
    /// `base.field = value` — record-update field assignment.
    AssignField(GoExpr, String, GoExpr),
    /// `name = expr` — plain assignment to an existing local (used by the TCO
    /// pass to reassign a function parameter before `continue`).
    Assign(String, GoExpr),
    /// `var name T` — a bare typed declaration. Emitted before assigning a
    /// SELF-RECURSIVE local function closure: Go's `name := func(){…name…}` leaves
    /// `name` undefined inside its own initializer, so a recursive `let` helper
    /// needs `var name T` then `name = func…` (issue #162).
    VarDecl(String, GoTy),
    /// `for { <body> }` — an unconditional forever-loop wrapping a TCO'd
    /// tail-recursive function body. Every tail leaf is a `Return`; every tail
    /// self-call is param-reassignment + `Continue`.
    Loop(Vec<GoStmt>),
    /// `continue` — the TCO jump back to the top of the enclosing `Loop`.
    Continue,
    Return(Option<GoExpr>),
    /// `if cond { then } else { els }`.
    If(GoExpr, Vec<GoStmt>, Vec<GoStmt>),
    /// `if <binder>, <ok> := <subj>.(<ty>); <ok> { then }` — a comma-ok type
    /// assertion guard. The idiomatic Go type-switch-case body for a sealed-ADT
    /// variant match: typed dispatch that binds the concrete variant struct
    /// `binder` (used for typed `.V{i}` field reads) when the assertion holds.
    /// NOT a coerce — it never panics.
    IfTypeAssert {
        binder: String,
        ok: String,
        subj: GoExpr,
        ty: GoTy,
        then: Vec<GoStmt>,
    },
    Comment(String),
    /// `{ stmts }` — a bare Go block statement: a new lexical scope with no
    /// closure. The tail-block flattening pass (`shape::flatten_tail_blocks`)
    /// emits it in place of `return func() T { stmts }()` when the IIFE body
    /// declares locals, so they keep their own scope without a closure.
    Scope(Vec<GoStmt>),
}

/// A Go top-level item.
#[derive(Clone, PartialEq, Debug)]
pub enum GoItem {
    Func(GoFuncDecl),
    /// `type Name = def` / `type Name struct {…}` / iota enum / sealed iface.
    Type(String, GoTypeDef),
    /// `var Name Ty = expr` (or no init).
    Var(String, GoTy, Option<GoExpr>),
    /// A raw `init()` body (registration calls, port defaults). Emitted verbatim,
    /// deterministically ordered by the lowerer.
    Init(Vec<GoStmt>),
    /// A pre-rendered raw declaration (for machinery the structural IR does not
    /// yet model — ADT constructor/arm/decode families). Deterministic string.
    Raw(String),
}

#[derive(Clone, PartialEq, Debug)]
pub struct GoFuncDecl {
    pub name: String,
    pub type_params: Vec<(String, GoTy)>,
    pub params: Vec<GoParam>,
    pub ret: GoTy,
    pub body: Vec<GoStmt>,
    /// A leading `// SKY-ORIGIN` / doc comment, if any.
    pub doc: Option<String>,
}

#[derive(Clone, PartialEq, Debug)]
pub enum GoTypeDef {
    /// `type Name = rt.SkyADT` alias + the ctor machinery is emitted as Raw items.
    AdtAlias,
    /// A sealed-interface ADT: `type Name interface { SkyVariantTag() int;
    /// SkyVariantName() string }` + one concrete `Name_<Ctor>_V` struct per
    /// variant (with typed `V0..Vn` payload fields) implementing the interface.
    /// Each entry: `(ctor_name, tag, field_go_types)`. Ctors / init-registration
    /// are emitted as `Raw` items by the lowerer (mirrors `AdtAlias`).
    SealedIface(Vec<(String, usize, Vec<GoTy>)>),
    /// `type Name int` (a named int, so the value keeps its Sky type at run
    /// time) + `const ( … = iota )` + a `SkyEnumName` method.
    IotaEnum(Vec<String>),
    /// `type Name struct { … }` (record `_R`).
    Struct(Vec<(String, GoTy)>),
    /// `type Name = <rt.Type>` — a transparent alias to a runtime struct whose
    /// fields are identical to this record's (e.g. `Sky.Core.Http.HttpResponse`
    /// → `rt.HttpResponse`). Lets a value the runtime kernel produces as the
    /// `rt` type narrow to the emitted type by a reflection-free
    /// `v.(rt.Type)` assertion, rather than the reflect `ConvertibleTo` the
    /// distinct-struct form forces (TinyGo cannot run that). The ctor is still
    /// emitted as a `Raw` item (the alias is transparent, so building by field
    /// name works). Mirrors `AdtAlias` for records.
    RuntimeAlias(String),
    /// `type Name = Underlying` transparent alias.
    Alias(GoTy),
}
