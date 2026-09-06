//! `sky spa-split <entry.sky> --out <dir>` — the Sky.Spa **auto-split
//! generator** (doc `docs/skyspa/auto-split.md` §12/§14 B3). It reads ONE
//! Sky.Spa project whose effects sit inline in `update` and emits **two ordinary
//! Sky source projects** the *existing* compiler builds unchanged:
//!
//!   * **backend/** — the whole app as a normal native Sky server, copied
//!     verbatim, with `main` replaced by a `Sky.Http.Server` that exposes one
//!     generated `POST /_rpc/<Msg>` endpoint per SERVER branch and serves the
//!     wasm client's `dist/`. Each handler decodes the branch's read-set, reuses
//!     the app's own `init` + `update` to run the REAL effect server-side, and
//!     answers with the write-set. Because it reuses `update`, the effect body
//!     (here `saveN`, File I/O) is never rewritten.
//!   * **frontend/** — the same app built to wasm: pure branches run verbatim
//!     client-local (zero round-trip); each SERVER branch is rewritten to a
//!     `Spa.postJson … "/_rpc/<Msg>" … Applied<Msg>` RPC over the SHARED codecs,
//!     with a generated `Applied<Msg>` arm folding the write-set back in.
//!     Server-tainted top-level bindings (per the partition analysis) are
//!     **omitted** from the client source — the security spine of the split.
//!   * **shared/Shared.sky** — the one wire contract (`<Msg>Req` / `<Msg>Resp`
//!     + codecs), copied into BOTH projects' `src/`.
//!
//! Source-to-source only: this reuses `spa_partition`'s analysis (verdicts +
//! per-branch read/write sets + tainted bindings + typed Model fields) and the
//! syntax crate's CST for verbatim slicing. It never lowers, emits Go, or
//! touches the compiler IR / the runtime-narrowing floor.
//!
//! Scope handled fully: a single-entry-module app — pure + N effectful branches,
//! field-precise read/write sets, **Msg-arg-typed RPC inputs** (a `Toggle Int`
//! puts a typed `id : Int` into the request; the backend reconstructs
//! `update (Toggle p.id) m`; the frontend sends `{ id = id }`), **non-primitive
//! field codecs** (a `List Todo` field wires to the project's own
//! `todoListCodec`, which — with the `Todo` type + `todoCodec` it needs — is
//! COPIED into `Shared`; `List X` / `Maybe X` fall back to `Codec.list` /
//! `Codec.maybe`), and the **whole-model fallback** (a branch reading/writing
//! `model` opaquely carries every field, each wired through the same resolver).
//! Fail-closed rather than mis-handled: a field whose codec cannot be resolved
//! is an Err (never a placeholder that won't compile).
//!
//! **Multi-module apps (§17).** A project whose `src/` spans several modules is
//! split by classifying EACH sibling module by whether it contains a
//! server-tainted def (the partition analysis already tracks these across every
//! module):
//!   * a module with NO tainted def is **pure** → copied verbatim into BOTH the
//!     frontend and backend trees (`Shared` imports it for any wire type/codec it
//!     declares, rather than re-copying the def);
//!   * a module with ANY tainted def is routed to the **backend only** — the
//!     whole module (§17's simpler+sound rule), never emitted into the wasm
//!     frontend nor imported by it.
//! The security invariant holds: an effectful module can never reach the client.
//! If a frontend-retained def references a (pure) def that lives in a
//! backend-only module, the generator refuses with a clear Err rather than leak
//! the module — fail-closed, never mis-generate.

use crate::spa_partition::{self, BranchIo, ModelFieldTy, SpaPartitionReport};
use base::{DefId, ModuleId};
use hir::SkyDb;
use skydb::SkyDatabase;
use std::collections::{BTreeSet, HashMap, HashSet};
use std::path::Path;
use syntax::ast::{AstNode, SourceFile};
use syntax::SyntaxKind;

// ---------------------------------------------------------------------------
// Codec resolution — the non-primitive-field engine (§14 #2).
// ---------------------------------------------------------------------------

/// A top-level `Codec <T>` binding the INPUT project defines — e.g. the user's
/// `todoCodec : Codec Todo` / `todoListCodec : Codec (List Todo)`. The generator
/// resolves a non-primitive Req/Resp field's codec against these first (priority
/// (a)): it references the binding by name and COPIES its def (+ the type + any
/// helper codec it needs) into the generated `Shared` module.
struct CodecBinding {
    name: String,
    def: DefId,
    /// The module the codec binding lives in. Entry-module codecs are COPIED into
    /// `Shared`; pure-sibling-module codecs are referenced via an `import` of that
    /// module into `Shared` instead (the module is copied whole to both trees).
    module: ModuleId,
    coded_ty: ty::Ty,
    /// The declared coded type as the user wrote it (`List Todo`), sliced from
    /// the binding's type annotation — the surface used for the generated Req/
    /// Resp field type, because the solved `coded_ty` has type aliases EXPANDED
    /// (a `List Todo` field would otherwise render as `List { id : Int, … }`).
    surface: String,
}

/// A resolved field codec: the codec expression to emit + the surface type name
/// to give the generated record field.
struct ResolvedCodec {
    codec: String,
    surface: String,
}

/// Resolves the `Std.Codec` expression for a field type, accumulating which user
/// codec bindings must be copied into `Shared`. Priority (§14 #2):
///   (a) a project `Codec <T>` binding whose T matches   → reference + copy it
///   (b) `List X` / `Maybe X` with a resolvable inner    → `Codec.list <inner>`
///   (c) a JSON primitive                                → `Codec.int` / …
///   (d) otherwise                                       → a clear Err (never a
///       placeholder codec that will not compile).
struct CodecResolver<'a> {
    registry: &'a [CodecBinding],
    /// Names of user codec bindings referenced (→ copied into `Shared`).
    needed: BTreeSet<String>,
}

impl<'a> CodecResolver<'a> {
    fn new(registry: &'a [CodecBinding]) -> Self {
        CodecResolver {
            registry,
            needed: BTreeSet::new(),
        }
    }

    fn resolve(&mut self, t: &ty::Ty) -> Result<ResolvedCodec, String> {
        // (a) A project-defined `Codec <T>` binding for exactly this type. Use
        // the binding's DECLARED surface for the field type (aliases un-expanded).
        for b in self.registry {
            if ty_matches(t, &b.coded_ty) {
                self.needed.insert(b.name.clone());
                return Ok(ResolvedCodec {
                    codec: b.name.clone(),
                    surface: b.surface.clone(),
                });
            }
        }
        // (b) List X / Maybe X built from the inner codec.
        if let ty::Ty::App(name, args) = t {
            let tail = tail_seg(name.as_str());
            if tail == "List" && args.len() == 1 {
                let inner = self.resolve(&args[0])?;
                return Ok(ResolvedCodec {
                    codec: format!("(Codec.list {})", inner.codec),
                    surface: format!("List {}", wrap_arg(&inner.surface)),
                });
            }
            if tail == "Maybe" && args.len() == 1 {
                let inner = self.resolve(&args[0])?;
                return Ok(ResolvedCodec {
                    codec: format!("(Codec.maybe {})", inner.codec),
                    surface: format!("Maybe {}", wrap_arg(&inner.surface)),
                });
            }
            // (c) JSON primitives.
            if args.is_empty() {
                let codec = match tail {
                    "Int" => Some("Codec.int"),
                    "String" => Some("Codec.string"),
                    "Bool" => Some("Codec.bool"),
                    "Float" => Some("Codec.float"),
                    _ => None,
                };
                if let Some(c) = codec {
                    return Ok(ResolvedCodec {
                        codec: c.to_string(),
                        surface: tail.to_string(),
                    });
                }
            }
        }
        // (d) No codec — fail closed with an actionable message.
        Err(format!(
            "no codec for a field of type `{0}` — define a top-level `Codec {0}` binding in the project (spa-split copies it into Shared) or reduce the field to `List`/`Maybe`/`Int`/`String`/`Bool`/`Float`",
            render_ty(t)
        ))
    }
}

/// Parenthesise a type argument if it is an application (`List Todo` → wrap;
/// `Todo` → leave) so `List (List Todo)` renders correctly.
fn wrap_arg(surface: &str) -> String {
    if surface.contains(' ') {
        format!("({surface})")
    } else {
        surface.to_string()
    }
}

/// Surface rendering of a type for an error message / a generated field type
/// (`List Todo`, `Todo`, `Int`). Tail-normalises folded nominal names.
fn render_ty(t: &ty::Ty) -> String {
    match t {
        ty::Ty::App(name, args) => {
            let tail = tail_seg(name.as_str());
            if args.is_empty() {
                tail.to_string()
            } else {
                let inner: Vec<String> = args.iter().map(render_ty).collect();
                format!("{} {}", tail, inner.join(" "))
            }
        }
        ty::Ty::Var(n) => n.as_str().to_string(),
        ty::Ty::Unit => "()".to_string(),
        _ => "any".to_string(),
    }
}

/// The tail segment of a folded nominal name (`Sky.Core.List.List` → `List`).
fn tail_seg(name: &str) -> &str {
    name.rsplit('.').next().unwrap_or(name)
}

/// Structural type equality, tolerant of home-folding (compares nominal tails)
/// and of type-variable renaming — enough to match a field's type against a
/// user `Codec <T>` binding's T (`List Todo` ≡ `List Todo`).
fn ty_matches(a: &ty::Ty, b: &ty::Ty) -> bool {
    use ty::Ty;
    match (a, b) {
        (Ty::App(n1, a1), Ty::App(n2, a2)) => {
            tail_seg(n1.as_str()) == tail_seg(n2.as_str())
                && a1.len() == a2.len()
                && a1.iter().zip(a2).all(|(x, y)| ty_matches(x, y))
        }
        (Ty::Tuple(x), Ty::Tuple(y)) => {
            x.len() == y.len() && x.iter().zip(y).all(|(p, q)| ty_matches(p, q))
        }
        (Ty::Record(f1, _), Ty::Record(f2, _)) => {
            f1.len() == f2.len()
                && f1.iter().zip(f2).all(|((n1, t1), (n2, t2))| {
                    n1.as_str() == n2.as_str() && ty_matches(t1, t2)
                })
        }
        (Ty::Var(_), Ty::Var(_)) => true,
        (Ty::Unit, Ty::Unit) => true,
        (Ty::Fun(a1, b1), Ty::Fun(a2, b2)) => ty_matches(a1, a2) && ty_matches(b1, b2),
        _ => false,
    }
}

/// Collect every nominal type NAME (tail-normalised) appearing in a type — used
/// to discover which project type declarations a wire field drags in.
fn collect_ty_names(t: &ty::Ty, out: &mut BTreeSet<String>) {
    match t {
        ty::Ty::App(name, args) => {
            out.insert(tail_seg(name.as_str()).to_string());
            for a in args {
                collect_ty_names(a, out);
            }
        }
        ty::Ty::Tuple(xs) => {
            for x in xs {
                collect_ty_names(x, out);
            }
        }
        ty::Ty::Record(fields, _) => {
            for (_, ft) in fields {
                collect_ty_names(ft, out);
            }
        }
        ty::Ty::Fun(a, b) => {
            collect_ty_names(a, out);
            collect_ty_names(b, out);
        }
        ty::Ty::Var(_) | ty::Ty::Unit | ty::Ty::Error => {}
    }
}

/// What `generate` produced, for the CLI + the acceptance test.
pub struct SpaSplitReport {
    pub out_dir: String,
    /// Project-relative paths of every file written.
    pub files: Vec<String>,
    pub server_branches: Vec<String>,
    pub client_branches: Vec<String>,
    /// Server-tainted top-level bindings OMITTED from the frontend source.
    pub excluded: Vec<String>,
    pub notes: Vec<String>,
}

/// Modules the frontend must NOT import — physically server-only effect
/// families whose FFI cannot run in the wasm client. Keyed off the module
/// name's tail segment. (`Http`/`Time`/`Random`/`Uuid` are client-*capable*
/// and left importable; the split routes their effects to the server anyway.)
fn is_server_only_module(module_path: &str) -> bool {
    let tail = module_path.rsplit('.').next().unwrap_or(module_path);
    matches!(
        tail,
        "File" | "Db" | "System" | "Process" | "Io" | "Server" | "Auth" | "RateLimit" | "Middleware"
    )
}

/// Render `s` as a Sky string literal (`redis://h:6379` → `"redis://h:6379"`),
/// escaping the two characters that would otherwise break the literal. Used to
/// bake the `--broker` URL into the generated backend's `spaBroker` binding.
fn sky_string_literal(s: &str) -> String {
    format!("\"{}\"", s.replace('\\', "\\\\").replace('"', "\\\""))
}

fn lower_first(s: &str) -> String {
    let mut c = s.chars();
    match c.next() {
        Some(f) => f.to_lowercase().collect::<String>() + c.as_str(),
        None => String::new(),
    }
}

/// The head constructor NAME of a branch label (`"SaveTagged tag"` → `"SaveTagged"`).
fn ctor_name(label: &str) -> &str {
    label.split_whitespace().next().unwrap_or(label)
}

fn slice<'a>(src: &'a str, node: &syntax::SyntaxNode) -> &'a str {
    let r = node.text_range();
    let a = u32::from(r.start()) as usize;
    let b = u32::from(r.end()) as usize;
    src.get(a..b).unwrap_or("")
}

/// The first `UpperIdent` token under a node (a pattern's head ctor, etc.).
fn first_upper(node: &syntax::SyntaxNode) -> Option<String> {
    node.descendants_with_tokens()
        .filter_map(|e| e.into_token())
        .find(|t| t.kind() == SyntaxKind::UpperIdent)
        .map(|t| t.text().to_string())
}

/// One SERVER branch's fully-resolved wire contract: the `<Msg>Req` (read-set
/// fields ∪ Msg args) and `<Msg>Resp` (write-set) records, EACH field carrying a
/// real, compilable `Std.Codec` combinator (never a placeholder).
struct Wire {
    name: String,
    req_fields: Vec<ModelFieldTy>,
    resp_fields: Vec<ModelFieldTy>,
}

/// Look a Model field's typed entry up by name.
fn lookup_field(model_fields: &[ModelFieldTy], name: &str) -> ModelFieldTy {
    model_fields
        .iter()
        .find(|f| f.name == name)
        .cloned()
        .unwrap_or(ModelFieldTy {
            name: name.to_string(),
            ty_name: "any".into(),
            codec: None,
            ty: None,
        })
}

/// Build one branch's resolved [`Wire`], resolving every field's codec through
/// `resolver` (which records the user codec bindings that must be copied into
/// `Shared`). Fails closed: a field whose codec cannot be resolved returns an
/// Err naming the field + type, so the generator refuses rather than emit a
/// `Shared` that will not compile.
fn build_wire(
    name: &str,
    io: &BranchIo,
    msg_arg_tys: &[ModelFieldTy],
    model_fields: &[ModelFieldTy],
    resolver: &mut CodecResolver,
) -> Result<Wire, String> {
    // Request = read-set (or whole model) + Msg args. Dedup by name (a Msg arg
    // shadowing a model field would otherwise emit a duplicate record field).
    let mut req: Vec<ModelFieldTy> = if io.reads_whole_model {
        model_fields.to_vec()
    } else {
        io.read_fields
            .iter()
            .map(|f| lookup_field(model_fields, f))
            .collect()
    };
    for a in msg_arg_tys {
        if !req.iter().any(|f| f.name == a.name) {
            req.push(a.clone());
        }
    }
    let mut resp: Vec<ModelFieldTy> = if io.writes_whole_model {
        model_fields.to_vec()
    } else {
        io.write_fields
            .iter()
            .map(|f| lookup_field(model_fields, f))
            .collect()
    };
    for f in req.iter_mut().chain(resp.iter_mut()) {
        if f.codec.is_none() {
            let t = f
                .ty
                .clone()
                .ok_or_else(|| format!("branch `{name}` field `{}` has no recoverable type — cannot wire a codec", f.name))?;
            let r = resolver
                .resolve(&t)
                .map_err(|e| format!("branch `{name}`, field `{}`: {e}", f.name))?;
            f.codec = Some(r.codec);
            f.ty_name = r.surface;
        }
    }
    Ok(Wire {
        name: name.to_string(),
        req_fields: req,
        resp_fields: resp,
    })
}

/// A `type alias <Name> = { … }` + its codec, rendered from a resolved field
/// list (every field's `codec` is `Some`).
fn render_wire_type(name: &str, codec_name: &str, fields: &[ModelFieldTy]) -> String {
    let mut out = String::new();
    if fields.is_empty() {
        out.push_str(&format!("type alias {name} =\n    {{}}\n\n\n"));
    } else {
        out.push_str(&format!("type alias {name} =\n"));
        for (i, f) in fields.iter().enumerate() {
            let lead = if i == 0 { "    { " } else { "    , " };
            out.push_str(&format!("{lead}{} : {}\n", f.name, f.ty_name));
        }
        out.push_str("    }\n\n\n");
    }
    out.push_str(&format!("{codec_name} : Codec {name}\n{codec_name} =\n"));
    out.push_str(&format!("    Codec.object {name}\n"));
    for f in fields {
        // Every field is resolved by construction; the fallback is defensive.
        let codec = f.codec.clone().unwrap_or_else(|| "Codec.string".into());
        out.push_str(&format!("        |> Codec.field \"{0}\" .{0} {1}\n", f.name, codec));
    }
    out.push_str("        |> Codec.buildObject\n");
    out
}

/// Build `shared/Shared.sky`: the copied user types + codecs (the transitive
/// closure the wire codecs reference) followed by the generated per-branch
/// `<Msg>Req` / `<Msg>Resp` records + codecs. `copied_decls` is the verbatim
/// source of the copied declarations (in source order), `copied_exposing` the
/// names to re-export for them.
fn gen_shared(
    wires: &[Wire],
    imports: &[String],
    copied_decls: &str,
    copied_exposing: &[String],
) -> String {
    let mut exposing: Vec<String> = copied_exposing.to_vec();
    let mut bodies = String::new();
    for w in wires {
        let req_ty = format!("{}Req", w.name);
        let resp_ty = format!("{}Resp", w.name);
        let req_codec = format!("{}ReqCodec", lower_first(&w.name));
        let resp_codec = format!("{}RespCodec", lower_first(&w.name));
        exposing.push(req_ty.clone());
        exposing.push(req_codec.clone());
        exposing.push(resp_ty.clone());
        exposing.push(resp_codec.clone());
        bodies.push_str(&format!(
            "-- | {} RPC — request = read-set + Msg args, response = write-set.\n",
            w.name
        ));
        bodies.push_str(&render_wire_type(&req_ty, &req_codec, &w.req_fields));
        bodies.push_str("\n\n");
        bodies.push_str(&render_wire_type(&resp_ty, &resp_codec, &w.resp_fields));
        bodies.push_str("\n\n");
    }
    // Dedup while preserving order (a type + its constructor could collide).
    let mut seen: HashSet<String> = HashSet::new();
    exposing.retain(|e| seen.insert(e.clone()));
    // A client-only app (no server branches) shares NO wire types, so the
    // exposing list is empty. An empty `exposing (\n    )` clause is a parse
    // error (there is no leading `    ,` for the `(` rewrite to land on), so fall
    // back to `exposing (..)` — a valid header for an export-nothing module.
    let module_header = if exposing.is_empty() {
        "module Shared exposing (..)".to_string()
    } else {
        let exposing_list = exposing
            .iter()
            .map(|s| format!("    , {s}"))
            .collect::<Vec<_>>()
            .join("\n")
            .replacen("    ,", "    (", 1);
        format!("module Shared exposing\n{exposing_list}\n    )")
    };
    let import_block = imports.join("\n");
    let copied_block = if copied_decls.trim().is_empty() {
        String::new()
    } else {
        format!(
            "-- Project types + codecs the wire contract references, copied verbatim\n\
             -- from the input so BOTH projects share ONE definition.\n{}\n\n\n",
            copied_decls.trim_end()
        )
    };
    format!(
        "-- | Shared — the ONE RPC wire contract compiled into BOTH the Sky.Spa wasm\n\
         -- client and the native Sky.Http.Server backend. Generated by `sky spa-split`.\n\
         -- One type, one codec, one wire shape: change a field and BOTH stop compiling.\n\
         {module_header}\n\n\
         {import_block}\n\n\n\
         {copied_block}{bodies}"
    )
    .trim_end()
    .to_string()
        + "\n"
}

struct ImportInfo {
    module_path: String,
    text: String,
}

fn collect_imports(file: &SourceFile, src: &str) -> Vec<ImportInfo> {
    file.imports()
        .map(|imp| ImportInfo {
            module_path: imp.name().map(|n| n.text()).unwrap_or_default(),
            text: slice(src, imp.syntax()).to_string(),
        })
        .collect()
}

fn has_module(imports: &[ImportInfo], path: &str) -> bool {
    imports.iter().any(|i| i.module_path == path)
}

/// A module name → its `src/`-relative file path (`Domain` → `Domain.sky`,
/// `Data.Todo` → `Data/Todo.sky`), matching the compiler's dotted-module layout.
fn module_relpath(name: &str) -> String {
    format!("{}.sky", name.replace('.', "/"))
}

/// `role` is `"frontend"` or `"backend"`. The `[spa]` marker records that this
/// project was GENERATED by `sky spa-split` — `sky build`/`sky run` read it (via
/// `is_generated_split_project`) to NOT auto-split it again. It is the load-bearing
/// recursion guard: the generated frontend is itself a `Spa.app` (it imports
/// `Std.Spa`), so without this marker a plain `sky build frontend/src/Main.sky`
/// would re-split forever.
fn sky_toml(name: &str, role: &str) -> String {
    format!(
        "name = \"{name}\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n\n[spa]\ngenerated = true\nrole = \"{role}\"\n"
    )
}

/// Generate the two projects. `out_dir` gets `shared/`, `backend/`, `frontend/`.
pub fn generate(
    repo_root: &Path,
    project_dir: &Path,
    entry_module: Option<&str>,
    out_dir: &Path,
    broker_url: Option<&str>,
) -> Result<SpaSplitReport, String> {
    // Fail-closed gate: the generator writes the wasm frontend, so an
    // unclassified effect kernel silently defaulting to client would be a real
    // leak. Refuse to emit if the compiler knows a kernel the auto-split
    // classification has not decided a split side for. (The
    // `classification_is_exhaustive` test keeps this empty on a shipped tree; this
    // is the runtime backstop for a kernel added ahead of the lists.)
    let gaps = spa_partition::unclassified_kernel_families();
    if !gaps.is_empty() {
        return Err(format!(
            "kernel module(s) {} are not classified for the Sky.Spa auto-split — add each to EFFECT (server) or KNOWN_PURE (client) in spa_partition::classify_kernel; defaulting an unknown kernel to client would leak it into the wasm frontend.",
            gaps.join(", ")
        ));
    }

    let (db, entry, check_ids) = crate::build::load_source_db(repo_root, project_dir, entry_module)?;
    let proj_name = project_dir
        .file_name()
        .map(|s| s.to_string_lossy().to_string())
        .unwrap_or_else(|| "app".to_string());
    let report: SpaPartitionReport =
        spa_partition::analyze_loaded(&db, entry, &check_ids, proj_name.clone())?;

    // The generator needs a per-branch `case msg of` split.
    if report.whole_update.is_some() || report.branches.is_empty() {
        return Err(
            "cannot auto-split: `update` has no resolvable `case msg of` (per-branch analysis unavailable)".into(),
        );
    }

    let mut notes: Vec<String> = Vec::new();

    // ---- multi-module routing (§17) ----
    // Every project module other than the entry is classified by whether it
    // contains a server-tainted def. A module with NO tainted def is PURE and is
    // copied into BOTH trees; a module with ANY tainted def is routed to the
    // BACKEND ONLY (whole module → backend, per §17's simpler+sound rule) and
    // its effects never reach the wasm frontend. `report.tainted` already tracks
    // server-tainted top-level bindings across every module.
    let entry_name = db.module_name(entry).to_string();
    let mut tainted_by_module: HashMap<String, HashSet<String>> = HashMap::new();
    for t in &report.tainted {
        tainted_by_module
            .entry(t.module.clone())
            .or_default()
            .insert(t.name.clone());
    }
    let module_is_backend_only = |mid: ModuleId, db: &SkyDatabase| -> bool {
        let mname = db.module_name(mid).to_string();
        tainted_by_module
            .get(&mname)
            .map(|s| !s.is_empty())
            .unwrap_or(false)
    };
    let mut backend_only_mods: Vec<ModuleId> = Vec::new();
    let mut pure_sibling_mods: Vec<ModuleId> = Vec::new();
    for m in check_ids.iter().copied().filter(|m| *m != entry) {
        if module_is_backend_only(m, &db) {
            backend_only_mods.push(m);
        } else {
            pure_sibling_mods.push(m);
        }
    }
    let backend_only_names: HashSet<String> = backend_only_mods
        .iter()
        .map(|m| db.module_name(*m).to_string())
        .collect();

    // Leak check (fail-closed): a PURE def that happens to live in a backend-only
    // (mixed) module cannot be reached by the frontend, because the whole module
    // is backend-only. If a frontend-retained def references such a pure def, the
    // frontend would need a value from a server-tainted module — a real error, so
    // we refuse with a clear message rather than emit a frontend that will not
    // compile (or, worse, silently drag the module in). Server-tainted defs are
    // never in this set, so a normal server helper referenced from a rewritten
    // server branch does NOT trip it.
    let mut backend_only_pure_defs: HashSet<DefId> = HashSet::new();
    for m in &backend_only_mods {
        let mname = db.module_name(*m).to_string();
        let tainted_here = tainted_by_module.get(&mname);
        for td in &db.resolve(*m).top_defs {
            let is_tainted = tainted_here
                .map(|s| s.contains(td.name.as_str()))
                .unwrap_or(false);
            if !is_tainted {
                backend_only_pure_defs.insert(td.def);
            }
        }
    }
    if !backend_only_pure_defs.is_empty() {
        // Frontend keeps: entry's non-tainted defs (update is rewritten, so its
        // server branches no longer reference the backend module) + every pure
        // sibling module's defs (copied verbatim).
        let entry_tainted = tainted_by_module.get(&entry_name);
        let mut roots: Vec<(ModuleId, DefId, String)> = Vec::new();
        for td in &db.resolve(entry).top_defs {
            let is_tainted = entry_tainted
                .map(|s| s.contains(td.name.as_str()))
                .unwrap_or(false);
            if !is_tainted {
                roots.push((entry, td.def, td.name.as_str().to_string()));
            }
        }
        for m in &pure_sibling_mods {
            for td in &db.resolve(*m).top_defs {
                roots.push((*m, td.def, td.name.as_str().to_string()));
            }
        }
        for (mid, def, name) in &roots {
            for c in spa_partition::body_def_callees(&db, *mid, *def) {
                if backend_only_pure_defs.contains(&c) {
                    let (cmod, cname) = db
                        .def_loc(c)
                        .map(|l| (db.module_name(l.module).to_string(), l.name.as_str().to_string()))
                        .unwrap_or_default();
                    return Err(format!(
                        "cannot auto-split: the frontend-retained def `{name}` references `{cname}` in module `{cmod}`, which is server-tainted and routed backend-only. A pure client value cannot depend on a server-tainted module — move `{cname}` into a PURE module (e.g. a `Domain` module) shared by both trees. (Refusing rather than leaking a server-tainted module into the wasm frontend.)"
                    ));
                }
            }
        }
    }
    if !backend_only_mods.is_empty() || !pure_sibling_mods.is_empty() {
        let pure: Vec<String> = pure_sibling_mods
            .iter()
            .map(|m| db.module_name(*m).to_string())
            .collect();
        let back: Vec<String> = backend_only_mods
            .iter()
            .map(|m| db.module_name(*m).to_string())
            .collect();
        notes.push(format!(
            "multi-module split: pure module(s) {pure:?} copied to BOTH trees; server-tainted module(s) {back:?} routed backend-only (never emitted into the wasm frontend)."
        ));
    }

    // SERVER branches, keyed by ctor name, with their RPC I/O + typed Msg args.
    let mut server: Vec<(String, BranchIo)> = Vec::new();
    let mut server_args: HashMap<String, Vec<ModelFieldTy>> = HashMap::new();
    let mut client_names: Vec<String> = Vec::new();
    for b in &report.branches {
        let name = ctor_name(&b.msg).to_string();
        if b.server {
            let io = b.io.clone().ok_or_else(|| {
                format!("server branch `{name}` has no derived RPC I/O")
            })?;
            server.push((name.clone(), io));
            server_args.insert(name, b.msg_arg_tys.clone());
        } else {
            client_names.push(name);
        }
    }
    if server.is_empty() {
        notes.push("no SERVER branches — the frontend is fully client-local and the backend only serves static assets.".into());
    }

    let parse = db.module_parse(entry);
    let src = parse.syntax().text().to_string();
    let file = parse.tree();

    let imports = collect_imports(&file, &src);

    // Tainted binding names → excluded from the frontend.
    let tainted_names: Vec<String> = report.tainted.iter().map(|t| t.name.clone()).collect();
    let tainted_set: HashSet<String> = tainted_names.iter().cloned().collect();

    // ---- resolve the wire codecs (§14 #2) ----
    // Registry of the project's own `Codec <T>` bindings, scanned across the
    // entry module AND every pure sibling module (a codec may live in `Domain`);
    // the resolver records which ones a wire field references, and each binding
    // remembers its module so Shared can COPY an entry codec but IMPORT a
    // sibling one.
    let mut codec_scan_mods: Vec<ModuleId> = vec![entry];
    codec_scan_mods.extend(pure_sibling_mods.iter().copied());
    let registry = build_codec_registry(&db, &codec_scan_mods);
    let mut resolver = CodecResolver::new(&registry);
    let mut wires: Vec<Wire> = Vec::new();
    for (name, io) in &server {
        let args = server_args.get(name).cloned().unwrap_or_default();
        wires.push(build_wire(name, io, &args, &report.model_fields, &mut resolver)?);
    }

    // ---- the copy closure Shared needs (§14 #2) ----
    // Value defs: the referenced user codecs + their transitive project-local,
    // non-tainted helper closure. Type decls: everything the wire field types /
    // copied codec bodies mention that is a project type declaration.
    let project_types = project_type_decls(&file);
    let mut copied_values = compute_value_copy(&db, entry, &registry, &resolver.needed, &tainted_set);
    // A record-alias constructor (`Codec.object Todo`) resolves to a def named
    // like the type — keep those in the TYPE-copy set, never the value set.
    copied_values.retain(|n| !project_types.contains_key(n));
    let mut seed_ty: BTreeSet<String> = BTreeSet::new();
    for w in &wires {
        for f in w.req_fields.iter().chain(w.resp_fields.iter()) {
            if let Some(t) = &f.ty {
                collect_ty_names(t, &mut seed_ty);
            }
        }
    }
    let copied_types = compute_type_copy(&file, &project_types, &seed_ty, &copied_values);
    let mut copied_names: HashSet<String> = HashSet::new();
    copied_names.extend(copied_values.iter().cloned());
    copied_names.extend(copied_types.iter().cloned());
    let copied_decls = render_copied_decls(&file, &src, &copied_names);
    let copied_exposing = copied_exposing_list(&project_types, &copied_types, &copied_values);

    // ---- pure sibling modules the wire references (Shared imports them) ----
    // A referenced codec or a wire-field type that is DECLARED in a pure sibling
    // module is NOT copied into Shared (the module is copied whole to both
    // trees); Shared imports the module instead. A referenced codec that lives in
    // a backend-only module is a real error (the wire would need a value from a
    // server-tainted module) — fail closed.
    let mut needed_siblings: BTreeSet<String> = BTreeSet::new();
    for b in &registry {
        if resolver.needed.contains(&b.name) && b.module != entry {
            let bmod = db.module_name(b.module).to_string();
            if backend_only_names.contains(&bmod) {
                return Err(format!(
                    "cannot auto-split: the wire references codec `{}` in module `{bmod}`, which is server-tainted and routed backend-only. Move the codec into a PURE module shared by both trees. (Refusing rather than leaking a server-tainted module into the wasm frontend / Shared.)",
                    b.name
                ));
            }
            needed_siblings.insert(bmod);
        }
    }
    // Wire-field types declared in a pure sibling module (e.g. `Todo` in `Domain`).
    for m in &pure_sibling_mods {
        let mname = db.module_name(*m).to_string();
        let mparse = db.module_parse(*m);
        let mfile = mparse.tree();
        let mtypes = project_type_decls(&mfile);
        if seed_ty.iter().any(|n| mtypes.contains_key(n)) {
            needed_siblings.insert(mname);
        }
    }
    let shared_imports = shared_import_lines(&imports, &needed_siblings, &backend_only_names);

    // update param names + the update annotation + the model type name.
    let update_decl = file
        .decls()
        .find(|d| decl_name(d) .as_deref()== Some("update") && is_value_decl(d));
    let (msg_param, model_param) = update_decl
        .as_ref()
        .map(|d| value_params(d))
        .map(|ps| {
            (
                ps.first().map(|s| s.trim().to_string()).filter(|s| !s.is_empty()).unwrap_or_else(|| "msg".into()),
                ps.get(1).map(|s| s.trim().to_string()).filter(|s| !s.is_empty()).unwrap_or_else(|| "model".into()),
            )
        })
        .unwrap_or_else(|| ("msg".into(), "model".into()));
    let update_anno = decl_text_by(&file, &src, "update", DeclKind::TypeAnno)
        .unwrap_or_else(|| "update : Msg -> Model -> ( Model, Cmd Msg )".to_string());
    let model_ty = model_type_name(&file, &src).unwrap_or_else(|| "Model".to_string());

    // ---- write the three trees ----
    let shared_src = gen_shared(&wires, &shared_imports, &copied_decls, &copied_exposing);
    let push_mode = report.subscribes_topics || report.publishes;
    let broker_url = broker_url.map(str::trim).filter(|s| !s.is_empty());
    if push_mode {
        match broker_url {
            Some(url) => notes.push(format!(
                "server->client PUSH enabled: mounted `GET /_sky/sub` (SSE) + a shared broker; RPC handlers fan their returned Cmd.publish through it. Cross-replica broker BAKED via --broker ({url}); SKY_LIVE_BROKER_URL still overrides it."
            )),
            None => notes.push(
                "server->client PUSH enabled: mounted `GET /_sky/sub` (SSE) + a shared broker; RPC handlers fan their returned Cmd.publish through it. In-process broker (single replica); pass --broker <url> (or set SKY_LIVE_BROKER_URL) for cross-replica fan-out.".into(),
            ),
        }
    } else if broker_url.is_some() {
        notes.push(
            "note: --broker <url> was given but the app has no Cmd.publish / Sub.subscribeTopic, so no push broker is generated; the flag is ignored.".into(),
        );
    }
    let backend_src = gen_backend(&file, &src, &imports, &server, &report.model_fields, &copied_names, push_mode, broker_url)?;
    let frontend_src = gen_frontend(
        &file,
        &src,
        &imports,
        &server,
        &client_names,
        &tainted_names,
        &copied_names,
        &msg_param,
        &model_param,
        &update_anno,
        &model_ty,
        &backend_only_names,
    )?;

    let mut files: Vec<String> = Vec::new();
    let write = |rel: &str, content: &str, files: &mut Vec<String>| -> Result<(), String> {
        let full = out_dir.join(rel);
        if let Some(p) = full.parent() {
            std::fs::create_dir_all(p).map_err(|e| format!("mkdir {}: {e}", p.display()))?;
        }
        std::fs::write(&full, content).map_err(|e| format!("write {}: {e}", full.display()))?;
        files.push(rel.to_string());
        Ok(())
    };

    write("shared/Shared.sky", &shared_src, &mut files)?;
    write("backend/src/Shared.sky", &shared_src, &mut files)?;
    write("frontend/src/Shared.sky", &shared_src, &mut files)?;
    write("backend/src/Main.sky", &backend_src, &mut files)?;
    write("frontend/src/Main.sky", &frontend_src, &mut files)?;
    // The generated projects must be able to REBUILD any third-party imports the
    // app uses: carry the `[dependencies]` (Sky packages) + `["go.dependencies"]`
    // (Go FFI) sections into each manifest, and copy the fetched `.skydeps/` /
    // `sky-ffi/` trees alongside (below). Without this, an app that imports an
    // external Sky library analyses fine but the generated frontend/backend can't
    // resolve the import.
    let dep_sections = emit_dep_sections(project_dir);
    write(
        "backend/sky.toml",
        &format!("{}{dep_sections}", sky_toml(&format!("{proj_name}-backend"), "backend")),
        &mut files,
    )?;
    write(
        "frontend/sky.toml",
        &format!("{}{dep_sections}", sky_toml(&format!("{proj_name}-frontend"), "frontend")),
        &mut files,
    )?;

    // ---- copy the sibling project modules (§17) ----
    // Pure modules go into BOTH trees verbatim; server-tainted (backend-only)
    // modules go into the backend ONLY (never the wasm frontend).
    for m in &pure_sibling_mods {
        let rel = module_relpath(&db.module_name(*m).to_string());
        let text = db.module_parse(*m).syntax().text().to_string();
        write(&format!("backend/src/{rel}"), &text, &mut files)?;
        write(&format!("frontend/src/{rel}"), &text, &mut files)?;
    }
    for m in &backend_only_mods {
        let rel = module_relpath(&db.module_name(*m).to_string());
        let text = db.module_parse(*m).syntax().text().to_string();
        write(&format!("backend/src/{rel}"), &text, &mut files)?;
    }

    // ---- propagate shipped assets (Bundle.withAsset / withAssetDir) ----
    // The `bundle` binding already copies into frontend/src/Main.sky verbatim, so
    // `sky build --target` (run on frontend/) reads the SAME declarations; copy
    // the declared asset files/dirs alongside it so it can stage them into dist/.
    propagate_bundle_assets(&src, project_dir, &out_dir.join("frontend"))?;

    // ---- propagate external dependencies (Sky `.skydeps/`, Go `sky-ffi/`) ----
    // so `sky build --target` run on the generated frontend/backend can resolve
    // the same third-party imports the app declared.
    propagate_deps(project_dir, &out_dir.join("frontend"))?;
    propagate_deps(project_dir, &out_dir.join("backend"))?;

    Ok(SpaSplitReport {
        out_dir: out_dir.to_string_lossy().to_string(),
        files,
        server_branches: server.iter().map(|(n, _)| n.clone()).collect(),
        client_branches: client_names,
        excluded: tainted_names,
        notes,
    })
}

/// Every string-literal argument of a `Bundle.<func>` call in `src`, matched on
/// word boundaries (so `withAsset` does not match `withAssetDir`). Mirrors
/// `scan_bundle_calls_all` in the sky crate — kept local to avoid a cross-crate
/// dependency for a 15-line scan.
fn scan_bundle_asset_calls(src: &str, func: &str) -> Vec<String> {
    let bytes = src.as_bytes();
    let is_word = |b: u8| b.is_ascii_alphanumeric() || b == b'_';
    let mut out = Vec::new();
    let mut from = 0;
    while let Some(rel) = src[from..].find(func) {
        let at = from + rel;
        from = at + func.len();
        let before_ok = at == 0 || !is_word(bytes[at - 1]);
        let after_ok = bytes.get(at + func.len()).map(|b| !is_word(*b)).unwrap_or(true);
        if !before_ok || !after_ok {
            continue;
        }
        let rest = &src[at + func.len()..];
        if let Some(q) = rest.find('"') {
            let after_q = &rest[q + 1..];
            if let Some(end) = after_q.find('"') {
                out.push(after_q[..end].to_string());
            }
        }
    }
    out
}

/// Recursively copy `from` into `to` (skipping dot-files), preserving structure.
fn copy_tree(from: &Path, to: &Path) -> Result<(), String> {
    std::fs::create_dir_all(to).map_err(|e| format!("create {}: {e}", to.display()))?;
    let rd = std::fs::read_dir(from).map_err(|e| format!("read {}: {e}", from.display()))?;
    for entry in rd {
        let entry = entry.map_err(|e| format!("read entry: {e}"))?;
        let name = entry.file_name();
        if name.to_string_lossy().starts_with('.') {
            continue;
        }
        let src_path = entry.path();
        let dst_path = to.join(&name);
        if src_path.is_dir() {
            copy_tree(&src_path, &dst_path)?;
        } else {
            std::fs::copy(&src_path, &dst_path)
                .map_err(|e| format!("copy {}: {e}", src_path.display()))?;
        }
    }
    Ok(())
}

/// Copy the app's `Bundle.withAsset` / `withAssetDir` sources from the input
/// project into the generated frontend project (at the same relative path), so
/// `sky build --target` run on `frontend/` finds and stages them. A declared
/// path that does not exist is left for `stage_bundle_assets` to report at build
/// time (a clearer, target-specific error than one raised here).
fn propagate_bundle_assets(src: &str, project_dir: &Path, frontend_dir: &Path) -> Result<(), String> {
    for dir in scan_bundle_asset_calls(src, "withAssetDir") {
        let from = project_dir.join(&dir);
        if from.is_dir() {
            copy_tree(&from, &frontend_dir.join(&dir))?;
        }
    }
    for file in scan_bundle_asset_calls(src, "withAsset") {
        let from = project_dir.join(&file);
        if from.is_file() {
            let to = frontend_dir.join(&file);
            if let Some(parent) = to.parent() {
                std::fs::create_dir_all(parent)
                    .map_err(|e| format!("create {}: {e}", parent.display()))?;
            }
            std::fs::copy(&from, &to).map_err(|e| format!("copy asset {file}: {e}"))?;
        }
    }
    Ok(())
}

/// Reconstruct the `[dependencies]` (Sky packages) and `["go.dependencies"]` (Go
/// FFI) sections from the project's `sky.toml`, to append to a generated
/// manifest. Empty string when the project declares no external deps.
fn emit_dep_sections(project_dir: &Path) -> String {
    let sky_toml = project_dir.join("sky.toml");
    let mut out = String::new();
    let sky_deps = crate::ffi_ops::read_sky_dependencies(&sky_toml);
    if !sky_deps.is_empty() {
        out.push_str("\n[dependencies]\n");
        for (k, v) in sky_deps {
            out.push_str(&format!("\"{k}\" = \"{v}\"\n"));
        }
    }
    let go_deps = crate::ffi_ops::read_go_dependencies(&sky_toml);
    if !go_deps.is_empty() {
        out.push_str("\n[\"go.dependencies\"]\n");
        for (k, v) in go_deps {
            out.push_str(&format!("\"{k}\" = \"{v}\"\n"));
        }
    }
    out
}

/// Copy the project's fetched external-dependency trees (`.skydeps/` Sky sources,
/// `sky-ffi/` Go FFI surface) and its own `native/` extension tree (native
/// Swift/Kotlin/Java + entitlement/manifest fragments) into a generated project,
/// so `sky build --target` run there can rebuild the same imports AND link the
/// same native code. No-op for trees that don't exist (a dep-free app).
fn propagate_deps(project_dir: &Path, gen_dir: &Path) -> Result<(), String> {
    for tree in [".skydeps", "sky-ffi", "native"] {
        let from = project_dir.join(tree);
        if from.is_dir() {
            copy_tree(&from, &gen_dir.join(tree))?;
        }
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// Codec registry + copy-closure helpers (§14 #2).
// ---------------------------------------------------------------------------

/// Scan `mods` (the entry module + every PURE sibling project module) for the
/// project's own zero-arg `Codec <T>` bindings. Each binding records the module
/// it lives in, so the generator can decide whether to COPY it into `Shared`
/// (entry-module codecs) or reference it via an `import` (sibling-module codecs).
fn build_codec_registry(db: &SkyDatabase, mods: &[ModuleId]) -> Vec<CodecBinding> {
    let mut out: Vec<CodecBinding> = Vec::new();
    for &mid in mods {
        let resolved = db.resolve(mid);
        let parse = db.module_parse(mid);
        let file = parse.tree();
        let src = parse.syntax().text().to_string();
        for td in &resolved.top_defs {
            let Some(body) = resolved.bodies.get(&td.def) else {
                continue;
            };
            // Only a zero-arg value binding IS a `Codec <T>` value (a function
            // `mkCodec : X -> Codec Y` is not directly referenceable as a codec).
            if !body.params.is_empty() {
                continue;
            }
            let result = ty::Typer::new(db).body_types(mid, td.def, body).result;
            if let Some(ty::Ty::App(name, args)) = &result {
                if tail_seg(name.as_str()) == "Codec" && args.len() == 1 {
                    let bname = td.name.as_str().to_string();
                    // Prefer the user's declared surface (`Codec (List Todo)` →
                    // `List Todo`); fall back to the solved (alias-expanded) type.
                    let surface = decl_text_by(&file, &src, &bname, DeclKind::TypeAnno)
                        .and_then(|a| codec_arg_surface(&a))
                        .unwrap_or_else(|| render_ty(&args[0]));
                    out.push(CodecBinding {
                        name: bname,
                        def: td.def,
                        module: mid,
                        coded_ty: args[0].clone(),
                        surface,
                    });
                }
            }
        }
    }
    out
}

/// Extract the `T` surface from a `<name> : Codec <T>` annotation, dropping one
/// layer of enclosing parentheses (`todoListCodec : Codec (List Todo)` →
/// `List Todo`). Returns `None` if the annotation is not a `Codec <T>` shape.
fn codec_arg_surface(anno: &str) -> Option<String> {
    let rhs = anno.split_once(':')?.1.trim();
    let after = rhs.strip_prefix("Codec")?.trim();
    let after = after.trim_start_matches('.').trim(); // tolerate `Codec.Codec`-ish
    let s = after.trim();
    // Strip a single fully-enclosing pair of parens.
    let s = if s.starts_with('(') && s.ends_with(')') {
        let inner = &s[1..s.len() - 1];
        // only strip if the parens are balanced as one group
        if paren_balanced_single_group(inner) {
            inner.trim().to_string()
        } else {
            s.to_string()
        }
    } else {
        s.to_string()
    };
    if s.is_empty() {
        None
    } else {
        Some(s)
    }
}

/// True if `inner` never drops to depth 0 before its end — i.e. the stripped
/// outer parens were a single enclosing group, not `(A) (B)`.
fn paren_balanced_single_group(inner: &str) -> bool {
    let mut depth = 0i32;
    for c in inner.chars() {
        match c {
            '(' => depth += 1,
            ')' => {
                depth -= 1;
                if depth < 0 {
                    return false;
                }
            }
            _ => {}
        }
    }
    depth == 0
}

/// The transitive, project-local, non-tainted value-def closure of the codec
/// bindings the wire references — the value declarations to copy into Shared.
fn compute_value_copy(
    db: &SkyDatabase,
    entry: ModuleId,
    registry: &[CodecBinding],
    needed: &BTreeSet<String>,
    tainted: &HashSet<String>,
) -> BTreeSet<String> {
    let mut result: BTreeSet<String> = BTreeSet::new();
    let mut work: Vec<DefId> = registry
        .iter()
        .filter(|b| needed.contains(&b.name))
        .map(|b| b.def)
        .collect();
    let mut seen: HashSet<DefId> = HashSet::new();
    while let Some(d) = work.pop() {
        if !seen.insert(d) {
            continue;
        }
        let Some(loc) = db.def_loc(d) else {
            continue;
        };
        // Only the entry module is copied (multi-module is refused up front).
        if loc.module != entry {
            continue;
        }
        let name = loc.name.as_str().to_string();
        // Never drag a server-tainted (effectful) def into Shared.
        if tainted.contains(&name) {
            continue;
        }
        result.insert(name);
        for c in spa_partition::body_def_callees(db, entry, d) {
            if !seen.contains(&c) {
                work.push(c);
            }
        }
    }
    result
}

/// The project type declarations (aliases + unions) by name.
fn project_type_decls(file: &SourceFile) -> HashMap<String, syntax::ast::Decl> {
    let mut out: HashMap<String, syntax::ast::Decl> = HashMap::new();
    for d in file.decls() {
        if matches!(decl_kind(&d), DeclKind::Alias | DeclKind::Union) {
            if let Some(n) = decl_name(&d) {
                out.insert(n, d);
            }
        }
    }
    out
}

/// Every `UpperIdent` token under a node (type names a decl mentions).
fn upper_idents(node: &syntax::SyntaxNode) -> Vec<String> {
    node.descendants_with_tokens()
        .filter_map(|e| e.into_token())
        .filter(|t| t.kind() == SyntaxKind::UpperIdent)
        .map(|t| t.text().to_string())
        .collect()
}

/// The transitive set of project type declarations the wire drags in: seeded
/// from the wire field types + the copied codec bodies, closed over each copied
/// type declaration's own referenced type names.
fn compute_type_copy(
    file: &SourceFile,
    project_types: &HashMap<String, syntax::ast::Decl>,
    seed: &BTreeSet<String>,
    copied_values: &BTreeSet<String>,
) -> BTreeSet<String> {
    let mut result: BTreeSet<String> = BTreeSet::new();
    let mut work: Vec<String> = Vec::new();
    for n in seed {
        if project_types.contains_key(n) {
            work.push(n.clone());
        }
    }
    // A copied codec body (`Codec.object Todo …`) names its record type.
    for d in file.decls() {
        if is_value_decl(&d) {
            if let Some(n) = decl_name(&d) {
                if copied_values.contains(&n) {
                    for u in upper_idents(d.syntax()) {
                        if project_types.contains_key(&u) {
                            work.push(u);
                        }
                    }
                }
            }
        }
    }
    while let Some(n) = work.pop() {
        if !result.insert(n.clone()) {
            continue;
        }
        if let Some(decl) = project_types.get(&n) {
            for u in upper_idents(decl.syntax()) {
                if project_types.contains_key(&u) && !result.contains(&u) {
                    work.push(u);
                }
            }
        }
    }
    result
}

/// The verbatim source of the copied declarations, in source order (each copied
/// name's type-annotation AND value declaration are emitted, as both match by
/// name).
fn render_copied_decls(file: &SourceFile, src: &str, copied: &HashSet<String>) -> String {
    let mut out = String::new();
    for d in file.decls() {
        if let Some(n) = decl_name(&d) {
            if copied.contains(&n) {
                out.push_str(slice(src, d.syntax()).trim_end());
                out.push_str("\n\n\n");
            }
        }
    }
    out
}

/// The `exposing` entries for the copied declarations (a union exports `(..)`).
fn copied_exposing_list(
    project_types: &HashMap<String, syntax::ast::Decl>,
    copied_types: &BTreeSet<String>,
    copied_values: &BTreeSet<String>,
) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for t in copied_types {
        let is_union = project_types
            .get(t)
            .map(|d| decl_kind(d) == DeclKind::Union)
            .unwrap_or(false);
        out.push(if is_union { format!("{t}(..)") } else { t.clone() });
    }
    for v in copied_values {
        out.push(v.clone());
    }
    out
}

/// Shared's imports: the input's imports minus the server-only effect families,
/// the `Std.Spa` framework (Shared is pure wire types + codecs), and any
/// **backend-only** project module (whose effects must never reach the wasm
/// client), with Prelude + Codec guaranteed present. `needed_siblings` are the
/// PURE sibling project modules whose types/codecs the wire references — Shared
/// imports each with a canonical `exposing (..)` (dropping the entry's own,
/// possibly-aliased, form to avoid a duplicate import).
fn shared_import_lines(
    imports: &[ImportInfo],
    needed_siblings: &BTreeSet<String>,
    backend_only: &HashSet<String>,
) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for i in imports {
        if is_server_only_module(&i.module_path) {
            continue;
        }
        if i.module_path.rsplit('.').next() == Some("Spa") {
            continue;
        }
        if backend_only.contains(&i.module_path) {
            continue;
        }
        if needed_siblings.contains(&i.module_path) {
            continue;
        }
        out.push(i.text.clone());
    }
    for m in needed_siblings {
        out.push(format!("import {m} exposing (..)"));
    }
    if !imports.iter().any(|i| i.module_path == "Sky.Core.Prelude") {
        out.insert(0, "import Sky.Core.Prelude exposing (..)".to_string());
    }
    if !imports.iter().any(|i| i.module_path == "Std.Codec") {
        out.push("import Std.Codec as Codec exposing (Codec)".to_string());
    }
    out
}

// ---------------------------------------------------------------------------
// Backend generation — copy the app verbatim, swap `main`, append handlers.
// ---------------------------------------------------------------------------

/// The curated GET-safe kernel allowlist (design §4.2). These are the
/// idempotent READ effects that are safe to run server-side on an SSR GET, so
/// the first paint carries real data. There is NO type-level idempotency
/// guarantee in Sky's uniform `Task Error a` boundary — this is a hand-curated
/// list, matched against the app's `init` source. Anything NOT on this list
/// (a write, a non-deterministic effect, an unrecognised shape) is fail-closed:
/// the route renders the pure `init` model (chrome-only) and the client resolves
/// the data post-hydrate, exactly as P1 did. Keep this list conservative — a GET
/// that mutates or is non-deterministic is a correctness + security bug.
const SSR_GET_SAFE_KERNELS: &[&str] = &[
    "File.readFile",
    "File.readFileLimit",
    "File.readFileBytes",
    "File.readDir",
    "Http.get",
    "Db.query",
    "Db.queryDecode",
    "Db.findOneByField",
];

/// Kernels that MUST NOT run on an SSR GET — writes + non-deterministic effects.
/// Their presence anywhere in `init`'s source forces fail-closed (chrome-only),
/// even if a GET-safe read is also present, because running `init`'s command
/// would fire them. This is the explicit "GET must never mutate" denylist.
const SSR_GET_UNSAFE_KERNELS: &[&str] = &[
    "File.writeFile",
    "File.writeFileBytes",
    "File.appendFile",
    "File.deleteFile",
    "Http.post",
    "Http.put",
    "Http.delete",
    "Http.patch",
    "Db.exec",
    "Db.execMany",
    "Db.insert",
    "Db.update",
    "Db.delete",
    "Time.now",
    "Uuid.",
    "Random.",
    "Crypto.random",
    "postJson",
];

/// Decide, FAIL-CLOSED, whether the app's `init` command is a curated GET-safe
/// read that the SSR handler may settle server-side (design §4.2). Sound because
/// it errs toward chrome-only, checked POSITIVELY per `Cmd.perform`:
///
///   * every `Cmd.perform` in `init` must apply a task whose HEAD is an
///     allowlisted read kernel (`SSR_GET_SAFE_KERNELS`) — a `Cmd.perform` of a
///     write, a non-deterministic effect, or an opaque task/helper value
///     (`Cmd.perform someTask …`, whose head we cannot prove safe) disqualifies
///     the whole `init`;
///   * there must be at least one such perform (a `Cmd.none` init has nothing to
///     settle → chrome-only, which is correct — there is no data to resolve);
///   * belt-and-suspenders, any unsafe kernel token anywhere in `init` also
///     disqualifies it.
///
/// Requiring the safe kernel at the PERFORM HEAD (not merely somewhere in the
/// text) closes the gap where `init` reads via one perform but writes via a
/// helper whose name is not a denylist token. The transitive, per-effect
/// positive allowlist that follows kernel identity through the compiler (rather
/// than this `init`-source scan) is the documented follow-on; this scan is
/// deliberately conservative and only ever under-approximates "safe".
fn init_cmd_is_get_safe(init_src: &str) -> bool {
    if SSR_GET_UNSAFE_KERNELS.iter().any(|k| init_src.contains(k)) {
        return false;
    }
    let mut saw_perform = false;
    let mut rest = init_src;
    while let Some(i) = rest.find("Cmd.perform") {
        saw_perform = true;
        // The task argument follows `Cmd.perform`, optionally wrapped in `(`.
        let after = rest[i + "Cmd.perform".len()..].trim_start();
        let head = after.trim_start_matches('(').trim_start();
        // The head must be an allowlisted kernel at a WORD BOUNDARY, so a prefix
        // like `File.readFile` cannot accept a longer, unlisted `File.readFileX`.
        let head_ok = SSR_GET_SAFE_KERNELS.iter().any(|k| {
            head.starts_with(k)
                && !head[k.len()..]
                    .chars()
                    .next()
                    .is_some_and(|c| c.is_alphanumeric() || c == '_')
        });
        if !head_ok {
            // This perform's head is not a proven GET-safe read → fail-closed.
            return false;
        }
        rest = &rest[i + "Cmd.perform".len()..];
    }
    saw_perform
}

/// The joined source of every decl named `init` (the type annotation and the
/// value binding are separate decls), so a scan sees the body. Shared by the
/// backend settle decision ([`init_cmd_is_get_safe`]) and the frontend
/// init-command strip below.
fn app_init_src(file: &SourceFile, src: &str) -> String {
    file.decls()
        .filter(|d| decl_name(d).as_deref() == Some("init"))
        .map(|d| slice(src, d.syntax()).to_string())
        .collect::<Vec<_>>()
        .join("\n")
}

/// Whole-word membership: `needle` occurs in `hay` bounded by non-identifier
/// bytes on both sides (so `db` does NOT match inside `dbPool` / `mydb`). Used
/// to decide whether the client `init` references a server-tainted top-level
/// binding that the frontend drops.
fn references_word(hay: &str, needle: &str) -> bool {
    if needle.is_empty() {
        return false;
    }
    let bytes = hay.as_bytes();
    let mut from = 0;
    while let Some(rel) = hay[from..].find(needle) {
        let i = from + rel;
        let before_ok = i == 0 || !is_ident_byte(bytes[i - 1]);
        let after = i + needle.len();
        let after_ok = after >= bytes.len() || !is_ident_byte(bytes[after]);
        if before_ok && after_ok {
            return true;
        }
        from = i + needle.len();
    }
    false
}

fn is_ident_byte(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b == b'_'
}

/// Rewrite the client `init` VALUE decl so its returned command is `Cmd.none`,
/// leaving the pure model expression unchanged — `init _ = ( <model>, <cmd> )`
/// becomes `init _ = ( <model>, Cmd.none )`.
///
/// The crux of the SSR client-leg (design §4.4/§4.5, blocker #3). `spa_partition`
/// routes a `Task.run` CAF (a `db` handle reaching `Db.open`/`Db.connect`) to the
/// BACKEND ONLY — it never reaches the wasm frontend. But the client keeps `init`
/// verbatim, so a DB-backed `init`'s `Cmd.perform (Db.query db …)` references the
/// dropped `db` and the frontend fails to compile (`Undefined name: db`). Under
/// SSR-on-by-default the backend SETTLES that GET-safe read and embeds the
/// resolved model in `#sky-model`; the client boots from that blob and NEVER runs
/// `init`'s command (the runtime drops `cmd0`). Stripping the command to
/// `Cmd.none` therefore both COMPILES the client tree without `db` and matches the
/// runtime behaviour — the server owns the read, the client must not.
///
/// Returns `None` when `init`'s body is not the expected `( model, cmd )` shape
/// (possibly through a `let … in`); the caller then keeps it verbatim so a genuine
/// mismatch surfaces as a normal compile error rather than a silent wrong strip.
fn frontend_init_value_without_cmd(src: &str, init_val: &syntax::ast::Decl) -> Option<String> {
    let (_model, cmd) = init_return_tuple(init_val)?;
    let cmd_node = cmd.syntax();
    let decl_node = init_val.syntax();
    let decl_start = u32::from(decl_node.text_range().start()) as usize;
    let a = u32::from(cmd_node.text_range().start()) as usize - decl_start;
    let b = u32::from(cmd_node.text_range().end()) as usize - decl_start;
    let mut out = slice(src, decl_node).to_string();
    out.replace_range(a..b, "Cmd.none");
    Some(out)
}

/// Wire `|> Spa.withModelDecoder spaModelDecoder_` onto the config builder chain
/// in `main` (the synthesised `main = Spa.app (Spa.config {…} |> Spa.with… )`).
/// Inserts the builder as its own line, indented to match the chain, immediately
/// before the line that closes the `Spa.app` argument. Idempotent; a `main` with
/// no closing paren is returned unchanged (the decoder binding then stays unused,
/// never a compile break).
fn inject_model_decoder_into_main(main_text: &str) -> String {
    if main_text.contains("Spa.withModelDecoder") {
        return main_text.to_string();
    }
    // The builder chain lives in the VALUE decl (`main = Spa.app (Spa.config …)`),
    // never the type annotation (`main : Task Error ()`, whose `()` would
    // otherwise catch `rfind(')')`). Only inject into the decl that builds the app.
    if !main_text.contains("Spa.app") && !main_text.contains("Spa.config") {
        return main_text.to_string();
    }
    let close = match main_text.rfind(')') {
        Some(i) => i,
        None => return main_text.to_string(),
    };
    // Start of the line that holds the closing paren.
    let line_start = main_text[..close].rfind('\n').map(|n| n + 1).unwrap_or(0);
    let mut out = main_text.to_string();
    out.insert_str(line_start, "            |> Spa.withModelDecoder spaModelDecoder_\n");
    out
}

/// The pure MODEL expression of `init` (the first element of its returned
/// `( model, cmd )` tuple), as source text. Used to derive the SSR model DECODER
/// blank client-side: `Codec.fromJson (Codec.auto <model>) json` (design §4.5).
/// The model expression is pure (no `db` reference in the standard shape), so the
/// derived decoder is NOT server-tainted and survives into the frontend tree.
fn init_pure_model_expr(src: &str, init_val: &syntax::ast::Decl) -> Option<String> {
    let (model, _cmd) = init_return_tuple(init_val)?;
    Some(slice(src, model.syntax()).to_string())
}

/// Drill `init`'s value body (through any `let … in`) to its returning
/// `( model, cmd )` tuple and return the two element exprs. `None` when the body
/// is not a 2-tuple.
fn init_return_tuple(init_val: &syntax::ast::Decl) -> Option<(syntax::ast::Expr, syntax::ast::Expr)> {
    let vd = match init_val {
        syntax::ast::Decl::Value(v) => v,
        _ => return None,
    };
    let mut ret = vd.body()?;
    while let syntax::ast::Expr::Let(l) = ret {
        ret = l.body()?;
    }
    let tuple = match ret {
        syntax::ast::Expr::Tuple(t) => t,
        _ => return None,
    };
    let mut elems = tuple
        .syntax()
        .children()
        .filter_map(syntax::ast::Expr::cast);
    let model = elems.next()?;
    let cmd = elems.next()?;
    if elems.next().is_some() {
        return None; // not a 2-tuple
    }
    Some((model, cmd))
}

/// Extract the literal route PATTERN strings from a synthesised `spaRoutes_`
/// binding source (e.g. `List.concatMap App.spaRoute ([ App.route "/" Home,
/// App.route "/items" Items ])`) so the backend can register one SSR GET handler
/// per pattern. Per-pattern registration (not one wildcard) is what lets asset
/// GETs fall through to `Server.static`: each literal pattern is a more-specific
/// mux entry that beats the static catch-all, while `main.wasm` / `wasm_exec.js`
/// match none of them (design §4.1). Only LITERAL patterns are extracted — a
/// pattern built from a variable is not statically visible and simply is not
/// pre-registered (its route resolves client-side); the root `/` is always
/// covered by the `GET /{$}` fallback the caller keeps. Returns patterns in
/// source order, de-duplicated, with the bare root `/` dropped (the caller emits
/// it as the exact-root `GET /{$}`).
fn spa_ssr_route_patterns(routes_src: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    // Walk each `App.route`/`App.routeInt`/`App.routeParam`/`Spa.route` head and
    // take its first string-literal argument as the pattern.
    for head in ["App.route", "App.routeInt", "App.routeParam", "Spa.route"] {
        let mut rest = routes_src;
        while let Some(i) = rest.find(head) {
            let after = &rest[i + head.len()..];
            // Find the first quote after the head (the pattern literal).
            if let Some(q) = after.find('"') {
                let tail = &after[q + 1..];
                if let Some(end) = tail.find('"') {
                    let pat = &tail[..end];
                    if !pat.is_empty() && pat != "/" && !out.contains(&pat.to_string()) {
                        out.push(pat.to_string());
                    }
                    rest = &tail[end + 1..];
                    continue;
                }
            }
            rest = &after[..];
        }
    }
    out
}

fn gen_backend(
    file: &SourceFile,
    src: &str,
    imports: &[ImportInfo],
    server: &[(String, BranchIo)],
    model_fields: &[ModelFieldTy],
    copied_names: &HashSet<String>,
    push_mode: bool,
    broker_url: Option<&str>,
) -> Result<String, String> {
    // Imports: keep every input import EXCEPT Std.Spa (framework, main-only),
    // then add the server-side machinery.
    let mut import_lines: Vec<String> = imports
        .iter()
        .filter(|i| i.module_path.rsplit('.').next() != Some("Spa"))
        .map(|i| i.text.clone())
        .collect();
    let add = |imports: &[ImportInfo], lines: &mut Vec<String>, path: &str, text: &str| {
        if !has_module(imports, path) {
            lines.push(text.to_string());
        }
    };
    add(imports, &mut import_lines, "Sky.Http.Server", "import Sky.Http.Server as Server exposing (Request, Response, Handler)");
    add(imports, &mut import_lines, "Std.Codec", "import Std.Codec as Codec");
    add(imports, &mut import_lines, "Sky.Core.System", "import Sky.Core.System as System");
    add(imports, &mut import_lines, "Sky.Core.Error", "import Sky.Core.Error as Error exposing (Error)");
    // SSR (design §4.1): a backend that carries `view`/`init` (≥1 server branch,
    // or push) gets an SSR `GET /{$}` route that renders the first paint. It
    // needs `Sky.Ffi` (the `Spa_ssr*` render-kernel aliases) and `Sky.Core.Task`
    // (the handler answers `Task Error Response`). Two gates narrow it:
    //   - a static-only backend (§2.5 — no app decls copied) has no `view` to
    //     render and is skipped (P2); and
    //   - the SSR route references the `spaView_` + `spaHead_` bindings that the
    //     App→Spa synthesis emits (main.rs::synthesize_spa_source), so SSR is
    //     scoped to the AUTO-SYNTHESISED `Std.App` path — a HAND-authored
    //     `Spa.app` backend has `view`/`init` under their own names + an inline
    //     `Spa.withHead`, no `spaView_`/`spaHead_`, so it keeps today's static
    //     shell (design §10 q2: require Std.App for automatic SSR).
    let has_synth_view = file
        .decls()
        .any(|d| decl_name(&d).as_deref() == Some("spaView_"));
    let has_synth_head = file
        .decls()
        .any(|d| decl_name(&d).as_deref() == Some("spaHead_"));
    // Per-route SSR (design §4.1): the App→Spa synthesis emits named
    // `spaRoutes_` / `spaNotFound_` bindings (main.rs synthesize_spa_source) so
    // the SSR handler can resolve the REQUEST path to the route's page
    // server-side (Spa_ssrResolveModel), rendering `/`, `/items`, … each to its
    // own content instead of only the root P1 rendered. A route-less app has
    // neither binding and keeps the root-only render.
    let has_synth_routes = file
        .decls()
        .any(|d| decl_name(&d).as_deref() == Some("spaRoutes_"));
    let has_synth_not_found = file
        .decls()
        .any(|d| decl_name(&d).as_deref() == Some("spaNotFound_"));
    // Data-resolved SSR (design §4.2): the GET-safe allowlist, applied
    // FAIL-CLOSED at synthesis over the app's `init` source. The settle
    // (Spa_ssrSettle) runs `init`'s `cmd0` to a data-bearing model server-side —
    // so the first paint carries REAL per-route content (the item list, the blog
    // post body) a crawler sees — but ONLY when `init`'s command is provably a
    // curated GET-safe read. See init_cmd_is_get_safe: this is where the
    // "a GET must never mutate / run a non-deterministic effect" boundary is
    // enforced. An `init` whose command is a write, is non-deterministic, or is
    // any shape this scan cannot recognise gets NO settle and renders the pure
    // `init` model (chrome-only, exactly P1) — the fail-closed default.
    // Join EVERY decl named `init` (the type annotation and the value binding are
    // separate decls) so the scan sees the body — matching only the annotation
    // `init : () -> ( Model, Cmd Msg )` would miss the `File.readFile` in the body.
    let init_src = app_init_src(file, src);
    let init_get_safe = init_cmd_is_get_safe(&init_src);
    let emit_ssr = !(server.is_empty() && !push_mode) && has_synth_view && has_synth_head;
    if emit_ssr {
        add(imports, &mut import_lines, "Sky.Ffi", "import Sky.Ffi as Ffi");
        add(imports, &mut import_lines, "Sky.Core.Task", "import Sky.Core.Task as Task");
    }
    if push_mode {
        // Server→client PUSH machinery (docs/skyspa/auto-split.md §16).
        add(imports, &mut import_lines, "Sky.Core.Task", "import Sky.Core.Task as Task");
        add(imports, &mut import_lines, "Sky.Ffi", "import Sky.Ffi as Ffi");
        add(imports, &mut import_lines, "Sky.Core.Maybe", "import Sky.Core.Maybe as Maybe");
        add(imports, &mut import_lines, "Sky.Http.Server.Stream", "import Sky.Http.Server.Stream as Stream exposing (StreamWriter)");
    }
    import_lines.push("import Shared exposing (..)".to_string());

    // All decls except `main` (both its annotation and value), verbatim —
    // MINUS the types/codecs copied into Shared (they arrive via `import Shared
    // exposing (..)`; re-declaring them here would be a duplicate definition).
    //
    // EXCEPTION: a STATIC-ONLY backend (no server branches, no push) runs no app
    // logic at all — it only serves the wasm client's assets. Its `init`/`update`/
    // `view`/`subscriptions` are dead here, and for a hand-authored Sky.Spa client
    // (issue #195) they reference the client-only `Std.Spa` framework
    // (`Spa.getJson`/`postJson`, and the `Spa_app`/`Spa_config` kernels are
    // `//go:build js` wasm-only), whose import this backend drops (above). Copying
    // them would leave `Spa.*` undefined server-side. So a static-only backend
    // copies NONE of the app's `*.sky` decls — just Shared + serverPort + main.
    // (When there ARE server branches, `update` is reused by the RPC handlers and
    // — in a well-formed auto-split input — contains no `Std.Spa` references, so
    // it is copied verbatim as before.)
    let static_only_backend = server.is_empty() && !push_mode;
    let mut body = String::new();
    if !static_only_backend {
        for d in file.decls() {
            if decl_name(&d).as_deref() == Some("main") {
                continue;
            }
            if let Some(n) = decl_name(&d) {
                if copied_names.contains(&n) {
                    continue;
                }
            }
            body.push_str(slice(src, d.syntax()).trim_end());
            body.push_str("\n\n\n");
        }
    }

    // The generated handlers + serverPort + main.
    let mut handlers = String::new();
    let mut routes: Vec<String> = Vec::new();
    handlers.push_str("badRequest : String -> Response\nbadRequest msg =\n    Server.withStatus 400 (Server.text msg)\n\n\n");

    // Server→client PUSH: one process-shared broker, a Cmd-publish interpreter,
    // and the SSE stream handler body — all thin kernel aliases (spa_push.go).
    if push_mode {
        // The broker URL baked by `sky spa-split --broker <url>` (empty string
        // when absent → env/in-process). SKY_LIVE_BROKER_URL still overrides it
        // at runtime (effectiveBrokerUrl, live_redis_broker.go).
        let baked_url = sky_string_literal(broker_url.unwrap_or(""));
        handlers.push_str(&format!(
            "-- Server->client PUSH (SSE) — the auto-split's Sub.subscribeTopic /\n\
             -- Cmd.publish channel (docs/skyspa/auto-split.md §16). One process-shared\n\
             -- broker (a memoised CAF); each RPC handler fans its returned Cmd's\n\
             -- publishes through it; `GET /_sky/sub?topic=…` streams them as SSE. The\n\
             -- broker URL below is baked by `--broker`; SKY_LIVE_BROKER_URL overrides it.\n\
             spaNewBroker : String -> any\n\
             spaNewBroker =\n\
             \x20   Ffi.kernel \"Spa_newBroker\"\n\n\n\
             spaBroker : any\n\
             spaBroker =\n\
             \x20   spaNewBroker {baked_url}\n\n\n\
             spaInterpretPublish : any -> Cmd Msg -> Task Error ()\n\
             spaInterpretPublish =\n\
             \x20   Ffi.kernel \"Spa_interpretPublish\"\n\n\n\
             spaStreamTopic : any -> String -> (StreamWriter -> Task Error ())\n\
             spaStreamTopic =\n\
             \x20   Ffi.kernel \"Spa_streamTopic\"\n\n\n\
             subHandler : Request -> Task Error Response\n\
             subHandler req =\n\
             \x20   Stream.stream \"text/event-stream\"\n\
             \x20       (spaStreamTopic spaBroker (Maybe.withDefault \"\" (Server.queryParam \"topic\" req)))\n\n\n"
        ));
    }
    for (name, io) in server {
        let handler = format!("{}Handler", lower_first(name));
        let req_codec = format!("{}ReqCodec", lower_first(name));
        let resp_codec = format!("{}RespCodec", lower_first(name));
        // Response field NAMES = the write-set (or the whole model). Codecs live
        // in Shared; the backend only needs the names to read `m2.<field>`.
        let resp_field_names: Vec<String> = if io.writes_whole_model {
            model_fields.iter().map(|f| f.name.clone()).collect()
        } else {
            io.write_fields.clone()
        };

        // The model the branch runs against.
        let run_setup = if io.reads_whole_model {
            // Req IS the whole model.
            "                m =\n                    p\n".to_string()
        } else if io.read_fields.is_empty() {
            "                ( base, _ ) =\n                    init ()\n\n                m =\n                    base\n".to_string()
        } else {
            let sets = io
                .read_fields
                .iter()
                .enumerate()
                .map(|(i, f)| {
                    let sep = if i == 0 { "" } else { ", " };
                    format!("{sep}{f} = p.{f}")
                })
                .collect::<String>();
            format!(
                "                ( base, _ ) =\n                    init ()\n\n                m =\n                    {{ base | {sets} }}\n"
            )
        };
        // The Msg constructor to run (args come from the wire payload).
        let ctor_app = if io.msg_args.is_empty() {
            name.clone()
        } else {
            let args = io
                .msg_args
                .iter()
                .map(|a| format!(" p.{a}"))
                .collect::<String>();
            format!("({name}{args})")
        };
        // The response value.
        let resp_val = if io.writes_whole_model {
            "m2".to_string()
        } else if resp_field_names.is_empty() {
            "{}".to_string()
        } else {
            let sets = resp_field_names
                .iter()
                .enumerate()
                .map(|(i, f)| {
                    let sep = if i == 0 { "" } else { ", " };
                    format!("{sep}{f} = m2.{f}")
                })
                .collect::<String>();
            format!("{{ {sets} }}")
        };
        // In push mode the returned Cmd is fed to the broker (a Cmd.publish fans
        // out to SSE subscribers) BEFORE the RPC answers; otherwise it is
        // discarded (`_`) exactly as before.
        let (cmd_binder, answer) = if push_mode {
            (
                "cmd",
                format!(
                    "spaInterpretPublish spaBroker cmd\n\
                     \x20               |> Task.andThen (\\_ -> Task.succeed (Server.json (Codec.toJson {resp_codec} {resp_val})))"
                ),
            )
        } else {
            (
                "_",
                format!("Task.succeed (Server.json (Codec.toJson {resp_codec} {resp_val}))"),
            )
        };
        handlers.push_str(&format!(
            "-- Generated endpoint for the SERVER branch `{name}`: decode the read-set,\n\
             -- reuse the app's own init + update to run the REAL effect, encode the write-set.\n\
             {handler} : Handler\n\
             {handler} req =\n\
             \x20   case Codec.fromJson {req_codec} req.body of\n\
             \x20       Ok p ->\n\
             \x20           let\n\
             {run_setup}\n\
             \x20               ( m2, {cmd_binder} ) =\n\
             \x20                   update {ctor_app} m\n\
             \x20           in\n\
             \x20           {answer}\n\n\
             \x20       Err e ->\n\
             \x20           Task.succeed (badRequest (Error.toString e))\n\n\n"
        ));
        routes.push(format!("        , Server.api \"POST /_rpc/{name}\" {handler}"));
    }
    if push_mode {
        // The SSE push endpoint (topic from the query string).
        routes.push("        , Server.api \"GET /_sky/sub\" subHandler".to_string());
    }

    // SSR first paint (design §4.1/§4.4). Emitted only for a backend that carries
    // `view`/`init` (≥1 server branch, or push) — a static-only backend has no
    // app decls and is skipped (P2). The route renders the ROOT `/` server-side
    // so a crawler sees real content + a per-route `<head>` instead of the empty
    // `#app` static shell; asset GETs fall through to `Server.static`.
    if emit_ssr {
        handlers.push_str(
            "-- SSR render kernels (design §4.1) — thin `Ffi.kernel` aliases over the\n\
             -- backend render half (runtime-go/rt/spa_ssr_notjs.go). Referencing them\n\
             -- here keeps renderAppHead / HtmlRenderWithHandlers LIVE in the backend\n\
             -- binary (link-time DCE drops them until an SSR route calls them).\n\
             spaSsrRenderHead : any -> model -> String\n\
             spaSsrRenderHead =\n\
             \x20   Ffi.kernel \"Spa_ssrRenderHead\"\n\n\n\
             spaSsrRenderBody : any -> String\n\
             spaSsrRenderBody =\n\
             \x20   Ffi.kernel \"Spa_ssrRenderBody\"\n\n\n\
             spaSsrPage : String -> String -> String -> String -> String\n\
             spaSsrPage =\n\
             \x20   Ffi.kernel \"Spa_ssrPage\"\n\n\n\
             spaSsrWasmName : String -> String\n\
             spaSsrWasmName =\n\
             \x20   Ffi.kernel \"Spa_ssrWasmName\"\n\n\n\
             -- The content-hashed wasm filename, resolved ONCE (a memoised CAF) from\n\
             -- the same frontend dist `Server.static` serves.\n\
             spaWasmName : String\n\
             spaWasmName =\n\
             \x20   spaSsrWasmName \"../frontend/dist\"\n\n\n",
        );
        // Per-route resolver alias — resolves the request path to the route's
        // page + model server-side (design §4.1). Emitted only when the app has
        // routes; a route-less app renders the root.
        if has_synth_routes {
            handlers.push_str(
                "-- Per-route SSR: resolve the request path to the route's page + model\n\
                 -- exactly as the client does at boot (Spa_ssrResolveModel).\n\
                 spaSsrResolveModel : any -> any -> model -> String -> model\n\
                 spaSsrResolveModel =\n\
                 \x20   Ffi.kernel \"Spa_ssrResolveModel\"\n\n\n",
            );
        }
        // Data-resolved settle alias — runs init's GET-safe read to a settled,
        // data-bearing model (design §4.2). Emitted ONLY when the fail-closed
        // allowlist scan proved init's command GET-safe (init_get_safe).
        if init_get_safe {
            handlers.push_str(
                "-- Data-resolved SSR: settle init's GET-safe read to a data-bearing\n\
                 -- model server-side so the first paint carries REAL content a crawler\n\
                 -- sees (Spa_ssrSettle). Emitted only because the allowlist scan proved\n\
                 -- init's command is a curated GET-safe read (spa_split.rs).\n\
                 spaSsrSettle : model -> any -> any -> model\n\
                 spaSsrSettle =\n\
                 \x20   Ffi.kernel \"Spa_ssrSettle\"\n\n\n",
            );
        }
        // The handler body: resolve the route, optionally settle its data, render.
        let req_param = if has_synth_routes { "req" } else { "_" };
        let cmd_bind = if init_get_safe { "cmd0" } else { "_" };
        let routed_expr = if has_synth_routes {
            "spaSsrResolveModel spaRoutes_ spaNotFound_ model0 req.path"
        } else {
            "model0"
        };
        let resolved_expr = if init_get_safe {
            "spaSsrSettle routed cmd0 update"
        } else {
            "routed"
        };
        handlers.push_str(&format!(
            "-- Server-render the REQUESTED route's first paint (design §4.1/§4.2):\n\
             -- run init, resolve the request path to this route's page + model, then\n\
             -- (when init's command is GET-safe) settle its read to a data-bearing\n\
             -- model so a crawler sees REAL per-route content; render head + body\n\
             -- inside a `data-sky-ssr`-marked #app; embed the resolved model as JSON\n\
             -- (design §4.5) so the client can boot from it instead of re-running the\n\
             -- effectful init. `Codec.auto` derives the model codec from the value —\n\
             -- it compiles for ANY model (an unencodable field degrades the blob at\n\
             -- runtime, it never breaks the build).\n\
             ssrHandler : Handler\n\
             ssrHandler {req_param} =\n\
             \x20   let\n\
             \x20       ( model0, {cmd_bind} ) =\n\
             \x20           init ()\n\n\
             \x20       routed =\n\
             \x20           {routed_expr}\n\n\
             \x20       resolved =\n\
             \x20           {resolved_expr}\n\n\
             \x20       modelJson =\n\
             \x20           Codec.toJson (Codec.auto resolved) resolved\n\
             \x20   in\n\
             \x20   Task.succeed\n\
             \x20       (Server.html\n\
             \x20           (spaSsrPage\n\
             \x20               (spaSsrRenderHead spaHead_ resolved)\n\
             \x20               (spaSsrRenderBody (spaView_ resolved))\n\
             \x20               spaWasmName\n\
             \x20               modelJson\n\
             \x20           )\n\
             \x20       )\n\n\n"
        ));
        // Register one SSR GET per LITERAL route pattern plus the exact root
        // `GET /{$}` (Go 1.22). Each literal pattern is a more-specific mux entry
        // than `Server.static "/"`, so app routes reach the SSR handler while
        // asset GETs (main.wasm, wasm_exec.js) fall through to the file server.
        routes.push("        , Server.api \"GET /{$}\" ssrHandler".to_string());
        if has_synth_routes {
            let routes_src = file
                .decls()
                .find(|d| decl_name(d).as_deref() == Some("spaRoutes_"))
                .map(|d| slice(src, d.syntax()).to_string())
                .unwrap_or_default();
            for pat in spa_ssr_route_patterns(&routes_src) {
                routes.push(format!("        , Server.api \"GET {pat}\" ssrHandler"));
            }
        }
    }

    // The static-asset route is always the LAST element of the listen list.
    // Pushing it into `routes` (rather than hard-coding it after the block)
    // keeps the leading-comma→`[` rewrite below uniform: a client-only app has
    // no RPC/push routes, so without this the list would open `[ , Server.static`
    // — a leading comma the parser rejects. With it, `routes` is never empty and
    // the first `        ,` always becomes the opening `        [`.
    routes.push("        , Server.static \"/\" \"../frontend/dist\"".to_string());

    // serverPort + main.
    let route_block = {
        let mut rs = routes.join("\n");
        // routes always has at least the Server.static entry, so this rewrite
        // of the first element's leading comma into `[` always fires.
        rs = rs.replacen("        ,", "        [", 1);
        rs
    };
    // Default port 8951 — MUST match the port the generated desktop/iOS/Android
    // shells load (`sky/src/main.rs`, "default 8951"). The mobile shells bake
    // `http://localhost:8951/` (iOS) / `http://10.0.2.2:8951/` (Android), so a
    // user who starts the backend bare (`./app`, no PORT) and launches the shell
    // must land on the same port. An explicit PORT still overrides both.
    handlers.push_str(
        "serverPort : Int\nserverPort =\n    case String.toInt (System.getenvOr \"PORT\" \"8951\") of\n        Just p ->\n            p\n\n        Nothing ->\n            8951\n\n\n",
    );
    handlers.push_str(&format!(
        "main : Task Error ()\nmain =\n    Server.listen\n        serverPort\n{route_block}\n        ]\n"
    ));

    Ok(format!(
        "module Main exposing (main)\n\n-- Native Sky.Http.Server BACKEND generated by `sky spa-split`. Runs the effectful\n-- branches behind generated RPC endpoints (the app's init + update, reused\n-- verbatim server-side) and serves the wasm client's static assets.\n\n{}\n\n\n{}{}",
        import_lines.join("\n"),
        body,
        handlers
    ))
}

// ---------------------------------------------------------------------------
// Frontend generation — pure branches verbatim, server branches → RPC.
// ---------------------------------------------------------------------------

#[allow(clippy::too_many_arguments)]
fn gen_frontend(
    file: &SourceFile,
    src: &str,
    imports: &[ImportInfo],
    server: &[(String, BranchIo)],
    _client_names: &[String],
    tainted: &[String],
    copied_names: &HashSet<String>,
    msg_param: &str,
    model_param: &str,
    update_anno: &str,
    model_ty: &str,
    backend_only: &HashSet<String>,
) -> Result<String, String> {
    // Imports: drop server-only effect modules AND any backend-only project
    // module (the security spine — an effectful module never reaches the client),
    // add Error + Shared.
    let mut import_lines: Vec<String> = imports
        .iter()
        .filter(|i| !is_server_only_module(&i.module_path) && !backend_only.contains(&i.module_path))
        .map(|i| i.text.clone())
        .collect();
    if !has_module(imports, "Sky.Core.Error") {
        import_lines.push("import Sky.Core.Error as Error exposing (Error)".to_string());
    }
    import_lines.push("import Shared exposing (..)".to_string());

    let server_ctors: Vec<&str> = server.iter().map(|(n, _)| n.as_str()).collect();

    // Client `init` command strip (design §4.4/§4.5, blocker #3). When `init`'s
    // command is a curated GET-safe read (settled server-side + embedded in
    // `#sky-model`) AND that command references a server-tainted binding the
    // frontend drops (a `db` `Task.run` CAF), keeping `init` verbatim leaves an
    // `Undefined name` in the wasm client. Strip the command to `Cmd.none`: the
    // client boots from the embedded model and never runs `cmd0`, so the read
    // stays server-owned and the client tree compiles without `db`. Gated on BOTH
    // conditions so a portable-kernel init (`File.readFile "lit"`, referencing no
    // tainted binding) is left verbatim — no behaviour change for that case.
    let init_src = app_init_src(file, src);
    let strip_init_cmd =
        init_cmd_is_get_safe(&init_src) && tainted.iter().any(|t| references_word(&init_src, t));

    // Client model DECODER (design §4.5, blocker #1/#2). When `init`'s command is
    // stripped, the client boots from the SSR-embedded `#sky-model` blob instead
    // of re-running `init` — so it needs a `String -> Result Error model` decoder
    // symmetric with the backend's `Codec.toJson (Codec.auto model)` embed. Derive
    // it CLIENT-SIDE from `init`'s PURE model expression (which references no `db`,
    // so it is not server-tainted): `Codec.fromJson (Codec.auto <model>) json`.
    // Emitted into the frontend + wired onto the config's `main` here (not in the
    // App→Spa synthesis) so the decoder is never seen by the taint analysis as
    // reaching `db`.
    let decoder_blank = if strip_init_cmd {
        file.decls()
            .find(|d| decl_name(d).as_deref() == Some("init") && is_value_decl(d))
            .and_then(|d| init_pure_model_expr(src, &d))
    } else {
        None
    };
    if decoder_blank.is_some() && !has_module(imports, "Std.Codec") {
        import_lines.push("import Std.Codec as Codec".to_string());
    }

    // Decls: handle by name/kind.
    let mut body = String::new();
    for d in file.decls() {
        let name = decl_name(&d);
        let name = name.as_deref();
        // Skip server-tainted bindings (both annotation + value) — the security spine.
        if let Some(n) = name {
            if tainted.iter().any(|t| t == n) {
                continue;
            }
            // Skip types/codecs copied into Shared — they arrive via `import
            // Shared exposing (..)`; re-declaring them would be a duplicate.
            if copied_names.contains(n) {
                continue;
            }
        }
        match (name, decl_kind(&d)) {
            (Some("main"), _) => {
                // Keep `main = Spa.app …` verbatim — but when a client model
                // decoder is emitted, wire it onto the config builder chain so the
                // driver can boot from `#sky-model` (design §4.5).
                let main_text = slice(src, d.syntax());
                let main_text = if decoder_blank.is_some() {
                    inject_model_decoder_into_main(main_text)
                } else {
                    main_text.to_string()
                };
                body.push_str(main_text.trim_end());
                body.push_str("\n\n\n");
            }
            (Some("update"), _) => {
                // Regenerated below; skip both annotation + value.
            }
            (Some("init"), DeclKind::Value) if strip_init_cmd => {
                // Strip `init`'s command to `Cmd.none` so the client tree compiles
                // without the dropped `db` CAF (design §4.4/§4.5). The `init`
                // annotation is unaffected and is copied verbatim by the catch-all.
                match frontend_init_value_without_cmd(src, &d) {
                    Some(rewritten) => {
                        body.push_str(rewritten.trim_end());
                        body.push_str("\n\n\n");
                    }
                    None => {
                        // Unexpected shape — keep verbatim so a real mismatch
                        // surfaces as a normal compile error, never a silent strip.
                        body.push_str(slice(src, d.syntax()).trim_end());
                        body.push_str("\n\n\n");
                    }
                }
            }
            (Some("Msg"), DeclKind::Union) => {
                // Msg union + generated Applied<Msg> variants.
                body.push_str(slice(src, d.syntax()).trim_end());
                for (m, _) in server {
                    body.push_str(&format!("\n    | Applied{m} (Result Error {m}Resp)"));
                }
                body.push_str("\n\n\n");
            }
            (Some("view"), DeclKind::TypeAnno) => {
                // The wasm client wants `view : Model -> any`.
                body.push_str(&format!("view : {model_ty} -> any\n\n\n"));
            }
            _ => {
                body.push_str(slice(src, d.syntax()).trim_end());
                body.push_str("\n\n\n");
            }
        }
    }

    // The client model decoder (design §4.5) — symmetric with the backend embed.
    // `Codec.auto` derives the codec from the model's TYPE, so a blank with empty
    // collections / a default ADT ctor decodes populated JSON correctly (verified
    // host-side); an unencodable field degrades the decode to Err at runtime (the
    // driver then falls back to `init`), it never breaks the build.
    if let Some(model) = &decoder_blank {
        body.push_str(&format!(
            "spaModelDecoder_ : String -> Result Error {model_ty}\n\
             spaModelDecoder_ jsonStr_ =\n    \
             Codec.fromJson (Codec.auto ({model})) jsonStr_\n\n\n"
        ));
    }

    // The regenerated update.
    let update_src = gen_frontend_update(file, src, server, &server_ctors, msg_param, model_param, update_anno)?;
    body.push_str(&update_src);
    body.push_str("\n");

    Ok(format!(
        "module Main exposing (main)\n\n-- Sky.Spa wasm CLIENT generated by `sky spa-split`. Pure branches run\n-- client-local (zero round-trip); each server branch goes through the explicit\n-- typed RPC boundary (Spa.postJson) using the SHARED codecs. Effectful\n-- (server-tainted) values/functions are NOT present in this source.\n\n{}\n\n\n{}",
        import_lines.join("\n"),
        body
    ))
}

fn gen_frontend_update(
    file: &SourceFile,
    src: &str,
    server: &[(String, BranchIo)],
    server_ctors: &[&str],
    msg_param: &str,
    model_param: &str,
    update_anno: &str,
) -> Result<String, String> {
    // Find update's ValueDecl → its `case msg of`.
    let update_val = file
        .decls()
        .find(|d| decl_name(d).as_deref() == Some("update") && is_value_decl(d))
        .ok_or_else(|| "no `update` value definition found".to_string())?;
    let case_node = update_val
        .syntax()
        .descendants()
        .find(|n| n.kind() == SyntaxKind::CaseExpr)
        .ok_or_else(|| "`update` has no `case … of` to rewrite".to_string())?;
    let case = syntax::ast::CaseExpr::cast(case_node).unwrap();

    let mut arms_out = String::new();
    for arm in case.arms() {
        let pat = arm.pattern().map(|p| p.syntax().clone());
        let head = pat.as_ref().and_then(first_upper);
        let is_server = head
            .as_ref()
            .map(|h| server_ctors.contains(&h.as_str()))
            .unwrap_or(false);
        if is_server {
            let m = head.unwrap();
            let io = &server.iter().find(|(n, _)| *n == m).unwrap().1;
            let pat_text = pat.map(|p| slice(src, &p).to_string()).unwrap_or_else(|| m.clone());
            let req_codec = format!("{}ReqCodec", lower_first(&m));
            let resp_codec = format!("{}RespCodec", lower_first(&m));
            // Request payload.
            let payload = if io.reads_whole_model {
                model_param.to_string()
            } else {
                let mut parts: Vec<String> = io
                    .read_fields
                    .iter()
                    .map(|f| format!("{f} = {model_param}.{f}"))
                    .collect();
                for a in &io.msg_args {
                    parts.push(format!("{a} = {a}"));
                }
                if parts.is_empty() {
                    "{}".to_string()
                } else {
                    format!("{{ {} }}", parts.join(", "))
                }
            };
            arms_out.push_str(&format!(
                "        {pat_text} ->\n            ( {model_param}\n            , Spa.postJson {req_codec} {resp_codec} \"/_rpc/{m}\" {payload} Applied{m}\n            )\n\n"
            ));
        } else {
            // Pure client-local branch — verbatim.
            let text = slice(src, arm.syntax());
            arms_out.push_str("        ");
            arms_out.push_str(text.trim());
            arms_out.push_str("\n\n");
        }
    }
    // Generated Applied<Msg> apply arms.
    for (m, io) in server {
        let apply = if io.writes_whole_model {
            format!("            ( resp, Cmd.none )")
        } else if io.write_fields.is_empty() {
            format!("            ( {model_param}, Cmd.none )")
        } else {
            let sets = io
                .write_fields
                .iter()
                .map(|f| format!("{f} = resp.{f}"))
                .collect::<Vec<_>>()
                .join(", ");
            format!("            ( {{ {model_param} | {sets} }}, Cmd.none )")
        };
        arms_out.push_str(&format!(
            "        Applied{m} (Ok resp) ->\n{apply}\n\n        Applied{m} (Err _) ->\n            ( {model_param}, Cmd.none )\n\n"
        ));
    }

    Ok(format!(
        "{update_anno}\nupdate {msg_param} {model_param} =\n    case {msg_param} of\n{}",
        arms_out.trim_end()
    ))
}

// ---------------------------------------------------------------------------
// CST decl helpers.
// ---------------------------------------------------------------------------

#[derive(PartialEq, Clone, Copy)]
enum DeclKind {
    Value,
    TypeAnno,
    Union,
    Alias,
    Foreign,
}

fn decl_kind(d: &syntax::ast::Decl) -> DeclKind {
    use syntax::ast::Decl;
    match d {
        Decl::Value(_) => DeclKind::Value,
        Decl::TypeAnno(_) => DeclKind::TypeAnno,
        Decl::Union(_) => DeclKind::Union,
        Decl::Alias(_) => DeclKind::Alias,
        Decl::Foreign(_) => DeclKind::Foreign,
    }
}

fn is_value_decl(d: &syntax::ast::Decl) -> bool {
    matches!(d, syntax::ast::Decl::Value(_))
}

fn decl_name(d: &syntax::ast::Decl) -> Option<String> {
    use syntax::ast::Decl;
    match d {
        Decl::Value(v) => v.name().map(|t| t.text().to_string()),
        Decl::TypeAnno(t) => t.name().map(|n| n.text().to_string()),
        Decl::Union(u) => u.name().map(|n| n.text().to_string()),
        Decl::Alias(a) => a.name().map(|n| n.text().to_string()),
        Decl::Foreign(_) => None,
    }
}

fn value_params(d: &syntax::ast::Decl) -> Vec<String> {
    if let syntax::ast::Decl::Value(v) = d {
        if let Some(pl) = v.params() {
            return pl.params().map(|p| p.syntax().text().to_string()).collect();
        }
    }
    Vec::new()
}

fn decl_text_by(file: &SourceFile, src: &str, name: &str, kind: DeclKind) -> Option<String> {
    file.decls()
        .find(|d| decl_name(d).as_deref() == Some(name) && decl_kind(d) == kind)
        .map(|d| slice(src, d.syntax()).trim_end().to_string())
}

/// The model type name = the parameter type of `view`'s annotation
/// (`view : Model -> …` → `Model`). Falls back to the `update` annotation.
fn model_type_name(file: &SourceFile, src: &str) -> Option<String> {
    let anno = decl_text_by(file, src, "view", DeclKind::TypeAnno)
        .or_else(|| decl_text_by(file, src, "update", DeclKind::TypeAnno))?;
    let after_colon = anno.split_once(':')?.1;
    let before_arrow = after_colon.split("->").next()?;
    let name = before_arrow.trim();
    if name.is_empty() {
        None
    } else {
        Some(name.to_string())
    }
}
