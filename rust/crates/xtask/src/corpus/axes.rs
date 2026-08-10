//! The axis space — mined from this repository's actual defect history.
//!
//! Every shipped defect listed in `.claude/AUTONOMOUS_GOAL.md` was ordinary
//! usage in a combination nobody had tried: the simple case compiles clean, one
//! axis changes, and it breaks. So the axes are not invented taxonomy — each one
//! is the dimension along which a real bug moved.
//!
//! | Axis | Introduced by | Values |
//! |---|---|---|
//! | `position` | #166 — record update inside a tuple dropped un-updated fields | where the expression sits |
//! | `carrier` | #171 — row-poly record update through `foldl`/`foldr` | the higher-order context it is reached through |
//! | `erasure` | #170/#172 — destructure on an *erased* subject | how the subject's type is erased |
//! | `nesting` | #173 — `Dict k (List Record)` | type constructor nesting |
//! | `import_shape` | #164 — same-named alias / import-alias collision | how a name enters scope |
//! | `collision` | `goty.rs` record-fieldset collision | same field NAMES, different field TYPES |
//! | `annotation` | #166's annotated-vs-unannotated split | whether the signature is written |
//! | `row` | #171 — closed vs open (row-polymorphic) records | record openness |
//!
//! A **stratum** is a named subset of the space that gets FULL cross, because
//! history says that triple produces bugs. Everything else is covered by a
//! pairwise covering array (`pairwise.rs`), because exhaustive cross over the
//! whole space is ~42,000 cases of mostly-redundant work.

use std::collections::BTreeMap;
use std::fmt;

/// One axis: a name and its closed value set.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct Axis {
    pub name: &'static str,
    pub values: &'static [&'static str],
}

impl Axis {
    pub const fn new(name: &'static str, values: &'static [&'static str]) -> Axis {
        Axis { name, values }
    }
}

// ---------------------------------------------------------------------------
// The axes
// ---------------------------------------------------------------------------

/// Where the expression under test sits. #166 moved along exactly this axis:
/// `{ r | a = 7 }` was correct bare and dropped fields inside a tuple.
pub const POSITION: Axis = Axis::new(
    "position",
    &[
        "bare",
        "in_tuple",
        "in_list",
        "in_record_field",
        "in_lambda",
        "in_let",
    ],
);

/// The higher-order context the expression is reached through. #171 moved along
/// this axis: correct directly, dropped fields through `foldl`/`foldr`.
pub const CARRIER: Axis = Axis::new(
    "carrier",
    &["direct", "via_foldl", "via_foldr", "via_map"],
);

/// Whether the enclosing definition carries a type annotation. #166's fix landed
/// for annotated params first; the unannotated edge is where it lingered.
pub const ANNOTATION: Axis = Axis::new("annotation", &["annotated", "unannotated"]);

/// Record openness. #171 was specifically a ROW-POLYMORPHIC update.
pub const ROW: Axis = Axis::new("row", &["closed", "open_subset"]);

/// How the subject's type is erased before it is destructured. #170/#172 moved
/// along this axis — the same destructure is fine on a typed subject and breaks
/// on an `any`-typed one.
pub const ERASURE: Axis = Axis::new(
    "erasure",
    &[
        "direct",
        "via_foldr",
        "via_let",
        "via_fst_snd",
        "via_tuple_destructure",
    ],
);

/// Type-constructor nesting. #173 was three defects in `Dict k (List Record)`.
pub const OUTER: Axis = Axis::new("outer", &["dict", "list", "maybe", "result"]);
pub const INNER: Axis = Axis::new("inner", &["none", "list", "maybe"]);
pub const ELEM: Axis = Axis::new("elem", &["int", "string", "record"]);

/// How a name enters scope. #164 was an import-alias collision — and the
/// qualifier heuristic that "fixed" it regressed a real app because the alias
/// was not the last path segment.
pub const IMPORT_SHAPE: Axis = Axis::new(
    "import_shape",
    &[
        "plain",
        "aliased",
        "alias_not_last_segment",
        "exposing_list",
        "exposing_all",
    ],
);

/// Name-collision shape. The `goty.rs` fieldset collision needed REAL stdlib
/// names in scope, which is why the generator draws from the real symbol table.
pub const COLLISION: Axis = Axis::new(
    "collision",
    &["none", "same_names_diff_types", "subset", "shadows_stdlib"],
);

/// **How the record is CONSTRUCTED.**
///
/// Added after a probe found a live `CoerceFailure` this corpus had missed. The
/// original `fieldset_collision` stratum built its records as inline literals in
/// a `let`, and every case passed. Building the *same* record inside an
/// annotated constructor function — `mk : String -> String -> Kv` — panics at
/// runtime while `sky build` reports "Types OK".
///
/// The axis exists because the defect lives in the CONSTRUCTION SITE, not in the
/// read: the constructor's parameters are coerced to the colliding nominal
/// type's field types. An inline literal carries its types with it and never
/// takes that path.
///
/// This is the mandate's thesis demonstrated on itself — "the simple case
/// compiles clean, one axis changes, and it breaks" — including the part where
/// the axis nobody thought of is the one that finds the bug.
pub const CONSTRUCTION: Axis = Axis::new("construction", &["inline", "via_ctor_fn"]);

/// Which record the case's fieldset collides WITH.
///
/// v2 §3.1: *"a generated hostile module graph that collides against fictional
/// stdlib names cannot reproduce #164 or the fieldset collision, both of which
/// required real stdlib names in scope."* That was right, and the first cut of
/// this generator ignored it — colliding two LOCAL aliases against each other
/// found nothing, while colliding against the real
/// `Std.Analytics.EventProp = { key : String, value : PropValue }` reproduces a
/// live runtime panic.
///
/// `stdlib_eventprop` is not an invented name: it is the field-name set of a
/// record that is in scope in every compilation, taken from the real stdlib.
pub const COLLIDER: Axis = Axis::new("collider", &["local", "stdlib_eventprop"]);

// ---------------------------------------------------------------------------
// Assignments
// ---------------------------------------------------------------------------

/// A point in the axis space: axis name -> chosen value. `BTreeMap` so the
/// rendering (and therefore every generated id) is deterministic.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord, Default)]
pub struct Assignment(pub BTreeMap<&'static str, &'static str>);

impl Assignment {
    pub fn new() -> Assignment {
        Assignment(BTreeMap::new())
    }

    pub fn with(mut self, axis: Axis, value: &'static str) -> Assignment {
        debug_assert!(
            axis.values.contains(&value),
            "value {value:?} is not in axis {:?}",
            axis.name
        );
        self.0.insert(axis.name, value);
        self
    }

    pub fn get(&self, axis: Axis) -> &'static str {
        self.0.get(axis.name).copied().unwrap_or_else(|| {
            panic!(
                "assignment has no value for axis {:?} (have: {:?})",
                axis.name,
                self.0.keys().collect::<Vec<_>>()
            )
        })
    }

    /// The axis-value pairs, for the covering-array coverage report.
    pub fn pairs(&self) -> Vec<((&'static str, &'static str), (&'static str, &'static str))> {
        let items: Vec<_> = self.0.iter().map(|(k, v)| (*k, *v)).collect();
        let mut out = Vec::new();
        for i in 0..items.len() {
            for j in (i + 1)..items.len() {
                out.push((items[i], items[j]));
            }
        }
        out
    }

    /// The **distance-1 neighbourhood**: every assignment differing from this one
    /// in exactly one axis. This is the mechanical definition of "the NEIGHBOURS
    /// of a past issue become cases too" — the mandate's rule, made computable.
    pub fn neighbourhood(&self, axes: &[Axis]) -> Vec<Assignment> {
        let mut out = Vec::new();
        for axis in axes {
            if !self.0.contains_key(axis.name) {
                continue;
            }
            let current = self.get(*axis);
            for &v in axis.values {
                if v == current {
                    continue;
                }
                out.push(self.clone().with(*axis, v));
            }
        }
        out.sort();
        out.dedup();
        out
    }

    /// A stable, human-legible slug for the case id.
    pub fn slug(&self) -> String {
        self.0
            .values()
            .copied()
            .collect::<Vec<_>>()
            .join("-")
    }
}

impl fmt::Display for Assignment {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let s: Vec<String> = self.0.iter().map(|(k, v)| format!("{k}={v}")).collect();
        write!(f, "{}", s.join(" "))
    }
}

// ---------------------------------------------------------------------------
// Strata
// ---------------------------------------------------------------------------

/// A named region of the axis space that gets FULL cross, because this
/// repository's history says this combination produces bugs.
///
/// `coordinate` is the issue the stratum was mined from. It is what makes the
/// distance-1 neighbourhood expansion meaningful: the pinned coordinate is the
/// bug's own axis assignment, and its neighbours are the combinations that were
/// never tried.
#[derive(Clone, Copy, Debug)]
pub struct Stratum {
    pub name: &'static str,
    pub axes: &'static [Axis],
    pub coordinate: Option<&'static str>,
    /// Forbidden from batching (v2 §3.2) — one compilation unit each.
    pub isolated: bool,
}

/// Every stratum. `isolated: true` marks the four families v2 §3.2 forbids from
/// batching, because their verdict depends on whole-compilation state:
/// `record_fieldsets` is built over the whole compilation
/// (`lower/src/lower.rs:246-266`) and the TEA-Model heuristic picks the first
/// `(Record, Cmd _)` candidate in `(module, name)` order (`:267-278`), which
/// `goty.rs:186-196` then resolves every strict-subset record to.
pub const STRATA: &[Stratum] = &[
    Stratum {
        name: "record_update",
        axes: &[POSITION, ANNOTATION, ROW, CARRIER],
        coordinate: Some("anzellai/sky#166"),
        isolated: false,
    },
    Stratum {
        name: "destructure",
        axes: &[ERASURE, POSITION],
        coordinate: Some("anzellai/sky#170"),
        isolated: false,
    },
    Stratum {
        name: "type_nesting",
        axes: &[OUTER, INNER, ELEM],
        coordinate: Some("anzellai/sky#173"),
        isolated: false,
    },
    Stratum {
        name: "import_shape",
        axes: &[IMPORT_SHAPE, COLLISION],
        coordinate: Some("anzellai/sky#164"),
        isolated: true,
    },
    Stratum {
        name: "fieldset_collision",
        axes: &[COLLISION, ERASURE],
        coordinate: Some("goty.rs record-fieldset collision"),
        isolated: true,
    },
    Stratum {
        name: "fieldset_ctor",
        axes: &[CONSTRUCTION, COLLIDER],
        coordinate: Some("goty.rs fieldset collision — construction site"),
        isolated: true,
    },
];

/// The pinned coordinate for each stratum — the axis assignment of the ORIGINAL
/// defect. Distance-1 expansion runs from these points.
pub fn pinned_coordinate(stratum: &str) -> Option<Assignment> {
    match stratum {
        // #166: `{ r | a = 7 }` inside a TUPLE, on an ANNOTATED function, over a
        // CLOSED record, reached DIRECTly.
        "record_update" => Some(
            Assignment::new()
                .with(POSITION, "in_tuple")
                .with(ANNOTATION, "annotated")
                .with(ROW, "closed")
                .with(CARRIER, "direct"),
        ),
        // #170/#172: destructure on an erased subject, reached through foldr.
        "destructure" => Some(
            Assignment::new()
                .with(ERASURE, "via_foldr")
                .with(POSITION, "in_let"),
        ),
        // #173: Dict k (List Record).
        "type_nesting" => Some(
            Assignment::new()
                .with(OUTER, "dict")
                .with(INNER, "list")
                .with(ELEM, "record"),
        ),
        // #164: an alias that is not the last path segment, colliding.
        "import_shape" => Some(
            Assignment::new()
                .with(IMPORT_SHAPE, "alias_not_last_segment")
                .with(COLLISION, "shadows_stdlib"),
        ),
        // goty: same field names, different field types, reached via fst/snd.
        "fieldset_collision" => Some(
            Assignment::new()
                .with(COLLISION, "same_names_diff_types")
                .with(ERASURE, "via_fst_snd"),
        ),
        // The coordinate of the LIVE defect this corpus found: a `{ key, value }`
        // record built through an annotated constructor function, colliding with
        // the real `Std.Analytics.EventProp`.
        "fieldset_ctor" => Some(
            Assignment::new()
                .with(CONSTRUCTION, "via_ctor_fn")
                .with(COLLIDER, "stdlib_eventprop"),
        ),
        _ => None,
    }
}

/// The full cross of a stratum's axes.
pub fn full_cross(s: &Stratum) -> Vec<Assignment> {
    let mut acc = vec![Assignment::new()];
    for axis in s.axes {
        let mut next = Vec::new();
        for a in &acc {
            for &v in axis.values {
                next.push(a.clone().with(*axis, v));
            }
        }
        acc = next;
    }
    acc
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn full_cross_is_the_product_of_the_axis_sizes() {
        for s in STRATA {
            let expected: usize = s.axes.iter().map(|a| a.values.len()).product();
            assert_eq!(
                full_cross(s).len(),
                expected,
                "stratum {} full cross size",
                s.name
            );
        }
    }

    #[test]
    fn every_stratum_has_a_pinned_coordinate_inside_its_own_cross() {
        for s in STRATA {
            let pin = pinned_coordinate(s.name)
                .unwrap_or_else(|| panic!("stratum {} has no pinned coordinate", s.name));
            let cross = full_cross(s);
            assert!(
                cross.contains(&pin),
                "stratum {}'s pinned coordinate {pin} is not a point in its own axis space",
                s.name
            );
        }
    }

    #[test]
    fn distance_1_neighbourhood_differs_in_exactly_one_axis() {
        for s in STRATA {
            let pin = pinned_coordinate(s.name).unwrap();
            for n in pin.neighbourhood(s.axes) {
                let differing = s
                    .axes
                    .iter()
                    .filter(|a| pin.get(**a) != n.get(**a))
                    .count();
                assert_eq!(
                    differing, 1,
                    "neighbour {n} of {pin} differs in {differing} axes, not 1"
                );
            }
        }
    }

    #[test]
    fn neighbourhood_size_is_sum_of_axis_sizes_minus_one() {
        for s in STRATA {
            let pin = pinned_coordinate(s.name).unwrap();
            let expected: usize = s.axes.iter().map(|a| a.values.len() - 1).sum();
            assert_eq!(
                pin.neighbourhood(s.axes).len(),
                expected,
                "stratum {} neighbourhood size",
                s.name
            );
        }
    }
}
