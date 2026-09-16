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
}

/// The three distinct SVG shapes a capability / sink node can take.
#[derive(Clone, Copy, PartialEq, Eq)]
enum SvgShape {
    Node,
    Database,
    Queue,
}

/// Draw one shaped node into the SVG at `(x, y)` with size `(w, h)`.
fn svg_shaped_node(
    svg: &mut diagram_svg::Svg,
    shape: SvgShape,
    x: f64,
    y: f64,
    w: f64,
    h: f64,
    label: &str,
) {
    match shape {
        SvgShape::Node => svg.node(
            x,
            y,
            w,
            h,
            diagram_svg::FILL_ALT,
            diagram_svg::STROKE,
            label,
            None,
        ),
        SvgShape::Database => svg.database(
            x,
            y,
            w,
            h,
            diagram_svg::FILL_ALT,
            diagram_svg::STROKE,
            label,
        ),
        SvgShape::Queue => svg.queue(
            x,
            y,
            w,
            h,
            diagram_svg::FILL_ALT,
            diagram_svg::STROKE,
            label,
        ),
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
    /// The derived app shape — drives the zones and the crossing labels.
    pub shape: AppShape,
    /// The `Std.Db` table names the app declares (sorted, deduped). Listed inside
    /// the Database container, capped in the renderer.
    pub tables: Vec<String>,
    /// For a Spa app: the count of EFFECTFUL `update` actions (server branches
    /// that round-trip as `POST /_rpc/<Msg>`). `None` for a non-Spa app or when
    /// the partition could not be run.
    pub rpc_effectful: Option<usize>,
    /// For a Spa app: the count of PURE client actions (client branches that run
    /// in the wasm client, no round-trip). `None` when unavailable.
    pub rpc_pure: Option<usize>,
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

/// The app SHAPE a diagram renders for — derived from the resolved `--target`
/// plus two facts read from the source. The shape drives the target-aware
/// labels/sections: a Spa app has a Browser/Server split over `/_rpc`, a Live app
/// is one trusted server serving SSR + SSE, a terminal app is a single binary
/// with no network boundary, an HTTP app is a client talking to an HTTP API.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum AppShape {
    /// A Sky.Spa wasm client + a server it reaches over `/_rpc`.
    Spa,
    /// A Sky.Live server-driven UI (SSR + one SSE channel per session).
    Live,
    /// A Sky.Tui full-screen terminal app.
    Tui,
    /// A Sky.Cli line-oriented / one-shot terminal app.
    Cli,
    /// A Sky.Http.Server HTTP/JSON API with no browser UI.
    Http,
}

impl AppShape {
    /// A terminal shape (Tui or Cli) — one binary, no network trust boundary.
    pub fn is_terminal(self) -> bool {
        matches!(self, AppShape::Tui | AppShape::Cli)
    }
}

/// Derive the [`AppShape`] from the resolved target and two source facts:
/// `has_app_ui` (the app declares a `Std.App` UI — routes / a page union),
/// `has_http_routes` (it registers `Sky.Http.Server` routes). A client / terminal
/// target decides the shape outright; with no such target a browser-facing app is
/// Sky.Live and a route-only server with no UI is an HTTP API.
pub fn app_shape(target: Option<&str>, has_app_ui: bool, has_http_routes: bool) -> AppShape {
    if let Some(t) = target {
        let t = t.trim();
        if target_is_spa_client(t) {
            return AppShape::Spa;
        }
        if t == "terminal:tui" {
            return AppShape::Tui;
        }
        if t == "terminal:cli" || t == "terminal" {
            return AppShape::Cli;
        }
    }
    if has_http_routes && !has_app_ui {
        AppShape::Http
    } else {
        AppShape::Live
    }
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

    // The app SHAPE — for the zones + crossing labels. Read the two shape facts
    // (a `Std.App` UI, `Sky.Http.Server` routes) from the same loaded db.
    let (_eps, has_app_ui, has_http_routes) = recover_endpoints(&db, &check_ids);
    let shape = if is_spa {
        AppShape::Spa
    } else {
        app_shape(app_target, has_app_ui, has_http_routes)
    };

    // The real table names inside the Database container. Declaring a table
    // (`Std.Db.Schema.table` / `Std.Db.Store.fromCodec`) means the app models
    // data through a database, so ensure the Database capability is present even
    // when the store is only declared, not yet queried — otherwise the table
    // names would have no container to sit in.
    let tables = collect_db_tables(&db, &check_ids);
    if !tables.is_empty() {
        all_caps.insert(Capability::Database);
    }

    // For a Spa app: count effectful (server, → /_rpc) vs pure (client) actions,
    // reusing the auto-split partition. Best-effort; `None` if it cannot run.
    let (rpc_effectful, rpc_pure) = if shape == AppShape::Spa {
        match crate::spa_partition::analyze(repo_root, project_dir, entry_module) {
            Ok(rep) => {
                let eff = rep.branches.iter().filter(|b| b.server).count();
                let pure = rep.branches.iter().filter(|b| !b.server).count();
                (Some(eff), Some(pure))
            }
            Err(_) => (None, None),
        }
    } else {
        (None, None)
    };

    let mut notes: Vec<String> = Vec::new();
    if all_caps.contains(&Capability::Telemetry) {
        notes.push(
            "Analytics has no distinct kernel family; it is folded into Telemetry/Logs (Std.Log)."
                .into(),
        );
    }
    match shape {
        AppShape::Spa => notes.push(
            "Sky.Spa: every effect runs on the server; the client (wasm) reaches it over /_rpc."
                .into(),
        ),
        AppShape::Live => notes.push(
            "Sky.Live: one trusted server serves SSR + one SSE channel per session; every \
             effect runs server-side."
                .into(),
        ),
        AppShape::Tui | AppShape::Cli => notes.push(
            "Terminal app: one local binary drives the effects directly; no network trust \
             boundary."
                .into(),
        ),
        AppShape::Http => notes.push(
            "Sky.Http.Server: a client reaches the HTTP API; every effect runs server-side."
                .into(),
        ),
    }

    Ok(ComponentGraph {
        project,
        is_spa,
        shape,
        tables,
        rpc_effectful,
        rpc_pure,
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

/// The capabilities of a project, collapsed into the C4 CONTAINER roles the
/// components diagram draws: data stores + an egress sink inside the trust
/// boundary, external systems outside it, an auth control on the crossing, and
/// the remaining effect families folded into the Backend container's subtitle.
struct C4Caps {
    /// Data stores in the server zone: `(label, edge label)`, e.g.
    /// `("Database", "SQL")`, `("Files", "read/write")`. Drawn as datastores.
    stores: Vec<(&'static str, &'static str)>,
    /// The egress sink (Telemetry/Logs), when present. Drawn as a queue labelled
    /// audit egress in the server zone.
    egress: Option<&'static str>,
    /// External systems OUTSIDE the trust boundary (External HTTP). Drawn as
    /// containers in the External zone.
    externals: Vec<&'static str>,
    /// The backend authenticates requests — a control marker on the crossing.
    auth: bool,
    /// Effect families that are not their own node: Env/Config, Jobs, Realtime,
    /// Time/Random/Uuid — folded into the Backend container's subtitle.
    inline: Vec<&'static str>,
}

fn classify_c4(g: &ComponentGraph) -> C4Caps {
    let mut stores: Vec<(&'static str, &'static str)> = Vec::new();
    let mut egress = None;
    let mut externals: Vec<&'static str> = Vec::new();
    let mut auth = false;
    let mut inline: Vec<&'static str> = Vec::new();
    for c in &g.capabilities {
        match c {
            Capability::Database => stores.push(("Database", "SQL")),
            Capability::File => stores.push(("Files", "read/write")),
            Capability::ExternalHttp => externals.push("External HTTP APIs"),
            Capability::Telemetry => egress = Some("Audit / logs"),
            Capability::Auth => auth = true,
            Capability::EnvConfig => inline.push("Env/Config"),
            Capability::Jobs => inline.push("Jobs"),
            Capability::Realtime => inline.push("Realtime/SSE"),
            Capability::Nondeterminism => inline.push("Time/Random/Uuid"),
        }
    }
    C4Caps {
        stores,
        egress,
        externals,
        auth,
        inline,
    }
}

/// The table-name lines for a PlantUML database-node label: up to eight names
/// joined by `\n`, with a `+N more` line when there are more. Empty when the app
/// declares no tables.
fn puml_table_lines(tables: &[String]) -> String {
    if tables.is_empty() {
        return String::new();
    }
    let cap = 8usize;
    let mut lines: Vec<String> = tables.iter().take(cap).cloned().collect();
    if tables.len() > cap {
        lines.push(format!("+{} more", tables.len() - cap));
    }
    lines.join("\\n")
}

/// The Backend container subtitle: `also: <inline caps>` (or empty).
fn backend_subtitle(c: &C4Caps) -> String {
    if c.inline.is_empty() {
        String::new()
    } else {
        format!("also: {}", c.inline.join(", "))
    }
}

fn render_components_puml(g: &ComponentGraph) -> String {
    let mut o = puml_header(&format!("Components (C4 container) — {}", g.project));
    let c = classify_c4(g);
    let sub = backend_subtitle(&c);
    let backend_desc = if sub.is_empty() {
        "Backend\\n«native»".to_string()
    } else {
        format!("Backend\\n«native»\\n{sub}")
    };

    o.push_str("actor \"User\" as user\n");
    if g.is_spa {
        o.push_str("rectangle \"Browser · untrusted\" <<boundary>> {\n");
        o.push_str("  rectangle \"SPA\\n«wasm client»\" as spa <<container>>\n");
        o.push_str("}\n");
    }
    let server_zone = match g.shape {
        AppShape::Tui | AppShape::Cli => "Process · local",
        _ => "Server · trusted",
    };
    o.push_str(&format!("rectangle \"{server_zone}\" <<boundary>> {{\n"));
    o.push_str(&format!(
        "  rectangle \"{backend_desc}\" as backend <<container>>\n"
    ));
    // The Database node lists the app's real table names (cap 8 + "+N more").
    let table_label = puml_table_lines(&g.tables);
    for (i, (label, _)) in c.stores.iter().enumerate() {
        if *label == "Database" && !table_label.is_empty() {
            o.push_str(&format!(
                "  database \"Database\\n{table_label}\" as store{i}\n"
            ));
        } else {
            o.push_str(&format!("  database \"{label}\" as store{i}\n"));
        }
    }
    if c.egress.is_some() {
        o.push_str("  queue \"Audit / logs\\n(egress)\" as egress\n");
    }
    o.push_str("}\n");
    if !c.externals.is_empty() {
        o.push_str("rectangle \"External\" <<boundary>> {\n");
        for (i, label) in c.externals.iter().enumerate() {
            o.push_str(&format!(
                "  rectangle \"{label}\\n«external system»\" as ext{i} <<container>>\n"
            ));
        }
        o.push_str("}\n");
    }

    // Edges.
    if g.is_spa {
        o.push_str("user --> spa : uses · HTTPS\n");
        let auth = if c.auth { " · auth" } else { "" };
        // Label the /_rpc crossing with the count of EFFECTFUL actions that
        // round-trip; a note carries the PURE client-action count.
        let eff = g.rpc_effectful.map(|n| format!(" · {n} effectful")).unwrap_or_default();
        o.push_str(&format!("spa --> backend : /_rpc{auth}{eff}\n"));
        if let Some(pure) = g.rpc_pure {
            o.push_str(&format!(
                "note bottom of spa : {pure} pure client actions (wasm)\n"
            ));
        }
    } else {
        let via = match g.shape {
            AppShape::Tui | AppShape::Cli => "in-process",
            AppShape::Live => "HTTPS + SSE",
            _ if g.capabilities.contains(&Capability::Realtime) => "HTTPS + SSE",
            _ => "HTTPS",
        };
        let auth = if c.auth && !g.shape.is_terminal() { " · auth" } else { "" };
        o.push_str(&format!("user --> backend : {via}{auth}\n"));
    }
    for (i, (_, edge)) in c.stores.iter().enumerate() {
        o.push_str(&format!("backend --> store{i} : {edge}\n"));
    }
    if c.egress.is_some() {
        o.push_str("backend --> egress : audit log\n");
    }
    for i in 0..c.externals.len() {
        o.push_str(&format!("backend --> ext{i} : HTTPS\n"));
    }

    // A legend decodes the shapes + the trust boundary.
    o.push_str("legend right\n");
    o.push_str("  <b>C4 container view</b>\n");
    o.push_str("  boundary = trust zone (dashed)\n");
    o.push_str("  database = data store · queue = egress\n");
    o.push_str("  external rectangle = outside the boundary\n");
    o.push_str("endlegend\n");
    o.push_str(&puml_footer());
    o
}

fn render_components_md(g: &ComponentGraph) -> String {
    let mut o = String::new();
    o.push_str(&format!(
        "# System architecture (C4 containers) — {} · generated {}\n\n",
        g.project,
        today_utc()
    ));
    let shape_line = match g.shape {
        AppShape::Spa => "Sky.Spa (wasm client + server over /_rpc)",
        AppShape::Live => "Sky.Live (one trusted server, SSR + SSE)",
        AppShape::Tui => "Sky.Tui (single terminal binary)",
        AppShape::Cli => "Sky.Cli (single terminal binary)",
        AppShape::Http => "Sky.Http.Server (HTTP API)",
    };
    o.push_str(&format!("App shape: {shape_line}\n\n"));

    // ---- the C4 CONTAINER view (the headline; modules are an appendix) ----
    o.push_str("## Containers\n\n");
    o.push_str("| Container | Trust zone | Technology | Responsibility |\n|---|---|---|---|\n");
    let has_split = matches!(g.shape, AppShape::Spa);
    if has_split {
        let pure = g.rpc_pure.unwrap_or(0);
        let eff = g.rpc_effectful.unwrap_or(0);
        o.push_str(&format!(
            "| Browser client | Untrusted (client) | wasm (Sky.Spa) | Renders the UI; {pure} pure client action(s); reaches the server over `/_rpc`. |\n"
        ));
        o.push_str(&format!(
            "| Application server | Trusted (server) | native Go (Sky.Spa SSR) | Serves `/_rpc`; runs EVERY effect; {eff} effectful action(s). |\n"
        ));
    } else {
        o.push_str(&format!(
            "| Application server | Trusted (server) | native Go ({shape_line}) | Runs the UI and every effect in one process. |\n"
        ));
    }
    if !g.tables.is_empty() {
        o.push_str(&format!(
            "| Data store | Trusted (server) | SQL | Persists {} table(s) (see below). |\n",
            g.tables.len()
        ));
    }
    o.push_str("| External HTTP APIs | Untrusted (third-party) | HTTPS | Payments / OAuth / mail etc. — see `sub-processors.md` for the named register. |\n\n");

    if !g.tables.is_empty() {
        o.push_str(&format!(
            "### Data store — {} table(s)\n\n{}\n\n",
            g.tables.len(),
            g.tables.iter().map(|t| format!("`{t}`")).collect::<Vec<_>>().join(", ")
        ));
    }

    // ---- appendix: the module → capability detail (C4 "code" level) ----
    o.push_str("## Appendix — modules\n\n");
    o.push_str("_Source modules and the capability families each reaches (the C4 code level; the containers above are what an auditor reads first)._\n\n");
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
        o.push_str(&format!(
            "| {} | {} |\n",
            md_cell(&m.module),
            md_cell(&caps)
        ));
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

/// The flagship: a C4 CONTAINER diagram. A handful of containers inside dashed
/// TRUST-BOUNDARY zones, left → right, with orthogonal edges only. The module →
/// capability detail stays in the `md` table; here every capability collapses to
/// its C4 role ([`classify_c4`]).
fn render_components_svg(g: &ComponentGraph) -> String {
    use diagram_svg as d;
    let mut svg = d::Svg::new(&format!("Components (C4 container) — {}", g.project));
    let c = classify_c4(g);

    // ---- sizing ----
    let ztop = 12.0;
    let zpad = 16.0;
    let zhdr = 24.0;
    let col_gap = 96.0;

    let backend_w: f64 = 244.0;
    let sub = backend_subtitle(&c);
    let sub_lines = if sub.is_empty() {
        0
    } else {
        d::wrap(&sub, ((backend_w - 20.0) / (10.5 * 0.6)) as usize).len()
    };
    let backend_h = if sub_lines == 0 {
        54.0
    } else {
        56.0 + sub_lines as f64 * 13.0 + 12.0
    };

    let store_w = 148.0;
    let store_h = 50.0;
    let store_gap = 22.0;
    // egress rides the store row as its last item.
    let n_store_items = c.stores.len() + if c.egress.is_some() { 1 } else { 0 };
    let store_row_w = if n_store_items == 0 {
        0.0
    } else {
        n_store_items as f64 * store_w + (n_store_items as f64 - 1.0) * store_gap
    };
    let store_vgap = 46.0;

    // Real table names listed INSIDE the Database store: cap the visible list and
    // fold the remainder into a "+N more" line.
    let tables_cap = 8usize;
    let table_lines: Vec<String> = {
        let mut v: Vec<String> = g.tables.iter().take(tables_cap).cloned().collect();
        if g.tables.len() > tables_cap {
            v.push(format!("+{} more", g.tables.len() - tables_cap));
        }
        v
    };
    // The Database store grows to fit its table list; other stores stay base.
    let db_store_h = if table_lines.is_empty() {
        store_h
    } else {
        46.0 + table_lines.len() as f64 * 13.0 + 8.0
    };
    let store_row_h = store_h.max(db_store_h);

    let spa_w = 176.0;
    let spa_h = 62.0;
    let ext_w = 188.0;
    let ext_h = 62.0;
    let ext_gap = 22.0;
    let n_ext = c.externals.len();
    let ext_stack_h = if n_ext == 0 {
        0.0
    } else {
        n_ext as f64 * ext_h + (n_ext as f64 - 1.0) * ext_gap
    };

    // Flow centre-line Y: high enough that the tallest centred group clears the
    // zone header, and the store row below the backend fits above the legend.
    let half_max = (backend_h / 2.0)
        .max(if g.is_spa { spa_h / 2.0 } else { 0.0 })
        .max(ext_stack_h / 2.0);
    let flow_y = ztop + zhdr + zpad + half_max;

    // ---- x layout ----
    let lx = 8.0;
    let actor_cx = lx + 22.0;
    let mut x = lx + 56.0 + col_gap;

    let (spa_zone_x, spa_x) = if g.is_spa {
        let zx = x;
        let sx = zx + zpad;
        x = zx + spa_w + 2.0 * zpad + col_gap;
        (zx, sx)
    } else {
        (0.0, 0.0)
    };

    let server_block_w = backend_w.max(store_row_w);
    let server_zone_x = x;
    let server_block_x = server_zone_x + zpad;
    let backend_x = server_block_x + (server_block_w - backend_w) / 2.0;
    let store_row_x = server_block_x + (server_block_w - store_row_w) / 2.0;
    x = server_zone_x + server_block_w + 2.0 * zpad;

    let (ext_zone_x, ext_x) = if n_ext > 0 {
        x += col_gap;
        let zx = x;
        let ex = zx + zpad;
        (zx, ex)
    } else {
        (0.0, 0.0)
    };
    let _ = x;

    // ---- vertical placement ----
    let backend_y = flow_y - backend_h / 2.0;
    let store_y = backend_y + backend_h + store_vgap;
    let spa_y = flow_y - spa_h / 2.0;
    let ext_top = flow_y - ext_stack_h / 2.0;

    // Shared zone bottom, so the trust zones read as aligned columns.
    let mut zbottom = backend_y + backend_h;
    if n_store_items > 0 {
        zbottom = zbottom.max(store_y + store_row_h);
    }
    if g.is_spa {
        zbottom = zbottom.max(spa_y + spa_h);
    }
    if n_ext > 0 {
        zbottom = zbottom.max(ext_top + ext_stack_h);
    }
    zbottom += zpad;
    let zone_h = zbottom - ztop;

    // ---- draw zones (dashed, behind the containers) ----
    if g.is_spa {
        svg.zone(
            spa_zone_x,
            ztop,
            spa_w + 2.0 * zpad,
            zone_h,
            "Browser · untrusted",
            d::BOUNDARY_UNTRUSTED,
        );
    }
    let server_zone_label = match g.shape {
        AppShape::Tui | AppShape::Cli => "Process · local",
        _ => "Server · trusted",
    };
    svg.zone(
        server_zone_x,
        ztop,
        server_block_w + 2.0 * zpad,
        zone_h,
        server_zone_label,
        d::BOUNDARY_TRUSTED,
    );
    if n_ext > 0 {
        svg.zone(
            ext_zone_x,
            ztop,
            ext_w + 2.0 * zpad,
            zone_h,
            "External · untrusted",
            d::EXTERNAL,
        );
    }

    // ---- actor ----
    let actor_label = match g.shape {
        AppShape::Tui | AppShape::Cli => "User (terminal)",
        AppShape::Live => "User (browser)",
        _ => "User",
    };
    svg.actor(actor_cx, flow_y - 44.0, actor_label);

    // ---- SPA container ----
    if g.is_spa {
        svg.container(
            spa_x,
            spa_y,
            spa_w,
            spa_h,
            d::FILL,
            "SPA",
            "wasm client",
            None,
        );
    }

    // ---- Backend container ----
    let backend_cx = backend_x + backend_w / 2.0;
    let (backend_title, backend_stereo) = match g.shape {
        AppShape::Tui | AppShape::Cli => ("App", "single binary"),
        _ => ("Backend", "native"),
    };
    svg.container(
        backend_x,
        backend_y,
        backend_w,
        backend_h,
        d::FILL,
        backend_title,
        backend_stereo,
        if sub.is_empty() {
            None
        } else {
            Some(sub.as_str())
        },
    );

    // ---- store row (data stores + egress) ----
    let mut store_centres: Vec<(f64, &'static str, bool)> = Vec::new(); // (cx, edge_label, is_egress)
    let mut sx = store_row_x;
    for (label, edge) in &c.stores {
        if *label == "Database" && !table_lines.is_empty() {
            svg.datastore_list(sx, store_y, store_w, db_store_h, "Database", &table_lines, d::STROKE);
        } else {
            svg.datastore(sx, store_y, store_w, store_h, label, d::STROKE);
        }
        store_centres.push((sx + store_w / 2.0, edge, false));
        sx += store_w + store_gap;
    }
    if let Some(egress) = c.egress {
        svg.queue(
            sx,
            store_y,
            store_w,
            store_h,
            d::FILL_ALT,
            d::EXTERNAL,
            egress,
        );
        store_centres.push((sx + store_w / 2.0, "audit log", true));
    }

    // ---- external containers ----
    let mut ext_cys: Vec<f64> = Vec::new();
    for (i, label) in c.externals.iter().enumerate() {
        let ey = ext_top + i as f64 * (ext_h + ext_gap);
        svg.container(
            ext_x,
            ey,
            ext_w,
            ext_h,
            d::FILL,
            label,
            "external system",
            None,
        );
        ext_cys.push(ey + ext_h / 2.0);
    }

    // ---- edges ----
    // actor → (SPA | Backend)
    if g.is_spa {
        svg.ortho(
            actor_cx + 20.0,
            flow_y,
            spa_x,
            flow_y,
            d::STROKE,
            Some("uses · HTTPS"),
        );
        // SPA → Backend across the boundary — the /_rpc crossing, labelled with
        // the count of effectful actions that round-trip.
        let mx = (spa_x + spa_w + backend_x) / 2.0;
        let rpc_label = match g.rpc_effectful {
            Some(n) => format!("{n} effectful → /_rpc"),
            None => "/_rpc".to_string(),
        };
        svg.ortho(
            spa_x + spa_w,
            flow_y,
            backend_x,
            flow_y,
            d::SERVER_EDGE,
            Some(&rpc_label),
        );
        // Pure client actions never leave the wasm client — a caption under the
        // SPA container states how many.
        if let Some(pure) = g.rpc_pure {
            svg.caption(
                spa_x + spa_w / 2.0,
                spa_y + spa_h + 15.0,
                &format!("{pure} pure client actions (wasm)"),
                "middle",
            );
        }
        if c.auth {
            svg.lock(mx, flow_y + 12.0, d::SERVER_EDGE);
            svg.caption(mx + 10.0, flow_y + 20.0, "auth", "start");
        }
    } else {
        let via = match g.shape {
            AppShape::Tui | AppShape::Cli => "in-process",
            AppShape::Live => "HTTPS + SSE",
            _ if g.capabilities.contains(&Capability::Realtime) => "HTTPS + SSE",
            _ => "HTTPS",
        };
        let mx = (actor_cx + 20.0 + backend_x) / 2.0;
        svg.ortho(
            actor_cx + 20.0,
            flow_y,
            backend_x,
            flow_y,
            d::SERVER_EDGE,
            Some(via),
        );
        // A terminal app has no trust-boundary crossing to guard; only a
        // networked shape shows the auth lock.
        if c.auth && !g.shape.is_terminal() {
            svg.lock(mx, flow_y + 12.0, d::SERVER_EDGE);
            svg.caption(mx + 10.0, flow_y + 20.0, "auth", "start");
        }
    }

    // Backend → stores (a staggered comb, one drop per store, no trunk smear).
    let bus_y = backend_y + backend_h + store_vgap * 0.42;
    for (cx, edge, is_egress) in &store_centres {
        let color = if *is_egress { d::EXTERNAL } else { d::STROKE };
        svg.ortho_via(
            backend_cx,
            backend_y + backend_h,
            *cx,
            store_y,
            bus_y,
            false,
            color,
        );
        svg.plate_lines(*cx, (bus_y + store_y) / 2.0, &[edge.to_string()], color);
    }

    // Backend → external systems.
    for (i, cy) in ext_cys.iter().enumerate() {
        let mid = backend_x + backend_w + col_gap * 0.5 + i as f64 * 14.0;
        svg.ortho_via(
            backend_x + backend_w,
            flow_y,
            ext_x,
            *cy,
            mid,
            true,
            d::EXTERNAL,
        );
        svg.plate_lines(
            (backend_x + backend_w + ext_x) / 2.0,
            (flow_y + *cy) / 2.0,
            &["HTTPS".to_string()],
            d::EXTERNAL,
        );
    }

    // ---- legend (only rows the diagram actually uses) ----
    let mut rows: Vec<(String, String)> =
        vec![(d::BOUNDARY_TRUSTED.into(), "trust boundary (dashed)".into())];
    if !c.stores.is_empty() {
        rows.push((d::STROKE.into(), "data store".into()));
    }
    if c.egress.is_some() || n_ext > 0 {
        rows.push((d::EXTERNAL.into(), "egress / external system".into()));
    }
    match g.shape {
        AppShape::Spa => rows.push((d::SERVER_EDGE.into(), "/_rpc server round-trip".into())),
        AppShape::Live => rows.push((d::SERVER_EDGE.into(), "browser → server (HTTPS + SSE)".into())),
        AppShape::Tui | AppShape::Cli => {
            rows.push((d::SERVER_EDGE.into(), "in-process call".into()))
        }
        AppShape::Http => rows.push((d::SERVER_EDGE.into(), "client → server request".into())),
    }
    svg.legend(lx, zbottom + 18.0, &rows);

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
    /// Effects the branch runs (Db / Http / …), joined for display. `None` for a
    /// pure branch.
    pub effects: Option<String>,
    /// The effect families this branch reaches, structured — the call-path trace
    /// (`Db`, `Http`, `Auth`, `Email`, `File`, …). Empty for a pure branch.
    pub effect_families: Vec<String>,
    /// The Model fields the branch READS (the request Model-field inputs). Empty
    /// when it reads the whole model or reads nothing.
    pub read_fields: Vec<String>,
    /// The Model fields the branch WRITES (the response Model-field outputs).
    pub write_fields: Vec<String>,
    /// The written fields assigned fresh on EVERY response leaf (server-produced:
    /// a constant, a Msg arg, an effect result). These are NOT in the request —
    /// the server reproduces them. A written field NOT here is preserved from the
    /// client model on some path and DOES ride the request (soundness bug #1).
    pub always_written: Vec<String>,
    /// True when the request is the whole model (no field-level request schema).
    pub reads_whole_model: bool,
    /// True when the response is the whole model.
    pub writes_whole_model: bool,
    /// The Msg args this endpoint binds, with their types — the extra request
    /// inputs beside the read-set. Parallel to the request's `+ {args}`.
    pub msg_arg_tys: Vec<crate::spa_partition::ModelFieldTy>,
}

impl WireEndpoint {
    /// Whether this endpoint's call-path reaches an auth check (`Std.Auth`
    /// session / token verification) — a confidential, access-controlled path.
    pub fn touches_auth(&self) -> bool {
        self.effect_families.iter().any(|f| f == "Auth")
    }
}

/// What kind of HTTP endpoint a recovered route is. The distinction drives the
/// `wire` diagram's sections and the CSRF note.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug)]
pub enum EndpointKind {
    /// A `Std.App` `route` / `routeParam` — a browser PAGE, served as `GET`.
    PageRoute,
    /// A `Std.App` `api` / `Sky.Http.Server.api` — a RAW HTTP endpoint, outside
    /// the session/SSE contract and CSRF-exempt (an inbound webhook, a JSON API).
    RawApi,
    /// A `Sky.Http.Server.{get,post,put,delete,any}` route on an HTTP-server app.
    HttpRoute,
}

impl EndpointKind {
    /// A raw HTTP endpoint (`api`) is CSRF-exempt — it is reached by a third
    /// party (a webhook sender, an API client), not the app's own session.
    pub fn is_csrf_exempt(self) -> bool {
        matches!(self, EndpointKind::RawApi)
    }
}

/// One HTTP endpoint an app registers: a `Sky.Http.Server` route
/// (`Server.get "/path" handler`), a `Std.App` page route (`App.route "/" Home`),
/// or a `Std.App` raw endpoint (`App.api "POST /webhooks/stripe" handler`). The
/// `wire` diagram lists these — the RPC-less answer for a Sky.Live / Http app,
/// and, for a Spa app, the raw `App.api` endpoints that sit BESIDE `/_rpc`.
#[derive(Clone, PartialEq, Eq, PartialOrd, Ord, Debug)]
pub struct HttpEndpoint {
    /// The HTTP method (`GET`, `POST`, `PUT`, `DELETE`, `ANY`).
    pub method: String,
    /// The route path (`/`, `/hello/:name`, `/webhooks/stripe`).
    pub path: String,
    /// The handler function name (`Payments.handleWebhook`), or `<inline>` for a
    /// lambda / non-def handler, or `<page>` for a `Std.App` page route (the page
    /// constructor stands in for a handler the author never writes).
    pub handler: String,
    /// The endpoint kind — page route, raw API, or HTTP-server route.
    pub kind: EndpointKind,
}

/// The wire contract for a project — pure data the renderer consumes.
pub struct WireReport {
    /// The project path, relative to the repo root when possible.
    pub project: String,
    /// True when the resolved target is a Sky.Spa wasm client. When false the
    /// app has no `/_rpc` contract, but it may still have an HTTP endpoint map
    /// ([`WireReport::http_endpoints`]).
    pub is_spa: bool,
    /// The derived app shape — drives the target-aware sections and labels.
    pub shape: AppShape,
    /// The resolved `[app] target` (or the `--target` override), for the note.
    pub target: Option<String>,
    /// The RPC endpoints, sorted by Msg name for deterministic output.
    pub endpoints: Vec<WireEndpoint>,
    /// The registered HTTP endpoints — `Sky.Http.Server` routes, `Std.App` page
    /// routes, and `Std.App` raw `api` endpoints. On a Spa app these are the raw
    /// `api` endpoints that sit BESIDE `/_rpc` (an inbound webhook); on a Live app
    /// they are the page routes + raw api; on an HTTP app the whole route map.
    /// Sorted for deterministic output.
    pub http_endpoints: Vec<HttpEndpoint>,
    /// Set when the app IS a Spa client but no per-branch endpoints could be
    /// recovered (a `Std.App` inline-effect shape, or an `update` that is not a
    /// resolvable `case msg of`). The renderer prints what it has plus a note.
    pub limited: bool,
    /// The typed Model fields (`name`, `ty_name`) — the raw material the OpenAPI
    /// generator resolves each endpoint's request/response fields against.
    pub model_fields: Vec<crate::spa_partition::ModelFieldTy>,
    /// The app's data store display name (`PostgreSQL` / `SQLite`), from
    /// `sky.toml [database] driver` — the `Db`-effect target in the call-path.
    pub data_store: Option<String>,
    /// Non-fatal reader notes.
    pub notes: Vec<String>,
}

/// Render the request shape of a branch from its public [`crate::spa_partition::BranchIo`]
/// fields — the request field-set (or the whole model) plus the Msg args.
///
/// The request field-set is `reads ∪ writes`, not the reads alone. A field an
/// internal branch preserves through `{ model | ... }` must travel in the
/// request, or the server rebuilds it as the empty-model default and clobbers
/// the client value. So the diagram charts `request_fields()` /
/// `request_whole_model()`, which is the true wire the split emits.
fn wire_request(io: &crate::spa_partition::BranchIo) -> String {
    let mut parts: Vec<String> = Vec::new();
    if io.request_whole_model() {
        parts.push("whole model".to_string());
    } else {
        let req_fields = io.request_fields();
        if !req_fields.is_empty() {
            parts.push(fmt_fields(&req_fields));
        }
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

    // Recover the HTTP endpoint map + the two shape facts from the resolved HIR
    // (one load). Best-effort: a load failure degrades to no endpoints, not an
    // error — the report still explains what it has.
    let (all_endpoints, has_app_ui, has_http_routes) =
        match crate::build::load_source_db(repo_root, project_dir, entry_module) {
            Ok((db, _e, ids)) => recover_endpoints(&db, &ids),
            Err(_) => (Vec::new(), false, false),
        };
    let shape = app_shape(app_target, has_app_ui, has_http_routes);

    if !is_spa {
        // A non-Spa app has no `/_rpc` contract. It may still have an HTTP endpoint
        // map: a Sky.Live app's page routes + raw `App.api` endpoints, or a
        // Sky.Http.Server app's whole route table.
        let http_endpoints: Vec<HttpEndpoint> = match shape {
            // A terminal app has NO network boundary — do not list routes.
            AppShape::Tui | AppShape::Cli => Vec::new(),
            _ => all_endpoints,
        };
        let mut notes: Vec<String> = Vec::new();
        match shape {
            AppShape::Live => {
                notes.push(
                    "Sky.Live has no `/_rpc` contract: the browser and server share one \
                     persistent SSE channel per session, and every interaction round-trips over \
                     it. The routes below are the HTTP surface — page GET routes and any raw \
                     `App.api` endpoint."
                        .to_string(),
                );
            }
            AppShape::Http => {
                notes.push(
                    "HTTP endpoint map — every route this Sky.Http.Server app registers."
                        .to_string(),
                );
            }
            AppShape::Tui | AppShape::Cli => {
                notes.push(
                    "A terminal app has no network trust boundary: the user drives one local \
                     binary directly. There is no client/server wire to chart."
                        .to_string(),
                );
            }
            AppShape::Spa => {}
        }
        if http_endpoints.is_empty() && !shape.is_terminal() {
            let tgt = app_target.unwrap_or("<none>");
            notes.push(format!(
                "No `Sky.Http.Server` / `Std.App` route registration was found for target \
                 `{tgt}`, so there is no endpoint map to chart. Re-run with `--target web:app` \
                 (or a `mobile:` / `desktop:` / `tablet:` client) to chart the Sky.Spa `/_rpc` \
                 contract."
            ));
        }
        return Ok(WireReport {
            project,
            is_spa: false,
            shape,
            target: app_target.map(str::to_string),
            endpoints: Vec::new(),
            http_endpoints,
            limited: false,
            model_fields: Vec::new(),
            data_store: data_store_name(project_dir),
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
        let ctor = b
            .msg
            .split_whitespace()
            .next()
            .unwrap_or(&b.msg)
            .to_string();
        // The effect families the branch reaches (`Db`, `Http`, `Auth`, …), now
        // surfaced by the partition report per branch.
        let effects = if b.effect_families.is_empty() {
            None
        } else {
            Some(b.effect_families.join(", "))
        };
        endpoints.push(WireEndpoint {
            msg: ctor,
            request: wire_request(io),
            response: wire_response(io),
            effects,
            effect_families: b.effect_families.clone(),
            read_fields: io.read_fields.clone(),
            write_fields: io.write_fields.clone(),
            always_written: io.always_written.clone(),
            reads_whole_model: io.reads_whole_model,
            writes_whole_model: io.writes_whole_model,
            msg_arg_tys: b.msg_arg_tys.clone(),
        });
    }
    endpoints.sort_by(|a, b| a.msg.cmp(&b.msg));

    // Raw `App.api` endpoints sit BESIDE `/_rpc` — an inbound webhook, a JSON API
    // reached by a third party, not the wasm client. List them as HTTP endpoints.
    let http_endpoints: Vec<HttpEndpoint> = all_endpoints
        .into_iter()
        .filter(|e| e.kind == EndpointKind::RawApi)
        .collect();

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
    if !http_endpoints.is_empty() {
        notes.push(
            "The `HTTP endpoints` are raw `App.api` routes: reached by a third party (a webhook \
             sender, an API client), OUTSIDE the session/CSRF contract, and CSRF-exempt."
                .to_string(),
        );
    }

    Ok(WireReport {
        project,
        is_spa: true,
        shape: AppShape::Spa,
        target: app_target.map(str::to_string),
        endpoints,
        http_endpoints,
        limited,
        model_fields: report.model_fields.clone(),
        data_store: data_store_name(project_dir),
        notes,
    })
}

/// Split an `api "METHOD /path"` spec into `(METHOD, /path)`; a spec with no
/// method word becomes `("ANY", spec)`.
fn split_api_spec(spec: &str) -> (String, String) {
    let mut it = spec.splitn(2, char::is_whitespace);
    let first = it.next().unwrap_or("").to_string();
    match it.next() {
        Some(rest) if !rest.trim().is_empty() => (first.to_uppercase(), rest.trim().to_string()),
        _ => ("ANY".to_string(), spec.to_string()),
    }
}

/// The handler def named by call `args[idx]`. `qualify` prefixes the owning
/// module (`Payments.handleWebhook`) — used for a raw `api` endpoint whose
/// handler often lives in another module; a plain `Sky.Http.Server` route keeps
/// the bare def name. `<inline>` for a lambda / non-def handler.
fn handler_name(db: &dyn SkyDb, body: &Body, args: &[ExprId], idx: usize, qualify: bool) -> String {
    args.get(idx)
        .and_then(|a| match &body.exprs[*a] {
            Expr::Var(Res::Def(hd)) => db.def_loc(*hd).map(|l| {
                let m = db.module_name(l.module);
                if qualify && !m.is_empty() {
                    format!("{m}.{}", l.name.as_str())
                } else {
                    l.name.as_str().to_string()
                }
            }),
            _ => None,
        })
        .unwrap_or_else(|| "<inline>".to_string())
}

/// Recover an app's HTTP endpoint map from the resolved HIR, over BOTH route
/// registrars:
///   * `Sky.Http.Server.{get,post,put,delete,any}` → an [`EndpointKind::HttpRoute`];
///     `Sky.Http.Server.api "METHOD /path" h` → [`EndpointKind::RawApi`].
///   * `Std.App.{route,routeParam} "/path" Page` → a [`EndpointKind::PageRoute`]
///     (GET; the page constructor stands in for the handler);
///     `Std.App.api "METHOD /path" h` → [`EndpointKind::RawApi`] (CSRF-exempt).
/// Returns the endpoints plus two shape facts: `has_std_app_ui` (any `Std.App`
/// route/api) and `has_http_routes` (any `Sky.Http.Server` route). Read-only.
fn recover_endpoints(db: &dyn SkyDb, check_ids: &[ModuleId]) -> (Vec<HttpEndpoint>, bool, bool) {
    let mut out: BTreeSet<HttpEndpoint> = BTreeSet::new();
    let mut has_std_app_ui = false;
    let mut has_http_routes = false;
    for mid in check_ids {
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
                let module = db.module_name(loc.module);
                let fname = loc.name.as_str();
                if args.is_empty() {
                    continue;
                }
                let Expr::Str(spec) = &body.exprs[args[0]] else {
                    continue;
                };
                let spec = spec.to_string();
                let (method, path, kind) = if module == "Sky.Http.Server" {
                    has_http_routes = true;
                    match fname {
                        "get" => ("GET".to_string(), spec.clone(), EndpointKind::HttpRoute),
                        "post" => ("POST".to_string(), spec.clone(), EndpointKind::HttpRoute),
                        "put" => ("PUT".to_string(), spec.clone(), EndpointKind::HttpRoute),
                        "delete" => ("DELETE".to_string(), spec.clone(), EndpointKind::HttpRoute),
                        "any" => ("ANY".to_string(), spec.clone(), EndpointKind::HttpRoute),
                        "api" => {
                            let (m, p) = split_api_spec(&spec);
                            (m, p, EndpointKind::RawApi)
                        }
                        _ => continue,
                    }
                } else if module == "Std.App" {
                    match fname {
                        "route" | "routeParam" => {
                            has_std_app_ui = true;
                            // A page route's "handler" slot is the page ctor.
                            let page = match args.get(1).map(|a| &body.exprs[*a]) {
                                Some(Expr::Var(Res::Ctor(cref))) => db
                                    .def_loc(cref.def)
                                    .map(|l| l.name.as_str().to_string())
                                    .unwrap_or_else(|| "<page>".to_string()),
                                _ => "<page>".to_string(),
                            };
                            out.insert(HttpEndpoint {
                                method: "GET".to_string(),
                                path: spec.clone(),
                                handler: page,
                                kind: EndpointKind::PageRoute,
                            });
                            continue;
                        }
                        "api" => {
                            has_std_app_ui = true;
                            let (m, p) = split_api_spec(&spec);
                            (m, p, EndpointKind::RawApi)
                        }
                        _ => continue,
                    }
                } else {
                    continue;
                };
                // A raw `api` handler is often in another module — qualify it; a
                // plain HTTP route keeps its bare def name (existing behaviour).
                let handler = handler_name(db, body, args, 1, kind == EndpointKind::RawApi);
                out.insert(HttpEndpoint {
                    method,
                    path,
                    handler,
                    kind,
                });
            }
        }
    }
    (out.into_iter().collect(), has_std_app_ui, has_http_routes)
}

/// Enumerate the `Std.Db` TABLE names an app declares — every
/// `Std.Db.Schema.table "<name>" …` and `Std.Db.Store.fromCodec "<name>" …`
/// whose first argument is a string literal. Sorted and deduped. (`Store.project`
/// takes a `List Table`, not a name string, so it is not a source here.)
/// Read-only.
fn collect_db_tables(db: &dyn SkyDb, check_ids: &[ModuleId]) -> Vec<String> {
    let mut out: BTreeSet<String> = BTreeSet::new();
    for mid in check_ids {
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
                let module = db.module_name(loc.module);
                let fname = loc.name.as_str();
                let is_table = (module == "Std.Db.Schema" && fname == "table")
                    || (module == "Std.Db.Store" && fname == "fromCodec");
                if !is_table {
                    continue;
                }
                if let Some(Expr::Str(name)) = args.first().map(|a| &body.exprs[*a]) {
                    out.insert(name.to_string());
                }
            }
        }
    }
    out.into_iter().collect()
}

/// Render a wire report to the requested format. Pure function of `r`.
pub fn render_wire(r: &WireReport, format: Format) -> String {
    match format {
        Format::Puml => render_wire_puml(r),
        Format::Md => render_wire_md(r),
        Format::Svg => render_wire_svg(r),
    }
}

/// A one-line label for an [`EndpointKind`], for the md/svg endpoint tables.
fn endpoint_kind_label(k: EndpointKind) -> &'static str {
    match k {
        EndpointKind::PageRoute => "page",
        EndpointKind::RawApi => "raw api · CSRF-exempt",
        EndpointKind::HttpRoute => "http",
    }
}

/// Render the HTTP endpoint table (page routes + raw api + http routes) as md.
fn wire_http_md(o: &mut String, eps: &[HttpEndpoint]) {
    o.push_str("| Method | Path | Handler / page | Kind |\n|---|---|---|---|\n");
    for e in eps {
        o.push_str(&format!(
            "| {} | {} | {} | {} |\n",
            md_cell(&e.method),
            md_cell(&e.path),
            md_cell(&e.handler),
            endpoint_kind_label(e.kind),
        ));
    }
}

/// Map an endpoint's effect families to their concrete call-path targets — the
/// audit-relevant "what this endpoint reaches": `Db → PostgreSQL`, `Http →
/// external`, `Auth → session`. Non-target families (Log/System/Time/Uuid) are
/// listed plainly. Empty → "—" (a pure branch).
fn wire_callpath(fams: &[String], store: Option<&str>) -> String {
    if fams.is_empty() {
        return "—".to_string();
    }
    let mut parts: Vec<String> = Vec::new();
    for f in fams {
        let mapped = match f.as_str() {
            "Db" => format!("Db → {}", store.unwrap_or("database")),
            "Http" => "Http → external".to_string(),
            "Auth" => "Auth → session/token".to_string(),
            "Email" => "Email → mail service".to_string(),
            "File" => "File → filesystem".to_string(),
            other => other.to_string(),
        };
        parts.push(mapped);
    }
    parts.join(" · ")
}

/// The access requirement for an RPC endpoint: the double-submit CSRF token is
/// always required (it is inside the session contract), and a `🔒 auth` marker
/// when the call-path verifies a `Std.Auth` session.
fn wire_rpc_access(e: &WireEndpoint) -> String {
    if e.touches_auth() {
        "CSRF + 🔒 auth".to_string()
    } else {
        "CSRF".to_string()
    }
}

fn render_wire_md(r: &WireReport) -> String {
    let mut o = String::new();
    o.push_str(&format!("# Wire — API & call-paths — {}\n\n", r.project));
    if let Some(store) = &r.data_store {
        o.push_str(&format!("**Data store:** {store}\n\n"));
    }
    if r.is_spa && !r.endpoints.is_empty() {
        o.push_str(
            "The Sky.Spa auto-split turns every SERVER `update` branch into a \
             `POST /_rpc/<Msg>` endpoint. The REQUEST is the Model fields the branch \
             reads plus the Msg args; the RESPONSE is the Model fields it writes. \
             **Access** is the endpoint's auth requirement; **Call-path** is the effect \
             families it reaches and their targets.\n\n",
        );
        o.push_str("## RPC endpoints (/_rpc)\n\n");
        o.push_str("| Endpoint | Access | Request (fields + args) | Response (writes) | Call-path |\n");
        o.push_str("|---|---|---|---|---|\n");
        let store = r.data_store.as_deref();
        for e in &r.endpoints {
            o.push_str(&format!(
                "| POST /_rpc/{} | {} | {} | {} | {} |\n",
                md_cell(&e.msg),
                md_cell(&wire_rpc_access(e)),
                md_cell(&e.request),
                md_cell(&e.response),
                md_cell(&wire_callpath(&e.effect_families, store)),
            ));
        }
        if !r.http_endpoints.is_empty() {
            o.push_str("\n## HTTP endpoints (raw `App.api`, beside /_rpc)\n\n");
            o.push_str("> ⚠ These are CSRF-exempt — reached by a third party (webhook / API client), outside the session contract. Verify each authenticates its caller.\n\n");
            wire_http_md(&mut o, &r.http_endpoints);
        }
    } else if !r.http_endpoints.is_empty() {
        match r.shape {
            AppShape::Live => o.push_str("HTTP route table (Sky.Live: page GET routes + raw api).\n\n"),
            AppShape::Http => o.push_str("HTTP endpoint map — every route this server registers.\n\n"),
            _ => o.push_str("HTTP endpoint map.\n\n"),
        }
        wire_http_md(&mut o, &r.http_endpoints);
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
    let mut o = puml_header(&format!("Wire (data-flow) — {}", r.project));
    if r.shape.is_terminal() {
        o.push_str("rectangle \"User (terminal)\" as U\n");
        o.push_str("rectangle \"App (single binary)\" as A\n");
        o.push_str("U --> A : local I/O (no network boundary)\n");
        o.push_str(&puml_footer());
        return o;
    }
    let spa = r.is_spa && !r.endpoints.is_empty();
    let have_http = !r.http_endpoints.is_empty();
    if spa || have_http {
        // The two entities either side of the trust boundary.
        o.push_str("rectangle \"Client · untrusted\" <<boundary>> {\n");
        o.push_str("  actor \"Client\" as C\n");
        o.push_str("}\n");
        o.push_str("rectangle \"Server · trusted\" <<boundary>> {\n");
        o.push_str("  rectangle \"Server\\n«process»\" as S <<container>>\n");
        // /_rpc endpoints (spa) as processes inside the server boundary.
        for (i, e) in r.endpoints.iter().enumerate() {
            o.push_str(&format!(
                "  rectangle \"POST /_rpc/{}\" as ep{i} <<endpoint>>\n",
                e.msg
            ));
        }
        for (i, e) in r.http_endpoints.iter().enumerate() {
            o.push_str(&format!(
                "  rectangle \"{} {}\" as hep{i} <<endpoint>>\n",
                e.method, e.path
            ));
        }
        o.push_str("}\n");
        // Per-endpoint effect families (from the partition report) as notes.
        for (i, e) in r.endpoints.iter().enumerate() {
            if let Some(fx) = &e.effects {
                o.push_str(&format!("note right of ep{i} : effects: {}\n", puml_msg_text(fx)));
            }
        }
        // An external inbound entity for any raw `api` endpoint (a webhook sender).
        if r.http_endpoints.iter().any(|e| e.kind == EndpointKind::RawApi) {
            o.push_str("rectangle \"External\\n«webhook / API client»\" as X <<boundary>>\n");
        }
        // Each /_rpc endpoint is one request in + one response out.
        for (i, e) in r.endpoints.iter().enumerate() {
            o.push_str(&format!(
                "C -[{}]-> ep{i} : req {}\n",
                diagram_svg::SERVER_EDGE,
                puml_msg_text(&e.request),
            ));
            o.push_str(&format!(
                "ep{i} -[{}]-> C : resp {}\n",
                diagram_svg::CLIENT_EDGE,
                puml_msg_text(&e.response),
            ));
        }
        for (i, e) in r.http_endpoints.iter().enumerate() {
            let from = if e.kind == EndpointKind::RawApi { "X" } else { "C" };
            o.push_str(&format!(
                "{from} -[{}]-> hep{i} : {}\n",
                diagram_svg::SERVER_EDGE,
                puml_msg_text(&e.handler),
            ));
        }
        let title = if spa {
            "/_rpc contract + HTTP endpoints"
        } else if r.shape == AppShape::Live {
            "Sky.Live routes (SSE session channel)"
        } else {
            "HTTP endpoint map"
        };
        o.push_str("legend right\n");
        o.push_str(&format!("  <b>Data-flow ({title})</b>\n"));
        o.push_str("  boundary = trust zone (dashed)\n");
        o.push_str("  orange = request (client -> server)\n");
        o.push_str("  blue = response (server -> client)\n");
        o.push_str("endlegend\n");
    } else {
        o.push_str("rectangle \"Client\" as C\n");
        o.push_str("rectangle \"Server\" as S\n");
        o.push_str("C .. S : no client boundary to chart\n");
    }
    o.push_str(&puml_footer());
    o
}

/// Sanitise a message-label string for a PlantUML `->` arrow label.
fn puml_msg_text(s: &str) -> String {
    s.replace('\n', " ").replace('\r', " ").replace(':', " ")
}

/// One column of a wire table: a header, its left x, and the header colour.
struct TCol {
    title: String,
    x: f64,
    color: &'static str,
}

/// Draw a bordered table (package box + column headers + wrapped rows) at
/// `(sec_x, sec_top)`, width `sec_w`. `rows` is row-major; each row carries one
/// wrapped-line list per column (parallel to `cols`). Returns the table's bottom
/// Y. A table never overflows a column: cells are pre-wrapped by the caller.
fn draw_wire_table(
    svg: &mut diagram_svg::Svg,
    sec_x: f64,
    sec_top: f64,
    sec_w: f64,
    title: &str,
    cols: &[TCol],
    rows: &[Vec<Vec<String>>],
) -> f64 {
    use diagram_svg as d;
    let pad = 12.0;
    let sec_hdr = 42.0;
    let line_h = 15.0;
    let row_pad = 10.0;
    let row_heights: Vec<f64> = rows
        .iter()
        .map(|r| {
            let n = r.iter().map(|c| c.len()).max().unwrap_or(1).max(1);
            n as f64 * line_h + row_pad
        })
        .collect();
    let body_h: f64 = row_heights.iter().sum::<f64>().max(line_h);
    let sec_h = sec_hdr + body_h + pad;
    svg.package(sec_x, sec_top, sec_w, sec_h, title);
    let hdr_y = sec_top + sec_hdr - 4.0;
    for c in cols {
        svg.text(c.x, hdr_y, &c.title, "start", 10.5, "700", c.color);
    }
    let mut ry = hdr_y + 8.0;
    for (row, rh) in rows.iter().zip(&row_heights) {
        svg.rule(sec_x + pad - 4.0, ry, sec_x + sec_w - pad, ry, "#e2e5ea", false);
        let base = ry + line_h;
        for (ci, cell) in row.iter().enumerate() {
            let cx = cols[ci].x;
            let (size, weight) = if ci == 0 { (11.0, "600") } else { (10.5, "500") };
            for (i, l) in cell.iter().enumerate() {
                svg.text(cx, base + i as f64 * line_h, l, "start", size, weight, d::TEXT);
            }
        }
        ry += rh;
    }
    sec_top + sec_h
}

/// A DATA-FLOW diagram (DFD). The headline is the trust boundary: a Client
/// external entity (untrusted) and a Server process (trusted) either side of a
/// dashed boundary line, with a representative request/response crossing it,
/// labelled for the app shape (`/_rpc` for Spa, an SSE session channel for Live,
/// HTTP for an API). Below it, one or two neat tables list every endpoint —
/// never a hairball of crossing arrows. A terminal app has no boundary at all.
fn render_wire_svg(r: &WireReport) -> String {
    use diagram_svg as d;
    let mut svg = d::Svg::new(&format!("Wire (data-flow) — {}", r.project));

    // Terminal: no network boundary — say so in one clear line, never a broken
    // diagram.
    if r.shape.is_terminal() {
        svg.text(
            8.0,
            26.0,
            "A terminal app has no client/server wire.",
            "start",
            13.0,
            "600",
            d::TEXT,
        );
        svg.text(
            8.0,
            48.0,
            "The user drives one local binary directly; there is no trust boundary to cross.",
            "start",
            11.0,
            "400",
            d::SUBTLE,
        );
        return svg.render();
    }

    let spa = r.is_spa && !r.endpoints.is_empty();
    let have_http = !r.http_endpoints.is_empty();
    if !spa && !have_http {
        svg.text(
            8.0,
            20.0,
            "No client boundary to chart.",
            "start",
            12.0,
            "400",
            d::SUBTLE,
        );
        return svg.render();
    }

    // ---- headline band: Client entity | boundary | Server process ----
    let lx = 8.0;
    let zpad = 16.0;
    let zhdr = 22.0;
    let ztop = 12.0;
    let band_content_h = 62.0;
    let band_h = zhdr + zpad + band_content_h + zpad;
    let flow_y = ztop + zhdr + zpad + band_content_h / 2.0;

    let client_zone_w = 150.0;
    let server_zone_w = 190.0;
    let gap = 160.0;
    let client_zone_x = lx;
    let server_zone_x = client_zone_x + client_zone_w + gap;
    let boundary_x = client_zone_x + client_zone_w + gap / 2.0;

    let client_label = if r.shape == AppShape::Live {
        "Browser · untrusted"
    } else {
        "Client · untrusted"
    };
    svg.zone(
        client_zone_x,
        ztop,
        client_zone_w,
        band_h,
        client_label,
        d::BOUNDARY_UNTRUSTED,
    );
    let client_glyph = if r.shape == AppShape::Live {
        "Browser"
    } else {
        "Client"
    };
    svg.actor(client_zone_x + client_zone_w / 2.0, flow_y - 30.0, client_glyph);
    svg.zone(
        server_zone_x,
        ztop,
        server_zone_w,
        band_h,
        "Server · trusted",
        d::BOUNDARY_TRUSTED,
    );
    let srv_x = server_zone_x + zpad;
    let srv_w = server_zone_w - 2.0 * zpad;
    let srv_sub = match r.shape {
        AppShape::Spa => "wasm reaches over /_rpc",
        AppShape::Live => "SSR + one SSE per session",
        _ => "HTTP process",
    };
    svg.container(
        srv_x,
        flow_y - band_content_h / 2.0,
        srv_w,
        band_content_h,
        d::FILL,
        "Server",
        "process",
        Some(srv_sub),
    );

    // The trust boundary: a dashed vertical line the crossings must pass.
    let band_bottom = ztop + band_h;
    svg.rule(boundary_x, ztop + 4.0, boundary_x, band_bottom - 16.0, d::SUBTLE, true);
    svg.text(
        boundary_x,
        band_bottom - 3.0,
        "trust boundary",
        "middle",
        9.5,
        "600",
        d::SUBTLE,
    );

    // Representative request/response crossing — labelled for the shape.
    let (req_label, resp_label) = match r.shape {
        AppShape::Spa => ("request · /_rpc".to_string(), "response".to_string()),
        AppShape::Live => ("interaction · SSE".to_string(), "patch · SSE".to_string()),
        _ => ("request · HTTP".to_string(), "response".to_string()),
    };
    let client_edge_x = client_zone_x + client_zone_w / 2.0 + 20.0;
    svg.ortho(client_edge_x, flow_y - 12.0, srv_x, flow_y - 12.0, d::SERVER_EDGE, Some(&req_label));
    svg.ortho(srv_x, flow_y + 14.0, client_edge_x, flow_y + 14.0, d::CLIENT_EDGE, Some(&resp_label));

    let sec_x = lx;
    let mut y = band_bottom + 28.0;
    let wrap_chars = 28usize;
    let wrap_col = |s: &str, w: usize| d::wrap(s, w);

    // ---- 1. RPC endpoints table (Spa) ----
    if spa {
        let pad = 12.0;
        let c1_x = sec_x + pad;
        let c1_w = 168.0;
        let c2_x = c1_x + c1_w + 18.0;
        let c2_w = 210.0;
        let c3_x = c2_x + c2_w + 18.0;
        let c3_w = 210.0;
        let c4_x = c3_x + c3_w + 18.0;
        let c4_w = 128.0;
        let sec_w = c4_x + c4_w + pad - sec_x;
        let cols = vec![
            TCol { title: "Endpoint (Msg)".into(), x: c1_x, color: d::SUBTLE },
            TCol { title: "Request (client → server)".into(), x: c2_x, color: d::SERVER_EDGE },
            TCol { title: "Response (server → client)".into(), x: c3_x, color: d::CLIENT_EDGE },
            TCol { title: "Effects".into(), x: c4_x, color: d::EXTERNAL },
        ];
        let rows: Vec<Vec<Vec<String>>> = r
            .endpoints
            .iter()
            .map(|e| {
                vec![
                    wrap_col(&format!("POST /_rpc/{}", e.msg), 22),
                    wrap_col(&e.request, wrap_chars),
                    wrap_col(&e.response, wrap_chars),
                    wrap_col(e.effects.as_deref().unwrap_or("—"), 16),
                ]
            })
            .collect();
        y = draw_wire_table(&mut svg, sec_x, y, sec_w, "RPC endpoints (/_rpc)", &cols, &rows) + 24.0;
    }

    // ---- 2. HTTP endpoints table ----
    if have_http {
        let pad = 12.0;
        // For a Spa app the http endpoints are raw `api` (webhooks): draw an
        // inbound EXTERNAL entity crossing the boundary to them. For Live/Http the
        // table is the whole route map.
        let raw_only = r.http_endpoints.iter().all(|e| e.kind == EndpointKind::RawApi);
        let ext_h = if spa && raw_only { 92.0 } else { 0.0 };
        let ext_w = 150.0;
        let table_x = if ext_h > 0.0 { sec_x + ext_w + 96.0 } else { sec_x };

        let c1_x = table_x + pad;
        let c1_w = 72.0;
        let c2_x = c1_x + c1_w + 16.0;
        let c2_w = 214.0;
        let c3_x = c2_x + c2_w + 16.0;
        let c3_w = 214.0;
        let c4_x = c3_x + c3_w + 16.0;
        let c4_w = 150.0;
        let sec_w = c4_x + c4_w + pad - table_x;
        let cols = vec![
            TCol { title: "Method".into(), x: c1_x, color: d::SERVER_EDGE },
            TCol { title: "Path".into(), x: c2_x, color: d::SUBTLE },
            TCol { title: "Handler / page".into(), x: c3_x, color: d::SUBTLE },
            TCol { title: "Kind".into(), x: c4_x, color: d::EXTERNAL },
        ];
        let rows: Vec<Vec<Vec<String>>> = r
            .http_endpoints
            .iter()
            .map(|e| {
                vec![
                    vec![e.method.clone()],
                    wrap_col(&e.path, 26),
                    wrap_col(&e.handler, 26),
                    wrap_col(endpoint_kind_label(e.kind), 16),
                ]
            })
            .collect();
        let title = if spa {
            "HTTP endpoints (raw api · CSRF-exempt)"
        } else if r.shape == AppShape::Live {
            "HTTP routes (pages + raw api)"
        } else {
            "HTTP endpoints"
        };
        let table_top = y;
        let table_bottom = draw_wire_table(&mut svg, table_x, table_top, sec_w, title, &cols, &rows);

        // The inbound external entity for a Spa app's raw webhooks.
        if ext_h > 0.0 {
            let ex_y = table_top + 6.0;
            let ex_cy = ex_y + ext_h / 2.0;
            svg.zone(sec_x, ex_y - 6.0, ext_w, ext_h + 12.0, "External · untrusted", d::EXTERNAL);
            svg.container(
                sec_x + 10.0,
                ex_y + 8.0,
                ext_w - 20.0,
                ext_h - 24.0,
                d::FILL,
                "Webhook sender",
                "external",
                Some("e.g. Stripe"),
            );
            // A dashed boundary + one inbound arrow into the table.
            let bx = sec_x + ext_w + 44.0;
            svg.rule(bx, table_top, bx, table_bottom, d::SUBTLE, true);
            svg.text(bx, table_bottom + 11.0, "trust boundary", "middle", 9.0, "600", d::SUBTLE);
            svg.ortho(sec_x + ext_w, ex_cy, table_x, ex_cy, d::SERVER_EDGE, Some("inbound"));
        }
        y = table_bottom;
    }

    // ---- legend ----
    let mut rows_l = vec![
        (d::BOUNDARY_TRUSTED.to_string(), "trust boundary (dashed)".to_string()),
        (d::SERVER_EDGE.to_string(), "request (client → server)".to_string()),
        (d::CLIENT_EDGE.to_string(), "response (server → client)".to_string()),
    ];
    if have_http && r.http_endpoints.iter().any(|e| e.kind == EndpointKind::RawApi) {
        rows_l.push((d::EXTERNAL.to_string(), "raw api · inbound external".to_string()));
    }
    svg.legend(sec_x, y + 18.0, &rows_l);
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
            Sink::Logs => "structured logs (console; OTel when OTEL_EXPORTER_OTLP_ENDPOINT set)",
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

    /// The egress grouping an auditor reads: does this data stay INTERNAL, leave
    /// the system as EXTERNAL egress, or record a Consent decision?
    fn group(self) -> &'static str {
        match self {
            Sink::Logs => "Internal",
            Sink::Analytics => "External egress",
            Sink::Consent => "Consent",
        }
    }

    /// The edge / accent colour for this sink's group.
    fn edge_color(self) -> &'static str {
        match self {
            Sink::Logs => diagram_svg::STROKE,
            Sink::Analytics => diagram_svg::EXTERNAL,
            Sink::Consent => diagram_svg::CLIENT_EDGE,
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
    let mut o = puml_header(&format!("Telemetry (data egress) — {}", r.project));
    let (modules, sinks) = telemetry_nodes(r);
    o.push_str("rectangle \"App modules\" <<boundary>> {\n");
    for m in &modules {
        o.push_str(&format!("  component \"{}\" as {}\n", m, module_node_id(m)));
    }
    o.push_str("}\n");
    // Sinks grouped by egress destination, in a fixed order.
    for group in ["Internal", "External egress", "Consent"] {
        let present: Vec<&Sink> = sinks.iter().filter(|s| s.group() == group).collect();
        if present.is_empty() {
            continue;
        }
        o.push_str(&format!("rectangle \"{group}\" <<boundary>> {{\n"));
        for s in present {
            o.push_str(&format!("  {}\n", s.puml_decl()));
        }
        o.push_str("}\n");
    }
    for (m, s, label) in telemetry_edges(r) {
        o.push_str(&format!(
            "{} -[{}]-> {} : {}\n",
            module_node_id(&m),
            s.edge_color(),
            s.node_id(),
            label,
        ));
    }
    o.push_str("legend right\n");
    o.push_str("  <b>Data egress inventory</b>\n");
    o.push_str("  what behavioural data leaves, and to where\n");
    o.push_str("  grey = internal · violet = external egress\n");
    o.push_str("  blue = consent state\n");
    o.push_str("endlegend\n");
    o.push_str(&puml_footer());
    o
}

/// A data-EGRESS inventory. Modules on the left; sinks on the right, grouped into
/// INTERNAL / EXTERNAL egress / Consent sections. One collapsed edge per
/// module→sink pair carries the comma-joined events (never parallel overlapping
/// edges). An auditor reads it as "what behavioural data leaves, and to where".
/// The capped event lines for one telemetry edge label: many events on one
/// module→sink pair are summarised (first few + "+N more") and wrapped, so a
/// chatty module — a CLI that logs a dozen lines — does not produce one giant
/// unbounded label. The FULL list stays in the `md` table.
fn telemetry_edge_lines(label: &str) -> Vec<String> {
    let events: Vec<&str> = label.split(", ").collect();
    let cap = 5usize;
    let joined = if events.len() > cap {
        events[..cap].join(", ")
    } else {
        label.to_string()
    };
    let mut lines = diagram_svg::wrap(&joined, 24);
    if lines.len() > 6 {
        lines.truncate(6);
        lines.push("…".into());
    }
    if events.len() > cap {
        lines.push(format!("+{} more events", events.len() - cap));
    }
    lines
}

fn render_telemetry_svg(r: &TelemetryReport) -> String {
    use diagram_svg as d;
    let mut svg = d::Svg::new(&format!("Telemetry (data egress) — {}", r.project));
    let (modules, sinks) = telemetry_nodes(r);

    let mod_w = 190.0;
    let sink_w = 176.0;
    let lx = 8.0;
    let mod_x = lx;
    let col_gap = 280.0;
    let sink_x = mod_x + mod_w + col_gap;

    // Pre-compute edges + capped labels, and reserve headroom above the first
    // module so a tall edge plate never overruns the title band.
    let edges: Vec<(String, Sink, Vec<String>)> = telemetry_edges(r)
        .into_iter()
        .map(|(m, s, label)| (m, s, telemetry_edge_lines(&label)))
        .collect();
    let max_lines = edges.iter().map(|(_, _, l)| l.len()).max().unwrap_or(1);
    let top = 14.0 + (d::Svg::plate_height(max_lines) / 2.0 - NODE_H / 2.0).max(0.0) + 24.0;

    // Left: module nodes stacked, with the row step widened when the edge plates
    // are tall so a plate never runs into the next module's plate.
    let mod_step = (NODE_H + VGAP).max(d::Svg::plate_height(max_lines) + 16.0);
    let mut mod_cy: std::collections::HashMap<String, f64> = std::collections::HashMap::new();
    let mut my = top;
    for m in &modules {
        svg.node(
            mod_x,
            my + (mod_step - NODE_H) / 2.0,
            mod_w,
            NODE_H,
            d::FILL,
            d::STROKE,
            m,
            None,
        );
        mod_cy.insert(m.clone(), my + mod_step / 2.0);
        my += mod_step;
    }
    let mod_block_bottom = my - (mod_step - NODE_H);

    // Right: sinks grouped in fixed order, each in its own labelled section box.
    let sink_h = 50.0;
    let sec_pad = 12.0;
    let sec_hdr = 24.0;
    let sec_gap = 20.0;
    let mut sink_cy: std::collections::HashMap<Sink, f64> = std::collections::HashMap::new();
    let mut sy = top;
    for group in ["Internal", "External egress", "Consent"] {
        let present: Vec<Sink> = sinks
            .iter()
            .copied()
            .filter(|s| s.group() == group)
            .collect();
        if present.is_empty() {
            continue;
        }
        let sec_h = sec_hdr
            + sec_pad
            + present.len() as f64 * sink_h
            + (present.len() as f64 - 1.0) * 10.0
            + sec_pad;
        let accent = present[0].edge_color();
        svg.zone(
            sink_x - sec_pad,
            sy,
            sink_w + 2.0 * sec_pad,
            sec_h,
            group,
            accent,
        );
        let mut iy = sy + sec_hdr + sec_pad;
        for s in &present {
            svg_shaped_node(
                &mut svg,
                s.svg_shape(),
                sink_x,
                iy,
                sink_w,
                sink_h,
                s.node_label(),
            );
            sink_cy.insert(*s, iy + sink_h / 2.0);
            iy += sink_h + 10.0;
        }
        sy += sec_h + sec_gap;
    }
    let sink_block_bottom = sy - sec_gap;

    // Collapsed edges: one per (module, sink). A module with several sinks fans
    // out; its labels are staggered vertically around the module row so no two
    // ever share a line, and each edge's bend X is staggered across the gap so no
    // two trunks smear together.
    let n_edges = edges.len().max(1) as f64;
    // Per-module edge count + running index, to stagger same-module labels.
    let mut per_mod_total: std::collections::HashMap<String, usize> =
        std::collections::HashMap::new();
    for (m, _, _) in &edges {
        *per_mod_total.entry(m.clone()).or_default() += 1;
    }
    let mut per_mod_seen: std::collections::HashMap<String, usize> =
        std::collections::HashMap::new();
    for (i, (m, s, lines)) in edges.iter().enumerate() {
        if let (Some(myc), Some(syc)) = (mod_cy.get(m), sink_cy.get(s)) {
            let color = s.edge_color();
            let kc = *per_mod_total.get(m).unwrap_or(&1);
            let k = per_mod_seen.entry(m.clone()).or_insert(0);
            // Stagger the exit Y across the module's right edge for its own edges.
            let exit_y = *myc
                + (*k as f64 - (kc as f64 - 1.0) / 2.0) * (d::Svg::plate_height(max_lines) + 8.0);
            *k += 1;
            let frac = (i as f64 + 1.0) / (n_edges + 1.0);
            let mid_x = mod_x + mod_w + col_gap * (0.30 + 0.42 * frac);
            svg.ortho_via(mod_x + mod_w, exit_y, sink_x, *syc, mid_x, true, color);
            // Event label near the module end, at the (staggered) exit row.
            let plate_w = lines
                .iter()
                .map(|l| d::text_width(l, 10.5))
                .fold(0.0, f64::max)
                + 12.0;
            svg.plate_lines(mod_x + mod_w + 16.0 + plate_w / 2.0, exit_y, lines, color);
        }
    }

    // Legend (only the sink groups this app actually uses).
    let mut rows: Vec<(String, String)> = Vec::new();
    if sinks.contains(&Sink::Logs) {
        rows.push((
            d::STROKE.to_string(),
            "internal (logs / console)".to_string(),
        ));
    }
    if sinks.contains(&Sink::Analytics) {
        rows.push((
            d::EXTERNAL.to_string(),
            "external egress (analytics store)".to_string(),
        ));
    }
    if sinks.contains(&Sink::Consent) {
        rows.push((d::CLIENT_EDGE.to_string(), "consent state".to_string()));
    }
    let legend_y = mod_block_bottom.max(sink_block_bottom) + 20.0;
    svg.legend(lx, legend_y, &rows);
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
    /// The action reaches a side effect: `server == Some(true)` (a `/_rpc`
    /// round-trip) OR its branch touches an effect family. `false` for a pure
    /// client-only UI update. Populated from the auto-split partition when it can
    /// run (any shape); defaults `false` when it cannot.
    pub effectful: bool,
    /// The effect families this action's branch reaches (`Db`, `Http`, `Auth`,
    /// …), sorted + deduped. Empty for a pure action, or when the partition could
    /// not run. Annotates the effectful chip (`AddToBasket · Db`).
    pub effect_families: Vec<String>,
}

/// The user journey for a project — pure data the renderer consumes.
pub struct JourneyReport {
    /// The project path, relative to the repo root when possible.
    pub project: String,
    /// True when the resolved target is a Sky.Spa wasm client — for the note.
    pub is_spa: bool,
    /// The derived app shape — drives the section labels and the Cli/Http cases.
    pub shape: AppShape,
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
    // Shape facts read from the resolved HIR (a `Std.App` UI, `Sky.Http.Server`
    // routes) — the two inputs the shape derivation needs beyond the target.
    let (_eps, has_app_ui, has_http_routes) = recover_endpoints(&db, &check_ids);
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
        if let Some(td) = resolved
            .top_defs
            .iter()
            .find(|t| t.name.as_str() == "update")
        {
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
        let mut v: Vec<(DefId, usize)> = union_ctors.iter().map(|(u, s)| (*u, s.len())).collect();
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
            effectful: false,
            effect_families: Vec::new(),
        });
    }
    actions.sort_by(|a, b| a.msg.cmp(&b.msg));
    actions.dedup_by(|a, b| a.msg == b.msg);

    // ---- 6. effect + client/server classification, reusing the auto-split. ----
    // The partition runs for ANY shape (it is a static analysis of `update`): it
    // gives per-branch `server` + effect families, which drive the Pure/Effectful
    // split. Only a Spa client carries the `/_rpc` client/server SPLIT, so
    // `action.server` (the `/_rpc` round-trip flag) is set only when `is_spa`.
    let shape = app_shape(app_target, has_app_ui, has_http_routes);
    let mut classified = false;
    if let Ok(report) = crate::spa_partition::analyze(repo_root, project_dir, entry_module) {
        let mut server_by_msg: HashMap<String, bool> = HashMap::new();
        let mut fams_by_msg: HashMap<String, Vec<String>> = HashMap::new();
        for b in &report.branches {
            let ctor = b
                .msg
                .split_whitespace()
                .next()
                .unwrap_or(&b.msg)
                .to_string();
            // A Msg with several arms is effectful if ANY arm is; union the families.
            let e = server_by_msg.entry(ctor.clone()).or_insert(false);
            *e = *e || b.server;
            let f = fams_by_msg.entry(ctor).or_default();
            f.extend(b.effect_families.iter().cloned());
        }
        for f in fams_by_msg.values_mut() {
            f.sort();
            f.dedup();
        }
        let effectful_of = |name: &str| -> bool {
            server_by_msg.get(name).copied().unwrap_or(false)
                || fams_by_msg.get(name).map(|f| !f.is_empty()).unwrap_or(false)
        };
        if actions.is_empty() {
            // Our own branch scan found nothing (a lambda / delegating `update`);
            // fall back to the auto-split's Msg inventory.
            let mut names: Vec<String> = server_by_msg.keys().cloned().collect();
            names.sort();
            for n in names {
                let server = if is_spa { server_by_msg.get(&n).copied() } else { None };
                actions.push(JourneyAction {
                    msg: n.clone(),
                    server,
                    navigates_to: Vec::new(),
                    dynamic_nav: false,
                    effectful: effectful_of(&n),
                    effect_families: fams_by_msg.get(&n).cloned().unwrap_or_default(),
                });
            }
        } else {
            for a in &mut actions {
                if is_spa {
                    if let Some(s) = server_by_msg.get(&a.msg) {
                        a.server = Some(*s);
                    }
                }
                a.effectful = effectful_of(&a.msg);
                a.effect_families = fams_by_msg.get(&a.msg).cloned().unwrap_or_default();
            }
        }
        classified = is_spa;
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
        let msg = match shape {
            AppShape::Tui | AppShape::Cli => {
                "This is a terminal app: every action runs in-process. Effectful actions reach \
                 a side effect; pure actions only update the model."
            }
            _ => {
                "This app is not built as a Sky.Spa wasm client, so there is no per-action \
                 client/server split: on Sky.Live every action round-trips to the server over \
                 the session's SSE channel."
            }
        };
        notes.push(msg.into());
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
        shape,
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

/// The chip label for an action in the Effectful/Pure inventory: the Msg name,
/// annotated with its effect families when known (`AddToBasket · Db, Http`).
fn action_chip_label(a: &JourneyAction) -> String {
    if a.effect_families.is_empty() {
        a.msg.clone()
    } else {
        format!("{} · {}", a.msg, a.effect_families.join(", "))
    }
}

/// The heading for the Effectful section, worded for the shape: a Spa client
/// round-trips over `/_rpc`; every other shape runs the effect server-side (Live)
/// or in-process (terminal).
fn effectful_section_label(shape: AppShape, n: usize) -> String {
    let how = match shape {
        AppShape::Spa => "server · /_rpc",
        AppShape::Tui | AppShape::Cli => "in-process",
        _ => "server-side",
    };
    format!("Effectful actions ({n}) — {how}, reach a side effect")
}

/// The heading for the Pure section, worded for the shape.
fn pure_section_label(shape: AppShape, n: usize) -> String {
    let how = match shape {
        AppShape::Spa => "client-only UI (wasm), no effect",
        _ => "model-only update, no effect",
    };
    format!("Pure actions ({n}) — {how}")
}

/// Draw the Effectful + Pure action inventory (two labelled chip sections) for
/// `actions`, from `(x0, section_y)`, wrapping before `content_right`. Returns
/// the bottom Y. Effectful chips carry their effect families; pure chips do not.
fn journey_inventory_svg(
    svg: &mut diagram_svg::Svg,
    shape: AppShape,
    actions: &[&JourneyAction],
    x0: f64,
    mut section_y: f64,
    content_right: f64,
) -> f64 {
    use diagram_svg as d;
    let effectful: Vec<&&JourneyAction> = actions.iter().filter(|a| a.effectful).collect();
    let pure: Vec<&&JourneyAction> = actions.iter().filter(|a| !a.effectful).collect();
    if !effectful.is_empty() {
        let items: Vec<(String, &'static str, &'static str)> = effectful
            .iter()
            .map(|a| (action_chip_label(a), d::SERVER_EDGE, d::SERVER_EDGE))
            .collect();
        svg.text(
            x0,
            section_y,
            &effectful_section_label(shape, effectful.len()),
            "start",
            12.0,
            "700",
            d::SUBTLE,
        );
        section_y = chip_grid(svg, &items, x0, section_y + 10.0, content_right) + 18.0;
    }
    if !pure.is_empty() {
        let items: Vec<(String, &'static str, &'static str)> = pure
            .iter()
            .map(|a| (a.msg.clone(), d::CLIENT_EDGE, d::CLIENT_EDGE))
            .collect();
        svg.text(
            x0,
            section_y,
            &pure_section_label(shape, pure.len()),
            "start",
            12.0,
            "700",
            d::SUBTLE,
        );
        section_y = chip_grid(svg, &items, x0, section_y + 10.0, content_right) + 16.0;
    }
    section_y
}

/// One collapsed transition: every Msg that shares the same `source → target`
/// seam, as `(msg, server)` pairs. Collapsing parallel edges is the fix for the
/// overlap disaster — five Msgs from Home to Login become ONE labelled edge.
struct JourneyEdge {
    msgs: Vec<(String, Option<bool>)>,
}

impl JourneyEdge {
    /// The edge colour: server-orange when every Msg round-trips, client-blue when
    /// every Msg is client-side, neutral when the Msgs mix (or are unclassified,
    /// e.g. a Sky.Live app where every action is an SSE round-trip).
    fn color(&self) -> &'static str {
        let mut any_server = false;
        let mut any_client = false;
        for (_, s) in &self.msgs {
            match s {
                Some(true) => any_server = true,
                Some(false) => any_client = true,
                None => {}
            }
        }
        match (any_server, any_client) {
            (true, false) => diagram_svg::SERVER_EDGE,
            (false, true) => diagram_svg::CLIENT_EDGE,
            _ => diagram_svg::STROKE,
        }
    }

    /// The label lines: one Msg per line, sorted, deduped.
    fn lines(&self) -> Vec<String> {
        let mut v: Vec<String> = self.msgs.iter().map(|(m, _)| m.clone()).collect();
        v.sort();
        v.dedup();
        v
    }
}

/// Collapse the action inventory into transition seams, all sourced at the
/// initial page (per-page attribution is not attempted — see the module note):
/// `(self-loop Msgs, per-target Msgs, dynamic-target Msgs)`. Every parallel edge
/// between the same two states is merged into one entry, so no two labels ever
/// stack on the same line.
fn journey_edges(
    r: &JourneyReport,
    init_name: &str,
) -> (
    JourneyEdge,
    std::collections::BTreeMap<String, JourneyEdge>,
    JourneyEdge,
) {
    let mut self_edge = JourneyEdge { msgs: Vec::new() };
    let mut by_target: std::collections::BTreeMap<String, JourneyEdge> =
        std::collections::BTreeMap::new();
    let mut dyn_edge = JourneyEdge { msgs: Vec::new() };
    for a in &r.actions {
        for t in &a.navigates_to {
            if t == init_name {
                self_edge.msgs.push((a.msg.clone(), a.server));
            } else {
                by_target
                    .entry(t.clone())
                    .or_insert_with(|| JourneyEdge { msgs: Vec::new() })
                    .msgs
                    .push((a.msg.clone(), a.server));
            }
        }
        if a.dynamic_nav {
            dyn_edge.msgs.push((a.msg.clone(), a.server));
        }
    }
    (self_edge, by_target, dyn_edge)
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

/// A PlantUML per-edge colour directive (`-[#rrggbb]->`) for a collapsed edge.
fn puml_arrow(color: &str) -> String {
    if color == diagram_svg::STROKE {
        "-->".to_string()
    } else {
        format!("-[{color}]->")
    }
}

/// The Effectful / Pure floating-note blocks for the given actions (shared by the
/// page-based and page-less puml journeys).
fn puml_action_notes(o: &mut String, shape: AppShape, actions: &[&JourneyAction]) {
    let effectful: Vec<&&JourneyAction> = actions.iter().filter(|a| a.effectful).collect();
    let pure: Vec<&&JourneyAction> = actions.iter().filter(|a| !a.effectful).collect();
    if !effectful.is_empty() {
        o.push_str("note as effectful_note\n");
        o.push_str(&format!("  <b>{}</b>\n", effectful_section_label(shape, effectful.len())));
        for a in &effectful {
            o.push_str(&format!("  {}\n", action_chip_label(a)));
        }
        o.push_str("end note\n");
    }
    if !pure.is_empty() {
        o.push_str("note as pure_note\n");
        o.push_str(&format!("  <b>{}</b>\n", pure_section_label(shape, pure.len())));
        for a in &pure {
            o.push_str(&format!("  {}\n", short_edge_label(&a.msg)));
        }
        o.push_str("end note\n");
    }
}

fn render_journey_puml(r: &JourneyReport) -> String {
    let mut o = puml_header(&format!("User journey (TEA state machine) — {}", r.project));
    // Http: no pages, no TEA loop.
    if r.shape == AppShape::Http {
        o.push_str("state \"HTTP API — no user journey\" as none\n");
        o.push_str("[*] --> none\n");
        o.push_str("note bottom of none : see `--diagram wire` for the endpoint map\n");
        o.push_str(&puml_footer());
        return o;
    }
    let Some(init) = initial_page(r) else {
        // No pages (a Cli / Tui TEA loop): no state machine — just the Effectful
        // and Pure action inventory. A truly empty app gets one clear state.
        if r.actions.is_empty() {
            o.push_str("state \"No pages found\" as none\n");
            o.push_str("[*] --> none\n");
        } else {
            o.push_str("state \"Actions (no pages — TEA loop)\" as loop\n");
            o.push_str("[*] --> loop\n");
            let all: Vec<&JourneyAction> = r.actions.iter().collect();
            puml_action_notes(&mut o, r.shape, &all);
        }
        o.push_str(&puml_footer());
        return o;
    };
    let init_name = r.pages[init].name.clone();
    let init_id = page_node_id(&init_name);
    let (self_edge, by_target, dyn_edge) = journey_edges(r, &init_name);

    // One state per page (URL folded into the label as a second line).
    for p in &r.pages {
        let label = match &p.url {
            Some(u) => format!("{}\\n{}", p.name, u),
            None => p.name.clone(),
        };
        o.push_str(&format!(
            "state \"{}\" as {}\n",
            label,
            page_node_id(&p.name)
        ));
    }
    let has_dyn = !dyn_edge.msgs.is_empty();
    if has_dyn {
        o.push_str("state \"(dynamic page)\\nchosen at run time\" as dyn_pg\n");
    }
    o.push_str(&format!("[*] --> {init_id}\n"));

    // Collapsed navigating transitions: one arrow per seam, its label the Msg
    // list (never parallel edges with stacked labels).
    for (target, edge) in &by_target {
        o.push_str(&format!(
            "{init_id} {} {} : {}\n",
            puml_arrow(edge.color()),
            page_node_id(target),
            edge.lines().join("\\n"),
        ));
    }
    if !self_edge.msgs.is_empty() {
        o.push_str(&format!(
            "{init_id} {} {init_id} : {}\n",
            puml_arrow(self_edge.color()),
            self_edge.lines().join("\\n"),
        ));
    }
    if has_dyn {
        o.push_str(&format!(
            "{init_id} {} dyn_pg : {}\n",
            puml_arrow(dyn_edge.color()),
            dyn_edge.lines().join("\\n"),
        ));
    }

    // Non-navigating actions: two floating notes — Effectful (chips annotated
    // with their effect families) and Pure — the same typed split the SVG draws.
    let internal = non_nav_actions(r);
    puml_action_notes(&mut o, r.shape, &internal);

    o.push_str("legend right\n");
    o.push_str("  <b>TEA state machine</b>\n");
    o.push_str("  states = pages · [*] = initial\n");
    o.push_str("  orange = server round-trip · blue = client\n");
    o.push_str("  parallel Msgs are collapsed onto one edge\n");
    o.push_str("endlegend\n");
    o.push_str(&puml_footer());
    o
}

/// Flow a list of `(text, border, text)` chips into a wrapped grid starting at
/// `(x0, y0)`, wrapping before `max_x`. Returns the bottom Y. Used for the
/// "Other pages" and "Internal events" inventory sections, which can be long.
fn chip_grid(
    svg: &mut diagram_svg::Svg,
    items: &[(String, &'static str, &'static str)],
    x0: f64,
    y0: f64,
    max_x: f64,
) -> f64 {
    let gap = 8.0;
    let row_gap = 8.0;
    let mut cx = x0;
    let mut cy = y0;
    for (text, border, tcolor) in items {
        let w = diagram_svg::text_width(text, 10.5) + 16.0;
        if cx > x0 && cx + w > max_x {
            cx = x0;
            cy += diagram_svg::Svg::CHIP_H + row_gap;
        }
        let drawn = svg.chip(cx, cy, text, border, tcolor);
        cx += drawn + gap;
    }
    cy + diagram_svg::Svg::CHIP_H
}

/// The flagship fix: a clean TEA STATE MACHINE. Pages are states ranked left →
/// right from the initial page; parallel Msgs between the same two states are
/// COLLAPSED onto one orthogonally-routed edge (no stacked labels); pages with
/// no attributed transition and non-navigating internal events go into tidy
/// inventory sections below, so nothing overlaps.
fn render_journey_svg(r: &JourneyReport) -> String {
    use diagram_svg as d;
    let mut svg = d::Svg::new(&format!("User journey (TEA state machine) — {}", r.project));

    // Http: no pages, no TEA loop — one clear line, never a broken diagram.
    if r.shape == AppShape::Http {
        svg.text(
            8.0,
            26.0,
            "An HTTP API has no user journey.",
            "start",
            13.0,
            "600",
            d::TEXT,
        );
        svg.text(
            8.0,
            48.0,
            "There are no pages or a TEA loop; see `--diagram wire` for the endpoint map.",
            "start",
            11.0,
            "400",
            d::SUBTLE,
        );
        return svg.render();
    }

    // A TEA app with no Page union (a Cli, or a Tui with no routing) — no state
    // machine, just the action inventory split into Effectful and Pure.
    if r.pages.is_empty() {
        if r.actions.is_empty() {
            svg.node(8.0, 8.0, 200.0, NODE_H, d::FILL, d::STROKE, "No pages found", None);
            return svg.render();
        }
        let all: Vec<&JourneyAction> = r.actions.iter().collect();
        let head = match r.shape {
            AppShape::Cli | AppShape::Tui => "Actions (no pages — terminal TEA loop)",
            _ => "Actions (no pages found)",
        };
        svg.text(8.0, 24.0, head, "start", 13.0, "700", d::TEXT);
        let y = journey_inventory_svg(&mut svg, r.shape, &all, 8.0, 46.0, 1100.0);
        let eff_label = if r.shape.is_terminal() {
            "effectful action (in-process)"
        } else {
            "effectful action (server-side)"
        };
        svg.legend(
            8.0,
            y + 6.0,
            &[
                (d::SERVER_EDGE.to_string(), eff_label.to_string()),
                (d::CLIENT_EDGE.to_string(), "pure action (no effect)".to_string()),
            ],
        );
        return svg.render();
    }

    let Some(init) = initial_page(r) else {
        svg.node(
            8.0,
            8.0,
            200.0,
            NODE_H,
            d::FILL,
            d::STROKE,
            "No pages found",
            None,
        );
        return svg.render();
    };
    let init_name = r.pages[init].name.clone();
    let (self_edge, by_target, dyn_edge) = journey_edges(r, &init_name);
    let has_dyn = !dyn_edge.msgs.is_empty();

    let page_w = 168.0;
    let col0_x = 46.0;
    let col_gap = 300.0;
    let col1_x = col0_x + page_w + col_gap;
    let top = 14.0;

    // Column-1 rows: every navigated target (sorted) then the dynamic state.
    // Row height reserves room for the (possibly multi-line) collapsed label.
    struct Row {
        key: String,
        is_dyn: bool,
        cy: f64,
    }
    let mut rows: Vec<Row> = Vec::new();
    let mut y = top;
    let push_row =
        |rows: &mut Vec<Row>, y: &mut f64, key: String, is_dyn: bool, label_lines: usize| {
            let h = NODE_H.max(d::Svg::plate_height(label_lines));
            rows.push(Row {
                key,
                is_dyn,
                cy: *y + h / 2.0,
            });
            *y += h + 26.0;
        };
    for (t, edge) in &by_target {
        push_row(&mut rows, &mut y, t.clone(), false, edge.lines().len());
    }
    if has_dyn {
        push_row(
            &mut rows,
            &mut y,
            "__dyn__".into(),
            true,
            dyn_edge.lines().len(),
        );
    }
    let col1_bottom = if rows.is_empty() {
        top + NODE_H
    } else {
        y - 26.0
    };

    // Initial page: vertically centred against the column-1 block.
    let init_cy = ((top + col1_bottom) / 2.0).max(top + NODE_H / 2.0);
    let init_y = init_cy - NODE_H / 2.0;
    let init_url = r.pages[init].url.clone();

    // Entry marker + arrow into the initial page.
    svg.rect(
        col0_x - 30.0,
        init_cy - 5.0,
        10.0,
        10.0,
        5.0,
        d::TEXT,
        d::TEXT,
        1.0,
    );
    svg.ortho(col0_x - 18.0, init_cy, col0_x, init_cy, d::STROKE, None);
    // The initial state box.
    svg.node(
        col0_x,
        init_y,
        page_w,
        NODE_H,
        d::FILL,
        d::STROKE,
        &init_name,
        init_url.as_deref(),
    );

    // Column-1 state boxes.
    let mut pos: std::collections::HashMap<String, (f64, f64)> = std::collections::HashMap::new();
    for row in &rows {
        let ry = row.cy - NODE_H / 2.0;
        if row.is_dyn {
            svg.node(
                col1_x,
                ry,
                page_w,
                NODE_H,
                d::FILL_ALT,
                d::PKG_STROKE,
                "(dynamic page)",
                Some("run-time chosen"),
            );
        } else {
            let url = r
                .pages
                .iter()
                .find(|p| p.name == row.key)
                .and_then(|p| p.url.clone());
            svg.node(
                col1_x,
                ry,
                page_w,
                NODE_H,
                d::FILL,
                d::STROKE,
                &row.key,
                url.as_deref(),
            );
        }
        pos.insert(row.key.clone(), (col1_x, row.cy));
    }

    // Collapsed navigating edges, orthogonally routed with staggered bends so no
    // two trunks smear together, each labelled by its full Msg list on one plate.
    let init_right = col0_x + page_w;
    let n_rows = rows.len().max(1) as f64;
    for (i, row) in rows.iter().enumerate() {
        let edge = if row.is_dyn {
            &dyn_edge
        } else {
            by_target.get(&row.key).unwrap()
        };
        let color = edge.color();
        let lines = edge.lines();
        // Source Y is staggered across the LOWER part of the init box edge, so no
        // target edge crosses the self-loop that sits at the box top. The bend X
        // is staggered across the gap so trunks never smear together.
        let src_y = init_y + NODE_H * (0.42 + 0.5 * (i as f64 + 1.0) / (n_rows + 1.0));
        let mid_x = init_right + col_gap * (0.28 + 0.38 * (i as f64 + 1.0) / (n_rows + 1.0));
        svg.ortho_via(init_right, src_y, col1_x, row.cy, mid_x, true, color);
        // The label sits HARD RIGHT, pinned to the target box, so it never
        // reaches the source area where the self-loop lives.
        let plate_w = lines
            .iter()
            .map(|l| d::text_width(l, 10.5))
            .fold(0.0, f64::max)
            + 12.0;
        svg.plate_lines(col1_x - 16.0 - plate_w / 2.0, row.cy, &lines, color);
    }
    // Self-loop (collapsed) on the initial state — arc + label near the source,
    // at the box top, clear of the target edges that exit the lower half.
    if !self_edge.msgs.is_empty() {
        let (lx, ly) = svg.loop_arc(init_right, init_y + 12.0, self_edge.color());
        let lines = self_edge.lines();
        let w = lines
            .iter()
            .map(|l| d::text_width(l, 10.5))
            .fold(0.0, f64::max)
            + 12.0;
        svg.plate_lines(lx + w / 2.0, ly, &lines, self_edge.color());
    }

    let content_right = col1_x + page_w + 40.0;
    let mut section_y = col1_bottom.max(init_y + NODE_H) + 34.0;

    // "Other pages" inventory: pages with no attributed transition.
    let linked: std::collections::HashSet<String> = rows
        .iter()
        .filter(|r| !r.is_dyn)
        .map(|r| r.key.clone())
        .chain(std::iter::once(init_name.clone()))
        .collect();
    let other: Vec<(String, &'static str, &'static str)> = r
        .pages
        .iter()
        .filter(|p| !linked.contains(&p.name))
        .map(|p| {
            let label = match &p.url {
                Some(u) => format!("{} · {}", p.name, u),
                None => p.name.clone(),
            };
            (label, d::PKG_STROKE, d::TEXT)
        })
        .collect();
    if !other.is_empty() {
        svg.text(
            col0_x,
            section_y,
            &format!("Other pages ({}) — no attributed transition", other.len()),
            "start",
            12.0,
            "700",
            d::SUBTLE,
        );
        section_y = chip_grid(&mut svg, &other, col0_x, section_y + 10.0, content_right) + 20.0;
    }

    // Non-navigating actions: split into Effectful (reach a side effect) and Pure
    // (model-only) sections, each chip annotated with its effect families.
    let internal = non_nav_actions(r);
    if !internal.is_empty() {
        section_y = journey_inventory_svg(&mut svg, r.shape, &internal, col0_x, section_y, content_right);
    }

    // Legend.
    let mut lrows: Vec<(String, String)> = Vec::new();
    if r.classified {
        lrows.push((d::SERVER_EDGE.into(), "navigation → server (/_rpc)".into()));
        lrows.push((d::CLIENT_EDGE.into(), "navigation (client / wasm)".into()));
    } else {
        lrows.push((d::STROKE.into(), "navigation (SSE round-trip)".into()));
    }
    let eff_label = match r.shape {
        AppShape::Spa => "effectful action (server · /_rpc)",
        AppShape::Tui | AppShape::Cli => "effectful action (in-process)",
        _ => "effectful action (server-side)",
    };
    lrows.push((d::SERVER_EDGE.into(), eff_label.into()));
    lrows.push((d::CLIENT_EDGE.into(), "pure action (no effect)".into()));
    lrows.push((d::PKG_STROKE.into(), "run-time / other page".into()));
    svg.legend(col0_x, section_y + 6.0, &lrows);

    svg.render()
}

fn render_journey_md(r: &JourneyReport) -> String {
    let mut o = String::new();
    o.push_str(&format!("# User journey — {}\n\n", r.project));
    let shape_line = match r.shape {
        AppShape::Spa => "Sky.Spa (wasm client + server over /_rpc)",
        AppShape::Live => "Sky.Live (one server, SSR + SSE)",
        AppShape::Tui => "Sky.Tui (single terminal binary)",
        AppShape::Cli => "Sky.Cli (single terminal binary)",
        AppShape::Http => "Sky.Http.Server (HTTP API, no user journey)",
    };
    o.push_str(&format!("App shape: {shape_line}\n\n"));
    if r.pages.is_empty() && r.actions.is_empty() {
        for n in &r.notes {
            o.push_str(&format!("> {n}\n"));
        }
        return o;
    }
    if !r.pages.is_empty() {
        o.push_str("## Pages\n\n");
        o.push_str("| Page | URL |\n|---|---|\n");
        for p in &r.pages {
            let url = p.url.as_deref().map(md_cell).unwrap_or_else(|| "—".into());
            o.push_str(&format!("| {} | {} |\n", md_cell(&p.name), url));
        }
        o.push('\n');
    }
    o.push_str("## Actions\n\n");
    o.push_str(
        "Each user action (Msg): whether it is Effectful (reaches a side effect) or Pure, \
         its effect families, and the page(s) it navigates to.\n\n",
    );
    o.push_str("| Action | Kind | Effects | Navigates to |\n|---|---|---|---|\n");
    for a in &r.actions {
        let kind = if a.effectful {
            match r.shape {
                AppShape::Spa => "effectful (server · /_rpc)".to_string(),
                AppShape::Tui | AppShape::Cli => "effectful (in-process)".to_string(),
                _ => "effectful (server-side)".to_string(),
            }
        } else {
            "pure".to_string()
        };
        let effects = if a.effect_families.is_empty() {
            "—".to_string()
        } else {
            a.effect_families.join(", ")
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
            "| {} | {} | {} | {} |\n",
            md_cell(&a.msg),
            kind,
            md_cell(&effects),
            md_cell(&nav_s)
        ));
    }
    o.push('\n');
    for n in &r.notes {
        o.push_str(&format!("> {n}\n"));
    }
    o
}

// ============================================================================
// flow — the behaviour graph (slice 1). A grounded interaction graph: the
// journey's pages + actions, PLUS which Msgs each page's VIEW can dispatch (so
// an action is an edge OUT of the page the user is on, not the whole inventory),
// PLUS the async CONTINUATION each effectful action's command dispatches. This
// replaces `journey`. Compliance overlays (Secret classification, named external
// systems) are slice 2.
// ============================================================================

/// A data-classification verdict for one action — the compliance overlay. An
/// auditor needs the REASON, not just the flag, so `reasons` names each cause
/// (`Secret arg \`apiKey\``, `Auth session`, `PII field \`email\``).
#[derive(Clone, Debug, Default)]
pub struct Classification {
    pub confidential: bool,
    pub reasons: Vec<String>,
}

/// The role an external system plays for the app — decides whether it is a data
/// SUB-PROCESSOR (a third party the app sends/receives application data to/from,
/// an audit concern) or merely EMBEDDED third-party content (a CDN / video embed,
/// no app-data flow). Conservative: an unknown host is `Api`, kept as a
/// sub-processor (safer for an audit than silently dropping it).
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum ExtRole {
    Payments,
    OAuth,
    Email,
    Llm,
    Messaging,
    Api,
    Cdn,
    Dynamic,
}

impl ExtRole {
    /// A data sub-processor (ISO A.15): the app flows application data to/from it.
    /// `Cdn` (embedded content / static assets) is NOT — it is third-party content.
    pub fn is_subprocessor(self) -> bool {
        !matches!(self, ExtRole::Cdn)
    }
    pub fn label(self) -> &'static str {
        match self {
            ExtRole::Payments => "Payments",
            ExtRole::OAuth => "OAuth / IdP",
            ExtRole::Email => "Email",
            ExtRole::Llm => "LLM",
            ExtRole::Messaging => "Messaging",
            ExtRole::Api => "API",
            ExtRole::Cdn => "CDN / embed",
            ExtRole::Dynamic => "Dynamic egress",
        }
    }
}

/// One named external system the app calls out to. `host` is the literal host from
/// an `Http` call URL (`api.stripe.com`); `purpose` is a short human label
/// (`Payments (Stripe)`); `role` decides sub-processor vs embedded content.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct ExternalSystem {
    pub host: String,
    pub purpose: String,
    pub role: ExtRole,
}

/// The behaviour graph for a project, with the compliance overlays. Wraps the
/// [`JourneyReport`] with the grounding edges (`flow`) plus the trust-boundary,
/// data-classification, and named-external-system overlays (audit grade).
pub struct FlowReport {
    /// The base journey (pages, actions, nav targets, effect classification).
    pub journey: JourneyReport,
    /// Page constructor name → the Msg constructor names its view can dispatch.
    /// Empty for a page whose view could not be attributed; see `grounded`.
    pub page_actions: HashMap<String, Vec<String>>,
    /// Msg → the follow-up Msg(s) its `Cmd.perform` result dispatches — the async
    /// continuation edges (`LoadPosts ⇢ GotPosts`). From the Sky.Spa auto-split.
    pub continuations: HashMap<String, Vec<String>>,
    /// True when at least one page's view was attributed (per-page grounding is
    /// real). False → every action is shown on every page (documented fallback).
    pub grounded: bool,
    /// Msg → its data classification (confidential + why). The overlay an auditor
    /// scores: which flows carry a `Secret`, a `Std.Auth` session, or PII.
    pub classifications: HashMap<String, Classification>,
    /// Msg → the named external hosts its branch reaches (`AddToBasket` →
    /// `api.stripe.com`). Drives the per-edge host label + the SVG external lane.
    pub action_externals: HashMap<String, Vec<String>>,
    /// Every named external system the app calls (deduped, sorted), with its role.
    /// Split at render into sub-processors (ISO A.15) and embedded content by
    /// [`ExtRole::is_subprocessor`]. The SVG external lane draws these.
    pub external_systems: Vec<ExternalSystem>,
    /// Hosts recognised as the app's OWN domain (from `CNAME` / `sky.toml`) and so
    /// excluded from the sub-processor list — recorded here so the exclusion is
    /// auditable ("filtered as self-referential: sky-lang.org").
    pub self_ref_hosts: Vec<String>,
    /// The app's data store, when it uses one (`PostgreSQL`, `SQLite`, or the
    /// generic `App database`). `None` when the app touches no `Db` effect.
    pub data_store: Option<String>,
    /// The global-chrome actions: Msgs dispatchable from EVERY page (shared nav /
    /// layout). Rendered once, not under every page. Empty when not grounded or
    /// there are < 2 pages.
    pub chrome_actions: Vec<String>,
}

/// The Msg union DefId — the union `update`'s dispatch `case` matches on. Its
/// constructors are the app's user actions; a value of one of them in a view is
/// a handler the user can trigger.
fn msg_union_of_update(db: &dyn SkyDb, check_ids: &[ModuleId]) -> Option<DefId> {
    for mid in check_ids {
        let resolved = db.resolve(*mid);
        let Some(td) = resolved.top_defs.iter().find(|t| t.name.as_str() == "update") else {
            continue;
        };
        let Some(body) = resolved.bodies.get(&td.def) else { continue };
        let Some(case_e) = find_dispatch_case(body) else { continue };
        let Expr::Case { branches, .. } = &body.exprs[case_e] else { continue };
        for br in branches {
            if let Pattern::Ctor { ctor: Some(c), .. } = &body.pats[br.pat] {
                return Some(c.type_);
            }
        }
    }
    None
}

/// Collect every Msg-union constructor name in a single expression `e` of `body`,
/// and the project defs the expression references (returned for a transitive
/// follow). Used per body by [`view_msgs_in`].
fn collect_msgs_and_refs(
    db: &dyn SkyDb,
    body: &Body,
    e: ExprId,
    msg_union: DefId,
    project_modules: &HashSet<ModuleId>,
    out: &mut BTreeSet<String>,
    refs: &mut Vec<DefId>,
) {
    let mut ids: Vec<ExprId> = Vec::new();
    walk_exprs(body, e, &mut |x| ids.push(x));
    for x in &ids {
        if let Some((cn, u)) = value_ctor(db, body, *x) {
            if u == msg_union {
                out.insert(cn);
            }
        }
        if let Expr::Var(Res::Def(d)) = &body.exprs[*x] {
            if db.def_loc(*d).map(|l| project_modules.contains(&l.module)).unwrap_or(false) {
                refs.push(*d);
            }
        }
    }
}

/// Collect every Msg-union constructor reachable from `e` in `body`, TRANSITIVELY
/// through the project defs it calls — so a page arm that calls `viewHome model`,
/// which calls `postForm`, which builds a `Ui.button [ onPress = Just Publish ]`,
/// still attributes `Publish` to that page. Bounded by a visited set (a def is
/// walked once) and a hard node cap, so a cyclic or huge view cannot loop or blow
/// up. Real views nest several helper calls deep, hence the transitive walk.
fn view_msgs_in(
    db: &dyn SkyDb,
    body: &Body,
    e: ExprId,
    msg_union: DefId,
    project_modules: &HashSet<ModuleId>,
    out: &mut BTreeSet<String>,
    _follow_defs: bool,
) {
    const CAP: usize = 4000;
    let mut queue: Vec<DefId> = Vec::new();
    // Msgs + project-def refs directly in the arm expression.
    let Some(root) = body.root else { return };
    let _ = root;
    collect_msgs_and_refs(db, body, e, msg_union, project_modules, out, &mut queue);
    let mut seen: HashSet<DefId> = HashSet::new();
    while let Some(d) = queue.pop() {
        if !seen.insert(d) {
            continue;
        }
        if seen.len() > CAP {
            break;
        }
        let Some(loc) = db.def_loc(d) else { continue };
        let resolved = db.resolve(loc.module);
        let Some(dbody) = resolved.bodies.get(&d) else { continue };
        let Some(droot) = dbody.root else { continue };
        collect_msgs_and_refs(db, dbody, droot, msg_union, project_modules, out, &mut queue);
    }
}

/// Attribute Msgs to the page whose view dispatches them. Finds the top `view`
/// def; if it dispatches on the page field (`case model.page of Home -> …`),
/// each arm's page constructor gets the Msgs its arm body (and the view helpers
/// it calls) can dispatch. Returns `(page → msgs, grounded)`; `grounded` is false
/// when no page-dispatching view was found (then the caller falls back to the
/// whole inventory on every page).
fn analyze_view_msgs(
    db: &dyn SkyDb,
    check_ids: &[ModuleId],
    msg_union: Option<DefId>,
    page_names: &HashSet<String>,
) -> (HashMap<String, Vec<String>>, bool) {
    let out: HashMap<String, Vec<String>> = HashMap::new();
    let Some(msg_union) = msg_union else { return (out, false) };
    if page_names.is_empty() {
        return (out, false);
    }
    let project_modules: HashSet<ModuleId> = check_ids.iter().copied().collect();
    // Find the page-dispatch `case`: the one whose arm constructors best match the
    // page-name set, ANYWHERE in the project (it is usually in a `pageBody` /
    // `viewPage` helper, not `view` itself). Require >= 2 matching arms so a
    // coincidental one-arm match is not mistaken for the router.
    let mut best: Option<(usize, ModuleId, DefId, ExprId)> = None;
    for mid in check_ids {
        let resolved = db.resolve(*mid);
        for (d, body) in &resolved.bodies {
            let Some(root) = body.root else { continue };
            let mut cases: Vec<ExprId> = Vec::new();
            walk_exprs(body, root, &mut |e| {
                if matches!(body.exprs[e], Expr::Case { .. }) {
                    cases.push(e);
                }
            });
            for ce in cases {
                let Expr::Case { branches, .. } = &body.exprs[ce] else { continue };
                let n = branches
                    .iter()
                    .filter(|br| {
                        pattern_ctor_name(body, br.pat)
                            .map(|nm| page_names.contains(&nm))
                            .unwrap_or(false)
                    })
                    .count();
                if n >= 2 && best.map(|(bn, ..)| n > bn).unwrap_or(true) {
                    best = Some((n, *mid, *d, ce));
                }
            }
        }
    }
    let Some((_, mid, def, case_e)) = best else { return (out, false) };
    // Re-resolve the winning module + walk each page arm for the Msgs its view
    // (and the view helpers it calls) can dispatch.
    let resolved = db.resolve(mid);
    let Some(body) = resolved.bodies.get(&def) else { return (out, false) };
    let Expr::Case { branches, .. } = &body.exprs[case_e] else { return (out, false) };
    let mut out: HashMap<String, Vec<String>> = HashMap::new();
    let mut grounded = false;
    for br in branches {
        let Some(page) = pattern_ctor_name(body, br.pat) else { continue };
        if !page_names.contains(&page) {
            continue;
        }
        let mut msgs: BTreeSet<String> = BTreeSet::new();
        view_msgs_in(db, body, br.body, msg_union, &project_modules, &mut msgs, true);
        if !msgs.is_empty() {
            grounded = true;
        }
        out.entry(page).or_default().extend(msgs);
    }
    if grounded {
        for v in out.values_mut() {
            v.sort();
            v.dedup();
        }
        return (out, true);
    }
    (HashMap::new(), false)
}

/// Conservative PII name heuristic: does a field / arg name look like personal
/// data? Case-insensitive substring match against a fixed list. Used for the
/// data-classification overlay (an auditor wants PII flows flagged). A false
/// positive is safe (over-marking is fine for compliance); the reason names the
/// field so a reviewer can confirm.
fn pii_reason(name: &str) -> Option<&'static str> {
    let n = name.to_ascii_lowercase();
    const PII: &[&str] = &[
        "email", "firstname", "lastname", "fullname", "surname", "address",
        "phone", "mobile", "postcode", "zipcode", "card", "cardnumber", "cvv",
        "iban", "sortcode", "ssn", "passport", "dob", "dateofbirth", "password",
        "secret", "token", "apikey", "creditcard",
    ];
    // `name`/`addr` are matched as whole-ish words to avoid `filename`/`address`
    // double-count noise; the list above already covers the compound forms.
    if PII.iter().any(|p| n.contains(p)) {
        return Some("PII");
    }
    if n == "name" || n == "addr" {
        return Some("PII");
    }
    None
}

/// Is a rendered type name the opaque `Secret`? `render_ty_name` tail-normalises
/// a folded nominal, so a `Sky.Core.Secret.Secret` field surfaces as `Secret`.
fn is_secret_ty(ty_name: &str) -> bool {
    ty_name.rsplit('.').next() == Some("Secret") || ty_name == "Secret"
}

/// The host of a URL literal (`https://api.stripe.com/v1/...` → `api.stripe.com`).
/// Best-effort: strips the scheme, takes up to the first `/`, `?`, or `:`. Returns
/// `None` for a non-URL string (so a stray literal is not mistaken for a host).
fn url_host(s: &str) -> Option<String> {
    let rest = s.strip_prefix("https://").or_else(|| s.strip_prefix("http://"))?;
    let host: String = rest
        .chars()
        .take_while(|&c| c != '/' && c != '?' && c != ':' && c != ' ')
        .collect();
    if host.is_empty() || !host.contains('.') {
        return None;
    }
    Some(host)
}

/// A short human purpose for a known host, for the sub-processor list.
fn host_purpose(host: &str) -> String {
    let h = host.to_ascii_lowercase();
    let known: &[(&str, &str)] = &[
        ("stripe.com", "Payments (Stripe)"),
        ("github.com", "OAuth / API (GitHub)"),
        ("githubusercontent.com", "GitHub assets"),
        ("google.com", "OAuth / API (Google)"),
        ("googleapis.com", "Google APIs"),
        ("sendgrid", "Email (SendGrid)"),
        ("mailgun", "Email (Mailgun)"),
        ("postmark", "Email (Postmark)"),
        ("resend.com", "Email (Resend)"),
        ("openai.com", "LLM (OpenAI)"),
        ("anthropic.com", "LLM (Anthropic)"),
        ("slack.com", "Messaging (Slack)"),
        ("twilio.com", "SMS (Twilio)"),
        ("cloudflare.com", "CDN / edge (Cloudflare)"),
        ("amazonaws.com", "AWS"),
    ];
    for (needle, label) in known {
        if h.contains(needle) {
            return (*label).to_string();
        }
    }
    format!("External service ({host})")
}

/// The role of a known host — decides sub-processor vs embedded content. A host
/// not recognised as a CDN / content host is `Api` (kept as a sub-processor).
fn host_role(host: &str) -> ExtRole {
    let h = host.to_ascii_lowercase();
    // Content / CDN / embed hosts — third-party CONTENT, not a data sub-processor.
    const CDN: &[&str] = &[
        "youtube.com", "youtu.be", "ytimg.com", "vimeo.com",
        "fonts.googleapis.com", "fonts.gstatic.com", "gstatic.com",
        "githubusercontent.com", "github.io", "gravatar.com",
        "jsdelivr.net", "unpkg.com", "cdnjs.cloudflare.com", "jquery.com",
    ];
    if CDN.iter().any(|n| h.contains(n)) {
        return ExtRole::Cdn;
    }
    const PAYMENTS: &[&str] = &["stripe.com", "paypal.com", "braintree", "adyen.com"];
    if PAYMENTS.iter().any(|n| h.contains(n)) {
        return ExtRole::Payments;
    }
    // OAuth / IdP — github/google are ALSO general APIs, but in a Sky app they are
    // reached through Std.Auth OAuth, so IdP is the audit-relevant role.
    const OAUTH: &[&str] = &["accounts.google.com", "github.com", "auth0.com", "okta.com", "login.microsoftonline.com"];
    if OAUTH.iter().any(|n| h.contains(n)) {
        return ExtRole::OAuth;
    }
    const EMAIL: &[&str] = &["sendgrid", "mailgun", "postmark", "resend.com", "smtp", "mailchimp", "mandrill", "ses.amazonaws"];
    if EMAIL.iter().any(|n| h.contains(n)) {
        return ExtRole::Email;
    }
    const LLM: &[&str] = &["openai.com", "anthropic.com", "cohere", "mistral.ai"];
    if LLM.iter().any(|n| h.contains(n)) {
        return ExtRole::Llm;
    }
    const MSG: &[&str] = &["slack.com", "twilio.com", "discord.com", "telegram.org"];
    if MSG.iter().any(|n| h.contains(n)) {
        return ExtRole::Messaging;
    }
    ExtRole::Api
}

/// The app's OWN domain(s), so calls to itself are not listed as sub-processors.
/// Sources: a `CNAME` file at the project root (GitHub-Pages / static-host
/// convention) and any `host`/`domain` value in `sky.toml`. Lower-cased; each
/// entry matches itself and its subdomains. Conservative: only these explicit
/// declarations count, never a guess.
fn own_domains(project_dir: &Path) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    if let Ok(c) = std::fs::read_to_string(project_dir.join("CNAME")) {
        for line in c.lines() {
            let d = line.trim().trim_end_matches('.').to_ascii_lowercase();
            if !d.is_empty() && d.contains('.') {
                out.push(d);
            }
        }
    }
    if let Ok(toml) = std::fs::read_to_string(project_dir.join("sky.toml")) {
        for line in toml.lines() {
            let t = line.trim();
            for key in ["domain", "host", "hostname"] {
                if let Some(v) = t.strip_prefix(key) {
                    let v = v.trim().trim_start_matches('=').trim().trim_matches('"').to_ascii_lowercase();
                    if v.contains('.') && !v.contains('/') {
                        out.push(v);
                    }
                }
            }
        }
    }
    out.sort();
    out.dedup();
    out
}

/// True when `host` is (or is a subdomain of) one of the app's own domains.
fn is_self_ref(host: &str, own: &[String]) -> bool {
    let h = host.to_ascii_lowercase();
    own.iter().any(|d| h == *d || h.ends_with(&format!(".{d}")))
}

/// Walk a def body (transitively through project callees) collecting the literal
/// hosts of every `Sky.Core.Http` call it reaches. Bounded like [`view_msgs_in`].
/// A non-literal URL target (`Http.get someVar`) contributes the marker host
/// `*dynamic*` so a dynamic egress is never silently dropped.
fn http_hosts_in(
    db: &dyn SkyDb,
    body: &Body,
    e: ExprId,
    project_modules: &HashSet<ModuleId>,
    out: &mut BTreeSet<String>,
) {
    const CAP: usize = 4000;
    let mut queue: Vec<DefId> = Vec::new();
    let mut seen: HashSet<DefId> = HashSet::new();
    let mut visit = |db: &dyn SkyDb, body: &Body, e: ExprId, out: &mut BTreeSet<String>, queue: &mut Vec<DefId>| {
        let mut ids: Vec<ExprId> = Vec::new();
        walk_exprs(body, e, &mut |x| ids.push(x));
        for x in &ids {
            if let Expr::Call(callee, args) = &body.exprs[*x] {
                if let Expr::Var(Res::Def(d)) = &body.exprs[*callee] {
                    let is_http = db
                        .def_loc(*d)
                        .map(|l| db.module_name(l.module) == "Sky.Core.Http")
                        .unwrap_or(false);
                    if is_http {
                        let mut got = false;
                        for a in args {
                            if let Expr::Str(s) = &body.exprs[*a] {
                                if let Some(h) = url_host(s) {
                                    out.insert(h);
                                    got = true;
                                }
                            }
                        }
                        if !got {
                            // an Http call whose URL is not a literal here
                            out.insert("*dynamic*".to_string());
                        }
                    }
                }
            }
            if let Expr::Var(Res::Def(d)) = &body.exprs[*x] {
                if db.def_loc(*d).map(|l| project_modules.contains(&l.module)).unwrap_or(false) {
                    queue.push(*d);
                }
            }
        }
    };
    visit(db, body, e, out, &mut queue);
    while let Some(d) = queue.pop() {
        if !seen.insert(d) || seen.len() > CAP {
            if seen.len() > CAP { break; }
            continue;
        }
        let Some(loc) = db.def_loc(d) else { continue };
        let resolved = db.resolve(loc.module);
        let Some(dbody) = resolved.bodies.get(&d) else { continue };
        let Some(droot) = dbody.root else { continue };
        visit(db, dbody, droot, out, &mut queue);
    }
}

/// Read the `[database] driver` from the project's `sky.toml`, mapping it to a
/// display name for the data-store node. `None` when unset.
fn data_store_name(project_dir: &Path) -> Option<String> {
    let toml = std::fs::read_to_string(project_dir.join("sky.toml")).ok()?;
    let mut in_db = false;
    for line in toml.lines() {
        let t = line.trim();
        if t.starts_with('[') {
            in_db = t.starts_with("[database]");
            continue;
        }
        if in_db {
            if let Some(v) = t.strip_prefix("driver") {
                let v = v.trim().trim_start_matches('=').trim().trim_matches('"').to_ascii_lowercase();
                return match v.as_str() {
                    "postgres" | "postgresql" | "pg" => Some("PostgreSQL".to_string()),
                    "sqlite" | "sqlite3" => Some("SQLite".to_string()),
                    other if !other.is_empty() => Some(other.to_string()),
                    _ => None,
                };
            }
        }
    }
    None
}

/// Analyse a project's behaviour graph. Reuses [`analyze_journey`] for the base,
/// then adds view→Msg grounding and command-continuation edges.
pub fn analyze_flow(
    repo_root: &Path,
    project_dir: &Path,
    entry_module: Option<&str>,
    app_target: Option<&str>,
) -> Result<FlowReport, String> {
    let journey = analyze_journey(repo_root, project_dir, entry_module, app_target)?;
    // View→Msg grounding (its own resolve pass; a diagram is not a hot path).
    let (db, _entry, check_ids) =
        crate::build::load_source_db(repo_root, project_dir, entry_module)?;
    let msg_union = msg_union_of_update(&db, &check_ids);
    let page_names: HashSet<String> = journey.pages.iter().map(|p| p.name.clone()).collect();
    let (page_actions, grounded) = analyze_view_msgs(&db, &check_ids, msg_union, &page_names);
    // Continuation edges: for each `update` arm, the follow-up Msg(s) its returned
    // `Cmd.perform task ToMsg` dispatches (`LoadPosts ⇢ GotPosts`). The general
    // extractor reports every resolvable ToMsg, not the narrow auto-split subset.
    let mut continuations: HashMap<String, Vec<String>> = HashMap::new();
    for mid in &check_ids {
        let resolved = db.resolve(*mid);
        let Some(td) = resolved.top_defs.iter().find(|t| t.name.as_str() == "update") else {
            continue;
        };
        let Some(body) = resolved.bodies.get(&td.def) else { continue };
        let Some(case_e) = find_dispatch_case(body) else { break };
        if let Expr::Case { branches, .. } = &body.exprs[case_e] {
            for br in branches {
                let Some(msg) = pattern_ctor_name(body, br.pat) else { continue };
                let conts = crate::spa_partition::arm_continuation_msgs(&db, body, br.body);
                if !conts.is_empty() {
                    let e = continuations.entry(msg).or_default();
                    e.extend(conts);
                    e.sort();
                    e.dedup();
                }
            }
        }
        break;
    }
    // ---- compliance overlays -------------------------------------------------
    let project_modules: HashSet<ModuleId> = check_ids.iter().copied().collect();
    // Named external systems, per action (transitive HTTP hosts) + the app-wide
    // deduped set (the sub-processor list).
    let mut action_externals: HashMap<String, Vec<String>> = HashMap::new();
    let mut all_hosts: BTreeSet<String> = BTreeSet::new();
    for mid in &check_ids {
        let resolved = db.resolve(*mid);
        let Some(td) = resolved.top_defs.iter().find(|t| t.name.as_str() == "update") else {
            continue;
        };
        let Some(body) = resolved.bodies.get(&td.def) else { continue };
        let Some(case_e) = find_dispatch_case(body) else { break };
        if let Expr::Case { branches, .. } = &body.exprs[case_e] {
            for br in branches {
                let Some(msg) = pattern_ctor_name(body, br.pat) else { continue };
                let mut hosts: BTreeSet<String> = BTreeSet::new();
                http_hosts_in(&db, body, br.body, &project_modules, &mut hosts);
                if !hosts.is_empty() {
                    all_hosts.extend(hosts.iter().cloned());
                    action_externals.insert(msg, hosts.into_iter().collect());
                }
            }
        }
        break;
    }
    // Supplement: a host is often a config CONSTANT (`stripeBase =
    // "https://api.stripe.com"`), not the literal at the `Http.get` call — so an
    // Http call site sees only a variable and we recorded `*dynamic*`. Scan every
    // project def body for URL string literals and add their hosts, so the
    // sub-processor list is complete. (App-wide only; not attributed per action.)
    for mid in &check_ids {
        let resolved = db.resolve(*mid);
        for td in &resolved.top_defs {
            let Some(body) = resolved.bodies.get(&td.def) else { continue };
            let Some(root) = body.root else { continue };
            let mut ids: Vec<ExprId> = Vec::new();
            walk_exprs(body, root, &mut |x| ids.push(x));
            for x in &ids {
                if let Expr::Str(s) = &body.exprs[*x] {
                    if let Some(h) = url_host(s) {
                        all_hosts.insert(h);
                    }
                }
            }
        }
    }
    // Own-domain hosts (the app calling itself) are not sub-processors — filter
    // them, but record what was filtered so the exclusion is auditable.
    let own = own_domains(project_dir);
    let mut self_ref_hosts: Vec<String> = Vec::new();
    let external_systems: Vec<ExternalSystem> = all_hosts
        .into_iter()
        .filter_map(|h| {
            if h == "*dynamic*" {
                return Some(ExternalSystem {
                    host: "(dynamic endpoint)".into(),
                    purpose: "Runtime-chosen HTTP target".into(),
                    role: ExtRole::Dynamic,
                });
            }
            if is_self_ref(&h, &own) {
                self_ref_hosts.push(h);
                return None;
            }
            let role = host_role(&h);
            Some(ExternalSystem { purpose: host_purpose(&h), host: h, role })
        })
        .collect();
    self_ref_hosts.sort();
    self_ref_hosts.dedup();
    drop(db);

    // Data classification, per action, from the auto-split branch analysis: the
    // Model's Secret/PII fields, the action's Secret/PII msg args, and the Auth
    // effect family. Fail-open (no report → no classification, never a crash).
    let mut classifications: HashMap<String, Classification> = HashMap::new();
    // Auth-family actions are confidential (they carry a session / token) — seed
    // from the journey actions, whose effect families are what the report shows.
    for a in &journey.actions {
        if a.effect_families.iter().any(|e| e == "Auth") {
            classifications
                .entry(a.msg.clone())
                .or_default()
                .reasons
                .push("Auth session / token".to_string());
        }
    }
    if let Ok(rep) = crate::spa_partition::analyze(repo_root, project_dir, entry_module) {
        // Sensitive Model fields: Secret-typed or PII-named.
        let mut sensitive_field: HashMap<String, String> = HashMap::new();
        for f in &rep.model_fields {
            if is_secret_ty(&f.ty_name) {
                sensitive_field.insert(f.name.clone(), format!("Secret field `{}`", f.name));
            } else if pii_reason(&f.name).is_some() {
                sensitive_field.insert(f.name.clone(), format!("PII field `{}`", f.name));
            }
        }
        for b in &rep.branches {
            let mut reasons: Vec<String> = Vec::new();
            if b.effect_families.iter().any(|e| e == "Auth") {
                reasons.push("Auth session / token".to_string());
            }
            for a in &b.msg_arg_tys {
                if is_secret_ty(&a.ty_name) {
                    reasons.push(format!("Secret arg `{}`", a.name));
                } else if pii_reason(&a.name).is_some() {
                    reasons.push(format!("PII arg `{}`", a.name));
                }
            }
            if let Some(io) = &b.io {
                for f in io.read_fields.iter().chain(io.write_fields.iter()) {
                    if let Some(r) = sensitive_field.get(f) {
                        reasons.push(r.clone());
                    }
                }
            }
            if !reasons.is_empty() {
                classifications.entry(b.msg.clone()).or_default().reasons.extend(reasons);
            }
        }
    }
    // Normalise: sort/dedup reasons and set the confidential flag.
    for c in classifications.values_mut() {
        c.reasons.sort();
        c.reasons.dedup();
        c.confidential = !c.reasons.is_empty();
    }
    classifications.retain(|_, c| c.confidential);

    // Data store node: present when any action reaches the `Db` family, or when
    // sky.toml declares a database driver.
    let uses_db = journey.actions.iter().any(|a| a.effect_families.iter().any(|e| e == "Db"));
    let data_store = if uses_db {
        Some(data_store_name(project_dir).unwrap_or_else(|| "App database".to_string()))
    } else {
        data_store_name(project_dir)
    };

    // Global-chrome split: a Msg dispatchable from EVERY page is shared chrome.
    let chrome_actions: Vec<String> = if grounded && page_actions.len() >= 2 {
        let mut counts: HashMap<String, usize> = HashMap::new();
        for msgs in page_actions.values() {
            for m in msgs {
                *counts.entry(m.clone()).or_default() += 1;
            }
        }
        let n = page_actions.len();
        let mut c: Vec<String> = counts
            .into_iter()
            .filter(|(_, k)| *k == n)
            .map(|(m, _)| m)
            .collect();
        c.sort();
        c
    } else {
        Vec::new()
    };

    Ok(FlowReport {
        journey,
        page_actions,
        continuations,
        grounded,
        classifications,
        action_externals,
        external_systems,
        self_ref_hosts,
        data_store,
        chrome_actions,
    })
}

/// The actions available on `page`: the grounded set from the view when we have
/// it, else the whole action inventory (fallback). Returns references into
/// `r.journey.actions`, ordered as the inventory is.
fn actions_on_page<'a>(r: &'a FlowReport, page: &str) -> Vec<&'a JourneyAction> {
    if r.grounded {
        let allowed = r.page_actions.get(page);
        r.journey
            .actions
            .iter()
            .filter(|a| allowed.map(|s| s.contains(&a.msg)).unwrap_or(false))
            // Global chrome is rendered once in its own section, not per page.
            .filter(|a| !r.chrome_actions.contains(&a.msg))
            .collect()
    } else {
        r.journey.actions.iter().collect()
    }
}

/// Today's date as `YYYY-MM-DD` (UTC), for the generated-on stamp. No chrono dep:
/// a civil-date computation from the Unix epoch day count.
pub fn today_utc() -> String {
    let secs = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let days = (secs / 86_400) as i64;
    // Howard Hinnant's civil_from_days.
    let z = days + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    format!("{y:04}-{m:02}-{d:02}")
}

/// One line describing an action edge: the Msg, its lane (server/_rpc vs client),
/// effect families, navigation target, and async continuation.
fn flow_action_line(r: &FlowReport, a: &JourneyAction) -> String {
    let lane = match a.server {
        Some(true) => " `POST /_rpc`".to_string(),
        Some(false) => " client".to_string(),
        None => String::new(),
    };
    // Effect families, with the named external host spliced in where the branch
    // reaches HTTP (`Http → api.stripe.com`).
    let hosts = r.action_externals.get(&a.msg);
    let eff = if a.effect_families.is_empty() {
        if a.effectful { " · effect".to_string() } else { String::new() }
    } else {
        let fams: Vec<String> = a
            .effect_families
            .iter()
            .map(|f| {
                if f == "Http" {
                    if let Some(hs) = hosts {
                        let named: Vec<&str> =
                            hs.iter().filter(|h| *h != "*dynamic*").map(|s| s.as_str()).collect();
                        if !named.is_empty() {
                            return format!("Http → {}", named.join(", "));
                        }
                    }
                }
                f.clone()
            })
            .collect();
        format!(" · {}", fams.join(", "))
    };
    let mut nav: Vec<String> = a.navigates_to.clone();
    if a.dynamic_nav {
        nav.push("(dynamic)".to_string());
    }
    let nav_s = if nav.is_empty() {
        String::new()
    } else {
        format!(" → **{}**", nav.join(", "))
    };
    let cont = match r.continuations.get(&a.msg) {
        Some(cs) if !cs.is_empty() => format!(" ⇢ _{}_", cs.join(", ")),
        _ => String::new(),
    };
    let conf = match r.classifications.get(&a.msg) {
        Some(c) if c.confidential => format!("  🔒 **CONFIDENTIAL** ({})", c.reasons.join("; ")),
        _ => String::new(),
    };
    format!("`{}`{}{}{}{}{}", a.msg, lane, eff, nav_s, cont, conf)
}

/// The external-systems Markdown: a sub-processor table (data flows — ISO A.15 /
/// SOC2 supplier evidence) and, separately, any embedded third-party content
/// (CDN / video — NOT a data processor), plus a note on hosts filtered as the
/// app's own domain. Shared by the journey md and the audit bundle's
/// `sub-processors.md`.
fn subprocessors_section(r: &FlowReport) -> String {
    let mut o = String::new();
    let subs: Vec<&ExternalSystem> =
        r.external_systems.iter().filter(|e| e.role.is_subprocessor()).collect();
    let embeds: Vec<&ExternalSystem> =
        r.external_systems.iter().filter(|e| !e.role.is_subprocessor()).collect();
    if !subs.is_empty() {
        o.push_str("## External systems (data sub-processors)\n\n");
        o.push_str("_Third parties the app sends or receives application data to/from (ISO 27001 A.15 / SOC2 supplier evidence)._\n\n");
        o.push_str("| Host | Role | Purpose |\n|---|---|---|\n");
        for e in &subs {
            o.push_str(&format!("| `{}` | {} | {} |\n", e.host, e.role.label(), e.purpose));
        }
        o.push('\n');
    }
    if !embeds.is_empty() {
        o.push_str("## Embedded third-party content\n\n");
        o.push_str("_Static / embedded content (CDN, fonts, video). NOT a data sub-processor — no application data flows to it._\n\n");
        o.push_str("| Host | Purpose |\n|---|---|\n");
        for e in &embeds {
            o.push_str(&format!("| `{}` | {} |\n", e.host, e.purpose));
        }
        o.push('\n');
    }
    if !r.self_ref_hosts.is_empty() {
        o.push_str(&format!(
            "_Excluded as the app's own domain (not a sub-processor): {}._\n\n",
            r.self_ref_hosts.iter().map(|h| format!("`{h}`")).collect::<Vec<_>>().join(", ")
        ));
    }
    o
}

/// The data-inventory Markdown (ISO 27001 A.8 asset inventory): the app's data
/// store and the confidential field classes it holds, each with the reason. A
/// store-level inventory (per-table recovery is not attempted); the confidential
/// field list is the audit-relevant part.
fn data_inventory_section(r: &FlowReport) -> String {
    let mut o = String::new();
    o.push_str("## Data inventory\n\n");
    o.push_str("_Data at rest and its classification (ISO 27001 A.8)._\n\n");
    match &r.data_store {
        Some(store) => o.push_str(&format!("**Store:** {store}\n\n")),
        None => {
            o.push_str("_No persistent data store detected (the app reaches no `Db` effect)._\n\n");
            return o;
        }
    }
    // Distinct confidential reasons across every classified action — the classes
    // of sensitive data the app handles (and therefore may persist).
    let mut classes: Vec<String> = r
        .classifications
        .values()
        .flat_map(|c| c.reasons.iter().cloned())
        .collect();
    classes.sort();
    classes.dedup();
    if classes.is_empty() {
        o.push_str("No `Secret`, `Std.Auth` session, or PII-classified fields were detected in the app's actions.\n\n");
    } else {
        o.push_str("| Data class | Classification |\n|---|---|\n");
        for c in &classes {
            o.push_str(&format!("| {c} | 🔒 Confidential |\n"));
        }
        o.push('\n');
    }
    o
}

/// Re-apply the own-domain (self-referential host) filter using the REAL project
/// directory. `analyze_flow` runs over the Spa-synthesised staged dir, which has
/// no `CNAME` / `sky.toml`, so its own-domain list is empty; the caller (which
/// holds the real project path) calls this to move the app's own hosts out of the
/// sub-processor list. Idempotent.
pub fn filter_self_ref(r: &mut FlowReport, project_dir: &Path) {
    let own = own_domains(project_dir);
    if own.is_empty() {
        return;
    }
    let mut kept: Vec<ExternalSystem> = Vec::new();
    for e in r.external_systems.drain(..) {
        if e.role != ExtRole::Dynamic && is_self_ref(&e.host, &own) {
            r.self_ref_hosts.push(e.host);
        } else {
            kept.push(e);
        }
    }
    r.external_systems = kept;
    r.self_ref_hosts.sort();
    r.self_ref_hosts.dedup();
}

/// Standalone sub-processor register for the audit bundle (ISO A.15 / SOC2).
pub fn render_subprocessors(r: &FlowReport) -> String {
    let body = subprocessors_section(r);
    let body = if body.trim().is_empty() {
        "_No external systems were detected in this app's HTTP call sites._\n".to_string()
    } else {
        body
    };
    format!(
        "# Sub-processors & external systems — {} · generated {}\n\n{}",
        r.journey.project,
        today_utc(),
        body
    )
}

/// Standalone data-inventory register for the audit bundle (ISO A.8).
pub fn render_data_inventory(r: &FlowReport) -> String {
    format!(
        "# Data inventory — {} · generated {}\n\n{}",
        r.journey.project,
        today_utc(),
        data_inventory_section(r)
    )
}

/// Render the behaviour graph to the requested format.
pub fn render_flow(r: &FlowReport, format: Format) -> String {
    match format {
        Format::Md => render_flow_md(r),
        Format::Puml => render_flow_puml(r),
        Format::Svg => render_flow_svg(r),
    }
}

/// The submittable artefact: a trust-boundary swimlane data-flow diagram. Four
/// lanes (Browser client · /_rpc server · Data store · External systems) with the
/// data flows between them; confidential flows are drawn red with a lock. This is
/// the picture a user hands a SOC2 / ISO 27001 auditor.
fn render_flow_svg(r: &FlowReport) -> String {
    use diagram_svg as d;
    let j = &r.journey;
    const CONF: &str = "#dc2626"; // confidential flow (red)
    let title = format!("{} — behaviour & data flow · generated {}", j.project, today_utc());
    let mut svg = d::Svg::new(&title);

    // Non-web shapes have no client/server split — one honest line, not a broken
    // swimlane. (Cli/Tui/Http still get the md + puml behaviour views.)
    if matches!(j.shape, AppShape::Http | AppShape::Cli | AppShape::Tui) {
        svg.text(16.0, 30.0, &title, "start", 13.0, "700", d::TEXT);
        svg.text(16.0, 54.0, "This app has no browser trust boundary; see the Markdown behaviour view and `--diagram wire`.", "start", 11.5, "400", d::SUBTLE);
        return svg.render();
    }

    let left = 24.0_f64;
    let width = 940.0_f64;
    let lane_w = width - left * 2.0;
    let confidential_n = j.actions.iter().filter(|a| r.classifications.get(&a.msg).map(|c| c.confidential).unwrap_or(false)).count();

    // Lane + trust-boundary labels differ by app shape: a Sky.Spa client is a wasm
    // app reaching the server over `/_rpc`; a Sky.Live client is a plain browser
    // (server-rendered HTML) reaching the server over an SSE event channel. Only
    // Spa/Live reach here (the early return handled Http/Cli/Tui).
    let (client_lane, server_lane, boundary_label) = match j.shape {
        AppShape::Live => (
            "① Browser — pages the user sees",
            "② Server (SSR + SSE) — every effect runs here",
            "— — — trust boundary: HTTPS / SSE events — — —",
        ),
        _ => (
            "① Browser client (wasm) — pages the user sees",
            "② Server (/_rpc) — every effect runs here",
            "— — — trust boundary: HTTPS / _rpc — — —",
        ),
    };

    // ---- Lane 1: Browser client (wasm) — page nodes in wrapped rows. ----
    let mut y = 60.0;
    let pn_w = 150.0_f64;
    let pn_h = 42.0_f64;
    let gap = 16.0_f64;
    let per_row = ((lane_w - 20.0) / (pn_w + gap)).floor().max(1.0) as usize;
    let pages: Vec<&JourneyPage> = j.pages.iter().collect();
    let rows = if pages.is_empty() { 1 } else { (pages.len() + per_row - 1) / per_row };
    let lane1_h = 30.0 + rows as f64 * (pn_h + gap);
    svg.zone(left, y, lane_w, lane1_h, client_lane, d::CLIENT_EDGE);
    let mut server_anchor_x = left + lane_w / 2.0;
    if pages.is_empty() {
        svg.node(left + 20.0, y + 30.0, pn_w, pn_h, d::FILL, d::CLIENT_EDGE, "(single view)", None);
    } else {
        for (i, p) in pages.iter().enumerate() {
            let col = i % per_row;
            let row = i / per_row;
            let nx = left + 20.0 + col as f64 * (pn_w + gap);
            let ny = y + 30.0 + row as f64 * (pn_h + gap);
            svg.node(nx, ny, pn_w, pn_h, d::FILL, d::CLIENT_EDGE, &p.name, p.url.as_deref());
        }
    }
    server_anchor_x = server_anchor_x.max(left + lane_w / 2.0);
    y += lane1_h + 46.0;

    // ---- Lane 2: /_rpc server ----
    let sv_w = 300.0_f64;
    let sv_h = 56.0_f64;
    let lane2_h = 30.0 + sv_h + 14.0;
    let boundary_y = y - 24.0;
    svg.zone(left, y, lane_w, lane2_h, server_lane, d::SERVER_EDGE);
    let sv_x = left + lane_w / 2.0 - sv_w / 2.0;
    let sv_y = y + 30.0;
    let srv_sub = match j.shape {
        AppShape::Spa => "Sky.Spa SSR backend",
        AppShape::Live => "Sky.Live server (SSR + SSE)",
        _ => "server",
    };
    svg.node(sv_x, sv_y, sv_w, sv_h, d::FILL_ALT, d::SERVER_EDGE, "Application server", Some(srv_sub));
    let sv_cx = sv_x + sv_w / 2.0;
    // client → server: the aggregated user-action flow (the trust-boundary cross).
    let act_label = if confidential_n > 0 {
        format!("{} user actions · {} confidential 🔒", j.actions.len(), confidential_n)
    } else {
        format!("{} user actions", j.actions.len())
    };
    let cross_color = if confidential_n > 0 { CONF } else { d::CLIENT_EDGE };
    svg.edge(sv_cx, sv_y, server_anchor_x, boundary_y + 4.0, cross_color, Some(&act_label));
    // the trust boundary line
    svg.text(left, boundary_y, boundary_label, "start", 10.5, "600", d::BOUNDARY_UNTRUSTED);
    y += lane2_h + 46.0;

    // ---- Lane 3: Data store ----
    if let Some(store) = &r.data_store {
        let lane3_h = 30.0 + 52.0 + 12.0;
        svg.zone(left, y, lane_w, lane3_h, "③ Data store", d::BOUNDARY_TRUSTED);
        let db_w = 220.0_f64;
        let db_x = left + lane_w / 2.0 - db_w / 2.0;
        let db_y = y + 30.0;
        svg.database(db_x, db_y, db_w, 52.0, d::FILL, d::BOUNDARY_TRUSTED, store);
        let db_conf = j.actions.iter().any(|a| a.effect_families.iter().any(|e| e == "Db") && r.classifications.get(&a.msg).map(|c| c.confidential).unwrap_or(false));
        let ecol = if db_conf { CONF } else { d::SERVER_EDGE };
        svg.edge(db_x + db_w / 2.0, db_y, sv_cx, sv_y + sv_h, ecol, Some(if db_conf { "reads/writes 🔒" } else { "reads/writes" }));
        y += lane3_h + 46.0;
    }

    // ---- Lane 4: External systems (sub-processors) ----
    if !r.external_systems.is_empty() {
        let ex_w = 190.0_f64;
        let ex_h = 46.0_f64;
        let per = ((lane_w - 20.0) / (ex_w + gap)).floor().max(1.0) as usize;
        let erows = (r.external_systems.len() + per - 1) / per;
        let lane4_h = 30.0 + erows as f64 * (ex_h + gap);
        svg.zone(left, y, lane_w, lane4_h, "④ External systems (sub-processors)", d::EXTERNAL);
        for (i, e) in r.external_systems.iter().enumerate() {
            let col = i % per;
            let row = i / per;
            let nx = left + 20.0 + col as f64 * (ex_w + gap);
            let ny = y + 30.0 + row as f64 * (ex_h + gap);
            svg.node(nx, ny, ex_w, ex_h, d::FILL, d::EXTERNAL, &e.host, Some(&e.purpose));
            svg.edge(nx + ex_w / 2.0, ny, sv_cx, sv_y + sv_h, d::EXTERNAL, None);
        }
        y += lane4_h + 46.0;
    }

    // ---- Legend ----
    svg.text(left, y, "Legend:", "start", 11.5, "700", d::TEXT);
    svg.text(left + 62.0, y, "blue = user action across the trust boundary", "start", 10.5, "400", d::CLIENT_EDGE);
    svg.text(left + 62.0, y + 16.0, "red 🔒 = confidential flow (Secret / Std.Auth session / PII)", "start", 10.5, "400", CONF);
    svg.text(left + 62.0, y + 32.0, "green = data store · purple = external sub-processor", "start", 10.5, "400", d::EXTERNAL);
    let _ = width;
    svg.render()
}

fn render_flow_md(r: &FlowReport) -> String {
    let j = &r.journey;
    let mut o = String::new();
    o.push_str(&format!("# Behaviour & data flow — {} · generated {}\n\n", j.project, today_utc()));
    let shape_line = match j.shape {
        AppShape::Spa => "Sky.Spa (wasm client + server over /_rpc)",
        AppShape::Live => "Sky.Live (one server, SSR + SSE)",
        AppShape::Tui => "Sky.Tui (single terminal binary)",
        AppShape::Cli => "Sky.Cli (single terminal binary)",
        AppShape::Http => "Sky.Http.Server (HTTP API, no user journey)",
    };
    o.push_str(&format!("App shape: {shape_line}\n\n"));
    // On a Sky.Spa app each action is classified `/_rpc` (server round-trip) vs
    // client; a Sky.Live app has no `/_rpc` boundary — every action reaches the
    // one server over the SSE event channel — so there is no per-action lane chip.
    let lane_clause = match j.shape {
        AppShape::Live => "its effect families",
        _ => "its lane (`/_rpc` vs client), effect families",
    };
    o.push_str(&format!(
        "The interaction graph: each page is a state the user sees; each action is \
         an edge out of the page whose view can trigger it, labelled with {lane_clause}, \
         the page it navigates to (**bold**), its async continuation (⇢ _Msg_), and a 🔒 \
         marker when the flow carries confidential data (a `Secret`, a `Std.Auth` \
         session, or PII).\n\n",
    ));
    // ---- overlays: data store + external systems (sub-processors) ----
    if let Some(store) = &r.data_store {
        o.push_str(&format!("**Data store:** {store}\n\n"));
    }
    o.push_str(&subprocessors_section(r));
    // ---- global chrome (actions on every page) ----
    if !r.chrome_actions.is_empty() {
        o.push_str("## Global (available on every page)\n\n");
        o.push_str("_Shared navigation / layout actions, dispatchable from any page._\n\n");
        for a in j.actions.iter().filter(|a| r.chrome_actions.contains(&a.msg)) {
            o.push_str(&format!("- {}\n", flow_action_line(r, a)));
        }
        o.push('\n');
    }
    if j.pages.is_empty() && j.actions.is_empty() {
        for n in &j.notes {
            o.push_str(&format!("> {n}\n"));
        }
        return o;
    }
    let init = initial_page(j);
    if r.grounded && !j.pages.is_empty() {
        // Per-page grounding: each page lists only the actions its view dispatches.
        let mut order: Vec<usize> = (0..j.pages.len()).collect();
        if let Some(i) = init {
            order.sort_by_key(|&k| if k == i { 0 } else { 1 });
        }
        for &idx in &order {
            let p = &j.pages[idx];
            let is_init = Some(idx) == init;
            let url = p.url.as_deref().map(|u| format!(" · `{u}`")).unwrap_or_default();
            let star = if is_init { " (initial)" } else { "" };
            o.push_str(&format!("## {}{}{}\n\n", p.name, url, star));
            let acts = actions_on_page(r, &p.name);
            if acts.is_empty() {
                o.push_str("_No page-specific actions (see Global above)._\n\n");
                continue;
            }
            for a in acts {
                o.push_str(&format!("- {}\n", flow_action_line(r, a)));
            }
            o.push('\n');
        }
    } else {
        // Ungrounded (the `view` does not dispatch on the page field) OR no pages:
        // list the pages once, then the full action set ONCE — never repeated per
        // page. The navigation / effect / lane / continuation / classification
        // edges are all still exact; only the page→action attribution is absent.
        if !j.pages.is_empty() {
            o.push_str("## Pages\n\n");
            for p in &j.pages {
                let url = p.url.as_deref().map(|u| format!(" · `{u}`")).unwrap_or_default();
                let star = if Some(p.name.clone()) == init.map(|i| j.pages[i].name.clone()) {
                    " (initial)"
                } else {
                    ""
                };
                o.push_str(&format!("- **{}**{}{}\n", p.name, url, star));
            }
            o.push('\n');
            o.push_str(
                "> Actions are listed once below rather than per page: this app's `view` does \
                 not dispatch on the page field, so the compiler cannot attribute an action to a \
                 specific page. Every other edge (navigation, effect, lane, external system, \
                 continuation, classification) is exact.\n\n",
            );
        }
        o.push_str("## Actions\n\n");
        for a in &j.actions {
            o.push_str(&format!("- {}\n", flow_action_line(r, a)));
        }
        o.push('\n');
    }
    // The journey's "one inventory / per-page attribution is best-effort" note is
    // wrong for `flow` when we DID attribute per page — drop it; keep the rest.
    for n in &j.notes {
        if r.grounded && (n.contains("one inventory") || n.contains("per-page attribution")) {
            continue;
        }
        o.push_str(&format!("> {n}\n"));
    }
    o
}

fn render_flow_puml(r: &FlowReport) -> String {
    let j = &r.journey;
    let mut o = String::new();
    o.push_str("@startuml\n");
    o.push_str(&format!("title Behaviour — {}\n", j.project));
    o.push_str("hide empty description\n");
    if j.pages.is_empty() {
        // No pages: emit the actions as a note, matching the journey fallback.
        o.push_str("state Actions\n");
        for a in &j.actions {
            o.push_str(&format!("Actions : {}\n", flow_action_line_plain(r, a)));
        }
        o.push_str("@enduml\n");
        return o;
    }
    let init = initial_page(j);
    for (idx, p) in j.pages.iter().enumerate() {
        let id = page_node_id(&p.name);
        o.push_str(&format!("state \"{}\" as {}\n", p.name, id));
        if Some(idx) == init {
            o.push_str(&format!("[*] --> {id}\n"));
        }
    }
    // One transition per action that navigates, sourced from the page(s) whose
    // view dispatches it (grounded), else from the initial page.
    for a in &j.actions {
        if a.navigates_to.is_empty() && !a.dynamic_nav {
            continue;
        }
        let sources = flow_sources(r, &a.msg, init.map(|i| j.pages[i].name.clone()));
        let arrow = match a.server {
            Some(true) => format!("-[{}]->", diagram_svg::SERVER_EDGE),
            Some(false) => format!("-[{}]->", diagram_svg::CLIENT_EDGE),
            None => "-->".to_string(),
        };
        for src in &sources {
            for t in &a.navigates_to {
                o.push_str(&format!(
                    "{} {} {} : {}\n",
                    page_node_id(src),
                    arrow,
                    page_node_id(t),
                    short_edge_label(&a.msg)
                ));
            }
            if a.dynamic_nav {
                o.push_str(&format!("{} --> [*] : {} (dynamic)\n", page_node_id(src), short_edge_label(&a.msg)));
            }
        }
    }
    // Overlay legend: external sub-processors + the data store, as a floating note.
    if !r.external_systems.is_empty() || r.data_store.is_some() {
        o.push_str("note as N1\n");
        if let Some(store) = &r.data_store {
            o.push_str(&format!("Data store: {store}\n"));
        }
        if !r.external_systems.is_empty() {
            o.push_str("External systems (sub-processors):\n");
            for e in &r.external_systems {
                o.push_str(&format!("  - {} ({})\n", e.host, e.purpose));
            }
        }
        o.push_str("end note\n");
    }
    o.push_str("@enduml\n");
    o
}

/// The plain (no-markdown) form of [`flow_action_line`] for the puml note fallback.
fn flow_action_line_plain(r: &FlowReport, a: &JourneyAction) -> String {
    let s = flow_action_line(r, a);
    s.replace(['`', '*', '_'], "")
}

/// The page(s) an action is sourced from: the grounded views that dispatch it,
/// else the given fallback page.
fn flow_sources(r: &FlowReport, msg: &str, fallback: Option<String>) -> Vec<String> {
    if r.grounded {
        let mut v: Vec<String> = r
            .page_actions
            .iter()
            .filter(|(_, msgs)| msgs.iter().any(|m| m == msg))
            .map(|(p, _)| p.clone())
            .collect();
        v.sort();
        if !v.is_empty() {
            return v;
        }
    }
    fallback.into_iter().collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn graph(is_spa: bool) -> ComponentGraph {
        graph_with(is_spa, Vec::new(), None, None)
    }

    fn graph_with(
        is_spa: bool,
        tables: Vec<String>,
        rpc_effectful: Option<usize>,
        rpc_pure: Option<usize>,
    ) -> ComponentGraph {
        let mut api = BTreeSet::new();
        api.insert(Capability::Database);
        api.insert(Capability::Auth);
        ComponentGraph {
            project: "examples/demo".into(),
            is_spa,
            shape: if is_spa { AppShape::Spa } else { AppShape::Live },
            tables,
            rpc_effectful,
            rpc_pure,
            modules: vec![
                ModuleUse {
                    module: "Api".into(),
                    caps: api,
                },
                ModuleUse {
                    module: "View".into(),
                    caps: BTreeSet::new(),
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

    fn is_svg(s: &str) -> bool {
        s.trim_start().starts_with("<svg") && s.trim_end().ends_with("</svg>")
    }
    fn count(hay: &str, needle: &str) -> usize {
        hay.matches(needle).count()
    }
    /// The `y` attribute of the first `<text …>label</text>` node whose content
    /// is exactly `label` — used to prove two stacked labels sit on distinct rows.
    fn text_y(svg: &str, label: &str) -> Option<f64> {
        let needle = format!(">{label}</text>");
        for seg in svg.split("<text").skip(1) {
            let head = seg.split('>').next().unwrap_or("");
            if seg.contains(&needle) {
                if let Some(i) = head.find("y=\"") {
                    let rest = &head[i + 3..];
                    if let Some(j) = rest.find('"') {
                        return rest[..j].parse().ok();
                    }
                }
            }
        }
        None
    }

    // ---- components ----

    #[test]
    fn components_puml_single_lane_is_c4() {
        let out = render_components(&graph(false), Format::Puml);
        assert!(out.starts_with("@startuml"), "{out}");
        assert!(out.trim_end().ends_with("@enduml"), "{out}");
        // C4: an actor, a trust-boundary server zone, a Backend container.
        assert!(out.contains("actor \"User\" as user"), "{out}");
        assert!(
            out.contains("rectangle \"Server · trusted\" <<boundary>>"),
            "{out}"
        );
        assert!(out.contains("as backend <<container>>"), "{out}");
        // Database collapses to a data store reached by a SQL edge.
        assert!(out.contains("database \"Database\" as store0"), "{out}");
        assert!(out.contains("backend --> store0 : SQL"), "{out}");
        // Auth is a control marker on the client → server crossing; a Live app's
        // crossing is HTTPS + SSE (the session channel).
        assert!(out.contains("user --> backend : HTTPS + SSE · auth"), "{out}");
        // single lane: no browser zone, no /_rpc crossing.
        assert!(!out.contains("Browser · untrusted"), "{out}");
        assert!(!out.contains("/_rpc"), "{out}");
        assert!(!out.contains("```"), "{out}");
    }

    #[test]
    fn components_puml_spa_has_zones_and_rpc() {
        let out = render_components(&graph(true), Format::Puml);
        // The two trust-boundary zones either side of the /_rpc crossing.
        assert!(
            out.contains("rectangle \"Browser · untrusted\" <<boundary>>"),
            "{out}"
        );
        assert!(out.contains("as spa <<container>>"), "{out}");
        assert!(
            out.contains("rectangle \"Server · trusted\" <<boundary>>"),
            "{out}"
        );
        assert!(out.contains("spa --> backend : /_rpc · auth"), "{out}");
        assert!(out.contains("database \"Database\" as store0"), "{out}");
        assert!(out.contains("backend --> store0 : SQL"), "{out}");
    }

    #[test]
    fn components_puml_lists_tables_and_rpc_counts() {
        let g = graph_with(true, vec!["users".into(), "orders".into()], Some(3), Some(5));
        let out = render_components(&g, Format::Puml);
        // The Database node label carries the real table names.
        assert!(
            out.contains("database \"Database\\nusers\\norders\" as store0"),
            "table list in the Database node:\n{out}"
        );
        // The /_rpc crossing carries the effectful count; a note the pure count.
        assert!(out.contains("spa --> backend : /_rpc · auth · 3 effectful"), "{out}");
        assert!(
            out.contains("note bottom of spa : 5 pure client actions (wasm)"),
            "{out}"
        );
    }

    #[test]
    fn components_puml_caps_tables_with_plus_n_more() {
        let tables: Vec<String> = (0..12).map(|i| format!("t{i}")).collect();
        let g = graph_with(false, tables, None, None);
        let out = render_components(&g, Format::Puml);
        assert!(out.contains("\\n+4 more\""), "over-cap folds into +N more:\n{out}");
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
    fn components_svg_is_c4_with_trust_zone_and_container() {
        let out = render_components(&graph(false), Format::Svg);
        assert!(is_svg(&out), "{out}");
        // A trust-boundary zone (dashed) and a C4 Backend container.
        assert!(
            out.contains("stroke-dasharray"),
            "trust boundary is dashed: {out}"
        );
        assert!(
            out.contains(">Server · trusted<"),
            "trust boundary zone: {out}"
        );
        assert!(out.contains(">Backend<"), "C4 container: {out}");
        assert!(out.contains(">Database<"), "data store: {out}");
        // orthogonal edges are polylines / lines.
        assert!(
            count(&out, "<line") >= 1 || count(&out, "<polyline") >= 1,
            "{out}"
        );
    }

    #[test]
    fn components_svg_spa_has_zones_and_rpc() {
        let out = render_components(&graph(true), Format::Svg);
        assert!(is_svg(&out), "{out}");
        assert!(out.contains(">Browser · untrusted<"), "{out}");
        assert!(out.contains(">Server · trusted<"), "{out}");
        assert!(out.contains(">SPA<"), "{out}");
        assert!(out.contains(">/_rpc<"), "{out}");
    }

    #[test]
    fn components_svg_lists_db_tables_inside_the_database() {
        let g = graph_with(false, vec!["users".into(), "orders".into()], None, None);
        let out = render_components(&g, Format::Svg);
        assert!(is_svg(&out), "{out}");
        assert!(out.contains(">users<"), "table name inside Database store: {out}");
        assert!(out.contains(">orders<"), "table name inside Database store: {out}");
    }

    #[test]
    fn components_md_lists_db_tables() {
        let g = graph_with(false, vec!["users".into(), "orders".into()], None, None);
        let out = render_components(&g, Format::Md);
        // The C4 container-led md lists the data store's tables in the data-store
        // section (a container row + the table list), and keeps the module detail
        // in the appendix.
        assert!(out.contains("### Data store — 2 table(s)"), "data-store heading:\n{out}");
        assert!(out.contains("`users`") && out.contains("`orders`"), "table names:\n{out}");
        assert!(out.contains("## Containers"), "C4 container view leads:\n{out}");
        assert!(out.contains("## Appendix — modules"), "module table demoted to appendix:\n{out}");
    }

    #[test]
    fn components_svg_caps_db_tables_with_plus_n_more() {
        let tables: Vec<String> = (0..12).map(|i| format!("t{i}")).collect();
        let g = graph_with(false, tables, None, None);
        let out = render_components(&g, Format::Svg);
        assert!(out.contains(">+4 more<"), "over-cap folds into +N more: {out}");
    }

    #[test]
    fn components_svg_spa_rpc_edge_counts_effectful_actions() {
        let g = graph_with(true, Vec::new(), Some(3), Some(5));
        let out = render_components(&g, Format::Svg);
        assert!(out.contains(">3 effectful → /_rpc<"), "effectful count on /_rpc edge: {out}");
        assert!(
            out.contains(">5 pure client actions (wasm)<"),
            "pure caption under SPA: {out}"
        );
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
            shape: AppShape::Spa,
            target: Some("web:app".into()),
            endpoints: vec![
                WireEndpoint {
                    msg: "SetRegion".into(),
                    request: "{basket, region} + {region}".into(),
                    response: "{basket, region}".into(),
                    effects: Some("Db".into()),
                    effect_families: vec!["Db".into()],
                    read_fields: vec!["basket".into(), "region".into()],
                    write_fields: vec!["basket".into(), "region".into()],
                    always_written: vec![],
                    reads_whole_model: false,
                    writes_whole_model: false,
                    msg_arg_tys: vec![],
                },
                WireEndpoint {
                    msg: "SaveAll".into(),
                    request: "whole model".into(),
                    response: "whole model".into(),
                    effects: None,
                    effect_families: vec![],
                    read_fields: vec![],
                    write_fields: vec![],
                    always_written: vec![],
                    reads_whole_model: true,
                    writes_whole_model: true,
                    msg_arg_tys: vec![],
                },
            ],
            http_endpoints: vec![],
            limited: false,
            model_fields: vec![],
            data_store: Some("PostgreSQL".into()),
            notes: vec!["a note".into()],
        }
    }

    /// A Spa app with a raw `App.api` webhook beside `/_rpc`.
    fn wire_report_with_webhook() -> WireReport {
        let mut r = wire_report();
        r.http_endpoints = vec![HttpEndpoint {
            method: "POST".into(),
            path: "/webhooks/stripe".into(),
            handler: "Payments.handleWebhook".into(),
            kind: EndpointKind::RawApi,
        }];
        r
    }

    #[test]
    fn wire_md_has_the_rpc_table() {
        let out = render_wire(&wire_report(), Format::Md);
        assert!(out.contains("## RPC endpoints (/_rpc)"), "{out}");
        assert!(
            out.contains("| Endpoint | Access | Request (fields + args) | Response (writes) | Call-path |"),
            "{out}"
        );
        assert!(
            out.contains(
                "| POST /_rpc/SetRegion | CSRF | {basket, region} + {region} | {basket, region} | Db → PostgreSQL |"
            ),
            "{out}"
        );
        assert!(
            out.contains("| POST /_rpc/SaveAll | CSRF | whole model | whole model | — |"),
            "{out}"
        );
    }

    #[test]
    fn wire_md_lists_the_app_api_webhook_beside_rpc() {
        let out = render_wire(&wire_report_with_webhook(), Format::Md);
        assert!(out.contains("## HTTP endpoints (raw `App.api`, beside /_rpc)"), "{out}");
        assert!(
            out.contains("| POST | /webhooks/stripe | Payments.handleWebhook | raw api · CSRF-exempt |"),
            "{out}"
        );
    }

    #[test]
    fn wire_svg_draws_the_webhook_as_an_inbound_external_entity() {
        let out = render_wire(&wire_report_with_webhook(), Format::Svg);
        assert!(is_svg(&out), "{out}");
        assert!(out.contains("HTTP endpoints (raw api · CSRF-exempt)"), "{out}");
        assert!(out.contains(">Webhook sender<"), "inbound external entity: {out}");
        assert!(out.contains("/webhooks/stripe"), "{out}");
        assert!(out.contains(">inbound<"), "inbound crossing arrow label: {out}");
    }

    #[test]
    fn wire_puml_is_a_dfd_with_boundary_and_endpoints() {
        let out = render_wire(&wire_report(), Format::Puml);
        assert!(out.starts_with("@startuml"), "{out}");
        // The two trust zones and the endpoint processes.
        assert!(
            out.contains("rectangle \"Client · untrusted\" <<boundary>>"),
            "{out}"
        );
        assert!(
            out.contains("rectangle \"Server · trusted\" <<boundary>>"),
            "{out}"
        );
        assert!(
            out.contains("rectangle \"POST /_rpc/SetRegion\" as ep0 <<endpoint>>"),
            "{out}"
        );
        // per-endpoint request + response across the boundary.
        assert!(out.contains("req {basket, region} + {region}"), "{out}");
        assert!(out.contains("resp {basket, region}"), "{out}");
        // per-endpoint effect families as a note (SetRegion reaches Db).
        assert!(out.contains("note right of ep") && out.contains("effects: Db"), "no effects note:\n{out}");
        assert!(!out.contains("```"), "{out}");
    }

    #[test]
    fn wire_svg_is_a_dfd_with_endpoints_section() {
        let out = render_wire(&wire_report(), Format::Svg);
        assert!(is_svg(&out), "{out}");
        assert!(out.contains(">Client · untrusted<"), "{out}");
        assert!(out.contains(">Server · trusted<"), "{out}");
        // The Endpoints section box + a per-endpoint row.
        assert!(out.contains("RPC endpoints (/_rpc)"), "{out}");
        assert!(out.contains("POST /_rpc/SetRegion"), "{out}");
    }

    // ---- wire (non-Spa HTTP endpoint map) ----

    fn http_wire_report() -> WireReport {
        WireReport {
            project: "examples/demo".into(),
            is_spa: false,
            shape: AppShape::Http,
            target: None,
            endpoints: vec![],
            http_endpoints: vec![
                HttpEndpoint {
                    method: "GET".into(),
                    path: "/".into(),
                    handler: "handleHome".into(),
                    kind: EndpointKind::HttpRoute,
                },
                HttpEndpoint {
                    method: "POST".into(),
                    path: "/api/echo".into(),
                    handler: "handleEcho".into(),
                    kind: EndpointKind::HttpRoute,
                },
            ],
            limited: false,
            model_fields: vec![],
            data_store: None,
            notes: vec![],
        }
    }

    #[test]
    fn wire_http_md_lists_the_endpoint_map() {
        let out = render_wire(&http_wire_report(), Format::Md);
        assert!(out.contains("| Method | Path | Handler / page | Kind |"), "{out}");
        assert!(out.contains("| GET | / | handleHome | http |"), "{out}");
        assert!(out.contains("| POST | /api/echo | handleEcho | http |"), "{out}");
        assert!(
            !out.contains("| Endpoint |"),
            "no /_rpc table for an HTTP app: {out}"
        );
    }

    #[test]
    fn wire_http_puml_and_svg() {
        let puml = render_wire(&http_wire_report(), Format::Puml);
        assert!(
            puml.contains("rectangle \"GET /\" as hep0 <<endpoint>>"),
            "{puml}"
        );
        assert!(
            puml.contains("rectangle \"POST /api/echo\" as hep1 <<endpoint>>"),
            "{puml}"
        );
        let svg = render_wire(&http_wire_report(), Format::Svg);
        assert!(is_svg(&svg), "{svg}");
        assert!(svg.contains("HTTP endpoints"), "{svg}");
        assert!(svg.contains("/api/echo"), "{svg}");
    }

    /// A Sky.Live app: page GET routes + a raw api, no /_rpc table.
    #[test]
    fn wire_live_lists_page_routes_and_raw_api() {
        let r = WireReport {
            project: "examples/demo".into(),
            is_spa: false,
            shape: AppShape::Live,
            target: Some("web".into()),
            endpoints: vec![],
            http_endpoints: vec![
                HttpEndpoint {
                    method: "GET".into(),
                    path: "/".into(),
                    handler: "HomePage".into(),
                    kind: EndpointKind::PageRoute,
                },
                HttpEndpoint {
                    method: "POST".into(),
                    path: "/webhooks/stripe".into(),
                    handler: "handleWebhook".into(),
                    kind: EndpointKind::RawApi,
                },
            ],
            limited: false,
            model_fields: vec![],
            data_store: None,
            notes: vec![],
        };
        let out = render_wire(&r, Format::Md);
        assert!(out.contains("| GET | / | HomePage | page |"), "{out}");
        assert!(
            out.contains("| POST | /webhooks/stripe | handleWebhook | raw api · CSRF-exempt |"),
            "{out}"
        );
        assert!(!out.contains("/_rpc"), "no /_rpc on a Live app: {out}");
    }

    /// A terminal app: no client boundary — one clear line, not a broken diagram.
    #[test]
    fn wire_terminal_is_a_single_line_not_a_diagram() {
        let r = WireReport {
            project: "examples/demo".into(),
            is_spa: false,
            shape: AppShape::Tui,
            target: Some("terminal:tui".into()),
            endpoints: vec![],
            http_endpoints: vec![],
            limited: false,
            model_fields: vec![],
            data_store: None,
            notes: vec![],
        };
        let svg = render_wire(&r, Format::Svg);
        assert!(is_svg(&svg), "{svg}");
        assert!(svg.contains("no client/server wire"), "{svg}");
        // No DFD zones are drawn for a terminal app.
        assert!(!svg.contains(">Server · trusted<"), "no boundary zone drawn: {svg}");
        assert!(!svg.contains(">Client · untrusted<"), "no client zone drawn: {svg}");
    }

    #[test]
    fn wire_non_spa_no_routes_is_a_note_not_a_table() {
        let r = WireReport {
            project: "examples/demo".into(),
            is_spa: false,
            shape: AppShape::Live,
            target: Some("web".into()),
            endpoints: vec![],
            http_endpoints: vec![],
            limited: false,
            model_fields: vec![],
            data_store: None,
            notes: vec!["not a Sky.Spa wasm client".into()],
        };
        let out = render_wire(&r, Format::Md);
        assert!(
            !out.contains("| Endpoint |") && !out.contains("| Method |"),
            "{out}"
        );
        assert!(out.contains("not a Sky.Spa wasm client"), "{out}");
    }

    // ---- telemetry ----

    fn telemetry_report() -> TelemetryReport {
        TelemetryReport {
            project: "examples/demo".into(),
            is_spa: false,
            calls: vec![
                TelemetryCall {
                    module: "Main".into(),
                    call: "Log.info".into(),
                    event: "startup".into(),
                    sink: Sink::Logs,
                },
                TelemetryCall {
                    module: "Update".into(),
                    call: "Analytics.track".into(),
                    event: "<dynamic>".into(),
                    sink: Sink::Analytics,
                },
                TelemetryCall {
                    module: "Main".into(),
                    call: "Analytics.setConsent".into(),
                    event: "<dynamic>".into(),
                    sink: Sink::Consent,
                },
            ],
            notes: vec!["a note".into()],
        }
    }

    #[test]
    fn telemetry_md_lists_each_call() {
        let out = render_telemetry(&telemetry_report(), Format::Md);
        assert!(out.contains("| Module | Call | Event | Sink |"), "{out}");
        assert!(out.contains("| Main | Log.info | startup | structured logs (console; OTel when OTEL_EXPORTER_OTLP_ENDPOINT set) |"), "{out}");
        assert!(
            out.contains("| Update | Analytics.track | <dynamic> | analytics store (DB) |"),
            "{out}"
        );
    }

    #[test]
    fn telemetry_puml_groups_internal_and_external_egress() {
        let out = render_telemetry(&telemetry_report(), Format::Puml);
        assert!(out.starts_with("@startuml"), "{out}");
        // Sinks are grouped by egress destination.
        assert!(out.contains("rectangle \"Internal\" <<boundary>>"), "{out}");
        assert!(
            out.contains("rectangle \"External egress\" <<boundary>>"),
            "{out}"
        );
        assert!(out.contains("rectangle \"Consent\" <<boundary>>"), "{out}");
        assert!(out.contains("queue \"Logs\" as sink_logs"), "{out}");
        assert!(
            out.contains("database \"Analytics DB\" as sink_analytics"),
            "{out}"
        );
        // Edges are colour-coded per egress class.
        assert!(
            out.contains("m_Main -[#333333]-> sink_logs : startup"),
            "{out}"
        );
        assert!(
            out.contains("m_Update -[#7c3aed]-> sink_analytics : dynamic"),
            "{out}"
        );
    }

    #[test]
    fn telemetry_svg_groups_sinks() {
        let out = render_telemetry(&telemetry_report(), Format::Svg);
        assert!(is_svg(&out), "{out}");
        // The internal + external egress grouping sections are present.
        assert!(out.contains(">Internal<"), "{out}");
        assert!(out.contains(">External egress<"), "{out}");
        assert!(out.contains(">Consent<"), "{out}");
        assert!(count(&out, "<polyline") >= 1, "{out}");
    }

    #[test]
    fn telemetry_empty_prints_the_no_sites_message() {
        let r = TelemetryReport {
            project: "x".into(),
            is_spa: false,
            calls: vec![],
            notes: vec![],
        };
        for f in [Format::Puml, Format::Md, Format::Svg] {
            assert_eq!(
                render_telemetry(&r, f),
                "No telemetry, analytics, or logging call sites found.\n"
            );
        }
    }

    // ---- journey (state machine) ----

    fn journey_report(classified: bool) -> JourneyReport {
        JourneyReport {
            project: "examples/demo".into(),
            is_spa: classified,
            target: Some("web:app".into()),
            pages: vec![
                JourneyPage {
                    name: "HomePage".into(),
                    url: Some("/".into()),
                },
                JourneyPage {
                    name: "LoginPage".into(),
                    url: None,
                },
            ],
            shape: if classified { AppShape::Spa } else { AppShape::Live },
            page_field: Some("currentPage".into()),
            actions: vec![
                JourneyAction {
                    msg: "Navigate".into(),
                    server: if classified { Some(false) } else { None },
                    navigates_to: vec![],
                    dynamic_nav: true,
                    effectful: false,
                    effect_families: vec![],
                },
                JourneyAction {
                    msg: "Refresh".into(),
                    server: if classified { Some(true) } else { None },
                    navigates_to: vec![],
                    dynamic_nav: false,
                    effectful: true,
                    effect_families: vec!["Http".into()],
                },
                JourneyAction {
                    msg: "UpvotePost".into(),
                    server: if classified { Some(true) } else { None },
                    navigates_to: vec!["LoginPage".into()],
                    dynamic_nav: false,
                    effectful: true,
                    effect_families: vec!["Db".into()],
                },
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
        assert!(out.contains("| Action | Kind | Effects | Navigates to |"), "{out}");
        assert!(
            out.contains("| UpvotePost | effectful (server · /_rpc) | Db | LoginPage |"),
            "{out}"
        );
        assert!(
            out.contains("| Navigate | pure | — | (dynamic page) |"),
            "{out}"
        );
    }

    #[test]
    fn journey_puml_is_a_state_machine() {
        let out = render_journey(&journey_report(true), Format::Puml);
        assert!(out.starts_with("@startuml"), "{out}");
        // page states with the URL folded in
        assert!(
            out.contains("state \"HomePage\\n/\" as pg_HomePage"),
            "{out}"
        );
        assert!(out.contains("state \"LoginPage\" as pg_LoginPage"), "{out}");
        // the dynamic pseudo-state + an initial marker into the / page
        assert!(out.contains("as dyn_pg"), "{out}");
        assert!(out.contains("[*] --> pg_HomePage"), "{out}");
        // a navigating transition, server-coloured (orange)
        assert!(
            out.contains("pg_HomePage -[#d9822b]-> pg_LoginPage : UpvotePost"),
            "{out}"
        );
        // the dynamic transition, client-coloured (blue)
        assert!(
            out.contains("pg_HomePage -[#2b6cb0]-> dyn_pg : Navigate"),
            "{out}"
        );
        // Non-navigating actions split into Effectful / Pure floating notes, the
        // effectful chip annotated with its effect family.
        assert!(out.contains("note as effectful_note"), "no effectful note:\n{out}");
        assert!(out.contains("<b>Effectful actions (1)"), "{out}");
        assert!(out.contains("Refresh · Http"), "effect family on the chip:\n{out}");
        assert!(!out.contains("```"), "{out}");
    }

    #[test]
    fn journey_collapses_parallel_edges_onto_one_labelled_edge() {
        // Two Msgs both navigate to LoginPage: they MUST collapse onto ONE edge
        // whose label lists BOTH (the overlap-disaster fix), never two parallel
        // edges with stacked-on-top labels.
        let r = JourneyReport {
            project: "x".into(),
            is_spa: true,
            shape: AppShape::Spa,
            target: Some("web:app".into()),
            pages: vec![
                JourneyPage {
                    name: "HomePage".into(),
                    url: Some("/".into()),
                },
                JourneyPage {
                    name: "LoginPage".into(),
                    url: None,
                },
            ],
            page_field: Some("page".into()),
            actions: vec![
                JourneyAction {
                    msg: "UpvotePost".into(),
                    server: Some(true),
                    navigates_to: vec!["LoginPage".into()],
                    dynamic_nav: false,
                    effectful: true,
                    effect_families: vec!["Db".into()],
                },
                JourneyAction {
                    msg: "DownvotePost".into(),
                    server: Some(true),
                    navigates_to: vec!["LoginPage".into()],
                    dynamic_nav: false,
                    effectful: true,
                    effect_families: vec!["Db".into()],
                },
            ],
            classified: true,
            notes: vec![],
        };
        // PlantUML: exactly ONE HomePage → LoginPage edge, both Msgs on its label.
        let puml = render_journey(&r, Format::Puml);
        assert_eq!(
            puml.matches("pg_HomePage -[#d9822b]-> pg_LoginPage :")
                .count(),
            1,
            "parallel edges must collapse to one:\n{puml}"
        );
        assert!(
            puml.contains("DownvotePost\\nUpvotePost"),
            "both Msgs on the one label:\n{puml}"
        );

        // SVG: both Msg labels appear, and on DISTINCT y (stacked, never on top
        // of each other) — the structural no-overlap check.
        let svg = render_journey(&r, Format::Svg);
        let yu = text_y(&svg, "UpvotePost").expect("UpvotePost label");
        let yd = text_y(&svg, "DownvotePost").expect("DownvotePost label");
        assert!(
            (yu - yd).abs() > 6.0,
            "stacked labels must have distinct y: {yu} vs {yd}"
        );
    }

    #[test]
    fn journey_svg_is_a_state_machine() {
        let out = render_journey(&journey_report(true), Format::Svg);
        assert!(is_svg(&out), "{out}");
        assert!(out.contains(">HomePage<"), "{out}");
        assert!(out.contains(">LoginPage<"), "{out}");
        assert!(out.contains(">(dynamic page)<"), "{out}");
        // the initial-page transition edge to LoginPage exists.
        assert!(
            count(&out, "<line") >= 2 || count(&out, "<polyline") >= 1,
            "{out}"
        );
    }

    #[test]
    fn journey_live_marks_actions_as_effectful_server_side() {
        let out = render_journey(&journey_report(false), Format::Md);
        assert!(
            out.contains("| UpvotePost | effectful (server-side) | Db | LoginPage |"),
            "{out}"
        );
    }

    #[test]
    fn journey_svg_splits_effectful_and_pure_sections() {
        // Two non-navigating actions: one effectful (Db), one pure — the SVG must
        // carry both labelled sections, with the effect family on the chip.
        let r = JourneyReport {
            project: "x".into(),
            is_spa: true,
            shape: AppShape::Spa,
            target: Some("web:app".into()),
            pages: vec![JourneyPage { name: "HomePage".into(), url: Some("/".into()) }],
            page_field: Some("page".into()),
            actions: vec![
                JourneyAction {
                    msg: "AddToBasket".into(),
                    server: Some(true),
                    navigates_to: vec![],
                    dynamic_nav: false,
                    effectful: true,
                    effect_families: vec!["Db".into()],
                },
                JourneyAction {
                    msg: "ToggleMenu".into(),
                    server: Some(false),
                    navigates_to: vec![],
                    dynamic_nav: false,
                    effectful: false,
                    effect_families: vec![],
                },
            ],
            classified: true,
            notes: vec![],
        };
        let svg = render_journey(&r, Format::Svg);
        assert!(svg.contains("Effectful actions (1)"), "effectful section: {svg}");
        assert!(svg.contains("Pure actions (1)"), "pure section: {svg}");
        assert!(svg.contains(">AddToBasket · Db<"), "effect family on chip: {svg}");
        assert!(svg.contains(">ToggleMenu<"), "pure chip: {svg}");
    }

    #[test]
    fn journey_empty_renders_a_placeholder_not_an_error() {
        let r = JourneyReport {
            project: "x".into(),
            is_spa: false,
            shape: AppShape::Live,
            target: None,
            pages: vec![],
            page_field: None,
            actions: vec![],
            classified: false,
            notes: vec!["nothing found".into()],
        };
        let puml = render_journey(&r, Format::Puml);
        assert!(puml.contains("No pages found"), "{puml}");
        assert!(
            puml.starts_with("@startuml") && puml.trim_end().ends_with("@enduml"),
            "{puml}"
        );
        let svg = render_journey(&r, Format::Svg);
        assert!(is_svg(&svg) && svg.contains("No pages found"), "{svg}");
        let md = render_journey(&r, Format::Md);
        assert!(md.contains("nothing found"), "{md}");
    }

    fn flow_report(grounded: bool) -> FlowReport {
        let journey = journey_report(true);
        let mut page_actions: HashMap<String, Vec<String>> = HashMap::new();
        let mut continuations: HashMap<String, Vec<String>> = HashMap::new();
        if grounded {
            // HomePage's view can dispatch Refresh; LoginPage's view UpvotePost.
            page_actions.insert("HomePage".into(), vec!["Refresh".into()]);
            page_actions.insert("LoginPage".into(), vec!["UpvotePost".into()]);
        }
        // Refresh fires a Cmd whose result dispatches Loaded.
        continuations.insert("Refresh".into(), vec!["Loaded".into()]);
        FlowReport {
            journey,
            page_actions,
            continuations,
            grounded,
            classifications: HashMap::new(),
            action_externals: HashMap::new(),
            external_systems: Vec::new(),
            self_ref_hosts: Vec::new(),
            data_store: None,
            chrome_actions: Vec::new(),
        }
    }

    #[test]
    fn flow_md_attributes_actions_per_page_when_grounded() {
        let r = flow_report(true);
        let md = render_flow_md(&r);
        // Grounded: HomePage lists ONLY Refresh (with its continuation), not
        // UpvotePost — which is attributed to LoginPage.
        let home = md.split("## LoginPage").next().unwrap();
        assert!(home.contains("`Refresh`"), "home section:\n{home}");
        assert!(
            home.contains("⇢ _Loaded_"),
            "continuation edge missing:\n{home}"
        );
        assert!(
            !home.contains("`UpvotePost`"),
            "UpvotePost must not appear on HomePage when grounded:\n{home}"
        );
        // The wrong journey inventory note is suppressed when grounded.
        assert!(
            !md.contains("per-page attribution is best-effort"),
            "stale journey note leaked:\n{md}"
        );
    }

    #[test]
    fn flow_md_lists_actions_once_when_not_grounded() {
        let r = flow_report(false);
        let md = render_flow_md(&r);
        // Not grounded: a single "## Pages" list + a single "## Actions" section —
        // NOT actions repeated under every page. Each action appears exactly once.
        assert!(md.contains("## Pages"), "{md}");
        assert!(md.contains("## Actions"), "{md}");
        assert_eq!(md.matches("`UpvotePost`").count(), 1, "action must appear once:\n{md}");
        assert!(md.contains("does not dispatch on the page field"), "{md}");
    }

    #[test]
    fn flow_md_overlays_classification_external_and_store() {
        let mut r = flow_report(true);
        r.data_store = Some("PostgreSQL".into());
        r.external_systems = vec![ExternalSystem {
            host: "api.stripe.com".into(),
            purpose: "Payments (Stripe)".into(),
            role: ExtRole::Payments,
        }];
        r.classifications.insert(
            "UpvotePost".into(),
            Classification { confidential: true, reasons: vec!["Auth session / token".into()] },
        );
        let md = render_flow_md(&r);
        assert!(md.contains("**Data store:** PostgreSQL"), "store overlay:\n{md}");
        assert!(md.contains("api.stripe.com") && md.contains("Payments (Stripe)"), "external overlay:\n{md}");
        assert!(md.contains("🔒 **CONFIDENTIAL** (Auth session / token)"), "classification overlay:\n{md}");
        // Title carries the generated-on date stamp.
        assert!(md.contains("· generated 20"), "date stamp missing:\n{md}");
    }

    #[test]
    fn flow_svg_is_a_swimlane_dfd() {
        let mut r = flow_report(true);
        r.data_store = Some("PostgreSQL".into());
        r.external_systems = vec![ExternalSystem {
            host: "api.stripe.com".into(),
            purpose: "Payments (Stripe)".into(),
            role: ExtRole::Payments,
        }];
        let svg = render_flow_svg(&r);
        assert!(svg.starts_with("<svg") && svg.trim_end().ends_with("</svg>"), "{svg}");
        for lane in ["Browser client", "Server (/_rpc)", "Data store", "External systems"] {
            assert!(svg.contains(lane), "lane `{lane}` missing:\n{svg}");
        }
        assert!(svg.contains("api.stripe.com") && svg.contains("PostgreSQL"), "nodes missing:\n{svg}");
        assert!(svg.contains("trust boundary"), "boundary missing:\n{svg}");
    }

    #[test]
    fn host_role_splits_subprocessor_from_embed() {
        // Data flows → sub-processors; content/CDN → embedded, NOT a sub-processor.
        assert_eq!(host_role("api.stripe.com"), ExtRole::Payments);
        assert_eq!(host_role("github.com"), ExtRole::OAuth);
        assert_eq!(host_role("api.sendgrid.com"), ExtRole::Email);
        assert_eq!(host_role("api.example.org"), ExtRole::Api); // unknown → API (kept)
        assert!(host_role("api.stripe.com").is_subprocessor());
        assert!(host_role("www.youtube.com").is_subprocessor() == false); // embed
        assert!(host_role("anzellai.github.io").is_subprocessor() == false); // pages host
        assert!(host_role("fonts.googleapis.com").is_subprocessor() == false);
    }

    #[test]
    fn is_self_ref_matches_domain_and_subdomains() {
        let own = vec!["sky-lang.org".to_string()];
        assert!(is_self_ref("sky-lang.org", &own));
        assert!(is_self_ref("www.sky-lang.org", &own));
        assert!(!is_self_ref("github.com", &own));
        assert!(!is_self_ref("notsky-lang.org", &own)); // not a subdomain
    }

    #[test]
    fn subprocessors_section_splits_and_notes_selfref() {
        let mut r = flow_report(true);
        r.external_systems = vec![
            ExternalSystem { host: "api.stripe.com".into(), purpose: "Payments (Stripe)".into(), role: ExtRole::Payments },
            ExternalSystem { host: "www.youtube.com".into(), purpose: "External service (www.youtube.com)".into(), role: ExtRole::Cdn },
        ];
        r.self_ref_hosts = vec!["sky-lang.org".into()];
        let md = subprocessors_section(&r);
        assert!(md.contains("data sub-processors") && md.contains("api.stripe.com"), "subproc table:\n{md}");
        assert!(md.contains("Embedded third-party content") && md.contains("www.youtube.com"), "embed table:\n{md}");
        // The embed host must not appear in the sub-processor table region.
        let subproc_region = &md[..md.find("Embedded").unwrap_or(md.len())];
        assert!(!subproc_region.contains("youtube"), "youtube leaked into sub-processors:\n{md}");
        assert!(md.contains("Excluded as the app's own domain") && md.contains("sky-lang.org"), "self-ref note:\n{md}");
    }

    #[test]
    fn filter_self_ref_moves_own_host_out() {
        let mut r = flow_report(true);
        r.external_systems = vec![
            ExternalSystem { host: "github.com".into(), purpose: "OAuth / API (GitHub)".into(), role: ExtRole::OAuth },
            ExternalSystem { host: "sky-lang.org".into(), purpose: "External service".into(), role: ExtRole::Api },
        ];
        let dir = std::env::temp_dir().join(format!("sky-selfref-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("CNAME"), "sky-lang.org\n").unwrap();
        filter_self_ref(&mut r, &dir);
        let _ = std::fs::remove_dir_all(&dir);
        assert!(r.external_systems.iter().any(|e| e.host == "github.com"), "github kept");
        assert!(!r.external_systems.iter().any(|e| e.host == "sky-lang.org"), "own host removed");
        assert!(r.self_ref_hosts.contains(&"sky-lang.org".to_string()), "own host recorded");
    }

    #[test]
    fn overlay_helpers_classify_and_name() {
        assert!(is_secret_ty("Secret") && is_secret_ty("Sky.Core.Secret.Secret"));
        assert!(!is_secret_ty("String"));
        assert!(pii_reason("customerEmail").is_some() && pii_reason("cardNumber").is_some());
        assert!(pii_reason("count").is_none());
        assert_eq!(url_host("https://api.stripe.com/v1/charges").as_deref(), Some("api.stripe.com"));
        assert_eq!(url_host("not a url"), None);
        assert_eq!(host_purpose("api.stripe.com"), "Payments (Stripe)");
    }

    #[test]
    fn flow_puml_is_a_state_machine_with_a_navigation_edge() {
        let r = flow_report(true);
        let puml = render_flow_puml(&r);
        assert!(
            puml.starts_with("@startuml") && puml.trim_end().ends_with("@enduml"),
            "{puml}"
        );
        // UpvotePost navigates HomePage/LoginPage -> LoginPage (a real transition).
        assert!(puml.contains("UpvotePost"), "{puml}");
        assert!(puml.contains(&page_node_id("LoginPage")), "{puml}");
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
                    Expr::Call(callee, args) => {
                        match http_def_name(&db, body, *callee).as_deref() {
                            Some("get") => direct.push((
                                "GET".into(),
                                args.first().and_then(|a| url_hint(body, *a)),
                            )),
                            Some("post") => direct.push((
                                "POST".into(),
                                args.first().and_then(|a| url_hint(body, *a)),
                            )),
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
                        }
                    }
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
                calls.insert(OutboundHttp {
                    method,
                    url,
                    module: mname.clone(),
                });
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
