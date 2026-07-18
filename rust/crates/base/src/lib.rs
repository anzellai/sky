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
}
