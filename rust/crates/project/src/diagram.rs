//! `sky doc --diagram <kind>` — read-only architecture diagrams derived from a
//! Sky project's resolved HIR. This module implements the FIRST kind,
//! `components`: at a glance, what an app is made of (its `src/` modules) and
//! what external capabilities each one touches (Database, External HTTP, Auth,
//! File, Env/Config, Telemetry/Logs, Jobs, Realtime/SSE, Time/Random/Uuid).
//!
//! It is strictly **read-only**: it loads the same source db the build assembles
//! ([`crate::build::load_source_db`]), resolves the HIR, and walks the effect
//! kernels each module transitively reaches — via
//! [`crate::spa_partition::body_effects_and_callees`], so the capability
//! classification is the SAME one the Sky.Spa auto-split uses and cannot drift.
//! It never type-checks, lowers, emits, or writes a file.
//!
//! For a Sky.Spa app (a wasm-client target, or an explicit `Std.Spa` use) the
//! diagram splits into a `Client` lane (the modules, authored for the browser)
//! and a `Server` lane (the capabilities, which run server-side under the v1
//! "any effect -> server" rule), with the `/_rpc` boundary between them. A
//! non-Spa app (Sky.Live / Http / Cli) renders a single lane — still useful.

use base::DefId;
use hir::SkyDb;
use std::collections::{BTreeSet, HashMap, HashSet};
use std::path::Path;

/// A single external capability bucket a module can touch. The set is
/// deliberately small + fixed so the diagram stays readable — one node per
/// bucket, never a per-function hairball.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug)]
pub enum Capability {
    Database,
    ExternalHttp,
    Auth,
    File,
    EnvConfig,
    Telemetry,
    Jobs,
    Realtime,
    Nondeterminism,
}

impl Capability {
    /// Map an effect-kernel family prefix (`Db`, `Http`, `System`, `Time`, …), as
    /// produced by [`crate::spa_partition::body_effects_and_callees`], to its
    /// capability bucket. Returns `None` for effect families outside the
    /// components view's buckets — the inbound `Server`, `Process`/`Io` stdio,
    /// the `RateLimit`/`Middleware`/`Context` plumbing, the `Cli`/`Tui`/`Webview`
    /// host shells, and the client-only `Native` family — which this first slice
    /// does not chart.
    ///
    /// There is no distinct `Analytics` kernel family (analytics output goes
    /// through `Std.Log`), so analytics folds into `Telemetry`; the components
    /// graph notes this so a reader knows the bucket includes it.
    pub fn from_family(fam: &str) -> Option<Capability> {
        Some(match fam {
            "Db" => Capability::Database,
            "Http" => Capability::ExternalHttp,
            "Auth" => Capability::Auth,
            "File" => Capability::File,
            "System" => Capability::EnvConfig,
            "Log" => Capability::Telemetry,
            "Jobs" => Capability::Jobs,
            "Live" => Capability::Realtime,
            "Time" | "Random" | "Uuid" => Capability::Nondeterminism,
            _ => return None,
        })
    }

    pub fn label(self) -> &'static str {
        match self {
            Capability::Database => "Database",
            Capability::ExternalHttp => "External HTTP",
            Capability::Auth => "Auth",
            Capability::File => "File",
            Capability::EnvConfig => "Env/Config",
            Capability::Telemetry => "Telemetry/Logs",
            Capability::Jobs => "Jobs",
            Capability::Realtime => "Realtime/SSE",
            Capability::Nondeterminism => "Time/Random/Uuid",
        }
    }

    /// The Mermaid node id — stable, ascii, one per bucket.
    fn node_id(self) -> &'static str {
        match self {
            Capability::Database => "cap_db",
            Capability::ExternalHttp => "cap_http",
            Capability::Auth => "cap_auth",
            Capability::File => "cap_file",
            Capability::EnvConfig => "cap_env",
            Capability::Telemetry => "cap_log",
            Capability::Jobs => "cap_jobs",
            Capability::Realtime => "cap_live",
            Capability::Nondeterminism => "cap_nd",
        }
    }

    /// A Mermaid node declaration with a distinct SHAPE per capability, so
    /// capability nodes read differently from the plain-rectangle module nodes.
    fn node_decl(self) -> String {
        let l = self.label();
        match self {
            // cylinder — a datastore
            Capability::Database => format!("cap_db[(\"{l}\")]"),
            // stadium — a network endpoint
            Capability::ExternalHttp => format!("cap_http([\"{l}\"])"),
            // hexagon — a trust/security boundary
            Capability::Auth => format!("cap_auth{{{{\"{l}\"}}}}"),
            // parallelogram — I/O
            Capability::File => format!("cap_file[/\"{l}\"/]"),
            Capability::EnvConfig => format!("cap_env[/\"{l}\"/]"),
            // subroutine — a side channel
            Capability::Telemetry => format!("cap_log[[\"{l}\"]]"),
            Capability::Jobs => format!("cap_jobs[[\"{l}\"]]"),
            // circle — a live stream
            Capability::Realtime => format!("cap_live((\"{l}\"))"),
            // hexagon — nondeterminism
            Capability::Nondeterminism => format!("cap_nd{{{{\"{l}\"}}}}"),
        }
    }
}

/// One project (`src/`) module and the capability buckets it transitively
/// reaches. `caps` is sorted (via [`BTreeSet`]) for deterministic output.
pub struct ModuleUse {
    pub module: String,
    pub caps: BTreeSet<Capability>,
}

/// The full component graph for a project — the data the renderer consumes. Pure
/// data, so the renderer is a pure function that a unit test can exercise with a
/// hand-built graph (no compile).
pub struct ComponentGraph {
    /// The project path, relative to the repo root when possible.
    pub project: String,
    /// Draw the `Client` / `Server` lanes + the `/_rpc` boundary.
    pub is_spa: bool,
    /// Project modules, sorted by name. Every project module is a node, even one
    /// that touches nothing external (it then reads as an isolated node — itself
    /// useful information).
    pub modules: Vec<ModuleUse>,
    /// The union of every capability any module reaches, sorted.
    pub capabilities: BTreeSet<Capability>,
    /// Non-fatal reader notes (e.g. the analytics-folding note).
    pub notes: Vec<String>,
}

/// The output format for [`render_components`].
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Format {
    /// A bare fenced ```mermaid flowchart.
    Mermaid,
    /// The mermaid fence plus a short Markdown legend + module→capability table.
    Md,
}

/// Does an `[app] target` select a Sky.Spa wasm client (vs a Sky.Live / Tui /
/// Cli backend)? Mirrors the target-family table in AGENTS.md / `Std.App`:
/// `web:app`, any `mobile*`, and the `desktop:<variant>` / `tablet:<variant>`
/// client shells run the wasm client; bare `web` / `desktop` / `tablet` and any
/// `terminal:*` do not.
pub fn target_is_spa_client(target: &str) -> bool {
    let t = target.trim();
    t == "web:app"
        || t == "mobile"
        || t.starts_with("mobile:")
        || t.starts_with("desktop:")
        || t.starts_with("tablet:")
}

/// Build the component graph for a project. Read-only.
///
/// `app_target` is the project's `[app] target` (from `sky.toml`), when known —
/// it decides, together with any explicit `Std.Spa` use, whether the app renders
/// as a client/server split.
pub fn analyze_components(
    repo_root: &Path,
    project_dir: &Path,
    entry_module: Option<&str>,
    app_target: Option<&str>,
) -> Result<ComponentGraph, String> {
    let (db, _entry, check_ids) =
        crate::build::load_source_db(repo_root, project_dir, entry_module)?;
    let project = project_dir
        .strip_prefix(repo_root)
        .unwrap_or(project_dir)
        .to_string_lossy()
        .to_string();

    // ---- 1. per-def DIRECT effects + callees over everything reachable from
    // the project's top-level defs (the walk crosses into stdlib + deps to find
    // the real `Ffi.kernel "<Prefix>_…"` origins, but only project modules
    // become nodes). ----
    let mut direct: HashMap<DefId, BTreeSet<Capability>> = HashMap::new();
    let mut callees: HashMap<DefId, Vec<DefId>> = HashMap::new();
    let mut spa_used = false;

    let mut work: Vec<DefId> = Vec::new();
    let mut seen: HashSet<DefId> = HashSet::new();
    for mid in &check_ids {
        for td in &db.resolve(*mid).top_defs {
            if seen.insert(td.def) {
                work.push(td.def);
            }
        }
    }
    while let Some(def) = work.pop() {
        let home = db.def_loc(def).map(|l| l.module);
        // `Std.Spa` is the wasm-client framework: treat its defs as pure leaves
        // (mirrors the split's own taint walk) so `Spa.postJson`/`getJson` — the
        // client SIDE of an author-drawn RPC boundary — are never charted as an
        // outbound-HTTP capability. Reaching one is the signal that the project
        // targets a Sky.Spa client.
        if let Some(hm) = home {
            if db.module_name(hm) == "Std.Spa" {
                spa_used = true;
                direct.entry(def).or_default();
                callees.entry(def).or_default();
                continue;
            }
        }
        let Some(walk_mid) = home else {
            // Opaque def (no location) — nothing to chart.
            direct.entry(def).or_default();
            callees.entry(def).or_default();
            continue;
        };
        let (fams, cs) = crate::spa_partition::body_effects_and_callees(&db, walk_mid, def);
        let mut caps: BTreeSet<Capability> = BTreeSet::new();
        for f in &fams {
            if let Some(c) = Capability::from_family(f) {
                caps.insert(c);
            }
        }
        direct.insert(def, caps);
        for c in &cs {
            if seen.insert(*c) {
                work.push(*c);
            }
        }
        callees.insert(def, cs);
    }

    // ---- 2. capability fixpoint: a def's capabilities = its own direct effects
    // ∪ every callee's. Iterated to a fixpoint (handles recursion/cycles). ----
    let mut caps: HashMap<DefId, BTreeSet<Capability>> = direct;
    loop {
        let mut changed = false;
        let keys: Vec<DefId> = caps.keys().copied().collect();
        for d in keys {
            let cs = callees.get(&d).cloned().unwrap_or_default();
            let mut add: BTreeSet<Capability> = BTreeSet::new();
            for c in &cs {
                if let Some(cc) = caps.get(c) {
                    add.extend(cc.iter().copied());
                }
            }
            let entry = caps.entry(d).or_default();
            let before = entry.len();
            entry.extend(add);
            if entry.len() != before {
                changed = true;
            }
        }
        if !changed {
            break;
        }
    }

    // ---- 3. per project module: union over its top-level defs' capabilities. --
    let mut modules: Vec<ModuleUse> = Vec::new();
    let mut all_caps: BTreeSet<Capability> = BTreeSet::new();
    for mid in &check_ids {
        let name = db.module_name(*mid).to_string();
        let mut mcaps: BTreeSet<Capability> = BTreeSet::new();
        for td in &db.resolve(*mid).top_defs {
            if let Some(cs) = caps.get(&td.def) {
                mcaps.extend(cs.iter().copied());
            }
        }
        all_caps.extend(mcaps.iter().copied());
        modules.push(ModuleUse {
            module: name,
            caps: mcaps,
        });
    }
    modules.sort_by(|a, b| a.module.cmp(&b.module));

    let is_spa = spa_used || app_target.map(target_is_spa_client).unwrap_or(false);

    let mut notes: Vec<String> = Vec::new();
    if all_caps.contains(&Capability::Telemetry) {
        notes.push(
            "Analytics has no distinct kernel family; it is folded into Telemetry/Logs (Std.Log)."
                .into(),
        );
    }
    if is_spa {
        notes.push(
            "Sky.Spa: every effect runs on the server; the client (wasm) reaches it over /_rpc."
                .into(),
        );
    }

    Ok(ComponentGraph {
        project,
        is_spa,
        modules,
        capabilities: all_caps,
        notes,
    })
}

/// Render a component graph to the requested format. Pure function of `g`.
pub fn render_components(g: &ComponentGraph, format: Format) -> String {
    let mermaid = render_mermaid(g);
    match format {
        Format::Mermaid => mermaid,
        Format::Md => render_md(g, &mermaid),
    }
}

/// A stable, ascii Mermaid node id for a dotted module name.
fn module_node_id(name: &str) -> String {
    let mut s = String::with_capacity(name.len() + 2);
    s.push_str("m_");
    for ch in name.chars() {
        if ch.is_ascii_alphanumeric() {
            s.push(ch);
        } else {
            s.push('_');
        }
    }
    s
}

fn render_mermaid(g: &ComponentGraph) -> String {
    let mut o = String::new();
    o.push_str("```mermaid\n");
    o.push_str("flowchart LR\n");
    o.push_str(&format!(
        "  %% sky doc --diagram components — {}\n",
        g.project
    ));
    if g.is_spa {
        render_spa(g, &mut o);
    } else {
        render_single(g, &mut o);
    }
    o.push_str("```\n");
    o
}

fn render_single(g: &ComponentGraph, o: &mut String) {
    for m in &g.modules {
        o.push_str(&format!(
            "  {}[\"{}\"]\n",
            module_node_id(&m.module),
            m.module
        ));
    }
    for c in &g.capabilities {
        o.push_str(&format!("  {}\n", c.node_decl()));
    }
    for m in &g.modules {
        for c in &m.caps {
            o.push_str(&format!(
                "  {} --> {}\n",
                module_node_id(&m.module),
                c.node_id()
            ));
        }
    }
}

fn render_spa(g: &ComponentGraph, o: &mut String) {
    o.push_str("  subgraph Client[\"Client · wasm\"]\n");
    for m in &g.modules {
        o.push_str(&format!(
            "    {}[\"{}\"]\n",
            module_node_id(&m.module),
            m.module
        ));
    }
    o.push_str("  end\n");
    o.push_str("  rpc{{\"/_rpc\"}}\n");
    o.push_str("  subgraph Server[\"Server · effects\"]\n");
    for c in &g.capabilities {
        o.push_str(&format!("    {}\n", c.node_decl()));
    }
    o.push_str("  end\n");
    // Every module that reaches an effect crosses /_rpc; the server runs it. The
    // per-module → capability mapping is preserved in the `md` table below.
    for m in &g.modules {
        if !m.caps.is_empty() {
            o.push_str(&format!("  {} --> rpc\n", module_node_id(&m.module)));
        }
    }
    for c in &g.capabilities {
        o.push_str(&format!("  rpc --> {}\n", c.node_id()));
    }
}

fn render_md(g: &ComponentGraph, mermaid: &str) -> String {
    let mut o = String::new();
    o.push_str(&format!("# Components — {}\n\n", g.project));
    o.push_str(&format!(
        "App shape: {}\n\n",
        if g.is_spa {
            "Sky.Spa (client/server split over /_rpc)"
        } else {
            "single-process (Sky.Live / Http / Cli)"
        }
    ));
    o.push_str(mermaid);
    o.push('\n');
    o.push_str("## Modules and capabilities\n\n");
    o.push_str("| Module | Capabilities |\n|---|---|\n");
    for m in &g.modules {
        let caps = if m.caps.is_empty() {
            "—".to_string()
        } else {
            m.caps
                .iter()
                .map(|c| c.label())
                .collect::<Vec<_>>()
                .join(", ")
        };
        o.push_str(&format!("| {} | {} |\n", m.module, caps));
    }
    if !g.notes.is_empty() {
        o.push('\n');
        for n in &g.notes {
            o.push_str(&format!("> {n}\n"));
        }
    }
    o
}

// ===========================================================================
// `sky doc --diagram wire` — the Sky.Spa auto-derived RPC contract.
// ===========================================================================
//
// For a Sky.Spa app the compiler splits `update` into a wasm client and a
// server, and every SERVER `update` branch becomes a `POST /_rpc/<Msg>`
// endpoint. That contract is otherwise invisible to the author — a missing
// read (the `SetRegion` request that forgot `basket`) is silent until it
// misbehaves. This slice charts it: one row per endpoint, its REQUEST (the
// Model fields the branch reads + the Msg args) and its RESPONSE (the Model
// fields it writes), so a wrong request/response shape is visible at a glance.
//
// It re-uses [`crate::spa_partition::analyze`] verbatim — the SAME per-branch
// read-set / write-set the auto-split derives, so the diagram cannot drift from
// what actually ships. It never re-derives, type-checks beyond the shared load,
// lowers, emits, or writes.

/// One `/_rpc/<Msg>` endpoint the auto-split derives for a SERVER branch.
pub struct WireEndpoint {
    /// The Msg constructor name (the `<Msg>` in `/_rpc/<Msg>`).
    pub msg: String,
    /// The request payload: read-set fields + Msg args, or "whole model".
    pub request: String,
    /// The response payload: write-set fields, or "whole model".
    pub response: String,
    /// Effects the branch runs (Db / Http / …). `None` for v1 — the partition
    /// report does not surface per-branch effect families cheaply, and this
    /// slice does not invent a new analysis (see `--diagram components`).
    pub effects: Option<String>,
}

/// The wire contract for a project — pure data the renderer consumes.
pub struct WireReport {
    /// The project path, relative to the repo root when possible.
    pub project: String,
    /// True when the resolved target is a Sky.Spa wasm client. When false the
    /// app has no `/_rpc` contract to chart and only [`WireReport::notes`] is
    /// rendered.
    pub is_spa: bool,
    /// The resolved `[app] target` (or the `--target` override), for the note.
    pub target: Option<String>,
    /// The RPC endpoints, sorted by Msg name for deterministic output.
    pub endpoints: Vec<WireEndpoint>,
    /// Set when the app IS a Spa client but no per-branch endpoints could be
    /// recovered (a `Std.App` inline-effect shape, or an `update` that is not a
    /// resolvable `case msg of`). The renderer prints what it has plus a note.
    pub limited: bool,
    /// Non-fatal reader notes.
    pub notes: Vec<String>,
}

/// Render the request shape of a branch from its public [`crate::spa_partition::BranchIo`]
/// fields — the read-set (or the whole model) plus the Msg args.
fn wire_request(io: &crate::spa_partition::BranchIo) -> String {
    let mut parts: Vec<String> = Vec::new();
    if io.reads_whole_model {
        parts.push("whole model".to_string());
    } else if !io.read_fields.is_empty() {
        parts.push(fmt_fields(&io.read_fields));
    }
    if !io.msg_args.is_empty() {
        parts.push(fmt_fields(&io.msg_args));
    }
    if parts.is_empty() {
        "{}".to_string()
    } else {
        parts.join(" + ")
    }
}

/// Render the response shape — the write-set, or the whole model.
fn wire_response(io: &crate::spa_partition::BranchIo) -> String {
    if io.writes_whole_model {
        "whole model".to_string()
    } else {
        fmt_fields(&io.write_fields)
    }
}

fn fmt_fields(items: &[String]) -> String {
    format!("{{{}}}", items.join(", "))
}

/// Build the wire contract for a project. Read-only.
///
/// `app_target` (the `[app] target`, or a `--target` override) decides whether
/// the app renders as a Sky.Spa client: the RPC contract only exists for a wasm
/// client target. A non-Spa app returns `is_spa = false` and only notes — the
/// analysis is NOT run, since there is no `/_rpc` boundary to chart.
pub fn analyze_wire(
    repo_root: &Path,
    project_dir: &Path,
    entry_module: Option<&str>,
    app_target: Option<&str>,
) -> Result<WireReport, String> {
    let project = project_dir
        .strip_prefix(repo_root)
        .unwrap_or(project_dir)
        .to_string_lossy()
        .to_string();
    let is_spa = app_target.map(target_is_spa_client).unwrap_or(false);

    if !is_spa {
        let tgt = app_target.unwrap_or("<none>");
        return Ok(WireReport {
            project,
            is_spa: false,
            target: app_target.map(str::to_string),
            endpoints: Vec::new(),
            limited: false,
            notes: vec![
                format!(
                    "`wire` charts the Sky.Spa RPC contract — the `/_rpc` boundary a wasm \
                     client calls. This project's target (`{tgt}`) is not a Sky.Spa wasm \
                     client, so it has no RPC contract to chart."
                ),
                "A Sky.Live app's \"wire\" is the SSE / session channel, not an RPC \
                 contract; a Sky.Http / Cli app has no client boundary at all."
                    .to_string(),
                "Re-run with `--target web:app` (or a `mobile:` / `desktop:` / `tablet:` \
                 client) to chart the Sky.Spa client contract."
                    .to_string(),
            ],
        });
    }

    let report = crate::spa_partition::analyze(repo_root, project_dir, entry_module)?;

    let mut endpoints: Vec<WireEndpoint> = Vec::new();
    for b in &report.branches {
        // Only SERVER branches carry an `/_rpc` endpoint; CLIENT arms run in the
        // browser with no round-trip (`io == None`).
        let Some(io) = &b.io else { continue };
        if !b.server {
            continue;
        }
        // `b.msg` is the arm pattern (`"SaveVia _"`); the endpoint is named by
        // the constructor alone (`SaveVia`).
        let ctor = b.msg.split_whitespace().next().unwrap_or(&b.msg).to_string();
        endpoints.push(WireEndpoint {
            msg: ctor,
            request: wire_request(io),
            response: wire_response(io),
            effects: None,
        });
    }
    endpoints.sort_by(|a, b| a.msg.cmp(&b.msg));

    let mut notes: Vec<String> = Vec::new();
    let mut limited = false;
    if endpoints.is_empty() {
        limited = true;
        if report.whole_update.is_some() {
            notes.push(
                "`update` is not a resolvable `case msg of` (a lambda / delegating shape), \
                 so per-branch wire extraction is unavailable for this app."
                    .to_string(),
            );
        } else {
            notes.push(
                "No SERVER `update` branches were surfaced (a `Std.App` inline-effect shape); \
                 per-branch wire extraction is limited for this app shape."
                    .to_string(),
            );
        }
    }
    notes.push(
        "Per-branch effects (Db / Http / …) are not surfaced by the partition report; \
         see `sky doc --diagram components` for the app's capability buckets."
            .to_string(),
    );

    Ok(WireReport {
        project,
        is_spa: true,
        target: app_target.map(str::to_string),
        endpoints,
        limited,
        notes,
    })
}

/// Render a wire report to the requested format. Pure function of `r`.
///
/// `wire`'s useful form is a table, so the default (used when the CLI is given
/// no `--format`) is [`Format::Md`]; [`Format::Mermaid`] emits a sequence
/// diagram of the same endpoints.
pub fn render_wire(r: &WireReport, format: Format) -> String {
    match format {
        Format::Mermaid => render_wire_mermaid(r),
        Format::Md => render_wire_md(r),
    }
}

fn render_wire_md(r: &WireReport) -> String {
    let mut o = String::new();
    o.push_str(&format!("# Wire (RPC contract) — {}\n\n", r.project));
    // A non-Spa app, or a Spa app whose per-branch endpoints could not be
    // recovered, has no table to draw — carry the notes alone.
    if !r.is_spa || r.endpoints.is_empty() {
        for n in &r.notes {
            o.push_str(&format!("> {n}\n"));
        }
        return o;
    }
    o.push_str(
        "The Sky.Spa auto-split turns every SERVER `update` branch into a \
         `POST /_rpc/<Msg>` endpoint. The REQUEST is the Model fields the branch \
         reads plus the Msg args; the RESPONSE is the Model fields it writes.\n\n",
    );
    o.push_str("| Endpoint | Request (reads + args) | Response (writes) | Effects |\n");
    o.push_str("|---|---|---|---|\n");
    for e in &r.endpoints {
        o.push_str(&format!(
            "| POST /_rpc/{} | {} | {} | {} |\n",
            e.msg,
            e.request,
            e.response,
            e.effects.as_deref().unwrap_or("—")
        ));
    }
    if !r.notes.is_empty() {
        o.push('\n');
        for n in &r.notes {
            o.push_str(&format!("> {n}\n"));
        }
    }
    o
}

fn render_wire_mermaid(r: &WireReport) -> String {
    let mut o = String::new();
    o.push_str("```mermaid\n");
    o.push_str("sequenceDiagram\n");
    o.push_str(&format!("  %% sky doc --diagram wire — {}\n", r.project));
    if !r.is_spa {
        // A sequence diagram with no messages is empty; carry the note instead.
        o.push_str("  note over Client,Server: not a Sky.Spa wasm client — no /_rpc contract\n");
        o.push_str("```\n");
        for n in &r.notes {
            o.push_str(&format!("\n> {n}\n"));
        }
        return o;
    }
    o.push_str("  participant Client as Client · wasm\n");
    o.push_str("  participant Server as Server · effects\n");
    if r.endpoints.is_empty() {
        o.push_str("  note over Client,Server: no per-branch /_rpc endpoints surfaced\n");
    }
    for e in &r.endpoints {
        o.push_str(&format!(
            "  Client->>Server: POST /_rpc/{} {}\n",
            e.msg, e.request
        ));
        o.push_str(&format!("  Server-->>Client: {}\n", e.response));
    }
    o.push_str("```\n");
    if !r.notes.is_empty() {
        for n in &r.notes {
            o.push_str(&format!("\n> {n}\n"));
        }
    }
    o
}

#[cfg(test)]
mod tests {
    use super::*;

    fn graph(is_spa: bool) -> ComponentGraph {
        let mut api = BTreeSet::new();
        api.insert(Capability::Database);
        api.insert(Capability::Auth);
        let view: BTreeSet<Capability> = BTreeSet::new();
        ComponentGraph {
            project: "examples/demo".into(),
            is_spa,
            modules: vec![
                ModuleUse {
                    module: "Api".into(),
                    caps: api,
                },
                ModuleUse {
                    module: "View".into(),
                    caps: view,
                },
            ],
            capabilities: {
                let mut s = BTreeSet::new();
                s.insert(Capability::Database);
                s.insert(Capability::Auth);
                s
            },
            notes: vec![],
        }
    }

    #[test]
    fn single_lane_has_capability_nodes_and_a_module_edge() {
        let out = render_components(&graph(false), Format::Mermaid);
        assert!(out.contains("flowchart LR"), "{out}");
        // capability nodes present (distinct shapes)
        assert!(out.contains("cap_db[(\"Database\")]"), "{out}");
        assert!(out.contains("cap_auth{{\"Auth\"}}"), "{out}");
        // at least one module -> capability edge
        assert!(out.contains("m_Api --> cap_db"), "{out}");
        // single-lane never draws the RPC boundary
        assert!(!out.contains("subgraph Client"), "{out}");
        assert!(!out.contains("/_rpc"), "{out}");
    }

    #[test]
    fn spa_has_both_subgraphs_and_the_rpc_boundary() {
        let out = render_components(&graph(true), Format::Mermaid);
        assert!(out.contains("subgraph Client"), "{out}");
        assert!(out.contains("subgraph Server"), "{out}");
        assert!(out.contains("rpc{{\"/_rpc\"}}"), "{out}");
        // client module crosses the boundary; the server runs the effect
        assert!(out.contains("m_Api --> rpc"), "{out}");
        assert!(out.contains("rpc --> cap_db"), "{out}");
    }

    #[test]
    fn md_format_adds_a_module_table() {
        let out = render_components(&graph(false), Format::Md);
        assert!(out.contains("| Module | Capabilities |"), "{out}");
        // capabilities render in the enum's declaration order (deterministic).
        assert!(out.contains("| Api | Database, Auth |"), "{out}");
        assert!(out.contains("| View | — |"), "{out}");
        // it still embeds the mermaid fence
        assert!(out.contains("```mermaid"), "{out}");
    }

    #[test]
    fn target_family_detection() {
        assert!(target_is_spa_client("web:app"));
        assert!(target_is_spa_client("mobile:ios"));
        assert!(target_is_spa_client("desktop:mac"));
        assert!(target_is_spa_client("tablet:ipad"));
        assert!(!target_is_spa_client("web"));
        assert!(!target_is_spa_client("desktop"));
        assert!(!target_is_spa_client("terminal:cli"));
    }

    #[test]
    fn family_mapping_covers_the_buckets_and_folds_the_rest() {
        assert_eq!(Capability::from_family("Db"), Some(Capability::Database));
        assert_eq!(
            Capability::from_family("Http"),
            Some(Capability::ExternalHttp)
        );
        assert_eq!(
            Capability::from_family("System"),
            Some(Capability::EnvConfig)
        );
        assert_eq!(
            Capability::from_family("Time"),
            Some(Capability::Nondeterminism)
        );
        assert_eq!(
            Capability::from_family("Uuid"),
            Some(Capability::Nondeterminism)
        );
        // outside the components buckets → dropped
        assert_eq!(Capability::from_family("Server"), None);
        assert_eq!(Capability::from_family("Native"), None);
    }

    fn wire_report() -> WireReport {
        WireReport {
            project: "examples/demo".into(),
            is_spa: true,
            target: Some("web:app".into()),
            endpoints: vec![
                WireEndpoint {
                    // a narrowed request/response
                    msg: "SetRegion".into(),
                    request: "{basket, region} + {region}".into(),
                    response: "{basket, region}".into(),
                    effects: None,
                },
                WireEndpoint {
                    // an over-approximated (whole-model) branch
                    msg: "SaveAll".into(),
                    request: "whole model".into(),
                    response: "whole model".into(),
                    effects: None,
                },
            ],
            limited: false,
            notes: vec!["a note".into()],
        }
    }

    #[test]
    fn wire_md_has_the_rpc_table_with_request_and_response() {
        let out = render_wire(&wire_report(), Format::Md);
        assert!(
            out.contains("| Endpoint | Request (reads + args) | Response (writes) | Effects |"),
            "{out}"
        );
        assert!(
            out.contains("| POST /_rpc/SetRegion | {basket, region} + {region} | {basket, region} | — |"),
            "{out}"
        );
        assert!(
            out.contains("| POST /_rpc/SaveAll | whole model | whole model | — |"),
            "{out}"
        );
    }

    #[test]
    fn wire_mermaid_is_a_sequence_diagram_per_endpoint() {
        let out = render_wire(&wire_report(), Format::Mermaid);
        assert!(out.contains("sequenceDiagram"), "{out}");
        assert!(
            out.contains("Client->>Server: POST /_rpc/SetRegion {basket, region} + {region}"),
            "{out}"
        );
        assert!(out.contains("Server-->>Client: {basket, region}"), "{out}");
    }

    #[test]
    fn wire_non_spa_prints_a_note_not_a_table() {
        let r = WireReport {
            project: "examples/demo".into(),
            is_spa: false,
            target: Some("web".into()),
            endpoints: vec![],
            limited: false,
            notes: vec!["not a Sky.Spa wasm client".into()],
        };
        let out = render_wire(&r, Format::Md);
        assert!(!out.contains("| Endpoint |"), "{out}");
        assert!(out.contains("not a Sky.Spa wasm client"), "{out}");
    }
}
