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
}
