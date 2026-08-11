//! Family **S** — the `Dict` KEY-TYPE × OPERATION × ACCESS-SHAPE crossing
//! (anzellai/sky#174).
//!
//! # Why this stratum exists, stated as the failure it is a response to
//!
//! Family S already asserted `Sky.Core.Dict` at five edge classes and shipped
//! #174 anyway:
//!
//! * `Dict.foldl` over a `Dict Int v` **PANICKED** at runtime —
//!   `rt.skyCallDirect: argument 0 type mismatch — function expects int, got
//!   string`.
//! * `Dict.toList` over a `Dict Char v` silently handed back char code **0**.
//! * `Dict.values` over integer keys came back in LEXICAL key order, so keys
//!   `1, 2, 9, 10` enumerated as `1, 10, 2, 9`.
//!
//! The reason the existing battery could not see any of it is a property of the
//! battery's SHAPE, not of its size: `dict_battery` asserts `String` keys
//! everywhere but one item, and a `Dict k v` is a Go `map[string]V`
//! (`goty.rs:531` pins that shape deliberately). Every key is stringified on the
//! way in; the LOOKUP-shaped operations (`get` / `member` / `insert` / `remove`)
//! stringify the probe too, so they agree for ANY key type. Only the
//! ITERATION-shaped operations (`toList` / `keys` / `values` / `foldl` / `map`)
//! let a key leave the runtime again, and only there does the key type matter.
//!
//! So the axis that distinguishes behaviour is **key type × operation**, and
//! neither the `surface` axis (one value: `dict`) nor the `edge` axis
//! (`nominal`/`empty`/`boundary`/…) crosses it. This stratum does.
//!
//! # The third axis, which is where the second half of the bug lived
//!
//! The first fix (`1e43f366`) recovered the key type from OUTSIDE the key,
//! through two STATIC channels: the compiler's call-site routing
//! (`dict_typed_key_specialised`) and the callback's declared first parameter
//! (`dictKeyKindForFn`). Both read the static type — and the static type is
//! exactly what a key-polymorphic helper does not have:
//!
//! ```text
//! keysOf : Dict k v -> List k        →  func Main_keysOf(map[string]any) []any
//! ```
//!
//! `k` is erased to `any`; there is nothing to route on and nothing to sniff.
//! `Dict.keys` through that helper still panicked (`rt.AsInt: expected numeric
//! value, got string`), and it took a second fix (`d3edf51f`, a self-describing
//! two-byte kind tag on the key itself) to close it. That is why `access` is an
//! axis here and not a footnote: **direct** and **through a key-polymorphic
//! helper** were fixed by different mechanisms on different days, and a corpus
//! that only ever calls `Dict.keys` directly would have gone green between them.
//!
//! # The honesty constraint (`gen.rs` module docstring, v2 §4.4)
//!
//! Every expectation below is constructed by this generator before any compiler
//! runs, from two sources and no others:
//!
//! 1. **The key sets are chosen here**, in ascending order of the key type's own
//!    ordering, and each carries the rendered form the generator predicts. The
//!    `Int` set is `1, 2, 10` — deliberately a set whose LEXICAL order (`1, 10,
//!    2`) differs, so an implementation that sorts the stringified key is red on
//!    `keys` / `values` / `toList` / `foldl` rather than accidentally right.
//!    The `Char` set is `'B', 'a', '~'` (66, 97, 126) for the same reason: by
//!    code point `B < a < ~`, by the decimal spelling of the code point
//!    `126 < 66 < 97`.
//! 2. **Elm semantics**, which `AGENTS.md` names as the surface Sky copies: a
//!    `Dict` enumerates ASCENDING BY KEY, `union` is left-biased, `fromList`
//!    lets a later pair overwrite an earlier one, and `remove` of an absent key
//!    is a no-op.
//!
//! No expectation is read off the compiler's output. The values `"p"`, `"q"`,
//! `"r"` are positional markers chosen here: they are laid out so that the
//! value at the SMALLEST key is `"p"`, so an assertion on `Dict.values` is an
//! assertion about the key ORDER even though no key appears in it — which is
//! the third #174 symptom, the one nobody reported.

use super::axes::*;
use super::gen::Body;
use super::stdlib::{bo, i, ln, ls, ms, s, Check};

// ---------------------------------------------------------------------------
// The key types
// ---------------------------------------------------------------------------

/// One value of the `dict_key` axis: a key type, its sample keys in the order
/// a correct `Dict` must enumerate them, and how the case renders one to a
/// `String`.
pub struct KeyKind {
    pub slug: &'static str,
    /// The Sky type name, as written in `Dict <ty> String`.
    pub ty: &'static str,
    /// `(Sky literal, the String the generator predicts it renders to)`, in
    /// ASCENDING key order. Chosen here; never observed.
    pub keys: &'static [(&'static str, &'static str)],
    /// A key that is NOT in `keys` — the absent-probe and the insert target.
    pub absent: (&'static str, &'static str),
    /// The body of `renderK : <ty> -> String`.
    pub render: &'static str,
}

/// Every key type a `Dict` can decode back to (`rt.decodeTaggedDictKey`'s five
/// kinds). Composite keys — tuple, list, record, ADT — are deliberately absent:
/// `%v` is not injective for them, so they are a REJECTION
/// (`[E2008] UnsupportedDictKey`) rather than a value, and the reject matrix
/// owns that case (`corpus/repro/dict_composite_key.sky`).
pub const KEY_KINDS: &[KeyKind] = &[
    // The neutral. `String` keys decode to themselves and sort lexically, which
    // is byte-for-byte what a `map[string]V` did before any of this — which is
    // precisely why every `String`-keyed assertion passed through #174.
    KeyKind {
        slug: "string",
        ty: "String",
        keys: &[("\"ka\"", "ka"), ("\"kb\"", "kb"), ("\"kc\"", "kc")],
        absent: ("\"zz\"", "zz"),
        render: "k",
    },
    // Ascending 1 < 2 < 10; lexically "1" < "10" < "2". The set exists to tell
    // those two apart.
    KeyKind {
        slug: "int",
        ty: "Int",
        keys: &[("1", "1"), ("2", "2"), ("10", "10")],
        absent: ("77", "77"),
        render: "String.fromInt k",
    },
    // Ascending 0.5 < 2.5 < 10.5; lexically "0.5" < "10.5" < "2.5".
    KeyKind {
        slug: "float",
        ty: "Float",
        keys: &[("0.5", "0.5"), ("2.5", "2.5"), ("10.5", "10.5")],
        absent: ("9.25", "9.25"),
        render: "String.fromFloat k",
    },
    // By code point 'B'(66) < 'a'(97) < '~'(126); by the decimal spelling of the
    // code point "126" < "66" < "97". `Dict.toList` on a `Dict Char v` returned
    // code point 0 before #174 — the symptom this row is aimed at.
    KeyKind {
        slug: "char",
        ty: "Char",
        keys: &[("'B'", "B"), ("'a'", "a"), ("'~'", "~")],
        absent: ("'Z'", "Z"),
        render: "String.fromChar k",
    },
    // Two keys, because `Bool` has two inhabitants. False < True.
    KeyKind {
        slug: "bool",
        ty: "Bool",
        keys: &[("False", "F"), ("True", "T")],
        // Every `Bool` is a key, so there is no absent one. The absent-probe
        // items are dropped for this kind rather than faked — see `battery`.
        absent: ("", ""),
        render: "if k then\n        \"T\"\n\n    else\n        \"F\"",
    },
];

pub fn key_kind(slug: &str) -> &'static KeyKind {
    KEY_KINDS
        .iter()
        .find(|k| k.slug == slug)
        .unwrap_or_else(|| panic!("no dict key kind for slug {slug:?}"))
}

// ---------------------------------------------------------------------------
// The access shapes
// ---------------------------------------------------------------------------

/// How each `Dict` operation is REACHED. The three values are the three shapes
/// that were fixed by three different mechanisms.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Access {
    /// `Dict.keys d` — the compiler sees `Dict Int String` at the call site and
    /// can route (`dict_typed_key_specialised`).
    Direct,
    /// `keysOf d` where `keysOf : Dict k v -> List k`. The key is erased to
    /// `any`; neither static channel exists. THIS is where the panic lived
    /// after the first fix.
    PolyHelper,
    /// Two levels of polymorphic indirection, plus the helper passed as a
    /// FIRST-CLASS VALUE — the shape `d3edf51f` records that neither
    /// dictionary-passing nor monomorphisation would have closed.
    PolyValue,
}

impl Access {
    fn of(slug: &str) -> Access {
        match slug {
            "direct" => Access::Direct,
            "poly_helper" => Access::PolyHelper,
            "poly_value" => Access::PolyValue,
            other => panic!("unknown dict access shape {other:?}"),
        }
    }

    /// The suffix a polymorphic helper carries at this access shape: `poly_value`
    /// routes through a SECOND helper that just calls the first.
    fn hop(self) -> &'static str {
        match self {
            Access::PolyValue => "2",
            _ => "",
        }
    }
}

// ---------------------------------------------------------------------------
// The call spellings
// ---------------------------------------------------------------------------

/// One operation, spelled for the access shape under test.
///
/// The three spellings compute THE SAME VALUE by construction — that is the
/// property under test. `access` must not change what a program means; before
/// `d3edf51f` it did, and the program panicked.
struct Calls {
    access: Access,
}

impl Calls {
    fn keys(&self, d: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.keys {d}"),
            Access::PolyHelper => format!("keysOf {d}"),
            Access::PolyValue => format!("applyKeys keysOf2 {d}"),
        }
    }
    fn values(&self, d: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.values {d}"),
            a => format!("valuesOf{} {d}", a.hop()),
        }
    }
    fn to_list(&self, d: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.toList {d}"),
            Access::PolyHelper => format!("pairsOf {d}"),
            Access::PolyValue => format!("applyPairs pairsOf2 {d}"),
        }
    }
    fn foldl(&self, f: &str, z: &str, d: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.foldl ({f}) {z} {d}"),
            Access::PolyHelper => format!("foldOf ({f}) {z} {d}"),
            Access::PolyValue => format!("applyFold foldOf2 ({f}) {z} {d}"),
        }
    }
    fn map(&self, f: &str, d: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.map ({f}) {d}"),
            a => format!("mapOf{} ({f}) {d}", a.hop()),
        }
    }
    fn get(&self, k: &str, d: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.get {k} {d}"),
            a => format!("getOf{} {k} {d}", a.hop()),
        }
    }
    fn insert(&self, k: &str, v: &str, d: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.insert {k} {v} {d}"),
            a => format!("insertOf{} {k} {v} {d}", a.hop()),
        }
    }
    fn member(&self, k: &str, d: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.member {k} {d}"),
            a => format!("memberOf{} {k} {d}", a.hop()),
        }
    }
    fn remove(&self, k: &str, d: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.remove {k} {d}"),
            a => format!("removeOf{} {k} {d}", a.hop()),
        }
    }
    fn size(&self, d: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.size {d}"),
            a => format!("sizeOf{} {d}", a.hop()),
        }
    }
    fn union(&self, l: &str, r: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.union {l} {r}"),
            a => format!("unionOf{} {l} {r}", a.hop()),
        }
    }
    fn from_list(&self, pairs: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.fromList {pairs}"),
            a => format!("fromPairs{} {pairs}", a.hop()),
        }
    }
    fn is_empty(&self, d: &str) -> String {
        match self.access {
            Access::Direct => format!("Dict.isEmpty {d}"),
            a => format!("isEmptyOf{} {d}", a.hop()),
        }
    }
}

// ---------------------------------------------------------------------------
// The battery
// ---------------------------------------------------------------------------

/// The value bound to the SMALLEST key. Chosen here so an assertion on
/// `Dict.values` — which mentions no key at all — is nevertheless an assertion
/// about key ORDER.
const VALS: &[&str] = &["p", "q", "r"];
/// The value `insert` writes. Chosen here.
const INSERTED: &str = "z";

/// A Sky list literal of `(key, value)` pairs, in an order that is NOT the
/// enumeration order — so a `Dict` that merely preserved insertion order would
/// be red on every ordered item below.
fn shuffled_pairs(kk: &KeyKind) -> String {
    let n = kk.keys.len();
    // Rotate by one: the largest key is written first.
    let order: Vec<usize> = (0..n).map(|i| (i + n - 1) % n).collect();
    let items: Vec<String> = order
        .iter()
        .map(|&i| format!("( {}, \"{}\" )", kk.keys[i].0, VALS[i]))
        .collect();
    format!("[ {} ]", items.join(", "))
}

/// The battery at one `(dict_key, dict_access)` point.
///
/// Every item names the operation it covers. Twelve operations plus the
/// key-order assertions; the three `access` shapes compute identical values by
/// construction, so the expectation is the same string at all three — which is
/// exactly the claim under test.
fn battery(key: &str, access: &str) -> Vec<Check> {
    let kk = key_kind(key);
    let c = Calls {
        access: Access::of(access),
    };
    let n = kk.keys.len();
    let asc_keys: Vec<&str> = kk.keys.iter().map(|(_, r)| *r).collect();
    let asc_vals: Vec<&str> = (0..n).map(|i| VALS[i]).collect();
    let (k0, r0) = kk.keys[0];
    let (k1, r1) = kk.keys[1];
    let d = "d3";

    let mut out = vec![
        // ---- the iteration-shaped operations: where #174 lived -------------
        //
        // `keys` — symptom 1. Pre-fix this handed back the STRINGIFIED key, and
        // the caller's `rt.AsListT[rt.T2[rune,int]]` coerced "97" to a rune and
        // got 0.
        ls(
            &["Dict.keys"],
            &format!("List.map renderK ({})", c.keys(d)),
            &asc_keys.join(","),
        ),
        // `toList` — symptom 1, with the value alongside, so a key that decoded
        // to the wrong thing cannot be masked by a right-looking value.
        ls(
            &["Dict.toList"],
            &format!(
                "List.map (\\pr -> renderK (fst pr) ++ \"=\" ++ snd pr) ({})",
                c.to_list(d)
            ),
            &asc_keys
                .iter()
                .zip(asc_vals.iter())
                .map(|(k, v)| format!("{k}={v}"))
                .collect::<Vec<_>>()
                .join(","),
        ),
        // `values` — symptom 3, the one nobody reported. No key appears in this
        // assertion; the ORDER is the assertion. Lexical key order on the `int`
        // and `float` rows produces a different string.
        ls(&["Dict.values"], &c.values(d), &asc_vals.join(",")),
        // `foldl` — symptom 2, the PANIC. The callback's first parameter is the
        // key, and `renderK` is typed on the key type, so a `string` arriving
        // here is `rt.skyCallDirect: argument 0 type mismatch`.
        s(
            &["Dict.foldl"],
            &c.foldl("\\k _ acc -> acc ++ renderK k", "\"\"", d),
            &asc_keys.join(""),
        ),
        // `map` — the unreported sibling of `foldl`: same callback shape, same
        // failure. Read back through `values` so the assertion covers the
        // re-keying too.
        ls(
            &["Dict.map", "Dict.values"],
            &c.values(&format!("({})", c.map("\\k v -> renderK k ++ v", d))),
            &asc_keys
                .iter()
                .zip(asc_vals.iter())
                .map(|(k, v)| format!("{k}{v}"))
                .collect::<Vec<_>>()
                .join(","),
        ),
        // ---- the lookup-shaped operations ----------------------------------
        //
        // These agreed for every key type even while iteration was broken —
        // they stringify the probe too. They are here so the crossing is
        // complete, and because `d3edf51f` re-routed them through
        // `dictProbeKey`: `Dict.get 10` must still find a bare "10" written by
        // an FFI / Std.Db map, and `Dict.insert 10` must REPLACE it rather than
        // add a second, logically-equal key.
        i(&["Dict.size"], &c.size(d), n as i64),
        ms(&["Dict.get"], &c.get(k1, d), Some(VALS[1])),
        bo(&["Dict.member"], &c.member(k0, d), true),
        ms(
            &["Dict.insert", "Dict.get"],
            &c.get(k0, &format!("({})", c.insert(k0, &format!("\"{INSERTED}\""), d))),
            Some(INSERTED),
        ),
        i(
            &["Dict.remove", "Dict.size"],
            &c.size(&format!("({})", c.remove(k0, d))),
            n as i64 - 1,
        ),
        bo(
            &["Dict.remove", "Dict.member"],
            &c.member(k0, &format!("({})", c.remove(k0, d))),
            false,
        ),
        // ---- construction + combination ------------------------------------
        //
        // `fromList`: a later pair overwrites an earlier one (Elm semantics).
        // The stringify-on-the-way-in shape makes this key-type-independent,
        // which is the point: it must STAY so once keys carry a kind tag.
        ms(
            &["Dict.fromList", "Dict.get"],
            &c.get(
                k0,
                &format!(
                    "({})",
                    c.from_list(&format!("[ ( {k0}, \"a\" ), ( {k0}, \"b\" ) ]"))
                ),
            ),
            Some("b"),
        ),
        i(
            &["Dict.fromList", "Dict.size"],
            &c.size(&format!(
                "({})",
                c.from_list(&format!("[ ( {k0}, \"a\" ), ( {k0}, \"b\" ) ]"))
            )),
            1,
        ),
        // `union` is LEFT-biased, and the union's enumeration must still be
        // ascending by the DECODED key — so the tag survives a map this
        // operation rebuilt.
        ms(
            &["Dict.union", "Dict.get"],
            &c.get(
                k0,
                &format!(
                    "({})",
                    c.union(
                        &format!("({})", c.from_list(&format!("[ ( {k0}, \"L\" ) ]"))),
                        &format!("({})", c.from_list(&format!("[ ( {k0}, \"R\" ), ( {k1}, \"S\" ) ]")))
                    )
                ),
            ),
            Some("L"),
        ),
        ls(
            &["Dict.union", "Dict.keys"],
            &format!(
                "List.map renderK ({})",
                c.keys(&format!(
                    "({})",
                    c.union(
                        &format!("({})", c.from_list(&format!("[ ( {k1}, \"L\" ) ]"))),
                        &format!("({})", c.from_list(&format!("[ ( {k0}, \"R\" ) ]")))
                    )
                ))
            ),
            &format!("{r0},{r1}"),
        ),
        // ---- the empty edge, at this key type ------------------------------
        bo(&["Dict.isEmpty", "Dict.empty"], &c.is_empty("emptyD"), true),
        ln(&["Dict.keys", "Dict.empty"], &c.keys("emptyD"), 0),
        i(
            &["Dict.foldl", "Dict.empty"],
            &c.foldl("\\_ _ acc -> acc + 1", "0", "emptyD"),
            0,
        ),
    ];

    // `Bool` has no absent key — every inhabitant is present in a two-entry
    // dict. Faking one would assert nothing, so the probe items are dropped
    // rather than padded (the same rule `axes::admissible` applies to a
    // surface with no unicode edge).
    if !kk.absent.0.is_empty() {
        let (kz, rz) = kk.absent;
        out.push(bo(&["Dict.member"], &c.member(kz, d), false));
        out.push(ms(&["Dict.get"], &c.get(kz, d), None));
        // `remove` of an absent key is a no-op, not an error.
        out.push(i(
            &["Dict.remove"],
            &c.size(&format!("({})", c.remove(kz, d))),
            n as i64,
        ));
        // Inserting a NEW key must land it in the right ORDER, which is the
        // iteration path again — reached through a map this operation built.
        let mut with_z: Vec<&str> = asc_keys.clone();
        with_z.push(rz);
        with_z.sort_by_key(|r| {
            // The generator's own ordering, taken from the declared ascending
            // list plus where the absent key belongs. Computed from the key
            // literals' declared order, never from the compiler.
            //
            // Ranks are DOUBLED so the absent key can be placed strictly
            // between two declared keys (`2 * rank - 1`) rather than tying with
            // one of them — a tie plus a stable sort put `'Z'`(90) after
            // `'a'`(97) and `9.25` after `10.5`, which is how the first run of
            // this stratum went red against a compiler that was right.
            match kk.keys.iter().position(|(_, kr)| kr == r) {
                Some(i) => 2 * i + 1,
                None => 2 * absent_rank(kk),
            }
        });
        out.push(ls(
            &["Dict.insert", "Dict.keys"],
            &format!(
                "List.map renderK ({})",
                c.keys(&format!(
                    "({})",
                    c.insert(kz, &format!("\"{INSERTED}\""), d)
                ))
            ),
            &with_z.join(","),
        ));
    }

    out
}

/// The index of the declared key the absent key sorts IMMEDIATELY BEFORE
/// (`keys.len()` when it sorts last).
///
/// Stated per key kind rather than derived from a string compare, because the
/// ordering under test is the KEY TYPE's ordering, and deriving it from the
/// rendered spelling is exactly the lexical-vs-numeric confusion this stratum
/// exists to catch. `77` sits after `10`; `9.25` sits between `2.5` and `10.5`;
/// `'Z'`(90) sits between `'B'`(66) and `'a'`(97); `"zz"` sorts last.
fn absent_rank(kk: &KeyKind) -> usize {
    match kk.slug {
        "string" => 3, // "zz" > "kc"          → last
        "int" => 3,    // 77 > 10              → last
        "float" => 2,  // 2.5 < 9.25 < 10.5    → before index 2
        "char" => 1,   // 'B' < 'Z' < 'a'      → before index 1
        other => panic!("no absent-key rank declared for {other:?}"),
    }
}

// ---------------------------------------------------------------------------
// The polymorphic helpers
// ---------------------------------------------------------------------------

/// The key-polymorphic helper declarations an access shape needs.
///
/// Each is annotated `Dict k v -> …` deliberately: the annotation is what
/// ERASES the key to `any` in the lowering, which is the condition the second
/// #174 fix had to survive. An unannotated helper would be inlined at a
/// concrete type and would test nothing this stratum is about.
fn helpers(access: Access) -> String {
    if access == Access::Direct {
        return String::new();
    }
    let mut s = String::from(
        "keysOf : Dict k v -> List k\n\
         keysOf d =\n    Dict.keys d\n\n\n\
         valuesOf : Dict k v -> List v\n\
         valuesOf d =\n    Dict.values d\n\n\n\
         pairsOf : Dict k v -> List ( k, v )\n\
         pairsOf d =\n    Dict.toList d\n\n\n\
         foldOf : (k -> v -> a -> a) -> a -> Dict k v -> a\n\
         foldOf f z d =\n    Dict.foldl f z d\n\n\n\
         mapOf : (k -> v -> b) -> Dict k v -> Dict k b\n\
         mapOf f d =\n    Dict.map f d\n\n\n\
         getOf : k -> Dict k v -> Maybe v\n\
         getOf k d =\n    Dict.get k d\n\n\n\
         memberOf : k -> Dict k v -> Bool\n\
         memberOf k d =\n    Dict.member k d\n\n\n\
         insertOf : k -> v -> Dict k v -> Dict k v\n\
         insertOf k v d =\n    Dict.insert k v d\n\n\n\
         removeOf : k -> Dict k v -> Dict k v\n\
         removeOf k d =\n    Dict.remove k d\n\n\n\
         sizeOf : Dict k v -> Int\n\
         sizeOf d =\n    Dict.size d\n\n\n\
         unionOf : Dict k v -> Dict k v -> Dict k v\n\
         unionOf l r =\n    Dict.union l r\n\n\n\
         fromPairs : List ( k, v ) -> Dict k v\n\
         fromPairs ps =\n    Dict.fromList ps\n\n\n\
         isEmptyOf : Dict k v -> Bool\n\
         isEmptyOf d =\n    Dict.isEmpty d\n\n\n",
    );
    if access == Access::PolyValue {
        // A SECOND polymorphic hop, then the first-class application. Both
        // shapes are named in `d3edf51f` as the ones the static channels could
        // not reach: monomorphisation closes neither a two-hop erasure nor a
        // helper used as a value.
        s.push_str(
            "keysOf2 : Dict k v -> List k\n\
             keysOf2 d =\n    keysOf d\n\n\n\
             valuesOf2 : Dict k v -> List v\n\
             valuesOf2 d =\n    valuesOf d\n\n\n\
             pairsOf2 : Dict k v -> List ( k, v )\n\
             pairsOf2 d =\n    pairsOf d\n\n\n\
             foldOf2 : (k -> v -> a -> a) -> a -> Dict k v -> a\n\
             foldOf2 f z d =\n    foldOf f z d\n\n\n\
             mapOf2 : (k -> v -> b) -> Dict k v -> Dict k b\n\
             mapOf2 f d =\n    mapOf f d\n\n\n\
             getOf2 : k -> Dict k v -> Maybe v\n\
             getOf2 k d =\n    getOf k d\n\n\n\
             memberOf2 : k -> Dict k v -> Bool\n\
             memberOf2 k d =\n    memberOf k d\n\n\n\
             insertOf2 : k -> v -> Dict k v -> Dict k v\n\
             insertOf2 k v d =\n    insertOf k v d\n\n\n\
             removeOf2 : k -> Dict k v -> Dict k v\n\
             removeOf2 k d =\n    removeOf k d\n\n\n\
             sizeOf2 : Dict k v -> Int\n\
             sizeOf2 d =\n    sizeOf d\n\n\n\
             unionOf2 : Dict k v -> Dict k v -> Dict k v\n\
             unionOf2 l r =\n    unionOf l r\n\n\n\
             fromPairs2 : List ( k, v ) -> Dict k v\n\
             fromPairs2 ps =\n    fromPairs ps\n\n\n\
             isEmptyOf2 : Dict k v -> Bool\n\
             isEmptyOf2 d =\n    isEmptyOf d\n\n\n\
             applyKeys : (Dict k v -> List k) -> Dict k v -> List k\n\
             applyKeys f d =\n    f d\n\n\n\
             applyPairs : (Dict k v -> List ( k, v )) -> Dict k v -> List ( k, v )\n\
             applyPairs f d =\n    f d\n\n\n\
             applyFold : ((k -> v -> a -> a) -> a -> Dict k v -> a) -> (k -> v -> a -> a) -> a -> Dict k v -> a\n\
             applyFold g f z d =\n    g f z d\n\n\n",
        );
    }
    s
}

// ---------------------------------------------------------------------------
// Assembling the case
// ---------------------------------------------------------------------------

/// Build the `dict_key_crossing` case at `(dict_key, dict_access)`.
pub fn case(a: &Assignment) -> (Body, String) {
    let key = a.get(DICT_KEY);
    let access = a.get(DICT_ACCESS);
    let kk = key_kind(key);
    let checks = battery(key, access);
    assert!(
        !checks.is_empty(),
        "dict_key_crossing: {key}/{access} has no battery — a case that asserts \
         nothing is exactly the vacuity this corpus exists to prevent"
    );

    let imports = "import Sky.Core.Dict\n".to_string();

    let mut decls = format!(
        "renderK : {ty} -> String\nrenderK k =\n    {render}\n\n\n\
         d3 : Dict {ty} String\nd3 =\n    Dict.fromList {pairs}\n\n\n\
         emptyD : Dict {ty} String\nemptyD =\n    Dict.empty\n\n\n",
        ty = kk.ty,
        render = kk.render,
        pairs = shuffled_pairs(kk),
    );
    decls.push_str(&helpers(Access::of(access)));
    for (n, c) in checks.iter().enumerate() {
        decls.push_str(&format!("s{n} : String\ns{n} =\n    {}\n\n\n", c.body()));
    }

    let items: Vec<String> = (0..checks.len()).map(|n| format!("s{n}")).collect();
    let check = format!(
        "String.join \"|\"\n        [ {}\n        ]",
        items.join("\n        , ")
    );
    let expected: Vec<&str> = checks.iter().map(|c| c.expect()).collect();

    (
        Body {
            imports,
            decls,
            check,
        },
        expected.join("|"),
    )
}

/// How many assertions the case at `(dict_key, dict_access)` carries.
///
/// Exposed so `gen.rs`'s render test can check that the expectation has exactly
/// as many `|`-joined items as the battery has assertions — without it a case
/// that silently lost an item would still pass, by comparing a shorter string to
/// a shorter string.
pub fn battery_len(key: &str, access: &str) -> usize {
    battery(key, access).len()
}

/// `(module, symbol)` pairs this stratum asserts something about — the coverage
/// claim, derived from the per-item `covers` tags exactly as Family S's
/// `stdlib_edge` derives its own.
pub fn covered_symbols() -> std::collections::BTreeSet<(String, String)> {
    let mut out = std::collections::BTreeSet::new();
    for k in KEY_KINDS {
        for acc in DICT_ACCESS.values {
            for c in battery(k.slug, acc) {
                for tag in c.covers() {
                    if let Some((_, sym)) = tag.split_once('.') {
                        out.insert(("Sky.Core.Dict".to_string(), sym.to_string()));
                    }
                }
            }
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The axis and the table agree — a slug in one and not the other would
    /// silently drop a key type from the crossing.
    #[test]
    fn key_kind_slugs_match_the_axis_exactly() {
        let table: std::collections::BTreeSet<&str> = KEY_KINDS.iter().map(|k| k.slug).collect();
        let axis: std::collections::BTreeSet<&str> = DICT_KEY.values.iter().copied().collect();
        assert_eq!(table, axis, "the `dict_key` axis and KEY_KINDS disagree");
    }

    /// **The shape claim, stated as a test.** Every one of the five iteration
    /// operations #174 broke must be asserted at every key type AND at every
    /// access shape. This is the crossing the old `dict_battery` did not have,
    /// and it is what makes "the corpus covers Dict" mean something.
    #[test]
    fn every_iteration_op_is_asserted_at_every_key_type_and_access_shape() {
        for k in KEY_KINDS {
            for acc in DICT_ACCESS.values {
                let tags: std::collections::BTreeSet<&str> = battery(k.slug, acc)
                    .iter()
                    .flat_map(|c| c.covers().iter().copied())
                    .collect();
                for op in [
                    "Dict.keys",
                    "Dict.values",
                    "Dict.toList",
                    "Dict.foldl",
                    "Dict.map",
                ] {
                    assert!(
                        tags.contains(op),
                        "{}/{acc}: the crossing is missing {op}, which is one of \
                         the five operations #174 broke",
                        k.slug
                    );
                }
            }
        }
    }

    /// The lookup-shaped operations are here too. They are the ones that AGREED
    /// for every key type while iteration was broken, so leaving them out would
    /// make the stratum's "× operation" claim false.
    #[test]
    fn the_lookup_shaped_ops_are_in_the_crossing_too() {
        for k in KEY_KINDS {
            for acc in DICT_ACCESS.values {
                let tags: std::collections::BTreeSet<&str> = battery(k.slug, acc)
                    .iter()
                    .flat_map(|c| c.covers().iter().copied())
                    .collect();
                for op in [
                    "Dict.get",
                    "Dict.insert",
                    "Dict.member",
                    "Dict.remove",
                    "Dict.size",
                    "Dict.union",
                    "Dict.fromList",
                    "Dict.empty",
                    "Dict.isEmpty",
                ] {
                    assert!(tags.contains(op), "{}/{acc}: missing {op}", k.slug);
                }
            }
        }
    }

    /// **The access axis is not inert on the SOURCE**, and it must be: if all
    /// three shapes produced the same program there would be nothing to cross.
    /// (Their VALUES are deliberately identical — that is the property under
    /// test — so the axis is witnessed by the emitted Go, not by the value.)
    #[test]
    fn the_access_axis_produces_three_different_programs() {
        for k in KEY_KINDS {
            let mut seen = std::collections::BTreeSet::new();
            for acc in DICT_ACCESS.values {
                let a = Assignment::new()
                    .with(DICT_KEY, k.slug)
                    .with(DICT_ACCESS, acc);
                let (body, _) = case(&a);
                assert!(
                    seen.insert(body.decls.clone()),
                    "{}/{acc}: this access shape produces the same source as another",
                    k.slug
                );
            }
        }
    }

    /// **The key axis is not inert on the VALUE.** The ordered assertions must
    /// differ between key types, or the crossing would be spending budget to
    /// assert the same string five times.
    #[test]
    fn the_key_axis_changes_the_expected_value() {
        let mut seen = std::collections::BTreeSet::new();
        for k in KEY_KINDS {
            let a = Assignment::new()
                .with(DICT_KEY, k.slug)
                .with(DICT_ACCESS, "direct");
            let (_, expected) = case(&a);
            assert!(
                seen.insert(expected.clone()),
                "key type {} expects the same string as another key type",
                k.slug
            );
        }
    }

    /// The `int`, `float` and `char` key sets must have a LEXICAL order that
    /// differs from their declared ascending order — otherwise a runtime that
    /// sorts the stringified key would pass, and #174's third symptom
    /// (`Dict.values` on keys 1,2,9,10 came back a,j,b,i) would be invisible.
    #[test]
    fn the_numeric_key_sets_distinguish_lexical_from_ordinal() {
        for slug in ["int", "float", "char"] {
            let kk = key_kind(slug);
            // What the runtime would enumerate if it sorted the STRINGIFIED
            // key. For `char` the stringified key is the decimal code point,
            // which is what `rt` writes.
            let mut lexical: Vec<String> = kk
                .keys
                .iter()
                .map(|(lit, _)| match slug {
                    "char" => lit.trim_matches('\'').chars().next().unwrap().to_string(),
                    _ => lit.to_string(),
                })
                .collect();
            if slug == "char" {
                lexical = kk
                    .keys
                    .iter()
                    .map(|(lit, _)| {
                        (lit.trim_matches('\'').chars().next().unwrap() as u32).to_string()
                    })
                    .collect();
            }
            let ordinal = lexical.clone();
            lexical.sort();
            assert_ne!(
                lexical, ordinal,
                "key set for {slug} sorts the same lexically as ordinally — it \
                 cannot distinguish the two, so it cannot see #174's ordering symptom"
            );
        }
    }

    /// No expectation may contain the join separator or lead/trail whitespace —
    /// the same two invariants `stdlib.rs` enforces, for the same reasons (an
    /// embedded `|` shifts every later item's diff position; the runner
    /// compares `stdout.trim()`).
    #[test]
    fn expectations_are_well_formed() {
        for k in KEY_KINDS {
            for acc in DICT_ACCESS.values {
                for c in battery(k.slug, acc) {
                    assert!(
                        !c.expect().contains('|'),
                        "{}/{acc}: {:?} contains the join separator",
                        k.slug,
                        c.expect()
                    );
                    assert_eq!(
                        c.expect().trim(),
                        c.expect(),
                        "{}/{acc}: {:?} has leading/trailing whitespace",
                        k.slug,
                        c.expect()
                    );
                }
            }
        }
    }
}
