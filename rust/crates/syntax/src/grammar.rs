//! The grammar productions (doc 04 §7, doc 03). Recursive descent for
//! module/decl/type/pattern; a Pratt loop for the operator layer. Every layout
//! decision reads `col`/`newline_before`/`ws_before` vs the current
//! `block_indent` anchor — never a raw column peek scattered through the code
//! (the scar doc 04 §13 names).

use crate::kind::SyntaxKind::{self, *};
use crate::parser::{is_reserved_word, CompletedMarker, Parser, TokenSet};

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

const TYPE_ATOM_START: TokenSet = TokenSet::new(&[LowerIdent, UpperIdent, LParen, LBrace]);

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

/// Reserved words that can neither start nor end an expression. In a value
/// position one was always meant as a name.
const RESERVED_NOT_EXPR: TokenSet =
    TokenSet::new(&[ModuleKw, ExposingKw, ImportKw, AsKw, TypeKw, AliasKw]);

/// Tokens that can begin an expression (a unary `-` is checked by its text).
const EXPR_START: TokenSet = TokenSet::new(&[
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
    Backslash,
    IfKw,
    LetKw,
    CaseKw,
]);

/// The current reserved word stands in a value position (an atom, an
/// argument, an operand) as a name. Where it does, it is one named error,
/// consumed as a placeholder, so parsing resumes without a cascade. The rule
/// follows the grammar:
///
/// - `module` `exposing` `import` `as` `type` `alias` never start or end an
///   expression, so here they are always a name.
/// - `then` `else` `of` `in` only END an expression, and only one that an open
///   `if` / `case` / `let` in the same bracket scope waits for. When no open
///   construct waits for it, the word ends nothing, so it is a name.
/// - `if` and `case` start an expression only when the next token continues
///   the block and can begin the condition or subject. `let` starts one only
///   when the next token continues the block and can begin a binding.
///   Otherwise (`)`, `,`, `]`, `}`, an operator, `then`, `of`, `in`, the end of
///   input, or a declaration on a fresh line) the word is a name.
fn at_reserved_value(p: &Parser) -> bool {
    let next = p.nth(1);
    match p.current() {
        ModuleKw | ExposingKw | ImportKw | AsKw | TypeKw | AliasKw => true,
        ThenKw | ElseKw | OfKw | InKw => !p.is_awaited(p.current()),
        IfKw | CaseKw => {
            let starts = EXPR_START.contains(next)
                || RESERVED_NOT_EXPR.contains(next)
                || (next == Op && p.nth_text(1) == "-");
            !(p.nth_continues(1) && starts)
        }
        LetKw => {
            // `let in = …` names a binding `in`. A bare `let in` is no binding.
            let starts = PAT_ATOM_START.contains(next)
                || (is_reserved_word(next) && (next != InKw || reserved_def_name_at(p, 1)));
            !(p.nth_continues(1) && starts)
        }
        _ => false,
    }
}

/// The current reserved word names a definition: it is followed, on the same
/// line, by `:` (an annotation) or by parameter patterns and then `=`
/// (`type = 1`, `case x = x`, `in : Int`). `type X`, `type alias`, `module X`
/// and `import X` start their own constructs, so they never name one.
fn at_reserved_def_name(p: &Parser) -> bool {
    reserved_def_name_at(p, 0)
}

/// [`at_reserved_def_name`] for the token `n` ahead.
fn reserved_def_name_at(p: &Parser, n: usize) -> bool {
    if !is_reserved_word(p.nth(n)) {
        return false;
    }
    if matches!(p.nth(n), TypeKw | ModuleKw | ImportKw)
        && matches!(p.nth(n + 1), UpperIdent | AliasKw)
    {
        return false;
    }
    let mut k = n + 1;
    loop {
        if p.nth_newline_before(k) {
            return false;
        }
        match p.nth(k) {
            Eq => return true,
            Colon if k == n + 1 => return true,
            kind if PAT_ATOM_START.contains(kind)
                || is_reserved_word(kind)
                || matches!(kind, RParen | RBrack | RBrace | Comma | Colon2) =>
            {
                k += 1
            }
            _ => return false,
        }
    }
}

/// The current reserved word is a constructor-pattern argument (`Just type`,
/// `W in`). No reserved word can follow a complete pattern except `as` before
/// its alias name (`Just x as y`), so any other reserved word here was meant
/// as a variable.
fn at_reserved_ctor_arg(p: &Parser) -> bool {
    p.at_reserved_word() && !(p.at(AsKw) && (p.nth(1) == LowerIdent || is_reserved_word(p.nth(1))))
}

const DECL_START: TokenSet = TokenSet::new(&[TypeKw, LowerIdent, UpperIdent]);

// ---- entry ---------------------------------------------------------------

pub(crate) fn source_file(p: &mut Parser) {
    let m = p.start();
    if p.at(ModuleKw) && !at_reserved_def_name(p) {
        module_header(p);
    }
    while p.at(ImportKw) && !at_reserved_def_name(p) {
        import_decl(p);
    }
    // top-level anchor is column 1: a decl body continues while col > 1.
    p.with_indent(1, |p| {
        while !p.at_end() {
            if p.at_any(DECL_START) || at_reserved_def_name(p) {
                decl(p);
            } else if p.at_block_boundary() {
                // A non-declaration token sitting AT the top-level anchor (col 1
                // on a fresh line) — e.g. a stray `import` after a decl, or a
                // mis-indented keyword. `err_recover` early-returns at a block
                // boundary WITHOUT advancing, so calling it here would spin this
                // `while !at_end()` loop forever (the token can never start a
                // declaration and is never consumed). Bump exactly one token to
                // guarantee forward progress — ill-formed input must terminate as
                // diagnostics, never hang (L7). Valid programs never reach here:
                // a well-formed top level is only decls after the imports.
                p.err_and_bump("expected a declaration");
            } else {
                // recover to the next declaration anchor rather than eating one
                // token at a time (doc 04 §11). Not at a block boundary here, so
                // `err_recover` consumes ≥1 token — progress is guaranteed.
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
    if at_reserved_def_name(p) {
        value_or_anno(p);
        return;
    }
    match p.current() {
        TypeKw => type_decl(p),
        LowerIdent | UpperIdent => value_or_anno(p),
        _ => p.err_and_bump("expected a declaration"),
    }
}

fn value_or_anno(p: &mut Parser) {
    let m = p.start();
    if p.at_reserved_word() {
        p.reserved_word_error_and_bump();
    } else {
        p.bump(); // the binding / type name
    }
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
        while (p.at_any(PAT_ATOM_START) || p.at_reserved_word()) && !p.at(Eq) {
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
        // atomic pattern arguments. A reserved word here was meant as a
        // variable (`Just type`, `W in`): `pattern_atom` names it.
        while p.at_any(PAT_ATOM_START) || at_reserved_ctor_arg(p) {
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
            if p.at(String) && !is_terminated_string(p.cur_text()) {
                p.error("unterminated string literal — add a closing `\"`");
            }
            p.bump();
            m.complete(p, PatString)
        }
        Char => {
            let m = p.start();
            if !valid_char_literal(p.cur_text()) {
                p.error(char_error_msg());
            }
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
        _ if p.at_reserved_word() => {
            let m = p.start();
            p.reserved_word_error_and_bump();
            m.complete(p, Error)
        }
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
    if !p.at_any(ARG_START) && !at_reserved_value(p) {
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
    while p.at(Dot)
        && !p.ws_before()
        && (p.nth(1) == LowerIdent || (is_reserved_word(p.nth(1)) && !p.nth_ws_before(1)))
    {
        let m = cm.precede(p);
        p.bump(); // .
        if p.at_reserved_word() {
            // `record.type`: a reserved word cannot be a field name.
            p.reserved_word_error_and_bump();
        } else {
            p.bump(); // field
        }
        cm = m.complete(p, FieldAccess);
    }
    cm
}

/// A well-formed Sky character literal contains exactly one character: either a
/// single non-backslash codepoint (`'a'`, `'é'`) or a backslash followed by
/// exactly one codepoint (`'\n'`, `'\\'`, `'\''`). The lexer scans to the next
/// quote of any length, so `''` (empty), `'ab'` (multi), and `'\x41'`
/// (multi-codepoint escape) reach the parser as `Char` tokens — the oracle
/// rejects all three at parse, so we do too. Checking the RAW inner structure
/// (not a decoded value) is deliberate: decoding would accept `\x41`/`\u{}` the
/// oracle rejects and could reject `\d` the oracle accepts.
fn valid_char_literal(text: &str) -> bool {
    let Some(inner) = text.strip_prefix('\'').and_then(|s| s.strip_suffix('\'')) else {
        return false; // unterminated / not a well-formed `'…'`
    };
    let n = inner.chars().count();
    (n == 1 && !inner.starts_with('\\')) || (n == 2 && inner.starts_with('\\'))
}

/// Whether a `String` token is terminated (has an unescaped closing `"`). The
/// lexer auto-closes an unterminated string at newline/EOF into the same `Str`
/// token, so the parser must reject it — else `msg = "oops` (no closing quote)
/// type-checks and go-builds clean (a check≢build hole; the oracle emits E0001).
fn is_terminated_string(text: &str) -> bool {
    let mut chars = text.chars();
    if chars.next() != Some('"') {
        return false;
    }
    let mut escaped = false;
    for c in chars {
        if escaped {
            escaped = false;
        } else if c == '\\' {
            escaped = true;
        } else if c == '"' {
            return true;
        }
    }
    false
}

fn char_error_msg() -> &'static str {
    "a character literal must contain exactly one character — e.g. `'a'` or an \
     escape like `'\\n'` / `'\\\\'`"
}

fn atom(p: &mut Parser) -> CompletedMarker {
    match p.current() {
        Int | HexInt | Float => {
            let m = p.start();
            p.bump();
            m.complete(p, Literal)
        }
        String => {
            let m = p.start();
            if !is_terminated_string(p.cur_text()) {
                p.error("unterminated string literal — add a closing `\"`");
            }
            p.bump();
            m.complete(p, Literal)
        }
        Char => {
            let m = p.start();
            if !valid_char_literal(p.cur_text()) {
                p.error(char_error_msg());
            }
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
            // A qualified path is `Upper(.Upper)*` optionally ended by ONE
            // `.lower` value segment. Module names are upper-case segments, so
            // the qualifier ends at the first lower-case segment: in
            // `Shape.origin.x` the value is `Shape.origin` and `.x` is a field
            // access, which `postfix` (via `primary_expr`) parses as it does for
            // `record.field`. Without the stop, the whole run became one
            // `QualRefExpr` and resolved as the undefined name `Shape.origin.x`.
            let mut qualified = false;
            while p.at(Dot) && !p.ws_before() && (p.nth(1) == LowerIdent || p.nth(1) == UpperIdent)
            {
                let value_segment = p.nth(1) == LowerIdent;
                p.bump(); // .
                p.bump(); // name
                qualified = true;
                if value_segment {
                    break;
                }
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
        // A reserved word that cannot start or end an expression here was
        // meant as a name (`in type`, `Log.println then)`).
        _ if at_reserved_value(p) => {
            let m = p.start();
            p.reserved_word_error_and_bump();
            m.complete(p, Error)
        }
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
    p.push_bracket();
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
    p.pop_awaited();
    m.complete(p, if is_tuple { TupleExpr } else { ParenExpr })
}

fn list_expr(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // [
    p.push_bracket();
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
    p.pop_awaited();
    m.complete(p, ListExpr)
}

fn record_expr(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // {
    p.push_bracket();
    let kind = p.with_indent(0, |p| {
        if p.at(RBrace) {
            p.bump();
            return RecordExpr;
        }
        // Update detection. The base is any value reference: a bare local
        // (`{ x | … }`) or a QUALIFIED path (`{ App.webDefaults | … }`). Record
        // FIELDS are always lowercase, so a leading UpperIdent is unambiguously
        // a qualified-base update (or an error) — never a literal. The base is
        // left as bare tokens in the CST (no wrapper node), exactly like the
        // bare case, so existing record-update CST/fmt/roundtrip goldens are
        // untouched.
        // A reserved word as the base (`{ type | … }`) is named by `expect`.
        let update =
            ((p.at(LowerIdent) || p.at_reserved_word()) && p.nth(1) == Pipe) || p.at(UpperIdent);
        if update {
            if p.at(UpperIdent) {
                // qualified base: `Upper(.Upper|.lower)*`, mirroring the atom
                // parser's QualRefExpr path.
                p.bump(); // Upper
                while p.at(Dot)
                    && !p.ws_before()
                    && (p.nth(1) == LowerIdent || p.nth(1) == UpperIdent)
                {
                    p.bump(); // .
                    p.bump(); // name
                }
            } else {
                p.expect(LowerIdent); // bare record name
            }
            p.expect(Pipe); // |
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
    p.pop_awaited();
    m.complete(p, kind)
}

fn lambda_expr(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // backslash
    let pl = p.start();
    while (p.at_any(PAT_ATOM_START) || p.at_reserved_word()) && !p.at(Arrow) {
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
    p.push_awaited(ThenKw);
    expr(p); // condition (stops at `then`)
    p.pop_awaited();
    p.expect(ThenKw);
    p.push_awaited(ElseKw);
    expr(p); // then-branch (stops at `else`)
    p.pop_awaited();
    p.expect(ElseKw);
    expr(p); // else-branch (a nested `if` folds an else-if chain)
    m.complete(p, IfExpr)
}

fn let_expr(p: &mut Parser) -> CompletedMarker {
    let m = p.start();
    p.bump(); // let
    let anchor = p.col(); // first binding's column
    p.push_awaited(InKw);
    loop {
        // `in` that names a definition (`in = …`, `in : Int`) is `in` misused
        // as a binding name, not the end of the bindings: parse it as a
        // binding so the error names it.
        if (p.at(InKw) && !at_reserved_def_name(p)) || p.at_end() {
            break;
        }
        let_binding(p, anchor);
        if p.at(InKw) && !at_reserved_def_name(p) {
            break;
        }
        // next sibling binding must align exactly at the anchor column.
        if p.newline_before() && p.col() == anchor {
            continue;
        }
        break;
    }
    p.pop_awaited();
    p.expect(InKw);
    expr(p); // the `in` body
    m.complete(p, LetExpr)
}

fn let_binding(p: &mut Parser, anchor: u32) {
    // A reserved word cannot start a pattern, so one at the start of a
    // binding was meant as its name (`expect` names it). `in` ends the
    // bindings unless it names a definition (see `let_expr`).
    let named =
        p.at(LowerIdent) || (p.at_reserved_word() && (!p.at(InKw) || at_reserved_def_name(p)));
    if named && p.nth(1) == Colon {
        // `name : Type` annotation binding
        let m = p.start();
        p.expect(LowerIdent); // name
        p.bump(); // :
        p.with_indent(anchor, |p| {
            ty(p);
        });
        m.complete(p, LetBinding);
    } else if named {
        // `name pat* = expr`
        let m = p.start();
        p.expect(LowerIdent); // name
        let pl = p.start();
        while (p.at_any(PAT_ATOM_START) || p.at_reserved_word()) && !p.at(Eq) {
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
    p.push_awaited(OfKw);
    expr(p); // subject (stops at `of`)
    p.pop_awaited();
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
