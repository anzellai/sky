//! The pinned Go-FFI signature table the type checker reads (doc 09).
//!
//! `sky add` records each Go binding's Sky type in `sky-ffi/<pkg>.kernel.json`
//! (`skyType`, e.g. `String -> Int -> Int -> Result Error (String, Int)`). This
//! module holds those strings, keyed the way the resolver keys a Go-FFI
//! reference (`Res::Foreign { package, name }` — `package` is the Sky module path
//! of the import, `Github.Com.Google.Uuid`), so every inference run can type a
//! foreign reference instead of giving it a fresh flexible variable.
//!
//! The table carries the RAW strings only. Parsing them into type schemes is the
//! type layer's job (`ty::ffi_sig`), and it happens lazily, per referenced
//! function: a large surface (a payment SDK has more than 80,000 bindings) is
//! never parsed as a whole. `hir` deliberately does not depend on the `ffi`
//! crate; the build driver projects the loaded registry into this shape.

use std::collections::BTreeMap;
use std::sync::Arc;

/// One Go-FFI function's pinned shape.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct FfiFnSig {
    /// The Sky-level arity recorded by the inspector (`() -> X` counts 1).
    pub arity: usize,
    /// The pinned `skyType` string, verbatim. Empty when the inspector omitted
    /// it (a handful of bindings do); the type layer then falls back to an
    /// arity-only scheme that still enforces the `Result Error` wrapper.
    pub sky_type: Arc<str>,
}

/// The loaded Go-FFI surface: package (Sky module path) → function → signature.
/// `BTreeMap`s so equality and iteration are deterministic (L4).
#[derive(Clone, Debug, Default, PartialEq, Eq, Hash)]
pub struct FfiSurface {
    packages: BTreeMap<String, BTreeMap<String, FfiFnSig>>,
}

impl FfiSurface {
    pub fn new() -> Self {
        FfiSurface::default()
    }

    /// Record one package's function table (replaces an earlier entry for the
    /// same package).
    pub fn insert_package(&mut self, package: &str, fns: BTreeMap<String, FfiFnSig>) {
        self.packages.insert(package.to_string(), fns);
    }

    /// Record one function (creating the package entry when absent).
    pub fn insert_fn(&mut self, package: &str, name: &str, sig: FfiFnSig) {
        self.packages
            .entry(package.to_string())
            .or_default()
            .insert(name.to_string(), sig);
    }

    /// Whether a surface for `package` is loaded at all.
    pub fn has_package(&self, package: &str) -> bool {
        self.packages.contains_key(package)
    }

    /// The pinned signature of `package.name`, if the package is loaded and
    /// defines it.
    pub fn lookup(&self, package: &str, name: &str) -> Option<FfiFnSig> {
        self.packages.get(package)?.get(name).cloned()
    }

    pub fn is_empty(&self) -> bool {
        self.packages.is_empty()
    }

    /// Number of loaded packages.
    pub fn package_count(&self) -> usize {
        self.packages.len()
    }

    /// Number of functions across every loaded package.
    pub fn fn_count(&self) -> usize {
        self.packages.values().map(|m| m.len()).sum()
    }

    /// Every `(package, name, sig)` in deterministic order — for census
    /// reporting (`-v`, docs), never for inference.
    pub fn iter(&self) -> impl Iterator<Item = (&str, &str, &FfiFnSig)> {
        self.packages
            .iter()
            .flat_map(|(p, fns)| fns.iter().map(move |(n, s)| (p.as_str(), n.as_str(), s)))
    }
}

/// A shared, cheaply-cloned surface handle (both database backends hold one).
pub type SharedFfiSurface = Arc<FfiSurface>;
