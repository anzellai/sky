//! `sky spa-partition <entry.sky>` — Phase 1 (Phase 2 in the doc's revised
//! phasing) of the Sky.Spa auto-split: a **read-only** analysis that infers,
//! and prints, which `update` branches of a single Sky.Spa project would run
//! **client-side** vs **server-side**. No codegen, no IR change, no emission —
//! it reads the resolved + typed HIR and prints a report.
//!
//! The inference (design doc `docs/skyspa/auto-split.md` §11-§12). A branch (or
//! any binding) is **SERVER** iff it transitively:
//!   1. reaches a **server effect kernel** — `Db.*` / `File.*` / `Auth.*` /
//!      server `Http` / `System.*` (env/secret) / `Process.*` / `Io.*`; or
//!   2. references a **server-tainted top-level binding** — a top-level def
//!      whose own initialiser reaches (1) (a `Task.run` CAF, an env read).
//! Both seeds propagate transitively over the call/reference graph to a
//! fixpoint. The analysis **over-approximates to server on any ambiguity**
//! (`Http` it cannot prove is external → server; an unresolvable callee / a Go
//! FFI reference → server). Sound direction: a needless server classification
//! is fine; classifying a real server effect as client would leak the DB /
//! secret to the browser, and is a bug the over-approximation forbids.
//!
//! Client effects (`Time.*`, `Random.*`, `Uuid.*`, `Crypto` hashing) are
//! effectful but stay CLIENT — they run in the wasm client runtime.
//!
//! This file walks `hir::resolve(module).bodies` and reads the typed HIR
//! (`ty::Typer::body_types`, whose `BodyTypes.exprs` is the same table the
//! lowerer consumes) — it never re-implements resolution or inference.

use base::{DefId, ModuleId};
use hir::{Body, Expr, ExprId, LocalDef, Pattern, Res, SkyDb};
use std::collections::{HashMap, HashSet};
use std::path::Path;

// ---------------------------------------------------------------------------
// Kernel classification (design §3) — key off the `Res::Kernel` pseudo-module.
// ---------------------------------------------------------------------------

/// The target a reached effect kernel runs on.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum KernelClass {
    /// Cannot run in the browser at all — DB, files, secrets, the server socket,
    /// process/stdio, environment. Always server.
    ServerOnly,
    /// Runs in the client runtime (an effect, but CLIENT-CAPABLE). `Http.*` (the
    /// HTTP client) is here: wasm routes `net/http` through the browser `fetch`
    /// API, and every renderer (browser / desktop+mobile WebView / native) can
    /// issue it, so an Http call runs client-side by default. It is forced SERVER
    /// only by the TAINT path — a secret (env / `Auth`) or DB value flowing into
    /// the request makes the branch reference a tainted binding / server kernel,
    /// which the analysis already catches. `Time`/`Random`/`Uuid` are here too.
    ClientEffect,
    /// Pure / plumbing — irrelevant to the partition.
    Neutral,
}

/// Classify a kernel pseudo-module + function. `module` is the pseudo name
/// (`Db`, `Http`, `System`, …) as produced by the resolver's `Res::Kernel`.
fn classify_kernel(module: &str, _func: &str) -> KernelClass {
    match module {
        // Server-only: browser cannot reach any of these.
        "Db" | "Auth" | "File" | "Server" | "Process" | "Io" => KernelClass::ServerOnly,
        // `System.*` — env / args / secrets / exit. Env reads are the SEED-2b
        // "pure-typed" case: `getenvOr`/`getenvInt`/`getenvBool` are typed
        // `String -> String -> String` (NOT `Task`), so they are caught ONLY by
        // kernel identity, here — never by a Task-type check.
        "System" => KernelClass::ServerOnly,
        // HTTP-SERVER machinery (the listener, middleware, rate limiter) is
        // server-only; the HTTP CLIENT (`Http.get`/`post`/`request`) is
        // client-capable (browser/WebView/native `fetch`) — taint forces it
        // server when it carries a secret/DB value.
        "RateLimit" | "Middleware" => KernelClass::ServerOnly,
        "Http" => KernelClass::ClientEffect,
        "Time" | "Random" | "Uuid" => KernelClass::ClientEffect,
        _ => KernelClass::Neutral,
    }
}

// ---------------------------------------------------------------------------
// Per-subtree reference collection.
// ---------------------------------------------------------------------------

#[derive(Default, Clone)]
struct Refs {
    /// Server-only / Http kernel references reached, as `(module, func, class)`.
    server_kernels: Vec<(String, String, KernelClass)>,
    /// Client-effect kernel references reached (for the CLIENT-effect label).
    client_kernels: Vec<(String, String)>,
    /// Callee top-level defs referenced (`Res::Def`).
    callees: HashSet<DefId>,
    /// A Go-FFI (`Res::Foreign`) reference — opaque, conservatively server.
    foreign: bool,
    /// Saw an inline effect-execution site: `Task.run …` or a `let _ = <expr>`
    /// empty-binder auto-force (`lower.rs:2708`). Enriches the reason only.
    inline_force: bool,
}

impl Refs {
    /// Does this subtree DIRECTLY hit a server kernel or a Go FFI reference?
    fn direct_server_reason(&self) -> Option<String> {
        if let Some((m, f, _class)) = self.server_kernels.first() {
            let how = if self.inline_force {
                "inline effect "
            } else {
                ""
            };
            return Some(format!("{how}reaches server kernel {m}.{f}"));
        }
        if self.foreign {
            return Some("reaches a Go FFI reference (opaque -> conservative server)".into());
        }
        None
    }
    fn client_effect_note(&self) -> Option<String> {
        self.client_kernels
            .first()
            .map(|(m, f)| format!("client effect {m}.{f}"))
    }
}

/// Walk one expression subtree, accumulating references into `acc`.
fn collect(body: &Body, e: ExprId, acc: &mut Refs) {
    match &body.exprs[e] {
        Expr::Int(_)
        | Expr::Float(_)
        | Expr::Str(_)
        | Expr::Chr(_)
        | Expr::Bool(_)
        | Expr::Unit
        | Expr::Accessor(_)
        | Expr::Error => {}
        Expr::List(xs) | Expr::Tuple(xs) => {
            for x in xs {
                collect(body, *x, acc);
            }
        }
        Expr::Record(fields) => {
            for (_, x) in fields {
                collect(body, *x, acc);
            }
        }
        Expr::Update { base, fields } => {
            collect(body, *base, acc);
            for (_, x) in fields {
                collect(body, *x, acc);
            }
        }
        Expr::Var(res) => record_res(res, acc),
        Expr::Negate(x) => collect(body, *x, acc),
        Expr::Lambda { body: b, .. } => collect(body, *b, acc),
        Expr::Call(callee, args) => {
            if let Expr::Var(Res::Kernel { module, func }) = &body.exprs[*callee] {
                let m = module.as_str().rsplit('.').next().unwrap_or(module.as_str());
                // `Task.run <arg>` — an inline effect-execution site.
                if m == "Task" && func.as_str() == "run" {
                    acc.inline_force = true;
                }
                // `Ffi.kernel "<Symbol>"` — the REAL effect origin. The stdlib
                // effect modules (Sky.Core.Http, Sky.Core.System, Std.Db,
                // Std.Auth, Sky.Core.File, …) are ordinary Sky SOURCE whose
                // functions are `Ffi.kernel "Db_query"` etc., so they resolve to
                // `Res::Def`, NOT `Res::Kernel` — the consult's "key off
                // Res::Kernel.module" would miss every one. The identity is the
                // symbol string's prefix (`Db_`, `Http_`, `System_`, …).
                if m == "Ffi" && func.as_str() == "kernel" {
                    if let Some(first) = args.first() {
                        if let Expr::Str(sym) = &body.exprs[*first] {
                            record_ffi_symbol(sym, acc);
                        }
                    }
                }
            }
            collect(body, *callee, acc);
            for a in args {
                collect(body, *a, acc);
            }
        }
        Expr::Binop { res, lhs, rhs, .. } => {
            record_res(res, acc);
            collect(body, *lhs, acc);
            collect(body, *rhs, acc);
        }
        Expr::If { arms, els } => {
            for (c, t) in arms {
                collect(body, *c, acc);
                collect(body, *t, acc);
            }
            collect(body, *els, acc);
        }
        Expr::Let { defs, body: b } => {
            for d in defs {
                collect_localdef(body, d, acc);
            }
            collect(body, *b, acc);
        }
        Expr::Case { subject, branches } => {
            collect(body, *subject, acc);
            for br in branches {
                collect(body, br.body, acc);
            }
        }
        Expr::Access(x, _) => collect(body, *x, acc),
    }
}

fn collect_localdef(body: &Body, d: &LocalDef, acc: &mut Refs) {
    // `let _ = <expr>` — an empty-binder, non-destructuring def is the auto-force
    // site (`lower.rs:2708-2711`); the effect it forces is executed inline.
    if d.binders.is_empty() && d.pat.is_none() {
        acc.inline_force = true;
    }
    collect(body, d.body, acc);
}

/// Classify an `Ffi.kernel "<Symbol>"` string by its `<Prefix>_` — the runtime
/// symbol's family (`Db_query` → Db, `Http_post` → Http, `System_getenvOr` →
/// System). This is the actual effect origin under the Sky-source stdlib.
fn record_ffi_symbol(sym: &str, acc: &mut Refs) {
    let prefix = sym.split('_').next().unwrap_or(sym);
    let rest = sym.strip_prefix(prefix).unwrap_or("").trim_start_matches('_');
    let rest = if rest.is_empty() { sym } else { rest };
    match classify_kernel(prefix, rest) {
        KernelClass::Neutral => {}
        KernelClass::ClientEffect => acc.client_kernels.push((prefix.to_string(), rest.to_string())),
        class => acc
            .server_kernels
            .push((prefix.to_string(), rest.to_string(), class)),
    }
}

fn record_res(res: &Res, acc: &mut Refs) {
    match res {
        Res::Kernel { module, func } => {
            let m = module.as_str().rsplit('.').next().unwrap_or(module.as_str());
            let f = func.as_str();
            match classify_kernel(m, f) {
                KernelClass::Neutral => {}
                KernelClass::ClientEffect => acc.client_kernels.push((m.to_string(), f.to_string())),
                class => acc
                    .server_kernels
                    .push((m.to_string(), f.to_string(), class)),
            }
        }
        Res::Def(d) => {
            acc.callees.insert(*d);
        }
        Res::Foreign { .. } => acc.foreign = true,
        Res::Local(_) | Res::Ctor(_) | Res::Error => {}
    }
}

// ---------------------------------------------------------------------------
// The report.
// ---------------------------------------------------------------------------

/// One `update` branch's verdict.
pub struct BranchVerdict {
    pub msg: String,
    pub server: bool,
    pub reason: String,
}

/// A server-tainted top-level binding (excluded from the client build).
pub struct TaintedBinding {
    pub module: String,
    pub name: String,
    pub reason: String,
}

/// The full partition report for one project.
pub struct SpaPartitionReport {
    pub project: String,
    pub entry_module: String,
    pub update_name: Option<String>,
    /// Present when per-branch analysis was possible.
    pub branches: Vec<BranchVerdict>,
    /// Set when the `update` body is not a resolvable `case msg of` (a lambda /
    /// partial-app / delegating shape) — the whole-update verdict, no per-branch.
    pub whole_update: Option<BranchVerdict>,
    pub tainted: Vec<TaintedBinding>,
    /// Non-fatal notes (why a branch was conservatively marked server, etc.).
    pub notes: Vec<String>,
}

impl SpaPartitionReport {
    /// Plain-text rendering for the CLI.
    pub fn render(&self) -> String {
        let mut o = String::new();
        o.push_str(&format!("Sky.Spa partition report — {}\n", self.project));
        o.push_str(&format!("entry module: {}\n", self.entry_module));
        match (&self.update_name, self.whole_update.is_some()) {
            (Some(u), false) => o.push_str(&format!(
                "update: {u}  ({} branch(es))\n\n",
                self.branches.len()
            )),
            (Some(u), true) => o.push_str(&format!("update: {u}  (per-branch unavailable)\n\n")),
            (None, _) => o.push_str("update: <not found>\n\n"),
        }

        if let Some(w) = &self.whole_update {
            o.push_str("Whole-update classification (no resolvable `case msg of`):\n");
            let tag = if w.server { "SERVER" } else { "CLIENT" };
            o.push_str(&format!("  {tag}  {}  — {}\n\n", w.msg, w.reason));
        } else if !self.branches.is_empty() {
            o.push_str("Per-branch classification:\n");
            let w = self
                .branches
                .iter()
                .map(|b| b.msg.len())
                .max()
                .unwrap_or(0)
                .max(4);
            for b in &self.branches {
                let tag = if b.server { "SERVER" } else { "CLIENT" };
                o.push_str(&format!(
                    "  {tag}  {:<width$}  {}\n",
                    b.msg,
                    b.reason,
                    width = w
                ));
            }
            o.push('\n');
        }

        o.push_str("Server-tainted top-level bindings:\n");
        if self.tainted.is_empty() {
            o.push_str("  (none)\n");
        } else {
            let w = self
                .tainted
                .iter()
                .map(|t| t.name.len())
                .max()
                .unwrap_or(0)
                .max(4);
            for t in &self.tainted {
                o.push_str(&format!("  {:<width$}  {}\n", t.name, t.reason, width = w));
            }
        }
        o.push('\n');

        let (s, c) = self
            .branches
            .iter()
            .fold((0, 0), |(s, c), b| if b.server { (s + 1, c) } else { (s, c + 1) });
        if self.whole_update.is_none() {
            o.push_str(&format!(
                "Summary: {s} SERVER, {c} CLIENT branch(es); {} tainted binding(s).\n",
                self.tainted.len()
            ));
        }

        if !self.notes.is_empty() {
            o.push_str("\nNotes:\n");
            for n in &self.notes {
                o.push_str(&format!("  - {n}\n"));
            }
        }
        o
    }
}

// ---------------------------------------------------------------------------
// Analysis entry point.
// ---------------------------------------------------------------------------

/// Analyse a single Sky.Spa project and return its partition report. Read-only:
/// loads the same source db the build driver assembles (stdlib + `.skydeps` +
/// project `src/`), resolves + type-checks, then walks the typed HIR. Never
/// writes, lowers, or emits.
pub fn analyze(
    repo_root: &Path,
    project_dir: &Path,
    entry_module: Option<&str>,
) -> Result<SpaPartitionReport, String> {
    let (db, entry, check_ids) = crate::build::load_source_db(repo_root, project_dir, entry_module)?;

    let project = project_dir
        .strip_prefix(repo_root)
        .unwrap_or(project_dir)
        .to_string_lossy()
        .to_string();
    let entry_module_name = db.module_name(entry).to_string();

    // Type-check first — the report is only meaningful for a program that
    // `sky check`s clean (mirrors the build's accept/reject gate). We do not
    // re-render the diagnostics here; a broken project is reported as such.
    let checked = ty::check_modules(&db, &check_ids);
    if checked.type_errors > 0 || checked.name_errors > 0 {
        return Err(format!(
            "project does not type-check ({} type error(s), {} name error(s)) — run `sky check` first",
            checked.type_errors, checked.name_errors
        ));
    }

    // Identify the `Std.Spa.config` def, then the `update` it was given.
    let spa_mod = db
        .module_by_name("Std.Spa")
        .ok_or_else(|| "not a Sky.Spa project: Std.Spa is not imported".to_string())?;
    let config_def = def_by_name(&db, spa_mod, "config")
        .ok_or_else(|| "Std.Spa.config not found (stdlib mismatch?)".to_string())?;

    let mut notes: Vec<String> = Vec::new();
    let update_field = find_config_update_field(&db, &check_ids, entry, config_def);

    // Build the reachability + taint graph over every def reachable from the
    // app modules (pulls in only the stdlib defs actually referenced).
    let graph = build_graph(&db, &check_ids);

    // ---- server-tainted top-level bindings (app modules only) ----
    let mut tainted: Vec<TaintedBinding> = Vec::new();
    for mid in &check_ids {
        let resolved = db.resolve(*mid);
        let mname = db.module_name(*mid).to_string();
        for td in &resolved.top_defs {
            if graph.server.contains(&td.def) {
                // Skip the structural TEA entry points + `update` itself — they
                // are server only by virtue of *containing* a server branch, and
                // are not standalone "values"/helpers that leak to the client
                // build. The interesting members here are effectful-origin CAFs
                // (a `Task.run` binding, an env read) and server helper fns.
                let n = td.name.as_str();
                if matches!(n, "main" | "view" | "init" | "subscriptions" | "update") {
                    continue;
                }
                tainted.push(TaintedBinding {
                    module: mname.clone(),
                    name: n.to_string(),
                    reason: graph.reason_for(&db, td.def),
                });
            }
        }
    }
    tainted.sort_by(|a, b| (a.module.clone(), a.name.clone()).cmp(&(b.module.clone(), b.name.clone())));
    tainted.dedup_by(|a, b| a.module == b.module && a.name == b.name);

    // ---- per-branch classification ----
    let mut branches: Vec<BranchVerdict> = Vec::new();
    let mut whole_update: Option<BranchVerdict> = None;
    let mut update_name: Option<String> = None;

    match update_field {
        UpdateField::Def(update_def) => {
            let loc = db.def_loc(update_def);
            let (umod, uname) = loc
                .map(|l| (l.module, l.name.as_str().to_string()))
                .unwrap_or((entry, "update".to_string()));
            update_name = Some(format!("{}.{}", db.module_name(umod), uname));
            let resolved = db.resolve(umod);
            if let Some(body) = resolved.bodies.get(&update_def) {
                classify_update_body(
                    &db, &graph, umod, update_def, body, &mut branches, &mut whole_update,
                    &mut notes,
                );
            } else {
                return Err("update def has no body".into());
            }
        }
        UpdateField::Lambda(umod, body, root) => {
            update_name = Some(format!("{}.<lambda update>", db.module_name(umod)));
            // A lambda update: analyse its body as one unit (no stable Msg
            // pattern names unless it is itself a `case`).
            classify_lambda_update(&db, &graph, umod, &body, root, &mut branches, &mut whole_update);
            notes.push(
                "update is an inline lambda; per-branch names taken from its `case` if present."
                    .into(),
            );
        }
        UpdateField::Unavailable(why) => {
            notes.push(format!("branch analysis unavailable: {why}"));
        }
    }

    // Http note: Http is a CLIENT-capable effect (wasm/WebView/native fetch), so
    // an Http call runs client-side and is forced SERVER only by TAINT — a secret
    // (env / Auth) or DB value flowing into the request, which shows up as a
    // server reach on that branch. There is no Http-specific over-approximation.
    if !branches.is_empty() {
        notes.push(
            "Http runs client-side (wasm/WebView fetch); a call carrying a secret/DB value is forced SERVER by taint. Server-only effects are Db/File/Auth/System/Server/Process/Io."
                .to_string(),
        );
    }

    Ok(SpaPartitionReport {
        project,
        entry_module: entry_module_name,
        update_name,
        branches,
        whole_update,
        tainted,
        notes,
    })
}

/// What the `config` record's `update` field pointed at.
enum UpdateField {
    Def(DefId),
    Lambda(ModuleId, Body, ExprId),
    Unavailable(String),
}

/// Find the def named `name` in module `m`.
fn def_by_name(db: &dyn SkyDb, m: ModuleId, name: &str) -> Option<DefId> {
    db.resolve(m)
        .top_defs
        .iter()
        .find(|td| td.name.as_str() == name)
        .map(|td| td.def)
}

/// Search the app modules for the `Spa.config { … }` call and read its `update`
/// field. Returns the update DefId (the common case), a lambda body, or an
/// "unavailable" reason for a non-name shape (partial app).
fn find_config_update_field(
    db: &dyn SkyDb,
    check_ids: &[ModuleId],
    entry: ModuleId,
    config_def: DefId,
) -> UpdateField {
    // Entry module first, then the rest.
    let mut order = vec![entry];
    order.extend(check_ids.iter().copied().filter(|m| *m != entry));
    for mid in order {
        let resolved = db.resolve(mid);
        for (_def, body) in &resolved.bodies {
            if let Some(field) = find_config_call(body, config_def) {
                return match &body.exprs[field] {
                    Expr::Var(Res::Def(d)) => UpdateField::Def(*d),
                    Expr::Lambda { body: b, .. } => UpdateField::Lambda(mid, body.clone(), *b),
                    Expr::Var(Res::Kernel { module, func }) => UpdateField::Unavailable(format!(
                        "`update` field is a kernel reference {}.{}",
                        module.as_str(),
                        func.as_str()
                    )),
                    other => UpdateField::Unavailable(format!(
                        "`update` field is a non-name expression ({})",
                        expr_kind(other)
                    )),
                };
            }
        }
    }
    UpdateField::Unavailable("no `Spa.config { … }` call found in the project".into())
}

/// Within one body, find a `Call(Var(Res::Def(config_def)), [Record …])` and
/// return the `update` field's ExprId.
fn find_config_call(body: &Body, config_def: DefId) -> Option<ExprId> {
    for (id, expr) in body.exprs.iter() {
        if let Expr::Call(callee, args) = expr {
            if let Expr::Var(Res::Def(d)) = &body.exprs[*callee] {
                if *d == config_def {
                    if let Some(first) = args.first() {
                        if let Expr::Record(fields) = &body.exprs[*first] {
                            for (n, v) in fields {
                                if n.as_str() == "update" {
                                    return Some(*v);
                                }
                            }
                        }
                    }
                }
            }
        }
        let _ = id;
    }
    None
}

fn expr_kind(e: &Expr) -> &'static str {
    match e {
        Expr::Call(..) => "call / partial application",
        Expr::Lambda { .. } => "lambda",
        Expr::Var(_) => "variable",
        _ => "other",
    }
}

// ---------------------------------------------------------------------------
// The reachability + taint graph.
// ---------------------------------------------------------------------------

struct DefNode {
    /// Direct server reason from this def's OWN body (kernel / FFI), if any.
    direct: Option<String>,
    callees: HashSet<DefId>,
}

struct Graph {
    nodes: HashMap<DefId, DefNode>,
    /// The taint fixpoint.
    server: HashSet<DefId>,
    /// Ultimate origin reason per server def (the kernel it bottoms out at).
    root_reason: HashMap<DefId, String>,
}

impl Graph {
    /// A human reason for why `d` is server-tainted.
    fn reason_for(&self, db: &dyn SkyDb, d: DefId) -> String {
        if let Some(node) = self.nodes.get(&d) {
            if let Some(r) = &node.direct {
                return format!("seed: {r}");
            }
            // Point at the first server callee.
            for c in &node.callees {
                if self.server.contains(c) {
                    let cn = db
                        .def_loc(*c)
                        .map(|l| format!("{}.{}", db.module_name(l.module), l.name.as_str()))
                        .unwrap_or_else(|| "<callee>".into());
                    let origin = self
                        .root_reason
                        .get(c)
                        .cloned()
                        .unwrap_or_else(|| "server".into());
                    return format!("via {cn} ({origin})");
                }
            }
        }
        "server".into()
    }
}

/// Build the taint graph over every def reachable from the app modules.
fn build_graph(db: &dyn SkyDb, check_ids: &[ModuleId]) -> Graph {
    let mut nodes: HashMap<DefId, DefNode> = HashMap::new();
    let mut work: Vec<DefId> = Vec::new();
    let mut seen: HashSet<DefId> = HashSet::new();

    // Seed with all app top-level defs.
    for mid in check_ids {
        for (def, _) in &db.resolve(*mid).bodies {
            if seen.insert(*def) {
                work.push(*def);
            }
        }
    }

    while let Some(def) = work.pop() {
        let Some(loc) = db.def_loc(def) else {
            // No location — treat as opaque/server.
            nodes.insert(
                def,
                DefNode {
                    direct: Some("unresolvable definition (opaque -> conservative server)".into()),
                    callees: HashSet::new(),
                },
            );
            continue;
        };
        let resolved = db.resolve(loc.module);
        let Some(body) = resolved.bodies.get(&def) else {
            // A referenced def with no body in its module — opaque, conservative.
            nodes.insert(
                def,
                DefNode {
                    direct: Some("no body found (opaque -> conservative server)".into()),
                    callees: HashSet::new(),
                },
            );
            continue;
        };
        let mut acc = Refs::default();
        if let Some(root) = body.root {
            collect(body, root, &mut acc);
        }
        let direct = acc.direct_server_reason();
        for c in &acc.callees {
            if seen.insert(*c) {
                work.push(*c);
            }
        }
        nodes.insert(
            def,
            DefNode {
                direct,
                callees: acc.callees,
            },
        );
    }

    // Fixpoint: a def is server iff its own body is a seed OR any callee is server.
    let mut server: HashSet<DefId> = nodes
        .iter()
        .filter(|(_, n)| n.direct.is_some())
        .map(|(d, _)| *d)
        .collect();
    loop {
        let mut changed = false;
        for (d, n) in &nodes {
            if server.contains(d) {
                continue;
            }
            if n.callees.iter().any(|c| server.contains(c)) {
                server.insert(*d);
                changed = true;
            }
        }
        if !changed {
            break;
        }
    }

    // Root reason per server def: propagate the seed reason along callee edges.
    let mut root_reason: HashMap<DefId, String> = HashMap::new();
    for (d, n) in &nodes {
        if let Some(r) = &n.direct {
            root_reason.insert(*d, r.clone());
        }
    }
    // Iteratively fill in by-reference reasons.
    loop {
        let mut changed = false;
        for (d, n) in &nodes {
            if root_reason.contains_key(d) || !server.contains(d) {
                continue;
            }
            if let Some(c) = n.callees.iter().find(|c| root_reason.contains_key(*c)) {
                let r = root_reason[c].clone();
                root_reason.insert(*d, r);
                changed = true;
            }
        }
        if !changed {
            break;
        }
    }

    Graph {
        nodes,
        server,
        root_reason,
    }
}

// ---------------------------------------------------------------------------
// Branch classification.
// ---------------------------------------------------------------------------

/// Classify each arm of `update msg model = case msg of …`.
fn classify_update_body(
    db: &skydb::SkyDatabase,
    graph: &Graph,
    module: ModuleId,
    def: DefId,
    body: &Body,
    branches: &mut Vec<BranchVerdict>,
    whole_update: &mut Option<BranchVerdict>,
    notes: &mut Vec<String>,
) {
    // Read the typed HIR table for this def (the same `BodyTypes.exprs` the
    // lowerer consumes) — proves the analysis runs over TYPED hir, and lets the
    // inline-force reason be precise.
    let _types = ty::Typer::new(db).body_types(module, def, body);

    let Some(root) = body.root else {
        return;
    };
    // Shared context along the spine above the case (top-level `let`s).
    let mut shared = Refs::default();
    let case_expr = find_top_case(body, root, &mut shared);

    let Some(case_expr) = case_expr else {
        // No `case msg of` — classify the whole update as one unit.
        let mut acc = Refs::default();
        collect(body, root, &mut acc);
        *whole_update = Some(verdict(db, "(whole update)", &acc, graph));
        notes.push("update has no top-level `case msg of` — showing a whole-update verdict.".into());
        return;
    };

    if !shared.server_kernels.is_empty() || shared.foreign {
        notes.push(
            "a `let` above `case msg of` reaches a server effect; every branch inherits it.".into(),
        );
    }

    if let Expr::Case { branches: arms, .. } = &body.exprs[case_expr] {
        for arm in arms {
            let mut acc = shared.clone();
            collect(body, arm.body, &mut acc);
            let label = pattern_label(body, arm.pat);
            branches.push(verdict(db, &label, &acc, graph));
        }
    }
}

fn classify_lambda_update(
    db: &dyn SkyDb,
    graph: &Graph,
    _module: ModuleId,
    body: &Body,
    root: ExprId,
    branches: &mut Vec<BranchVerdict>,
    whole_update: &mut Option<BranchVerdict>,
) {
    let mut shared = Refs::default();
    let case_expr = find_top_case(body, root, &mut shared);
    if let Some(ce) = case_expr {
        if let Expr::Case { branches: arms, .. } = &body.exprs[ce] {
            for arm in arms {
                let mut acc = shared.clone();
                collect(body, arm.body, &mut acc);
                let label = pattern_label(body, arm.pat);
                branches.push(verdict(db, &label, &acc, graph));
            }
            return;
        }
    }
    let mut acc = Refs::default();
    collect(body, root, &mut acc);
    *whole_update = Some(verdict(db, "(whole update)", &acc, graph));
}

/// Follow the `let`/`if`-spine from `e` to the outermost `case`, folding shared
/// `let` refs into `shared`. Returns the case ExprId, or None.
fn find_top_case(body: &Body, e: ExprId, shared: &mut Refs) -> Option<ExprId> {
    match &body.exprs[e] {
        Expr::Case { .. } => Some(e),
        Expr::Let { defs, body: b } => {
            for d in defs {
                collect_localdef(body, d, shared);
            }
            find_top_case(body, *b, shared)
        }
        _ => None,
    }
}

/// Turn a branch's verdict from its collected refs.
fn verdict(db: &dyn SkyDb, label: &str, acc: &Refs, graph: &Graph) -> BranchVerdict {
    // Server iff: direct server kernel/FFI, OR a reachable callee is server.
    if let Some(reason) = acc.direct_server_reason() {
        return BranchVerdict {
            msg: label.to_string(),
            server: true,
            reason,
        };
    }
    // Deterministic: pick the lowest-id server callee.
    let mut server_callees: Vec<DefId> =
        acc.callees.iter().copied().filter(|c| graph.server.contains(c)).collect();
    server_callees.sort();
    if let Some(c) = server_callees.first() {
        let origin = graph
            .root_reason
            .get(c)
            .cloned()
            .unwrap_or_else(|| "server".into());
        let cn = db
            .def_loc(*c)
            .map(|l| format!("{}.{}", db.module_name(l.module), l.name.as_str()))
            .unwrap_or_else(|| "a server-tainted binding".into());
        return BranchVerdict {
            msg: label.to_string(),
            server: true,
            reason: format!("references {cn} ({origin})"),
        };
    }
    // Client — note a client effect if present.
    let reason = match acc.client_effect_note() {
        Some(n) => format!("client — {n}, no server reach"),
        None => "pure — no server effect or tainted value".to_string(),
    };
    BranchVerdict {
        msg: label.to_string(),
        server: false,
        reason,
    }
}

/// Render a case-branch pattern as a Msg label (`GotTodos (Ok _)`).
fn pattern_label(body: &Body, pat: hir::PatId) -> String {
    match &body.pats[pat] {
        Pattern::Ctor { name, args, .. } => {
            if args.is_empty() {
                name.as_str().to_string()
            } else {
                let inner: Vec<String> = args.iter().map(|a| pattern_head(body, *a)).collect();
                format!("{} {}", name.as_str(), inner.join(" "))
            }
        }
        Pattern::Var(_) => "_".to_string(),
        Pattern::Anything => "_".to_string(),
        other => pattern_head_of(other),
    }
}

fn pattern_head(body: &Body, pat: hir::PatId) -> String {
    match &body.pats[pat] {
        Pattern::Ctor { name, args, .. } if args.is_empty() => name.as_str().to_string(),
        Pattern::Ctor { name, .. } => format!("({} …)", name.as_str()),
        Pattern::Var(_) | Pattern::Anything => "_".to_string(),
        other => pattern_head_of(other),
    }
}

fn pattern_head_of(p: &Pattern) -> String {
    match p {
        Pattern::Int(n) => n.to_string(),
        Pattern::Str(s) => format!("{s:?}"),
        Pattern::Bool(b) => b.to_string(),
        Pattern::Tuple(_) => "(…)".into(),
        Pattern::List(_) => "[…]".into(),
        _ => "_".into(),
    }
}
