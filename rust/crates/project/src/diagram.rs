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

use crate::diagram_svg;
use base::{DefId, ModuleId};
use hir::{Body, Expr, ExprId, LocalDef, PatId, Pattern, Res, SkyDb};
use std::collections::{BTreeSet, HashMap, HashSet};
use std::path::Path;
use syntax::ast;

/// A single external capability bucket a module can touch. The set is
/// deliberately small + fixed so the diagram stays readable — one node per
/// bucket, never a per-function hairball.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
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

    /// A PlantUML node declaration with a distinct KIND per capability, so a
    /// capability reads differently from the plain-rectangle module nodes:
    /// Database→database, External HTTP→cloud, Auth→component <<security>>,
    /// File→folder, Env/Config + Time/Random/Uuid→card, Telemetry + Jobs +
    /// Realtime→queue.
    fn puml_decl(self) -> String {
        let l = self.label();
        let id = self.node_id();
        match self {
            Capability::Database => format!("database \"{l}\" as {id}"),
            Capability::ExternalHttp => format!("cloud \"{l}\" as {id}"),
            Capability::Auth => format!("component \"{l}\" as {id} <<security>>"),
            Capability::File => format!("folder \"{l}\" as {id}"),
            Capability::EnvConfig => format!("card \"{l}\" as {id}"),
            Capability::Telemetry => format!("queue \"{l}\" as {id}"),
            Capability::Jobs => format!("queue \"{l}\" as {id}"),
            Capability::Realtime => format!("queue \"{l}\" as {id}"),
            Capability::Nondeterminism => format!("card \"{l}\" as {id}"),
        }
    }

    /// Which SVG shape draws this capability — a datastore cylinder, a queue, or
    /// a plain node.
    fn svg_shape(self) -> SvgShape {
        match self {
            Capability::Database => SvgShape::Database,
            Capability::Telemetry | Capability::Jobs | Capability::Realtime => SvgShape::Queue,
            _ => SvgShape::Node,
        }
    }
}

/// The three distinct SVG shapes a capability / sink node can take.
#[derive(Clone, Copy, PartialEq, Eq)]
enum SvgShape {
    Node,
    Database,
    Queue,
}

/// Draw one shaped node into the SVG at `(x, y)` with size `(w, h)`.
fn svg_shaped_node(svg: &mut diagram_svg::Svg, shape: SvgShape, x: f64, y: f64, w: f64, h: f64, label: &str) {
    match shape {
        SvgShape::Node => svg.node(x, y, w, h, diagram_svg::FILL_ALT, diagram_svg::STROKE, label, None),
        SvgShape::Database => svg.database(x, y, w, h, diagram_svg::FILL_ALT, diagram_svg::STROKE, label),
        SvgShape::Queue => svg.queue(x, y, w, h, diagram_svg::FILL_ALT, diagram_svg::STROKE, label),
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

/// The output format for the diagram renderers.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Format {
    /// A raw PlantUML document (`@startuml … @enduml`) with no prose, so it
    /// pipes straight into a `.puml` file.
    Puml,
    /// A Markdown document: a table (plus notes) meant to be read as-is.
    Md,
    /// A self-contained SVG we draw ourselves (no external tool).
    Svg,
}

/// The PlantUML `skinparam` block shared by every kind — a clean, monochrome,
/// shadow-free look with rounded rectangles and a readable font.
fn puml_skin() -> &'static str {
    "skinparam backgroundColor #ffffff\n\
     skinparam shadowing false\n\
     skinparam defaultFontName Helvetica\n\
     skinparam defaultFontSize 12\n\
     skinparam roundCorner 8\n\
     skinparam ArrowColor #444444\n\
     skinparam ArrowFontColor #444444\n\
     skinparam componentStyle rectangle\n\
     skinparam RectangleBackgroundColor #ffffff\n\
     skinparam RectangleBorderColor #333333\n\
     skinparam ComponentBackgroundColor #ffffff\n\
     skinparam ComponentBorderColor #333333\n\
     skinparam DatabaseBackgroundColor #ffffff\n\
     skinparam DatabaseBorderColor #333333\n\
     skinparam QueueBackgroundColor #ffffff\n\
     skinparam QueueBorderColor #333333\n\
     skinparam CardBackgroundColor #ffffff\n\
     skinparam CardBorderColor #333333\n\
     skinparam FolderBackgroundColor #ffffff\n\
     skinparam FolderBorderColor #333333\n\
     skinparam CloudBackgroundColor #ffffff\n\
     skinparam CloudBorderColor #333333\n\
     skinparam StateBackgroundColor #ffffff\n\
     skinparam StateBorderColor #333333\n\
     skinparam PackageBackgroundColor #fbfbfc\n\
     skinparam PackageBorderColor #c4c7cc\n\
     skinparam InterfaceBackgroundColor #ffffff\n\
     skinparam InterfaceBorderColor #333333\n\
     skinparam ParticipantBorderColor #333333\n\
     skinparam ParticipantBackgroundColor #ffffff\n\
     skinparam LifeLineBorderColor #333333\n"
}

/// Build a raw PlantUML header: `@startuml`, one concise `title` line, and the
/// shared skin. No prose, no fences — the output is a valid `.puml` file.
fn puml_header(title: &str) -> String {
    format!("@startuml\ntitle {title}\n{}", puml_skin())
}

fn puml_footer() -> String {
    "@enduml\n".to_string()
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
    match format {
        Format::Puml => render_components_puml(g),
        Format::Md => render_components_md(g),
        Format::Svg => render_components_svg(g),
    }
}

/// A stable, ascii node id for a dotted module name.
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

fn render_components_puml(g: &ComponentGraph) -> String {
    let mut o = puml_header(&format!("Components — {}", g.project));
    if g.is_spa {
        o.push_str("package \"Client · wasm\" {\n");
        for m in &g.modules {
            o.push_str(&format!("  component \"{}\" as {}\n", m.module, module_node_id(&m.module)));
        }
        o.push_str("}\n");
        o.push_str("interface \"/_rpc\" as rpc\n");
        o.push_str("package \"Server · effects\" {\n");
        for c in &g.capabilities {
            o.push_str(&format!("  {}\n", c.puml_decl()));
        }
        o.push_str("}\n");
        for m in &g.modules {
            if !m.caps.is_empty() {
                o.push_str(&format!("{} --> rpc\n", module_node_id(&m.module)));
            }
        }
        for c in &g.capabilities {
            o.push_str(&format!("rpc --> {}\n", c.node_id()));
        }
    } else {
        for m in &g.modules {
            o.push_str(&format!("component \"{}\" as {}\n", m.module, module_node_id(&m.module)));
        }
        for c in &g.capabilities {
            o.push_str(&format!("{}\n", c.puml_decl()));
        }
        for m in &g.modules {
            for c in &m.caps {
                o.push_str(&format!("{} --> {}\n", module_node_id(&m.module), c.node_id()));
            }
        }
    }
    o.push_str(&puml_footer());
    o
}

fn render_components_md(g: &ComponentGraph) -> String {
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
    o.push_str("| Module | Capabilities |\n|---|---|\n");
    for m in &g.modules {
        let caps = if m.caps.is_empty() {
            "—".to_string()
        } else {
            m.caps.iter().map(|c| c.label()).collect::<Vec<_>>().join(", ")
        };
        o.push_str(&format!("| {} | {} |\n", md_cell(&m.module), md_cell(&caps)));
    }
    if !g.notes.is_empty() {
        o.push('\n');
        for n in &g.notes {
            o.push_str(&format!("> {n}\n"));
        }
    }
    o
}

const NODE_H: f64 = 44.0;
const VGAP: f64 = 18.0;

fn render_components_svg(g: &ComponentGraph) -> String {
    let mut svg = diagram_svg::Svg::new(&format!("Components — {}", g.project));
    let mod_w = 160.0;
    let cap_w = 160.0;

    if g.is_spa {
        // Three columns inside light package boxes: Client → /_rpc → Server.
        let pad = 14.0;
        let hdr = 26.0;
        let client_x = 0.0;
        let client_inner_x = client_x + pad;
        let n_mod = g.modules.len().max(1) as f64;
        let n_cap = g.capabilities.len().max(1) as f64;
        let client_h = hdr + pad + n_mod * NODE_H + (n_mod - 1.0) * VGAP + pad;
        let server_h = hdr + pad + n_cap * NODE_H + (n_cap - 1.0) * VGAP + pad;
        let lane_h = client_h.max(server_h);
        let rpc_x = client_x + mod_w + 2.0 * pad + 60.0;
        let rpc_w = 90.0;
        let server_x = rpc_x + rpc_w + 60.0;
        let server_inner_x = server_x + pad;

        svg.package(client_x, 0.0, mod_w + 2.0 * pad, lane_h, "Client · wasm");
        let mut mod_cy: Vec<f64> = Vec::new();
        for (i, m) in g.modules.iter().enumerate() {
            let y = hdr + pad + i as f64 * (NODE_H + VGAP);
            svg.node(client_inner_x, y, mod_w, NODE_H, diagram_svg::FILL, diagram_svg::STROKE, &m.module, None);
            mod_cy.push(y + NODE_H / 2.0);
        }

        // /_rpc boundary node, vertically centred.
        let rpc_h = 40.0;
        let rpc_y = (lane_h - rpc_h) / 2.0;
        svg.node(rpc_x, rpc_y, rpc_w, rpc_h, diagram_svg::FILL_ALT, diagram_svg::STROKE, "/_rpc", None);
        let rpc_cy = rpc_y + rpc_h / 2.0;

        svg.package(server_x, 0.0, cap_w + 2.0 * pad, lane_h, "Server · effects");
        let mut cap_cy: Vec<f64> = Vec::new();
        for (j, c) in g.capabilities.iter().enumerate() {
            let y = hdr + pad + j as f64 * (NODE_H + VGAP);
            svg_shaped_node(&mut svg, c.svg_shape(), server_inner_x, y, cap_w, NODE_H, c.label());
            cap_cy.push(y + NODE_H / 2.0);
        }

        // module → /_rpc (only modules that reach an effect), then /_rpc → cap.
        for (i, m) in g.modules.iter().enumerate() {
            if !m.caps.is_empty() {
                svg.edge(client_inner_x + mod_w, mod_cy[i], rpc_x, rpc_cy, diagram_svg::SERVER_EDGE, None);
            }
        }
        for j in 0..g.capabilities.len() {
            svg.edge(rpc_x + rpc_w, rpc_cy, server_inner_x, cap_cy[j], diagram_svg::SERVER_EDGE, None);
        }
    } else {
        // Two layered columns: modules → capabilities.
        let mod_x = 0.0;
        let cap_x = mod_x + mod_w + 170.0;
        let mut mod_cy: Vec<f64> = Vec::new();
        for (i, m) in g.modules.iter().enumerate() {
            let y = i as f64 * (NODE_H + VGAP);
            svg.node(mod_x, y, mod_w, NODE_H, diagram_svg::FILL, diagram_svg::STROKE, &m.module, None);
            mod_cy.push(y + NODE_H / 2.0);
        }
        let caps: Vec<Capability> = g.capabilities.iter().copied().collect();
        let mut cap_cy: std::collections::HashMap<Capability, f64> = std::collections::HashMap::new();
        for (j, c) in caps.iter().enumerate() {
            let y = j as f64 * (NODE_H + VGAP);
            svg_shaped_node(&mut svg, c.svg_shape(), cap_x, y, cap_w, NODE_H, c.label());
            cap_cy.insert(*c, y + NODE_H / 2.0);
        }
        for (i, m) in g.modules.iter().enumerate() {
            for c in &m.caps {
                if let Some(cy) = cap_cy.get(c) {
                    svg.edge(mod_x + mod_w, mod_cy[i], cap_x, *cy, diagram_svg::STROKE, None);
                }
            }
        }
    }
    svg.render()
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

/// One HTTP route a non-Spa server app registers (`Server.get "/path" handler`).
/// This is the `wire` diagram's answer for a Sky.Http.Server / API-only app,
/// which has no `/_rpc` contract but does have an HTTP endpoint map.
#[derive(Clone, PartialEq, Eq, PartialOrd, Ord, Debug)]
pub struct HttpEndpoint {
    /// The HTTP method (`GET`, `POST`, `PUT`, `DELETE`, `ANY`).
    pub method: String,
    /// The route path (`/`, `/hello/:name`).
    pub path: String,
    /// The handler function name, or `<inline>` for a lambda / non-def handler.
    pub handler: String,
}

/// The wire contract for a project — pure data the renderer consumes.
pub struct WireReport {
    /// The project path, relative to the repo root when possible.
    pub project: String,
    /// True when the resolved target is a Sky.Spa wasm client. When false the
    /// app has no `/_rpc` contract, but it may still have an HTTP endpoint map
    /// ([`WireReport::http_endpoints`]).
    pub is_spa: bool,
    /// The resolved `[app] target` (or the `--target` override), for the note.
    pub target: Option<String>,
    /// The RPC endpoints, sorted by Msg name for deterministic output.
    pub endpoints: Vec<WireEndpoint>,
    /// For a non-Spa Sky.Http.Server / API-only app: the registered HTTP routes,
    /// sorted for deterministic output. Empty for a Spa app or an app with no
    /// discoverable route registration.
    pub http_endpoints: Vec<HttpEndpoint>,
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
        // A non-Spa app has no `/_rpc` contract. But a Sky.Http.Server / API-only
        // app DOES have an HTTP endpoint map — recover it from the resolved HIR
        // (`Server.get "/path" handler`, `Server.post`, …). If none is found the
        // app has no client boundary at all, and we degrade to a single note.
        let http_endpoints = analyze_http_endpoints(repo_root, project_dir, entry_module)
            .unwrap_or_default();
        let mut notes: Vec<String> = Vec::new();
        if http_endpoints.is_empty() {
            let tgt = app_target.unwrap_or("<none>");
            notes.push(format!(
                "`wire` charts an app's client boundary. This project's target (`{tgt}`) is \
                 not a Sky.Spa wasm client (no `/_rpc` contract) and no Sky.Http.Server route \
                 registration was found, so there is no endpoint map to chart."
            ));
            notes.push(
                "Re-run with `--target web:app` (or a `mobile:` / `desktop:` / `tablet:` \
                 client) to chart the Sky.Spa `/_rpc` contract, or add `Server.get`/`Server.post` \
                 routes for an HTTP endpoint map."
                    .to_string(),
            );
        }
        return Ok(WireReport {
            project,
            is_spa: false,
            target: app_target.map(str::to_string),
            endpoints: Vec::new(),
            http_endpoints,
            limited: false,
            notes,
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
        http_endpoints: Vec::new(),
        limited,
        notes,
    })
}

/// Recover a non-Spa app's HTTP endpoint map from the resolved HIR: every
/// `Sky.Http.Server.{get,post,put,delete,any,api}` call gives a method, a path
/// (its string-literal first argument), and a handler (the second argument's def
/// name, or `<inline>`). Read-only. Returns an empty vec for an app that
/// registers no routes.
fn analyze_http_endpoints(
    repo_root: &Path,
    project_dir: &Path,
    entry_module: Option<&str>,
) -> Result<Vec<HttpEndpoint>, String> {
    let (db, _entry, check_ids) =
        crate::build::load_source_db(repo_root, project_dir, entry_module)?;
    let mut out: BTreeSet<HttpEndpoint> = BTreeSet::new();
    for mid in &check_ids {
        let resolved = db.resolve(*mid);
        for (_d, body) in &resolved.bodies {
            let Some(root) = body.root else { continue };
            let mut ids: Vec<ExprId> = Vec::new();
            walk_exprs(body, root, &mut |e| ids.push(e));
            for e in ids {
                let Expr::Call(callee, args) = &body.exprs[e] else {
                    continue;
                };
                let Expr::Var(Res::Def(d)) = &body.exprs[*callee] else {
                    continue;
                };
                let Some(loc) = db.def_loc(*d) else { continue };
                if db.module_name(loc.module) != "Sky.Http.Server" {
                    continue;
                }
                let fname = loc.name.as_str();
                let verb = match fname {
                    "get" => "GET",
                    "post" => "POST",
                    "put" => "PUT",
                    "delete" => "DELETE",
                    "any" => "ANY",
                    "api" => "API",
                    _ => continue,
                };
                if args.is_empty() {
                    continue;
                }
                let Expr::Str(spec) = &body.exprs[args[0]] else {
                    continue;
                };
                let spec = spec.to_string();
                let (method, path) = if verb == "API" {
                    // `api "METHOD /path"` — split the method off the spec.
                    let mut it = spec.splitn(2, char::is_whitespace);
                    let first = it.next().unwrap_or("").to_string();
                    match it.next() {
                        Some(rest) if !rest.trim().is_empty() => {
                            (first.to_uppercase(), rest.trim().to_string())
                        }
                        _ => ("ANY".to_string(), spec.clone()),
                    }
                } else {
                    (verb.to_string(), spec.clone())
                };
                let handler = args
                    .get(1)
                    .and_then(|a| match &body.exprs[*a] {
                        Expr::Var(Res::Def(hd)) => {
                            db.def_loc(*hd).map(|l| l.name.as_str().to_string())
                        }
                        _ => None,
                    })
                    .unwrap_or_else(|| "<inline>".to_string());
                out.insert(HttpEndpoint { method, path, handler });
            }
        }
    }
    Ok(out.into_iter().collect())
}

/// Render a wire report to the requested format. Pure function of `r`.
pub fn render_wire(r: &WireReport, format: Format) -> String {
    match format {
        Format::Puml => render_wire_puml(r),
        Format::Md => render_wire_md(r),
        Format::Svg => render_wire_svg(r),
    }
}

fn render_wire_md(r: &WireReport) -> String {
    let mut o = String::new();
    o.push_str(&format!("# Wire — {}\n\n", r.project));
    if r.is_spa && !r.endpoints.is_empty() {
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
                md_cell(&e.msg),
                md_cell(&e.request),
                md_cell(&e.response),
                e.effects.as_deref().unwrap_or("—")
            ));
        }
    } else if !r.http_endpoints.is_empty() {
        o.push_str("HTTP endpoint map — every route this server registers.\n\n");
        o.push_str("| Method | Path | Handler |\n|---|---|---|\n");
        for e in &r.http_endpoints {
            o.push_str(&format!(
                "| {} | {} | {} |\n",
                md_cell(&e.method),
                md_cell(&e.path),
                md_cell(&e.handler)
            ));
        }
    }
    if !r.notes.is_empty() {
        o.push('\n');
        for n in &r.notes {
            o.push_str(&format!("> {n}\n"));
        }
    }
    o
}

fn render_wire_puml(r: &WireReport) -> String {
    let mut o = puml_header(&format!("Wire — {}", r.project));
    if r.is_spa && !r.endpoints.is_empty() {
        o.push_str("participant \"Client · wasm\" as C\n");
        o.push_str("participant Server as S\n");
        for e in &r.endpoints {
            o.push_str(&format!("== {} ==\n", e.msg));
            o.push_str(&format!("C -> S : POST /_rpc/{}\n", e.msg));
            o.push_str(&format!(
                "note right of S\n  reads {}\n  args {}\nend note\n",
                puml_note_text(&e.request),
                e.msg
            ));
            o.push_str(&format!("S --> C : writes {}\n", puml_msg_text(&e.response)));
        }
    } else if !r.http_endpoints.is_empty() {
        o.push_str("participant Client as C\n");
        o.push_str("participant Server as S\n");
        for e in &r.http_endpoints {
            o.push_str(&format!("C -> S : {} {}\n", e.method, e.path));
            o.push_str(&format!("S --> C : {}\n", e.handler));
        }
    } else {
        // Nothing to chart — a single concise note keeps the file valid.
        o.push_str("note over \"Client\",\"Server\" : no client boundary to chart\n");
    }
    o.push_str(&puml_footer());
    o
}

/// Sanitise a request/response payload string for a PlantUML `note` body: strip
/// characters that break the note grammar.
fn puml_note_text(s: &str) -> String {
    s.replace('\n', " ").replace('\r', " ")
}

/// Sanitise a message-label string for a PlantUML `->` arrow label.
fn puml_msg_text(s: &str) -> String {
    s.replace('\n', " ").replace('\r', " ").replace(':', " ")
}

fn render_wire_svg(r: &WireReport) -> String {
    let mut svg = diagram_svg::Svg::new(&format!("Wire — {}", r.project));
    let client_x = 140.0;
    let server_x = 460.0;
    let head_h = 34.0;

    let spa = r.is_spa && !r.endpoints.is_empty();
    let http = !spa && !r.http_endpoints.is_empty();
    if !spa && !http {
        svg.text(0.0, 20.0, "No client boundary to chart.", "start", 12.0, "400", diagram_svg::SUBTLE);
        return svg.render();
    }

    // Two lifeline heads.
    let (left_label, right_label) = if spa {
        ("Client · wasm", "Server")
    } else {
        ("Client", "Server")
    };
    svg.node(client_x - 70.0, 0.0, 140.0, head_h, diagram_svg::FILL_ALT, diagram_svg::STROKE, left_label, None);
    svg.node(server_x - 70.0, 0.0, 140.0, head_h, diagram_svg::FILL_ALT, diagram_svg::STROKE, right_label, None);

    let n = if spa { r.endpoints.len() } else { r.http_endpoints.len() };
    let step = 64.0;
    let top = head_h + 24.0;
    let bottom = top + (n as f64) * step + 8.0;

    // Lifelines.
    svg.edge(client_x, head_h, client_x, bottom, diagram_svg::PKG_STROKE, None);
    svg.edge(server_x, head_h, server_x, bottom, diagram_svg::PKG_STROKE, None);

    let mut y = top + 12.0;
    if spa {
        for e in &r.endpoints {
            svg.edge(client_x, y, server_x, y, diagram_svg::SERVER_EDGE, Some(&format!("POST /_rpc/{}", e.msg)));
            svg.caption(server_x + 12.0, y - 4.0, &format!("reads {}", e.request), "start");
            let ry = y + 26.0;
            svg.edge(server_x, ry, client_x, ry, diagram_svg::CLIENT_EDGE, Some(&format!("writes {}", e.response)));
            y += step;
        }
    } else {
        for e in &r.http_endpoints {
            svg.edge(client_x, y, server_x, y, diagram_svg::SERVER_EDGE, Some(&format!("{} {}", e.method, e.path)));
            let ry = y + 26.0;
            svg.edge(server_x, ry, client_x, ry, diagram_svg::CLIENT_EDGE, Some(&e.handler));
            y += step;
        }
    }
    svg.render()
}

// ===========================================================================
// `sky doc --diagram telemetry` — the privacy / observability inventory.
// ===========================================================================
//
// What does this app track or log, from where, and to which sink? For a shop
// with a consent banner that is the question a privacy review asks: what
// behavioural / telemetry data do we capture, and where does it go. This slice
// answers it as a flat inventory — one row per telemetry / analytics / logging
// CALL SITE in the user's own modules: the module the call is in, the call
// (`Log.info`, `Analytics.track`, …), the event/message (its first string
// literal argument when it is a literal, else `<dynamic>`), and the sink.
//
// Detection is a read-only walk over each project module's resolved HIR (the
// SAME source db the build loads). A call is telemetry when its callee resolves
// to a def in `Std.Log` or `Std.Analytics` (both are ordinary Sky-source stdlib
// modules whose functions are `Ffi.kernel "Log_…"` / `"Analytics_…"`, so a user
// call resolves to a `Res::Def` there — reliably detectable, not best-effort).
// A call routed through a user's own helper is captured at that helper (its own
// call site), which is where the data flow actually originates in the app.
//
// These are effect kernels, so under a Sky.Spa split they all run on the SERVER,
// reached from the wasm client over `/_rpc` — the render carries that as a note.
// It never type-checks beyond the shared load, lowers, emits, or writes.

/// Where a telemetry / analytics / logging call sends its data. Coarse but
/// honest — one node per real destination.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub enum Sink {
    /// `Log.*` — structured logs (console; OTel when the endpoint is set).
    Logs,
    /// `Analytics.track` / `.trackEvent` / `.identify` / … — the analytics store.
    Analytics,
    /// `Analytics.setConsent` — the per-session consent state.
    Consent,
}

impl Sink {
    /// The honest, full sink description used in the `md` table.
    pub fn label(self) -> &'static str {
        match self {
            Sink::Logs => {
                "structured logs (console; OTel when OTEL_EXPORTER_OTLP_ENDPOINT set)"
            }
            Sink::Analytics => "analytics store (DB)",
            Sink::Consent => "consent state (per session)",
        }
    }

    /// A short label for the Mermaid sink node.
    fn node_label(self) -> &'static str {
        match self {
            Sink::Logs => "Logs",
            Sink::Analytics => "Analytics DB",
            Sink::Consent => "Consent",
        }
    }

    /// A stable, ascii Mermaid node id, one per sink.
    fn node_id(self) -> &'static str {
        match self {
            Sink::Logs => "sink_logs",
            Sink::Analytics => "sink_analytics",
            Sink::Consent => "sink_consent",
        }
    }

    /// A PlantUML node declaration with a distinct KIND per sink: Logs→queue,
    /// Analytics→database, Consent→card.
    fn puml_decl(self) -> String {
        let l = self.node_label();
        let id = self.node_id();
        match self {
            Sink::Logs => format!("queue \"{l}\" as {id}"),
            Sink::Analytics => format!("database \"{l}\" as {id}"),
            Sink::Consent => format!("card \"{l}\" as {id}"),
        }
    }

    /// Which SVG shape draws this sink.
    fn svg_shape(self) -> SvgShape {
        match self {
            Sink::Logs => SvgShape::Queue,
            Sink::Analytics => SvgShape::Database,
            Sink::Consent => SvgShape::Node,
        }
    }
}

/// One telemetry / analytics / logging call site. Deduplicated: identical rows
/// (same module + call + event + sink) collapse to one.
#[derive(Clone, PartialEq, Eq, PartialOrd, Ord, Debug)]
pub struct TelemetryCall {
    /// The user module the call site is in.
    pub module: String,
    /// The call, `<ShortModule>.<func>` (`Log.info`, `Analytics.track`).
    pub call: String,
    /// The event / message: the first string-literal argument, or `<dynamic>`.
    pub event: String,
    /// Where the data goes.
    pub sink: Sink,
}

/// The telemetry inventory for a project — pure data the renderer consumes.
pub struct TelemetryReport {
    /// The project path, relative to the repo root when possible.
    pub project: String,
    /// True when the resolved target is a Sky.Spa wasm client — for the note.
    pub is_spa: bool,
    /// Call sites, sorted deterministically and deduplicated.
    pub calls: Vec<TelemetryCall>,
    /// Non-fatal reader notes.
    pub notes: Vec<String>,
}

/// Classify a resolved callee as a telemetry call. Returns the display call name
/// (`Log.info`, `Analytics.track`) and its sink, or `None` when the callee is
/// not a `Std.Log` / `Std.Analytics` function.
fn telemetry_callee(db: &dyn SkyDb, res: &Res) -> Option<(String, Sink)> {
    let (full, name): (String, String) = match res {
        Res::Def(d) => {
            let loc = db.def_loc(*d)?;
            (
                db.module_name(loc.module).to_string(),
                loc.name.as_str().to_string(),
            )
        }
        // Belt-and-suspenders: if a logging/analytics function ever resolves as a
        // bare kernel rather than a Sky-source def, catch it by pseudo-module.
        Res::Kernel { module, func } => (module.as_str().to_string(), func.as_str().to_string()),
        _ => return None,
    };
    let is_log = full == "Std.Log" || full == "Log";
    let is_analytics = full == "Std.Analytics" || full == "Analytics";
    if is_log {
        Some((format!("Log.{name}"), Sink::Logs))
    } else if is_analytics {
        // `setConsent` writes consent state; every other Analytics.* effect
        // writes the analytics store.
        let sink = if name == "setConsent" {
            Sink::Consent
        } else {
            Sink::Analytics
        };
        Some((format!("Analytics.{name}"), sink))
    } else {
        None
    }
}

/// The first string-literal argument of a call, or `<dynamic>` when the first
/// argument (or all arguments) is not a bare string literal.
fn first_str_arg(body: &Body, args: &[ExprId]) -> String {
    for a in args {
        if let Expr::Str(s) = &body.exprs[*a] {
            return s.to_string();
        }
    }
    "<dynamic>".to_string()
}

/// Walk one expression subtree, recording every telemetry call site into `out`.
/// Read-only; mirrors the exhaustive traversal in [`crate::spa_partition`].
fn walk_telemetry(
    db: &dyn SkyDb,
    module_name: &str,
    body: &Body,
    e: ExprId,
    out: &mut Vec<TelemetryCall>,
) {
    match &body.exprs[e] {
        Expr::Int(_)
        | Expr::Float(_)
        | Expr::Str(_)
        | Expr::Chr(_)
        | Expr::Bool(_)
        | Expr::Unit
        | Expr::Var(_)
        | Expr::Accessor(_)
        | Expr::Error => {}
        Expr::List(xs) | Expr::Tuple(xs) => {
            for x in xs {
                walk_telemetry(db, module_name, body, *x, out);
            }
        }
        Expr::Record(fields) => {
            for (_, x) in fields {
                walk_telemetry(db, module_name, body, *x, out);
            }
        }
        Expr::Update { base, fields } => {
            walk_telemetry(db, module_name, body, *base, out);
            for (_, x) in fields {
                walk_telemetry(db, module_name, body, *x, out);
            }
        }
        Expr::Negate(x) => walk_telemetry(db, module_name, body, *x, out),
        Expr::Lambda { body: b, .. } => walk_telemetry(db, module_name, body, *b, out),
        Expr::Call(callee, args) => {
            if let Expr::Var(res) = &body.exprs[*callee] {
                if let Some((call, sink)) = telemetry_callee(db, res) {
                    out.push(TelemetryCall {
                        module: module_name.to_string(),
                        call,
                        event: first_str_arg(body, args),
                        sink,
                    });
                }
            }
            walk_telemetry(db, module_name, body, *callee, out);
            for a in args {
                walk_telemetry(db, module_name, body, *a, out);
            }
        }
        Expr::Binop { lhs, rhs, .. } => {
            walk_telemetry(db, module_name, body, *lhs, out);
            walk_telemetry(db, module_name, body, *rhs, out);
        }
        Expr::If { arms, els } => {
            for (c, t) in arms {
                walk_telemetry(db, module_name, body, *c, out);
                walk_telemetry(db, module_name, body, *t, out);
            }
            walk_telemetry(db, module_name, body, *els, out);
        }
        Expr::Let { defs, body: b } => {
            for d in defs {
                walk_telemetry_localdef(db, module_name, body, d, out);
            }
            walk_telemetry(db, module_name, body, *b, out);
        }
        Expr::Case { subject, branches } => {
            walk_telemetry(db, module_name, body, *subject, out);
            for br in branches {
                walk_telemetry(db, module_name, body, br.body, out);
            }
        }
        Expr::Access(x, _) => walk_telemetry(db, module_name, body, *x, out),
    }
}

fn walk_telemetry_localdef(
    db: &dyn SkyDb,
    module_name: &str,
    body: &Body,
    d: &LocalDef,
    out: &mut Vec<TelemetryCall>,
) {
    walk_telemetry(db, module_name, body, d.body, out);
}

/// Build the telemetry inventory for a project. Read-only.
///
/// `app_target` (the `[app] target`, or a `--target` override) only decides the
/// `is_spa` note — telemetry call sites live in the user's own modules either
/// way, so the analysis is the same for every target.
pub fn analyze_telemetry(
    repo_root: &Path,
    project_dir: &Path,
    entry_module: Option<&str>,
    app_target: Option<&str>,
) -> Result<TelemetryReport, String> {
    let (db, _entry, check_ids) =
        crate::build::load_source_db(repo_root, project_dir, entry_module)?;
    let project = project_dir
        .strip_prefix(repo_root)
        .unwrap_or(project_dir)
        .to_string_lossy()
        .to_string();

    let mut calls: Vec<TelemetryCall> = Vec::new();
    for mid in &check_ids {
        let name = db.module_name(*mid).to_string();
        let resolved = db.resolve(*mid);
        for (_def, body) in &resolved.bodies {
            if let Some(root) = body.root {
                walk_telemetry(&db, &name, body, root, &mut calls);
            }
        }
    }
    // Deterministic order + dedup of identical rows.
    calls.sort();
    calls.dedup();

    let is_spa = app_target.map(target_is_spa_client).unwrap_or(false);

    let mut notes: Vec<String> = Vec::new();
    if !calls.is_empty() {
        notes.push(
            "Log and Analytics are effect kernels: under a Sky.Spa split they run on the \
             server, reached from the wasm client over /_rpc."
                .into(),
        );
        notes.push(
            "Detection covers direct `Std.Log` / `Std.Analytics` call sites in the project's \
             own modules; a call routed through a user helper is listed at that helper."
                .into(),
        );
    }

    Ok(TelemetryReport {
        project,
        is_spa,
        calls,
        notes,
    })
}

/// Render a telemetry report to the requested format. Pure function of `r`.
pub fn render_telemetry(r: &TelemetryReport, format: Format) -> String {
    if r.calls.is_empty() {
        return "No telemetry, analytics, or logging call sites found.\n".to_string();
    }
    match format {
        Format::Puml => render_telemetry_puml(r),
        Format::Md => render_telemetry_md(r),
        Format::Svg => render_telemetry_svg(r),
    }
}

/// Escape a `|` in a Markdown table cell so it does not split the column.
fn md_cell(s: &str) -> String {
    s.replace('|', "\\|")
}

/// A short, safe edge label: `<dynamic>` becomes plain `dynamic`, control /
/// grammar-breaking characters become spaces, and the label is truncated so the
/// diagram stays readable.
fn short_edge_label(event: &str) -> String {
    if event == "<dynamic>" {
        return "dynamic".to_string();
    }
    let s: String = event
        .chars()
        .map(|c| match c {
            '|' | '"' | '`' | '\n' | '\r' | '<' | '>' => ' ',
            other => other,
        })
        .collect();
    let s = s.trim();
    if s.chars().count() > 40 {
        let mut t: String = s.chars().take(39).collect();
        t.push('…');
        t
    } else {
        s.to_string()
    }
}

fn render_telemetry_md(r: &TelemetryReport) -> String {
    let mut o = String::new();
    o.push_str(&format!("# Telemetry — {}\n\n", r.project));
    o.push_str(
        "Everything this app tracks or logs, and where it goes — one row per \
         telemetry / analytics / logging call site.\n\n",
    );
    o.push_str("| Module | Call | Event | Sink |\n|---|---|---|---|\n");
    for c in &r.calls {
        o.push_str(&format!(
            "| {} | {} | {} | {} |\n",
            md_cell(&c.module),
            md_cell(&c.call),
            md_cell(&c.event),
            c.sink.label()
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

/// The distinct module set and sink set actually used, in deterministic order.
fn telemetry_nodes(r: &TelemetryReport) -> (Vec<String>, Vec<Sink>) {
    let mut modules: BTreeSet<String> = BTreeSet::new();
    let mut sinks: BTreeSet<Sink> = BTreeSet::new();
    for c in &r.calls {
        modules.insert(c.module.clone());
        sinks.insert(c.sink);
    }
    (modules.into_iter().collect(), sinks.into_iter().collect())
}

/// The distinct (module → sink) edges, each with its comma-joined event labels.
fn telemetry_edges(r: &TelemetryReport) -> Vec<(String, Sink, String)> {
    // (module, sink) -> ordered set of event labels
    let mut map: std::collections::BTreeMap<(String, Sink), BTreeSet<String>> =
        std::collections::BTreeMap::new();
    for c in &r.calls {
        map.entry((c.module.clone(), c.sink))
            .or_default()
            .insert(short_edge_label(&c.event));
    }
    map.into_iter()
        .map(|((m, s), evs)| (m, s, evs.into_iter().collect::<Vec<_>>().join(", ")))
        .collect()
}

fn render_telemetry_puml(r: &TelemetryReport) -> String {
    let mut o = puml_header(&format!("Telemetry — {}", r.project));
    let (modules, sinks) = telemetry_nodes(r);
    o.push_str("package \"Modules\" {\n");
    for m in &modules {
        o.push_str(&format!("  component \"{}\" as {}\n", m, module_node_id(m)));
    }
    o.push_str("}\n");
    for s in &sinks {
        o.push_str(&format!("{}\n", s.puml_decl()));
    }
    for (m, s, label) in telemetry_edges(r) {
        o.push_str(&format!("{} --> {} : {}\n", module_node_id(&m), s.node_id(), label));
    }
    o.push_str(&puml_footer());
    o
}

fn render_telemetry_svg(r: &TelemetryReport) -> String {
    let mut svg = diagram_svg::Svg::new(&format!("Telemetry — {}", r.project));
    let (modules, sinks) = telemetry_nodes(r);
    let mod_w = 170.0;
    let sink_w = 170.0;
    let mod_x = 0.0;
    let sink_x = mod_x + mod_w + 200.0;

    let mut mod_cy: std::collections::HashMap<String, f64> = std::collections::HashMap::new();
    for (i, m) in modules.iter().enumerate() {
        let y = i as f64 * (NODE_H + VGAP);
        svg.node(mod_x, y, mod_w, NODE_H, diagram_svg::FILL, diagram_svg::STROKE, m, None);
        mod_cy.insert(m.clone(), y + NODE_H / 2.0);
    }
    let mut sink_cy: std::collections::HashMap<Sink, f64> = std::collections::HashMap::new();
    for (j, s) in sinks.iter().enumerate() {
        let y = j as f64 * (NODE_H + VGAP);
        svg_shaped_node(&mut svg, s.svg_shape(), sink_x, y, sink_w, NODE_H, s.node_label());
        sink_cy.insert(*s, y + NODE_H / 2.0);
    }
    for (m, s, label) in telemetry_edges(r) {
        if let (Some(my), Some(sy)) = (mod_cy.get(&m), sink_cy.get(&s)) {
            svg.edge(mod_x + mod_w, *my, sink_x, *sy, diagram_svg::STROKE, Some(&label));
        }
    }
    svg.render()
}

// ===========================================================================
// `sky doc --diagram journey` — the user journey: pages + actions.
// ===========================================================================
//
// What are the app's pages, and what does a user DO on them? Two linked things:
//   1. a PAGE set — the `Page` ADT the Model's page field uses, plus the
//      URL of each page when the app declares an `App.withRoutes` table
//      (`App.route "/" HomePage`, `App.routeParam "/p/:slug" ProductPage`); and
//   2. an ACTION inventory — every `update` Msg, annotated `[client]` /
//      `[server /_rpc/<Msg>]` (reusing the SAME client/server classification
//      `wire` computes, via the Sky.Spa auto-split), plus the page(s) each
//      action navigates to (a branch that sets the page field to a page
//      constructor).
//
// Extraction is deliberately conservative — a correct inventory beats a wrong
// per-page graph. Pages, the page field, and the Page union are all recovered
// from the resolved HIR (the constructors an `update` branch assigns to the page
// field, and the `App.route` table), never guessed. Per-page action attribution
// is NOT attempted (an action can fire from any page); the actions are shown as
// one annotated inventory, and navigation targets are the pages an action routes
// TO, with the source page left unattributed. It never type-checks beyond the
// shared load, lowers, emits, or writes.

/// One page in the app — a `Page` ADT variant, with its route URL when the app
/// declares one.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct JourneyPage {
    /// The page constructor name (`HomePage`, `ProductPage`).
    pub name: String,
    /// The route URL from an `App.withRoutes` table, if the app has one.
    pub url: Option<String>,
}

/// One user action — an `update` Msg constructor.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct JourneyAction {
    /// The Msg constructor name (`Navigate`, `UpvotePost`).
    pub msg: String,
    /// `Some(true)` → the action round-trips as `POST /_rpc/<Msg>`; `Some(false)`
    /// → it runs in the browser (client); `None` → not classified (a Sky.Live
    /// app, where every action round-trips over SSE — see the report notes).
    pub server: Option<bool>,
    /// Page constructors this action deterministically navigates to (it sets the
    /// page field to that constructor in tail position). Sorted, deduped.
    pub navigates_to: Vec<String>,
    /// The action sets the page field to a non-constant value (a Msg arg, a
    /// helper call) — it navigates, but the target is chosen at run time.
    pub dynamic_nav: bool,
}

/// The user journey for a project — pure data the renderer consumes.
pub struct JourneyReport {
    /// The project path, relative to the repo root when possible.
    pub project: String,
    /// True when the resolved target is a Sky.Spa wasm client — for the note.
    pub is_spa: bool,
    /// The resolved `[app] target` (or the `--target` override), for the note.
    pub target: Option<String>,
    /// The app's pages, sorted by name.
    pub pages: Vec<JourneyPage>,
    /// The Model field that holds the current page (`page`, `currentPage`), when
    /// it could be identified — the anchor for navigation-edge detection.
    pub page_field: Option<String>,
    /// The user actions, sorted by Msg name.
    pub actions: Vec<JourneyAction>,
    /// True when per-action client/server classification is available (a Sky.Spa
    /// client, via the auto-split); false on a Sky.Live app (SSE round-trips).
    pub classified: bool,
    /// Non-fatal reader notes.
    pub notes: Vec<String>,
}

/// Visit every sub-expression of `body` reachable from `e`, calling `f` on each
/// (including `e` itself). Read-only; mirrors the exhaustive traversal used by
/// the telemetry walk, factored so the route-table + navigation scans share it.
fn walk_exprs(body: &Body, e: ExprId, f: &mut dyn FnMut(ExprId)) {
    f(e);
    match &body.exprs[e] {
        Expr::List(xs) | Expr::Tuple(xs) => {
            for x in xs {
                walk_exprs(body, *x, f);
            }
        }
        Expr::Record(fields) => {
            for (_, x) in fields {
                walk_exprs(body, *x, f);
            }
        }
        Expr::Update { base, fields } => {
            walk_exprs(body, *base, f);
            for (_, x) in fields {
                walk_exprs(body, *x, f);
            }
        }
        Expr::Negate(x) => walk_exprs(body, *x, f),
        Expr::Lambda { body: b, .. } => walk_exprs(body, *b, f),
        Expr::Call(callee, args) => {
            walk_exprs(body, *callee, f);
            for a in args {
                walk_exprs(body, *a, f);
            }
        }
        Expr::Binop { lhs, rhs, .. } => {
            walk_exprs(body, *lhs, f);
            walk_exprs(body, *rhs, f);
        }
        Expr::If { arms, els } => {
            for (c, t) in arms {
                walk_exprs(body, *c, f);
                walk_exprs(body, *t, f);
            }
            walk_exprs(body, *els, f);
        }
        Expr::Let { defs, body: b } => {
            for d in defs {
                walk_exprs(body, d.body, f);
            }
            walk_exprs(body, *b, f);
        }
        Expr::Case { subject, branches } => {
            walk_exprs(body, *subject, f);
            for br in branches {
                walk_exprs(body, br.body, f);
            }
        }
        Expr::Access(x, _) => walk_exprs(body, *x, f),
        Expr::Int(_)
        | Expr::Float(_)
        | Expr::Str(_)
        | Expr::Chr(_)
        | Expr::Bool(_)
        | Expr::Unit
        | Expr::Var(_)
        | Expr::Accessor(_)
        | Expr::Error => {}
    }
}

/// If `e` (or, for an applied constructor, its callee) resolves to a data
/// constructor, return `(ctor name, owning-union DefId)`. `None` for any other
/// value (a variable, a helper call, a literal) — which the navigation scan
/// treats as a dynamic (run-time-chosen) page target.
fn value_ctor(db: &dyn SkyDb, body: &Body, e: ExprId) -> Option<(String, DefId)> {
    match &body.exprs[e] {
        Expr::Var(Res::Ctor(cref)) => {
            let nm = db.def_loc(cref.def)?.name.as_str().to_string();
            Some((nm, cref.type_))
        }
        // `PostPage 3` / `ProductPage slug` — the head is the constructor.
        Expr::Call(callee, _) => value_ctor(db, body, *callee),
        _ => None,
    }
}

/// The constructor name a `case` arm pattern matches (`UpvotePost id` →
/// `UpvotePost`), unwrapping an `as` alias. `None` for a wildcard / literal arm.
fn pattern_ctor_name(body: &Body, p: PatId) -> Option<String> {
    match &body.pats[p] {
        Pattern::Ctor { name, .. } => Some(name.as_str().to_string()),
        Pattern::Alias(inner, _) => pattern_ctor_name(body, *inner),
        _ => None,
    }
}

/// The `case msg of` that dispatches `update`. `update msg model = case msg of …`
/// resolves to a body whose root is that `Case` (possibly under `let` bindings).
fn find_dispatch_case(body: &Body) -> Option<ExprId> {
    let mut e = body.root?;
    loop {
        match &body.exprs[e] {
            Expr::Case { .. } => return Some(e),
            Expr::Let { body: b, .. } => e = *b,
            _ => return None,
        }
    }
}

/// The constructor names of a union declared as `name` in `module`, in source
/// order — read from the module's parse tree. Empty when the union is not found.
fn union_variants(db: &dyn SkyDb, module: ModuleId, name: &str) -> Vec<String> {
    let tree = db.module_parse(module).tree();
    for d in tree.decls() {
        if let ast::Decl::Union(u) = d {
            if u.name().map(|t| t.text().to_string()).as_deref() == Some(name) {
                return u
                    .variants()
                    .iter()
                    .filter_map(|v| v.name().map(|t| t.text().to_string()))
                    .collect();
            }
        }
    }
    Vec::new()
}

/// A page-field name preference score: a field whose name mentions `page` /
/// `route` is a stronger page-field candidate than an arbitrary field that
/// happens to hold a union value.
fn page_field_pref(name: &str) -> u8 {
    let l = name.to_ascii_lowercase();
    if l.contains("page") || l.contains("route") || l.contains("screen") {
        1
    } else {
        0
    }
}

/// Build the user journey for a project. Read-only.
///
/// `app_target` (the `[app] target`, or a `--target` override) decides whether
/// per-action client/server classification is available: it is derived from the
/// Sky.Spa auto-split, which only exists for a wasm-client target. On any target
/// the pages + action inventory are recovered; a Sky.Live app simply has no
/// per-`/_rpc` split (every action round-trips over SSE), noted in the report.
pub fn analyze_journey(
    repo_root: &Path,
    project_dir: &Path,
    entry_module: Option<&str>,
    app_target: Option<&str>,
) -> Result<JourneyReport, String> {
    let (db, _entry, check_ids) =
        crate::build::load_source_db(repo_root, project_dir, entry_module)?;
    let project = project_dir
        .strip_prefix(repo_root)
        .unwrap_or(project_dir)
        .to_string_lossy()
        .to_string();
    let is_spa = app_target.map(target_is_spa_client).unwrap_or(false);
    // Only a user-declared union can be an app page. This excludes builtin /
    // stdlib unions (`Maybe`, `Result`, `List`) an `update` branch also assigns
    // (`session = Just …`) — and, defensively, keeps every `module_parse` /
    // `def_loc` below off the builtin pseudo-module (whose id is not a real
    // module row).
    let project_modules: HashSet<ModuleId> = check_ids.iter().copied().collect();
    let is_project_union = |u: DefId| -> bool {
        db.def_loc(u)
            .map(|l| project_modules.contains(&l.module))
            .unwrap_or(false)
    };

    // ---- 1. the `App.withRoutes` table: page constructor -> URL. ----
    let mut route_urls: HashMap<String, String> = HashMap::new();
    let mut route_union: Option<DefId> = None;
    let mut api_endpoints: Vec<String> = Vec::new();
    for mid in &check_ids {
        let resolved = db.resolve(*mid);
        for (_d, body) in &resolved.bodies {
            let Some(root) = body.root else { continue };
            let mut ids: Vec<ExprId> = Vec::new();
            walk_exprs(body, root, &mut |e| ids.push(e));
            for e in ids {
                let Expr::Call(callee, args) = &body.exprs[e] else {
                    continue;
                };
                let Expr::Var(Res::Def(d)) = &body.exprs[*callee] else {
                    continue;
                };
                let Some(loc) = db.def_loc(*d) else { continue };
                if db.module_name(loc.module) != "Std.App" {
                    continue;
                }
                let fname = loc.name.as_str();
                if (fname == "route" || fname == "routeParam") && args.len() >= 2 {
                    if let Expr::Str(url) = &body.exprs[args[0]] {
                        if let Some((cn, ct)) = value_ctor(&db, body, args[1]) {
                            if is_project_union(ct) {
                                route_urls.entry(cn).or_insert_with(|| url.to_string());
                                route_union.get_or_insert(ct);
                            }
                        }
                    }
                } else if fname == "api" && !args.is_empty() {
                    if let Expr::Str(sig) = &body.exprs[args[0]] {
                        api_endpoints.push(sig.to_string());
                    }
                }
            }
        }
    }

    // ---- 2. `update`'s dispatch branches: per Msg, the page-field assignments. -
    let mut update_ref: Option<(ModuleId, DefId)> = None;
    for mid in &check_ids {
        let resolved = db.resolve(*mid);
        if let Some(td) = resolved.top_defs.iter().find(|t| t.name.as_str() == "update") {
            update_ref = Some((*mid, td.def));
            break;
        }
    }
    // (msg, [(field, Some((ctor, union)) | None)]) — None = a non-constructor
    // value assigned to that field (a variable / helper call).
    let mut branches_raw: Vec<(String, Vec<(String, Option<(String, DefId)>)>)> = Vec::new();
    if let Some((umod, udef)) = update_ref {
        let resolved = db.resolve(umod);
        if let Some(body) = resolved.bodies.get(&udef) {
            if let Some(case_e) = find_dispatch_case(body) {
                if let Expr::Case { branches, .. } = &body.exprs[case_e] {
                    for br in branches {
                        let Some(msg) = pattern_ctor_name(body, br.pat) else {
                            continue;
                        };
                        let mut ids: Vec<ExprId> = Vec::new();
                        walk_exprs(body, br.body, &mut |e| ids.push(e));
                        let mut assigns: Vec<(String, Option<(String, DefId)>)> = Vec::new();
                        for e in ids {
                            if let Expr::Update { fields, .. } = &body.exprs[e] {
                                for (fname, fv) in fields {
                                    assigns.push((
                                        fname.as_str().to_string(),
                                        value_ctor(&db, body, *fv),
                                    ));
                                }
                            }
                        }
                        branches_raw.push((msg, assigns));
                    }
                }
            }
        }
    }

    // ---- 3. identify the Page union + the Model page field. ----
    let mut union_ctors: HashMap<DefId, HashSet<String>> = HashMap::new();
    let mut field_union_count: HashMap<(String, DefId), usize> = HashMap::new();
    for (_msg, assigns) in &branches_raw {
        for (field, oc) in assigns {
            if let Some((cn, u)) = oc {
                if !is_project_union(*u) {
                    continue;
                }
                union_ctors.entry(*u).or_default().insert(cn.clone());
                *field_union_count.entry((field.clone(), *u)).or_default() += 1;
            }
        }
    }
    // Prefer the route table's union (authoritative); else the union with the
    // most distinct constructors assigned to a page field (ties → lowest DefId).
    let page_union: Option<DefId> = route_union.or_else(|| {
        let mut v: Vec<(DefId, usize)> =
            union_ctors.iter().map(|(u, s)| (*u, s.len())).collect();
        v.sort_by(|a, b| b.1.cmp(&a.1).then(a.0.cmp(&b.0)));
        v.first().map(|(u, _)| *u)
    });
    let page_field: Option<String> = page_union.and_then(|pu| {
        let mut cands: Vec<(String, usize)> = field_union_count
            .iter()
            .filter(|((_, u), _)| *u == pu)
            .map(|((f, _), c)| (f.clone(), *c))
            .collect();
        cands.sort_by(|a, b| {
            b.1.cmp(&a.1)
                .then(page_field_pref(&b.0).cmp(&page_field_pref(&a.0)))
                .then(a.0.cmp(&b.0))
        });
        cands.first().map(|(f, _)| f.clone())
    });

    // ---- 4. the page set: the Page union's variants (+ route URLs). ----
    let mut pages: Vec<JourneyPage> = Vec::new();
    if let Some(pu) = page_union {
        if let Some(loc) = db.def_loc(pu) {
            for name in union_variants(&db, loc.module, loc.name.as_str()) {
                let url = route_urls.get(&name).cloned();
                pages.push(JourneyPage { name, url });
            }
        }
    }
    // Route-table-only fallback (the union could not be enumerated).
    if pages.is_empty() && !route_urls.is_empty() {
        for (name, url) in &route_urls {
            pages.push(JourneyPage {
                name: name.clone(),
                url: Some(url.clone()),
            });
        }
    }
    pages.sort_by(|a, b| a.name.cmp(&b.name));
    pages.dedup_by(|a, b| a.name == b.name);

    // ---- 5. the action inventory + navigation targets. ----
    let mut actions: Vec<JourneyAction> = Vec::new();
    for (msg, assigns) in &branches_raw {
        let mut nav: BTreeSet<String> = BTreeSet::new();
        let mut dynamic = false;
        if let Some(pf) = &page_field {
            for (field, oc) in assigns {
                if field != pf {
                    continue;
                }
                match oc {
                    Some((cn, u)) if Some(*u) == page_union => {
                        nav.insert(cn.clone());
                    }
                    _ => dynamic = true,
                }
            }
        }
        actions.push(JourneyAction {
            msg: msg.clone(),
            server: None,
            navigates_to: nav.into_iter().collect(),
            dynamic_nav: dynamic,
        });
    }
    actions.sort_by(|a, b| a.msg.cmp(&b.msg));
    actions.dedup_by(|a, b| a.msg == b.msg);

    // ---- 6. client/server classification, reusing the Sky.Spa auto-split. ----
    let mut classified = false;
    if is_spa {
        if let Ok(report) = crate::spa_partition::analyze(repo_root, project_dir, entry_module) {
            let mut server_by_msg: HashMap<String, bool> = HashMap::new();
            for b in &report.branches {
                let ctor = b.msg.split_whitespace().next().unwrap_or(&b.msg).to_string();
                server_by_msg.insert(ctor, b.server);
            }
            if actions.is_empty() {
                // Our own branch scan found nothing (a lambda / delegating
                // `update`); fall back to the auto-split's Msg inventory.
                let mut names: Vec<String> = server_by_msg.keys().cloned().collect();
                names.sort();
                for n in names {
                    let server = server_by_msg.get(&n).copied();
                    actions.push(JourneyAction {
                        msg: n,
                        server,
                        navigates_to: Vec::new(),
                        dynamic_nav: false,
                    });
                }
            } else {
                for a in &mut actions {
                    if let Some(s) = server_by_msg.get(&a.msg) {
                        a.server = Some(*s);
                    }
                }
            }
            classified = true;
        }
    }

    // ---- 7. notes. ----
    let mut notes: Vec<String> = Vec::new();
    if pages.is_empty() {
        notes.push(
            "Pages could not be determined: no `Page` union or `App.withRoutes` table was \
             found. Showing the action inventory only."
                .into(),
        );
    }
    if !actions.is_empty() {
        notes.push(
            "Actions are shown as one inventory (per-page attribution is best-effort): the \
             `Navigates to` column is where an action routes; its source page is not \
             attributed, since an action can fire from any page."
                .into(),
        );
    }
    if classified {
        notes.push(
            "`server` actions round-trip as `POST /_rpc/<Msg>`; `client` actions run in the \
             browser (wasm). Classification reuses the Sky.Spa auto-split (see `--diagram wire`)."
                .into(),
        );
    } else if !actions.is_empty() {
        notes.push(
            "This app is not built as a Sky.Spa wasm client, so there is no per-action \
             client/server split: on Sky.Live every action round-trips to the server over \
             the session's SSE channel."
                .into(),
        );
    }
    if !api_endpoints.is_empty() {
        api_endpoints.sort();
        api_endpoints.dedup();
        notes.push(format!(
            "Server API endpoints (routed, not user pages): {}.",
            api_endpoints.join(", ")
        ));
    }

    Ok(JourneyReport {
        project,
        is_spa,
        target: app_target.map(str::to_string),
        pages,
        page_field,
        actions,
        classified,
        notes,
    })
}

/// A stable, ascii node id / state alias for a page constructor name.
fn page_node_id(name: &str) -> String {
    let mut s = String::with_capacity(name.len() + 3);
    s.push_str("pg_");
    for ch in name.chars() {
        if ch.is_ascii_alphanumeric() {
            s.push(ch);
        } else {
            s.push('_');
        }
    }
    s
}

/// The index of the app's initial page: the one routed at `/`, else the first.
fn initial_page(r: &JourneyReport) -> Option<usize> {
    if r.pages.is_empty() {
        return None;
    }
    r.pages
        .iter()
        .position(|p| p.url.as_deref() == Some("/"))
        .or(Some(0))
}

/// Actions that do NOT navigate (no page-field constructor assignment) — shown
/// as internal events on the initial page state.
fn non_nav_actions(r: &JourneyReport) -> Vec<&JourneyAction> {
    r.actions
        .iter()
        .filter(|a| a.navigates_to.is_empty() && !a.dynamic_nav)
        .collect()
}

/// Does any action navigate to a run-time-chosen page?
fn any_dynamic_nav(r: &JourneyReport) -> bool {
    r.actions.iter().any(|a| a.dynamic_nav)
}

/// Render a user journey to the requested format. Pure function of `r`.
///
/// The journey is a page STATE MACHINE: pages are states, `[*]` marks the
/// initial page, and every navigating Msg is a transition between states.
pub fn render_journey(r: &JourneyReport, format: Format) -> String {
    match format {
        Format::Puml => render_journey_puml(r),
        Format::Md => render_journey_md(r),
        Format::Svg => render_journey_svg(r),
    }
}

fn render_journey_puml(r: &JourneyReport) -> String {
    let mut o = puml_header(&format!("User journey — {}", r.project));
    let Some(init) = initial_page(r) else {
        // No pages: a single clear state, never a broken diagram.
        o.push_str("state \"No pages found\" as none\n");
        o.push_str("[*] --> none\n");
        o.push_str(&puml_footer());
        return o;
    };
    // One state per page (URL folded into the label as a second line).
    for p in &r.pages {
        let label = match &p.url {
            Some(u) => format!("{}\\n{}", p.name, u),
            None => p.name.clone(),
        };
        o.push_str(&format!("state \"{}\" as {}\n", label, page_node_id(&p.name)));
    }
    let init_id = page_node_id(&r.pages[init].name);
    o.push_str(&format!("[*] --> {init_id}\n"));
    // Navigating transitions from the initial page to each target.
    for a in &r.actions {
        let tag = if a.server == Some(true) { " [server]" } else { "" };
        for t in &a.navigates_to {
            o.push_str(&format!(
                "{init_id} --> {} : {}{}\n",
                page_node_id(t),
                short_edge_label(&a.msg),
                tag
            ));
        }
        if a.dynamic_nav {
            o.push_str(&format!(
                "{init_id} --> {init_id} : {}{} (dynamic)\n",
                short_edge_label(&a.msg),
                tag
            ));
        }
    }
    // Non-navigating actions: internal events on the initial page state.
    for a in non_nav_actions(r) {
        let tag = if a.server == Some(true) { " [server]" } else { "" };
        o.push_str(&format!("{init_id} : {}{}\n", short_edge_label(&a.msg), tag));
    }
    o.push_str(&puml_footer());
    o
}

fn render_journey_svg(r: &JourneyReport) -> String {
    let mut svg = diagram_svg::Svg::new(&format!("User journey — {}", r.project));
    let Some(init) = initial_page(r) else {
        svg.node(0.0, 0.0, 200.0, NODE_H, diagram_svg::FILL, diagram_svg::STROKE, "No pages found", None);
        return svg.render();
    };
    let page_w = 160.0;
    let col_gap = 190.0;
    let row_h = 78.0;

    // BFS rank from the initial page: rank 0 = initial, rank 1 = everything else.
    let init_name = r.pages[init].name.clone();
    let mut rank: std::collections::HashMap<String, usize> = std::collections::HashMap::new();
    rank.insert(init_name.clone(), 0);
    for p in &r.pages {
        rank.entry(p.name.clone()).or_insert(1);
    }
    let has_dyn = any_dynamic_nav(r);

    // Assign a slot (column, row) to each page + the dynamic pseudo-state.
    let mut col_counts: std::collections::HashMap<usize, usize> = std::collections::HashMap::new();
    let mut pos: std::collections::HashMap<String, (f64, f64)> = std::collections::HashMap::new();
    // Initial first (top of column 0), then the rest in name order.
    let mut ordered: Vec<&JourneyPage> = r.pages.iter().collect();
    ordered.sort_by(|a, b| {
        let ra = if a.name == init_name { 0 } else { 1 };
        let rb = if b.name == init_name { 0 } else { 1 };
        ra.cmp(&rb).then(a.name.cmp(&b.name))
    });
    let internal = non_nav_actions(r);
    // The initial box is taller when it carries internal events.
    let init_h = NODE_H + (internal.len() as f64) * 15.0 + if internal.is_empty() { 0.0 } else { 8.0 };

    for p in &ordered {
        let col = *rank.get(&p.name).unwrap_or(&1);
        let row = *col_counts.entry(col).or_insert(0);
        col_counts.insert(col, row + 1);
        let x = 40.0 + col as f64 * (page_w + col_gap);
        let y = row as f64 * row_h;
        pos.insert(p.name.clone(), (x, y));
    }
    // The dynamic pseudo-state sits in column 1 after the pages.
    if has_dyn {
        let col = 1usize;
        let row = *col_counts.entry(col).or_insert(0);
        col_counts.insert(col, row + 1);
        let x = 40.0 + col as f64 * (page_w + col_gap);
        let y = row as f64 * row_h;
        pos.insert("__dyn__".to_string(), (x, y));
    }

    // Entry marker: a small filled circle to the left of the initial page.
    let (ix, iy) = pos[&init_name];
    svg.rect(ix - 26.0, iy + init_h / 2.0 - 5.0, 10.0, 10.0, 5.0, diagram_svg::TEXT, diagram_svg::TEXT, 1.0);
    svg.edge(ix - 14.0, iy + init_h / 2.0, ix, iy + init_h / 2.0, diagram_svg::STROKE, None);

    // Draw the page states.
    for p in &r.pages {
        let (x, y) = pos[&p.name];
        let h = if p.name == init_name { init_h } else { NODE_H };
        if p.name == init_name && !internal.is_empty() {
            svg.rect(x, y, page_w, h, 8.0, diagram_svg::FILL, diagram_svg::STROKE, 1.5);
            match &p.url {
                Some(u) => {
                    svg.text(x + page_w / 2.0, y + 18.0, &p.name, "middle", 13.0, "600", diagram_svg::TEXT);
                    svg.text(x + page_w / 2.0, y + 32.0, u, "middle", 11.0, "400", diagram_svg::SUBTLE);
                }
                None => {
                    svg.text(x + page_w / 2.0, y + 26.0, &p.name, "middle", 13.0, "600", diagram_svg::TEXT);
                }
            }
            let mut ly = y + NODE_H + 4.0;
            for a in &internal {
                let color = if a.server == Some(true) { diagram_svg::SERVER_EDGE } else { diagram_svg::SUBTLE };
                svg.text(x + 10.0, ly + 8.0, &short_edge_label(&a.msg), "start", 10.5, "500", color);
                ly += 15.0;
            }
        } else {
            svg.node(x, y, page_w, h, diagram_svg::FILL, diagram_svg::STROKE, &p.name, p.url.as_deref());
        }
    }
    // The dynamic pseudo-state.
    if has_dyn {
        let (x, y) = pos["__dyn__"];
        svg.node(x, y, page_w, NODE_H, diagram_svg::FILL_ALT, diagram_svg::PKG_STROKE, "(dynamic page)", None);
    }

    // Transitions from the initial page.
    let init_right = ix + page_w;
    let init_cy = iy + init_h / 2.0;
    for a in &r.actions {
        let color = match a.server {
            Some(true) => diagram_svg::SERVER_EDGE,
            Some(false) => diagram_svg::CLIENT_EDGE,
            None => diagram_svg::STROKE,
        };
        for t in &a.navigates_to {
            if let Some((tx, ty)) = pos.get(t) {
                if t == &init_name {
                    svg.self_loop(init_right, iy, color, &short_edge_label(&a.msg));
                } else {
                    svg.edge(init_right, init_cy, *tx, ty + NODE_H / 2.0, color, Some(&short_edge_label(&a.msg)));
                }
            }
        }
        if a.dynamic_nav {
            if let Some((tx, ty)) = pos.get("__dyn__") {
                svg.edge(init_right, init_cy, *tx, ty + NODE_H / 2.0, color, Some(&short_edge_label(&a.msg)));
            }
        }
    }
    svg.render()
}

fn render_journey_md(r: &JourneyReport) -> String {
    let mut o = String::new();
    o.push_str(&format!("# User journey — {}\n\n", r.project));
    o.push_str(&format!(
        "App shape: {}\n\n",
        if r.classified {
            "Sky.Spa (client/server split over /_rpc)"
        } else {
            "single-process (Sky.Live / Http)"
        }
    ));
    if r.pages.is_empty() && r.actions.is_empty() {
        for n in &r.notes {
            o.push_str(&format!("> {n}\n"));
        }
        return o;
    }
    o.push_str("## Pages\n\n");
    if r.pages.is_empty() {
        o.push_str("_Pages could not be determined._\n\n");
    } else {
        o.push_str("| Page | URL |\n|---|---|\n");
        for p in &r.pages {
            let url = p.url.as_deref().map(md_cell).unwrap_or_else(|| "—".into());
            o.push_str(&format!("| {} | {} |\n", md_cell(&p.name), url));
        }
        o.push('\n');
    }
    o.push_str("## Actions\n\n");
    o.push_str(
        "Each user action (Msg): whether it runs in the browser or round-trips to the \
         server, and the page(s) it navigates to.\n\n",
    );
    o.push_str("| Action | Runs | Navigates to |\n|---|---|---|\n");
    for a in &r.actions {
        let runs = match a.server {
            Some(true) => format!("server (POST /_rpc/{})", a.msg),
            Some(false) => "client".to_string(),
            None if r.classified => "client".to_string(),
            None => "server (SSE)".to_string(),
        };
        let mut nav = a.navigates_to.clone();
        if a.dynamic_nav {
            nav.push("(dynamic page)".to_string());
        }
        let nav_s = if nav.is_empty() {
            "—".to_string()
        } else {
            nav.join(", ")
        };
        o.push_str(&format!(
            "| {} | {} | {} |\n",
            md_cell(&a.msg),
            runs,
            md_cell(&nav_s)
        ));
    }
    o.push('\n');
    for n in &r.notes {
        o.push_str(&format!("> {n}\n"));
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
        ComponentGraph {
            project: "examples/demo".into(),
            is_spa,
            modules: vec![
                ModuleUse { module: "Api".into(), caps: api },
                ModuleUse { module: "View".into(), caps: BTreeSet::new() },
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

    fn is_svg(s: &str) -> bool {
        s.trim_start().starts_with("<svg") && s.trim_end().ends_with("</svg>")
    }
    fn count(hay: &str, needle: &str) -> usize {
        hay.matches(needle).count()
    }

    // ---- components ----

    #[test]
    fn components_puml_single_lane() {
        let out = render_components(&graph(false), Format::Puml);
        assert!(out.starts_with("@startuml"), "{out}");
        assert!(out.trim_end().ends_with("@enduml"), "{out}");
        assert!(out.contains("database \"Database\" as cap_db"), "{out}");
        assert!(out.contains("component \"Auth\" as cap_auth <<security>>"), "{out}");
        assert!(out.contains("m_Api --> cap_db"), "{out}");
        // no prose, no fence
        assert!(!out.contains("```"), "{out}");
        assert!(!out.contains("interface \"/_rpc\""), "single lane has no rpc: {out}");
    }

    #[test]
    fn components_puml_spa_has_lanes_and_rpc() {
        let out = render_components(&graph(true), Format::Puml);
        assert!(out.contains("package \"Client · wasm\""), "{out}");
        assert!(out.contains("interface \"/_rpc\" as rpc"), "{out}");
        assert!(out.contains("package \"Server · effects\""), "{out}");
        assert!(out.contains("m_Api --> rpc"), "{out}");
        assert!(out.contains("rpc --> cap_db"), "{out}");
    }

    #[test]
    fn components_md_has_table() {
        let out = render_components(&graph(false), Format::Md);
        assert!(out.contains("| Module | Capabilities |"), "{out}");
        assert!(out.contains("| Api | Database, Auth |"), "{out}");
        assert!(out.contains("| View | — |"), "{out}");
        assert!(!out.contains("@startuml") && !out.contains("```"), "{out}");
    }

    #[test]
    fn components_svg_is_wellformed_with_nodes_and_edges() {
        let out = render_components(&graph(false), Format::Svg);
        assert!(is_svg(&out), "{out}");
        // 2 modules + 2 capabilities = 4 rounded rects at least; edges = Api→(db,auth) = 2.
        assert!(count(&out, "<rect") >= 4, "expected node rects: {out}");
        assert!(count(&out, "<line") >= 2, "expected module→cap edges: {out}");
        assert!(out.contains(">Database<") || out.contains(">Database"), "{out}");
    }

    #[test]
    fn components_svg_spa_has_packages_and_rpc() {
        let out = render_components(&graph(true), Format::Svg);
        assert!(is_svg(&out), "{out}");
        assert!(out.contains(">Client · wasm<"), "{out}");
        assert!(out.contains(">Server · effects<"), "{out}");
        assert!(out.contains(">/_rpc<"), "{out}");
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
        assert_eq!(Capability::from_family("Http"), Some(Capability::ExternalHttp));
        assert_eq!(Capability::from_family("System"), Some(Capability::EnvConfig));
        assert_eq!(Capability::from_family("Time"), Some(Capability::Nondeterminism));
        assert_eq!(Capability::from_family("Uuid"), Some(Capability::Nondeterminism));
        assert_eq!(Capability::from_family("Server"), None);
        assert_eq!(Capability::from_family("Native"), None);
    }

    #[test]
    fn retired_mermaid_format_is_not_a_variant() {
        // Compile-time proof the enum has exactly the three shipped formats. A
        // `mermaid` string never maps to a Format (the CLI rejects it).
        let all = [Format::Puml, Format::Md, Format::Svg];
        assert_eq!(all.len(), 3);
    }

    // ---- wire (Spa /_rpc) ----

    fn wire_report() -> WireReport {
        WireReport {
            project: "examples/demo".into(),
            is_spa: true,
            target: Some("web:app".into()),
            endpoints: vec![
                WireEndpoint {
                    msg: "SetRegion".into(),
                    request: "{basket, region} + {region}".into(),
                    response: "{basket, region}".into(),
                    effects: None,
                },
                WireEndpoint {
                    msg: "SaveAll".into(),
                    request: "whole model".into(),
                    response: "whole model".into(),
                    effects: None,
                },
            ],
            http_endpoints: vec![],
            limited: false,
            notes: vec!["a note".into()],
        }
    }

    #[test]
    fn wire_md_has_the_rpc_table() {
        let out = render_wire(&wire_report(), Format::Md);
        assert!(out.contains("| Endpoint | Request (reads + args) | Response (writes) | Effects |"), "{out}");
        assert!(out.contains("| POST /_rpc/SetRegion | {basket, region} + {region} | {basket, region} | — |"), "{out}");
        assert!(out.contains("| POST /_rpc/SaveAll | whole model | whole model | — |"), "{out}");
    }

    #[test]
    fn wire_puml_is_a_sequence_per_endpoint() {
        let out = render_wire(&wire_report(), Format::Puml);
        assert!(out.starts_with("@startuml"), "{out}");
        assert!(out.contains("participant \"Client · wasm\" as C"), "{out}");
        assert!(out.contains("== SetRegion =="), "{out}");
        assert!(out.contains("C -> S : POST /_rpc/SetRegion"), "{out}");
        assert!(out.contains("S --> C : writes {basket, region}"), "{out}");
        assert!(!out.contains("```"), "{out}");
    }

    #[test]
    fn wire_svg_is_a_sequence_with_two_lifelines() {
        let out = render_wire(&wire_report(), Format::Svg);
        assert!(is_svg(&out), "{out}");
        assert!(out.contains(">Client · wasm<"), "{out}");
        assert!(out.contains(">Server<"), "{out}");
        // one request + one response arrow per endpoint, plus two lifelines.
        assert!(count(&out, "<line") >= 6, "expected lifelines + arrows: {out}");
    }

    // ---- wire (non-Spa HTTP endpoint map) ----

    fn http_wire_report() -> WireReport {
        WireReport {
            project: "examples/demo".into(),
            is_spa: false,
            target: Some("web".into()),
            endpoints: vec![],
            http_endpoints: vec![
                HttpEndpoint { method: "GET".into(), path: "/".into(), handler: "handleHome".into() },
                HttpEndpoint { method: "POST".into(), path: "/api/echo".into(), handler: "handleEcho".into() },
            ],
            limited: false,
            notes: vec![],
        }
    }

    #[test]
    fn wire_http_md_lists_the_endpoint_map() {
        let out = render_wire(&http_wire_report(), Format::Md);
        assert!(out.contains("| Method | Path | Handler |"), "{out}");
        assert!(out.contains("| GET | / | handleHome |"), "{out}");
        assert!(out.contains("| POST | /api/echo | handleEcho |"), "{out}");
        assert!(!out.contains("| Endpoint |"), "no /_rpc table for an HTTP app: {out}");
    }

    #[test]
    fn wire_http_puml_and_svg() {
        let puml = render_wire(&http_wire_report(), Format::Puml);
        assert!(puml.contains("C -> S : GET /"), "{puml}");
        assert!(puml.contains("C -> S : POST /api/echo"), "{puml}");
        let svg = render_wire(&http_wire_report(), Format::Svg);
        assert!(is_svg(&svg), "{svg}");
        assert!(svg.contains("GET /"), "{svg}");
    }

    #[test]
    fn wire_non_spa_no_routes_is_a_note_not_a_table() {
        let r = WireReport {
            project: "examples/demo".into(),
            is_spa: false,
            target: Some("web".into()),
            endpoints: vec![],
            http_endpoints: vec![],
            limited: false,
            notes: vec!["not a Sky.Spa wasm client".into()],
        };
        let out = render_wire(&r, Format::Md);
        assert!(!out.contains("| Endpoint |") && !out.contains("| Method |"), "{out}");
        assert!(out.contains("not a Sky.Spa wasm client"), "{out}");
    }

    // ---- telemetry ----

    fn telemetry_report() -> TelemetryReport {
        TelemetryReport {
            project: "examples/demo".into(),
            is_spa: false,
            calls: vec![
                TelemetryCall { module: "Main".into(), call: "Log.info".into(), event: "startup".into(), sink: Sink::Logs },
                TelemetryCall { module: "Update".into(), call: "Analytics.track".into(), event: "<dynamic>".into(), sink: Sink::Analytics },
                TelemetryCall { module: "Main".into(), call: "Analytics.setConsent".into(), event: "<dynamic>".into(), sink: Sink::Consent },
            ],
            notes: vec!["a note".into()],
        }
    }

    #[test]
    fn telemetry_md_lists_each_call() {
        let out = render_telemetry(&telemetry_report(), Format::Md);
        assert!(out.contains("| Module | Call | Event | Sink |"), "{out}");
        assert!(out.contains("| Main | Log.info | startup | structured logs (console; OTel when OTEL_EXPORTER_OTLP_ENDPOINT set) |"), "{out}");
        assert!(out.contains("| Update | Analytics.track | <dynamic> | analytics store (DB) |"), "{out}");
    }

    #[test]
    fn telemetry_puml_draws_module_to_sink_edges() {
        let out = render_telemetry(&telemetry_report(), Format::Puml);
        assert!(out.starts_with("@startuml"), "{out}");
        assert!(out.contains("queue \"Logs\" as sink_logs"), "{out}");
        assert!(out.contains("database \"Analytics DB\" as sink_analytics"), "{out}");
        assert!(out.contains("card \"Consent\" as sink_consent"), "{out}");
        assert!(out.contains("m_Main --> sink_logs : startup"), "{out}");
        assert!(out.contains("m_Update --> sink_analytics : dynamic"), "{out}");
    }

    #[test]
    fn telemetry_svg_wellformed() {
        let out = render_telemetry(&telemetry_report(), Format::Svg);
        assert!(is_svg(&out), "{out}");
        // 2 modules + 3 sinks nodes, 3 edges (Main→Logs, Update→Analytics, Main→Consent).
        assert!(count(&out, "<line") >= 3, "{out}");
    }

    #[test]
    fn telemetry_empty_prints_the_no_sites_message() {
        let r = TelemetryReport { project: "x".into(), is_spa: false, calls: vec![], notes: vec![] };
        for f in [Format::Puml, Format::Md, Format::Svg] {
            assert_eq!(render_telemetry(&r, f), "No telemetry, analytics, or logging call sites found.\n");
        }
    }

    // ---- journey (state machine) ----

    fn journey_report(classified: bool) -> JourneyReport {
        JourneyReport {
            project: "examples/demo".into(),
            is_spa: classified,
            target: Some("web:app".into()),
            pages: vec![
                JourneyPage { name: "HomePage".into(), url: Some("/".into()) },
                JourneyPage { name: "LoginPage".into(), url: None },
            ],
            page_field: Some("currentPage".into()),
            actions: vec![
                JourneyAction { msg: "Navigate".into(), server: if classified { Some(false) } else { None }, navigates_to: vec![], dynamic_nav: true },
                JourneyAction { msg: "Refresh".into(), server: if classified { Some(true) } else { None }, navigates_to: vec![], dynamic_nav: false },
                JourneyAction { msg: "UpvotePost".into(), server: if classified { Some(true) } else { None }, navigates_to: vec!["LoginPage".into()], dynamic_nav: false },
            ],
            classified,
            notes: vec!["a note".into()],
        }
    }

    #[test]
    fn journey_md_lists_pages_and_actions() {
        let out = render_journey(&journey_report(true), Format::Md);
        assert!(out.contains("## Pages"), "{out}");
        assert!(out.contains("| HomePage | / |"), "{out}");
        assert!(out.contains("| LoginPage | — |"), "{out}");
        assert!(out.contains("| UpvotePost | server (POST /_rpc/UpvotePost) | LoginPage |"), "{out}");
        assert!(out.contains("| Navigate | client | (dynamic page) |"), "{out}");
    }

    #[test]
    fn journey_puml_is_a_state_machine() {
        let out = render_journey(&journey_report(true), Format::Puml);
        assert!(out.starts_with("@startuml"), "{out}");
        // page states with the URL folded in
        assert!(out.contains("state \"HomePage\\n/\" as pg_HomePage"), "{out}");
        assert!(out.contains("state \"LoginPage\" as pg_LoginPage"), "{out}");
        // an initial marker into the / page
        assert!(out.contains("[*] --> pg_HomePage"), "{out}");
        // a navigating transition, server-tagged
        assert!(out.contains("pg_HomePage --> pg_LoginPage : UpvotePost [server]"), "{out}");
        // a dynamic self transition
        assert!(out.contains("pg_HomePage --> pg_HomePage : Navigate"), "{out}");
        // a non-navigating action as an internal event
        assert!(out.contains("pg_HomePage : Refresh"), "{out}");
        assert!(!out.contains("```"), "{out}");
    }

    #[test]
    fn journey_svg_is_a_state_machine() {
        let out = render_journey(&journey_report(true), Format::Svg);
        assert!(is_svg(&out), "{out}");
        assert!(out.contains(">HomePage<"), "{out}");
        assert!(out.contains(">LoginPage<"), "{out}");
        assert!(out.contains(">(dynamic page)<"), "{out}");
        // the initial-page transition edge to LoginPage exists.
        assert!(count(&out, "<line") >= 2 || count(&out, "<polyline") >= 1, "{out}");
    }

    #[test]
    fn journey_live_marks_actions_as_sse() {
        let out = render_journey(&journey_report(false), Format::Md);
        assert!(out.contains("| UpvotePost | server (SSE) | LoginPage |"), "{out}");
    }

    #[test]
    fn journey_empty_renders_a_placeholder_not_an_error() {
        let r = JourneyReport {
            project: "x".into(), is_spa: false, target: None,
            pages: vec![], page_field: None, actions: vec![], classified: false,
            notes: vec!["nothing found".into()],
        };
        let puml = render_journey(&r, Format::Puml);
        assert!(puml.contains("No pages found"), "{puml}");
        assert!(puml.starts_with("@startuml") && puml.trim_end().ends_with("@enduml"), "{puml}");
        let svg = render_journey(&r, Format::Svg);
        assert!(is_svg(&svg) && svg.contains("No pages found"), "{svg}");
        let md = render_journey(&r, Format::Md);
        assert!(md.contains("nothing found"), "{md}");
    }
}

// ---------------------------------------------------------------------------
// `sky test --scaffold-mocks` — emit mock-fixture skeletons for the app's
// outbound HTTP boundary, derived from the typed HIR (the same read-only load
// the diagrams use). This is the "an AI tool can write the mocks" path from
// docs/tooling/testing.md: the compiler knows which outbound calls the app
// makes and to which URLs, so it can pre-fill `match.method` + `match.urlContains`
// and leave only the response `body` to paste from a captured payload.
// ---------------------------------------------------------------------------

/// One outbound HTTP call site found in the project's OWN modules.
#[derive(Clone, PartialEq, Eq, Debug, PartialOrd, Ord)]
pub struct OutboundHttp {
    /// The HTTP method (`GET`/`POST`/…), or empty when it is only known at run
    /// time (a `request` built without a literal `withMethod`).
    pub method: String,
    /// The literal URL, when the call passes one as a string literal (or a
    /// `withUrl "…"` in a `request` builder). `None` when the URL is computed.
    pub url: Option<String>,
    /// The project module the call site is in.
    pub module: String,
}

/// The outbound-HTTP inventory for a project — pure data the `sky test
/// --scaffold-mocks` writer consumes.
pub struct MockScaffold {
    /// The project path, relative to the repo root when possible.
    pub project: String,
    /// Distinct outbound calls, deduped and sorted.
    pub calls: Vec<OutboundHttp>,
    /// Non-fatal reader notes.
    pub notes: Vec<String>,
}

/// If `callee` resolves to a `Sky.Core.Http` function, its name (`get`, `post`,
/// `request`, `withUrl`, `withMethod`, …); else `None`.
fn http_def_name(db: &dyn SkyDb, body: &Body, callee: ExprId) -> Option<String> {
    if let Expr::Var(Res::Def(d)) = &body.exprs[callee] {
        if let Some(loc) = db.def_loc(*d) {
            if db.module_name(loc.module) == "Sky.Core.Http" {
                return Some(loc.name.as_str().to_string());
            }
        }
    }
    None
}

/// A `urlContains` hint for a URL expression: the LONGEST single string literal
/// anywhere in its subtree. Any single literal is a real substring of the final
/// URL, so `apiBase ++ "/checkout/sessions"` yields `/checkout/sessions` (a
/// perfect host-independent match) and a fully-literal URL yields the whole
/// string. `None` when the expression carries no literal (a fully computed URL).
fn url_hint(body: &Body, e: ExprId) -> Option<String> {
    let mut best: Option<String> = None;
    walk_exprs(body, e, &mut |x| {
        if let Expr::Str(s) = &body.exprs[x] {
            let s = s.to_string();
            if best.as_ref().map(|b| s.len() > b.len()).unwrap_or(true) {
                best = Some(s);
            }
        }
    });
    best
}

/// The string literal an expression IS, or `None`.
fn expr_str_lit(body: &Body, e: ExprId) -> Option<String> {
    match &body.exprs[e] {
        Expr::Str(s) => Some(s.to_string()),
        _ => None,
    }
}

/// Discover every outbound HTTP call the project's own modules make, with the
/// method + literal URL where the HIR carries them. Read-only: loads the same
/// source db the build assembles, resolves, and walks — never lowers, emits, or
/// writes.
pub fn scaffold_mocks(
    repo_root: &Path,
    project_dir: &Path,
    entry_module: Option<&str>,
) -> Result<MockScaffold, String> {
    let (db, _entry, check_ids) =
        crate::build::load_source_db(repo_root, project_dir, entry_module)?;
    let project = project_dir
        .strip_prefix(repo_root)
        .unwrap_or(project_dir)
        .to_string_lossy()
        .to_string();

    let mut calls: BTreeSet<OutboundHttp> = BTreeSet::new();
    let mut any_dynamic = false;

    for mid in &check_ids {
        let mname = db.module_name(*mid).to_string();
        let resolved = db.resolve(*mid);
        for td in &resolved.top_defs {
            let Some(body) = resolved.bodies.get(&td.def) else {
                continue;
            };
            let Some(root) = body.root else {
                continue;
            };
            // Collect per DEF: the `|>` pipeline keeps a function applied to its
            // piped value as an `Expr::Binop { op: "|>", .. }`, NOT a `Call`, so a
            // piped `builder |> Http.request` (how most real code is written) is
            // not a Call node. `get`/`post` are matched in BOTH shapes (direct
            // `Http.get "u"` and piped `"u" |> Http.get`); a `request`'s method +
            // URL come from the `withMethod "…"` / `withUrl "…"` builder calls in
            // the same body (those ARE Call nodes — the literal is their argument).
            let mut direct: Vec<(String, Option<String>)> = Vec::new();
            let mut builder_urls: Vec<String> = Vec::new();
            let mut builder_methods: Vec<String> = Vec::new();
            let mut saw_request = false;

            let mut sites: Vec<ExprId> = Vec::new();
            walk_exprs(body, root, &mut |e| sites.push(e));
            for e in sites {
                match &body.exprs[e] {
                    Expr::Call(callee, args) => match http_def_name(&db, body, *callee).as_deref() {
                        Some("get") => {
                            direct.push(("GET".into(), args.first().and_then(|a| url_hint(body, *a))))
                        }
                        Some("post") => {
                            direct.push(("POST".into(), args.first().and_then(|a| url_hint(body, *a))))
                        }
                        // `defaultRequest url` and `withUrl "url"` both carry the URL.
                        Some("defaultRequest") | Some("withUrl") => {
                            if let Some(u) = args.first().and_then(|a| url_hint(body, *a)) {
                                builder_urls.push(u);
                            }
                        }
                        Some("withMethod") => {
                            if let Some(m) = args.first().and_then(|a| expr_str_lit(body, *a)) {
                                builder_methods.push(m.to_uppercase());
                            }
                        }
                        Some("request") => saw_request = true,
                        _ => {}
                    },
                    // `value |> f` — f is a bare Var here, not a Call.
                    Expr::Binop { op, lhs, rhs, .. } if op.as_str() == "|>" => {
                        match http_def_name(&db, body, *rhs).as_deref() {
                            Some("get") => direct.push(("GET".into(), url_hint(body, *lhs))),
                            Some("post") => direct.push(("POST".into(), url_hint(body, *lhs))),
                            Some("request") => saw_request = true,
                            _ => {}
                        }
                    }
                    // `f <| value`
                    Expr::Binop { op, lhs, rhs, .. } if op.as_str() == "<|" => {
                        match http_def_name(&db, body, *lhs).as_deref() {
                            Some("get") => direct.push(("GET".into(), url_hint(body, *rhs))),
                            Some("post") => direct.push(("POST".into(), url_hint(body, *rhs))),
                            Some("request") => saw_request = true,
                            _ => {}
                        }
                    }
                    _ => {}
                }
            }

            for (method, url) in direct {
                if url.is_none() {
                    any_dynamic = true;
                }
                calls.insert(OutboundHttp { method, url, module: mname.clone() });
            }
            // Pair builder URLs with methods: positional when counts match, else
            // the single method applies to every URL (the common one-request-per-
            // -function shape). A `request` with no literal URL is a dynamic call.
            if builder_urls.is_empty() {
                if saw_request {
                    any_dynamic = true;
                }
            } else {
                for (i, u) in builder_urls.iter().enumerate() {
                    let method = builder_methods
                        .get(i)
                        .or_else(|| builder_methods.first())
                        .cloned()
                        .unwrap_or_default();
                    calls.insert(OutboundHttp {
                        method,
                        url: Some(u.clone()),
                        module: mname.clone(),
                    });
                }
            }
        }
    }

    let mut notes: Vec<String> = Vec::new();
    if calls.is_empty() {
        notes.push(
            "No outbound HTTP calls found in the project's own modules. A mock \
             fixture is only needed for calls the app makes to an external service."
                .into(),
        );
    }
    if any_dynamic {
        notes.push(
            "Some call URLs are computed at run time (not a string literal), so \
             their `urlContains` is left blank for you to fill — a blank matches \
             any URL, so narrow it. Run the tests once with no fixture to see the \
             exact URL in the fail-closed error."
                .into(),
        );
    }

    Ok(MockScaffold {
        project,
        calls: calls.into_iter().collect(),
        notes,
    })
}
