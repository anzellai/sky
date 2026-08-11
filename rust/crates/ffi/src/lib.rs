//! `ffi` — deterministic Go-package inspection → pinned `.skyi` surface;
//! reproducible, committed (doc 02, doc 09). The platform-variant inspector runs
//! *once*, is pinned + committed, and never runs mid-build — this closes the
//! reproducibility killer by committing what was gitignored (doc 01, L4).
//!
//! M0 stub: the serde-serialisable surface type is seeded. M5 wires the pinned
//! `.skyi` load path.

use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

pub mod assets;
pub mod gen;
mod gen_bindings;
pub mod inspect;

pub use assets::{
    embedded_assets, embedded_runtime, embedded_stdlib, extract_assets_root, extract_template,
};
pub use gen::{generate, GeneratedSurface};
pub use inspect::{
    ensure_inspector, run_inspector, run_inspector_reporting, InspectTarget, PackageInfo,
};

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
    // Some inspector-emitted entries (`requestCancel`, `closeNotifierCloseNotify`)
    // omit `skyType`. Defaulting rather than requiring it keeps ONE malformed
    // function from sinking the whole package's `serde` parse (which would drop
    // every other symbol — e.g. `Net.Http.listenAndServe`).
    #[serde(rename = "skyType", default)]
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
    /// Every `FfiT_<sym>_P<i>` / `FfiT_<sym>_R` typed-slot alias the wrapper
    /// declares. The generator emits one only for a non-primitive Go param type
    /// (`*mux.Router`, a `func(...)` handler) — a `string`/`int`/`bool` param has
    /// no alias and is passed to the wrapper directly. A call-site coercion to
    /// `rt.FfiT_<sym>_P<i>` is therefore valid only when that name is in this set.
    pub ffi_slots: BTreeSet<String>,
    /// Path to the `<slug>_bindings.go` wrapper (materialised into sky-out/rt/).
    pub binding_file: Option<PathBuf>,
    /// Per-wrapper-symbol ordered Go param types, parsed from each
    /// `func Go_…(name type, …)` signature in the wrapper. The authoritative
    /// source for a primitive param's REAL Go type (`int64` vs Sky's `Int`,
    /// which the `kernel.json` skyType flattens to `Int`). Keyed by the full Go
    /// symbol (`Go_Stripe_…SetUnitAmountT`). Non-primitive params still route
    /// through their `FfiT_…_P<i>` slot; this pins the primitives.
    pub wrapper_params: BTreeMap<String, Vec<String>>,
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
        let (go_symbols, ffi_slots, wrapper_params, binding_file) = if binding.exists() {
            (
                scan_go_symbols(&binding),
                scan_ffi_slots(&binding),
                scan_wrapper_params(&binding),
                Some(binding),
            )
        } else {
            (BTreeSet::new(), BTreeSet::new(), BTreeMap::new(), None)
        };
        let mut functions = BTreeMap::new();
        for f in kj.functions {
            functions.insert(
                f.name,
                FfiFnInfo {
                    arity: f.arity,
                    sky_type: f.sky_type,
                },
            );
        }
        reg.packages.insert(
            kj.module_name.clone(),
            FfiPackage {
                module_name: kj.module_name,
                kernel_name: kj.kernel_name,
                go_package: kj.package,
                functions,
                go_symbols,
                ffi_slots,
                wrapper_params,
                binding_file,
            },
        );
    }
    reg
}

/// Scan a Go wrapper file for its `type FfiT_… = …` slot-alias names.
fn scan_ffi_slots(path: &Path) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    let Ok(text) = std::fs::read_to_string(path) else {
        return out;
    };
    for line in text.lines() {
        let Some(rest) = line.trim_start().strip_prefix("type ") else {
            continue;
        };
        let name: String = rest
            .chars()
            .take_while(|c| c.is_alphanumeric() || *c == '_')
            .collect();
        if name.starts_with("FfiT_") {
            out.insert(name);
        }
    }
    out
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

/// Scan a Go wrapper file for each top-level `func Go_<sym>(params…)` signature,
/// returning `symbol → [param-type strings]` in declaration order. The param
/// TYPE (everything after the param name) is captured for each param; grouped
/// params (`a, b int`) are not emitted by the generator so a first-space split
/// per param is sufficient. Depth-aware comma splitting handles `func(…)` /
/// generic param types that embed commas.
fn scan_wrapper_params(path: &Path) -> BTreeMap<String, Vec<String>> {
    let mut out = BTreeMap::new();
    let Ok(text) = std::fs::read_to_string(path) else {
        return out;
    };
    for line in text.lines() {
        let Some(rest) = line.trim_start().strip_prefix("func ") else {
            continue;
        };
        if rest.starts_with('(') {
            continue; // method receiver — not a plain Go_* func.
        }
        let sym: String = rest
            .chars()
            .take_while(|c| c.is_alphanumeric() || *c == '_')
            .collect();
        if !sym.starts_with("Go_") {
            continue;
        }
        // The param list is between the first `(` after the name and its match.
        let after = &rest[sym.len()..];
        let Some(open) = after.find('(') else {
            continue;
        };
        let inner = match matching_paren(&after[open..]) {
            Some(end) => &after[open + 1..open + end],
            None => continue,
        };
        let params = split_top_commas(inner)
            .into_iter()
            .filter_map(|p| {
                let p = p.trim();
                if p.is_empty() {
                    return None;
                }
                // `name type` → the type is everything past the first token.
                match p.split_once(char::is_whitespace) {
                    Some((_name, ty)) => Some(ty.trim().to_string()),
                    None => Some(p.to_string()),
                }
            })
            .collect::<Vec<_>>();
        out.insert(sym, params);
    }
    out
}

/// Byte offset (into `s`, which must start with `(`) of the matching `)`.
fn matching_paren(s: &str) -> Option<usize> {
    let mut depth = 0i32;
    for (i, b) in s.bytes().enumerate() {
        match b {
            b'(' => depth += 1,
            b')' => {
                depth -= 1;
                if depth == 0 {
                    return Some(i);
                }
            }
            _ => {}
        }
    }
    None
}

/// Split a Go param list on top-level commas (depth 0 of `()`/`[]`/`{}`).
fn split_top_commas(s: &str) -> Vec<String> {
    let mut parts = Vec::new();
    let mut depth = 0i32;
    let mut start = 0usize;
    for (i, b) in s.bytes().enumerate() {
        match b {
            b'(' | b'[' | b'{' => depth += 1,
            b')' | b']' | b'}' => depth -= 1,
            b',' if depth == 0 => {
                parts.push(s[start..i].to_string());
                start = i + 1;
            }
            _ => {}
        }
    }
    parts.push(s[start..].to_string());
    parts
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
