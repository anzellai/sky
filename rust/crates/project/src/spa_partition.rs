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
use hir::{Body, Expr, ExprId, LocalDef, LocalId, PatId, Pattern, Res, SkyDb};
use std::collections::{BTreeSet, HashMap, HashSet};
use std::path::Path;

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
    /// Reserved: a client-side effect. v1 puts every effect on the server (see
    /// `ServerOnly`), so nothing maps here yet — kept as the seam for a future
    /// opt-in (e.g. a client-local uuid/time) rather than deleted.
    #[allow(dead_code)]
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
    "Time", "Random", "Uuid", "Log", "Live", "Jobs", "Cli", "Tui", "Webview", "Context",
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
    "Ffi",
];

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
    // `sky check`s clean (mirrors the build's accept/reject gate). We do not
    // re-render the diagnostics here; a broken project is reported as such.
    let checked = ty::check_modules(db, &check_ids);
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
    let mut model_fields: Vec<ModelFieldTy> = Vec::new();

    match update_field {
        UpdateField::Def(update_def) => {
            let loc = db.def_loc(update_def);
            let (umod, uname) = loc
                .map(|l| (l.module, l.name.as_str().to_string()))
                .unwrap_or((entry, "update".to_string()));
            update_name = Some(format!("{}.{}", db.module_name(umod), uname));
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
            } else {
                return Err("update def has no body".into());
            }
        }
        UpdateField::Lambda(umod, body, root) => {
            update_name = Some(format!("{}.<lambda update>", db.module_name(umod)));
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

    Ok(SpaPartitionReport {
        project,
        entry_module: entry_module_name,
        update_name,
        branches,
        whole_update,
        tainted,
        model_fields,
        subscribes_topics,
        publishes,
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
            // Conservative ctx: `update` is an ordinary callee here, so a helper
            // that calls `update` is forced to server (soundness — never under-
            // mark). Msg-constant precision applies ONLY to update's own arms.
            collect(body, root, &mut acc, &CollectCtx::default());
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

    // Emit verdicts with a helpful reason. SERVER branches also carry their
    // derived RPC read-set / write-set (B1); CLIENT branches need no I/O.
    for i in 0..n {
        let f = &facts[i];
        if let Some(r) = &direct[i] {
            out.push(BranchVerdict {
                msg: f.label.clone(),
                server: true,
                reason: r.clone(),
                io: Some(compute_branch_io(body, arms[i].body, arms[i].pat, model_local, src)),
                msg_arg_tys: msg_arg_field_tys(body, arms[i].pat, src, locals),
            });
        } else if server[i] {
            out.push(BranchVerdict {
                msg: f.label.clone(),
                server: true,
                reason: compose_reason(&f.refs.scoped_updates, &by_name, &server, &direct),
                io: Some(compute_branch_io(body, arms[i].body, arms[i].pat, model_local, src)),
                msg_arg_tys: msg_arg_field_tys(body, arms[i].pat, src, locals),
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
            });
        }
    }
}

// ---------------------------------------------------------------------------
// B1 — per-server-branch read-set / write-set (the RPC I/O).
// ---------------------------------------------------------------------------

/// Compute the RPC read-set / write-set for ONE `update` arm (§13-§14). Walks
/// the arm body over the SAME HIR the verdict used:
///   * read-set  = every `field` in `Access(Var(model), field)`, PLUS the Msg
///     args the arm pattern binds.
///   * write-set = every `field` key of a tail `Update { base = Var(model), … }`.
///   * OVER-APPROXIMATE to the whole Model (sound) when `model` is used opaquely
///     — any `Var(model)` that is NOT the base of an `Access`/`Update` nor a bare
///     model returned in the final `(model, cmd)` tuple ⇒ `reads_whole_model`;
///     a returned model that is a fresh `Record` or flows through a helper ⇒
///     `writes_whole_model`. Under-approximating is a correctness bug — unknown
///     ⇒ send more (§14 B1).
fn compute_branch_io(
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
    collect_writes_tail(
        body,
        arm_body,
        model_local,
        &mut write_fields,
        &mut writes_whole,
        &mut allowed_bare,
    );

    let mut read_fields: BTreeSet<String> = BTreeSet::new();
    let mut reads_whole = false;
    collect_reads(
        body,
        arm_body,
        model_local,
        &allowed_bare,
        &mut read_fields,
        &mut reads_whole,
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
fn collect_reads(
    body: &Body,
    e: ExprId,
    model_local: Option<LocalId>,
    allowed_bare: &HashSet<ExprId>,
    read_fields: &mut BTreeSet<String>,
    reads_whole: &mut bool,
) {
    macro_rules! go {
        ($x:expr) => {
            collect_reads(body, $x, model_local, allowed_bare, read_fields, reads_whole)
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
            go!(*callee);
            for a in args {
                go!(*a);
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
            for d in defs {
                go!(d.body);
            }
            go!(*b);
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
/// A tail `{ model | … }` records its field keys; a bare `model` is a no-write
/// return (recorded in `allowed_bare` so the read walk does not count it as
/// opaque); a fresh `Record` or any other shape (a helper call producing the
/// model) ⇒ `writes_whole`.
fn collect_writes_tail(
    body: &Body,
    e: ExprId,
    model_local: Option<LocalId>,
    write_fields: &mut BTreeSet<String>,
    writes_whole: &mut bool,
    allowed_bare: &mut HashSet<ExprId>,
) {
    match &body.exprs[e] {
        Expr::Tuple(xs) if xs.len() == 2 => {
            let m = xs[0];
            match &body.exprs[m] {
                Expr::Update { base, fields } if is_model_var(body, *base, model_local) => {
                    for (n, _) in fields {
                        write_fields.insert(n.as_str().to_string());
                    }
                }
                Expr::Var(Res::Local(l)) if Some(*l) == model_local => {
                    // Bare `( model, cmd )` — returns model unchanged, writes
                    // nothing. Mark so the read walk does not over-approximate.
                    allowed_bare.insert(m);
                }
                // A fresh record, or `helper model` / any other producer — the
                // written shape is not a visible `{ model | … }`, so be sound.
                _ => *writes_whole = true,
            }
        }
        Expr::Let { body: b, .. } => {
            collect_writes_tail(body, *b, model_local, write_fields, writes_whole, allowed_bare)
        }
        Expr::If { arms, els } => {
            for (_, t) in arms {
                collect_writes_tail(body, *t, model_local, write_fields, writes_whole, allowed_bare);
            }
            collect_writes_tail(body, *els, model_local, write_fields, writes_whole, allowed_bare);
        }
        Expr::Case { branches, .. } => {
            for br in branches {
                collect_writes_tail(
                    body, br.body, model_local, write_fields, writes_whole, allowed_bare,
                );
            }
        }
        // The arm did not evaluate to a recognizable `(model', cmd)` tuple (e.g.
        // it delegates to a helper returning the whole pair) — be conservative.
        _ => *writes_whole = true,
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
        // Unknown family -> conservative SERVER (fail-closed), never Neutral.
        assert_eq!(classify_kernel("BrandNewEffect", "boom"), KernelClass::ServerOnly);
    }

    /// EFFECT and KNOWN_PURE are disjoint — no kernel can be both server and pure.
    #[test]
    fn effect_and_pure_are_disjoint() {
        for m in EFFECT_KERNELS {
            assert!(
                !KNOWN_PURE_KERNELS.contains(m),
                "kernel `{m}` is in both EFFECT and KNOWN_PURE"
            );
        }
    }
}
