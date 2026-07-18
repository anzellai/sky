//! The grammar productions (doc 04 §7, doc 03). Recursive descent for
//! module/decl/type/pattern; a Pratt loop for the operator layer. Every layout
//! decision reads `col`/`newline_before`/`ws_before` vs the current
//! `block_indent` anchor — never a raw column peek scattered through the code
//! (the scar doc 04 §13 names).

use crate::kind::SyntaxKind::{self, *};
use crate::parser::{CompletedMarker, Parser, TokenSet};

// ---- token sets ----------------------------------------------------------

/// Atoms admissible as an *application argument* (doc 03 §5.1). Excludes
/// `lambda`/`if`/`let`/`case`, which need parens as arguments.
const ARG_START: TokenSet = TokenSet::new(&[
    Int,
    HexInt,
    Float,
    String,
    MultilineString,
    Char,
    LowerIdent,
    UpperIdent,
    TrueKw,
    FalseKw,
    LParen,
    LBrack,
    LBrace,
    Dot,
]);

const TYPE_ATOM_START: TokenSet =
    TokenSet::new(&[LowerIdent, UpperIdent, LParen, LBrace]);

const PAT_ATOM_START: TokenSet = TokenSet::new(&[
    Underscore,
    LowerIdent,
    UpperIdent,
    LParen,
    LBrack,
    LBrace,
    Int,
    HexInt,
    Float,
    String,
    MultilineString,
    Char,
    TrueKw,
    FalseKw,
]);

const DECL_START: TokenSet =
    TokenSet::new(&[TypeKw, ForeignKw, LowerIdent, UpperIdent]);

// ---- entry ---------------------------------------------------------------

pub(crate) fn source_file(p: &mut Parser) {
    let m = p.start();
    if p.at(ModuleKw) {
        module_header(p);
    }
    while p.at(ImportKw) {
        import_decl(p);
    }
    // top-level anchor is column 1: a decl body continues while col > 1.
    p.with_indent(1, |p| {
        while !p.at_end() {
            if p.at_any(DECL_START) {
                decl(p);
            } else {
                // recover to the next declaration anchor rather than eating one
                // token at a time (doc 04 §11).
                p.err_recover("expected a declaration", DECL_START);
            }
        }
    });
    m.complete(p, SourceFile);
}

// ---- module header + imports --------------------------------------------

fn module_name(p: &mut Parser) {
    let m = p.start();
    p.expect(UpperIdent);
    while p.at(Dot) && p.nth(1) == UpperIdent {
        p.bump(); // .
        p.bump(); // segment
    }
    m.complete(p, ModuleName);
}

fn module_header(p: &mut Parser) {
    let m = p.start();
    p.bump(); // module
    module_name(p);
    if p.at(ExposingKw) {
        exposing_list(p, ExposingList);
    }
    m.complete(p, ModuleHeader);
}

fn exposing_list(p: &mut Parser, kind: SyntaxKind) {
    let m = p.start();
    p.bump(); // exposing
    // `exposing` and `(` may be separated by a newline.
    p.expect(LParen);
    p.with_indent(0, |p| {
        if p.eat(DotDot) {
            // expose all
        } else {
            loop {
                if p.at(RParen) || p.at_end() {
                    break;
                }
                exposed_item(p);
                if !p.eat(Comma) {
                    break;
                }
            }
        }
        p.expect(RParen);
    });
    m.complete(p, kind);
}

fn exposed_item(p: &mut Parser) {
    match p.current() {
        LowerIdent => {
            let m = p.start();
            p.bump();
            m.complete(p, ExposedValue);
        }
        UpperIdent => {
            let m = p.start();
            p.bump();
            if p.at(LParen) {
                let cl = p.start();
                p.bump(); // (
                if p.eat(DotDot) {
                    // Type(..)
                } else {
                    loop {
                        if p.at(RParen) || p.at_end() {
                            break;
                        }
                        p.expect(UpperIdent);
                        if !p.eat(Comma) {
                            break;
                        }
                    }
                }
                p.expect(RParen);
                cl.complete(p, ExposedCtorList);
            }
            m.complete(p, ExposedType);
        }
        LParen => {
            // an operator, e.g. `(|>)`
            let m = p.start();
            p.bump(); // (
            if !p.at(RParen) {
                p.bump(); // the operator glyph
            }
            p.expect(RParen);
            m.complete(p, ExposedOperator);
        }
        _ => p.err_and_bump("expected an exposed value, type, or operator"),
    }
}

fn import_decl(p: &mut Parser) {
    let m = p.start();
    p.bump(); // import
    module_name(p);
    if p.at(AsKw) {
        let am = p.start();
        p.bump(); // as
        if p.at(UpperIdent) || p.at(Underscore) {
            p.bump();
        } else {
            p.error("expected an alias name after `as`");
        }
        am.complete(p, ImportAlias);
    }
    if p.at(ExposingKw) {
        exposing_list(p, ImportExposing);
    }
    m.complete(p, Import);
}

// ---- declarations --------------------------------------------------------

fn decl(p: &mut Parser) {
    match p.current() {
        TypeKw => type_decl(p),
        ForeignKw => foreign_decl(p),
        LowerIdent | UpperIdent => value_or_anno(p),
        _ => p.err_and_bump("expected a declaration"),
    }
}

fn value_or_anno(p: &mut Parser) {
    let m = p.start();
    p.bump(); // the binding / type name
    if p.at(Colon) {
        // `name : Type` annotation (the `:` may be on a continuation line).
        p.bump();
        p.with_indent(1, |p| {
            ty(p);
        });
        m.complete(p, TypeAnnoDecl);
    } else {
        // `name pat* = expr`. Each param is a constructor-application
        // pattern (`pattern_app`), so an unparenthesized uppercase
        // constructor greedily consumes its argument patterns —
        // `f onChange selected RadioOption value labelEl` binds three
        // params, the third being `RadioOption value labelEl`. This
        // mirrors the Haskell oracle's `functionParams`/`pattern_`
        // (patternCtorArgs is greedy). A lowercase/paren/literal param
        // delegates to `pattern_atom`, so those are unchanged.
        let pl = p.start();
        while p.at_any(PAT_ATOM_START) && !p.at(Eq) {
            pattern_app(p);
        }
        pl.complete(p, ParamList);
        p.expect(Eq);
        let body_col = p.col();
        p.with_indent(body_col, |p| {
            expr(p);
        });
        m.complete(p, ValueDecl);
    }
}

fn type_decl(p: &mut Parser) {
    let m = p.start();
    p.bump(); // type
    if p.eat(AliasKw) {
        p.expect(UpperIdent);
        type_var_list(p);
        p.expect(Eq);
        p.with_indent(1, |p| {
            ty(p);
        });
        m.complete(p, AliasDecl);
    } else {
        p.expect(UpperIdent);
        type_var_list(p);
        p.expect(Eq);
        p.with_indent(1, |p| {
            union_variants(p);
        });
        m.complete(p, UnionDecl);
    }
}

fn type_var_list(p: &mut Parser) {
    let m = p.start();
    while p.at(LowerIdent) {
        p.bump();
    }
    m.complete(p, TypeVarList);
}

fn union_variants(p: &mut Parser) {
    let m = p.start();
    variant(p);
    while p.at(Pipe) && p.at_continuation() {
        p.bump(); // |
        variant(p);
    }
    m.complete(p, UnionVariantList);
}

fn variant(p: &mut Parser) {
    let m = p.start();
    p.expect(UpperIdent);
    // constructor args are *atomic* types (typeAtomForCtor).
    while p.at_any(TYPE_ATOM_START) && p.at_continuation() && !p.at(Pipe) {
        type_atom(p);
    }
    m.complete(p, UnionVariant);
}

fn foreign_decl(p: &mut Parser) {
    let m = p.start();
    p.bump(); // foreign
    // consume the rest of the foreign line + indented continuations.
    p.with_indent(1, |p| {
        while !p.at_end() && p.at_continuation() {
            p.bump();
        }
    });
    m.complete(p, ForeignDecl);
}

// ---- types ---------------------------------------------------------------

pub(crate) fn ty(p: &mut Parser) -> CompletedMarker {
    if p.depth_enter() {
        let cm = p.error_node();
        p.depth_leave();
        return cm;
    }
    let lhs = type_app(p);
    // function arrow (right-assoc); `->` may sit on a continuation line.
    let cm = if p.at(Arrow) && p.at_continuation() {
        let m = lhs.precede(p);
        p.bump(); // ->
        ty(p);
        m.complete(p, TypeFun)
    } else {
        lhs
    };
    p.depth_leave();
    cm
}

fn type_app(p: &mut Parser) -> CompletedMarker {
    let first = type_atom(p);
    if p.at_any(TYPE_ATOM_START) && p.at_continuation() {
        let m = first.precede(p);
        while p.at_any(TYPE_ATOM_START) && p.at_continuation() {
            type_atom(p);
        }
        m.complete(p, TypeApp)
    } else {
        first
    }
}

fn type_atom(p: &mut Parser) -> CompletedMarker {
    match p.current() {
        LowerIdent => {
            let m = p.start();
            p.bump();
            m.complete(p, TypeVar)
        }
        UpperIdent => {
            let m = p.start();
            p.bump();
            let mut qualified = false;
            while p.at(Dot) && (p.nth(1) == UpperIdent || p.nth(1) == LowerIdent) {
                p.bump(); // .
                p.bump(); // segment / final name
                qualified = true;
            }
            m.complete(p, if qualified { TypeQual } else { TypeCon })
        }
        LParen => type_paren(p),
        LBrace => type_record(p),
        _ => {
            let m = p.start();
            p.error("expected a type");
            if !p.at_end() {
                p.bump();
            }
            m.complete(p, Error)
        }
    }
}

fn type_paren(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // (
    if p.at(RParen) {
        p.bump();
        return m.complete(p, TypeUnit);
    }
    let is_tuple = p.with_indent(0, |p| {
        ty(p);
        let mut tuple = false;
        while p.at(Comma) {
            tuple = true;
            p.bump();
            if p.at(RParen) {
                break;
            }
            ty(p);
        }
        p.expect(RParen);
        tuple
    });
    m.complete(p, if is_tuple { TypeTuple } else { TypeParen })
}

fn type_record(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // {
    p.with_indent(0, |p| {
        if p.at(RBrace) {
            p.bump();
            return;
        }
        // row-poly: `{ r | f : T }`
        if p.at(LowerIdent) && p.nth(1) == Pipe {
            let rm = p.start();
            p.bump(); // row var
            rm.complete(p, RowVar);
            p.bump(); // |
        }
        loop {
            if p.at(RBrace) || p.at_end() {
                break;
            }
            let fm = p.start();
            p.expect(LowerIdent);
            p.expect(Colon);
            ty(p);
            fm.complete(p, TypeRecordField);
            if !p.eat(Comma) {
                break;
            }
        }
        p.expect(RBrace);
    });
    m.complete(p, TypeRecord)
}

// ---- patterns ------------------------------------------------------------

pub(crate) fn pattern(p: &mut Parser) -> CompletedMarker {
    if p.depth_enter() {
        let cm = p.error_node();
        p.depth_leave();
        return cm;
    }
    let lhs = pattern_cons(p);
    let cm = if p.at(AsKw) {
        let m = lhs.precede(p);
        p.bump(); // as
        p.expect(LowerIdent);
        m.complete(p, PatAlias)
    } else {
        lhs
    };
    p.depth_leave();
    cm
}

fn pattern_cons(p: &mut Parser) -> CompletedMarker {
    let lhs = pattern_app(p);
    if p.at(Colon2) {
        let m = lhs.precede(p);
        p.bump(); // ::
        pattern_cons(p); // right-assoc
        m.complete(p, PatCons)
    } else {
        lhs
    }
}

fn pattern_app(p: &mut Parser) -> CompletedMarker {
    if p.at(UpperIdent) {
        let m = p.start();
        p.bump(); // ctor head
        let mut qualified = false;
        while p.at(Dot) && (p.nth(1) == UpperIdent || p.nth(1) == LowerIdent) {
            p.bump(); // .
            p.bump(); // segment / ctor
            qualified = true;
        }
        // atomic pattern arguments
        while p.at_any(PAT_ATOM_START) {
            pattern_atom(p);
        }
        m.complete(p, if qualified { PatCtorQual } else { PatCtor })
    } else {
        pattern_atom(p)
    }
}

fn pattern_atom(p: &mut Parser) -> CompletedMarker {
    // negative-literal pattern: `-` abutting a digit
    if p.at(Op) && p.cur_text() == "-" && !p.nth_ws_before(1) && is_num(p.nth(1)) {
        let m = p.start();
        p.bump(); // -
        p.bump(); // number
        return m.complete(p, PatNegate);
    }
    match p.current() {
        Underscore => {
            let m = p.start();
            p.bump();
            m.complete(p, PatWildcard)
        }
        LowerIdent => {
            let m = p.start();
            p.bump();
            m.complete(p, PatVar)
        }
        UpperIdent => {
            // nullary constructor (args are handled by pattern_app)
            let m = p.start();
            p.bump();
            let mut qualified = false;
            while p.at(Dot) && (p.nth(1) == UpperIdent || p.nth(1) == LowerIdent) {
                p.bump();
                p.bump();
                qualified = true;
            }
            m.complete(p, if qualified { PatCtorQual } else { PatCtor })
        }
        Int | HexInt => {
            let m = p.start();
            p.bump();
            m.complete(p, PatInt)
        }
        Float => {
            let m = p.start();
            p.bump();
            m.complete(p, PatFloat)
        }
        String | MultilineString => {
            let m = p.start();
            p.bump();
            m.complete(p, PatString)
        }
        Char => {
            let m = p.start();
            p.bump();
            m.complete(p, PatChar)
        }
        TrueKw | FalseKw => {
            let m = p.start();
            p.bump();
            m.complete(p, PatBool)
        }
        LParen => pattern_paren(p),
        LBrack => pattern_list(p),
        LBrace => pattern_record(p),
        _ => {
            let m = p.start();
            p.error("expected a pattern");
            if !p.at_end() {
                p.bump();
            }
            m.complete(p, Error)
        }
    }
}

fn pattern_paren(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // (
    if p.at(RParen) {
        p.bump();
        return m.complete(p, PatUnit);
    }
    let is_tuple = p.with_indent(0, |p| {
        pattern(p);
        let mut tuple = false;
        while p.at(Comma) {
            tuple = true;
            p.bump();
            if p.at(RParen) {
                break;
            }
            pattern(p);
        }
        p.expect(RParen);
        tuple
    });
    m.complete(p, if is_tuple { PatTuple } else { PatParen })
}

fn pattern_list(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // [
    p.with_indent(0, |p| {
        if !p.at(RBrack) {
            pattern(p);
            while p.eat(Comma) {
                if p.at(RBrack) {
                    break;
                }
                pattern(p);
            }
        }
        p.expect(RBrack);
    });
    m.complete(p, PatList)
}

fn pattern_record(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // {
    p.with_indent(0, |p| {
        loop {
            if p.at(RBrace) || p.at_end() {
                break;
            }
            p.expect(LowerIdent);
            if !p.eat(Comma) {
                break;
            }
        }
        p.expect(RBrace);
    });
    m.complete(p, PatRecord)
}

// ---- expressions: Pratt layer --------------------------------------------

pub(crate) fn expr(p: &mut Parser) -> CompletedMarker {
    expr_bp(p, 0)
}

fn expr_bp(p: &mut Parser, min_bp: u8) -> CompletedMarker {
    if p.depth_enter() {
        let cm = p.error_node();
        p.depth_leave();
        return cm;
    }
    let mut lhs = app_expr(p);
    loop {
        if !operator_continues(p) {
            break;
        }
        let (l_bp, r_bp) = bin_power(p);
        if l_bp < min_bp {
            break;
        }
        let m = lhs.precede(p);
        // consume the operator (Op glyph or `::`)
        p.bump();
        expr_bp(p, r_bp);
        lhs = m.complete(p, BinExpr);
    }
    p.depth_leave();
    lhs
}

/// The current token is a binary operator that continues the current
/// expression (same line, or a fresh line indented past the block anchor —
/// the `|>` pipeline shape).
fn operator_continues(p: &Parser) -> bool {
    if !(p.at(Op) || p.at(Colon2)) {
        return false;
    }
    !p.newline_before() || p.col() > p.block_indent
}

fn bin_power(p: &Parser) -> (u8, u8) {
    const fn left(prec: u8) -> (u8, u8) {
        (prec * 2, prec * 2 + 1)
    }
    const fn right(prec: u8) -> (u8, u8) {
        (prec * 2 + 1, prec * 2)
    }
    if p.at(Colon2) {
        return right(5);
    }
    match p.cur_text() {
        ">>" => left(9),
        "<<" => right(9),
        "^" => right(8),
        "*" | "/" | "//" | "%" => left(7),
        "+" | "-" => left(6),
        "++" => right(5),
        "==" | "/=" | "<" | ">" | "<=" | ">=" => left(4), // non-assoc → climb left
        "&&" => right(3),
        "||" => right(2),
        "|>" => left(0),
        "<|" => right(0),
        _ => left(9), // unknown operator (dead surface — no custom ops)
    }
}

fn app_expr(p: &mut Parser) -> CompletedMarker {
    let first = primary_expr(p);
    if !arg_continues(p) {
        return first;
    }
    let m = first.precede(p);
    while arg_continues(p) {
        if is_negative_literal_arg(p) {
            let nm = p.start();
            p.bump(); // -
            let lm = p.start();
            p.bump(); // number
            lm.complete(p, Literal);
            nm.complete(p, NegateExpr);
        } else {
            primary_expr(p);
        }
    }
    m.complete(p, CallExpr)
}

fn arg_continues(p: &Parser) -> bool {
    if p.at_end() {
        return false;
    }
    if is_negative_literal_arg(p) {
        return true;
    }
    if !p.at_any(ARG_START) {
        return false;
    }
    !p.newline_before() || p.col() > p.block_indent
}

fn is_negative_literal_arg(p: &Parser) -> bool {
    if !(p.at(Op) && p.cur_text() == "-" && p.ws_before()) {
        return false;
    }
    if p.nth_ws_before(1) || !is_num(p.nth(1)) {
        return false;
    }
    !p.newline_before() || p.col() > p.block_indent
}

fn is_num(k: SyntaxKind) -> bool {
    matches!(k, Int | HexInt | Float)
}

fn primary_expr(p: &mut Parser) -> CompletedMarker {
    let cm = atom(p);
    postfix(p, cm)
}

/// Field-access chains: `record.field.sub`.
fn postfix(p: &mut Parser, mut cm: CompletedMarker) -> CompletedMarker {
    while p.at(Dot) && !p.ws_before() && p.nth(1) == LowerIdent {
        let m = cm.precede(p);
        p.bump(); // .
        p.bump(); // field
        cm = m.complete(p, FieldAccess);
    }
    cm
}

fn atom(p: &mut Parser) -> CompletedMarker {
    match p.current() {
        Int | HexInt | Float | String | Char => {
            let m = p.start();
            p.bump();
            m.complete(p, Literal)
        }
        MultilineString => multiline_literal(p),
        TrueKw | FalseKw => {
            let m = p.start();
            p.bump();
            m.complete(p, Literal)
        }
        LowerIdent => {
            let m = p.start();
            p.bump();
            m.complete(p, RefExpr)
        }
        UpperIdent => {
            let m = p.start();
            p.bump();
            let mut qualified = false;
            while p.at(Dot) && !p.ws_before() && (p.nth(1) == LowerIdent || p.nth(1) == UpperIdent)
            {
                p.bump(); // .
                p.bump(); // name
                qualified = true;
            }
            m.complete(p, if qualified { QualRefExpr } else { RefExpr })
        }
        Dot => {
            // accessor function `.field`
            let m = p.start();
            p.bump(); // .
            p.expect(LowerIdent);
            m.complete(p, AccessorExpr)
        }
        Op if p.cur_text() == "-" => {
            // unary negation at expression start: `-1`, `(-x)`, `-record.field`
            // (doc 03 §1.4/§5.1 — `Src.Negate`). Binary `-` is handled by Pratt
            // once there is an lhs.
            let m = p.start();
            p.bump(); // -
            primary_expr(p);
            m.complete(p, NegateExpr)
        }
        LParen => paren_expr(p),
        LBrack => list_expr(p),
        LBrace => record_expr(p),
        Backslash => lambda_expr(p),
        IfKw => if_expr(p),
        LetKw => let_expr(p),
        CaseKw => case_expr(p),
        _ => {
            let m = p.start();
            p.error("expected an expression");
            if !p.at_end() {
                p.bump();
            }
            m.complete(p, Error)
        }
    }
}

fn paren_expr(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // (
    if p.at(RParen) {
        p.bump();
        return m.complete(p, UnitExpr);
    }
    let is_tuple = p.with_indent(0, |p| {
        expr(p);
        let mut tuple = false;
        while p.at(Comma) {
            tuple = true;
            p.bump();
            if p.at(RParen) {
                break;
            }
            expr(p);
        }
        p.expect(RParen);
        tuple
    });
    m.complete(p, if is_tuple { TupleExpr } else { ParenExpr })
}

fn list_expr(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // [
    p.with_indent(0, |p| {
        if !p.at(RBrack) {
            expr(p);
            while p.eat(Comma) {
                if p.at(RBrack) {
                    break;
                }
                expr(p);
            }
        }
        p.expect(RBrack);
    });
    m.complete(p, ListExpr)
}

fn record_expr(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // {
    let kind = p.with_indent(0, |p| {
        if p.at(RBrace) {
            p.bump();
            return RecordExpr;
        }
        let update = p.at(LowerIdent) && p.nth(1) == Pipe;
        if update {
            p.bump(); // record name
            p.bump(); // |
        }
        loop {
            if p.at(RBrace) || p.at_end() {
                break;
            }
            let fm = p.start();
            p.expect(LowerIdent);
            p.expect(Eq);
            expr(p);
            fm.complete(p, RecordField);
            if !p.eat(Comma) {
                break;
            }
        }
        p.expect(RBrace);
        if update {
            RecordUpdate
        } else {
            RecordExpr
        }
    });
    m.complete(p, kind)
}

fn lambda_expr(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // backslash
    let pl = p.start();
    while p.at_any(PAT_ATOM_START) && !p.at(Arrow) {
        // `pattern_app` so an uppercase constructor param consumes its
        // args (`\Just x -> …`), matching the oracle's `lambdaParams`.
        pattern_app(p);
    }
    pl.complete(p, ParamList);
    p.expect(Arrow);
    expr(p);
    m.complete(p, LambdaExpr)
}

fn if_expr(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // if
    expr(p); // condition (stops at `then`)
    p.expect(ThenKw);
    expr(p); // then-branch (stops at `else`)
    p.expect(ElseKw);
    expr(p); // else-branch (a nested `if` folds an else-if chain)
    m.complete(p, IfExpr)
}

fn let_expr(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // let
    let anchor = p.col(); // first binding's column
    loop {
        if p.at(InKw) || p.at_end() {
            break;
        }
        let_binding(p, anchor);
        if p.at(InKw) {
            break;
        }
        // next sibling binding must align exactly at the anchor column.
        if p.newline_before() && p.col() == anchor {
            continue;
        }
        break;
    }
    p.expect(InKw);
    expr(p); // the `in` body
    m.complete(p, LetExpr)
}

fn let_binding(p: &mut Parser, anchor: u32) {
    if p.at(LowerIdent) && p.nth(1) == Colon {
        // `name : Type` annotation binding
        let m = p.start();
        p.bump(); // name
        p.bump(); // :
        p.with_indent(anchor, |p| {
            ty(p);
        });
        m.complete(p, LetBinding);
    } else if p.at(LowerIdent) {
        // `name pat* = expr`
        let m = p.start();
        p.bump(); // name
        let pl = p.start();
        while p.at_any(PAT_ATOM_START) && !p.at(Eq) {
            // `pattern_app` so a constructor param consumes its args,
            // matching the oracle's greedy `pattern_` in let bindings.
            pattern_app(p);
        }
        pl.complete(p, ParamList);
        p.expect(Eq);
        p.with_indent(anchor, |p| {
            expr(p);
        });
        m.complete(p, LetBinding);
    } else {
        // destructure: `(a, b) = e`, `{ x } = r`, `Just x = m`, `_ = e`
        let m = p.start();
        pattern(p);
        p.expect(Eq);
        p.with_indent(anchor, |p| {
            expr(p);
        });
        m.complete(p, DestructureBinding);
    }
}

fn case_expr(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // case
    expr(p); // subject (stops at `of`)
    p.expect(OfKw);
    let anchor = p.col(); // first arm's column
    loop {
        if p.at_end() {
            break;
        }
        // an arm must align exactly at the branch anchor.
        if p.newline_before() && p.col() != anchor {
            break;
        }
        match_arm(p, anchor);
        if !(p.newline_before() && p.col() == anchor) {
            break;
        }
    }
    m.complete(p, CaseExpr)
}

fn match_arm(p: &mut Parser, pat_col: u32) {
    let m = p.start();
    pattern(p);
    p.expect(Arrow);
    // arm body binds `withIndent (max patCol bodyCol)` so the next sibling arm
    // is not slurped into this body (doc 03 §5.5).
    let body_col = p.col();
    let ind = pat_col.max(body_col);
    p.with_indent(ind, |p| {
        expr(p);
    });
    m.complete(p, MatchArm);
}

// ---- multiline string + interpolation (doc 04 §9) -----------------------

fn multiline_literal(p: &mut Parser) -> CompletedMarker {
    // Baseline: the `"""…"""` token is kept opaque inside a `MULTILINE_LITERAL`
    // node (byte-exact, zero error nodes). The interpolation interior split
    // (doc 04 §9) is layered on in `crate::interp` while preserving this
    // round-trip.
    let m = p.start();
    crate::interp::multiline(p);
    m.complete(p, MultilineLiteral)
}
