//! Work counters for the type layer's size-sensitive loops.
//!
//! Each counter counts the nodes one loop visits: a read-back frame, an occurs
//! check step, a record field copied or unified, an owned type node rebuilt
//! from a [`crate::tytable::TyTable`], a `Dict`-key walk step, a node interned,
//! and a node mapped to a Go type by the lowerer. They exist so a test can pin
//! how the work GROWS with the size of a program: a loop that is linear in the
//! program doubles when the program doubles, a quadratic one quadruples
//! (`rust/crates/skydb/tests/typed_table_growth.rs`).
//!
//! The counters never change what the compiler computes. They are process-wide
//! relaxed atomics, so a reader on another thread sees a consistent total once
//! the work has finished. `sky build` with `SKY_TIMINGS=1` prints them as a
//! note under the phase table.

use std::sync::atomic::{AtomicU64, Ordering};

/// One monotonic work counter.
pub struct Counter(AtomicU64);

impl Counter {
    const fn new() -> Self {
        Counter(AtomicU64::new(0))
    }

    /// Add `n` units of work.
    #[inline]
    pub fn add(&self, n: u64) {
        self.0.fetch_add(n, Ordering::Relaxed);
    }

    /// Add one unit of work.
    #[inline]
    pub fn bump(&self) {
        self.add(1);
    }

    fn get(&self) -> u64 {
        self.0.load(Ordering::Relaxed)
    }
}

/// `Infer::read_back_*` frames (one per union-find node read back).
pub static READBACK_NODES: Counter = Counter::new();
/// `UnionFind::occurs_in` steps.
pub static OCCURS_NODES: Counter = Counter::new();
/// Record fields copied or compared by the unifier (`content` clones of a
/// record, `normalize_record`, `unify_records`).
pub static RECORD_FIELDS: Counter = Counter::new();
/// Owned `Ty` nodes rebuilt from a hash-consed table (`TyTable::get`).
pub static OWNED_REBUILD_NODES: Counter = Counter::new();
/// `dictkey::walk` steps (the `[E2008]` scan).
pub static DICTKEY_NODES: Counter = Counter::new();
/// Nodes offered to a `TyTableBuilder`.
pub static INTERN_NODES: Counter = Counter::new();
/// Sky type nodes mapped to a Go type by the lowerer (`goty`).
pub static GOTY_NODES: Counter = Counter::new();

/// A point-in-time copy of every counter.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub readback_nodes: u64,
    pub occurs_nodes: u64,
    pub record_fields: u64,
    pub owned_rebuild_nodes: u64,
    pub dictkey_nodes: u64,
    pub intern_nodes: u64,
    pub goty_nodes: u64,
}

impl Snapshot {
    /// The counters as `(name, value)` pairs, in a fixed order.
    pub fn fields(&self) -> [(&'static str, u64); 7] {
        [
            ("readback", self.readback_nodes),
            ("occurs", self.occurs_nodes),
            ("record_fields", self.record_fields),
            ("owned_rebuild", self.owned_rebuild_nodes),
            ("dictkey", self.dictkey_nodes),
            ("intern", self.intern_nodes),
            ("goty", self.goty_nodes),
        ]
    }

    /// `self - earlier`, counter by counter.
    pub fn since(&self, earlier: &Snapshot) -> Snapshot {
        Snapshot {
            readback_nodes: self.readback_nodes - earlier.readback_nodes,
            occurs_nodes: self.occurs_nodes - earlier.occurs_nodes,
            record_fields: self.record_fields - earlier.record_fields,
            owned_rebuild_nodes: self.owned_rebuild_nodes - earlier.owned_rebuild_nodes,
            dictkey_nodes: self.dictkey_nodes - earlier.dictkey_nodes,
            intern_nodes: self.intern_nodes - earlier.intern_nodes,
            goty_nodes: self.goty_nodes - earlier.goty_nodes,
        }
    }
}

impl std::fmt::Display for Snapshot {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let parts: Vec<String> = self
            .fields()
            .iter()
            .map(|(n, v)| format!("{n}={v}"))
            .collect();
        f.write_str(&parts.join(" "))
    }
}

/// Read every counter.
pub fn snapshot() -> Snapshot {
    Snapshot {
        readback_nodes: READBACK_NODES.get(),
        occurs_nodes: OCCURS_NODES.get(),
        record_fields: RECORD_FIELDS.get(),
        owned_rebuild_nodes: OWNED_REBUILD_NODES.get(),
        dictkey_nodes: DICTKEY_NODES.get(),
        intern_nodes: INTERN_NODES.get(),
        goty_nodes: GOTY_NODES.get(),
    }
}
