//! `SyntaxKind` — the single `u16` kind space for both tokens and nodes
//! (doc 04 §3, §5). L6: one exhaustive enum. The lexer produces the token
//! variants; the parser produces the node variants. rowan stores the raw `u16`.

/// Generate the `SyntaxKind` enum plus a total `from_u16` mapping. The enum is
/// `#[repr(u16)]` with default discriminants (declaration order = 0..N), so the
/// `KINDS` slice indexes back to the variant by discriminant — no `unsafe`
/// transmute (L8 crate keeps `#![forbid(unsafe_code)]`).
macro_rules! syntax_kinds {
    ( $( $variant:ident ),* $(,)? ) => {
        /// Every token + node kind in Sky's CST.
        #[repr(u16)]
        #[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
        #[allow(clippy::upper_case_acronyms)]
        pub enum SyntaxKind {
            $( $variant, )*
        }

        impl SyntaxKind {
            /// Every kind, indexed by discriminant. `pub` because it is the
            /// LANGUAGE DENOMINATOR: `kind_class::KIND_CLASSES` classifies it
            /// and `kind_class` asserts that classification is TOTAL over this
            /// slice, so a kind added here fails the build until it is
            /// classified (docs/ci-test-architecture-v2.md §5.3).
            pub const KINDS: &'static [SyntaxKind] = &[ $( SyntaxKind::$variant ),* ];

            /// Total inverse of the `#[repr(u16)]` discriminant. Out-of-range
            /// values clamp to `Error` (defensive; rowan only ever hands back
            /// values we produced).
            pub fn from_u16(v: u16) -> SyntaxKind {
                *Self::KINDS.get(v as usize).unwrap_or(&SyntaxKind::Error)
            }
        }
    };
}

syntax_kinds! {
    // ---- trivia (indices 0..=3 — see `is_trivia`) ----
    Whitespace,
    Newline,
    LineComment,
    BlockComment,

    // ---- literals ----
    Int,
    HexInt,
    Float,
    String,
    MultilineString,
    Char,

    // ---- idents / keywords ----
    LowerIdent,
    UpperIdent,
    ModuleKw,
    ExposingKw,
    ImportKw,
    AsKw,
    TypeKw,
    AliasKw,
    IfKw,
    ThenKw,
    ElseKw,
    CaseKw,
    OfKw,
    LetKw,
    InKw,
    TrueKw,
    FalseKw,

    // ---- symbols ----
    Eq,
    Colon,
    Colon2,
    Dot,
    DotDot,
    Pipe,
    Arrow,
    LArrow,
    Backslash,
    Op,
    LParen,
    RParen,
    LBrack,
    RBrack,
    LBrace,
    RBrace,
    Comma,
    Underscore,

    // ---- multiline interpolation tokens ----
    StringChunk,
    InterpOpen,
    InterpClose,

    // ---- sentinels ----
    Error,
    Eof,

    // ==== nodes ====
    SourceFile,
    ModuleHeader,
    ModuleName,
    ExposingList,
    ExposedValue,
    ExposedType,
    ExposedCtorList,
    ExposedOperator,
    Import,
    ImportAlias,
    ImportExposing,
    ValueDecl,
    TypeAnnoDecl,
    UnionDecl,
    AliasDecl,
    ParamList,
    TypeVarList,
    UnionVariantList,
    UnionVariant,
    // types
    TypeFun,
    TypeApp,
    TypeVar,
    TypeCon,
    TypeQual,
    TypeRecord,
    TypeRecordField,
    TypeTuple,
    TypeUnit,
    TypeParen,
    RowVar,
    // patterns
    PatWildcard,
    PatVar,
    PatCtor,
    PatCtorQual,
    PatList,
    PatCons,
    PatTuple,
    PatUnit,
    PatRecord,
    PatAlias,
    PatInt,
    PatFloat,
    PatString,
    PatChar,
    PatBool,
    PatParen,
    PatNegate,
    // expressions
    Literal,
    MultilineLiteral,
    Interpolation,
    RefExpr,
    QualRefExpr,
    AccessorExpr,
    FieldAccess,
    ListExpr,
    TupleExpr,
    UnitExpr,
    RecordExpr,
    RecordUpdate,
    RecordField,
    ParenExpr,
    NegateExpr,
    BinExpr,
    CallExpr,
    LambdaExpr,
    IfExpr,
    ElseIf,
    LetExpr,
    LetBinding,
    DestructureBinding,
    BindBinding,
    CaseExpr,
    MatchArm,

    // ---- builder internal (must stay last-ish; never emitted into a real tree) ----
    Tombstone,
}

impl SyntaxKind {
    /// Trivia: whitespace, newlines, both comment forms. Kept in the tree (L8),
    /// skipped by the parser's significant-token cursor.
    #[inline]
    pub fn is_trivia(self) -> bool {
        matches!(
            self,
            SyntaxKind::Whitespace
                | SyntaxKind::Newline
                | SyntaxKind::LineComment
                | SyntaxKind::BlockComment
        )
    }
}

impl From<SyntaxKind> for rowan::SyntaxKind {
    fn from(k: SyntaxKind) -> Self {
        rowan::SyntaxKind(k as u16)
    }
}

impl SyntaxKind {
    /// How a parser message names this kind: a token by its source text in
    /// backticks (`in`, `=`, `)`), a class of tokens or a node by plain words
    /// (a name, an expression). The one mapping every "expected …" message
    /// uses, so no message shows an internal kind name. The match is total:
    /// a new kind fails the build until it is named.
    pub fn describe(self) -> &'static str {
        use SyntaxKind as K;
        match self {
            K::Whitespace => "a space",
            K::Newline => "a new line",
            K::LineComment | K::BlockComment => "a comment",
            K::Int | K::HexInt => "an integer",
            K::Float => "a float",
            K::String | K::MultilineString | K::StringChunk => "a string",
            K::Char => "a character",
            K::LowerIdent => "a name",
            K::UpperIdent => "a capitalised name",
            K::ModuleKw => "`module`",
            K::ExposingKw => "`exposing`",
            K::ImportKw => "`import`",
            K::AsKw => "`as`",
            K::TypeKw => "`type`",
            K::AliasKw => "`alias`",
            K::IfKw => "`if`",
            K::ThenKw => "`then`",
            K::ElseKw => "`else`",
            K::CaseKw => "`case`",
            K::OfKw => "`of`",
            K::LetKw => "`let`",
            K::InKw => "`in`",
            K::TrueKw => "`True`",
            K::FalseKw => "`False`",
            K::Eq => "`=`",
            K::Colon => "`:`",
            K::Colon2 => "`::`",
            K::Dot => "`.`",
            K::DotDot => "`..`",
            K::Pipe => "`|`",
            K::Arrow => "`->`",
            K::LArrow => "`<-`",
            K::Backslash => "`\\`",
            K::Op => "an operator",
            K::LParen => "`(`",
            K::RParen => "`)`",
            K::LBrack => "`[`",
            K::RBrack => "`]`",
            K::LBrace => "`{`",
            K::RBrace => "`}`",
            K::Comma => "`,`",
            K::Underscore => "`_`",
            K::InterpOpen => "`{{`",
            K::InterpClose => "`}}`",
            K::Error => "a token",
            K::Eof => "the end of the file",
            K::SourceFile => "a module",
            K::ModuleHeader => "a module header",
            K::ModuleName => "a module name",
            K::ExposingList | K::ImportExposing => "an exposing list",
            K::ExposedValue | K::ExposedType | K::ExposedOperator => "an exposed name",
            K::ExposedCtorList => "a constructor list",
            K::Import => "an import",
            K::ImportAlias => "an import alias",
            K::ValueDecl => "a definition",
            K::TypeAnnoDecl => "a type annotation",
            K::UnionDecl => "a type declaration",
            K::AliasDecl => "a type alias",
            K::ParamList => "parameters",
            K::TypeVarList => "type variables",
            K::UnionVariantList | K::UnionVariant => "a constructor",
            K::TypeFun
            | K::TypeApp
            | K::TypeVar
            | K::TypeCon
            | K::TypeQual
            | K::TypeRecord
            | K::TypeTuple
            | K::TypeUnit
            | K::TypeParen
            | K::RowVar => "a type",
            K::TypeRecordField => "a record field type",
            K::PatWildcard
            | K::PatVar
            | K::PatCtor
            | K::PatCtorQual
            | K::PatList
            | K::PatCons
            | K::PatTuple
            | K::PatUnit
            | K::PatRecord
            | K::PatAlias
            | K::PatInt
            | K::PatFloat
            | K::PatString
            | K::PatChar
            | K::PatBool
            | K::PatParen
            | K::PatNegate => "a pattern",
            K::Literal
            | K::MultilineLiteral
            | K::Interpolation
            | K::RefExpr
            | K::QualRefExpr
            | K::AccessorExpr
            | K::FieldAccess
            | K::ListExpr
            | K::TupleExpr
            | K::UnitExpr
            | K::RecordExpr
            | K::RecordUpdate
            | K::ParenExpr
            | K::NegateExpr
            | K::BinExpr
            | K::CallExpr
            | K::LambdaExpr
            | K::IfExpr
            | K::LetExpr
            | K::CaseExpr => "an expression",
            K::RecordField => "a record field",
            K::ElseIf => "`else if`",
            K::LetBinding | K::DestructureBinding | K::BindBinding => "a let binding",
            K::MatchArm => "a case branch",
            K::Tombstone => "a token",
        }
    }
}

/// The rowan `Language` binding for Sky's CST (doc 04 §5.1).
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub enum SkyLang {}

impl rowan::Language for SkyLang {
    type Kind = SyntaxKind;

    fn kind_from_raw(raw: rowan::SyntaxKind) -> Self::Kind {
        SyntaxKind::from_u16(raw.0)
    }

    fn kind_to_raw(kind: Self::Kind) -> rowan::SyntaxKind {
        kind.into()
    }
}

/// A Sky syntax node in the lossless tree.
pub type SyntaxNode = rowan::SyntaxNode<SkyLang>;
/// A Sky token in the lossless tree.
pub type SyntaxToken = rowan::SyntaxToken<SkyLang>;
/// A node-or-token element.
pub type SyntaxElement = rowan::SyntaxElement<SkyLang>;
