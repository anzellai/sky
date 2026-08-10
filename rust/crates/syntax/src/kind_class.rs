//! The LANGUAGE DENOMINATOR — every `SyntaxKind` classified as a user-facing
//! language **construct** (which must carry corpus cases) or a
//! **non-construct** (with a stated reason).
//!
//! # Why this table exists
//!
//! "100 % of the language is covered" is only meaningful against a denominator
//! that cannot silently shrink. The denominator is
//! [`SyntaxKind::KINDS`] — macro-generated in `kind.rs`, therefore total by
//! construction — **not** a hand-counted list in a design document (the
//! topology doc's hand-count said 72; `KINDS` says 124). A hand-count drifts the
//! moment a kind is added; `KINDS` cannot.
//!
//! Raw `KINDS` is not the right numerator base either: `Whitespace`, `Comma` and
//! `Tombstone` are not language constructs a test corpus should be expected to
//! "cover". So every kind is classified here, once, in a committed table, and
//! [`assert_total`] proves the classification is TOTAL over `KINDS` — **a newly
//! added kind fails the build until someone classifies it**. That is the whole
//! point: the denominator can grow (fine, it must be re-covered) but it cannot
//! silently shrink, and a new construct cannot enter the language unnoticed.
//!
//! # The classification rule
//!
//! * **`Construct`** — every NODE kind (each is a distinct grammar production, so
//!   a corpus case either exercises it or does not), plus the LITERAL token kinds
//!   (`Int`/`HexInt`/`Float`/`String`/`MultilineString`/`Char` each have their own
//!   lexing rules and their own failure modes — escape handling, hex parsing,
//!   float parsing — which no node kind distinguishes), plus `Op` (the operator
//!   set is user-facing and `BinExpr` alone does not distinguish `+` from `|>`).
//! * **`NonConstruct(reason)`** — trivia, identifier tokens, keyword tokens,
//!   punctuation/delimiter tokens, the multiline-interpolation sub-tokens, and the
//!   internal sentinels. Each of these is either *carried* by a construct that is
//!   itself classified `Construct`, or is a lexer/builder artefact that never
//!   denotes a user-facing feature. The reason string on each entry says which.
//!
//! # Known live hole this closes
//!
//! The `can_cast` implementations in `ast.rs` (`matches!` lists at the `Decl`,
//! `Type`, `Pattern` and `Expr` sum-type casts) enumerate kinds by hand and are
//! **not compiler-checked**: adding a new `Expr*` node kind and forgetting to add
//! it to `Expr::can_cast` compiles clean and silently makes the new node
//! invisible to every AST consumer. Nothing fails. This table does not fix
//! `can_cast` — it makes the omission *detectable*, because the new kind must be
//! classified here, and classifying it as a `Construct` puts it into the language
//! denominator that coverage is measured against.

use crate::kind::SyntaxKind;
use SyntaxKind::*;

/// How a [`SyntaxKind`] participates in the language coverage denominator.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum KindClass {
    /// A user-facing language construct: it must have corpus cases.
    Construct,
    /// Not a language construct. The payload states WHY — an unexplained
    /// exclusion is how a denominator shrinks quietly.
    NonConstruct(&'static str),
}

impl KindClass {
    /// `true` for [`KindClass::Construct`].
    pub fn is_construct(self) -> bool {
        matches!(self, KindClass::Construct)
    }
}

const TRIVIA: KindClass = KindClass::NonConstruct(
    "lexical trivia — preserved losslessly in the CST and asserted by the \
     byte-exact round-trip gate, but it denotes no language construct",
);
const IDENT: KindClass = KindClass::NonConstruct(
    "identifier token — the payload of whichever construct binds or references \
     it (ValueDecl, RefExpr, PatVar, TypeCon, …), not a construct itself",
);
const KEYWORD: KindClass = KindClass::NonConstruct(
    "keyword token — the marker of the node kind it introduces, which is \
     classified Construct; covering the node covers the keyword",
);
const PUNCT: KindClass = KindClass::NonConstruct(
    "punctuation/delimiter token — structural glue inside a node kind that is \
     itself classified Construct",
);
const INTERP: KindClass = KindClass::NonConstruct(
    "multiline-string interpolation sub-token — an internal lexer product of \
     the MultilineLiteral/Interpolation nodes, which are classified Construct",
);
const SENTINEL: KindClass = KindClass::NonConstruct(
    "internal sentinel — never denotes source the user wrote (Error marks \
     recovery, Eof marks input end, Tombstone is a tree-builder artefact)",
);

/// The committed classification of EVERY [`SyntaxKind`], listed in `KINDS`
/// order. [`assert_total`] proves this is total over
/// [`SyntaxKind::KINDS`].
pub const KIND_CLASSES: &[(SyntaxKind, KindClass)] = &[
    // ---- trivia ----
    (Whitespace, TRIVIA),
    (Newline, TRIVIA),
    (LineComment, TRIVIA),
    (BlockComment, TRIVIA),
    // ---- literals: each has distinct lexing rules + failure modes ----
    (Int, KindClass::Construct),
    (HexInt, KindClass::Construct),
    (Float, KindClass::Construct),
    (String, KindClass::Construct),
    (MultilineString, KindClass::Construct),
    (Char, KindClass::Construct),
    // ---- idents / keywords ----
    (LowerIdent, IDENT),
    (UpperIdent, IDENT),
    (ModuleKw, KEYWORD),
    (ExposingKw, KEYWORD),
    (ImportKw, KEYWORD),
    (AsKw, KEYWORD),
    (TypeKw, KEYWORD),
    (AliasKw, KEYWORD),
    (ForeignKw, KEYWORD),
    (IfKw, KEYWORD),
    (ThenKw, KEYWORD),
    (ElseKw, KEYWORD),
    (CaseKw, KEYWORD),
    (OfKw, KEYWORD),
    (LetKw, KEYWORD),
    (InKw, KEYWORD),
    (TrueKw, KEYWORD),
    (FalseKw, KEYWORD),
    // ---- symbols ----
    (Eq, PUNCT),
    (Colon, PUNCT),
    (Colon2, PUNCT),
    (Dot, PUNCT),
    (DotDot, PUNCT),
    (Pipe, PUNCT),
    (Arrow, PUNCT),
    (Backslash, PUNCT),
    // The operator SET is user-facing and BinExpr does not distinguish `+`
    // from `|>` / `::` / `++` — precedence and associativity are per-operator.
    (Op, KindClass::Construct),
    (LParen, PUNCT),
    (RParen, PUNCT),
    (LBrack, PUNCT),
    (RBrack, PUNCT),
    (LBrace, PUNCT),
    (RBrace, PUNCT),
    (Comma, PUNCT),
    (Underscore, PUNCT),
    // ---- multiline interpolation tokens ----
    (StringChunk, INTERP),
    (InterpOpen, INTERP),
    (InterpClose, INTERP),
    // ---- sentinels ----
    (Error, SENTINEL),
    (Eof, SENTINEL),
    // ==== nodes: one grammar production each ====
    (SourceFile, KindClass::Construct),
    (ModuleHeader, KindClass::Construct),
    (ModuleName, KindClass::Construct),
    (ExposingList, KindClass::Construct),
    (ExposedValue, KindClass::Construct),
    (ExposedType, KindClass::Construct),
    (ExposedCtorList, KindClass::Construct),
    (ExposedOperator, KindClass::Construct),
    (Import, KindClass::Construct),
    (ImportAlias, KindClass::Construct),
    (ImportExposing, KindClass::Construct),
    (ValueDecl, KindClass::Construct),
    (TypeAnnoDecl, KindClass::Construct),
    (UnionDecl, KindClass::Construct),
    (AliasDecl, KindClass::Construct),
    (ForeignDecl, KindClass::Construct),
    (ParamList, KindClass::Construct),
    (TypeVarList, KindClass::Construct),
    (UnionVariantList, KindClass::Construct),
    (UnionVariant, KindClass::Construct),
    // types
    (TypeFun, KindClass::Construct),
    (TypeApp, KindClass::Construct),
    (TypeVar, KindClass::Construct),
    (TypeCon, KindClass::Construct),
    (TypeQual, KindClass::Construct),
    (TypeRecord, KindClass::Construct),
    (TypeRecordField, KindClass::Construct),
    (TypeTuple, KindClass::Construct),
    (TypeUnit, KindClass::Construct),
    (TypeParen, KindClass::Construct),
    (RowVar, KindClass::Construct),
    // patterns
    (PatWildcard, KindClass::Construct),
    (PatVar, KindClass::Construct),
    (PatCtor, KindClass::Construct),
    (PatCtorQual, KindClass::Construct),
    (PatList, KindClass::Construct),
    (PatCons, KindClass::Construct),
    (PatTuple, KindClass::Construct),
    (PatUnit, KindClass::Construct),
    (PatRecord, KindClass::Construct),
    (PatAlias, KindClass::Construct),
    (PatInt, KindClass::Construct),
    (PatFloat, KindClass::Construct),
    (PatString, KindClass::Construct),
    (PatChar, KindClass::Construct),
    (PatBool, KindClass::Construct),
    (PatParen, KindClass::Construct),
    (PatNegate, KindClass::Construct),
    // expressions
    (Literal, KindClass::Construct),
    (MultilineLiteral, KindClass::Construct),
    (Interpolation, KindClass::Construct),
    (RefExpr, KindClass::Construct),
    (QualRefExpr, KindClass::Construct),
    (AccessorExpr, KindClass::Construct),
    (FieldAccess, KindClass::Construct),
    (ListExpr, KindClass::Construct),
    (TupleExpr, KindClass::Construct),
    (UnitExpr, KindClass::Construct),
    (RecordExpr, KindClass::Construct),
    (RecordUpdate, KindClass::Construct),
    (RecordField, KindClass::Construct),
    (ParenExpr, KindClass::Construct),
    (NegateExpr, KindClass::Construct),
    (BinExpr, KindClass::Construct),
    (CallExpr, KindClass::Construct),
    (LambdaExpr, KindClass::Construct),
    (IfExpr, KindClass::Construct),
    (ElseIf, KindClass::Construct),
    (LetExpr, KindClass::Construct),
    (LetBinding, KindClass::Construct),
    (DestructureBinding, KindClass::Construct),
    (CaseExpr, KindClass::Construct),
    (MatchArm, KindClass::Construct),
    // ---- builder internal ----
    (Tombstone, SENTINEL),
];

/// The classification of `k`, or `None` if the table does not cover it (which
/// [`assert_total`] makes impossible).
pub fn classify(k: SyntaxKind) -> Option<KindClass> {
    KIND_CLASSES.iter().find(|(kind, _)| *kind == k).map(|(_, c)| *c)
}

/// Every kind classified as a language construct — the LANGUAGE DENOMINATOR
/// that `xtask denominators` reports and the coverage numerator is measured
/// against.
pub fn construct_kinds() -> Vec<SyntaxKind> {
    KIND_CLASSES
        .iter()
        .filter(|(_, c)| c.is_construct())
        .map(|(k, _)| *k)
        .collect()
}

/// Total number of kinds (the raw denominator, `KINDS.len()`).
pub fn kind_count() -> usize {
    SyntaxKind::KINDS.len()
}

/// Prove [`KIND_CLASSES`] is TOTAL over [`SyntaxKind::KINDS`]: every kind
/// classified, exactly once, with nothing extra.
///
/// Returns `Err(message)` describing the exact failure. This is a plain
/// function rather than only a `#[test]` so `xtask denominators` runs the same
/// check before it dares emit a language denominator — a gate that reports a
/// number from an incomplete table is reporting a shrunk denominator.
pub fn assert_total() -> Result<(), std::string::String> {
    let mut problems: Vec<std::string::String> = Vec::new();

    // (a) every kind is classified.
    for k in SyntaxKind::KINDS {
        let hits = KIND_CLASSES.iter().filter(|(kind, _)| kind == k).count();
        match hits {
            0 => problems.push(format!(
                "UNCLASSIFIED kind `{k:?}` — add it to kind_class::KIND_CLASSES as \
                 Construct or NonConstruct(reason)"
            )),
            1 => {}
            n => problems.push(format!("kind `{k:?}` classified {n} times (duplicate row)")),
        }
    }
    // (b) nothing classified that is not a kind (a stale row after a removal).
    for (k, _) in KIND_CLASSES {
        if !SyntaxKind::KINDS.contains(k) {
            problems.push(format!("stale row `{k:?}` — no such SyntaxKind"));
        }
    }
    // (c) no NonConstruct without a reason.
    for (k, c) in KIND_CLASSES {
        if let KindClass::NonConstruct(reason) = c {
            if reason.trim().is_empty() {
                problems.push(format!("`{k:?}` is NonConstruct with an empty reason"));
            }
        }
    }

    if problems.is_empty() {
        Ok(())
    } else {
        Err(format!(
            "SyntaxKind classification is not total over KINDS ({} kinds, {} rows):\n  - {}",
            SyntaxKind::KINDS.len(),
            KIND_CLASSES.len(),
            problems.join("\n  - ")
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// THE GATE. A kind added to `syntax_kinds!` without a `KIND_CLASSES` row
    /// fails here — the language denominator cannot grow in silence.
    #[test]
    fn classification_is_total_over_kinds() {
        if let Err(msg) = assert_total() {
            panic!("{msg}");
        }
    }

    /// Pins the split so a wholesale reclassification (e.g. quietly demoting
    /// node kinds to non-constructs to make coverage look better) is a visible,
    /// reviewed diff and not an invisible denominator shrink.
    #[test]
    fn construct_split_is_pinned() {
        let constructs = construct_kinds().len();
        let total = kind_count();
        assert_eq!(total, 124, "SyntaxKind count changed — reclassify, then update this pin");
        assert_eq!(
            constructs, 80,
            "construct count changed ({constructs} of {total}) — intentional? update this pin \
             in the same commit that changes KIND_CLASSES"
        );
    }
}
