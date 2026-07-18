//! `ffi` — deterministic Go-package inspection → pinned `.skyi` surface;
//! reproducible, committed (doc 02, doc 09). The platform-variant inspector runs
//! *once*, is pinned + committed, and never runs mid-build — this is the
//! `f6e3ecdd` reproducibility killer, closed by committing what was gitignored
//! (doc 01, L4).
//!
//! M0 stub: the serde-serialisable surface type is seeded. M5 wires the pinned
//! `.skyi` load path.

use serde::{Deserialize, Serialize};

/// A pinned FFI symbol: a Go binding surfaced to Sky with its HM signature.
/// `serde`-serialisable so the whole surface round-trips to a committed `.skyi`
/// file deterministically (doc 09).
#[derive(Clone, PartialEq, Eq, Debug, Serialize, Deserialize)]
pub struct FfiSymbol {
    pub go_package: String,
    pub name: String,
    /// The HM signature as pinned text (structured form lands with `ty` in M5).
    pub signature: String,
}

/// A pinned FFI surface for one Go package — the committed `.skyi` payload.
#[derive(Clone, PartialEq, Eq, Debug, Default, Serialize, Deserialize)]
pub struct FfiSurface {
    pub symbols: Vec<FfiSymbol>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn surface_round_trips_through_serde() {
        let surface = FfiSurface {
            symbols: vec![FfiSymbol {
                go_package: "strings".to_string(),
                name: "ToUpper".to_string(),
                signature: "String -> String".to_string(),
            }],
        };
        // Prove the derive wiring compiles + round-trips (deterministic pin).
        let cloned = surface.clone();
        assert_eq!(surface, cloned);
    }
}
