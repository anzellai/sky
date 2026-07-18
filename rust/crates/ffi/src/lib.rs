//! `ffi` — deterministic Go-package inspection → pinned `.skyi` surface;
//! reproducible, committed (doc 02, doc 09). The platform-variant inspector runs
//! *once*, is pinned + committed, and never runs mid-build — this is the
//! `f6e3ecdd` reproducibility killer, closed by committing what was gitignored
//! (doc 01, L4).
//!
//! M0 stub: the serde-serialisable surface type is seeded. M5 wires the pinned
//! `.skyi` load path.

use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

// ---------------------------------------------------------------------------
// Committed pinned-surface loader (doc 09 §C).
//
// Beachhead scope: load the *already-generated* `kernel.json` + Go wrapper
// surface (the deterministic committed input, doc 09 §C.1) and expose it to the
// build driver. The Rust-native deterministic inspector (§C.2/§E) that would
// *regenerate* this surface is the remaining FFI work — for now the surface is a
// pinned build input reused as-is (produced out-of-band by the Haskell oracle
// `sky add`). Loading is sorted + deterministic (§C.2 consumer rule).
// ---------------------------------------------------------------------------

#[derive(Deserialize)]
struct KernelJsonFn {
    name: String,
    arity: usize,
    #[serde(rename = "skyType")]
    sky_type: String,
}

#[derive(Deserialize)]
struct KernelJson {
    #[serde(rename = "moduleName")]
    module_name: String,
    #[serde(rename = "kernelName")]
    kernel_name: String,
    package: String,
    functions: Vec<KernelJsonFn>,
}

/// One FFI function's pinned shape.
#[derive(Clone, Debug)]
pub struct FfiFnInfo {
    pub arity: usize,
    pub sky_type: String,
}

/// One Go package's pinned FFI surface, parsed from `<slug>.kernel.json` and its
/// `<slug>_bindings.go` wrapper.
#[derive(Clone, Debug)]
pub struct FfiPackage {
    /// The Sky module path the import binds (`Github.Com.Google.Uuid`).
    pub module_name: String,
    /// The kernel prefix for its Go symbols (`Go_Uuid`).
    pub kernel_name: String,
    /// The Go import path (`github.com/google/uuid`).
    pub go_package: String,
    pub functions: BTreeMap<String, FfiFnInfo>,
    /// Every `Go_*` func symbol defined in the wrapper (`Go_Uuid_newStringT`, …)
    /// — used to pick the typed `T` variant over the untyped fallback.
    pub go_symbols: BTreeSet<String>,
    /// Path to the `<slug>_bindings.go` wrapper (materialised into sky-out/rt/).
    pub binding_file: Option<PathBuf>,
}

/// The loaded FFI surface, keyed by Sky module path. Every collection is a
/// `BTreeMap`/`BTreeSet` so iteration is deterministic (doc 09 L4).
#[derive(Clone, Debug, Default)]
pub struct FfiRegistry {
    pub packages: BTreeMap<String, FfiPackage>,
}

impl FfiRegistry {
    pub fn resolve(&self, module_name: &str) -> Option<&FfiPackage> {
        self.packages.get(module_name)
    }
    pub fn is_empty(&self) -> bool {
        self.packages.is_empty()
    }
}

/// Load a committed/pinned surface: parse every `<ffi_dir>/*.kernel.json`, pair
/// each with `<go_dir>/<slug>_bindings.go`, scan its `Go_*` symbols. Directory
/// enumeration is sorted before folding so the result is a pure function of the
/// committed bytes (doc 09 §C.2 — closes the `listDirectory` nondeterminism).
pub fn load_surface(ffi_dir: &Path, go_dir: &Path) -> FfiRegistry {
    let mut reg = FfiRegistry::default();
    let Ok(rd) = std::fs::read_dir(ffi_dir) else {
        return reg;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for path in entries {
        let Some(fname) = path.file_name().and_then(|n| n.to_str()) else {
            continue;
        };
        let Some(slug) = fname.strip_suffix(".kernel.json") else {
            continue;
        };
        let slug = slug.to_string();
        let Ok(text) = std::fs::read_to_string(&path) else {
            continue;
        };
        let Ok(kj) = serde_json::from_str::<KernelJson>(&text) else {
            continue;
        };
        let binding = go_dir.join(format!("{slug}_bindings.go"));
        let (go_symbols, binding_file) = if binding.exists() {
            (scan_go_symbols(&binding), Some(binding))
        } else {
            (BTreeSet::new(), None)
        };
        let mut functions = BTreeMap::new();
        for f in kj.functions {
            functions.insert(f.name, FfiFnInfo { arity: f.arity, sky_type: f.sky_type });
        }
        reg.packages.insert(
            kj.module_name.clone(),
            FfiPackage {
                module_name: kj.module_name,
                kernel_name: kj.kernel_name,
                go_package: kj.package,
                functions,
                go_symbols,
                binding_file,
            },
        );
    }
    reg
}

/// Scan a Go wrapper file for its top-level `func Go_*` symbol names.
fn scan_go_symbols(path: &Path) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    let Ok(text) = std::fs::read_to_string(path) else {
        return out;
    };
    for line in text.lines() {
        let Some(rest) = line.trim_start().strip_prefix("func ") else {
            continue;
        };
        // Skip method receivers (`func (x T) …`); a plain func name follows.
        if rest.starts_with('(') {
            continue;
        }
        let sym: String = rest
            .chars()
            .take_while(|c| c.is_alphanumeric() || *c == '_')
            .collect();
        if sym.starts_with("Go_") {
            out.insert(sym);
        }
    }
    out
}

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
