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
    KernelModuleApi {
        module: "Std.Live",
        kernel_only: true,
        overview: "Server-driven web UI on The Elm Architecture. HTTP-first: the \
                   first request returns full HTML; every later event streams a \
                   DOM-diff patch over one persistent SSE connection. Sessions, \
                   cookies, URL routing, and async commands are built in. `init` \
                   runs once per session (a browser reload restores the Model from \
                   the session store — it does NOT re-run `init`).",
        example: "main =\n    \
                  Live.app\n        \
                  { init = init\n        \
                  , update = update\n        \
                  , view = view\n        \
                  , subscriptions = subscriptions\n        \
                  , routes = [ route \"/\" HomePage, route \"/apps/:slug\" AppPage ]\n        \
                  , notFound = HomePage\n        \
                  }",
        bindings: &[
            KernelBinding {
                name: "app",
                sig: "app : { init : Request -> ( model, Cmd msg ), update : msg -> model -> ( model, Cmd msg ), view : model -> Element msg, subscriptions : model -> Sub msg, routes : List Route, notFound : page } -> Task Error ()",
                summary: "Run a Sky.Live app. The cfg record is ROW-OPEN — you may also add the optional fields `head : Model -> List (Html msg)`, `consoleAuth : Request -> Task Error (Maybe Identity)`, and `status : { reconnecting : String, offline : String }`.",
            },
            KernelBinding {
                name: "route",
                sig: "route : String -> page -> Route",
                summary: "Map a URL path to a Page value; `:name` segments are captured and delivered to the Page constructor as String. Declare literals before patterns (`route \"/apps/new\" NewAppPage` before `route \"/apps/:slug\" AppPage`).",
            },
            KernelBinding {
                name: "api",
                sig: "api : String -> (Request -> Task Error Response) -> Route",
                summary: "Mount a raw HTTP/JSON handler OUTSIDE the TEA cycle (file uploads, webhooks, a JSON API next to the UI). The spec is `\"METHOD /path\"` (e.g. `\"POST /api/upload\"`); an omitted method matches any.",
            },
            KernelBinding {
                name: "lifecycle",
                sig: "lifecycle : msg -> msg",
                summary: "Wrap a Msg to tag it for lifecycle logging/tracing in the dev console. Idempotent; returns the same Msg for dispatch.",
            },
        ],
    },
    KernelModuleApi {
        module: "Std.Tui",
        kernel_only: true,
        overview: "Terminal UI on The Elm Architecture — the same init/update/view/\
                   subscriptions shape as Sky.Live, rendered to ANSI cells on a \
                   logical-pixel canvas. `Std.Ui` views render identically across \
                   Sky.Live, Sky.Tui, and Sky.Webview.",
        example: "main =\n    \
                  Tui.app\n        \
                  { init = init\n        \
                  , update = update\n        \
                  , view = view\n        \
                  , subscriptions = subscriptions\n        \
                  , onKey = KeyPressed\n        \
                  }\n        \
                  |> Task.run",
        bindings: &[KernelBinding {
            name: "app",
            sig: "app : { init : () -> ( model, Cmd msg ), update : msg -> model -> ( model, Cmd msg ), view : model -> Element msg, subscriptions : model -> Sub msg, onKey : KeyEvent -> msg } -> Task Error ()",
            summary: "Run a Sky.Tui app. The cfg record is ROW-OPEN — optional fields: `guard : msg -> model -> Result Error ()`, `canvasWidth : Int` (default 1280), `canvasHeight : Int` (default 720).",
        }],
    },
    KernelModuleApi {
        module: "Std.Jobs",
        kernel_only: true,
        overview: "Durable background jobs: define a typed handler once, enqueue \
                   payloads to run it (optionally after a delay), and cancel a \
                   pending job by id.",
        example: "sendEmail : Job EmailPayload\n\
                  sendEmail =\n    \
                  Jobs.define \"send-email\" (\\payload -> Email.send provider payload)\n\n\
                  enqueueWelcome : EmailPayload -> Task Error JobId\n\
                  enqueueWelcome payload =\n    \
                  Jobs.enqueue sendEmail payload",
        bindings: &[
            KernelBinding {
                name: "define",
                sig: "define : String -> (a -> Task Error ()) -> Job a",
                summary: "Register a named job handler over a typed payload `a`.",
            },
            KernelBinding {
                name: "enqueue",
                sig: "enqueue : Job a -> a -> Task Error JobId",
                summary: "Enqueue a payload to run its job as soon as a worker is free.",
            },
            KernelBinding {
                name: "enqueueIn",
                sig: "enqueueIn : Int -> Job a -> a -> Task Error JobId",
                summary: "Enqueue a payload to run after a delay (milliseconds).",
            },
            KernelBinding {
                name: "cancel",
                sig: "cancel : JobId -> Task Error ()",
                summary: "Cancel a pending job by its id (no-op if already run).",
            },
        ],
    },
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
