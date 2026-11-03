#![forbid(unsafe_code)]
//! `base` — interners, ids, spans, small utils. **No logic** (doc 02).
//!
//! This crate is the spine of the interner-centric design (doc 01, law L3):
//! everything with identity becomes an integer id compared by `==` on the int,
//! allocated append-only. Sits at the root of the crate DAG — every other crate
//! depends on it, it depends on none of ours.

use indexmap::IndexSet;
use smol_str::SmolStr;
use std::hash::Hash;

/// Signal a violated **compiler invariant** — a "this cannot happen unless the
/// compiler itself is buggy" condition. Use ONLY for internal invariants that are
/// NOT reachable from ill-formed user input (user-facing failures must be
/// diagnostics / `Result`s). The robustness fuzzer proves 0 panics reach these on
/// arbitrary input, so a `bug!` firing in the field is a genuine compiler bug.
///
/// Panics with a structured, greppable message that mirrors the runtime's
/// `CompilerBug` panic class (`runtime-go/rt/panic_recover.go`):
/// `internal compiler error (please report): <msg> [file:line]`.
///
/// This is a thin wrapper over [`panic!`] — it still UNWINDS, so the fuzzer's
/// `catch_unwind` and the top-level handler catch it exactly as before; only the
/// panic *message* changes (into the greppable, actionable form).
#[macro_export]
macro_rules! bug {
    ($($arg:tt)*) => {
        ::std::panic!(
            "internal compiler error (please report): {} [{}:{}]",
            ::std::format_args!($($arg)*),
            ::std::file!(),
            ::std::line!(),
        )
    };
}

/// Stack left below which [`ensure_stack`] moves onto a new segment.
const STACK_RED_ZONE: usize = 256 * 1024;
/// Size of each new stack segment [`ensure_stack`] allocates.
const STACK_SEGMENT: usize = 4 * 1024 * 1024;

/// Run `f`, first moving onto a fresh heap-allocated stack segment when less
/// than [`STACK_RED_ZONE`] of the current thread's stack is left.
///
/// Every pass over the expression tree (resolve, infer, lower, emit and the
/// checks between them) recurses, and a left-nested `|>` pipeline or a
/// generated record codec (`Codec.object C |> Codec.field … |> …`) is as deep
/// as it is long. Call this at the recursive entry of each such walker so depth
/// is bounded by memory, not by the thread's stack: the same code then runs on
/// `sky`'s main thread and on a 2 MiB LSP worker. It costs one stack-pointer
/// comparison when the stack is not low.
#[inline]
pub fn ensure_stack<R>(f: impl FnOnce() -> R) -> R {
    stacker::maybe_grow(STACK_RED_ZONE, STACK_SEGMENT, f)
}

/// The text of a Go interpreted string literal for `s`, without the quotes.
///
/// Every Sky text that reaches emitted Go source as a string literal goes
/// through here: a Sky string literal, and every name the build writes into
/// a generated Go file. A Go source file is UTF-8, and the Go lexer refuses a
/// NUL (U+0000) and a byte order mark (U+FEFF) anywhere in it but at the start
/// ("invalid BOM in the middle of the file"), so both are escaped, as are the
/// characters an interpreted literal cannot hold (`"`, `\`, a newline), every
/// other C0 and C1 control and DEL (so the source stays readable). Every other
/// code point is written as itself, so ordinary Unicode text is unchanged.
/// Before v0.29.2 (eighth Judge) a U+FEFF in a Sky string type-checked and
/// then failed `go build`.
///
/// Rust's `{:?}` is NOT a Go literal: it writes `\u{feff}`, which Go refuses.
pub fn go_escape_string(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\t' => out.push_str("\\t"),
            '\r' => out.push_str("\\r"),
            c if (c as u32) < 0x20 || c == '\u{7f}' => {
                out.push_str(&format!("\\x{:02x}", c as u32))
            }
            c if ('\u{80}'..='\u{9f}').contains(&c) || c == '\u{feff}' => {
                out.push_str(&format!("\\u{:04x}", c as u32))
            }
            c => out.push(c),
        }
    }
    out
}

/// `s` as a quoted Go interpreted string literal (see [`go_escape_string`]).
pub fn go_string_literal(s: &str) -> String {
    format!("\"{}\"", go_escape_string(s))
}

/// Declares a transparent `u32` newtype id: Copy, cheap, `==`-comparable (L3).
macro_rules! id_newtype {
    ($(#[$m:meta])* $name:ident) => {
        $(#[$m])*
        #[derive(Copy, Clone, PartialEq, Eq, Hash, PartialOrd, Ord, Debug)]
        pub struct $name(pub u32);

        impl $name {
            /// The raw index this id wraps.
            #[inline]
            pub const fn index(self) -> u32 {
                self.0
            }
        }
    };
}

id_newtype!(
    /// A source file. Replaces ad-hoc `FilePath` keys (doc 01).
    FileId
);
id_newtype!(
    /// A canonical module. Replaces `ModuleName.Canonical` (doc 01).
    ModuleId
);
id_newtype!(
    /// A top-level or local definition. Replaces name-string map keys (doc 01).
    DefId
);

/// An interned symbol name — a cheap, `O(1)`-clone small string (L3).
#[derive(Clone, PartialEq, Eq, Hash, PartialOrd, Ord, Debug)]
pub struct Name(SmolStr);

impl Name {
    #[inline]
    pub fn new(s: &str) -> Self {
        Name(SmolStr::new(s))
    }

    #[inline]
    pub fn as_str(&self) -> &str {
        self.0.as_str()
    }
}

impl From<&str> for Name {
    fn from(s: &str) -> Self {
        Name::new(s)
    }
}

/// A source span: a byte range within a file. Replaces `A.Region` (doc 01).
#[derive(Copy, Clone, PartialEq, Eq, Hash, Debug)]
pub struct Span {
    pub file: FileId,
    /// Half-open `[start, end)` byte offsets.
    pub range: (u32, u32),
}

impl Span {
    #[inline]
    pub const fn new(file: FileId, start: u32, end: u32) -> Self {
        Span {
            file,
            range: (start, end),
        }
    }
}

/// A minimal append-only interner over an insertion-ordered set.
///
/// Monotonic, single-writer, deterministic — the "register-on-first-mention"
/// pattern (doc 01). Iterating ids in allocation order is deterministic (L4).
#[derive(Clone, Debug, Default)]
pub struct Interner<T: Eq + Hash + Clone> {
    items: IndexSet<T>,
}

impl<T: Eq + Hash + Clone> Interner<T> {
    pub fn new() -> Self {
        Interner {
            items: IndexSet::new(),
        }
    }

    /// Intern a value, returning its stable id. Idempotent per value.
    pub fn intern(&mut self, value: T) -> u32 {
        let (idx, _) = self.items.insert_full(value);
        idx as u32
    }

    /// Look up a previously interned value by id.
    pub fn lookup(&self, id: u32) -> Option<&T> {
        self.items.get_index(id as usize)
    }

    pub fn len(&self) -> usize {
        self.items.len()
    }

    pub fn is_empty(&self) -> bool {
        self.items.is_empty()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn interner_is_idempotent_and_ordered() {
        let mut i: Interner<Name> = Interner::new();
        let a = i.intern(Name::new("foo"));
        let b = i.intern(Name::new("bar"));
        let a2 = i.intern(Name::new("foo"));
        assert_eq!(a, a2);
        assert_ne!(a, b);
        assert_eq!(i.lookup(a).map(Name::as_str), Some("foo"));
        assert_eq!(i.len(), 2);
    }

    #[test]
    fn ids_are_cheap_and_comparable() {
        assert_eq!(FileId(3).index(), 3);
        assert_eq!(Span::new(FileId(0), 1, 4).range, (1, 4));
    }

    #[test]
    fn bug_macro_unwinds_with_greppable_prefix() {
        // The macro must UNWIND (so catch_unwind / the top-level handler catch it,
        // exactly like a bare panic!) and carry the CompilerBug-class prefix.
        let caught = std::panic::catch_unwind(|| {
            bug!("impossible state {}", 42);
        });
        let payload = caught.expect_err("bug! must panic");
        let msg = payload
            .downcast_ref::<String>()
            .map(String::as_str)
            .or_else(|| payload.downcast_ref::<&str>().copied())
            .expect("bug! payload is a formatted message");
        assert!(
            msg.starts_with("internal compiler error (please report): impossible state 42 ["),
            "unexpected bug! message: {msg}"
        );
    }

    /// Every Unicode scalar value: the literal holds no character the Go
    /// lexer refuses (NUL, U+FEFF, a newline) or a bare control, and reading
    /// its escapes back gives the input. The Go compiler leg of the same
    /// sweep is `rust/crates/sky/tests/go_string_literal_flow.rs`.
    #[test]
    fn go_escape_string_round_trips_every_scalar_value() {
        let all: String = (0u32..=0x10FFFF).filter_map(char::from_u32).collect();
        let lit = go_escape_string(&all);
        for c in lit.chars() {
            assert!(
                c != '\0' && c != '\u{feff}' && c != '\n' && !c.is_control(),
                "the literal holds U+{:04X} unescaped",
                c as u32
            );
        }
        assert_eq!(unescape_go(&lit), all);
        assert_eq!(
            go_string_literal("a\u{feff}\0\"\\"),
            "\"a\\ufeff\\x00\\\"\\\\\""
        );
        // Ordinary text is written as itself.
        assert_eq!(
            go_escape_string("héllo, 世界 \u{2028} 🎉"),
            "héllo, 世界 \u{2028} 🎉"
        );
    }

    /// The Go interpreted-literal escapes `go_escape_string` writes.
    fn unescape_go(s: &str) -> String {
        let mut out = String::new();
        let mut it = s.chars();
        while let Some(c) = it.next() {
            if c != '\\' {
                out.push(c);
                continue;
            }
            match it.next().unwrap() {
                'n' => out.push('\n'),
                't' => out.push('\t'),
                'r' => out.push('\r'),
                '"' => out.push('"'),
                '\\' => out.push('\\'),
                k @ ('x' | 'u') => {
                    let n = if k == 'x' { 2 } else { 4 };
                    let hex: String = it.by_ref().take(n).collect();
                    out.push(char::from_u32(u32::from_str_radix(&hex, 16).unwrap()).unwrap());
                }
                other => panic!("unexpected escape \\{other}"),
            }
        }
        out
    }
}
