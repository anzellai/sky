//! Typed AST view over the untyped CST (doc 04 §12), rust-analyzer style:
//! zero-cost wrapper structs implementing [`AstNode`], plus exhaustive enum
//! views (`Expr`/`Type`/`Pattern`/`Decl`) with no `_ =>` catch-all (L6). `hir`
//! (doc 05) consumes *this*, never a raw `SyntaxKind`.

use crate::kind::{SyntaxKind, SyntaxNode, SyntaxToken};
use crate::TextRange;
// Token-kind variants that don't collide with the node wrapper struct names
// (those must stay `SyntaxKind::`-qualified — see the enum-view impls).
use SyntaxKind::{Char, Colon2, Float, HexInt, Int, LowerIdent, Op, TrueKw, UpperIdent};

/// A typed wrapper over a CST node.
pub trait AstNode {
    fn can_cast(kind: SyntaxKind) -> bool
    where
        Self: Sized;
    fn cast(node: SyntaxNode) -> Option<Self>
    where
        Self: Sized;
    fn syntax(&self) -> &SyntaxNode;
}

macro_rules! ast_node {
    ($(#[$m:meta])* $name:ident, $kind:ident) => {
        $(#[$m])*
        #[derive(Clone, PartialEq, Eq, Hash)]
        pub struct $name(SyntaxNode);

        impl AstNode for $name {
            fn can_cast(kind: SyntaxKind) -> bool {
                kind == SyntaxKind::$kind
            }
            fn cast(node: SyntaxNode) -> Option<Self> {
                if node.kind() == SyntaxKind::$kind {
                    Some($name(node))
                } else {
                    None
                }
            }
            fn syntax(&self) -> &SyntaxNode {
                &self.0
            }
        }
    };
}

/// Typed child/children/token queries (ra `support` module).
mod support {
    use super::{AstNode, SyntaxKind, SyntaxNode, SyntaxToken};

    pub fn child<N: AstNode>(parent: &SyntaxNode) -> Option<N> {
        parent.children().find_map(N::cast)
    }

    pub fn children<N: AstNode>(parent: &SyntaxNode) -> impl Iterator<Item = N> {
        parent.children().filter_map(N::cast)
    }

    pub fn token(parent: &SyntaxNode, kind: SyntaxKind) -> Option<SyntaxToken> {
        parent
            .children_with_tokens()
            .filter_map(|e| e.into_token())
            .find(|t| t.kind() == kind)
    }
}

// ---- node structs --------------------------------------------------------

ast_node!(SourceFile, SourceFile);
ast_node!(ModuleHeader, ModuleHeader);
ast_node!(ModuleName, ModuleName);
ast_node!(ExposingList, ExposingList);
ast_node!(Import, Import);
ast_node!(ImportAlias, ImportAlias);
ast_node!(ImportExposing, ImportExposing);

ast_node!(ValueDecl, ValueDecl);
ast_node!(TypeAnnoDecl, TypeAnnoDecl);
ast_node!(UnionDecl, UnionDecl);
ast_node!(AliasDecl, AliasDecl);
ast_node!(ForeignDecl, ForeignDecl);
ast_node!(ParamList, ParamList);
ast_node!(TypeVarList, TypeVarList);
ast_node!(UnionVariantList, UnionVariantList);
ast_node!(UnionVariant, UnionVariant);

ast_node!(TypeFun, TypeFun);
ast_node!(TypeApp, TypeApp);
ast_node!(TypeVar, TypeVar);
ast_node!(TypeCon, TypeCon);
ast_node!(TypeQual, TypeQual);
ast_node!(TypeRecord, TypeRecord);
ast_node!(TypeRecordField, TypeRecordField);
ast_node!(TypeTuple, TypeTuple);
ast_node!(TypeUnit, TypeUnit);
ast_node!(TypeParen, TypeParen);
ast_node!(RowVar, RowVar);

ast_node!(Literal, Literal);
ast_node!(MultilineLiteral, MultilineLiteral);
ast_node!(Interpolation, Interpolation);
ast_node!(RefExpr, RefExpr);
ast_node!(QualRefExpr, QualRefExpr);
ast_node!(AccessorExpr, AccessorExpr);
ast_node!(FieldAccess, FieldAccess);
ast_node!(ListExpr, ListExpr);
ast_node!(TupleExpr, TupleExpr);
ast_node!(UnitExpr, UnitExpr);
ast_node!(RecordExpr, RecordExpr);
ast_node!(RecordUpdate, RecordUpdate);
ast_node!(RecordField, RecordField);
ast_node!(ParenExpr, ParenExpr);
ast_node!(NegateExpr, NegateExpr);
ast_node!(BinExpr, BinExpr);
ast_node!(CallExpr, CallExpr);
ast_node!(LambdaExpr, LambdaExpr);
ast_node!(IfExpr, IfExpr);
ast_node!(LetExpr, LetExpr);
ast_node!(LetBinding, LetBinding);
ast_node!(DestructureBinding, DestructureBinding);
ast_node!(CaseExpr, CaseExpr);
ast_node!(MatchArm, MatchArm);

ast_node!(PatWildcard, PatWildcard);
ast_node!(PatVar, PatVar);
ast_node!(PatCtor, PatCtor);
ast_node!(PatCtorQual, PatCtorQual);
ast_node!(PatList, PatList);
ast_node!(PatCons, PatCons);
ast_node!(PatTuple, PatTuple);
ast_node!(PatUnit, PatUnit);
ast_node!(PatRecord, PatRecord);
ast_node!(PatAlias, PatAlias);
ast_node!(PatInt, PatInt);
ast_node!(PatFloat, PatFloat);
ast_node!(PatString, PatString);
ast_node!(PatChar, PatChar);
ast_node!(PatBool, PatBool);
ast_node!(PatParen, PatParen);
ast_node!(PatNegate, PatNegate);

// ---- enum views ----------------------------------------------------------

/// A top-level declaration.
#[derive(Clone)]
pub enum Decl {
    Value(ValueDecl),
    TypeAnno(TypeAnnoDecl),
    Union(UnionDecl),
    Alias(AliasDecl),
    Foreign(ForeignDecl),
}

impl AstNode for Decl {
    fn can_cast(kind: SyntaxKind) -> bool {
        use SyntaxKind::*;
        matches!(
            kind,
            ValueDecl | TypeAnnoDecl | UnionDecl | AliasDecl | ForeignDecl
        )
    }
    fn cast(node: SyntaxNode) -> Option<Self> {
        Some(match node.kind() {
            SyntaxKind::ValueDecl => Decl::Value(ValueDecl(node)),
            SyntaxKind::TypeAnnoDecl => Decl::TypeAnno(TypeAnnoDecl(node)),
            SyntaxKind::UnionDecl => Decl::Union(UnionDecl(node)),
            SyntaxKind::AliasDecl => Decl::Alias(AliasDecl(node)),
            SyntaxKind::ForeignDecl => Decl::Foreign(ForeignDecl(node)),
            _ => return None,
        })
    }
    fn syntax(&self) -> &SyntaxNode {
        match self {
            Decl::Value(n) => n.syntax(),
            Decl::TypeAnno(n) => n.syntax(),
            Decl::Union(n) => n.syntax(),
            Decl::Alias(n) => n.syntax(),
            Decl::Foreign(n) => n.syntax(),
        }
    }
}

/// An expression (exhaustive over the CST expression node kinds).
#[derive(Clone)]
pub enum Expr {
    Literal(Literal),
    Multiline(MultilineLiteral),
    Ref(RefExpr),
    QualRef(QualRefExpr),
    Accessor(AccessorExpr),
    FieldAccess(FieldAccess),
    List(ListExpr),
    Tuple(TupleExpr),
    Unit(UnitExpr),
    Record(RecordExpr),
    RecordUpdate(RecordUpdate),
    Paren(ParenExpr),
    Negate(NegateExpr),
    Bin(BinExpr),
    Call(CallExpr),
    Lambda(LambdaExpr),
    If(IfExpr),
    Let(LetExpr),
    Case(CaseExpr),
}

impl AstNode for Expr {
    fn can_cast(kind: SyntaxKind) -> bool {
        use SyntaxKind::*;
        matches!(
            kind,
            Literal
                | MultilineLiteral
                | RefExpr
                | QualRefExpr
                | AccessorExpr
                | FieldAccess
                | ListExpr
                | TupleExpr
                | UnitExpr
                | RecordExpr
                | RecordUpdate
                | ParenExpr
                | NegateExpr
                | BinExpr
                | CallExpr
                | LambdaExpr
                | IfExpr
                | LetExpr
                | CaseExpr
        )
    }
    fn cast(node: SyntaxNode) -> Option<Self> {
        Some(match node.kind() {
            SyntaxKind::Literal => Expr::Literal(Literal(node)),
            SyntaxKind::MultilineLiteral => Expr::Multiline(MultilineLiteral(node)),
            SyntaxKind::RefExpr => Expr::Ref(RefExpr(node)),
            SyntaxKind::QualRefExpr => Expr::QualRef(QualRefExpr(node)),
            SyntaxKind::AccessorExpr => Expr::Accessor(AccessorExpr(node)),
            SyntaxKind::FieldAccess => Expr::FieldAccess(FieldAccess(node)),
            SyntaxKind::ListExpr => Expr::List(ListExpr(node)),
            SyntaxKind::TupleExpr => Expr::Tuple(TupleExpr(node)),
            SyntaxKind::UnitExpr => Expr::Unit(UnitExpr(node)),
            SyntaxKind::RecordExpr => Expr::Record(RecordExpr(node)),
            SyntaxKind::RecordUpdate => Expr::RecordUpdate(RecordUpdate(node)),
            SyntaxKind::ParenExpr => Expr::Paren(ParenExpr(node)),
            SyntaxKind::NegateExpr => Expr::Negate(NegateExpr(node)),
            SyntaxKind::BinExpr => Expr::Bin(BinExpr(node)),
            SyntaxKind::CallExpr => Expr::Call(CallExpr(node)),
            SyntaxKind::LambdaExpr => Expr::Lambda(LambdaExpr(node)),
            SyntaxKind::IfExpr => Expr::If(IfExpr(node)),
            SyntaxKind::LetExpr => Expr::Let(LetExpr(node)),
            SyntaxKind::CaseExpr => Expr::Case(CaseExpr(node)),
            _ => return None,
        })
    }
    fn syntax(&self) -> &SyntaxNode {
        match self {
            Expr::Literal(n) => n.syntax(),
            Expr::Multiline(n) => n.syntax(),
            Expr::Ref(n) => n.syntax(),
            Expr::QualRef(n) => n.syntax(),
            Expr::Accessor(n) => n.syntax(),
            Expr::FieldAccess(n) => n.syntax(),
            Expr::List(n) => n.syntax(),
            Expr::Tuple(n) => n.syntax(),
            Expr::Unit(n) => n.syntax(),
            Expr::Record(n) => n.syntax(),
            Expr::RecordUpdate(n) => n.syntax(),
            Expr::Paren(n) => n.syntax(),
            Expr::Negate(n) => n.syntax(),
            Expr::Bin(n) => n.syntax(),
            Expr::Call(n) => n.syntax(),
            Expr::Lambda(n) => n.syntax(),
            Expr::If(n) => n.syntax(),
            Expr::Let(n) => n.syntax(),
            Expr::Case(n) => n.syntax(),
        }
    }
}

/// A type expression.
#[derive(Clone)]
pub enum Type {
    Fun(TypeFun),
    App(TypeApp),
    Var(TypeVar),
    Con(TypeCon),
    Qual(TypeQual),
    Record(TypeRecord),
    Tuple(TypeTuple),
    Unit(TypeUnit),
    Paren(TypeParen),
}

impl AstNode for Type {
    fn can_cast(kind: SyntaxKind) -> bool {
        use SyntaxKind::*;
        matches!(
            kind,
            TypeFun | TypeApp | TypeVar | TypeCon | TypeQual | TypeRecord | TypeTuple | TypeUnit
                | TypeParen
        )
    }
    fn cast(node: SyntaxNode) -> Option<Self> {
        Some(match node.kind() {
            SyntaxKind::TypeFun => Type::Fun(TypeFun(node)),
            SyntaxKind::TypeApp => Type::App(TypeApp(node)),
            SyntaxKind::TypeVar => Type::Var(TypeVar(node)),
            SyntaxKind::TypeCon => Type::Con(TypeCon(node)),
            SyntaxKind::TypeQual => Type::Qual(TypeQual(node)),
            SyntaxKind::TypeRecord => Type::Record(TypeRecord(node)),
            SyntaxKind::TypeTuple => Type::Tuple(TypeTuple(node)),
            SyntaxKind::TypeUnit => Type::Unit(TypeUnit(node)),
            SyntaxKind::TypeParen => Type::Paren(TypeParen(node)),
            _ => return None,
        })
    }
    fn syntax(&self) -> &SyntaxNode {
        match self {
            Type::Fun(n) => n.syntax(),
            Type::App(n) => n.syntax(),
            Type::Var(n) => n.syntax(),
            Type::Con(n) => n.syntax(),
            Type::Qual(n) => n.syntax(),
            Type::Record(n) => n.syntax(),
            Type::Tuple(n) => n.syntax(),
            Type::Unit(n) => n.syntax(),
            Type::Paren(n) => n.syntax(),
        }
    }
}

/// A pattern.
#[derive(Clone)]
pub enum Pattern {
    Wildcard(PatWildcard),
    Var(PatVar),
    Ctor(PatCtor),
    CtorQual(PatCtorQual),
    List(PatList),
    Cons(PatCons),
    Tuple(PatTuple),
    Unit(PatUnit),
    Record(PatRecord),
    Alias(PatAlias),
    Int(PatInt),
    Float(PatFloat),
    Str(PatString),
    Char(PatChar),
    Bool(PatBool),
    Paren(PatParen),
    Negate(PatNegate),
}

impl AstNode for Pattern {
    fn can_cast(kind: SyntaxKind) -> bool {
        use SyntaxKind::*;
        matches!(
            kind,
            PatWildcard
                | PatVar
                | PatCtor
                | PatCtorQual
                | PatList
                | PatCons
                | PatTuple
                | PatUnit
                | PatRecord
                | PatAlias
                | PatInt
                | PatFloat
                | PatString
                | PatChar
                | PatBool
                | PatParen
                | PatNegate
        )
    }
    fn cast(node: SyntaxNode) -> Option<Self> {
        Some(match node.kind() {
            SyntaxKind::PatWildcard => Pattern::Wildcard(PatWildcard(node)),
            SyntaxKind::PatVar => Pattern::Var(PatVar(node)),
            SyntaxKind::PatCtor => Pattern::Ctor(PatCtor(node)),
            SyntaxKind::PatCtorQual => Pattern::CtorQual(PatCtorQual(node)),
            SyntaxKind::PatList => Pattern::List(PatList(node)),
            SyntaxKind::PatCons => Pattern::Cons(PatCons(node)),
            SyntaxKind::PatTuple => Pattern::Tuple(PatTuple(node)),
            SyntaxKind::PatUnit => Pattern::Unit(PatUnit(node)),
            SyntaxKind::PatRecord => Pattern::Record(PatRecord(node)),
            SyntaxKind::PatAlias => Pattern::Alias(PatAlias(node)),
            SyntaxKind::PatInt => Pattern::Int(PatInt(node)),
            SyntaxKind::PatFloat => Pattern::Float(PatFloat(node)),
            SyntaxKind::PatString => Pattern::Str(PatString(node)),
            SyntaxKind::PatChar => Pattern::Char(PatChar(node)),
            SyntaxKind::PatBool => Pattern::Bool(PatBool(node)),
            SyntaxKind::PatParen => Pattern::Paren(PatParen(node)),
            SyntaxKind::PatNegate => Pattern::Negate(PatNegate(node)),
            _ => return None,
        })
    }
    fn syntax(&self) -> &SyntaxNode {
        match self {
            Pattern::Wildcard(n) => n.syntax(),
            Pattern::Var(n) => n.syntax(),
            Pattern::Ctor(n) => n.syntax(),
            Pattern::CtorQual(n) => n.syntax(),
            Pattern::List(n) => n.syntax(),
            Pattern::Cons(n) => n.syntax(),
            Pattern::Tuple(n) => n.syntax(),
            Pattern::Unit(n) => n.syntax(),
            Pattern::Record(n) => n.syntax(),
            Pattern::Alias(n) => n.syntax(),
            Pattern::Int(n) => n.syntax(),
            Pattern::Float(n) => n.syntax(),
            Pattern::Str(n) => n.syntax(),
            Pattern::Char(n) => n.syntax(),
            Pattern::Bool(n) => n.syntax(),
            Pattern::Paren(n) => n.syntax(),
            Pattern::Negate(n) => n.syntax(),
        }
    }
}

// ---- accessors -----------------------------------------------------------

impl SourceFile {
    pub fn module_header(&self) -> Option<ModuleHeader> {
        support::child(&self.0)
    }
    pub fn imports(&self) -> impl Iterator<Item = Import> {
        support::children(&self.0)
    }
    pub fn decls(&self) -> impl Iterator<Item = Decl> {
        support::children(&self.0)
    }
}

impl ModuleHeader {
    pub fn name(&self) -> Option<ModuleName> {
        support::child(&self.0)
    }
    pub fn exposing(&self) -> Option<ExposingList> {
        support::child(&self.0)
    }
}

impl ModuleName {
    /// The full dotted module name text (`Sky.Core.List`) — significant tokens
    /// only (leading/trailing trivia is ignored).
    pub fn text(&self) -> String {
        self.0
            .children_with_tokens()
            .filter_map(|e| e.into_token())
            .filter(|t| !t.kind().is_trivia())
            .map(|t| t.text().to_string())
            .collect()
    }
}

impl Import {
    pub fn name(&self) -> Option<ModuleName> {
        support::child(&self.0)
    }
    pub fn alias(&self) -> Option<SyntaxToken> {
        support::child::<ImportAlias>(&self.0)
            .and_then(|a| support::token(a.syntax(), UpperIdent))
    }
    pub fn exposing(&self) -> Option<ImportExposing> {
        support::child(&self.0)
    }
}

impl ValueDecl {
    pub fn name(&self) -> Option<SyntaxToken> {
        support::token(&self.0, LowerIdent).or_else(|| support::token(&self.0, UpperIdent))
    }
    pub fn params(&self) -> Option<ParamList> {
        support::child(&self.0)
    }
    pub fn body(&self) -> Option<Expr> {
        support::child(&self.0)
    }
}

impl TypeAnnoDecl {
    pub fn name(&self) -> Option<SyntaxToken> {
        support::token(&self.0, LowerIdent).or_else(|| support::token(&self.0, UpperIdent))
    }
    pub fn ty(&self) -> Option<Type> {
        support::child(&self.0)
    }
}

impl AliasDecl {
    pub fn name(&self) -> Option<SyntaxToken> {
        support::token(&self.0, UpperIdent)
    }
    pub fn ty(&self) -> Option<Type> {
        support::child(&self.0)
    }
}

impl UnionDecl {
    pub fn name(&self) -> Option<SyntaxToken> {
        support::token(&self.0, UpperIdent)
    }
    pub fn variants(&self) -> Vec<UnionVariant> {
        support::child::<UnionVariantList>(&self.0)
            .map(|l| support::children::<UnionVariant>(l.syntax()).collect())
            .unwrap_or_default()
    }
}

impl UnionVariant {
    pub fn name(&self) -> Option<SyntaxToken> {
        support::token(&self.0, UpperIdent)
    }
}

impl ParamList {
    pub fn params(&self) -> impl Iterator<Item = Pattern> {
        support::children(&self.0)
    }
}

impl LetExpr {
    pub fn bindings(&self) -> impl Iterator<Item = LetBinding> {
        support::children(&self.0)
    }
    pub fn body(&self) -> Option<Expr> {
        support::children::<Expr>(&self.0).last()
    }
}

impl LetBinding {
    pub fn name(&self) -> Option<SyntaxToken> {
        support::token(&self.0, LowerIdent)
    }
    pub fn body(&self) -> Option<Expr> {
        support::child(&self.0)
    }
}

impl CaseExpr {
    pub fn arms(&self) -> impl Iterator<Item = MatchArm> {
        support::children(&self.0)
    }
    pub fn subject(&self) -> Option<Expr> {
        support::child(&self.0)
    }
}

impl MatchArm {
    pub fn pattern(&self) -> Option<Pattern> {
        support::child(&self.0)
    }
    pub fn body(&self) -> Option<Expr> {
        support::child(&self.0)
    }
}

impl IfExpr {
    /// `[condition, then-branch, else-branch]` in order.
    pub fn parts(&self) -> Vec<Expr> {
        support::children::<Expr>(&self.0).collect()
    }
}

impl LambdaExpr {
    pub fn params(&self) -> Option<ParamList> {
        support::child(&self.0)
    }
    pub fn body(&self) -> Option<Expr> {
        support::child(&self.0)
    }
}

impl CallExpr {
    /// Callee then arguments, in order.
    pub fn parts(&self) -> Vec<Expr> {
        support::children::<Expr>(&self.0).collect()
    }
}

impl BinExpr {
    pub fn lhs(&self) -> Option<Expr> {
        support::children::<Expr>(&self.0).next()
    }
    pub fn rhs(&self) -> Option<Expr> {
        support::children::<Expr>(&self.0).nth(1)
    }
    pub fn op(&self) -> Option<SyntaxToken> {
        self.0
            .children_with_tokens()
            .filter_map(|e| e.into_token())
            .find(|t| matches!(t.kind(), Op | Colon2))
    }
}

impl RecordExpr {
    pub fn fields(&self) -> impl Iterator<Item = RecordField> {
        support::children(&self.0)
    }
}

impl RecordUpdate {
    pub fn fields(&self) -> impl Iterator<Item = RecordField> {
        support::children(&self.0)
    }
}

impl RecordField {
    pub fn name(&self) -> Option<SyntaxToken> {
        support::token(&self.0, LowerIdent)
    }
    pub fn value(&self) -> Option<Expr> {
        support::child(&self.0)
    }
}

impl ListExpr {
    pub fn elements(&self) -> impl Iterator<Item = Expr> {
        support::children(&self.0)
    }
}

impl RefExpr {
    pub fn name(&self) -> Option<SyntaxToken> {
        support::token(&self.0, LowerIdent).or_else(|| support::token(&self.0, UpperIdent))
    }
}

/// Classification of an integer literal token — see [`Literal::int_literal`].
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum IntLiteral {
    /// A literal whose value fits in a 64-bit `Int`.
    InRange(i64),
    /// A literal whose magnitude exceeds `i64::MAX` — must be rejected at check
    /// time. Carries the raw digits + the token span for the diagnostic.
    OutOfRange { text: String, range: TextRange },
}

impl Literal {
    fn token(&self) -> Option<SyntaxToken> {
        self.0
            .children_with_tokens()
            .filter_map(|e| e.into_token())
            .find(|t| !t.kind().is_trivia())
    }

    /// Parse an `INT`/`HEX_INT` literal.
    pub fn as_int(&self) -> Option<i64> {
        let t = self.token()?;
        let text = t.text();
        match t.kind() {
            Int => text.parse().ok(),
            HexInt => i64::from_str_radix(text.strip_prefix("0x")?, 16).ok(),
            _ => None,
        }
    }

    /// Classify this literal when it is an integer token (`INT` / `HEX_INT`),
    /// distinguishing an in-range `i64` value from one whose magnitude exceeds
    /// `i64::MAX`. Returns `None` for non-integer literals (float / bool /
    /// string / char).
    ///
    /// Sky's `Int` lowers to Go's 64-bit `int`; a literal that does not fit is
    /// silently truncated by the Haskell oracle and lowers here to a codegen
    /// node that panics at runtime as a classified `TypeMismatch`. Surfacing the
    /// [`IntLiteral::OutOfRange`] arm lets the resolver reject it at CHECK time
    /// (`sky check ≡ sky build` → "if it compiles it works") instead.
    pub fn int_literal(&self) -> Option<IntLiteral> {
        let t = self.token()?;
        let text = t.text();
        let parsed = match t.kind() {
            // `INT` is `[0-9]+` and `HEX_INT` is `0x[0-9a-fA-F]+` (lexer), so the
            // ONLY way either fails to parse is out-of-range — never a malformed
            // digit. That makes `None` here an unambiguous overflow signal.
            Int => text.parse::<i64>().ok(),
            HexInt => i64::from_str_radix(text.strip_prefix("0x")?, 16).ok(),
            _ => return None,
        };
        Some(match parsed {
            Some(v) => IntLiteral::InRange(v),
            None => IntLiteral::OutOfRange {
                text: text.to_string(),
                range: t.text_range(),
            },
        })
    }

    /// Parse a `FLOAT` literal.
    pub fn as_float(&self) -> Option<f64> {
        let t = self.token()?;
        if t.kind() == Float {
            t.text().parse().ok()
        } else {
            None
        }
    }

    pub fn as_bool(&self) -> Option<bool> {
        match self.token()?.kind() {
            TrueKw => Some(true),
            SyntaxKind::FalseKw => Some(false),
            _ => None,
        }
    }

    /// Decode a `STRING`/`CHAR` literal's escapes (doc 03 §1.5).
    pub fn as_string(&self) -> Option<String> {
        let t = self.token()?;
        let raw = t.text();
        match t.kind() {
            SyntaxKind::String => Some(decode_escapes(strip_delims(raw, '"'))),
            Char => Some(decode_escapes(strip_delims(raw, '\''))),
            _ => None,
        }
    }

    /// True when this literal is a single-quoted `CHAR` (distinct from a
    /// double-quoted `String`) — the type checker needs the distinction.
    pub fn is_char(&self) -> bool {
        self.token().map(|t| t.kind() == Char).unwrap_or(false)
    }
}

impl PatString {
    /// The decoded value of a string pattern (`"add"` → `add`) — escapes +
    /// delimiters handled the same way as an expression `String` literal.
    pub fn value(&self) -> String {
        self.0
            .children_with_tokens()
            .filter_map(|e| e.into_token())
            .find(|t| !t.kind().is_trivia())
            .map(|t| decode_escapes(strip_delims(t.text(), '"')))
            .unwrap_or_default()
    }
}

impl PatChar {
    /// The decoded value of a char pattern (`'a'` → `a`).
    pub fn value(&self) -> String {
        self.0
            .children_with_tokens()
            .filter_map(|e| e.into_token())
            .find(|t| !t.kind().is_trivia())
            .map(|t| decode_escapes(strip_delims(t.text(), '\'')))
            .unwrap_or_default()
    }
}

fn strip_delims(s: &str, delim: char) -> &str {
    let s = s.strip_prefix(delim).unwrap_or(s);
    s.strip_suffix(delim).unwrap_or(s)
}

/// Decode Sky string escapes; an unknown `\X` is kept verbatim (doc 03 §1.5).
fn decode_escapes(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut chars = s.chars().peekable();
    while let Some(c) = chars.next() {
        if c != '\\' {
            out.push(c);
            continue;
        }
        match chars.next() {
            Some('n') => out.push('\n'),
            Some('t') => out.push('\t'),
            Some('r') => out.push('\r'),
            Some('\\') => out.push('\\'),
            Some('"') => out.push('"'),
            Some('\'') => out.push('\''),
            Some('0') => out.push('\0'),
            Some('a') => out.push('\u{7}'),
            Some('b') => out.push('\u{8}'),
            Some('f') => out.push('\u{C}'),
            Some('v') => out.push('\u{B}'),
            Some('x') => {
                let hex: String = (0..2).filter_map(|_| chars.next()).collect();
                match u32::from_str_radix(&hex, 16).ok().and_then(char::from_u32) {
                    Some(ch) => out.push(ch),
                    None => {
                        out.push_str("\\x");
                        out.push_str(&hex);
                    }
                }
            }
            Some('u') => {
                if chars.peek() == Some(&'{') {
                    chars.next();
                    let hex: String = chars.by_ref().take_while(|&c| c != '}').collect();
                    match u32::from_str_radix(&hex, 16).ok().and_then(char::from_u32) {
                        Some(ch) => out.push(ch),
                        None => {
                            out.push_str("\\u{");
                            out.push_str(&hex);
                            out.push('}');
                        }
                    }
                } else {
                    let hex: String = (0..4).filter_map(|_| chars.next()).collect();
                    match u32::from_str_radix(&hex, 16).ok().and_then(char::from_u32) {
                        Some(ch) => out.push(ch),
                        None => {
                            out.push_str("\\u");
                            out.push_str(&hex);
                        }
                    }
                }
            }
            Some(other) => {
                out.push('\\');
                out.push(other);
            }
            None => out.push('\\'),
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::parse;
    use base::FileId;

    #[test]
    fn source_file_accessors() {
        let p = parse(
            "module Main exposing (main)\n\nimport Std.Log exposing (println)\n\nmain =\n    println \"hi\"\n",
            FileId(0),
        );
        let tree = p.tree();
        assert_eq!(
            tree.module_header().and_then(|h| h.name()).map(|n| n.text()),
            Some("Main".to_string())
        );
        assert_eq!(tree.imports().count(), 1);
        let decl = tree.decls().next().unwrap();
        match decl {
            Decl::Value(v) => {
                assert_eq!(v.name().unwrap().text(), "main");
                assert!(v.body().is_some());
            }
            _ => panic!("expected a value decl"),
        }
    }

    #[test]
    fn literal_values() {
        let p = parse("x =\n    42\n", FileId(0));
        let v = match p.tree().decls().next().unwrap() {
            Decl::Value(v) => v,
            _ => panic!(),
        };
        match v.body().unwrap() {
            Expr::Literal(l) => assert_eq!(l.as_int(), Some(42)),
            _ => panic!("expected literal"),
        }
    }

    fn body_literal(src: &str) -> Literal {
        let p = parse(src, FileId(0));
        match p.tree().decls().next().unwrap() {
            Decl::Value(v) => match v.body().unwrap() {
                Expr::Literal(l) => l,
                _ => panic!("expected literal body"),
            },
            _ => panic!("expected value decl"),
        }
    }

    #[test]
    fn int_literal_in_range() {
        // i64::MAX must classify as InRange and keep its exact value.
        let l = body_literal("x =\n    9223372036854775807\n");
        assert_eq!(l.int_literal(), Some(IntLiteral::InRange(9223372036854775807)));
        // hex is an integer form too.
        let l = body_literal("x =\n    0xFF\n");
        assert_eq!(l.int_literal(), Some(IntLiteral::InRange(255)));
    }

    #[test]
    fn int_literal_out_of_range() {
        // A 29-digit decimal literal overflows i64 → OutOfRange (rejected at check).
        let l = body_literal("x =\n    12345678901234567890123456789\n");
        match l.int_literal() {
            Some(IntLiteral::OutOfRange { text, .. }) => {
                assert_eq!(text, "12345678901234567890123456789");
            }
            other => panic!("expected OutOfRange, got {other:?}"),
        }
        // One past i64::MAX also overflows.
        let l = body_literal("x =\n    9223372036854775808\n");
        assert!(matches!(l.int_literal(), Some(IntLiteral::OutOfRange { .. })));
        // Oversized hex overflows too.
        let l = body_literal("x =\n    0xFFFFFFFFFFFFFFFFFF\n");
        assert!(matches!(l.int_literal(), Some(IntLiteral::OutOfRange { .. })));
    }

    #[test]
    fn int_literal_none_for_non_int() {
        // Float / string literals are not integer literals.
        assert_eq!(body_literal("x =\n    3.14\n").int_literal(), None);
        assert_eq!(body_literal("x =\n    \"hi\"\n").int_literal(), None);
    }

    #[test]
    fn string_escape_decoding() {
        let p = parse("x =\n    \"a\\nb\"\n", FileId(0));
        let v = match p.tree().decls().next().unwrap() {
            Decl::Value(v) => v,
            _ => panic!(),
        };
        match v.body().unwrap() {
            Expr::Literal(l) => assert_eq!(l.as_string(), Some("a\nb".to_string())),
            _ => panic!(),
        }
    }
}
