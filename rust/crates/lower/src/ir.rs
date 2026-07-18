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
#[derive(Clone, PartialEq, Eq, Hash, Debug)]
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

impl GoTy {
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

#[derive(Clone, PartialEq, Debug)]
pub struct GoExpr {
    pub kind: GoExprKind,
    pub ty: GoTy,
}

impl GoExpr {
    pub fn new(kind: GoExprKind, ty: GoTy) -> Self {
        GoExpr { kind, ty }
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
    Return(Option<GoExpr>),
    /// `if cond { then } else { els }`.
    If(GoExpr, Vec<GoStmt>, Vec<GoStmt>),
    Comment(String),
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
    /// `type Name = int` + `const ( … = iota )`.
    IotaEnum(Vec<String>),
    /// `type Name struct { … }` (record `_R`).
    Struct(Vec<(String, GoTy)>),
    /// `type Name = Underlying` transparent alias.
    Alias(GoTy),
}
