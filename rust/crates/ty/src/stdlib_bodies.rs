//! Type-check the BODIES of every stdlib module.
//!
//! A build trusts the stdlib's signatures and never checks its bodies:
//! `project::build` puts only the project's own modules, its path dependencies
//! and its `.skydeps` packages in `check_ids`, so `ty::check_modules` never sees
//! a `sky-stdlib/` module. That is deliberate: the stdlib is compiler-owned and
//! fixed per compiler version, so re-checking it on every user build would prove
//! the same fact again each time. But it left nothing that proved the fact even
//! once. A stdlib record literal that lacked a field its alias had just gained
//! built clean and passed every gate; the same file, renamed into a user module,
//! was rejected at once with `[E2001] record is missing field(s)`.
//!
//! This module is that proof, run once per change instead of once per build. It
//! loads every stdlib module into one world and runs the SAME checker a user
//! module gets ([`crate::check_modules_with_world`]) over each of them. The
//! stdlib is granted `Sky.Ffi` exactly as the build and the editor grant it
//! (`hir::FfiTrust::modules`), so `[E1011]` stays a user-code rule.
//!
//! Two faces read it: `ty/tests/stdlib_bodies.rs` (per-commit `cargo test`) and
//! the `stdlib-bodies` gate in `xtask harness`, whose falsifying mutation drops
//! a field from a record literal in a stdlib body.

use crate::reject_corpus::load_stdlib;
use diagnostics::{Diagnostic, Severity};
use std::path::Path;

/// One `[E1004]` (a type or constructor that reuses a Prelude-exposed name) the
/// stdlib is allowed to declare. `[E1004]` is a rule for user modules. Every
/// entry names the module, the exact diagnostic text and why the stdlib may
/// declare it. An entry that no longer fires is a FAILURE, so this list can
/// only shrink.
pub struct PreludeShadow {
    pub module: &'static str,
    pub message: &'static str,
    pub reason: &'static str,
}

pub const ALLOWED_PRELUDE_SHADOWS: &[PreludeShadow] = &[
    PreludeShadow {
        module: "Sky.Core.Error",
        message: "Type `Error` shadows a Prelude-exposed name — pick a different name",
        reason: "Sky.Core.Error DEFINES the Prelude's `Error` type; this is the \
                 declaration the Prelude re-exports, not a shadow of it",
    },
    PreludeShadow {
        module: "Sky.Core.Error",
        message: "Constructor `Error` shadows a Prelude-exposed name — pick a different name",
        reason: "Sky.Core.Error DEFINES the Prelude's `Error` constructor",
    },
    PreludeShadow {
        module: "Sky.Config",
        message: "Constructor `Error` shadows a Prelude-exposed name — pick a different name",
        reason: "the public `LogLevel` constructor `Sky.Config.Error`. Kept by \
                 decision (v0.27.18): a rename would change a published API and \
                 the runtime enum name (`Sky_Config_LogLevel`). A user module \
                 that imports `LogLevel(..)` and writes a bare `Error` gets \
                 `[E1017]` CONSTRUCTOR CLASH, which names both meanings and the \
                 qualified forms. Inside Sky.Config it is used only as a \
                 `LogLevel` pattern",
    },
];

/// The verdict for one stdlib module.
pub struct ModuleReport {
    /// Module name, e.g. `Std.Ai.Provider`.
    pub module: String,
    /// Every error the checker reports for this module's bodies, rendered as
    /// `<line>: [CODE] message`. Allowed `[E1004]` entries are removed.
    pub errors: Vec<String>,
}

/// The verdict for the whole stdlib.
pub struct StdlibReport {
    pub modules: Vec<ModuleReport>,
    /// Allowed `[E1004]` entries that did not fire. Each one is a failure.
    pub stale_allowances: Vec<String>,
}

impl StdlibReport {
    /// Number of errors across every module.
    pub fn error_count(&self) -> usize {
        self.modules.iter().map(|m| m.errors.len()).sum()
    }

    /// True when every module body type-checks and no allowance is stale.
    pub fn clean(&self) -> bool {
        self.error_count() == 0 && self.stale_allowances.is_empty()
    }

    /// Every error, one per line, prefixed with its module.
    pub fn render(&self) -> String {
        let mut out = String::new();
        for m in &self.modules {
            for e in &m.errors {
                out.push_str(&format!("{}:{}\n", m.module, e));
            }
        }
        for s in &self.stale_allowances {
            out.push_str(&format!(
                "stale ALLOWED_PRELUDE_SHADOWS entry (no longer fires): {s}\n"
            ));
        }
        out
    }
}

/// Load `<root>/sky-stdlib` and check every module body.
pub fn check_repo_stdlib(root: &Path) -> StdlibReport {
    check_stdlib_modules(&load_stdlib(root))
}

/// Check every module in `stdlib` (a whole stdlib: `(module name, parse)`).
/// Callers may pass an overlay (an edited module) to test the check itself.
pub fn check_stdlib_modules(stdlib: &[(String, syntax::Parse)]) -> StdlibReport {
    let mut db = hir::SourceDb::new();
    let mut trust = hir::FfiTrust::default();
    let mut ids = Vec::with_capacity(stdlib.len());
    for (name, parse) in stdlib {
        ids.push((name.clone(), db.add_module(name, parse.clone())));
        trust.modules.insert(name.clone());
    }
    let mut surface = hir::FfiSurface::new();
    surface.set_trust(trust);
    db.set_ffi_surface(std::sync::Arc::new(surface));

    let world = crate::TyDb::check_world(&db);
    let mut used = vec![false; ALLOWED_PRELUDE_SHADOWS.len()];
    let mut modules = Vec::with_capacity(ids.len());
    for (name, id) in &ids {
        // One module per call: every module's spans carry `FileId(0)` here, so
        // the module a diagnostic belongs to is the module being checked.
        let out = crate::check_modules_with_world(&db, world.clone(), &[*id]);
        let src = hir::SkyDb::module_parse(&db, *id)
            .syntax()
            .text()
            .to_string();
        let mut errors = Vec::new();
        for d in &out.diagnostics {
            if d.severity != Severity::Error {
                continue;
            }
            if d.code.0 == "E1004" {
                if let Some(i) = ALLOWED_PRELUDE_SHADOWS
                    .iter()
                    .position(|a| a.module == name && a.message == d.message)
                {
                    used[i] = true;
                    continue;
                }
            }
            errors.push(render(d, &src));
        }
        modules.push(ModuleReport {
            module: name.clone(),
            errors,
        });
    }
    // An allowance only counts as stale when its module was checked: an
    // overlay that leaves a module out says nothing about it.
    let stale_allowances = ALLOWED_PRELUDE_SHADOWS
        .iter()
        .zip(&used)
        .filter(|(a, used)| !**used && ids.iter().any(|(n, _)| n == a.module))
        .map(|(a, _)| format!("{}: {}", a.module, a.message))
        .collect();
    StdlibReport {
        modules,
        stale_allowances,
    }
}

/// `<line>: [CODE] message` for one diagnostic.
fn render(d: &Diagnostic, src: &str) -> String {
    let line = d
        .labels
        .first()
        .map(|l| {
            let at = (l.span.range.0 as usize).min(src.len());
            src[..at].matches('\n').count() + 1
        })
        .map(|n| n.to_string())
        .unwrap_or_else(|| "?".to_string());
    format!("{line}: [{}] {}", d.code.0, d.message.replace('\n', " "))
}
