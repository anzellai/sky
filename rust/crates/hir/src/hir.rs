//! The resolved High-level IR (doc 05 §3). One-to-one with the AST, but every
//! name slot carries a `Res` / `DefId` rather than a re-decodable string. Bodies
//! own three arenas (expr / pattern / type); ids index into them.

use crate::ids::{CtorRef, LocalId, Res, TypeRes};
use base::{DefId, Name, Span};
use la_arena::{Arena, Idx};

pub type ExprId = Idx<Expr>;
pub type PatId = Idx<Pattern>;
pub type TypeId = Idx<Type>;

/// A resolved expression (doc 05 §3).
#[derive(Clone, PartialEq, Debug)]
pub enum Expr {
    Int(i64),
    Float(f64),
    Str(Box<str>),
    Chr(Box<str>),
    Bool(bool),
    Unit,
    List(Vec<ExprId>),
    Tuple(Vec<ExprId>),
    /// Field order carried in insertion order; lowering sorts by `_fieldIndex`.
    Record(Vec<(Name, ExprId)>),
    Update {
        base: ExprId,
        fields: Vec<(Name, ExprId)>,
    },
    /// A resolved value / constructor reference. `res` is `Res::Error` on an
    /// unresolved name (a diagnostic was emitted).
    Var(Res),
    Negate(ExprId),
    Lambda {
        params: Vec<PatId>,
        body: ExprId,
    },
    Call(ExprId, Vec<ExprId>),
    /// A binary operator, desugared to its kernel reference (`Basics.add`, …).
    Binop {
        op: Name,
        res: Res,
        lhs: ExprId,
        rhs: ExprId,
    },
    /// `if c1 then e1 else if c2 then e2 … else ef`.
    If {
        arms: Vec<(ExprId, ExprId)>,
        els: ExprId,
    },
    Let {
        defs: Vec<LocalDef>,
        body: ExprId,
    },
    Case {
        subject: ExprId,
        branches: Vec<CaseBranch>,
    },
    Accessor(Name),
    Access(ExprId, Name),
    /// Recovery node for an unrepresentable / broken sub-expression (L7).
    Error,
}

/// A `let` binding (define or destructure).
#[derive(Clone, PartialEq, Debug)]
pub struct LocalDef {
    /// The binder(s) this def introduces, for scope bookkeeping.
    pub binders: Vec<(Name, LocalId)>,
    /// For a destructure, the pattern; for a plain define, `None`.
    pub pat: Option<PatId>,
    pub params: Vec<PatId>,
    pub body: ExprId,
}

/// A `case` branch.
#[derive(Clone, PartialEq, Debug)]
pub struct CaseBranch {
    pub pat: PatId,
    pub body: ExprId,
}

/// A resolved pattern (doc 05 §3).
#[derive(Clone, PartialEq, Debug)]
pub enum Pattern {
    Anything,
    Var(LocalId),
    Unit,
    Bool(bool),
    Chr(Box<str>),
    Str(Box<str>),
    Int(i64),
    Float(f64),
    Record(Vec<(Name, LocalId)>),
    Alias(PatId, LocalId),
    Tuple(Vec<PatId>),
    List(Vec<PatId>),
    Cons(PatId, PatId),
    /// A constructor pattern. `ctor` is `None` when the head did not resolve to
    /// a known constructor — Elm treats a bare unknown upper-name pattern head
    /// as an error only when qualified; unqualified degrades (doc 05 §12).
    Ctor {
        ctor: Option<CtorRef>,
        name: Name,
        args: Vec<PatId>,
    },
    Error,
}

/// A resolved type (doc 05 §3).
#[derive(Clone, PartialEq, Debug)]
pub enum Type {
    Var(Name),
    Con {
        con: Option<TypeRes>,
        name: Name,
        args: Vec<TypeId>,
    },
    /// A qualified type whose qualifier is a Go FFI package (class-b) — kept
    /// distinct so the gate never flags it as a resolver bug.
    Foreign {
        package: Name,
        name: Name,
        args: Vec<TypeId>,
    },
    Lambda(TypeId, TypeId),
    Tuple(Vec<TypeId>),
    Record(Vec<(Name, TypeId)>, Option<Name>),
    Unit,
    Error,
}

/// One resolved top-level definition body, with its arenas.
#[derive(Clone, Debug, Default)]
pub struct Body {
    pub exprs: Arena<Expr>,
    pub pats: Arena<Pattern>,
    pub types: Arena<Type>,
    /// The root expression (a value / function body). `None` for annotation-only
    /// or type declarations.
    pub root: Option<ExprId>,
    /// A resolved type annotation root, if the def carried one.
    pub anno: Option<TypeId>,
}

impl Body {
    pub fn expr(&mut self, e: Expr) -> ExprId {
        self.exprs.alloc(e)
    }
    pub fn pat(&mut self, p: Pattern) -> PatId {
        self.pats.alloc(p)
    }
    pub fn ty(&mut self, t: Type) -> TypeId {
        self.types.alloc(t)
    }
}

/// Metadata for a top-level definition (doc 05 §1 `ModuleDecls`).
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct TopDef {
    pub def: DefId,
    pub name: Name,
    pub span: Span,
}
