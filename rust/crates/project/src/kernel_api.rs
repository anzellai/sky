//! Curated API documentation for the KERNEL stdlib modules — the ones whose
//! bindings live in the Go runtime, not in `.sky` source (`Std.Live`, `Std.Tui`,
//! `Std.Jobs`), plus the kernel-only verbs of a module that DOES have a `.sky`
//! file (`Sky.Http.Server`'s `get`/`post`/`listen`/…, which the `.sky` surface
//! doesn't declare). `sky doc` has no source file to parse for these, so without
//! this table it could only list bare names — useless for an AI writing Sky.
//!
//! These signatures are DOCUMENTATION (the canonical shape a caller writes), not
//! the enforced type: the real kernel types are reflective / row-polymorphic
//! (that is exactly why these modules have no `.sky` file). Keep them accurate.
//!
//! SYNC CONTRACT (see CLAUDE.md "Kernel-module doc sync"): when a kernel binding
//! is added / renamed / re-typed, update its entry here in the SAME change. The
//! `kernel_api_covers_registered_kernel_functions` gate fails if a binding listed
//! in `hir::KERNEL_FUNCTIONS` has no entry here.

/// One documented kernel binding.
pub struct KernelBinding {
    /// The binding's Sky name. Read by the `kernel_api_covers_registered_kernel_functions`
    /// sync gate (doc.rs, `#[cfg(test)]`) to match against `hir::KERNEL_FUNCTIONS`;
    /// `render` shows the `sig` (which leads with the name), so a non-test build
    /// never reads this field directly.
    #[cfg_attr(not(test), allow(dead_code))]
    pub name: &'static str,
    pub sig: &'static str,
    pub summary: &'static str,
}

/// A kernel module's curated API page.
pub struct KernelModuleApi {
    /// Full dotted module name (`Std.Live`).
    pub module: &'static str,
    /// `true` when the module has NO `.sky` file, so this table is its ONLY doc
    /// source and the sync gate requires every registered kernel binding here.
    /// `false` for a DUAL module (`Sky.Http.Server`) whose `.sky` file documents
    /// part of the surface — the gate then can't require completeness here.
    /// Read by the `kernel_api_covers_registered_kernel_functions` sync gate
    /// (doc.rs, `#[cfg(test)]`); a non-test build never reads it.
    #[cfg_attr(not(test), allow(dead_code))]
    pub kernel_only: bool,
    /// One-paragraph orientation.
    pub overview: &'static str,
    /// A minimal, copy-pasteable usage example (rendered verbatim).
    pub example: &'static str,
    pub bindings: &'static [KernelBinding],
}

/// Lookup by full name (`Std.Live`) or trailing segment (`Live`).
pub fn for_module(name: &str) -> Option<&'static KernelModuleApi> {
    let suffix = format!(".{name}");
    KERNEL_API
        .iter()
        .find(|m| m.module == name || m.module.ends_with(&suffix))
}

pub const KERNEL_API: &[KernelModuleApi] = &[
    // Std.Live migrated to Layer-3 Sky source (sky-stdlib/Std/Live.sky) — its
    // sigs + docs + example now live in the .sky file, read by the type-checker,
    // LSP hover, and `sky doc` from that ONE source (v0.19 kernel-metadata
    // unification). The row-open `app` record became the typed `config`/`withX`
    // builder. No kernel_api entry needed.
    // Std.Tui + Std.Cli migrated to Layer-3 Sky source (sky-stdlib/Std/Tui.sky,
    // sky-stdlib/Std/Cli.sky) — the row-open `app`/`program` records became the
    // typed `config`/`withX` builder; sigs + docs now read from the .sky source
    // (v0.19 kernel-metadata unification). No kernel_api entries needed.
    // Std.Jobs migrated to Layer-3 Sky source (sky-stdlib/Std/Jobs.sky) — its
    // sigs + docs + example now live in the .sky file, read by the type-checker,
    // LSP hover, and `sky doc` from that ONE source (v0.19 kernel-metadata
    // unification). No kernel_api entry needed.
    KernelModuleApi {
        module: "Sky.Http.Server",
        kernel_only: false,
        overview: "Headless HTTP / JSON API (no browser UI). These verbs are \
                   kernel-provided; the module's `.sky` surface adds the extractors \
                   (`param`, `queryParam`, `header`, `getCookie`, `static`) and the \
                   `Handler` alias (`Request -> Task Error Response`).",
        example: "main =\n    \
                  Server.listen 8000\n        \
                  [ Server.get \"/\" (\\_ -> Task.succeed (Server.text \"Hello!\"))\n        \
                  , Server.get \"/users/:id\" getUser\n        \
                  , Server.post \"/users\" createUser\n        \
                  ]",
        bindings: &[
            KernelBinding {
                name: "listen",
                sig: "listen : Int -> List Route -> Task Error ()",
                summary: "Bind a port and serve the given routes (+ middleware).",
            },
            KernelBinding {
                name: "get",
                sig: "get : String -> Handler -> Route",
                summary: "Route a GET request for a path (with `:param` segments) to a handler.",
            },
            KernelBinding {
                name: "post",
                sig: "post : String -> Handler -> Route",
                summary: "Route a POST request to a handler.",
            },
            KernelBinding {
                name: "put",
                sig: "put : String -> Handler -> Route",
                summary: "Route a PUT request to a handler.",
            },
            KernelBinding {
                name: "delete",
                sig: "delete : String -> Handler -> Route",
                summary: "Route a DELETE request to a handler.",
            },
            KernelBinding {
                name: "any",
                sig: "any : String -> Handler -> Route",
                summary: "Route a request of ANY method to a handler.",
            },
            KernelBinding {
                name: "text",
                sig: "text : String -> Response",
                summary: "A `text/plain` 200 response.",
            },
            KernelBinding {
                name: "json",
                sig: "json : String -> Response",
                summary: "An `application/json` 200 response (body already encoded).",
            },
            KernelBinding {
                name: "html",
                sig: "html : String -> Response",
                summary: "A `text/html` 200 response.",
            },
            KernelBinding {
                name: "redirect",
                sig: "redirect : String -> Response",
                summary: "A 303 redirect to the given location.",
            },
            KernelBinding {
                name: "withStatus",
                sig: "withStatus : Int -> Response -> Response",
                summary: "Override a response's status code.",
            },
        ],
    },
];

/// Render a kernel module's curated page (overview + typed bindings + example).
pub fn render(m: &KernelModuleApi) -> String {
    let mut out = format!("── {} ──\n\n{}\n\n", m.module, m.overview);
    for b in m.bindings {
        out.push_str(&format!("  {}\n      {}\n\n", b.sig, b.summary));
    }
    if !m.example.is_empty() {
        out.push_str("Example:\n\n");
        for line in m.example.lines() {
            out.push_str("  ");
            out.push_str(line);
            out.push('\n');
        }
        out.push('\n');
    }
    out
}
