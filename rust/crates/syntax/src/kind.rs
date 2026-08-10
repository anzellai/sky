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
    ForeignKw,
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
    ForeignDecl,
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
