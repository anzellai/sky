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

use base::{DefId, FileId, ModuleId};
use hir::{Body, Expr, ExprId, LocalDef, LocalId, PatId, Pattern, Res, SkyDb};
use std::collections::{BTreeSet, HashMap, HashSet};
use std::path::Path;

/// A `diagnostics::SourceProvider` over an already-loaded source db, keyed the
/// same way the build driver keys its own (`FileId(module_id.index())`), so a
/// re-rendered type-check diagnostic shows the offending source line + caret and
/// a `<Module>:line:col` header instead of a bare error count (BUG-3). Path is
/// the module's dotted name (the analysis has no on-disk path map; the name is
/// enough to identify the file that failed).
struct SpaSources {
    text: HashMap<FileId, String>,
    paths: HashMap<FileId, String>,
}

impl SpaSources {
    fn from_db(db: &skydb::SkyDatabase, check_ids: &[ModuleId]) -> Self {
        let mut text = HashMap::new();
        let mut paths = HashMap::new();
        for m in check_ids {
            let fid = FileId(m.index());
            text.insert(fid, db.module_parse(*m).syntax().text().to_string());
            paths.insert(fid, db.module_name(*m).to_string());
        }
        SpaSources { text, paths }
    }
}

impl diagnostics::SourceProvider for SpaSources {
    fn text(&self, file: FileId) -> Option<&str> {
        self.text.get(&file).map(String::as_str)
    }
    fn path(&self, file: FileId) -> Option<&str> {
        self.paths.get(&file).map(String::as_str)
    }
}

// ---------------------------------------------------------------------------
// Kernel classification (design §3) — key off the `Res::Kernel` pseudo-module.
// ---------------------------------------------------------------------------

/// The target a reached effect kernel runs on.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum KernelClass {
    /// An EFFECT — runs on the server. v1 rule: **any** effect is server-side,
    /// so the client stays 100% pure. That includes not just the physically
    /// server-only families (DB, files, auth, secrets, the socket, process/stdio,
    /// env) but also the client-*capable* ones (`Http`, `Time`, `Random`, `Uuid`)
    /// — an Http call routes through the backend, a uuid/timestamp is a server
    /// round-trip. The trust model this buys: the client has NO effects, so no
    /// secret / DB handle / env value can ever reach it — auditable at a glance,
    /// and no env/CORS/CSP semantics for the author to learn.
    ServerOnly,
    /// A client-side effect — an effect that PHYSICALLY belongs in the browser /
    /// webview and must run in the wasm client, never behind an RPC. Routing one
    /// to the server would call its `//go:build !js` stub, which returns `Err`
    /// (there is no clipboard / geolocation / share sheet on the server). The
    /// first real inhabitant is [`CLIENT_EFFECT_KERNELS`] — `Std.Native.*`. A
    /// branch whose only effect refs are `ClientEffect` has no
    /// `direct_server_reason`, so it stays in the frontend and its kernel runs in
    /// wasm.
    ClientEffect,
    /// Pure / plumbing — irrelevant to the partition.
    Neutral,
}

/// **EFFECT** kernel pseudo-modules — every one runs on the **SERVER** under the
/// v1 rule "any effect -> server" (the client is 100% pure UI). This list is one
/// half of the exhaustive classification the `classification_is_exhaustive`
/// completeness test enforces against the compiler's real kernel-module table
/// (`hir::kernel::KERNEL_MODULES`): a kernel pseudo-module MUST appear here or in
/// [`KNOWN_PURE_KERNELS`], or the build fails. Defaulting an unclassified kernel
/// to client/pure would leak a real effect into the wasm frontend.
///
/// Two sub-groups, both server under the v1 rule:
///   * physically server-only — the browser cannot reach them at all:
///     `Db`/`Auth`/`File`/`Server`/`Process`/`Io`/`System`/`RateLimit`/
///     `Middleware`, plus the shell/host loops `Log`/`Live`/`Jobs`/`Cli`/`Tui`/
///     `Webview` and the effect-plumbing `Context` (cancellation/deadline).
///   * client-*capable* but routed to the server for the v1 secure-by-default
///     model — `Http` (routes through the backend), `Time`/`Random`/`Uuid`
///     (a client-local timestamp/uuid is a documented *later* optimisation).
///
/// `System.*` env reads are the SEED-2b "pure-typed" case: `getenvOr`/`getenvInt`
/// are typed `String -> String -> String` (NOT `Task`), so they are caught ONLY
/// by kernel identity, here — never by a Task-type check.
///
/// **`Ffi` is deliberately NOT here** — see [`KNOWN_PURE_KERNELS`]: it is the
/// universal implementation mechanism of *every* kernel (pure and effect), so
/// the effect lives in the symbol string, not the bare `Ffi` reference.
const EFFECT_KERNELS: &[&str] = &[
    "Db", "Auth", "File", "Server", "Process", "Io", "System", "RateLimit", "Middleware", "Http",
    "Time", "Random", "Uuid", "Log", "Live", "Jobs", "Cli", "Tui", "Webview", "Context", "Image",
];

/// **KNOWN-PURE** kernel pseudo-modules — pure computation / pure TEA plumbing
/// that is safe on the **CLIENT** (maps to [`KernelClass::Neutral`]). The other
/// half of the exhaustive classification (see [`EFFECT_KERNELS`]).
///
/// `Task` is here because `Task.succeed`/`map`/`andThen` merely *build* a task;
/// the effect is the `Ffi.kernel "<Symbol>"` inside it (classified by symbol
/// prefix) and the force site is `Task.run` (tracked separately as an inline
/// effect). `Cmd`/`Sub` are pure descriptions in the TEA loop. `Crypto` covers
/// pure hashing (`sha256`) — a client-side hash is pure UI, not an effect.
///
/// **`Ffi` is here, not in [`EFFECT_KERNELS`], and this is load-bearing.** A bare
/// `Ffi.*` reference (`Ffi.kernel`, `Ffi.call`, …) is the compiler's universal
/// kernel-implementation plumbing: EVERY kernel — pure (`String.isEmpty`,
/// `List.filter`, `Codec.*`) and effectful (`Db.query`) alike — has a body of
/// `Ffi.kernel "<Symbol>"`, so treating the bare `Ffi` module as an effect would
/// mark the entire stdlib server and leak nothing but false positives. The real
/// effect is the **symbol prefix**, classified by [`record_ffi_symbol`] (which is
/// itself fail-closed: an unknown prefix → server). Raw Go FFI is caught
/// separately as a `Res::Foreign` reference (`Refs::foreign` → server).
const KNOWN_PURE_KERNELS: &[&str] = &[
    "Basics", "String", "List", "Dict", "Set", "Maybe", "Result", "Task", "Math", "Regex",
    "Crypto", "Encoding", "Char", "Path", "Cmd", "Sub", "JsonEnc", "JsonDec", "JsonDecP", "Fmt",
    "Ffi", "Codec",
];

/// **CLIENT-EFFECT** kernel families — effects that must run in the wasm CLIENT,
/// never behind an RPC (maps to [`KernelClass::ClientEffect`]). These reach a
/// browser/webview-only platform API (`navigator.clipboard`, `navigator.share`,
/// `navigator.vibrate`, the Geolocation API); their `//go:build !js` counterpart
/// is an `Err` stub, so routing them to the server — the fail-closed default for
/// an unknown effect — would make every call fail. `Std.Native.*` is emitted as
/// raw `Ffi.kernel "Native_<cap>"` symbols, so the family is the `Native_` symbol
/// PREFIX, classified by [`record_ffi_symbol`] — "Native" is NOT a registered
/// `hir::KERNEL_MODULES` pseudo-module, so it is not (and need not be) in the
/// EFFECT/PURE exhaustiveness lists. Adding a family here is a deliberate
/// statement that its effect is safe + correct to run client-side.
const CLIENT_EFFECT_KERNELS: &[&str] = &["Native"];

/// Classify a kernel pseudo-module + function. `module` is the pseudo name
/// (`Db`, `Http`, `System`, …) as produced by the resolver's `Res::Kernel`, or
/// an `Ffi.kernel "<Symbol>"` prefix (`Db`, `Http`, …).
///
/// **FAIL-CLOSED.** A module in neither [`EFFECT_KERNELS`] nor
/// [`KNOWN_PURE_KERNELS`] is treated as a SERVER effect — never Neutral/client.
/// Defaulting an unrecognised kernel to client would leak it into the wasm
/// frontend. The `classification_is_exhaustive` test makes an unclassified
/// *known* kernel a BUILD FAILURE; this branch is the runtime defense-in-depth
/// for a family added ahead of the lists (or an unexpected FFI-symbol prefix).
fn classify_kernel(module: &str, _func: &str) -> KernelClass {
    if EFFECT_KERNELS.contains(&module) {
        KernelClass::ServerOnly
    } else if CLIENT_EFFECT_KERNELS.contains(&module) {
        // A browser/webview-only effect — runs in the wasm client, not via RPC.
        KernelClass::ClientEffect
    } else if KNOWN_PURE_KERNELS.contains(&module) {
        KernelClass::Neutral
    } else {
        // Unknown family → conservative server (fail-closed).
        KernelClass::ServerOnly
    }
}

/// The kernel pseudo-modules the compiler knows (`hir::kernel::KERNEL_MODULES`)
/// that are classified for the Sky.Spa auto-split in **neither** [`EFFECT_KERNELS`]
/// **nor** [`KNOWN_PURE_KERNELS`]. The completeness invariant is that this is
/// **empty** (enforced by `classification_is_exhaustive`). The generator
/// (`spa_split::generate`) calls this and refuses to emit when it is non-empty —
/// a kernel whose split side has not been decided must not silently default to
/// client. Sorted + deduped; distinct pseudo-module names only.
pub fn unclassified_kernel_families() -> Vec<String> {
    let mut gaps: BTreeSet<String> = BTreeSet::new();
    for (_import_path, pseudo) in hir::KERNEL_MODULES {
        if !EFFECT_KERNELS.contains(pseudo) && !KNOWN_PURE_KERNELS.contains(pseudo) {
            gaps.insert((*pseudo).to_string());
        }
    }
    gaps.into_iter().collect()
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
    /// Msg-constant precision (arm analysis ONLY): scoped `update <LiteralMsg> …`
    /// calls found in this subtree, recorded by the literal Msg ctor's NAME. A
    /// scoped call does NOT record `update` as a generic callee — the composing
    /// arm inherits the *composed* arm's verdict via the arm-level fixpoint,
    /// keyed by name. Populated only when `CollectCtx::update_def` is set.
    scoped_updates: Vec<String>,
    /// A NON-scoped use of the `update` def within this subtree: `update` with no
    /// args, a dynamic (non-literal) first arg, or `update` referenced as a
    /// value. Forces the arm conservatively to server (update-as-a-whole reaches
    /// server). Populated only when `CollectCtx::update_def` is set.
    generic_update: bool,
}

/// Context for `collect`. Empty (`default()`) reproduces the conservative walk
/// used everywhere except an `update` arm: `update` is recorded as an ordinary
/// callee, so any def (including a helper) that calls it is forced to server.
///
/// The Msg-constant precision applies ONLY to `update`'s own arms: when
/// `update_def` (and `db`, for ctor-name lookup) are set, a direct
/// `update <LiteralMsg> …` call is recorded as a *scoped* composition instead of
/// a generic callee, so composing a PURE arm no longer over-marks the composer
/// server. A NON-arm helper that calls `update` never sets this — it keeps the
/// conservative treatment. Never under-marks: a non-literal / dynamic-Msg / value
/// use of `update` sets `generic_update` → server.
#[derive(Clone, Copy, Default)]
struct CollectCtx<'a> {
    db: Option<&'a dyn SkyDb>,
    update_def: Option<DefId>,
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

/// Walk one expression subtree, accumulating references into `acc`. `ctx` is
/// `default()` everywhere except the Msg-constant-precision arm walk (see
/// `CollectCtx`).
fn collect(body: &Body, e: ExprId, acc: &mut Refs, ctx: &CollectCtx) {
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
                collect(body, *x, acc, ctx);
            }
        }
        Expr::Record(fields) => {
            for (_, x) in fields {
                collect(body, *x, acc, ctx);
            }
        }
        Expr::Update { base, fields } => {
            collect(body, *base, acc, ctx);
            for (_, x) in fields {
                collect(body, *x, acc, ctx);
            }
        }
        Expr::Var(res) => {
            // Msg-constant precision: `update` referenced as a VALUE (not the
            // callee of a `update <LiteralMsg> …` call) is a GENERIC use →
            // conservative server. Never record `update` as a callee here.
            if let (Some(update_def), Res::Def(d)) = (ctx.update_def, res) {
                if *d == update_def {
                    acc.generic_update = true;
                    return;
                }
            }
            record_res(res, acc);
        }
        Expr::Negate(x) => collect(body, *x, acc, ctx),
        Expr::Lambda { body: b, .. } => collect(body, *b, acc, ctx),
        Expr::Call(callee, args) => {
            // Msg-constant precision (arm analysis only): a direct
            // `update <LiteralMsg> …` call composes another arm. Record it as a
            // SCOPED call keyed by the Msg ctor name — NOT as a generic `update`
            // callee — so composing a pure arm does not force this arm server.
            if let (Some(update_def), Some(db)) = (ctx.update_def, ctx.db) {
                if let Expr::Var(Res::Def(d)) = &body.exprs[*callee] {
                    if *d == update_def {
                        match args.first().and_then(|a| literal_ctor_name(body, db, *a)) {
                            Some(name) => acc.scoped_updates.push(name),
                            // No args, or a dynamic / non-literal first arg →
                            // GENERIC use → conservative server.
                            None => acc.generic_update = true,
                        }
                        // Descend into the ARGS (a payload may carry its own
                        // effect) but NOT the callee — `update` stays off the
                        // callee set for this scoped/generic use.
                        for a in args {
                            collect(body, *a, acc, ctx);
                        }
                        return;
                    }
                }
            }
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
            collect(body, *callee, acc, ctx);
            for a in args {
                collect(body, *a, acc, ctx);
            }
        }
        Expr::Binop { res, lhs, rhs, .. } => {
            record_res(res, acc);
            collect(body, *lhs, acc, ctx);
            collect(body, *rhs, acc, ctx);
        }
        Expr::If { arms, els } => {
            for (c, t) in arms {
                collect(body, *c, acc, ctx);
                collect(body, *t, acc, ctx);
            }
            collect(body, *els, acc, ctx);
        }
        Expr::Let { defs, body: b } => {
            for d in defs {
                collect_localdef(body, d, acc, ctx);
            }
            collect(body, *b, acc, ctx);
        }
        Expr::Case { subject, branches } => {
            collect(body, *subject, acc, ctx);
            for br in branches {
                collect(body, br.body, acc, ctx);
            }
        }
        Expr::Access(x, _) => collect(body, *x, acc, ctx),
    }
}

/// The literal Msg ctor NAME of an `update` call's first argument, if it is one:
/// a nullary ctor (`Expr::Var(Res::Ctor _)`) or an applied ctor
/// (`Expr::Call(Var(Res::Ctor _), …)`). `None` for a dynamic / non-ctor arg.
fn literal_ctor_name(body: &Body, db: &dyn SkyDb, e: ExprId) -> Option<String> {
    let cref = match &body.exprs[e] {
        Expr::Var(Res::Ctor(c)) => c,
        Expr::Call(callee, _) => match &body.exprs[*callee] {
            Expr::Var(Res::Ctor(c)) => c,
            _ => return None,
        },
        _ => return None,
    };
    db.def_loc(cref.def).map(|l| l.name.as_str().to_string())
}

fn collect_localdef(body: &Body, d: &LocalDef, acc: &mut Refs, ctx: &CollectCtx) {
    // `let _ = <expr>` — an empty-binder, non-destructuring def is the auto-force
    // site (`lower.rs:2708-2711`); the effect it forces is executed inline.
    if d.binders.is_empty() && d.pat.is_none() {
        acc.inline_force = true;
    }
    collect(body, d.body, acc, ctx);
}

/// Classify an `Ffi.kernel "<Symbol>"` string by its `<Prefix>_` — the runtime
/// symbol's family (`Db_query` → Db, `Http_post` → Http, `System_getenvOr` →
/// System). This is the actual effect origin under the Sky-source stdlib.
fn record_ffi_symbol(sym: &str, acc: &mut Refs) {
    // `Task_run` is the FORCE primitive — the Sky-source stdlib defines
    // `Task.run = Ffi.kernel "Task_run"`, so a `Task.run <task>` call resolves to
    // this def, NOT to `Res::Kernel{Task, run}` (the arm at the `Expr::Call` site
    // never matches real code). Reaching this symbol means an effect is FORCED in
    // a run position — the phase-2 differential fuzzer's fence keys on it (a
    // branch that transitively forces an effect is not deterministic under
    // identical stubs and is deferred to the phase-3 effect-mock harness). It is
    // effect-NEUTRAL for the server/client taint (the forced task's OWN symbols
    // decide the side), so we only flip `inline_force` here.
    if sym == "Task_run" {
        acc.inline_force = true;
    }
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

/// The top-level defs (`Res::Def`) referenced by `def`'s body — the raw
/// material the `spa-split` generator uses to copy a user codec's transitive
/// helper closure into the generated `Shared` module. Read-only walk over the
/// resolved HIR; conservative `default()` context (no Msg-precision needed here).
/// Returns the callee `DefId`s (deduped, sorted for determinism).
pub fn body_def_callees(db: &dyn SkyDb, module: ModuleId, def: DefId) -> Vec<DefId> {
    let resolved = db.resolve(module);
    let Some(body) = resolved.bodies.get(&def) else {
        return Vec::new();
    };
    let mut acc = Refs::default();
    if let Some(root) = body.root {
        collect(body, root, &mut acc, &CollectCtx::default());
    }
    let mut out: Vec<DefId> = acc.callees.into_iter().collect();
    out.sort();
    out
}

/// True when `def`'s body is a direct kernel alias `Ffi.kernel "<sym>"` whose
/// symbol is one of `targets`. The Sky-source stdlib defines `Sub.subscribeTopic`
/// / `Cmd.publish` as exactly this shape (`Ffi.kernel "Sub_subscribeTopic"`,
/// `Ffi.kernel "Cmd_publish"`), so a reachable def matching a target proves the
/// app uses that surface — the raw material for the `spa-split` generator's
/// push-mode decision.
fn def_is_kernel_alias_to(db: &dyn SkyDb, def: DefId, targets: &[&str]) -> bool {
    let Some(loc) = db.def_loc(def) else {
        return false;
    };
    let resolved = db.resolve(loc.module);
    let Some(body) = resolved.bodies.get(&def) else {
        return false;
    };
    let Some(root) = body.root else {
        return false;
    };
    if let Expr::Call(callee, args) = &body.exprs[root] {
        if args.len() == 1 {
            if let Expr::Var(Res::Kernel { func, .. }) = &body.exprs[*callee] {
                if func.as_str() == "kernel" {
                    if let Expr::Str(sym) = &body.exprs[args[0]] {
                        let sym_str: &str = sym;
                        return targets.contains(&sym_str);
                    }
                }
            }
        }
    }
    false
}

/// Whether the app reaches ANY def that is a kernel alias to one of `targets`,
/// scanning every def in the reachability graph (app top-defs + their transitive
/// callees, which is where `Sub.subscribeTopic` / `Cmd.publish` land when used).
fn app_reaches_kernel(db: &dyn SkyDb, graph: &Graph, targets: &[&str]) -> bool {
    graph
        .nodes
        .keys()
        .any(|d| def_is_kernel_alias_to(db, *d, targets))
}

// ---------------------------------------------------------------------------
// The report.
// ---------------------------------------------------------------------------

/// The RPC read-set / write-set of a SERVER branch (B1). Client branches carry
/// no I/O (`BranchVerdict::io == None`) — they run locally with no round-trip.
///
/// The read-set (Model fields read + Msg args bound) becomes the RPC *request*;
/// the write-set (Model fields written) becomes the RPC *response*. Both are
/// **over-approximated to the whole Model** when the branch uses `model`
/// opaquely (threads it into a helper, returns a fresh record, …) — a bigger
/// payload, never a wrong value. Under-approximating reads/writes would be a
/// correctness bug, so on any ambiguity we include MORE (`*_whole_model`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct BranchIo {
    /// The branch reads `model` opaquely → the request must carry EVERY field.
    pub reads_whole_model: bool,
    /// Model fields read via `model.field` (sorted, deduped). Ignored for the
    /// request shape when `reads_whole_model` is set (whole model subsumes them).
    pub read_fields: Vec<String>,
    /// Msg args the arm pattern binds (`ToggleTodo id` → `["id"]`) — RPC inputs
    /// that are NOT model fields. In source (binding) order.
    pub msg_args: Vec<String>,
    /// The branch's returned model flows out opaquely (a helper call / a fresh
    /// record) → the response must carry EVERY field.
    pub writes_whole_model: bool,
    /// Model fields written via `{ model | f = … }` in tail position (sorted,
    /// deduped). Ignored for the response shape when `writes_whole_model` is set.
    pub write_fields: Vec<String>,
}

impl BranchIo {
    /// The RPC request shape (`in: …`) — read-set fields ∪ Msg args.
    fn render_in(&self) -> String {
        let mut parts: Vec<String> = Vec::new();
        if self.reads_whole_model {
            parts.push("<whole model>".to_string());
        } else if !self.read_fields.is_empty() {
            parts.push(fmt_set(&self.read_fields));
        }
        if !self.msg_args.is_empty() {
            parts.push(fmt_set(&self.msg_args));
        }
        if parts.is_empty() {
            "{}".to_string()
        } else {
            parts.join(" + ")
        }
    }
    /// The RPC response shape (`out: …`) — write-set fields.
    fn render_out(&self) -> String {
        if self.writes_whole_model {
            "<whole model>".to_string()
        } else {
            fmt_set(&self.write_fields)
        }
    }
}

fn fmt_set(items: &[String]) -> String {
    format!("{{{}}}", items.join(", "))
}

/// One `update` branch's verdict.
pub struct BranchVerdict {
    pub msg: String,
    pub server: bool,
    pub reason: String,
    /// The RPC read-set / write-set — `Some` for SERVER branches (the derived
    /// RPC I/O), `None` for CLIENT branches (no round-trip, so no I/O sets).
    pub io: Option<BranchIo>,
    /// The Msg args this branch's pattern binds, with their **types** (parallel
    /// to `io.msg_args` by name+order) — the raw material the `spa-split`
    /// generator uses to give each Msg-arg Req field a real codec. Empty for a
    /// nullary branch or a CLIENT branch. Kept off [`BranchIo`] so that type
    /// stays `Eq` (the typed `ty::Ty` is not).
    pub msg_arg_tys: Vec<ModelFieldTy>,
    /// TRANSITIVE forces-effect: this branch's `update` body — or any def it
    /// reaches — FORCES a side effect in a run position (`Task.run …` or a
    /// `let _ = <task>` auto-force). This is the phase-2 differential fuzzer's
    /// fence: a branch that forces an effect during `update` is NOT deterministic
    /// under identical stubs (it reaches a DB read / a fresh Uuid / the clock),
    /// so it is deferred to the phase-3 effect-mock harness rather than diffed.
    /// A pure-typed kernel like `System.getenvOr` (String -> String -> String,
    /// never wrapped in `Task.run`) does not set this — it is deterministic and
    /// stays checkable. Distinct from `server`: a branch can be server (reads a
    /// server kernel) yet force NOTHING at update time (it returns a `Cmd` value
    /// the runtime runs later) — that branch IS phase-2 checkable.
    pub forces_effect: bool,
}

/// A server-tainted top-level binding (excluded from the client build).
pub struct TaintedBinding {
    pub module: String,
    pub name: String,
    pub reason: String,
}

/// One Model field with its rendered Sky type name and the `Std.Codec`
/// combinator that encodes it — the raw material the `spa-split` generator uses
/// to synthesise the shared wire records (`<Msg>Req` / `<Msg>Resp`). Populated
/// for primitive field types (`Int` / `String` / `Bool` / `Float`); `codec` is
/// `None` for a field whose type the generator does not know how to encode
/// (the generator then notes it as a deferred shape rather than guessing).
#[derive(Clone, Debug)]
pub struct ModelFieldTy {
    pub name: String,
    /// The rendered type name (`Int`), tail-segment of a folded nominal name.
    /// For a non-primitive type this is a best-effort surface rendering
    /// (`List Todo`) — the authoritative shape for codec resolution is [`ty`].
    pub ty_name: String,
    /// The `Std.Codec` combinator for a primitive field (`Codec.int`), or `None`
    /// when the field's type is non-primitive. The `spa-split` generator resolves
    /// the non-primitive case against the project's own `Codec <T>` bindings /
    /// `Codec.list`, using [`ty`].
    pub codec: Option<String>,
    /// The field's fully-resolved type, when recoverable — the raw material the
    /// generator's codec resolver consumes (`List Todo`, a user record/union,
    /// …). `None` only when the type could not be read from the typed HIR.
    pub ty: Option<ty::Ty>,
}

/// Map a solved field type to its `(type name, codec combinator)` — the four
/// JSON primitives the generator can wire. Keys off the nominal tail so a
/// home-folded `Sky.Core.Basics.Int` still reads as `Int`.
fn field_ty_codec(t: &ty::Ty) -> ModelFieldTy {
    if let ty::Ty::App(name, args) = t {
        if args.is_empty() {
            let tail = name.as_str().rsplit('.').next().unwrap_or(name.as_str());
            let codec = match tail {
                "Int" => Some("Codec.int"),
                "String" => Some("Codec.string"),
                "Bool" => Some("Codec.bool"),
                "Float" => Some("Codec.float"),
                _ => None,
            };
            return ModelFieldTy {
                name: String::new(),
                ty_name: tail.to_string(),
                codec: codec.map(str::to_string),
                ty: Some(t.clone()),
            };
        }
    }
    // Non-primitive (a `List X`, a user record/union, …). Carry the full type so
    // the generator's codec resolver can wire it against the project's codecs;
    // render a best-effort surface name for display.
    ModelFieldTy {
        name: String::new(),
        ty_name: render_ty_name(t),
        codec: None,
        ty: Some(t.clone()),
    }
}

/// A best-effort surface rendering of a type for a generated `type alias` field
/// (`List Todo`, `Todo`, `Int`). Tail-normalises folded nominal names. Falls
/// back to `any` for shapes the generator cannot spell as a field type.
fn render_ty_name(t: &ty::Ty) -> String {
    match t {
        ty::Ty::App(name, args) => {
            let tail = name.as_str().rsplit('.').next().unwrap_or(name.as_str());
            if args.is_empty() {
                tail.to_string()
            } else {
                let inner: Vec<String> = args.iter().map(render_ty_name).collect();
                format!("{} {}", tail, inner.join(" "))
            }
        }
        _ => "any".to_string(),
    }
}

/// The Model's fields — name + type + codec — recovered from the `update`
/// result type `( Model, Cmd msg )`. Empty when the shape is not the TEA tuple.
fn model_fields_typed(result: &Option<ty::Ty>) -> Vec<ModelFieldTy> {
    if let Some(ty::Ty::Tuple(xs)) = result {
        if xs.len() == 2 {
            if let ty::Ty::Record(fields, _) = &xs[0] {
                let mut out: Vec<ModelFieldTy> = fields
                    .iter()
                    .map(|(n, t)| {
                        let mut f = field_ty_codec(t);
                        f.name = n.as_str().to_string();
                        f
                    })
                    .collect();
                out.sort_by(|a, b| a.name.cmp(&b.name));
                return out;
            }
        }
    }
    Vec::new()
}

/// The full partition report for one project.
pub struct SpaPartitionReport {
    pub project: String,
    pub entry_module: String,
    pub update_name: Option<String>,
    /// The name of the module that DECLARES `update` (`"Update"`, `"Main"`, …),
    /// resolved cross-module via the `Spa.config` graph — the raw material the
    /// `spa-split` generator needs to regenerate the partitioned `update` in its
    /// OWN module copy when `update` is factored into a sibling (GAP-1), rather
    /// than assuming it lives in the entry. `None` when `update` was not resolved
    /// to a named def (a lambda / partial-application shape).
    pub update_module_name: Option<String>,
    /// Present when per-branch analysis was possible.
    pub branches: Vec<BranchVerdict>,
    /// Set when the `update` body is not a resolvable `case msg of` (a lambda /
    /// partial-app / delegating shape) — the whole-update verdict, no per-branch.
    pub whole_update: Option<BranchVerdict>,
    pub tainted: Vec<TaintedBinding>,
    /// The Model's fields with their types + codecs (for the `spa-split`
    /// generator's shared wire records). Empty when the Model shape could not
    /// be recovered from `update`'s result type.
    pub model_fields: Vec<ModelFieldTy>,
    /// The app reaches `Sub.subscribeTopic` (a server→client PUSH consumer) —
    /// the `spa-split` generator mounts the SSE push endpoint when set.
    pub subscribes_topics: bool,
    /// The app reaches `Cmd.publish` / `Cmd.publishNoEcho` (a server→client
    /// PUSH producer) — the generator wires publish-interpreting RPC handlers +
    /// the broker when set. Either flag turns on the auto-split's push mode.
    pub publishes: bool,
    /// Non-fatal notes (why a branch was conservatively marked server, etc.).
    pub notes: Vec<String>,
    /// G5 refusal input: the server reads embedded in `init`'s returned MODEL
    /// (the first `( model, cmd )` element — NOT the command), each named
    /// `Module.name (origin)`. The wasm client cannot reproduce these values, so
    /// the `spa-split` generator REFUSES to emit when this is non-empty. Empty
    /// for the supported pattern (a server read deferred to `init`'s COMMAND).
    pub init_model_server_reads: Vec<String>,
    /// SERVER-INTERNAL Msgs (server-internal effect chaining). A Msg dispatched
    /// ONLY from a server arm's `Cmd.perform`/`Cmd.batch` toMsg — never a view
    /// event, a client arm, a subscription, nor its own wire branch. It has no
    /// `/_rpc/<Msg>` route, no `Applied<Msg>` variant, and its client `update`
    /// arm is DROPPED (the whole chain settles server-side in the triggering
    /// branch's RPC). Sorted + deduped.
    pub server_internal: Vec<String>,
    /// The SERVER branch ctor names whose RPC handler settles a `Cmd.perform`
    /// chain server-side (binds the returned command + runs
    /// `Spa_settleServerChain`), rather than discarding it. Their write/read
    /// sets already carry the UNION over every server-internal continuation arm.
    pub chaining_branches: Vec<String>,
    /// PATTERN-2 client-result performs (docs/skyspa/auto-split.md). Each
    /// `(root, result_msg)`: a SERVER branch `root` whose command (through a
    /// guard/HOF wrapper) is a single `Cmd.perform serverTask result_msg` with a
    /// server task and a CLIENT-pure `result_msg`. The `root` RPC runs the task
    /// and returns its RESULT; the frontend `Applied<root>` dispatches
    /// `result_msg result` client-side. `result_msg` stays a client arm.
    pub client_result: Vec<(String, String)>,
    /// G5 fail-closed warnings — a server branch returns a `Cmd.perform` the
    /// analysis refused to chain (a client `Std.Native` effect, an ambiguous
    /// continuation, or an opaque command). The follow-up runs nowhere; surfaced
    /// prominently by the `spa-split` CLI.
    pub server_chain_warnings: Vec<String>,
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
                match &b.io {
                    // SERVER branch: lead with the derived RPC I/O, reason below.
                    Some(io) => {
                        o.push_str(&format!(
                            "  {tag}  {:<width$}  in: {:<24}  out: {}\n",
                            b.msg,
                            io.render_in(),
                            io.render_out(),
                            width = w
                        ));
                        o.push_str(&format!(
                            "          {:<width$}  {}\n",
                            "",
                            b.reason,
                            width = w
                        ));
                    }
                    None => o.push_str(&format!(
                        "  {tag}  {:<width$}  {}\n",
                        b.msg,
                        b.reason,
                        width = w
                    )),
                }
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
    analyze_loaded(&db, entry, &check_ids, project)
}

/// The analysis over an already-loaded source db. `analyze` is the thin wrapper
/// that assembles the db from a project dir; the `spa-split` generator calls
/// this directly so it can reuse the SAME db for its CST slicing + type reads.
pub fn analyze_loaded(
    db: &skydb::SkyDatabase,
    entry: ModuleId,
    check_ids: &[ModuleId],
    project: String,
) -> Result<SpaPartitionReport, String> {
    let check_ids = check_ids.to_vec();
    let entry_module_name = db.module_name(entry).to_string();

    // Type-check first — the report is only meaningful for a program that
    // `sky check`s clean (mirrors the build's accept/reject gate). When it does
    // NOT check, re-render the ACTUAL diagnostics with file:line + caret (BUG-3)
    // — a bare `1 type error(s)` count discarded exactly the information a user
    // needs, and on the `--target web:app` path the failing entry is a
    // SYNTHESISED file they never wrote, so the count alone is undiagnosable.
    let checked = ty::check_modules(db, &check_ids);
    if checked.type_errors > 0 || checked.name_errors > 0 {
        let sources = SpaSources::from_db(db, &check_ids);
        // Surface the error/exhaustiveness diagnostics (the `E1…`/`E2…`/`E3001`
        // classes the build's accept/reject gate keys on), rendered Elm-style.
        let ds: Vec<String> = checked
            .diagnostics
            .iter()
            .filter(|d| {
                d.severity == diagnostics::Severity::Error
                    && (d.code.0.starts_with("E1")
                        || d.code.0.starts_with("E2")
                        || d.code.0 == "E3001")
            })
            .map(|d| d.render_cli(&sources))
            .collect();
        let rendered = if ds.is_empty() {
            String::new()
        } else {
            format!("\n\n{}", ds.join("\n"))
        };
        return Err(format!(
            "project does not type-check ({} type error(s), {} name error(s)):{}",
            checked.type_errors, checked.name_errors, rendered
        ));
    }

    // Identify the `Std.Spa.config` def, then the `update` it was given.
    let spa_mod = db
        .module_by_name("Std.Spa")
        .ok_or_else(|| "not a Sky.Spa project: Std.Spa is not imported".to_string())?;
    let config_def = def_by_name(db, spa_mod, "config")
        .ok_or_else(|| "Std.Spa.config not found (stdlib mismatch?)".to_string())?;

    let mut notes: Vec<String> = Vec::new();
    let update_field = find_config_update_field(db, &check_ids, entry, config_def);

    // Build the reachability + taint graph over every def reachable from the
    // app modules (pulls in only the stdlib defs actually referenced).
    let graph = build_graph(db, &check_ids);

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
                    reason: graph.reason_for(db, td.def),
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
    let mut update_module_name: Option<String> = None;
    let mut model_fields: Vec<ModelFieldTy> = Vec::new();
    let mut server_internal: Vec<String> = Vec::new();
    let mut chaining_branches: Vec<String> = Vec::new();
    let mut client_result: Vec<(String, String)> = Vec::new();
    let mut server_chain_warnings: Vec<String> = Vec::new();

    match update_field {
        UpdateField::Def(update_def) => {
            let loc = db.def_loc(update_def);
            let (umod, uname) = loc
                .map(|l| (l.module, l.name.as_str().to_string()))
                .unwrap_or((entry, "update".to_string()));
            update_name = Some(format!("{}.{}", db.module_name(umod), uname));
            update_module_name = Some(db.module_name(umod).to_string());
            let resolved = db.resolve(umod);
            if let Some(body) = resolved.bodies.get(&update_def) {
                // Recover the Model field list + types from `update`'s result
                // type — the raw material for the generator's wire records.
                let types = ty::Typer::new(db).body_types(umod, update_def, body);
                model_fields = model_fields_typed(&types.result);
                classify_update_body(
                    db, &graph, umod, update_def, body, &mut branches, &mut whole_update,
                    &mut notes,
                );
                // Server-internal effect chaining (Phase 1 + Phase 2). Resolve
                // `view`/`subscriptions` for the client-construction scan, then
                // classify the server-internal Msgs + widen the chaining
                // branches' RPC I/O to the union over their continuation arms.
                let view_def = find_config_field_def(db, &check_ids, entry, "view");
                let subs_def = find_config_field_def(db, &check_ids, entry, "subscriptions");
                let chaining =
                    compute_server_chaining(db, umod, body, view_def, subs_def, &mut branches);
                server_internal = chaining.server_internal;
                chaining_branches = chaining.chaining_branches;
                client_result = chaining.client_result;
                server_chain_warnings = chaining.warnings;
            } else {
                return Err("update def has no body".into());
            }
        }
        UpdateField::Lambda(umod, body, root) => {
            update_name = Some(format!("{}.<lambda update>", db.module_name(umod)));
            update_module_name = Some(db.module_name(umod).to_string());
            // A lambda update: analyse its body as one unit (no stable Msg
            // pattern names unless it is itself a `case`).
            classify_lambda_update(db, &graph, umod, &body, root, &mut branches, &mut whole_update);
            notes.push(
                "update is an inline lambda; per-branch names taken from its `case` if present."
                    .into(),
            );
        }
        UpdateField::Unavailable(why) => {
            notes.push(format!("branch analysis unavailable: {why}"));
        }
    }

    // The rule, stated on every report so the model is never a mystery:
    // pure → client, ANY effect → server. Secure by default — an effectful
    // value/function (DB, files, auth, secrets, env, Http, time, random) never
    // reaches client code, so the client is 100% pure UI.
    if !branches.is_empty() {
        notes.push(
            "Rule: pure -> client, any effect -> server. The client is 100% pure UI; effectful values/functions (Db/File/Auth/System/Http/Time/Random/…) never reach it — secure by default."
                .to_string(),
        );
    }

    // Fail-closed defense-in-depth: if the compiler knows a kernel pseudo-module
    // the auto-split classification lists have not caught up to, say so loudly.
    // `classify_kernel` already treats such a family as SERVER (conservative), so
    // the report stays sound; this note names it so the omission gets fixed.
    let gaps = unclassified_kernel_families();
    if !gaps.is_empty() {
        notes.push(format!(
            "FAIL-CLOSED: kernel module(s) {} are not classified for the Sky.Spa auto-split (neither EFFECT nor KNOWN_PURE in spa_partition); treated conservatively as SERVER. Add each to EFFECT (server) or KNOWN_PURE (client) in spa_partition::classify_kernel.",
            gaps.join(", ")
        ));
    }

    // Server→client PUSH detection (docs/skyspa/auto-split.md §16). The generator
    // turns on push mode (broker + publish-interpreting handlers + the SSE
    // endpoint) when the app produces or consumes topic broadcasts.
    let subscribes_topics = app_reaches_kernel(db, &graph, &["Sub_subscribeTopic"]);
    let publishes = app_reaches_kernel(db, &graph, &["Cmd_publish", "Cmd_publishNoEcho"]);

    // G5: `init`'s returned MODEL embedding a server read (Db/File/…) is
    // unreproducible in the wasm client — the generator refuses on it.
    let init_model_server_reads = init_model_server_reads(db, &graph, &check_ids, entry);
    if !init_model_server_reads.is_empty() {
        notes.push(format!(
            "`init`'s returned model embeds server read(s) [{}] the wasm client cannot reproduce; the auto-split refuses. Defer the read to `init`'s command and fold it in via a `Got<Field>` update arm.",
            init_model_server_reads.join(", ")
        ));
    }

    Ok(SpaPartitionReport {
        project,
        entry_module: entry_module_name,
        update_name,
        update_module_name,
        branches,
        whole_update,
        tainted,
        model_fields,
        subscribes_topics,
        publishes,
        notes,
        init_model_server_reads,
        server_internal,
        chaining_branches,
        client_result,
        server_chain_warnings,
    })
}

/// G5 refusal input: the server reads embedded in `init`'s returned MODEL — the
/// FIRST element of the `( model, cmd )` tuple, NOT the command. Returns each
/// offending read named `Module.name (origin)` (server kernels reached directly
/// plus server-tainted callees the model expression reaches), sorted + deduped.
///
/// Empty when `init` is not a resolvable named def, when its model expression is
/// not isolable, or when the model is pure. The COMMAND is deliberately never
/// walked: a server read placed in `init`'s command is the SUPPORTED pattern (it
/// runs server-side and the client hydrates from the SSR result), so it must
/// never trip this refusal — the isolation to the tuple's first element is what
/// keeps the deferred pattern free of false positives.
fn init_model_server_reads(
    db: &skydb::SkyDatabase,
    graph: &Graph,
    check_ids: &[ModuleId],
    entry: ModuleId,
) -> Vec<String> {
    let Some(init_def) = find_config_field_def(db, check_ids, entry, "init") else {
        return Vec::new();
    };
    let Some(loc) = db.def_loc(init_def) else {
        return Vec::new();
    };
    let resolved = db.resolve(loc.module);
    let Some(body) = resolved.bodies.get(&init_def) else {
        return Vec::new();
    };
    let Some(root) = body.root else {
        return Vec::new();
    };
    // Isolate the returned MODEL (first tuple element), never the command.
    let mut model_exprs: Vec<ExprId> = Vec::new();
    collect_init_model_exprs(body, root, &mut model_exprs);
    if model_exprs.is_empty() {
        // Could not isolate the model — do not risk a false positive.
        return Vec::new();
    }
    let mut acc = Refs::default();
    for m in model_exprs {
        collect(body, m, &mut acc, &CollectCtx::default());
    }

    // The refusal fires only on a GENUINE server EFFECT the client cannot
    // reproduce — a read whose origin is an [`EFFECT_KERNELS`] kernel (`Db` /
    // `File` / `System` / `Http` / `Auth` / …) or a Go FFI reference. A callee is
    // server-tainted for MANY reasons under the fail-closed model: a
    // pure-but-unclassified kernel (`Secret.unsafeFromString "x"` bottoms out at
    // the fail-closed `Secret_fromString`, a PURE construction the wasm client can
    // reproduce) is marked server for SECURITY, not because it is a read. Keying
    // the refusal on the ultimate EFFECT kernel — not on the coarse `graph.server`
    // taint — is what excludes those pure constructions and keeps the deferred
    // pattern free of false positives.
    let name_of = |c: &DefId| -> String {
        let name = db
            .def_loc(*c)
            .map(|l| format!("{}.{}", db.module_name(l.module), l.name.as_str()))
            .unwrap_or_else(|| "a server-tainted binding".into());
        let origin = graph
            .root_reason
            .get(c)
            .cloned()
            .unwrap_or_else(|| "server".into());
        format!("{name} ({origin})")
    };
    let mut offenders: BTreeSet<String> = BTreeSet::new();
    // A genuine effect kernel named directly in the model expression.
    for (m, f, _class) in &acc.server_kernels {
        if EFFECT_KERNELS.contains(&m.as_str()) {
            offenders.insert(format!("{m}.{f}"));
        }
    }
    // A callee whose value requires running a genuine server effect (`loadAll` ->
    // `Db.query`, a `db` connection CAF -> `Db.open`, an env-reading CAF).
    let mut visited: HashSet<DefId> = HashSet::new();
    for c in &acc.callees {
        if def_reaches_genuine_effect(db, *c, &mut visited) {
            offenders.insert(name_of(c));
        }
    }
    if acc.foreign {
        offenders.insert("a Go FFI reference (opaque -> server)".into());
    }
    offenders.into_iter().collect()
}

/// Whether evaluating `d`'s body requires running a GENUINE server effect — a
/// kernel in [`EFFECT_KERNELS`] (`Db` / `File` / `System` / `Http` / `Auth` / …)
/// or a Go FFI reference — reached transitively through its callees. This is the
/// discriminator between a real server READ (unreproducible in the wasm client)
/// and a callee that is merely fail-closed to `server` under the taint model
/// because it touches a pure-but-unclassified kernel (e.g. `Secret`). `Std.Spa`'s
/// own client-boundary helpers are pure client leaves (mirrors `build_graph`).
fn def_reaches_genuine_effect(
    db: &skydb::SkyDatabase,
    d: DefId,
    visited: &mut HashSet<DefId>,
) -> bool {
    if !visited.insert(d) {
        return false;
    }
    let Some(loc) = db.def_loc(d) else {
        return false;
    };
    if db.module_name(loc.module) == "Std.Spa" {
        return false;
    }
    let resolved = db.resolve(loc.module);
    let Some(body) = resolved.bodies.get(&d) else {
        return false;
    };
    let Some(root) = body.root else {
        return false;
    };
    let mut acc = Refs::default();
    collect(body, root, &mut acc, &CollectCtx::default());
    if acc
        .server_kernels
        .iter()
        .any(|(m, _, _)| EFFECT_KERNELS.contains(&m.as_str()))
    {
        return true;
    }
    if acc.foreign {
        return true;
    }
    acc.callees
        .iter()
        .any(|c| def_reaches_genuine_effect(db, *c, visited))
}

/// The candidate MODEL expressions of an `init` body — the FIRST element of every
/// `( model, cmd )` tuple the body can evaluate to (walking the `let`/`if`/`case`
/// spine). The command element is never collected. Empty when the tail is not a
/// recognisable pair.
fn collect_init_model_exprs(body: &Body, e: ExprId, out: &mut Vec<ExprId>) {
    match &body.exprs[e] {
        Expr::Tuple(xs) if xs.len() == 2 => out.push(xs[0]),
        Expr::Let { body: b, .. } => collect_init_model_exprs(body, *b, out),
        Expr::If { arms, els } => {
            for (_, t) in arms {
                collect_init_model_exprs(body, *t, out);
            }
            collect_init_model_exprs(body, *els, out);
        }
        Expr::Case { branches, .. } => {
            for br in branches {
                collect_init_model_exprs(body, br.body, out);
            }
        }
        _ => {}
    }
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

/// Resolve the `DefId` a `Spa.config { <field> = … }` field points at, when the
/// field is a plain top-level name reference (the common shape — `init`,
/// `view`, `subscriptions`). Searches every project module for the config call.
/// Returns `None` for a lambda / partial-application / kernel field, or when no
/// `Spa.config { … }` call is found. Used by the auto-split generator to resolve
/// the DECLARING module of `init`/`view` (which may be factored into a sibling
/// module — the sky-lang.org shape) via the import graph, rather than scanning
/// the entry source text (which would miss them).
pub fn find_config_field_def(
    db: &skydb::SkyDatabase,
    check_ids: &[ModuleId],
    entry: ModuleId,
    field: &str,
) -> Option<DefId> {
    let spa_mod = db.module_by_name("Std.Spa")?;
    let config_def = def_by_name(db, spa_mod, "config")?;
    let mut order = vec![entry];
    order.extend(check_ids.iter().copied().filter(|m| *m != entry));
    for mid in order {
        let resolved = db.resolve(mid);
        for (_def, body) in &resolved.bodies {
            if let Some(f) = find_config_field(body, config_def, field) {
                if let Expr::Var(Res::Def(d)) = &body.exprs[f] {
                    return Some(*d);
                }
            }
        }
    }
    None
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
            if let Some(field) = find_config_field(body, config_def, "update") {
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
/// return the named field's ExprId.
fn find_config_field(body: &Body, config_def: DefId, field: &str) -> Option<ExprId> {
    for (id, expr) in body.exprs.iter() {
        if let Expr::Call(callee, args) = expr {
            if let Expr::Var(Res::Def(d)) = &body.exprs[*callee] {
                if *d == config_def {
                    if let Some(first) = args.first() {
                        if let Expr::Record(fields) = &body.exprs[*first] {
                            for (n, v) in fields {
                                if n.as_str() == field {
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
    /// This def's OWN body forces an effect in a run position (`Task.run …` /
    /// `let _ = <task>`). Seeds the `forces_effect` fixpoint (parallel to
    /// `server`). An opaque / body-less def is conservatively `true` — it MIGHT
    /// force, and the phase-2 fence must exclude a branch that might, never
    /// under-mark. A `Std.Spa` client leaf is `false` (a pure fetch, no run-time
    /// force — the same decision the taint walk already makes for it).
    forces: bool,
}

struct Graph {
    nodes: HashMap<DefId, DefNode>,
    /// The taint fixpoint.
    server: HashSet<DefId>,
    /// Ultimate origin reason per server def (the kernel it bottoms out at).
    root_reason: HashMap<DefId, String>,
    /// The forces-effect fixpoint: defs that force a run-position effect
    /// transitively. The phase-2 differential fuzzer's fence (see
    /// [`BranchVerdict::forces_effect`]).
    forces_effect: HashSet<DefId>,
}

impl Graph {
    /// Does `d` — or any def it reaches — force a run-position effect?
    fn forces(&self, d: DefId) -> bool {
        self.forces_effect.contains(&d)
    }

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
                    // Opaque: might force. Fail the fence closed (never checkable).
                    forces: true,
                },
            );
            continue;
        };
        // `Std.Spa`'s client-boundary helpers (`getJson` / `postJson`) are
        // ordinary Sky over `Cmd.perform` + `Http` + `Codec`
        // (sky-stdlib/Std/Spa.sky), so the taint walk would otherwise follow them
        // into `Http_*` and mark every branch that issues an explicit RPC SERVER
        // (issue #195). But `Std.Spa` IS the wasm-client framework — an explicit
        // `Spa.postJson`/`getJson` is the client SIDE of an author-drawn boundary
        // (the same fetch the generated frontend performs), not a server effect to
        // lift into a synthesized whole-model RPC. Treat every `Std.Spa` def as a
        // pure client leaf: a branch whose only server reach is `Spa.*` stays
        // CLIENT and its body is copied verbatim. This is SOUND — nothing in
        // `Std.Spa` touches a secret / DB / env; it only sends user data to a
        // URL. Raw `Http.*` (or `Db.*`, …) used DIRECTLY in `update` is unaffected
        // — that taints via the arm's own refs, never through this module.
        if db.module_name(loc.module) == "Std.Spa" {
            nodes.insert(
                def,
                DefNode {
                    direct: None,
                    callees: HashSet::new(),
                    // A pure client leaf — no run-time force (see the taint note).
                    forces: false,
                },
            );
            continue;
        }
        let resolved = db.resolve(loc.module);
        let Some(body) = resolved.bodies.get(&def) else {
            // A referenced def with no body in its module — opaque, conservative.
            nodes.insert(
                def,
                DefNode {
                    direct: Some("no body found (opaque -> conservative server)".into()),
                    callees: HashSet::new(),
                    // Body-less: might force. Fail the fence closed.
                    forces: true,
                },
            );
            continue;
        };
        let mut acc = Refs::default();
        if let Some(root) = body.root {
            // Conservative ctx: `update` is an ordinary callee here, so a helper
            // that calls `update` is forced to server (soundness — never under-
            // mark). Msg-constant precision applies ONLY to update's own arms.
            collect(body, root, &mut acc, &CollectCtx::default());
        }
        let direct = acc.direct_server_reason();
        let forces = acc.inline_force;
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
                forces,
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

    // forces-effect fixpoint (parallel to `server`): a def forces iff its OWN
    // body forces (`Task.run` / `let _ =`) OR any callee forces. Seeds on the
    // per-node `forces` flag. Same monotone least-fixpoint as `server`.
    let mut forces_effect: HashSet<DefId> =
        nodes.iter().filter(|(_, n)| n.forces).map(|(d, _)| *d).collect();
    loop {
        let mut changed = false;
        for (d, n) in &nodes {
            if forces_effect.contains(d) {
                continue;
            }
            if n.callees.iter().any(|c| forces_effect.contains(c)) {
                forces_effect.insert(*d);
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
        forces_effect,
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
    // lowerer consumes) — proves the analysis runs over TYPED hir, lets the
    // inline-force reason be precise, and gives the Model field list (from the
    // `( Model, Cmd msg )` result type) for the whole-model I/O over-approx.
    let types = ty::Typer::new(db).body_types(module, def, body);
    let model_fields = model_fields_from_result(&types.result);
    let model_local = model_param_local(body);
    // Source text of the update module, for slicing Msg-arg binder names out of
    // their pattern spans (a `Pattern::Var` carries a `LocalId`, not a name).
    let src = db.module_parse(module).syntax().text().to_string();
    if model_local.is_none() {
        notes.push(
            "could not identify `update`'s `model` parameter — server-branch read/write sets are over-approximated to the whole model.".into(),
        );
    }
    if model_fields.is_none() {
        notes.push(
            "could not recover the Model field list — whole-model I/O is shown without enumerating fields.".into(),
        );
    }

    let Some(root) = body.root else {
        return;
    };
    // Shared context along the spine above the case (top-level `let`s). The
    // conservative ctx here means a `let` above the case using `update` is
    // treated conservatively (server) — sound; precision is per-arm below.
    let mut shared = Refs::default();
    let case_expr = find_top_case(body, root, &mut shared, &CollectCtx::default());

    let Some(case_expr) = case_expr else {
        // No `case msg of` — classify the whole update as one unit.
        let mut acc = Refs::default();
        collect(body, root, &mut acc, &CollectCtx::default());
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
        // Msg-constant precision. `def` IS the `update` DefId — thread it in so a
        // direct `update <LiteralMsg> …` call in an arm composes another arm
        // (scoped) rather than dragging in `update`-as-a-whole (server). Helpers
        // keep the conservative treatment (build_graph uses `default()`), so this
        // never under-marks (§ soundness).
        let ctx = CollectCtx {
            db: Some(db),
            update_def: Some(def),
        };
        classify_case_arms(
            db, graph, body, arms, &shared, &ctx, model_local, &src, &types.locals, branches,
        );
    }
}

/// One arm's collected facts, before the arm-level fixpoint.
struct ArmFacts {
    /// Full pattern label for display (`GotTodos (Ok _)`).
    label: String,
    /// The arm's head Msg-ctor NAME, for keying composition. `None` for a
    /// non-ctor pattern (`_`, literal) — such an arm can never be a compose
    /// TARGET (a scoped call naming it would not resolve → conservative server).
    key: Option<String>,
    refs: Refs,
}

/// Classify each `case` arm with the arm-level server fixpoint (Msg-constant
/// precision). An arm is server iff it has a DIRECT server reason (own kernel /
/// FFI / non-`update` server callee / a generic `update` use) OR it scoped-calls
/// `update <S>` where arm `S` is server. Iterated to a fixpoint (arms compose
/// arms; cycles terminate). Match scoped-call names to arm keys by name.
#[allow(clippy::too_many_arguments)]
fn classify_case_arms(
    db: &dyn SkyDb,
    graph: &Graph,
    body: &Body,
    arms: &[hir::CaseBranch],
    shared: &Refs,
    ctx: &CollectCtx,
    model_local: Option<LocalId>,
    src: &str,
    locals: &HashMap<LocalId, ty::Ty>,
    out: &mut Vec<BranchVerdict>,
) {
    let facts: Vec<ArmFacts> = arms
        .iter()
        .map(|arm| {
            let mut acc = shared.clone();
            collect(body, arm.body, &mut acc, ctx);
            ArmFacts {
                label: pattern_label(body, arm.pat),
                key: arm_ctor_key(body, arm.pat),
                refs: acc,
            }
        })
        .collect();

    // Name → arm index (first wins; Msg ctors are unique per union anyway).
    let mut by_name: HashMap<String, usize> = HashMap::new();
    for (i, f) in facts.iter().enumerate() {
        if let Some(k) = &f.key {
            by_name.entry(k.clone()).or_insert(i);
        }
    }

    // Direct (non-compose) server reason per arm — independent of composition.
    let direct: Vec<Option<String>> = facts
        .iter()
        .map(|f| arm_direct_reason(db, &f.refs, graph))
        .collect();

    // Fixpoint: seed with direct-server arms, then propagate scoped composition.
    let n = facts.len();
    let mut server: Vec<bool> = direct.iter().map(|d| d.is_some()).collect();
    loop {
        let mut changed = false;
        for i in 0..n {
            if server[i] {
                continue;
            }
            let force = facts[i].refs.scoped_updates.iter().any(|s| match by_name.get(s) {
                Some(&j) => server[j],
                // A scoped Msg name that matches no arm → cannot resolve → be
                // conservative (server). Never under-mark.
                None => true,
            });
            if force {
                server[i] = true;
                changed = true;
            }
        }
        if !changed {
            break;
        }
    }

    // forces-effect per arm (the phase-2 fence). Seed: the arm's own body forces
    // (`inline_force`) OR a reachable callee forces (graph fixpoint). Then
    // propagate through scoped `update <LiteralMsg>` composition, exactly as the
    // `server` fixpoint above — composing an arm that forces means this arm forces
    // when it runs. An unresolved scoped name is conservatively a force (never
    // under-mark, mirroring the `server` treatment).
    let mut forces: Vec<bool> = facts
        .iter()
        .map(|f| f.refs.inline_force || f.refs.callees.iter().any(|c| graph.forces(*c)))
        .collect();
    loop {
        let mut changed = false;
        for i in 0..n {
            if forces[i] {
                continue;
            }
            let f = facts[i].refs.scoped_updates.iter().any(|s| match by_name.get(s) {
                Some(&j) => forces[j],
                None => true,
            });
            if f {
                forces[i] = true;
                changed = true;
            }
        }
        if !changed {
            break;
        }
    }

    // Emit verdicts with a helpful reason. SERVER branches also carry their
    // derived RPC read-set / write-set (B1); CLIENT branches need no I/O.
    for i in 0..n {
        let f = &facts[i];
        if let Some(r) = &direct[i] {
            out.push(BranchVerdict {
                msg: f.label.clone(),
                server: true,
                reason: r.clone(),
                io: Some(compute_branch_io(db, body, arms[i].body, arms[i].pat, model_local, src)),
                msg_arg_tys: msg_arg_field_tys(body, arms[i].pat, src, locals),
                forces_effect: forces[i],
            });
        } else if server[i] {
            out.push(BranchVerdict {
                msg: f.label.clone(),
                server: true,
                reason: compose_reason(&f.refs.scoped_updates, &by_name, &server, &direct),
                io: Some(compute_branch_io(db, body, arms[i].body, arms[i].pat, model_local, src)),
                msg_arg_tys: msg_arg_field_tys(body, arms[i].pat, src, locals),
                forces_effect: forces[i],
            });
        } else {
            let reason = match f.refs.client_effect_note() {
                Some(note) => format!("client — {note}, no server reach"),
                None => "pure — no server effect or tainted value".to_string(),
            };
            out.push(BranchVerdict {
                msg: f.label.clone(),
                server: false,
                reason,
                io: None,
                msg_arg_tys: Vec::new(),
                forces_effect: forces[i],
            });
        }
    }
}

// ---------------------------------------------------------------------------
// Server-internal effect chaining (Phase 1 classification + Phase 2 I/O union).
// ---------------------------------------------------------------------------

/// The result of the server-chaining analysis over `update`. Consumed by the
/// `spa-split` generator to (a) chain the triggering branches' RPC handlers,
/// (b) prune the server-internal Msgs from the frontend.
#[derive(Default)]
pub struct ServerChaining {
    /// Msgs to DROP from the frontend (client arm + union variant). See
    /// [`SpaPartitionReport::server_internal`].
    pub server_internal: Vec<String>,
    /// Server branch ctor names whose handler settles a chain server-side.
    pub chaining_branches: Vec<String>,
    /// PATTERN-2 client-result performs: `(root, result_msg)` pairs. See
    /// [`SpaPartitionReport::client_result`].
    pub client_result: Vec<(String, String)>,
    /// G5 fail-closed warnings — a branch with a `Cmd.perform` the analysis
    /// refused to chain (client effect, ambiguous ownership, opaque command).
    pub warnings: Vec<String>,
}

/// One leaf of a statically-resolved `Cmd` tree returned by an `update` arm.
#[derive(Clone, Debug)]
enum CmdLeaf {
    /// `Cmd.none` — no effect.
    NoneCmd,
    /// `Cmd.perform task toMsg`. `to_msg` is the toMsg's ctor NAME (`None` when
    /// it is not a resolvable single ctor); `task_client_effect` is true when the
    /// task reaches a `Std.Native` client effect (must run in the wasm client, so
    /// the branch cannot be chained server-side — fail closed).
    Perform {
        to_msg: Option<String>,
        task_client_effect: bool,
    },
    /// `Cmd.publish` / `Cmd.publishNoEcho` — a server→client push leaf (not a
    /// server-runnable read; a chain containing one is not chained).
    Publish,
    /// A shape the static resolver could not read (an opaque let-bound Cmd, a
    /// helper returning a Cmd, a non-list batch). Forces fail-closed.
    Unresolvable,
}

/// The `Cmd` constructor a def is a kernel alias to.
enum CmdDefKind {
    Perform,
    Batch,
    NoneCmd,
    Publish,
    Other,
}

fn cmd_def_kind(db: &dyn SkyDb, d: DefId) -> CmdDefKind {
    if def_is_kernel_alias_to(db, d, &["Cmd_perform"]) {
        CmdDefKind::Perform
    } else if def_is_kernel_alias_to(db, d, &["Cmd_batch"]) {
        CmdDefKind::Batch
    } else if def_is_kernel_alias_to(db, d, &["Cmd_none"]) {
        CmdDefKind::NoneCmd
    } else if def_is_kernel_alias_to(db, d, &["Cmd_publish", "Cmd_publishNoEcho"]) {
        CmdDefKind::Publish
    } else {
        CmdDefKind::Other
    }
}

/// Looks THROUGH a guard/HOF wrapper —
/// the `requireAdmin model (\_ -> ( model, cmd ))` shape (darraghstudio), where
/// the `( model, cmd )` pair is returned from the LAST-argument thunk rather than
/// the arm's own tail. This walk is used ONLY by the pattern-2 (client-result)
/// detection, so the direct-tuple chaining analysis (pattern-1) is untouched: a
/// branch pattern-1 already settles keeps its exact classification. FAIL-CLOSED:
/// only a `Call` whose FINAL argument is a `\… -> …` lambda is treated as a
/// wrapper (its body walked); any other call shape contributes nothing (the
/// branch stays a plain wire branch).
fn collect_guarded_tail_cmd_exprs(body: &Body, e: ExprId, out: &mut Vec<ExprId>) {
    match &body.exprs[e] {
        Expr::Tuple(xs) if xs.len() == 2 => out.push(xs[1]),
        Expr::Let { body: b, .. } => collect_guarded_tail_cmd_exprs(body, *b, out),
        Expr::If { arms, els } => {
            for (_, t) in arms {
                collect_guarded_tail_cmd_exprs(body, *t, out);
            }
            collect_guarded_tail_cmd_exprs(body, *els, out);
        }
        Expr::Case { branches, .. } => {
            for br in branches {
                collect_guarded_tail_cmd_exprs(body, br.body, out);
            }
        }
        // A guard/HOF wrapper: `guard model (\_ -> ( model, cmd ))`. The returned
        // pair lives in the LAST argument's thunk body — walk it.
        Expr::Call(_, args) => {
            if let Some(last) = args.last() {
                if let Expr::Lambda { body: lb, .. } = &body.exprs[*last] {
                    collect_guarded_tail_cmd_exprs(body, *lb, out);
                }
            }
        }
        Expr::Lambda { body: b, .. } => collect_guarded_tail_cmd_exprs(body, *b, out),
        _ => {}
    }
}

/// Statically resolve a command expression into its leaves. FAIL-CLOSED: any
/// shape the resolver cannot read becomes [`CmdLeaf::Unresolvable`], which stops
/// the triggering branch from being chained (never a silently-dropped write).
fn resolve_cmd_leaves(db: &dyn SkyDb, body: &Body, e: ExprId, out: &mut Vec<CmdLeaf>) {
    let mut visited: HashSet<DefId> = HashSet::new();
    resolve_cmd_leaves_rec(db, body, e, out, 0, &mut visited);
}

/// The recursion ceiling for command resolution through helper defs (a chain of
/// `Cmd`-returning helpers). Deeper than this falls back to `Unresolvable`.
const CMD_RESOLVE_DEPTH: usize = 12;

fn resolve_cmd_leaves_rec(
    db: &dyn SkyDb,
    body: &Body,
    e: ExprId,
    out: &mut Vec<CmdLeaf>,
    depth: usize,
    visited: &mut HashSet<DefId>,
) {
    if depth > CMD_RESOLVE_DEPTH {
        out.push(CmdLeaf::Unresolvable);
        return;
    }
    match &body.exprs[e] {
        Expr::Call(callee, args) => {
            if let Expr::Var(Res::Def(d)) = &body.exprs[*callee] {
                match cmd_def_kind(db, *d) {
                    CmdDefKind::Perform => {
                        if args.len() == 2 {
                            let to_msg = literal_ctor_name(body, db, args[1]);
                            let mut tref = Refs::default();
                            collect(body, args[0], &mut tref, &CollectCtx::default());
                            let client = task_refs_client_effect(db, &tref);
                            out.push(CmdLeaf::Perform {
                                to_msg,
                                task_client_effect: client,
                            });
                        } else {
                            out.push(CmdLeaf::Unresolvable);
                        }
                    }
                    CmdDefKind::Batch => {
                        if args.len() == 1 {
                            if let Expr::List(xs) = &body.exprs[args[0]] {
                                for x in xs {
                                    resolve_cmd_leaves_rec(db, body, *x, out, depth, visited);
                                }
                                return;
                            }
                        }
                        // A batch over anything but a literal list is opaque.
                        out.push(CmdLeaf::Unresolvable);
                    }
                    CmdDefKind::NoneCmd => out.push(CmdLeaf::NoneCmd),
                    CmdDefKind::Publish => out.push(CmdLeaf::Publish),
                    // A HELPER returning a `Cmd` (e.g. `shippedCmd o = Cmd.perform
                    // (Mailer.sendShipped o) EmailSent`) — resolve INTO its body so
                    // the perform edge is seen, not treated as opaque.
                    CmdDefKind::Other => resolve_helper_cmd(db, *d, out, depth + 1, visited),
                }
                return;
            }
            out.push(CmdLeaf::Unresolvable);
        }
        // A bare reference: `Cmd.none`, or a nullary `Cmd`-returning helper.
        Expr::Var(Res::Def(d)) => match cmd_def_kind(db, *d) {
            CmdDefKind::NoneCmd => out.push(CmdLeaf::NoneCmd),
            CmdDefKind::Other => resolve_helper_cmd(db, *d, out, depth + 1, visited),
            _ => out.push(CmdLeaf::Unresolvable),
        },
        // Control flow that a `Cmd`-returning helper body threads through: resolve
        // every tail branch as a command.
        Expr::Case { branches, .. } => {
            for br in branches {
                resolve_cmd_leaves_rec(db, body, br.body, out, depth, visited);
            }
        }
        Expr::If { arms, els } => {
            for (_, t) in arms {
                resolve_cmd_leaves_rec(db, body, *t, out, depth, visited);
            }
            resolve_cmd_leaves_rec(db, body, *els, out, depth, visited);
        }
        Expr::Let { body: b, .. } => resolve_cmd_leaves_rec(db, body, *b, out, depth, visited),
        _ => out.push(CmdLeaf::Unresolvable),
    }
}

/// Resolve a helper def `d` (whose result is a `Cmd msg`) into its command
/// leaves, by walking its own body. Cycle- and depth-bounded (fail-closed to
/// `Unresolvable`). `Std.Cmd` / `Std.Spa` helpers are not walked here — those are
/// caught by `cmd_def_kind` (the kernel aliases) before reaching this.
fn resolve_helper_cmd(
    db: &dyn SkyDb,
    d: DefId,
    out: &mut Vec<CmdLeaf>,
    depth: usize,
    visited: &mut HashSet<DefId>,
) {
    if !visited.insert(d) || depth > CMD_RESOLVE_DEPTH {
        out.push(CmdLeaf::Unresolvable);
        return;
    }
    let Some(loc) = db.def_loc(d) else {
        out.push(CmdLeaf::Unresolvable);
        return;
    };
    let resolved = db.resolve(loc.module);
    let Some(hbody) = resolved.bodies.get(&d) else {
        out.push(CmdLeaf::Unresolvable);
        return;
    };
    let Some(root) = hbody.root else {
        out.push(CmdLeaf::Unresolvable);
        return;
    };
    resolve_cmd_leaves_rec(db, hbody, root, out, depth, visited);
    // Path-scoped cycle guard: a sibling call to the SAME helper (a `Cmd.batch`
    // with two `shippedCmd` calls) must still resolve — only a self-referential
    // cycle is blocked.
    visited.remove(&d);
}

/// Collect the tail-position command LEAVES of an arm/helper body, each TAGGED
/// with whether it was reached THROUGH a higher-order guard wrapper. This is the
/// TRANSITIVE twin of [`collect_tail_cmd_exprs_tagged`] + [`resolve_cmd_leaves`]:
/// it follows the SAME tail scaffolding (tuple / let / if / case / guard-wrapper
/// lambda) AND, additionally, a WHOLE-ARM helper delegate `f a0 … an` that returns
/// `( model, cmd )` — crossing INTO `f`'s body to find its OWN tail command. That
/// last case is what lets a continuation whose arm delegates to a helper (the
/// darraghstudio `handleFinalize` / `createOrder` shape, the `record v model`
/// fixture) contribute its FURTHER perform continuations, so a 3+-hop all-server
/// chain settles transitively. Depth- and cycle-bounded (a helper already on the
/// path, or a delegation chain deeper than `CMD_RESOLVE_DEPTH`, yields
/// [`CmdLeaf::Unresolvable`] — fail-closed). FAIL-CLOSED on every unrecognised
/// shape: it contributes NOTHING, so the caller sees no leaf and treats the arm
/// as dirty, exactly as before this change. The guard-wrapper branch is checked
/// BEFORE the whole-arm-delegate branch, so a `guard model (\_ -> …)` keeps its
/// stricter (guarded) treatment and a plain delegate `record v model` is crossed.
fn collect_tail_cmd_leaves_tagged(
    db: &dyn SkyDb,
    body: &Body,
    e: ExprId,
    guarded: bool,
    out: &mut Vec<(CmdLeaf, bool)>,
    depth: usize,
    visited: &mut HashSet<DefId>,
) {
    if depth > CMD_RESOLVE_DEPTH {
        out.push((CmdLeaf::Unresolvable, guarded));
        return;
    }
    match &body.exprs[e] {
        // A DIRECT `( model, cmd )` tail — resolve the command's leaves (which
        // already follows `Cmd`-returning helpers + literal-list batches), each
        // tagged with the current guarded state.
        Expr::Tuple(xs) if xs.len() == 2 => {
            let mut leaves: Vec<CmdLeaf> = Vec::new();
            resolve_cmd_leaves(db, body, xs[1], &mut leaves);
            for l in leaves {
                out.push((l, guarded));
            }
        }
        Expr::Let { body: b, .. } => {
            collect_tail_cmd_leaves_tagged(db, body, *b, guarded, out, depth, visited)
        }
        Expr::If { arms, els } => {
            for (_, t) in arms {
                collect_tail_cmd_leaves_tagged(db, body, *t, guarded, out, depth, visited);
            }
            collect_tail_cmd_leaves_tagged(db, body, *els, guarded, out, depth, visited);
        }
        Expr::Case { branches, .. } => {
            for br in branches {
                collect_tail_cmd_leaves_tagged(db, body, br.body, guarded, out, depth, visited);
            }
        }
        Expr::Lambda { body: b, .. } => {
            collect_tail_cmd_leaves_tagged(db, body, *b, guarded, out, depth, visited)
        }
        Expr::Call(callee, args) => {
            // A guard/HOF wrapper: `guard model (\_ -> ( model, cmd ))` — the pair
            // lives in the LAST argument's thunk body; walk it, TAGGED guarded.
            if let Some(last) = args.last() {
                if let Expr::Lambda { body: lb, .. } = &body.exprs[*last] {
                    collect_tail_cmd_leaves_tagged(db, body, *lb, true, out, depth, visited);
                    return;
                }
            }
            // A WHOLE-ARM delegate `f a0 … an` returning `( model, cmd )` — cross
            // INTO `f`'s body and resolve ITS tail command. Preserves `guarded`:
            // a delegate reached from inside a guard thunk stays guarded.
            if let Expr::Var(Res::Def(d)) = &body.exprs[*callee] {
                collect_delegate_tail_cmd_leaves(db, *d, guarded, out, depth + 1, visited);
                return;
            }
            // Any other call shape contributes nothing (fail-closed).
        }
        _ => {}
    }
}

/// Cross into a WHOLE-ARM delegate helper `d` (which returns `( model, cmd )`) and
/// resolve its OWN tail command leaves. Path-scoped cycle guard + depth ceiling
/// mirror [`resolve_helper_cmd`]; a body we cannot read yields
/// [`CmdLeaf::Unresolvable`] (fail-closed).
fn collect_delegate_tail_cmd_leaves(
    db: &dyn SkyDb,
    d: DefId,
    guarded: bool,
    out: &mut Vec<(CmdLeaf, bool)>,
    depth: usize,
    visited: &mut HashSet<DefId>,
) {
    if !visited.insert(d) || depth > CMD_RESOLVE_DEPTH {
        out.push((CmdLeaf::Unresolvable, guarded));
        return;
    }
    let resolved = db.def_loc(d).map(|loc| db.resolve(loc.module));
    match resolved
        .as_ref()
        .and_then(|r| r.bodies.get(&d))
        .and_then(|b| b.root.map(|root| (b, root)))
    {
        Some((hbody, root)) => {
            collect_tail_cmd_leaves_tagged(db, hbody, root, guarded, out, depth, visited)
        }
        None => out.push((CmdLeaf::Unresolvable, guarded)),
    }
    // Path-scoped: a sibling delegate to the SAME helper still resolves.
    visited.remove(&d);
}

/// Whether a task's collected refs reach a `Std.Native` CLIENT effect — directly
/// (`client_kernels`) or transitively through a callee. A client effect cannot
/// run server-side (its `!js` stub returns `Err`), so a chain containing one is
/// NOT chained (G5 fail-closed).
fn task_refs_client_effect(db: &dyn SkyDb, tref: &Refs) -> bool {
    if !tref.client_kernels.is_empty() {
        return true;
    }
    let mut visited: HashSet<DefId> = HashSet::new();
    tref.callees
        .iter()
        .any(|c| def_reaches_client_effect(db, *c, &mut visited))
}

/// Whether evaluating `d`'s body reaches a `Std.Native` CLIENT effect through its
/// callees — the client-effect twin of [`def_reaches_genuine_effect`].
fn def_reaches_client_effect(db: &dyn SkyDb, d: DefId, visited: &mut HashSet<DefId>) -> bool {
    if !visited.insert(d) {
        return false;
    }
    let Some(loc) = db.def_loc(d) else {
        return false;
    };
    // `Std.Spa`'s client-boundary helpers are pure client leaves (mirrors
    // build_graph) — never a Native effect.
    if db.module_name(loc.module) == "Std.Spa" {
        return false;
    }
    let resolved = db.resolve(loc.module);
    let Some(body) = resolved.bodies.get(&d) else {
        return false;
    };
    let Some(root) = body.root else {
        return false;
    };
    let mut acc = Refs::default();
    collect(body, root, &mut acc, &CollectCtx::default());
    if !acc.client_kernels.is_empty() {
        return true;
    }
    acc.callees
        .iter()
        .any(|c| def_reaches_client_effect(db, *c, visited))
}

/// Record every constructor NAME (`Res::Ctor`) built directly in `e`'s subtree.
fn walk_ctor_names(db: &dyn SkyDb, body: &Body, e: ExprId, out: &mut BTreeSet<String>) {
    match &body.exprs[e] {
        Expr::Var(Res::Ctor(c)) => {
            if let Some(l) = db.def_loc(c.def) {
                out.insert(l.name.as_str().to_string());
            }
        }
        Expr::List(xs) | Expr::Tuple(xs) => {
            for x in xs {
                walk_ctor_names(db, body, *x, out);
            }
        }
        Expr::Record(fields) => {
            for (_, x) in fields {
                walk_ctor_names(db, body, *x, out);
            }
        }
        Expr::Update { base, fields } => {
            walk_ctor_names(db, body, *base, out);
            for (_, x) in fields {
                walk_ctor_names(db, body, *x, out);
            }
        }
        Expr::Negate(x) | Expr::Access(x, _) => walk_ctor_names(db, body, *x, out),
        Expr::Lambda { body: b, .. } => walk_ctor_names(db, body, *b, out),
        Expr::Call(callee, args) => {
            walk_ctor_names(db, body, *callee, out);
            for a in args {
                walk_ctor_names(db, body, *a, out);
            }
        }
        Expr::Binop { lhs, rhs, .. } => {
            walk_ctor_names(db, body, *lhs, out);
            walk_ctor_names(db, body, *rhs, out);
        }
        Expr::If { arms, els } => {
            for (c, t) in arms {
                walk_ctor_names(db, body, *c, out);
                walk_ctor_names(db, body, *t, out);
            }
            walk_ctor_names(db, body, *els, out);
        }
        Expr::Let { defs, body: b } => {
            for d in defs {
                walk_ctor_names(db, body, d.body, out);
            }
            walk_ctor_names(db, body, *b, out);
        }
        Expr::Case { subject, branches } => {
            walk_ctor_names(db, body, *subject, out);
            for br in branches {
                walk_ctor_names(db, body, br.body, out);
            }
        }
        _ => {}
    }
}

/// Every constructor a top-level def BUILDS, transitively through its callees.
/// Over-approximates (follows every reachable def) — the set is used to KEEP a
/// Msg client-side, so including more only keeps more (never drops a live arm).
fn collect_constructed_ctors(
    db: &dyn SkyDb,
    def: DefId,
    out: &mut BTreeSet<String>,
    visited: &mut HashSet<DefId>,
) {
    if !visited.insert(def) {
        return;
    }
    let Some(loc) = db.def_loc(def) else {
        return;
    };
    let resolved = db.resolve(loc.module);
    let Some(body) = resolved.bodies.get(&def) else {
        return;
    };
    let Some(root) = body.root else {
        return;
    };
    walk_ctor_names(db, body, root, out);
    let mut acc = Refs::default();
    collect(body, root, &mut acc, &CollectCtx::default());
    for c in acc.callees {
        collect_constructed_ctors(db, c, out, visited);
    }
}

/// Every constructor built by an EXPRESSION (a client arm body), transitively.
fn expr_constructed_ctors(db: &dyn SkyDb, body: &Body, e: ExprId, out: &mut BTreeSet<String>) {
    walk_ctor_names(db, body, e, out);
    let mut acc = Refs::default();
    collect(body, e, &mut acc, &CollectCtx::default());
    let mut visited: HashSet<DefId> = HashSet::new();
    for c in acc.callees {
        collect_constructed_ctors(db, c, out, &mut visited);
    }
}

/// Phase 1 + Phase 2: classify the server-internal Msgs and widen the chaining
/// branches' RPC I/O to the UNION over their server-internal continuation arms.
///
/// A Msg is SERVER-INTERNAL iff it is the `toMsg` of a server arm's
/// `Cmd.perform`/`Cmd.batch` AND it is constructed NOWHERE on the client (not
/// in `view`, `subscriptions`, nor any client arm) AND it is not itself a wire
/// (server RPC) branch. A server branch CHAINS iff its whole transitive command
/// chain is statically resolvable, contains ≥1 perform, and contains no publish
/// leaf, no client-effect perform, and no ambiguous continuation (a toMsg that
/// is a wire branch or is also client-constructed). FAIL-CLOSED throughout: any
/// shape the analysis cannot fully resolve leaves the branch UNCHAINED (today's
/// behaviour) with a loud warning, and keeps every Msg it could not prove
/// server-internal OUT of the drop set.
#[allow(clippy::too_many_arguments)]
fn compute_server_chaining(
    db: &skydb::SkyDatabase,
    umod: ModuleId,
    body: &Body,
    view_def: Option<DefId>,
    subs_def: Option<DefId>,
    branches: &mut [BranchVerdict],
) -> ServerChaining {
    let mut out = ServerChaining::default();
    let Some(root) = body.root else {
        return out;
    };
    // Locate the update's `case msg of` (same spine walk classify uses).
    let mut throwaway = Refs::default();
    let Some(case_expr) = find_top_case(body, root, &mut throwaway, &CollectCtx::default()) else {
        return out;
    };
    let Expr::Case { branches: arms, .. } = &body.exprs[case_expr] else {
        return out;
    };
    let model_local = model_param_local(body);
    let src = db.module_parse(umod).syntax().text().to_string();

    // Head ctor → arm indices (a Msg like `Reloaded` owns its Ok/Err arms).
    let mut arms_by_ctor: HashMap<String, Vec<usize>> = HashMap::new();
    for (i, arm) in arms.iter().enumerate() {
        if let Some(k) = arm_ctor_key(body, arm.pat) {
            arms_by_ctor.entry(k).or_default().push(i);
        }
    }

    // Every Msg head that has ≥1 `update` arm.
    let all_heads: Vec<String> = arms_by_ctor.keys().cloned().collect();

    // `client_dispatched` — every Msg the CLIENT can construct: built by `view`,
    // `subscriptions`, or a CLIENT-classified arm body (all transitive). This is
    // the authority on "the client dispatches this Msg". Over-approximated, so a
    // Msg wrongly kept out of the drop set is safe; a Msg wrongly pruned would
    // break client dispatch, so we include MORE here, never fewer.
    let server_head_set: HashSet<String> = branches
        .iter()
        .filter(|b| b.server)
        .map(|b| pattern_head_ctor(&b.msg))
        .collect();
    let mut client_dispatched: BTreeSet<String> = BTreeSet::new();
    let mut vis: HashSet<DefId> = HashSet::new();
    if let Some(v) = view_def {
        collect_constructed_ctors(db, v, &mut client_dispatched, &mut vis);
    }
    if let Some(s) = subs_def {
        collect_constructed_ctors(db, s, &mut client_dispatched, &mut vis);
    }
    for arm in arms.iter() {
        let head = arm_ctor_key(body, arm.pat);
        let is_server = head.as_ref().map(|h| server_head_set.contains(h)).unwrap_or(false);
        if !is_server {
            // A CLIENT arm (or a non-ctor arm) — the Msgs it constructs are
            // client-dispatched (a client re-dispatch).
            expr_constructed_ctors(db, body, arm.body, &mut client_dispatched);
        }
    }

    // Per-head command shape: the clean perform continuations, and whether the
    // head is "dirty" (a shape that cannot fully settle server-side).
    struct HeadInfo {
        clean_conts: Vec<String>,
        dirty: bool,
        has_perform: bool,
    }
    let head_info = |head: &str| -> HeadInfo {
        let mut clean_conts: Vec<String> = Vec::new();
        let mut dirty = false;
        let mut has_perform = false;
        if let Some(idxs) = arms_by_ctor.get(head) {
            for &ai in idxs {
                // Tagged tail-cmd walk: a DIRECT `( model, cmd )` pair is tagged
                // `false`; a pair returned THROUGH a guard/HOF wrapper (`GUARD
                // model (\_ -> …)`) is tagged `true`. A direct perform keeps
                // today's exact rule; a guarded perform feeds the chain ONLY when
                // its continuation is a SERVER head, so an all-server guard-wrapped
                // chain settles while a guard-wrapped CLIENT-result perform is
                // left untouched for pattern-2.
                // TRANSITIVE tail-cmd resolution: follows the SAME tail
                // scaffolding as the direct walk PLUS a whole-arm helper delegate
                // `record v model` returning `( model, cmd )` — crossing into the
                // helper so a continuation whose own arm spawns further performs
                // (a 3+-hop chain) contributes its leaves rather than reading as
                // an empty (dirty) pair. Fail-closed: an unresolvable delegate
                // yields `Unresolvable`, still dirty.
                let mut cmd_leaves: Vec<(CmdLeaf, bool)> = Vec::new();
                let mut cmd_visited: HashSet<DefId> = HashSet::new();
                collect_tail_cmd_leaves_tagged(
                    db,
                    body,
                    arms[ai].body,
                    false,
                    &mut cmd_leaves,
                    0,
                    &mut cmd_visited,
                );
                if cmd_leaves.is_empty() {
                    dirty = true; // no isolable `( model, cmd )` pair
                    continue;
                }
                // `arm_contributed` — the arm fed at least one leaf into the
                // pattern-1 analysis. `arm_guarded_skip` — the arm had a guarded
                // perform we deliberately left for pattern-2. A guarded-only arm
                // that contributes nothing keeps the pre-change `dirty`: the
                // direct walk saw no pair before this change, so it was dirty.
                let mut arm_contributed = false;
                let mut arm_guarded_skip = false;
                {
                    for (leaf, guarded) in cmd_leaves {
                        match leaf {
                            CmdLeaf::NoneCmd => arm_contributed = true,
                            CmdLeaf::Publish | CmdLeaf::Unresolvable => {
                                dirty = true;
                                arm_contributed = true;
                            }
                            CmdLeaf::Perform { to_msg, task_client_effect } if !guarded => {
                                // DIRECT perform — today's rule, unchanged.
                                has_perform = true;
                                arm_contributed = true;
                                match to_msg {
                                    _ if task_client_effect => dirty = true,
                                    None => dirty = true,
                                    Some(m) => {
                                        // A perform to a client-dispatched Msg is
                                        // ambiguous ownership — the chain escapes to
                                        // the client, so it cannot settle server-side.
                                        if client_dispatched.contains(&m) {
                                            dirty = true;
                                        } else {
                                            clean_conts.push(m);
                                        }
                                    }
                                }
                            }
                            CmdLeaf::Perform { to_msg, task_client_effect } => {
                                // GUARDED perform. It joins the server-side chain
                                // ONLY when the continuation is a resolvable,
                                // unambiguous SERVER head (reaches a server effect).
                                // Any other guarded shape — a client-pure result
                                // Msg, a client `Std.Native` task, a client-
                                // dispatched (ambiguous) Msg, or an opaque `toMsg`
                                // — is left EXACTLY as before this change: invisible
                                // to pattern-1, so a client-result guarded perform
                                // still reaches pattern-2 and a fail-closed shape
                                // stays a wire branch.
                                match to_msg {
                                    Some(m)
                                        if !task_client_effect
                                            && !client_dispatched.contains(&m)
                                            && server_head_set.contains(&m) =>
                                    {
                                        has_perform = true;
                                        arm_contributed = true;
                                        clean_conts.push(m);
                                    }
                                    _ => arm_guarded_skip = true,
                                }
                            }
                        }
                    }
                }
                // Preserve the pre-change classification for a guarded-only arm
                // whose perform we deliberately skipped: the direct walk saw no
                // pair, so it was `dirty`.
                if !arm_contributed && arm_guarded_skip {
                    dirty = true;
                }
            }
        }
        HeadInfo { clean_conts, dirty, has_perform }
    };
    let mut info: HashMap<String, HeadInfo> = HashMap::new();
    for h in &all_heads {
        info.insert(h.clone(), head_info(h));
    }

    // `is_continuation` — a Msg dispatched by SOME head's clean perform. Reload is
    // NOT a continuation (nothing performs to it), so it stays a wire entry;
    // EmailSent IS (Cmd.perform … EmailSent), so it can become server-internal.
    let mut is_continuation: HashSet<String> = HashSet::new();
    for hi in info.values() {
        for c in &hi.clean_conts {
            is_continuation.insert(c.clone());
        }
    }

    // `settleable` (S) — the greatest set of Msgs that fully settle server-side: a
    // continuation, not client-dispatched, not dirty, and whose own clean
    // continuations are all settleable. Iterative removal to the fixpoint.
    let mut settleable: HashSet<String> = all_heads
        .iter()
        .filter(|h| {
            is_continuation.contains(*h)
                && !client_dispatched.contains(*h)
                && info.get(*h).map(|i| !i.dirty).unwrap_or(false)
        })
        .cloned()
        .collect();
    loop {
        let mut changed = false;
        let current: Vec<String> = settleable.iter().cloned().collect();
        for h in current {
            let escapes = info
                .get(&h)
                .map(|i| i.clean_conts.iter().any(|c| !settleable.contains(c)))
                .unwrap_or(true);
            if escapes {
                settleable.remove(&h);
                changed = true;
            }
        }
        if !changed {
            break;
        }
    }

    // Chain ROOTS = wire entries (a server head that is client-dispatched OR is
    // not a clean continuation of any head) whose whole chain settles. From each
    // root, every reachable continuation is server-internal.
    let mut server_internal: BTreeSet<String> = BTreeSet::new();
    let mut io_updates: HashMap<String, BranchIo> = HashMap::new();
    for bn in server_head_set.iter() {
        let is_wire_entry = client_dispatched.contains(bn) || !is_continuation.contains(bn);
        if !is_wire_entry {
            // A mid-chain server head (server-internal, reached via a root's BFS).
            continue;
        }
        let hi = match info.get(bn) {
            Some(i) => i,
            None => continue,
        };
        if !hi.has_perform {
            continue; // a plain server RPC — nothing to chain.
        }
        let clean = !hi.dirty && hi.clean_conts.iter().all(|c| settleable.contains(c));
        if !clean {
            out.warnings.push(format!(
                "server branch `{bn}` returns a `Cmd.perform` the auto-split could NOT chain server-side (a client `Std.Native` effect, an ambiguous continuation also dispatched on the client, or an opaque command shape). Its follow-up effect runs NOWHERE — keep the branch to a single server round, or split the follow-up into its own explicit RPC."
            ));
            continue;
        }
        // Chainable root: BFS its clean continuations (all in `settleable`),
        // collecting the server-internal set + the reachable arms for the I/O union.
        out.chaining_branches.push(bn.clone());
        let mut reachable_arms: BTreeSet<usize> =
            arms_by_ctor.get(bn).map(|v| v.iter().copied().collect()).unwrap_or_default();
        let mut visited: HashSet<String> = HashSet::new();
        visited.insert(bn.clone());
        let mut queue: Vec<String> = hi.clean_conts.clone();
        while let Some(m) = queue.pop() {
            if !visited.insert(m.clone()) {
                continue;
            }
            server_internal.insert(m.clone());
            if let Some(idxs) = arms_by_ctor.get(&m) {
                for &j in idxs {
                    reachable_arms.insert(j);
                }
            }
            if let Some(mi) = info.get(&m) {
                for c in &mi.clean_conts {
                    queue.push(c.clone());
                }
            }
        }
        // Union the I/O over every reachable arm (the root PLUS every reachable
        // server-internal continuation arm). Widen to whole model on any opaque
        // use — never narrow (soundness): a dropped write is a correctness bug.
        let mut io = BranchIo::default();
        for &j in &reachable_arms {
            let arm_io = compute_branch_io(db, body, arms[j].body, arms[j].pat, model_local, &src);
            io.reads_whole_model |= arm_io.reads_whole_model;
            io.writes_whole_model |= arm_io.writes_whole_model;
            for f in arm_io.read_fields {
                if !io.read_fields.contains(&f) {
                    io.read_fields.push(f);
                }
            }
            for f in arm_io.write_fields {
                if !io.write_fields.contains(&f) {
                    io.write_fields.push(f);
                }
            }
        }
        io.read_fields.sort();
        io.write_fields.sort();
        io_updates.insert(bn.clone(), io);
    }

    // Apply the widened I/O to the chaining server branches (preserving their own
    // msg_args — the request inputs; a continuation's args come from the task
    // result, not the client).
    for b in branches.iter_mut() {
        if !b.server {
            continue;
        }
        let head = pattern_head_ctor(&b.msg);
        if let Some(io) = io_updates.get(&head) {
            if let Some(existing) = &mut b.io {
                existing.reads_whole_model = io.reads_whole_model;
                existing.writes_whole_model = io.writes_whole_model;
                existing.read_fields = io.read_fields.clone();
                existing.write_fields = io.write_fields.clone();
            }
        }
    }

    // PATTERN-2 (client-result perform). A server branch pattern-1 did NOT settle
    // (its `Cmd.perform` is returned THROUGH a guard/HOF wrapper, so the direct
    // tail walk missed it and it fell to a plain wire branch, with the perform
    // effect dropped) whose single command is `Cmd.perform serverTask ResultMsg`
    // with a SERVER task and a CLIENT-pure `ResultMsg`. The `ResultMsg` result
    // must cross to the client and be dispatched there — the effect runs
    // server-side inside the root's RPC, its RESULT is returned, and the frontend
    // `Applied<root>` dispatches `ResultMsg result`. This is DISTINCT from
    // pattern-1 (a direct-tuple chain settling server-side, untouched above) and
    // from a plain wire branch. FAIL-CLOSED: any shape that is not a single clean
    // server perform to a client-pure result Msg is left exactly as today.
    let already_owned: HashSet<String> = out
        .chaining_branches
        .iter()
        .cloned()
        .chain(server_internal.iter().cloned())
        .collect();
    for bn in server_head_set.iter() {
        if already_owned.contains(bn) {
            continue; // pattern-1 (chaining root or mid-chain continuation) owns it.
        }
        // The command, seen through a guard/HOF wrapper. Empty (or a non-wrapper
        // shape) → nothing to reclassify.
        let idxs = match arms_by_ctor.get(bn) {
            Some(v) => v,
            None => continue,
        };
        let mut cmd_exprs: Vec<ExprId> = Vec::new();
        for &ai in idxs {
            collect_guarded_tail_cmd_exprs(body, arms[ai].body, &mut cmd_exprs);
        }
        if cmd_exprs.is_empty() {
            continue;
        }
        let mut leaves: Vec<CmdLeaf> = Vec::new();
        for ce in &cmd_exprs {
            resolve_cmd_leaves(db, body, *ce, &mut leaves);
        }
        // Require EXACTLY ONE perform, every other leaf a no-op. A publish, an
        // unresolvable shape, a second perform, or a client-`Std.Native` task →
        // fail closed (not a clean single server perform).
        let mut result_msg: Option<String> = None;
        let mut clean = true;
        let mut perform_count = 0usize;
        for leaf in &leaves {
            match leaf {
                CmdLeaf::NoneCmd => {}
                CmdLeaf::Perform { to_msg, task_client_effect } => {
                    perform_count += 1;
                    if *task_client_effect {
                        clean = false; // a client Std.Native task cannot run server-side.
                    }
                    match to_msg {
                        Some(m) => result_msg = Some(m.clone()),
                        None => clean = false,
                    }
                }
                CmdLeaf::Publish | CmdLeaf::Unresolvable => clean = false,
            }
        }
        if !clean || perform_count != 1 {
            continue;
        }
        let rm = match result_msg {
            Some(m) => m,
            None => continue,
        };
        // The result Msg must be a real arm.
        if !arms_by_ctor.contains_key(&rm) {
            continue;
        }
        // FAIL-CLOSED: a result Msg whose OWN arm reaches a server effect is a
        // DEEPER chain (out of pattern-2's scope). Handing its RESULT to the
        // client would run that server effect in the wasm client — forbidden. Keep
        // today's behaviour and warn.
        if server_head_set.contains(&rm) {
            out.warnings.push(format!(
                "server branch `{bn}` performs a server task whose result Msg `{rm}` is ALSO a server arm (it reaches a Db/File/… effect) — a deeper chain the auto-split does not settle. Its follow-up effect runs NOWHERE. Split `{rm}`'s server work into its own explicit RPC, or keep `{bn}` to a single server round."
            ));
            continue;
        }
        out.client_result.push((bn.clone(), rm));
    }
    out.client_result.sort();
    out.client_result.dedup();

    out.server_internal = server_internal.into_iter().collect();
    out.chaining_branches.sort();
    out.chaining_branches.dedup();
    out
}

/// The head constructor name of a branch label (`"Reloaded (Ok raw)"` →
/// `"Reloaded"`), the first whitespace-delimited token.
fn pattern_head_ctor(label: &str) -> String {
    label.split_whitespace().next().unwrap_or(label).to_string()
}

// ---------------------------------------------------------------------------
// B1 — per-server-branch read-set / write-set (the RPC I/O).
// ---------------------------------------------------------------------------

/// The recursion ceiling for cross-def I/O inference (field-preserving-helper
/// resolution, whole-arm delegation, let-alias chasing). A chain deeper than this
/// falls back to the sound over-approximation (whole model) rather than looping
/// on a mutually recursive helper.
const IO_DELEGATE_DEPTH: usize = 8;

/// Compute the RPC read-set / write-set for ONE `update` arm (§13-§14). Walks
/// the arm body over the SAME HIR the verdict used:
///   * read-set  = every `field` in `Access(Var(model), field)`, PLUS the Msg
///     args the arm pattern binds, PLUS the read-set a pure `Model -> X` helper
///     the arm passes the bare model to reads (inherited, e.g. an accessor).
///   * write-set = every `field` key of a tail `Update { base = <field-preserving>,
///     … }`. A tail model built by a chain of field-preserving `Model -> Model`
///     helpers (`noteLog (stamp { model | … })`) narrows to the UNION of the
///     fields each link rewrites; a whole-arm delegate `handle model` inherits the
///     helper's tail write-set; a let-bound `(model', cmd)` returned by name is
///     resolved to its binding.
///   * OVER-APPROXIMATE to the whole Model (sound) when `model` is used opaquely
///     — any `Var(model)` that is NOT the base of an `Access`/`Update`, not a
///     provable field-preserving transform, nor a bare model returned in the final
///     `(model, cmd)` tuple ⇒ `reads_whole_model`; a returned model that is a
///     FRESH `Record` or flows through a helper that is NOT provably
///     field-preserving ⇒ `writes_whole_model`. Under-approximating (dropping a
///     real write) is a correctness bug — on any doubt we send MORE (§14 B1).
fn compute_branch_io(
    db: &dyn SkyDb,
    body: &Body,
    arm_body: ExprId,
    pat: PatId,
    model_local: Option<LocalId>,
    src: &str,
) -> BranchIo {
    // Writes first — the tail walk also records the bare-model returns that the
    // read walk must NOT count as opaque uses.
    let mut write_fields: BTreeSet<String> = BTreeSet::new();
    let mut writes_whole = false;
    let mut allowed_bare: HashSet<ExprId> = HashSet::new();
    let let_locals: HashMap<LocalId, ExprId> = HashMap::new();
    collect_writes_tail(
        db,
        body,
        arm_body,
        model_local,
        &let_locals,
        &mut write_fields,
        &mut writes_whole,
        &mut allowed_bare,
        0,
        None,
    );

    let mut read_fields: BTreeSet<String> = BTreeSet::new();
    let mut reads_whole = false;
    collect_reads(
        db,
        body,
        arm_body,
        model_local,
        &allowed_bare,
        &let_locals,
        &mut read_fields,
        &mut reads_whole,
        0,
    );

    // If we could not identify the `model` parameter at all, we cannot bound the
    // read/write sets — over-approximate BOTH to the whole model (sound).
    if model_local.is_none() {
        reads_whole = true;
        writes_whole = true;
    }

    BranchIo {
        reads_whole_model: reads_whole,
        read_fields: read_fields.into_iter().collect(),
        msg_args: msg_arg_names(body, pat, src),
        writes_whole_model: writes_whole,
        write_fields: write_fields.into_iter().collect(),
    }
}

/// Is expression `e` the bare model parameter (`Var(Res::Local(model))`)?
fn is_model_var(body: &Body, e: ExprId, model_local: Option<LocalId>) -> bool {
    matches!(&body.exprs[e], Expr::Var(Res::Local(l)) if Some(*l) == model_local)
}

/// Walk the arm body for the READ-SET. `Access(model, f)` records `f`; the model
/// base of an `Update` and the bare-model tail returns (`allowed_bare`) are the
/// only permitted `model` occurrences — any OTHER `Var(model)` is an opaque use
/// and forces `reads_whole` (sound over-approximation).
#[allow(clippy::too_many_arguments)]
fn collect_reads(
    db: &dyn SkyDb,
    body: &Body,
    e: ExprId,
    model_local: Option<LocalId>,
    allowed_bare: &HashSet<ExprId>,
    let_locals: &HashMap<LocalId, ExprId>,
    read_fields: &mut BTreeSet<String>,
    reads_whole: &mut bool,
    depth: usize,
) {
    macro_rules! go {
        ($x:expr) => {
            collect_reads(
                db, body, $x, model_local, allowed_bare, let_locals, read_fields, reads_whole,
                depth,
            )
        };
    }
    match &body.exprs[e] {
        Expr::Int(_)
        | Expr::Float(_)
        | Expr::Str(_)
        | Expr::Chr(_)
        | Expr::Bool(_)
        | Expr::Unit
        | Expr::Accessor(_)
        | Expr::Error => {}
        Expr::Access(base, field) => {
            if is_model_var(body, *base, model_local) {
                // `model.field` — a precise field read.
                read_fields.insert(field.as_str().to_string());
            } else {
                // e.g. `model.ui.newTitle` — the inner `model.ui` records "ui".
                go!(*base);
            }
        }
        Expr::Update { base, fields } => {
            // `{ model | … }` — the model base is a WRITE base, not an opaque
            // read; skip it. A non-model base is walked normally.
            if !is_model_var(body, *base, model_local) {
                go!(*base);
            }
            for (_, v) in fields {
                go!(*v);
            }
        }
        Expr::Var(res) => {
            if let Res::Local(l) = res {
                if Some(*l) == model_local && !allowed_bare.contains(&e) {
                    // An opaque use of `model` (helper arg, list element, …).
                    *reads_whole = true;
                }
            }
        }
        Expr::List(xs) | Expr::Tuple(xs) => {
            for x in xs {
                go!(*x);
            }
        }
        Expr::Record(fields) => {
            for (_, x) in fields {
                go!(*x);
            }
        }
        Expr::Negate(x) => go!(*x),
        Expr::Lambda { body: b, .. } => go!(*b),
        Expr::Call(callee, args) => {
            // Read-delegation: an arm that passes the BARE model to a resolvable
            // `Model -> X` def (a pure accessor `pluck model`, a whole-arm handler
            // `handle model`) reads only what that def reads of its param — inherit
            // its read-set instead of over-approximating to the whole model. Sound:
            // a smaller request. A def we cannot resolve, or one that uses its param
            // opaquely, yields the whole model (`None` → reads_whole).
            let callee_def = match &body.exprs[*callee] {
                Expr::Var(Res::Def(f)) => Some(*f),
                _ => None,
            };
            for (i, a) in args.iter().enumerate() {
                if is_model_var(body, *a, model_local) {
                    // The BARE model threaded into this callee.
                    match callee_def {
                        Some(f) => match helper_readset(db, f, i, depth + 1) {
                            Some(fields) => read_fields.extend(fields),
                            None => *reads_whole = true,
                        },
                        // A non-def callee applied to the bare model uses it
                        // opaquely — the whole model (sound).
                        None => *reads_whole = true,
                    }
                } else if model_write_shape(db, body, *a, model_local, let_locals, depth).is_some()
                {
                    // A provably model-DERIVED value (one that RETURNS the model
                    // record) flows into the callee: a `{ model | … }` update, a
                    // field-preserving `Model -> Model` helper chain (any arity — see
                    // `model_write_shape`), or a `let`/alias of such. The callee may
                    // read any field that passes THROUGH the derivation — e.g.
                    // `recompute (clear { model | region = r })` and
                    // `recompute (stamp k model)` both read `model.basket` inside
                    // `recompute` — and from here `collect_reads` cannot map those
                    // reads back to model fields. Fail CLOSED: send the whole model
                    // (the sound over-approximation, symmetric with the write side's
                    // `writes_whole`). Before this, such a threaded read was silently
                    // DROPPED, so the RPC ran the branch against a fresh `init ()`
                    // and returned a wrong, input-independent result (darraghstudio
                    // `SetRegion` recomputed shipping on an empty basket → 0).
                    //
                    // `model_write_shape` is return-type-aware: it is `None` for a
                    // call that returns a NON-model (`pluck model : … -> Tag`), so a
                    // pure accessor threaded into a helper stays PRECISE (its read is
                    // recorded by the nested walk), and `None` for a plain
                    // `model.field` access (recorded by the `Access` arm). Only a
                    // value that is itself the model record forces the whole model.
                    *reads_whole = true;
                } else {
                    go!(*a);
                }
            }
            // The callee reference itself is a function, not a model read; walk it
            // only when it is not a bare def (e.g. a computed callee).
            if callee_def.is_none() {
                go!(*callee);
            }
        }
        Expr::Binop { lhs, rhs, .. } => {
            go!(*lhs);
            go!(*rhs);
        }
        Expr::If { arms, els } => {
            for (c, t) in arms {
                go!(*c);
                go!(*t);
            }
            go!(*els);
        }
        Expr::Let { defs, body: b } => {
            // Thread the let-bound locals so `may_carry_model` / `model_write_shape`
            // can chase an alias to a model-derived value
            // (`let m2 = { model | … } in helper m2`). Without this the read walk
            // saw `m2` as an opaque local and dropped the consuming helper's
            // pass-through reads — a silent wrong answer. Mirrors the write side
            // (`collect_writes_tail` / `model_write_shape`).
            let mut ls = let_locals.clone();
            add_let_locals(defs, &mut ls);
            for d in defs {
                collect_reads(
                    db, body, d.body, model_local, allowed_bare, &ls, read_fields, reads_whole,
                    depth,
                );
            }
            collect_reads(
                db, body, *b, model_local, allowed_bare, &ls, read_fields, reads_whole, depth,
            );
        }
        Expr::Case { subject, branches } => {
            go!(*subject);
            for br in branches {
                go!(br.body);
            }
        }
    }
}

/// Walk the arm body's TAIL positions for the WRITE-SET. The tail of an
/// `update` arm is the `(model', cmd)` tuple (possibly under `let`/`if`/`case`).
/// The first tuple element is analysed by [`model_write_shape`]: a bare `model`
/// is a no-write return (recorded in `allowed_bare` so the read walk does not
/// count it as opaque); a `{ model | … }` (directly, or through a chain of
/// provably field-preserving `Model -> Model` helpers) records the UNION of the
/// rewritten fields; anything not provably narrow (a fresh `Record`, an opaque
/// producer) ⇒ `writes_whole`. Two whole-arm shapes are also resolved: a delegate
/// `handle model` inherits the helper's own tail write-set, and a let-bound
/// `(model', cmd)` returned by name is chased to its binding.
///
/// `cont_local` is the guard-continuation parameter, set ONLY while analysing a
/// guard helper's own body (see [`collect_guard_wrapper_writes`]). A tail that IS
/// a call to that continuation (`cont ()`) is the authorised passthrough — its
/// writes are covered separately by the inline-lambda analysis — so it
/// contributes nothing here. It is `None` in every arm-level walk.
#[allow(clippy::too_many_arguments)]
fn collect_writes_tail(
    db: &dyn SkyDb,
    body: &Body,
    e: ExprId,
    model_local: Option<LocalId>,
    let_locals: &HashMap<LocalId, ExprId>,
    write_fields: &mut BTreeSet<String>,
    writes_whole: &mut bool,
    allowed_bare: &mut HashSet<ExprId>,
    depth: usize,
    cont_local: Option<LocalId>,
) {
    if depth > IO_DELEGATE_DEPTH {
        *writes_whole = true;
        return;
    }
    // Structural recursion within THIS body (`if`/`case`/`let` scaffolding of the
    // arm tail) walks a finite HIR tree, so it preserves `depth` — the budget is
    // for CROSS-DEF delegation and for chasing let-bound names (which can cycle),
    // not for control-flow nesting. This mirrors [`collect_reads`], whose `go!`
    // likewise preserves depth. Incrementing on every structural step made a
    // deeply nested but perfectly narrow arm (e.g. `doSignIn`: let>if>case>if>
    // case>if>let) trip the ceiling and fall back to the whole model.
    macro_rules! recur {
        ($x:expr, $ls:expr) => {
            collect_writes_tail(
                db, body, $x, model_local, $ls, write_fields, writes_whole, allowed_bare, depth,
                cont_local,
            )
        };
    }
    match &body.exprs[e] {
        Expr::Tuple(xs) if xs.len() == 2 => {
            let m = xs[0];
            // Bare `( model, cmd )` — returns model unchanged, writes nothing. Mark
            // so the read walk does not over-approximate.
            if is_model_var(body, m, model_local) {
                allowed_bare.insert(m);
                return;
            }
            match model_write_shape(db, body, m, model_local, let_locals, depth) {
                Some(fields) => {
                    for f in fields {
                        write_fields.insert(f);
                    }
                }
                // Not provably a field-preserving transform of `model` (a fresh
                // record, an opaque producer) — the whole model rides out (sound).
                None => *writes_whole = true,
            }
        }
        Expr::Let { defs, body: b } => {
            let mut ls = let_locals.clone();
            add_let_locals(defs, &mut ls);
            recur!(*b, &ls);
        }
        Expr::If { arms, els } => {
            for (_, t) in arms {
                recur!(*t, let_locals);
            }
            recur!(*els, let_locals);
        }
        Expr::Case { branches, .. } => {
            for br in branches {
                recur!(br.body, let_locals);
            }
        }
        // A let-bound `(model', cmd)` tuple returned BY NAME (`badCreds` / the
        // `stamped` shape) — resolve the binding and analyse it as the tail.
        // Chasing a name CAN cycle (`let x = … x …`), so this step DOES spend
        // depth — the ceiling then bounds a runaway alias chain (falling back to
        // the whole model, sound).
        Expr::Var(Res::Local(l)) => match let_locals.get(l) {
            Some(bound) => collect_writes_tail(
                db,
                body,
                *bound,
                model_local,
                let_locals,
                write_fields,
                writes_whole,
                allowed_bare,
                depth + 1,
                cont_local,
            ),
            None => *writes_whole = true,
        },
        // A whole-arm delegate `f a0 … an` where EXACTLY ONE argument is the bare
        // model (`handle model`, or `viaHelper label model` with the model a later
        // arg) — inherit the helper's own tail write-set, computed with respect to
        // the helper parameter that the model flows into. If the bare model appears
        // in zero or in more than one argument, the target return is ambiguous, so
        // over-approximate to the whole model (sound). The other arguments carry Msg
        // payloads or pure values; they never widen the MODEL write-set.
        Expr::Call(callee, args) => {
            // A call to the guard's continuation parameter (`cont ()`) — the
            // authorised passthrough. Its writes are the inline lambda's, analysed
            // separately by `collect_guard_wrapper_writes`. Contribute nothing here.
            if let Expr::Var(Res::Local(l)) = &body.exprs[*callee] {
                if cont_local == Some(*l) {
                    return;
                }
            }
            // Guard-wrapper tail: `GUARD model (\_ -> CONT)` — a higher-order auth
            // guard applied to the bare model plus an INLINE lambda continuation.
            // The arm's write-set is the UNION of (i) the lambda body's own writes
            // (the authorised path) and (ii) the guard helper's OWN writes on its
            // model parameter (its deny/unauth path). Both are analysed narrowly;
            // on any doubt either widens to the whole model (sound). See the fn.
            if let Some(gw) = detect_guard_wrapper(db, body, *callee, args, model_local) {
                // (i) the inline lambda body — the authorised continuation. The
                // lambda captures the arm's `model_local`; walk it in place.
                recur!(gw.lambda_body, let_locals);
                // (ii) the guard helper's own writes on its model parameter, with
                // its continuation call skipped.
                collect_guard_wrapper_writes(db, gw.guard, gw.model_arg_idx, gw.cont_arg_idx, write_fields, writes_whole, depth + 1);
                return;
            }
            let model_positions: Vec<usize> = args
                .iter()
                .enumerate()
                .filter(|(_, a)| is_model_var(body, **a, model_local))
                .map(|(i, _)| i)
                .collect();
            if model_positions.len() == 1 {
                if let Expr::Var(Res::Def(f)) = &body.exprs[*callee] {
                    inherit_delegate_writes(
                        db,
                        *f,
                        model_positions[0],
                        write_fields,
                        writes_whole,
                        depth + 1,
                    );
                } else {
                    *writes_whole = true;
                }
            } else {
                *writes_whole = true;
            }
        }
        // The arm did not evaluate to a recognizable `(model', cmd)` tuple — be
        // conservative (send the whole model).
        _ => *writes_whole = true,
    }
}

/// Record the plain single-binder value `let`s (`x = expr`, no params, no
/// destructure) of `defs` into `ls` (LocalId → its bound expression), so a later
/// by-name return of a `(model', cmd)` tuple or a model alias can be resolved.
/// Effect-forcing binders (`let _ = …`) and destructures carry no name and are
/// skipped.
fn add_let_locals(defs: &[LocalDef], ls: &mut HashMap<LocalId, ExprId>) {
    for d in defs {
        if d.params.is_empty() && d.pat.is_none() && d.binders.len() == 1 {
            ls.insert(d.binders[0].1, d.body);
        }
    }
}

/// The value-parameter local at position `i` of a def, when it is a plain
/// `Var`/`Alias` binder. Used to map a bare-model argument to the helper
/// parameter it flows into. `None` when the position is absent or destructured.
fn param_local_at(body: &Body, i: usize) -> Option<LocalId> {
    let pat = *body.params.get(i)?;
    match &body.pats[pat] {
        Pattern::Var(l) => Some(*l),
        Pattern::Alias(_, l) => Some(*l),
        _ => None,
    }
}

/// The write-set of a helper `f : … -> Model -> … -> Model` (returning a BARE
/// `Model`) that takes its model parameter at position `i`. `Some(fields)` when `f` is provably
/// field-preserving over that parameter (a `{ param | … }` return, directly or
/// through a field-preserving chain, via [`model_write_shape`]); `None` when it is
/// not — e.g. it returns a NON-model value (`pluck model : … -> Tag` → the body is
/// not a `{ param | … }` shape), uses the parameter opaquely, or could not be
/// resolved. Uses `model_write_shape` (a bare-`Model` return), NOT the
/// tuple-based `inherit_delegate_writes` (which analyses a `(Model, Cmd)` arm
/// tail).
fn helper_writeset_at(db: &dyn SkyDb, f: DefId, i: usize, depth: usize) -> Option<BTreeSet<String>> {
    if depth > IO_DELEGATE_DEPTH {
        return None;
    }
    let loc = db.def_loc(f)?;
    let resolved = db.resolve(loc.module);
    let body = resolved.bodies.get(&f)?;
    let root = body.root?;
    let mlocal = param_local_at(body, i)?;
    let let_locals: HashMap<LocalId, ExprId> = HashMap::new();
    model_write_shape(db, body, root, Some(mlocal), &let_locals, depth + 1)
}

/// Inherit a whole-arm delegate's tail write-set: `f : … -> Model -> … ->
/// (Model, Cmd)` applied with the bare model at argument index `i` contributes
/// exactly the fields `f`'s own tail rewrites of its parameter at index `i`. An
/// unresolvable `f`, a parameter at `i` that is absent or destructured, or a tail
/// that is not a recognisable narrow return, sets `writes_whole` (sound). The
/// tail is analysed by the SAME `collect_writes_tail` machinery, which already
/// fails closed to the whole model for a fresh record or an opaque producer.
fn inherit_delegate_writes(
    db: &dyn SkyDb,
    f: DefId,
    i: usize,
    write_fields: &mut BTreeSet<String>,
    writes_whole: &mut bool,
    depth: usize,
) {
    if depth > IO_DELEGATE_DEPTH {
        *writes_whole = true;
        return;
    }
    let Some(loc) = db.def_loc(f) else {
        *writes_whole = true;
        return;
    };
    let resolved = db.resolve(loc.module);
    let Some(body) = resolved.bodies.get(&f) else {
        *writes_whole = true;
        return;
    };
    let (Some(root), Some(mlocal)) = (body.root, param_local_at(body, i)) else {
        *writes_whole = true;
        return;
    };
    let mut allowed: HashSet<ExprId> = HashSet::new();
    let let_locals: HashMap<LocalId, ExprId> = HashMap::new();
    collect_writes_tail(
        db,
        body,
        root,
        Some(mlocal),
        &let_locals,
        write_fields,
        writes_whole,
        &mut allowed,
        depth + 1,
        None,
    );
}

/// A recognised guard-wrapper tail `GUARD model (\_ -> CONT)` (see
/// [`detect_guard_wrapper`]).
struct GuardWrapper {
    /// The guard helper def (`requireAdmin`).
    guard: DefId,
    /// The call-argument index the bare model occupies (the guard's model param).
    model_arg_idx: usize,
    /// The call-argument index the inline lambda occupies (the guard's
    /// continuation param).
    cont_arg_idx: usize,
    /// The inline lambda's body — the authorised continuation.
    lambda_body: ExprId,
}

/// Recognise a higher-order guard-wrapper tail `GUARD model (\_ -> CONT)`: a
/// resolvable `Def` callee applied to EXACTLY two value arguments, one the bare
/// model and the other an INLINE lambda continuation. Returns the guard def, the
/// argument index the bare model occupies, the argument index the lambda occupies
/// (its complement), and the lambda body. `None` — the caller then keeps today's
/// whole-model delegate behaviour (sound) — when the callee is not a resolvable
/// def, the arity is not two, the model is not passed bare, the other argument is
/// not an inline lambda, or the guard's own body does not bind plain parameters at
/// both positions (so its own I/O is not analysable). This is the fail-closed
/// gate: a shape that does not MATCH the guard-wrapper is never narrowed.
fn detect_guard_wrapper(
    db: &dyn SkyDb,
    body: &Body,
    callee: ExprId,
    args: &[ExprId],
    model_local: Option<LocalId>,
) -> Option<GuardWrapper> {
    let Expr::Var(Res::Def(guard)) = &body.exprs[callee] else {
        return None;
    };
    if args.len() != 2 {
        return None;
    }
    // Exactly one argument is the bare model.
    let model_positions: Vec<usize> = args
        .iter()
        .enumerate()
        .filter(|(_, a)| is_model_var(body, **a, model_local))
        .map(|(i, _)| i)
        .collect();
    if model_positions.len() != 1 {
        return None;
    }
    let model_arg_idx = model_positions[0];
    // With two arguments the continuation is the complement index.
    let cont_arg_idx = 1 - model_arg_idx;
    // The OTHER argument must be an INLINE lambda continuation — a passed-by-name
    // helper (`GUARD model handler`) is opaque and stays whole-model (fail closed).
    let Expr::Lambda { body: lambda_body, .. } = &body.exprs[args[cont_arg_idx]] else {
        return None;
    };
    // The guard's own body must resolve and bind plain parameters at BOTH the
    // model and continuation positions, so its model I/O + its continuation are
    // both identifiable. Otherwise fail closed to the whole model.
    let loc = db.def_loc(*guard)?;
    let resolved = db.resolve(loc.module);
    let gbody = resolved.bodies.get(guard)?;
    gbody.root?;
    param_local_at(gbody, model_arg_idx)?;
    param_local_at(gbody, cont_arg_idx)?;
    Some(GuardWrapper {
        guard: *guard,
        model_arg_idx,
        cont_arg_idx,
        lambda_body: *lambda_body,
    })
}

/// The guard helper's OWN write-set on its model parameter — its deny / unauth
/// path (e.g. `( { model | error = … }, cmd )`, or a bare `( model, cmd )` that
/// writes nothing). The guard's continuation call (`cont ()`, whose parameter is
/// at `cont_arg_idx`) is SKIPPED: that path's writes are the inline lambda's,
/// unioned by the caller. Fail-closed: an unresolvable guard, a missing plain
/// parameter, or a tail the walk cannot bound sets `writes_whole` (sound).
fn collect_guard_wrapper_writes(
    db: &dyn SkyDb,
    guard: DefId,
    model_arg_idx: usize,
    cont_arg_idx: usize,
    write_fields: &mut BTreeSet<String>,
    writes_whole: &mut bool,
    depth: usize,
) {
    if depth > IO_DELEGATE_DEPTH {
        *writes_whole = true;
        return;
    }
    let Some(loc) = db.def_loc(guard) else {
        *writes_whole = true;
        return;
    };
    let resolved = db.resolve(loc.module);
    let Some(body) = resolved.bodies.get(&guard) else {
        *writes_whole = true;
        return;
    };
    let (Some(root), Some(mlocal), Some(clocal)) = (
        body.root,
        param_local_at(body, model_arg_idx),
        param_local_at(body, cont_arg_idx),
    ) else {
        *writes_whole = true;
        return;
    };
    let mut allowed: HashSet<ExprId> = HashSet::new();
    let let_locals: HashMap<LocalId, ExprId> = HashMap::new();
    collect_writes_tail(
        db,
        body,
        root,
        Some(mlocal),
        &let_locals,
        write_fields,
        writes_whole,
        &mut allowed,
        depth + 1,
        Some(clocal),
    );
}

/// The read-set a `Model -> X` helper `f` reads of its parameter at position `i`
/// — inherited when an arm passes it the bare model. `Some(fields)` when `f`
/// reads its parameter only via precise `param.field` accesses (or via further
/// pure accessors); `None` when `f` uses the parameter opaquely or could not be
/// resolved (the caller then reads the whole model). Reuses the arm-level walks
/// (writes first to populate the bare-return set, then reads) so a helper is
/// analysed exactly as an arm body would be.
fn helper_readset(db: &dyn SkyDb, f: DefId, i: usize, depth: usize) -> Option<BTreeSet<String>> {
    if depth > IO_DELEGATE_DEPTH {
        return None;
    }
    let loc = db.def_loc(f)?;
    let resolved = db.resolve(loc.module);
    let body = resolved.bodies.get(&f)?;
    let root = body.root?;
    let mlocal = param_local_at(body, i)?;
    let let_locals: HashMap<LocalId, ExprId> = HashMap::new();
    let mut wf: BTreeSet<String> = BTreeSet::new();
    let mut ww = false;
    let mut allowed: HashSet<ExprId> = HashSet::new();
    collect_writes_tail(
        db,
        body,
        root,
        Some(mlocal),
        &let_locals,
        &mut wf,
        &mut ww,
        &mut allowed,
        depth + 1,
        None,
    );
    let mut rf: BTreeSet<String> = BTreeSet::new();
    let mut whole = false;
    collect_reads(
        db,
        body,
        root,
        Some(mlocal),
        &allowed,
        &let_locals,
        &mut rf,
        &mut whole,
        depth + 1,
    );
    if whole {
        None
    } else {
        Some(rf)
    }
}

/// Analyse an expression `e` that must evaluate to a `Model` value, in a context
/// whose model parameter is `model_local`. Returns `Some(fields)` when `e` is a
/// PROVABLY field-preserving transform of that model — a bare model, a
/// `{ model | … }` update, a `let`/model alias of such, or a chain of
/// field-preserving `Model -> Model` helpers applied to such — writing exactly
/// `fields`. Returns `None` when the value is not provably narrow (a fresh
/// `Record`, an opaque producer, a helper that is not field-preserving), so the
/// caller over-approximates to the whole model (sound — never drops a write).
fn model_write_shape(
    db: &dyn SkyDb,
    body: &Body,
    e: ExprId,
    model_local: Option<LocalId>,
    let_locals: &HashMap<LocalId, ExprId>,
    depth: usize,
) -> Option<BTreeSet<String>> {
    if depth > IO_DELEGATE_DEPTH {
        return None;
    }
    match &body.exprs[e] {
        // The bare model parameter: a field-preserving identity, writes nothing.
        Expr::Var(Res::Local(l)) if Some(*l) == model_local => Some(BTreeSet::new()),
        // A `let`-bound local aliasing a model-valued expression: resolve it.
        // Chasing a name CAN cycle, so this DOES spend depth (the ceiling bounds
        // a runaway alias chain — `None`, i.e. the whole model, sound).
        Expr::Var(Res::Local(l)) => {
            let bound = *let_locals.get(l)?;
            model_write_shape(db, body, bound, model_local, let_locals, depth + 1)
        }
        // `{ base | f = … }` — `base` must itself be field-preserving. Structural
        // (a sub-expression of a finite tree): preserve depth.
        Expr::Update { base, fields } => {
            let mut s = model_write_shape(db, body, *base, model_local, let_locals, depth)?;
            for (n, _) in fields {
                s.insert(n.as_str().to_string());
            }
            Some(s)
        }
        // `f arg` — a field-preserving `Model -> Model` helper applied to a
        // field-preserving argument. The written set is the union. The helper
        // call crosses a def boundary, so it spends depth; the argument is a
        // structural sub-expression and does not.
        // `f a0 … an` — a field-preserving `… -> Model -> … -> Model` helper applied
        // to a field-preserving model-derived value in EXACTLY ONE argument
        // position. The written set is the helper's own writes on THAT parameter
        // (`helper_writeset_at`, which honours the model's position) unioned with the
        // derived argument's writes. The other arguments carry Msg payloads or pure
        // values; they never widen the MODEL shape. Zero or ≥2 model-derived args ⇒
        // ambiguous target ⇒ `None` (whole model). A callee that returns a NON-model
        // (`pluck model : … -> Tag`) yields `None` via `helper_writeset_at`, so it is
        // NOT treated as a model-returning value — the read-side guard then leaves a
        // pure accessor precise. Generalises the former single-argument form.
        Expr::Call(callee, args) => {
            let Expr::Var(Res::Def(f)) = &body.exprs[*callee] else {
                return None;
            };
            let derived: Vec<(usize, BTreeSet<String>)> = args
                .iter()
                .enumerate()
                .filter_map(|(i, a)| {
                    model_write_shape(db, body, *a, model_local, let_locals, depth)
                        .map(|s| (i, s))
                })
                .collect();
            if derived.len() != 1 {
                return None;
            }
            let (i, arg_shape) = &derived[0];
            let mut s = helper_writeset_at(db, *f, *i, depth + 1)?;
            s.extend(arg_shape.iter().cloned());
            Some(s)
        }
        // `let`/`if`/`case` scaffolding is structural — a finite HIR sub-tree, so
        // it preserves depth (matches the tail walk in `collect_writes_tail`).
        Expr::Let { defs, body: b } => {
            let mut ls = let_locals.clone();
            add_let_locals(defs, &mut ls);
            model_write_shape(db, body, *b, model_local, &ls, depth)
        }
        Expr::If { arms, els } => {
            let mut s = BTreeSet::new();
            for (_, t) in arms {
                s.extend(model_write_shape(db, body, *t, model_local, let_locals, depth)?);
            }
            s.extend(model_write_shape(db, body, *els, model_local, let_locals, depth)?);
            Some(s)
        }
        Expr::Case { branches, .. } => {
            let mut s = BTreeSet::new();
            for br in branches {
                s.extend(model_write_shape(db, body, br.body, model_local, let_locals, depth)?);
            }
            Some(s)
        }
        // A fresh `Record`, an opaque producer, … — not provably narrow.
        _ => None,
    }
}

/// The Msg args an arm pattern binds, in source order (`ToggleTodo id` →
/// `["id"]`; `StartEdit id current` → `["id", "current"]`). Names are sliced
/// from each binder's source span (`Pattern::Var` carries a `LocalId`, not a
/// name); a `Record`-destructure binder already carries its field name.
fn msg_arg_names(body: &Body, pat: PatId, src: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    if let Pattern::Ctor { args, .. } = &body.pats[pat] {
        for a in args {
            collect_binder_names(body, *a, src, &mut out);
        }
    }
    out
}

/// The Msg args an arm pattern binds, WITH their types — parallel to
/// [`msg_arg_names`] by name+order, but each entry carries the binder's resolved
/// `ty::Ty` (from the typed HIR `locals` table) + its primitive codec. The
/// `spa-split` generator uses these to give each Msg-arg RPC-request field a real
/// codec (`Toggle Int` → `id : Int` with `Codec.int`). A binder whose type could
/// not be read yields `ty: None` / `codec: None`, which the generator reports as
/// an unresolved-codec error rather than guessing.
fn msg_arg_field_tys(
    body: &Body,
    pat: PatId,
    src: &str,
    locals: &HashMap<LocalId, ty::Ty>,
) -> Vec<ModelFieldTy> {
    let mut out: Vec<ModelFieldTy> = Vec::new();
    if let Pattern::Ctor { args, .. } = &body.pats[pat] {
        for a in args {
            collect_binder_fields(body, *a, src, locals, &mut out);
        }
    }
    out
}

fn field_for_local(name: String, local: Option<LocalId>, locals: &HashMap<LocalId, ty::Ty>) -> ModelFieldTy {
    let ty = local.and_then(|l| locals.get(&l)).cloned();
    let mut f = match &ty {
        Some(t) => field_ty_codec(t),
        None => ModelFieldTy {
            name: String::new(),
            ty_name: "any".to_string(),
            codec: None,
            ty: None,
        },
    };
    f.name = name;
    f
}

fn collect_binder_fields(
    body: &Body,
    pat: PatId,
    src: &str,
    locals: &HashMap<LocalId, ty::Ty>,
    out: &mut Vec<ModelFieldTy>,
) {
    match &body.pats[pat] {
        Pattern::Var(l) => {
            if let Some(name) = slice_binder_name(body, pat, src) {
                out.push(field_for_local(name, Some(*l), locals));
            }
        }
        Pattern::Alias(inner, l) => {
            if let Some(name) = slice_binder_name(body, pat, src) {
                out.push(field_for_local(name, Some(*l), locals));
            }
            collect_binder_fields(body, *inner, src, locals, out);
        }
        Pattern::Record(binders) => {
            for (n, l) in binders {
                out.push(field_for_local(n.as_str().to_string(), Some(*l), locals));
            }
        }
        Pattern::Tuple(ps) | Pattern::List(ps) => {
            for p in ps {
                collect_binder_fields(body, *p, src, locals, out);
            }
        }
        Pattern::Cons(h, t) => {
            collect_binder_fields(body, *h, src, locals, out);
            collect_binder_fields(body, *t, src, locals, out);
        }
        Pattern::Ctor { args, .. } => {
            for a in args {
                collect_binder_fields(body, *a, src, locals, out);
            }
        }
        _ => {}
    }
}

fn collect_binder_names(body: &Body, pat: PatId, src: &str, out: &mut Vec<String>) {
    match &body.pats[pat] {
        Pattern::Var(_) => {
            if let Some(name) = slice_binder_name(body, pat, src) {
                out.push(name);
            }
        }
        Pattern::Alias(inner, _) => {
            // `p as name` — take the alias name plus any binders inside `p`.
            if let Some(name) = slice_binder_name(body, pat, src) {
                out.push(name);
            }
            collect_binder_names(body, *inner, src, out);
        }
        Pattern::Record(binders) => {
            for (n, _) in binders {
                out.push(n.as_str().to_string());
            }
        }
        Pattern::Tuple(ps) | Pattern::List(ps) => {
            for p in ps {
                collect_binder_names(body, *p, src, out);
            }
        }
        Pattern::Cons(h, t) => {
            collect_binder_names(body, *h, src, out);
            collect_binder_names(body, *t, src, out);
        }
        Pattern::Ctor { args, .. } => {
            for a in args {
                collect_binder_names(body, *a, src, out);
            }
        }
        _ => {}
    }
}

/// Slice a binder's identifier text out of the module source via its pattern
/// span. Returns `None` if the span is absent or the sliced text is not a plain
/// identifier (e.g. a recovery node) — never fabricates a name.
fn slice_binder_name(body: &Body, pat: PatId, src: &str) -> Option<String> {
    let span = body.pat_span(pat)?;
    let (start, end) = (span.range.0 as usize, span.range.1 as usize);
    let text = src.get(start..end)?.trim();
    if !text.is_empty()
        && text
            .chars()
            .all(|c| c.is_alphanumeric() || c == '_' || c == '\'')
        && text.chars().next().is_some_and(|c| !c.is_numeric())
    {
        Some(text.to_string())
    } else {
        None
    }
}

/// The Model's field list from the `update` result type `( Model, Cmd msg )`.
/// `None` when the shape is not the expected TEA tuple (then callers print
/// "whole model" without enumerating).
fn model_fields_from_result(result: &Option<ty::Ty>) -> Option<Vec<String>> {
    if let Some(ty::Ty::Tuple(xs)) = result {
        if xs.len() == 2 {
            if let ty::Ty::Record(fields, _) = &xs[0] {
                let mut names: Vec<String> =
                    fields.iter().map(|(n, _)| n.as_str().to_string()).collect();
                names.sort();
                return Some(names);
            }
        }
    }
    None
}

/// The `LocalId` of `update`'s second parameter (`model`), if it is a plain
/// `Pattern::Var`. `None` for a destructured / aliased model param — callers
/// then over-approximate the I/O sets to the whole model.
fn model_param_local(body: &Body) -> Option<LocalId> {
    let pat = *body.params.get(1)?;
    match &body.pats[pat] {
        Pattern::Var(l) => Some(*l),
        Pattern::Alias(_, l) => Some(*l),
        _ => None,
    }
}

/// The DIRECT (non-compose) server reason for an arm: its own server kernel /
/// FFI, a non-`update` server callee, or a generic `update` use. `None` → the
/// arm is server only if it composes a server arm (handled by the fixpoint).
fn arm_direct_reason(db: &dyn SkyDb, acc: &Refs, graph: &Graph) -> Option<String> {
    if let Some(reason) = acc.direct_server_reason() {
        return Some(reason);
    }
    // `update` is never in `acc.callees` under precision ctx, so this cannot pick
    // it up — only genuine non-`update` server callees.
    let mut server_callees: Vec<DefId> = acc
        .callees
        .iter()
        .copied()
        .filter(|c| graph.server.contains(c))
        .collect();
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
        return Some(format!("references {cn} ({origin})"));
    }
    if acc.generic_update {
        return Some("uses `update` generically (dynamic/value use → conservative server)".into());
    }
    None
}

/// The reason a composing arm is server: name the first scoped call it makes to
/// a server arm, carrying that arm's origin ("composes DoServer (…)").
fn compose_reason(
    scoped: &[String],
    by_name: &HashMap<String, usize>,
    server: &[bool],
    direct: &[Option<String>],
) -> String {
    for s in scoped {
        match by_name.get(s) {
            Some(&j) if server[j] => {
                let origin = direct[j]
                    .clone()
                    .unwrap_or_else(|| "reaches a server branch".to_string());
                return format!("composes {s} ({origin})");
            }
            None => return format!("composes {s} (unresolved Msg → conservative server)"),
            _ => {}
        }
    }
    "composes a server branch".to_string()
}

/// The head Msg-ctor NAME of an arm pattern, for composition keying.
fn arm_ctor_key(body: &Body, pat: hir::PatId) -> Option<String> {
    match &body.pats[pat] {
        Pattern::Ctor { name, .. } => Some(name.as_str().to_string()),
        _ => None,
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
    // A lambda `update` has no stable DefId for itself to compose against, so the
    // conservative ctx is correct here (any `update` reference stays server).
    let ctx = CollectCtx::default();
    let mut shared = Refs::default();
    let case_expr = find_top_case(body, root, &mut shared, &ctx);
    if let Some(ce) = case_expr {
        if let Expr::Case { branches: arms, .. } = &body.exprs[ce] {
            for arm in arms {
                let mut acc = shared.clone();
                collect(body, arm.body, &mut acc, &ctx);
                let label = pattern_label(body, arm.pat);
                branches.push(verdict(db, &label, &acc, graph));
            }
            return;
        }
    }
    let mut acc = Refs::default();
    collect(body, root, &mut acc, &ctx);
    *whole_update = Some(verdict(db, "(whole update)", &acc, graph));
}

/// Follow the `let`/`if`-spine from `e` to the outermost `case`, folding shared
/// `let` refs into `shared`. Returns the case ExprId, or None.
fn find_top_case(body: &Body, e: ExprId, shared: &mut Refs, ctx: &CollectCtx) -> Option<ExprId> {
    match &body.exprs[e] {
        Expr::Case { .. } => Some(e),
        Expr::Let { defs, body: b } => {
            for d in defs {
                collect_localdef(body, d, shared, ctx);
            }
            find_top_case(body, *b, shared, ctx)
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
            io: None,
            msg_arg_tys: Vec::new(),
            // Whole-update path: io is None → never phase-2 checkable regardless.
            forces_effect: acc.inline_force,
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
            io: None,
            msg_arg_tys: Vec::new(),
            forces_effect: acc.inline_force || acc.callees.iter().any(|c| graph.forces(*c)),
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
        io: None,
        msg_arg_tys: Vec::new(),
        forces_effect: acc.inline_force || acc.callees.iter().any(|c| graph.forces(*c)),
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

// ---------------------------------------------------------------------------
// Fail-closed classification-completeness guard (design §13/§15 residual).
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    /// The classification-completeness check, parametrised over a kernel-module
    /// table so the "guard bites" test can feed it a synthetic extra module.
    /// Returns the distinct pseudo-modules classified in NEITHER set (sorted).
    /// The shipped [`unclassified_kernel_families`] is this same logic pinned to
    /// the real `hir::KERNEL_MODULES` — asserted equal below, so the "bites" test
    /// exercises the exact guard the compiler ships.
    fn gaps_in(modules: &[(&str, &str)]) -> Vec<String> {
        let mut gaps: BTreeSet<String> = BTreeSet::new();
        for (_import, pseudo) in modules {
            if !EFFECT_KERNELS.contains(pseudo) && !KNOWN_PURE_KERNELS.contains(pseudo) {
                gaps.insert((*pseudo).to_string());
            }
        }
        gaps.into_iter().collect()
    }

    /// COMPLETENESS — the build fails if ANY kernel pseudo-module the compiler
    /// knows (`hir::KERNEL_MODULES`, the authoritative table — NOT a hardcoded
    /// copy) is classified for the Sky.Spa auto-split in neither [`EFFECT_KERNELS`]
    /// (server) nor [`KNOWN_PURE_KERNELS`] (client). Adding a kernel without
    /// deciding its split side is a BUILD FAILURE — an unclassified effect kernel
    /// defaulting to client would leak it into the wasm frontend.
    #[test]
    fn classification_is_exhaustive() {
        let gaps = unclassified_kernel_families();
        assert!(
            gaps.is_empty(),
            "kernel module(s) `{}` are not classified for the Sky.Spa auto-split — add each to EFFECT (server) or KNOWN_PURE (client) in spa_partition::classify_kernel; defaulting an unknown kernel to client would leak it into the wasm frontend.",
            gaps.join("`, `")
        );
        // The public guard and the parametrised check agree over the real table.
        assert_eq!(gaps, gaps_in(hir::KERNEL_MODULES));
    }

    /// The guard BITES — a synthetic new effect kernel added to the table but not
    /// classified is reported as a gap, with the failure message the completeness
    /// gate would raise. (Demonstrates the failure, then leaves the real tree
    /// green: `classification_is_exhaustive` proves the shipped table has none.)
    #[test]
    fn unclassified_kernel_is_rejected() {
        let mut table: Vec<(&str, &str)> = hir::KERNEL_MODULES.to_vec();
        // A brand-new EFFECT kernel family added to the compiler but NOT to the
        // classification lists — exactly the leak this guard exists to catch.
        table.push(("Sky.Core.Telemetry", "Telemetry"));
        let gaps = gaps_in(&table);
        assert!(
            gaps.contains(&"Telemetry".to_string()),
            "a kernel in neither EFFECT nor KNOWN_PURE must be reported as a gap"
        );
        let msg = format!(
            "kernel module(s) `{}` are not classified for the Sky.Spa auto-split — add each to EFFECT (server) or KNOWN_PURE (client) in spa_partition::classify_kernel; defaulting an unknown kernel to client would leak it into the wasm frontend.",
            gaps.join("`, `")
        );
        assert!(msg.contains("Telemetry"), "failure message names the culprit: {msg}");
    }

    /// The three classification outcomes — including the fail-closed default that
    /// treats an unrecognised family as SERVER (never Neutral/client).
    #[test]
    fn classify_kernel_is_fail_closed() {
        // Known effect -> server.
        assert_eq!(classify_kernel("Db", "query"), KernelClass::ServerOnly);
        assert_eq!(classify_kernel("Log", "println"), KernelClass::ServerOnly);
        assert_eq!(classify_kernel("Http", "get"), KernelClass::ServerOnly);
        // Known pure -> client (Neutral).
        assert_eq!(classify_kernel("String", "toUpper"), KernelClass::Neutral);
        assert_eq!(classify_kernel("List", "map"), KernelClass::Neutral);
        // Client-effect family -> ClientEffect (stays in the wasm client, not RPC).
        assert_eq!(classify_kernel("Native", "geolocation"), KernelClass::ClientEffect);
        assert_eq!(classify_kernel("Native", "clipboardWrite"), KernelClass::ClientEffect);
        // Unknown family -> conservative SERVER (fail-closed), never Neutral.
        assert_eq!(classify_kernel("BrandNewEffect", "boom"), KernelClass::ServerOnly);
    }

    /// A `Std.Native.*` FFI symbol (`Native_<cap>`) records as a CLIENT effect,
    /// never a server kernel — so a branch using it has no `direct_server_reason`
    /// and stays in the frontend wasm.
    #[test]
    fn native_ffi_symbol_is_a_client_effect() {
        let mut acc = Refs::default();
        record_ffi_symbol("Native_clipboardWrite", &mut acc);
        record_ffi_symbol("Native_geolocation", &mut acc);
        assert!(
            acc.server_kernels.is_empty(),
            "Std.Native must not record as a server kernel: {:?}",
            acc.server_kernels
        );
        assert!(acc.direct_server_reason().is_none(), "no server reason for a client effect");
        assert_eq!(acc.client_kernels.len(), 2, "both Native symbols land in client_kernels");
        assert!(acc.client_effect_note().is_some(), "client-effect note is populated");
    }

    /// EFFECT, KNOWN_PURE, and CLIENT_EFFECT are pairwise disjoint — no kernel can
    /// be classified two ways.
    #[test]
    fn effect_pure_and_client_effect_are_disjoint() {
        for m in EFFECT_KERNELS {
            assert!(
                !KNOWN_PURE_KERNELS.contains(m),
                "kernel `{m}` is in both EFFECT and KNOWN_PURE"
            );
            assert!(
                !CLIENT_EFFECT_KERNELS.contains(m),
                "kernel `{m}` is in both EFFECT and CLIENT_EFFECT"
            );
        }
        for m in CLIENT_EFFECT_KERNELS {
            assert!(
                !KNOWN_PURE_KERNELS.contains(m),
                "kernel `{m}` is in both CLIENT_EFFECT and KNOWN_PURE"
            );
        }
    }
}
