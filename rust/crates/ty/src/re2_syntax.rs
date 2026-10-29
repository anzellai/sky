//! `[E2014]`: is a literal regular expression valid RE2?
//!
//! `Regex.fromString` compiles its pattern with Go's `regexp.Compile`, which
//! parses with `regexp/syntax` under the `Perl` flags. When the pattern is a
//! literal, the checker runs the same parse here, so an invalid pattern is a
//! compile error with source context instead of an `Err` at run time.
//!
//! This is a port of the VALIDATION in Go's `regexp/syntax/parse.go` (go1.26):
//! the same scan, the same decisions about what is a repeat, an escape, a
//! group or a class, and the same error codes and texts
//! (`error parsing regexp: missing closing ): \`(\``). It builds only the shape
//! the checks need (a stack of groups, repeats and atoms), never a program.
//!
//! It must never refuse a pattern Go accepts: that would reject a working
//! program. Where an exact check would need Go's own tables, it accepts:
//!
//! * a Unicode class name in braces (`\p{Greek}`) is accepted whatever the
//!   name, unless it is empty (a one-letter name, `\pL`, must be one of Go's
//!   seven general categories);
//! * the size limits (`expression too large`, `expression nests too deeply`)
//!   are not checked.
//!
//! A pattern accepted here that Go refuses still fails safely: it is the `Err`
//! of `Regex.fromString` at run time. `rust/crates/sky/tests/regex_literal_flow.rs`
//! compares this parser with Go's on a table of patterns.

/// Go's `syntax.ErrorCode` texts.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Re2Error {
    InvalidCharRange,
    InvalidEscape,
    InvalidNamedCapture,
    InvalidPerlOp,
    InvalidRepeatOp,
    InvalidRepeatSize,
    MissingBracket,
    MissingParen,
    MissingRepeatArgument,
    TrailingBackslash,
    UnexpectedParen,
}

impl Re2Error {
    pub fn text(self) -> &'static str {
        match self {
            Re2Error::InvalidCharRange => "invalid character class range",
            Re2Error::InvalidEscape => "invalid escape sequence",
            Re2Error::InvalidNamedCapture => "invalid named capture",
            Re2Error::InvalidPerlOp => "invalid or unsupported Perl syntax",
            Re2Error::InvalidRepeatOp => "invalid nested repetition operator",
            Re2Error::InvalidRepeatSize => "invalid repeat count",
            Re2Error::MissingBracket => "missing closing ]",
            Re2Error::MissingParen => "missing closing )",
            Re2Error::MissingRepeatArgument => "missing argument to repetition operator",
            Re2Error::TrailingBackslash => "trailing backslash at end of expression",
            Re2Error::UnexpectedParen => "unexpected )",
        }
    }
}

/// The error `regexp.Compile` returns, as its `Error()` text.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Re2Failure {
    pub code: Re2Error,
    pub expr: String,
}

impl Re2Failure {
    /// `error parsing regexp: <code>: \`<expr>\``, exactly as Go prints it.
    pub fn message(&self) -> String {
        format!(
            "error parsing regexp: {}: `{}`",
            self.code.text(),
            self.expr
        )
    }
}

fn fail<T>(code: Re2Error, expr: &str) -> Result<T, Re2Failure> {
    Err(Re2Failure {
        code,
        expr: expr.to_string(),
    })
}

/// One element of the parse stack.
#[derive(Clone, Debug)]
enum Node {
    /// `(` or `(?:` (a pseudo-op: a repeat cannot apply to it).
    LeftParen,
    /// `|` (a pseudo-op).
    VerticalBar,
    /// A repeat of its operand: `{min,max}` (`counted`, `max` -1 for no
    /// upper bound), or `*` `+` `?` (not counted: the size check looks
    /// through them, as Go's does through `OpStar`).
    Repeat {
        counted: bool,
        min: i64,
        max: i64,
        sub: Box<Node>,
    },
    /// Anything else, with the repeats inside it (a group, a concatenation,
    /// an alternation, an atom).
    Group(Vec<Node>),
}

impl Node {
    fn is_pseudo(&self) -> bool {
        matches!(self, Node::LeftParen | Node::VerticalBar)
    }
}

/// Go's `repeatIsValid`: the product of nested repeat counts stays under
/// `n` (1000).
fn repeat_is_valid(re: &Node, mut n: i64) -> bool {
    match re {
        Node::Repeat {
            counted: false,
            sub,
            ..
        } => repeat_is_valid(sub, n),
        Node::Repeat { min, max, sub, .. } => {
            let mut m = *max;
            if m == 0 {
                return true;
            }
            if m < 0 {
                m = *min;
            }
            if m > n {
                return false;
            }
            if m > 0 {
                n /= m;
            }
            repeat_is_valid(sub, n)
        }
        Node::Group(subs) => subs.iter().all(|s| repeat_is_valid(s, n)),
        _ => true,
    }
}

struct Parser {
    stack: Vec<Node>,
}

impl Parser {
    /// Collapse the stack above the nearest pseudo-op into one node (Go's
    /// `concat`).
    fn concat(&mut self) {
        let mut i = self.stack.len();
        while i > 0 && !self.stack[i - 1].is_pseudo() {
            i -= 1;
        }
        let subs: Vec<Node> = self.stack.drain(i..).collect();
        self.stack.push(Node::Group(subs));
    }

    /// Go's `parseVerticalBar`.
    fn vertical_bar(&mut self) {
        self.concat();
        // The alternation so far stays below the bar.
        self.stack.push(Node::VerticalBar);
    }

    /// Collapse everything above the nearest `(` (or the bottom) into one
    /// node: the concatenation since the last `|`, and the alternation.
    fn alternate(&mut self) {
        self.concat();
        let mut i = self.stack.len();
        while i > 0 && !matches!(self.stack[i - 1], Node::LeftParen) {
            i -= 1;
        }
        let subs: Vec<Node> = self
            .stack
            .drain(i..)
            .filter(|n| !matches!(n, Node::VerticalBar))
            .collect();
        self.stack.push(Node::Group(subs));
    }

    fn right_paren(&mut self, whole: &str) -> Result<(), Re2Failure> {
        self.alternate();
        let n = self.stack.len();
        if n < 2 {
            return fail(Re2Error::UnexpectedParen, whole);
        }
        let re1 = self.stack.pop().unwrap();
        let re2 = self.stack.pop().unwrap();
        if !matches!(re2, Node::LeftParen) {
            return fail(Re2Error::UnexpectedParen, whole);
        }
        self.stack.push(Node::Group(vec![re1]));
        Ok(())
    }

    /// Go's `repeat`. `before` is the text from the operator on, `after` the
    /// text after it (its non-greedy `?` not yet taken).
    fn repeat(
        &mut self,
        min: i64,
        max: i64,
        is_counted: bool,
        before: &str,
        mut after: &str,
        last_repeat: &str,
    ) -> Result<String, Re2Failure> {
        if let Some(rest) = after.strip_prefix('?') {
            after = rest;
        }
        if !last_repeat.is_empty() {
            let cut = last_repeat.len() - after.len();
            return fail(Re2Error::InvalidRepeatOp, &last_repeat[..cut]);
        }
        let op_text = &before[..before.len() - after.len()];
        let Some(sub) = self.stack.pop() else {
            return fail(Re2Error::MissingRepeatArgument, op_text);
        };
        if sub.is_pseudo() {
            return fail(Re2Error::MissingRepeatArgument, op_text);
        }
        let re = Node::Repeat {
            counted: is_counted,
            min,
            max,
            sub: Box::new(sub),
        };
        if is_counted && (min >= 2 || max >= 2) && !repeat_is_valid(&re, 1000) {
            return fail(Re2Error::InvalidRepeatSize, op_text);
        }
        self.stack.push(re);
        Ok(after.to_string())
    }
}

/// Validate `pattern` as Go's `regexp.Compile` would.
pub fn validate(pattern: &str) -> Result<(), Re2Failure> {
    let mut p = Parser { stack: Vec::new() };
    let whole = pattern;
    let mut t: &str = pattern;
    let mut last_repeat = String::new();
    while let Some(c) = t.chars().next() {
        let mut repeat = String::new();
        match c {
            '(' => {
                if t.as_bytes().get(1) == Some(&b'?') {
                    t = parse_perl_flags(&mut p, t)?;
                } else {
                    p.stack.push(Node::LeftParen);
                    t = &t[1..];
                }
            }
            '|' => {
                p.vertical_bar();
                t = &t[1..];
            }
            ')' => {
                p.right_paren(whole)?;
                t = &t[1..];
            }
            '^' | '$' | '.' => {
                p.stack.push(Node::Group(Vec::new()));
                t = &t[1..];
            }
            '[' => {
                t = parse_class(t)?;
                p.stack.push(Node::Group(Vec::new()));
            }
            '*' | '+' | '?' => {
                let before = t;
                let after = p.repeat(0, 0, false, before, &t[1..], &last_repeat)?;
                repeat = before.to_string();
                t = &before[before.len() - after.len()..];
            }
            '{' => {
                let before = t;
                match parse_repeat(t) {
                    None => {
                        // Not a repeat: `{` is a literal.
                        p.stack.push(Node::Group(Vec::new()));
                        t = &t[1..];
                    }
                    Some((min, max, after)) => {
                        if min < 0 || min > 1000 || max > 1000 || (max >= 0 && min > max) {
                            return fail(
                                Re2Error::InvalidRepeatSize,
                                &before[..before.len() - after.len()],
                            );
                        }
                        let rest = p.repeat(min, max, true, before, after, &last_repeat)?;
                        repeat = before.to_string();
                        t = &before[before.len() - rest.len()..];
                    }
                }
            }
            '\\' => {
                t = parse_backslash(&mut p, t)?;
            }
            _ => {
                p.stack.push(Node::Group(Vec::new()));
                t = &t[c.len_utf8()..];
            }
        }
        // `repeat` holds the text from the operator; `last_repeat` is what
        // Go compares the next operator against.
        last_repeat = if repeat.is_empty() {
            String::new()
        } else {
            repeat
        };
    }
    p.alternate();
    if p.stack.len() != 1 {
        return fail(Re2Error::MissingParen, whole);
    }
    Ok(())
}

/// A `\` outside a class: the Perl assertions and `\Q…\E`, a Unicode class,
/// a Perl class, else an ordinary escape.
fn parse_backslash<'a>(p: &mut Parser, t: &'a str) -> Result<&'a str, Re2Failure> {
    let b = t.as_bytes();
    if b.len() >= 2 {
        match b[1] {
            b'A' | b'b' | b'B' | b'z' => {
                p.stack.push(Node::Group(Vec::new()));
                return Ok(&t[2..]);
            }
            b'C' => return fail(Re2Error::InvalidEscape, &t[..2]),
            b'Q' => {
                // `\Q…\E`: everything up to `\E` (or the end) is literal.
                let rest = &t[2..];
                let (lit, after) = match rest.find("\\E") {
                    Some(i) => (&rest[..i], &rest[i + 2..]),
                    None => (rest, ""),
                };
                for _ in lit.chars() {
                    p.stack.push(Node::Group(Vec::new()));
                }
                return Ok(after);
            }
            _ => {}
        }
    }
    if let Some(rest) = parse_unicode_class(t)? {
        p.stack.push(Node::Group(Vec::new()));
        return Ok(rest);
    }
    if let Some(rest) = parse_perl_class_escape(t) {
        p.stack.push(Node::Group(Vec::new()));
        return Ok(rest);
    }
    let rest = parse_escape(t)?;
    p.stack.push(Node::Group(Vec::new()));
    Ok(rest)
}

/// `(?…`: a named group `(?P<name>` / `(?<name>`, or flags `(?i)` /
/// `(?i:`. Go's `parsePerlFlags`.
fn parse_perl_flags<'a>(p: &mut Parser, s: &'a str) -> Result<&'a str, Re2Failure> {
    let b = s.as_bytes();
    let starts_with_p = b.len() > 4 && b[2] == b'P' && b[3] == b'<';
    let starts_with_name = b.len() > 3 && b[2] == b'<';
    if starts_with_p || starts_with_name {
        let expr_start = if starts_with_p { 4 } else { 3 };
        let Some(end) = s.find('>') else {
            return fail(Re2Error::InvalidNamedCapture, s);
        };
        let capture = &s[..end + 1];
        let name = &s[expr_start..end];
        if !is_valid_capture_name(name) {
            return fail(Re2Error::InvalidNamedCapture, capture);
        }
        p.stack.push(Node::LeftParen);
        return Ok(&s[end + 1..]);
    }
    let mut t = &s[2..];
    let mut sign = 1;
    let mut saw_flag = false;
    while let Some(c) = t.chars().next() {
        t = &t[c.len_utf8()..];
        match c {
            'i' | 'm' | 's' | 'U' => saw_flag = true,
            '-' => {
                if sign < 0 {
                    break;
                }
                sign = -1;
                saw_flag = false;
            }
            ':' | ')' => {
                if sign < 0 && !saw_flag {
                    break;
                }
                if c == ':' {
                    p.stack.push(Node::LeftParen);
                }
                return Ok(t);
            }
            _ => break,
        }
    }
    fail(Re2Error::InvalidPerlOp, &s[..s.len() - t.len()])
}

/// Go's `isValidCaptureName`: non-empty, letters, digits and `_`.
fn is_valid_capture_name(name: &str) -> bool {
    !name.is_empty() && name.chars().all(|c| c == '_' || c.is_alphanumeric())
}

/// `{min}`, `{min,}` or `{min,max}` at the start of `s`, with what follows.
/// `None` when it is not a repeat (then `{` is a literal). A number too big
/// for Go's parser is `-1`, which the caller refuses as a repeat count.
fn parse_repeat(s: &str) -> Option<(i64, i64, &str)> {
    let s = s.strip_prefix('{')?;
    let (min, s) = parse_int(s)?;
    let (max, s) = if let Some(s) = s.strip_prefix(',') {
        if let Some(rest) = s.strip_prefix('}') {
            return Some((min, -1, rest));
        }
        let (max, s) = parse_int(s)?;
        if max < 0 {
            // Go: an overlong max makes min -1, refused as a repeat count.
            return s.strip_prefix('}').map(|rest| (-1, max, rest));
        }
        (max, s)
    } else {
        (min, s)
    };
    s.strip_prefix('}').map(|rest| (min, max, rest))
}

/// Go's `parseInt`: decimal digits, no leading zero, `-1` past 8 digits.
fn parse_int(s: &str) -> Option<(i64, &str)> {
    let b = s.as_bytes();
    if b.is_empty() || !b[0].is_ascii_digit() {
        return None;
    }
    if b.len() >= 2 && b[0] == b'0' && b[1].is_ascii_digit() {
        return None;
    }
    let n = b.iter().take_while(|c| c.is_ascii_digit()).count();
    let digits = &s[..n];
    let v = if n > 8 {
        -1
    } else {
        digits.parse::<i64>().unwrap_or(-1)
    };
    Some((v, &s[n..]))
}

/// `\d \D \s \S \w \W`.
fn parse_perl_class_escape(s: &str) -> Option<&str> {
    let b = s.as_bytes();
    if b.len() < 2 || b[0] != b'\\' {
        return None;
    }
    matches!(b[1], b'd' | b'D' | b's' | b'S' | b'w' | b'W').then(|| &s[2..])
}

/// `\pL`, `\p{Name}`, `\PL`, `\P{^Name}`. `Ok(None)` when `s` is not one.
/// Any non-empty name is accepted (see the module docs).
fn parse_unicode_class(s: &str) -> Result<Option<&str>, Re2Failure> {
    let b = s.as_bytes();
    if b.len() < 2 || b[0] != b'\\' || (b[1] != b'p' && b[1] != b'P') {
        return Ok(None);
    }
    let t = &s[2..];
    let Some(c) = t.chars().next() else {
        return fail(Re2Error::InvalidCharRange, &s[..2]);
    };
    let (seq, rest, name) = if c != '{' {
        let seq = &s[..2 + c.len_utf8()];
        (seq, &s[seq.len()..], &seq[2..])
    } else {
        let Some(end) = s.find('}') else {
            return fail(Re2Error::InvalidCharRange, s);
        };
        (&s[..end + 1], &s[end + 1..], &s[3..end])
    };
    let name = name.strip_prefix('^').unwrap_or(name);
    // A one-letter name is a general category, and Go has seven of them.
    let one_letter_unknown = c != '{' && !matches!(name, "C" | "L" | "M" | "N" | "P" | "S" | "Z");
    if name.is_empty() || one_letter_unknown {
        return fail(Re2Error::InvalidCharRange, seq);
    }
    Ok(Some(rest))
}

/// The POSIX class names Go knows (`[:alpha:]`, negated `[:^alpha:]`).
const POSIX_NAMES: &[&str] = &[
    "alnum", "alpha", "ascii", "blank", "cntrl", "digit", "graph", "lower", "print", "punct",
    "space", "upper", "word", "xdigit",
];

/// `[:name:]` inside a class. `Ok(None)` when `s` does not start one.
fn parse_named_class(s: &str) -> Result<Option<&str>, Re2Failure> {
    if !s.starts_with("[:") {
        return Ok(None);
    }
    let Some(i) = s[2..].find(":]") else {
        return Ok(None);
    };
    let i = i + 2;
    let name = &s[..i + 2];
    let inner = &name[2..name.len() - 2];
    let inner = inner.strip_prefix('^').unwrap_or(inner);
    if !POSIX_NAMES.contains(&inner) {
        return fail(Re2Error::InvalidCharRange, name);
    }
    Ok(Some(&s[i + 2..]))
}

/// `[…]`, from its `[`. Returns what follows the closing `]`.
fn parse_class(s: &str) -> Result<&str, Re2Failure> {
    let mut t = &s[1..];
    if let Some(rest) = t.strip_prefix('^') {
        t = rest;
    }
    let mut first = true;
    while t.is_empty() || !t.starts_with(']') || first {
        first = false;
        if t.len() > 2 && t.starts_with("[:") {
            if let Some(rest) = parse_named_class(t)? {
                t = rest;
                continue;
            }
        }
        if let Some(rest) = parse_unicode_class(t)? {
            t = rest;
            continue;
        }
        if let Some(rest) = parse_perl_class_escape(t) {
            t = rest;
            continue;
        }
        let rng = t;
        let (lo, rest) = parse_class_char(t, s)?;
        t = rest;
        let tb = t.as_bytes();
        if tb.len() >= 2 && tb[0] == b'-' && tb[1] != b']' {
            t = &t[1..];
            let (hi, rest) = parse_class_char(t, s)?;
            t = rest;
            if hi < lo {
                return fail(Re2Error::InvalidCharRange, &rng[..rng.len() - t.len()]);
            }
        }
    }
    Ok(&t[1..])
}

fn parse_class_char<'a>(s: &'a str, whole_class: &str) -> Result<(u32, &'a str), Re2Failure> {
    if s.is_empty() {
        return fail(Re2Error::MissingBracket, whole_class);
    }
    if s.starts_with('\\') {
        return parse_escape_value(s);
    }
    let c = s.chars().next().unwrap();
    Ok((c as u32, &s[c.len_utf8()..]))
}

fn parse_escape(s: &str) -> Result<&str, Re2Failure> {
    parse_escape_value(s).map(|(_, rest)| rest)
}

fn unhex(c: char) -> Option<u32> {
    c.to_digit(16)
}

/// Go's `parseEscape`: an ordinary escape, with the character it stands for.
fn parse_escape_value(s: &str) -> Result<(u32, &str), Re2Failure> {
    let mut t = &s[1..];
    let Some(c) = t.chars().next() else {
        return fail(Re2Error::TrailingBackslash, "");
    };
    t = &t[c.len_utf8()..];
    let invalid = |t: &str| fail(Re2Error::InvalidEscape, &s[..s.len() - t.len()]);
    match c {
        '1'..='7' => {
            // A single non-zero digit is a backreference, not supported.
            match t.as_bytes().first() {
                Some(d) if (b'0'..=b'7').contains(d) => {}
                _ => return invalid(t),
            }
            Ok(octal(c, t))
        }
        '0' => Ok(octal(c, t)),
        'x' => {
            let Some(c1) = t.chars().next() else {
                return invalid(t);
            };
            t = &t[c1.len_utf8()..];
            if c1 == '{' {
                let mut nhex = 0;
                let mut r: u32 = 0;
                loop {
                    let Some(d) = t.chars().next() else {
                        return invalid(t);
                    };
                    t = &t[d.len_utf8()..];
                    if d == '}' {
                        break;
                    }
                    let Some(v) = unhex(d) else {
                        return invalid(t);
                    };
                    r = r * 16 + v;
                    if r > 0x10FFFF {
                        return invalid(t);
                    }
                    nhex += 1;
                }
                if nhex == 0 {
                    return invalid(t);
                }
                return Ok((r, t));
            }
            let x = unhex(c1);
            let c2 = t.chars().next();
            if let Some(c2) = c2 {
                t = &t[c2.len_utf8()..];
            }
            match (x, c2.and_then(unhex)) {
                (Some(x), Some(y)) => Ok((x * 16 + y, t)),
                _ => invalid(t),
            }
        }
        'a' => Ok((7, t)),
        'f' => Ok((12, t)),
        'n' => Ok((10, t)),
        'r' => Ok((13, t)),
        't' => Ok((9, t)),
        'v' => Ok((11, t)),
        c if (c as u32) < 0x80 && !c.is_ascii_alphanumeric() => Ok((c as u32, t)),
        _ => invalid(t),
    }
}

/// Up to two more octal digits after the first.
fn octal(first: char, mut t: &str) -> (u32, &str) {
    let mut r = first as u32 - '0' as u32;
    for _ in 1..3 {
        match t.as_bytes().first() {
            Some(d) if (b'0'..=b'7').contains(d) => {
                r = r * 8 + (*d - b'0') as u32;
                t = &t[1..];
            }
            _ => break,
        }
    }
    (r, t)
}

#[cfg(test)]
mod tests {
    use super::validate;

    fn err(p: &str) -> String {
        validate(p).expect_err(p).message()
    }

    #[test]
    fn valid_patterns_pass() {
        for p in [
            "",
            r"\s+",
            r"\b\w+\b",
            r"\d{2,4}",
            r"\d{2}",
            r"\d{2,}",
            "a{",
            "a{,3}",
            "{",
            "}",
            "x{01}",
            "a*?",
            "a{2}?",
            "(?i)abc",
            "(?i:a)b",
            "(?-s:.)",
            "(?P<year>\\d+)",
            "(?<year>\\d+)",
            "[a-z0-9_]",
            "[]a]",
            "[^]a]",
            "[a-]",
            "[[:alpha:]]",
            "[[:^space:]]",
            r"\pL\p{Greek}\PN\p{^Han}",
            r"[\d\s\pL]",
            r"\x41\x{1F600}\101\0",
            r"\Qa.b*\E+",
            r"\Qunclosed.",
            r"\A^$\z",
            "^*",
            "()*",
            "a|b|",
            "|",
            r"\.\-\_\/",
            r"[\-\]]",
            "(a{10}){10}",
            "(?)",
            "((a{10})*){10}",
        ] {
            assert!(validate(p).is_ok(), "{p:?}: {:?}", validate(p));
        }
    }

    #[test]
    fn invalid_patterns_fail_with_go_text() {
        assert_eq!(err("("), "error parsing regexp: missing closing ): `(`");
        assert_eq!(err("a)"), "error parsing regexp: unexpected ): `a)`");
        assert_eq!(err("[a"), "error parsing regexp: missing closing ]: `[a`");
        assert_eq!(
            err("*"),
            "error parsing regexp: missing argument to repetition operator: `*`"
        );
        assert_eq!(
            err("a**"),
            "error parsing regexp: invalid nested repetition operator: `**`"
        );
        assert_eq!(
            err("a{3,2}"),
            "error parsing regexp: invalid repeat count: `{3,2}`"
        );
        assert_eq!(
            err("a{1001}"),
            "error parsing regexp: invalid repeat count: `{1001}`"
        );
        assert_eq!(
            err("(a{100}){11}"),
            "error parsing regexp: invalid repeat count: `{11}`"
        );
        assert_eq!(
            err(r"\8"),
            "error parsing regexp: invalid escape sequence: `\\8`"
        );
        assert_eq!(
            err(r"\1"),
            "error parsing regexp: invalid escape sequence: `\\1`"
        );
        assert_eq!(
            err(r"\C"),
            "error parsing regexp: invalid escape sequence: `\\C`"
        );
        assert_eq!(
            err("a\\"),
            "error parsing regexp: trailing backslash at end of expression: ``"
        );
        assert_eq!(
            err("[z-a]"),
            "error parsing regexp: invalid character class range: `z-a`"
        );
        assert_eq!(
            err("[[:foo:]]"),
            "error parsing regexp: invalid character class range: `[:foo:]`"
        );
        assert_eq!(
            err("(?=a)"),
            "error parsing regexp: invalid or unsupported Perl syntax: `(?=`"
        );
        assert_eq!(
            err("(?P<>a)"),
            "error parsing regexp: invalid named capture: `(?P<>`"
        );
        assert_eq!(
            err(r"\p{}"),
            "error parsing regexp: invalid character class range: `\\p{}`"
        );
        assert_eq!(
            err("{2}"),
            "error parsing regexp: missing argument to repetition operator: `{2}`"
        );
        assert_eq!(
            err("a|*"),
            "error parsing regexp: missing argument to repetition operator: `*`"
        );
        assert_eq!(
            err(r"[\b]"),
            "error parsing regexp: invalid escape sequence: `\\b`"
        );
        assert_eq!(
            err("((a{100})*){11}"),
            "error parsing regexp: invalid repeat count: `{11}`"
        );
    }
}
