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
    /// Which checked modules may use `Sky.Ffi` directly (`[E1011]`).
    trust: FfiTrust,
}

/// Which checked modules may use `Sky.Ffi` (`kernel`, `call`, `callPure`,
/// `callTask`) directly.
///
/// `Sky.Ffi` is stdlib-only: `Ffi.kernel "Sym"` trusts the def's annotation
/// without comparing it to the kernel's real signature, and `Ffi.call*` reach a
/// Go binding by name with a free type. The type checker only checks APP code
/// (the stdlib is trusted and never re-checked), so an app module gets no
/// `Sky.Ffi` at all unless the build grants it here. There are two grants,
/// and the build decides both from facts a user does not write by accident:
///
/// * `modules` — a module whose source text IS the compiler's own bundled-app
///   source (`sky-bundled/<app>/src`, compared by content, not by name), or a
///   stdlib module opened in the editor. Full `Sky.Ffi`, like the stdlib.
/// * `kernel_prefixes` — a project the Sky.Spa split GENERATED (`[spa]
///   generated = true`, written only by the generator). Its backend binds the
///   split's own `Spa_*` plumbing kernels. `Ffi.kernel` only, and only for a
///   literal symbol with one of these prefixes.
///
/// A module is never trusted by its NAME: a project module declared as
/// `module Sky.Evil` is app code like any other.
#[derive(Clone, Debug, Default, PartialEq, Eq, Hash)]
pub struct FfiTrust {
    /// Module names granted full `Sky.Ffi` (compiler-owned source).
    pub modules: std::collections::BTreeSet<String>,
    /// Kernel-symbol prefixes any checked module may bind with `Ffi.kernel`.
    pub kernel_prefixes: std::collections::BTreeSet<String>,
}

impl FfiTrust {
    /// May `module` use `Sky.Ffi.<member>`? `symbol` is the literal kernel
    /// symbol of an `Ffi.kernel "Sym"` application, `None` when the reference
    /// is not applied to a string literal.
    pub fn allows(&self, module: &str, member: &str, symbol: Option<&str>) -> bool {
        if self.modules.contains(module) {
            return true;
        }
        member == "kernel"
            && symbol.is_some_and(|s| {
                self.kernel_prefixes
                    .iter()
                    .any(|p| s.starts_with(p.as_str()))
            })
    }
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

    /// The `Sky.Ffi` grants of this build (see [`FfiTrust`]).
    pub fn trust(&self) -> &FfiTrust {
        &self.trust
    }

    /// Replace the `Sky.Ffi` grants of this build (see [`FfiTrust`]).
    pub fn set_trust(&mut self, trust: FfiTrust) {
        self.trust = trust;
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
